#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dispatcher="${repo_root}/scripts/run-make-workflow.sh"
test_dir="${repo_root}/.gitstore/make-workflow-test.$$"
mkdir -p "${repo_root}/.gitstore"
mkdir -m 700 "${test_dir}"
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
assert_dispatch check infrastructure _check-infrastructure
assert_dispatch clean git-data _clean-git-data
assert_dispatch clean controller-checkpoints _clean-controller-checkpoints
assert_dispatch bootstrap all _bootstrap-all
assert_dispatch bootstrap token _bootstrap-token
assert_dispatch bootstrap namespace _bootstrap-namespace
assert_dispatch bootstrap repository _bootstrap-repository
assert_dispatch bootstrap infrastructure _bootstrap-infrastructure
assert_dispatch secret jwt _secret-jwt
assert_dispatch secret grpc-hmac _secret-grpc-hmac
assert_dispatch secret signing-key _secret-signing-key
assert_dispatch generate authz _generate-authz
assert_dispatch generate all _generate-all

api_default="$(make --no-print-directory -C "${repo_root}" -np bootstrap TARGET=token | sed -n '/^API_URL /p')"
[[ "${api_default}" == 'API_URL = http://localhost:4000/admin/graphql' ]]

assert_dev_health_url() {
  local expected="$1" output
  shift
  output="$(env -u API_URL -u API_HEALTH_URL make --no-print-directory -C "${repo_root}" -n dev "$@")"
  if [[ "${output}" != *"curl --silent --fail --output /dev/null \"${expected}\""* ]]; then
    echo "native startup did not poll the expected Admin health URL: ${expected}" >&2
    exit 1
  fi
}
assert_dev_health_url http://localhost:4000/health
assert_dev_health_url http://localhost:4400/health API_URL=http://localhost:4400/admin/graphql
assert_dev_health_url https://api.example.test/prefix/health API_URL=https://api.example.test/prefix/admin/graphql
assert_dev_health_url https://ops.example.test/health \
  API_URL=https://api.example.test/catalog/graphql API_HEALTH_URL=https://ops.example.test/health

for target in compose ps logs stop down; do
  output="$(make --no-print-directory -C "${repo_root}" -n "${target}" OBSERVABILITY=otel)"
  [[ "${output}" == *"--profile observability"* ]]
  [[ "${output}" == *"GITSTORE_OBSERVABILITY__OTLP__URI=http://otel-collector:4318"* ]]
done
output="$(make --no-print-directory -C "${repo_root}" -n compose)"
[[ "${output}" != *"--profile observability"* ]]

bootstrap_output="$(env -u NAMESPACE_TIER make --no-print-directory -C "${repo_root}" -np bootstrap TARGET=namespace | sed -n '/^NAMESPACE_TIER /p')"
[[ "${bootstrap_output}" == "NAMESPACE_TIER = ORGANIZATION" ]]

# CI's shared gitci namespace is not the admin user's personal namespace.
ci_bootstrap_count=0
ci_git_auth_count=0
while IFS= read -r line; do
  if [[ "${line}" == *"run: make bootstrap "* ]]; then
    [[ "${line}" == *"NAMESPACE_TIER=ORGANIZATION"* ]]
    ci_bootstrap_count=$((ci_bootstrap_count + 1))
  fi
  if [[ "${line}" == *"git config --global http.http://localhost:9000/.extraHeader"* ]]; then
    ci_git_auth_count=$((ci_git_auth_count + 1))
  fi
done < "${repo_root}/.github/workflows/ci-integration.yml"
[[ "${ci_bootstrap_count}" -eq 2 ]]
[[ "${ci_git_auth_count}" -eq 2 ]]

for legacy_namespace in controllers ''; do
  if make --no-print-directory -C "${repo_root}" -n controller CONTROLLER_SERVICEACCOUNT_NAMESPACE="${legacy_namespace}" >/dev/null 2>&1; then
    echo "removed CONTROLLER_SERVICEACCOUNT_NAMESPACE was accepted" >&2; exit 1
  fi
done
for target in controller dev enroll-controller-serviceaccount; do
  output="$(make --no-print-directory -C "${repo_root}" -n "${target}")"
  [[ "${output}" != *"--namespace"* && "${output}" != *"SERVICEACCOUNT__NAMESPACE"* ]]
done

