package takeout

import (
	"errors"
	"testing"
	"time"

	"github.com/nextexcite/photo-bridge/internal/model"
)

func entry(name string, size int64, when time.Time) model.ManifestEntry {
	return model.ManifestEntry{Path: name, Size: size, ModTime: when}
}

func TestInspectSelectsLatestCompleteMultipartSet(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{
		entry("takeout-20260719T010000Z-001.zip", 1, now),
		entry("takeout-20260720T010000Z-002.zip", 20, now),
		entry("takeout-20260720T010000Z-001.zip", 10, now),
	}
	candidate, err := Inspect(entries, Observation{}, now, 2*time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	if !candidate.Ready || len(candidate.Paths) != 2 || candidate.TotalBytes != 30 {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
}

func TestInspectRequiresStableObservation(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	entries := []model.ManifestEntry{entry("takeout-20260720T010000Z.zip", 10, now)}
	first, err := Inspect(entries, Observation{}, now, 2*time.Hour, false)
	if err != nil || first.Ready {
		t.Fatalf("unexpected first observation: %#v, %v", first, err)
	}
	second, err := Inspect(entries, first.Observation, now.Add(3*time.Hour), 2*time.Hour, false)
	if err != nil || !second.Ready {
		t.Fatalf("unexpected stable observation: %#v, %v", second, err)
	}
}

func TestInspectRejectsGap(t *testing.T) {
	now := time.Now().UTC()
	_, err := Inspect([]model.ManifestEntry{
		entry("takeout-20260720T010000Z-001.zip", 1, now),
		entry("takeout-20260720T010000Z-003.zip", 1, now),
	}, Observation{}, now, time.Hour, true)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("expected ambiguity, got %v", err)
	}
}
