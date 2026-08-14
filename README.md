# lore

A CLI for keeping per-project agentic AI context version-controlled in a
single private central repository. Projects keep real local copies. Lore
reconciles those copies with central without exposing central's Git state.

Status: preparing the first release.

## Supported platforms

Linux and macOS on amd64 and arm64. Lore follows the Go 1.25 runtime
baseline: macOS 12 Monterey or newer, and Linux kernel 3.2 or newer. Windows
is not supported natively. Use WSL.

## Install with Go

This method requires Go 1.25 or newer.

```sh
go install github.com/nathanclairmonte/lore/cmd/lore@latest
lore --version
```

## Install a prebuilt binary

Each [GitHub Release](https://github.com/nathanclairmonte/lore/releases)
contains one archive for each supported operating system and architecture.
Download the matching archive and `checksums.txt`. Then verify and install it:

```sh
archive=lore_0.1.0_darwin_arm64.tar.gz
grep " $archive$" checksums.txt | shasum -a 256 -c - &&
tmp="$(mktemp -d)" &&
tar -xzf "$archive" -C "$tmp" &&
mkdir -p "$HOME/.local/bin" &&
install -m 0755 "$tmp/lore" "$HOME/.local/bin/lore" &&
rm -rf "$tmp" &&
"$HOME/.local/bin/lore" --version
```

Replace the archive name with the version, operating system, and architecture
you downloaded. Linux users can use `sha256sum -c -` instead of
`shasum -a 256 -c -`. The archive holds `lore`, `README.md`, and `LICENSE` at
its root, so it is extracted into a temporary directory to keep it from
overwriting files in your current directory. Add `$HOME/.local/bin` to your
`PATH` if it is not there already, so you can run `lore` by name.

GitHub also records build provenance for every archive. If you use the GitHub
CLI, verify it with:

```sh
gh attestation verify "$archive" -R nathanclairmonte/lore
```

The macOS archives are not signed or notarized by Apple. macOS can require you
to approve the downloaded binary before its first run. Use the Go installation
method if you do not want to approve an unsigned download.

Automation can use `lore --version --json`, which emits a stable
`{"version":"<version>"}` envelope.

## Upgrade

For a Go installation, rerun the `go install` command above. For a prebuilt
installation, download the desired newer release, verify it, and replace the
existing `lore` binary.

Lore follows Semantic Versioning. Patch releases remain compatible. Before
`v1.0.0`, minor releases can contain documented breaking changes. Lore will
provide migrations when stored configuration or state formats change.

## Quick start

```
lore init                       # set up the central repository
lore add                        # track the current project's agent directories
lore push --project-from-cwd    # capture local changes in central
```

`lore push` and `lore pull` only reconcile project and central copies. They
never contact a Git remote. Run plain `git push` or `git pull` inside central
for remote operations.

See `lore --help` and the docs for more.

## Development setup

lore is developed with [mise](https://mise.jdx.dev/) for tool-version pinning
and [Task](https://taskfile.dev/) as the command runner.

### One-time setup

```sh
mise install    # installs Go, Task, and golangci-lint at the pinned versions
task setup      # writes ~/.local/bin/lore-dev (the dev wrapper)
```

### Daily loop

Run the in-progress lore from anywhere on disk:

```sh
lore-dev init                   # or any other lore subcommand
```

`lore-dev` always executes the current source via `go run`, so there's no
"is my installed copy up to date?" question.

### Common tasks

```sh
task            # list available tasks
task build      # produce ./bin/lore via go build (exercises the prod compile path)
task test       # run the Go test suite
task lint       # run golangci-lint with the default linter set
task clean      # remove ./bin
task release:check     # validate the GoReleaser configuration
task release:snapshot  # build unpublished release archives in ./dist
```

### Moving the repo

The `lore-dev` wrapper has the repo's absolute path baked in. If you rename a
parent directory or move the repo elsewhere, re-run `task setup` from the new
location to regenerate the wrapper.
