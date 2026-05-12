#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$REPO_ROOT"
chmod +x .githooks/pre-commit
chmod +x .githooks/commit-msg
chmod +x scripts/check-go-license-headers.sh
chmod +x scripts/check-rust-license-headers.sh
chmod +x scripts/check-js-license-headers.sh
cp .githooks/pre-commit .git/hooks/pre-commit
cp .githooks/commit-msg .git/hooks/commit-msg

echo "Git hooks installed."

