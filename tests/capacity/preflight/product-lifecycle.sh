#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Validate that a Product lifecycle run has the independent API/controller
# replicas and release topology needed to make its results meaningful.
set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib/preflight.sh"

evidence_dir="${1:?evidence directory is required}"
mode="$(capacity_preflight_mode)"

if [[ "${mode}" != "diagnostic" ]]; then
  capacity_preflight_controller_topology "${mode}" "Product lifecycle"
fi

capacity_preflight_controller_environment "${mode}" >"${evidence_dir}/preflight-environment.json"
