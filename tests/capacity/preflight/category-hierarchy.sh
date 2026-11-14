#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Validate that a category hierarchy run has the independent API/controller
# replicas, release builds, dataset scale and push-driver declaration needed to
# make its results meaningful. The topology is checked, never created.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib/preflight.sh"

evidence_dir="${1:?evidence directory is required}"
mode="$(capacity_preflight_mode)"
subtree_size="${CAPACITY_CATEGORY_SUBTREE_SIZE:-10000}"
mutation_rate="${CAPACITY_RATE:-20}"
duration_seconds="${CAPACITY_CATEGORY_DURATION_SECONDS:-600}"
push_evidence="${CAPACITY_CATEGORY_PUSH_EVIDENCE:-}"

[[ "${subtree_size}" =~ ^[0-9]+$ && "${mutation_rate}" =~ ^[0-9]+$ && "${duration_seconds}" =~ ^[0-9]+$ ]] || { echo "CAPACITY_CATEGORY_SUBTREE_SIZE, CAPACITY_RATE and CAPACITY_CATEGORY_DURATION_SECONDS must be integers" >&2; exit 2; }

if [[ "${mode}" != "diagnostic" ]]; then
  capacity_preflight_controller_topology "${mode}" "category hierarchy"
  (( subtree_size >= 10000 )) || { echo "${mode} category hierarchy requires a subtree of at least 10000 descendants" >&2; exit 2; }
  (( mutation_rate >= 20 )) || { echo "${mode} category hierarchy requires at least 20 mutations/s (CAPACITY_RATE)" >&2; exit 2; }
  (( duration_seconds >= 300 )) || { echo "${mode} category hierarchy requires at least 300s of sustained load" >&2; exit 2; }
  # k6 cannot push to Git; the companion push driver writes this file while
  # the workload runs and the verifier checks that it overlapped the load.
  [[ -n "${push_evidence}" ]] || { echo "${mode} category hierarchy requires CAPACITY_CATEGORY_PUSH_EVIDENCE (companion Git push driver output)" >&2; exit 2; }
fi

capacity_preflight_controller_environment "${mode}" | jq \
  --arg chaos "${CAPACITY_CHAOS_PROFILE:-}" --arg push_evidence "${push_evidence}" \
  --argjson subtree "${subtree_size}" --argjson rate "${mutation_rate}" --argjson duration "${duration_seconds}" \
  '. + {dataset:{subtreeDescendants:$subtree,mutationRatePerSecond:$rate,durationSeconds:$duration},pushDriverEvidence:$push_evidence,faultProfiles:{declared:["controller-restart","api-restart"],injectedThisRun:$chaos}}' \
  >"${evidence_dir}/preflight-environment.json"
