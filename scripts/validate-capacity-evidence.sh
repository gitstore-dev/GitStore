#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

evidence_dir="${1:?evidence directory is required}"
target="${2:?capacity target is required}"
profile="${3:?capacity profile is required}"
mode="${4:?capacity mode is required}"
phase="${5:-preflight}"

case "${phase}" in
  preflight|postflight) ;;
  source-state)
    (
      cd "$(dirname "${BASH_SOURCE[0]}")/.."
      {
        git status --porcelain=v1
        git diff --binary HEAD
        while IFS= read -r -d '' untracked; do
          shasum -a 256 "${untracked}"
        done < <(git ls-files --others --exclude-standard -z)
      } | shasum -a 256 | awk '{print $1}'
    )
    exit 0
    ;;
  *) echo "capacity evidence phase must be preflight or postflight" >&2; exit 2 ;;
esac

evidence_file="${evidence_dir}/${phase}-environment.json"

case "${REPOSITORY_CAPACITY_SECRET_SCENARIO:-0}" in
  0)
    if [[ -e "${evidence_dir}/secret-evidence.json" || -L "${evidence_dir}/secret-evidence.json" ||
          -e "${evidence_dir}/secret" || -L "${evidence_dir}/secret" ]]; then
      echo "secret capacity evidence requires REPOSITORY_CAPACITY_SECRET_SCENARIO=1; it cannot be validated as Repository-only evidence" >&2
      exit 2
    fi
    ;;
  1)
    [[ "${target}/${profile}" == repository/lifecycle &&
       "${CHAOS_CONFIRM:-0}" == 1 && "${REPOSITORY_CAPACITY_SECRET_OWNED_DEPLOYMENT:-0}" == 1 &&
       -r "${REPOSITORY_CAPACITY_SECRET_FIXTURE_DIR:-}/owned-fixture.json" ]] || {
      echo "secret capacity requires repository/lifecycle, CHAOS_CONFIRM=1 and provisioned owned fixtures" >&2
      exit 2
    }
    ;;
  *)
    echo "REPOSITORY_CAPACITY_SECRET_SCENARIO must be 0 or 1" >&2
    exit 2
    ;;
esac

