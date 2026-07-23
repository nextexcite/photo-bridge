set shell := ["bash", "-euo", "pipefail", "-c"]

default:
  @just --list

fmt:
  gofmt -w cmd internal

fmt-check:
  scripts/fmt-check.sh

test:
  go test ./...

vet:
  go vet ./...

integration:
  scripts/integration-local.sh

# Requires disposable MinIO and WebDAV endpoints; CI supplies both.
integration-backends:
  scripts/integration-backends.sh

# Validate, tag, and publish a SemVer release. Example: just release v0.1.3
release version:
  scripts/release.sh "{{version}}"

build:
  mkdir -p bin
  CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/photo-bridge ./cmd/photo-bridge

public-audit:
  scripts/public-audit.sh

public-audit-self-test:
  scripts/public-audit.sh --self-test

install-hooks:
  scripts/install-git-hooks.sh

check: fmt-check test vet public-audit-self-test public-audit

# OCI images are intentionally built only by GitHub Actions.
image-build:
  @echo "Local image builds are disabled; use the GitHub Actions image workflow." >&2
  @exit 1
