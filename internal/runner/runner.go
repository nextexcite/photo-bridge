package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nextexcite/photo-bridge/internal/config"
	"github.com/nextexcite/photo-bridge/internal/manifest"
	"github.com/nextexcite/photo-bridge/internal/model"
	"github.com/nextexcite/photo-bridge/internal/process"
	"github.com/nextexcite/photo-bridge/internal/redact"
	"github.com/nextexcite/photo-bridge/internal/state"
	"github.com/nextexcite/photo-bridge/internal/takeout"
)

var (
	ErrTransferFailed     = errors.New("transfer failed")
	ErrVerificationFailed = errors.New("verification failed")
	ErrWaiting            = errors.New("waiting for a ready export")
	ErrSelectionFailed    = errors.New("source selection failed")
)

type RunOptions struct {
	DryRun       bool
	TakeoutReady bool
}

type Service struct {
	StateDir   string
	RcloneBin  string
	RcloneConf string
	Executor   process.Executor
	Out        io.Writer
	Now        func() time.Time
}

func (s Service) Plan(job config.Job) model.Plan {
	plan := model.Plan{
		Job:               job.Name,
		Operation:         job.Operation,
		SourceDriver:      job.Source.Driver,
		DestinationDriver: job.Destination.Driver,
		Manifest:          job.Policy.Manifest,
		Verification:      job.Policy.Verification,
		Transfers:         job.Policy.Transfers,
		Retries:           job.Policy.Retries,
		NonDestructive:    true,
		Executor:          "rclone-copy",
	}
	if job.Source.Selector != nil {
		plan.Selector = job.Source.Selector.Kind
	}
	return plan
}

func (s Service) Run(ctx context.Context, job config.Job, options RunOptions) (model.RunReport, error) {
	s = s.withDefaults()
	if err := s.validateRuntime(job); err != nil {
		return model.RunReport{}, err
	}

	layout := state.Layout{Root: s.StateDir}
	if err := layout.Ensure(); err != nil {
		return model.RunReport{}, fmt.Errorf("prepare state: %w", err)
	}
	lock, err := layout.Acquire(job.Name)
	if err != nil {
		return model.RunReport{}, err
	}
	defer lock.Close()

	started := s.Now().UTC()
	runID, paths, err := layout.NewRun(job.Name, started)
	if err != nil {
		return model.RunReport{}, fmt.Errorf("create run: %w", err)
	}
	report := newReport(job, runID, started, options.DryRun)
	report.Manifest.Requested = job.Policy.Manifest
	report.Verification.Requested = job.Policy.Verification

	builder := manifest.Builder{
		Executor:   s.Executor,
		RcloneBin:  s.RcloneBin,
		RcloneConf: s.RcloneConf,
	}
	var selected map[string]struct{}
	if job.Source.Selector != nil {
		selection, selectionReport, selectionErr := s.selectTakeout(ctx, layout, job, builder, options.TakeoutReady)
		report.Selection = &selectionReport
		if selectionErr != nil {
			status := "selection_failed"
			if errors.Is(selectionErr, ErrWaiting) {
				status = "waiting"
			}
			return s.finishFailure(layout, paths, report, status, selectionErr)
		}
		selected = make(map[string]struct{}, len(selection.Paths))
		for _, selectedPath := range selection.Paths {
			selected[selectedPath] = struct{}{}
		}
		if err := writeSelectionList(paths.SelectionList, selection.Paths); err != nil {
			return s.finishFailure(layout, paths, report, "selection_failed", fmt.Errorf("%w: %v", ErrSelectionFailed, err))
		}
	}

	manifestResult, err := builder.BuildFiltered(ctx, job.Source, job.Policy.Manifest, selected)
	if err != nil {
		return s.finishFailure(layout, paths, report, "manifest_failed", err)
	}
	if selected != nil && len(manifestResult.Entries) != len(selected) {
		return s.finishFailure(layout, paths, report, "manifest_failed", fmt.Errorf("pinned source set changed: expected %d archives, found %d", len(selected), len(manifestResult.Entries)))
	}
	if err := manifest.WriteJSONL(paths.Manifest, manifestResult.Entries); err != nil {
		return s.finishFailure(layout, paths, report, "manifest_failed", err)
	}
	report.Manifest.Level = manifestResult.Level
	report.Manifest.Entries = len(manifestResult.Entries)
	report.Manifest.Path = layout.Relative(paths.Manifest)

	transferResult, err := s.runCopy(ctx, job, options.DryRun, paths.TransferLog, paths.SelectionList, selected != nil, layout)
	report.Transfer = transferResult
	if err != nil {
		return s.finishFailure(layout, paths, report, "transfer_failed", fmt.Errorf("%w: %v", ErrTransferFailed, err))
	}
	if options.DryRun {
		report.Status = "dry_run"
		report.Verification = model.VerificationResult{
			Requested: job.Policy.Verification,
			Method:    verificationMethod(job.Policy.Verification),
			Status:    "skipped",
			ExitCode:  0,
		}
		return s.finish(layout, paths, report, nil)
	}

	verificationResult, err := s.runVerification(ctx, job, paths.VerificationLog, paths.SelectionList, selected != nil, layout)
	report.Verification = verificationResult
	if err != nil {
		return s.finishFailure(layout, paths, report, "verification_failed", fmt.Errorf("%w: %v", ErrVerificationFailed, err))
	}
	report.Status = "succeeded"
	if job.Source.Selector != nil {
		if err := s.completeTakeout(layout, job.Name); err != nil {
			return s.finishFailure(layout, paths, report, "selection_failed", fmt.Errorf("%w: %v", ErrSelectionFailed, err))
		}
		report.Selection.Status = "completed"
	}
	return s.finish(layout, paths, report, nil)
}

