# Docker Sandboxes integration validation

Validation covers the connection from an ACP editor through `sbx-connect` to
Codex or Claude in Docker Sandboxes. Local tests exercise the launcher with a
fake `sbx`; live acceptance checks exercise the published ACP kits and an editor.

## Local automated coverage

```sh
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...
```

The race detector requires a host C compiler and headers. The integration suite
also instruments the launcher subprocess when run with `-race`. Tests do not
require `sbx`, Node.js, npm, provider credentials, or model calls. A fake `sbx`
creates isolated kit launchers and executes them over stdio.

Coverage includes matching default kits and launcher paths for both agents,
custom kit references, creation failures and retry, missing kit launchers,
preservation of older sandbox data, reuse without changing kits/templates,
stopped sandbox resume, service-list failure, concurrent launches, paths with
spaces and shell metacharacters, logical symlink paths, namespace restrictions,
lifecycle removal confirmation, read-only doctor, setup stdin disconnection,
byte-for-byte ACP stdio, adapter arguments after `--`, clean stdout, exit codes,
signal forwarding, lock timeouts and process death, and lock release before
streaming. Removed `--adapter-version` flags are rejected before invoking `sbx`.

Automatic reuse checks cover the `sbx ls --json` object/array response, matching
agents and running status, exact/parent/additional mounts, directory boundaries,
read-only mount shadowing, external sandboxes taking priority over managed
fallbacks, deterministic selection within each group by mount specificity and
name, explicit ACP kit addition with `--add-acp-kit`, clone-mode exclusion,
and managed fallback. Missing launchers without the flag report guidance and
never modify a sandbox or start ACP elsewhere. Concurrent connections with
the flag add the kit once. Malformed metadata, failed connection checks,
failed kit additions, and missing launchers
after kit addition never create a sandbox or start ACP.
Borrowed sandbox data and lifecycle ownership are preserved; disposable runs
bypass discovery and disposable candidates are excluded from reuse.

