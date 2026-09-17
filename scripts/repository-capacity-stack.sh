#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
action="${1:-}"
capacity_target="${CAPACITY_STACK_TARGET:-repository}"
capacity_profile="${CAPACITY_STACK_PROFILE:-lifecycle}"
controller_kind="${CAPACITY_STACK_CONTROLLER_KIND:-${capacity_target^}}"
state_dir="${REPOSITORY_CAPACITY_STATE_DIR:-${repo_root}/.gitstore/repository-capacity}"
token_file="${REPOSITORY_TOKEN_FILE:-${state_dir}/token}"
trigger_file="${REPOSITORY_REPLACEMENT_TRIGGER_FILE:-${state_dir}/replace-api-b}"
revision="${CAPACITY_GIT_REVISION:-$(git -C "${repo_root}" rev-parse HEAD)}"
project="${REPOSITORY_CAPACITY_PROJECT:-gitstore-repository-capacity}"
replacement_watcher="${REPOSITORY_CAPACITY_REPLACEMENT_WATCHER:-${repo_root}/scripts/watch-repository-capacity-replacement.sh}"
capacity_runner="${REPOSITORY_CAPACITY_RUNNER:-${repo_root}/scripts/run-capacity-target.sh}"
watcher_pid=""

compose=(docker compose -p "${project}" --profile capacity-stack
  -f "${repo_root}/compose.yml"
  -f "${repo_root}/compose.scylla.cluster.yml"
  -f "${repo_root}/compose.capacity.yml")

export CAPACITY_GIT_REVISION="${revision}"
# The nested dispatcher uses this identity for its evidence directory. Keep it
# in the parent harness too, so the post-k6 Git/watch probe appends to that
# same immutable run directory rather than a sibling with an empty name.
export CAPACITY_RUN_ID="${CAPACITY_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
export CONFIG_FILE="${CONFIG_FILE:-${repo_root}/config/config.toml}"
export SCYLLA_CLUSTER_SMP="${SCYLLA_CLUSTER_SMP:-1}"
export SCYLLA_CLUSTER_MEMORY_LIMIT="${SCYLLA_CLUSTER_MEMORY_LIMIT:-1536m}"
export CAPACITY_PROMETHEUS_TARGETS_FILE="${CAPACITY_PROMETHEUS_TARGETS_FILE:-${state_dir}/prometheus-targets.json}"
export GITSTORE_BOOTSTRAP_ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
export GITSTORE_BOOTSTRAP_ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin123}"

wait_http() {
  local url="$1" label="$2" deadline=$((SECONDS + ${REPOSITORY_CAPACITY_WAIT_SECONDS:-300}))
  until curl -fsS --max-time 2 "${url}" >/dev/null 2>&1; do
    if (( SECONDS >= deadline )); then
      echo "timed out waiting for ${label}: ${url}" >&2
      return 1
    fi
    sleep 2
  done
}

wait_stack() {
  wait_http http://127.0.0.1:4000/ready "API A readiness"
  wait_http http://127.0.0.1:4001/ready "API B readiness"
  wait_http http://127.0.0.1:5001/health "controller A health"
  wait_http http://127.0.0.1:5002/health "controller B health"
  for endpoint in http://127.0.0.1:5001 http://127.0.0.1:5002; do
    health="$(curl -fsS --max-time 2 "${endpoint}/health")"
    jq -e --arg kind "${controller_kind}" '.credentialReady == true and .kinds[$kind].registered == true' <<<"${health}" >/dev/null || {
      echo "${controller_kind} controller is not ready at ${endpoint}" >&2
      return 1
    }
  done
}

controller_instance_id() {
  local endpoint="$1" metrics instance_id
  metrics="$(curl -fsS --max-time 5 "${endpoint}/metrics")"
  instance_id="$(sed -n 's/^gitstore_controller_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${metrics}" | head -1)"
  [[ -n "${instance_id}" ]] || {
    echo "controller does not expose a process identity at ${endpoint}" >&2
    return 1
  }
  printf '%s\n' "${instance_id}"
}

