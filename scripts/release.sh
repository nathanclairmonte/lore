#!/usr/bin/env bash
set -euo pipefail

action="${1:-}"
version="${2:-}"

if [ "$action" != "prepare" ] && [ "$action" != "publish" ]; then
  echo "usage: $0 <prepare|publish> <version>" >&2
  exit 1
fi

if [ -z "$version" ] || [[ "$version" == v* ]]; then
  echo "version must omit the v prefix, for example: 0.1.0" >&2
  exit 1
fi

tag="v$version"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(git -C "$script_dir" rev-parse --show-toplevel)"
cd "$repo_root"

"$script_dir/validate-release-tag.sh" "$tag" --version-only

if [ "$(git branch --show-current)" != "main" ]; then
  echo "release tasks must run from main" >&2
  exit 1
fi

if [ -n "$(git status --porcelain)" ]; then
  echo "release tasks require a clean worktree and index" >&2
  exit 1
fi

git fetch origin main --tags

head_commit="$(git rev-parse HEAD)"
main_commit="$(git rev-parse origin/main)"
if [ "$head_commit" != "$main_commit" ]; then
  echo "local main must exactly match origin/main" >&2
  exit 1
fi

if [ "$action" = "prepare" ]; then
  if git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null; then
    echo "release tag already exists: $tag" >&2
    exit 1
  fi

  task check
  task release:snapshot
  git tag -a "$tag" -m "$tag"

  if ! "$script_dir/validate-release-tag.sh" "$tag"; then
    git tag -d "$tag" >/dev/null
    echo "removed invalid local release tag: $tag" >&2
    exit 1
  fi

  echo "prepared $tag locally"
  echo "publish it with: task release:publish VERSION=$version"
  exit 0
fi

"$script_dir/validate-release-tag.sh" "$tag"

tag_commit="$(git rev-parse "$tag^{commit}")"
if [ "$tag_commit" != "$head_commit" ]; then
  echo "release tag must point at the current main commit: $tag" >&2
  exit 1
fi

if git ls-remote --exit-code --tags origin "refs/tags/$tag" >/dev/null 2>&1; then
  echo "release tag already exists on origin: $tag" >&2
  exit 1
fi

printf 'type %s to publish this release: ' "$tag"
IFS= read -r confirmation
if [ "$confirmation" != "$tag" ]; then
  echo "release cancelled" >&2
  exit 1
fi

git push origin "refs/tags/$tag:refs/tags/$tag"

origin_url="$(git remote get-url origin)"
case "$origin_url" in
  git@github.com:*) repo_url="https://github.com/${origin_url#git@github.com:}" ;;
  https://github.com/*) repo_url="$origin_url" ;;
  *) repo_url="" ;;
esac
repo_url="${repo_url%.git}"

echo "publication started for $tag"
if [ -n "$repo_url" ]; then
  echo "follow it at: $repo_url/actions/workflows/release.yml"
fi