# Generation owns both private files; enrollment only registers public material.
signing_root="${test_dir}/controller secrets"
signing_key="${signing_root}/test-controller/privateKey"
signing_record="${signing_root}/test-controller.json"
for attempt in 1 2; do
  output="$(make --no-print-directory -C "${repo_root}" secret TARGET=signing-key \
    CONTROLLER_SECRET_DIR="${signing_root}" CONTROLLER_SECRET_NAME=test-controller \
    CONTROLLER_SERVICEACCOUNT_KEY_ID=test-key)"
  [[ "${output}" != *"PRIVATE KEY"* ]]
  jq -e --rawfile pem "${signing_key}" \
    '.format == "serviceaccount-signing-key/v1" and .values.privateKey == ($pem | @base64) and .values.keyID == ("test-key" | @base64)' \
    "${signing_record}" >/dev/null
  [[ "$(stat -c %a "${signing_key}" 2>/dev/null || stat -f %Lp "${signing_key}")" == 600 ]]
  [[ "$(stat -c %a "${signing_record}" 2>/dev/null || stat -f %Lp "${signing_record}")" == 600 ]]
  if [[ "${attempt}" == 1 ]]; then
    cp "${signing_key}" "${test_dir}/original-key"
    cp "${signing_record}" "${test_dir}/original-record"
  else
    cmp "${signing_key}" "${test_dir}/original-key"
    cmp "${signing_record}" "${test_dir}/original-record"
  fi
done
if make --no-print-directory -C "${repo_root}" secret TARGET=signing-key \
  CONTROLLER_SECRET_DIR="${signing_root}" CONTROLLER_SECRET_NAME=test-controller \
  CONTROLLER_SERVICEACCOUNT_KEY_ID=different-key >/dev/null 2>&1; then
  echo "signing record silently accepted a changed key ID" >&2; exit 1
fi
cmp "${signing_key}" "${test_dir}/original-key"
cmp "${signing_record}" "${test_dir}/original-record"
for target in controller enroll-controller-serviceaccount; do
  output="$(make --no-print-directory -C "${repo_root}" -n "${target}" \
    CONTROLLER_SECRET_DIR="${signing_root}" CONTROLLER_SECRET_NAME=test-controller \
    CONTROLLER_SERVICEACCOUNT_KEY_ID=test-key)"
  [[ "${output}" != *"generate-signing-key"* && "${output}" != *"--record-output-path"* ]]
done
make --no-print-directory -C "${repo_root}" secret TARGET=signing-key \
  DESTINATION_PATH="${test_dir}/custom/private.pem" >/dev/null
[[ -f "${test_dir}/custom/private.pem.json" ]]
make --no-print-directory -C "${repo_root}" secret TARGET=signing-key \
  DESTINATION_PATH="${test_dir}/custom/private.pem" RECORD_OUTPUT_PATH="${test_dir}/records/controller.json" >/dev/null
cmp "${test_dir}/custom/private.pem.json" "${test_dir}/records/controller.json"
make --no-print-directory -C "${repo_root}" secret TARGET=signing-key \
  DESTINATION_PATH="${test_dir}/issuer/private.pem" RECORD_OUTPUT_PATH= >/dev/null
[[ -f "${test_dir}/issuer/private.pem" && ! -e "${test_dir}/issuer/private.pem.json" ]]
make --no-print-directory -C "${repo_root}" secret TARGET=signing-key \
  ROOT="${test_dir}/relative-root" API_DIR="${repo_root}/gitstore-api" \
  DESTINATION_PATH=keys/private.pem RECORD_OUTPUT_PATH=records/controller.json >/dev/null
[[ -f "${test_dir}/relative-root/keys/private.pem" && -f "${test_dir}/relative-root/records/controller.json" ]]
if make --no-print-directory -C "${repo_root}" controller \
  CONTROLLER_SECRET_DIR="${test_dir}/missing-secrets" CONTROLLER_CHECKPOINT_DIR="${test_dir}/empty-checkpoints" \
  >"${test_dir}/missing-record.log" 2>&1; then
  echo "controller started without a signing record" >&2; exit 1
fi
rg -q 'Missing controller signing record' "${test_dir}/missing-record.log"
[[ ! -e "${test_dir}/missing-secrets/controller-manager.json" ]]

# Exercise enrollment and launch dispatch without a live API or controller.
mkdir "${test_dir}/bin"
cat >"${test_dir}/bin/go" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  "run ./cmd/gitctl enroll-serviceaccount "*)
    printf '%s\n' "$@" >"${MOCK_GO_LOG}"
    ;;
  "run ./cmd/controller")
    [[ "${GITSTORE_CONTROLLER__SERVICEACCOUNT__UID}" == fixture-uid ]]
    [[ "${GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__NAME}" == test-controller ]]
    [[ "${GITSTORE_CONTROLLER__SECRET_PROVIDERS__BOOTSTRAP__BASE_PATH}" == "${MOCK_SECRET_DIR}" ]]
    printf 'controller\n' >"${MOCK_GO_LOG}"
    ;;
  *) echo "unexpected Go invocation in mocked enrollment/startup" >&2; exit 1 ;;
