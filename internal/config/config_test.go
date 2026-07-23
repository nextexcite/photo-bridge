package config

import (
	"strings"
	"testing"
)

const validConfig = `apiVersion: photo-bridge/v1alpha2
jobs:
  - name: example-archive
    operation: copy
    source:
      driver: filesystem
      path: /data/input
    destination:
      driver: rclone
      remote: archive-remote
      path: /photos/account-a
    policy: {}
`

func TestDecodeNormalizesDefaults(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	job := cfg.Jobs[0]
	if job.Policy.Integrity.Manifest != "auto" || job.Policy.Integrity.Verification != "auto" {
		t.Fatalf("unexpected policy defaults: %#v", job.Policy)
	}
	if job.Policy.Transfer.Transfers != 8 || job.Policy.Transfer.Checkers != 8 {
		t.Fatalf("unexpected numeric defaults: %#v", job.Policy)
	}
	if got := job.Destination.RcloneSpec(); got != "archive-remote:photos/account-a" {
		t.Fatalf("unexpected rclone spec: %q", got)
	}
}

func TestDecodeRejectsV1Alpha1AndInvalidByteValues(t *testing.T) {
	if _, err := Decode(strings.NewReader(strings.Replace(validConfig, "v1alpha2", "v1alpha1", 1))); err == nil {
		t.Fatal("expected v1alpha1 rejection")
	}
	input := strings.Replace(validConfig, "policy: {}", "policy:\n      transfer:\n        bufferSize: 16.5MiB", 1)
	if _, err := Decode(strings.NewReader(input)); err == nil {
		t.Fatal("expected fractional buffer-size rejection")
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	_, err := Decode(strings.NewReader(validConfig + "unknown: true\n"))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestDecodeRejectsDestructiveOperation(t *testing.T) {
	input := strings.Replace(validConfig, "operation: copy", "operation: sync", 1)
	_, err := Decode(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "supports only copy") {
		t.Fatalf("expected copy-only error, got %v", err)
	}
}

func TestDecodeRejectsRelativeFilesystemPath(t *testing.T) {
	input := strings.Replace(validConfig, "/data/input", "data/input", 1)
	_, err := Decode(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestFindJob(t *testing.T) {
	cfg, err := Decode(strings.NewReader(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.FindJob("missing"); err == nil {
		t.Fatal("expected missing job error")
	}
}

func TestDecodeTakeoutSelectorDefaultsSettleWindow(t *testing.T) {
	input := strings.Replace(validConfig, "path: /data/input", "path: /data/input\n      selector:\n        kind: google-takeout-latest", 1)
	cfg, err := Decode(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Jobs[0].Source.Selector.SettleFor; got != "2h" {
		t.Fatalf("unexpected settle window: %q", got)
	}
}

func TestDecodeRejectsDestinationSelector(t *testing.T) {
	input := strings.Replace(validConfig, "path: /photos/account-a", "path: /photos/account-a\n      selector:\n        kind: google-takeout-latest", 1)
	_, err := Decode(strings.NewReader(input))
	if err == nil || !strings.Contains(err.Error(), "destination must not set selector") {
		t.Fatalf("expected destination selector error, got %v", err)
	}
}
