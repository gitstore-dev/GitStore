#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -n "${CAPACITY_TOKEN_FILE:-}" ]]; then
  if [[ ! -r "${CAPACITY_TOKEN_FILE}" ]]; then
    echo "CAPACITY_TOKEN_FILE is not readable" >&2
    exit 2
  fi
  CAPACITY_TOKEN="$(<"${CAPACITY_TOKEN_FILE}")"
  export CAPACITY_TOKEN
fi
profile="${1:-${CAPACITY_PROFILE:-}}"
mode="${MODE:-diagnostic}"
case "${mode}" in
  diagnostic|alpha|production) ;;
  *) echo "MODE must be diagnostic, alpha, or production" >&2; exit 2 ;;
esac
if [[ -z "${profile}" ]]; then
  echo "usage: run-capacity.sh <internal-k6-profile>" >&2
  exit 2
fi
if [[ ! "${profile}" =~ ^[a-z0-9][a-z0-9-]*$ ]]; then
  echo "capacity profile must contain only lowercase letters, digits, and hyphens" >&2
  exit 2
fi

script="${repo_root}/tests/capacity/profiles/${profile}.js"
if [[ ! -f "${script}" ]]; then
  echo "unknown capacity profile: ${profile}" >&2
  exit 2
fi

k6_image="${K6_IMAGE:-grafana/k6:2.1.0}"
run_id="${CAPACITY_RUN_ID:-$(date -u +%Y%m%dt%H%M%Sz)}"
evidence_root="${CAPACITY_EVIDENCE_DIR:-${repo_root}/.gitstore/capacity}"
target="${CAPACITY_TARGET:-api}"
scenario="${CAPACITY_SCENARIO:-readiness}"
evidence_dir="${evidence_root}/${target}/${scenario}/${mode}/${run_id}"
mkdir -p "${evidence_dir}"
if [[ "${profile}" == api-endpoint-split && -e "${evidence_dir}/metadata.json" ]]; then
  echo 'endpoint-split evidence requires a fresh run ID' >&2;exit 2
fi

for manifest_kind in config environment; do
  case "${manifest_kind}" in
    config) variable_name="CAPACITY_CONFIG_MANIFEST" ;;
    environment) variable_name="CAPACITY_ENVIRONMENT_MANIFEST" ;;
  esac
  manifest_path="${!variable_name:-}"
  if [[ -n "${manifest_path}" ]]; then
    if [[ ! -r "${manifest_path}" ]] || ! jq empty "${manifest_path}"; then
      echo "${variable_name} must identify a readable JSON document" >&2
      exit 2
    fi
    jq -S . "${manifest_path}" >"${evidence_dir}/${manifest_kind}.json"
  fi
done

metadata="${evidence_dir}/metadata.json"
source_state_sha256="$({
  git status --porcelain=v1
  git diff --binary HEAD
  while IFS= read -r -d '' untracked; do shasum -a 256 "${untracked}"; done < <(git ls-files --others --exclude-standard -z)
} | shasum -a 256 | awk '{print $1}')"
git_dirty=false
if [[ "${CAPACITY_TEST_FORCE_DIRTY:-0}" == "1" || -n "$(git -C "${repo_root}" status --porcelain --untracked-files=normal)" ]]; then
  git_dirty=true
fi
jq -n \
  --arg schema_version "1" \
  --arg profile "${profile}" \
  --arg target "${target}" \
  --arg scenario "${scenario}" \
  --arg mode "${mode}" \
  --arg run_id "${run_id}" \
  --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg git_revision "$(git -C "${repo_root}" rev-parse HEAD)" \
  --arg source_state_sha256 "${source_state_sha256}" \
  --argjson git_dirty "${git_dirty}" \
  --arg k6_image "${k6_image}" \
  --arg config_digest "$(if [[ -f "${evidence_dir}/config.json" ]]; then shasum -a 256 "${evidence_dir}/config.json" | awk '{print $1}'; fi)" \
  --arg environment_digest "$(if [[ -f "${evidence_dir}/environment.json" ]]; then shasum -a 256 "${evidence_dir}/environment.json" | awk '{print $1}'; fi)" \
  '{schemaVersion:($schema_version|tonumber), target:$target, scenario:$scenario, profile:$profile, mode:$mode, evidenceClass:$mode, productionTargets:{visibilityP95Seconds:1,visibilityP99Seconds:3}, runId:$run_id, startedAt:$started_at, gitRevision:$git_revision, gitDirty:$git_dirty, loadGenerator:{name:"k6", image:$k6_image}} + (if $config_digest == "" then {} else {configDigest:$config_digest} end) + (if $environment_digest == "" then {} else {environmentDigest:$environment_digest} end)' \
  >"${metadata}"
jq --arg source "${source_state_sha256}" '. + {sourceStateSha256:$source}' "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"

