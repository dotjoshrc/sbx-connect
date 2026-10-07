# Troubleshooting

See the [README](../README.md) for the common cases. This page covers the rest.

- **Editor startup timeout:** run `prepare AGENT --project PATH` in a terminal,
  then run the initialization smoke check to populate the adapter cache before
  reconnecting the editor.
- **Missing `sbx`:** install Docker Sandboxes and set `--sbx-bin` or
  `SBX_CONNECT_SBX_BIN` to the CLI's absolute path if needed.
- **Cannot list sandboxes:** inspect `sbx ls --json` (discovery), `sbx ls --quiet`
  (managed lifecycle), host login, and local Docker
  Sandboxes service availability. A listing failure aborts provisioning; it never
  triggers creation.
- **Kit launcher missing:** run `prepare AGENT --add-acp-kit` to add ACP to the
  discovered running sandbox. If adding it fails, inspect stderr and
  `sbx kit add --help`; an older sandbox may predate kit-add support. Preserve sandbox-local data
  before explicitly removing and recreating it. For a custom kit, verify it
  supplies the documented launcher path.
- **Adapter download/start fails:** read stderr and verify template Node/npm
  compatibility, the agent binary, and network policy for registry downloads.
  Retry the connection; the kit manages npm caching.
- **Provider authentication fails:** check host `sbx` secret configuration and
  provider/network access. `doctor` does not validate paid model access.
- **Provisioning lock timeout:** another process may still be creating the
  sandbox. Stop that launcher or let it finish; no stale-lock deletion is needed.
- **Kit/template changes appear ignored:** both affect creation only. Preserve
  needed sandbox-local data, explicitly remove the sandbox with `--yes`, then
  prepare it again with the new kit/template.
- **Zed registration conflict:** inspect the named entry and use `--print` for a
  manual merge. Invalid JSONC and duplicate object keys are rejected without edits.
- **ACP connection issues:** inspect launcher stderr and Zed's **dev: open acp
  logs**. Check that the editor's project path matches the launcher's logical path.
  Use `--debug` or `SBX_CONNECT_DEBUG=1` for detailed lifecycle diagnostics.

`doctor` checks the local OS, project directory permissions, `sbx version`, and
service listing, and reports whether each project's managed sandbox exists. It
does not perform automatic reuse discovery. It cannot
verify a template, sandbox mounts, installed Node/npm, provider credentials, or
network policy without provisioning/live use; it leaves stopped sandboxes stopped.
