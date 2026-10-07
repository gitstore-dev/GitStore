#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
readonly pattern='zap\.(String|Stringer|ByteString|Binary|Any|Reflect|Object|Objects|Inline|Dict)[[:space:]]*\([^)]*(token|assertion|private[[:space:]_-]*key|signing[[:space:]_-]*key)'

cd "$repo_root"

matches="$(
  find \
    gitstore-api/internal/auth/provider/serviceaccountassertion \
    gitstore-api/internal/auth/provider/serviceaccountjwt \
    gitstore-controller-manager/internal/graphqlclient \
    -type f -name '*.go' ! -name '*_test.go' \
    -exec grep -Ein "$pattern" {} + || true
)"

if [[ -n "$matches" ]]; then
  echo "credential/log leakage check failed: raw credential material must not be passed to zap." >&2
  echo "$matches" >&2
  exit 1
fi

echo "credential/log leakage check passed"
