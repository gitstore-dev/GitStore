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
Usage: scripts/check-js-license-headers.sh [--all | --staged | --diff-base <ref>]

Checks JavaScript/TypeScript files for required license headers.

Modes:
  --all               Check all tracked JS/TS files in the repository (default)
  --staged            Check only staged added/modified JS/TS files
  --diff-base <ref>   Check added/modified JS/TS files in <ref>...HEAD

Checked extensions: .ts, .tsx, .js, .jsx, .mjs, .cjs

Rules:
  1. All checked files must include:
     // SPDX-License-Identifier: AGPL-3.0-or-later
     // Copyright (c) <year or year-year> GitStore contributors
  2. For changed files (--staged / --diff-base), the copyright year must include
     the current year.

Generated files (containing "Code generated", "AUTO-GENERATED", or
"DO NOT EDIT" in the first 12 lines) are skipped.
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

is_generated_file() {
  local file="$1"
  local head_lines
  head_lines="$(head -n 12 "$file" 2>/dev/null || true)"
  grep -Eqi 'Code generated|AUTO-GENERATED|DO NOT EDIT' <<<"$head_lines"
}

extract_copyright_line() {
  local file="$1"
  head -n 14 "$file" | grep -E '^// Copyright \(c\) ' | head -n 1 || true
}

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
  git ls-files '*.ts' '*.tsx' '*.js' '*.jsx' '*.mjs' '*.cjs'
}

collect_files_staged_or_diff() {
  local target="$1"
  local git_cmd=(git diff --name-status --diff-filter=AMR)

  if [[ "$target" == "staged" ]]; then
    git_cmd+=(--cached)
  else
    git_cmd+=("${DIFF_BASE}...HEAD")
  fi

  git_cmd+=(-- '*.ts' '*.tsx' '*.js' '*.jsx' '*.mjs' '*.cjs')

  "${git_cmd[@]}" | while read -r status p1 p2; do
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

  if [[ ! -f "$file" ]]; then
    return
  fi

  if is_generated_file "$file"; then
    return
  fi

  checked=$((checked + 1))

  if ! head -n 14 "$file" | grep -Fxq "$LICENSE_ID_LINE"; then
    echo "[FAIL] $file: missing SPDX license identifier" >&2
    failures=$((failures + 1))
    return
  fi

  local copyright_line
  copyright_line="$(extract_copyright_line "$file")"

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

echo "JS/TS license header check passed ($checked file(s) checked)."

