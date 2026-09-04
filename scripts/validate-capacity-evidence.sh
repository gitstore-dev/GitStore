#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
target="${2:?capacity target is required}"
profile="${3:?capacity profile is required}"
mode="${4:?capacity mode is required}"

if [[ "${mode}" == "diagnostic" ]]; then
  jq -n --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
    '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true}' \
    >"${evidence_dir}/preflight-environment.json"
  exit 0
fi

config="${evidence_dir}/config.json"
environment="${evidence_dir}/environment.json"
[[ -r "${config}" ]] || { echo "${mode} capacity evidence requires CAPACITY_CONFIG_MANIFEST" >&2; exit 2; }
[[ -r "${environment}" ]] || { echo "${mode} capacity evidence requires CAPACITY_ENVIRONMENT_MANIFEST" >&2; exit 2; }

jq -e '
  .schemaVersion == 1 and
  (.runtime | strings | length) > 0 and
  (.architecture | strings | length) > 0 and
  (.host.logicalCPUs | numbers) > 0 and
  (.host.memoryBytes | numbers) > 0
' "${environment}" >/dev/null || {
  echo "capacity environment manifest must declare runtime, architecture, host CPUs, and host memory" >&2
  exit 2
}

require_declared_integer() {
  local name="$1" expected="$2" value="${!1:-}"
  [[ "${value}" =~ ^[1-9][0-9]*$ ]] || { echo "${mode} capacity evidence requires ${name}" >&2; exit 2; }
  (( value == expected )) || { echo "${name}=${value} does not match manifest value ${expected}" >&2; exit 2; }
}

