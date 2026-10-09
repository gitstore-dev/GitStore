#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

readonly source_file="gitstore-api/cmd/gitctl/enroll_serviceaccount.go"

if grep -nE 'fmt\.(Fprint|Fprintf|Fprintln)\((stdout|stderr).*(adminToken|privateKey|publicKey|token)' "$source_file"; then
  echo "enroll-serviceaccount must not write credential material to stdout or stderr" >&2
  exit 1
fi

(
  cd gitstore-api
  go test ./cmd/gitctl -run '^TestEnrollServiceAccount' -count=1
)

echo "enroll-serviceaccount output leakage check passed"