validate_controllers_post_run() {
  local endpoint health identity_a identity_b
  for endpoint in http://127.0.0.1:5001 http://127.0.0.1:5002; do
    # curl -f makes this an explicit HTTP 200 requirement, not merely a JSON
    # parse of an unhealthy response body.
    health="$(curl -fsS --max-time 5 "${endpoint}/health")"
    jq -e --arg kind "${controller_kind}" '.credentialReady == true and .kinds[$kind].registered == true' <<<"${health}" >/dev/null || {
      echo "${controller_kind} controller failed post-run registration validation at ${endpoint}" >&2
      return 1
    }
  done
  identity_a="$(controller_instance_id http://127.0.0.1:5001)"
  identity_b="$(controller_instance_id http://127.0.0.1:5002)"
  [[ "${identity_a}" != "${identity_b}" ]] || {
    echo "post-run controller endpoints resolve to the same process identity" >&2
    return 1
  }
}

cleanup_replacement_watcher() {
  if [[ -n "${watcher_pid}" ]]; then
    kill "${watcher_pid}" 2>/dev/null || true
    wait "${watcher_pid}" 2>/dev/null || true
    watcher_pid=""
  fi
}

capture_local_stack_failure() {
  mkdir -p "${state_dir}"
  "${compose[@]}" ps --all >"${state_dir}/compose-failure-ps.log" 2>&1 || true
  "${compose[@]}" logs --no-color >"${state_dir}/compose-failure.log" 2>&1 || true
  echo "Repository capacity failure logs: ${state_dir}/compose-failure.log" >&2
}

cleanup_local_stack() {
  local status="${1:-0}"
  cleanup_replacement_watcher
  if (( status != 0 )); then
    capture_local_stack_failure
  fi
  "${compose[@]}" down -v --remove-orphans || true
  rm -f "${trigger_file}" "${token_file}"
}

finish_local_stack() {
  local status=$?
  trap - EXIT INT TERM
  cleanup_local_stack "${status}"
  exit "${status}"
}

bootstrap_token() {
  mkdir -p "${state_dir}"
  local query payload response token
  query='mutation Login($username: String!, $password: String!) { login(input: { username: $username, password: $password }) { token { accessToken } } }'
  payload="$(jq -n --arg query "${query}" --arg username "${ADMIN_USERNAME:-admin}" --arg password "${ADMIN_PASSWORD:-admin123}" '{query:$query,variables:{username:$username,password:$password}}')"
  response="$(curl -fsS --max-time 10 -H 'Content-Type: application/json' --data "${payload}" http://127.0.0.1:4000/graphql)"
  token="$(jq -er 'if ((.errors // []) | length) == 0 then .data.login.token.accessToken else empty end' <<<"${response}")" || {
    jq -r '.errors[]?.message' <<<"${response}" >&2
    return 1
  }
  umask 077
  printf '%s\n' "${token}" >"${token_file}"
  echo "Repository capacity token written to ${token_file}"
}

host_architecture() {
  case "$(uname -m)" in
    arm64|aarch64) printf 'arm64\n' ;;
    x86_64|amd64) printf 'amd64\n' ;;
    *) uname -m ;;
  esac
}

host_memory_bytes() {
  local detected="${REPOSITORY_CAPACITY_HOST_MEMORY_BYTES:-}"
  if [[ -z "${detected}" ]] && command -v sysctl >/dev/null 2>&1; then
    detected="$(sysctl -n hw.memsize 2>/dev/null || true)"
  fi
  if [[ ! "${detected}" =~ ^[1-9][0-9]*$ ]] && [[ -r /proc/meminfo ]]; then
    detected="$(awk '/^MemTotal:/ {printf "%.0f", $2 * 1024; exit}' /proc/meminfo)"
  fi
  if [[ ! "${detected}" =~ ^[1-9][0-9]*$ ]] && command -v docker >/dev/null 2>&1; then
    detected="$(docker info --format '{{.MemTotal}}' 2>/dev/null || true)"
  fi
  [[ "${detected}" =~ ^[1-9][0-9]*$ ]] || {
    echo "unable to determine host memory; set REPOSITORY_CAPACITY_HOST_MEMORY_BYTES" >&2
    return 1
  }
  printf '%s\n' "${detected}"
}