if [[ "${target}/${profile}" == api/authorization && "${phase}" == postflight ]]; then
  artifacts=(authorization-latency-1.json authorization-latency-8.json authorization-latency-32.json authorization-reload.json authorization-evaluator-audit.json)
  for artifact in "${artifacts[@]}"; do
    path="${evidence_dir}/${artifact}"
    [[ -f "${path}" && ! -L "${path}" && "$(wc -c <"${path}")" -le 1048576 ]] || {
      echo "authorization evidence requires a regular bounded ${artifact}" >&2
      exit 2
    }
  done
  declared_environment='{}'
  if [[ "${mode}" != diagnostic ]]; then
    [[ -f "${evidence_dir}/environment.json" && ! -L "${evidence_dir}/environment.json" ]] || {
      echo "authorization acceptance requires its declared runtime environment" >&2
      exit 2
    }
    declared_environment="$(jq -c . "${evidence_dir}/environment.json")"
  fi
  jq -e -n --arg mode "${mode}" \
    --argjson environment "${declared_environment}" \
    --slurpfile one "${evidence_dir}/authorization-latency-1.json" \
    --slurpfile eight "${evidence_dir}/authorization-latency-8.json" \
    --slurpfile thirtytwo "${evidence_dir}/authorization-latency-32.json" \
    --slurpfile reload "${evidence_dir}/authorization-reload.json" \
    --slurpfile audit "${evidence_dir}/authorization-evaluator-audit.json" '
      def integer: type == "number" and . >= 0 and floor == .;
      def digest: type == "string" and test("^[0-9a-f]{64}$");
      def histogram:
        (.samples | integer) and .samples > 0 and
        (.calls | integer) and .calls >= .samples and
        (.maxNanoseconds | integer) and .maxNanoseconds > 0 and
        (.bucketsUpperMicroseconds | length) == 2001 and
        all(.bucketsUpperMicroseconds[]; integer) and
        (.bucketsUpperMicroseconds | add) == .samples and
        (if $mode == "diagnostic" then true else
          .samples >= 1000 and
          # Bucket 999 has the conservative upper bound of 1ms, so exclude it.
          (.bucketsUpperMicroseconds[0:999] | add) >= ((.samples * 0.99) | ceil)
        end);
      [$one[0], $eight[0], $thirtytwo[0]] as $latencies |
      ($one | length) == 1 and ($eight | length) == 1 and ($thirtytwo | length) == 1 and
      ($reload | length) == 1 and ($audit | length) == 1 and
      [$latencies[].concurrency] == [1,8,32] and
      ([$latencies[].policyDigest] | unique | length) == 1 and
      ([$latencies[].runtime] | unique | length) == 1 and
      ([$latencies[].architecture] | unique | length) == 1 and
      all($latencies[];
        .schemaVersion == 1 and (.policyDigest | digest) and
        .policyDimensions == {roles:200,bindings:1000,rules:2000,groups:50} and
        .cases == ["early-allow","last-allow","no-binding","overlapping-deny","owner-transfer","missing-attribute"] and
        .sampleProbability == "1/64" and .invalidOutcomes == 0 and
        (.runtime | type == "string" and length > 0) and
        (.os | type == "string" and length > 0) and
        (.architecture | type == "string" and length > 0) and
        (.logicalCPUs | integer) and .logicalCPUs > 0 and
        (.gomaxprocs | integer) and .gomaxprocs > 0 and
        (.histograms | length) == 6 and all(.histograms[]; histogram) and
        (.warmupSeconds | type == "number") and .warmupSeconds > 0 and
        (.measurementSeconds | type == "number") and .measurementSeconds > 0 and
        ($mode == "diagnostic" or
          (.warmupSeconds >= 60 and .measurementSeconds >= 600 and
           .runtime == $environment.runtime and .architecture == $environment.architecture and
           .logicalCPUs == $environment.host.logicalCPUs))) and
      ($reload[0] |
        .schemaVersion == 1 and .concurrency == 32 and
        (.oldDigest | digest) and (.newDigest | digest) and .oldDigest != .newDigest and
        .oldDigest == $latencies[0].policyDigest and
        (.decisions | integer) and .decisions > 0 and .invalidOutcomes == 0 and
        (.reloads | integer) and .reloads > 0 and (.reloads % 2) == 0 and
        (.rejectedReloads | integer) and .rejectedReloads == (.reloads * 3) and
        (.beforeHeapBytes | integer) and .beforeHeapBytes > 0 and
        (.afterHeapBytes | integer) and .afterHeapBytes > 0 and
        (.durationSeconds | type == "number") and .durationSeconds > 0 and
        ($mode == "diagnostic" or
          (.durationSeconds >= 1800 and .reloads >= 60 and .afterHeapBytes <= (.beforeHeapBytes * 1.1)))) and
      ($audit[0] |
        .schemaVersion == 1 and .passed == true and .root == "Authorize" and
        .allowedClockRead == "time.Now" and
        .forbiddenDependencies == ["policy-file","datastore-client","network-client"] and
        (.functions | index("Authorize")) != null and
        (.functions | index("loadPolicy")) == null and
        (.calls | type == "array" and length > 0))
    ' >/dev/null || {
      echo "authorization evidence failed duration, case, accounting, latency, snapshot, heap or dependency checks" >&2
      exit 1
    }
  jq -n --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
    '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true,scope:"evaluator-only"}' \
    >"${evidence_file}"
  exit 0
fi

readonly authorization_snapshot_filter='
  def integer: type == "number" and floor == .;
  def digest: type == "string" and test("^[0-9a-f]{64}$");
  def snapshot:
    keys == ["authn","authz","catalogDigest","policy","userdir"] and
    (.catalogDigest | digest) and
    (.authn | type == "array" and length > 0 and length <= 5 and (unique | length) == length) and
    all(.authn[]; . == "static-users" or . == "anonymous" or . == "oidc-jwt" or
      . == "serviceaccount-assertion" or . == "serviceaccount-jwt") and
    (.authz | type == "string" and length > 0 and length <= 64) and
    (.userdir | type == "string" and length > 0 and length <= 64) and
    (.authz != "rbac-local" or .policy.apiVersion == "rbac.authorization.gitstore.dev/v1beta1") and
    (.policy |
      keys == ["apiVersion","bindings","digest","loaded","roles","rules"] and .loaded == true and
      (.apiVersion | type == "string" and length > 0 and length <= 128) and
      (.digest | digest) and (.roles | integer) and .roles >= 0 and .roles <= 200 and
      (.bindings | integer) and .bindings >= 0 and .bindings <= 1000 and
      (.rules | integer) and .rules >= 0 and .rules <= 2000);
'

