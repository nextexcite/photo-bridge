package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/nextexcite/photo-bridge/internal/model"
)

const SchemaVersion = "photo-bridge.state/v1alpha2"

var ErrLocked = errors.New("job is already running")

type Layout struct{ Root string }
type marker struct {
	SchemaVersion string `json:"schemaVersion"`
}
type RunPaths struct {
	Directory       string
	Manifest        string
	TransferLog     string
	VerificationLog string
	Report          string
	SelectionList   string
}
type PruneResult struct {
	DeletedRuns    int   `json:"deletedRuns"`
	ReclaimedBytes int64 `json:"reclaimedBytes"`
	DryRun         bool  `json:"dryRun"`
}

func (l Layout) JobPath(job string, elements ...string) string {
	parts := []string{l.Root, "jobs", job}
	return filepath.Join(append(parts, elements...)...)
}
func (l Layout) MarkerPath() string { return filepath.Join(l.Root, ".photo-bridge-state.json") }
func (l Layout) ReadJSON(filePath string, value any) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func (l Layout) WriteJSON(filePath string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filePath, append(data, '\n'), 0o640)
}

// Ensure creates an explicitly versioned state root. A nonempty unmarked root is
// deliberately rejected rather than interpreted as an earlier release's state.
func (l Layout) Ensure() error {
	if l.Root == "" {
		return errors.New("state directory is required")
	}
	if err := os.MkdirAll(l.Root, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(l.Root)
	if err != nil {
		return err
	}
	markerPath := l.MarkerPath()
	if len(entries) == 0 {
		return l.WriteJSON(markerPath, marker{SchemaVersion: SchemaVersion})
	}
	var current marker
	if err := l.ReadJSON(markerPath, &current); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("nonempty state directory lacks %s marker; remove only this job state before using v1alpha2", SchemaVersion)
		}
		return fmt.Errorf("read state marker: %w", err)
	}
	if current.SchemaVersion != SchemaVersion {
		return fmt.Errorf("state schema %q is not supported; expected %q", current.SchemaVersion, SchemaVersion)
	}
	return nil
}

func (l Layout) Acquire(job string) (*Lock, error) {
	lockDir := l.JobPath(job)
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(lockDir, "job.lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{file: file}, nil
}

func (l Layout) IsLocked(job string) (bool, error) {
	lockDir := l.JobPath(job)
	if err := os.MkdirAll(lockDir, 0o750); err != nil {
		return false, err
	}
	file, err := os.OpenFile(filepath.Join(lockDir, "job.lock"), os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return false, err
	}
	defer file.Close()
	err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		return false, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}

func (l Layout) NewRun(job string, now time.Time) (string, RunPaths, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", RunPaths{}, err
	}
	runID := now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random)
	directory := l.JobPath(job, "runs", runID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", RunPaths{}, err
	}
	return runID, RunPaths{Directory: directory, Manifest: filepath.Join(directory, "manifest.jsonl"), TransferLog: filepath.Join(directory, "transfer.log"), VerificationLog: filepath.Join(directory, "verification.log"), Report: filepath.Join(directory, "report.json"), SelectionList: filepath.Join(directory, "selection.txt")}, nil
}
func (l Layout) Relative(filePath string) string {
	relative, err := filepath.Rel(l.Root, filePath)
	if err != nil {
		return filepath.Base(filePath)
	}
	return filepath.ToSlash(relative)
}
func (l Layout) WriteCurrent(job string, report model.RunReport) error {
	report.UpdatedAt = time.Now().UTC()
	return l.WriteJSON(l.JobPath(job, "current.json"), report)
}
func (l Layout) LoadCurrent(job string) (model.RunReport, error) {
	var report model.RunReport
	err := l.ReadJSON(l.JobPath(job, "current.json"), &report)
	if err != nil {
		return report, err
	}
	return report, validateReport(report)
}
func (l Layout) ClearCurrent(job string) error {
	err := os.Remove(l.JobPath(job, "current.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l Layout) ReconcileCurrent(job string, now time.Time) error {
	report, err := l.LoadCurrent(job)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if report.Status != "running" {
		return l.ClearCurrent(job)
	}
	report.Status, report.Phase, report.Error = "interrupted", "interrupted", "previous process exited before recording a terminal result"
	report.FinishedAt, report.UpdatedAt = now.UTC(), now.UTC()
	paths := RunPaths{Report: l.JobPath(job, "runs", report.RunID, "report.json")}
	return l.WriteReport(job, paths, report)
}

func (l Layout) WriteReport(job string, paths RunPaths, report model.RunReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := atomicWrite(paths.Report, data, 0o640); err != nil {
		return fmt.Errorf("write run report: %w", err)
	}
	if err := atomicWrite(l.JobPath(job, "latest.json"), data, 0o640); err != nil {
		return fmt.Errorf("write latest report: %w", err)
	}
	if err := l.ClearCurrent(job); err != nil {
		return fmt.Errorf("clear active state: %w", err)
	}
	return nil
}
func (l Layout) LoadLatest(job string) (model.RunReport, error) {
	var report model.RunReport
	err := l.ReadJSON(l.JobPath(job, "latest.json"), &report)
	if err != nil {
		return report, err
	}
	return report, validateReport(report)
}

func validateReport(report model.RunReport) error {
	if report.SchemaVersion != model.ReportSchemaVersion {
		return fmt.Errorf("report schema %q is not supported; expected %q", report.SchemaVersion, model.ReportSchemaVersion)
	}
	return nil
}

func (l Layout) Prune(job string, maxAge time.Duration, minRuns int, dryRun bool, now time.Time) (PruneResult, error) {
	if minRuns < 1 {
		return PruneResult{}, errors.New("retention minRuns must be positive")
	}
	runsDir := l.JobPath(job, "runs")
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return PruneResult{DryRun: dryRun}, nil
	}
	if err != nil {
		return PruneResult{}, err
	}
	type candidate struct {
		name     string
		finished time.Time
		bytes    int64
	}
	items := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var report model.RunReport
		if err := l.ReadJSON(filepath.Join(runsDir, entry.Name(), "report.json"), &report); err != nil {
			continue
		}
		if report.FinishedAt.IsZero() {
			continue
		}
		bytes, err := directorySize(filepath.Join(runsDir, entry.Name()))
		if err != nil {
			return PruneResult{}, err
		}
		items = append(items, candidate{name: entry.Name(), finished: report.FinishedAt, bytes: bytes})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].finished.After(items[j].finished) })
	result := PruneResult{DryRun: dryRun}
	cutoff := now.UTC().Add(-maxAge)
	for index, item := range items {
		if index < minRuns || !item.finished.Before(cutoff) {
			continue
		}
		result.DeletedRuns++
		result.ReclaimedBytes += item.bytes
		if !dryRun {
			if err := os.RemoveAll(filepath.Join(runsDir, item.name)); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}
func directorySize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}
func atomicWrite(filePath string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(filePath), ".write-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
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
	return os.Rename(temporaryPath, filePath)
}

type Lock struct{ file *os.File }

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}
