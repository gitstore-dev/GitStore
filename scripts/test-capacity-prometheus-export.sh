#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
trace_exporter="$(sed -n '/^  otlphttp\/traces:/,/^  otlphttp\/logs:/p' "${repo_root}/tests/capacity/prometheus/otel-collector.yml")"
for setting in \
  '    timeout: 3s' \
  '      queue_size: 128' \
  '      num_consumers: 2' \
  '      initial_interval: 250ms' \
  '      max_interval: 1s' \
  '      max_elapsed_time: 30s'; do
  grep -Fxq "${setting}" <<<"${trace_exporter}" || {
    echo "trace exporter retry cadence or fixed capacity bound changed: ${setting}" >&2
    exit 1
  }
done
test_dir="${repo_root}/.capacity-prometheus-export-test.$$"
mkdir "${test_dir}"
trap 'rm -rf "${test_dir}"' EXIT
mkdir -p "${test_dir}/bin" "${test_dir}/evidence"
printf '#!/usr/bin/env bash\nprintf '\''{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}'\''\n' >"${test_dir}/bin/curl"
chmod +x "${test_dir}/bin/curl"
printf '{"target":"namespace","scenario":"watch","startedAt":"2026-09-04T00:00:00Z"}\n' >"${test_dir}/evidence/metadata.json"

PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/evidence" http://prometheus.invalid >/dev/null

jq -e '.schemaVersion == 1 and .lookback == "30m" and (.queries | length) == 7 and ([.queries[] | select(.name != "api_targets_up")] | all(.[]; .query | contains("[30m]"))) and (.queries[] | select(.name == "namespace_datastore_errors") | .query | contains("or on() vector(0)"))' "${test_dir}/evidence/prometheus/manifest.json" >/dev/null
for name in api_targets_up namespace_admission_stage_p95 namespace_datastore_operation_p95 namespace_datastore_errors namespace_cdc_discovery_p95 namespace_materializer_stage_p95 namespace_delivery_p95; do
  jq -e '.status == "success" and (.data.result | length) == 1' "${test_dir}/evidence/prometheus/${name}.json" >/dev/null
done

if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=invalid \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/evidence" http://prometheus.invalid >/dev/null 2>&1; then
  echo "invalid Prometheus lookback unexpectedly succeeded" >&2
  exit 1
fi

printf '#!/usr/bin/env bash\nprintf '\''{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"0"]}]}}'\''\n' >"${test_dir}/bin/curl"
if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/evidence" http://prometheus.invalid >/dev/null 2>&1; then
  echo "down Prometheus scrape target unexpectedly produced evidence" >&2
  exit 1
fi

printf '#!/usr/bin/env bash\nprintf '\''{"status":"success","data":{"resultType":"vector","result":[]}}'\''\n' >"${test_dir}/bin/curl"
if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/evidence" http://prometheus.invalid >/dev/null 2>&1; then
  echo "empty Prometheus results unexpectedly produced evidence" >&2
  exit 1
fi

targets_file="${test_dir}/prometheus/targets.json"
"${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
  "${targets_file}" "api-a.internal:4000,api-b.internal:4001"
jq -e '.[0].targets == ["api-a.internal:4000","api-b.internal:4001"]' "${targets_file}" >/dev/null
if "${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
  "${targets_file}" 'bad target' >/dev/null 2>&1; then
  echo "invalid Prometheus target unexpectedly succeeded" >&2
  exit 1
fi

mkdir -p "${test_dir}/readiness"
printf '{"target":"api","scenario":"readiness","startedAt":"2026-09-04T00:00:00Z"}\n' >"${test_dir}/readiness/metadata.json"
printf '#!/usr/bin/env bash\nprintf '\''{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}'\''\n' >"${test_dir}/bin/curl"
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/readiness" http://prometheus.invalid >/dev/null
jq -e '.capacityTarget == "api/readiness" and (.queries | map(.name)) == ["api_targets_up"]' \
  "${test_dir}/readiness/prometheus/manifest.json" >/dev/null
