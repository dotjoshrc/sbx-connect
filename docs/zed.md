# Zed registration details

See the [README](../README.md) for the quickstart (`sbx-connect install zed`).
This page covers the rest of what `install zed` can do.

## If Zed can't find `sbx`

If a GUI-launched editor cannot find `sbx`, register an explicit path:

```sh
sbx-connect --sbx-bin /absolute/path/to/sbx install zed
```

That setting is recorded in both agents' environment. Re-registering different
entries reports a conflict; rename or remove the old entries, or merge the output
of `sbx-connect install zed --print` manually. Differing entries with the same
names are treated as conflicts, never silently replaced; differently named
entries are retained.

## What gets edited

The installer edits `$XDG_CONFIG_HOME/zed/settings.json`, or
`~/.config/zed/settings.json` by default on both platforms. Use `--settings PATH`
for a different file. It preserves comments, trailing commas, and unrelated
settings. Before replacing an existing file atomically it creates a sibling
`settings.json.backup-*` containing the original bytes, with the original mode.
Repeated installation with identical entries does not write files or backups.
Symlinked settings files retain their symlink. Avoid editing settings concurrently
with installation; detected external changes abort the update.

`--print` emits only the JSON entries and writes no settings or backups:

```sh
sbx-connect install zed --settings /path/to/settings.json --print
```

## Ephemeral and auto entries

`install zed` accepts `--mode`, the same `reuse` (default), `ephemeral`, or
`auto` values as `run --mode` (see [the command reference](../README.md)).
Registering with a non-default mode names the entries after it and bakes the
matching `--mode=` argument into them:

```sh
sbx-connect install zed --mode=ephemeral
```

This adds **Codex in Ephemeral Docker Sandbox** and **Claude in Ephemeral Docker
Sandbox** entries. Each thread creates a separate sandbox and removes it after
the ACP session exits. `--mode=auto` similarly adds **Codex in Auto Docker
Sandbox** and **Claude in Auto Docker Sandbox** entries, reusing a discovered
sandbox if one exists and otherwise falling back to a disposable one. The
default `reuse`-mode entries continue to use stable project sandboxes that
persist across editor sessions, and registering a different mode adds entries
alongside them rather than replacing them.
