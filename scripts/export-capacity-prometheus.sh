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

translation_query=""
translation_targets=null
translation_scrape_queries=null
capture_translation_history() {
  curl -fsS --max-time 120 --max-filesize 268435456 --get --data-urlencode "query=$1" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
    --data-urlencode 'step=5s' "${prometheus_url%/}/api/v1/query_range" >"$2"
}
if [[ "${capacity_target}" == api/readiness && "${CAPACITY_TRANSLATION_READINESS:-0}" == 1 ]]; then
  "$(dirname "${BASH_SOURCE[0]}")/write-capacity-prometheus-targets.sh" \
    "${output_dir}/translation-targets.json" "${CAPACITY_PROMETHEUS_TARGETS:-}" \
    "${CAPACITY_PROMETHEUS_CONTROLLER_TARGETS:-}" >/dev/null 2>&1 || {
      echo "Translation Prometheus scrape targets are missing or invalid" >&2
      exit 2
    }
  translation_targets="$(python3 - "${output_dir}/translation-targets.json" <<'PY'
import json
import pathlib
import re
import sys

configuration = json.loads(pathlib.Path(sys.argv[1]).read_text())
targets = {}
for index, role in enumerate(("api", "controller")):
    values = configuration[index]["targets"] if index < len(configuration) else []
    if (len(values) != 2 or len(set(values)) != 2 or
        any(len(value) > 255 for value in values)):
        sys.exit(f"Translation Prometheus {role} targets require two distinct host:port addresses")
    targets[role] = values
    targets[f"{role}Matcher"] = "instance=~" + json.dumps("|".join(re.escape(value) for value in values))
json.dump(targets, sys.stdout)
PY
)"
  translation_query="{job=\"gitstore-api-capacity\",$(jq -r .apiMatcher <<<"${translation_targets}"),__name__=~\"up|target_info|gitstore_api_process_instance_info|gitstore_translation_.*|process_start_time_seconds|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines|go_gc_heap_live_bytes|go_gc_cycles_total\"}"
  capture_translation_history "${translation_query}" "${output_dir}/translation-api-series.json"
  api_scrape_query="timestamp(label_replace(${translation_query}, \"source_metric\", \"\$1\", \"__name__\", \"(.+)\"))"
  capture_translation_history "${api_scrape_query}" "${output_dir}/translation-api-scrape-times.json"
fi

