package state

import (
	"errors"
	"testing"
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
