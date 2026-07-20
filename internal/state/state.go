package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/nextexcite/photo-bridge/internal/model"
)

var ErrLocked = errors.New("job is already running")

type Layout struct {
	Root string
}

type RunPaths struct {
	Directory       string
	Manifest        string
	TransferLog     string
	VerificationLog string
	Report          string
	SelectionList   string
}

func (l Layout) JobPath(job string, elements ...string) string {
	parts := []string{l.Root, "jobs", job}
	parts = append(parts, elements...)
	return filepath.Join(parts...)
}

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

func (l Layout) Ensure() error {
	if l.Root == "" {
		return errors.New("state directory is required")
	}
	return os.MkdirAll(l.Root, 0o750)
}

func (l Layout) Acquire(job string) (*Lock, error) {
	lockDir := filepath.Join(l.Root, "jobs", job)
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

func (l Layout) NewRun(job string, now time.Time) (string, RunPaths, error) {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return "", RunPaths{}, err
	}
	runID := now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random)
	directory := filepath.Join(l.Root, "jobs", job, "runs", runID)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", RunPaths{}, err
	}
	return runID, RunPaths{
		Directory:       directory,
		Manifest:        filepath.Join(directory, "manifest.jsonl"),
		TransferLog:     filepath.Join(directory, "transfer.log"),
		VerificationLog: filepath.Join(directory, "verification.log"),
		Report:          filepath.Join(directory, "report.json"),
		SelectionList:   filepath.Join(directory, "selection.txt"),
	}, nil
}

func (l Layout) Relative(filePath string) string {
	relative, err := filepath.Rel(l.Root, filePath)
	if err != nil {
		return filepath.Base(filePath)
	}
	return filepath.ToSlash(relative)
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
	latest := filepath.Join(l.Root, "jobs", job, "latest.json")
	if err := atomicWrite(latest, data, 0o640); err != nil {
		return fmt.Errorf("write latest report: %w", err)
	}
	return nil
}

func (l Layout) LoadLatest(job string) (model.RunReport, error) {
	data, err := os.ReadFile(filepath.Join(l.Root, "jobs", job, "latest.json"))
	if err != nil {
		return model.RunReport{}, err
	}
	var report model.RunReport
	if err := json.Unmarshal(data, &report); err != nil {
		return model.RunReport{}, err
	}
	return report, nil
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

type Lock struct {
	file *os.File
}

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