write_manifests() {
  mkdir -p "${state_dir}"
  jq -n --argjson smp "${SCYLLA_CLUSTER_SMP}" '{schemaVersion:1,services:{api:{build:"release",replicas:2,watch:{pollInterval:"100ms",idleBackoffMax:"2s",readBatch:256,liveRing:512,subscriberBuffer:64}},gitService:{build:"release",replicas:1},scylla:{nodes:3,smpPerNode:$smp}}}' >"${state_dir}/config-manifest.json"
  jq -n --arg architecture "$(host_architecture)" --argjson cpus "$(getconf _NPROCESSORS_ONLN)" \
    --argjson memory "$(host_memory_bytes)" --argjson datastore_memory "${REPOSITORY_CAPACITY_SCYLLA_MEMORY_BYTES:-1610612736}" \
    '{schemaVersion:1,runtime:"docker",architecture:$architecture,host:{logicalCPUs:$cpus,memoryBytes:$memory},datastore:{authenticationMode:"local-unauthenticated",memoryBytesPerNode:$datastore_memory,requireNoUnexpectedRestarts:true,requireNoOOMKills:true},topology:{apiReplicas:2,gitServiceReplicas:1,scyllaNodes:3}}' >"${state_dir}/environment-manifest.json"
}

run_alpha() {
  wait_stack
  [[ -r "${token_file}" ]] || bootstrap_token
  write_manifests
  rm -f "${trigger_file}"
  "${replacement_watcher}" "${trigger_file}" "${project}" "${revision}" &
  watcher_pid=$!
  trap cleanup_replacement_watcher EXIT
  trap 'cleanup_replacement_watcher; exit 130' INT
  trap 'cleanup_replacement_watcher; exit 143' TERM
  CAPACITY_API_REPLICAS=2 \
  CAPACITY_CONTROLLER_REPLICAS=2 \
  CAPACITY_API_BUILD=release \
  CAPACITY_CONTROLLER_BUILD=release \
  CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_API_ENDPOINTS=http://127.0.0.1:4000,http://127.0.0.1:4001 \
  CAPACITY_API_CONTAINERS=gitstore-capacity-api-a,gitstore-capacity-api-b \
  CAPACITY_GIT_SERVICE_CONTAINER=gitstore-capacity-git-service \
  CAPACITY_SCYLLA_NODES=3 \
  CAPACITY_SCYLLA_SMP="${SCYLLA_CLUSTER_SMP}" \
  CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE="${REPOSITORY_CAPACITY_SCYLLA_MEMORY_BYTES:-1610612736}" \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated \
  CAPACITY_DATASTORE_CONTAINERS=gitstore-capacity-scylla-1,gitstore-capacity-scylla-2,gitstore-capacity-scylla-3 \
  CAPACITY_CONFIG_MANIFEST="${state_dir}/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${state_dir}/environment-manifest.json" \
  CAPACITY_API_A=http://127.0.0.1:4000 \
  CAPACITY_API_B=http://127.0.0.1:4001 \
  PRODUCT_CAPACITY_API_A=http://api-a:4000/graphql \
  PRODUCT_CAPACITY_API_B=http://api-b:4000/graphql \
  CAPACITY_DOCKER_NETWORK="${project}_gitstore-network" \
  CAPACITY_CONTROLLER_A=http://127.0.0.1:5001 \
  CAPACITY_CONTROLLER_B=http://127.0.0.1:5002 \
  CAPACITY_TOKEN_FILE="${token_file}" \
  REPOSITORY_API_A=http://127.0.0.1:4000 \
  REPOSITORY_API_B=http://127.0.0.1:4001 \
  REPOSITORY_OVERFLOW_API=http://127.0.0.1:4000 \
  REPOSITORY_CONTROLLER_A=http://127.0.0.1:5001 \
  REPOSITORY_CONTROLLER_B=http://127.0.0.1:5002 \
  REPOSITORY_API_REPLACEMENT=http://127.0.0.1:4001 \
  REPOSITORY_REPLACEMENT_TRIGGER_FILE="${trigger_file}" \
  REPOSITORY_TOKEN_FILE="${token_file}" \
  REPOSITORY_CAPACITY_DURATION="${REPOSITORY_CAPACITY_DURATION:-10m}" \
  REPOSITORY_CAPACITY_SUBSCRIBERS="${REPOSITORY_CAPACITY_SUBSCRIBERS:-100}" \
  REPOSITORY_CAPACITY_REPLAY_EVENTS="${REPOSITORY_CAPACITY_REPLAY_EVENTS:-1000}" \
  REPOSITORY_CAPACITY_REPLAY_SAMPLES="${REPOSITORY_CAPACITY_REPLAY_SAMPLES:-5}" \
  REPOSITORY_CAPACITY_RESOURCE_POOL="${REPOSITORY_CAPACITY_RESOURCE_POOL:-20}" \
  REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS="${REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS:-256}" \
  REPOSITORY_CAPACITY_OVERFLOW_BACKPRESSURE_WAIT="${REPOSITORY_CAPACITY_OVERFLOW_BACKPRESSURE_WAIT:-31s}" \
  REPOSITORY_CAPACITY_BURST_INTERVAL="${REPOSITORY_CAPACITY_BURST_INTERVAL:-1m}" \
  REPOSITORY_CAPACITY_BURST_SIZE="${REPOSITORY_CAPACITY_BURST_SIZE:-20}" \
  REPOSITORY_CAPACITY_TRANSITION_INTERVAL="${REPOSITORY_CAPACITY_TRANSITION_INTERVAL:-500ms}" \
  REPOSITORY_CAPACITY_REPLACEMENT_DELAY="${REPOSITORY_CAPACITY_REPLACEMENT_DELAY:-5m}" \
  REPOSITORY_CAPACITY_BASELINE_STABILIZATION="${REPOSITORY_CAPACITY_BASELINE_STABILIZATION:-1m}" \
  REPOSITORY_CAPACITY_POST_LOAD_STABILIZATION="${REPOSITORY_CAPACITY_POST_LOAD_STABILIZATION:-1m}" \
  "${capacity_runner}" "${capacity_target}" "${capacity_profile}" alpha
  if [[ "${capacity_target}" == "product" ]]; then
    local product_evidence_dir product_probe_status
    product_evidence_dir="${CAPACITY_EVIDENCE_DIR:-${repo_root}/.gitstore/capacity}/product/lifecycle/alpha/${CAPACITY_RUN_ID}"
    mkdir -p "${product_evidence_dir}"
    set +e
    PRODUCT_WATCH_API_A=http://127.0.0.1:4000 \
    PRODUCT_WATCH_API_B=http://127.0.0.1:4001 \
    PRODUCT_WATCH_API_REPLACEMENT=http://127.0.0.1:4001 \
    PRODUCT_WATCH_REPLACEMENT_TRIGGER_FILE="${trigger_file}" \
    PRODUCT_WATCH_TOKEN_FILE="${token_file}" \
    PRODUCT_CAPACITY_GIT_URL=http://127.0.0.1:9000 \
    PRODUCT_CAPACITY_NAMESPACE=default \
    PRODUCT_CAPACITY_REPOSITORY=gitstore-system \
    go -C "${repo_root}/tests/integration" test -count=1 -run '^TestProduct(Watch|CapacityGitPushParity)' . \
      2>&1 | tee "${product_evidence_dir}/product-integration.log"
    product_probe_status=${PIPESTATUS[0]}
    set -e
    jq -n --argjson exit_code "${product_probe_status}" \
      '{schemaVersion:1,gitPush:true,durableWatch:true,rollingReplacement:true,passed:($exit_code == 0),exitCode:$exit_code}' \
      >"${product_evidence_dir}/product-integration.json"
    # The k6 domain verifier covers GraphQL admission and the concurrent
    # deletion race; this companion artifact is a required gate input for the
    # real Git-push and WebSocket/replacement probes executed afterwards.
    jq -e '.passed == true and .gitPush == true and .durableWatch == true and .rollingReplacement == true and .exitCode == 0' \
      "${product_evidence_dir}/product-integration.json" >/dev/null || return 1
    (( product_probe_status == 0 )) || return "${product_probe_status}"
  fi
  validate_controllers_post_run
  cleanup_replacement_watcher
  trap - EXIT INT TERM
}

run_local_alpha() {
  mkdir -p "${state_dir}"
  rm -f "${trigger_file}"
  trap finish_local_stack EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  "${compose[@]}" up -d --build api-a api-b controller-manager-a controller-manager-b
  # Keep run_alpha's watcher-only traps scoped to the verifier so the outer
  # stack teardown remains installed for every success and failure path.
  (run_alpha)
  cleanup_local_stack 0
  trap - EXIT INT TERM
}

case "${action}" in
  up)
    mkdir -p "${state_dir}"
    rm -f "${trigger_file}"
    "${compose[@]}" up -d --build api-a api-b controller-manager-a controller-manager-b
    ;;
  wait) wait_stack ;;
  token) wait_stack; bootstrap_token ;;
  alpha) run_alpha ;;
  local-alpha) run_local_alpha ;;
  down)
    "${compose[@]}" down -v --remove-orphans
    rm -f "${trigger_file}" "${token_file}"
    ;;
  config) "${compose[@]}" config ;;
  *)
    echo "usage: $0 <up|wait|token|alpha|local-alpha|down|config>" >&2
    exit 2
    ;;
esac
