package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nextexcite/photo-bridge/internal/model"
)

func TestLockRejectsConcurrentHolder(t *testing.T) {
	layout := Layout{Root: t.TempDir()}
	first, err := layout.Acquire("example-job")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := layout.Acquire("example-job")
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
}

func TestEnsureRejectsNonemptyLegacyState(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "legacy.json"), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := (Layout{Root: root}).Ensure(); err == nil {
		t.Fatal("expected legacy state rejection")
	}
}

func TestLoadLatestRejectsLegacyReport(t *testing.T) {
	root := t.TempDir()
	layout := Layout{Root: root}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(model.RunReport{SchemaVersion: "photo-bridge.report/v1alpha1"})
	if err != nil {
		t.Fatal(err)
	}
	path := layout.JobPath("example-job", "latest.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := layout.LoadLatest("example-job"); err == nil {
		t.Fatal("expected legacy report rejection")
	}
}
