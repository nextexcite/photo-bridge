#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

skip_gitleaks=false
if [[ "${1:-}" == "--skip-gitleaks" ]]; then
  skip_gitleaks=true
fi

failures=0
audit_files=$(mktemp)
trap 'rm -f "$audit_files"' EXIT
git ls-files --cached --others --exclude-standard -z > "$audit_files"

while IFS= read -r -d '' file; do
  case "$file" in
    *.jpg|*.jpeg|*.png|*.gif|*.heic|*.avif|*.webp|*.tif|*.tiff|*.dng|*.raw|*.mp4|*.mov|*.mkv|*.avi|*.zip|*.7z|*.sqlite|*.sqlite3|*.db|*.log)
      echo "public audit: forbidden tracked/runtime artifact shape: $file" >&2
      failures=$((failures + 1))
      ;;
  esac

  [[ -f "$file" ]] || continue

  if rg -q --pcre2 '(?i)\b(?!(?:[A-Z0-9._%+\-]+@(?:example\.invalid|example\.com|users\.noreply\.github\.com)|noreply@github\.com)\b)[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b' "$file"; then
    echo "public audit: possible non-example email address in $file" >&2
    failures=$((failures + 1))
  fi

  if rg -q --pcre2 '/Users/(?!example-user(?:/|\b))[A-Za-z0-9._-]+(?:/|\b)|/home/(?!example-user(?:/|\b))[A-Za-z0-9._-]+(?:/|\b)' "$file"; then
    echo "public audit: possible personal home path in $file" >&2
    failures=$((failures + 1))
  fi

  if rg -q --pcre2 '(?<![0-9])(?!(?:0\.0\.0\.0|127\.0\.0\.1|192\.0\.2\.[0-9]{1,3}|198\.51\.100\.[0-9]{1,3}|203\.0\.113\.[0-9]{1,3})(?![0-9]))(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})(?:\.(?:25[0-5]|2[0-4][0-9]|1?[0-9]{1,2})){3}(?![0-9])' "$file"; then
    echo "public audit: possible non-documentation IPv4 address in $file" >&2
    failures=$((failures + 1))
  fi
done < "$audit_files"

private_terms_file=${PUBLIC_AUDIT_PRIVATE_TERMS_FILE:-$repo_root/.git/public-audit-private-terms}
if [[ -f "$private_terms_file" ]]; then
  while IFS= read -r private_term; do
    [[ -n "$private_term" ]] || continue
    while IFS= read -r -d '' file; do
      [[ -f "$file" ]] || continue
      if rg -q -F -- "$private_term" "$file"; then
        echo "public audit: private term found in $file" >&2
        failures=$((failures + 1))
      fi
    done < "$audit_files"
  done < "$private_terms_file"
fi

if git rev-parse --verify HEAD >/dev/null 2>&1; then
  while IFS= read -r commit_email; do
    [[ -n "$commit_email" ]] || continue
    if [[ "$commit_email" != *@users.noreply.github.com && "$commit_email" != "noreply@github.com" ]]; then
      echo "public audit: commit history contains a non-noreply author or committer email" >&2
      failures=$((failures + 1))
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
  if git rev-parse --verify HEAD >/dev/null 2>&1; then
    gitleaks git --redact --no-banner .
  fi
fi

if ((failures > 0)); then
  echo "public audit failed with $failures finding(s)" >&2
  exit 1
fi

echo "public audit passed"
