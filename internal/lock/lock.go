// Package lock provides process-scoped advisory locks on macOS and Linux.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const Timeout = 5 * time.Minute

// Acquire keeps the lock file permanently: unlinking it would let waiters lock
// different inodes. The kernel releases flock on close, including process death.
func Acquire(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		if err := wait.Err(); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("waiting for lock %s: %w", path, err)
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, err
		}
		select {
		case <-wait.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}
