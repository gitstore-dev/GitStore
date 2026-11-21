#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${root}/gitstore-api"
scratch="$(mktemp -d)"
trap 'rm -rf "${scratch}"' EXIT
snapshot() {
  while IFS= read -r path; do shasum -a 256 "${path}"; done < <(rg --files internal/admin/graph internal/storefront/graph | LC_ALL=C sort)
}
snapshot >"${scratch}/before"
go generate ./...
snapshot >"${scratch}/after"
if ! cmp -s "${scratch}/before" "${scratch}/after"; then
  echo 'GraphQL generated sources were stale; regenerated Admin and Storefront outputs must be reviewed.' >&2
  diff -u "${scratch}/before" "${scratch}/after" >&2 || true
  exit 1
fi