if [[ "${target}/${profile}" == api/readiness && "${phase}" == postflight &&
      ("${mode}" != diagnostic || -n "${CAPACITY_TOKEN_FILE:-}${CAPACITY_TOKEN:-}" ||
       -e "${evidence_dir}/authorization-probes.json" || -L "${evidence_dir}/authorization-probes.json") ]]; then
  path="${evidence_dir}/authorization-probes.json"
  [[ -f "${path}" && ! -L "${path}" && "$(wc -c <"${path}")" -le 1048576 ]] || {
    echo "API readiness requires regular bounded authorization-probes.json evidence" >&2
    exit 2
  }
  preflight='{}'
  if [[ "${mode}" != diagnostic ]]; then
    [[ -f "${evidence_dir}/preflight-environment.json" && ! -L "${evidence_dir}/preflight-environment.json" ]] || {
      echo "API readiness acceptance requires verified preflight replica identities" >&2
      exit 2
    }
    preflight="$(jq -c . "${evidence_dir}/preflight-environment.json")"
  fi
  jq -e --arg mode "${mode}" --arg run "${CAPACITY_RUN_ID:-}" --argjson preflight "${preflight}" "${authorization_snapshot_filter}"'
    keys == ["component","denialCode","deniedIdentity","droppedIterations","duration","maxVUs","measurementSeconds","mismatches",
      "mode","passed","preAllocatedVUs","rate","replicas","runID","schemaVersion"] and
    .schemaVersion == 1 and .component == "api-readiness-authorization" and
    .mode == $mode and .runID == $run and .passed == true and
    (.deniedIdentity == "anonymous" or .deniedIdentity == "credential") and
    (.denialCode == "FORBIDDEN" or .denialCode == "UNAUTHENTICATED") and
    .mismatches == 0 and .droppedIterations == 0 and (.measurementSeconds | type == "number" and . > 0) and
    (.replicas | type == "array" and length > 0) and
    ([.replicas[].endpoint] | unique | length) == (.replicas | length) and
    ([.replicas[].auth] | unique | length) == 1 and
    all(.replicas[];
      keys == ["allowed","attempts","auth","denied","endpoint","readiness"] and
      (.endpoint | type == "string" and test("^https?://[^@\\s,?#]+$")) and
      (.auth | snapshot) and
      (.attempts | integer) and .attempts > 0 and
      .allowed == .attempts and .denied == .attempts and
      (.readiness |
        keys == ["httpFailureRate","p95Milliseconds","p99Milliseconds"] and
        (.p95Milliseconds | type == "number" and . >= 0 and . < 1000) and
        (.p99Milliseconds | type == "number" and . >= 0 and . < 3000) and
        (.httpFailureRate | type == "number" and . >= 0 and . < 0.001))) and
    ($mode == "diagnostic" or
      (.rate == 10 and .preAllocatedVUs == 10 and .maxVUs == 100 and .measurementSeconds >= 600 and
       (.replicas | length) >= 2 and all(.replicas[]; .attempts >= 6000) and
       $preflight.passed == true and $preflight.mode == $mode and
       ([.replicas[].endpoint] | sort) == ([$preflight.topology.liveReplicas[].endpoint] | sort) and
       all(.replicas[]; . as $replica |
         any($preflight.topology.liveReplicas[]; .endpoint == $replica.endpoint and .auth == $replica.auth)) and
       ([$preflight.topology.liveReplicas[].instanceID] | unique | length) == (.replicas | length)))
  ' "${path}" >/dev/null || {
    echo "API readiness authorization evidence failed exact outcomes, sampling, snapshot, replica or duration checks" >&2
    exit 1
  }
  jq -n --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
    '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true,scope:"readiness-and-baseline-authorization"}' \
    >"${evidence_file}"
  exit 0
fi

if [[ "${target}/${profile}" == repository/lifecycle && "${phase}" == postflight &&
      ("${mode}" != diagnostic || -e "${evidence_dir}/authorization-probes.json" ||
       -L "${evidence_dir}/authorization-probes.json") ]]; then
  path="${evidence_dir}/authorization-probes.json"
  preflight="${evidence_dir}/preflight-environment.json"
  for artifact in "${path}" "${preflight}"; do
    [[ -f "${artifact}" && ! -L "${artifact}" && "$(wc -c <"${artifact}")" -le 1048576 ]] || {
      echo "Repository lifecycle requires regular bounded authorization and preflight evidence" >&2
      exit 2
    }
  done
  jq -e --arg mode "${mode}" --arg run "${CAPACITY_RUN_ID:-}" --slurpfile preflight "${preflight}" \
    "${authorization_snapshot_filter}"'
    def replicas:
      type == "array" and length == 2 and
      ([.[].endpoint] | unique | length) == 2 and
      all(.[];
        keys == ["allowed","auth","denialCode","denied","endpoint"] and
        (.endpoint | type == "string" and test("^https?://[^@\\s,?#]+$")) and
        (.auth | snapshot) and .allowed == true and .denied == true and
        .denialCode == (if (.auth.authn | index("anonymous")) != null then "FORBIDDEN" else "UNAUTHENTICATED" end));
    keys == ["after","before","component","mode","passed","runID","schemaVersion"] and
    .schemaVersion == 1 and .component == "repository-lifecycle-authorization" and
    .mode == $mode and .runID == $run and .passed == true and
    (.before | replicas) and (.after | replicas) and
    ([.before[].auth, .after[].auth] | unique | length) == 1 and
    ([.before[].endpoint] | sort) == ([.after[].endpoint] | sort) and
    $preflight[0].passed == true and $preflight[0].mode == $mode and
    ([.before[].endpoint] | sort) == ([$preflight[0].topology.liveApiReplicas[].endpoint] | sort) and
    ($preflight[0].topology.liveApiReplicas | length) == 2 and
    all($preflight[0].topology.liveApiReplicas[]; .instanceID | type == "string" and length > 0) and
    ([$preflight[0].topology.liveApiReplicas[].instanceID] | unique | length) == 2
  ' "${path}" >/dev/null || {
    echo "Repository lifecycle authorization evidence failed exact outcomes, snapshot or replica checks" >&2
    exit 1
  }
