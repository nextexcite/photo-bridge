package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	ErrLimitExceeded      = errors.New("configured transfer limit exceeded")
	ErrTimeout            = errors.New("configured job duration exceeded")
	ErrSourceChanged      = errors.New("pinned source identities changed")
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
	Progress   io.Writer
	Out        io.Writer
	Now        func() time.Time
}
type verificationPlan struct {
	requested, method, algorithm, strength string
	fallback                               bool
	args                                   []string
}

func (s Service) Plan(job config.Job) model.Plan {
	effectiveBufferMemory := int64(job.Policy.Transfer.Transfers) * job.Policy.BufferSizeBytes()
	if cap := job.Policy.MaxBufferMemoryBytes(); effectiveBufferMemory > cap {
		effectiveBufferMemory = cap
	}
	plan := model.Plan{Job: job.Name, Operation: job.Operation, SourceDriver: job.Source.Driver, DestinationDriver: job.Destination.Driver, Manifest: job.Policy.Integrity.Manifest, Verification: job.Policy.Integrity.Verification, Transfers: job.Policy.Transfer.Transfers, Checkers: job.Policy.Transfer.Checkers, BufferSize: job.Policy.Transfer.BufferSize, MaxBufferMemory: job.Policy.Transfer.MaxBufferMemory, EffectiveBufferMemory: effectiveBufferMemory, EffectiveTransfers: job.Policy.Transfer.Transfers, MaxDuration: job.Policy.Limits.MaxDuration, MaxFiles: job.Policy.Limits.MaxFiles, MaxBytes: job.Policy.Limits.MaxBytes, ProgressInterval: job.Policy.ProgressInterval, NonDestructive: true, Executor: "rclone-copy", DataPath: dataPath(job), ServerSideCopy: false, VerificationResolution: "runtime-preflight"}
	if job.Source.Selector != nil {
		plan.Selector = job.Source.Selector.Kind
	}
	return plan
}

func dataPath(job config.Job) string {
	if job.Source.Driver == "filesystem" && job.Destination.Driver == "filesystem" {
		return "host-local"
	}
	if job.Source.Driver == "filesystem" {
		return "host-upload"
	}
	if job.Destination.Driver == "filesystem" {
		return "host-download"
	}
	return "host-relay"
}

