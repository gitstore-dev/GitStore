#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
summary="${evidence_dir}/summary.json"
output="${evidence_dir}/domain-verifier.json"

[[ -r "${summary}" ]] || { echo "namespace admission verifier requires summary.json" >&2; exit 2; }

jq -e '
  .metrics.gitstore_namespace_admitted as $admitted |
  .metrics.checks as $checks |
  .metrics.gitstore_namespace_graphql_failed as $failures |
  .metrics.gitstore_namespace_visibility_ms as $visibility |
  [$admitted.count, $checks.passes, $checks.fails, $failures.passes, $failures.fails, $visibility.count] |
  all(type == "number" and . >= 0 and . == floor) and
  $admitted.count > 0 and $checks.passes > 0 and $checks.fails == 0 and
  $visibility.count == $admitted.count and
  ($failures.passes + $failures.fails) > 0 and
  ($failures.value | type == "number" and . >= 0 and . < 0.001) and
  ($visibility["p(95)"] | type == "number" and . >= 0 and . <= 1000) and
  ($visibility["p(99)"] | type == "number" and . >= 0 and . <= 3000)
' "${summary}" >/dev/null || {
  jq -n '{schemaVersion:1,passed:false,reason:"admission correctness or cross-replica visibility objective failed"}' >"${output}"
  echo "namespace admission domain verification failed" >&2
  exit 1
}

jq -n --slurpfile summary "${summary}" '
  {schemaVersion:1,passed:true,
   admitted:$summary[0].metrics.gitstore_namespace_admitted.count,
   visibilitySamples:$summary[0].metrics.gitstore_namespace_visibility_ms.count,
   visibilityP95Milliseconds:$summary[0].metrics.gitstore_namespace_visibility_ms["p(95)"],
   visibilityP99Milliseconds:$summary[0].metrics.gitstore_namespace_visibility_ms["p(99)"]}
' >"${output}"