for observability_mode in none prometheus; do
  PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY="${observability_mode}" \
    CAPACITY_PROMETHEUS_GIT_TARGETS=git.internal:50052 \
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/readiness" http://prometheus.invalid >/dev/null
  jq -e --arg mode "${observability_mode}" '.observabilityMode == $mode and
    .observabilityQuery == "" and .observabilitySettleSeconds == 0 and
    .queries == [{name:"api_targets_up",query:"up{job=\"gitstore-api-capacity\"}"}]' \
    "${test_dir}/readiness/prometheus/manifest.json" >/dev/null
  [[ ! -e "${test_dir}/readiness/prometheus/observability-series.json" &&
    ! -e "${test_dir}/readiness/telemetry" ]] || {
    echo "non-full mode unexpectedly exported collector or telemetry evidence" >&2
    exit 1
  }
done

mkdir -p "${test_dir}/repository"
printf '{"target":"repository","scenario":"lifecycle","startedAt":"2026-09-04T00:00:00Z","runId":"export-fixture","gitRevision":"1111111111111111111111111111111111111111","sourceStateSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","configDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","environmentDigest":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}\n' >"${test_dir}/repository/metadata.json"
cat >"${test_dir}/bin/sleep" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${test_dir}/bin/sleep"
cat >"${test_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  */api/v1/query_range)
    response='{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","instance":"api-a.internal:4000"},"values":[[1,"1"],[2,"1"]]},{"metric":{"__name__":"gitstore_git_http_stage_duration_seconds_count","operation":"receive_pack","phase":"handler","outcome":"success","instance":"api-a.internal:4000"},"values":[[1,"10"],[2,"20"]]},{"metric":{"__name__":"gitstore_authz_decision_duration_seconds_count","action":"namespace.read","outcome":"allow","instance":"api-a.internal:4000"},"values":[[1,"10"],[2,"20"]]},{"metric":{"__name__":"gitstore_api_process_instance_info","instance":"api-a.internal:4000","instance_id":"process-api-a"},"values":[[1,"1"],[2,"1"]]}]}}'
    case "${AUTHZ_EXPORT_FAILURE:-}" in
      nonfinite) jq '.data.result[2].values[0][1] = "NaN"' <<<"${response}" ;;
      negative) jq '.data.result[2].values[0][1] = "-1"' <<<"${response}" ;;
      missing) jq 'del(.data.result[2])' <<<"${response}" ;;
      stale) jq '.data.result[2].values[1][1] = "10"' <<<"${response}" ;;
      missing-identity) jq 'del(.data.result[3])' <<<"${response}" ;;
      invalid-identity) jq '.data.result[3].metric.instance_id = ""' <<<"${response}" ;;
      reset-only) jq '.data.result[2].values = [[1,"20"],[2,"0"]]' <<<"${response}" ;;
      reset-recovered) jq '.data.result[2].values = [[1,"20"],[2,"0"],[3,"1"]]' <<<"${response}" ;;
      second-up-only|second-active)
        jq --arg variant "${AUTHZ_EXPORT_FAILURE}" '
          .data.result += [{metric:{__name__:"up",instance:"api-b.internal:4001"},values:[[1,"1"],[2,"1"]]},
            (.data.result[3] | .metric.instance = "api-b.internal:4001" | .metric.instance_id = "process-api-b")] |
          if $variant == "second-active" then
            .data.result += [(.data.result[2] | .metric.instance = "api-b.internal:4001")]
          else . end' <<<"${response}" ;;
      subject|resource_name|request_id|policy_digest|catalog_digest|fixture_digest)
        jq --arg field "${AUTHZ_EXPORT_FAILURE}" '.data.result[2].metric[$field] = "forbidden"' <<<"${response}" ;;
      *) printf '%s' "${response}" ;;
    esac
    ;;
  *) printf '{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}' ;;
esac
EOF
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" http://prometheus.invalid >/dev/null
jq -e '.capacityTarget == "repository/lifecycle" and (.queries | map(.name)) == ["api_targets_up","repository_datastore_operation_p95","repository_datastore_errors","git_http_stage_p95","git_http_stage_samples","git_read_phase_p95","catalog_callback_p95","namespace_cdc_discovery_p95","namespace_materializer_stage_p95","namespace_delivery_p95"] and .apiStepSeconds == 5 and (.apiQuery | contains("gitstore_git_http_stage_duration_seconds")) and (.apiQuery | contains("process_start_time_seconds"))' \
  "${test_dir}/repository/prometheus/manifest.json" >/dev/null
