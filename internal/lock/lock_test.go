package lock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLockTimeoutAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	release, err := Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Acquire(context.Background(), path, 60*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx, path, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestLockReleasedOnProcessDeath(t *testing.T) {
	if path := os.Getenv("SBX_CONNECT_LOCK_TEST"); path != "" {
		release, err := Acquire(context.Background(), path, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := os.WriteFile(path+".ready", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	path := filepath.Join(t.TempDir(), "lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockReleasedOnProcessDeath$")
	cmd.Env = append(os.Environ(), "SBX_CONNECT_LOCK_TEST="+path)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lock holder not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := Acquire(context.Background(), path, 60*time.Millisecond); err == nil {
		t.Fatal("child did not hold lock")
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	release, err := Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
