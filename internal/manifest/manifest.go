package manifest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nextexcite/photo-bridge/internal/config"
	"github.com/nextexcite/photo-bridge/internal/model"
	"github.com/nextexcite/photo-bridge/internal/process"
)

type Builder struct {
	Executor   process.Executor
	RcloneBin  string
	RcloneConf string
}

type Result struct {
	Entries []model.ManifestEntry
	Level   string
}

func (b Builder) Build(ctx context.Context, endpoint config.Endpoint, requested string) (Result, error) {
	return b.BuildFiltered(ctx, endpoint, requested, nil)
}

func (b Builder) BuildFiltered(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}) (Result, error) {
	switch endpoint.Driver {
	case "filesystem":
		return buildFilesystem(ctx, endpoint.Path, requested, allowed)
	case "rclone":
		return b.buildRclone(ctx, endpoint, requested, allowed)
	default:
		return Result{}, fmt.Errorf("manifest driver %q is not supported", endpoint.Driver)
	}
}

func buildFilesystem(ctx context.Context, root, requested string, allowed map[string]struct{}) (Result, error) {
	info, err := os.Stat(root)
	if err != nil {
		return Result{}, fmt.Errorf("inspect source root: %w", err)
	}
	if !info.IsDir() {
		return Result{}, errors.New("source filesystem path must be a directory")
	}

	entries := make([]model.ManifestEntry, 0)
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if filePath == root || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source contains unsupported symbolic link %q", relativePath(root, filePath))
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("source contains unsupported non-regular file %q", relativePath(root, filePath))
		}

		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		relative := relativePath(root, filePath)
		if allowed != nil {
			if _, ok := allowed[relative]; !ok {
				return nil
			}
		}
		manifestEntry := model.ManifestEntry{
			Path:    relative,
			Size:    entryInfo.Size(),
			ModTime: entryInfo.ModTime().UTC(),
		}
		if requested == "sha256" || requested == "auto" {
			hash, err := hashFile(ctx, filePath)
			if err != nil {
				return fmt.Errorf("hash %q: %w", manifestEntry.Path, err)
			}
			manifestEntry.HashAlgorithm = "sha256"
			manifestEntry.Hash = hash
		}
		entries = append(entries, manifestEntry)
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	level := "metadata"
	if requested == "sha256" || requested == "auto" {
		level = "sha256"
	}
	return Result{Entries: entries, Level: level}, nil
}

type rcloneEntry struct {
	Path    string            `json:"Path"`
	Size    int64             `json:"Size"`
	ModTime time.Time         `json:"ModTime"`
	Hashes  map[string]string `json:"Hashes"`
	IsDir   bool              `json:"IsDir"`
}

func (b Builder) buildRclone(ctx context.Context, endpoint config.Endpoint, requested string, allowed map[string]struct{}) (Result, error) {
	if b.Executor == nil {
		return Result{}, errors.New("rclone manifest executor is not configured")
	}
	if b.RcloneBin == "" {
		b.RcloneBin = "rclone"
	}
	args := []string{"lsjson", endpoint.RcloneSpec(), "--recursive", "--files-only", "--hash"}
	if b.RcloneConf != "" {
		args = append(args, "--config", b.RcloneConf)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	result := b.Executor.Run(ctx, b.RcloneBin, args, &stdout, &stderr)
	if result.Err != nil {
		return Result{}, fmt.Errorf("list rclone source: exit %d: %s", result.ExitCode, strings.TrimSpace(stderr.String()))
	}

	var remoteEntries []rcloneEntry
	if err := json.Unmarshal(stdout.Bytes(), &remoteEntries); err != nil {
		return Result{}, fmt.Errorf("decode rclone source listing: %w", err)
	}

	entries := make([]model.ManifestEntry, 0, len(remoteEntries))
	level := "metadata"
	for _, remoteEntry := range remoteEntries {
		if remoteEntry.IsDir {
			continue
		}
		entryPath := filepath.ToSlash(remoteEntry.Path)
		if allowed != nil {
			if _, ok := allowed[entryPath]; !ok {
				continue
			}
		}
		entry := model.ManifestEntry{
			Path:    entryPath,
			Size:    remoteEntry.Size,
			ModTime: remoteEntry.ModTime.UTC(),
		}
		algorithm, hash := chooseHash(remoteEntry.Hashes, requested)
		if requested == "sha256" && algorithm != "sha256" {
			return Result{}, fmt.Errorf("rclone source does not expose sha256 for %q; use manifest auto or metadata", entry.Path)
		}
		if hash != "" {
			entry.HashAlgorithm = algorithm
			entry.Hash = hash
			if algorithm == "sha256" {
				level = "sha256"
			} else if level != "sha256" {
				level = "provider-hash"
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return Result{Entries: entries, Level: level}, nil
}

func chooseHash(hashes map[string]string, requested string) (string, string) {
	if requested == "metadata" || len(hashes) == 0 {
		return "", ""
	}
	for key, value := range hashes {
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", ""))
		if normalized == "sha256" && value != "" {
			return "sha256", value
		}
	}
	if requested == "sha256" {
		return "", ""
	}
	keys := make([]string, 0, len(hashes))
	for key, value := range hashes {
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", ""
	}
	key := keys[0]
	return strings.ToLower(key), hashes[key]
}

func WriteJSONL(filePath string, entries []model.ManifestEntry) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return err
	}
	writer := bufio.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			_ = temporary.Close()
			return fmt.Errorf("encode manifest entry: %w", err)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	return nil
}

func hashFile(ctx context.Context, filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	hash := sha256.New()
	buffer := make([]byte, 1024*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buffer)
		if n > 0 {
			if _, err := hash.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func relativePath(root, filePath string) string {
	relative, err := filepath.Rel(root, filePath)
	if err != nil {
		return filepath.ToSlash(filePath)
	}
	return filepath.ToSlash(relative)
}
