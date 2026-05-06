#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$REPO_ROOT"
git config core.hooksPath .githooks
chmod +x .githooks/pre-commit scripts/check-go-license-headers.sh
chmod +x scripts/check-rust-license-headers.sh

echo "Git hooks installed (core.hooksPath=.githooks)."


