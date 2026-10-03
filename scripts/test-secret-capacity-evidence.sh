#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT
dispatcher="$repo_root/scripts/run-capacity-target.sh"
for value in true -1 2; do
  if REPOSITORY_CAPACITY_SECRET_SCENARIO="$value" CAPACITY_DRY_RUN=1 "$dispatcher" repository lifecycle diagnostic >"$test_dir/output" 2>&1; then
    echo "invalid secret scenario flag accepted" >&2
    exit 1
  fi
done
if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "$dispatcher" namespace watch diagnostic >"$test_dir/output" 2>&1; then
  echo "secret scenario routed to an unrelated profile" >&2
  exit 1
fi
REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "$dispatcher" repository lifecycle diagnostic >"$test_dir/output"
grep -q 'REPOSITORY_CAPACITY_SECRET_SCENARIO=1' "$test_dir/output"
if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$repo_root/scripts/repository-capacity-stack.sh" local-alpha >"$test_dir/output" 2>&1; then
  echo "managed stack accepted an unimplemented secret workload" >&2
  exit 1
fi
grep -q 'refusing to start' "$test_dir/output"
if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$dispatcher" repository lifecycle diagnostic >"$test_dir/output" 2>&1; then
  echo "secret scenario ran the existing Repository-only verifier" >&2
  exit 1
fi
grep -q 'not implemented' "$test_dir/output"

mkdir "$test_dir/evidence"
if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$repo_root/scripts/validate-capacity-evidence.sh" "$test_dir/evidence" repository lifecycle diagnostic >"$test_dir/output" 2>&1; then
  echo "secret scenario accepted without its implemented domain verifier" >&2
  exit 1
fi

if CHAOS_CONFIRM=0 CHAOS_TARGET=gitstore-controller-test "$repo_root/scripts/run-chaos.sh" controller-restart >"$test_dir/output" 2>&1; then
  echo "controller restart accepted without confirmation" >&2
  exit 1
fi
grep -q 'CHAOS_CONFIRM=1' "$test_dir/output"
if CHAOS_CONFIRM=1 CHAOS_TARGET='gitstore-*' "$repo_root/scripts/run-chaos.sh" controller-restart >"$test_dir/output" 2>&1; then
  echo "controller restart accepted a wildcard target" >&2
  exit 1
fi
grep -q 'one explicit' "$test_dir/output"
jq -e '.action == "restart" and .expectedRecovery == "60s"' "$repo_root/tests/chaos/profiles/controller-restart.json" >/dev/null
echo "secret capacity dispatch and fail-closed preflight checks passed"
