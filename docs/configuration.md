# Configuration reference

Full detail on kit configuration, config file resolution, and modifying an
existing sandbox's kits. See the [README](../README.md) for a quickstart.

## Config files

Define extra kits in `~/.config/sbx-connect/config.json` (or
`$XDG_CONFIG_HOME/sbx-connect/config.json`) for all your projects. Use
`.sbx-connect.json` in a project's root for project settings. Both `run` and
`prepare` load these files automatically, including when launched by Zed.

```json
{
  "kits": [
    "docker.io/sbx/neovim-kit:latest"
  ],
  "agents": {
    "codex": {
      "kits": ["./kits/project-tools"]
    },
    "claude": {
      "kits": []
    }
  }
}
```

`kits` applies to both agents; `agents.codex.kits` and `agents.claude.kits` add
agent-specific kits. The matching ACP kit is always included first. For the
example above, create `./kits/project-tools` as a valid local mixin or replace it
with a published kit reference. References can be OCI artifacts, Git URLs, local
directories, or ZIPs supported by `sbx`. Prefer dated tags or digests when you
want to control updates.

Settings are resolved as follows:

1. Read the user config, then `.sbx-connect.json` directly in the selected
   `--project` directory (or current directory). Parent directories are not searched.
2. A project `kits` list replaces the user `kits` list. Each project's agent list
   separately replaces the corresponding user agent list. Omitted lists inherit;
   `[]` clears the corresponding list.
3. Combine the ACP kit, common kits, agent-specific kits, and CLI additions in
   that order. Exact duplicate references are passed only once.

Paths beginning with `./` or `../` in config files resolve relative to that
file's directory. Other references are passed unchanged. JSON must use the
documented fields and supported agent names; invalid files stop launch before
invoking `sbx`.

For a one-off addition, repeat `--add-kit`:

```sh
sbx-connect prepare codex \
  --add-kit docker.io/sbx/neovim-kit:latest \
  --add-kit ./kits/project-tools
```

CLI additions are appended to configured kits. Each flag supplies one reference;
commas are preserved. Relative CLI paths resolve from the invoking directory.
`--kit REF` continues to replace the ACP kit specifically. You can also set
`agents.codex.acp_kit` or `agents.claude.acp_kit` in a config file; the project
value overrides the user value, and `--kit` overrides both.

To use a single config instead of automatic user/project discovery, set the global
`--config PATH` option or `SBX_CONNECT_CONFIG`. The flag takes precedence over
the environment variable, and an explicitly selected file must exist. Register
it with Zed to save its absolute path into both agent entries:

```sh
sbx-connect --config /path/to/kits.json install zed
```

## Changing kits on an existing sandbox

Additional kit settings apply when creating a sandbox. With `--add-acp-kit`,
the ACP kit also applies when a discovered running sandbox lacks its ACP
launcher. Editing a config or changing flags does not replace an existing ACP
launcher or update other kits.
Preserve needed sandbox-local data, remove that sandbox explicitly using its
owning tool, then prepare it again to use the new kits.

## Default ACP kits

| Agent / sandbox flavor | Default kit repository | Kit launcher |
| --- | --- | --- |
| `codex` | [`docker.io/sbx/codex-acp-kit`](https://hub.docker.com/r/sbx/codex-acp-kit) | `/home/agent/.local/bin/codex-acp` |
| `claude` | [`docker.io/sbx/claude-acp-kit`](https://hub.docker.com/r/sbx/claude-acp-kit) | `/home/agent/.local/bin/claude-acp` |

Both defaults use the published tag
`20260924-d058fedc156325f87612d9bcd9bd313ab74ba100`. The kit release owns the npm
adapter version; this release uses Codex ACP `1.1.0` and Claude Agent ACP `0.51.0`.
These differ from the older launcher's pins. Dated tags avoid following `latest`
by default, but tags, transitive npm dependencies, and base images are not a
promise of fully reproducible environments.

Use `--kit REF` to select another compatible ACP kit release when creating a sandbox
or adding ACP to a discovered running sandbox with `--add-acp-kit`.
The launcher checks the sandbox's `PATH` for `{agent}-acp` (such as `codex-acp`),
then `acp`, then falls back to the kit launcher path above. It starts the resolved
executable for the ACP session. Custom kits can supply any of these commands. The old
`--adapter-version` option has been removed. Changing a kit reference or upgrading
this CLI does not replace an existing ACP launcher. `version` reports defaults,
not the kit or adapter installed in existing sandboxes.

Sandboxes created by the previous npm-based launcher lack the kit launcher.
When such a sandbox is running and discovered for the selected project,
`prepare` and `run` report the missing launcher. Use `--add-acp-kit` to add the
ACP kit through `sbx kit add`. Older sandboxes that predate kit-add support may
be refused by `sbx`; failures stop the connection
and report diagnostics. Stopped managed sandboxes still require their launcher
to be present. Preserve needed sandbox-local data before explicitly removing
and recreating a sandbox that cannot accept the kit.
