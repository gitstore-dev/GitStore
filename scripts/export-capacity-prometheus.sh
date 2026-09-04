#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
prometheus_url="${2:?Prometheus URL is required}"
metadata="${evidence_dir}/metadata.json"
[[ -r "${metadata}" ]] || { echo "capacity metadata is required to scope Prometheus evidence" >&2; exit 2; }
started_at="$(jq -er '.startedAt | fromdateiso8601' "${metadata}")"
completed_at="$(date +%s)"
scrape_slack="${CAPACITY_PROMETHEUS_SCRAPE_SLACK_SECONDS:-15}"
[[ "${scrape_slack}" =~ ^[0-9]+$ ]] || { echo "CAPACITY_PROMETHEUS_SCRAPE_SLACK_SECONDS must be a non-negative integer" >&2; exit 2; }
run_seconds=$(( completed_at - started_at + scrape_slack ))
(( run_seconds > 0 )) || { echo "capacity metadata startedAt must precede Prometheus export" >&2; exit 2; }
lookback="${CAPACITY_PROMETHEUS_LOOKBACK:-${run_seconds}s}"
if [[ ! "${lookback}" =~ ^[1-9][0-9]*[smhdwy]$ ]]; then
  echo "CAPACITY_PROMETHEUS_LOOKBACK must be a positive Prometheus duration such as 60m" >&2
  exit 2
fi
output_dir="${evidence_dir}/prometheus"
mkdir -p "${output_dir}"
capacity_target="$(jq -r '[.target, (.scenario // .profile)] | join("/")' "${metadata}")"

names=(
  api_targets_up
)
queries=(
  'up{job="gitstore-api-capacity"}'
)

case "${capacity_target}" in
  namespace/admission|namespace/watch|namespace/recovery)
    names+=(namespace_admission_stage_p95 namespace_datastore_operation_p95 namespace_datastore_errors)
    queries+=(
      "histogram_quantile(0.95, sum by (le,stage,instance) (increase(gitstore_namespace_admission_stage_duration_seconds_bucket[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_bucket{operation=~\"CreateNamespace|UpdateNamespace\"}[${lookback}])))"
      "sum by (operation,backend,instance) (increase(gitstore_datastore_operation_errors_total{operation=~\"CreateNamespace|UpdateNamespace\"}[${lookback}])) or on() vector(0)"
    )
    ;;
esac
case "${capacity_target}" in
  namespace/watch|namespace/recovery)
    names+=(namespace_cdc_discovery_p95 namespace_materializer_stage_p95 namespace_delivery_p95)
    queries+=(
      "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_namespace_watch_cdc_discovery_seconds_bucket[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,stage,instance) (increase(gitstore_namespace_watch_materializer_stage_duration_seconds_bucket[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_namespace_watch_delivery_latency_seconds_bucket[${lookback}])))"
    )
    ;;
esac

for index in "${!names[@]}"; do
  response="${output_dir}/${names[index]}.json"
  curl -fsS --get --data-urlencode "query=${queries[index]}" \
    "${prometheus_url%/}/api/v1/query" >"${response}"
  jq -e '.status == "success" and (.data.result | length) > 0' "${response}" >/dev/null || {
    echo "Prometheus query ${names[index]} returned no samples" >&2
    exit 1
  }
  if [[ "${names[index]}" == "api_targets_up" ]]; then
    jq -e 'all(.data.result[]; .value[1] == "1")' "${response}" >/dev/null || {
      echo "one or more configured API scrape targets are down" >&2
      exit 1
    }
  fi
done

jq -n \
  --arg collected_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg source "${prometheus_url%/}" \
  --arg capacity_target "${capacity_target}" \
  --arg lookback "${lookback}" \
  --arg started_at "$(jq -r '.startedAt' "${metadata}")" \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson queries "$(for index in "${!names[@]}"; do jq -n --arg name "${names[index]}" --arg query "${queries[index]}" '{name:$name,query:$query}'; done | jq -s .)" \
  '{schemaVersion:1,collectedAt:$collected_at,source:$source,capacityTarget:$capacity_target,runStartedAt:$started_at,runCompletedAt:$completed_at,lookback:$lookback,queries:$queries}' \
  >"${output_dir}/manifest.json"

echo "Prometheus capacity evidence: ${output_dir}"
