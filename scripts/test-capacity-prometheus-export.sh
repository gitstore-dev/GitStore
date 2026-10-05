#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
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

mkdir -p "${test_dir}/repository"
printf '{"target":"repository","scenario":"lifecycle","startedAt":"2026-09-04T00:00:00Z"}\n' >"${test_dir}/repository/metadata.json"
PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/repository" http://prometheus.invalid >/dev/null
jq -e '.capacityTarget == "repository/lifecycle" and (.queries | map(.name)) == ["api_targets_up","repository_datastore_operation_p95","repository_datastore_errors","namespace_cdc_discovery_p95","namespace_materializer_stage_p95","namespace_delivery_p95"]' \
  "${test_dir}/repository/prometheus/manifest.json" >/dev/null

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

echo "capacity Prometheus export tests passed"
