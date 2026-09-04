#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dispatcher="${repo_root}/scripts/run-capacity-target.sh"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

assert_dispatch() {
  local target="$1" profile="$2" expected="$3"
  local output
  output="$(CAPACITY_DRY_RUN=1 "${dispatcher}" "${target}" "${profile}" alpha)"
  [[ "${output}" == *"target=${target} profile=${profile} mode=alpha"* ]]
  [[ "${output}" == *"MODE=alpha"* ]]
  [[ "${output}" == *"${expected}"* ]]
}

assert_dispatch api readiness '_capacity-k6'
assert_dispatch namespace admission 'CAPACITY_PROFILE=namespace-admission'
assert_dispatch namespace validation '_capacity-namespace-admission'
assert_dispatch namespace watch '_capacity-namespace-watch'
assert_dispatch namespace recovery '_capacity-namespace-recovery'
assert_dispatch scylla soak '_capacity-scylla-soak'

if CAPACITY_DRY_RUN=1 "${dispatcher}" namespace unknown alpha >/dev/null 2>&1; then
  echo "invalid target/profile combination unexpectedly succeeded" >&2
  exit 1
fi
if CAPACITY_DRY_RUN=1 "${dispatcher}" namespace watch gate >/dev/null 2>&1; then
  echo "legacy gate mode unexpectedly succeeded" >&2
  exit 1
fi

make_output="$(make --no-print-directory -C "${repo_root}" capacity TARGET=namespace PROFILE=watch MODE=alpha CAPACITY_DRY_RUN=1)"
[[ "${make_output}" == *"target=namespace profile=watch mode=alpha"* ]]

for removed_target in capacity-observability capacity-observability-down test-scylla-capacity test-namespace-admission-capacity test-namespace-watch-capacity test-namespace-watch-recovery; do
  if rg -q "^${removed_target}:" "${repo_root}/Makefile"; then
    echo "removed public target ${removed_target} is still defined" >&2
    exit 1
  fi
done

mkdir -p "${test_dir}/bin" "${test_dir}/evidence"
printf '#!/usr/bin/env bash\necho "mock capacity command: $*"\n' >"${test_dir}/bin/make"
chmod +x "${test_dir}/bin/make"
PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=test-run \
  "${dispatcher}" namespace validation diagnostic >/dev/null
metadata="${test_dir}/evidence/namespace/validation/diagnostic/test-run/metadata.json"
jq -e '.target == "namespace" and .profile == "validation" and .mode == "diagnostic" and .passed == false' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY=invalid "${dispatcher}" namespace validation diagnostic >/dev/null 2>&1; then
  echo "invalid capacity observability mode unexpectedly succeeded" >&2
  exit 1
fi

echo "capacity dispatcher tests passed"
