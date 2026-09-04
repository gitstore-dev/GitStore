#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

output="${1:?output file is required}"
targets_csv="${2:?comma-separated Prometheus targets are required}"
IFS=',' read -r -a targets <<<"${targets_csv}"
(( ${#targets[@]} > 0 )) || { echo "at least one Prometheus target is required" >&2; exit 2; }

for target in "${targets[@]}"; do
  if [[ ! "${target}" =~ ^[a-zA-Z0-9][a-zA-Z0-9._-]*:([1-9][0-9]{0,4})$ ]] || (( BASH_REMATCH[1] > 65535 )); then
    echo "invalid Prometheus scrape target: ${target}" >&2
    exit 2
  fi
done

mkdir -p "$(dirname "${output}")"
printf '%s\n' "${targets[@]}" | jq -Rsc '
  split("\n") | map(select(length > 0)) |
  [{targets:.,labels:{job:"gitstore-api-capacity"}}]
' >"${output}"
