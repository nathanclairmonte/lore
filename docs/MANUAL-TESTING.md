# Manual testing

Recipes for the bits of `lore` that aren't exercised by the Go test suite — typically interactive terminal flows that can't be driven from `go test` without a real TTY.

Sections are organised by slice. When a slice's manual procedure stops being relevant (because automated coverage caught up, or the feature changed), retire the section instead of letting it rot.

---

## `lore-dev` and prerequisites

All recipes here use `lore-dev`, the dev-loop shim. It's a tiny script at `$HOME/.local/bin/lore-dev` that runs `go run ./cmd/lore` against the current repo — so every invocation reflects the source tree on disk, no rebuild needed.

**Before any manual testing, run once from the repo root:**

```bash
task setup
```

That installs `lore-dev` (idempotent — re-run after moving the repo). Make sure `$HOME/.local/bin` is on your `$PATH`.

The setup task also warns if `$HOME/.local/bin` isn't on your `$PATH`. Fix that warning before continuing.

---

## Slice 4 — `lore add` gitignore picker

When the agent dir of a project isn't gitignored, `lore add` opens an interactive picker offering three targets (repo `.gitignore`, global excludes, `.git/info/exclude`). All three write paths are unit-tested with a stubbed picker, but the real `huh` form against a real TTY is only exercised by hand.

### Prerequisites

1. `task setup` has been run (see the section above).
2. `lore-dev init` has been run at least once. Accept the default `~/agent-context` or point it at your preferred location — see [Pointing init at a custom path](#pointing-init-at-a-custom-path) below.
3. The default agent dir name used by these recipes (`.codex`) is not already in your global gitignore. Verify with:

   ```bash
   git -C /tmp check-ignore -v .codex
   ```

   Should print nothing. If `.codex` is in your global rules (e.g. `~/.gitignore_global` referenced via `core.excludesFile`, or `~/.config/git/ignore`), pick a different default agent dir that's clean for you and adapt `scripts/smoke-picker-reset.sh` + the verification steps below.

### Pointing init at a custom path

`lore init` interactive support is scoped to slice 14. Until then, the workaround is to pre-create the config file before running `init`:

```bash
mkdir -p ~/.config/lore
cat > ~/.config/lore/config.toml <<EOF
central_repo = "/your/preferred/path/agent-context"
projects_roots = ["~/Canonical/git-repos", "~/PersonalProjects/git-repos"]
projects_root_depth = 2
agent_dirs = [".agents", ".claude", ".codex", ".cursor"]
[commit]
auto = true
add_template     = "feat: add {project}"
rm_template      = "chore: remove {project}"
mv_template      = "refactor: rename {old} to {new}"
sync_template    = "chore: sync ({projects})"
[sync]
auto_push = false
EOF
lore-dev init
```

Running `init` with a config that points at a non-existent directory hits the "configured but central dir missing" branch, asks you to confirm re-creating at the configured path, and writes the central repo there.

### Reset between scenarios

Each scenario assumes a clean sandbox. Reset with:

```bash
task smoke:picker-reset
```

This wipes `/tmp/lore-smoke/demo-project`, the central's copy under `projects/demo-project/`, and lore's state file; then rebuilds a fresh `demo-project` with `.codex/` as the agent dir. The script lives at `scripts/smoke-picker-reset.sh` if you want to read or adapt it.

The script will refuse with a hint if `lore-dev` isn't installed or no central repo exists yet, pointing you back at `task setup` or `lore-dev init`.

### Scenario 1 — pick "repo .gitignore" (default)

```bash
task smoke:picker-reset
lore-dev add /tmp/lore-smoke/demo-project
```

- **Picker:** three options, "Append to repo .gitignore (survives clones)" pre-selected. Press Enter.
- **Verify:**
  - `cat /tmp/lore-smoke/demo-project/.gitignore` is exactly `.codex`
  - `/tmp/lore-smoke/demo-project/.codex` is a symlink into central
  - `git -C /tmp/lore-smoke/demo-project status` shows only `.gitignore` as untracked (NOT `.codex`)
  - Central commit landed: `git -C "$(lore-dev path --repo)" log --oneline | head -1` shows `feat: add demo-project`

### Scenario 2 — pick "global gitignore"

```bash
task smoke:picker-reset
lore-dev add /tmp/lore-smoke/demo-project
```

- **Picker:** arrow-down once, Enter.
- **Verify:**
  - Confirmation line: `✓ added .codex to <your global excludes path>`
  - The global excludes file (whatever `git config --global --get core.excludesFile` returns, or `~/.config/git/ignore` if unset) now ends with `.codex`
  - `cat /tmp/lore-smoke/demo-project/.gitignore` returns "no such file"
  - `git -C /tmp/lore-smoke/demo-project status` shows no untracked entries
- **Cleanup (important — this leaks into your real global gitignore):**
  ```bash
  sed -i '/^\.codex$/d' "$(git config --global --get core.excludesFile)"
  # if core.excludesFile is unset, edit ~/.config/git/ignore instead
  ```

### Scenario 3 — pick `.git/info/exclude`

```bash
task smoke:picker-reset
lore-dev add /tmp/lore-smoke/demo-project
```

- **Picker:** arrow-down twice, Enter.
- **Verify:**
  - Confirmation line: `✓ added .codex to /tmp/lore-smoke/demo-project/.git/info/exclude`
  - `cat /tmp/lore-smoke/demo-project/.git/info/exclude` ends with `.codex`
  - `cat /tmp/lore-smoke/demo-project/.gitignore` returns "no such file"
  - `git -C /tmp/lore-smoke/demo-project status` shows no untracked entries

### Scenario 4 — dismiss with Ctrl+C or ESC

```bash
task smoke:picker-reset
lore-dev add /tmp/lore-smoke/demo-project
# press ESC or Ctrl+C at the picker
echo $?
```

- **Exit code:** `5` (CodeRefusedSafety)
- **Error message:** mentions "must be gitignored"
- **Disk state:**
  - `/tmp/lore-smoke/demo-project/.gitignore` does NOT exist (no stray write)
  - `/tmp/lore-smoke/demo-project/.codex` is still a real directory (not a symlink — lore didn't proceed)
  - No `projects/demo-project/` directory under central
  - No new commit on central's HEAD

### Scenario 5 — non-TTY refuses

```bash
task smoke:picker-reset
lore-dev add /tmp/lore-smoke/demo-project < /dev/null
echo $?
```

- **Exit code:** `7` (CodeInconsistency)
- **Error message:** mentions "stdin is not a terminal"
- **Disk state:** same as scenario 4 (no writes, no symlink, no central state)

### Final cleanup

```bash
rm -rf /tmp/lore-smoke
rm -rf "$(lore-dev path --repo)"
rm -rf ~/.config/lore
rm -rf "${XDG_STATE_HOME:-$HOME/.local/state}/lore"
```

If you ran scenario 2 and didn't clean up your global excludes inline, do it now (see the cleanup snippet in scenario 2).
