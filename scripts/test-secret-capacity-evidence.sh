#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${repo_root}/scripts/test-fixtures/shell.sh"
shell_test_setup secret-capacity-evidence
dispatcher="$repo_root/scripts/run-capacity-target.sh"
for value in true -1 2; do
  assert_rejected "invalid secret scenario flag accepted" "$test_dir/output" \
    env REPOSITORY_CAPACITY_SECRET_SCENARIO="$value" CAPACITY_DRY_RUN=1 "$dispatcher" repository lifecycle diagnostic
done
assert_rejected "secret scenario routed to an unrelated profile" "$test_dir/output" \
  env REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "$dispatcher" namespace watch diagnostic
REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "$dispatcher" repository lifecycle diagnostic >"$test_dir/output"
grep -q 'REPOSITORY_CAPACITY_SECRET_SCENARIO=1' "$test_dir/output"
assert_rejected "managed stack accepted secret faults without approval" "$test_dir/output" \
  env CHAOS_CONFIRM=0 REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$repo_root/scripts/repository-capacity-stack.sh" local-alpha
grep -q 'CHAOS_CONFIRM=1' "$test_dir/output"
assert_rejected "secret scenario ran the existing Repository-only verifier" "$test_dir/output" \
  env CHAOS_CONFIRM=0 REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$dispatcher" repository lifecycle diagnostic
grep -q 'provisioned explicitly owned deployment' "$test_dir/output"

mkdir "$test_dir/evidence"
printf '%s\n' '{"schemaVersion":1,"scenario":"repository-lifecycle-secrets/v1"}' >"$test_dir/evidence/secret-evidence.json"
assert_rejected "secret artifacts accepted as Repository-only evidence without the scenario flag" "$test_dir/output" \
  env REPOSITORY_CAPACITY_SECRET_SCENARIO=0 "$repo_root/scripts/validate-capacity-evidence.sh" "$test_dir/evidence" repository lifecycle diagnostic
grep -q 'requires REPOSITORY_CAPACITY_SECRET_SCENARIO=1' "$test_dir/output"
rm "$test_dir/evidence/secret-evidence.json"
assert_rejected "secret scenario accepted without owned fixtures and fault approval" "$test_dir/output" \
  env REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$repo_root/scripts/validate-capacity-evidence.sh" "$test_dir/evidence" repository lifecycle diagnostic

for phase in preflight postflight; do
  for mode in diagnostic alpha production; do
    assert_rejected "secret scenario without prerequisites accepted during $phase in $mode" "$test_dir/output" \
      env REPOSITORY_CAPACITY_SECRET_SCENARIO=1 "$repo_root/scripts/validate-capacity-evidence.sh" "$test_dir/evidence" repository lifecycle "$mode" "$phase"
    [[ ! -e "$test_dir/evidence/$phase-environment.json" ]] || {
      echo "failed secret scenario emitted success-shaped environment evidence" >&2
      exit 1
    }
  done
done

assert_rejected "controller restart accepted without confirmation" "$test_dir/output" \
  env CHAOS_CONFIRM=0 CHAOS_TARGET=gitstore-controller-test "$repo_root/scripts/run-chaos.sh" controller-restart
grep -q 'CHAOS_CONFIRM=1' "$test_dir/output"
assert_rejected "controller restart accepted a wildcard target" "$test_dir/output" \
  env CHAOS_CONFIRM=1 CHAOS_TARGET='gitstore-*' "$repo_root/scripts/run-chaos.sh" controller-restart
grep -q 'one explicit' "$test_dir/output"
jq -e '.action == "restart" and .expectedRecovery == "60s"' "$repo_root/tests/chaos/profiles/controller-restart.json" >/dev/null
echo "secret capacity dispatch and fail-closed preflight checks passed"
