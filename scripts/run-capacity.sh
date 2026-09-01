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
if [[ -z "${profile}" ]]; then
  echo "usage: make capacity CAPACITY_PROFILE=<profile>" >&2
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
run_id="${CAPACITY_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CAPACITY_EVIDENCE_DIR:-${repo_root}/.gitstore/capacity}"
evidence_dir="${evidence_root}/${profile}/${run_id}"
mkdir -p "${evidence_dir}"

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
jq -n \
  --arg schema_version "1" \
  --arg profile "${profile}" \
  --arg run_id "${run_id}" \
  --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg git_revision "$(git -C "${repo_root}" rev-parse HEAD)" \
  --arg git_dirty "$(if [[ -z "$(git -C "${repo_root}" status --porcelain --untracked-files=normal)" ]]; then echo false; else echo true; fi)" \
  --arg k6_image "${k6_image}" \
  --arg config_digest "$(if [[ -f "${evidence_dir}/config.json" ]]; then shasum -a 256 "${evidence_dir}/config.json" | awk '{print $1}'; fi)" \
  --arg environment_digest "$(if [[ -f "${evidence_dir}/environment.json" ]]; then shasum -a 256 "${evidence_dir}/environment.json" | awk '{print $1}'; fi)" \
  '{schemaVersion:($schema_version|tonumber), profile:$profile, runId:$run_id, startedAt:$started_at, gitRevision:$git_revision, gitDirty:($git_dirty=="true"), loadGenerator:{name:"k6", image:$k6_image}} + (if $config_digest == "" then {} else {configDigest:$config_digest} end) + (if $environment_digest == "" then {} else {environmentDigest:$environment_digest} end)' \
  >"${metadata}"

export CAPACITY_EVIDENCE_DIR="${evidence_dir}"
export CAPACITY_RUN_ID="${run_id}"

container_check_status=0
if [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]]; then
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${evidence_dir}/datastore-before.json"
fi

preflight="${repo_root}/tests/capacity/preflight/${profile}.sh"
if [[ -x "${preflight}" ]]; then
  "${preflight}" "${evidence_dir}" 2>&1 | tee "${evidence_dir}/preflight.log"
fi

chaos_pid=""
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
      CAPACITY_*|K6_*) docker_args+=(-e "${name}") ;;
    esac
  done < <(env)
  docker "${docker_args[@]}" "${k6_image}" run --summary-export /evidence/summary.json "/workspace/tests/capacity/profiles/${profile}.js" 2>&1 | tee "${evidence_dir}/k6.log"
  status=${PIPESTATUS[0]}
fi
set -e

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
fi

verifier="${repo_root}/tests/capacity/verifiers/${profile}.sh"
verifier_required=false
if [[ "${profile}" != "api-readiness" && "${CAPACITY_MODE:-gate}" == "gate" ]]; then
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

if (( status == 0 && chaos_status != 0 )); then
  status=${chaos_status}
fi
if (( status == 0 && verifier_status != 0 )); then
  status=${verifier_status}
fi
if (( status == 0 && container_check_status != 0 )); then
  status=${container_check_status}
fi

jq \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson exit_code "${status}" \
  --argjson chaos_exit_code "${chaos_status}" \
  --argjson verifier_exit_code "${verifier_status}" \
  --argjson datastore_exit_code "${container_check_status}" \
  --argjson verifier_required "${verifier_required}" \
  '. + {completedAt:$completed_at, exitCode:$exit_code, chaosExitCode:$chaos_exit_code, verifierRequired:$verifier_required, verifierExitCode:$verifier_exit_code, datastoreVerifierExitCode:$datastore_exit_code, passed:($exit_code == 0)}' \
  "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"

echo "capacity evidence: ${evidence_dir}"
exit "${status}"
