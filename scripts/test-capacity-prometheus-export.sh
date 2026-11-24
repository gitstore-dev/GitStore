#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${repo_root}/scripts/test-fixtures/shell.sh"
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

mkdir -p "${test_dir}/translation"
python3 - "${test_dir}/translation/metadata.json" <<'PY'
import datetime
import json
import pathlib
import sys

pathlib.Path(sys.argv[1]).write_text(json.dumps({
    "target": "api", "profile": "readiness", "mode": "diagnostic",
    "runId": "translation-prometheus", "sourceStateSha256": "a" * 64,
    "configDigest": "b" * 64, "environmentDigest": "c" * 64,
    "startedAt": (datetime.datetime.now(datetime.timezone.utc) -
                  datetime.timedelta(seconds=10)).strftime("%Y-%m-%dT%H:%M:%SZ"),
}))
PY
write_mock "${test_dir}/translation-bin/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
python3 - "$@" <<'PY'
import json
import os
import sys
import time

args = sys.argv[1:]
parameters = dict(arg.split("=", 1) for index, arg in enumerate(args)
                  if index > 0 and args[index - 1] == "--data-urlencode")
query = parameters["query"]
with open(os.environ["MOCK_PROMETHEUS_CALLS"], "a") as stream:
    stream.write(json.dumps(parameters) + "\n")
controller = 'job="gitstore-controller-capacity"' in query
targets = ["controller-a:4000", "controller-b:4000"] if controller else ["api-a:4000", "api-b:4000"]
names = ["up"]
if "query_range" in args[-1]:
    names += ["process_start_time_seconds", "process_cpu_seconds_total", "process_resident_memory_bytes", "go_goroutines",
              "go_gc_heap_live_bytes", "go_gc_cycles_total",
              "gitstore_controller_process_instance_info" if controller else "gitstore_api_process_instance_info",
              "gitstore_translation_reconciliation_seconds_bucket"]
now = int(os.environ.get("MOCK_PROMETHEUS_TIMESTAMP", time.time()))
series = []
for target in targets:
    if target == os.environ.get("MOCK_PROMETHEUS_MISSING_TARGET"):
        continue
    for name in names:
        if name == os.environ.get("MOCK_PROMETHEUS_MISSING_METRIC"):
            continue
        sample = {"metric": {"__name__": name, "instance": target}}
        value = os.environ.get("MOCK_PROMETHEUS_SAMPLE", "1")
        sample.update({"values": [[now - 5, value], [now, value]]} if "query_range" in args[-1]
                      else {"value": [now, value]})
        if query.startswith("timestamp("):
            sample["metric"] = {"instance": target, "source_metric": name}
            sample["values"] = [[now - 5, str(now - 5)], [now, str(now)]]
            if os.environ.get("MOCK_PROMETHEUS_MISSING_SCRAPE_TIME"):
                sample["metric"].pop("source_metric")
        series.append(sample)
response = {"status": "success", "data": {
    "resultType": "matrix" if "query_range" in args[-1] else "vector", "result": series,
}}
if os.environ.get("MOCK_PROMETHEUS_WARNING"):
    response["warnings"] = ["incomplete data"]
print(json.dumps(response))
PY
SH
translation_prometheus() {
  PATH="${test_dir}/translation-bin:${PATH}" \
    MOCK_PROMETHEUS_CALLS="${test_dir}/translation-calls.jsonl" \
    CAPACITY_TRANSLATION_READINESS=1 \
    CAPACITY_PROMETHEUS_TARGETS="${1-api-a:4000,api-b:4000}" \
    CAPACITY_PROMETHEUS_CONTROLLER_TARGETS="${2-controller-a:4000,controller-b:4000}" \
    "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/translation" http://prometheus
}
translation_prometheus
jq -e '.data.result | any(.metric.__name__ == "process_cpu_seconds_total")' \
  "${test_dir}/translation/prometheus/translation-api-series.json" >/dev/null
jq -e '.data.result | any(.metric.__name__ == "gitstore_translation_reconciliation_seconds_bucket")' \
  "${test_dir}/translation/prometheus/controller-series.json" >/dev/null
jq -e '.runId == "translation-prometheus" and .sourceStateSha256 == ("a" * 64) and
  .configDigest == ("b" * 64) and .environmentDigest == ("c" * 64) and
  .translationTargets.api == ["api-a:4000","api-b:4000"] and
  .translationTargets.controller == ["controller-a:4000","controller-b:4000"]' \
  "${test_dir}/translation/prometheus/manifest.json" >/dev/null
python3 - "${test_dir}/translation-calls.jsonl" <<'PY'
import hashlib
import json
import pathlib
import sys

calls = [json.loads(line) for line in pathlib.Path(sys.argv[1]).read_text().splitlines()]
assert len(calls) == 5
assert all("instance=~" in call["query"] for call in calls)
assert sum("start" in call and "end" in call and call["step"] == "5s" for call in calls) == 4
root = pathlib.Path(sys.argv[1]).parent / "translation/prometheus"
manifest = json.loads((root / "manifest.json").read_text())
for call in calls:
    if "start" in call:
        assert int(call["start"]) == manifest["translationRange"]["startUnixSeconds"]
        assert int(call["end"]) == manifest["translationRange"]["endUnixSeconds"]
for role, name in (("api", "translation-api-series.json"), ("controller", "controller-series.json")):
    assert manifest["translationResourceDigests"][role] == hashlib.sha256((root / name).read_bytes()).hexdigest()
    times = root / ("translation-" + role + "-scrape-times.json")
    assert manifest["translationScrapeTimeDigests"][role] == hashlib.sha256(times.read_bytes()).hexdigest()
    assert any(call["query"] == manifest["translationScrapeTimeQueries"][role] for call in calls)
    points = json.loads(times.read_text())["data"]["result"]
    assert all(sample["metric"]["source_metric"] and all(float(value) == at for at, value in sample["values"])
               for sample in points)
PY
for bad_targets in '' 'api-a:4000' 'api-a:4000,api-a:4000' 'api-a:4000,api-b:4000,' 'api-a:4000,api-b:4000,api-c:4000' 'api-a:4000"),up,api-b:4000'; do
  assert_rejected "Translation metrics accepted missing, duplicate or unsafe targets" "${test_dir}/output" \
    translation_prometheus "${bad_targets}"
done
assert_rejected "Translation metrics accepted missing controllers" "${test_dir}/output" \
  translation_prometheus 'api-a:4000,api-b:4000' ''
MOCK_PROMETHEUS_MISSING_TARGET=api-b:4000 \
  assert_rejected "Translation metrics accepted one API's resource history" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_MISSING_TARGET=controller-b:4000 \
  assert_rejected "Translation metrics accepted one controller's resource history" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_MISSING_METRIC=process_resident_memory_bytes \
  assert_rejected "Translation metrics accepted missing resource measurements" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_MISSING_METRIC=process_start_time_seconds \
  assert_rejected "Translation metrics accepted a missing native process epoch" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_SAMPLE=NaN \
  assert_rejected "Translation metrics accepted nonfinite samples" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_WARNING=1 \
  assert_rejected "Translation metrics ignored partial-query warnings" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_TIMESTAMP=-1 \
  assert_rejected "Translation metrics accepted invalid sample times" "${test_dir}/output" translation_prometheus
MOCK_PROMETHEUS_MISSING_SCRAPE_TIME=1 \
  assert_rejected "Translation metrics accepted unattributable scrape times" "${test_dir}/output" translation_prometheus

echo "capacity Prometheus export tests passed"
