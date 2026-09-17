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
[[ -r "${summary}" ]] || { echo "Product lifecycle verifier requires summary.json" >&2; exit 2; }

jq -e '
  (.metrics.checks.fails // .metrics.checks.values.fails // 1) == 0 and
  (.metrics.http_req_failed.value // .metrics.http_req_failed.values.rate // 1) < 0.001 and
  (.metrics.product_lifecycle_replica_checks.count // .metrics.product_lifecycle_replica_checks.values.count // 0) >= 2 and
  (.metrics.product_lifecycle_admission_checks.count // .metrics.product_lifecycle_admission_checks.values.count // 0) >= 1 and
  (.metrics.product_lifecycle_deletion_race_checks.count // .metrics.product_lifecycle_deletion_race_checks.values.count // 0) >= 1
' "${summary}" >/dev/null || {
  jq -n '{schemaVersion:1,passed:false,reason:"Product lifecycle replica, admission, or deletion-race correctness failed"}' >"${output}"
  echo "Product lifecycle domain verification failed" >&2
  exit 1
}

jq -n --slurpfile summary "${summary}" '{schemaVersion:1,passed:true,replicaChecks:($summary[0].metrics.product_lifecycle_replica_checks.count // $summary[0].metrics.product_lifecycle_replica_checks.values.count),admissionChecks:($summary[0].metrics.product_lifecycle_admission_checks.count // $summary[0].metrics.product_lifecycle_admission_checks.values.count),deletionRaceChecks:($summary[0].metrics.product_lifecycle_deletion_race_checks.count // $summary[0].metrics.product_lifecycle_deletion_race_checks.values.count)}' >"${output}"
