package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"sbx-connect/internal/agent"
	"sbx-connect/internal/config"
	"sbx-connect/internal/process"
	"sbx-connect/internal/sandbox"
	"sbx-connect/internal/zed"

	"github.com/urfave/cli/v3"
)

type App struct {
	Version  string
	In       io.Reader
	Out, Err io.Writer
}

func cacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "sbx-connect"), nil
}

func (a App) manager(c *cli.Command) (sandbox.Manager, error) {
	binary, err := exec.LookPath(c.String("sbx-bin"))
	if err != nil {
		return sandbox.Manager{}, fmt.Errorf("cannot find the Docker Sandboxes CLI (sbx): install it or set --sbx-bin /absolute/path/to/sbx: %w", err)
	}

	cache, err := cacheDir()
	if err != nil {
		return sandbox.Manager{}, err
	}

	return sandbox.Manager{
		Runner: process.Runner{
			Binary: binary,
			Stdin:  a.In,
			Stdout: a.Out,
			Stderr: a.Err,
		},
		CacheDir: cache,
		Debug:    c.Bool("debug"),
	}, nil
}

func projectFlag() cli.Flag {
	return &cli.StringFlag{
		Name:  "project",
		Value: ".",
		Usage: "project/worktree directory",
	}
}

func provisionFlags() []cli.Flag {
	return []cli.Flag{
		projectFlag(),
		&cli.StringFlag{
			Name:  "kit",
			Usage: "ACP kit reference for new sandboxes or --add-acp-kit (defaults to the agent's published kit)",
		},
		&cli.BoolFlag{
			Name:  "add-acp-kit",
			Usage: "add the ACP kit to a discovered sandbox if its launcher is missing (recreates its container)",
		},
		&cli.StringSliceFlag{
			Name:  "add-kit",
			Usage: "additional kit reference (repeatable; new sandboxes only)",
		},
		&cli.StringFlag{
			Name:  "template",
			Usage: "Docker Sandbox template (new sandboxes only)",
		},
	}
}

func (a App) provision(run bool) cli.ActionFunc {
	return func(ctx context.Context, c *cli.Command) error {
		if c.NArg() < 1 {
			return fmt.Errorf("specify an agent: codex or claude")
		}

		if !run && c.NArg() != 1 {
			return fmt.Errorf("prepare accepts exactly one agent")
		}

		ag, err := agent.Lookup(c.Args().First())
		if err != nil {
			return err
		}

		path, err := sandbox.AbsoluteProject(c.String("project"))
		if err != nil {
			return err
		}
		opts, err := config.Load(path, c.String("config"), ag.Name)
		if err != nil {
			return err
		}
		kit := opts.ACPKit
		if kit == "" {
			kit = ag.Kit
		}
		if c.IsSet("kit") {
			kit = c.String("kit")
		}
		kits := append([]string{kit}, opts.Kits...)
		kits = append(kits, c.StringSlice("add-kit")...)
		if err := config.Validate(kits); err != nil {
			return err
		}
		kits = config.Unique(kits)

		p := sandbox.Project{
			Path:      path,
			Name:      sandbox.Name(ag.Name, path),
			Agent:     ag,
			Kit:       kit,
			ExtraKits: kits[1:],
			Template:  c.String("template"),
			AddACPKit: c.Bool("add-acp-kit"),
		}
		mode := sandbox.ModeReuse
		if run {
			if mode, err = parseMode(c.String("mode")); err != nil {
				return err
			}
		}
		m, err := a.manager(c)
		if err != nil {
			return err
		}
		if run {
			return m.Run(ctx, p, c.Args().Slice()[1:], mode)
		}
		p, err = m.Prepare(ctx, p)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.Out, p.Name)
		return nil
	}
}

func parseMode(s string) (sandbox.Mode, error) {
	switch s {
	case "ephemeral":
		return sandbox.ModeEphemeral, nil
	case "reuse":
		return sandbox.ModeReuse, nil
	case "auto":
		return sandbox.ModeAuto, nil
	default:
		return 0, fmt.Errorf("invalid --mode %q: must be ephemeral, reuse, or auto", s)
	}
}

