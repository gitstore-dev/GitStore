#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

capacity_preflight_mode() {
  case "${MODE:-diagnostic}" in
    diagnostic|alpha|production) printf '%s\n' "${MODE:-diagnostic}" ;;
    *) echo "MODE must be diagnostic, alpha, or production" >&2; return 2 ;;
  esac
}

capacity_preflight_controller_topology() {
  local mode="$1" scenario="$2"
  local api_replicas="${CAPACITY_API_REPLICAS:-0}" controller_replicas="${CAPACITY_CONTROLLER_REPLICAS:-0}"
  local scylla_nodes="${CAPACITY_SCYLLA_NODES:-0}" scylla_smp="${CAPACITY_SCYLLA_SMP:-0}"

  [[ -n "${CAPACITY_CONFIG_MANIFEST:-}" && -n "${CAPACITY_ENVIRONMENT_MANIFEST:-}" ]] || { echo "${mode} ${scenario} evidence requires deployment manifests" >&2; return 2; }
  [[ -n "${CAPACITY_API_A:-}" && -n "${CAPACITY_API_B:-}" ]] || { echo "${mode} ${scenario} requires CAPACITY_API_A and CAPACITY_API_B" >&2; return 2; }
  [[ -n "${CAPACITY_CONTROLLER_A:-}" && -n "${CAPACITY_CONTROLLER_B:-}" ]] || { echo "${mode} ${scenario} requires CAPACITY_CONTROLLER_A and CAPACITY_CONTROLLER_B" >&2; return 2; }
  (( api_replicas >= 2 && controller_replicas >= 2 )) || { echo "${mode} ${scenario} requires two API and two controller replicas" >&2; return 2; }
  [[ "${CAPACITY_API_BUILD:-unknown}" == "release" && "${CAPACITY_CONTROLLER_BUILD:-unknown}" == "release" && "${CAPACITY_GIT_SERVICE_BUILD:-unknown}" == "release" ]] || { echo "${mode} ${scenario} requires release API, controller, and git-service builds" >&2; return 2; }
  (( scylla_nodes >= 3 && scylla_smp >= 1 )) || { echo "${mode} ${scenario} requires a three-node Scylla topology" >&2; return 2; }
}

capacity_preflight_controller_environment() {
  jq -n \
    --arg mode "$1" --arg api_a "${CAPACITY_API_A:-}" --arg api_b "${CAPACITY_API_B:-}" \
    --arg controller_a "${CAPACITY_CONTROLLER_A:-}" --arg controller_b "${CAPACITY_CONTROLLER_B:-}" \
    --arg api_build "${CAPACITY_API_BUILD:-unknown}" --arg controller_build "${CAPACITY_CONTROLLER_BUILD:-unknown}" --arg git_build "${CAPACITY_GIT_SERVICE_BUILD:-unknown}" \
    --argjson api_replicas "${CAPACITY_API_REPLICAS:-0}" --argjson controller_replicas "${CAPACITY_CONTROLLER_REPLICAS:-0}" \
    --argjson scylla_nodes "${CAPACITY_SCYLLA_NODES:-0}" --argjson scylla_smp "${CAPACITY_SCYLLA_SMP:-0}" \
    '{schemaVersion:1,mode:$mode,topology:{apiReplicas:$api_replicas,controllerReplicas:$controller_replicas,scyllaNodes:$scylla_nodes,scyllaSmpPerNode:$scylla_smp},endpoints:{apiA:$api_a,apiB:$api_b,controllerA:$controller_a,controllerB:$controller_b},builds:{api:$api_build,controller:$controller_build,gitService:$git_build}}'
}
