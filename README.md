# sbx-connect

Connect ACP editors to coding agents in Docker Sandboxes.

`sbx-connect` brings Docker Sandboxes to Zed and other editors that support the
Agent Client Protocol (ACP). Keep your editor local while Codex or Claude runs
inside a Docker Sandbox, sharing your project files so agent edits appear
immediately in the editor.

- Use Codex or Claude in Docker Sandboxes from your editor's agent panel.
- Share a local project directory so edits are visible on both sides.
- Reconnect to the same sandbox across editor sessions.
- Prepare a sandbox before opening a thread to avoid sandbox creation delays.

**Requires:** an existing Docker Sandboxes installation with `sbx create --kit`
support for v2 kits, `sbx ls --json`, and access to its local service, plus `sbx
kit add` support if you want to add ACP to a sandbox you already created.
Supports macOS and Linux on amd64 and arm64.

**Validation status:** automated launcher tests with fake kit launchers are
covered. Authenticated Docker Sandbox sessions and Zed conversations still need
live validation; see [validation](docs/validation.md).

## Quickstart

1. **Set up Docker Sandboxes** (separately, if you haven't already) and
   authenticate:

   ```sh
   sbx login
   sbx secret set -g openai --oauth
   ```

   For Codex API-key auth use `sbx secret set -g openai` instead; for
   non-interactive Claude auth use `sbx secret set -g anthropic`. See `sbx
   secret set --help` for your installed CLI's options. `sbx-connect` delegates
   provider auth and network policy to Docker Sandboxes — it does not copy host
   credentials, `~/.codex`, `~/.claude`, or agent configuration. If you add
   credentials after creating a sandbox, recreate that sandbox to pick them up.

2. **Install `sbx-connect`**, either via Homebrew once a release is published
   (see [Homebrew releases](docs/homebrew.md)):

   ```sh
   brew install dotjoshrc/tap/sbx-connect
   ```

   or from source (Go 1.26+):

   ```sh
   go install github.com/dotjoshrc/sbx-connect@latest
   sbx-connect version
   ```

   This installs to `$(go env GOPATH)/bin` (`~/go/bin` by default); add it to
   `PATH` if `sbx-connect` isn't found afterward. `go install` doesn't inject a
   release version, so `version` reports `dev`; Homebrew and GoReleaser
   archives carry the real version.

3. **Prepare your project's sandbox** (optional, but avoids first-connection
   delays):

   ```sh
   cd /path/to/project
   sbx-connect doctor
   sbx-connect prepare codex   # or: sbx-connect prepare claude
   ```

   This finds or creates a sandbox for the project and checks that its ACP
   launcher is ready. It does not start an ACP session or call a model.

4. **Connect Zed:**

   ```sh
   sbx-connect install zed
   ```

   Open a project folder in Zed and pick **Codex in Docker Sandbox** or **Claude
   in Docker Sandbox** from the agent panel. Restart Zed if the entries don't
   appear. Both entries default to the agent's full-access mode, leaving Docker
   Sandboxes itself as the permission boundary.

   For an explicit `--sbx-bin` path, `--print`-only output, disposable
   `--mode=ephemeral` entries, or how settings files get edited, see
   [Zed registration details](docs/zed.md).

