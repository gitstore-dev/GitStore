#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
umask 077

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

observability_query=""
exemplar_query=""
correlated_log_query=""
if [[ "${CAPACITY_OBSERVABILITY:-none}" == full ]]; then
  # Allow bounded SDK/collector batches and Tempo's idle-trace indexing to settle.
  sleep 15
  completed_at="$(date +%s)"
  run_seconds=$(( completed_at - started_at + scrape_slack ))
  lookback="${CAPACITY_PROMETHEUS_LOOKBACK:-${run_seconds}s}"
  observability_query='{job=~"gitstore-observability-capacity|gitstore-git-capacity",__name__=~"up|otelcol_.*|gitstore_git_.*|gitstore_telemetry_records_total|grpc_server_.*|process_.*"} or {job="gitstore-otlp-metrics-capacity"}'
  curl -fsS --max-time 120 --max-filesize 67108864 --get --data-urlencode "query=${observability_query}" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" --data-urlencode "step=5s" \
    "${prometheus_url%/}/api/v1/query_range" >"${output_dir}/observability-series.json"
  jq -e --arg git_targets "${CAPACITY_PROMETHEUS_GIT_TARGETS:-}" '
    def has_increment:
      .values as $values |
      any(range(1; $values | length); . as $i |
        ($values[$i][1] | tonumber) > ($values[$i - 1][1] | tonumber));
    .data.result as $series |
    .status == "success" and .data.resultType == "matrix" and
    all(["capacity-otel-collector:8888", "capacity-tempo:3200", "capacity-loki:3100"][]; . as $target |
      any($series[]; .metric.__name__ == "up" and .metric.job == "gitstore-observability-capacity" and
        .metric.instance == $target and any(.values[]; .[1] == "1"))) and
    any($series[]; .metric.__name__ == "up" and .metric.job == "gitstore-otlp-metrics-capacity" and
      .metric.instance == "capacity-otel-collector:8889" and any(.values[]; .[1] == "1")) and
    all(($git_targets | split(",") | map(select(length > 0)))[]; . as $target |
      any($series[]; .metric.__name__ == "up" and .metric.job == "gitstore-git-capacity" and
        .metric.instance == $target and any(.values[]; .[1] == "1")) and
      all(["grpc_server_handled_total", "grpc_server_handling_seconds_count"][]; . as $name |
        any($series[]; .metric.__name__ == $name and .metric.job == "gitstore-git-capacity" and
          .metric.instance == $target and has_increment)))
  ' "${output_dir}/observability-series.json" >/dev/null || {
    echo "collector/store scrape or current-run native RPC completion history is incomplete" >&2; exit 1;
  }
  telemetry_dir="${evidence_dir}/telemetry"
  mkdir -p "${telemetry_dir}"
  exemplar_query='{job=~"gitstore-(api|controller|git|otlp-metrics)-capacity",__name__=~".*_bucket"}'
  curl -fsS --max-time 120 --max-filesize 67108864 --get --data-urlencode "query=${exemplar_query}" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
    "${prometheus_url%/}/api/v1/query_exemplars" >"${output_dir}/exemplars.json"
  jq -e --argjson start "${started_at}" --argjson end "${completed_at}" '
    .status == "success" and (.data | type) == "array" and
    ([.data[].exemplars[]] | length) > 0 and ([.data[].exemplars[]] | length) <= 10000 and
    all(.data[]; (.seriesLabels.job | test("^gitstore-(api|controller|git|otlp-metrics)-capacity$")) and
      (.seriesLabels.__name__ | endswith("_bucket")) and
      all(.exemplars[];
        (.labels.trace_id | test("^[a-f0-9]{32}$")) and .labels.trace_id != "00000000000000000000000000000000" and
        (.labels.span_id | test("^[a-f0-9]{16}$")) and .labels.span_id != "0000000000000000" and
        (.value | tonumber | isfinite) and (.value | tonumber) >= 0 and
        (.timestamp | type) == "number" and .timestamp >= $start and .timestamp <= $end))
  ' "${output_dir}/exemplars.json" >/dev/null || {
    echo "current-run histogram exemplars are missing, malformed or exceeded the 10000-sample bound" >&2; exit 1;
  }
  exemplar_trace_ids="$(jq -r '
    [.data[] | .seriesLabels.job as $job | .exemplars[] | {id:.labels.trace_id,job:$job}] |
    group_by(.id) | sort_by([(map(.job) | unique | length), .[0].id]) | reverse |
    .[:32][] | .[0].id' "${output_dir}/exemplars.json")"
  curl -fsS --max-time 120 --max-filesize 67108864 --get \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" --data-urlencode "limit=1000" \
    "http://127.0.0.1:${CAPACITY_TEMPO_PORT:-3200}/api/search" >"${telemetry_dir}/trace-index.json"
  jq -e '(.traces | length) > 0 and (.traces | length) < 1000' "${telemetry_dir}/trace-index.json" >/dev/null || {
    echo "trace index is empty or exceeded the bounded export" >&2; exit 1;
  }
  { jq -r '.traces[].traceID' "${telemetry_dir}/trace-index.json"; printf '%s\n' "${exemplar_trace_ids}"; } | sort -u | {
  total_trace_bytes=0
  while IFS= read -r trace; do
    [[ "${trace}" =~ ^[a-f0-9]{16,32}$ ]] || { echo "invalid trace identifier" >&2; exit 1; }
    curl -fsS --max-time 15 --max-filesize 2097152 -H 'Accept: application/json' --get \
      --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
      "http://127.0.0.1:${CAPACITY_TEMPO_PORT:-3200}/api/traces/${trace}" >"${telemetry_dir}/${trace}.json"
    jq -e '(.batches // .resourceSpans // []) | length > 0' "${telemetry_dir}/${trace}.json" >/dev/null || {
      echo "trace detail is incomplete" >&2; exit 1;
    }
    total_trace_bytes=$((total_trace_bytes + $(wc -c <"${telemetry_dir}/${trace}.json")))
    (( total_trace_bytes <= 134217728 )) || { echo "trace export exceeded 128MiB bound" >&2; exit 1; }
  done
  }
  trace_pattern="$(printf '%s\n' "${exemplar_trace_ids}" | paste -sd '|' -)"
  correlated_log_query="{service_name=~\"gitstore-.*\"} | trace_id=~\"${trace_pattern}\" | span_id!=\"\""
  curl -fsS --max-time 120 --max-filesize 67108864 --get \
    --data-urlencode "query=${correlated_log_query}" \
    --data-urlencode "start=${started_at}000000000" --data-urlencode "end=${completed_at}000000000" \
    --data-urlencode 'limit=10000' --data-urlencode 'direction=forward' \
    "http://127.0.0.1:${CAPACITY_LOKI_PORT:-3100}/loki/api/v1/query_range" >"${telemetry_dir}/correlated-logs.json"
  jq -e --arg pattern "^(${trace_pattern})$" '
    .status == "success" and ([.data.result[].values[]] | length) > 0 and
    ([.data.result[].values[]] | length) < 10000 and
    all(.data.result[]; (.stream.trace_id | test($pattern)) and
      (.stream.span_id | test("^[a-f0-9]{16}$")) and .stream.span_id != "0000000000000000")
  ' "${telemetry_dir}/correlated-logs.json" >/dev/null || {
    echo "exemplar-linked log evidence is missing, malformed or exceeded the bounded export" >&2; exit 1;
  }
  curl -fsS --max-time 120 --max-filesize 67108864 --get \
    --data-urlencode 'query={service_name=~"gitstore-.*"} |~ "git_http_request_timing|post_receive_admission_timing|post_receive_dispatch_timing|Git RPC completed|graphql HTTP request completed|reconcile attempt completed|profil|telemetry export failed"' \
    --data-urlencode "start=${started_at}000000000" --data-urlencode "end=${completed_at}000000000" \
    --data-urlencode 'limit=10000' --data-urlencode 'direction=forward' \
    "http://127.0.0.1:${CAPACITY_LOKI_PORT:-3100}/loki/api/v1/query_range" >"${telemetry_dir}/phase-logs.json"
  jq -e '.status == "success" and ([.data.result[].values[]] | length) > 0 and
    ([.data.result[].values[]] | length) < 10000' "${telemetry_dir}/phase-logs.json" >/dev/null || {
    echo "phase log export is empty or exceeded the bounded export" >&2; exit 1;
  }
