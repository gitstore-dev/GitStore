#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
summary="${evidence_dir}/summary.json"
output="${evidence_dir}/domain-verifier.json"

if [[ ! -f "${summary}" || -L "${summary}" || ! -r "${summary}" ]] ||
   [[ "$(wc -c <"${summary}")" -gt 1048576 ]]; then
  jq -n '{schemaVersion:1,passed:false,reason:"summary must be a readable bounded regular file"}' >"${output}"
  echo "namespace admission verifier requires a bounded regular summary.json" >&2
  exit 2
fi

jq -e -s '
  def integer: type == "number" and . >= 0 and . <= 9007199254740991 and . == floor;
  def count($name): .metrics[$name].count;
  def key($base; $name): $base + "{schedule:" + $name + "}";
  def schedule_accounted:
    . as $summary |
    .metrics.gitstore_namespace_burst_rounds.value as $rounds |
    if ($rounds | integer) and $rounds + 1 <= (.metrics | length) then
      (["sustained"] + [range(1; $rounds + 1) | "burst" + tostring]) as $names |
      ["gitstore_namespace_scheduled", "gitstore_namespace_offered",
       "gitstore_namespace_missed_schedules", "gitstore_namespace_endpoint_callbacks"] as $bases |
      ($bases | all(. as $base |
        ($summary.metrics | keys | map(select(startswith($base + "{schedule:")))) ==
        ([$names[] | key($base; .)] | sort))) and
      ($names | all(. as $name | $summary |
        count(key("gitstore_namespace_scheduled"; $name)) as $scheduled |
        count(key("gitstore_namespace_offered"; $name)) as $offered |
        count(key("gitstore_namespace_missed_schedules"; $name)) as $missed |
        count(key("gitstore_namespace_endpoint_callbacks"; $name)) as $endpoint |
        ([$scheduled, $offered, $missed, $endpoint] | all(integer)) and
        $scheduled > 0 and $offered == $scheduled and $missed == 0 and $endpoint <= 1)) and
      ($bases | all(. as $base |
        $summary | count($base) == ([$names[] as $name | $summary | count(key($base; $name))] | add)))
    else false end;
  if length == 1 and (.[0] | type == "object") then .[0] else error("expected one summary object") end |
  .metrics.gitstore_namespace_offered as $offered |
  .metrics.gitstore_namespace_admitted as $admitted |
  .metrics.checks as $checks |
  .metrics.gitstore_namespace_graphql_failed as $failures |
  .metrics.gitstore_namespace_visibility_ms as $visibility |
  .metrics.http_req_failed as $http |
  .metrics.dropped_iterations as $dropped |
  .metrics.gitstore_namespace_scheduled as $scheduled |
  .metrics.gitstore_namespace_missed_schedules as $missed |
  .metrics.gitstore_namespace_endpoint_callbacks as $endpoint |
  . as $summary |
  [$offered.count, $admitted.count, $checks.passes, $checks.fails, $failures.passes, $failures.fails,
   $visibility.count, $http.passes, $http.fails, $dropped.count, $scheduled.count, $missed.count, $endpoint.count] |
  all(integer) and ($summary | schedule_accounted) and
  $offered.count == $scheduled.count and $missed.count == 0 and
  $offered.count == ($failures.passes + $failures.fails) and
  $admitted.count == $failures.fails and $dropped.count == 0 and
  $admitted.count > 0 and $checks.passes > 0 and $checks.fails == 0 and
  $checks.passes == (2 * $offered.count + $admitted.count) and
  $visibility.count == $admitted.count and
  ($failures.passes + $failures.fails) > 0 and
  ($failures.value | type == "number" and . >= 0 and . < 0.001) and
  $failures.value == ($failures.passes / ($failures.passes + $failures.fails)) and
  ($http.passes + $http.fails) > 0 and
  ($http.value | type == "number" and . >= 0 and . < 0.001) and
  $http.value == ($http.passes / ($http.passes + $http.fails)) and
  ($visibility["p(95)"] | type == "number" and . >= 0 and . <= 1000) and
  ($visibility["p(99)"] | type == "number" and . >= 0 and . <= 3000) and
  $visibility["p(95)"] <= $visibility["p(99)"]
' "${summary}" >/dev/null || {
  jq -n '{schemaVersion:1,passed:false,reason:"admission correctness or cross-replica visibility objective failed"}' >"${output}"
  echo "namespace admission domain verification failed" >&2
  exit 1
}

jq -n --slurpfile summary "${summary}" '
  {schemaVersion:1,passed:true,
   scheduled:$summary[0].metrics.gitstore_namespace_scheduled.count,
   offered:$summary[0].metrics.gitstore_namespace_offered.count,
   missedSchedules:$summary[0].metrics.gitstore_namespace_missed_schedules.count,
   endpointCallbacks:$summary[0].metrics.gitstore_namespace_endpoint_callbacks.count,
   burstRounds:$summary[0].metrics.gitstore_namespace_burst_rounds.value,
   admitted:$summary[0].metrics.gitstore_namespace_admitted.count,
   failedOutcomes:$summary[0].metrics.gitstore_namespace_graphql_failed.passes,
   droppedIterations:$summary[0].metrics.dropped_iterations.count,
   httpFailedRequests:$summary[0].metrics.http_req_failed.passes,
   httpFailureRate:$summary[0].metrics.http_req_failed.value,
   visibilitySamples:$summary[0].metrics.gitstore_namespace_visibility_ms.count,
   visibilityP95Milliseconds:$summary[0].metrics.gitstore_namespace_visibility_ms["p(95)"],
   visibilityP99Milliseconds:$summary[0].metrics.gitstore_namespace_visibility_ms["p(99)"]}
' >"${output}"
