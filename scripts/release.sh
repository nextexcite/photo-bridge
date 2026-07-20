#!/usr/bin/env bash
set -euo pipefail

version=${1:-}
if ! [[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$ ]]; then
  echo "release: version must be SemVer in vX.Y.Z form, optionally with a prerelease suffix" >&2
  exit 2
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

if [[ "$(git branch --show-current)" != "main" ]]; then
  echo "release: releases must be created from main" >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "release: worktree must be clean" >&2
  exit 1
fi

git fetch --quiet origin main --tags
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "release: local main must exactly match origin/main" >&2
  exit 1
fi
if git rev-parse --verify --quiet "refs/tags/$version" >/dev/null; then
  echo "release: tag already exists: $version" >&2
  exit 1
fi
if git ls-remote --exit-code --tags origin "refs/tags/$version" >/dev/null 2>&1; then
  echo "release: remote tag already exists: $version" >&2
  exit 1
fi

mise exec -- just check
mise exec -- just integration

git tag --annotate "$version" --message "photo-bridge $version"
git push origin "refs/tags/$version"

echo "release tag published: $version"