jq -e '.data.result[1].metric.phase == "handler" and .data.result[1].values == [[1,"10"],[2,"20"]]' \
  "${test_dir}/repository/prometheus/api-series.json" >/dev/null
jq -e --slurpfile metadata "${test_dir}/repository/metadata.json" '
  .runContext == ($metadata[0] | {runId,gitRevision,sourceStateSha256,configDigest,environmentDigest})
' "${test_dir}/repository/prometheus/manifest.json" >/dev/null
jq -e 'any(.data.result[]; .metric.__name__ == "gitstore_authz_decision_duration_seconds_count" and
  .metric.action == "namespace.read" and .metric.outcome == "allow" and .values == [[1,"10"],[2,"20"]])' \
  "${test_dir}/repository/prometheus/api-series.json" >/dev/null
for failure in nonfinite negative subject resource_name request_id policy_digest catalog_digest fixture_digest; do
  if PATH="${test_dir}/bin:${PATH}" AUTHZ_EXPORT_FAILURE="${failure}" \
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" \
    http://prometheus.invalid >/dev/null 2>&1; then
    echo "invalid authorization metric history unexpectedly succeeded: ${failure}" >&2
    exit 1
  fi
done
jq '.mode = "alpha"' "${test_dir}/repository/metadata.json" >"${test_dir}/repository/metadata.next.json"
mv "${test_dir}/repository/metadata.next.json" "${test_dir}/repository/metadata.json"
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000 \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" http://prometheus.invalid >/dev/null
for failure in missing stale reset-only missing-identity invalid-identity; do
  if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000 \
    AUTHZ_EXPORT_FAILURE="${failure}" \
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" \
    http://prometheus.invalid >/dev/null 2>&1; then
    echo "missing or stale alpha authorization history unexpectedly succeeded: ${failure}" >&2
    exit 1
  fi
done
if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000,api-b.internal:4001 \
  AUTHZ_EXPORT_FAILURE=second-up-only \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" \
  http://prometheus.invalid >/dev/null 2>&1; then
  echo "missing second API authorization history unexpectedly succeeded" >&2
  exit 1
fi
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000,api-b.internal:4001 \
  AUTHZ_EXPORT_FAILURE=second-active \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" http://prometheus.invalid >/dev/null
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000 \
  AUTHZ_EXPORT_FAILURE=reset-recovered \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" http://prometheus.invalid >/dev/null
jq 'del(.mode)' "${test_dir}/repository/metadata.json" >"${test_dir}/repository/metadata.next.json"
mv "${test_dir}/repository/metadata.next.json" "${test_dir}/repository/metadata.json"
cp "${test_dir}/repository/metadata.json" "${test_dir}/repository/metadata.saved.json"
for mutation in '.runId = ("x" * 129)' '.gitRevision = "not-a-revision"' \
  '.sourceStateSha256 = []' '.configDigest = "not-a-digest"' '.environmentDigest = 42'; do
  jq "${mutation}" "${test_dir}/repository/metadata.saved.json" >"${test_dir}/repository/metadata.json"
  if PATH="${test_dir}/bin:${PATH}" \
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" \
    http://prometheus.invalid >/dev/null 2>&1; then
    echo "invalid run/fixture provenance unexpectedly succeeded: ${mutation}" >&2
    exit 1
  fi
done
mv "${test_dir}/repository/metadata.saved.json" "${test_dir}/repository/metadata.json"
jq -e '(.apiQuery | contains("gitstore_namespace_deletion_.*")) and
  (.apiQuery | contains("gitstore_authz_.*")) and
  (.apiQuery | contains("gitstore_api_process_instance_info")) and
  (.apiQuery | contains("gitstore_product_deletion_.*")) and
  (.apiQuery | contains("gitstore_api_graphql_http_request_duration_seconds_.*")) and
  all(.queries[] | select(.name == "repository_datastore_operation_p95" or .name == "repository_datastore_errors");
    .query | contains("HasRepositories") and contains("HasCatalogResources") and
    contains("CompleteRepositoryDeletion") and contains("DeleteNamespaceWithResourceVersion") and
    contains("CreateProduct") and contains("MarkProductTerminating") and contains("CompleteProductDeletion") and
    contains("CreateCategoryTaxonomy") and contains("MarkCategoryTaxonomyDeletion") and contains("CompleteCategoryTaxonomyDeletion") and
    contains("CreateProductVariant") and contains("DeleteProductVariantWithResourceVersion") and
    contains("CreateCollection") and contains("DeleteCollectionWithResourceVersion") and
    contains("CreateFile") and contains("DeleteFileWithResourceVersion") and
    contains("gitstore-api-capacity"))' "${test_dir}/repository/prometheus/manifest.json" >/dev/null
if CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000,api-b.internal:4001 \
  PATH="${test_dir}/bin:${PATH}" "${repo_root}/scripts/export-capacity-prometheus.sh" \
  "${test_dir}/repository" http://prometheus.invalid >/dev/null 2>&1; then
  echo "missing second API history unexpectedly succeeded" >&2
  exit 1
fi

mkdir -p "${test_dir}/category"
printf '{"target":"category","scenario":"hierarchy","startedAt":"2026-09-04T00:00:00Z"}\n' >"${test_dir}/category/metadata.json"
cat >"${test_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  */api/v1/query_range)
    jq -n --arg missing "${MISSING_CATEGORY_REPLICA:-}" '
      {status:"success",data:{resultType:"matrix",result:(
        ["api-a.internal:4000","api-b.internal:4001"] | map(select(. != $missing)) |
        map(. as $instance |
          [{metric:{__name__:"up",instance:$instance},values:[[1,"1"],[2,"1"]]},
           {metric:{__name__:"gitstore_datastore_operation_duration_seconds_count",operation:"ListCategoryProducts",
             backend:"scylla",instance:$instance},values:[[1,"10"],[2,"20"]]}]) | add
      )}}'
    ;;
  *) printf '{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}' ;;
esac
EOF
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=30m \
  CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000,api-b.internal:4001 \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/category" http://prometheus.invalid >/dev/null
jq -e '.capacityTarget == "category/hierarchy" and .apiStepSeconds == 5 and
  (.apiQuery | contains("gitstore_category_mutation_.*")) and
  (.apiQuery | contains("gitstore_datastore_operation_errors_total")) and
  (.apiQuery | contains("gitstore_category_ancestor_index_.*")) and
  (.apiQuery | contains("gitstore_datastore_projection_.*")) and
  (.apiQuery | contains("process_start_time_seconds")) and
  (.queries | map(.name)) == ["api_targets_up","category_mutation_p95","category_mutation_p99",
    "category_mutation_outcomes","category_product_operation_p95","category_product_operation_p99",
    "category_product_operation_samples","category_product_operation_errors","resource_watch_cdc_discovery_p95",
    "resource_watch_materializer_stage_p95","resource_watch_delivery_p95"] and
  all(.queries[] | select(.name != "api_targets_up"); .query | contains("[30m]")) and
  all(.queries[] | select(.name | startswith("category_product_operation")); .query |
    contains("ListCategoryProducts") and contains("ReplaceCategoryProductMembership") and
    contains("ListCategoryDescendants") and contains("ListCategoryTaxonomies") and
    contains("UpdateProductStatus") and contains("gitstore-api-capacity")) and
  (.queries[] | select(.name == "category_product_operation_errors") | .query | contains("or on() vector(0)"))' \
  "${test_dir}/category/prometheus/manifest.json" >/dev/null
jq -e '[.data.result[] | select(.metric.operation == "ListCategoryProducts")] |
  length == 2 and all(.[]; .values == [[1,"10"],[2,"20"]] and .metric.backend == "scylla")' \
  "${test_dir}/category/prometheus/api-series.json" >/dev/null
if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_TARGETS=api-a.internal:4000,api-b.internal:4001 \
  MISSING_CATEGORY_REPLICA=api-b.internal:4001 \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/category" http://prometheus.invalid >/dev/null 2>&1; then
  echo "missing second Category API history unexpectedly succeeded" >&2
  exit 1
fi

"${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
  "${targets_file}" "api-a.internal:4000,api-b.internal:4001" "controller-a.internal:5001,controller-b.internal:5001"
jq -e '.[1] == {targets:["controller-a.internal:5001","controller-b.internal:5001"],labels:{job:"gitstore-controller-capacity"}}' "${targets_file}" >/dev/null
if "${repo_root}/scripts/write-capacity-prometheus-targets.sh" "${targets_file}" "api-a.internal:4000" "bad controller" >/dev/null 2>&1; then
  echo "invalid controller scrape target unexpectedly succeeded" >&2
  exit 1