The discovery format was verified against Docker Sandboxes v0.46.0's
[`ls.go`](https://github.com/docker/sandboxes/blob/v0.46.0/cli-plugin/commands/ls.go)
and [`workspaces.go`](https://github.com/docker/sandboxes/blob/v0.46.0/cli-plugin/commands/workspaces.go).
Local workspaces use the same absolute path in the guest. `workspaces` includes
additional mounts and marks read-only entries with `:ro`. Clone mode is checked
inside the guest through `/run/sandbox/source`. Kit addition uses the
[`kit.go`](https://github.com/docker/sandboxes/blob/v0.46.0/cli-plugin/commands/kit.go)
`sbx kit add SANDBOX REFERENCE` command, which recreates the container and
preserves workspace data and kit-owned volumes. Live reuse still needs validation
against an installed `sbx` service.

Zed checks cover comments, trailing commas, existing agents and unrelated settings,
idempotence, conflicts, duplicate keys, file modes, symlink preservation, backups,
custom settings paths, and `--print` without writes.

Kit configuration checks cover user/project list overrides, empty-list clearing,
per-agent ACP overrides, relative paths, explicit config selection, validation
before `sbx`, repeated CLI additions (including commas and shell metacharacters),
deduplication, and the config environment saved by Zed registration.

Recorded for the kit integration on 2026-09-28, using Go 1.26.0 on Linux arm64:

| Check | Result |
| --- | --- |
| Go tests, including fake-sbx kit provisioning and ACP streaming | Passed |
| `go vet` and native build | Passed |
| Go smoke command: successful response, protocol error, EOF, partial-line timeout, graceful shutdown, logical project paths | Passed with local subprocess fixtures |
| Race detection | Blocked: no C compiler installed |
| Real kit provisioning, authenticated sessions, and Zed | Pending: no `sbx` installed |

The workspace exposes no usable Git metadata, so local Go checks used
`GOFLAGS=-buildvcs=false` and writable caches under `/tmp`.

## Live kit initialization smoke check

Build the launcher, configure host provider secrets, and choose a disposable
project directory. This check creates/reuses persistent sandboxes and may download
images, kits, and npm packages. It sends only ACP `initialize`, without a model
prompt. Sandboxes remain afterward for editor testing or explicit removal.

```sh
go build -o sbx-connect .
go run ./scripts/check-adapters --launcher ./sbx-connect --project /path/to/disposable-project
```

Use `--agent codex` or `--agent claude` to check only one agent. `--timeout 120`
sets the time allowed for each prepare/initialize operation, including cold
downloads. Fractional seconds are supported. On timeout, the command sends SIGTERM
to the launcher and allows up to five additional seconds for shutdown before
forcing termination. Project paths preserve logical symlinks, matching the launcher.
No Python or host Node/npm is required. The command reports the adapter's actual
`agentInfo` instead of comparing it against a launcher-owned npm pin.

`prepare` checks that the kit launcher is executable; it does not download the
adapter or validate its runtime. The smoke check starts the real kit launcher,
exercising its `npx` download and ACP initialization. It does not establish that
provider authentication, paid model access, or an editor conversation works.

## Required live acceptance before publication

This implementation environment has no `sbx`. Kit provisioning, authenticated
sessions, and Zed acceptance remain pending on a host with Docker Sandboxes.
Record OS/architecture, `sbx version`, template/image identity, launcher version,
kit reference, actual adapter version, and results. Default kit tags are
`20260924-d058fedc156325f87612d9bcd9bd313ab74ba100`; the kit sources specify Codex
ACP `1.1.0` and Claude Agent ACP `0.51.0`, replacing the older launcher's npm pins.

1. Install Docker Sandboxes with v2 kit support. Configure host OpenAI and
   Anthropic secrets before creating sandboxes, plus appropriate network policy.
   Choose a disposable project whose absolute path contains spaces. Run `doctor`
   and verify that it has not created or started a sandbox.
2. For each agent, run `prepare AGENT --project PATH`. Verify that creation uses
   the matching ACP kit and built-in agent, the project is shared at the same
   absolute path, and the documented kit launcher is executable. Verify no
   adapter files appear in the project.
3. Run the initialization smoke check above. Verify ACP-only stdout, reported
   adapter versions, first-run downloads, and clean local and remote process
   shutdown after the launcher receives SIGTERM. Remote shutdown depends on `sbx`.
4. Register Zed with the installed binary and correct `sbx` path. Open the project,
   select each agent, and complete an authenticated conversation. This step may
   incur provider charges.
5. Ask each agent to create a small uniquely named file in the disposable project.
   Verify its content from the host and Zed. Edit it on the host and ask the agent
   to read the change.
6. Close and reopen the connection. Confirm the sandbox name and kit are unchanged
   and another conversation works. Session history behavior belongs to the adapter;
   this launcher promises sandbox reuse, not session migration.
7. Stop the sandbox, then reconnect and confirm automatic resume. Confirm changed
   config files, `--kit`, `--add-kit`, and `--template` options do not alter an existing sandbox. A custom kit
   used on a new sandbox must supply the matching documented launcher path.
   On a new disposable project, configure common and agent-specific extra kits;
   verify their tools are available after creation. Repeat with `--add-kit` and
   with a config path saved using `--config PATH install zed`.
8. Try an older managed sandbox without a kit launcher. Verify `prepare`/`run`
   fail with recovery guidance and preserve its files. After preserving needed
   sandbox-local data, explicitly remove it and prepare it again with the kit.
9. Open a second worktree with no existing covering mount and confirm it receives
   a distinct sandbox. Check `list`,
   then explicitly remove only disposable sandboxes with `remove NAME --yes`.
   Confirm unrelated and original `zed-codex-*` sandboxes remain.
10. Separately create a running sandbox for each agent with its ACP kit and a
    parent directory mounted. Run `prepare` from a project subdirectory and
    confirm it prints that existing sandbox's name. Run the initialization smoke
    check and verify the ACP working directory is the selected subdirectory and
    host edits are shared. Add a second candidate with an exact project mount,
    then another with the same mount; verify closest-mount and alphabetical-name
    selection. Verify an additional writable mount qualifies, a read-only mount
    and clone mode do not. Create another running sandbox without the ACP kit;
    also leave a managed sandbox with an exact project mount running. Verify
    `prepare` prefers the external sandbox and reports `--add-acp-kit` guidance
    without modifying it or creating another sandbox. Run `prepare AGENT --add-acp-kit`
    to add the kit there and verify subsequent `run` without the flag starts
    ACP there without creating a managed sandbox or reinstalling the kit.
    Confirm kit-add or launcher-verification failure aborts without creating
    another sandbox. Close the connection and confirm externally created
    sandboxes retain their data and stay outside managed lifecycle commands.
    Run `run AGENT --mode=ephemeral` and confirm only its new disposable sandbox
    is removed.

Run live checks on both supported host operating systems before claiming full
macOS/Linux compatibility. Publication is a separate action.

## Release checks

Run `goreleaser check`, then `goreleaser release --snapshot --clean` in a Git
checkout to cross-build Darwin/Linux on amd64 and arm64 without publishing.
GoReleaser 2.18.2 is pinned in `mise.toml`. Save `dist/SHA256SUMS`, repeat the
snapshot build at the same commit, and compare the sorted checksum files to check
archive repeatability. Verify archive contents and run the native binary's
`version` command to check version injection and default kit references.
Cross-compilation is not runtime validation on each host.
