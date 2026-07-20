package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/nextexcite/photo-bridge/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit((app.Application{Out: os.Stdout, Err: os.Stderr}).Run(ctx, os.Args[1:]))
}