func (s Service) Run(ctx context.Context, job config.Job, options RunOptions) (model.RunReport, error) {
	s = s.withDefaults()
	if err := s.validateRuntime(job); err != nil {
		return model.RunReport{}, err
	}
	if max := job.Policy.MaxDuration(); max > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, max)
		defer cancel()
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
	if err := layout.ReconcileCurrent(job.Name, s.Now()); err != nil {
		return model.RunReport{}, fmt.Errorf("reconcile active state: %w", err)
	}
	started := s.Now().UTC()
	runID, paths, err := layout.NewRun(job.Name, started)
	if err != nil {
		return model.RunReport{}, fmt.Errorf("create run: %w", err)
	}
	report := newReport(job, runID, started, options.DryRun)
	report.Manifest.Requested = job.Policy.Integrity.Manifest
	report.Verification.Requested = job.Policy.Integrity.Verification
	if err := layout.WriteCurrent(job.Name, report); err != nil {
		return report, err
	}
	update := func(phase string, progress *model.Progress) {
		report.Phase = phase
		report.UpdatedAt = s.Now().UTC()
		if progress != nil {
			report.Progress = progress
		}
		_ = layout.WriteCurrent(job.Name, report)
	}
	update("selection", nil)
	builder := manifest.Builder{Executor: s.Executor, RcloneBin: s.RcloneBin, RcloneConf: s.RcloneConf, TempDir: paths.Directory}
	var selected *takeout.Selection
	if job.Source.Selector != nil {
		selection, selectionReport, selectionErr := s.selectTakeout(ctx, layout, job, builder, options.TakeoutReady)
		report.Selection = &selectionReport
		if selectionErr != nil {
			return s.finishFailure(layout, paths, report, job, waitingStatus(selectionErr), selectionErr)
		}
		selected = &selection
		if err := writeSelectionList(paths.SelectionList, selection.Paths); err != nil {
			return s.finishFailure(layout, paths, report, job, "selection_failed", fmt.Errorf("%w: %v", ErrSelectionFailed, err))
		}
	}
	update("manifest", nil)
	allowed := selectedPathSet(selected)
	manifestResult, err := builder.BuildToJSONL(ctx, job.Source, job.Policy.Integrity.Manifest, allowed, paths.Manifest)
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "manifest_failed", timeoutAware(ctx, err))
	}
	var selectedEntries []model.ManifestEntry
	if selected != nil {
		selectedEntries, err = manifest.ReadJSONL(paths.Manifest)
		if err != nil {
			return s.finishFailure(layout, paths, report, job, "manifest_failed", err)
		}
	}
	if err := validateEntries(job, selected, selectedEntries); err != nil {
		return s.finishFailure(layout, paths, report, job, "selection_failed", err)
	}
	if manifestResult.Entries == 0 && !job.Policy.Integrity.AllowEmpty {
		return s.finishFailure(layout, paths, report, job, "manifest_failed", errors.New("source is empty; set policy.integrity.allowEmpty to true to permit an empty copy"))
	}
	if err := enforceManifestLimits(job, manifestResult.Entries, manifestResult.Bytes); err != nil {
		return s.finishFailure(layout, paths, report, job, "limit_exceeded", err)
	}
	report.Manifest.Level, report.Manifest.Entries, report.Manifest.Path = manifestResult.Level, manifestResult.Entries, layout.Relative(paths.Manifest)
	update("verification-preflight", nil)
	verification, err := s.resolveVerification(ctx, job)
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "verification_failed", fmt.Errorf("%w: %v", ErrVerificationFailed, timeoutAware(ctx, err)))
	}
	if options.DryRun {
		transferResult, copyErr := s.runCopy(ctx, job, true, paths.TransferLog, paths.SelectionList, selected != nil, layout, update)
		report.Transfer = transferResult
		if copyErr != nil {
			return s.finishFailure(layout, paths, report, job, "transfer_failed", fmt.Errorf("%w: %v", ErrTransferFailed, timeoutAware(ctx, copyErr)))
		}
		report.Status, report.Phase = "dry_run", "completed"
		report.Verification = verification.result("skipped", 0, "")
		return s.finish(layout, paths, report, job, nil)
	}
	update("transfer", nil)
	transferResult, err := s.runCopy(ctx, job, false, paths.TransferLog, paths.SelectionList, selected != nil, layout, update)
	report.Transfer = transferResult
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "transfer_failed", fmt.Errorf("%w: %v", ErrTransferFailed, timeoutAware(ctx, err)))
	}
	update("source-revalidation", nil)
	postPath := filepath.Join(paths.Directory, ".source-revalidation.jsonl")
	defer os.Remove(postPath)
	post, err := builder.BuildToJSONL(ctx, job.Source, job.Policy.Integrity.Manifest, allowed, postPath)
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "selection_failed", timeoutAware(ctx, err))
	}
	if manifestResult.Fingerprint != post.Fingerprint || manifestResult.Entries != post.Entries || manifestResult.Bytes != post.Bytes {
		return s.finishFailure(layout, paths, report, job, "selection_failed", fmt.Errorf("%w after copy", ErrSourceChanged))
	}
	update("verification", nil)
	verificationResult, err := s.runVerification(ctx, job, verification, paths.VerificationLog, paths.SelectionList, selected != nil, layout, update)
	report.Verification = verificationResult
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "verification_failed", fmt.Errorf("%w: %v", ErrVerificationFailed, timeoutAware(ctx, err)))
	}
	report.Status, report.Phase = "succeeded", "completed"
	if selected != nil {
		if err := s.completeTakeout(layout, job.Name); err != nil {
			return s.finishFailure(layout, paths, report, job, "selection_failed", fmt.Errorf("%w: %v", ErrSelectionFailed, err))
		}
		report.Selection.Status = "completed"
	}
	return s.finish(layout, paths, report, job, nil)
}

