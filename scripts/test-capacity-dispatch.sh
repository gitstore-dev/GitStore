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

mkdir -p "${test_dir}/bin" "${test_dir}/readiness-valid" "${test_dir}/readiness-missing"
source_revision="$(git -C "${repo_root}" rev-parse HEAD)"
image_digest="$(printf 'a%.0s' {1..64})"
printf '#!/usr/bin/env bash\ncase "$*" in *api-a*|*api-alias*) if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-a-replacement; else instance=instance-a; fi; printf "process_start_time_seconds 100\\ngitstore_api_process_instance_info{instance_id=\\"%%s\\"} 1\\n" "$instance" ;; *api-b*) if [[ "${MOCK_SAME_START:-0}" == 1 ]]; then start=100; else start=200; fi; if [[ "${MOCK_REPLACE_ALL:-0}" == 1 && -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-b-replacement; else instance=instance-b; fi; printf "process_start_time_seconds %%s\\ngitstore_api_process_instance_info{instance_id=\\"%%s\\"} 1\\n" "$start" "$instance" ;; *) exit 1 ;; esac\n' >"${test_dir}/bin/curl"
chmod +x "${test_dir}/bin/curl"
jq -n --arg revision "${source_revision}" --arg digest "${image_digest}" '[
  {Id:"api-id-1",Name:"/api-1",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"api-id-2",Name:"/api-2",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}}
]' >"${test_dir}/readiness-containers.json"
jq '.[0].Config.Image = "ghcr.io/gitstore-dev/api:latest"' "${test_dir}/readiness-containers.json" >"${test_dir}/unverified-readiness-containers.json"
printf '#!/usr/bin/env bash\nif [[ "$1" == exec ]]; then case "$2" in api-1) if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-a-replacement; else instance=instance-a; fi; printf "process_start_time_seconds 100\\ngitstore_api_process_instance_info{instance_id=\\"%%s\\"} 1\\n" "$instance" ;; api-2) if [[ "${MOCK_SAME_START:-0}" == 1 ]]; then start=100; else start=200; fi; printf "process_start_time_seconds %%s\\ngitstore_api_process_instance_info{instance_id=\\"instance-b\\"} 1\\n" "$start" ;; *) exit 1 ;; esac; else cat "${MOCK_READINESS_CONTAINERS_FILE}"; fi\n' >"${test_dir}/bin/docker"
chmod +x "${test_dir}/bin/docker"
export MOCK_READINESS_CONTAINERS_FILE="${test_dir}/readiness-containers.json"
cp "${repo_root}/tests/capacity/examples/config-manifest.json" "${test_dir}/readiness-valid/config.json"
cp "${repo_root}/tests/capacity/examples/environment-manifest.json" "${test_dir}/readiness-valid/environment.json"
PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 \
  CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid"
jq -e '.passed == true and .topology.apiReplicas == 2 and (.topology.liveReplicas | length) == 2 and .resources.runtimeMemoryBytes == 17179869184' \
  "${test_dir}/readiness-valid/preflight-environment.json" >/dev/null
if PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 \
  CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-missing" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly passed without deployment manifests" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-a.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 \
  CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted one live process twice" >&2
  exit 1
fi
MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid"
if PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://unverified.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted an unrelated workload URL" >&2
  exit 1
fi
if MOCK_READINESS_CONTAINERS_FILE="${test_dir}/unverified-readiness-containers.json" \
  PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted a mutable live image" >&2
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