func noArgs(c *cli.Command) error {
	if c.NArg() != 0 {
		return fmt.Errorf("%s does not accept positional arguments", c.Name)
	}
	return nil
}

func (a App) Command() *cli.Command {
	return &cli.Command{
		Name: "sbx-connect", Usage: "Connect ACP editors to coding agents in Docker Sandboxes", Version: a.Version,
		Reader: a.In, Writer: a.Out, ErrWriter: a.Err,
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Sources: cli.EnvVars("SBX_CONNECT_CONFIG"),
				Usage:   "kit config file (replaces user/project config discovery)",
			},
			&cli.StringFlag{
				Name:    "sbx-bin",
				Value:   "sbx",
				Sources: cli.EnvVars("SBX_CONNECT_SBX_BIN"),
				Usage:   "Docker Sandboxes CLI executable or absolute path",
			},
			&cli.BoolFlag{
				Name:    "debug",
				Sources: cli.EnvVars("SBX_CONNECT_DEBUG"),
				Usage:   "write lifecycle debug logs to stderr",
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			if c.NArg() != 0 {
				return fmt.Errorf("unknown command %q", c.Args().First())
			}
			return cli.ShowAppHelp(c)
		},
		Commands: []*cli.Command{
			{
				Name:                      "run",
				DisableSliceFlagSeparator: true,
				Usage:                     "Connect ACP to an agent in Docker Sandboxes",
				ArgsUsage:                 "AGENT [options] [-- ADAPTER_ARGS...]",
				Flags: append(provisionFlags(),
					&cli.StringFlag{
						Name:  "mode",
						Value: "reuse",
						Usage: "sandbox lifecycle mode: reuse (default; discover a compatible sandbox or create/resume the stable per-project sandbox; never removed automatically), ephemeral (bypasses discovery, always creates a disposable sandbox, removes it after the ACP session exits), auto (reuse a discovered sandbox if one exists; otherwise fall back to a disposable sandbox removed after the ACP session exits)",
					},
				),
				Action: a.provision(true),
			},
			{
				Name:                      "prepare",
				DisableSliceFlagSeparator: true,
				Usage:                     "Prepare a Docker Sandbox without starting ACP",
				ArgsUsage:                 "AGENT [options]",
				Flags:                     provisionFlags(),
				Action:                    a.provision(false),
			},
			{
				Name:  "list",
				Usage: "List Docker Sandboxes managed by sbx-connect",
				Action: func(ctx context.Context, c *cli.Command) error {
					if err := noArgs(c); err != nil {
						return err
					}

					m, err := a.manager(c)
					if err != nil {
						return err
					}

					names, err := m.Names(ctx)
					if err != nil {
						return err
					}

					for _, name := range names {
						fmt.Fprintln(a.Out, name)
					}
					return nil
				},
			},
			a.lifecycle("stop", "stop"),
			a.lifecycle("remove", "rm"),
			{
				Name:   "doctor",
				Usage:  "Check dependencies, service access, and project preconditions without creating sandboxes",
				Flags:  []cli.Flag{projectFlag()},
				Action: a.doctor,
			},
			{Name: "version", Usage: "Print launcher version and default ACP kit references", Action: func(ctx context.Context, c *cli.Command) error {
				if err := noArgs(c); err != nil {
					return err
				}
				fmt.Fprintln(a.Out, "sbx-connect", a.Version)
				for _, ag := range agent.All {
					fmt.Fprintf(a.Out, "%s %s\n", ag.Name, ag.Kit)
				}
				return nil
			}},
			{Name: "install", Usage: "Register an editor", Commands: []*cli.Command{{Name: "zed", Usage: "Register both agents in Zed JSONC settings", Flags: []cli.Flag{&cli.StringFlag{Name: "settings", Usage: "settings.json path"}, &cli.BoolFlag{Name: "print", Usage: "print entries without editing settings"}, &cli.BoolFlag{Name: "ephemeral", Usage: "register disposable entries that remove sandboxes when sessions close"}}, Action: a.installZed}}, Action: func(ctx context.Context, c *cli.Command) error { return fmt.Errorf("use install zed") }},
		},
	}
}

