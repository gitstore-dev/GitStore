#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
mode="${MODE:-diagnostic}"
output="${evidence_dir}/preflight-environment.json"

if [[ "${mode}" == "diagnostic" ]]; then
  jq -n --arg mode "${mode}" \
    '{schemaVersion:1,mode:$mode,passed:true,note:"diagnostic evidence does not assert deployment topology"}' \
    >"${output}"
  exit 0
fi

config="${evidence_dir}/config.json"
environment="${evidence_dir}/environment.json"
[[ -r "${config}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_CONFIG_MANIFEST" >&2; exit 2; }
[[ -r "${environment}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_ENVIRONMENT_MANIFEST" >&2; exit 2; }
[[ -n "${CAPACITY_BASE_URL:-}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_BASE_URL" >&2; exit 2; }

config_replicas="$(jq -er '.services.api.replicas | select(type == "number" and . >= 2)' "${config}")" || {
  echo "${mode} API readiness config must declare at least two API replicas" >&2
  exit 2
}
config_build="$(jq -er '.services.api.build | select(. == "release")' "${config}")" || {
  echo "${mode} API readiness config must identify a release API build" >&2
  exit 2
}
environment_replicas="$(jq -er '.topology.apiReplicas | select(type == "number" and . >= 2)' "${environment}")" || {
  echo "${mode} API readiness environment must declare at least two API replicas" >&2
  exit 2
}
environment_memory="$(jq -er '.host.memoryBytes | select(type == "number" and . > 0)' "${environment}")" || {
  echo "${mode} API readiness environment must declare positive host memory" >&2
  exit 2
}
jq -e '
  .schemaVersion == 1 and
  (.runtime | strings | length) > 0 and
  (.architecture | strings | length) > 0 and
  (.host.logicalCPUs | numbers) > 0
' "${environment}" >/dev/null || {
  echo "${mode} API readiness environment must declare runtime, architecture, and logical CPUs" >&2
  exit 2
}
(( config_replicas == environment_replicas )) || { echo "API replica counts differ between manifests" >&2; exit 2; }
[[ "${CAPACITY_API_BUILD:-}" == "${config_build}" ]] || {
  echo "CAPACITY_API_BUILD=release must match the config manifest" >&2
  exit 2
}
[[ "${CAPACITY_API_REPLICAS:-}" =~ ^[1-9][0-9]*$ ]] && (( CAPACITY_API_REPLICAS == config_replicas )) || {
  echo "CAPACITY_API_REPLICAS must match the running topology declared by both manifests" >&2
  exit 2
}
[[ "${CAPACITY_RUNTIME_MEMORY_BYTES:-}" =~ ^[1-9][0-9]*$ ]] && (( CAPACITY_RUNTIME_MEMORY_BYTES == environment_memory )) || {
  echo "CAPACITY_RUNTIME_MEMORY_BYTES must match the environment manifest" >&2
  exit 2
}

jq -n \
  --arg mode "${mode}" --arg base_url "${CAPACITY_BASE_URL}" --arg api_build "${config_build}" \
  --argjson api_replicas "${config_replicas}" --argjson runtime_memory_bytes "${environment_memory}" \
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,mode:$mode,passed:true,baseURL:$base_url,topology:{apiReplicas:$api_replicas},resources:{runtimeMemoryBytes:$runtime_memory_bytes},builds:{api:$api_build},config:$config[0],environment:$environment[0]}' \
  >"${output}"
