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
    .revision == $revision and
    ((.imageReference | test("@sha256:[0-9a-f]{64}$")) or
      (.imageReference == .imageID and (.imageID | test("^sha256:[0-9a-f]{64}$")))))
' <<<"${container_json}" >/dev/null || {
  echo "CAPACITY_API_CONTAINERS must identify distinct running digest-pinned release containers for the tested revision" >&2
  exit 1
}
identity_json='[]'
for index in "${!api_endpoints[@]}"; do
  endpoint="${api_endpoints[$index]}"
  container="${api_containers[$index]}"
  # The canonical Compose command reads a mounted signing key in a shell,
  # then execs the release binary. Docker's configured Path stays /bin/sh;
  # inspect the live PID 1 executable rather than trusting command text.
  running_executable="$(docker exec "${container}" readlink /proc/1/exe)"
  [[ "${running_executable}" == /app/api ]] || {
    echo "API container PID 1 must run the release /app/api binary: ${container}" >&2
    exit 1
  }
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

if [[ "${CAPACITY_IDENTITY_READINESS:-0}" == 1 ]]; then
  fixture="${CAPACITY_IDENTITY_FIXTURE_FILE:-}"
  [[ -r "${fixture}" ]] || { echo "identity readiness requires a private CAPACITY_IDENTITY_FIXTURE_FILE" >&2; exit 2; }
  jq -e '
    (.cases | type == "array" and length >= 3) and
    all(.cases[];
      (.kind == "Staff" or .kind == "Customer" or .kind == "ServiceAccount") and
      (.allowed | type == "boolean") and (.token | strings | length > 0) and
      (.actorID | strings | length > 0) and (.groups | type == "array") and
      (.groups | unique | length) == (.groups | length)) and
    ([.cases[].kind] | unique | length) == 3 and
    any(.cases[]; .kind == "Customer" and .allowed == false) and
    any(.cases[]; .kind == "Staff" and .allowed == true) and
    any(.cases[]; .kind == "Staff" and .allowed == false)
  ' "${fixture}" >/dev/null || { echo "identity fixture lacks the typed grant/denial/group matrix" >&2; exit 2; }
  identity_config="$(jq -ec '.services.identity' "${config}")"
  jq -e '
    (.owner | strings | length > 0) and
    (.staff | numbers) >= 1000 and (.customers | numbers) >= 100000 and
    (.serviceAccounts | numbers) >= 100 and .cacheEntries == 256 and
    (.kratosContainers | length) == 2 and
    all([.postgresContainer,.scyllaContainer,.gitContainer] + .kratosContainers | .[];
      type == "string" and test("^gitstore-[a-zA-Z0-9_.-]+$"))
  ' <<<"${identity_config}" >/dev/null || { echo "identity config requires an owned bounded seeded topology" >&2; exit 2; }
  [[ "${CAPACITY_RATE:-500}" =~ ^[0-9]+$ ]] && (( ${CAPACITY_RATE:-500} >= 500 )) || {
    echo "identity acceptance requires at least 500 principal requests/s per API" >&2; exit 2;
  }
  duration="${CAPACITY_DURATION:-30m}"
  [[ "${duration}" =~ ^([0-9]+)(s|m|h)$ ]] || { echo "identity duration must use seconds, minutes or hours" >&2; exit 2; }
  duration_seconds="${BASH_REMATCH[1]}"
  case "${BASH_REMATCH[2]}" in m) duration_seconds=$((duration_seconds * 60)) ;; h) duration_seconds=$((duration_seconds * 3600)) ;; esac
  (( duration_seconds >= 1800 )) || { echo "identity acceptance requires the full thirty-minute duration" >&2; exit 2; }
  owner="$(jq -r '.owner' <<<"${identity_config}")"
  postgres="$(jq -r '.postgresContainer' <<<"${identity_config}")"
  scylla="$(jq -r '.scyllaContainer' <<<"${identity_config}")"
  dependency_containers=()
  while IFS= read -r dependency; do dependency_containers+=("${dependency}"); done \
    < <(jq -r '[.postgresContainer,.scyllaContainer,.gitContainer] + .kratosContainers | .[]' <<<"${identity_config}")
  dependency_inspection="$(docker inspect "${dependency_containers[@]}")"
  jq -e --arg owner "${owner}" '
    length == 5 and ([.[].Id] | unique | length) == 5 and
    all(.[]; .Config.Labels["dev.gitstore.test_owner"] == $owner and .State.Running and
      (.State.Paused | not) and (.State.OOMKilled | not) and .HostConfig.Memory > 0 and .HostConfig.NanoCpus > 0)
  ' <<<"${dependency_inspection}" >/dev/null || { echo "identity dependencies must be distinct live bounded owned containers" >&2; exit 2; }
  counts="$(docker exec "${postgres}" psql -U kratos -d kratos -At -v ON_ERROR_STOP=1 -c \
    "SELECT json_build_object('staff',count(*) FILTER(WHERE schema_id='staff'),'customers',count(*) FILTER(WHERE schema_id='customer'),'serviceAccounts',count(*) FILTER(WHERE schema_id='serviceAccount')) FROM identities;")"
  source_count="$(docker exec "${scylla}" cqlsh -e 'SELECT count(*) FROM gitstore.service_accounts_by_cluster_name;' |
    awk 'NF == 1 && $1 ~ /^[0-9]+$/ {print $1}')"
  jq -e --argjson declared "${identity_config}" --argjson source "${source_count}" '
    .staff == $declared.staff and .customers == $declared.customers and
    .serviceAccounts == $declared.serviceAccounts and .serviceAccounts == $source
  ' <<<"${counts}" >/dev/null || { echo "native directory/source counts do not match the declared identity envelope" >&2; exit 2; }
  fault_windows='[]'
  if [[ "${CAPACITY_IDENTITY_CHAOS:-0}" == 1 ]]; then
    fault_windows="$(jq -ec '
      .faultWindows | select(length == 2 and
        all(.[];
          (.replicas | length > 0) and all(.replicas[]; . == 0 or . == 1) and
          (.startMillis | numbers) > 0 and
          (.endMillis - .startMillis) > 0 and (.endMillis - .startMillis) <= 30000) and
        .[0].endMillis < .[1].startMillis)
    ' "${fixture}")" || { echo "identity chaos requires two disjoint bounded fault windows" >&2; exit 2; }
  fi
  if [[ "${CAPACITY_IDENTITY_ENVELOPE:-alpha}" == production ]]; then
    jq -e '.customers >= 1000000 and .serviceAccounts >= 1000' <<<"${counts}" >/dev/null || {
      echo "production identity envelope requires one million customers and one thousand ServiceAccounts" >&2; exit 2;
    }
  fi
  jq -n --argjson counts "${counts}" --argjson duration "${duration_seconds}" \
    --argjson rate "${CAPACITY_RATE:-500}" --argjson dependencies "${dependency_inspection}" \
    --argjson fault_windows "${fault_windows}" \
    '{schemaVersion:1,passed:true,counts:$counts,durationSeconds:$duration,ratePerReplica:$rate,
      faultWindows:$fault_windows,dependencies:[$dependencies[] | {id:.Id,name:.Name,image:.Image,startedAt:.State.StartedAt,
        memoryBytes:.HostConfig.Memory,nanoCPUs:.HostConfig.NanoCpus}]}' >"${evidence_dir}/identity-preflight.json"
fi

jq -n \
  --arg mode "${mode}" --arg base_url "${base_url}" --arg api_build "${config_build}" \
  --argjson api_replicas "${config_replicas}" --argjson runtime_memory_bytes "${environment_memory}" \
  --argjson live_replicas "${identity_json}" --argjson api_containers "${container_json}" \
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,mode:$mode,passed:true,baseURL:$base_url,topology:{apiReplicas:$api_replicas,liveReplicas:$live_replicas},resources:{runtimeMemoryBytes:$runtime_memory_bytes},builds:{api:$api_build},artifacts:{apiContainers:$api_containers},config:$config[0],environment:$environment[0]}' \
  >"${output}"
