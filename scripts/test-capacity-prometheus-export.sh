#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT
mkdir -p "${test_dir}/bin" "${test_dir}/evidence"
printf '#!/usr/bin/env bash\nprintf '\''{"status":"success","data":{"resultType":"vector","result":[]}}'\''\n' >"${test_dir}/bin/curl"
chmod +x "${test_dir}/bin/curl"

PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_PROMETHEUS_LOOKBACK=30m \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/evidence" http://prometheus.invalid >/dev/null

jq -e '.schemaVersion == 1 and .lookback == "30m" and (.queries | length) == 6 and all(.queries[]; .query | contains("[30m]"))' "${test_dir}/evidence/prometheus/manifest.json" >/dev/null
for name in namespace_admission_stage_p95 namespace_datastore_operation_p95 namespace_datastore_errors namespace_cdc_discovery_p95 namespace_materializer_stage_p95 namespace_delivery_p95; do
  jq -e '.status == "success"' "${test_dir}/evidence/prometheus/${name}.json" >/dev/null
done

if PATH="${test_dir}/bin:${PATH}" CAPACITY_PROMETHEUS_LOOKBACK=invalid \
  "${repo_root}/scripts/export-capacity-prometheus.sh" "${test_dir}/evidence" http://prometheus.invalid >/dev/null 2>&1; then
  echo "invalid Prometheus lookback unexpectedly succeeded" >&2
  exit 1
fi

echo "capacity Prometheus export tests passed"