verify_translation_history() {
  jq -e --argjson targets "$2" --arg identity "$3" --arg name_label "${4:-__name__}" '
    .data.result as $series |
    .status == "success" and .data.resultType == "matrix" and
    ((.warnings // []) | length == 0) and
    ($series | type == "array" and length > 0) and
    all($series[]; (.values | type == "array" and length > 0) and
      all(.values[]; type == "array" and length == 2 and
        (.[0] | type == "number" and isnan == false and isinfinite == false and . >= 0) and
        (.[1] | tonumber | isnan == false and isinfinite == false and . >= 0))) and
    all($targets[]; . as $target |
      all(["up",$identity,"process_start_time_seconds","process_cpu_seconds_total","process_resident_memory_bytes","go_goroutines"][];
        . as $name | any($series[]; .metric.instance == $target and .metric[$name_label] == $name and
          (if $name == $identity and $name_label == "__name__" then any(.values[]; .[1] == "1")
           elif $name == "process_start_time_seconds" and $name_label == "__name__" then all(.values[]; (.[1] | tonumber) > 0)
           else true end))))
  ' "$1" >/dev/null || {
    echo "Translation Prometheus process/resource history is missing or invalid" >&2
    return 1
  }
}
if [[ -n "${translation_query}" ]]; then
  verify_translation_history "${output_dir}/translation-api-series.json" \
    "$(jq -c .api <<<"${translation_targets}")" gitstore_api_process_instance_info
  verify_translation_history "${output_dir}/translation-api-scrape-times.json" \
    "$(jq -c .api <<<"${translation_targets}")" gitstore_api_process_instance_info source_metric
fi

if [[ "${capacity_target}" == api/endpoint-split ]]; then
  endpoint_query='{job="gitstore-api-capacity",__name__=~"up|gitstore_api_endpoint_.*|gitstore_api_process_instance_info|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines|go_gc_duration_seconds.*"}'
  curl -fsS --max-time 120 --get --data-urlencode "query=${endpoint_query}" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
    --data-urlencode 'step=5s' "${prometheus_url%/}/api/v1/query_range" >"${output_dir}/endpoint-series.json"
  jq -e '.status=="success" and .data.resultType=="matrix" and (.data.result|length)>0' "${output_dir}/endpoint-series.json" >/dev/null || { echo 'endpoint SDK/resource history is missing' >&2;exit 1; }
fi

controller_query=""
if [[ -n "${CAPACITY_PROMETHEUS_CONTROLLER_TARGETS:-}" ]]; then
  controller_query='{job="gitstore-controller-capacity",__name__=~"up|gitstore_controller_.*|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines"}'
  if [[ -n "${translation_query}" ]]; then
    controller_query="{job=\"gitstore-controller-capacity\",$(jq -r .controllerMatcher <<<"${translation_targets}"),__name__=~\"up|target_info|gitstore_controller_.*|gitstore_translation_.*|process_start_time_seconds|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines|go_gc_heap_live_bytes|go_gc_cycles_total\"}"
    capture_translation_history "${controller_query}" "${output_dir}/controller-series.json"
    controller_scrape_query="timestamp(label_replace(${controller_query}, \"source_metric\", \"\$1\", \"__name__\", \"(.+)\"))"
    capture_translation_history "${controller_scrape_query}" "${output_dir}/translation-controller-scrape-times.json"
    translation_scrape_queries="$(jq -n --arg api "${api_scrape_query}" --arg controller "${controller_scrape_query}" '{api:$api,controller:$controller}')"
  else
    curl -fsS --max-time 120 --get --data-urlencode "query=${controller_query}" \
      --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
      --data-urlencode "step=5s" "${prometheus_url%/}/api/v1/query_range" \
      >"${output_dir}/controller-series.json"
  fi
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
  if [[ -n "${translation_query}" ]]; then
    verify_translation_history "${output_dir}/controller-series.json" \
      "$(jq -c .controller <<<"${translation_targets}")" gitstore_controller_process_instance_info
    verify_translation_history "${output_dir}/translation-controller-scrape-times.json" \
      "$(jq -c .controller <<<"${translation_targets}")" gitstore_controller_process_instance_info source_metric
  fi
fi

names=(
  api_targets_up
)
queries=(
  'up{job="gitstore-api-capacity"}'
)
if [[ -n "${translation_query}" ]]; then
  queries[0]="up{job=\"gitstore-api-capacity\",$(jq -r .apiMatcher <<<"${translation_targets}")}"
fi

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
  --arg translation_query "${translation_query}" \
  --argjson translation_targets "${translation_targets}" \
  --argjson translation_scrape_queries "${translation_scrape_queries}" \
  --arg translation_api_digest "$(if [[ -n "${translation_query}" ]]; then shasum -a 256 "${output_dir}/translation-api-series.json" | awk '{print $1}'; fi)" \
  --arg translation_controller_digest "$(if [[ -n "${translation_query}" ]]; then shasum -a 256 "${output_dir}/controller-series.json" | awk '{print $1}'; fi)" \
  --arg translation_api_times_digest "$(if [[ -n "${translation_query}" ]]; then shasum -a 256 "${output_dir}/translation-api-scrape-times.json" | awk '{print $1}'; fi)" \
  --arg translation_controller_times_digest "$(if [[ -n "${translation_query}" ]]; then shasum -a 256 "${output_dir}/translation-controller-scrape-times.json" | awk '{print $1}'; fi)" \
  --argjson query_start "${started_at}" \
  --argjson query_end "${completed_at}" \
  --slurpfile metadata "${metadata}" \
  --argjson queries "$(for index in "${!names[@]}"; do jq -n --arg name "${names[index]}" --arg query "${queries[index]}" '{name:$name,query:$query}'; done | jq -s .)" \
  '{schemaVersion:1,collectedAt:$collected_at,source:$source,capacityTarget:$capacity_target,runStartedAt:$started_at,runCompletedAt:$completed_at,lookback:$lookback,queries:$queries,controllerQuery:$controller_query,controllerStepSeconds:5} +
    (if $translation_targets == null then {} else {
      runId:$metadata[0].runId,sourceStateSha256:$metadata[0].sourceStateSha256,
      configDigest:$metadata[0].configDigest,environmentDigest:$metadata[0].environmentDigest,
      translationQuery:$translation_query,translationTargets:$translation_targets,translationStepSeconds:5,
      translationResourceDigests:{api:$translation_api_digest,controller:$translation_controller_digest},
      translationScrapeTimeQueries:$translation_scrape_queries,
      translationScrapeTimeDigests:{api:$translation_api_times_digest,controller:$translation_controller_times_digest},
      translationRange:{startUnixSeconds:$query_start,endUnixSeconds:$query_end,stepSeconds:5}
    } end)' \
  >"${output_dir}/manifest.json"

echo "Prometheus capacity evidence: ${output_dir}"
