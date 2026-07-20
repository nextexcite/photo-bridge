package process

import (
	"context"
	"errors"
	"io"
	"os/exec"
)

type Result struct {
	ExitCode int
	Err      error
}

type Executor interface {
	Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) Result
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, name string, args []string, stdout, stderr io.Writer) Result {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if err == nil {
		return Result{ExitCode: 0}
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return Result{ExitCode: exitErr.ExitCode(), Err: err}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return Result{ExitCode: 124, Err: ctx.Err()}
	}
	return Result{ExitCode: 127, Err: err}
}
