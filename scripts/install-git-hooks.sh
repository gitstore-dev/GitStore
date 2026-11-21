#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$REPO_ROOT"
if git config --get core.hooksPath >/dev/null; then
  echo "error: core.hooksPath is configured; review existing hook ownership before installing." >&2
  exit 1
fi

HOOKS_DIR="$(git rev-parse --git-path hooks)"
for hook in pre-commit commit-msg; do
  destination="$HOOKS_DIR/$hook"
  if [[ -e "$destination" || -L "$destination" ]]; then
    if [[ -L "$destination" || ! -f "$destination" ]] ||
        ! cmp -s ".githooks/$hook" "$destination"; then
      echo "error: refusing to replace existing hook: $destination; review and preserve it first." >&2
      exit 1
    fi
  fi
done

echo "Installing clone-shared hooks in $HOOKS_DIR; this updates all linked worktrees." >&2
mkdir -p "$HOOKS_DIR"
chmod +x .githooks/pre-commit
chmod +x .githooks/commit-msg
chmod +x scripts/check-license-headers.sh
cp .githooks/pre-commit "$HOOKS_DIR/pre-commit"
cp .githooks/commit-msg "$HOOKS_DIR/commit-msg"

echo "Git hooks installed."

