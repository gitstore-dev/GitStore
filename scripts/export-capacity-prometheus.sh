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

controller_query=""
if [[ -n "${CAPACITY_PROMETHEUS_CONTROLLER_TARGETS:-}" ]]; then
  controller_query='{job="gitstore-controller-capacity",__name__=~"up|gitstore_controller_.*|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines"}'
  curl -fsS --max-time 120 --get --data-urlencode "query=${controller_query}" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
    --data-urlencode "step=5s" "${prometheus_url%/}/api/v1/query_range" \
    >"${output_dir}/controller-series.json"
  jq -e --arg targets "${CAPACITY_PROMETHEUS_CONTROLLER_TARGETS}" '
    .data.result as $series |
    .status == "success" and .data.resultType == "matrix" and ($series | length) > 0 and
    all($series[]; (.values | length) > 0) and
    all(($targets | split(","))[]; . as $target |
      any($series[]; .metric.__name__ == "gitstore_controller_process_instance_info" and
        .metric.instance == $target and any(.values[]; .[1] == "1")))
  ' \
    "${output_dir}/controller-series.json" >/dev/null || {
      echo "Prometheus controller history is missing or invalid" >&2
      exit 1
    }
fi

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
  repository/lifecycle)
    names+=(repository_datastore_operation_p95 repository_datastore_errors)
    queries+=(
      "histogram_quantile(0.95, sum by (le,operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_bucket{operation=~\"CreateRepository|UpdateRepository\"}[${lookback}])))"
      "sum by (operation,backend,instance) (increase(gitstore_datastore_operation_errors_total{operation=~\"CreateRepository|UpdateRepository\"}[${lookback}])) or on() vector(0)"
    )
    ;;
esac
case "${capacity_target}" in
  namespace/watch|namespace/recovery|repository/lifecycle)
    names+=(namespace_cdc_discovery_p95 namespace_materializer_stage_p95 namespace_delivery_p95)
    queries+=(
      "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_resource_watch_cdc_discovery_seconds_bucket[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,stage,instance) (increase(gitstore_resource_watch_materializer_stage_duration_seconds_bucket[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_resource_watch_delivery_latency_seconds_bucket[${lookback}])))"
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
  --arg controller_query "${controller_query}" \
  --arg started_at "$(jq -r '.startedAt' "${metadata}")" \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson queries "$(for index in "${!names[@]}"; do jq -n --arg name "${names[index]}" --arg query "${queries[index]}" '{name:$name,query:$query}'; done | jq -s .)" \
  '{schemaVersion:1,collectedAt:$collected_at,source:$source,capacityTarget:$capacity_target,runStartedAt:$started_at,runCompletedAt:$completed_at,lookback:$lookback,queries:$queries,controllerQuery:$controller_query,controllerStepSeconds:5}' \
  >"${output_dir}/manifest.json"

echo "Prometheus capacity evidence: ${output_dir}"
