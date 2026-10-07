# How sandboxes are discovered, reused, and named

See the [README](../README.md) for the quickstart. This page covers the
internals behind `--mode` and automatic reuse.

## Discovery

Normal `run` and `prepare` automatically discover sandboxes using `sbx ls --json`.
A sandbox qualifies when it is running, targets the requested agent, and has a
writable workspace mount containing the selected project directory. Parent
mounts and additional workspace mounts qualify; path matching preserves logical
symlink spelling and respects directory boundaries. The ACP process starts in
the selected project directory, at the same absolute path inside the sandbox.

Externally created sandboxes take priority over managed sandboxes, so a sandbox
created by an earlier fallback does not hide your existing project sandbox.
Within each group, candidates are checked in order of the most specific
covering mount, then sandbox name alphabetically. A more specific read-only
mount blocks reuse of its writable parent. Candidates using clone mode are skipped. Disposable
sandboxes, missing workspaces, mount-policy blocks, and known unresponsive
guests are excluded.

If the selected sandbox lacks the requested executable ACP launcher, the
connection stops with guidance to use `--add-acp-kit`; it does not create or
select another sandbox. With that flag, the launcher adds the configured ACP
kit using `sbx kit add NAME REF`, then verifies the launcher. Kit addition
recreates the sandbox's container and may interrupt running processes;
Docker Sandboxes preserves workspace data and kit-owned
volumes across the swap. Only the ACP kit is added; extra kits and templates
remain creation settings. An existing executable ACP launcher is retained,
even with `--add-acp-kit`; the flag does not force reinstallation.
`prepare` performs this setup without starting an ACP session or model call.
`run` performs the same discovery under the default `--mode=reuse` or under
`--mode=auto`; `--mode=ephemeral` skips it entirely.

When no compatible candidate exists, the launcher creates or resumes its stable
managed sandbox as before. Discovery, connection-check, or kit-add failures
abort without creating another sandbox. The selected name and reuse diagnostics go to stderr;
`prepare` prints only the selected name on stdout. Reuse starts a new ACP session,
not an attachment to an existing agent conversation. ACP runs inside the reused
sandbox for the lifetime of the editor connection.

Externally created sandboxes retain their names and ownership. Automatic reuse
does not add them to `sbx-connect list` or allow `stop`/`remove` outside the reserved
namespace. `--mode=ephemeral` bypasses discovery and always creates its own
disposable sandbox. `--mode=auto` performs this same discovery first and only
creates a disposable sandbox as a fallback when no compatible candidate is
found.

## Naming

Managed names are `sbx-connect-<agent>-<project-folder>-<hash>`, where `project-folder`
is a lowercase sanitized copy of the project directory's basename and the hash is
the first 24 hexadecimal characters of SHA-256 over the cleaned absolute project
path. A valid logical `PWD` and symlink spelling are preserved, and Git roots are
not substituted. Separate worktree paths and separate agents therefore get
separate sandboxes. Renaming a project creates a new identity; old sandboxes
remain until explicitly removed. Paths ending in `:ro` or `:rw` are rejected
because `sbx` interprets them as mount modes.

The prefix and full name pattern are reserved for this tool. Lifecycle commands
refuse names outside that namespace and never prune automatically. Do not give
manually created sandboxes matching names. The original launcher, its
`zed-codex-*` sandboxes, and its sessions are not migrated or modified.

Disposable runs created with `run --mode=ephemeral` (or an `--mode=auto`
fallback) use the same owned namespace with a random per-session suffix. They
are intended for one ACP session and are removed automatically after that
session exits.

## Kit launchers and locking

Kit launchers live under `/home/agent/.local/bin`, outside the shared workspace.
Their `npx` invocations manage adapter downloads and caching inside the sandbox.
`sbx-connect` does not install npm packages or maintain adapter readiness markers.

An OS file lock serializes creation and launcher verification for each sandbox,
waiting at most five minutes. Locks are under the host's Go user cache directory
in `sbx-connect/` (`$XDG_CACHE_HOME` or `~/.cache` on Linux; `~/Library/Caches` on
macOS). Lock files remain on disk, but the kernel releases the lock on process
exit, including SIGKILL. Do not delete a lock file while launchers are running.
The lock is released before ACP starts. Multiple ACP connections can then share
the sandbox; kit-managed npm downloads happen outside this lock. Stopping or
removing the sandbox interrupts those sessions.

The launcher forwards SIGINT, SIGTERM, and SIGHUP to the local `sbx` process group
and returns subprocess exit status (128 + signal number for signal termination).
Remote process shutdown still depends on `sbx`'s connection behavior.

The agent runs inside Docker Sandboxes. Zed terminals, language servers, and tools
delegated back to the editor retain their normal execution behavior. This is not
isolation of the entire editor. Clone mode, remote/cloud projects, additional
mounts, interactive agent terminals, and other sandbox backends are out of scope.
