#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="${1:-}"
profile="${2:-}"
mode="${3:-}"

usage() {
  cat >&2 <<'EOF'
usage: make capacity TARGET=<target> PROFILE=<profile> MODE=<mode>

valid target/profile combinations:
  api/readiness
  namespace/admission
  namespace/validation
  namespace/watch
  namespace/recovery
  scylla/soak

valid modes: diagnostic, alpha, production
EOF
}

case "${mode}" in
  diagnostic|alpha|production) ;;
  *) usage; exit 2 ;;
esac

case "${target}/${profile}" in
  api/readiness)
    runner_kind=k6
    command=(make --no-print-directory _capacity-k6 CAPACITY_PROFILE=api-readiness "MODE=${mode}")
    ;;
  namespace/admission)
    runner_kind=k6
    command=(make --no-print-directory _capacity-k6 CAPACITY_PROFILE=namespace-admission "MODE=${mode}")
    ;;
  namespace/validation)
    runner_kind=go-test
    command=(make --no-print-directory _capacity-namespace-admission "MODE=${mode}")
    ;;
  namespace/watch)
    runner_kind=go-test
    command=(make --no-print-directory _capacity-namespace-watch "MODE=${mode}")
    ;;
  namespace/recovery)
    runner_kind=go-test
    command=(make --no-print-directory _capacity-namespace-recovery "MODE=${mode}")
    ;;
  scylla/soak)
    runner_kind=go-test
    command=(make --no-print-directory _capacity-scylla-soak "MODE=${mode}")
    ;;
  *) usage; exit 2 ;;
esac

export CAPACITY_TARGET="${target}"
export CAPACITY_SCENARIO="${profile}"

if [[ "${CAPACITY_DRY_RUN:-0}" == "1" ]]; then
  printf 'target=%s profile=%s mode=%s command=' "${target}" "${profile}" "${mode}"
  printf '%q ' "${command[@]}"
  printf '\n'
  exit 0
fi

cd "${repo_root}"
run_id="${CAPACITY_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CAPACITY_EVIDENCE_DIR:-${repo_root}/.gitstore/capacity}"
evidence_dir="${evidence_root}/${target}/${profile}/${mode}/${run_id}"
mkdir -p "${evidence_dir}"
export CAPACITY_RUN_ID="${run_id}"

observability="${CAPACITY_OBSERVABILITY:-none}"
case "${observability}" in
  none) ;;
  prometheus)
    export CAPACITY_PROMETHEUS_TARGETS_FILE="${evidence_dir}/prometheus/targets.json"
    make --no-print-directory _capacity-observability
    export CAPACITY_PROMETHEUS_URL="${CAPACITY_PROMETHEUS_URL:-http://127.0.0.1:${CAPACITY_PROMETHEUS_PORT:-9090}}"
    trap 'make --no-print-directory _capacity-observability-down' EXIT
    ;;
  *)
    echo "CAPACITY_OBSERVABILITY must be none or prometheus" >&2
    exit 2
    ;;
esac

if [[ "${runner_kind}" == "k6" ]]; then
  "${command[@]}"
  exit $?
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
git_revision="$(git rev-parse HEAD)"
worktree_dirty=false
if [[ -n "$(git status --porcelain=v1)" ]]; then
  worktree_dirty=true
