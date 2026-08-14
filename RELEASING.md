# Releasing Lore

Lore releases use annotated Semantic Versioning tags. A matching tag starts a
GitHub Actions workflow that builds four archives, runs each archive on its
native architecture, records provenance, and publishes the release.

The first planned release is `v0.1.0`. Homebrew distribution, Apple signing,
Apple notarization, Cosign signatures, and SBOMs are deferred.

## Release outputs

Each release contains:

- macOS amd64 and arm64 archives;
- Linux amd64 and arm64 archives;
- `checksums.txt` with SHA-256 checksums;
- GitHub build provenance attestations;
- GitHub-native generated release notes.

The archives contain `lore`, `README.md`, and `LICENSE`.

## Toolchain

`mise.toml` pins the Go and GoReleaser versions, and the workflows pin the same
versions so local and CI builds use one toolchain. GitHub Actions are pinned to
full commit SHAs. Dependabot proposes monthly Actions updates.

Change a pinned version only in a reviewed pull request. Run the full release
check after every toolchain update.

## GitHub configuration

The release workflow uses the repository's automatic `GITHUB_TOKEN`. It does
not need a custom secret.

The jobs declare only these write permissions:

- `contents: write` to create the draft and publish the release;
- `id-token: write` to request the provenance identity token;
- `attestations: write` to store build provenance.

Enable immutable releases in the GitHub repository settings before publishing
`v0.1.0`. Published tags and assets must not change. Publish a new patch version
for every correction.

## Validate locally

Start from a clean checkout of `main` with the pinned mise tools installed:

```sh
mise install
task check
task release:snapshot
```

Inspect the four archives and `dist/checksums.txt`. A snapshot never creates a
Git tag or GitHub Release.

## Publish

Confirm that `main` contains the intended release commit and that the release
check workflow passed. Creating and pushing the tag is the publication
decision.

For `v0.1.0`, refresh the remote references, create an annotated tag, and
validate it before pushing:

```sh
git fetch origin main --tags
git tag -a v0.1.0 -m "v0.1.0"
task release:validate-tag TAG=v0.1.0
git push origin v0.1.0
```

The workflow rejects malformed versions, lightweight tags, and commits that
are not reachable from `main`. It then performs these steps:

1. Run the Go tests and validate the GoReleaser configuration.
2. Build a draft GitHub Release with all archives and checksums.
3. Execute each packaged binary on its native operating system and architecture.
4. Create GitHub provenance attestations from `checksums.txt`.
5. Publish the draft only after every preceding step succeeds.

If validation fails, the workflow leaves the release unpublished. Review the
draft and workflow logs before deciding how to recover. Never replace or move
a published release tag.

## Verify a published release

Follow the checksum and provenance steps in the README's "Install a prebuilt
binary" section, then confirm the stamped version:

```sh
./lore --version
```

The expected first-release output is `lore version 0.1.0`. A published archive
that reports `devel` means the release build lost its version ldflag.