fi

if [[ "${mode}" == "diagnostic" ]]; then
  jq -n --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
    '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true}' \
    >"${evidence_file}"
  exit 0
fi

config="${evidence_dir}/config.json"
environment="${evidence_dir}/environment.json"
[[ -r "${config}" ]] || { echo "${mode} capacity evidence requires CAPACITY_CONFIG_MANIFEST" >&2; exit 2; }
[[ -r "${environment}" ]] || { echo "${mode} capacity evidence requires CAPACITY_ENVIRONMENT_MANIFEST" >&2; exit 2; }

jq -e '
  .schemaVersion == 1 and
  (.runtime | strings | length) > 0 and
  (.architecture | strings | length) > 0 and
  (.host.logicalCPUs | numbers) > 0 and
  (.host.memoryBytes | numbers) > 0
' "${environment}" >/dev/null || {
  echo "capacity environment manifest must declare runtime, architecture, host CPUs, and host memory" >&2
  exit 2
}

require_declared_integer() {
  local name="$1" expected="$2" value="${!1:-}"
  [[ "${value}" =~ ^[1-9][0-9]*$ ]] || { echo "${mode} capacity evidence requires ${name}" >&2; exit 2; }
  (( value == expected )) || { echo "${name}=${value} does not match manifest value ${expected}" >&2; exit 2; }
}