fi
source_state_sha256="$({
  git status --porcelain=v1
  git diff --binary HEAD
  while IFS= read -r -d '' untracked; do
    shasum -a 256 "${untracked}"
  done < <(git ls-files --others --exclude-standard -z)
} | shasum -a 256 | awk '{print $1}')"
jq -n \
  --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
  --arg run_id "${run_id}" --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg git_revision "${git_revision}" --arg source_state_sha256 "${source_state_sha256}" \
  --arg config_digest "$(if [[ -f "${evidence_dir}/config.json" ]]; then shasum -a 256 "${evidence_dir}/config.json" | awk '{print $1}'; fi)" \
  --arg environment_digest "$(if [[ -f "${evidence_dir}/environment.json" ]]; then shasum -a 256 "${evidence_dir}/environment.json" | awk '{print $1}'; fi)" \
  --argjson worktree_dirty "${worktree_dirty}" \
  '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,evidenceClass:$mode,productionTargets:{visibilityP95Seconds:1,visibilityP99Seconds:3},runId:$run_id,startedAt:$started_at,gitRevision:$git_revision,worktreeDirty:$worktree_dirty,sourceStateSha256:$source_state_sha256,verifier:{kind:"go-test"}} + (if $config_digest == "" then {} else {configDigest:$config_digest} end) + (if $environment_digest == "" then {} else {environmentDigest:$environment_digest} end)' \
  >"${metadata}"

preflight_status=0
set +e
"${repo_root}/scripts/validate-capacity-evidence.sh" "${evidence_dir}" "${target}" "${profile}" "${mode}" \
  2>&1 | tee "${evidence_dir}/preflight.log"
preflight_status=${PIPESTATUS[0]}
set -e

container_check_status=0
if (( preflight_status == 0 )) && [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]]; then
  if [[ -r "${evidence_dir}/environment.json" ]]; then
    export CAPACITY_DATASTORE_EXPECTED_COUNT="$(jq -r '.topology.scyllaNodes // 0' "${evidence_dir}/environment.json")"
    export CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES="$(jq -r '.datastore.memoryBytesPerNode // 0' "${evidence_dir}/environment.json")"
  fi
  set +e
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${evidence_dir}/datastore-before.json" \
    2>&1 | tee "${evidence_dir}/datastore-snapshot.log"
  container_check_status=${PIPESTATUS[0]}
  set -e
fi

verifier_status=${preflight_status}
if (( preflight_status == 0 && container_check_status == 0 )); then
  set +e
  CAPACITY_EVIDENCE_DIR="${evidence_dir}" "${command[@]}" 2>&1 | tee "${evidence_dir}/verifier.log"
  verifier_status=${PIPESTATUS[0]}
  set -e
fi

if (( preflight_status == 0 && container_check_status == 0 )) && [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]]; then
  set +e
  "${repo_root}/scripts/check-capacity-containers.sh" verify \
    "${evidence_dir}/datastore-before.json" "${evidence_dir}/datastore-after.json" \
    2>&1 | tee "${evidence_dir}/datastore-verifier.log"
  container_check_status=${PIPESTATUS[0]}
  set -e
fi

status=${preflight_status}
if (( status == 0 && verifier_status != 0 )); then
  status=${verifier_status}
fi
if (( status == 0 && container_check_status != 0 )); then
  status=${container_check_status}
fi

prometheus_status=0
if [[ -n "${CAPACITY_PROMETHEUS_URL:-}" ]]; then
  set +e
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${evidence_dir}" "${CAPACITY_PROMETHEUS_URL}" \
    2>&1 | tee "${evidence_dir}/prometheus-export.log"
  prometheus_status=${PIPESTATUS[0]}
  set -e
fi
if (( status == 0 && prometheus_status != 0 )); then
  status=${prometheus_status}
fi

jq --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson exit_code "${status}" --argjson preflight_exit_code "${preflight_status}" \
  --argjson verifier_exit_code "${verifier_status}" --argjson datastore_exit_code "${container_check_status}" \
  --argjson prometheus_exit_code "${prometheus_status}" \
  '. + {completedAt:$completed_at,exitCode:$exit_code,preflightRequired:(.mode != "diagnostic"),preflightExitCode:$preflight_exit_code,verifierRequired:true,verifierExitCode:$verifier_exit_code,datastoreVerifierExitCode:$datastore_exit_code,prometheusExitCode:$prometheus_exit_code,passed:($exit_code == 0 and .mode != "diagnostic")}' \
  "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"
echo "capacity evidence: ${evidence_dir}"
exit "${status}"
