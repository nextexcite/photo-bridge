package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nextexcite/photo-bridge/internal/buildinfo"
	"github.com/nextexcite/photo-bridge/internal/config"
	"github.com/nextexcite/photo-bridge/internal/process"
	"github.com/nextexcite/photo-bridge/internal/redact"
	"github.com/nextexcite/photo-bridge/internal/runner"
	"github.com/nextexcite/photo-bridge/internal/state"
)

const (
	ExitOK           = 0
	ExitConfig       = 2
	ExitTransfer     = 3
	ExitVerification = 4
	ExitLocked       = 5
	ExitWaiting      = 6
	ExitSelection    = 7
	ExitInternal     = 10
)

type Application struct {
	Out      io.Writer
	Err      io.Writer
	Executor process.Executor
}

func (a Application) Run(ctx context.Context, args []string) int {
	if a.Out == nil {
		a.Out = io.Discard
	}
	if a.Err == nil {
		a.Err = io.Discard
	}
	if a.Executor == nil {
		a.Executor = process.OSExecutor{}
	}
	if len(args) == 0 {
		a.usage()
		return ExitConfig
	}

	var err error
	switch args[0] {
	case "config":
		err = a.runConfig(args[1:])
	case "plan":
		err = a.runPlan(args[1:])
	case "run":
		err = a.runJob(ctx, args[1:])
	case "verify":
		err = a.runVerify(ctx, args[1:])
	case "status":
		err = a.runStatus(args[1:])
	case "version":
		err = json.NewEncoder(a.Out).Encode(buildinfo.Current())
	case "help", "--help", "-h":
		a.usage()
		return ExitOK
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(a.Err, redact.String(err.Error()))
	return classifyError(err)
}

func (a Application) runConfig(args []string) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: photo-bridge config validate [--config PATH]")
	}
	flags := newFlagSet("config validate", a.Err)
	configPath := flags.String("config", defaultConfigPath(), "configuration path")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.Out, "configuration valid: %d job(s)\n", len(cfg.Jobs))
	return err
}

func (a Application) runPlan(args []string) error {
	flags := newFlagSet("plan", a.Err)
	configPath := flags.String("config", defaultConfigPath(), "configuration path")
	jobName := flags.String("job", "", "job name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	job, err := loadJob(*configPath, *jobName)
	if err != nil {
		return err
	}
	return runner.WriteJSON(a.Out, (runner.Service{}).Plan(job))
}

func (a Application) runJob(ctx context.Context, args []string) error {
	flags := newFlagSet("run", a.Err)
	configPath := flags.String("config", defaultConfigPath(), "configuration path")
	stateDir := flags.String("state-dir", defaultStateDir(), "state directory")
	jobName := flags.String("job", "", "job name")
	dryRun := flags.Bool("dry-run", false, "plan transfers without writing destination data")
	takeoutReady := flags.Bool("takeout-ready", false, "confirm the latest Takeout export is complete")
	if err := flags.Parse(args); err != nil {
		return err
	}
	job, err := loadJob(*configPath, *jobName)
	if err != nil {
		return err
	}
	report, err := a.service(*stateDir).Run(ctx, job, runner.RunOptions{DryRun: *dryRun, TakeoutReady: *takeoutReady})
	if report.RunID != "" {
		_ = runner.WriteJSON(a.Out, report)
	}
	return err
}

func (a Application) runVerify(ctx context.Context, args []string) error {
	flags := newFlagSet("verify", a.Err)
	configPath := flags.String("config", defaultConfigPath(), "configuration path")
	stateDir := flags.String("state-dir", defaultStateDir(), "state directory")
	jobName := flags.String("job", "", "job name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	job, err := loadJob(*configPath, *jobName)
	if err != nil {
		return err
	}
	report, err := a.service(*stateDir).Verify(ctx, job)
	if report.RunID != "" {
		_ = runner.WriteJSON(a.Out, report)
	}
	return err
}

func (a Application) runStatus(args []string) error {
	flags := newFlagSet("status", a.Err)
	stateDir := flags.String("state-dir", defaultStateDir(), "state directory")
	jobName := flags.String("job", "", "job name")
	jsonOutput := flags.Bool("json", false, "emit JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *jobName == "" {
		return errors.New("--job is required")
	}
	report, err := (runner.Service{StateDir: *stateDir}).Status(*jobName)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return runner.WriteJSON(a.Out, report)
	}
	_, err = fmt.Fprintf(a.Out, "%s: %s (%s)\n", report.Job, report.Status, report.RunID)
	return err
}

func (a Application) service(stateDir string) runner.Service {
	return runner.Service{
		StateDir:   stateDir,
		RcloneBin:  envOr("PHOTOBRIDGE_RCLONE_BIN", "rclone"),
		RcloneConf: os.Getenv("PHOTOBRIDGE_RCLONE_CONFIG_FILE"),
		Executor:   a.Executor,
		Out:        a.Out,
	}
}

func (a Application) usage() {
	fmt.Fprintln(a.Out, `photo-bridge performs non-destructive, manifest-backed archival copies.

Commands:
  config validate  Validate a value-free job configuration.
  plan             Print the normalized non-destructive execution plan.
  run              Create a manifest, copy, and verify a job.
  verify           Verify an existing source and destination pair.
  status           Read the latest persisted job report.
  version          Print build information.`)
}

func loadJob(configPath, name string) (config.Job, error) {
	if strings.TrimSpace(name) == "" {
		return config.Job{}, errors.New("--job is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Job{}, err
	}
	return cfg.FindJob(name)
}

func classifyError(err error) int {
	switch {
	case errors.Is(err, state.ErrLocked):
		return ExitLocked
	case errors.Is(err, runner.ErrVerificationFailed):
		return ExitVerification
	case errors.Is(err, runner.ErrTransferFailed):
		return ExitTransfer
	case errors.Is(err, runner.ErrWaiting):
		return ExitWaiting
	case errors.Is(err, runner.ErrSelectionFailed):
		return ExitSelection
	}
	message := err.Error()
	if strings.Contains(message, "configuration") || strings.Contains(message, "--job") || strings.Contains(message, "apiVersion") || strings.Contains(message, "required") || strings.Contains(message, "usage:") || strings.Contains(message, "unknown command") {
		return ExitConfig
	}
	return ExitInternal
}

func newFlagSet(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	return flags
}

func defaultConfigPath() string {
	return envOr("PHOTOBRIDGE_CONFIG", "/config/config.yaml")
}

func defaultStateDir() string {
	return envOr("PHOTOBRIDGE_STATE_DIR", "/state")
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
