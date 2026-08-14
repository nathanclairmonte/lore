#!/usr/bin/env bash
set -euo pipefail

tag="${1:-}"
main_ref="${2:-origin/main}"

if [[ ! "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$ ]]; then
  echo "invalid semantic version tag: $tag" >&2
  exit 1
fi

version_without_build="${tag%%+*}"
if [[ "$version_without_build" == *-* ]]; then
  prerelease="${version_without_build#*-}"
  IFS='.' read -r -a identifiers <<< "$prerelease"
  for identifier in "${identifiers[@]}"; do
    if [[ "$identifier" =~ ^[0-9]+$ && ${#identifier} -gt 1 && "$identifier" == 0* ]]; then
      echo "numeric prerelease identifiers must not contain leading zeroes: $tag" >&2
      exit 1
    fi
  done
fi

if [ "$main_ref" = "--version-only" ]; then
  exit 0
fi

if ! git rev-parse --verify --quiet "refs/tags/$tag" >/dev/null; then
  echo "release tag does not exist: $tag" >&2
  exit 1
fi

if [ "$(git cat-file -t "$tag")" != "tag" ]; then
  echo "release tag must be annotated: $tag" >&2
  exit 1
fi

if ! git rev-parse --verify --quiet "$main_ref^{commit}" >/dev/null; then
  echo "main reference does not exist: $main_ref" >&2
  exit 1
fi

if ! git merge-base --is-ancestor "$tag^{commit}" "$main_ref^{commit}"; then
  echo "release commit must be reachable from $main_ref: $tag" >&2
  exit 1
fi
