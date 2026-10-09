#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

trigger_file="${1:?replacement trigger file is required}"
project="${2:?Compose project is required}"
revision="${3:?Git revision is required}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export CAPACITY_GIT_REVISION="${revision}"
export CONFIG_FILE="${CONFIG_FILE:-${repo_root}/config/config.toml}"
export SCYLLA_CLUSTER_SMP="${SCYLLA_CLUSTER_SMP:-1}"
export SCYLLA_CLUSTER_MEMORY_LIMIT="${SCYLLA_CLUSTER_MEMORY_LIMIT:-1536m}"

compose=(docker compose -p "${project}" --profile capacity-stack
  -f "${repo_root}/compose.yml"
  -f "${repo_root}/compose.scylla.cluster.yml"
  -f "${repo_root}/compose.capacity.yml")

while [[ ! -e "${trigger_file}" ]]; do
  sleep 0.1
done

# Remove and recreate only API B. A stop/start retains the container identity
# and is therefore insufficient evidence for a rolling replacement.
"${compose[@]}" stop api-b
"${compose[@]}" rm -f api-b
"${compose[@]}" up -d --no-deps api-b