func (a App) lifecycle(name, action string) *cli.Command {
	c := &cli.Command{Name: name, Usage: name + " one sbx-connect sandbox", ArgsUsage: "NAME"}
	if action == "rm" {
		c.Flags = []cli.Flag{&cli.BoolFlag{Name: "yes", Usage: "confirm permanent removal of sandbox-local data"}}
	}
	c.Action = func(ctx context.Context, c *cli.Command) error {
		if c.NArg() != 1 {
			return fmt.Errorf("%s requires exactly one sandbox name", name)
		}
		if action == "rm" && !c.Bool("yes") {
			return fmt.Errorf("removal deletes sandbox-local data; specify NAME and --yes")
		}
		if !sandbox.Owned(c.Args().First()) {
			return fmt.Errorf("%q is not a sbx-connect sandbox name", c.Args().First())
		}
		m, err := a.manager(c)
		if err != nil {
			return err
		}
		return m.Lifecycle(ctx, action, c.Args().First())
	}
	return c
}

func (a App) doctor(ctx context.Context, c *cli.Command) error {
	if err := noArgs(c); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("supported hosts: macOS and Linux")
	}
	path, err := sandbox.AbsoluteProject(c.String("project"))
	if err != nil {
		return err
	}
	// Access checks don't modify the project or create a sandbox.
	if err := syscall.Access(path, 7); err != nil {
		return fmt.Errorf("project must be readable, writable and searchable: %w", err)
	}
	m, err := a.manager(c)
	if err != nil {
		return err
	}
	version, err := m.Runner.Capture(ctx, "version")
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "Host: %s/%s\nsbx: %s (%s)\nProject: %s\n", runtime.GOOS, runtime.GOARCH, m.Runner.Binary, strings.TrimSpace(version), path)
	names, err := m.Names(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, "Sandbox service: accessible")
	for _, ag := range agent.All {
		name := sandbox.Name(ag.Name, path)
		status := "needs prepare"
		for _, n := range names {
			if n == name {
				status = "exists; prepare verifies adapter"
			}
		}
		fmt.Fprintf(a.Out, "%s: %s\n", name, status)
	}
	fmt.Fprintln(a.Out, "Preflight passed. Sandbox kit launchers, adapter runtime, mounts, provider authentication and network policy require prepare/live validation; no sandboxes were started or model calls made.")
	return nil
}

func (a App) installZed(ctx context.Context, c *cli.Command) error {
	if err := noArgs(c); err != nil {
		return err
	}
	executable, err := zed.ResolveExecutable()
	if err != nil {
		return err
	}
	sbx := ""
	if c.IsSet("sbx-bin") {
		sbx, err = exec.LookPath(c.String("sbx-bin"))
		if err != nil {
			return err
		}
		sbx, err = filepath.Abs(sbx)
		if err != nil {
			return err
		}
	}
	configPath := c.String("config")
	if configPath != "" {
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return err
		}
	}
	entries := zed.Entries(executable, sbx, configPath, c.Bool("ephemeral"))
	if c.Bool("print") {
		b, err := zed.Snippet(entries)
		if err != nil {
			return err
		}
		_, err = a.Out.Write(b)
		return err
	}
	path := c.String("settings")
	if path == "" {
		path, err = zed.DefaultPath()
		if err != nil {
			return err
		}
	}
	cache, err := cacheDir()
	if err != nil {
		return err
	}
	r, err := zed.Install(ctx, path, cache, entries)
	if err != nil {
		return err
	}
	if r.Changed {
		fmt.Fprintln(a.Out, "Updated", r.Path)
		if r.Backup != "" {
			fmt.Fprintln(a.Out, "Backup:", r.Backup)
		}
	} else {
		fmt.Fprintln(a.Out, "Already installed:", r.Path)
	}
	return nil
}
