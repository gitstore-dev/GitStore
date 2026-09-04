#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
prometheus_url="${2:?Prometheus URL is required}"
lookback="${CAPACITY_PROMETHEUS_LOOKBACK:-90m}"
if [[ ! "${lookback}" =~ ^[1-9][0-9]*[smhdwy]$ ]]; then
  echo "CAPACITY_PROMETHEUS_LOOKBACK must be a positive Prometheus duration such as 90m" >&2
  exit 2
fi
output_dir="${evidence_dir}/prometheus"
mkdir -p "${output_dir}"

names=(
  namespace_admission_stage_p95
  namespace_datastore_operation_p95
  namespace_datastore_errors
  namespace_cdc_discovery_p95
  namespace_materializer_stage_p95
  namespace_delivery_p95
)
queries=(
  "histogram_quantile(0.95, sum by (le,stage,instance) (increase(gitstore_namespace_admission_stage_duration_seconds_bucket[${lookback}])))"
  "histogram_quantile(0.95, sum by (le,operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_bucket{operation=~\"CreateNamespace|UpdateNamespace\"}[${lookback}])))"
  "sum by (operation,backend,instance) (increase(gitstore_datastore_operation_errors_total{operation=~\"CreateNamespace|UpdateNamespace\"}[${lookback}]))"
  "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_namespace_watch_cdc_discovery_seconds_bucket[${lookback}])))"
  "histogram_quantile(0.95, sum by (le,stage,instance) (increase(gitstore_namespace_watch_materializer_stage_duration_seconds_bucket[${lookback}])))"
  "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_namespace_watch_delivery_latency_seconds_bucket[${lookback}])))"
)

for index in "${!names[@]}"; do
  response="${output_dir}/${names[index]}.json"
  curl -fsS --get --data-urlencode "query=${queries[index]}" \
    "${prometheus_url%/}/api/v1/query" >"${response}"
  jq -e '.status == "success"' "${response}" >/dev/null
done

jq -n \
  --arg collected_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg source "${prometheus_url%/}" \
  --arg lookback "${lookback}" \
  --argjson queries "$(for index in "${!names[@]}"; do jq -n --arg name "${names[index]}" --arg query "${queries[index]}" '{name:$name,query:$query}'; done | jq -s .)" \
  '{schemaVersion:1,collectedAt:$collected_at,source:$source,lookback:$lookback,queries:$queries}' \
  >"${output_dir}/manifest.json"

echo "Prometheus capacity evidence: ${output_dir}"
