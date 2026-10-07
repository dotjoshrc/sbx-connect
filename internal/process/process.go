// Package process runs sbx without a shell or TTY. Setup never reads editor stdin.
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type SignalError struct{ Signal syscall.Signal }

func (e SignalError) Error() string { return e.Signal.String() }

type Runner struct {
	Binary         string
	Stdin          io.Reader
	Stdout, Stderr io.Writer
}

func (r Runner) run(ctx context.Context, in io.Reader, out io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, r.Binary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, r.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		sig := syscall.SIGTERM
		if cause, ok := errors.AsType[SignalError](context.Cause(ctx)); ok {
			sig = cause.Signal
		}
		err := syscall.Kill(-cmd.Process.Pid, sig)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sbx %s: %w", args[0], err)
	}
	return nil
}

func (r Runner) Setup(ctx context.Context, args ...string) error {
	return r.run(ctx, nil, r.Stderr, args...)
}

func (r Runner) Capture(ctx context.Context, args ...string) (string, error) {
	var b bytes.Buffer
	err := r.run(ctx, nil, &b, args...)
	return b.String(), err
}

func (r Runner) Stream(ctx context.Context, args ...string) error {
	return r.run(ctx, r.Stdin, r.Stdout, args...)
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if sig, ok := errors.AsType[SignalError](err); ok {
		return 128 + int(sig.Signal)
	}
	if child, ok := errors.AsType[*exec.ExitError](err); ok {
		if status, ok := child.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal())
		}
		return child.ExitCode()
	}
	return 1
}
