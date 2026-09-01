#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
profile="${1:-${CHAOS_PROFILE:-}}"
profile_file="${repo_root}/tests/chaos/profiles/${profile}.json"
if [[ -z "${profile}" || ! "${profile}" =~ ^[a-z0-9][a-z0-9-]*$ || ! -f "${profile_file}" ]]; then
  echo "usage: make chaos CHAOS_PROFILE=<profile> CHAOS_TARGET=<gitstore-container> CHAOS_CONFIRM=1" >&2
  exit 2
fi
if [[ "${CHAOS_CONFIRM:-0}" != "1" ]]; then
  echo "refusing fault injection without CHAOS_CONFIRM=1" >&2
  exit 2
fi

target="${CHAOS_TARGET:-}"
if [[ ! "${target}" =~ ^gitstore-[a-zA-Z0-9_.-]+$ ]]; then
  echo "CHAOS_TARGET must be one explicit GitStore container name beginning with gitstore-" >&2
  exit 2
fi
if ! docker inspect "${target}" >/dev/null 2>&1; then
  echo "target container does not exist: ${target}" >&2
  exit 2
fi

action="$(jq -er '.action' "${profile_file}")"
duration="$(jq -r '.duration // empty' "${profile_file}")"
pumba_image="${PUMBA_IMAGE:-ghcr.io/alexei-led/pumba:1.1.7}"
run_id="${CHAOS_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CHAOS_EVIDENCE_DIR:-${repo_root}/.gitstore/chaos}"
evidence_dir="${evidence_root}/${profile}/${run_id}"
mkdir -p "${evidence_dir}"

docker inspect "${target}" >"${evidence_dir}/target-before.json"
jq -n --arg profile "${profile}" --arg target "${target}" --arg action "${action}" \
  --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg image "${pumba_image}" \
  '{schemaVersion:1, profile:$profile, target:$target, action:$action, startedAt:$started_at, injector:{name:"pumba", image:$image}}' \
  >"${evidence_dir}/metadata.json"

case "${action}" in
  restart)
    command_args=(restart "${target}")
    ;;
  pause)
    [[ -n "${duration}" ]] || { echo "pause profile requires duration" >&2; exit 2; }
    command_args=(pause --duration "${duration}" "${target}")
    ;;
  *)
    echo "unsupported safe chaos action: ${action}" >&2
    exit 2
    ;;
esac

set +e
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock "${pumba_image}" --log-level info "${command_args[@]}" 2>&1 | tee "${evidence_dir}/pumba.log"
status=${PIPESTATUS[0]}
set -e

docker inspect "${target}" >"${evidence_dir}/target-after.json" 2>/dev/null || true
jq --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson exit_code "${status}" \
  '. + {completedAt:$completed_at, exitCode:$exit_code, injected:($exit_code == 0)}' \
  "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"

echo "chaos evidence: ${evidence_dir}"
exit "${status}"
