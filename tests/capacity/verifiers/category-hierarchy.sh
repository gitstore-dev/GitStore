#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Category hierarchy domain verifier (spec 057).
#   SC-009: every filtered `categories` result set, on both API replicas, equals
#           a walk over all categories' status.resolved.path after the cascade.
#   SC-007: every acknowledged mutation returned a record with its own content
#           and a commit-id metadata.revision, and the revisions of all
#           mutation-owned records are pairwise distinct.
# Alpha/production additionally require the companion push driver's evidence to
# overlap the load window and, after recovery, a zero-finding projection audit.
set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
mode="${MODE:-diagnostic}"
summary="${evidence_dir}/summary.json"
output="${evidence_dir}/domain-verifier.json"
fail() {
  jq -n --arg reason "$1" '{schemaVersion:1,passed:false,reason:$reason}' >"${output}"
  echo "category hierarchy domain verification failed: $1" >&2
  exit 1
}

[[ -f "${summary}" && ! -L "${summary}" && "$(wc -c <"${summary}")" -le 1048576 ]] ||
  fail "summary.json must be a regular bounded native k6 summary"
jq -se 'length == 1 and (.[0] | type == "object")' "${summary}" >/dev/null 2>&1 ||
  fail "summary.json must contain exactly one document"
case "${mode}" in diagnostic|alpha|production) ;; *) fail "unknown capacity mode" ;; esac

metrics='def integer: type == "number" and . >= 0 and . <= 9007199254740991 and floor == .;
  def m($n): .metrics[$n];
  def c($n): m($n).count;
  def counters($names): . as $s | all($names[]; $s.metrics[.].count | integer);
  def trend($n; $p95; $p99): m($n) |
    (.count | integer) and .count > 0 and
    (.["p(95)"] | type == "number" and . >= 0 and . <= $p95) and
    (.["p(99)"] | type == "number" and . >= 0 and . <= $p99) and
    .["p(95)"] <= .["p(99)"];'

jq -e "${metrics}"'
  counters(["category_seed_created","category_mutation_offered","category_mutation_missed_schedules",
    "category_mutation_endpoint_callbacks","category_filtered_read_endpoint_callbacks",
    "category_filtered_read_offered","category_filtered_read_completed","category_filtered_read_failed",
    "category_filtered_read_missed_schedules","category_mutation_acknowledged",
    "category_mutation_failed","category_mutation_conflicts","category_sc007_own_commit_checks",
    "category_sc007_violations","category_sc007_pool_records","category_sc007_duplicate_revisions",
    "category_sc009_sets_checked","category_sc009_set_mismatches","category_sc009_missing",
    "category_sc009_extra","category_unreconciled_at_audit","category_cascade_converged",
    "dropped_iterations"]) and
  (.metrics.checks.passes | integer) and .metrics.checks.passes > 0 and
  .metrics.checks.fails == 0 and
  (.metrics.http_req_failed.passes | integer) and (.metrics.http_req_failed.fails | integer) and
  (.metrics.http_req_failed.passes + .metrics.http_req_failed.fails) > 0 and
  (.metrics.http_req_failed.value | type == "number" and . >= 0 and . < 0.001) and
  .metrics.http_req_failed.value ==
    (.metrics.http_req_failed.passes / (.metrics.http_req_failed.passes + .metrics.http_req_failed.fails)) and
  c("dropped_iterations") == 0 and
  trend("http_req_duration{operation:createCategory}"; 750; 2000) and
  trend("http_req_duration{operation:updateCategory}"; 750; 2000) and
  trend("http_req_duration{operation:filteredCategories}"; 150; 500) and
  (.metrics.category_cascade_convergence_ms.count | integer) and
  .metrics.category_cascade_convergence_ms.count > 0 and
  (.metrics.category_load_start_ms.value | integer) and .metrics.category_load_start_ms.value > 0 and
  (.metrics.category_load_end_ms.value | integer) and
  .metrics.category_load_end_ms.value > .metrics.category_load_start_ms.value
' "${summary}" >/dev/null 2>&1 || fail "missing, unsampled, malformed, dropped or over-threshold native k6 observations"

