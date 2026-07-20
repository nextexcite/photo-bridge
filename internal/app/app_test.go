package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigValidate(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	code := (Application{Out: &out, Err: &stderr}).Run(context.Background(), []string{"config", "validate", "--config", configPath})
	if code != ExitOK {
		t.Fatalf("unexpected exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "configuration valid") {
		t.Fatalf("unexpected output: %q", out.String())
	}
}

func TestPlanDoesNotExposeEndpointPaths(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0o640); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	code := (Application{Out: &out, Err: &stderr}).Run(context.Background(), []string{"plan", "--config", configPath, "--job", "example-archive"})
	if code != ExitOK {
		t.Fatalf("unexpected exit %d: %s", code, stderr.String())
	}
	if strings.Contains(out.String(), "/data/input") || strings.Contains(out.String(), "archive-remote") {
		t.Fatalf("plan exposed endpoint identity: %s", out.String())
	}
	if !strings.Contains(out.String(), `"nonDestructive": true`) {
		t.Fatalf("plan omitted safety invariant: %s", out.String())
	}
}

func TestUnknownCommandIsConfigurationError(t *testing.T) {
	var stderr bytes.Buffer
	code := (Application{Out: ioDiscard{}, Err: &stderr}).Run(context.Background(), []string{"unknown"})
	if code != ExitConfig {
		t.Fatalf("expected exit %d, got %d", ExitConfig, code)
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