func (s Service) Verify(ctx context.Context, job config.Job) (model.RunReport, error) {
	s = s.withDefaults()
	if err := s.validateRuntime(job); err != nil {
		return model.RunReport{}, err
	}
	layout := state.Layout{Root: s.StateDir}
	if err := layout.Ensure(); err != nil {
		return model.RunReport{}, fmt.Errorf("prepare state: %w", err)
	}
	lock, err := layout.Acquire(job.Name)
	if err != nil {
		return model.RunReport{}, err
	}
	defer lock.Close()

	started := s.Now().UTC()
	runID, paths, err := layout.NewRun(job.Name, started)
	if err != nil {
		return model.RunReport{}, err
	}
	report := newReport(job, runID, started, false)
	report.Operation = "verify"
	report.Manifest = model.ManifestResult{Requested: job.Policy.Manifest, Level: "skipped"}
	report.Transfer = model.CommandResult{Status: "skipped", ExitCode: 0}

	selected := false
	if job.Source.Selector != nil {
		var completed takeout.Selection
		if err := layout.ReadJSON(layout.JobPath(job.Name, "takeout", "completed.json"), &completed); err != nil {
			return s.finishFailure(layout, paths, report, "selection_failed", fmt.Errorf("%w: no completed Takeout selection: %v", ErrSelectionFailed, err))
		}
		if err := writeSelectionList(paths.SelectionList, completed.Paths); err != nil {
			return s.finishFailure(layout, paths, report, "selection_failed", fmt.Errorf("%w: %v", ErrSelectionFailed, err))
		}
		completedResult := selectionResult(completed, "completed", time.Time{}, time.Time{}, false)
		report.Selection = &completedResult
		selected = true
	}
	verificationResult, err := s.runVerification(ctx, job, paths.VerificationLog, paths.SelectionList, selected, layout)
	report.Verification = verificationResult
	if err != nil {
		return s.finishFailure(layout, paths, report, "verification_failed", fmt.Errorf("%w: %v", ErrVerificationFailed, err))
	}
	report.Status = "succeeded"
	return s.finish(layout, paths, report, nil)
}

func (s Service) Status(job string) (model.RunReport, error) {
	s = s.withDefaults()
	return (state.Layout{Root: s.StateDir}).LoadLatest(job)
}

func (s Service) runCopy(ctx context.Context, job config.Job, dryRun bool, logPath, selectionList string, selected bool, layout state.Layout) (model.CommandResult, error) {
	args := []string{
		"copy",
		job.Source.RcloneSpec(),
		job.Destination.RcloneSpec(),
		"--transfers", strconv.Itoa(job.Policy.Transfers),
		"--retries", strconv.Itoa(job.Policy.Retries),
		"--low-level-retries", "10",
		"--use-json-log",
		"--log-level", "INFO",
		"--stats", "30s",
	}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if selected {
		args = append(args, "--files-from-raw", selectionList)
	}
	args = s.withRcloneConfig(args)
	emitPhase(s.Out, "transfer", "started")
	result, err := s.runLogged(ctx, args, logPath, "transfer")
	commandResult := model.CommandResult{
		Status:   "succeeded",
		ExitCode: result.ExitCode,
		LogPath:  layout.Relative(logPath),
	}
	if err != nil {
		commandResult.Status = "failed"
	}
	return commandResult, err
}

