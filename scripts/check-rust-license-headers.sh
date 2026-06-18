#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

CURRENT_YEAR="$(date +%Y)"
LICENSE_ID_LINE='// SPDX-License-Identifier: AGPL-3.0-or-later'
COPYRIGHT_REGEX='^// Copyright \(c\) ([0-9]{4})(-([0-9]{4}))? GitStore contributors$'

MODE="all"
DIFF_BASE=""

usage() {
  cat <<'EOF'
Usage: scripts/check-rust-license-headers.sh [--all | --staged | --diff-base <ref>]

Checks Rust files for required license headers.

Modes:
  --all               Check all tracked .rs files in the repository (default)
  --staged            Check only staged added/modified .rs files
  --diff-base <ref>   Check added/modified .rs files in <ref>...HEAD

Rules:
  1. All checked files must include:
     // SPDX-License-Identifier: AGPL-3.0-or-later
     // Copyright (c) <year or year-year> GitStore contributors
  2. For changed files (--staged / --diff-base), the copyright year must include
     the current year.

Generated files (containing "Code generated", "@generated", or "DO NOT EDIT"
in the first 8 lines) are skipped.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --all)
      MODE="all"
      shift
      ;;
    --staged)
      MODE="staged"
      shift
      ;;
    --diff-base)
      MODE="diff"
      DIFF_BASE="${2:-}"
      if [[ -z "$DIFF_BASE" ]]; then
        echo "error: --diff-base requires a git ref" >&2
        exit 2
      fi
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

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "error: must run inside a git repository" >&2
  exit 2
fi

current_year_present() {
  local line="$1"
  if [[ "$line" =~ ^//\ Copyright\ \(c\)\ ([0-9]{4})(-([0-9]{4}))?\ GitStore\ contributors$ ]]; then
    local start_year="${BASH_REMATCH[1]}"
    local end_year="${BASH_REMATCH[3]:-${BASH_REMATCH[1]}}"
    if (( end_year == CURRENT_YEAR )); then
      return 0
    fi
    if (( start_year == CURRENT_YEAR )); then
      return 0
    fi
  fi
  return 1
}

collect_files_all() {
  git ls-files '*.rs'
}

collect_files_staged_or_diff() {
  local target="$1"
  local git_cmd=(git diff --name-status --diff-filter=AMR)

  if [[ "$target" == "staged" ]]; then
    git_cmd+=(--cached)
  else
    git_cmd+=("${DIFF_BASE}...HEAD")
  fi

  git_cmd+=(-- '*.rs')

  "${git_cmd[@]}" | while IFS=$'\t' read -r status p1 p2; do
    case "$status" in
      A|M)
        printf '%s\t%s\n' "$status" "$p1"
        ;;
      R*)
        printf 'M\t%s\n' "$p2"
        ;;
    esac
  done
}

failures=0
checked=0

check_file() {
  local status="$1"
  local file="$2"

  # In --staged mode read the blob from the index so staged content is checked,
  # not the working-tree file (which may differ from what will be committed).
  local content
  if [[ "$MODE" == "staged" ]]; then
    if ! content="$(git show ":$file" 2>/dev/null)"; then
      return  # removed from index; nothing to check
    fi
  else
    if [[ ! -f "$file" ]]; then
      return
    fi
    content="$(cat "$file")"
  fi

  local head8 head12
  head8="$(head -n 8  <<<"$content")"
  head12="$(head -n 12 <<<"$content")"

  if grep -Eq 'Code generated|@generated|DO NOT EDIT' <<<"$head8"; then
    return
  fi

  checked=$((checked + 1))

  if ! grep -Fxq "$LICENSE_ID_LINE" <<<"$head12"; then
    echo "[FAIL] $file: missing SPDX license identifier" >&2
    failures=$((failures + 1))
    return
  fi

  local copyright_line
  copyright_line="$(grep -E '^// Copyright \(c\) ' <<<"$head12" | head -n 1 || true)"

  if [[ -z "$copyright_line" ]]; then
    echo "[FAIL] $file: missing copyright line" >&2
    failures=$((failures + 1))
    return
  fi

  if ! [[ "$copyright_line" =~ $COPYRIGHT_REGEX ]]; then
    echo "[FAIL] $file: invalid copyright format" >&2
    failures=$((failures + 1))
    return
  fi

  if [[ "$status" == "A" || "$status" == "M" ]]; then
    if ! current_year_present "$copyright_line"; then
      echo "[FAIL] $file: changed files must include current year ($CURRENT_YEAR)" >&2
      failures=$((failures + 1))
      return
    fi
  fi
}

if [[ "$MODE" == "all" ]]; then
  while read -r file; do
    [[ -n "$file" ]] || continue
    check_file "ALL" "$file"
  done < <(collect_files_all)
else
  while IFS=$'\t' read -r status file; do
    [[ -n "$file" ]] || continue
    check_file "$status" "$file"
  done < <(collect_files_staged_or_diff "$MODE")
fi

if (( failures > 0 )); then
  echo "Checked $checked file(s): $failures failure(s)." >&2
  exit 1
fi

echo "Rust license header check passed ($checked file(s) checked)."