func waitingStatus(err error) string {
	if errors.Is(err, ErrWaiting) {
		return "waiting"
	}
	return "selection_failed"
}
func selectedPathSet(selection *takeout.Selection) map[string]struct{} {
	if selection == nil {
		return nil
	}
	result := make(map[string]struct{}, len(selection.Paths))
	for _, item := range selection.Paths {
		result[item] = struct{}{}
	}
	return result
}
func validateEntries(job config.Job, selection *takeout.Selection, entries []model.ManifestEntry) error {
	if selection == nil {
		return nil
	}
	if len(selection.Entries) == 0 {
		return fmt.Errorf("%w: pinned selection has no identities", ErrSelectionFailed)
	}
	if !sameEntries(selection.Entries, entries) {
		return fmt.Errorf("%w before copy", ErrSourceChanged)
	}
	return nil
}
func sameEntries(left, right []model.ManifestEntry) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]model.ManifestEntry(nil), left...)
	right = append([]model.ManifestEntry(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i].Path < left[j].Path })
	sort.Slice(right, func(i, j int) bool { return right[i].Path < right[j].Path })
	for i := range left {
		if left[i].Path != right[i].Path || left[i].Size != right[i].Size || !left[i].ModTime.UTC().Equal(right[i].ModTime.UTC()) || left[i].HashAlgorithm != right[i].HashAlgorithm || left[i].Hash != right[i].Hash {
			return false
		}
	}
	return true
}
func enforceLimits(job config.Job, entries []model.ManifestEntry) error {
	var total int64
	for _, entry := range entries {
		total += entry.Size
	}
	if job.Policy.Limits.MaxFiles > 0 && int64(len(entries)) > job.Policy.Limits.MaxFiles {
		return fmt.Errorf("%w: source has %d files, maximum is %d", ErrLimitExceeded, len(entries), job.Policy.Limits.MaxFiles)
	}
	if job.Policy.Limits.MaxBytes > 0 && total > job.Policy.Limits.MaxBytes {
		return fmt.Errorf("%w: source has %d bytes, maximum is %d", ErrLimitExceeded, total, job.Policy.Limits.MaxBytes)
	}
	return nil
}

func enforceManifestLimits(job config.Job, count int, total int64) error {
	if job.Policy.Limits.MaxFiles > 0 && int64(count) > job.Policy.Limits.MaxFiles {
		return fmt.Errorf("%w: source has %d files, maximum is %d", ErrLimitExceeded, count, job.Policy.Limits.MaxFiles)
	}
	if job.Policy.Limits.MaxBytes > 0 && total > job.Policy.Limits.MaxBytes {
		return fmt.Errorf("%w: source has %d bytes, maximum is %d", ErrLimitExceeded, total, job.Policy.Limits.MaxBytes)
	}
	return nil
}
func timeoutAware(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrTimeout, err)
	}
	return err
}

func (s Service) Verify(ctx context.Context, job config.Job) (model.RunReport, error) {
	s = s.withDefaults()
	if err := s.validateRuntime(job); err != nil {
		return model.RunReport{}, err
	}
	if max := job.Policy.MaxDuration(); max > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, max)
		defer cancel()
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
	if err := layout.ReconcileCurrent(job.Name, s.Now()); err != nil {
		return model.RunReport{}, err
	}
	started := s.Now().UTC()
	runID, paths, err := layout.NewRun(job.Name, started)
	if err != nil {
		return model.RunReport{}, err
	}
	report := newReport(job, runID, started, false)
	report.Operation, report.Manifest, report.Transfer = "verify", model.ManifestResult{Requested: job.Policy.Integrity.Manifest, Level: "persisted"}, model.CommandResult{Status: "skipped", ExitCode: 0}
	_ = layout.WriteCurrent(job.Name, report)
	update := func(phase string, progress *model.Progress) {
		report.Phase = phase
		if progress != nil {
			report.Progress = progress
		}
		_ = layout.WriteCurrent(job.Name, report)
	}
	latest, err := layout.LoadLatest(job.Name)
	if err != nil || latest.Manifest.Path == "" {
		return s.finishFailure(layout, paths, report, job, "manifest_failed", errors.New("no persisted manifest is available; run the job first"))
	}
	persistedPath := filepath.Join(s.StateDir, filepath.FromSlash(latest.Manifest.Path))
	persisted, err := manifest.SummarizeJSONL(persistedPath)
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "manifest_failed", err)
	}
	builder := manifest.Builder{Executor: s.Executor, RcloneBin: s.RcloneBin, RcloneConf: s.RcloneConf, TempDir: paths.Directory}
	update("source-revalidation", nil)
	currentPath := filepath.Join(paths.Directory, ".source-verification.jsonl")
	defer os.Remove(currentPath)
	current, err := builder.BuildToJSONL(ctx, job.Source, job.Policy.Integrity.Manifest, nil, currentPath)
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "manifest_failed", timeoutAware(ctx, err))
	}
	if persisted.Fingerprint != current.Fingerprint || persisted.Entries != current.Entries || persisted.Bytes != current.Bytes {
		return s.finishFailure(layout, paths, report, job, "selection_failed", fmt.Errorf("%w before verification", ErrSourceChanged))
	}
	var selected *takeout.Selection
	if job.Source.Selector != nil {
		var completed takeout.Selection
		if err := layout.ReadJSON(layout.JobPath(job.Name, "takeout", "completed.json"), &completed); err != nil {
			return s.finishFailure(layout, paths, report, job, "selection_failed", fmt.Errorf("%w: no completed Takeout selection: %v", ErrSelectionFailed, err))
		}
		persistedEntries, err := manifest.ReadJSONL(persistedPath)
		if err != nil {
			return s.finishFailure(layout, paths, report, job, "manifest_failed", err)
		}
		if !sameEntries(completed.Entries, persistedEntries) {
			return s.finishFailure(layout, paths, report, job, "selection_failed", fmt.Errorf("%w: completed Takeout does not match persisted manifest", ErrSourceChanged))
		}
		selected = &completed
		if err := writeSelectionList(paths.SelectionList, completed.Paths); err != nil {
			return s.finishFailure(layout, paths, report, job, "selection_failed", err)
		}
		result := selectionResult(completed, "completed", time.Time{}, time.Time{}, false)
		report.Selection = &result
	}
	update("verification-preflight", nil)
	resolution, err := s.resolveVerification(ctx, job)
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "verification_failed", fmt.Errorf("%w: %v", ErrVerificationFailed, err))
	}
	update("verification", nil)
	check, err := s.runVerification(ctx, job, resolution, paths.VerificationLog, paths.SelectionList, selected != nil, layout, update)
	report.Verification = check
	if err != nil {
		return s.finishFailure(layout, paths, report, job, "verification_failed", fmt.Errorf("%w: %v", ErrVerificationFailed, timeoutAware(ctx, err)))
	}
	report.Status, report.Phase = "succeeded", "completed"
	return s.finish(layout, paths, report, job, nil)
}