func (s Service) runVerification(ctx context.Context, job config.Job, logPath, selectionList string, selected bool, layout state.Layout) (model.VerificationResult, error) {
	args := []string{
		"check",
		job.Source.RcloneSpec(),
		job.Destination.RcloneSpec(),
		"--one-way",
		"--use-json-log",
		"--log-level", "INFO",
	}
	switch job.Policy.Verification {
	case "checksum":
		args = append(args, "--checksum")
	case "size":
		args = append(args, "--size-only")
	}
	if selected {
		args = append(args, "--files-from-raw", selectionList)
	}
	args = s.withRcloneConfig(args)
	emitPhase(s.Out, "verification", "started")
	result, err := s.runLogged(ctx, args, logPath, "verification")
	verificationResult := model.VerificationResult{
		Requested: job.Policy.Verification,
		Method:    verificationMethod(job.Policy.Verification),
		Status:    "succeeded",
		ExitCode:  result.ExitCode,
		LogPath:   layout.Relative(logPath),
	}
	if err != nil {
		verificationResult.Status = "failed"
	}
	return verificationResult, err
}

func (s Service) selectTakeout(ctx context.Context, layout state.Layout, job config.Job, builder manifest.Builder, manualReady bool) (takeout.Selection, model.SelectionResult, error) {
	takeoutDir := layout.JobPath(job.Name, "takeout")
	activePath := filepath.Join(takeoutDir, "active.json")
	var active takeout.Selection
	if err := layout.ReadJSON(activePath, &active); err == nil {
		return active, selectionResult(active, "pinned", time.Time{}, time.Time{}, manualReady), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "error"}, fmt.Errorf("%w: read pinned selection: %v", ErrSelectionFailed, err)
	}

	listing, err := builder.Build(ctx, job.Source, "metadata")
	if err != nil {
		return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "error"}, fmt.Errorf("%w: list Takeout source: %v", ErrSelectionFailed, err)
	}
	observationPath := filepath.Join(takeoutDir, "observation.json")
	var previous takeout.Observation
	if err := layout.ReadJSON(observationPath, &previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "error"}, fmt.Errorf("%w: read observation: %v", ErrSelectionFailed, err)
	}
	candidate, err := takeout.Inspect(listing.Entries, previous, s.Now().UTC(), job.Source.SettleDuration(), manualReady)
	if err != nil {
		if errors.Is(err, takeout.ErrNoArchive) {
			return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "waiting"}, fmt.Errorf("%w: %v", ErrWaiting, err)
		}
		return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "ambiguous"}, fmt.Errorf("%w: %v", ErrSelectionFailed, err)
	}
	if err := layout.WriteJSON(observationPath, candidate.Observation); err != nil {
		return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "error"}, fmt.Errorf("%w: write observation: %v", ErrSelectionFailed, err)
	}
	report := selectionResult(candidate.Selection, "waiting", candidate.Observation.UnchangedSince, candidate.ReadyAfter, manualReady)

	var completed takeout.Selection
	completedPath := filepath.Join(takeoutDir, "completed.json")
	if err := layout.ReadJSON(completedPath, &completed); err == nil && completed.Fingerprint == candidate.Fingerprint {
		report.Status = "up_to_date"
		return takeout.Selection{}, report, fmt.Errorf("%w: latest export was already completed", ErrWaiting)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return takeout.Selection{}, report, fmt.Errorf("%w: read completed selection: %v", ErrSelectionFailed, err)
	}
	if !candidate.Ready {
		return takeout.Selection{}, report, fmt.Errorf("%w: export has not been unchanged for %s", ErrWaiting, job.Source.Selector.SettleFor)
	}
	if err := layout.WriteJSON(activePath, candidate.Selection); err != nil {
		return takeout.Selection{}, report, fmt.Errorf("%w: pin selection: %v", ErrSelectionFailed, err)
	}
	report.Status = "pinned"
	return candidate.Selection, report, nil
}

func (s Service) completeTakeout(layout state.Layout, job string) error {
	takeoutDir := layout.JobPath(job, "takeout")
	activePath := filepath.Join(takeoutDir, "active.json")
	var active takeout.Selection
	if err := layout.ReadJSON(activePath, &active); err != nil {
		return err
	}
	if err := layout.WriteJSON(filepath.Join(takeoutDir, "completed.json"), active); err != nil {
		return err
	}
	return os.Remove(activePath)
}

