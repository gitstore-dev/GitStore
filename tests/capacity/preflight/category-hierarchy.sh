#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Validate that a category hierarchy run has the independent API/controller
# replicas, release builds, dataset scale and push-driver declaration needed to
# make its results meaningful. The topology is checked, never created.
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
subtree_size="${CAPACITY_CATEGORY_SUBTREE_SIZE:-10000}"
mutation_rate="${CAPACITY_RATE:-20}"
duration_seconds="${CAPACITY_CATEGORY_DURATION_SECONDS:-600}"
push_evidence="${CAPACITY_CATEGORY_PUSH_EVIDENCE:-}"

case "${mode}" in diagnostic|alpha|production) ;; *) echo "MODE must be diagnostic, alpha, or production" >&2; exit 2 ;; esac
[[ "${subtree_size}" =~ ^[0-9]+$ && "${mutation_rate}" =~ ^[0-9]+$ && "${duration_seconds}" =~ ^[0-9]+$ ]] || { echo "CAPACITY_CATEGORY_SUBTREE_SIZE, CAPACITY_RATE and CAPACITY_CATEGORY_DURATION_SECONDS must be integers" >&2; exit 2; }

if [[ "${mode}" != "diagnostic" ]]; then
  [[ -n "${CAPACITY_CONFIG_MANIFEST:-}" && -n "${CAPACITY_ENVIRONMENT_MANIFEST:-}" ]] || { echo "${mode} category hierarchy evidence requires deployment manifests" >&2; exit 2; }
  [[ -n "${CAPACITY_API_A:-}" && -n "${CAPACITY_API_B:-}" ]] || { echo "${mode} category hierarchy requires CAPACITY_API_A and CAPACITY_API_B" >&2; exit 2; }
  [[ -n "${CAPACITY_CONTROLLER_A:-}" && -n "${CAPACITY_CONTROLLER_B:-}" ]] || { echo "${mode} category hierarchy requires CAPACITY_CONTROLLER_A and CAPACITY_CONTROLLER_B" >&2; exit 2; }
  (( api_replicas >= 2 && controller_replicas >= 2 )) || { echo "${mode} category hierarchy requires two API and two controller replicas" >&2; exit 2; }
  [[ "${api_build}" == "release" && "${controller_build}" == "release" && "${git_build}" == "release" ]] || { echo "${mode} category hierarchy requires release API, controller, and git-service builds" >&2; exit 2; }
  (( scylla_nodes >= 3 && scylla_smp >= 1 )) || { echo "${mode} category hierarchy requires a three-node Scylla topology" >&2; exit 2; }
  (( subtree_size >= 10000 )) || { echo "${mode} category hierarchy requires a subtree of at least 10000 descendants" >&2; exit 2; }
  (( mutation_rate >= 20 )) || { echo "${mode} category hierarchy requires at least 20 mutations/s (CAPACITY_RATE)" >&2; exit 2; }
  (( duration_seconds >= 300 )) || { echo "${mode} category hierarchy requires at least 300s of sustained load" >&2; exit 2; }
  # k6 cannot push to Git; the companion push driver writes this file while
  # the workload runs and the verifier checks that it overlapped the load.
  [[ -n "${push_evidence}" ]] || { echo "${mode} category hierarchy requires CAPACITY_CATEGORY_PUSH_EVIDENCE (companion Git push driver output)" >&2; exit 2; }
fi

jq -n \
  --arg mode "${mode}" --arg api_a "${CAPACITY_API_A:-}" --arg api_b "${CAPACITY_API_B:-}" \
  --arg controller_a "${CAPACITY_CONTROLLER_A:-}" --arg controller_b "${CAPACITY_CONTROLLER_B:-}" \
  --arg api_build "${api_build}" --arg controller_build "${controller_build}" --arg git_build "${git_build}" \
  --arg chaos "${CAPACITY_CHAOS_PROFILE:-}" --arg push_evidence "${push_evidence}" \
  --argjson api_replicas "${api_replicas}" --argjson controller_replicas "${controller_replicas}" \
  --argjson scylla_nodes "${scylla_nodes}" --argjson scylla_smp "${scylla_smp}" \
  --argjson subtree "${subtree_size}" --argjson rate "${mutation_rate}" --argjson duration "${duration_seconds}" \
  '{schemaVersion:1,mode:$mode,topology:{apiReplicas:$api_replicas,controllerReplicas:$controller_replicas,scyllaNodes:$scylla_nodes,scyllaSmpPerNode:$scylla_smp},endpoints:{apiA:$api_a,apiB:$api_b,controllerA:$controller_a,controllerB:$controller_b},builds:{api:$api_build,controller:$controller_build,gitService:$git_build},dataset:{subtreeDescendants:$subtree,mutationRatePerSecond:$rate,durationSeconds:$duration},pushDriverEvidence:$push_evidence,faultProfiles:{declared:["controller-restart","api-restart"],injectedThisRun:$chaos}}' \
  >"${evidence_dir}/preflight-environment.json"