live_api_replicas='[]'
release_service_containers='[]'
validate_api_deployment() {
  local expected_replicas="$1" endpoint metrics process_start instance_id
  [[ -n "${CAPACITY_API_ENDPOINTS:-}" ]] || {
    echo "${target}/${profile} evidence requires CAPACITY_API_ENDPOINTS" >&2
    exit 2
  }
  IFS=',' read -r -a api_endpoints <<<"${CAPACITY_API_ENDPOINTS}"
  (( ${#api_endpoints[@]} == expected_replicas )) || {
    echo "CAPACITY_API_ENDPOINTS count does not match the declared API topology" >&2
    exit 2
  }
  for endpoint in "${api_endpoints[@]}"; do
    [[ "${endpoint}" =~ ^https?://[^[:space:],]+$ ]] || { echo "invalid live API endpoint: ${endpoint}" >&2; exit 2; }
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
    live_api_replicas="$(jq -c --arg endpoint "${endpoint%/}" --arg process_start "${process_start}" --arg instance_id "${instance_id}" \
      '. + [{endpoint:$endpoint,instanceID:$instance_id,processStartTimeSeconds:($process_start|tonumber)}]' <<<"${live_api_replicas}")"
  done
  [[ "$(jq '[.[].instanceID] | unique | length' <<<"${live_api_replicas}")" == "${expected_replicas}" ]] || {
    echo "CAPACITY_API_ENDPOINTS must identify distinct live API process instances" >&2
    exit 1
  }
}

validate_release_service_containers() {
  local expected_replicas="$1" source_revision inspection api_names_json container metrics process_start instance_id
  local endpoint_start endpoint_instance container_start container_instance container_id index
  [[ -n "${CAPACITY_API_CONTAINERS:-}" && -n "${CAPACITY_GIT_SERVICE_CONTAINER:-}" ]] || {
    echo "${target}/${profile} evidence requires CAPACITY_API_CONTAINERS and CAPACITY_GIT_SERVICE_CONTAINER" >&2
    exit 2
  }
  IFS=',' read -r -a api_containers <<<"${CAPACITY_API_CONTAINERS}"
  (( ${#api_containers[@]} == expected_replicas )) || {
    echo "CAPACITY_API_CONTAINERS count does not match the declared API topology" >&2
    exit 2
  }
  for container in "${api_containers[@]}" "${CAPACITY_GIT_SERVICE_CONTAINER}"; do
    [[ "${container}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || {
      echo "invalid release service container name: ${container}" >&2
      exit 2
    }
  done
  api_names_json="$(printf '%s\n' "${api_containers[@]}" | jq -Rsc 'split("\n")[:-1]')"
  source_revision="$(git rev-parse HEAD)"
  inspection="$(docker inspect "${api_containers[@]}" "${CAPACITY_GIT_SERVICE_CONTAINER}")" || {
    echo "cannot inspect live API and Git-service containers" >&2
    exit 1
  }
  release_service_containers="$(jq -c \
    --argjson api_names "${api_names_json}" --arg git_name "${CAPACITY_GIT_SERVICE_CONTAINER}" \
    --arg revision "${source_revision}" '
      [.[] |
        (.Name | ltrimstr("/")) as $name |
        {id:.Id,
         name:$name,
         role:(if ($api_names | index($name)) != null then "api" elif $name == $git_name then "git-service" else "unknown" end),
         imageReference:.Config.Image,
         imageID:.Image,
         executable:.Path,
         revision:(.Config.Labels["org.opencontainers.image.revision"] // ""),
         running:.State.Running}]
    ' <<<"${inspection}")"
  jq -e --argjson expected_replicas "${expected_replicas}" --arg revision "${source_revision}" '
    length == ($expected_replicas + 1) and
    ([.[].id] | unique | length) == length and
    ([.[].name] | unique | length) == length and
    ([.[] | select(.role == "api")] | length) == $expected_replicas and
    ([.[] | select(.role == "git-service")] | length) == 1 and
    all(.[];
      .running and .revision == $revision and
      (.imageReference | test("@sha256:[0-9a-f]{64}$")) and
      ((.role == "api" and .executable == "/app/api") or
       (.role == "git-service" and .executable == "/app/git-service")))
  ' <<<"${release_service_containers}" >/dev/null || {
    echo "live API and Git-service containers must use digest-pinned release images for the tested revision" >&2
    exit 1
  }

  container_identities='[]'
  for container in "${api_containers[@]}"; do
    metrics="$(docker exec "${container}" wget -qO- http://127.0.0.1:4000/metrics)" || {
      echo "cannot scrape API metrics inside container: ${container}" >&2
      exit 1
    }
    process_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${metrics}")"
    [[ "${process_start}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]] || {
      echo "API container does not expose process_start_time_seconds: ${container}" >&2
      exit 1
    }
    instance_id="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${metrics}" | head -1)"
    [[ -n "${instance_id}" ]] || {
      echo "API container does not expose gitstore_api_process_instance_info: ${container}" >&2
      exit 1
    }
    container_identities="$(jq -c --arg name "${container}" --arg process_start "${process_start}" --arg instance_id "${instance_id}" \
      '. + [{name:$name,instanceID:$instance_id,processStartTimeSeconds:($process_start|tonumber)}]' <<<"${container_identities}")"
  done
  for index in "${!api_containers[@]}"; do
    endpoint_start="$(jq -r --argjson index "${index}" '.[$index].processStartTimeSeconds' <<<"${live_api_replicas}")"
    endpoint_instance="$(jq -r --argjson index "${index}" '.[$index].instanceID' <<<"${live_api_replicas}")"
    container_start="$(jq -r --argjson index "${index}" '.[$index].processStartTimeSeconds' <<<"${container_identities}")"
    container_instance="$(jq -r --argjson index "${index}" '.[$index].instanceID' <<<"${container_identities}")"
    [[ "${endpoint_start}" == "${container_start}" && "${endpoint_instance}" == "${container_instance}" ]] || {
      echo "API endpoint ${api_endpoints[$index]} does not map to container ${api_containers[$index]}" >&2
      exit 1
    }
    container_id="$(jq -r --arg name "${api_containers[$index]}" '.[] | select(.name == $name) | .id' <<<"${release_service_containers}")"
    live_api_replicas="$(jq -c --argjson index "${index}" --arg container "${api_containers[$index]}" --arg id "${container_id}" \
      '.[$index] += {container:$container,containerID:$id}' <<<"${live_api_replicas}")"
  done
  release_service_containers="$(jq -c --argjson identities "${container_identities}" '
    map(if .role == "api" then . as $service | . + ($identities[] | select(.name == $service.name) | {instanceID,processStartTimeSeconds}) else . end)
  ' <<<"${release_service_containers}")"
}

validate_scylla_deployment() {
  local config_nodes config_smp environment_nodes environment_memory environment_auth
  config_nodes="$(jq -r '.services.scylla.nodes' "${config}")"
  config_smp="$(jq -r '.services.scylla.smpPerNode' "${config}")"
  environment_nodes="$(jq -r '.topology.scyllaNodes' "${environment}")"
  environment_memory="$(jq -r '.datastore.memoryBytesPerNode' "${environment}")"
  environment_auth="$(jq -r '.datastore.authenticationMode' "${environment}")"
  (( config_nodes == environment_nodes )) || { echo "Scylla node counts differ between manifests" >&2; exit 2; }
  require_declared_integer CAPACITY_SCYLLA_NODES "${config_nodes}"
  require_declared_integer CAPACITY_SCYLLA_SMP "${config_smp}"
  require_declared_integer CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE "${environment_memory}"
  [[ "${CAPACITY_SCYLLA_AUTH_MODE:-}" == "${environment_auth}" ]] || {
    echo "CAPACITY_SCYLLA_AUTH_MODE must match the environment manifest" >&2
    exit 2
  }
  [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]] || {
    echo "${target}/${profile} evidence requires CAPACITY_DATASTORE_CONTAINERS" >&2
    exit 2
  }
  IFS=',' read -r -a datastore_containers <<<"${CAPACITY_DATASTORE_CONTAINERS}"
  (( ${#datastore_containers[@]} == config_nodes )) || {
    echo "CAPACITY_DATASTORE_CONTAINERS count does not match the declared Scylla topology" >&2
    exit 2
  }
}

case "${target}/${profile}" in
  namespace/validation)
    jq -e '.schemaVersion == 1 and .services.api.replicas == 2' "${config}" >/dev/null || {
      echo "namespace/validation evidence requires a config manifest declaring its two in-process API replicas" >&2
      exit 2
    }
    [[ "$(jq -r '.topology.apiReplicas' "${environment}")" == "2" ]] || {
      echo "namespace/validation environment manifest must declare its two in-process API replicas" >&2
      exit 2
    }
    ;;
  namespace/watch|namespace/recovery)
    if [[ "${profile}" == "watch" && "${NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT:-0}" == "1" ]]; then
      echo "NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT is only valid in diagnostic mode" >&2
      exit 2
    fi
    jq -e '
      .schemaVersion == 1 and
      (.services.api.replicas | numbers) >= 2 and
      .services.api.build == "release" and
      (.services.scylla.nodes | numbers) >= 3 and
      (.services.scylla.smpPerNode | numbers) >= 2 and
      (.services.gitService.build == "release")
    ' "${config}" >/dev/null || {
      echo "${target}/${profile} evidence requires three Scylla nodes with two shards each and a release Git service" >&2
      exit 2
    }
    jq -e '
      (.datastore.authenticationMode | strings | length) > 0 and
      .datastore.authenticationMode != "unknown" and
      (.datastore.memoryBytesPerNode | numbers) > 0 and
      .datastore.requireNoUnexpectedRestarts == true and
      .datastore.requireNoOOMKills == true and
      (.topology.scyllaNodes | numbers) >= 3
    ' "${environment}" >/dev/null || {
      echo "${target}/${profile} evidence requires explicit Scylla topology, memory, authentication, restart, and OOM declarations" >&2
      exit 2
    }
    config_api_replicas="$(jq -r '.services.api.replicas' "${config}")"
    environment_api_replicas="$(jq -r '.topology.apiReplicas' "${environment}")"
    (( config_api_replicas == environment_api_replicas )) || { echo "API replica counts differ between manifests" >&2; exit 2; }
    require_declared_integer CAPACITY_API_REPLICAS "${config_api_replicas}"
    [[ "${CAPACITY_API_BUILD:-}" == "release" ]] || { echo "CAPACITY_API_BUILD=release is required" >&2; exit 2; }
    [[ "${CAPACITY_GIT_SERVICE_BUILD:-}" == "release" ]] || { echo "CAPACITY_GIT_SERVICE_BUILD=release is required" >&2; exit 2; }
    validate_api_deployment "${config_api_replicas}"
    [[ -n "${NAMESPACE_WATCH_API_A:-}" && -n "${NAMESPACE_WATCH_API_B:-}" ]] || {
      echo "${target}/${profile} evidence requires both API endpoints" >&2
      exit 2
    }
    api_a="${NAMESPACE_WATCH_API_A%/}"
    api_b="${NAMESPACE_WATCH_API_B%/}"
    [[ "${api_a}" != "${api_b}" ]] || {
      echo "${target}/${profile} API endpoints must identify distinct replicas" >&2
      exit 2
    }
    jq -e --arg api_a "${api_a}" --arg api_b "${api_b}" \
      '([.[].endpoint] | index($api_a)) != null and ([.[].endpoint] | index($api_b)) != null' \
      <<<"${live_api_replicas}" >/dev/null || {
      echo "NAMESPACE_WATCH_API_A and NAMESPACE_WATCH_API_B must be members of CAPACITY_API_ENDPOINTS" >&2
      exit 2
    }
    validate_release_service_containers "${config_api_replicas}"
    validate_scylla_deployment
    if [[ "${profile}" == "recovery" ]]; then
      [[ -n "${NAMESPACE_WATCH_API_REPLACEMENT:-}" ]] || {
        echo "namespace/recovery evidence requires both API endpoints and the replacement endpoint" >&2
        exit 2
      }
      replacement="${NAMESPACE_WATCH_API_REPLACEMENT%/}"
      [[ "${replacement}" == "${api_a}" || "${replacement}" == "${api_b}" ]] || {
        echo "namespace/recovery replacement must identify one of the two replicas" >&2
        exit 2
      }
      [[ -n "${NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE:-}" && ! -e "${NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE}" ]] || {
        echo "namespace/recovery requires a fresh NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE" >&2
        exit 2
      }
      [[ -n "${NAMESPACE_WATCH_TOKEN:-}" || -r "${NAMESPACE_WATCH_TOKEN_FILE:-}" ]] || {
        echo "namespace/recovery evidence requires NAMESPACE_WATCH_TOKEN or a readable token file" >&2
        exit 2
      }
      [[ "${NAMESPACE_WATCH_OVERFLOW_TRANSITIONS:-}" =~ ^[1-9][0-9]*$ ]] || {
        echo "namespace/recovery evidence requires NAMESPACE_WATCH_OVERFLOW_TRANSITIONS" >&2
        exit 2
      }
    fi
    ;;
  scylla/soak)
    jq -e '
      .schemaVersion == 1 and
      (.services.scylla.nodes | numbers) >= 3 and
      (.services.scylla.smpPerNode | numbers) >= 2
    ' "${config}" >/dev/null || {
      echo "scylla/soak evidence requires three Scylla nodes with at least two shards each" >&2
      exit 2
    }
    jq -e '
      (.datastore.authenticationMode | strings | length) > 0 and
      .datastore.authenticationMode != "unknown" and
      (.datastore.memoryBytesPerNode | numbers) > 0 and
      .datastore.requireNoUnexpectedRestarts == true and
      .datastore.requireNoOOMKills == true and
      (.topology.scyllaNodes | numbers) >= 3
    ' "${environment}" >/dev/null || {
      echo "scylla/soak evidence requires explicit Scylla topology, memory, authentication, restart, and OOM declarations" >&2
      exit 2
    }
    validate_scylla_deployment
    echo "scylla/soak cannot produce ${mode} evidence until the harness verifies the observed preloaded product count" >&2
    exit 2
    ;;
  *)
    echo "unsupported non-k6 capacity profile: ${target}/${profile}" >&2
    exit 2
    ;;
esac

jq -n \
  --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
  --argjson live_api_replicas "${live_api_replicas}" \
  --argjson release_service_containers "${release_service_containers}" \
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true,config:$config[0],environment:$environment[0]} +
   (if ($live_api_replicas | length) == 0 then {} else {topology:{liveApiReplicas:$live_api_replicas},artifacts:{releaseServiceContainers:$release_service_containers}} end)' \
  >"${evidence_dir}/preflight-environment.json"
