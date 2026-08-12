# Manual testing

Use these recipes for interactive terminal behavior that the Go tests cannot drive through a real TTY.

## Setup

Run from the repository root:

```bash
task setup
lore-dev init
```

`task setup` installs the `lore-dev` shim. It runs the current source through `go run ./cmd/lore`.

Create a disposable project when a recipe needs one:

```bash
mkdir -p /tmp/lore-smoke/demo-project/.codex
git -C /tmp/lore-smoke/demo-project init
printf 'context\n' > /tmp/lore-smoke/demo-project/.codex/AGENTS.md
```

## Gitignore picker

Ensure `.codex/` is not already ignored, then run:

```bash
lore-dev add /tmp/lore-smoke/demo-project
```

Verify each picker choice separately:

- Repository `.gitignore`: the file contains `/.codex/`.
- Global excludes: the configured global ignore file contains `.codex/`.
- `.git/info/exclude`: that file contains `/.codex/`.
- Escape or Ctrl+C: add exits 5 and does not create a central project.

After a successful choice, verify that both copies are real directories:

```bash
test -d /tmp/lore-smoke/demo-project/.codex
test ! -L /tmp/lore-smoke/demo-project/.codex
test -f "$(lore-dev path demo-project)/AGENTS.md"
git -C /tmp/lore-smoke/demo-project status --short
```

The project status must not include `.codex/`.

## Copy reconciliation

Edit the project copy, then capture it:

```bash
printf 'project edit\n' >> /tmp/lore-smoke/demo-project/.codex/AGENTS.md
lore-dev status demo-project
lore-dev push --project demo-project
```

Edit and commit the central copy, then receive it:

```bash
printf 'central edit\n' >> "$(lore-dev path demo-project)/AGENTS.md"
git -C "$(lore-dev path --repo)" add -A
git -C "$(lore-dev path --repo)" commit -m 'test: central edit'
cd /tmp/lore-smoke/demo-project
lore-dev pull demo-project
```

Verify that `lore-dev status demo-project` reports `in-sync`.

## Rename and removal

Rename the central project without moving local files:

```bash
lore-dev mv demo-project demo-renamed
test -d /tmp/lore-smoke/demo-project/.codex
lore-dev ls
```

Default removal must keep the project copy:

```bash
lore-dev rm demo-renamed
test -d /tmp/lore-smoke/demo-project/.codex
```

Repeat setup to test purge. Add an unsynced file and run `lore-dev rm demo-project --purge`. The command must refuse after confirmation. Re-run with `--force` only in the disposable sandbox. The `.codex/` directory must then be absent.

## Doctor repair

With a tracked disposable project, remove one project-copy directory and run:

```bash
lore-dev doctor
lore-dev doctor --fix
lore-dev status demo-project
```

The first command exits 7. The fix restores the missing directory from committed central content. The final status reports `in-sync`.

## Cleanup

Remove the disposable project and any test-only global ignore rule. Do not remove a real central repository or real lore state during cleanup.
