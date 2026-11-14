#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CURRENT_YEAR="$(date +%Y)"
LICENSE_ID_LINE='// SPDX-License-Identifier: AGPL-3.0-or-later'
COPYRIGHT_REGEX='^// Copyright \(c\) ([0-9]{4})(-([0-9]{4}))? GitStore contributors$'
UPSTREAM_LICENSE='shared/scylla-cdc-go/LICENSE.txt'
MODE=all
DIFF_BASE=

usage() {
  cat <<'EOF'
Usage: scripts/check-license-headers.sh [--all | --staged | --diff-base <ref>]

Checks tracked Go, Rust and JavaScript/TypeScript license headers.
Extensions: .go, .rs, .ts, .tsx, .js, .jsx, .mjs, .cjs

  --all              Check working-tree headers of all tracked sources (default)
  --staged           Check added/modified/renamed sources from the Git index
  --diff-base <ref>  Check working-tree sources changed in <ref>...HEAD

Headers require AGPL-3.0-or-later and a GitStore contributors copyright.
Changed sources must have the current year as a copyright range endpoint.
Generated sources are skipped using language-specific header markers.
The pinned shared/scylla-cdc-go copy retains its upstream Apache attribution.
EOF
}

while (( $# )); do
  case "$1" in
    --all|--staged)
      MODE="${1#--}"
      shift
      ;;
    --diff-base)
      DIFF_BASE="${2:-}"
      if [[ -z "$DIFF_BASE" || "$DIFF_BASE" == -* ]]; then
        echo "error: --diff-base requires a git ref" >&2
        exit 2
      fi
      MODE=diff
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "error: unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

cd "$REPO_ROOT"
git rev-parse --is-inside-work-tree >/dev/null

read_content() {
  if [[ "$MODE" == staged ]]; then
    git show ":$1" 2>/dev/null
  else
    [[ -f "$1" ]] && cat -- "$1"
  fi
}

failures=0
checked=0
upstream_checked=false

fail() {
  printf '[FAIL] %s: %s\n' "$1" "$2" >&2
  failures=$((failures + 1))
}

check_file() {
  local file="$1" content header generated_lines=8 header_lines=12
  local generated_regex='Code generated|DO NOT EDIT' copyright_line
  local grep_flags=-Eq

  if [[ "$file" == "$UPSTREAM_LICENSE" || "$file" == shared/scylla-cdc-go/*.go ]]; then
    if [[ "$upstream_checked" == false ]]; then
      upstream_checked=true
      if ! content="$(read_content "$UPSTREAM_LICENSE")" ||
          ! grep -q 'Apache License' <<<"$content"; then
        fail "$UPSTREAM_LICENSE" 'missing upstream Apache license'
      fi
    fi
    return
  fi

  # Deleted sources have no remaining blob to check.
  if ! content="$(read_content "$file")"; then
    return
  fi
  case "$file" in
    *.rs) generated_regex='Code generated|@generated|DO NOT EDIT' ;;
    *.go) ;;
    *.ts|*.tsx|*.js|*.jsx|*.mjs|*.cjs)
      generated_lines=12
      header_lines=14
      generated_regex='Code generated|AUTO-GENERATED|DO NOT EDIT'
      grep_flags=-Eqi
      ;;
    *) return ;;
  esac
  if grep "$grep_flags" "$generated_regex" <<<"$(head -n "$generated_lines" <<<"$content")"; then
    return
  fi
  header="$(head -n "$header_lines" <<<"$content")"
  checked=$((checked + 1))
  if ! grep -Fxq "$LICENSE_ID_LINE" <<<"$header"; then
    fail "$file" 'missing SPDX license identifier'
    return
  fi
  copyright_line="$(grep -m 1 -E '^// Copyright \(c\) ' <<<"$header" || true)"
  if [[ -z "$copyright_line" ]]; then
    fail "$file" 'missing copyright line'
  elif ! [[ "$copyright_line" =~ $COPYRIGHT_REGEX ]]; then
    fail "$file" 'invalid copyright format'
  elif [[ "$MODE" != all ]]; then
    local start_year="${BASH_REMATCH[1]}"
    local end_year="${BASH_REMATCH[3]:-${BASH_REMATCH[1]}}"
    if [[ "$start_year" != "$CURRENT_YEAR" && "$end_year" != "$CURRENT_YEAR" ]]; then
      fail "$file" "changed files must include current year ($CURRENT_YEAR)"
    fi
  fi
}

if [[ "$MODE" == all ]]; then
  files_cmd=(git ls-files -z)
else
  files_cmd=(git diff --name-only -z --diff-filter=AMRD --find-renames)
  if [[ "$MODE" == staged ]]; then
    files_cmd+=(--cached)
  else
    files_cmd+=("${DIFF_BASE}...HEAD")
  fi
fi
files_cmd+=(-- '*.go' '*.rs' '*.ts' '*.tsx' '*.js' '*.jsx' '*.mjs' '*.cjs' "$UPSTREAM_LICENSE")

# NUL-delimited paths preserve whitespace, tabs and newlines, including rename
# destinations. Wait for Git as well: a failed diff must never pass as empty.
exec 3< <("${files_cmd[@]}")
files_pid=$!
while IFS= read -r -d '' file; do
  check_file "$file"
done <&3
exec 3<&-
if ! wait "$files_pid"; then
  echo 'error: could not collect tracked sources' >&2
  exit 2
fi

if (( failures )); then
  echo "Checked $checked file(s): $failures failure(s)." >&2
  exit 1
fi
echo "License header check passed ($checked file(s) checked)."