fi

api_query=""
if [[ "${capacity_target}" == "repository/lifecycle" || "${capacity_target}" == "category/hierarchy" ]]; then
  api_query='{job="gitstore-api-capacity",__name__=~"up|gitstore_telemetry_records_total|rpc_.*|gitstore_api_graphql_http_request_duration_seconds_.*|gitstore_git_http_stage_duration_seconds_.*|gitstore_git_file_read_server_phase_seconds_.*|gitstore_datastore_operation_duration_seconds_.*|gitstore_datastore_operation_errors_total|gitstore_datastore_projection_.*|gitstore_datastore_compensation_.*|gitstore_category_mutation_.*|gitstore_category_ancestor_index_.*|gitstore_namespace_admission_stage_duration_seconds_.*|gitstore_namespace_deletion_.*|gitstore_product_deletion_.*|gitstore_catalog_grpc_server_handling_seconds_.*|grpc_client_handling_seconds_.*|gitstore_resource_watch_.*|process_cpu_seconds_total|process_resident_memory_bytes|process_start_time_seconds|go_.*"}'
  curl -fsS --max-time 120 --max-filesize 67108864 --get --data-urlencode "query=${api_query}" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
    --data-urlencode "step=5s" "${prometheus_url%/}/api/v1/query_range" \
    >"${output_dir}/api-series.json"
  jq -e --arg targets "${CAPACITY_PROMETHEUS_TARGETS:-}" '
    .data.result as $series |
    .status == "success" and .data.resultType == "matrix" and ($series | length) > 0 and
    all($series[]; (.values | length) > 0) and
    any($series[]; .metric.__name__ == "up" and any(.values[]; .[1] == "1")) and
    all(($targets | split(",") | map(select(length > 0)))[]; . as $target |
      any($series[]; .metric.__name__ == "up" and .metric.instance == $target and
        any(.values[]; .[1] == "1")))
  ' "${output_dir}/api-series.json" >/dev/null || {
    echo "Prometheus API history is missing or invalid" >&2
    exit 1
  }
