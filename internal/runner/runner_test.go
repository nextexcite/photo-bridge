package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nextexcite/photo-bridge/internal/config"
	"github.com/nextexcite/photo-bridge/internal/process"
)

type fakeExecutor struct {
	calls  [][]string
	failAt int
}

func (f *fakeExecutor) Run(_ context.Context, name string, args []string, stdout, _ io.Writer) process.Result {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	if len(args) >= 2 && args[0] == "backend" && args[1] == "features" {
		_, _ = io.WriteString(stdout, `{"Features":{"Hashes":["sha256"]}}`)
		return process.Result{ExitCode: 0}
	}
	if f.failAt > 0 && len(f.calls) == f.failAt {
		return process.Result{ExitCode: 9, Err: context.Canceled}
	}
	_, _ = io.WriteString(stdout, "completed\n")
	return process.Result{ExitCode: 0}
}

func testJob(source, destination string) config.Job {
	return config.Job{
		Name:      "example-job",
		Operation: "copy",
		Source: config.Endpoint{
			Driver: "filesystem",
			Path:   source,
		},
		Destination: config.Endpoint{
			Driver: "filesystem",
			Path:   destination,
		},
		Policy: config.Policy{
			Integrity:        config.IntegrityPolicy{Manifest: "sha256", Verification: "auto"},
			Transfer:         config.TransferPolicy{Transfers: 4, Checkers: 4, BufferSize: "16MiB", MaxBufferMemory: "256MiB"},
			Limits:           config.LimitsPolicy{MaxDuration: "0s"},
			ProgressInterval: "5s",
			Retention:        config.RetentionPolicy{Mode: "automatic", MaxAge: "720h", MinRuns: 5},
		},
	}
}

func TestRunUsesCopyAndOneWayCheck(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "example.txt"), []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	service := Service{
		StateDir: t.TempDir(),
		Executor: executor,
		Now: func() time.Time {
			return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		},
	}
	report, err := service.Run(context.Background(), testJob(source, destination), RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "succeeded" || !report.NonDestructive {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.Runtime.ConfiguredBufferCeiling != 256<<20 {
		t.Fatalf("unexpected runtime buffer ceiling: %#v", report.Runtime)
	}
	if report.Manifest.TemporaryBytes < 0 {
		t.Fatalf("invalid temporary metadata accounting: %#v", report.Manifest)
	}
	var copyCall, checkCall []string
	for _, call := range executor.calls {
		if len(call) > 1 && call[1] == "copy" {
			copyCall = call
		}
		if len(call) > 1 && call[1] == "check" {
			checkCall = call
		}
	}
	if copyCall == nil || checkCall == nil {
		t.Fatalf("expected copy and check calls, got %#v", executor.calls)
	}
	for _, forbidden := range []string{"sync", "move", "delete", "purge"} {
		if slices.Contains(copyCall, forbidden) {
			t.Fatalf("copy command contains forbidden operation %q", forbidden)
		}
	}
	if !slices.Contains(checkCall, "--one-way") {
		t.Fatalf("verification is not one-way: %#v", checkCall)
	}
}

func TestDryRunSkipsVerification(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "example.txt"), []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}
	executor := &fakeExecutor{}
	report, err := (Service{StateDir: t.TempDir(), Executor: executor}).Run(context.Background(), testJob(source, destination), RunOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	var copyCall []string
	for _, call := range executor.calls {
		if len(call) > 1 && call[1] == "copy" {
			copyCall = call
		}
	}
	if copyCall == nil || !slices.Contains(copyCall, "--dry-run") {
		t.Fatalf("unexpected dry-run calls: %#v", executor.calls)
	}
	if report.Verification.Status != "skipped" {
		t.Fatalf("verification should be skipped: %#v", report.Verification)
	}
}

func TestRunWritesRedactedFailureReport(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "example.txt"), []byte("content"), 0o640); err != nil {
		t.Fatal(err)
	}
	stateDir := t.TempDir()
	executor := &fakeExecutor{failAt: 3}
	report, err := (Service{StateDir: stateDir, Executor: executor}).Run(context.Background(), testJob(source, destination), RunOptions{})
	if err == nil {
		t.Fatal("expected transfer failure")
	}
	if report.Status != "transfer_failed" {
		t.Fatalf("unexpected status: %s", report.Status)
	}
	data, readErr := os.ReadFile(filepath.Join(stateDir, "jobs", "example-job", "latest.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(data), `"status": "transfer_failed"`) {
		t.Fatalf("failure report missing status: %s", data)
	}
}

func TestTakeoutRunPinsOnlyLatestArchiveSet(t *testing.T) {
	source := t.TempDir()
	destination := t.TempDir()
	for _, name := range []string{
		"takeout-20260719T010000Z-001.zip",
		"takeout-20260720T010000Z-001.zip",
		"takeout-20260720T010000Z-002.zip",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	job := testJob(source, destination)
	job.Source.Selector = &config.Selector{Kind: "google-takeout-latest", SettleFor: "2h"}
	executor := &fakeExecutor{}
	stateDir := t.TempDir()
	report, err := (Service{StateDir: stateDir, Executor: executor}).Run(
		context.Background(), job, RunOptions{TakeoutReady: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Selection == nil || report.Selection.Status != "completed" || report.Selection.ArchiveCount != 2 {
		t.Fatalf("unexpected selection report: %#v", report.Selection)
	}
	for _, call := range executor.calls {
		if len(call) < 2 || (call[1] != "copy" && call[1] != "check") {
			continue
		}
		if !slices.Contains(call, "--files-from-raw") {
			t.Fatalf("selected operation lacks --files-from-raw: %#v", call)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, "jobs", job.Name, "takeout", "active.json")); !os.IsNotExist(err) {
		t.Fatalf("active selection was not cleared: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "jobs", job.Name, "takeout", "completed.json")); err != nil {
		t.Fatalf("completed selection missing: %v", err)
	}
}
