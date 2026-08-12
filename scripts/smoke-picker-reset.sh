#!/usr/bin/env bash
# resets the lore add gitignore-picker smoke sandbox so
# the next `lore-dev add` runs against a fresh, non-tracked demo-project
# with .codex as the agent dir.
#
# kept dumb on purpose: no flags, no overrides. The fixture name
# (demo-project) and agent dir (.codex) are hard-coded. .codex is the only
# default agent dir name unlikely to already match a developer's global
# gitignore; that's what lets the picker actually fire.
set -euo pipefail

SMOKE_DIR=/tmp/lore-smoke
PROJECT="$SMOKE_DIR/demo-project"
STATE_FILE="${XDG_STATE_HOME:-$HOME/.local/state}/lore/state.toml"

# prereq 1: lore-dev must exist. it's installed by `task setup` and is
# the dev-loop shim that runs `go run ./cmd/lore` against the repo's
# current source. without it, this script has nothing to point lore at.
if ! command -v lore-dev >/dev/null 2>&1; then
  echo "error: lore-dev not found on PATH." >&2
  echo "  fix: run 'task setup' from the lore repo to install it," >&2
  echo "       then make sure \$HOME/.local/bin is on your PATH." >&2
  exit 1
fi

# prereq 2: a central repo must exist. lore path --repo always succeeds
# (it just prints the configured or default path), so we don't trust it
# alone. instead, expand the path and check the directory + .git/ are
# really there. that's the same shape `lore add` will check momentarily.
CENTRAL_REPO="$(lore-dev path --repo 2>/dev/null || true)"
# expand a leading ~ since `lore path` prints the contracted form.
case "$CENTRAL_REPO" in
  "~/"*) CENTRAL_REPO="$HOME/${CENTRAL_REPO#~/}" ;;
  "~")   CENTRAL_REPO="$HOME" ;;
esac
if [ -z "$CENTRAL_REPO" ] || [ ! -d "$CENTRAL_REPO/.git" ]; then
  echo "error: no lore central repo found." >&2
  echo "       expected a git repo at: ${CENTRAL_REPO:-(unknown)}" >&2
  echo "  fix: run 'lore-dev init' first to create one." >&2
  exit 1
fi

# wipe disk state. each target can already be absent on a clean run
# (rm -rf and rm -f tolerate missing paths), so the script is idempotent.
rm -rf "$PROJECT"
rm -rf "$CENTRAL_REPO/projects/demo-project"
rm -f "$STATE_FILE"

# rebuild the fixture. git init is silent; the rest mirrors what the
# scenario walkthrough's manual setup block would otherwise paste.
mkdir -p "$SMOKE_DIR"
git init "$PROJECT" >/dev/null
mkdir "$PROJECT/.codex"
echo "test codex md" > "$PROJECT/.codex/CODEX.md"
# blank out .git/info/exclude in case a previous scenario 3 wrote to it
# (the file exists by default with git's instructional comments).
: > "$PROJECT/.git/info/exclude"

echo "reset done: $PROJECT is fresh and .codex is not yet gitignored."
echo "sanity check (should print nothing if .codex is reachable to the picker):"
git -C "$PROJECT" check-ignore -v .codex || true
