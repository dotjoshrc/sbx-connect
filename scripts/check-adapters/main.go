// Command check-adapters smoke-tests kit ACP initialization without model prompts.
// Sandboxes are retained after validation.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"sbx-connect/internal/agent"
	"sbx-connect/internal/sandbox"
)

const initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{},"clientInfo":{"name":"sbx-connect-validation","version":"0"}}}` + "\n"

type options struct {
	launcher, project string
	agents            []string
	timeout           time.Duration
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("check-adapters", flag.ContinueOnError)
	flags.SetOutput(out)
	flags.Usage = func() {
		fmt.Fprintln(out, "Smoke-test kit ACP initialization without model prompts.")
		fmt.Fprintln(out, "Requires sbx and its local service; configure provider credentials before first use.")
		fmt.Fprintln(out, "Creates/reuses persistent sandboxes and may download kits, images, and adapters.")
		fmt.Fprintln(out, "Sandboxes are retained after validation.\n\nUsage: check-adapters --project PATH [options]")
		flags.PrintDefaults()
	}
	flags.StringVar(&opts.launcher, "launcher", "./sbx-connect", "built launcher path")
	flags.StringVar(&opts.project, "project", "", "project directory (required)")
	seconds := flags.Float64("timeout", 120, "seconds per prepare/initialize, including cold downloads")
	flags.Func("agent", "codex or claude (repeatable; defaults to both)", func(name string) error {
		if _, err := agent.Lookup(name); err != nil {
			return err
		}
		opts.agents = append(opts.agents, name)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected positional arguments: %q", flags.Args())
	}
	if opts.project == "" {
		return opts, fmt.Errorf("--project is required")
	}
	if math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds <= 0 || *seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return opts, fmt.Errorf("--timeout must be a positive, finite duration")
	}
	opts.timeout = time.Duration(*seconds * float64(time.Second))
	if opts.timeout <= 0 {
		return opts, fmt.Errorf("--timeout must be at least one nanosecond")
	}
	var err error
	opts.launcher, err = filepath.Abs(opts.launcher)
	if err != nil {
		return opts, err
	}
	// Match the launcher's identity rules, including logical PWD and symlinks.
	opts.project, err = sandbox.AbsoluteProject(opts.project)
	if err != nil {
		return opts, err
	}
	if len(opts.agents) == 0 {
		for _, ag := range agent.All {
			opts.agents = append(opts.agents, ag.Name)
		}
	}
	return opts, nil
}

func launcherCommand(ctx context.Context, opts options, verb, name string, diagnostics io.Writer) *exec.Cmd {
	cmd := exec.CommandContext(ctx, opts.launcher, verb, name, "--project", opts.project)
	cmd.Stderr = diagnostics
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Let the launcher forward termination to sbx and reap it before escalating.
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func prepare(ctx context.Context, opts options, name string, diagnostics io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	cmd := launcherCommand(ctx, opts, "prepare", name, diagnostics)
	cmd.Stdout = diagnostics
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func initialize(out io.Reader) (json.RawMessage, error) {
	lines := bufio.NewScanner(out)
	lines.Buffer(make([]byte, 4096), 1024*1024)
	for lines.Scan() {
		var response struct {
			ID     json.RawMessage `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result struct {
				ProtocolVersion int             `json:"protocolVersion"`
				AgentInfo       json.RawMessage `json:"agentInfo"`
			} `json:"result"`
		}
		if err := json.Unmarshal(lines.Bytes(), &response); err != nil {
			return nil, fmt.Errorf("invalid ACP response: %w", err)
		}
		if string(response.ID) != "1" {
			continue
		}
		var info struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if len(response.Error) != 0 || response.Result.ProtocolVersion != 1 || json.Unmarshal(response.Result.AgentInfo, &info) != nil || info.Name == "" || info.Version == "" {
			return nil, fmt.Errorf("unexpected ACP response: %s", lines.Bytes())
		}
		return response.Result.AgentInfo, nil
	}
	if err := lines.Err(); err != nil {
		return nil, fmt.Errorf("read ACP response: %w", err)
	}
	return nil, fmt.Errorf("ACP stream closed before initialization")
}

func check(ctx context.Context, opts options, name string, diagnostics io.Writer) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()
	cmd := launcherCommand(ctx, opts, "run", name, diagnostics)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer in.Close()
	out, writer := io.Pipe()
	defer out.Close()
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		writer.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		err := cmd.Wait()
		writer.CloseWithError(err)
		close(done)
	}()
	defer func() {
		cancel()
		// Release any stdout copy blocked after the initialize response.
		out.Close()
		<-done
	}()
	if _, err := io.WriteString(in, initializeRequest); err != nil {
		return nil, fmt.Errorf("send ACP initialize: %w", err)
	}
	info, err := initialize(out)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ACP initialization: %w", ctx.Err())
	}
	return info, err
}

func run(ctx context.Context, opts options, out, diagnostics io.Writer) error {
	for _, name := range opts.agents {
		if err := prepare(ctx, opts, name, diagnostics); err != nil {
			return fmt.Errorf("%s prepare: %w", name, err)
		}
		info, err := check(ctx, opts, name, diagnostics)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := fmt.Fprintf(out, "PASS %s: ACP initialize %s (no model prompt)\n", name, info); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
		err = run(ctx, opts, os.Stdout, os.Stderr)
		stop()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "check-adapters:", err)
		os.Exit(1)
	}
}
