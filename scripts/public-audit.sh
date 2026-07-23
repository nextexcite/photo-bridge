#!/usr/bin/env bash
set -euo pipefail

# Public repositories need a stricter boundary than a generic secret scanner.
# This audit is intentionally conservative: it only accepts text-shaped tracked
# files and the synthetic identities documented in AGENTS.md.

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

skip_gitleaks=false
self_test=false
for argument in "$@"; do
  case "$argument" in
    --skip-gitleaks) skip_gitleaks=true ;;
    --self-test) self_test=true ;;
    *) echo "public audit: unknown argument: $argument" >&2; exit 2 ;;
  esac
done

failures=0

record_failure() {
  echo "public audit: $1" >&2
  failures=$((failures + 1))
}

forbidden_path() {
  local path
  path=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  # Only deployment roots are prohibited. Package names such as
  # internal/config and internal/state are ordinary public Go source.
  [[ "$path" =~ ^(config|state|data)(/|$) ]] ||
    [[ "$path" =~ (^|/)(runtime|reports?|browser(-profiles?)?|rclone)(/|$) ]] ||
    [[ "$path" =~ ^(takeout|archives?)(/|$) ]]
}

forbidden_extension() {
  local path
  path=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  [[ "$path" =~ \.(jpg|jpeg|png|gif|heic|avif|webp|tif|tiff|dng|raw|mp3|wav|flac|mp4|mov|mkv|avi|zip|tar|tgz|gz|bz2|xz|7z|rar|sqlite|sqlite3|db|log|har|pem|p12|pfx|key)$ ]]
}

allowed_domain() {
  local domain
  domain=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  case "$domain" in
    example.invalid|example.com|users.noreply.github.com|github.com|docs.github.com|ghcr.io|rclone.org|downloads.rclone.org|developers.google.com|google.com|golang.org|go.dev|apache.org|www.apache.org|spdx.org|gitleaks.io|pkg.go.dev)
      return 0
      ;;
    *) return 1 ;;
  esac
}

audit_content() {
  local file=$1
  local domain

  if rg -q --pcre2 '(?i)\b(?!(?:[A-Z0-9._%+\-]+@(?:example\.invalid|example\.com|users\.noreply\.github\.com)|noreply@github\.com)\b)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b' "$file"; then
    record_failure "possible non-example email address in $file"
  fi
  if rg -q --pcre2 '/Users/(?!example-user(?:/|\b))[A-Za-z0-9._-]+(?:/|\b)|/home/(?!example-user(?:/|\b))[A-Za-z0-9._-]+(?:/|\b)' "$file"; then
    record_failure "possible personal home path in $file"
  fi
  if rg -q --pcre2 '(?<![0-9])(?!(?:0\.0\.0\.0|127\.0\.0\.1|192\.0\.2\.[0-9]{1,3}|198\.51\.100\.[0-9]{1,3}|203\.0\.113\.[0-9]{1,3})(?![0-9]))(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})(?:\.(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})){3}(?![0-9])' "$file"; then
    record_failure "possible non-documentation IPv4 address in $file"
  fi
  if rg -q --pcre2 '(?i)\b[0-9]{6,}-[A-Za-z0-9_-]{20,}\.apps\.googleusercontent\.com\b|\b(?:oauth|refresh)[_-]?token\s*[:=]|\b(?:cookie|set-cookie)\s*[:=]|\b(?:AKIA|ASIA)[A-Z0-9]{16}\b' "$file"; then
    record_failure "possible OAuth, cookie, or cloud credential material in $file"
  fi

  # URLs are allowed only for public documentation and public project endpoints.
  while IFS= read -r domain; do
    allowed_domain "$domain" || record_failure "unapproved domain in $file"
  done < <(rg -o --pcre2 '(?i)https?://(?:[a-z0-9-]+\.)+[a-z]{2,}' "$file" | sed -E 's#^https?://##I' | sort -u)
}

audit_file() {
  local file=$1
  local mime
  forbidden_path "$file" && record_failure "forbidden tracked runtime path: $file"
  forbidden_extension "$file" && record_failure "forbidden tracked/runtime artifact shape: $file"
  [[ -f "$file" ]] || return 0

  mime=$(file --brief --mime-type "$file")
  case "$mime" in
    text/*|application/json|application/xml|application/x-yaml|inode/*) ;;
    *) record_failure "non-text tracked file ($mime): $file"; return 0 ;;
  esac
  audit_content "$file"
}

run_self_test() {
  local fixture_root before
  fixture_root=$(mktemp -d)
  trap 'rm -rf "$fixture_root"' RETURN

  printf 'contact@example.invalid\n' > "$fixture_root/good.txt"
  before=$failures; audit_file "$fixture_root/good.txt"
  (( failures == before )) || { echo "public audit self-test: rejected valid synthetic fixture" >&2; return 1; }

  printf '%s@%s\n' person private.example > "$fixture_root/email.txt"
  before=$failures; audit_file "$fixture_root/email.txt"
  (( failures == before + 1 )) || { echo "public audit self-test: missed email" >&2; return 1; }

  printf 'binary\n' > "$fixture_root/archive.ZIP"
  before=$failures; audit_file "$fixture_root/archive.ZIP"
  (( failures == before + 1 )) || { echo "public audit self-test: missed case-insensitive archive" >&2; return 1; }

  printf '%s\n' "https://private."example > "$fixture_root/domain.txt"
  before=$failures; audit_file "$fixture_root/domain.txt"
  (( failures == before + 1 )) || { echo "public audit self-test: missed unapproved domain" >&2; return 1; }

  printf '192.0.2.10\n' > "$fixture_root/documentation-ip.txt"
  before=$failures; audit_file "$fixture_root/documentation-ip.txt"
  (( failures == before )) || { echo "public audit self-test: rejected documentation IP" >&2; return 1; }
  echo "public audit self-test passed"
}

if [[ "$self_test" == true ]]; then
  run_self_test
  exit 0
fi

audit_files=$(mktemp)
trap 'rm -f "$audit_files"' EXIT
git ls-files --cached --others --exclude-standard -z > "$audit_files"
while IFS= read -r -d '' file; do
  audit_file "$file"
done < "$audit_files"

private_terms_file=${PUBLIC_AUDIT_PRIVATE_TERMS_FILE:-$repo_root/.git/public-audit-private-terms}
if [[ -f "$private_terms_file" ]]; then
  while IFS= read -r private_term; do
    [[ -n "$private_term" && "$private_term" != \#* ]] || continue
    while IFS= read -r -d '' file; do
      [[ -f "$file" ]] || continue
      if rg -q -F -- "$private_term" "$file"; then
        record_failure "private term found in $file"
      fi
    done < "$audit_files"
  done < "$private_terms_file"
fi

if git rev-parse --verify HEAD >/dev/null 2>&1; then
  while IFS= read -r commit_email; do
    [[ -n "$commit_email" ]] || continue
    if [[ "$commit_email" != *@users.noreply.github.com && "$commit_email" != "noreply@github.com" ]]; then
      record_failure "commit history contains a non-noreply author or committer email"
      break
    fi
  done < <(git log --format='%ae%n%ce' --all | sort -u)
fi

if [[ "$skip_gitleaks" == false ]]; then
  if ! command -v gitleaks >/dev/null 2>&1; then
    echo "public audit: gitleaks is required; run mise install" >&2
    exit 1
  fi
  gitleaks dir --redact --no-banner .
  gitleaks git --redact --no-banner --log-opts="--all" .
fi

if ((failures > 0)); then
  echo "public audit failed with $failures finding(s)" >&2
  exit 1
fi

echo "public audit passed"