fi

cat >"${test_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *gitstore-api-capacity*/api/v1/query_range)
    printf '{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"up","instance":"api-a.internal:4000"},"values":[[1,"1"]]}]}}'
    ;;
  */api/v1/query_range)
    printf '{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"__name__":"gitstore_controller_stalled_workers","kind":"Product"},"values":[[1,"0"],[2,"1"]]},{"metric":{"__name__":"gitstore_controller_process_instance_info","instance":"controller-a.internal:5001","instance_id":"test-process-00001"},"values":[[1,"1"]]}]}}'
    ;;
  *) printf '{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}' ;;
esac
EOF
CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=controller-a.internal:5001 \
  PATH="${test_dir}/bin:${PATH}" "${repo_root}/scripts/export-capacity-prometheus.sh" \
  "${test_dir}/repository" http://prometheus.invalid >/dev/null
jq -e '.data.result[0].metric.kind == "Product" and .data.result[0].values == [[1,"0"],[2,"1"]]' \
  "${test_dir}/repository/prometheus/controller-series.json" >/dev/null
jq -e '.controllerStepSeconds == 5 and (.controllerQuery | contains("gitstore-controller-capacity"))' \
  "${test_dir}/repository/prometheus/manifest.json" >/dev/null
if CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=controller-a.internal:5001,controller-b.internal:5001 \
  PATH="${test_dir}/bin:${PATH}" "${repo_root}/scripts/export-capacity-prometheus.sh" \
  "${test_dir}/repository" http://prometheus.invalid >/dev/null 2>&1; then
  echo "missing second controller history unexpectedly succeeded" >&2
  exit 1
fi
printf '#!/usr/bin/env bash\nprintf '\''{"status":"success","data":{"resultType":"matrix","result":[]}}'\''\n' >"${test_dir}/bin/curl"
if CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=controller-a.internal:5001 \
  PATH="${test_dir}/bin:${PATH}" "${repo_root}/scripts/export-capacity-prometheus.sh" \
  "${test_dir}/repository" http://prometheus.invalid >/dev/null 2>&1; then
  echo "missing controller history unexpectedly succeeded" >&2
  exit 1
fi

"${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
  "${targets_file}" "api-a.internal:4000" "" "git.internal:50052"
jq -e '.[1] == {targets:["git.internal:50052"],labels:{job:"gitstore-git-capacity"}}' "${targets_file}" >/dev/null
if "${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
  "${targets_file}" "api-a.internal:4000" "" "bad git target" >/dev/null 2>&1; then
  echo "invalid Git scrape target unexpectedly succeeded" >&2
  exit 1
fi

"${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
  "${targets_file}" "api-a.internal:4000" "controller-a.internal:5001" "git.internal:50052" \
  "scylla-a.internal:9180,scylla-b.internal:9180"
jq -e 'length == 4 and .[3] == {targets:["scylla-a.internal:9180","scylla-b.internal:9180"],
  labels:{job:"gitstore-scylla-capacity"}}' "${targets_file}" >/dev/null
for invalid in "bad scylla target" "scylla:0" "scylla:65536" "scylla:9180," "scylla:9180,,peer:9180"; do
  if "${repo_root}/scripts/write-capacity-prometheus-targets.sh" \
    "${targets_file}" "api-a.internal:4000" "" "" "${invalid}" >/dev/null 2>&1; then
    echo "invalid Scylla scrape target unexpectedly succeeded: ${invalid}" >&2
    exit 1
  fi