mkdir -p "${test_dir}/evidence"
printf '#!/usr/bin/env bash\necho "mock capacity command: $*"\nif [[ -n "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE:-}" ]]; then cp "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE}" "${MOCK_SERVICE_CONTAINERS_FILE}"; fi\nif [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" ]]; then touch "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}"; fi\n[[ "${FAIL_MAKE:-0}" != "1" ]]\n' >"${test_dir}/bin/make"
printf '#!/usr/bin/env bash\nif [[ "$1" == exec ]]; then case "$2" in api-1) if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-a-replacement; else instance=instance-a; fi; printf "process_start_time_seconds 100\\ngitstore_api_process_instance_info{instance_id=\\"%%s\\"} 1\\n" "$instance" ;; api-2) if [[ "${MOCK_SAME_START:-0}" == 1 ]]; then start=100; else start=200; fi; if [[ "${MOCK_REPLACE_ALL:-0}" == 1 && -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-b-replacement; else instance=instance-b; fi; printf "process_start_time_seconds %%s\\ngitstore_api_process_instance_info{instance_id=\\"%%s\\"} 1\\n" "$start" "$instance" ;; *) if [[ "${MOCK_SCYLLA_BAD_MEMBERSHIP:-0}" == 1 ]]; then printf "UN node-1\\n"; else printf "UN node-1\\nUN node-2\\nUN node-3\\n"; fi ;; esac; exit 0; fi\nif [[ "$*" == *api-1* ]]; then cat "${MOCK_SERVICE_CONTAINERS_FILE}"; else cat "${MOCK_SCYLLA_CONTAINERS_FILE}"; fi\n' >"${test_dir}/bin/docker"
chmod +x "${test_dir}/bin/make"
chmod +x "${test_dir}/bin/docker"
jq -n --arg revision "${source_revision}" --arg digest "${image_digest}" '[
  {Id:"api-id-1",Name:"/api-1",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"api-id-2",Name:"/api-2",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"git-id-1",Name:"/git-1",Image:("sha256:"+$digest),Path:"/app/git-service",Config:{Image:("ghcr.io/gitstore-dev/git-service@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}}
]' >"${test_dir}/service-containers.json"
jq '.[0].Config.Image = "ghcr.io/gitstore-dev/api:latest"' "${test_dir}/service-containers.json" >"${test_dir}/unverified-service-containers.json"
jq '.[1].Id = .[0].Id' "${test_dir}/service-containers.json" >"${test_dir}/duplicate-service-containers.json"
jq -n '[range(1;4) as $i | {Id:("scylla-id-"+($i|tostring)),Name:("/scylla-"+($i|tostring)),Config:{Image:"scylla",Cmd:["--smp=2"]},RestartCount:0,HostConfig:{Memory:3221225472,NanoCpus:0,CpusetCpus:""},State:{Running:true,OOMKilled:false,StartedAt:"start"}}]' >"${test_dir}/scylla-containers.json"
jq '[.[0], .[0], .[0]]' "${test_dir}/scylla-containers.json" >"${test_dir}/duplicate-scylla-containers.json"
export MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/scylla-containers.json"
export MOCK_REPLACEMENT_MARKER_PREFIX="${test_dir}/replacement"
export NAMESPACE_WATCH_API_REPLACEMENT=http://api-a.internal
export NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE="${test_dir}/replacement-trigger"
PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=test-run \
  "${dispatcher}" namespace validation diagnostic >/dev/null
metadata="${test_dir}/evidence/namespace/validation/diagnostic/test-run/metadata.json"
jq -e '.target == "namespace" and .profile == "validation" and .mode == "diagnostic" and .passed == false and .verifierExitCode == 0' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=missing-manifests \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "alpha non-k6 capacity evidence unexpectedly passed without manifests" >&2
  exit 1
fi

PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=validated-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  "${dispatcher}" namespace validation alpha >/dev/null
metadata="${test_dir}/evidence/namespace/validation/alpha/validated-run/metadata.json"
jq -e '
  .passed == true and .preflightRequired == true and .preflightExitCode == 0 and
  .verifierRequired == true and .verifierExitCode == 0 and
  (.configDigest | length) == 64 and (.environmentDigest | length) == 64
' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_TEST_FORCE_DIRTY=1 \
  CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=dirty-verifier \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "alpha capacity evidence unexpectedly accepted a dirty verifier checkout" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/validation/alpha/dirty-verifier/metadata.json"
jq -e '.passed == false and .worktreeDirty == true and .sourceStateExitCode == 2' "${metadata}" >/dev/null

MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=deployed-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null
metadata="${test_dir}/evidence/namespace/watch/alpha/deployed-run/metadata.json"
jq -e '.passed == true and .postflightRequired == true and .postflightExitCode == 0 and .datastoreVerifierExitCode == 0' "${metadata}" >/dev/null
jq -e '(.topology.liveApiReplicas | length) == 2 and (.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/preflight-environment.json" >/dev/null
jq -e '(.topology.liveApiReplicas | length) == 2 and (.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/postflight-environment.json" >/dev/null
jq -e 'length == 3 and all(.[]; .memoryLimitBytes == 3221225472 and .smpPerNode == 2 and .scyllaLiveNodes == 3)' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/datastore-before.json" >/dev/null

if MOCK_SAME_START=1 MOCK_REPLACE_ALL=1 PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=all-replicas-replaced-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted replacement of every API replica" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/watch/alpha/all-replicas-replaced-run/metadata.json"
jq -e '.passed == false and .postflightExitCode == 1' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=aliased-api-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-alias.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-alias.internal \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted one live API process twice" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=unverified-service-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/unverified-service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted an unverified service image" >&2
  exit 1
fi

cp "${test_dir}/service-containers.json" "${test_dir}/replacement-service-containers.json"
if MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=unverified-replacement-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/replacement-service-containers.json" \
  MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE="${test_dir}/unverified-service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted an unverified replacement image" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/watch/alpha/unverified-replacement-run/metadata.json"
jq -e '.passed == false and .preflightExitCode == 0 and .verifierExitCode == 0 and .postflightExitCode == 1' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=duplicate-service-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/duplicate-service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted duplicate service container identities" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  CAPACITY_DATASTORE_EXPECTED_COUNT=3 CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 \
  CAPACITY_DATASTORE_EXPECTED_SMP=3 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/wrong-smp.json" >/dev/null 2>&1; then
  echo "runtime Scylla SMP mismatch unexpectedly passed" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_BAD_MEMBERSHIP=1 \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 CAPACITY_DATASTORE_EXPECTED_COUNT=3 \
  CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 CAPACITY_DATASTORE_EXPECTED_SMP=2 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/wrong-membership.json" >/dev/null 2>&1; then
  echo "undersized live Scylla membership unexpectedly passed" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/duplicate-scylla-containers.json" \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-1,scylla-1 CAPACITY_DATASTORE_EXPECTED_COUNT=3 \
  CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 CAPACITY_DATASTORE_EXPECTED_SMP=2 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/duplicate-containers.json" >/dev/null 2>&1; then
  echo "duplicate datastore container identities unexpectedly passed" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=skipped-replacement \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT=1 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly skipped rolling replacement" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" FAIL_MAKE=1 CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=failed-verifier \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "failed non-k6 verifier unexpectedly produced passing evidence" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/validation/alpha/failed-verifier/metadata.json"
jq -e '.passed == false and .verifierExitCode == 1' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=unverified-dataset \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" scylla soak production >/dev/null 2>&1; then
  echo "Scylla production evidence unexpectedly passed without observed dataset verification" >&2
  exit 1
fi
metadata="${test_dir}/evidence/scylla/soak/production/unverified-dataset/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY=invalid "${dispatcher}" namespace validation diagnostic >/dev/null 2>&1; then
  echo "invalid capacity observability mode unexpectedly succeeded" >&2
  exit 1
fi

echo "capacity dispatcher tests passed"