live_api_replicas='[]'
release_service_containers='[]'
validate_api_deployment() {
  local expected_replicas="$1" endpoint metrics process_start instance_id
  [[ -n "${CAPACITY_API_ENDPOINTS:-}" ]] || {
    echo "${target}/${profile} evidence requires CAPACITY_API_ENDPOINTS" >&2
    exit 2
  }
  IFS=',' read -r -a api_endpoints <<<"${CAPACITY_API_ENDPOINTS}"
  (( ${#api_endpoints[@]} == expected_replicas )) || {
    echo "CAPACITY_API_ENDPOINTS count does not match the declared API topology" >&2
    exit 2
  }
  for endpoint in "${api_endpoints[@]}"; do
    [[ "${endpoint}" =~ ^https?://[^[:space:],]+$ ]] || { echo "invalid live API endpoint: ${endpoint}" >&2; exit 2; }
    metrics="$(curl -fsS --max-time 5 "${endpoint%/}/metrics")" || {
      echo "cannot scrape live API endpoint: ${endpoint}" >&2
      exit 1
    }
    process_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${metrics}")"
    [[ "${process_start}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]] || {
      echo "live API endpoint does not expose process_start_time_seconds: ${endpoint}" >&2
      exit 1
    }
    instance_id="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${metrics}" | head -1)"
    [[ -n "${instance_id}" ]] || {
      echo "live API endpoint does not expose gitstore_api_process_instance_info: ${endpoint}" >&2
      exit 1
    }
    live_api_replicas="$(jq -c --arg endpoint "${endpoint%/}" --arg process_start "${process_start}" --arg instance_id "${instance_id}" \
      '. + [{endpoint:$endpoint,instanceID:$instance_id,processStartTimeSeconds:($process_start|tonumber)}]' <<<"${live_api_replicas}")"
  done
  [[ "$(jq '[.[].instanceID] | unique | length' <<<"${live_api_replicas}")" == "${expected_replicas}" ]] || {
    echo "CAPACITY_API_ENDPOINTS must identify distinct live API process instances" >&2
    exit 1
  }
}

validate_release_service_containers() {
  local expected_replicas="$1" expected_git_replicas="${2:-1}" source_revision inspection api_names_json git_names_json
  local git_container_csv container metrics process_start instance_id
  local endpoint_start endpoint_instance container_start container_instance container_id index
  git_container_csv="${CAPACITY_GIT_SERVICE_CONTAINERS:-${CAPACITY_GIT_SERVICE_CONTAINER:-}}"
  [[ -n "${CAPACITY_API_CONTAINERS:-}" && -n "${git_container_csv}" ]] || {
    echo "${target}/${profile} evidence requires CAPACITY_API_CONTAINERS and CAPACITY_GIT_SERVICE_CONTAINERS" >&2
    exit 2
  }
  IFS=',' read -r -a api_containers <<<"${CAPACITY_API_CONTAINERS}"
  IFS=',' read -r -a git_containers <<<"${git_container_csv}"
  (( ${#api_containers[@]} == expected_replicas )) || {
    echo "CAPACITY_API_CONTAINERS count does not match the declared API topology" >&2
    exit 2
  }
  (( ${#git_containers[@]} == expected_git_replicas )) || {
    echo "Git-service container count does not match the declared Git-service topology" >&2
    exit 2
  }
  for container in "${api_containers[@]}" "${git_containers[@]}"; do
    [[ "${container}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || {
      echo "invalid release service container name: ${container}" >&2
      exit 2
    }
  done
  api_names_json="$(printf '%s\n' "${api_containers[@]}" | jq -Rsc 'split("\n")[:-1]')"
  git_names_json="$(printf '%s\n' "${git_containers[@]}" | jq -Rsc 'split("\n")[:-1]')"
  source_revision="$(git rev-parse HEAD)"
  inspection="$(docker inspect "${api_containers[@]}" "${git_containers[@]}")" || {
    echo "cannot inspect live API and Git-service containers" >&2
    exit 1
  }
  release_service_containers="$(jq -c \
    --argjson api_names "${api_names_json}" --argjson git_names "${git_names_json}" \
    --arg revision "${source_revision}" '
      [.[] |
        (.Name | ltrimstr("/")) as $name |
        {id:.Id,
         name:$name,
         role:(if ($api_names | index($name)) != null then "api" elif ($git_names | index($name)) != null then "git-service" else "unknown" end),
         imageReference:.Config.Image,
         imageID:.Image,
         executable:.Path,
         arguments:(.Args // []),
         gitServiceURI:(((.Config.Env // []) | map(select(startswith("GITSTORE_API__GIT_SERVICE__URI="))) | first // "") | sub("^GITSTORE_API__GIT_SERVICE__URI="; "")),
         catalogServiceURI:(((.Config.Env // []) | map(select(startswith("GITSTORE_GIT_SERVICE__CATALOG__URI="))) | first // "") | sub("^GITSTORE_GIT_SERVICE__CATALOG__URI="; "")),
         revision:(.Config.Labels["org.opencontainers.image.revision"] // ""),
         running:.State.Running}]
    ' <<<"${inspection}")"
  jq -e --argjson expected_replicas "${expected_replicas}" --argjson expected_git_replicas "${expected_git_replicas}" --arg revision "${source_revision}" --arg mode "${mode}" '
    length == ($expected_replicas + $expected_git_replicas) and
    ([.[].id] | unique | length) == length and
    ([.[].name] | unique | length) == length and
    ([.[] | select(.role == "api")] | length) == $expected_replicas and
    ([.[] | select(.role == "git-service")] | length) == $expected_git_replicas and
    all(.[];
      .running and .revision == $revision and
      (if $mode == "production" then
         (.imageReference | test("@sha256:[0-9a-f]{64}$"))
       else
         (.imageID | test("^sha256:[0-9a-f]{64}$"))
       end) and
      ((.role == "api" and (
          .executable == "/app/api" or
          (.executable == "/bin/sh" and
           (.arguments | length) == 2 and
           .arguments[0] == "-ec" and
           (.arguments[1] | contains("cat /run/secrets/serviceaccount-signing-key")) and
           (.arguments[1] | contains("exec /app/api --config-file /etc/gitstore/gitstore.toml"))))) or
       (.role == "git-service" and .executable == "/app/git-service")))
  ' <<<"${release_service_containers}" >/dev/null || {
    echo "live API and Git-service containers must use exact-revision release images with immutable identities and an approved executable shape (production requires digest pins)" >&2
    exit 1
  }
  if [[ "${target}/${profile}" == "repository/lifecycle" ]]; then
    jq -e --argjson expected_replicas "${expected_replicas}" '
      ([.[] | select(.role == "api") | .gitServiceURI] | length) == $expected_replicas and
      ([.[] | select(.role == "api") | .gitServiceURI] | all(length > 0)) and
      ([.[] | select(.role == "api") | .gitServiceURI] | unique | length) == 1 and
      ([.[] | select(.role == "git-service") | .catalogServiceURI] == ["dns:///api-a:6000"])
    ' <<<"${release_service_containers}" >/dev/null || {
      echo "repository/lifecycle evidence requires both APIs to use the singleton Git service whose CatalogService callback targets API A" >&2
      exit 1
    }
  fi

  container_identities='[]'
  for container in "${api_containers[@]}"; do
    metrics="$(docker exec "${container}" wget -qO- http://127.0.0.1:4000/metrics)" || {
      echo "cannot scrape API metrics inside container: ${container}" >&2
      exit 1
    }
    process_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${metrics}")"
    [[ "${process_start}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]] || {
      echo "API container does not expose process_start_time_seconds: ${container}" >&2
      exit 1
    }
    instance_id="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${metrics}" | head -1)"
    [[ -n "${instance_id}" ]] || {
      echo "API container does not expose gitstore_api_process_instance_info: ${container}" >&2
      exit 1
    }
    container_identities="$(jq -c --arg name "${container}" --arg process_start "${process_start}" --arg instance_id "${instance_id}" \
      '. + [{name:$name,instanceID:$instance_id,processStartTimeSeconds:($process_start|tonumber)}]' <<<"${container_identities}")"
  done
  for index in "${!api_containers[@]}"; do
    endpoint_start="$(jq -r --argjson index "${index}" '.[$index].processStartTimeSeconds' <<<"${live_api_replicas}")"
    endpoint_instance="$(jq -r --argjson index "${index}" '.[$index].instanceID' <<<"${live_api_replicas}")"
    container_start="$(jq -r --argjson index "${index}" '.[$index].processStartTimeSeconds' <<<"${container_identities}")"
    container_instance="$(jq -r --argjson index "${index}" '.[$index].instanceID' <<<"${container_identities}")"
    [[ "${endpoint_start}" == "${container_start}" && "${endpoint_instance}" == "${container_instance}" ]] || {
      echo "API endpoint ${api_endpoints[$index]} does not map to container ${api_containers[$index]}" >&2
      exit 1
    }
    container_id="$(jq -r --arg name "${api_containers[$index]}" '.[] | select(.name == $name) | .id' <<<"${release_service_containers}")"
    live_api_replicas="$(jq -c --argjson index "${index}" --arg container "${api_containers[$index]}" --arg id "${container_id}" \
      '.[$index] += {container:$container,containerID:$id}' <<<"${live_api_replicas}")"
  done
  release_service_containers="$(jq -c --argjson identities "${container_identities}" '
    map((if .role == "api" then . as $service | . + ($identities[] | select(.name == $service.name) | {instanceID,processStartTimeSeconds}) else . end)
      | . + {argumentCount:(.arguments | length),approvedExecutable:true} | del(.arguments))
  ' <<<"${release_service_containers}")"
}

validate_scylla_deployment() {
  local config_nodes config_smp environment_nodes environment_memory environment_auth
  config_nodes="$(jq -r '.services.scylla.nodes' "${config}")"
  config_smp="$(jq -r '.services.scylla.smpPerNode' "${config}")"
  environment_nodes="$(jq -r '.topology.scyllaNodes' "${environment}")"
  environment_memory="$(jq -r '.datastore.memoryBytesPerNode' "${environment}")"
  environment_auth="$(jq -r '.datastore.authenticationMode' "${environment}")"
  (( config_nodes == environment_nodes )) || { echo "Scylla node counts differ between manifests" >&2; exit 2; }
  require_declared_integer CAPACITY_SCYLLA_NODES "${config_nodes}"
  require_declared_integer CAPACITY_SCYLLA_SMP "${config_smp}"
  require_declared_integer CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE "${environment_memory}"
  [[ "${CAPACITY_SCYLLA_AUTH_MODE:-}" == "${environment_auth}" ]] || {
    echo "CAPACITY_SCYLLA_AUTH_MODE must match the environment manifest" >&2
    exit 2
  }
  [[ -n "${CAPACITY_DATASTORE_CONTAINERS:-}" ]] || {
    echo "${target}/${profile} evidence requires CAPACITY_DATASTORE_CONTAINERS" >&2
    exit 2
  }
  IFS=',' read -r -a datastore_containers <<<"${CAPACITY_DATASTORE_CONTAINERS}"
  (( ${#datastore_containers[@]} == config_nodes )) || {
    echo "CAPACITY_DATASTORE_CONTAINERS count does not match the declared Scylla topology" >&2
    exit 2
  }
}

case "${target}/${profile}" in
  api/authorization)
    jq -e '.schemaVersion == 1' "${config}" >/dev/null || {
      echo "authorization evidence requires a versioned configuration manifest" >&2
      exit 2
    }
    ;;
  namespace/validation)
    jq -e '.schemaVersion == 1 and .services.api.replicas == 2' "${config}" >/dev/null || {
      echo "namespace/validation evidence requires a config manifest declaring its two in-process API replicas" >&2
      exit 2
    }
    [[ "$(jq -r '.topology.apiReplicas' "${environment}")" == "2" ]] || {
      echo "namespace/validation environment manifest must declare its two in-process API replicas" >&2
      exit 2
    }
    ;;
  namespace/watch|namespace/recovery|repository/lifecycle)
    if [[ "${profile}" == "watch" && "${NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT:-0}" == "1" ]]; then
      echo "NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT is only valid in diagnostic mode" >&2
      exit 2
    fi
    jq -e --arg mode "${mode}" '
      .schemaVersion == 1 and
      (.services.api.replicas | numbers) >= 2 and
      .services.api.build == "release" and
      (.services.scylla.nodes | numbers) >= 3 and
      ((.services.scylla.smpPerNode | numbers) >= (if $mode == "alpha" then 1 else 2 end)) and
      (.services.gitService.build == "release")
    ' "${config}" >/dev/null || {
      echo "${target}/${profile} evidence requires three Scylla nodes, alpha >=1 shard per node (production >=2), and a release Git service" >&2
      exit 2
    }
    jq -e '
      (.datastore.authenticationMode | strings | length) > 0 and
      .datastore.authenticationMode != "unknown" and
      (.datastore.memoryBytesPerNode | numbers) > 0 and
      .datastore.requireNoUnexpectedRestarts == true and
      .datastore.requireNoOOMKills == true and
      (.topology.scyllaNodes | numbers) >= 3
    ' "${environment}" >/dev/null || {
      echo "${target}/${profile} evidence requires explicit Scylla topology, memory, authentication, restart, and OOM declarations" >&2
      exit 2
    }
    config_api_replicas="$(jq -r '.services.api.replicas' "${config}")"
    environment_api_replicas="$(jq -r '.topology.apiReplicas' "${environment}")"
    config_git_replicas="$(jq -r '.services.gitService.replicas // 1' "${config}")"
    environment_git_replicas="$(jq -r '.topology.gitServiceReplicas // 1' "${environment}")"
    (( config_api_replicas == environment_api_replicas )) || { echo "API replica counts differ between manifests" >&2; exit 2; }
    (( config_git_replicas == environment_git_replicas )) || { echo "Git-service replica counts differ between manifests" >&2; exit 2; }
    if [[ "${target}/${profile}" == "repository/lifecycle" ]] && (( config_git_replicas != 1 )); then
      echo "repository/lifecycle evidence requires exactly one singleton Git service" >&2
      exit 2
    fi
    require_declared_integer CAPACITY_API_REPLICAS "${config_api_replicas}"
    [[ "${CAPACITY_API_BUILD:-}" == "release" ]] || { echo "CAPACITY_API_BUILD=release is required" >&2; exit 2; }
    [[ "${CAPACITY_GIT_SERVICE_BUILD:-}" == "release" ]] || { echo "CAPACITY_GIT_SERVICE_BUILD=release is required" >&2; exit 2; }
    validate_api_deployment "${config_api_replicas}"
    [[ -n "${NAMESPACE_WATCH_API_A:-}" && -n "${NAMESPACE_WATCH_API_B:-}" ]] || {
      echo "${target}/${profile} evidence requires both API endpoints" >&2
      exit 2
    }
    api_a="${NAMESPACE_WATCH_API_A%/}"
    api_b="${NAMESPACE_WATCH_API_B%/}"
    [[ "${api_a}" != "${api_b}" ]] || {
      echo "${target}/${profile} API endpoints must identify distinct replicas" >&2
      exit 2
    }
    jq -e --arg api_a "${api_a}" --arg api_b "${api_b}" \
      '([.[].endpoint] | index($api_a)) != null and ([.[].endpoint] | index($api_b)) != null' \
      <<<"${live_api_replicas}" >/dev/null || {
      echo "NAMESPACE_WATCH_API_A and NAMESPACE_WATCH_API_B must be members of CAPACITY_API_ENDPOINTS" >&2
      exit 2
    }
    validate_release_service_containers "${config_api_replicas}" "${config_git_replicas}"
    validate_scylla_deployment
    [[ -n "${NAMESPACE_WATCH_API_REPLACEMENT:-}" ]] || {
      echo "${target}/${profile} evidence requires a replacement endpoint" >&2
      exit 2
    }
    replacement="${NAMESPACE_WATCH_API_REPLACEMENT%/}"
    [[ "${replacement}" == "${api_a}" || "${replacement}" == "${api_b}" ]] || {
      echo "${target}/${profile} replacement must identify one of the two replicas" >&2
      exit 2
    }
    [[ -n "${NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE:-}" ]] || {
      echo "${target}/${profile} requires NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE" >&2
      exit 2
    }
    [[ "${phase}" == "postflight" || ! -e "${NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE}" ]] || {
      echo "${target}/${profile} requires a fresh NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE" >&2
      exit 2
    }
    if [[ "${phase}" == "postflight" ]]; then
      preflight_file="${evidence_dir}/preflight-environment.json"
      [[ -r "${preflight_file}" ]] || {
        echo "postflight replacement validation requires preflight-environment.json" >&2
        exit 2
      }
      jq -e --arg replacement "${replacement}" --argjson after "${live_api_replicas}" '
        .topology.liveApiReplicas as $before |
        ($before | length) == ($after | length) and
        ([
          $before[] as $old |
          $after[] |
          select(.endpoint == $old.endpoint and .instanceID != $old.instanceID) |
          .endpoint
        ] == [$replacement]) and
        all($before[];
          . as $old |
          any($after[];
            .endpoint == $old.endpoint and
            (if .endpoint == $replacement then
               .instanceID != $old.instanceID
             else
               .instanceID == $old.instanceID and .containerID == $old.containerID
             end)))
      ' "${preflight_file}" >/dev/null || {
        echo "postflight must replace exactly the selected API replica and leave every other replica unchanged" >&2
        exit 1
      }
    fi
    if [[ "${profile}" == "recovery" || "${target}/${profile}" == "repository/lifecycle" ]]; then
      [[ -n "${NAMESPACE_WATCH_TOKEN:-}" || -r "${NAMESPACE_WATCH_TOKEN_FILE:-}" ]] || {
        echo "${target}/${profile} evidence requires a watch token or readable token file" >&2
        exit 2
      }
      minimum_overflow=1000
      if [[ "${target}/${profile}" == "repository/lifecycle" && "${mode}" == "alpha" ]]; then
        minimum_overflow=256
      fi
      [[ "${NAMESPACE_WATCH_OVERFLOW_TRANSITIONS:-}" =~ ^[1-9][0-9]*$ ]] &&
        (( 10#${NAMESPACE_WATCH_OVERFLOW_TRANSITIONS} >= minimum_overflow )) || {
        echo "${target}/${profile} ${mode} evidence requires at least ${minimum_overflow} overflow transitions" >&2
        exit 2
      }
    fi
    ;;
  scylla/soak)
    jq -e '
      .schemaVersion == 1 and
      (.services.scylla.nodes | numbers) >= 3 and
      (.services.scylla.smpPerNode | numbers) >= 2
    ' "${config}" >/dev/null || {
      echo "scylla/soak evidence requires three Scylla nodes with at least two shards each" >&2
      exit 2
    }
    jq -e '
      (.datastore.authenticationMode | strings | length) > 0 and
      .datastore.authenticationMode != "unknown" and
      (.datastore.memoryBytesPerNode | numbers) > 0 and
      .datastore.requireNoUnexpectedRestarts == true and
      .datastore.requireNoOOMKills == true and
      (.topology.scyllaNodes | numbers) >= 3
    ' "${environment}" >/dev/null || {
      echo "scylla/soak evidence requires explicit Scylla topology, memory, authentication, restart, and OOM declarations" >&2
      exit 2
    }
    validate_scylla_deployment
    echo "scylla/soak cannot produce ${mode} evidence until the harness verifies the observed preloaded product count" >&2
    exit 2
    ;;
  *)
    echo "unsupported non-k6 capacity profile: ${target}/${profile}" >&2
    exit 2
    ;;
esac

jq -n \
  --arg target "${target}" --arg profile "${profile}" --arg mode "${mode}" \
  --argjson live_api_replicas "${live_api_replicas}" \
  --argjson release_service_containers "${release_service_containers}" \
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,target:$target,profile:$profile,mode:$mode,passed:true,config:$config[0],environment:$environment[0]} +
   (if ($live_api_replicas | length) == 0 then {} else {topology:{liveApiReplicas:$live_api_replicas},artifacts:{releaseServiceContainers:$release_service_containers}} end)' \
  >"${evidence_file}"