done
jq -e '.scyllaQuery == ""' "${test_dir}/readiness/prometheus/manifest.json" >/dev/null
[[ ! -e "${test_dir}/readiness/prometheus/scylla-series.json" ]]
cat >"${test_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *gitstore-scylla-capacity*/api/v1/query_range)
    [[ "$*" == *"step=5s"* && "$*" == *"scylla_scheduler_"* && "$*" == *"scylla_transport_requests_"* ]] || exit 2
    jq -n --arg failure "${SCYLLA_EXPORT_FAILURE:-}" '
      {status:"success",data:{resultType:"matrix",result:(
        if $failure == "empty" then [] else
          ["scylla-a.internal:9180","scylla-b.internal:9180"] |
          map(select($failure != "missing-node" or . != "scylla-b.internal:9180")) |
          map(. as $instance |
            ["up","scylla_reactor_utilization","scylla_storage_proxy_coordinator_cas_write_latency_count"] |
            map(select($failure != "missing-cas" or . != "scylla_storage_proxy_coordinator_cas_write_latency_count")) |
            map(. as $name | {
              metric:{__name__:$name,instance:$instance,job:
                (if $failure == "wrong-job" then "gitstore-api-capacity" else "gitstore-scylla-capacity" end)},
              values:(if $failure == "nonfinite" then [[1,"NaN"]]
                elif $failure == "negative" then [[1,"-1"]]
                elif $failure == "empty-values" then []
                elif $name == "up" then
                  (if $failure == "down" then [[1,"0"],[2,"0"]]
                    elif $failure == "fault" then [[1,"1"],[2,"0"]]
                    else [[1,"1"],[2,"1"]] end)
                elif $failure == "reset" then [[1,"10"],[2,"1"]]
                else [[1,"0"],[2,"2"]] end)
            })) | add
        end
      )}}'
    ;;
  *) printf '{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}' ;;
esac
EOF
for valid in "" fault reset; do
  PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_SCYLLA_TARGETS=scylla-a.internal:9180,scylla-b.internal:9180 \
    SCYLLA_EXPORT_FAILURE="${valid}" "${repo_root}/scripts/export-capacity-prometheus.sh" \
    "${test_dir}/readiness" http://prometheus.invalid >/dev/null
  jq -e '.scyllaStepSeconds == 5 and (.scyllaQuery | contains("gitstore-scylla-capacity")) and
    (.scyllaQuery | contains("cas_.*")) and (.scyllaQuery | contains("scylla_reactor_utilization"))' \
    "${test_dir}/readiness/prometheus/manifest.json" >/dev/null
  jq -e '.data.result | length == 6' "${test_dir}/readiness/prometheus/scylla-series.json" >/dev/null
done
for invalid in empty missing-node missing-cas nonfinite negative empty-values wrong-job down; do
  if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_SCYLLA_TARGETS=scylla-a.internal:9180,scylla-b.internal:9180 \
    SCYLLA_EXPORT_FAILURE="${invalid}" "${repo_root}/scripts/export-capacity-prometheus.sh" \
    "${test_dir}/readiness" http://prometheus.invalid >/dev/null 2>&1; then
    echo "invalid Scylla history unexpectedly succeeded: ${invalid}" >&2
    exit 1
  fi
done