jq -e "${metrics}"'
  ([.metrics.category_declared_mutations.value,.metrics.category_declared_reads.value,
    .metrics.category_mutation_rate.value,.metrics.category_read_rate.value,
    .metrics.category_duration_seconds.value] | all(integer)) and
  .metrics.category_mutation_rate.value > 0 and .metrics.category_read_rate.value > 0 and
  .metrics.category_duration_seconds.value >= 60 and
  .metrics.category_declared_mutations.value ==
    (.metrics.category_mutation_rate.value * .metrics.category_duration_seconds.value) and
  .metrics.category_declared_reads.value ==
    (.metrics.category_read_rate.value * .metrics.category_duration_seconds.value) and
  c("category_mutation_offered") == .metrics.category_declared_mutations.value and
  c("category_mutation_offered") ==
    (c("category_mutation_acknowledged") + c("category_mutation_failed")) and
  c("category_filtered_read_offered") == .metrics.category_declared_reads.value and
  c("category_filtered_read_offered") ==
    (c("category_filtered_read_completed") + c("category_filtered_read_failed")) and
  c("category_filtered_read_failed") == 0 and
  .metrics["http_req_duration{operation:filteredCategories}"].count == c("category_filtered_read_completed") and
  c("category_mutation_missed_schedules") == 0 and c("category_filtered_read_missed_schedules") == 0 and
  c("category_mutation_endpoint_callbacks") <= 1 and c("category_filtered_read_endpoint_callbacks") <= 1 and
  .metrics.category_load_end_ms.value - .metrics.category_load_start_ms.value ==
    (.metrics.category_duration_seconds.value * 1000)
' "${summary}" >/dev/null 2>&1 || fail "declared mutation/read schedule is incomplete, late or unaccounted"

jq -e "${metrics}"'
  counters(["category_product_seeded","category_product_offered","category_product_completed",
    "category_product_failed","category_product_missed_schedules","category_product_endpoint_callbacks",
    "category_product_reassigned","category_product_oracle_checks","category_product_pages",
    "category_product_pages_forward","category_product_pages_backward"]) and
  ([m("category_product_rate").value,m("category_product_declared_updates").value,
    m("category_product_fixture_count").value,m("category_product_expected_max_shard_members").max] | all(integer)) and
  m("category_product_rate").value > 0 and m("category_product_fixture_count").value >= 3 and
  c("category_product_seeded") == m("category_product_fixture_count").value and
  m("category_product_declared_updates").value ==
    (m("category_product_rate").value * m("category_duration_seconds").value) and
  c("category_product_offered") == m("category_product_declared_updates").value and
  c("category_product_offered") == (c("category_product_completed") + c("category_product_failed")) and
  c("category_product_failed") == 0 and c("category_product_missed_schedules") == 0 and
  c("category_product_endpoint_callbacks") == 0 and
  c("category_product_reassigned") == (c("category_product_completed") / 2 | floor) and
  c("category_product_oracle_checks") == (16 * (c("category_product_completed") + 2)) and
  c("category_product_pages_forward") >= (8 * (c("category_product_completed") + 2)) and
  c("category_product_pages_backward") >= (8 * (c("category_product_completed") + 2)) and
  c("category_product_pages") == (c("category_product_pages_forward") + c("category_product_pages_backward")) and
  m("category_product_expected_max_shard_members").max > 0 and
  m("category_product_expected_max_shard_members").max <= m("category_product_fixture_count").value and
  (m("category_product_page_ms").count | integer) and
  m("category_product_page_ms").count == c("category_product_pages") and
  (m("category_product_convergence_ms").count | integer) and
  m("category_product_convergence_ms").count == c("category_product_completed")
' "${summary}" >/dev/null 2>&1 || fail "Product write/reassignment schedule or independent replica membership oracle is incomplete"

jq -e '
  . as $summary |
  ["category_product_page_ms","category_product_convergence_ms"] |
  all(. as $name | $summary.metrics[$name] |
    (.["p(95)"] | type == "number" and isfinite and . >= 0) and
    (.["p(99)"] | type == "number" and isfinite and . >= 0) and
    .["p(95)"] <= .["p(99)"])
' "${summary}" >/dev/null 2>&1 || fail "Product membership latency observations are missing or malformed"