fi

scylla_query=""
if [[ -n "${CAPACITY_PROMETHEUS_SCYLLA_TARGETS:-}" ]]; then
  scylla_query='{job="gitstore-scylla-capacity",__name__=~"up|scylla_reactor_utilization|scylla_scheduler_(runtime|waittime)_ms|scylla_storage_proxy_coordinator_(cas_.*|read_latency_.*|write_latency_.*)|scylla_transport_requests_(served|serving|shed|blocked_memory|blocked_memory_current|memory_available)"}'
  curl -fsS --max-time 120 --max-filesize 67108864 --get --data-urlencode "query=${scylla_query}" \
    --data-urlencode "start=${started_at}" --data-urlencode "end=${completed_at}" \
    --data-urlencode "step=5s" "${prometheus_url%/}/api/v1/query_range" \
    >"${output_dir}/scylla-series.json"
  jq -e --arg targets "${CAPACITY_PROMETHEUS_SCYLLA_TARGETS}" '
    .data.result as $series |
    .status == "success" and .data.resultType == "matrix" and ($series | length) > 0 and
    all($series[];
      .metric.job == "gitstore-scylla-capacity" and (.values | length) > 0 and
      all(.values[]; (.[1] | tonumber | isfinite) and (.[1] | tonumber) >= 0)) and
    all(($targets | split(","))[]; . as $target |
      any($series[]; .metric.__name__ == "up" and .metric.instance == $target and
        all(.values[]; .[1] == "0" or .[1] == "1") and any(.values[]; .[1] == "1")) and
      all(["scylla_reactor_utilization", "scylla_storage_proxy_coordinator_cas_write_latency_count"][]; . as $name |
        any($series[]; .metric.__name__ == $name and .metric.instance == $target)))
  ' "${output_dir}/scylla-series.json" >/dev/null || {
    echo "Prometheus Scylla CAS or utilization history is missing or invalid" >&2
    exit 1
  }
fi

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

