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

for removed_target in validate-local-config compose-config-check license-check credential-output-check credential-leakage-check git-clean-data bootstrap-token bootstrap-namespace bootstrap-repository gen-jwt-secret gen-hmac-secret capacity-dispatch-test test-datastore-contracts test-scylla-integration test-secret-integration; do
  if rg -q "^${removed_target}:" "${repo_root}/Makefile"; then
    echo "removed public target ${removed_target} is still defined" >&2
    exit 1
  fi
done

all_tests="$(env -u TARGET -u MAKEFLAGS -u MFLAGS -u MAKEOVERRIDES make --no-print-directory -C "${repo_root}" -n test)"
[[ "${all_tests}" == *"cargo test"* && "${all_tests}" == *"test-secret-config.sh"* &&
   "${all_tests}" == *"test-repository-capacity-stack.sh"* && "${all_tests}" == *"TestSecretCapacity"* ]]
[[ "${all_tests}" != *"SECRET_TEST_RUN=1"* && "${all_tests}" != *"-tags scylla"* ]]
explicit_all="$(env -u MAKEFLAGS -u MFLAGS -u MAKEOVERRIDES make --no-print-directory -C "${repo_root}" -n test TARGET=all)"
[[ "${all_tests}" == "${explicit_all}" ]]
datastore_tests="$(make --no-print-directory -C "${repo_root}" -n test TARGET=datastore DATASTORE=memdb)"
[[ "${datastore_tests}" == *"-tags memdb"* && "${datastore_tests}" != *"-tags scylla"* ]]
scylla_tests="$(make --no-print-directory -C "${repo_root}" -n test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=127.0.0.1:9142)"
[[ "${scylla_tests}" == *"-tags scylla"* && "${scylla_tests}" == *"127.0.0.1:9142"* ]]
secret_tests="$(make --no-print-directory -C "${repo_root}" -n test TARGET=secret-integration)"
[[ "${secret_tests}" == *"SECRET_TEST_OWNED_DEPLOYMENT=1 is required"* &&
   "${secret_tests}" == *"TestSecretBootstrapRotationDeployed"* ]]
pr_ready="$(make --no-print-directory -C "${repo_root}" -n pr-ready TARGET=datastore)"
[[ "${pr_ready}" == *"cargo test"* && "${pr_ready}" == *"test-secret-config.sh"* ]]
for invalid in unknown api capacity ""; do
  if make --no-print-directory -C "${repo_root}" test TARGET="${invalid}" >"${test_dir}/invalid-test.log" 2>&1; then
    echo "test accepted an invalid TARGET=${invalid}" >&2
    exit 1
  fi
done
if make --no-print-directory -C "${repo_root}" test TARGET=datastore DATASTORE=unknown >/dev/null 2>&1; then
  echo "datastore tests accepted an unknown backend" >&2
  exit 1
fi
if make --no-print-directory -C "${repo_root}" test TARGET=secret-integration SECRET_TEST_OWNED_DEPLOYMENT=0 >/dev/null 2>&1; then
  echo "secret integration tests ran without explicit deployment ownership" >&2
  exit 1
fi

printf '[api\nport = 4000\n' >"${test_dir}/invalid-config.toml"
if make --no-print-directory -C "${repo_root}" check TARGET=config \
  CONFIG_FILE="${test_dir}/invalid-config.toml" POLICY_FILE="${repo_root}/config/policy.yaml" >/dev/null 2>&1; then
  echo "config check unexpectedly accepted malformed TOML" >&2
  exit 1
fi
printf 'version: v1\nroles: [\n' >"${test_dir}/invalid-policy.yaml"
if make --no-print-directory -C "${repo_root}" check TARGET=config \
  CONFIG_FILE="${repo_root}/config/config.toml" POLICY_FILE="${test_dir}/invalid-policy.yaml" >/dev/null 2>&1; then
  echo "config check unexpectedly accepted malformed YAML" >&2
  exit 1
fi

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
