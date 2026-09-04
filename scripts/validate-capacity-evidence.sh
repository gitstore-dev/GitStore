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
    [[ "${CAPACITY_GIT_SERVICE_BUILD:-}" == "release" ]] || { echo "CAPACITY_GIT_SERVICE_BUILD=release is required" >&2; exit 2; }
    validate_scylla_deployment
    if [[ "${profile}" == "recovery" ]]; then
      [[ -n "${NAMESPACE_WATCH_API_A:-}" && -n "${NAMESPACE_WATCH_API_B:-}" && -n "${NAMESPACE_WATCH_API_REPLACEMENT:-}" ]] || {
        echo "namespace/recovery evidence requires both API endpoints and the replacement endpoint" >&2
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
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true,config:$config[0],environment:$environment[0]}' \
  >"${evidence_dir}/preflight-environment.json"