if [[ "${CAPACITY_OBSERVABILITY:-none}" == full && -n "${CAPACITY_PROMETHEUS_GIT_TARGETS:-}" ]]; then
  names+=(git_rpc_completed git_rpc_method_p95 git_rpc_non_ok_statuses)
  queries+=(
    "sum by (grpc_method,grpc_code,instance) (increase(grpc_server_handled_total{job=\"gitstore-git-capacity\"}[${lookback}]))"
    "histogram_quantile(0.95, sum by (le,grpc_method,instance) (increase(grpc_server_handling_seconds_bucket{job=\"gitstore-git-capacity\"}[${lookback}])))"
    "sum by (grpc_method,grpc_code,instance) (increase(grpc_server_handled_total{job=\"gitstore-git-capacity\",grpc_code!=\"Ok\"}[${lookback}])) or on() vector(0)"
  )
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
  category/hierarchy)
    category_operations='CreateCategoryTaxonomy|UpdateCategoryTaxonomy|UpdateCategoryTaxonomyStatus|ListCategoryTaxonomies|ListCategoryDescendants|CreateProduct|UpdateProduct|UpdateProductStatus|ReplaceCategoryProductMembership|ListCategoryProducts'
    names+=(category_mutation_p95 category_mutation_p99 category_mutation_outcomes category_product_operation_p95 category_product_operation_p99 category_product_operation_samples category_product_operation_errors resource_watch_cdc_discovery_p95 resource_watch_materializer_stage_p95 resource_watch_delivery_p95)
    queries+=(
      "histogram_quantile(0.95, sum by (le,operation,instance) (increase(gitstore_category_mutation_duration_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
      "histogram_quantile(0.99, sum by (le,operation,instance) (increase(gitstore_category_mutation_duration_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
      "sum by (operation,outcome,instance) (increase(gitstore_category_mutation_total{job=\"gitstore-api-capacity\"}[${lookback}]))"
      "histogram_quantile(0.95, sum by (le,operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_bucket{job=\"gitstore-api-capacity\",operation=~\"${category_operations}\"}[${lookback}])))"
      "histogram_quantile(0.99, sum by (le,operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_bucket{job=\"gitstore-api-capacity\",operation=~\"${category_operations}\"}[${lookback}])))"
      "sum by (operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_count{job=\"gitstore-api-capacity\",operation=~\"${category_operations}\"}[${lookback}]))"
      "sum by (operation,backend,instance) (increase(gitstore_datastore_operation_errors_total{job=\"gitstore-api-capacity\",operation=~\"${category_operations}\"}[${lookback}])) or on() vector(0)"
      "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_resource_watch_cdc_discovery_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,stage,instance) (increase(gitstore_resource_watch_materializer_stage_duration_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,instance) (increase(gitstore_resource_watch_delivery_latency_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
    )
    ;;
  repository/lifecycle)
    repository_operations='CreateRepository|UpdateRepository|HasRepositories|HasCatalogResources|CompleteRepositoryDeletion|DeleteNamespaceWithResourceVersion|CreateProduct|MarkProductTerminating|CompleteProductDeletion|CreateCategoryTaxonomy|MarkCategoryTaxonomyDeletion|CompleteCategoryTaxonomyDeletion|CreateProductVariant|DeleteProductVariantWithResourceVersion|CreateCollection|DeleteCollectionWithResourceVersion|CreateFile|DeleteFileWithResourceVersion'
    names+=(repository_datastore_operation_p95 repository_datastore_errors git_http_stage_p95 git_http_stage_samples git_read_phase_p95 catalog_callback_p95)
    queries+=(
      "histogram_quantile(0.95, sum by (le,operation,backend,instance) (increase(gitstore_datastore_operation_duration_seconds_bucket{job=\"gitstore-api-capacity\",operation=~\"${repository_operations}\"}[${lookback}])))"
      "sum by (operation,backend,instance) (increase(gitstore_datastore_operation_errors_total{job=\"gitstore-api-capacity\",operation=~\"${repository_operations}\"}[${lookback}])) or on() vector(0)"
      "histogram_quantile(0.95, sum by (le,operation,phase,outcome,instance) (increase(gitstore_git_http_stage_duration_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
      "sum by (operation,phase,outcome,instance) (increase(gitstore_git_http_stage_duration_seconds_count{job=\"gitstore-api-capacity\"}[${lookback}]))"
      "histogram_quantile(0.95, sum by (le,mode,phase,instance) (increase(gitstore_git_file_read_server_phase_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
      "histogram_quantile(0.95, sum by (le,grpc_method,grpc_code,instance) (increase(gitstore_catalog_grpc_server_handling_seconds_bucket{job=\"gitstore-api-capacity\"}[${lookback}])))"
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
  curl -fsS --max-time 120 --max-filesize 67108864 --get --data-urlencode "query=${queries[index]}" \
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
  --arg scylla_query "${scylla_query}" \
  --arg observability_query "${observability_query}" \
  --arg exemplar_query "${exemplar_query}" \
  --arg correlated_log_query "${correlated_log_query}" \
  --arg observability_mode "${CAPACITY_OBSERVABILITY:-none}" \
  --arg api_query "${api_query}" \
  --arg started_at "$(jq -r '.startedAt' "${metadata}")" \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson queries "$(for index in "${!names[@]}"; do jq -n --arg name "${names[index]}" --arg query "${queries[index]}" '{name:$name,query:$query}'; done | jq -s .)" \
  '{schemaVersion:1,collectedAt:$collected_at,source:$source,capacityTarget:$capacity_target,runStartedAt:$started_at,runCompletedAt:$completed_at,lookback:$lookback,queries:$queries,apiQuery:$api_query,apiStepSeconds:5,controllerQuery:$controller_query,controllerStepSeconds:5,scyllaQuery:$scylla_query,scyllaStepSeconds:5,observabilityMode:$observability_mode,observabilityQuery:$observability_query,observabilityStepSeconds:5,observabilitySettleSeconds:(if $observability_mode == "full" then 15 else 0 end),exemplarQuery:$exemplar_query,exemplarSampleLimit:10000,exemplarTraceLimit:32,correlatedLogQuery:$correlated_log_query}' \
  >"${output_dir}/manifest.json"

echo "Prometheus capacity evidence: ${output_dir}"
