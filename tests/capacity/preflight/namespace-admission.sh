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
fi

preflight_file="${evidence_dir}/preflight-environment.json"
jq -n \
  --arg mode "${mode}" \
  --argjson api_replicas "${api_replicas}" \
  --argjson scylla_nodes "${scylla_nodes}" \
  --argjson scylla_smp "${scylla_smp}" \
  --arg git_service_build "${git_build}" \
  '{schemaVersion:1, mode:$mode, topology:{apiReplicas:$api_replicas, scyllaNodes:$scylla_nodes, scyllaSmpPerNode:$scylla_smp}, builds:{gitService:$git_service_build}}' \
  >"${preflight_file}"

if [[ "${mode}" == "diagnostic" ]]; then
  echo "diagnostic mode: topology/build floors are recorded but not enforced"
fi
