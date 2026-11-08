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

requires_ownership="$(jq -r '.requiresOwnership // false' "${profile_file}")"
owner="${CHAOS_OWNER:-}"
ownership_label="${CHAOS_OWNERSHIP_LABEL:-com.docker.compose.project}"
target_id=""
if [[ "${requires_ownership}" == true ]]; then
  pattern="$(jq -er '.targetPattern' "${profile_file}")"
  [[ "${target}" =~ ${pattern} ]] || { echo "profile requires an explicit identity-service target" >&2; exit 2; }
  [[ -n "${owner}" && "${ownership_label}" =~ ^[a-zA-Z0-9_.-]+$ ]] || {
    echo "identity faults require CHAOS_OWNER and a valid CHAOS_OWNERSHIP_LABEL" >&2
    exit 2
  }
  ownership="$(docker inspect "${target}")"
  jq -e --arg label "${ownership_label}" --arg owner "${owner}" \
    'length == 1 and .[0].Config.Labels[$label] == $owner and .[0].State.Running and (.[0].State.Paused | not)' \
    <<<"${ownership}" >/dev/null || { echo "identity target ownership or running state does not match" >&2; exit 2; }
  target_id="$(jq -er '.[0].Id' <<<"${ownership}")"
fi

action="$(jq -er '.action' "${profile_file}")"
duration="$(jq -r '.duration // empty' "${profile_file}")"
pumba_image="${PUMBA_IMAGE:-ghcr.io/alexei-led/pumba:1.1.7}"
run_id="${CHAOS_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CHAOS_EVIDENCE_DIR:-${repo_root}/.gitstore/chaos}"
evidence_dir="${evidence_root}/${profile}/${run_id}"
mkdir -p "${evidence_dir}"

record_target_state() {
  local output_file="$1"

  docker inspect "${target}" | jq '[.[] | {
    Id,
    Name,
    Image,
    Created,
    RestartCount,
    Config: {Image: .Config.Image},
    HostConfig: {
      Memory: .HostConfig.Memory,
      NanoCpus: .HostConfig.NanoCpus,
      CpuPeriod: .HostConfig.CpuPeriod,
      CpuQuota: .HostConfig.CpuQuota,
      CpusetCpus: .HostConfig.CpusetCpus,
      PidsLimit: .HostConfig.PidsLimit,
      RestartPolicy: .HostConfig.RestartPolicy
    },
    State: {
      Status: .State.Status,
      Running: .State.Running,
      Paused: .State.Paused,
      Restarting: .State.Restarting,
      OOMKilled: .State.OOMKilled,
      Dead: .State.Dead,
      ExitCode: .State.ExitCode,
      Error: .State.Error,
      StartedAt: .State.StartedAt,
      FinishedAt: .State.FinishedAt,
      Health: (if .State.Health == null then null else {
        Status: .State.Health.Status,
        FailingStreak: .State.Health.FailingStreak
      } end)
    }
  }]' >"${output_file}"
}

record_target_state "${evidence_dir}/target-before.json"
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
injection_status="${status}"

restored=false
if [[ "${requires_ownership}" == true ]]; then
  if current="$(docker inspect "${target}" 2>/dev/null)" &&
    jq -e --arg label "${ownership_label}" --arg owner "${owner}" --arg id "${target_id}" \
      'length == 1 and .[0].Id == $id and .[0].Config.Labels[$label] == $owner' <<<"${current}" >/dev/null; then
    if [[ "$(jq -r '.[0].State.Running' <<<"${current}")" != true ]]; then
      docker start "${target_id}" >/dev/null || status=1
    fi
    if record_target_state "${evidence_dir}/target-after.json" &&
      jq -e --arg id "${target_id}" \
        'length == 1 and .[0].Id == $id and .[0].State.Running and (.[0].State.Paused | not)' \
        "${evidence_dir}/target-after.json" >/dev/null; then
      restored=true
    fi
  fi
  if [[ "${restored}" != true && "${status}" == 0 ]]; then status=1; fi
else
  record_target_state "${evidence_dir}/target-after.json" 2>/dev/null || true
fi
jq --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson exit_code "${status}" \
  --argjson injection_exit_code "${injection_status}" \
  --argjson restoration_required "${requires_ownership}" --argjson restored "${restored}" \
  '. + {completedAt:$completed_at, exitCode:$exit_code, injectorExitCode:$injection_exit_code,
    injected:($injection_exit_code == 0),
    restorationRequired:$restoration_required, restored:$restored}' \
  "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"

echo "chaos evidence: ${evidence_dir}"
exit "${status}"
