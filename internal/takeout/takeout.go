package takeout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nextexcite/photo-bridge/internal/model"
)

var archiveName = regexp.MustCompile(`(?i)^(takeout-(\d{8}T\d{6}Z))(?:-(\d+))?(?:-(\d+))?\.zip$`)

var (
	ErrNoArchive = errors.New("no Google Takeout ZIP archive was found")
	ErrAmbiguous = errors.New("latest Google Takeout export is ambiguous")
)

type Observation struct {
	Fingerprint    string    `json:"fingerprint"`
	UnchangedSince time.Time `json:"unchangedSince"`
	ObservedAt     time.Time `json:"observedAt"`
}

type Selection struct {
	Fingerprint string    `json:"fingerprint"`
	Paths       []string  `json:"paths"`
	TotalBytes  int64     `json:"totalBytes"`
	SelectedAt  time.Time `json:"selectedAt"`
}

type Candidate struct {
	Selection
	Observation Observation
	ReadyAfter  time.Time
	Ready       bool
}

type parsed struct {
	entry      model.ManifestEntry
	group      string
	part       int
	parted     bool
	logicalKey string
}

func Inspect(entries []model.ManifestEntry, previous Observation, now time.Time, settleFor time.Duration, manualReady bool) (Candidate, error) {
	groups := make(map[string][]parsed)
	for _, entry := range entries {
		base := path.Base(entry.Path)
		match := archiveName.FindStringSubmatch(base)
		if match == nil {
			continue
		}
		group := strings.ToLower(match[1])
		partText := match[3]
		if match[4] != "" {
			group += "-" + match[3]
			partText = match[4]
		}
		part := 0
		parted := partText != ""
		if parted {
			part, _ = strconv.Atoi(partText)
		}
		groups[group] = append(groups[group], parsed{entry: entry, group: group, part: part, parted: parted, logicalKey: strings.ToLower(base)})
	}
	if len(groups) == 0 {
		return Candidate{}, ErrNoArchive
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	latest := groups[keys[len(keys)-1]]

	seenNames := make(map[string]struct{}, len(latest))
	seenParts := make(map[int]struct{}, len(latest))
	parted := false
	for _, item := range latest {
		if _, exists := seenNames[item.logicalKey]; exists {
			return Candidate{}, fmt.Errorf("%w: duplicate archive name", ErrAmbiguous)
		}
		seenNames[item.logicalKey] = struct{}{}
		if item.parted {
			parted = true
			if item.part < 1 {
				return Candidate{}, fmt.Errorf("%w: invalid part number", ErrAmbiguous)
			}
			if _, exists := seenParts[item.part]; exists {
				return Candidate{}, fmt.Errorf("%w: duplicate part number", ErrAmbiguous)
			}
			seenParts[item.part] = struct{}{}
		}
	}
	if parted {
		for partNumber := 1; partNumber <= len(latest); partNumber++ {
			if _, exists := seenParts[partNumber]; !exists {
				return Candidate{}, fmt.Errorf("%w: archive part sequence has a gap", ErrAmbiguous)
			}
		}
	} else if len(latest) != 1 {
		return Candidate{}, fmt.Errorf("%w: multiple unsuffixed archives", ErrAmbiguous)
	}

	sort.Slice(latest, func(i, j int) bool { return latest[i].entry.Path < latest[j].entry.Path })
	selection := Selection{SelectedAt: now.UTC()}
	hasher := sha256.New()
	encoder := json.NewEncoder(hasher)
	for _, item := range latest {
		selection.Paths = append(selection.Paths, item.entry.Path)
		selection.TotalBytes += item.entry.Size
		_ = encoder.Encode(struct {
			Path    string
			Size    int64
			ModTime time.Time
		}{item.entry.Path, item.entry.Size, item.entry.ModTime.UTC()})
	}
	selection.Fingerprint = hex.EncodeToString(hasher.Sum(nil))

	observation := Observation{Fingerprint: selection.Fingerprint, ObservedAt: now.UTC(), UnchangedSince: now.UTC()}
	if previous.Fingerprint == selection.Fingerprint && !previous.UnchangedSince.IsZero() {
		observation.UnchangedSince = previous.UnchangedSince.UTC()
	}
	readyAfter := observation.UnchangedSince.Add(settleFor)
	return Candidate{
		Selection:   selection,
		Observation: observation,
		ReadyAfter:  readyAfter,
		Ready:       manualReady || !now.Before(readyAfter),
	}, nil
}
