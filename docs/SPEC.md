# lore: Specification

> A CLI for keeping per-project agentic AI context (`.claude/`, `.agents/`, etc.) version-controlled in one private central repository, with real copies in each checkout.

---

## Table of contents

1. [Origin and problem statement](#origin-and-problem-statement)
2. [Goals and non-goals](#goals-and-non-goals)
3. [Mental model](#mental-model)
4. [Naming](#naming)
5. [Central repository structure](#central-repository-structure)
6. [Configuration file](#configuration-file)
7. [Local state file](#local-state-file)
8. [Command reference](#command-reference)
9. [Output design principles](#output-design-principles)
10. [Agent-friendliness](#agent-friendliness)
11. [Implementation notes](#implementation-notes)
12. [Future work](#future-work)
13. [Open design questions](#open-design-questions)
14. [Glossary](#glossary)

---

## Origin and problem statement

Many developers maintain per-project agentic AI context — a `.claude/` directory (or `.agents/`, `.cursor/`, etc.) containing `CLAUDE.md`, prompts, examples, project lore, scratch notes, and accumulated rules. These directories are often globally gitignored because the contents are personal and not appropriate for team-shared org repos, but they're also valuable knowledge that should survive laptop loss, machine migrations, and accidental deletion.

The problem: how do you version-control personal per-project agentic AI context **without** committing it to the project's main repository?

The naive solutions all have rough edges:

- **Commit to the main repo:** leaks personal config to the team.
- **Globally gitignore and hope:** loses everything on data loss.
- **Nested git repo per project:** works for one or two projects, gets unwieldy at ten-plus.
- **Manual dotfiles-style symlinks:** works but is error-prone and tedious to onboard new projects.
- **chezmoi or similar:** powerful, but mostly solves a different problem (per-machine config templating) and the symlink-only use case is a small slice of its surface area.

`lore` is a small, focused CLI for exactly this workflow: a single private central repository stores all your per-project agentic AI context, organized by project, and symlinked into each project on disk. Each project sees its agent directory as if it were local; the source of truth lives in one git repo that's easy to back up, sync across machines, and recover from.

The name comes from the metaphor: the unwritten knowledge about how a codebase actually works — its quirks, conventions, history, gotchas — is the project's *lore*. `lore add`, `lore list`, `lore status` all read as natural English.

---

## Goals and non-goals

### Goals

- Let users keep per-project agentic AI context out of the main project repo while still version-controlling it privately.
- Make onboarding a new project trivially fast (one command from inside the project directory).
- Make recovery on a new machine a single command after cloning the central repo.
- Be tool-agnostic about which agent directory name a project uses (`.claude`, `.agents`, `.cursor`, `.aider`, anything else).
- Be agent-friendly: a well-designed CLI with structured output, stable exit codes, and clear errors that an agent can read and act on.
- Stay small. Wrap git only where wrapping is genuinely useful for automation.

### Non-goals

- Not a git frontend. The central repo is a normal git repo and users use `git` directly for `push`, `pull`, branching, history, etc.
- Not a dotfiles manager. Adjacent problem, different tool. If you want machine-specific templating or encrypted secrets, use chezmoi or sops.
- Not a knowledge-base search engine (yet). See [future work](#future-work).
- Not a network/cloud service. Distribution is via git only.
- Not opinionated about the contents of agent directories. `lore` moves and symlinks them; what's inside is the user's business.

---

## Mental model

`lore` operates with three distinct layers of state:

| Layer | Location | Purpose | Synced across machines? |
|---|---|---|---|
| **Central repository** | `~/agent-context` (configurable) | Source of truth for tracked projects and their contents | Yes, via git |
| **Configuration** | `~/.config/lore/config.toml` | User preferences (paths, behavior, templates) | User's choice (often via dotfiles) |
| **Local state** | `~/.local/state/lore/state.toml` | Per-machine checkout paths and sync-bases | No, rebuilt by `lore pull` |

This separation is load-bearing. The central repository is portable and machine-agnostic. Configuration captures preferences. Local state records each checkout on this machine and the central commit it last matched. `lore pull` rebuilds this state when necessary.

---

## Naming

- **CLI binary:** `lore`
- **Repository default name:** `agent-context` (user-overridable)
- **Default agent dir candidates** (auto-detected at `lore add` time): `.agents`, `.claude`, `.codex`, `.cursor`

---

## Central repository structure

```
agent-context/
├── README.md
├── projects/
│   ├── my-nextjs-app/
│   │   └── .claude/
│   │       ├── CLAUDE.md
│   │       └── ...
│   ├── trading-bot/
│   │   └── .claude/
│   ├── docs-site/
│   │   └── .agents/             ← different agent dir name, no problem
│   │       └── AGENTS.md
│   └── ...
└── .lore/                        ← optional: repo-level lore metadata (reserved)
```

Notes:

- Each project subdirectory under `projects/` contains the entire agent directory verbatim, including its leading dot. This is the central copy.
- The `.lore/` directory at the repo root is reserved for future repo-level metadata (e.g., schema versioning).

---

## Configuration file

Location: `~/.config/lore/config.toml` (follows XDG Base Directory spec).

```toml
# Path to the central agent-context repository.
central_repo = "~/agent-context"

# Roots that `lore link --all` scans for project checkouts.
# Supports multiple roots for developers whose projects live in
# more than one parent directory.
projects_roots = [
  "~/Canonical/git-repos",
  "~/PersonalProjects/git-repos",
]

# How deep to recurse under each root when scanning.
projects_root_depth = 2

# Agent directory names to auto-detect during `lore add`.
# All detected dirs are tracked together; pass --dir to scope a run.
# Order drives the interactive picker for `lore add --create`.
agent_dirs = [".agents", ".claude", ".codex", ".cursor"]

# Commit behaviour for lore's own operations.
[commit]
auto = true
add_template     = "feat: add {project}"
rm_template      = "chore: remove {project}"
mv_template      = "refactor: rename {old} to {new}"
sync_template    = "chore: sync ({projects})"

```

Template variables: `{project}`, `{projects}` (comma-separated list), `{old}`, `{new}`.

Default style for commit messages: lowercase conventional-commits format.

---

## Local state file

Location: `~/.local/state/lore/state.toml`.

This file is **not** committed anywhere. It is per-machine. Each checkout has its own sync-base.

```toml
# Schema version for forward compatibility.
schema_version = 3

[projects.my-nextjs-app]
agent_dirs = [".claude", ".cursor"]

[[projects.my-nextjs-app.checkouts]]
path = "/home/nathan/Canonical/git-repos/my-nextjs-app"
sync_base = "0123456789abcdef0123456789abcdef01234567"

[[projects.my-nextjs-app.checkouts]]
path = "/home/nathan/worktrees/my-nextjs-app-feature"
sync_base = "89abcdef0123456789abcdef0123456789abcdef"
```

Schema v3 replaces the existing v2 layout. Version 2 already stored each project's agent-directory set, so the copy-model state could not safely reuse that version number. Lore rejects older schemas and does not migrate them. To start fresh, delete the incompatible state file and any legacy project symlinks, then run `lore pull --all` to rebuild current state from central and the configured project roots.

---

## Command reference

### Conventions

- Every command supports `--help`.
- Every command that returns data supports `--json` for structured output.
- Every command is **idempotent**: safe to re-run.
- Exit codes are **global and semantic** — the same code means the same *class* of failure across every subcommand. The exact cause goes in the stderr message; the code lets scripts and agents decide how to react.
- Commands that mutate state always echo what they did, and (where applicable) what the user should do next.

#### Exit code table

| Code | Class | Examples |
|---|---|---|
| 0 | Success | All good, possibly a no-op |
| 1 | Generic error | Catch-all for unexpected failures |
| 2 | Setup missing | No central repo, no config, lore not initialized |
| 3 | Not found | Project not tracked, checkout doesn't exist, agent dir absent, cwd not inside a tracked project |
| 4 | Conflict / already exists | Project already tracked, name collision, symlink at expected location pointing elsewhere |
| 5 | Refused for safety | Agent dir contains tracked files, agent dir is a symlink, nested `.git` present, gitignore picker dismissed |
| 6 | Git / network failure | Central is behind remote (refused push), `git` operation failed, clone failed |
| 7 | Inconsistency / unhealthy | `lore doctor` found unfixed problems, `lore status` found divergence or unhealthy copy state, broken config (e.g. empty `central_repo`), interactive prompt required but stdin is not a terminal |

### `lore init`

Create or clone the central repository. Wizard-driven by default; pass any flag to skip the wizard and run non-interactively.

```
lore init                                                # interactive wizard (default)
lore init --path ~/agent-context                         # non-interactive, fresh init at path
lore init --from git@github.com:you/agent-context.git    # non-interactive, clone from remote
lore init --from <url> --path ~/agent-context            # non-interactive, clone to specific path
lore init --remote git@github.com:you/agent-context.git  # non-interactive, fresh init + add remote
lore init --no-interactive                               # use all defaults, no prompts
```

**Wizard behavior (default `lore init` with no flags):**

1. **Central repo path.** Prompt for location. Smart default:
   - If cwd looks like an existing lore central repo (has `projects/` directory and `.git`) → pre-fill cwd. This covers the "I cloned my agent-context manually first" and "my config got deleted but the repo's still here" cases.
   - Otherwise → default `~/agent-context`.
2. **Clone or init.** Ask whether to clone from an existing remote (skip prompt and skip clone if cwd was pre-filled as an existing repo).
3. **Remote URL** (if not cloning). Optional; can be added later via `git remote add`.
4. **Confirm and execute.**

**Non-interactive behavior (any flag passed, or `--no-interactive`):**

- Without `--from`: `git init` a new repo at `central_repo` (from `--path` or config default), write a default `README.md` plus an empty `projects/.gitkeep`, make a single bootstrap commit (`chore: initialize lore central repository`, authored as `lore <lore@localhost>` with gpg signing disabled so users without a configured git identity or signing key aren't blocked from the very first command), then write `~/.config/lore/config.toml`. If any step after the directory is created fails, the directory is removed so a retry sees a clean slate.
- With `--from`: `git clone` the remote to `central_repo`. Write the config.
- If `--path <path>` is passed and `<path>` already contains a git repo, **adopt** it rather than re-initializing. Lets users point lore at a manually-prepared repo.
- If `--path` is passed, **update `central_repo` in config to the new path** (even if a previous central repo was configured elsewhere). The previous repo on disk is left untouched; switch back later via `lore use <old-path>` or another `lore init --path`.
- If a config already exists AND no `--path` is passed AND the configured central repo dir already exists → refuse with `*central repo already configured at <path>. Use `lore init --path <new-path>` to switch, `lore use <existing-path>` to point lore at an existing repo, or remove the directory first.*` (Exit 4 — conflict.)

After init, if no remote is set, end output with:

```
⚠ No remote configured. Set one with:
    cd $(lore path --repo) && git remote add origin <url>
```

`lore doctor` also flags missing remotes as a warning. lore does not wrap `git remote add` — that's git's job.

Alias: `lore clone <url>` is equivalent to `lore init --from <url>`.

### `lore use`

Point lore at an existing central repo without initializing anything. Just updates `central_repo` in config.

```
lore use ~/agent-context-2
```

Behavior:

- Validates that the path exists, is a git repo, and looks like a lore central repo (has `projects/` directory).
- If valid: updates `central_repo` in `~/.config/lore/config.toml`, prints confirmation showing old → new.
- If not valid: errors with `*<path> doesn't look like a lore central repo (no projects/ directory found). Use `lore init --path <path>` to initialize a new one.*` (Exit 3 — not found.)

Useful for: switching between central repos you've already created, recovering from a deleted config when the repo is still on disk, pointing lore at a central repo you migrated to a different drive.

### `lore add`

Onboard a project. Copy its agent directories into central. Keep the original directories as real project copies.

```
lore add                              # from cwd, auto-detect everything
lore add ~/code/my-app                # explicit project path
lore add --dir .agents                # explicit agent dir name (skip selector)
lore add --name foo-work              # override project name
lore add --create                     # scaffold an empty agent dir if none exists
```

Detection chain:

1. **Project path:** cwd unless overridden positionally.
2. **Project name:** basename of project path unless overridden with `--name`. Both paths run through a strict-slug validator (letters, digits, dots, dashes; must start with a letter or digit) before any work happens, so an invalid name fails fast rather than landing in central state. If `--name` resolves to a project already in local state at a different path, or to a `central/projects/<name>/` directory that local state doesn't know about, lore exits 4 with a message suggesting `--name <alternative>` (and, in the latter case, `lore use <name>` to adopt the orphan).
3. **Agent dir:** iterate `agent_dirs` from config and collect every one that exists at the project root. If multiple exist, all of them are added by default; pass `--dir` (repeatable) to scope to a subset. If none exists and `--create` is set, scaffold the agent dir. With `--dir`, each named dir that doesn't already exist is created with an empty `AGENTS.md` inside (silent default; no prompts fire in scripted mode). Without `--dir`, an interactive picker offers the configured agent dirs (plus a free-text "other") and then a filename picker (`AGENTS.md` default, `CLAUDE.md`, free-text); non-TTY or `--json` mode requires `--dir` and exits 7 otherwise. Existing dirs are left untouched (idempotent skip). The starter file is intentionally empty; users own the content.
4. **Tracked-file safety check:** for each agent directory being added, refuse if any file inside is currently tracked by the project's Git repository. The project copy must stay personal and ignored. The error tells the user how to recover, for example `git rm --cached -r .claude && git commit`, then retry.
5. **Gitignore check:** run `git check-ignore -v` on each agent-directory path. If Git does not ignore it, present an interactive picker with three options: append to the repository `.gitignore`, append to the global Git ignore file, or append to `.git/info/exclude`. Write directory-only patterns such as `/.claude/`. Refuse to continue silently when the directory is not ignored.

Operation:

1. Copy each selected agent directory into a temporary sibling inside central.
2. Reject sockets, FIFOs, devices, and other special files.
3. Preserve regular-file contents, executable permissions, directory structure, and nested symlink targets.
4. Rename each complete temporary copy into `projects/<name>/<agent-dir>/`.
5. Leave the original project copy in place as a real directory.
6. Stage and commit only the new central paths using `commit.add_template`.
7. Record the resulting central commit SHA as this checkout's sync-base.

Git does not version empty directories. Lore does not inject marker files. Empty directories can exist in either working tree, but an empty directory cannot survive a clone unless it later contains a versioned file.

Add always commits. It does not support `--no-commit`, and `commit.auto` does not disable the onboarding commit. The commit is required because the sync-base must identify a real central commit.

Re-entry and recovery:

- Re-running add for an already-recorded checkout is a no-op. Add does not interpret ordinary content changes. `lore status`, `lore push`, and `lore pull` own reconciliation.
- If local state is missing but every centrally tracked agent directory exists in the checkout and matches central, add records the checkout again without creating a commit.
- Add compares relative paths, object types, file contents, executable permissions, and nested symlink targets. It ignores timestamps, ownership, ACLs, and extended attributes.
- If an existing copy differs from central, add refuses and directs the user to `lore pull`.
- Before adding another agent directory to a recorded checkout, add requires every existing tracked agent directory to match central. This keeps the single checkout-wide sync-base truthful.
- If the central commit succeeds but local-state persistence fails, add keeps the backup commit and reports an inconsistency. A retry adopts the identical copies safely.

Happy-path output:

```
$ cd ~/code/my-nextjs-app
$ lore add
✓ Project:   my-nextjs-app (~/code/my-nextjs-app)
✓ Agent dir: .claude
✓ Globally gitignored: yes

→ Copied .claude  →  ~/agent-context/projects/my-nextjs-app/.claude
✓ Committed to central repo: "feat: add my-nextjs-app"
✓ Recorded sync-base: 0123456789abcdef0123456789abcdef01234567

Next: cd ~/agent-context && git push
```

Failure modes (each with a distinct exit code):

- Central repo not initialized → exit with clear message pointing at `lore init`.
- Project already tracked at this checkout: idempotent no-op with friendly status.
- A recorded project copy is missing or is not a real directory: exit 7 and suggest `lore pull`.
- The top-level agent directory is a symlink: exit 5 and require a real directory.
- An agent directory contains an unsupported special file: exit 5 without publishing a partial central copy.
- Name collision with an existing tracked project → suggest `--name <alternative>`.
- Name collision with a central project whose checkout copy differs: exit 4 and suggest `lore pull`.
- No agent directory found and `--create` not passed → error.
- Agent dir contains files tracked by the project's git repo → refuse with recovery instructions (see detection step 4).

JSON result:

```json
{
  "project": {
    "name": "my-nextjs-app",
    "path": "/home/nathan/code/my-nextjs-app"
  },
  "central_repo": "/home/nathan/agent-context",
  "commit": {
    "sha": "0123456789abcdef0123456789abcdef01234567",
    "message": "feat: add my-nextjs-app"
  },
  "sync_base": "0123456789abcdef0123456789abcdef01234567",
  "scaffold_requested": false,
  "dirs": [
    {
      "name": ".claude",
      "status": "added",
      "central_path": "/home/nathan/agent-context/projects/my-nextjs-app/.claude",
      "safety_violations": []
    }
  ]
}
```

`commit` is `null` when add adopts existing identical copies. `central_path` is `null` for a directory that add skips for safety. Directory status is `added`, `scaffolded`, `already-tracked`, `adopted`, or `skipped-safety`.

### `lore link`

Create symlinks from project checkouts to their central-repo agent directories. Used after `lore clone` on a new machine, or any time a project's symlink is missing.

```
lore link                            # link the tracked project at cwd (errors if ambiguous)
lore link --all                      # scan projects_roots, link everything found
lore link my-app                     # link a single tracked project (uses local state)
lore link my-app --path ~/elsewhere  # link with explicit path (also records to state)
```

`lore link` (no args) resolves the project by looking at cwd: matches against tracked project names by basename, or by checking cwd ancestry against local state's recorded paths. Errors with a clear "use `--name` to disambiguate" message if multiple tracked projects share the basename.

`lore link --all` behavior:

1. For each tracked project in the central repo, check local state for a recorded path. If present and valid, use it.
2. If no path is recorded, scan each entry in `projects_roots` (up to `projects_root_depth` levels deep) for a directory whose basename matches the project name.
3. Single match → record to local state and create the symlink.
4. Multiple matches → interactive prompt to pick.
5. Zero matches → skip with a hint about `lore link <name> --path <path>`.

Happy-path output:

```
$ lore link --all
Scanning configured roots for tracked projects...
  ~/Canonical/git-repos
  ~/PersonalProjects/git-repos

✓ my-nextjs-app    found at ~/Canonical/git-repos/my-nextjs-app       → linked
✓ trading-bot      found at ~/PersonalProjects/git-repos/trading-bot  → linked
⚠ weird-thing      not found in any root
                   run: lore link weird-thing --path <path>
✓ docs-site        already linked
```

Refuses to clobber a real directory at the symlink location. To resolve, the user must remove the real directory or use `lore add --adopt` (TBD; see [open design questions](#open-design-questions)).

### `lore unlink`

Remove a project's symlink without removing it from the central repo. The agent dir stays safe in the central repo; the project just no longer has it linked.

```
lore unlink my-app
lore unlink --all
```

### `lore rm`

Stop tracking a project entirely. Removes every symlink, removes from local state, and deletes from the central repo's working tree. Files persist in git history, so the operation is recoverable via `git checkout HEAD~1 -- projects/<name>/` in the central repo.

```
lore rm my-app             # interactive confirmation required
lore rm my-app --force     # skip confirmation (for scripting / batch removal)
```

Confirmation prompt:

```
$ lore rm scratch-project
This will:
  - Remove projects/scratch-project/ from the central repo working tree
  - Remove ~/code/scratch-project/.claude (symlink, if present)
  - Remove scratch-project from local state
  - Commit the deletion to central (recoverable via git history)
Continue? [y/N]
```

If you only want to remove the local symlink while keeping the project tracked, use `lore unlink` instead.

### `lore mv`

Rename a tracked project.

```
lore mv <old-name> <new-name>                       # rename in central + retarget existing symlink
lore mv <old-name> <new-name> --path ~/code/bar     # also update on-disk path lore tracks
```

`lore mv` never renames the user's on-disk project directory. That's the user's job (`mv ~/code/foo ~/code/bar`); lore only updates its own tracking.

Two modes:

- **Default (rename in central only).** Updates central repo dir name, updates the existing symlink's target to point at the new central path, updates local state's name key. The symlink stays at whatever disk path lore knew about. Use this when you're renaming the project conceptually but haven't moved its directory.
- **With `--path <new-path>`.** Same as above plus: removes the symlink at the old recorded path (if it still exists), creates a fresh symlink at `<new-path>`, updates the path in local state. Use this when you've renamed the project's directory on disk as well.

No automatic cwd detection — explicit `--path` is required for the disk-rename case. Magic that does the right thing 95% of the time is worse than explicit that does the right thing 100% of the time, especially for an operation as rare as renaming a project.

Happy-path output (with `--path`):

```
$ mv ~/code/foo ~/code/bar      # user renames on disk first
$ lore mv foo bar --path ~/code/bar
✓ Renamed projects/foo → projects/bar in central repo
✓ Removed old symlink: ~/code/foo/.claude
✓ Created new symlink: ~/code/bar/.claude → projects/bar/.claude
✓ Updated local state path: ~/code/foo → ~/code/bar
✓ Committed: "refactor: rename foo to bar"
```

### `lore ls`

List tracked projects.

```
lore ls
lore ls --json
```

Sample output:

```
NAME              DIR       LINKED  CENTRAL PATH
my-nextjs-app     .claude   ✓       projects/my-nextjs-app/.claude
trading-bot       .claude   ✓       projects/trading-bot/.claude
docs-site         .agents   ✓       projects/docs-site/.agents
scratch-project   .claude   ✗       projects/scratch-project/.claude (no checkout)
```

### `lore status`

Show the sync state of one project or all tracked projects. Status is read-only.

```
lore status                # all projects
lore status my-app         # every checkout of one project
lore status --json
```

Status compares each project copy and the committed central copy at `HEAD` against that
checkout's sync-base. Every file or nested symlink is classified as `in-sync`,
`project-ahead`, `central-ahead`, or `diverged`. Adds, modifications, deletions, executable
state, object type, and symlink target changes all participate. Directories are structural
containers and do not appear as file records because Git cannot reproduce empty directories.

Checkout rollup follows these rules:

- any diverged file makes the checkout diverged;
- project-ahead and central-ahead files in the same checkout make the checkout diverged;
- otherwise, the changed side determines the checkout state;
- byte-identical project and central copies are in-sync, even if both changed from the base.
  In that last case, `base_stale` is true because a later mutating command must adopt the
  current central commit.

Status reports health issues separately from the four sync states. Issues include a missing
or invalid project copy, a missing central copy, an unreachable sync-base, unsupported
filesystem objects, paths that cannot be inspected, unresolved Git-style conflict markers,
and dirty central content in the project scope. Conflict-marker detection requires a complete
ordered marker sequence in a regular UTF-8 text file. Lore does not follow symlinks while
inspecting a copy.

Each issue appears once at its narrowest scope. Agent-directory issues use that directory's
`issues` array. Checkout-wide issues use the checkout's `issues` array. Project-wide issues
use the project's `issues` array. Directory issue paths are relative to that agent directory.
Checkout and project issue paths identify the affected absolute path or sync-base.

The central side always comes from committed `HEAD`. Uncommitted, untracked, or ignored
central content under the project is reported as `central-dirty`. Lore does not classify that
working-tree content as if it were committed because the sync-base must remain reproducible.

Human output includes per-directory counts for project-ahead, central-ahead, and diverged
files. JSON output has this stable shape:

```json
{
  "central_repo": "/home/nathan/agent-context",
  "central_head": "89abcdef0123456789abcdef0123456789abcdef",
  "projects": [{
    "name": "my-app",
    "checkouts": [{
      "path": "/home/nathan/code/my-app",
      "sync_base": "0123456789abcdef0123456789abcdef01234567",
      "state": "project-ahead",
      "base_stale": false,
      "dirs": [{
        "name": ".claude",
        "state": "project-ahead",
        "base_stale": false,
        "counts": {"in_sync": 3, "project_ahead": 1, "central_ahead": 0, "diverged": 0},
        "files": [{
          "path": "CLAUDE.md",
          "state": "project-ahead",
          "project_change": "modified",
          "central_change": "none",
          "project": {"type": "file", "executable": false},
          "central": {"type": "file", "executable": false},
          "base": {"type": "file", "executable": false}
        }],
        "issues": []
      }],
      "issues": []
    }],
    "issues": []
  }]
}
```

`project_change` and `central_change` are `none`, `added`, `modified`, or `deleted` relative
to the sync-base. Entry objects are `null` when that path is absent on a side.

Exit code is `0` for in-sync, project-ahead, and central-ahead results. Status emits its full
result and exits 7 when any checkout is diverged or has a health issue.

### `lore diff`

Show project-copy differences from committed central `HEAD`. Diff is read-only and inspects
every recorded checkout for the named project.

```
lore diff my-app
lore diff my-app --json
```

Human output shows unified patches for changed regular text files. Binary files, symlinks,
type changes, and executable-state changes use metadata records. When central has dirty
content in the project scope, diff reports the health issue and suppresses patches so it does
not imply that the dirty working tree was compared.

JSON uses the same envelope and file records as status, narrowed to paths where the project
copy differs from committed central `HEAD`. Counts describe that narrowed `files` array, so
`in_sync` is always zero in diff output. Patch text is not included in JSON.

Ordinary differences, including divergence, exit 0. Unknown projects exit 3. Missing copies,
unreachable bases, dirty central content, conflict markers, and other health issues exit 7.

### `lore path`

Print the central-repo path to a project's agent directory (or to the central repo itself with `--repo`). Useful for scripting.

```
lore path my-app
# /home/nathan/agent-context/projects/my-app/.claude

lore path my-app --dir .cursor
# /home/nathan/agent-context/projects/my-app/.cursor

lore path --repo
# /home/nathan/agent-context

cd "$(lore path my-app)"
```

### `lore doctor`

Diagnose common issues across the entire setup. Categories of checks:

1. **Central repo health** — exists, is a git repo, has remote, working tree state, ahead/behind remote.
2. **Global gitignore** — all configured `agent_dirs` are ignored.
3. **Per-project copy health:** copy exists, is a real directory, has a reachable sync-base, and has no unresolved divergence.
4. **Orphan detection** — projects in central with no on-disk checkout for a long time (informational, not an error).

```
lore doctor              # diagnose
lore doctor --fix        # auto-fix safe issues
```

`--fix` only auto-fixes unambiguous, reversible issues (re-materialise a missing project copy from central, append a missing gitignore entry). It does **not** auto-resolve divergence.

### `lore pull`

Receive committed central content into project copies. Pull does not read from or contact a Git remote.

```
lore pull                         # pull the tracked checkout containing cwd
lore pull my-app                  # pull every recorded checkout for one project
lore pull my-app --path <path>    # pull or adopt one exact checkout
lore pull --all                   # use recorded paths, then scan projects_roots
```

Behavior in the non-merge implementation:

- A missing project copy is materialised from committed central `HEAD`.
- A central-ahead checkout receives the complete changed agent-directory trees. Deletions, executable modes, and nested symlinks are preserved.
- A byte-identical checkout with missing state or an unreachable sync-base is adopted at central `HEAD`.
- A project-ahead checkout is left unchanged and remains ready to push.
- Divergence and a differing copy without a usable sync-base are refused. Three-way merge and explicit direction selection are added by the next implementation slice.
- A named pull preflights every recorded checkout before mutation. `--all` handles each checkout atomically, continues after a failure, reports every result, and returns the most severe failure code.
- `--all` uses every valid recorded checkout. If none is valid, it scans configured roots. One match is adopted. Several matches remain ambiguous and require explicit `--path` commands.
- Pull refuses dirty central content. Committed `HEAD` remains the authority.
- Pull silently accepts any effective Git ignore rule. An interactive single pull offers the existing repository, global, and `.git/info/exclude` picker when needed. `--all` and JSON never prompt. They report unignored copies instead.

In JSON mode, pull returns `central_repo`, `central_head`, `scanned_roots`, and `projects`. Each project includes its status, ambiguous matches, checkout results, and error text. Each checkout includes its path, status, sync-base, and per-directory results. Per-directory results include the path, outcome, and `unignored` warning flag. Arrays are always present.

### `lore push`

Capture project-copy changes in the central repository. Push copies additions, edits, executable-mode changes, symlink changes, and deletions into central. It then commits the selected paths and advances each pushed checkout's sync-base.

```
lore push                          # push all tracked projects
lore push --auto                   # quiet on a no-op
lore push --project my-app         # scope to one tracked project
lore push --project-from-cwd       # scope to the checkout containing cwd
```

Behavior:

- Push preflights the complete selection before it mutates central. A dirty central copy, an unhealthy checkout, or a selected checkout that is central-ahead or diverged refuses the complete operation. The error tells the user to run `lore pull` first when reconciliation is required.
- Project-ahead content is copied as a complete agent-directory tree. This propagates deletions without leaving stale central files.
- Push always commits with `commit.sync_template`. `{projects}` resolves to the sorted, comma-separated project names that changed. `--no-commit` is not supported because a dirty central working tree cannot be represented by a SHA sync-base.
- If project and central already match but the base is stale, push safely advances the local sync-base to central `HEAD` without creating a commit.
- If several checkouts for one project have identical project-ahead content, push captures that content once and advances each matching checkout. If their content differs, push refuses and directs the user to select one with `--project-from-cwd`.
- `--auto` suppresses only genuine no-op output. Failures remain loud and are recorded at `~/.local/state/lore/last-sync-error.txt`. The next human-mode lore command reports and clears that notice. JSON commands do not consume it. A later successful automatic push clears a stale notice.
- **`--project-from-cwd` is a contract assertion that cwd is inside a tracked checkout.** If it is not, push exits 3 in both auto and interactive modes.

Lore push never contacts a Git remote. Lore's push and pull verbs reconcile the project copy with central's working tree only. To back up the resulting central commit remotely, use plain Git:

```sh
cd "$(lore path --repo)" && git push
```

In JSON mode, push returns `central_repo`, `previous_head`, `commit_sha`, `commit_message`, and `projects`. `previous_head` is the central `HEAD` inspected before push. `commit_sha` and `commit_message` are empty when push creates no commit. Each project contains checkout paths and an outcome of `pushed`, `adopted`, or `unchanged`.

---

## Output design principles

These are normative for the entire CLI:

1. **Every command tells you what it's about to do, then does it, then tells you what to do next.** The "Next: cd ~/agent-context && git push" line in `lore add` output is functional, not decoration.
2. **Errors are actionable.** Never bare `Error: not found`. Always `"Project 'foo' is tracked but not on disk. Either clone it to ~/code/foo or run 'lore rm foo' to stop tracking."`
3. **`--json` everywhere data flows out.** The JSON schema is part of the public API and must be stable.
4. **Idempotency is non-negotiable.** Every command should be safe to re-run.
5. **No surprises with state.** Every output line should make it obvious which of the two state stores (filesystem symlinks, central git repo) was modified.
6. **Quiet when invoked by automation.** `--auto` / `--quiet` flags should produce no output on no-op success.

---

## Agent-friendliness

`lore` is deliberately CLI-first rather than MCP-first. MCP integration may be added later as a thin wrapper, but the CLI is the foundation.

For a CLI to be genuinely usable by agents, it must:

- Have excellent `--help` at every level, with concrete usage examples in the help text itself.
- Support `--json` on every data-returning command, with a documented and stable schema.
- Use distinct, documented exit codes per failure mode.
- Produce errors that say what to do next.
- Be fully idempotent.

These properties also happen to make the CLI better for humans.

---

## Implementation notes

### Language

**Go** is the recommended language for the v1 implementation.

Rationale:

- Single static binary — trivial distribution via Homebrew, direct download, or `go install`.
- Sub-10ms startup; matters for tools run frequently in shells.
- Best-in-class CLI ecosystem: `cobra` (subcommands, help, completions), `viper` (config), `bubbletea` / `lipgloss` / `huh` (interactive prompts, pretty output).
- Trivial cross-compilation to all targets.

Rust is a reasonable alternative if the author wants to use this as a Rust-learning project. `clap` is excellent for CLIs and gives the same single-binary distribution win, at the cost of steeper learning curve and longer build times.

Python or TypeScript are **not** recommended for the public release. Distribution friction (runtime versions, pipx, npm) and startup overhead make them worse fits for a tool run frequently. Either would be fine for a private prototype.

### Suggested libraries (Go)

- CLI framework: `github.com/spf13/cobra`
- Config: `github.com/spf13/viper` (or pelletier/go-toml directly)
- Interactive prompts: `github.com/charmbracelet/huh`
- Styled output: `github.com/charmbracelet/lipgloss`
- TOML parsing: `github.com/pelletier/go-toml/v2`
- Filesystem walking: `io/fs` + `filepath.WalkDir`
- Git operations: shell out to `git` (do not embed `go-git`; the surface area is too small to justify)

### Suggested package layout

```
cmd/lore/                    # main entry point
internal/
  config/                    # config file load/save
  state/                     # local state load/save
  central/                   # central repo operations
  link/                      # symlink operations
  detect/                    # agent dir auto-detection, gitignore checks
  hooks/                     # harness integration
    claudecode/              # Claude Code provider
    provider.go              # Harness interface
  commands/                  # one file per subcommand
  output/                    # human + json formatters
  git/                       # narrow git shell-out helpers
```

---

## Future work

Captured here so they aren't lost. Order is rough priority guidance, not commitment.

### Near-term (v0.2)

- **`lore hooks install`.** v1 expects users to wire up their own harness hooks manually (`lore push --auto --project-from-cwd` from Claude Code's `SessionEnd` hook, etc.). A `lore hooks` command would automate that for common harnesses via a provider interface (`Name`, `Detect`, `InstallHook`, `UninstallHook`, `Status`), with Claude Code first, then Cursor, Aider, and Goose as they grow first-class lifecycle hooks. It must defensively merge into existing user hook config and never clobber it. Failed hooks should write to `~/.local/state/lore/last-sync-error.txt`, and the next interactive `lore` invocation should surface it as a banner.
- **Better interactive `lore doctor --fix` flow** with per-issue confirmation.
- **Bash/zsh/fish completions** via `lore completion <shell>`.

### Medium-term (v0.3+)

- **`lore link --all --clone-missing`.** When central has tracked projects with no on-disk checkout in any configured root, optionally clone them from a recorded git remote URL. Hugely improves the new-machine recovery story (currently fully manual for the cloning step). Requires per-project metadata in central (see next item).
- **Per-project metadata file in central (`projects/<name>/.lore.toml`).** Introduced when the first feature needs it — likely `--clone-missing` (records project's git remote) and/or "last seen at path X on host Y" hints to make `lore link --all` smarter when same-basename collisions or missing checkouts happen. Intentionally not in v1 because no v1 feature requires it; YAGNI until then.
- **Richer sync commit messages.** Instead of a static `chore: sync ({projects})` template, optionally prompt the user (or the agent, via transcript inspection) for a one-line description of what the session changed. Would make the central repo's `git log` actually useful for archaeology without giving up automatic capture. Likely needs harness cooperation to read the transcript or surface a prompt at session-end. See also: `lore squash` as a complementary approach for cleaning up history after the fact.
- **`lore squash --since <ref>`** — rewrite a range of auto-sync commits into one meaningful commit, for users who want clean history without giving up auto-capture.
- **`lore search <query>`** — full-text search across all tracked project lore. Needs an index (sqlite + fts5, or ripgrep-as-library).
- **Common-locations hints in the central repo** — optional `projects/<name>/.lore.toml` recording common checkout paths to make first-time `lore link --all` smarter on a fresh machine. Skipped in v1 because `projects_roots` scanning should be sufficient; revisit if real friction emerges.
- **`lore log <project>`**: thin scoped wrapper over `git log` for a project's directory in central. Possibly skip entirely if `cd $(lore path foo) && git log .` proves to be enough.

### Long-term / speculative

- **`lore export` / `lore import`** — bundle/unbundle for sharing project lore with collaborators. Likely unnecessary if `tar` and direct repo sharing suffice; revisit if a real sharing use case emerges that needs metadata bundling (compat version, sanitization).
- **TUI mode (`lore ui`)** — interactive project browser, status dashboard, content preview.
- **Encrypted secrets in central repo** — integration with `age` or `sops` for users who want API keys etc. in their lore. May point to chezmoi-style tools instead.
- **Web-accessible lore viewer** — static-site generator that turns the central repo into a browsable site.
- **Multiple central repos / profiles** — e.g., work lore separate from personal lore. Defer until someone asks; likely solvable with multiple installations or a `--profile` flag.
- **MCP server wrapper** — exposes `lore`'s read-only operations (status, search, list) as MCP tools for agents that prefer that protocol. Strictly additive on top of the CLI.

### Explicitly out of scope

- Acting as a full git frontend.
- Cloud sync separate from git.
- Anything that requires a server.
- Opinions about the contents of agent directories (`CLAUDE.md` format, etc.). `lore` moves and symlinks; what's inside is the user's concern.
- Mixing personal and team-shared content inside the same agent directory (e.g. a project where `.claude/CLAUDE.md` is committed by the team but the user also wants personal prompts under `.claude/`). lore moves agent dirs as a unit; if you want a split, organize it yourself (e.g. `.claude/` stays with the team, `.claude-personal/` is lore-managed).
- Native Windows support. See [ADR 0001](docs/adr/0001-linux-and-macos-only.md).

---

## Open design questions

The originally-deferred open questions have all been resolved as part of the design — see resolved sections below. The remaining open questions are smaller and worth revisiting once there's real usage data.

### Resolved

1. **~~`lore add --adopt`~~** — omitted from v1. `lore add` refuses if a real (non-symlink) agent directory contains anything lore can't safely move. User resolves manually. Revisit only if real users hit this often enough to justify merge-resolution complexity.
2. **~~Path-hint metadata in the central repo (`.lore.toml` per project)~~** — deferred until the first feature needs it (likely `--clone-missing` in v0.2). YAGNI for v1.
3. **~~Nested `.git` inside an agent directory~~** — `lore add` refuses with clear recovery instructions.
4. **~~Agent dir is itself a symlink at onboarding time~~** — `lore add` refuses; user replaces with a real directory before retrying.
5. **~~Multiple agent dirs per project~~** — supported as a first-class concept. See [Project definition](CONTEXT.md) and the `lore add` agent-dir detection step. Project identity is `(name, set-of-agent-dirs)`; commands operate on the whole project by default and accept `--dir` to scope.
6. **~~`lore push --project-from-cwd` when cwd isn't inside any tracked project~~**: error loudly (non-zero exit, stderr) in both `--auto` and interactive modes. See `lore push` behavior.

### Still open

1. **Two projects with the same basename in different parent directories.** Currently handled by `--name` override at `lore add` time. May want better auto-disambiguation if it becomes common.

---

## Glossary

- **Central repo.** The single private git repository (`~/agent-context` by default) holding all tracked project lore.
- **Tracked project.** A project that has been onboarded with `lore add` and has a subdirectory under `projects/` in the central repo.
- **Agent directory.** The dot-prefixed directory inside a project that holds agentic AI context — `.claude/`, `.agents/`, `.cursor/`, `.aider/`, etc.
- **Symlink.** The link from a project's expected agent directory location to the actual content in the central repo.
- **Local state.** Per-machine path mapping (`~/.local/state/lore/state.toml`) recording where each tracked project lives on this machine.
- **Harness.** An agentic AI tool that runs sessions in projects — Claude Code, Cursor, Aider, etc. `lore hooks` integrates with harnesses via a provider interface.
- **Lore.** The accumulated, often-unwritten knowledge about a codebase: its quirks, conventions, history, and the prompts/rules/examples a user has built up for an agent working in it.
