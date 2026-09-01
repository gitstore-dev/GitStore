#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="$1"
mode="${CAPACITY_MODE:-gate}"
api_replicas="${CAPACITY_API_REPLICAS:-0}"
scylla_nodes="${CAPACITY_SCYLLA_NODES:-0}"
scylla_smp="${CAPACITY_SCYLLA_SMP:-0}"
git_build="${CAPACITY_GIT_SERVICE_BUILD:-unknown}"
runtime_memory="${CAPACITY_RUNTIME_MEMORY_BYTES:-0}"
scylla_memory="${CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE:-0}"
scylla_auth="${CAPACITY_SCYLLA_AUTH_MODE:-unknown}"

if [[ "${mode}" != "gate" && "${mode}" != "diagnostic" ]]; then
  echo "CAPACITY_MODE must be gate or diagnostic" >&2
  exit 2
fi
if [[ "${mode}" == "gate" ]]; then
  [[ -n "${CAPACITY_CONFIG_MANIFEST:-}" ]] || { echo "capacity gate requires CAPACITY_CONFIG_MANIFEST" >&2; exit 2; }
  [[ -n "${CAPACITY_ENVIRONMENT_MANIFEST:-}" ]] || { echo "capacity gate requires CAPACITY_ENVIRONMENT_MANIFEST" >&2; exit 2; }
  (( api_replicas >= 2 )) || { echo "capacity gate requires CAPACITY_API_REPLICAS>=2" >&2; exit 2; }
  (( scylla_nodes >= 3 )) || { echo "capacity gate requires CAPACITY_SCYLLA_NODES>=3" >&2; exit 2; }
  (( scylla_smp >= 2 )) || { echo "capacity gate requires CAPACITY_SCYLLA_SMP>=2" >&2; exit 2; }
  [[ "${git_build}" == "release" ]] || { echo "capacity gate requires CAPACITY_GIT_SERVICE_BUILD=release" >&2; exit 2; }
  (( runtime_memory >= 17179869184 )) || { echo "capacity gate requires CAPACITY_RUNTIME_MEMORY_BYTES>=17179869184" >&2; exit 2; }
  (( scylla_memory > 0 )) || { echo "capacity gate requires explicit CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE" >&2; exit 2; }
  [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]] || { echo "capacity gate requires CAPACITY_DATASTORE_CONTAINERS" >&2; exit 2; }
  [[ "${scylla_auth}" != "unknown" ]] || { echo "capacity gate requires CAPACITY_SCYLLA_AUTH_MODE" >&2; exit 2; }
fi

preflight_file="${evidence_dir}/preflight-environment.json"
jq -n \
  --arg mode "${mode}" \
  --argjson api_replicas "${api_replicas}" \
  --argjson scylla_nodes "${scylla_nodes}" \
  --argjson scylla_smp "${scylla_smp}" \
  --arg git_service_build "${git_build}" \
  --argjson runtime_memory_bytes "${runtime_memory}" \
  --argjson scylla_memory_bytes_per_node "${scylla_memory}" \
  --arg scylla_auth_mode "${scylla_auth}" \
  '{schemaVersion:1, mode:$mode, topology:{apiReplicas:$api_replicas, scyllaNodes:$scylla_nodes, scyllaSmpPerNode:$scylla_smp}, resources:{runtimeMemoryBytes:$runtime_memory_bytes, scyllaMemoryBytesPerNode:$scylla_memory_bytes_per_node}, datastore:{authenticationMode:$scylla_auth_mode}, builds:{gitService:$git_service_build}}' \
  >"${preflight_file}"

if [[ "${mode}" == "diagnostic" ]]; then
  echo "diagnostic mode: topology/build floors are recorded but not enforced"
fi
