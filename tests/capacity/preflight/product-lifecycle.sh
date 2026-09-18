#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Validate that a Product lifecycle run has the independent API/controller
# replicas and release topology needed to make its results meaningful.
set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
mode="${MODE:-diagnostic}"
api_replicas="${CAPACITY_API_REPLICAS:-0}"
controller_replicas="${CAPACITY_CONTROLLER_REPLICAS:-0}"
api_build="${CAPACITY_API_BUILD:-unknown}"
controller_build="${CAPACITY_CONTROLLER_BUILD:-unknown}"
git_build="${CAPACITY_GIT_SERVICE_BUILD:-unknown}"
scylla_nodes="${CAPACITY_SCYLLA_NODES:-0}"
scylla_smp="${CAPACITY_SCYLLA_SMP:-0}"

case "${mode}" in diagnostic|alpha|production) ;; *) echo "MODE must be diagnostic, alpha, or production" >&2; exit 2 ;; esac

if [[ "${mode}" != "diagnostic" ]]; then
  [[ -n "${CAPACITY_CONFIG_MANIFEST:-}" && -n "${CAPACITY_ENVIRONMENT_MANIFEST:-}" ]] || { echo "${mode} Product lifecycle evidence requires deployment manifests" >&2; exit 2; }
  [[ -n "${CAPACITY_API_A:-}" && -n "${CAPACITY_API_B:-}" ]] || { echo "${mode} Product lifecycle requires CAPACITY_API_A and CAPACITY_API_B" >&2; exit 2; }
  [[ -n "${CAPACITY_CONTROLLER_A:-}" && -n "${CAPACITY_CONTROLLER_B:-}" ]] || { echo "${mode} Product lifecycle requires CAPACITY_CONTROLLER_A and CAPACITY_CONTROLLER_B" >&2; exit 2; }
  (( api_replicas >= 2 && controller_replicas >= 2 )) || { echo "${mode} Product lifecycle requires two API and two controller replicas" >&2; exit 2; }
  [[ "${api_build}" == "release" && "${controller_build}" == "release" && "${git_build}" == "release" ]] || { echo "${mode} Product lifecycle requires release API, controller, and git-service builds" >&2; exit 2; }
  (( scylla_nodes >= 3 && scylla_smp >= 1 )) || { echo "${mode} Product lifecycle requires a three-node Scylla topology" >&2; exit 2; }
fi

jq -n \
  --arg mode "${mode}" --arg api_a "${CAPACITY_API_A:-}" --arg api_b "${CAPACITY_API_B:-}" \
  --arg controller_a "${CAPACITY_CONTROLLER_A:-}" --arg controller_b "${CAPACITY_CONTROLLER_B:-}" \
  --arg api_build "${api_build}" --arg controller_build "${controller_build}" --arg git_build "${git_build}" \
  --argjson api_replicas "${api_replicas}" --argjson controller_replicas "${controller_replicas}" \
  --argjson scylla_nodes "${scylla_nodes}" --argjson scylla_smp "${scylla_smp}" \
  '{schemaVersion:1,mode:$mode,topology:{apiReplicas:$api_replicas,controllerReplicas:$controller_replicas,scyllaNodes:$scylla_nodes,scyllaSmpPerNode:$scylla_smp},endpoints:{apiA:$api_a,apiB:$api_b,controllerA:$controller_a,controllerB:$controller_b},builds:{api:$api_build,controller:$controller_build,gitService:$git_build}}' \
  >"${evidence_dir}/preflight-environment.json"
