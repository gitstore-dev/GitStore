#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors
set -euo pipefail
evidence="${1:?evidence directory required}"
mode="${MODE:-diagnostic}"
fixture_outcome=blocked
fail() {
  jq -n --arg reason "$1" '{schemaVersion:1,passed:false,boundary:"failed",publicationFixture:"blocked",publishedCatalog:"blocked",reason:$reason}' >"${evidence}/domain-verifier.json"
  echo "$1" >&2
  exit 1
}
jq -e '
  def v($n): .metrics[$n] | (.values // .);
  (v("endpoint_public_unavailable").count // 0)>0 and
  (v("endpoint_admin_completed").count // 0)>0 and
  ((v("endpoint_unexpected_errors") | .rate // .value) // 1)<0.001 and
  ((v("endpoint_admin_unexpected_errors") | .rate // .value) // 1)<0.001 and
  ((v("endpoint_public_unexpected_errors") | .rate // .value) // 1)<0.001 and
  (v("dropped_iterations").count // 0)==0 and
  ((v("checks") | .rate // .value) // 0)>0.999 and
  (v("endpoint_public_unsafe_responses").count // 1)==0 and
  (v("endpoint_admin_duration_ms")["p(95)"] // 1e9)<=2000
' "${evidence}/summary.json" >/dev/null || fail "boundary metrics failed; unavailable responses are never published reads"
if [[ -r "${evidence}/publication-fixture.json" ]]; then
  jq -e --slurpfile tested "${evidence}/metadata.json" '.component=="storefront-identity-fixture/v1" and .passed==true and .evidenceClass=="publication-fixture" and .publishedCatalog=="blocked" and .sourceStateSha256==$tested[0].sourceStateSha256' "${evidence}/publication-fixture.json" >/dev/null || fail 'publication fixture receipt is not attributable to the tested source'
  fixture_outcome=passed
fi
if [[ "${mode}" != diagnostic ]]; then
  jq -e '
    def v($n): .metrics[$n] | (.values // .);
    (v("endpoint_recovery_admin_duration_ms")["p(95)"] // 1e9)<=2000 and
    ((v("endpoint_recovery_admin_success") | .rate // .value) // 0)==1 and
    ((v("endpoint_recovery_public_unavailable") | .rate // .value) // 0)==1
  ' "${evidence}/summary.json" >/dev/null || fail "baseline did not recover within the thirty-second allowance"
  [[ "${fixture_outcome}" == passed ]] || fail 'publication identity fixture receipt is required'
  samples="${evidence}/prometheus/endpoint-series.json"
  [[ -r "${samples}" ]] || fail 'SDK/process resource samples are required'
  jq -e --slurpfile preflight "${evidence}/preflight-environment.json" --slurpfile before "${evidence}/preflight-environment-before.json" '
    .data.result as $rows |
    [$rows[] | select(.metric.__name__=="gitstore_api_process_instance_info" and any(.values[];.[1]=="1"))] as $identities |
    [$preflight[0].topology.liveReplicas[].instanceID,$before[0].topology.liveReplicas[].instanceID] as $expected |
    ($identities|length)>=2 and all($identities[]; .metric.instance_id as $id | ($expected|index($id))!=null) and
    ([ $rows[] | select(.metric.__name__=="gitstore_api_endpoint_limits") ] | length)>=6 and
    all($identities[]; .metric.instance as $instance |
      any($rows[];.metric.instance==$instance and .metric.__name__=="process_resident_memory_bytes" and (.values|length)>0) and
      any($rows[];.metric.instance==$instance and .metric.__name__=="process_cpu_seconds_total" and (.values|length)>0) and
      all(["admin","storefront"][]; . as $endpoint |
        any($rows[];.metric.instance==$instance and .metric.endpoint==$endpoint and .metric.__name__=="gitstore_api_endpoint_in_flight" and .metric.reason=="http" and (.values|length)>0) and
        any($rows[];.metric.instance==$instance and .metric.endpoint==$endpoint and .metric.__name__=="gitstore_api_endpoint_limiter_keys" and .metric.reason=="ip" and (.values|length)>0)) and
      any($rows[];.metric.instance==$instance and .metric.endpoint=="admin" and .metric.__name__=="gitstore_api_endpoint_limiter_keys" and .metric.reason=="principal" and (.values|length)>0) and
      any($rows[];.metric.instance==$instance and .metric.endpoint=="admin" and .metric.__name__=="gitstore_api_endpoint_connections" and (.values|length)>0)) and
    all($rows[] | select(.metric.__name__|test("^gitstore_api_endpoint_(in_flight|limiter_keys|connections)$"));
      . as $observed |
      (if .metric.__name__=="gitstore_api_endpoint_limiter_keys" then "limiter_keys" elif .metric.__name__=="gitstore_api_endpoint_connections" then "websocket" else .metric.reason end) as $reason |
      [$rows[] | select(.metric.__name__=="gitstore_api_endpoint_limits" and .metric.instance==$observed.metric.instance and .metric.endpoint==$observed.metric.endpoint and .metric.reason==$reason) | .values[][1] | tonumber] as $limits |
      ($limits|length)>0 and ($limits|min)>0 and ($limits|unique|length)==1 and
      all(.values[]; (.[1]|tonumber)>=0 and (.[1]|tonumber)<=($limits|min)))
  ' "${samples}" >/dev/null || fail 'missing process identity/resource evidence or endpoint resource ceiling exceeded'
  connected="${CAPACITY_CONNECTED_EVIDENCE:-}/connected-result.json"
  push="${CAPACITY_PUSH_EVIDENCE:-}"
  [[ -r "${connected}" && -r "${push}" ]] || fail "concurrent repository/watch and push receipts are required"
  jq -e --slurpfile tested "${evidence}/metadata.json" '
    .component=="endpoint-split-connected/v1" and .passed==true and
    .subscribers==20 and .durationSeconds>=2160 and .failed==0 and .dropped==0 and .admitted==.offered and .offered>=4320 and .adminAuthoringP95Ms<=2000 and .apiRecoveryMs<=30000 and
    .gitRevision==$tested[0].gitRevision and
    .sourceStateSha256==$tested[0].sourceStateSha256 and
    .configDigest==$tested[0].configDigest and
    .environmentDigest==$tested[0].environmentDigest and
    .startedAt<=$tested[0].loadStartedAt and .completedAt>=$tested[0].loadCompletedAt
  ' "${connected}" >/dev/null || fail "connected receipts do not identify a matching overlapping verified workload"
  jq -e --arg run "${CAPACITY_RUN_ID:-}" --slurpfile tested "${evidence}/metadata.json" '.runId==$run and .pushes>=2160 and .failures==0 and .dropped==0 and .intervalMs==1000 and .offered==.pushes and .oracleContract=="admin-and-durable-watch/v1" and .oracleVerified==.pushes and .oracleFailed==0 and .oracleDropped==0 and .startedAt<=$tested[0].loadStartedAt and .completedAt>=$tested[0].loadCompletedAt' "${push}" >/dev/null || fail "push acknowledgement/convergence receipt failed"
  cp "${connected}" "${evidence}/connected-result.json"
  cp "${push}" "${evidence}/push-load.json"
fi
jq -n --arg mode "${mode}" --arg fixture "${fixture_outcome}" '{schemaVersion:1,passed:true,evidenceClass:$mode,boundary:"passed",publicationFixture:$fixture,publishedCatalog:"blocked",publicReadSuccesses:0,note:"503 throughput proves only the unavailable boundary; publication/target support remains separate"}' >"${evidence}/domain-verifier.json"