if [[ "${mode}" != "diagnostic" && "${git_dirty}" == "true" ]]; then
  echo "${mode} capacity evidence requires a clean verifier checkout" | tee "${evidence_dir}/source-state.log" >&2
  jq --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '. + {completedAt:$completed_at,exitCode:2,sourceStateExitCode:2,passed:false}' \
    "${metadata}" >"${metadata}.tmp"
  mv "${metadata}.tmp" "${metadata}"
  echo "capacity evidence: ${evidence_dir}"
  exit 2
fi

export CAPACITY_EVIDENCE_DIR="${evidence_dir}"
export CAPACITY_RUN_ID="${run_id}"

container_check_status=0
if [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]]; then
  if [[ -r "${evidence_dir}/environment.json" ]]; then
    export CAPACITY_DATASTORE_EXPECTED_COUNT="$(jq -r '.topology.scyllaNodes // 0' "${evidence_dir}/environment.json")"
    export CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES="$(jq -r '.datastore.memoryBytesPerNode // 0' "${evidence_dir}/environment.json")"
  fi
  if [[ -r "${evidence_dir}/config.json" ]]; then
    export CAPACITY_DATASTORE_EXPECTED_SMP="$(jq -r '.services.scylla.smpPerNode // 0' "${evidence_dir}/config.json")"
  fi
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${evidence_dir}/datastore-before.json"
fi

preflight="${repo_root}/tests/capacity/preflight/${profile}.sh"
if [[ -x "${preflight}" ]]; then
  "${preflight}" "${evidence_dir}" 2>&1 | tee "${evidence_dir}/preflight.log"
elif [[ "${mode}" != "diagnostic" ]]; then
  echo "gate profile requires an executable environment preflight: ${preflight}" >&2
  exit 2
fi

chaos_pid=""
profile_children=()
cleanup_profile_children() {
  for child in "${profile_children[@]}"; do kill -TERM "${child}" 2>/dev/null || true; done
  for child in "${profile_children[@]}"; do wait "${child}" 2>/dev/null || true; done
  if [[ -n "${chaos_pid}" ]]; then
    kill -TERM "${chaos_pid}" 2>/dev/null || true
    wait "${chaos_pid}" 2>/dev/null || true
  fi
}
trap cleanup_profile_children EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ "${profile}" == api-endpoint-split && "${mode}" != diagnostic ]]; then
  connected_dir="$(cd "${evidence_dir}" && pwd)/connected"
  mkdir -p "${connected_dir}"
  export CAPACITY_CONNECTED_EVIDENCE="${connected_dir}"
  export CAPACITY_CONNECTED_READY_FILE="${connected_dir}/ready.json"
  export CAPACITY_CONNECTED_STARTED_FILE="${connected_dir}/started.json"
  export CAPACITY_PUSH_READY_FILE="${connected_dir}/push-ready.json"
  export CAPACITY_PUSH_STARTED_FILE="${connected_dir}/push-started.json"
  export CAPACITY_LOAD_START_FILE="${connected_dir}/load-start.json"
  export CAPACITY_SOURCE_STATE_SHA256="${source_state_sha256}"
  export CAPACITY_SOURCE_REVISION="$(git -C "${repo_root}" rev-parse HEAD)"
  export CAPACITY_CONFIG_DIGEST="$(jq -r '.configDigest' "${metadata}")"
  export CAPACITY_ENVIRONMENT_DIGEST="$(jq -r '.environmentDigest' "${metadata}")"
  (cd "${repo_root}/gitstore-api" && CAPACITY_FIXTURE_EVIDENCE_FILE="${connected_dir%/connected}/publication-fixture.json" go test -count=1 -run '^TestStorefrontPublicationIdentityFixture$' ./internal/storefront)
  export REPOSITORY_TOKEN_FILE="${REPOSITORY_TOKEN_FILE:-${CAPACITY_TOKEN_FILE:-}}"
  [[ -n "${GIT_URL:-}" ]] || { echo 'explicit owned GIT_URL is required for connected deletion/push parity' >&2;exit 2; }
  [[ -n "${CAPACITY_PUSH_REMOTE:-}" && -n "${CAPACITY_PUSH_REPOSITORY:-}" && -n "${CAPACITY_PUSH_TOKEN_FILE:-${CAPACITY_TOKEN_FILE:-}}" ]] || { echo 'owned push remote/repository and token file are required' >&2;exit 2; }
  [[ "${CAPACITY_PUSH_NAMESPACE:-}" == endpoint-split-* && "${CAPACITY_PUSH_NAMESPACE}" == "${REPOSITORY_CAPACITY_NAMESPACE:-}" ]] || { echo 'push and management fixtures must use the same explicit endpoint-split-* test namespace' >&2;exit 2; }
  [[ -n "${REPOSITORY_REPLACEMENT_TRIGGER_FILE:-}" ]] || { echo 'owned API replacement watcher/trigger is required' >&2;exit 2; }
  (cd "${repo_root}/tests/integration" && GOWORK=off go test -c -o "${connected_dir}/workload.test" .)
  API_ENDPOINT_SPLIT_CAPACITY_RUN=1 CAPACITY_EVIDENCE_DIR="${connected_dir}" \
    python3 "${repo_root}/scripts/capacity-child.py" 4500 "${connected_dir}/workload.log" \
      "${connected_dir}/workload.test" -test.v -test.timeout=75m -test.run='^TestEndpointSplitConnectedEvidenceGate$' &
  connected_pid=$!
  profile_children+=("${connected_pid}")
  wait_profile_file() {
    local file="$1" child="$2" deadline=$((SECONDS + 600))
    until [[ -r "${file}" ]]; do
      kill -0 "${child}" 2>/dev/null || { echo 'owned workload failed before its load barrier; see connected logs' >&2;exit 1; }
      (( SECONDS < deadline )) || { echo 'owned workload setup exceeded ten minutes' >&2;exit 1; }
      sleep 1
    done
  }
  wait_profile_file "${CAPACITY_CONNECTED_READY_FILE}" "${connected_pid}"
  export CAPACITY_PUSH_EVIDENCE="${connected_dir}/push-load.json"
  CAPACITY_PUSH_DRIVER=1 CAPACITY_PUSH_DURATION=36m CAPACITY_PUSH_INTERVAL=1s \
    CAPACITY_PUSH_VERIFY_API_A="${REPOSITORY_API_A}" CAPACITY_PUSH_VERIFY_API_B="${REPOSITORY_API_B}" \
    CAPACITY_PUSH_WORKERS="${CAPACITY_PUSH_WORKERS:-1}" CAPACITY_PUSH_TOKEN_FILE="${CAPACITY_PUSH_TOKEN_FILE:-${CAPACITY_TOKEN_FILE}}" \
    python3 "${repo_root}/scripts/capacity-child.py" 3000 "${connected_dir}/push.log" \
      "${connected_dir}/workload.test" -test.v -test.timeout=50m -test.run='^TestCapacityGitPushDriver$' &
  push_pid=$!
  profile_children+=("${push_pid}")
  wait_profile_file "${CAPACITY_PUSH_READY_FILE}" "${push_pid}"
  jq -n --arg run "${run_id}" '{runId:$run}' >"${CAPACITY_LOAD_START_FILE}.tmp"
  mv "${CAPACITY_LOAD_START_FILE}.tmp" "${CAPACITY_LOAD_START_FILE}"
  wait_profile_file "${CAPACITY_CONNECTED_STARTED_FILE}" "${connected_pid}"
  wait_profile_file "${CAPACITY_PUSH_STARTED_FILE}" "${push_pid}"
