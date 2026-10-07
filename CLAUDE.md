# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`sbx-connect` is a standalone Go CLI (`github.com/urfave/cli/v3`) that connects ACP
(Agent Client Protocol) editors — Zed, or anything else that speaks ACP — to Codex
or Claude running inside a Docker Sandbox. It shells out to the `sbx` CLI directly;
there is no daemon, no Docker SDK integration, and no ACP protocol translation of
its own. It delegates provider auth, network policy, and credentials entirely to
Docker Sandboxes.

## Commands

```sh
go build -o sbx-connect .
go test -count=1 ./...
go vet ./...
mise run lint                                # golangci-lint, pinned in mise.toml
CGO_ENABLED=1 go test -race -count=1 ./...   # requires a host C compiler and headers
go test -run TestName ./internal/<pkg>/...   # single test
go run ./scripts/check-adapters --launcher ./sbx-connect --project /path/to/disposable-project
goreleaser check
goreleaser release --snapshot --clean
```

`scripts/check-adapters` is a live smoke check: it requires real Docker Sandboxes
access, creates/reuses sandboxes, and initializes ACP without sending model prompts.
GoReleaser version is pinned in `mise.toml` (`mise install goreleaser` to match it);
run release commands from a Git checkout. See `docs/development.md` for the full
test matrix and release process, `docs/validation.md` for the live (non-automated)
validation checklist.

## Architecture

Source is split by concern, and most changes touch more than one package:

- `internal/app` — CLI command wiring (`urfave/cli/v3` commands/flags), argument
  parsing and validation (e.g. `parseMode`), and the `install zed` action.
- `internal/agent` — the static list of supported agents (`codex`, `claude`): each
  agent's default ACP kit reference, in-sandbox launcher binary path, and Zed entry
  naming/default permission mode.
- `internal/config` — resolves `~/.config/sbx-connect/config.json` (user) and
  `.sbx-connect.json` (project) kit settings, merges them with CLI `--add-kit`/`--kit`
  flags.
- `internal/sandbox` — the core: sandbox naming/identity (`Name`, `EphemeralName`,
  `Owned`), discovery of reusable sandboxes (`discovery.go`), launcher verification
  (`launcher.go`), and the `Manager` type in `sandbox.go` that drives `Prepare`,
  `Run`, and lifecycle commands (`stop`/`remove`/`list`).
- `internal/lock` — OS file locks (one per sandbox name) serializing creation and
  launcher verification; released before ACP starts so multiple connections can
  share a sandbox.
- `internal/process` — runs `sbx` as a subprocess with no shell/TTY, forwards
  SIGINT/SIGTERM/SIGHUP to its process group, and maps exit/signal status back to
  an exit code.
- `internal/zed` — merges `agent_servers` entries into Zed's JSONC `settings.json`
  without reserializing the rest of the file (preserves comments/formatting);
  backs up before replacing, and treats conflicting existing entries as fatal
  (`--print` for manual merging).

### `run`/`prepare` and `--mode`

`Manager.Run`/`Manager.Prepare` (`internal/sandbox/sandbox.go`) implement three
lifecycle modes (`sandbox.Mode`):

- `reuse` (default) — discover a compatible running sandbox via `sbx ls --json`
  (matches agent + a writable mount covering the project directory), or
  create/resume the stable per-project managed sandbox. Never auto-removed.
- `ephemeral` — skip discovery entirely, always create a disposable sandbox with
  a random-suffixed owned name, remove it after the ACP subprocess exits (even on
  signal, via an uncancelable bounded cleanup context).
- `auto` — reuse mode's discovery first; fall back to ephemeral's disposable
  creation/cleanup only if nothing is found.

Managed sandbox names follow `sbx-connect-<agent>-<project-slug>-<hash>` and are
matched by the `Owned`/`ownedName` regex; lifecycle commands (`stop`/`remove`)
refuse any name outside that reserved namespace. Full details, including mount
precedence/tie-breaking rules and kit-launcher-missing recovery via
`--add-acp-kit`, are in `docs/reuse.md`.

### Install `zed`

`install zed` (`internal/app/app.go` → `internal/zed`) takes the same `--mode`
values as `run` and bakes the resulting `--mode=` argument into the generated
`agent_servers` entry args; non-`reuse` modes get a distinguishing entry name
(e.g. "Codex in Ephemeral Docker Sandbox", "Codex in Auto Docker Sandbox").

## Testing approach

`internal/integration` builds a deliberately strict fake `sbx` binary
(`testdata/fakesbx.go`) and runs the real CLI against it end-to-end — exercising
kit selection, discovery/reuse, locking, and signal/process handling without a
real Docker Sandboxes install. `race_on_test.go`/`race_off_test.go` use
`//go:build race` / `!race` to assert different timing behavior depending on
whether `-race` is active.
