package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextexcite/photo-bridge/internal/config"
	"github.com/nextexcite/photo-bridge/internal/model"
	"github.com/nextexcite/photo-bridge/internal/process"
)

func TestFilesystemManifestIsDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "z.txt"), []byte("z"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "a.txt"), []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}

	result, err := (Builder{}).Build(context.Background(), config.Endpoint{Driver: "filesystem", Path: root}, "sha256")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(result.Entries))
	}
	if result.Entries[0].Path != "nested/a.txt" || result.Entries[1].Path != "z.txt" {
		t.Fatalf("entries are not sorted: %#v", result.Entries)
	}
	if result.Level != "sha256" || result.Entries[0].Hash == "" {
		t.Fatalf("expected sha256 manifest, got %#v", result)
	}
}

func TestFilesystemManifestRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := (Builder{}).Build(context.Background(), config.Endpoint{Driver: "filesystem", Path: root}, "metadata")
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestChooseHashIsStable(t *testing.T) {
	algorithm, value := chooseHash(map[string]string{"MD5": "b", "SHA-1": "a"}, "auto")
	if algorithm != "md5" || value != "b" {
		t.Fatalf("unexpected selected hash: %s %s", algorithm, value)
	}
	algorithm, _ = chooseHash(map[string]string{"SHA-256": "c", "MD5": "b"}, "auto")
	if algorithm != "sha256" {
		t.Fatalf("expected sha256, got %s", algorithm)
	}
}

func TestBuildToJSONLStreamsAndSpillsDeterministically(t *testing.T) {
	if testing.Short() {
		t.Skip("large deterministic streaming fixture")
	}
	const entries = 250_000
	work := t.TempDir()
	first := filepath.Join(work, "first.jsonl")
	second := filepath.Join(work, "second.jsonl")

	build := func(offset int, destination string) Summary {
		t.Helper()
		result, err := (Builder{
			Executor:            syntheticListingExecutor(entries, offset),
			TempDir:             filepath.Join(work, "spool"),
			MetadataMemoryLimit: 512 << 10,
		}).BuildToJSONL(context.Background(), config.Endpoint{Driver: "rclone", Remote: "example-remote", Path: "library"}, "metadata", nil, destination)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	firstSummary := build(0, first)
	secondSummary := build(1, second)
	if firstSummary.Entries != entries || firstSummary.Bytes != int64(entries*(entries-1)/2) {
		t.Fatalf("unexpected summary: %#v", firstSummary)
	}
	if firstSummary.Fingerprint == "" || firstSummary.Fingerprint != secondSummary.Fingerprint {
		t.Fatalf("non-deterministic fingerprints: %q and %q", firstSummary.Fingerprint, secondSummary.Fingerprint)
	}
	if firstSummary.TemporaryBytes <= 512<<10 {
		t.Fatalf("expected spilled metadata beyond threshold, got %d bytes", firstSummary.TemporaryBytes)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("deterministic manifests differed for shuffled listing input")
	}
	loaded, err := ReadJSONL(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != entries || loaded[0].Path != "library/000000.jpg" || loaded[len(loaded)-1].Path != "library/249999.jpg" {
		t.Fatalf("manifest ordering is invalid: first=%#v last=%#v", loaded[0], loaded[len(loaded)-1])
	}
	streamed, err := WalkJSONL(context.Background(), first, func(model.ManifestEntry) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if streamed.Entries != firstSummary.Entries || streamed.Bytes != firstSummary.Bytes || streamed.Fingerprint != firstSummary.Fingerprint {
		t.Fatalf("streamed summary does not match build: %#v != %#v", streamed, firstSummary)
	}
	spoolEntries, err := os.ReadDir(filepath.Join(work, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if len(spoolEntries) != 0 {
		t.Fatalf("temporary sort directories were retained: %#v", spoolEntries)
	}
}

type listingExecutor struct {
	entries int
	offset  int
}

func syntheticListingExecutor(entries, offset int) process.Executor {
	return listingExecutor{entries: entries, offset: offset}
}

func (e listingExecutor) Run(_ context.Context, _ string, _ []string, stdout, _ io.Writer) process.Result {
	if _, err := io.WriteString(stdout, "["); err != nil {
		return process.Result{ExitCode: 1, Err: err}
	}
	encoder := json.NewEncoder(stdout)
	for index := 0; index < e.entries; index++ {
		if index > 0 {
			if _, err := io.WriteString(stdout, ","); err != nil {
				return process.Result{ExitCode: 1, Err: err}
			}
		}
		shuffled := (index*7919 + e.offset) % e.entries
		entry := rcloneEntry{
			Path:    fmt.Sprintf("library/%06d.jpg", shuffled),
			Size:    int64(shuffled),
			ModTime: time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC),
		}
		if err := encoder.Encode(entry); err != nil {
			return process.Result{ExitCode: 1, Err: err}
		}
	}
	if _, err := io.WriteString(stdout, "]"); err != nil {
		return process.Result{ExitCode: 1, Err: err}
	}
	return process.Result{}
}
