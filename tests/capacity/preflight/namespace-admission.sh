#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib/preflight.sh"

evidence_dir="$1"
mode="$(capacity_preflight_mode)"
if [[ "${CAPACITY_SCENARIO:-}" == "validation" ]]; then
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
  corpus="${repo_root}/gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json"
  harness="${repo_root}/gitstore-api/internal/cataloggrpc/namespace_capacity_test.go"
  jq -e '.valid | type == "object" and length > 0' "${corpus}" >/dev/null
  corpus_digest="$(shasum -a 256 "${corpus}" | awk '{print $1}')"
  harness_digest="$(shasum -a 256 "${harness}" | awk '{print $1}')"
  [[ "${corpus_digest}" =~ ^[a-f0-9]{64}$ && "${harness_digest}" =~ ^[a-f0-9]{64}$ ]] || {
    echo "validation corpus/harness source digests are required" >&2
    exit 2
  }
  jq -n \
    --arg mode "${mode}" \
    --arg corpus_digest "${corpus_digest}" \
    --arg harness_digest "${harness_digest}" \
    --argjson valid_cases "$(jq '.valid | keys' "${corpus}")" \
    --argjson target_kinds "$(jq '.targets | keys' "${corpus}")" \
    '{schemaVersion:1,mode:$mode,scope:"catalog-service-test-helpers",connectedIngestion:false,
      corpusSha256:$corpus_digest,harnessSha256:$harness_digest,
      translationValidCases:$valid_cases,targetKinds:$target_kinds,
      workload:{filesPerRequest:500,namespaceManifestsPerRequest:50,helperProcesses:2,
        requestsPerSecond:10,workers:20,minimumDurationSeconds:1800,
        translationMaximumSourceBytes:1048576,
        classes:["normal","namespace-negative","translation-locale-negative","translation-field-negative","translation-near-limit"]}}' \
    >"${evidence_dir}/validation-corpus.json"
  exit 0
fi

api_replicas="${CAPACITY_API_REPLICAS:-0}"
api_build="${CAPACITY_API_BUILD:-unknown}"
scylla_nodes="${CAPACITY_SCYLLA_NODES:-0}"
scylla_smp="${CAPACITY_SCYLLA_SMP:-0}"
git_build="${CAPACITY_GIT_SERVICE_BUILD:-unknown}"
runtime_memory="${CAPACITY_RUNTIME_MEMORY_BYTES:-0}"
scylla_memory="${CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE:-0}"
scylla_auth="${CAPACITY_SCYLLA_AUTH_MODE:-unknown}"

if [[ "${mode}" != "diagnostic" ]]; then
  [[ -n "${CAPACITY_CONFIG_MANIFEST:-}" ]] || { echo "${mode} capacity evidence requires CAPACITY_CONFIG_MANIFEST" >&2; exit 2; }
  [[ -n "${CAPACITY_ENVIRONMENT_MANIFEST:-}" ]] || { echo "${mode} capacity evidence requires CAPACITY_ENVIRONMENT_MANIFEST" >&2; exit 2; }
  (( api_replicas >= 2 )) || { echo "${mode} capacity evidence requires CAPACITY_API_REPLICAS>=2" >&2; exit 2; }
  [[ "${api_build}" == "release" ]] || { echo "${mode} capacity evidence requires CAPACITY_API_BUILD=release" >&2; exit 2; }
  (( scylla_nodes >= 3 )) || { echo "${mode} capacity evidence requires CAPACITY_SCYLLA_NODES>=3" >&2; exit 2; }
  (( scylla_smp >= 2 )) || { echo "${mode} capacity evidence requires CAPACITY_SCYLLA_SMP>=2" >&2; exit 2; }
  [[ "${git_build}" == "release" ]] || { echo "${mode} capacity evidence requires CAPACITY_GIT_SERVICE_BUILD=release" >&2; exit 2; }
  (( runtime_memory >= 17179869184 )) || { echo "${mode} capacity evidence requires CAPACITY_RUNTIME_MEMORY_BYTES>=17179869184" >&2; exit 2; }
  (( scylla_memory > 0 )) || { echo "${mode} capacity evidence requires explicit CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE" >&2; exit 2; }
  [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]] || { echo "${mode} capacity evidence requires CAPACITY_DATASTORE_CONTAINERS" >&2; exit 2; }
  [[ "${scylla_auth}" != "unknown" ]] || { echo "${mode} capacity evidence requires CAPACITY_SCYLLA_AUTH_MODE" >&2; exit 2; }
fi

preflight_file="${evidence_dir}/preflight-environment.json"
jq -n \
  --arg mode "${mode}" \
  --argjson api_replicas "${api_replicas}" \
  --arg api_build "${api_build}" \
  --argjson scylla_nodes "${scylla_nodes}" \
  --argjson scylla_smp "${scylla_smp}" \
  --arg git_service_build "${git_build}" \
  --argjson runtime_memory_bytes "${runtime_memory}" \
  --argjson scylla_memory_bytes_per_node "${scylla_memory}" \
  --arg scylla_auth_mode "${scylla_auth}" \
  '{schemaVersion:1, mode:$mode, topology:{apiReplicas:$api_replicas, scyllaNodes:$scylla_nodes, scyllaSmpPerNode:$scylla_smp}, resources:{runtimeMemoryBytes:$runtime_memory_bytes, scyllaMemoryBytesPerNode:$scylla_memory_bytes_per_node}, datastore:{authenticationMode:$scylla_auth_mode}, builds:{api:$api_build,gitService:$git_service_build}}' \
  >"${preflight_file}"

if [[ "${mode}" == "diagnostic" ]]; then
  echo "diagnostic mode: topology/build and latency floors are recorded but cannot produce passing gate evidence"
fi
