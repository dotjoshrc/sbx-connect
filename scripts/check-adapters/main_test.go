package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestOptions(t *testing.T) {
	root := t.TempDir()
	physical, logical := filepath.Join(root, "physical"), filepath.Join(root, "logical")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(physical, logical); err != nil {
		t.Fatal(err)
	}
	t.Chdir(logical)
	t.Setenv("PWD", logical)
	opts, err := parseOptions([]string{"--project", ".", "--timeout", "0.25"}, io.Discard)
	if err != nil || opts.project != logical || opts.timeout != 250*time.Millisecond || !reflect.DeepEqual(opts.agents, []string{"codex", "claude"}) {
		t.Fatalf("%+v, %v", opts, err)
	}
	opts, err = parseOptions([]string{"--project", logical, "--agent", "claude", "--agent", "codex"}, io.Discard)
	if err != nil || !reflect.DeepEqual(opts.agents, []string{"claude", "codex"}) {
		t.Fatalf("%+v, %v", opts, err)
	}
	for _, args := range [][]string{
		{},
		{"--project", ".", "extra"},
		{"--project", ".", "--agent", "other"},
		{"--project", ".", "--timeout", "0"},
		{"--project", ".", "--timeout", "-1"},
		{"--project", ".", "--timeout", "NaN"},
		{"--project", ".", "--timeout", "+Inf"},
		{"--project", ".", "--timeout", "1e20"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}

// Run this test as a subprocess behind a shell shim to emulate the launcher CLI.
func TestLauncherHelper(t *testing.T) {
	if os.Getenv("SBX_CHECK_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	if len(args) != 4 || args[2] != "--project" || args[3] != os.Getenv("SBX_CHECK_PROJECT") {
		os.Exit(10)
	}
	verb, name := args[0], args[1]
	mode := os.Getenv("SBX_CHECK_MODE")
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	stop := func() {
		<-signals
		if err := os.WriteFile(filepath.Join(os.Getenv("SBX_CHECK_STATE"), verb+"-"+name+".terminated"), nil, 0o600); err != nil {
			os.Exit(11)
		}
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, verb+" diagnostics")
	if verb == "prepare" {
		if mode == "prepare-timeout" {
			stop()
		}
		if mode == "prepare-failure" {
			os.Exit(23)
		}
		os.Exit(0)
	}
	var request struct {
		ID     int    `json:"id"`
		Method string `json:"method"`
	}
	decoder := json.NewDecoder(os.Stdin)
	if decoder.Decode(&request) != nil || request.ID != 1 || request.Method != "initialize" {
		os.Exit(12)
	}
	switch mode {
	case "eof":
		os.Exit(0)
	case "protocol-error":
		fmt.Println(`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"failed"}}`)
	case "wrong-version":
		fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":2,"agentInfo":{"name":"fake","version":"1"}}}`)
	case "malformed":
		fmt.Println("not json")
	case "partial":
		fmt.Print(`{"jsonrpc":`)
	case "silent":
	default:
		fmt.Println(`{"jsonrpc":"2.0","method":"notification","params":{}}`)
		fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentInfo":{"name":"fake","version":"1"}}}`)
	}
	stop()
}

func helperOptions(t *testing.T, mode string) options {
	t.Helper()
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SBX_CHECK_HELPER", "1")
	t.Setenv("SBX_CHECK_EXE", exe)
	t.Setenv("SBX_CHECK_PROJECT", root)
	t.Setenv("SBX_CHECK_STATE", root)
	t.Setenv("SBX_CHECK_MODE", mode)
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	launcher := filepath.Join(root, "launcher")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec \"$SBX_CHECK_EXE\" -test.run '^TestLauncherHelper$' -- \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return options{launcher: launcher, project: root, agents: []string{"codex", "claude"}, timeout: 2 * time.Second}
}

func TestSuccessfulCheckAndShutdown(t *testing.T) {
	opts := helperOptions(t, "success")
	var out, diagnostics bytes.Buffer
	if err := run(context.Background(), opts, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, name := range opts.agents {
		if !strings.Contains(out.String(), "PASS "+name+": ACP initialize") {
			t.Fatal(out.String())
		}
		if _, err := os.Stat(filepath.Join(opts.project, "run-"+name+".terminated")); err != nil {
			t.Fatal("launcher did not receive SIGTERM:", err)
		}
	}
	if !strings.Contains(diagnostics.String(), "run diagnostics") || !strings.Contains(diagnostics.String(), "prepare diagnostics") {
		t.Fatal(diagnostics.String())
	}
}

func TestFailedChecks(t *testing.T) {
	for _, mode := range []string{"protocol-error", "wrong-version", "malformed", "eof", "prepare-failure", "partial", "silent", "prepare-timeout"} {
		t.Run(mode, func(t *testing.T) {
			opts := helperOptions(t, mode)
			opts.timeout = 300 * time.Millisecond
			var out bytes.Buffer
			err := run(context.Background(), opts, &out, io.Discard)
			if err == nil || out.Len() != 0 {
				t.Fatalf("output %s, error %v", out.String(), err)
			}
			if mode == "partial" || mode == "silent" || mode == "prepare-timeout" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
				verb := "run"
				if mode == "prepare-timeout" {
					verb = "prepare"
				}
				if _, err := os.Stat(filepath.Join(opts.project, verb+"-codex.terminated")); err != nil {
					t.Fatal("timeout bypassed graceful shutdown:", err)
				}
			}
		})
	}
}
