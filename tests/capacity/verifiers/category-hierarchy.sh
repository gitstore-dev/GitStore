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
[[ -r "${summary}" ]] || { echo "category hierarchy verifier requires summary.json" >&2; exit 2; }

fail() {
  jq -n --arg reason "$1" '{schemaVersion:1,passed:false,reason:$reason}' >"${output}"
  echo "category hierarchy domain verification failed: $1" >&2
  exit 1
}

metrics='def m($n): (.metrics[$n] // {}) | (.values // .);
  def c($n): m($n) | (.count // 0);'

jq -e "${metrics}"'
  c("category_mutation_acknowledged") > 0 and
  c("category_seed_created") >= 1 and
  c("category_mutation_failed") == 0 and
  c("category_sc007_own_commit_checks") == c("category_mutation_acknowledged") and
  c("category_sc007_violations") == 0 and
  c("category_sc007_pool_records") > 0 and
  c("category_sc007_duplicate_revisions") == 0
' "${summary}" >/dev/null || fail "SC-007: a mutation returned a record that is not its own commit, or revisions are not unique"

jq -e "${metrics}"'
  c("category_sc009_sets_checked") >= 14 and
  c("category_sc009_set_mismatches") == 0 and
  c("category_sc009_missing") == 0 and
  c("category_sc009_extra") == 0 and
  c("category_unreconciled_at_audit") == 0 and
  c("category_cascade_converged") >= 1
' "${summary}" >/dev/null || fail "SC-009: filtered results differ from the status.resolved.path walk, or the re-parent cascade did not converge"

push_json='null'
audit_findings='null'
if [[ "${mode}" != "diagnostic" ]]; then
  push_file="${CAPACITY_CATEGORY_PUSH_EVIDENCE:-}"
  [[ -r "${push_file}" ]] || fail "push driver evidence CAPACITY_CATEGORY_PUSH_EVIDENCE is missing"
  cp "${push_file}" "${evidence_dir}/category-push-load.json"
  # Push evidence: {repository, pushes, failures, firstPushMs, lastPushMs}.
  jq -e --slurpfile s "${summary}" '
    ($s[0] | (.metrics.category_load_start_ms // {}) | (.values // .) | (.value // 0)) as $start |
    ($s[0] | (.metrics.category_load_end_ms // {}) | (.values // .) | (.value // 0)) as $end |
    .repository == "gitstore-system" and .pushes >= 1 and .failures == 0 and
    $start > 0 and $end > $start and
    ((([.lastPushMs, $end] | min) - ([.firstPushMs, $start] | max)) >= (($end - $start) / 2))
  ' "${evidence_dir}/category-push-load.json" >/dev/null || fail "push driver evidence missing, failed, or did not overlap at least half the load window"
  push_json="$(jq -c . "${evidence_dir}/category-push-load.json")"

  audit_file="${CAPACITY_CATEGORY_AUDIT_FILE:-}"
  [[ -r "${audit_file}" ]] || fail "post-recovery gitctl scylla-projection-audit output (CAPACITY_CATEGORY_AUDIT_FILE) is missing"
  cp "${audit_file}" "${evidence_dir}/projection-audit.json"
  audit_findings="$(jq '.findings | length' "${evidence_dir}/projection-audit.json")"
  [[ "${audit_findings}" == "0" ]] || fail "scylla-projection-audit reported ${audit_findings} findings after recovery"
fi

jq -n --slurpfile s "${summary}" --argjson push "${push_json}" --argjson audit "${audit_findings}" '
  def v($n): ($s[0].metrics[$n] // {}) | (.values // .);
  {schemaVersion:1,passed:true,
   sc007:{acknowledgedMutations:v("category_mutation_acknowledged").count,poolRecords:v("category_sc007_pool_records").count,duplicateRevisions:(v("category_sc007_duplicate_revisions").count // 0),conflictRetries:(v("category_mutation_conflicts").count // 0)},
   sc009:{filteredSetsChecked:v("category_sc009_sets_checked").count,missing:(v("category_sc009_missing").count // 0),extra:(v("category_sc009_extra").count // 0)},
   cascade:{convergedMs:v("category_cascade_convergence_ms"),reportedNotGated:true},
   seededDescendants:v("category_seed_created").count,
   pushLoad:$push,projectionAuditFindings:$audit}' >"${output}"