jq -e "${metrics}"'
  c("category_mutation_acknowledged") > 0 and
  c("category_seed_created") >= 1 and
  c("category_mutation_failed") == 0 and
  c("category_sc007_own_commit_checks") == c("category_mutation_acknowledged") and
  c("category_sc007_violations") == 0 and
  c("category_sc007_pool_records") == c("category_mutation_acknowledged") and
  c("category_sc007_duplicate_revisions") == 0
' "${summary}" >/dev/null 2>&1 || fail "SC-007: a mutation returned a record that is not its own commit, or revisions are not unique"

jq -e "${metrics}"'
  c("category_sc009_sets_checked") >= 14 and
  c("category_sc009_set_mismatches") == 0 and
  c("category_sc009_missing") == 0 and
  c("category_sc009_extra") == 0 and
  c("category_unreconciled_at_audit") == 0 and
  c("category_cascade_converged") >= 1
' "${summary}" >/dev/null 2>&1 || fail "SC-009: filtered results differ from the status.resolved.path walk, or the re-parent cascade did not converge"

push_json='null'
audit_findings='null'
if [[ "${mode}" != "diagnostic" ]]; then
  jq -e '.metrics.category_mutation_rate.value >= 20 and
    .metrics.category_duration_seconds.value >= 300' "${summary}" >/dev/null 2>&1 ||
    fail "acceptance requires the existing minimum 20 mutations/s for 300 seconds"
  jq -e '.metrics.category_seed_created.count >= 10000' "${summary}" >/dev/null 2>&1 ||
    fail "acceptance requires at least 10000 acknowledged descendants"
  push_file="${CAPACITY_CATEGORY_PUSH_EVIDENCE:-}"
  [[ -f "${push_file}" && ! -L "${push_file}" && "$(wc -c <"${push_file}")" -le 1048576 ]] ||
    fail "push driver evidence must be a regular bounded document"
  jq -se 'length == 1 and (.[0] | type == "object")' "${push_file}" >/dev/null 2>&1 ||
    fail "push driver evidence must contain exactly one document"
  jq -e --slurpfile s "${summary}" --arg run "${CAPACITY_RUN_ID:-}" \
    --arg namespace "${CAPACITY_CATEGORY_NAMESPACE:-default}" '
    def integer: type == "number" and . >= 0 and . <= 9007199254740991 and floor == .;
    $s[0].metrics.category_load_start_ms.value as $start |
    $s[0].metrics.category_load_end_ms.value as $end |
    (keys - ["repository","namespace","kind","runId","workers","intervalMs","offered",
      "pushes","failures","dropped","retries","firstPushMs","lastPushMs","latencyMs",
      "intervalNs","durationNs","scheduled","missedSchedules","offeredDurationNs",
      "drainDurationNs","drainTimeoutNs","drainTimedOut","cancelled"] | length) == 0 and
    .repository == "gitstore-system" and .namespace == $namespace and .kind == "category" and
    $run != "" and .runId == $run and
    ([.workers,.intervalMs,.offered,.pushes,.failures,.dropped,.retries,.firstPushMs,.lastPushMs,
      .intervalNs,.durationNs,.scheduled,.missedSchedules,.offeredDurationNs,.drainDurationNs,.drainTimeoutNs] |
      all(integer)) and .workers >= 1 and .workers <= 32 and .intervalMs >= 50 and
    .intervalNs >= 50000000 and .intervalMs == (.intervalNs / 1000000 | floor) and
    .durationNs > 0 and .durationNs <= 7200000000000 and
    .scheduled == (((.durationNs - 1) / .intervalNs | floor) + 1) and
    .offered == .scheduled and .missedSchedules == 0 and
    .offeredDurationNs >= .durationNs and .offeredDurationNs < (.durationNs + .intervalNs) and
    .drainTimeoutNs == 30000000000 and .drainDurationNs <= .drainTimeoutNs and
    .drainTimedOut == false and .cancelled == false and
    .pushes > 0 and .failures == 0 and .dropped == 0 and
    .offered == (.pushes + .failures + .dropped) and .retries <= (.pushes + .failures) and
    (.latencyMs | keys == ["max","p50","p95","p99"] and all(.[]; integer) and
      .p50 <= .p95 and .p95 <= .p99 and .p99 <= .max) and
    .firstPushMs > 0 and .lastPushMs >= .firstPushMs and
    ((([.lastPushMs, $end] | min) - ([.firstPushMs, $start] | max)) >= (($end - $start) / 2))
  ' "${push_file}" >/dev/null 2>&1 ||
    fail "push driver identity, complete schedule, cancellation, drain or half-window overlap failed"
  cp "${push_file}" "${evidence_dir}/category-push-load.json"
  push_json="$(jq -c . "${evidence_dir}/category-push-load.json")"

  audit_file="${CAPACITY_CATEGORY_AUDIT_FILE:-}"
  [[ -f "${audit_file}" && ! -L "${audit_file}" && "$(wc -c <"${audit_file}")" -le 1048576 ]] ||
    fail "post-recovery audit must be a regular bounded document"
  jq -se 'length == 1 and (.[0] | type == "object")' "${audit_file}" >/dev/null 2>&1 ||
    fail "post-recovery audit must contain exactly one document"
  jq -e '
    def empty_plan_field($n): has($n) and
      (.[$n] | (type == "array" or type == "null") and length == 0);
    (keys - ["findings","actions","namespaceRepositoryFences","serviceAccountEnrollments"] | length) == 0 and
    empty_plan_field("findings") and empty_plan_field("actions") and
    (if has("namespaceRepositoryFences") then empty_plan_field("namespaceRepositoryFences") else true end) and
    (if has("serviceAccountEnrollments") then empty_plan_field("serviceAccountEnrollments") else true end)
  ' "${audit_file}" >/dev/null 2>&1 || fail "projection audit is incomplete or retains findings, actions or repairs"
  cp "${audit_file}" "${evidence_dir}/projection-audit.json"
  audit_findings="$(jq '.findings | length' "${evidence_dir}/projection-audit.json")"
  [[ "${audit_findings}" == "0" ]] || fail "scylla-projection-audit reported ${audit_findings} findings after recovery"
