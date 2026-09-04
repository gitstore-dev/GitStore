#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dispatcher="${repo_root}/scripts/run-make-workflow.sh"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

assert_dispatch() {
  local family="$1" target="$2" expected="$3"
  local output
  output="$(WORKFLOW_DRY_RUN=1 "${dispatcher}" "${family}" "${target}")"
  [[ "${output}" == *"family=${family} target=${target}"* ]]
  [[ "${output}" == *"${expected}"* ]]
}

assert_dispatch check all _check-all
assert_dispatch check config _check-local-config
assert_dispatch check compose _check-compose-config
assert_dispatch check licenses _check-licenses
assert_dispatch check credentials _check-credentials
assert_dispatch clean git-data _clean-git-data
assert_dispatch clean controller-checkpoints _clean-controller-checkpoints
assert_dispatch bootstrap all _bootstrap-all
assert_dispatch bootstrap token _bootstrap-token
assert_dispatch bootstrap namespace _bootstrap-namespace
assert_dispatch bootstrap repository _bootstrap-repository
assert_dispatch secret jwt _secret-jwt
assert_dispatch secret grpc-hmac _secret-grpc-hmac

for invalid in check/unknown clean/all bootstrap/unknown secret/hmac; do
  if WORKFLOW_DRY_RUN=1 "${dispatcher}" "${invalid%/*}" "${invalid#*/}" >/dev/null 2>&1; then
    echo "invalid workflow ${invalid} unexpectedly succeeded" >&2
    exit 1
  fi
done

for public_target in check clean bootstrap secret; do
  output="$(make --no-print-directory -C "${repo_root}" "${public_target}" TARGET="$({
    case "${public_target}" in
      check) printf all ;;
      clean) printf git-data ;;
      bootstrap) printf all ;;
      secret) printf jwt ;;
    esac
  })" WORKFLOW_DRY_RUN=1)"
  [[ "${output}" == *"family=${public_target}"* ]]
done

for removed_target in validate-local-config compose-config-check license-check credential-output-check credential-leakage-check git-clean-data bootstrap-token bootstrap-namespace bootstrap-repository gen-jwt-secret gen-hmac-secret; do
  if rg -q "^${removed_target}:" "${repo_root}/Makefile"; then
    echo "removed public target ${removed_target} is still defined" >&2
    exit 1
  fi
done

mkdir -p "${test_dir}/git-data" "${test_dir}/controller-checkpoints" "${test_dir}/preserved"
touch "${test_dir}/git-data/repository" "${test_dir}/controller-checkpoints/cursor" "${test_dir}/preserved/sentinel"

if make --no-print-directory -C "${repo_root}" clean TARGET=git-data GIT_DATA_DIR="${test_dir}/git-data" >/dev/null 2>&1; then
  echo "git-data cleanup unexpectedly ran without CONFIRM=1" >&2
  exit 1
fi
[[ -f "${test_dir}/git-data/repository" ]]

clean_output="$(make --no-print-directory -C "${repo_root}" clean TARGET=git-data CONFIRM=1 GIT_DATA_DIR="${test_dir}/git-data")"
[[ "${clean_output}" == *"Removing Git data only: ${test_dir}/git-data"* ]]
[[ ! -e "${test_dir}/git-data" ]]
[[ -f "${test_dir}/preserved/sentinel" ]]

clean_output="$(make --no-print-directory -C "${repo_root}" clean TARGET=controller-checkpoints CONFIRM=1 CONTROLLER_CHECKPOINT_DIR="${test_dir}/controller-checkpoints")"
[[ "${clean_output}" == *"Removing controller checkpoints only: ${test_dir}/controller-checkpoints"* ]]
[[ ! -e "${test_dir}/controller-checkpoints" ]]
[[ -f "${test_dir}/preserved/sentinel" ]]

if make --no-print-directory -C "${repo_root}" clean TARGET=git-data CONFIRM=1 GIT_DATA_DIR="${repo_root}" >/dev/null 2>&1; then
  echo "unsafe repository-root cleanup unexpectedly succeeded" >&2
  exit 1
fi

echo "Make workflow dispatcher tests passed"