fi
if [[ -n "${CAPACITY_CHAOS_PROFILE:-}" ]]; then
  if [[ -z "${CAPACITY_CHAOS_TARGET:-}" || "${CAPACITY_CHAOS_CONFIRM:-0}" != "1" ]]; then
    echo "integrated chaos requires CAPACITY_CHAOS_TARGET and CAPACITY_CHAOS_CONFIRM=1" >&2
    exit 2
  fi
  (
    sleep "${CAPACITY_CHAOS_DELAY:-30s}"
    CHAOS_PROFILE="${CAPACITY_CHAOS_PROFILE}" \
      CHAOS_TARGET="${CAPACITY_CHAOS_TARGET}" \
      CHAOS_CONFIRM=1 \
      CHAOS_EVIDENCE_DIR="${evidence_dir}/chaos" \
      "${repo_root}/scripts/run-chaos.sh" "${CAPACITY_CHAOS_PROFILE}"
  ) >"${evidence_dir}/chaos-runner.log" 2>&1 &
  chaos_pid=$!
fi

jq --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '. + {loadStartedAt:$at}' "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"
set +e
if [[ -n "${K6_BIN:-}" ]]; then
  "${K6_BIN}" run --summary-export "${evidence_dir}/summary.json" "${script}" 2>&1 | tee "${evidence_dir}/k6.log"
  status=${PIPESTATUS[0]}
elif command -v k6 >/dev/null 2>&1; then
  k6 run --summary-export "${evidence_dir}/summary.json" "${script}" 2>&1 | tee "${evidence_dir}/k6.log"
  status=${PIPESTATUS[0]}
