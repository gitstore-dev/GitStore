#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
summary="${evidence_dir}/summary.json"
output="${evidence_dir}/domain-verifier.json"

[[ -r "${summary}" ]] || { echo "namespace admission verifier requires summary.json" >&2; exit 2; }

jq -e '
  (.metrics.gitstore_namespace_admitted.values.count // 0) > 0 and
  (.metrics.checks.values.fails // 0) == 0 and
  (.metrics.gitstore_namespace_graphql_failed.values.rate // 1) < 0.001 and
  (.metrics.gitstore_namespace_visibility_ms.values["p(95)"] // 1e99) <= 1000 and
  (.metrics.gitstore_namespace_visibility_ms.values["p(99)"] // 1e99) <= 3000
' "${summary}" >/dev/null || {
  jq -n '{schemaVersion:1,passed:false,reason:"admission correctness or cross-replica visibility objective failed"}' >"${output}"
  echo "namespace admission domain verification failed" >&2
  exit 1
}

jq -n --slurpfile summary "${summary}" '
  {schemaVersion:1,passed:true,
   admitted:$summary[0].metrics.gitstore_namespace_admitted.values.count,
   visibilityP95Milliseconds:$summary[0].metrics.gitstore_namespace_visibility_ms.values["p(95)"],
   visibilityP99Milliseconds:$summary[0].metrics.gitstore_namespace_visibility_ms.values["p(99)"]}
' >"${output}"
