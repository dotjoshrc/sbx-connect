package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"sbx-connect/internal/app"
	"sbx-connect/internal/process"
)

var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)

	go func() {
		select {
		case sig := <-signals:
			cancel(process.SignalError{Signal: sig.(syscall.Signal)})
		case <-ctx.Done():
		}
	}()

	a := app.App{
		Version: version,
		In:      os.Stdin,
		Out:     os.Stdout,
		Err:     os.Stderr,
	}
	err := a.Command().Run(ctx, os.Args)

	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sbx-connect:", err)
	}
	return process.ExitCode(err)
}
