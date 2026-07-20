#!/usr/bin/env bash
set -euo pipefail

git config --local core.hooksPath .githooks
echo "installed repository-local public audit hooks"