func selectionResult(selection takeout.Selection, status string, unchangedSince, readyAfter time.Time, manualReady bool) model.SelectionResult {
	return model.SelectionResult{
		Kind:           "google-takeout-latest",
		Status:         status,
		ArchiveCount:   len(selection.Paths),
		TotalBytes:     selection.TotalBytes,
		ObservedAt:     selection.SelectedAt,
		UnchangedSince: unchangedSince,
		ReadyAfter:     readyAfter,
		ManualReady:    manualReady,
	}
}

func writeSelectionList(filePath string, paths []string) error {
	for _, item := range paths {
		if item == "" || strings.ContainsAny(item, "\r\n") {
			return errors.New("selection contains an invalid path")
		}
	}
	return os.WriteFile(filePath, []byte(strings.Join(paths, "\n")+"\n"), 0o640)
}

func (s Service) runLogged(ctx context.Context, args []string, logPath, phase string) (process.Result, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return process.Result{ExitCode: 1, Err: err}, err
	}
	defer logFile.Close()

	logWriter := redact.NewLineWriter(logFile)
	target := io.Writer(logWriter)
	var progress *progressWriter
	if s.Out != nil {
		progress = newProgressWriter(logWriter, s.Out, phase)
		target = progress
	}
	result := s.Executor.Run(ctx, s.RcloneBin, args, target, target)
	flushErr := logWriter.Flush()
	if progress != nil {
		if err := progress.Flush(); flushErr == nil {
			flushErr = err
		}
	}
	if result.Err != nil {
		return result, result.Err
	}
	if flushErr != nil {
		return process.Result{ExitCode: 1, Err: flushErr}, flushErr
	}
	return result, nil
}

func emitPhase(out io.Writer, phase, status string) {
	if out != nil {
		_, _ = fmt.Fprintf(out, "photo-bridge: phase=%s status=%s\n", phase, status)
	}
}

func (s Service) validateRuntime(job config.Job) error {
	if s.StateDir == "" {
		return errors.New("state directory is required")
	}
	if s.Executor == nil {
		return errors.New("command executor is required")
	}
	if job.Source.UsesRcloneConfig() || job.Destination.UsesRcloneConfig() {
		if s.RcloneConf == "" {
			return errors.New("PHOTOBRIDGE_RCLONE_CONFIG_FILE is required for rclone endpoints")
		}
		info, err := os.Stat(s.RcloneConf)
		if err != nil {
			return fmt.Errorf("inspect rclone configuration file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("rclone configuration path must be a regular file")
		}
		if info.Mode().Perm()&0o007 != 0 {
			return errors.New("rclone configuration file must not be world-accessible")
		}
	}
	return nil
}

func (s Service) withDefaults() Service {
	if s.StateDir == "" {
		s.StateDir = "/state"
	}
	if s.RcloneBin == "" {
		s.RcloneBin = "rclone"
	}
	if s.Executor == nil {
		s.Executor = process.OSExecutor{}
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	return s
}

func (s Service) withRcloneConfig(args []string) []string {
	if s.RcloneConf == "" {
		return args
	}
	return append(args, "--config", s.RcloneConf)
}

func (s Service) finishFailure(layout state.Layout, paths state.RunPaths, report model.RunReport, status string, err error) (model.RunReport, error) {
	report.Status = status
	report.Error = redact.String(err.Error())
	return s.finish(layout, paths, report, err)
}

func (s Service) finish(layout state.Layout, paths state.RunPaths, report model.RunReport, runErr error) (model.RunReport, error) {
	report.FinishedAt = s.Now().UTC()
	if err := layout.WriteReport(report.Job, paths, report); err != nil {
		if runErr != nil {
			return report, fmt.Errorf("%v; additionally failed to write report: %w", runErr, err)
		}
		return report, err
	}
	return report, runErr
}

func newReport(job config.Job, runID string, started time.Time, dryRun bool) model.RunReport {
	return model.RunReport{
		SchemaVersion:     model.ReportSchemaVersion,
		RunID:             runID,
		Job:               job.Name,
		Operation:         job.Operation,
		Status:            "running",
		StartedAt:         started,
		SourceDriver:      job.Source.Driver,
		DestinationDriver: job.Destination.Driver,
		NonDestructive:    true,
		DryRun:            dryRun,
	}
}

func verificationMethod(requested string) string {
	switch requested {
	case "checksum":
		return "common-checksum"
	case "size":
		return "size-and-name"
	default:
		return "hash-when-available-otherwise-size"
	}
}

func WriteJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func RelativeStatePath(root, filePath string) string {
	relative, err := filepath.Rel(root, filePath)
	if err != nil {
		return filepath.Base(filePath)
	}
	return filepath.ToSlash(relative)
}