else
  docker_args=(run --rm --network "${CAPACITY_DOCKER_NETWORK:-host}" -v "${repo_root}:/workspace:ro" -v "${evidence_dir}:/evidence" -w /workspace)
  if [[ -n "${CAPACITY_ENV_FILE:-}" ]]; then
    docker_args+=(--env-file "${CAPACITY_ENV_FILE}")
  fi
  while IFS='=' read -r name _; do
    case "${name}" in
      CAPACITY_*|PRODUCT_CAPACITY_*|K6_*) docker_args+=(-e "${name}") ;;
    esac
  done < <(env)
  docker "${docker_args[@]}" "${k6_image}" run --summary-export /evidence/summary.json "/workspace/tests/capacity/profiles/${profile}.js" 2>&1 | tee "${evidence_dir}/k6.log"
  status=${PIPESTATUS[0]}
fi
set -e
jq --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '. + {loadCompletedAt:$at}' "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"
if [[ "${profile}" == api-endpoint-split && "${mode}" != diagnostic ]]; then
  set +e
  wait "${connected_pid}"; connected_status=$?
  wait "${push_pid}"; push_status=$?
  set -e
  if (( status==0 && (connected_status!=0 || push_status!=0) )); then status=1; fi
  profile_children=()
fi

if [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]]; then
  set +e
  "${repo_root}/scripts/check-capacity-containers.sh" verify \
    "${evidence_dir}/datastore-before.json" "${evidence_dir}/datastore-after.json" \
    2>&1 | tee "${evidence_dir}/datastore-verifier.log"
  container_check_status=${PIPESTATUS[0]}
  set -e
fi

chaos_status=0
if [[ -n "${chaos_pid}" ]]; then
  if kill -0 "${chaos_pid}" 2>/dev/null; then
    kill "${chaos_pid}" 2>/dev/null || true
    wait "${chaos_pid}" 2>/dev/null || true
    echo "configured chaos delay exceeded the load duration; no fault was injected" | tee -a "${evidence_dir}/chaos-runner.log"
    chaos_status=124
  else
  set +e
  wait "${chaos_pid}"
  chaos_status=$?
  set -e
  fi
  chaos_pid=""
fi

prometheus_status=0
export_prometheus() {
  if [[ -n "${CAPACITY_PROMETHEUS_URL:-}" ]]; then
    set +e
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${evidence_dir}" "${CAPACITY_PROMETHEUS_URL}" \
      2>&1 | tee "${evidence_dir}/prometheus-export.log"
    prometheus_status=${PIPESTATUS[0]}
    set -e
  fi
}
# This verifier consumes the same run's SDK resource samples. Preserve the
# existing export order for profiles whose domain verifier precedes capture.
if [[ "${profile}" == api-endpoint-split ]]; then
  if [[ "${mode}" != diagnostic ]]; then
    cp "${evidence_dir}/preflight-environment.json" "${evidence_dir}/preflight-environment-before.json"
    "${preflight}" "${evidence_dir}" >"${evidence_dir}/postflight-environment.log" 2>&1
  fi
  export_prometheus
fi

verifier="${repo_root}/tests/capacity/verifiers/${profile}.sh"
verifier_required=false
if [[ "${profile}" != "api-readiness" && "${mode}" != "diagnostic" ]]; then
  verifier_required=true
fi

verifier_status=0
if [[ -x "${verifier}" ]]; then
  set +e
  "${verifier}" "${evidence_dir}" 2>&1 | tee "${evidence_dir}/verifier.log"
  verifier_status=${PIPESTATUS[0]}
  set -e
elif [[ "${verifier_required}" == "true" ]]; then
  echo "gate profile requires an executable domain verifier: ${verifier}" | tee "${evidence_dir}/verifier.log" >&2
  verifier_status=2
fi

if [[ "${profile}" != api-endpoint-split ]]; then export_prometheus; fi

if (( status == 0 && chaos_status != 0 )); then
  status=${chaos_status}
fi
if (( status == 0 && verifier_status != 0 )); then
  status=${verifier_status}
fi
if (( status == 0 && container_check_status != 0 )); then
  status=${container_check_status}
fi
if (( status == 0 && prometheus_status != 0 )); then
  status=${prometheus_status}
fi

jq \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson exit_code "${status}" \
  --argjson chaos_exit_code "${chaos_status}" \
  --argjson verifier_exit_code "${verifier_status}" \
  --argjson datastore_exit_code "${container_check_status}" \
  --argjson prometheus_exit_code "${prometheus_status}" \
  --argjson verifier_required "${verifier_required}" \
  '. + {completedAt:$completed_at, exitCode:$exit_code, sourceStateExitCode:0, chaosExitCode:$chaos_exit_code, verifierRequired:$verifier_required, verifierExitCode:$verifier_exit_code, datastoreVerifierExitCode:$datastore_exit_code, prometheusExitCode:$prometheus_exit_code, passed:($exit_code == 0 and .mode != "diagnostic" and .gitDirty == false)}' \
  "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"

echo "capacity evidence: ${evidence_dir}"
exit "${status}"