esac
MOCK
chmod +x "${test_dir}/bin/go"
for attempt in 1 2; do
  PATH="${test_dir}/bin:${PATH}" MOCK_GO_LOG="${test_dir}/enrollment.args" \
    make --no-print-directory -C "${repo_root}" enroll-controller-serviceaccount \
    CONTROLLER_SECRET_DIR="${signing_root}" CONTROLLER_SECRET_NAME=test-controller \
    CONTROLLER_SERVICEACCOUNT_KEY_ID=test-key API_URL=http://fixture.invalid/graphql \
    DEV_ADMIN_USERNAME=fixture-user DEV_ADMIN_PASSWORD=fixture-password
  rg -q '^--replace-existing-key$' "${test_dir}/enrollment.args"
  rg -q -F -x "${signing_key}" "${test_dir}/enrollment.args"
done
printf 'GITSTORE_CONTROLLER__SERVICEACCOUNT__UID=fixture-uid\n' >"${signing_root}/test-controller/serviceaccount.env"
PATH="${test_dir}/bin:${PATH}" MOCK_GO_LOG="${test_dir}/controller.args" MOCK_SECRET_DIR="${signing_root}" \
  make --no-print-directory -C "${repo_root}" controller CONTROLLER_SERVICEACCOUNT_UID= \
  CONTROLLER_SECRET_DIR="${signing_root}" CONTROLLER_SECRET_NAME=test-controller \
  CONTROLLER_CHECKPOINT_DIR="${test_dir}/mock-checkpoints"
[[ "$(cat "${test_dir}/controller.args")" == controller ]]
cmp "${signing_key}" "${test_dir}/original-key"
cmp "${signing_record}" "${test_dir}/original-record"

# The parent test target exports its bootstrap namespace; this case tests an
# invocation without an operator-supplied namespace.
role_output="$(env -u NAMESPACE make --no-print-directory -C "${repo_root}" add-role ROLE=reader ALLOW=product.read SCOPE=namespace WORKFLOW_DRY_RUN=1)"
[[ "${role_output}" == *"--scope namespace"* && "${role_output}" != *"--namespace"* ]]
binding_output="$(make --no-print-directory -C "${repo_root}" assign-role ROLE=reader SUBJECT=bot SUBJECT_KIND=ServiceAccount ROLE_KIND=Role SCOPE=namespace NAMESPACE=acme WORKFLOW_DRY_RUN=1)"
[[ "${binding_output}" == *"--namespace acme"* && "${binding_output}" == *"--subject-kind ServiceAccount"* && "${binding_output}" == *"--role-kind Role"* ]]
cluster_output="$(env -u NAMESPACE make --no-print-directory -C "${repo_root}" assign-role ROLE=reader SUBJECT=alice WORKFLOW_DRY_RUN=1)"
[[ "${cluster_output}" == *"--scope cluster"* && "${cluster_output}" != *"--namespace"* ]]
if env -u NAMESPACE make --no-print-directory -C "${repo_root}" assign-role ROLE=reader SUBJECT=alice SCOPE=namespace WORKFLOW_DRY_RUN=1 >/dev/null 2>&1; then
  echo "namespace binding silently used bootstrap NAMESPACE" >&2; exit 1
fi
if make --no-print-directory -C "${repo_root}" add-role ROLE=reader ALLOW=product.read DENY= WORKFLOW_DRY_RUN=1 >/dev/null 2>&1; then
  echo "DENY variable was accepted" >&2; exit 1
fi
if make --no-print-directory -C "${repo_root}" add-role ROLE=reader ALLOW=product.read NAMESPACE=acme WORKFLOW_DRY_RUN=1 >/dev/null 2>&1; then
  echo "role definition accepted a namespace" >&2; exit 1
fi

for invalid in check/unknown clean/all bootstrap/unknown secret/hmac generate/unknown; do
  if WORKFLOW_DRY_RUN=1 "${dispatcher}" "${invalid%/*}" "${invalid#*/}" >/dev/null 2>&1; then
    echo "invalid workflow ${invalid} unexpectedly succeeded" >&2
    exit 1
  fi
done

for public_target in check clean bootstrap secret generate; do
  case "${public_target}" in
    check|bootstrap) selector=all ;;
    clean) selector=git-data ;;
    secret) selector=jwt ;;
    generate) selector=authz ;;
  esac
  output="$(make --no-print-directory -C "${repo_root}" "${public_target}" TARGET="${selector}" WORKFLOW_DRY_RUN=1)"
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
   "${all_tests}" == *"test-repository-capacity-stack.sh"* && "${all_tests}" == *"TestSecretCapacity"* &&
   "${all_tests}" == *"CapacityPush"* && "${all_tests}" == *"EndpointSplit"* &&
   "${all_tests}" == *"RepositoryController"* ]]
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
