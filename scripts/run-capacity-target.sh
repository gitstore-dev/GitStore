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
    command=(make --no-print-directory _capacity-k6 CAPACITY_PROFILE=api-readiness "MODE=${mode}")
    ;;
  namespace/admission)
    command=(make --no-print-directory _capacity-k6 CAPACITY_PROFILE=namespace-admission "MODE=${mode}")
    ;;
  namespace/validation)
    command=(make --no-print-directory _capacity-namespace-admission "MODE=${mode}")
    ;;
  namespace/watch)
    command=(make --no-print-directory _capacity-namespace-watch "MODE=${mode}")
    ;;
  namespace/recovery)
    command=(make --no-print-directory _capacity-namespace-recovery "MODE=${mode}")
    ;;
  scylla/soak)
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
observability="${CAPACITY_OBSERVABILITY:-none}"
case "${observability}" in
  none) ;;
  prometheus)
    make --no-print-directory _capacity-observability
    export CAPACITY_PROMETHEUS_URL="${CAPACITY_PROMETHEUS_URL:-http://127.0.0.1:${CAPACITY_PROMETHEUS_PORT:-9090}}"
    trap 'make --no-print-directory _capacity-observability-down' EXIT
    ;;
  *)
    echo "CAPACITY_OBSERVABILITY must be none or prometheus" >&2
    exit 2
    ;;
esac

if [[ "${target}/${profile}" == "api/readiness" || "${target}/${profile}" == "namespace/admission" ]]; then
  "${command[@]}"
  exit $?
fi

run_id="${CAPACITY_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CAPACITY_EVIDENCE_DIR:-${repo_root}/.gitstore/capacity}"
evidence_dir="${evidence_root}/${target}/${profile}/${mode}/${run_id}"
mkdir -p "${evidence_dir}"
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
  --argjson worktree_dirty "${worktree_dirty}" \
  '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,evidenceClass:$mode,productionTargets:{visibilityP95Seconds:1,visibilityP99Seconds:3},runId:$run_id,startedAt:$started_at,gitRevision:$git_revision,worktreeDirty:$worktree_dirty,sourceStateSha256:$source_state_sha256}' \
  >"${metadata}"

set +e
CAPACITY_EVIDENCE_DIR="${evidence_dir}" "${command[@]}" 2>&1 | tee "${evidence_dir}/harness.log"
status=${PIPESTATUS[0]}
set -e

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
  --argjson exit_code "${status}" --argjson prometheus_exit_code "${prometheus_status}" \
  '. + {completedAt:$completed_at,exitCode:$exit_code,prometheusExitCode:$prometheus_exit_code,passed:($exit_code == 0 and .mode != "diagnostic")}' \
  "${metadata}" >"${metadata}.tmp"
mv "${metadata}.tmp" "${metadata}"
echo "capacity evidence: ${evidence_dir}"
exit "${status}"
