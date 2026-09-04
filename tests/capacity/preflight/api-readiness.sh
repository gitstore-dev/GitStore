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
[[ -n "${CAPACITY_API_ENDPOINTS:-}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_API_ENDPOINTS" >&2; exit 2; }
[[ -n "${CAPACITY_API_CONTAINERS:-}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_API_CONTAINERS" >&2; exit 2; }

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

IFS=',' read -r -a api_endpoints <<<"${CAPACITY_API_ENDPOINTS}"
IFS=',' read -r -a api_containers <<<"${CAPACITY_API_CONTAINERS}"
(( ${#api_endpoints[@]} == config_replicas )) || {
  echo "CAPACITY_API_ENDPOINTS count must match the declared API replicas" >&2
  exit 2
}
(( ${#api_containers[@]} == config_replicas )) || {
  echo "CAPACITY_API_CONTAINERS count must match the declared API replicas" >&2
  exit 2
}
for container in "${api_containers[@]}"; do
  [[ "${container}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || { echo "invalid API container name: ${container}" >&2; exit 2; }
done
inspection="$(docker inspect "${api_containers[@]}")" || {
  echo "cannot inspect live API containers" >&2
  exit 1
}
source_revision="$(git rev-parse HEAD)"
container_json="$(jq -c '[.[] | {
  id:.Id,name:(.Name | ltrimstr("/")),running:.State.Running,
  imageReference:.Config.Image,imageID:.Image,revision:(.Config.Labels["org.opencontainers.image.revision"] // ""),executable:.Path
}]' <<<"${inspection}")"
jq -e --argjson expected "${config_replicas}" --arg revision "${source_revision}" '
  length == $expected and all(.[]; .running) and
  ([.[].id] | unique | length) == length and
  ([.[].name] | unique | length) == length and
  all(.[];
    .revision == $revision and .executable == "/app/api" and
    (.imageReference | test("@sha256:[0-9a-f]{64}$")))
' <<<"${container_json}" >/dev/null || {
  echo "CAPACITY_API_CONTAINERS must identify distinct running digest-pinned release containers for the tested revision" >&2
  exit 1
}
identity_json='[]'
for index in "${!api_endpoints[@]}"; do
  endpoint="${api_endpoints[$index]}"
  container="${api_containers[$index]}"
  [[ "${endpoint}" =~ ^https?://[^[:space:],]+$ ]] || { echo "invalid live API endpoint: ${endpoint}" >&2; exit 2; }
  curl -fsS --max-time 5 "${endpoint%/}/health" >/dev/null || {
    echo "live API endpoint failed health probe: ${endpoint}" >&2
    exit 1
  }
  curl -fsS --max-time 5 "${endpoint%/}/ready" >/dev/null || {
    echo "live API endpoint failed readiness probe: ${endpoint}" >&2
    exit 1
  }
  metrics="$(curl -fsS --max-time 5 "${endpoint%/}/metrics")" || {
    echo "cannot scrape live API endpoint: ${endpoint}" >&2
    exit 1
  }
  process_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${metrics}")"
  [[ "${process_start}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]] || {
    echo "live API endpoint does not expose process_start_time_seconds: ${endpoint}" >&2
    exit 1
  }
  instance_id="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${metrics}" | head -1)"
  [[ -n "${instance_id}" ]] || {
    echo "live API endpoint does not expose gitstore_api_process_instance_info: ${endpoint}" >&2
    exit 1
  }
  container_metrics="$(docker exec "${container}" wget -qO- http://127.0.0.1:4000/metrics)" || {
    echo "cannot scrape API metrics inside container: ${container}" >&2
    exit 1
  }
  container_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${container_metrics}")"
  container_instance="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${container_metrics}" | head -1)"
  [[ "${process_start}" == "${container_start}" && "${instance_id}" == "${container_instance}" ]] || {
    echo "API endpoint ${endpoint} does not map to container ${container}" >&2
    exit 1
  }
  container_id="$(jq -r --arg name "${container}" '.[] | select(.name == $name) | .id' <<<"${container_json}")"
  identity_json="$(jq -c --arg endpoint "${endpoint%/}" --arg process_start "${process_start}" \
    --arg instance_id "${instance_id}" --arg container "${container}" --arg container_id "${container_id}" \
    '. + [{endpoint:$endpoint,instanceID:$instance_id,container:$container,containerID:$container_id,processStartTimeSeconds:($process_start|tonumber)}]' <<<"${identity_json}")"
done
[[ "$(jq '[.[].instanceID] | unique | length' <<<"${identity_json}")" == "${config_replicas}" ]] || {
  echo "CAPACITY_API_ENDPOINTS must identify distinct live API process instances" >&2
  exit 1
}
base_url="${CAPACITY_BASE_URL%/}"
jq -e --arg base_url "${base_url}" '([.[].endpoint] | index($base_url)) != null' <<<"${identity_json}" >/dev/null || {
  echo "CAPACITY_BASE_URL must identify one of the verified CAPACITY_API_ENDPOINTS" >&2
  exit 2
}

jq -n \
  --arg mode "${mode}" --arg base_url "${base_url}" --arg api_build "${config_build}" \
  --argjson api_replicas "${config_replicas}" --argjson runtime_memory_bytes "${environment_memory}" \
  --argjson live_replicas "${identity_json}" --argjson api_containers "${container_json}" \
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,mode:$mode,passed:true,baseURL:$base_url,topology:{apiReplicas:$api_replicas,liveReplicas:$live_replicas},resources:{runtimeMemoryBytes:$runtime_memory_bytes},builds:{api:$api_build},artifacts:{apiContainers:$api_containers},config:$config[0],environment:$environment[0]}' \
  >"${output}"