cat >"${test_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  *gitstore-observability-capacity*/api/v1/query_range)
    [[ "$*" == *' or {job="gitstore-otlp-metrics-capacity"}'* ]] || {
      echo "observability export must retain all OTLP metric names" >&2
      exit 2
    }
    jq -n --arg failure "${EXPORT_FAILURE:-}" '
      {status:"success",data:{resultType:"matrix",result:((
        ["capacity-otel-collector:8888","capacity-tempo:3200","capacity-loki:3100"] |
        map(select($failure != "missing-store" or . != "capacity-loki:3100") |
          {metric:{__name__:"up",job:"gitstore-observability-capacity",instance:.},values:[[1,"1"]]})
      ) + (if $failure == "missing-otlp" then [] else
        [{metric:{__name__:"up",job:"gitstore-otlp-metrics-capacity",instance:"capacity-otel-collector:8889"},
          values:[[1,(if $failure == "down-otlp" then "0" else "1" end)]]},
         {metric:{__name__:"capacity_otlp_probe_total",job:"gitstore-otlp-metrics-capacity",
           instance:"capacity-otel-collector:8889",service_name:"gitstore-probe"},values:[[1,"1"],[2,"2"]]}] end) +
        (["otelcol_exporter_sent_metric_points_total","otelcol_exporter_send_failed_metric_points_total",
          "otelcol_exporter_queue_size"] |
          map({metric:{__name__:.,job:"gitstore-observability-capacity",instance:"capacity-otel-collector:8888",
            exporter:"prometheus/metrics"},values:[[1,"0"],[2,"1"]]})) +
        (if $failure == "missing-git" then [] else
        [{metric:{__name__:"up",job:"gitstore-git-capacity",instance:"git.internal:50052"},values:[[1,"1"]]}] end) +
        (if $failure == "missing-rpc" then [] else
          ["grpc_server_handled_total","grpc_server_handling_seconds_count"] |
          map({metric:{__name__:.,job:"gitstore-git-capacity",instance:"git.internal:50052",grpc_method:"GetFiles"},
            values:(if $failure == "zero-rpc" then [[1,"0"],[2,"0"]]
              elif $failure == "stale-rpc" then [[1,"10"],[2,"10"]]
              else [[1,"10"],[2,"20"]] end)})
        end))}}'
    ;;
  */api/search)
    case "${EXPORT_FAILURE:-}" in
      empty-traces) printf '{"traces":[]}' ;;
      truncated-traces) jq -n '{traces:[range(1000) | {traceID:"b71bb94adb59498d8a3a6f1079a3fdaa"}]}' ;;
      invalid-trace) printf '{"traces":[{"traceID":"../escape"}]}' ;;
      *) printf '{"traces":[{"traceID":"c71bb94adb59498d8a3a6f1079a3fdaa"}]}' ;;
    esac
    ;;
  */api/v1/query_exemplars)
    jq -n --arg failure "${EXPORT_FAILURE:-}" '
      {status:"success",data:(if $failure == "empty-exemplars" then [] else
        [{seriesLabels:{__name__:"grpc_server_handling_seconds_bucket",job:"gitstore-git-capacity",
            instance:"git.internal:50052",grpc_method:"GetFiles",le:"0.005"},
          exemplars:[range(if $failure == "truncated-exemplars" then 10001 else 1 end) |
            {labels:{
              trace_id:(if $failure == "invalid-exemplar-trace" then "../escape"
                elif $failure == "zero-exemplar-trace" then "00000000000000000000000000000000"
                else "b71bb94adb59498d8a3a6f1079a3fdaa" end),
              span_id:(if $failure == "invalid-exemplar-span" then "" else "893c48c0a0b61a2e" end)},
             value:(if $failure == "invalid-exemplar-value" then "NaN"
               elif $failure == "negative-exemplar-value" then "-0.004" else "0.004" end),
             timestamp:(if $failure == "stale-exemplar" then 1 else
               (if $failure == "future-exemplar" then "2100-09-04T00:00:01Z"
                 else "2026-09-04T00:00:01Z" end | fromdateiso8601) end)}]}] end)}'
    ;;
  */api/traces/*)
    [[ "$*" == *"start="* && "$*" == *"end="* ]] || {
      echo "trace detail request must be scoped to the current run" >&2
      exit 2
    }
    if [[ "$*" == *'/api/traces/b71bb94adb59498d8a3a6f1079a3fdaa'* ]]; then
      case "${EXPORT_FAILURE:-}" in
        exemplar-trace-error) exit 22 ;;
        empty-exemplar-detail) printf '{"batches":[]}'; exit 0 ;;
      esac
    fi
    case "${EXPORT_FAILURE:-}" in
      trace-error) exit 22 ;;
      empty-detail) printf '{"batches":[]}' ;;
      *) printf '{"batches":[{"resource":{"attributes":[]}}]}' ;;
    esac
    ;;
  */loki/api/v1/query_range)
    if [[ "$*" == *'trace_id=~'* ]]; then
      jq -n --arg failure "${EXPORT_FAILURE:-}" '
        {status:"success",data:{result:(if $failure == "empty-correlated-logs" then [] else
          [{stream:{service_name:"gitstore-api",
              trace_id:(if $failure == "unmatched-correlated-trace" then "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
                else "b71bb94adb59498d8a3a6f1079a3fdaa" end),
              span_id:(if $failure == "invalid-correlated-span" then ""
                elif $failure == "zero-correlated-span" then "0000000000000000"
                else "893c48c0a0b61a2e" end)},
            values:[range(if $failure == "truncated-correlated-logs" then 10000 else 1 end) |
              ["1788480001000000000","sampled request completed"]]}] end)}}'
      exit 0
    fi
    [[ "$*" == *"auth provider registry reload accepted"* &&
      "$*" == *"auth provider registry reload rejected"* ]] || {
      echo "phase query omitted complete-registry reload diagnostics" >&2; exit 1;
    }
    case "${EXPORT_FAILURE:-}" in
      empty-logs) printf '{"status":"success","data":{"result":[]}}' ;;
      truncated-logs) jq -n '{status:"success",data:{result:[{values:[range(10000) | ["1","phase"]]}]}}' ;;
      *) printf '{"status":"success","data":{"result":[{"values":[["1","git_http_request_timing contract"]]}]}}' ;;
    esac
    ;;
  *) printf '{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}' ;;