func (s Service) Status(job string) (model.RunReport, error) {
	s = s.withDefaults()
	layout := state.Layout{Root: s.StateDir}
	if err := layout.Ensure(); err != nil {
		return model.RunReport{}, err
	}
	locked, err := layout.IsLocked(job)
	if err != nil {
		return model.RunReport{}, err
	}
	if locked {
		if current, err := layout.LoadCurrent(job); err == nil {
			return current, nil
		}
	}
	if current, err := layout.LoadCurrent(job); err == nil && current.Status == "running" {
		current.Status = "interrupted"
		current.Phase = "interrupted"
		current.Error = "previous process exited before recording a terminal result"
		return current, nil
	}
	return layout.LoadLatest(job)
}

func (s Service) resolveVerification(ctx context.Context, job config.Job) (verificationPlan, error) {
	requested := job.Policy.Integrity.Verification
	if requested == "size" {
		return verificationPlan{requested: requested, method: "size-and-name", strength: "size", args: []string{"--size-only"}}, nil
	}
	if requested == "download" {
		return verificationPlan{requested: requested, method: "download-content", strength: "content", args: []string{"--download"}}, nil
	}
	source, err := s.backendHashes(ctx, job.Source)
	if err != nil {
		return verificationPlan{}, err
	}
	destination, err := s.backendHashes(ctx, job.Destination)
	if err != nil {
		return verificationPlan{}, err
	}
	algorithm := commonHash(source, destination)
	if algorithm != "" {
		return verificationPlan{requested: requested, method: "common-checksum", algorithm: algorithm, strength: "checksum", args: []string{"--checksum"}}, nil
	}
	if requested == "checksum" {
		return verificationPlan{}, errors.New("checksum verification requires a common advertised source and destination hash")
	}
	return verificationPlan{requested: requested, method: "size-and-name", strength: "size", fallback: true, args: []string{"--size-only"}}, nil
}
func (s Service) backendHashes(ctx context.Context, endpoint config.Endpoint) ([]string, error) {
	args := s.withRcloneConfig([]string{"backend", "features", endpoint.RcloneSpec()})
	var stdout, stderr strings.Builder
	result := s.Executor.Run(ctx, s.RcloneBin, args, &stdout, &stderr)
	if result.Err != nil {
		return nil, fmt.Errorf("preflight backend features: exit %d", result.ExitCode)
	}
	var payload any
	if err := json.Unmarshal([]byte(stdout.String()), &payload); err != nil {
		return nil, fmt.Errorf("decode backend features: %w", err)
	}
	hashes := findHashes(payload)
	if len(hashes) == 0 {
		return nil, nil
	}
	return hashes, nil
}
func findHashes(value any) []string {
	result := map[string]struct{}{}
	var walk func(any)
	walk = func(v any) {
		switch value := v.(type) {
		case map[string]any:
			for key, child := range value {
				if strings.EqualFold(key, "Hashes") {
					if values, ok := child.([]any); ok {
						for _, item := range values {
							if text, ok := item.(string); ok && text != "" {
								result[strings.ToLower(text)] = struct{}{}
							}
						}
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(value)
	out := make([]string, 0, len(result))
	for key := range result {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
func commonHash(source, destination []string) string {
	has := map[string]struct{}{}
	for _, item := range source {
		has[strings.ToLower(item)] = struct{}{}
	}
	for _, item := range destination {
		if _, ok := has[strings.ToLower(item)]; ok {
			return strings.ToLower(item)
		}
	}
	return ""
}
func (p verificationPlan) result(status string, code int, logPath string) model.VerificationResult {
	return model.VerificationResult{Requested: p.requested, Method: p.method, HashAlgorithm: p.algorithm, Fallback: p.fallback, Strength: p.strength, Status: status, ExitCode: code, LogPath: logPath}
}

func (s Service) runCopy(ctx context.Context, job config.Job, dryRun bool, logPath, selectionList string, selected bool, layout state.Layout, update func(string, *model.Progress)) (model.CommandResult, error) {
	args := []string{"copy", job.Source.RcloneSpec(), job.Destination.RcloneSpec(), "--transfers", strconv.Itoa(job.Policy.Transfer.Transfers), "--checkers", strconv.Itoa(job.Policy.Transfer.Checkers), "--buffer-size", job.Policy.Transfer.BufferSize, "--max-buffer-memory", job.Policy.Transfer.MaxBufferMemory, "--low-level-retries", "10", "--use-json-log", "--log-level", "INFO", "--stats", job.Policy.ProgressInterval}
	if dryRun {
		args = append(args, "--dry-run")
	}
	if selected {
		args = append(args, "--files-from-raw", selectionList)
	}
	args = s.withRcloneConfig(args)
	emitPhase(s.progressWriter(), "transfer", "started")
	result, err := s.runLogged(ctx, args, logPath, "transfer", job.Policy.ProgressEvery(), update)
	command := model.CommandResult{Status: "succeeded", ExitCode: result.ExitCode, LogPath: layout.Relative(logPath)}
	if err != nil {
		command.Status = "failed"
	}
	return command, err
}
func (s Service) runVerification(ctx context.Context, job config.Job, verification verificationPlan, logPath, selectionList string, selected bool, layout state.Layout, update func(string, *model.Progress)) (model.VerificationResult, error) {
	args := []string{"check", job.Source.RcloneSpec(), job.Destination.RcloneSpec(), "--one-way", "--use-json-log", "--log-level", "INFO"}
	args = append(args, verification.args...)
	if selected {
		args = append(args, "--files-from-raw", selectionList)
	}
	args = s.withRcloneConfig(args)
	emitPhase(s.progressWriter(), "verification", "started")
	result, err := s.runLogged(ctx, args, logPath, "verification", job.Policy.ProgressEvery(), update)
	out := verification.result("succeeded", result.ExitCode, layout.Relative(logPath))
	if err != nil {
		out.Status = "failed"
	}
	return out, err
}

func (s Service) selectTakeout(ctx context.Context, layout state.Layout, job config.Job, builder manifest.Builder, manualReady bool) (takeout.Selection, model.SelectionResult, error) {
	takeoutDir := layout.JobPath(job.Name, "takeout")
	activePath := filepath.Join(takeoutDir, "active.json")
	var active takeout.Selection
	if err := layout.ReadJSON(activePath, &active); err == nil {
		if len(active.Entries) == 0 {
			return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "error"}, fmt.Errorf("%w: active selection lacks v1alpha2 identities", ErrSelectionFailed)
		}
		return active, selectionResult(active, "pinned", time.Time{}, time.Time{}, manualReady), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return takeout.Selection{}, model.SelectionResult{Kind: "google-takeout-latest", Status: "error"}, fmt.Errorf("%w: read pinned selection: %v", ErrSelectionFailed, err)
	}
	listing, err := builder.Build(ctx, job.Source, job.Policy.Integrity.Manifest)
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
	activePath := layout.JobPath(job, "takeout", "active.json")
	var active takeout.Selection
	if err := layout.ReadJSON(activePath, &active); err != nil {
		return err
	}
	if err := layout.WriteJSON(layout.JobPath(job, "takeout", "completed.json"), active); err != nil {
		return err
	}
	return os.Remove(activePath)
}
func selectionResult(selection takeout.Selection, status string, unchangedSince, readyAfter time.Time, manualReady bool) model.SelectionResult {
	return model.SelectionResult{Kind: "google-takeout-latest", Status: status, ArchiveCount: len(selection.Paths), TotalBytes: selection.TotalBytes, ObservedAt: selection.SelectedAt, UnchangedSince: unchangedSince, ReadyAfter: readyAfter, ManualReady: manualReady}
}
func writeSelectionList(filePath string, paths []string) error {
	for _, item := range paths {
		if item == "" || strings.ContainsAny(item, "\r\n") {
			return errors.New("selection contains an invalid path")
		}
	}
	return os.WriteFile(filePath, []byte(strings.Join(paths, "\n")+"\n"), 0o640)
}
func (s Service) runLogged(ctx context.Context, args []string, logPath, phase string, every time.Duration, update func(string, *model.Progress)) (process.Result, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return process.Result{ExitCode: 1, Err: err}, err
	}
	defer logFile.Close()
	logWriter := redact.NewLineWriter(logFile)
	target := io.Writer(logWriter)
	progress := newProgressWriter(logWriter, s.progressWriter(), phase, every, func(p model.Progress) { update(phase, &p) })
	target = progress
	result := s.Executor.Run(ctx, s.RcloneBin, args, target, target)
	flushErr := logWriter.Flush()
	if err := progress.Flush(); flushErr == nil {
		flushErr = err
	}
	if result.Err != nil {
		return result, result.Err
	}
	if flushErr != nil {
		return process.Result{ExitCode: 1, Err: flushErr}, flushErr
	}
	return result, nil
}
func (s Service) progressWriter() io.Writer {
	if s.Progress != nil {
		return s.Progress
	}
	if s.Out != nil {
		return s.Out
	}
	return io.Discard
}
func emitPhase(out io.Writer, phase, status string) {
	_, _ = fmt.Fprintf(out, "photo-bridge: phase=%s status=%s\n", phase, status)
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
func (s Service) finishFailure(layout state.Layout, paths state.RunPaths, report model.RunReport, job config.Job, status string, err error) (model.RunReport, error) {
	report.Status = status
	report.Phase = status
	report.Error = redact.String(err.Error())
	return s.finish(layout, paths, report, job, err)
}
func (s Service) finish(layout state.Layout, paths state.RunPaths, report model.RunReport, job config.Job, runErr error) (model.RunReport, error) {
	report.FinishedAt = s.Now().UTC()
	if err := layout.WriteReport(report.Job, paths, report); err != nil {
		if runErr != nil {
			return report, fmt.Errorf("%v; additionally failed to write report: %w", runErr, err)
		}
		return report, err
	}
	if job.Policy.Retention.Mode == "automatic" {
		result, err := layout.Prune(job.Name, job.Policy.RetentionAge(), job.Policy.Retention.MinRuns, false, s.Now())
		if err != nil {
			report.Retention = model.RetentionResult{Status: "warning", Warning: "retention maintenance failed"}
		} else {
			report.Retention = model.RetentionResult{Status: "completed", DeletedRuns: result.DeletedRuns, ReclaimedBytes: result.ReclaimedBytes}
		}
		if err := layout.WriteReport(report.Job, paths, report); err != nil {
			if runErr != nil {
				return report, fmt.Errorf("%v; additionally failed to update report: %w", runErr, err)
			}
			return report, err
		}
	}
	return report, runErr
}
func newReport(job config.Job, runID string, started time.Time, dryRun bool) model.RunReport {
	return model.RunReport{SchemaVersion: model.ReportSchemaVersion, RunID: runID, Job: job.Name, Operation: job.Operation, Status: "running", Phase: "starting", StartedAt: started, UpdatedAt: started, SourceDriver: job.Source.Driver, DestinationDriver: job.Destination.Driver, NonDestructive: true, DryRun: dryRun}
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
