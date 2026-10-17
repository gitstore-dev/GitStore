#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Product lifecycle load is valid only when both API replicas answered and the
# k6 scenario recorded no GraphQL/check failures. Product-specific end-to-end
# The Git-push and durable-watch probes run alongside k6 in the capacity stack
# and emit their own evidence. This verifier owns the concurrent GraphQL
# mutation signals and fails closed if any lifecycle phase is absent.
set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
summary="${evidence_dir}/summary.json"
output="${evidence_dir}/domain-verifier.json"
[[ -f "${summary}" && ! -L "${summary}" && "$(wc -c <"${summary}")" -le 1048576 ]] &&
jq -se 'length == 1 and (.[0] | type == "object")' "${summary}" >/dev/null 2>&1 && jq -e '
  def integer: type == "number" and . >= 0 and . <= 9007199254740991 and floor == .;
  [.metrics.checks.passes,.metrics.checks.fails,.metrics.http_req_failed.passes,
    .metrics.http_req_failed.fails,.metrics.product_lifecycle_offered.count,.metrics.dropped_iterations.count,
    .metrics.product_lifecycle_replica_checks.count,
    .metrics.product_lifecycle_admission_checks.count,.metrics.product_lifecycle_deletion_race_checks.count,
    .metrics.product_lifecycle_deletion_replica_checks.count] |
  all(integer)
' "${summary}" >/dev/null 2>&1 && jq -e '
  .metrics.checks.passes > 0 and .metrics.checks.fails == 0 and
  .metrics.checks.passes == (.metrics.product_lifecycle_admission_checks.count * 6) and
  .metrics.product_lifecycle_offered.count == .metrics.product_lifecycle_admission_checks.count and
  .metrics.dropped_iterations.count == 0 and
  (.metrics.http_req_failed.passes + .metrics.http_req_failed.fails) > 0 and
  (.metrics.http_req_failed.value | type == "number" and . >= 0 and . < 0.001) and
  .metrics.http_req_failed.value ==
    (.metrics.http_req_failed.passes / (.metrics.http_req_failed.passes + .metrics.http_req_failed.fails)) and
  .metrics.product_lifecycle_admission_checks.count > 0 and
  .metrics.product_lifecycle_replica_checks.count == (.metrics.product_lifecycle_admission_checks.count * 2) and
  .metrics.product_lifecycle_deletion_race_checks.count == .metrics.product_lifecycle_admission_checks.count and
  .metrics.product_lifecycle_deletion_replica_checks.count == (.metrics.product_lifecycle_admission_checks.count * 2)
' "${summary}" >/dev/null 2>&1 || {
  jq -n '{schemaVersion:1,passed:false,reason:"Product lifecycle replica, admission, or deletion-race correctness failed"}' >"${output}"
  echo "Product lifecycle domain verification failed" >&2
  exit 1
}

jq -n --slurpfile summary "${summary}" '{schemaVersion:1,passed:true,
  offered:$summary[0].metrics.product_lifecycle_offered.count,
  droppedIterations:$summary[0].metrics.dropped_iterations.count,
  replicaChecks:$summary[0].metrics.product_lifecycle_replica_checks.count,
  admissionChecks:$summary[0].metrics.product_lifecycle_admission_checks.count,
  deletionRaceChecks:$summary[0].metrics.product_lifecycle_deletion_race_checks.count,
  deletionReplicaChecks:$summary[0].metrics.product_lifecycle_deletion_replica_checks.count}' >"${output}"
