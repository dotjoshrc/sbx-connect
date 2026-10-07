# Homebrew releases

The release workflow publishes GoReleaser archives to GitHub Releases and updates
`Formula/sbx-connect.rb` in a separate Homebrew tap. The formula installs prebuilt
binaries for macOS and Linux on Intel and ARM64. Users do not need Go.
Docker Sandboxes is installed and configured separately.

## One-time setup

1. Host this project in a GitHub repository.
2. Create a public `homebrew-tap` repository under the same owner, initialized
   with a README so it has a default branch.
3. Add the release repository's Actions secret `HOMEBREW_TAP_TOKEN`: a fine-grained
   token with **Contents: read and write** access to the tap repository. The
   workflow's `GITHUB_TOKEN` publishes releases in the source repository; it
   cannot write to a separate tap.
4. For a different tap destination, set the source repository's Actions variables
   `HOMEBREW_TAP_OWNER` and `HOMEBREW_TAP_REPO` (the full repository name, including
   `homebrew-`). Defaults are the source repository owner and `homebrew-tap`.
5. Replace the README's `OWNER/tap` install example with the chosen tap name.

## Publish a stable release

Push a tag such as `v0.1.0` on the commit to release. The workflow accepts stable
`vMAJOR.MINOR.PATCH` tags, runs tests and vet, builds and publishes the four
archives with GoReleaser 2.18.2, then commits the formula to the tap's default
branch. That branch must allow the token to push. Release jobs are serialized.

The generator checks each archive against `dist/SHA256SUMS` before emitting the
formula. Download URLs use the source repository and exact release tag; nothing
depends on a mutable `latest` URL. It avoids GoReleaser's deprecated formula
generator; its replacement casks support only macOS.

After publication, verify on a Homebrew host:

```sh
brew install OWNER/tap/sbx-connect
brew test OWNER/tap/sbx-connect
sbx-connect version
```

The formula test runs `version` without Docker Sandboxes or provider credentials.
Use `brew update && brew upgrade sbx-connect` for subsequent versions.
Zed registration stores the executable's absolute path, which may point into a
versioned Homebrew Cellar directory. After an upgrade, inspect
`sbx-connect install zed --print` and update old entries as described in the
README's Zed registration section.

## Validate packaging without publishing

From a Git checkout tagged with the intended version:

```sh
goreleaser check
goreleaser release --skip=publish --clean
go run ./scripts/homebrew-formula \
  --repository OWNER/sbx-connect --tag v0.1.0 > dist/sbx-connect.rb
ruby -c dist/sbx-connect.rb
```

Use the actual source repository and tag. Snapshot archives are intentionally
rejected. Do not publish a formula until its release assets are downloadable.

If release publication succeeds but updating the tap fails, fix the tap access,
download the four published archives and `SHA256SUMS` into `dist`, run the
generator with that release's repository and tag, and commit the result as
`Formula/sbx-connect.rb` in the tap. This uses the published bytes rather than
rebuilding an existing release.
