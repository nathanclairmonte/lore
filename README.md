# lore

A CLI for keeping per-project agentic AI context version-controlled in a
single private central repository, symlinked into each project on disk.

Status: early development. See [docs/SPEC.md](docs/SPEC.md) for the
intended design.

## Supported platforms

Linux and macOS. Windows is not supported natively; use WSL.

## Install

```
go install github.com/nathanclairmonte/lore/cmd/lore@latest
```

## Quick start

```
lore init                       # set up the central repository
```

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
```

### Moving the repo

The `lore-dev` wrapper has the repo's absolute path baked in. If you rename a
parent directory or move the repo elsewhere, re-run `task setup` from the new
location to regenerate the wrapper.