fi

jq -n --slurpfile s "${summary}" --argjson push "${push_json}" --argjson audit "${audit_findings}" '
  def v($n): $s[0].metrics[$n];
  {schemaVersion:1,passed:true,
   schedule:{mutationRate:v("category_mutation_rate").value,readRate:v("category_read_rate").value,
     durationSeconds:v("category_duration_seconds").value,
     declaredMutations:v("category_declared_mutations").value,offeredMutations:v("category_mutation_offered").count,
     declaredReads:v("category_declared_reads").value,completedReads:v("category_filtered_read_completed").count,
     missedMutations:v("category_mutation_missed_schedules").count,missedReads:v("category_filtered_read_missed_schedules").count,
     mutationEndpointCallbacks:v("category_mutation_endpoint_callbacks").count,
     readEndpointCallbacks:v("category_filtered_read_endpoint_callbacks").count},
   sc007:{acknowledgedMutations:v("category_mutation_acknowledged").count,poolRecords:v("category_sc007_pool_records").count,duplicateRevisions:(v("category_sc007_duplicate_revisions").count // 0),conflictRetries:(v("category_mutation_conflicts").count // 0)},
   sc009:{filteredSetsChecked:v("category_sc009_sets_checked").count,missing:(v("category_sc009_missing").count // 0),extra:(v("category_sc009_extra").count // 0)},
   cascade:{convergedMs:v("category_cascade_convergence_ms"),reportedNotGated:true},
   seededDescendants:v("category_seed_created").count,
   productMembership:{fixtures:v("category_product_fixture_count").value,
     rate:v("category_product_rate").value,declared:v("category_product_declared_updates").value,
     offered:v("category_product_offered").count,completed:v("category_product_completed").count,
     reassigned:v("category_product_reassigned").count,oracleChecks:v("category_product_oracle_checks").count,
     forwardPages:v("category_product_pages_forward").count,backwardPages:v("category_product_pages_backward").count,
     expectedMaxShardMembers:v("category_product_expected_max_shard_members").max,
     expectedShardRowsNotStorageMeasurement:true,pageLatency:v("category_product_page_ms"),
     convergence:v("category_product_convergence_ms"),latencyReportedNotGated:true},
   pushLoad:$push,projectionAuditFindings:$audit}' >"${output}"