To connect a different ACP editor instead of Zed, configure a custom stdio agent
pointing at `sbx-connect run AGENT --project PATH` — see
[Connect other ACP editors](#connect-other-acp-editors) below.

## Configuring kits

By default each agent gets its matching published ACP kit
(`docker.io/sbx/codex-acp-kit` or `docker.io/sbx/claude-acp-kit`). To add more
kits — a published kit, a local directory, or a one-off `--add-kit` flag — see
[Configuration](docs/configuration.md), which also covers kit resolution order
and how to update kits on an existing sandbox.

## Connect other ACP editors

Point another ACP editor at `sbx-connect` as a custom stdio agent, using the
launcher's absolute path:

```json
{
  "command": "/absolute/path/to/sbx-connect",
  "args": ["run", "codex", "--project", "/absolute/path/to/project"]
}
```

Replace `codex` with `claude` as needed. Omit `--project` if the editor launches
the process with its project directory as the working directory. stdin/stdout
belong exclusively to ACP; diagnostics go to stderr. Adapter arguments must
follow `--`.

## Command reference

| Command                                                                   | Behavior                                                                                           |
| ------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| `run AGENT [options] [-- ARGS...]`                                        | Provision a sandbox per `--mode` (default `reuse`), then connect ACP over stdio.                   |
| `prepare AGENT [options]`                                                 | Select and verify a running sandbox or prepare a managed one without starting ACP; print its name. |
| `install zed [--settings PATH] [--print] [--mode reuse\|ephemeral\|auto]` | Register both agents using the executable's absolute path.                                         |
| `list`                                                                    | Print only names in this tool's reserved namespace.                                                |
| `stop NAME`                                                               | Stop one existing managed sandbox.                                                                 |
| `remove NAME --yes`                                                       | Permanently remove one managed sandbox and its sandbox-local data.                                 |
| `doctor [--project PATH]`                                                 | Read-only host/service/project preflight; no sandbox starts or model calls.                        |
| `version`                                                                 | Print launcher version and default ACP kit references.                                             |

`AGENT` is `codex` or `claude`. `run` and `prepare` accept:

| Option                | Default and scope                                                                                                    |
| --------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `--project PATH`      | Current directory; one local project root.                                                                           |
| `--kit REF`           | Agent-specific published ACP kit release; used for new sandboxes or with `--add-acp-kit`.                            |
| `--add-acp-kit`       | Add the ACP kit to a discovered running sandbox if its launcher is missing; recreates its container. Default: false. |
| `--add-kit REF`       | Append an extra kit; repeat for multiple kits. Configured kits are also included.                                    |
| `--template TEMPLATE` | The `sbx` agent-specific default; custom templates affect new sandboxes only.                                        |

`run` also accepts `--mode`, selecting how it provisions the sandbox it connects
to:

| Mode              | Behavior                                                                                                                                             |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `reuse` (default) | Discovers a compatible running sandbox or creates/resumes the stable per-project sandbox, same as `prepare`. Never removed automatically.            |
| `ephemeral`       | Bypasses discovery and always creates a new disposable sandbox; removes it after the ACP subprocess exits. Never touches the stable project sandbox. |
| `auto`            | Reuses a compatible running sandbox if found; otherwise falls back to a disposable sandbox, removed after the ACP subprocess exits.                  |

Other global flags: `--sbx-bin PATH` (or `SBX_CONNECT_SBX_BIN`) to point at a
specific `sbx` binary, and `--debug` (or `SBX_CONNECT_DEBUG=1`) for lifecycle
logs on stderr.

For exactly how `sbx-connect` picks, names, and locks sandboxes for reuse, see
[How sandboxes are shared and reused](docs/reuse.md).

## Troubleshooting

- **Editor startup timeout:** run `sbx-connect prepare AGENT --project PATH` in
  a terminal first to populate caches before reconnecting the editor.
- **Missing `sbx`:** install Docker Sandboxes, or set `--sbx-bin` /
  `SBX_CONNECT_SBX_BIN` to its absolute path.
- **Kit launcher missing on an existing sandbox:** run `sbx-connect prepare
  AGENT --add-acp-kit`.
- **Provider authentication fails:** check host `sbx` secret configuration and
  network/provider access.

More scenarios, including lock timeouts, Zed registration conflicts, and
`doctor`'s exact coverage, are in [Troubleshooting](docs/troubleshooting.md).

## Development

```sh
go build -o sbx-connect .
go test -count=1 ./...
go vet ./...
```

See [Development and release archives](docs/development.md) for the full test
matrix, the GoReleaser release process, and the source layout.
