#!/usr/bin/env bash
set -euo pipefail

unformatted=$(gofmt -l cmd internal)
if [[ -n "$unformatted" ]]; then
  echo "Go files require gofmt:" >&2
  printf '%s\n' "$unformatted" | sed 's/^/  /' >&2
  exit 1
fi
