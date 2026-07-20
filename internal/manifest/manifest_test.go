package manifest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nextexcite/photo-bridge/internal/config"
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