esac
EOF
PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY=full \
  CAPACITY_PROMETHEUS_GIT_TARGETS=git.internal:50052 \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/readiness" http://prometheus.invalid >/dev/null
jq -e '.observabilityMode == "full" and .observabilityStepSeconds == 5 and
  .observabilitySettleSeconds == 15 and (.observabilityQuery | contains("otelcol_.*")) and
  (.observabilityQuery | contains("gitstore_telemetry_records_total")) and
  .observabilityQuery == "{job=~\"gitstore-observability-capacity|gitstore-git-capacity\",__name__=~\"up|otelcol_.*|gitstore_git_.*|gitstore_telemetry_records_total|grpc_server_.*|process_.*\"} or {job=\"gitstore-otlp-metrics-capacity\"}" and
  any(.queries[]; .name == "git_rpc_completed" and (.query | contains("grpc_server_handled_total"))) and
  any(.queries[]; .name == "git_rpc_method_p95" and (.query | contains("grpc_server_handling_seconds_bucket"))) and
  any(.queries[]; .name == "git_rpc_non_ok_statuses" and (.query | contains("grpc_code!=\"Ok\"")))' "${test_dir}/readiness/prometheus/manifest.json" >/dev/null
jq -e '.data.result as $series |
  any($series[]; .metric.__name__ == "capacity_otlp_probe_total" and
    .metric.job == "gitstore-otlp-metrics-capacity" and .metric.service_name == "gitstore-probe" and
    .values == [[1,"1"],[2,"2"]]) and
  all(["otelcol_exporter_sent_metric_points_total","otelcol_exporter_send_failed_metric_points_total",
    "otelcol_exporter_queue_size"][]; . as $name |
    any($series[]; .metric.__name__ == $name and .metric.job == "gitstore-observability-capacity" and
      .metric.exporter == "prometheus/metrics" and .values == [[1,"0"],[2,"1"]]))' \
  "${test_dir}/readiness/prometheus/observability-series.json" >/dev/null
jq -e '.batches | length == 1' \
  "${test_dir}/readiness/telemetry/b71bb94adb59498d8a3a6f1079a3fdaa.json" >/dev/null
jq -e '.batches | length == 1' \
  "${test_dir}/readiness/telemetry/c71bb94adb59498d8a3a6f1079a3fdaa.json" >/dev/null
jq -e '.exemplarQuery | contains("gitstore-(api|controller|git|otlp-metrics)-capacity")' \
  "${test_dir}/readiness/prometheus/manifest.json" >/dev/null
jq -e '.data[0].exemplars[0].labels.trace_id == "b71bb94adb59498d8a3a6f1079a3fdaa"' \
  "${test_dir}/readiness/prometheus/exemplars.json" >/dev/null
jq -e '.data.result[0].stream.trace_id == "b71bb94adb59498d8a3a6f1079a3fdaa"' \
  "${test_dir}/readiness/telemetry/correlated-logs.json" >/dev/null
for failure in missing-store missing-otlp down-otlp missing-git missing-rpc zero-rpc stale-rpc empty-traces truncated-traces invalid-trace trace-error empty-detail empty-logs truncated-logs empty-exemplars truncated-exemplars invalid-exemplar-trace zero-exemplar-trace invalid-exemplar-span invalid-exemplar-value negative-exemplar-value stale-exemplar future-exemplar exemplar-trace-error empty-exemplar-detail empty-correlated-logs unmatched-correlated-trace invalid-correlated-span zero-correlated-span truncated-correlated-logs; do
  if PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY=full \
    CAPACITY_PROMETHEUS_GIT_TARGETS=git.internal:50052 EXPORT_FAILURE="${failure}" \
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/readiness" \
    http://prometheus.invalid >/dev/null 2>&1; then
    echo "incomplete full observability export unexpectedly succeeded: ${failure}" >&2
    exit 1
  fi
done

echo "capacity Prometheus export tests passed"
