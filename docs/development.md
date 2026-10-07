# Development and release archives

`sbx-connect` is a standalone Go CLI using `github.com/urfave/cli/v3`. It invokes
`sbx` directly, with no additional daemon, Docker SDK integration, or ACP protocol
translation.

```sh
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./... # requires a host C compiler and headers
go build -o sbx-connect .
go run ./scripts/check-adapters --launcher ./sbx-connect --project /path/to/disposable-project
goreleaser check
goreleaser release --snapshot --clean
```

Install the pinned GoReleaser version with `mise install goreleaser` (or install
GoReleaser 2.18.2 separately), and run release commands from a Git checkout.
The smoke check requires live Docker Sandboxes; it creates/reuses sandboxes and
initializes ACP without sending model prompts. The configuration in
`.goreleaser.yaml` builds macOS/Linux amd64/arm64 with cgo disabled and writes
four tar.gz archives plus `dist/SHA256SUMS`. Each archive
contains the binary, README, and validation guide. The binary version comes from
the Git tag, with `-SNAPSHOT` appended for snapshot builds.

Snapshot builds do not publish. To produce release-version archives from an
existing version tag such as `v0.1.0`, use
`goreleaser release --skip=publish --clean`. Publication is separate.
`--clean` replaces the generated `dist` directory.

Archive timestamps use the Git commit date. With the same commit, sources,
Go toolchain, GoReleaser version, and dependencies, repeated builds produce
identical archives. Verify with `sha256sum -c SHA256SUMS` on Linux or
`shasum -a 256 -c SHA256SUMS` on macOS, from inside `dist`.

Sources are separated into `internal/app` (commands), `agent` (flavors/kits),
`config` (user/project kit settings),
`process` (subprocess I/O/signals), `sandbox` (identity/provisioning/lifecycle),
`lock` (OS locks), and `zed` (JSONC settings). Integration tests execute a fake
`sbx` that provisions isolated kit launchers and streams their output through the
real CLI process handling.
