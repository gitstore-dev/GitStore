#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dispatcher="${repo_root}/scripts/run-capacity-target.sh"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

assert_dispatch() {
  local target="$1" profile="$2" expected="$3"
  local output
  output="$(CAPACITY_DRY_RUN=1 "${dispatcher}" "${target}" "${profile}" alpha)"
  [[ "${output}" == *"target=${target} profile=${profile} mode=alpha"* ]]
  [[ "${output}" == *"MODE=alpha"* ]]
  [[ "${output}" == *"${expected}"* ]]
}

assert_dispatch api readiness '_capacity-k6'
for workload in file combined; do
  client_output="$(REPOSITORY_CAPACITY_CLIENT_TIMING=1 REPOSITORY_CAPACITY_WORKLOAD="${workload}" \
    CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle diagnostic)"
  [[ "${client_output}" == *"REPOSITORY_CAPACITY_CLIENT_TIMING=1"* ]]
done
for value in 1 true 2; do
  for arguments in 'repository lifecycle diagnostic' 'repository lifecycle alpha' \
    'repository lifecycle production' 'api readiness diagnostic'; do
    if REPOSITORY_CAPACITY_CLIENT_TIMING="${value}" CAPACITY_DRY_RUN=1 \
      "${dispatcher}" ${arguments} >/dev/null 2>&1; then
      echo "invalid client timing scope unexpectedly passed: ${value} ${arguments}" >&2
      exit 1
    fi
  done
done
if REPOSITORY_CAPACITY_CLIENT_TIMING=1 REPOSITORY_CAPACITY_WORKLOAD=repository CAPACITY_DRY_RUN=1 \
  "${dispatcher}" repository lifecycle diagnostic >/dev/null 2>&1; then
  echo "client timing unexpectedly accepted a Repository-only comparison" >&2
  exit 1
fi
for workload in repository file combined; do
  workload_output="$(REPOSITORY_CAPACITY_WORKLOAD="${workload}" CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle diagnostic)"
  [[ "${workload_output}" == *"REPOSITORY_CAPACITY_WORKLOAD=${workload}"* ]]
  for mode in alpha production; do
    if REPOSITORY_CAPACITY_WORKLOAD="${workload}" CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle "${mode}" >/dev/null 2>&1; then
      echo "partial workload comparison unexpectedly accepted ${mode} mode" >&2
      exit 1
    fi
  done
done
for arguments in 'api readiness diagnostic' 'repository lifecycle diagnostic'; do
  if REPOSITORY_CAPACITY_WORKLOAD=invalid CAPACITY_DRY_RUN=1 "${dispatcher}" ${arguments} >/dev/null 2>&1; then
    echo "unknown workload selector unexpectedly passed" >&2
    exit 1
  fi
done
if REPOSITORY_CAPACITY_WORKLOAD=file REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 \
  "${dispatcher}" repository lifecycle diagnostic >/dev/null 2>&1; then
  echo "partial comparison unexpectedly substituted for complete secret scenario" >&2
  exit 1
fi
if REPOSITORY_CAPACITY_WORKLOAD=file CAPACITY_DRY_RUN=1 "${dispatcher}" namespace watch diagnostic >/dev/null 2>&1; then
  echo "workload comparison unexpectedly accepted a different profile" >&2
  exit 1
fi
authz_output="$(CAPACITY_DRY_RUN=1 "${dispatcher}" api authorization diagnostic)"
[[ "$authz_output" == *"_capacity-authorization"* ]]
assert_dispatch api authorization '_capacity-authorization'
CAPACITY_DRY_RUN=1 "${dispatcher}" api authorization production >/dev/null
authorization_dir="${test_dir}/authorization"
mkdir -p "${authorization_dir}"
authorization_digest="$(printf 'a%.0s' {1..64})"
alternate_digest="$(printf 'b%.0s' {1..64})"
for concurrency in 1 8 32; do
  jq -n --arg digest "${authorization_digest}" --argjson concurrency "${concurrency}" '
    {schemaVersion:1,concurrency:$concurrency,policyDigest:$digest,
     runtime:"go1.26.0",os:"linux",architecture:"arm64",logicalCPUs:8,gomaxprocs:8,
     policyDimensions:{roles:200,bindings:1000,rules:2000,groups:50},
     cases:["early-allow","last-allow","no-binding","overlapping-deny","owner-transfer","missing-attribute"],
     sampleProbability:"1/64",invalidOutcomes:0,warmupSeconds:60,measurementSeconds:600,
     histograms:[range(6) | {samples:1000,calls:64000,maxNanoseconds:101000,
       bucketsUpperMicroseconds:[range(2001) | if . == 100 then 1000 else 0 end]}]}
  ' >"${authorization_dir}/authorization-latency-${concurrency}.json"
done
jq -n --arg old "${authorization_digest}" --arg new "${alternate_digest}" '
  {schemaVersion:1,concurrency:32,durationSeconds:1800,decisions:100000,reloads:60,
   rejectedReloads:180,invalidOutcomes:0,beforeHeapBytes:10000000,afterHeapBytes:11000000,
   oldDigest:$old,newDigest:$new}
' >"${authorization_dir}/authorization-reload.json"
jq -n '
  {schemaVersion:1,passed:true,root:"Authorize",allowedClockRead:"time.Now",
   forbiddenDependencies:["policy-file","datastore-client","network-client"],
   functions:["Authorize"],calls:["time.Now"]}
' >"${authorization_dir}/authorization-evaluator-audit.json"
jq -n '{schemaVersion:1,runtime:"go1.26.0",architecture:"arm64",host:{logicalCPUs:8}}' \
  >"${authorization_dir}/environment.json"
for mode in diagnostic alpha production; do
  "${repo_root}/scripts/validate-capacity-evidence.sh" "${authorization_dir}" api authorization "${mode}" postflight
  jq -e '.passed and .scope == "evaluator-only"' "${authorization_dir}/postflight-environment.json" >/dev/null
done
reject_authorization_artifact() {
  local artifact="$1" mutation="$2"
  cp "${authorization_dir}/${artifact}" "${test_dir}/original-authorization.json"
  jq "${mutation}" "${test_dir}/original-authorization.json" >"${authorization_dir}/${artifact}"
  if "${repo_root}/scripts/validate-capacity-evidence.sh" "${authorization_dir}" api authorization alpha postflight >/dev/null 2>&1; then
    echo "authorization evidence accepted invalid ${artifact}: ${mutation}" >&2
    exit 1
  fi
  cp "${test_dir}/original-authorization.json" "${authorization_dir}/${artifact}"
}
reject_authorization_artifact authorization-latency-1.json '.warmupSeconds = 59'
reject_authorization_artifact authorization-latency-8.json '.measurementSeconds = 599'
reject_authorization_artifact authorization-latency-32.json '.histograms[0].samples = 0'
reject_authorization_artifact authorization-latency-32.json '.histograms[0].calls = 999'
reject_authorization_artifact authorization-latency-32.json '.histograms[0].bucketsUpperMicroseconds[100] = 0 | .histograms[0].bucketsUpperMicroseconds[999] = 1000'
reject_authorization_artifact authorization-latency-8.json '.policyDigest = ("b" * 64)'
reject_authorization_artifact authorization-latency-8.json '.cases[0] = "unspecified-case"'
reject_authorization_artifact authorization-latency-8.json '.logicalCPUs = 7'
reject_authorization_artifact authorization-reload.json '.durationSeconds = 1799'
reject_authorization_artifact authorization-reload.json '.afterHeapBytes = 11000001'
reject_authorization_artifact authorization-reload.json '.invalidOutcomes = 1'
reject_authorization_artifact authorization-reload.json '.rejectedReloads = 179'
reject_authorization_artifact authorization-evaluator-audit.json '.passed = false'
reject_authorization_artifact authorization-evaluator-audit.json '.functions += ["loadPolicy"]'
mv "${authorization_dir}/authorization-reload.json" "${test_dir}/saved-reload.json"
ln -s "${test_dir}/saved-reload.json" "${authorization_dir}/authorization-reload.json"
if "${repo_root}/scripts/validate-capacity-evidence.sh" "${authorization_dir}" api authorization diagnostic postflight >/dev/null 2>&1; then
  echo "authorization evidence accepted a symlink artifact" >&2
  exit 1
fi
rm "${authorization_dir}/authorization-reload.json"
mv "${test_dir}/saved-reload.json" "${authorization_dir}/authorization-reload.json"
assert_dispatch namespace admission 'CAPACITY_PROFILE=namespace-admission'
assert_dispatch namespace validation '_capacity-namespace-admission'
assert_dispatch namespace watch '_capacity-namespace-watch'
assert_dispatch namespace recovery '_capacity-namespace-recovery'
assert_dispatch repository lifecycle '_capacity-repository-lifecycle'
assert_dispatch product lifecycle 'CAPACITY_PROFILE=product-lifecycle'
assert_dispatch category hierarchy 'CAPACITY_PROFILE=category-hierarchy'
assert_dispatch scylla soak '_capacity-scylla-soak'

secret_output="$(REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle production)"
[[ "${secret_output}" == *"_capacity-repository-lifecycle"* ]]
[[ "${secret_output}" == *"REPOSITORY_CAPACITY_SECRET_SCENARIO=1"* ]]
normal_output="$(REPOSITORY_CAPACITY_SECRET_SCENARIO=0 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle production)"
[[ "${normal_output}" != *"REPOSITORY_CAPACITY_SECRET_SCENARIO=1"* ]]
for target_profile in 'namespace watch' 'product lifecycle' 'api readiness' 'scylla soak'; do
  read -r other_target other_profile <<<"${target_profile}"
  if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "${dispatcher}" "${other_target}" "${other_profile}" production >/dev/null 2>&1; then
    echo "secret scenario flag accepted on an unrelated capacity profile" >&2
    exit 1
  fi
done

if CAPACITY_DRY_RUN=1 "${dispatcher}" namespace unknown alpha >/dev/null 2>&1; then
  echo "invalid target/profile combination unexpectedly succeeded" >&2
  exit 1
fi
if CAPACITY_DRY_RUN=1 "${dispatcher}" namespace watch gate >/dev/null 2>&1; then
  echo "legacy gate mode unexpectedly succeeded" >&2
  exit 1
fi

mkdir -p "${test_dir}/bin" "${test_dir}/readiness-valid" "${test_dir}/readiness-missing"
source_revision="$(git -C "${repo_root}" rev-parse HEAD)"
image_digest="$(printf 'a%.0s' {1..64})"
cat >"${test_dir}/bin/curl" <<'SH'
#!/usr/bin/env bash
if [[ "${MOCK_BAD_READY:-0}" == 1 && "$*" == *api-b.internal/ready* ]]; then exit 22; fi
if [[ "$*" == */ready* ]]; then
  if [[ "${MOCK_MISSING_AUTH:-0}" == 1 ]]; then printf '{}'; exit 0; fi
  digest="$(printf 'a%.0s' {1..64})"
  if [[ "${MOCK_AUTH_DIVERGENCE:-0}" == 1 && "$*" == *api-b* ]]; then digest="$(printf 'b%.0s' {1..64})"; fi
  jq -n --arg digest "$digest" '{auth:{authn:["static-users","anonymous"],authz:"rbac-local",userdir:"none",catalogDigest:("a"*64),
    policy:{loaded:true,apiVersion:"rbac.authorization.gitstore.dev/v1beta1",digest:$digest,roles:1,bindings:1,rules:1}}}'
  exit 0
fi
case "$*" in
  *api-a*|*api-alias*)
    if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-a-replacement; else instance=instance-a; fi
    printf 'process_start_time_seconds 100\ngitstore_api_process_instance_info{instance_id="%s"} 1\n' "$instance"
    ;;
  *api-b*)
    if [[ "${MOCK_SAME_START:-0}" == 1 ]]; then start=100; else start=200; fi
    if [[ "${MOCK_REPLACE_ALL:-0}" == 1 && -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-b-replacement; else instance=instance-b; fi
    printf 'process_start_time_seconds %s\ngitstore_api_process_instance_info{instance_id="%s"} 1\n' "$start" "$instance"
    ;;
  *) exit 1 ;;
esac
SH
chmod +x "${test_dir}/bin/curl"
jq -n --arg revision "${source_revision}" --arg digest "${image_digest}" '[
  {Id:"api-id-1",Name:"/api-1",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"api-id-2",Name:"/api-2",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}}
]' >"${test_dir}/readiness-containers.json"
jq '.[0].Config.Image = "ghcr.io/gitstore-dev/api:latest"' "${test_dir}/readiness-containers.json" >"${test_dir}/unverified-readiness-containers.json"
printf '#!/usr/bin/env bash\nif [[ "$1" == exec ]]; then case "$2" in api-1) if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then instance=instance-a-replacement; else instance=instance-a; fi; printf "process_start_time_seconds 100\\ngitstore_api_process_instance_info{instance_id=\\"%%s\\"} 1\\n" "$instance" ;; api-2) if [[ "${MOCK_SAME_START:-0}" == 1 ]]; then start=100; else start=200; fi; printf "process_start_time_seconds %%s\\ngitstore_api_process_instance_info{instance_id=\\"instance-b\\"} 1\\n" "$start" ;; *) exit 1 ;; esac; else cat "${MOCK_READINESS_CONTAINERS_FILE}"; fi\n' >"${test_dir}/bin/docker"
chmod +x "${test_dir}/bin/docker"
export MOCK_READINESS_CONTAINERS_FILE="${test_dir}/readiness-containers.json"
cp "${repo_root}/tests/capacity/examples/config-manifest.json" "${test_dir}/readiness-valid/config.json"
cp "${repo_root}/tests/capacity/examples/environment-manifest.json" "${test_dir}/readiness-valid/environment.json"
PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 \
  CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid"
jq -e '.passed == true and .topology.apiReplicas == 2 and (.topology.liveReplicas | length) == 2 and .resources.runtimeMemoryBytes == 17179869184' \
  "${test_dir}/readiness-valid/preflight-environment.json" >/dev/null
if PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 \
  CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-missing" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly passed without deployment manifests" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-a.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 \
  CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted one live process twice" >&2
  exit 1
fi
MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid"
if PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://unverified.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted an unrelated workload URL" >&2
  exit 1
fi
if MOCK_BAD_READY=1 PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted an unready secondary replica" >&2
  exit 1
fi
if MOCK_READINESS_CONTAINERS_FILE="${test_dir}/unverified-readiness-containers.json" \
  PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
  echo "production API readiness unexpectedly accepted a mutable live image" >&2
  exit 1
fi

jq 'map(.Path = "/bin/sh" | .Args = ["-ec",
  "export GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY=\"$(cat /run/secrets/serviceaccount-signing-key)\"\nexec /app/api --config-file /etc/gitstore/gitstore.toml\n"])' \
  "${test_dir}/readiness-containers.json" >"${test_dir}/wrapped-readiness-containers.json"
MOCK_READINESS_CONTAINERS_FILE="${test_dir}/wrapped-readiness-containers.json" \
  PATH="${test_dir}/bin:${PATH}" MODE=alpha CAPACITY_BASE_URL=http://api-a.internal \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
  "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid"
jq -e 'all(.artifacts.apiContainers[]; .approvedExecutable == true and .argumentCount == 2 and (has("arguments") | not))' \
  "${test_dir}/readiness-valid/preflight-environment.json" >/dev/null
for mutation in '.[0].Path = "/bin/bash"' '.[0].Args[0] = "-c"' \
  '.[0].Args[1] = "exec /app/api"' '.[0].Args += ["unexpected"]'; do
  jq "${mutation}" "${test_dir}/wrapped-readiness-containers.json" >"${test_dir}/invalid-wrapped-readiness-containers.json"
  if MOCK_READINESS_CONTAINERS_FILE="${test_dir}/invalid-wrapped-readiness-containers.json" \
    PATH="${test_dir}/bin:${PATH}" MODE=alpha CAPACITY_BASE_URL=http://api-a.internal \
    CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
    CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
    "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
    echo "API readiness unexpectedly accepted an unapproved startup wrapper: ${mutation}" >&2
    exit 1
  fi
done

make_output="$(make --no-print-directory -C "${repo_root}" capacity TARGET=namespace PROFILE=watch MODE=alpha CAPACITY_DRY_RUN=1)"
[[ "${make_output}" == *"target=namespace profile=watch mode=alpha"* ]]

for failure in MOCK_MISSING_AUTH MOCK_AUTH_DIVERGENCE; do
  if env "${failure}=1" PATH="${test_dir}/bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
    CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
    CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
    "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/readiness-valid" >/dev/null 2>&1; then
    echo "API readiness unexpectedly accepted ${failure}" >&2
    exit 1
  fi
done
readiness_semantic_run=readiness-semantics
jq '.mode = "alpha"' "${test_dir}/readiness-valid/preflight-environment.json" >"${test_dir}/readiness-valid/preflight-environment.json.tmp"
mv "${test_dir}/readiness-valid/preflight-environment.json.tmp" "${test_dir}/readiness-valid/preflight-environment.json"
jq --arg run "${readiness_semantic_run}" '
  {schemaVersion:1,component:"api-readiness-authorization",runID:$run,mode:"alpha",passed:true,
   rate:10,duration:"10m",preAllocatedVUs:10,maxVUs:100,measurementSeconds:600.1,
   deniedIdentity:"anonymous",denialCode:"FORBIDDEN",mismatches:0,droppedIterations:0,
   replicas:[.topology.liveReplicas[] | {endpoint,auth,attempts:6000,allowed:6000,denied:6000,
     readiness:{p95Milliseconds:100,p99Milliseconds:200,httpFailureRate:0}}]}
' "${test_dir}/readiness-valid/preflight-environment.json" >"${test_dir}/readiness-valid/authorization-probes.json"
CAPACITY_RUN_ID="${readiness_semantic_run}" "${repo_root}/scripts/validate-capacity-evidence.sh" \
  "${test_dir}/readiness-valid" api readiness alpha postflight
cp "${test_dir}/readiness-valid/authorization-probes.json" "${test_dir}/original-readiness-probes.json"
for mutation in \
  '.passed = false' '.mismatches = 1' '.droppedIterations = 1' '.measurementSeconds = 599.9' '.rate = 9' '.maxVUs = 101' \
  '.replicas[0].readiness.p95Milliseconds = 1000' '.replicas[1].readiness.p99Milliseconds = 3000' \
  '.replicas[1].readiness.httpFailureRate = 0.001' \
  '.runID = "other-run"' '.denialCode = "NOT_FOUND"' '.replicas[0].allowed = 5999' \
  'del(.replicas[1].denied)' '.replicas = [.replicas[0]]' \
  '.replicas[1].endpoint = .replicas[0].endpoint' \
  '.replicas[0].auth.policy.loaded = false' '.replicas[0].auth.policy.roles = 201' \
  '.replicas[].auth.policy.apiVersion = "v1"' \
  '.replicas[0].auth.catalogDigest = ("b"*64)' \
  '.replicas[].auth.policy.digest = ("b"*64)' \
  '.replicas[0].token = "forbidden-material"' '.token = "forbidden-material"'; do
  jq "${mutation}" "${test_dir}/original-readiness-probes.json" >"${test_dir}/readiness-valid/authorization-probes.json"
  if CAPACITY_RUN_ID="${readiness_semantic_run}" "${repo_root}/scripts/validate-capacity-evidence.sh" \
    "${test_dir}/readiness-valid" api readiness alpha postflight >/dev/null 2>&1; then
    echo "API readiness accepted invalid semantic evidence: ${mutation}" >&2
    exit 1
  fi
done
cp "${test_dir}/original-readiness-probes.json" "${test_dir}/readiness-valid/authorization-probes.json"
mv "${test_dir}/readiness-valid/authorization-probes.json" "${test_dir}/saved-readiness-probes.json"
ln -s "${test_dir}/saved-readiness-probes.json" "${test_dir}/readiness-valid/authorization-probes.json"
if CAPACITY_RUN_ID="${readiness_semantic_run}" "${repo_root}/scripts/validate-capacity-evidence.sh" \
  "${test_dir}/readiness-valid" api readiness alpha postflight >/dev/null 2>&1; then
  echo "API readiness accepted a symlink semantic artifact" >&2
  exit 1
fi
rm "${test_dir}/readiness-valid/authorization-probes.json"
mv "${test_dir}/saved-readiness-probes.json" "${test_dir}/readiness-valid/authorization-probes.json"

lifecycle_semantic_dir="${test_dir}/lifecycle-semantics"
mkdir -p "${lifecycle_semantic_dir}"
jq '.mode = "diagnostic" | .topology.liveApiReplicas = .topology.liveReplicas' \
  "${test_dir}/readiness-valid/preflight-environment.json" >"${lifecycle_semantic_dir}/preflight-environment.json"
jq '
  def replicas: [.replicas[] | {endpoint,auth,allowed:true,denied:true,denialCode:"FORBIDDEN"}];
  {schemaVersion:1,component:"repository-lifecycle-authorization",runID:"lifecycle-semantics",
   mode:"diagnostic",passed:true,before:replicas,after:replicas}
' "${test_dir}/original-readiness-probes.json" >"${lifecycle_semantic_dir}/authorization-probes.json"
if CAPACITY_RUN_ID=lifecycle-semantics "${repo_root}/scripts/validate-capacity-evidence.sh" \
  "${lifecycle_semantic_dir}" repository lifecycle diagnostic postflight >/dev/null 2>&1; then
  echo "Repository lifecycle accepted before/after-only evidence without acknowledged-load coverage" >&2
  exit 1
fi
jq '.schemaVersion = 2 | .underLoad = {
  cycles:1,allowed:[1,1],denied:[1,1],firstStartedMS:100010,lastCompletedMS:100100,
  loadStartMS:100000,loadEndMS:700000,acknowledgedBefore:1,acknowledgedAfter:2,
  minimumAcknowledgedAdvance:1,unavailableAttempts:0
}' "${lifecycle_semantic_dir}/authorization-probes.json" >"${lifecycle_semantic_dir}/authorization.next.json"
mv "${lifecycle_semantic_dir}/authorization.next.json" "${lifecycle_semantic_dir}/authorization-probes.json"
CAPACITY_RUN_ID=lifecycle-semantics "${repo_root}/scripts/validate-capacity-evidence.sh" \
  "${lifecycle_semantic_dir}" repository lifecycle diagnostic postflight
cp "${lifecycle_semantic_dir}/authorization-probes.json" "${test_dir}/original-lifecycle-probes.json"
for mutation in \
  '.passed = false' '.runID = "another-run"' '.mode = "alpha"' \
  '.before = []' '.after = null' '.after[1].denied = false' '.after[0].allowed = false' \
  '.after[0].denialCode = "NOT_FOUND"' '.after[0].auth.policy.digest = ("c"*64)' \
  '.before[].auth.policy.apiVersion = "v1" | .after[].auth.policy.apiVersion = "v1"' \
  '.before[].endpoint = "http://other-api.internal"' \
  '.after[1].endpoint = .after[0].endpoint' \
  '.before[0].auth.policy.loaded = false' '.after[0].token = "private-token-marker"' \
  '.schemaVersion = 1' 'del(.underLoad)' '.underLoad = null' \
  '.underLoad.cycles = 0' '.underLoad.cycles = 0.5' '.underLoad.allowed[0] = 0' \
  '.underLoad.denied[1] = 0' '.underLoad.minimumAcknowledgedAdvance = 0' \
  '.underLoad.acknowledgedAfter = .underLoad.acknowledgedBefore' \
  '.underLoad.firstStartedMS = (.underLoad.loadStartMS - 1)' \
  '.underLoad.lastCompletedMS = .underLoad.loadEndMS' \
  '.underLoad.lastCompletedMS = .underLoad.firstStartedMS' \
  '.underLoad.unavailableAttempts = -1' '.underLoad.cycles = 9007199254740992' \
  '.underLoad.minimumAcknowledgedAdvance = 2' '.underLoad.token = "private-token-marker"' \
  '.token = "private-token-marker"'; do
  jq "${mutation}" "${test_dir}/original-lifecycle-probes.json" >"${lifecycle_semantic_dir}/authorization-probes.json"
  if CAPACITY_RUN_ID=lifecycle-semantics "${repo_root}/scripts/validate-capacity-evidence.sh" \
    "${lifecycle_semantic_dir}" repository lifecycle diagnostic postflight >/dev/null 2>&1; then
    echo "Repository lifecycle accepted invalid authorization evidence: ${mutation}" >&2
    exit 1
  fi
done
cp "${test_dir}/original-lifecycle-probes.json" "${lifecycle_semantic_dir}/authorization-probes.json"
mv "${lifecycle_semantic_dir}/authorization-probes.json" "${test_dir}/saved-lifecycle-probes.json"
ln -s "${test_dir}/saved-lifecycle-probes.json" "${lifecycle_semantic_dir}/authorization-probes.json"
if CAPACITY_RUN_ID=lifecycle-semantics "${repo_root}/scripts/validate-capacity-evidence.sh" \
  "${lifecycle_semantic_dir}" repository lifecycle diagnostic postflight >/dev/null 2>&1; then
  echo "Repository lifecycle accepted symlink authorization evidence" >&2
  exit 1
fi
rm "${lifecycle_semantic_dir}/authorization-probes.json"
mv "${test_dir}/saved-lifecycle-probes.json" "${lifecycle_semantic_dir}/authorization-probes.json"

for removed_target in capacity-observability capacity-observability-down test-scylla-capacity test-namespace-admission-capacity test-namespace-watch-capacity test-namespace-watch-recovery; do
  if rg -q "^${removed_target}:" "${repo_root}/Makefile"; then
    echo "removed public target ${removed_target} is still defined" >&2
    exit 1
  fi
done

mkdir -p "${test_dir}/evidence"
export MOCK_LIFECYCLE_AUTH_FILE="${test_dir}/original-readiness-probes.json"
cat >"${test_dir}/bin/make" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
echo "mock capacity command: $*"
if [[ -n "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE:-}" ]]; then
  cp "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE}" "${MOCK_SERVICE_CONTAINERS_FILE}"
fi
if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" ]]; then
  touch "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}"
fi
if [[ "$*" == *"_capacity-repository-lifecycle"* && "${MODE:-}" != diagnostic ]]; then
  jq --arg run "${CAPACITY_RUN_ID}" --slurpfile fixture "${MOCK_LIFECYCLE_AUTH_FILE}" '
    def replicas: [.topology.liveApiReplicas[] |
      {endpoint,auth:$fixture[0].replicas[0].auth,allowed:true,denied:true,denialCode:"FORBIDDEN"}];
    {schemaVersion:2,component:"repository-lifecycle-authorization",runID:$run,
     mode:.mode,passed:true,before:replicas,after:replicas,
     underLoad:{cycles:1,allowed:[1,1],denied:[1,1],firstStartedMS:100010,lastCompletedMS:100100,
       loadStartMS:100000,loadEndMS:700000,acknowledgedBefore:1,acknowledgedAfter:2,
       minimumAcknowledgedAdvance:1,unavailableAttempts:0}}
  ' "${CAPACITY_EVIDENCE_DIR}/preflight-environment.json" >"${CAPACITY_EVIDENCE_DIR}/authorization-probes.json"
fi
[[ "${FAIL_MAKE:-0}" != "1" ]]
EOF
cat >"${test_dir}/bin/docker" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == top ]]; then
  [[ "$*" == *"-eo pid,lstart,comm" ]] || exit 2
  printf 'PID STARTED COMMAND\n10 Tue Oct 20 22:37:19 2026 supervisord\n'
  case "${MOCK_SCYLLA_NATIVE_STATE:-}" in
    inspection-failed) exit 1 ;;
    missing) ;;
    multiple) printf '101 Tue Oct 20 22:37:20 2026 scylla\n102 Tue Oct 20 22:37:20 2026 scylla\n' ;;
    restarted) printf '201 Tue Oct 20 22:37:20 2026 scylla\n' ;;
    reused-pid) printf '101 Tue Oct 20 22:37:21 2026 scylla\n' ;;
    malformed) printf 'unknown Tue Oct 20 22:37:20 2026 scylla\n' ;;
    *) printf '101 Tue Oct 20 22:37:20 2026 scylla\n' ;;
  esac
  printf '103 Tue Oct 20 22:37:20 2026 scylla-housekee\n104 Tue Oct 20 22:37:20 2026 sleep\n'
  exit 0
fi
if [[ "$1" == exec ]]; then
  if [[ "${3:-}" == date ]]; then
    if [[ "${MOCK_CLOCK_INVALID:-0}" == 1 ]]; then echo unknown; exit 0; fi
    echo "$(( $(date -u +%s) + ${MOCK_CLOCK_OFFSET_SECONDS:-0} ))"
    exit 0
  fi
  case "$2" in
    api-1)
      if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then
        instance=instance-a-replacement
      else
        instance=instance-a
      fi
      printf 'process_start_time_seconds 100\ngitstore_api_process_instance_info{instance_id="%s"} 1\n' "$instance"
      ;;
    api-2)
      if [[ "${MOCK_SAME_START:-0}" == 1 ]]; then start=100; else start=200; fi
      if [[ "${MOCK_REPLACE_ALL:-0}" == 1 && -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" && -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then
        instance=instance-b-replacement
      else
        instance=instance-b
      fi
      printf 'process_start_time_seconds %s\ngitstore_api_process_instance_info{instance_id="%s"} 1\n' "$start" "$instance"
      ;;
    *)
      if [[ "${MOCK_SCYLLA_BAD_MEMBERSHIP:-0}" == 1 ]]; then
        printf 'UN node-1\n'
      else
        printf 'UN node-1\nUN node-2\nUN node-3\n'
      fi
      ;;
  esac
  exit 0
fi
if [[ "$*" == *api-1* ]]; then
  cat "${MOCK_SERVICE_CONTAINERS_FILE}"
else
  cat "${MOCK_SCYLLA_CONTAINERS_FILE}"
fi
EOF
printf '#!/usr/bin/env bash\ncase "$1" in rev-parse) printf "%%s\\n" "${MOCK_SOURCE_REVISION}" ;; status|diff|ls-files) exit 0 ;; *) exit 1 ;; esac\n' >"${test_dir}/bin/git"
chmod +x "${test_dir}/bin/make"
chmod +x "${test_dir}/bin/docker"
chmod +x "${test_dir}/bin/git"
export MOCK_SOURCE_REVISION="${source_revision}"
jq -n --arg revision "${source_revision}" --arg digest "${image_digest}" '[
  {Id:"api-id-1",Name:"/api-1",Image:("sha256:"+$digest),Path:"/bin/sh",Args:["-ec","export GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY=\"$(cat /run/secrets/serviceaccount-signing-key)\"; exec /app/api --config-file /etc/gitstore/gitstore.toml"],Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"api-id-2",Name:"/api-2",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"git-id-1",Name:"/git-1",Image:("sha256:"+$digest),Path:"/app/git-service",Config:{Image:("ghcr.io/gitstore-dev/git-service@sha256:"+$digest),Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}}
]' >"${test_dir}/service-containers.json"
jq 'map(.Config.Image |= sub("@sha256:[0-9a-f]+$"; ":local-alpha"))' "${test_dir}/service-containers.json" >"${test_dir}/local-service-containers.json"
jq -n --arg revision "${source_revision}" --arg digest "${image_digest}" '[
  {Id:"api-id-1",Name:"/api-1",Image:("sha256:"+$digest),Path:"/bin/sh",Args:["-ec","export GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY=\"$(cat /run/secrets/serviceaccount-signing-key)\"; exec /app/api --config-file /etc/gitstore/gitstore.toml"],Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Env:["GITSTORE_API__GIT_SERVICE__URI=dns:///git-1:50051"],Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"api-id-2",Name:"/api-2",Image:("sha256:"+$digest),Path:"/app/api",Config:{Image:("ghcr.io/gitstore-dev/api@sha256:"+$digest),Env:["GITSTORE_API__GIT_SERVICE__URI=dns:///git-1:50051"],Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}},
  {Id:"git-id-1",Name:"/git-1",Image:("sha256:"+$digest),Path:"/app/git-service",Config:{Image:("ghcr.io/gitstore-dev/git-service@sha256:"+$digest),Env:["GITSTORE_GIT_SERVICE__CATALOG__URI=dns:///api-a:6000"],Labels:{"org.opencontainers.image.revision":$revision}},State:{Running:true}}
]' >"${test_dir}/repository-service-containers.json"
jq 'map(.Config.Image |= sub("@sha256:[0-9a-f]+$"; ":local-alpha"))' "${test_dir}/repository-service-containers.json" >"${test_dir}/repository-local-service-containers.json"
jq '.[0].Config.Labels["org.opencontainers.image.revision"] = "wrong-revision"' "${test_dir}/service-containers.json" >"${test_dir}/unverified-service-containers.json"
jq '.[1].Id = .[0].Id' "${test_dir}/service-containers.json" >"${test_dir}/duplicate-service-containers.json"
jq -n '[range(1;4) as $i | {Id:("scylla-id-"+($i|tostring)),Name:("/scylla-"+($i|tostring)),Config:{Image:"scylla",Cmd:["--smp=2"]},RestartCount:0,HostConfig:{Memory:3221225472,NanoCpus:0,CpusetCpus:""},State:{Running:true,OOMKilled:false,StartedAt:"start"}}]' >"${test_dir}/scylla-containers.json"
jq 'map(.Config.Cmd = ["--smp=1"] | .HostConfig.Memory = 1610612736)' "${test_dir}/scylla-containers.json" >"${test_dir}/alpha-scylla-containers.json"
jq '.services.scylla.smpPerNode = 1 | .services.gitService.replicas = 1' "${repo_root}/tests/capacity/examples/config-manifest.json" >"${test_dir}/alpha-config-manifest.json"
jq '.datastore.memoryBytesPerNode = 1610612736 | .topology.gitServiceReplicas = 1' "${repo_root}/tests/capacity/examples/environment-manifest.json" >"${test_dir}/alpha-environment-manifest.json"
jq '[.[0], .[0], .[0]]' "${test_dir}/scylla-containers.json" >"${test_dir}/duplicate-scylla-containers.json"
export MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/scylla-containers.json"
export MOCK_REPLACEMENT_MARKER_PREFIX="${test_dir}/replacement"
export NAMESPACE_WATCH_API_REPLACEMENT=http://api-a.internal
export NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE="${test_dir}/replacement-trigger"
PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=test-run \
  "${dispatcher}" namespace validation diagnostic >/dev/null
metadata="${test_dir}/evidence/namespace/validation/diagnostic/test-run/metadata.json"
jq -e '.target == "namespace" and .profile == "validation" and .mode == "diagnostic" and .passed == false and .verifierExitCode == 0' "${metadata}" >/dev/null
grep -Fq "CAPACITY_EVIDENCE_DIR=${test_dir}/evidence/namespace/validation/diagnostic/test-run" \
  "${test_dir}/evidence/namespace/validation/diagnostic/test-run/verifier.log"

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=missing-manifests \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "alpha non-k6 capacity evidence unexpectedly passed without manifests" >&2
  exit 1
fi

PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=validated-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  "${dispatcher}" namespace validation alpha >/dev/null
metadata="${test_dir}/evidence/namespace/validation/alpha/validated-run/metadata.json"
jq -e '
  .passed == true and .preflightRequired == true and .preflightExitCode == 0 and
  .verifierRequired == true and .verifierExitCode == 0 and
  (.configDigest | length) == 64 and (.environmentDigest | length) == 64
' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_TEST_FORCE_DIRTY=1 \
  CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=dirty-verifier \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "alpha capacity evidence unexpectedly accepted a dirty verifier checkout" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/validation/alpha/dirty-verifier/metadata.json"
jq -e '.passed == false and .worktreeDirty == true and .sourceStateExitCode == 2' "${metadata}" >/dev/null

if MODE=alpha CAPACITY_TEST_FORCE_DIRTY=1 CAPACITY_TARGET=api CAPACITY_SCENARIO=readiness \
  CAPACITY_RUN_ID=dirty-k6-verifier CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" \
  "${repo_root}/scripts/run-capacity.sh" api-readiness >/dev/null 2>&1; then
  echo "alpha k6 capacity evidence unexpectedly accepted a dirty verifier checkout" >&2
  exit 1
fi
metadata="${test_dir}/evidence/api/readiness/alpha/dirty-k6-verifier/metadata.json"
jq -e '.passed == false and .gitDirty == true and .sourceStateExitCode == 2' "${metadata}" >/dev/null

mkdir -p "${test_dir}/chaos-bin"
cat >"${test_dir}/chaos-bin/k6" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == *"--log-format json"* ]]
while [[ $# -gt 0 ]]; do
  if [[ "$1" == --summary-export ]]; then summary="$2"; shift; fi
  shift
done
sleep 0.2
[[ ! -f "${MOCK_CHAOS_INJECTED}" ]] || { echo "fault preceded setup completion" >&2; exit 2; }
start="$(jq -n 'now * 1000 | floor')"
case "${MOCK_CHAOS_WINDOW:-valid}" in
  valid|outside|overrun|recovery-overrun)
    jq -ncr --arg run "${CAPACITY_RUN_ID}" --argjson start "${start}" '
      {level:"info",msg:("CAPACITY_LOAD_WINDOW " +
        ({schemaVersion:1,runId:$run,startMs:$start,
          endMs:($start+(if env.MOCK_CHAOS_WINDOW == "overrun" or env.MOCK_CHAOS_WINDOW == "recovery-overrun" then 2000 else 10000 end))} | tojson))} |
      if env.MOCK_CHAOS_GO == "1" then .msg else tojson end
    '
    ;;
  wrong-run)
    jq -ncr --argjson start "${start}" '
      {level:"info",msg:("CAPACITY_LOAD_WINDOW " +
        ({schemaVersion:1,runId:"another-run",startMs:$start,endMs:($start+10000)} | tojson))} |
      if env.MOCK_CHAOS_GO == "1" then .msg else tojson end
    '
    ;;
  malformed)
    if [[ "${MOCK_CHAOS_GO:-0}" == 1 ]]; then printf '%s\n' 'CAPACITY_LOAD_WINDOW invalid'
    else printf '%s\n' '{"level":"info","msg":"CAPACITY_LOAD_WINDOW invalid"}'; fi
    ;;
  missing) ;;
esac
if [[ ("${MOCK_CHAOS_WINDOW:-valid}" == valid || "${MOCK_CHAOS_WINDOW:-}" == overrun ||
       "${MOCK_CHAOS_WINDOW:-}" == recovery-overrun) &&
      "${CAPACITY_CHAOS_DELAY:-}" != 5s ]]; then
  deadline=$((SECONDS + 5))
  until [[ -f "${summary%/*}/chaos-injection-started" ]]; do
    (( SECONDS < deadline )) || { echo "mock load never observed fault startup" >&2; exit 2; }
    sleep 0.05
  done
fi
sleep 0.5
printf '%s\n' '{"metrics":{}}' >"${summary}"
SH
cat >"${test_dir}/chaos-bin/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  inspect)
    started=before
    if [[ -f "${MOCK_CHAOS_INJECTED}" && "${MOCK_CHAOS_RECOVERY:-}" != no-restart &&
          "${MOCK_CHAOS_ACTION:-restart}" == restart ]]; then started=after; fi
    jq -n --arg project "${MOCK_CHAOS_PROJECT:-owned-project}" --arg started "${started}" '
      [{Id:"owned-api",Name:"/owned-api",Image:"fixture-image",Config:{Image:"fixture",
        Labels:{"com.docker.compose.project":$project}},HostConfig:{Memory:1024},
        State:{Running:true,Paused:false,OOMKilled:false,Restarting:false,Dead:false,Pid:123,StartedAt:$started}}]' |
      jq 'if (env.MOCK_POLICY_DIR // "") != "" then
        .[0].Config.Env = ["GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE=/run/owned-policy/policy.yaml"] |
        .[0].Mounts = [{Type:"bind",Source:env.MOCK_POLICY_DIR,Destination:"/run/owned-policy",RW:false}]
      else . end |
      if env.MOCK_CHAOS_RECOVERY == "writable-policy-mount" then .[0].Mounts[0].RW = true
      elif env.MOCK_CHAOS_RECOVERY == "wrong-policy-mount" then .[0].Mounts[0].Source = "/unrelated"
      elif env.MOCK_CHAOS_RECOVERY == "wrong-policy-env" then
        .[0].Config.Env = ["GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE=/etc/gitstore/policy.yaml"]
      else . end |
      if env.MOCK_CHAOS_RECOVERY == "changed-container" and
        (.[0].State.StartedAt == "after") then .[0].Id = "replacement-container"
      elif env.MOCK_CHAOS_RECOVERY == "changed-allocation" and
        (.[0].State.StartedAt == "after") then .[0].HostConfig.Memory = 2048
      else . end'
    ;;
  exec)
    if [[ "$*" == *"sha256sum /run/owned-policy/policy.yaml"* ]]; then
      if [[ "${MOCK_CHAOS_RECOVERY:-}" == interrupted ]] &&
        grep -q '^apiVersion: \[' "${MOCK_POLICY_DIR}/policy.yaml"; then
        kill -TERM "${PPID}"
      fi
      shasum -a 256 "${MOCK_POLICY_DIR}/policy.yaml"
      exit 0
    fi
    if [[ "$*" == *"sha256sum /proc/1/exe"* ]]; then
      hash="$(printf '%064d' 1)"
      if [[ -f "${MOCK_CHAOS_INJECTED}" && "${MOCK_CHAOS_RECOVERY:-}" == changed-executable ]]; then
        hash="$(printf '%064d' 2)"
      fi
      printf '%s  /proc/1/exe\n' "${hash}"
      exit 0
    fi
    if [[ "$*" == *"wget -q -T 1 -O - http://127.0.0.1:5001/health"* ]]; then
      jq -n '{status:"ok",ready:true,credentialReady:true,
        kinds:{Namespace:{registered:true,stalled:false,recovering:false,poisonItems:0,
          activeWorkers:1,queueDepth:2}}}' |
        jq 'if env.MOCK_CHAOS_RECOVERY == "controller-poisoned" then
          .kinds.Namespace.poisonItems = 1 else . end'
      exit 0
    fi
    [[ "$*" == *"wget -q -T 1 -O - http://127.0.0.1:4000/ready"* ]] || exit 2
    if [[ -f "${MOCK_CHAOS_INJECTED}" && "${MOCK_CHAOS_RECOVERY:-}" == late-ready &&
          ! -f "${MOCK_CHAOS_INJECTED}.observed" ]]; then
      touch "${MOCK_CHAOS_INJECTED}.observed"
      sleep 3
    fi
    if [[ -f "${MOCK_CHAOS_INJECTED}" && "${MOCK_CHAOS_RECOVERY:-}" == one-retry &&
          ! -f "${MOCK_CHAOS_INJECTED}.observed" ]]; then
      touch "${MOCK_CHAOS_INJECTED}.observed"
      exit 1
    fi
    digest="$(printf '%064d' 1)"
    if [[ -f "${MOCK_CHAOS_INJECTED}" && "${MOCK_CHAOS_RECOVERY:-}" == changed-policy ]]; then
      digest="$(printf '%064d' 2)"
    fi
    if [[ "${MOCK_CHAOS_RECOVERY:-}" == accepted-invalid ]] &&
      grep -q '^apiVersion: \[' "${MOCK_POLICY_DIR}/policy.yaml"; then
      digest="$(printf '%064d' 2)"
    fi
    if [[ "${MOCK_CHAOS_RECOVERY:-}" == invalid-baseline ]]; then printf '{}'; exit 0; fi
    jq -n --arg digest "${digest}" '{status:"healthy",
      auth:{authn:["static-users","serviceaccount-assertion","serviceaccount-jwt","anonymous"],
        authz:"rbac-local",userdir:"static-users",catalogDigest:("a"*64),
        policy:{loaded:true,apiVersion:"rbac.authorization.gitstore.dev/v1beta1",
          digest:$digest,roles:1,bindings:1,rules:1,privateField:"must-not-be-retained"}}}' |
      jq 'if env.MOCK_CHAOS_RECOVERY == "pluggable" then
        .auth.authn = ["custom-provider"] | .auth.authz = "external-authorizer" | .auth.policy = null
      else . end'
    ;;
  run)
    printf '%s\n' injected >"${MOCK_CHAOS_INJECTED}"
    if [[ "${MOCK_CHAOS_WINDOW:-}" == overrun ]]; then sleep 3; fi
    if [[ "${MOCK_CHAOS_RECOVERY:-}" == deadline ]]; then sleep 31; fi
    ;;
  kill)
    printf '%s\n' injected >"${MOCK_CHAOS_INJECTED}"
    ;;
  logs)
    if grep -q '^apiVersion: \[' "${MOCK_POLICY_DIR}/policy.yaml"; then
      if [[ "${MOCK_CHAOS_RECOVERY:-}" == missing-rejection ]]; then
        printf '%s\n' '{"msg":"rbac-local policy reloaded"}'
      else
        printf '%s\n' '{"msg":"auth provider reload failed; previous configuration remains active"}'
      fi
    elif [[ "${MOCK_CHAOS_RECOVERY:-}" == missing-restoration ]]; then
      printf '%s\n' '{"msg":"rbac-local policy reloaded"}'
    else
      printf '%s\n' '{"msg":"auth providers reloaded atomically"}'
    fi
    ;;
  *) exit 2 ;;
esac
SH
cat >"${test_dir}/chaos-bin/make" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[[ "$*" == *"_capacity-repository-lifecycle"* ]]
[[ "$*" == *"CAPACITY_EVIDENCE_DIR=${CAPACITY_EVIDENCE_DIR}"* ]]
exec "${BASH_SOURCE[0]%/*}/k6" run --log-format json --summary-export "${CAPACITY_EVIDENCE_DIR}/summary.json"
SH
chmod +x "${test_dir}/chaos-bin/k6" "${test_dir}/chaos-bin/docker" "${test_dir}/chaos-bin/make"
run_window_contract() {
  local name="$1" window="$2" delay="$3" project="${4:-owned-project}" recovery="${5:-valid}"
  local profile="${6:-api-restart}" action="${7:-restart}"
  local runner="${8:-k6}" target=api scenario=readiness
  local command=("${repo_root}/scripts/run-capacity.sh" api-readiness)
  if [[ "${runner}" == go ]]; then
    target=repository
    scenario=lifecycle
    command=("${dispatcher}" repository lifecycle diagnostic)
  fi
  PATH="${test_dir}/chaos-bin:${PATH}" K6_BIN="${test_dir}/chaos-bin/k6" \
    CAPACITY_TOKEN= CAPACITY_TOKEN_FILE= CAPACITY_DENIED_TOKEN_FILE= CAPACITY_PROMETHEUS_URL= \
    CAPACITY_DATASTORE_CONTAINERS= CAPACITY_TARGET="${target}" CAPACITY_SCENARIO="${scenario}" MODE=diagnostic \
    COMPOSE_PROJECT_NAME=owned-project CAPACITY_CHAOS_PROFILE="${profile}" \
    CAPACITY_CHAOS_POLICY_DIR="${MOCK_POLICY_DIR:-}" \
    CAPACITY_CHAOS_TARGET=owned-api CAPACITY_CHAOS_CONFIRM=1 CAPACITY_CHAOS_DELAY="${delay}" \
    CAPACITY_EVIDENCE_DIR="${test_dir}/window-evidence" CAPACITY_RUN_ID="${name}" \
    MOCK_CHAOS_WINDOW="${window}" MOCK_CHAOS_PROJECT="${project}" \
    MOCK_CHAOS_RECOVERY="${recovery}" \
    MOCK_CHAOS_ACTION="${action}" \
    MOCK_CHAOS_GO="$(if [[ "${runner}" == go ]]; then printf 1; else printf 0; fi)" \
    MOCK_CHAOS_INJECTED="${test_dir}/injected-${name}" \
    "${command[@]}" >"${test_dir}/window-${name}.log" 2>&1
}
run_window_contract repository-valid valid 0s owned-project valid api-restart restart go
repository_evidence="${test_dir}/window-evidence/repository/lifecycle/diagnostic/repository-valid"
jq -e '.exitCode == 0 and .chaosExitCode == 0 and .passed == false and .sourceStateUnchanged' \
  "${repository_evidence}/metadata.json" >/dev/null
grep -q '^CAPACITY_LOAD_WINDOW ' "${repository_evidence}/verifier.log"
jq -e --slurpfile window "${repository_evidence}/chaos-load-window.json" '
  .injected and .recovery.passed and .recovery.budgetMilliseconds == 30000 and
  .startedAtMs >= $window[0].startMs and .completedAtMs < $window[0].endMs
' "${repository_evidence}/chaos/api-restart/repository-valid/metadata.json" >/dev/null
for window in missing malformed wrong-run pending overrun; do
  fixture="${window}"
  delay=0s
  if [[ "${window}" == pending ]]; then fixture=valid; delay=5s; fi
  if run_window_contract "repository-${window}" "${fixture}" "${delay}" owned-project valid api-restart restart go; then
    echo "Repository runner accepted invalid chaos window: ${window}" >&2
    exit 1
  fi
  jq -e '.passed == false and .exitCode != 0 and .chaosExitCode != 0' \
    "${test_dir}/window-evidence/repository/lifecycle/diagnostic/repository-${window}/metadata.json" >/dev/null
  if [[ "${window}" != overrun ]]; then
    [[ ! -f "${test_dir}/injected-repository-${window}" ]]
  fi
done
for runner in k6 go; do
  if run_window_contract "late-recovery-${runner}" recovery-overrun 0s owned-project late-ready api-restart restart "${runner}"; then
    echo "chaos accepted readiness recovery outside the declared load window" >&2
    exit 1
  fi
  case "${runner}" in k6) path=api/readiness ;; go) path=repository/lifecycle ;; esac
  late_evidence="${test_dir}/window-evidence/${path}/diagnostic/late-recovery-${runner}"
  jq -e '.exitCode != 0 and .chaosExitCode == 124 and .passed == false' \
    "${late_evidence}/metadata.json" >/dev/null
  jq -e --slurpfile window "${late_evidence}/chaos-load-window.json" '
    .injected and .exitCode == 0 and .recovery.passed and
    .completedAtMs < $window[0].endMs and .recovery.completedAtMs >= $window[0].endMs
  ' "${late_evidence}/chaos/api-restart/late-recovery-${runner}/metadata.json" >/dev/null
done
for target_profile in 'namespace watch' 'namespace validation' 'api authorization' 'product lifecycle' 'scylla soak'; do
  read -r other_target other_profile <<<"${target_profile}"
  if CAPACITY_CHAOS_PROFILE=api-restart CAPACITY_CHAOS_TARGET=owned-api CAPACITY_CHAOS_CONFIRM=1 \
    CAPACITY_DRY_RUN=1 "${dispatcher}" "${other_target}" "${other_profile}" diagnostic >/dev/null 2>&1; then
    echo "unsupported Go/smoke runner silently accepted integrated chaos" >&2
    exit 1
  fi
done
if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_CHAOS_PROFILE=api-restart CAPACITY_CHAOS_TARGET=owned-api \
  CAPACITY_CHAOS_CONFIRM=1 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle diagnostic >/dev/null 2>&1; then
  echo "external chaos overlapped the secret scenario's managed fault schedule" >&2
  exit 1
fi
run_window_contract valid valid 0.001m
window_evidence="${test_dir}/window-evidence/api/readiness/diagnostic/valid"
jq -e '.exitCode == 0 and .chaosExitCode == 0 and .passed == false and .sourceStateUnchanged' \
  "${window_evidence}/metadata.json" >/dev/null
jq -e --slurpfile window "${window_evidence}/chaos-load-window.json" '
  .injected and .recovery.required and .recovery.passed and .recovery.budgetMilliseconds == 30000 and
  .recovery.elapsedMilliseconds <= .recovery.budgetMilliseconds and .composeProject == "owned-project" and
  .startedAtMs >= $window[0].startMs and .completedAtMs < $window[0].endMs
' "${window_evidence}/chaos/api-restart/valid/metadata.json" >/dev/null
[[ ! -f "${window_evidence}/chaos/api-restart/valid/readiness-body.tmp" ]]
jq -e '.auth.policy | has("privateField") | not' \
  "${window_evidence}/chaos/api-restart/valid/readiness-before.json" >/dev/null
[[ -f "${test_dir}/injected-valid" ]]
metadata_digest="$(shasum -a 256 "${window_evidence}/chaos/api-restart/valid/metadata.json")"
if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_TARGET=owned-api \
  CHAOS_COMPOSE_PROJECT=owned-project CHAOS_RUN_ID=valid \
  CHAOS_EVIDENCE_DIR="${window_evidence}/chaos" MOCK_CHAOS_INJECTED="${test_dir}/duplicate-injected" \
  "${repo_root}/scripts/run-chaos.sh" api-restart >/dev/null 2>&1; then
  echo "chaos overwrote retained fault evidence" >&2
  exit 1
fi
[[ ! -f "${test_dir}/duplicate-injected" ]]
[[ "${metadata_digest}" == "$(shasum -a 256 "${window_evidence}/chaos/api-restart/valid/metadata.json")" ]]
run_window_contract pluggable valid 0s owned-project pluggable
jq -e '.recovery.passed' \
  "${test_dir}/window-evidence/api/readiness/diagnostic/pluggable/chaos/api-restart/pluggable/metadata.json" >/dev/null
run_window_contract retried valid 0s owned-project one-retry
jq -e '.recovery.passed and .recovery.attempts == 2' \
  "${test_dir}/window-evidence/api/readiness/diagnostic/retried/chaos/api-restart/retried/metadata.json" >/dev/null
run_window_contract controller valid 0s owned-project valid controller-restart
jq -e '.recovery.passed and .recovery.kind == "controller" and .recovery.budgetMilliseconds == 60000' \
  "${test_dir}/window-evidence/api/readiness/diagnostic/controller/chaos/controller-restart/controller/metadata.json" >/dev/null
run_window_contract paused valid 0s owned-project valid api-pause-5s pause
jq -e '.recovery.passed and .action == "pause"' \
  "${test_dir}/window-evidence/api/readiness/diagnostic/paused/chaos/api-pause-5s/paused/metadata.json" >/dev/null
mkdir -m 700 "${test_dir}/owned-policy"
policy_dir="$(cd "${test_dir}/owned-policy" && pwd -P)"
printf '%s\n' 'original fixture policy' >"${policy_dir}/policy.yaml"
chmod 600 "${policy_dir}/policy.yaml"
jq -n '{schemaVersion:1,composeProject:"owned-project",containerId:"owned-api",purpose:"policy-reload"}' \
  >"${policy_dir}/owned-policy.json"
MOCK_POLICY_DIR="${policy_dir}" run_window_contract policy-reload valid 0s owned-project valid api-invalid-policy-reload reload-invalid-policy
reload_metadata="${test_dir}/window-evidence/api/readiness/diagnostic/policy-reload/chaos/api-invalid-policy-reload/policy-reload/metadata.json"
jq -e '.injected and .recovery.passed and .policyReload.restored and .injector.name == "docker-signal"' \
  "${reload_metadata}" >/dev/null
for recovery in missing-rejection accepted-invalid missing-restoration interrupted pluggable invalid-baseline; do
  if MOCK_POLICY_DIR="${policy_dir}" run_window_contract "reload-${recovery}" valid 0s owned-project "${recovery}" api-invalid-policy-reload reload-invalid-policy; then
    echo "policy reload accepted invalid proof: ${recovery}" >&2
    exit 1
  fi
  [[ "$(cat "${policy_dir}/policy.yaml")" == "original fixture policy" ]]
  if [[ "${recovery}" != pluggable && "${recovery}" != invalid-baseline ]]; then
    jq -e --arg recovery "${recovery}" '.injected and .exitCode != 0 and
      .policyReload.restored == ($recovery != "missing-restoration")' \
      "${test_dir}/window-evidence/api/readiness/diagnostic/reload-${recovery}/chaos/api-invalid-policy-reload/reload-${recovery}/metadata.json" >/dev/null
  else
    [[ ! -f "${test_dir}/injected-reload-${recovery}" ]]
  fi
  jq -e '.injector.name == "docker-signal"' \
    "${test_dir}/window-evidence/api/readiness/diagnostic/reload-${recovery}/chaos/api-invalid-policy-reload/reload-${recovery}/metadata.json" >/dev/null
done
for recovery in writable-policy-mount wrong-policy-mount wrong-policy-env; do
  if MOCK_POLICY_DIR="${policy_dir}" run_window_contract "reload-${recovery}" valid 0s owned-project "${recovery}" api-invalid-policy-reload reload-invalid-policy; then
    echo "policy reload accepted unsafe mount/config: ${recovery}" >&2
    exit 1
  fi
  [[ ! -f "${test_dir}/injected-reload-${recovery}" ]]
  [[ "$(cat "${policy_dir}/policy.yaml")" == "original fixture policy" ]]
done
ln -s "${policy_dir}" "${test_dir}/policy-link"
if MOCK_POLICY_DIR="${test_dir}/policy-link" run_window_contract reload-symlink valid 0s owned-project valid api-invalid-policy-reload reload-invalid-policy; then
  echo "policy reload accepted a symlinked fixture directory" >&2
  exit 1
fi
[[ ! -f "${test_dir}/injected-reload-symlink" ]]
if MOCK_POLICY_DIR="${repo_root}" run_window_contract reload-repository valid 0s owned-project valid api-invalid-policy-reload reload-invalid-policy; then
  echo "policy reload accepted a repository directory" >&2
  exit 1
fi
[[ ! -f "${test_dir}/injected-reload-repository" ]]
jq '.containerId = "unrelated-api"' "${policy_dir}/owned-policy.json" >"${policy_dir}/marker.tmp"
mv "${policy_dir}/marker.tmp" "${policy_dir}/owned-policy.json"
if PYTHONOPTIMIZE=1 MOCK_POLICY_DIR="${policy_dir}" run_window_contract reload-wrong-owner valid 0s owned-project valid api-invalid-policy-reload reload-invalid-policy; then
  echo "policy reload accepted a mismatched ownership marker" >&2
  exit 1
fi
[[ ! -f "${test_dir}/injected-reload-wrong-owner" ]]
[[ "$(cat "${policy_dir}/policy.yaml")" == "original fixture policy" ]]
if run_window_contract controller-poisoned valid 0s owned-project controller-poisoned controller-restart; then
  echo "chaos accepted controller poison as a ready baseline" >&2
  exit 1
fi
[[ ! -f "${test_dir}/injected-controller-poisoned" ]]
for recovery in changed-policy changed-executable no-restart invalid-baseline changed-container changed-allocation deadline; do
  if run_window_contract "${recovery}" valid 0s owned-project "${recovery}"; then
    echo "integrated chaos accepted invalid recovery: ${recovery}" >&2
    exit 1
  fi
  recovery_metadata="${test_dir}/window-evidence/api/readiness/diagnostic/${recovery}/chaos/api-restart/${recovery}/metadata.json"
  jq -e '.exitCode != 0 and .recovery.required and .recovery.passed == false' \
    "${recovery_metadata}" >/dev/null
  if [[ "${recovery}" == invalid-baseline ]]; then
    [[ ! -f "${test_dir}/injected-${recovery}" ]]
    jq -e '.injected == false' "${recovery_metadata}" >/dev/null
  else
    [[ -f "${test_dir}/injected-${recovery}" ]]
    jq -e '.injected and .injectorExitCode == 0' "${recovery_metadata}" >/dev/null
    if [[ "${recovery}" == deadline ]]; then
      jq -e '.recovery.elapsedMilliseconds > .recovery.budgetMilliseconds and
        .recovery.reason == "readiness deadline exceeded"' "${recovery_metadata}" >/dev/null
    fi
  fi
done
for url in https://127.0.0.1:4000/ready http://external.example:4000/ready \
  http://user:password@127.0.0.1:4000/ready http://127.0.0.1:4000/health \
  'http://127.0.0.1:4000/ready?token=private'; do
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_TARGET=owned-api \
    CHAOS_COMPOSE_PROJECT=owned-project CHAOS_READINESS_URL="${url}" \
    MOCK_CHAOS_INJECTED="${test_dir}/injected-unsafe-url" \
    "${repo_root}/scripts/run-chaos.sh" api-restart >/dev/null 2>&1; then
    echo "chaos accepted a non-loopback or credential-bearing readiness URL" >&2
    exit 1
  fi
done
[[ ! -f "${test_dir}/injected-unsafe-url" ]]
for window in missing wrong-run malformed outside pending; do
  delay=0s
  if [[ "${window}" == outside ]]; then delay=1h; fi
  fixture="${window}"
  if [[ "${window}" == pending ]]; then fixture=valid; delay=5s; fi
  if run_window_contract "${window}" "${fixture}" "${delay}"; then
    echo "integrated chaos accepted an invalid load window: ${window}" >&2
    exit 1
  fi
  [[ ! -f "${test_dir}/injected-${window}" ]]
  jq -e '.exitCode != 0 and .chaosExitCode != 0 and .passed == false' \
    "${test_dir}/window-evidence/api/readiness/diagnostic/${window}/metadata.json" >/dev/null
done
if run_window_contract wrong-project valid 0s unrelated-project; then
  echo "integrated chaos accepted an unrelated Compose-owned container" >&2
  exit 1
fi
[[ ! -f "${test_dir}/injected-wrong-project" ]]
if run_window_contract overrun overrun 0s; then
  echo "integrated chaos accepted injection completion outside its load window" >&2
  exit 1
fi
[[ -f "${test_dir}/injected-overrun" ]]
jq -e '.exitCode != 0 and .chaosExitCode == 124 and .passed == false' \
  "${test_dir}/window-evidence/api/readiness/diagnostic/overrun/metadata.json" >/dev/null
for delay in invalid -1s 7201s; do
  if run_window_contract invalid-delay valid "${delay}"; then
    echo "integrated chaos accepted invalid delay: ${delay}" >&2
    exit 1
  fi
done
[[ ! -f "${test_dir}/injected-invalid-delay" ]]

mkdir -p "${test_dir}/admission-verifier"
jq -n '{metrics:{gitstore_namespace_admitted:{count:10,rate:10},
  gitstore_namespace_offered:{count:10},
  gitstore_namespace_scheduled:{count:10},
  gitstore_namespace_missed_schedules:{count:0},
  gitstore_namespace_endpoint_callbacks:{count:0},
  gitstore_namespace_burst_rounds:{value:0},
  "gitstore_namespace_offered{schedule:sustained}":{count:10},
  "gitstore_namespace_scheduled{schedule:sustained}":{count:10},
  "gitstore_namespace_missed_schedules{schedule:sustained}":{count:0},
  "gitstore_namespace_endpoint_callbacks{schedule:sustained}":{count:0},
  dropped_iterations:{count:0},
  http_req_failed:{passes:0,fails:100,value:0},
  checks:{passes:30,fails:0,value:1,thresholds:{"rate==1":false}},
  gitstore_namespace_graphql_failed:{passes:0,fails:10,value:0,thresholds:{"rate<0.001":false}},
  gitstore_namespace_visibility_ms:{count:10,"p(95)":100,"p(99)":200,thresholds:{"p(95)<=1000":false,"p(99)<=3000":false}}}}' \
  >"${test_dir}/admission-verifier/summary.json"
"${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier"
jq -e '.passed == true and .offered == 10 and .admitted == 10 and .failedOutcomes == 0 and
  .droppedIterations == 0 and .httpFailedRequests == 0 and .visibilityP99Milliseconds == 200' \
  "${test_dir}/admission-verifier/domain-verifier.json" >/dev/null
cp "${test_dir}/admission-verifier/summary.json" "${test_dir}/admission-verifier/native-summary.json"
for mutation in \
  'del(.metrics.gitstore_namespace_scheduled)' \
  '.metrics.gitstore_namespace_scheduled.count = 11' \
  '.metrics.gitstore_namespace_missed_schedules.count = 1' \
  '.metrics.gitstore_namespace_endpoint_callbacks.count = 1' \
  'del(.metrics.gitstore_namespace_burst_rounds)' \
  '.metrics.gitstore_namespace_burst_rounds.value = 1' \
  '.metrics.gitstore_namespace_burst_rounds.value = -1' \
  '.metrics.gitstore_namespace_burst_rounds.value = 0.5' \
  '.metrics.gitstore_namespace_burst_rounds.value = 9007199254740991' \
  'del(.metrics["gitstore_namespace_offered{schedule:sustained}"])' \
  '.metrics["gitstore_namespace_offered{schedule:sustained}"].count = 9' \
  '.metrics["gitstore_namespace_scheduled{schedule:sustained}"].count = 11' \
  '.metrics["gitstore_namespace_missed_schedules{schedule:sustained}"].count = 1' \
  '.metrics["gitstore_namespace_endpoint_callbacks{schedule:sustained}"].count = 2 |
   .metrics.gitstore_namespace_endpoint_callbacks.count = 2' \
  '.metrics["gitstore_namespace_offered{schedule:unknown}"] = {count:0}' \
  '.metrics.dropped_iterations.count = 1' \
  'del(.metrics.dropped_iterations)' \
  'del(.metrics.gitstore_namespace_offered)' \
  '.metrics.gitstore_namespace_offered.count = 11' \
  '.metrics.gitstore_namespace_offered.count = 9' \
  '.metrics.gitstore_namespace_offered.count = 10.5' \
  '.metrics.gitstore_namespace_offered.count = 9007199254740992' \
  'del(.metrics.http_req_failed)' \
  '.metrics.http_req_failed.value = 0.001' \
  '.metrics.http_req_failed.passes = 1' \
  '.metrics.http_req_failed.fails = 0' \
  '.metrics.http_req_failed.value = -1' \
  '.metrics.http_req_failed.fails = 100.5' \
  '.metrics.gitstore_namespace_graphql_failed.fails = 9 |
   .metrics.gitstore_namespace_graphql_failed.passes = 1 |
   .metrics.gitstore_namespace_graphql_failed.value = 0.1' \
  'del(.metrics.checks)' \
  '.metrics.checks.passes = 0' \
  '.metrics.checks.passes = 29' \
  '.metrics.checks.passes = 31' \
  '.metrics.checks.fails = 1' \
  'del(.metrics.gitstore_namespace_admitted)' \
  '.metrics.gitstore_namespace_admitted.count = 0' \
  '.metrics.gitstore_namespace_admitted.count = "10"' \
  '.metrics.gitstore_namespace_graphql_failed.value = 0.001' \
  '.metrics.gitstore_namespace_graphql_failed.value = -1' \
  '.metrics.gitstore_namespace_graphql_failed.fails = 0' \
  '.metrics.gitstore_namespace_graphql_failed.passes = 1' \
  '.metrics.gitstore_namespace_visibility_ms["p(95)"] = 1001' \
  '.metrics.gitstore_namespace_visibility_ms["p(99)"] = 3001' \
  'del(.metrics.gitstore_namespace_visibility_ms["p(99)"])' \
  'del(.metrics.gitstore_namespace_visibility_ms.count)' \
  '.metrics.gitstore_namespace_visibility_ms.count = 0' \
  '.metrics.gitstore_namespace_visibility_ms.count = 9' \
  '.metrics.gitstore_namespace_visibility_ms["p(95)"] = -1' \
  '.metrics.gitstore_namespace_visibility_ms["p(99)"] = 99' \
  '.metrics |= with_entries(.value = {values:.value})'; do
  jq "${mutation}" "${test_dir}/admission-verifier/native-summary.json" >"${test_dir}/admission-verifier/summary.json"
  if "${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier" >/dev/null 2>&1; then
    echo "namespace admission verifier accepted invalid native evidence: ${mutation}" >&2
    exit 1
  fi
  jq -e '.passed == false' "${test_dir}/admission-verifier/domain-verifier.json" >/dev/null
done
jq '.metrics.gitstore_namespace_visibility_ms["p(95)"] = 1000 |
  .metrics.gitstore_namespace_visibility_ms["p(99)"] = 3000' \
  "${test_dir}/admission-verifier/native-summary.json" >"${test_dir}/admission-verifier/summary.json"
"${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier"
cp "${test_dir}/admission-verifier/native-summary.json" "${test_dir}/admission-verifier/summary.json"
for endpoint_callbacks in 0 1; do
  jq --argjson callbacks "${endpoint_callbacks}" '
    .metrics.gitstore_namespace_burst_rounds.value = 2 |
    .metrics["gitstore_namespace_scheduled{schedule:sustained}"].count = 8 |
    .metrics["gitstore_namespace_offered{schedule:sustained}"].count = 8 |
    reduce ["burst1","burst2"][] as $name (.;
      .metrics["gitstore_namespace_scheduled{schedule:" + $name + "}"] = {count:1} |
      .metrics["gitstore_namespace_offered{schedule:" + $name + "}"] = {count:1} |
      .metrics["gitstore_namespace_missed_schedules{schedule:" + $name + "}"] = {count:0} |
      .metrics["gitstore_namespace_endpoint_callbacks{schedule:" + $name + "}"] = {count:$callbacks}) |
    .metrics["gitstore_namespace_endpoint_callbacks{schedule:sustained}"].count = $callbacks |
    .metrics.gitstore_namespace_endpoint_callbacks.count = (3 * $callbacks)' \
    "${test_dir}/admission-verifier/native-summary.json" >"${test_dir}/admission-verifier/summary.json"
  "${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier"
done
cp "${test_dir}/admission-verifier/summary.json" "${test_dir}/admission-verifier/native-burst-summary.json"
for mutation in \
  'del(.metrics["gitstore_namespace_offered{schedule:burst1}"])' \
  '.metrics["gitstore_namespace_offered{schedule:burst1}"].count = 0' \
  '.metrics["gitstore_namespace_scheduled{schedule:burst1}"].count = 0' \
  '.metrics.gitstore_namespace_burst_rounds.value = 1' \
  '.metrics["gitstore_namespace_endpoint_callbacks{schedule:burst1}"].count = 2 |
   .metrics.gitstore_namespace_endpoint_callbacks.count = 4' \
  '.metrics["gitstore_namespace_missed_schedules{schedule:burst2}"].count = 1 |
   .metrics.gitstore_namespace_missed_schedules.count = 1' \
  '.metrics["gitstore_namespace_offered{schedule:burst1}"].count = 2 |
   .metrics["gitstore_namespace_offered{schedule:burst2}"].count = 0'; do
  jq "${mutation}" "${test_dir}/admission-verifier/native-burst-summary.json" \
    >"${test_dir}/admission-verifier/summary.json"
  if "${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier" >/dev/null 2>&1; then
    echo "namespace admission verifier accepted an incomplete declared burst schedule: ${mutation}" >&2
    exit 1
  fi
done
cp "${test_dir}/admission-verifier/native-summary.json" "${test_dir}/admission-verifier/summary.json"
for invalid in multiple-documents malformed oversized symlink directory; do
  rm "${test_dir}/admission-verifier/summary.json"
  case "${invalid}" in
    multiple-documents) cat "${test_dir}/admission-verifier/native-summary.json" \
      "${test_dir}/admission-verifier/native-summary.json" >"${test_dir}/admission-verifier/summary.json" ;;
    malformed) printf '{' >"${test_dir}/admission-verifier/summary.json" ;;
    oversized) head -c 1048577 /dev/zero >"${test_dir}/admission-verifier/summary.json" ;;
    symlink) ln -s native-summary.json "${test_dir}/admission-verifier/summary.json" ;;
    directory) mkdir "${test_dir}/admission-verifier/summary.json" ;;
  esac
  if "${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier" >/dev/null 2>&1; then
    echo "namespace admission verifier accepted ${invalid} summary" >&2
    exit 1
  fi
  jq -e '.passed == false' "${test_dir}/admission-verifier/domain-verifier.json" >/dev/null
  if [[ "${invalid}" == directory ]]; then
    rmdir "${test_dir}/admission-verifier/summary.json"
  else
    rm "${test_dir}/admission-verifier/summary.json"
  fi
  cp "${test_dir}/admission-verifier/native-summary.json" "${test_dir}/admission-verifier/summary.json"
done

mkdir -p "${test_dir}/category-verifier" "${test_dir}/product-verifier"
jq -n '{metrics:{
  checks:{passes:100,fails:0,value:1},
  http_req_failed:{passes:0,fails:100,value:0},
  dropped_iterations:{count:0},
  category_seed_created:{count:10000},
  category_mutation_offered:{count:6000},
  category_mutation_missed_schedules:{count:0},
  category_mutation_endpoint_callbacks:{count:0},
  category_mutation_acknowledged:{count:6000},
  category_mutation_failed:{count:0},
  category_filtered_read_offered:{count:3000},
  category_filtered_read_completed:{count:3000},
  category_filtered_read_failed:{count:0},
  category_filtered_read_missed_schedules:{count:0},
  category_filtered_read_endpoint_callbacks:{count:0},
  category_declared_mutations:{value:6000},
  category_declared_reads:{value:3000},
  category_mutation_rate:{value:20},
  category_read_rate:{value:10},
  category_duration_seconds:{value:300},
  category_product_seeded:{count:321},
  category_product_offered:{count:300},
  category_product_completed:{count:300},
  category_product_failed:{count:0},
  category_product_missed_schedules:{count:0},
  category_product_endpoint_callbacks:{count:0},
  category_product_reassigned:{count:150},
  category_product_oracle_checks:{count:4832},
  category_product_pages:{count:7248},
  category_product_pages_forward:{count:3624},
  category_product_pages_backward:{count:3624},
  category_product_declared_updates:{value:300},
  category_product_rate:{value:1},
  category_product_fixture_count:{value:321},
  category_product_expected_max_shard_members:{value:0,max:15},
  category_product_page_ms:{count:7248,"p(95)":100,"p(99)":200},
  category_product_convergence_ms:{count:300,"p(95)":100,"p(99)":200},
  category_mutation_conflicts:{count:0},
  category_sc007_own_commit_checks:{count:6000},
  category_sc007_violations:{count:0},
  category_sc007_pool_records:{count:6000},
  category_sc007_duplicate_revisions:{count:0},
  category_sc009_sets_checked:{count:14},
  category_sc009_set_mismatches:{count:0},
  category_sc009_missing:{count:0},
  category_sc009_extra:{count:0},
  category_unreconciled_at_audit:{count:0},
  category_cascade_converged:{count:1},
  category_cascade_convergence_ms:{count:1,"p(95)":100,"p(99)":100},
  category_load_start_ms:{value:1000},
  category_load_end_ms:{value:301000},
  "http_req_duration{operation:createCategory}":{count:3000,"p(95)":750,"p(99)":2000},
  "http_req_duration{operation:updateCategory}":{count:3000,"p(95)":750,"p(99)":2000},
  "http_req_duration{operation:filteredCategories}":{count:3000,"p(95)":150,"p(99)":500}
}}' >"${test_dir}/category-verifier/native-summary.json"
jq -n '{repository:"gitstore-system",namespace:"default",kind:"category",runId:"category-contract",
  workers:1,intervalMs:1000,offered:300,pushes:300,failures:0,dropped:0,retries:0,
  intervalNs:1000000000,durationNs:300000000000,scheduled:300,missedSchedules:0,
  offeredDurationNs:300000000000,drainDurationNs:10000000,drainTimeoutNs:30000000000,
  drainTimedOut:false,cancelled:false,
  firstPushMs:1000,lastPushMs:301000,latencyMs:{p50:10,p95:20,p99:30,max:30}}' \
  >"${test_dir}/category-verifier/valid-push.json"
jq -n '{findings:[],actions:[]}' >"${test_dir}/category-verifier/valid-audit.json"
cp "${test_dir}/category-verifier/native-summary.json" "${test_dir}/category-verifier/summary.json"
cp "${test_dir}/category-verifier/valid-push.json" "${test_dir}/category-verifier/push-input.json"
cp "${test_dir}/category-verifier/valid-audit.json" "${test_dir}/category-verifier/audit-input.json"
category_verify() {
  MODE="$1" CAPACITY_RUN_ID=category-contract \
    CAPACITY_CATEGORY_PUSH_EVIDENCE="${test_dir}/category-verifier/push-input.json" \
    CAPACITY_CATEGORY_AUDIT_FILE="${test_dir}/category-verifier/audit-input.json" \
    "${repo_root}/tests/capacity/verifiers/category-hierarchy.sh" "${test_dir}/category-verifier"
}
for mode in diagnostic alpha production; do
  category_verify "${mode}"
done
jq '.metrics.category_mutation_endpoint_callbacks.count = 1 |
  .metrics.category_filtered_read_endpoint_callbacks.count = 1' \
  "${test_dir}/category-verifier/native-summary.json" >"${test_dir}/category-verifier/summary.json"
category_verify production
for mutation in \
  'del(.metrics.category_mutation_offered)' \
  'del(.metrics.category_mutation_missed_schedules)' \
  'del(.metrics.category_mutation_endpoint_callbacks)' \
  'del(.metrics.category_filtered_read_endpoint_callbacks)' \
  '.metrics.category_mutation_endpoint_callbacks.count = 2' \
  '.metrics.category_filtered_read_endpoint_callbacks.count = 2' \
  'del(.metrics.category_filtered_read_completed)' \
  'del(.metrics.category_filtered_read_failed)' \
  'del(.metrics.category_filtered_read_missed_schedules)' \
  '.metrics.category_mutation_offered.count = 59 |
    .metrics.category_mutation_acknowledged.count = 59 |
    .metrics.category_sc007_own_commit_checks.count = 59 |
    .metrics.category_sc007_pool_records.count = 59' \
  '.metrics.category_mutation_offered.count = 61' \
  '.metrics.category_filtered_read_offered.count = 59 |
    .metrics.category_filtered_read_completed.count = 59' \
  '.metrics.category_filtered_read_completed.count = 59' \
  '.metrics.category_filtered_read_failed.count = 1' \
  '.metrics["http_req_duration{operation:filteredCategories}"].count = 59' \
  '.metrics.category_mutation_missed_schedules.count = 1' \
  '.metrics.category_filtered_read_missed_schedules.count = 1' \
  'del(.metrics.category_declared_reads)' \
  'del(.metrics.category_mutation_rate)' \
  'del(.metrics.category_read_rate)' \
  'del(.metrics.category_duration_seconds)' \
  '.metrics.category_declared_mutations.value = 59' \
  '.metrics.category_mutation_offered.count = 9007199254740992' \
  '.metrics.category_duration_seconds.value = 59' \
  '.metrics.category_read_rate.value = 1.5' \
  '.metrics.category_load_end_ms.value = 60999' \
  'del(.metrics.category_mutation_failed)' \
  'del(.metrics.category_sc007_duplicate_revisions)' \
  '.metrics.category_sc007_pool_records.count = 9' \
  '.metrics.category_mutation_acknowledged.count = "10"' \
  '.metrics.category_sc009_sets_checked.count = 14.5' \
  '.metrics.checks.passes = 0' \
  '.metrics.checks.fails = 1' \
  '.metrics.http_req_failed.value = -1' \
  '.metrics.http_req_failed.value = 0.001' \
  '.metrics.http_req_failed.fails = 0' \
  '.metrics.http_req_failed.passes = 1' \
  '.metrics.dropped_iterations.count = 1' \
  'del(.metrics.dropped_iterations)' \
  '.metrics["http_req_duration{operation:filteredCategories}"].count = 0' \
  '.metrics["http_req_duration{operation:filteredCategories}"]["p(95)"] = 151' \
  '.metrics["http_req_duration{operation:filteredCategories}"]["p(99)"] = 501' \
  '.metrics["http_req_duration{operation:createCategory}"]["p(95)"] = 751' \
  '.metrics["http_req_duration{operation:updateCategory}"]["p(99)"] = 2001' \
  '.metrics["http_req_duration{operation:updateCategory}"]["p(99)"] = 700' \
  'del(.metrics.category_product_seeded)' \
  '.metrics.category_product_seeded.count = 320' \
  '.metrics.category_product_offered.count = 299 | .metrics.category_product_completed.count = 299' \
  '.metrics.category_product_failed.count = 1' \
  '.metrics.category_product_missed_schedules.count = 1' \
  '.metrics.category_product_endpoint_callbacks.count = 1' \
  '.metrics.category_product_reassigned.count = 149' \
  '.metrics.category_product_oracle_checks.count = 4816' \
  '.metrics.category_product_pages_forward.count = 1' \
  '.metrics.category_product_pages_backward.count = 1' \
  '.metrics.category_product_pages.count = 7247' \
  '.metrics.category_product_declared_updates.value = 299' \
  '.metrics.category_product_rate.value = 0' \
  '.metrics.category_product_fixture_count.value = 320' \
  '.metrics.category_product_expected_max_shard_members.max = 0' \
  '.metrics.category_product_expected_max_shard_members.max = 322' \
  '.metrics.category_product_page_ms.count = 7247' \
  '.metrics.category_product_convergence_ms.count = 299' \
  '.metrics.category_product_page_ms["p(95)"] = -1' \
  '.metrics.category_product_convergence_ms["p(99)"] = 99' \
  'del(.metrics.category_product_page_ms["p(99)"])' \
  '.metrics |= with_entries(.value = {values:.value})'; do
  jq "${mutation}" "${test_dir}/category-verifier/native-summary.json" >"${test_dir}/category-verifier/summary.json"
  if category_verify diagnostic >/dev/null 2>&1; then
    echo "category verifier accepted invalid native evidence: ${mutation}" >&2
    exit 1
  fi
  jq -e '.passed == false' "${test_dir}/category-verifier/domain-verifier.json" >/dev/null
done
for mutation in \
  '.metrics.category_mutation_rate.value = 19 |
    .metrics.category_declared_mutations.value = 5700 |
    .metrics.category_mutation_offered.count = 5700 |
    .metrics.category_mutation_acknowledged.count = 5700 |
    .metrics.category_sc007_own_commit_checks.count = 5700 |
    .metrics.category_sc007_pool_records.count = 5700' \
  '.metrics.category_duration_seconds.value = 299 |
    .metrics.category_declared_mutations.value = 5980 |
    .metrics.category_mutation_offered.count = 5980 |
    .metrics.category_mutation_acknowledged.count = 5980 |
    .metrics.category_sc007_own_commit_checks.count = 5980 |
    .metrics.category_sc007_pool_records.count = 5980 |
    .metrics.category_declared_reads.value = 2990 |
    .metrics.category_filtered_read_offered.count = 2990 |
    .metrics.category_filtered_read_completed.count = 2990 |
    .metrics["http_req_duration{operation:filteredCategories}"].count = 2990 |
    .metrics.category_load_end_ms.value = 300000 |
    .metrics.category_product_declared_updates.value = 299 |
    .metrics.category_product_offered.count = 299 |
    .metrics.category_product_completed.count = 299 |
    .metrics.category_product_reassigned.count = 149 |
    .metrics.category_product_oracle_checks.count = 4816 |
    .metrics.category_product_convergence_ms.count = 299'; do
  jq "${mutation}" "${test_dir}/category-verifier/native-summary.json" >"${test_dir}/category-verifier/summary.json"
  category_verify diagnostic
  for mode in alpha production; do
    if category_verify "${mode}" >/dev/null 2>&1; then
      echo "category verifier accepted a below-minimum acceptance workload: ${mutation}" >&2
      exit 1
    fi
    jq -e '.passed == false and (.reason | contains("minimum 20 mutations/s for 300 seconds"))' \
      "${test_dir}/category-verifier/domain-verifier.json" >/dev/null
  done
done
cp "${test_dir}/category-verifier/native-summary.json" "${test_dir}/category-verifier/summary.json"
for mutation in \
  '.dropped = 1 | .offered = 61' \
  '.offered = 61' \
  'del(.scheduled)' \
  '.scheduled = 59' \
  '.offered = 59 | .pushes = 59' \
  '.missedSchedules = 1' \
  'del(.missedSchedules)' \
  '.durationNs = 61000000000' \
  '.durationNs = 0' \
  '.intervalNs = 999999999' \
  '.intervalNs = 1e99' \
  '.offeredDurationNs = 59000000000' \
  '.offeredDurationNs = 61000000000' \
  'del(.offeredDurationNs)' \
  '.drainDurationNs = 30000000001' \
  '.drainTimeoutNs = 60000000000' \
  '.drainTimedOut = true' \
  'del(.drainTimedOut)' \
  '.cancelled = true' \
  '.cancelled = "false"' \
  'del(.cancelled)' \
  'del(.offered)' \
  '.pushes = "60"' \
  '.retries = 301' \
  '.kind = "product"' \
  '.namespace = "different"' \
  '.runId = "other-run"' \
  '.firstPushMs = 250000' \
  '.token = "must-not-be-copied"'; do
  jq "${mutation}" "${test_dir}/category-verifier/valid-push.json" >"${test_dir}/category-verifier/push-input.json"
  if category_verify alpha >/dev/null 2>&1; then
    echo "category verifier accepted invalid push accounting: ${mutation}" >&2
    exit 1
  fi
done
cp "${test_dir}/category-verifier/valid-push.json" "${test_dir}/category-verifier/push-input.json"
jq '.drainDurationNs = 30000000000' "${test_dir}/category-verifier/valid-push.json" >"${test_dir}/category-verifier/push-input.json"
category_verify alpha
cp "${test_dir}/category-verifier/valid-push.json" "${test_dir}/category-verifier/push-input.json"
for mutation in \
  'del(.findings)' \
  '.findings = {}' \
  '.findings = [{type:"missing"}]' \
  'del(.actions)' \
  '.actions = [{type:"insert"}]' \
  '.namespaceRepositoryFences = [{namespace:"default"}]' \
  '.serviceAccountEnrollments = [{name:"incomplete"}]' \
  '.token = "must-not-be-copied"'; do
  jq "${mutation}" "${test_dir}/category-verifier/valid-audit.json" >"${test_dir}/category-verifier/audit-input.json"
  if category_verify production >/dev/null 2>&1; then
    echo "category verifier accepted incomplete projection audit: ${mutation}" >&2
    exit 1
  fi
done
jq -n '{findings:null,actions:null}' >"${test_dir}/category-verifier/audit-input.json"
category_verify alpha
jq '.metrics.category_seed_created.count = 9999' "${test_dir}/category-verifier/native-summary.json" \
  >"${test_dir}/category-verifier/summary.json"
if category_verify alpha >/dev/null 2>&1; then
  echo "category acceptance allowed fewer than 10000 descendants" >&2
  exit 1
fi
category_verify diagnostic
cp "${test_dir}/category-verifier/native-summary.json" "${test_dir}/category-verifier/summary.json"
rm "${test_dir}/category-verifier/audit-input.json"
ln -s "${test_dir}/category-verifier/valid-audit.json" "${test_dir}/category-verifier/audit-input.json"
if category_verify alpha >/dev/null 2>&1; then
  echo "category acceptance followed a symlink audit" >&2
  exit 1
fi

jq -n '{metrics:{checks:{passes:6,fails:0,value:1},
  http_req_failed:{passes:0,fails:7,value:0},
  product_lifecycle_offered:{count:1},
  dropped_iterations:{count:0},
  product_lifecycle_replica_checks:{count:2},
  product_lifecycle_admission_checks:{count:1},
  product_lifecycle_deletion_race_checks:{count:1},
  product_lifecycle_deletion_replica_checks:{count:2}
}}' >"${test_dir}/product-verifier/native-summary.json"
cp "${test_dir}/product-verifier/native-summary.json" "${test_dir}/product-verifier/summary.json"
"${repo_root}/tests/capacity/verifiers/product-lifecycle.sh" "${test_dir}/product-verifier"
for mutation in \
  '.metrics.checks.passes = 0' \
  '.metrics.checks.passes = 5' \
  '.metrics.checks.passes = 7' \
  'del(.metrics.product_lifecycle_offered)' \
  '.metrics.product_lifecycle_offered.count = 2' \
  '.metrics.product_lifecycle_offered.count = 9007199254740992' \
  'del(.metrics.dropped_iterations)' \
  '.metrics.dropped_iterations.count = 1' \
  '.metrics.checks.fails = 1' \
  '.metrics.http_req_failed.value = -1' \
  '.metrics.http_req_failed.fails = 0' \
  '.metrics.product_lifecycle_replica_checks.count = "2"' \
  '.metrics.http_req_failed.passes = 1' \
  '.metrics.product_lifecycle_admission_checks.count = 1.5' \
  '.metrics.product_lifecycle_deletion_race_checks.count = 0' \
  '.metrics.product_lifecycle_deletion_replica_checks.count = 1' \
  'del(.metrics.product_lifecycle_deletion_replica_checks)' \
  '.metrics |= with_entries(.value = {values:.value})'; do
  jq "${mutation}" "${test_dir}/product-verifier/native-summary.json" >"${test_dir}/product-verifier/summary.json"
  if "${repo_root}/tests/capacity/verifiers/product-lifecycle.sh" "${test_dir}/product-verifier" >/dev/null 2>&1; then
    echo "Product verifier accepted invalid native evidence: ${mutation}" >&2
    exit 1
  fi
  jq -e '.passed == false' "${test_dir}/product-verifier/domain-verifier.json" >/dev/null
done

node --no-warnings --experimental-vm-modules --input-type=module - "${repo_root}" <<'JS'
import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const root = process.argv[2];
let request;
let batches;
let getRequest;
let observedChecks;
let observedCounters;
let sleepRequest = () => {};
class Counter {
  constructor(name) { this.name = name; }
  add(value) { observedCounters[this.name] = (observedCounters[this.name] || 0) + value; }
}
class Rate extends Counter {
  add(value) {
    super.add(Number(value));
    const samples = `${this.name}_samples`;
    observedCounters[samples] = (observedCounters[samples] || 0) + 1;
  }
}
const context = vm.createContext({ __ENV: {}, __VU: 1, __ITER: 0 });
const stubs = {
  'k6/execution': { default: { scenario: { iterationInTest: 0, startTime: 1000 } } },
  'k6/http': { default: { batch: (requests) => batches(requests), get: (...args) => getRequest(...args) } },
  'k6/encoding': { default: {
    b64decode: (value) => Buffer.from(value, 'base64').toString('utf8'),
    b64encode: (value) => Buffer.from(value).toString('base64'),
  } },
  'k6': {
    check: (value, predicates) => {
      const outcomes = Object.values(predicates).map((predicate) => !!predicate(value));
      observedCounters.stub_checks_passes = (observedCounters.stub_checks_passes || 0) + outcomes.filter(Boolean).length;
      observedCounters.stub_checks_fails = (observedCounters.stub_checks_fails || 0) + outcomes.filter((outcome) => !outcome).length;
      const valid = outcomes.every(Boolean);
      observedChecks.push(valid);
      return valid;
    },
    sleep: (seconds) => sleepRequest(seconds),
  },
  'k6/metrics': { Counter, Gauge: Counter, Trend: Counter, Rate },
  '../lib/config.js': {
    env: (name, fallback) => ({
      CAPACITY_API_A: 'http://api-a/graphql', CAPACITY_API_B: 'http://api-b/graphql',
      CAPACITY_BASE_URL: 'http://api-a',
      CAPACITY_TOKEN: 'fixture', CAPACITY_RUN_ID: 'contract-run',
    })[name] || fallback || 'fixture',
    integer: (_, fallback) => fallback,
  },
  '../lib/graphql.js': {
    graphql: (...args) => request(...args),
    graphqlPair: (urls, ...args) => {
      assert.equal(urls.length, 2);
      assert.notEqual(urls[0], urls[1]);
      return urls.map((url) => request(url, ...args));
    },
  },
};
const configuration = new vm.SourceTextModule(
  fs.readFileSync(`${root}/tests/capacity/lib/config.js`, 'utf8'), { context });
await configuration.link(() => { throw new Error('unexpected config dependency'); });
await configuration.evaluate();
stubs['../lib/config.js'].integer = configuration.namespace.integer;
stubs['../lib/config.js'].durationNanoseconds = configuration.namespace.durationNanoseconds;
stubs['../lib/config.js'].announceLoadWindow = configuration.namespace.announceLoadWindow;
assert.equal(configuration.namespace.integer('TEST_INTEGER', 20), 20);
assert.throws(() => configuration.namespace.integer('TEST_INTEGER', 20, 21), /exact decimal integer/);
context.__ENV.TEST_INTEGER = '0';
assert.equal(configuration.namespace.integer('TEST_INTEGER', 20, 0), 0);
context.__ENV.TEST_INTEGER = '';
assert.equal(configuration.namespace.integer('TEST_INTEGER', 20), 20);
for (const [value, expected] of [['1', 1], ['0020', 20], ['9007199254740991', 9007199254740991]]) {
  context.__ENV.TEST_INTEGER = value;
  assert.equal(configuration.namespace.integer('TEST_INTEGER', 20), expected);
}
for (const value of ['20invalid', '1.5', '1e3', '-1', '0', ' 20 ', '9007199254740992']) {
  context.__ENV.TEST_INTEGER = value;
  assert.throws(() => configuration.namespace.integer('TEST_INTEGER', 20), /exact decimal integer/);
}
delete context.__ENV.TEST_INTEGER;
for (const [duration, expected] of [['1s', '1000000000'], ['1h2m3.000000001s', '3723000000001']]) {
  assert.equal(configuration.namespace.durationNanoseconds(duration).toString(), expected);
}
for (const duration of ['', '0s', '1ms', '1m invalid', null]) {
  assert.throws(() => configuration.namespace.durationNanoseconds(duration), /Capacity scenario/);
}
const windows = [];
const transportWarnings = [];
context.console = { info: (message) => windows.push(message), warn: (message) => transportWarnings.push(message) };
const windowStart = vm.runInContext('Date.now()', context);
configuration.namespace.announceLoadWindow(windowStart, '10000000000');
assert.equal(windows.length, 0, 'unconfigured chaos must not emit a load event');
context.__ENV.CAPACITY_CHAOS_PROFILE = 'api-restart';
context.__ENV.CAPACITY_RUN_ID = 'contract-window';
configuration.namespace.announceLoadWindow(windowStart, '10000000000');
assert.equal(windows.length, 1);
assert.deepEqual(JSON.parse(windows[0].replace('CAPACITY_LOAD_WINDOW ', '')), {
  schemaVersion: 1, runId: 'contract-window', startMs: windowStart, endMs: windowStart + 10000,
});
configuration.namespace.announceLoadWindow(windowStart, '10000999999');
assert.equal(JSON.parse(windows[1].replace('CAPACITY_LOAD_WINDOW ', '')).endMs, windowStart + 10000,
  'sub-millisecond duration must not extend the native fault window');
for (const [start, duration] of [[0, '1000000000'], [windowStart + 60000, '1000000000'],
  [1, '1000000000'], [windowStart, '0'], [windowStart, '9007199254740991000000']]) {
  assert.throws(() => configuration.namespace.announceLoadWindow(start, duration), /active exact declared load window/);
}
delete context.__ENV.CAPACITY_CHAOS_PROFILE;
delete context.__ENV.CAPACITY_RUN_ID;
const graphqlHelper = new vm.SourceTextModule(
  fs.readFileSync(`${root}/tests/capacity/lib/graphql.js`, 'utf8'), { context });
await graphqlHelper.link((name) => {
  assert.equal(name, 'k6/http');
  return new vm.SyntheticModule(['default'], function () {
    this.setExport('default', stubs['k6/http'].default);
  }, { context });
});
await graphqlHelper.evaluate();
const pairedEndpoints = ['http://api-a/graphql', 'http://api-b/graphql'];
batches = (requests) => {
  assert.equal(requests.length, 2, 'a read pair never launches more than two HTTP requests');
  requests.forEach((request, index) => {
    assert.equal(request.method, 'POST');
    assert.equal(request.url, pairedEndpoints[index]);
    assert.equal(request.params.timeout, '250ms');
    assert.equal(request.params.tags.operation, 'pairedRead');
    assert.equal(request.params.headers['Content-Type'], 'application/json');
    assert.ok(request.params.headers.Authorization.endsWith('fixture'));
    assert.equal(JSON.parse(request.body).variables.name, 'leaf');
  });
  return requests.map((_, index) => ({ status: index === 0 ? 200 : 503,
    json: () => ({ data: { replica: index } }) }));
};
const pairedResponses = graphqlHelper.namespace.graphqlPair(pairedEndpoints, 'fixture', 'query { __typename }',
  { name: 'leaf' }, { operation: 'pairedRead' }, { timeout: '250ms' });
assert.equal(pairedResponses[0].body.data.replica, 0);
assert.equal(pairedResponses[1].body.data.replica, 1);
assert.equal(graphqlHelper.namespace.graphqlSucceeded(pairedResponses[0]), true);
assert.equal(graphqlHelper.namespace.graphqlSucceeded(pairedResponses[1]), false);
for (const urls of [[], pairedEndpoints.slice(0, 1), [...pairedEndpoints, 'http://api-c/graphql'],
  [pairedEndpoints[0], pairedEndpoints[0]], [null, pairedEndpoints[1]]]) {
  assert.throws(() => graphqlHelper.namespace.graphqlPair(urls, '', '', {}), /two distinct endpoints/);
}
batches = () => [];
assert.throws(() => graphqlHelper.namespace.graphqlPair(pairedEndpoints, '', '', {}), /both responses/);
batches = () => [{ status: 200, json: () => { throw new Error('invalid JSON'); } },
  { status: 200, json: () => ({ data: { __typename: 'Query' } }) }];
const nonJSONPair = graphqlHelper.namespace.graphqlPair(pairedEndpoints, '', '', {});
assert.equal(graphqlHelper.namespace.graphqlSucceeded(nonJSONPair[0]), false);
assert.equal(graphqlHelper.namespace.graphqlSucceeded(nonJSONPair[1]), true);
stubs['../lib/graphql.js'].graphqlSucceeded = graphqlHelper.namespace.graphqlSucceeded;
stubs['../lib/graphql.js'].graphqlErrorCode = graphqlHelper.namespace.graphqlErrorCode;
const success = { response: { status: 200 }, body: { data: { __typename: 'Query' } } };
assert.equal(graphqlHelper.namespace.graphqlSucceeded(success), true);
assert.equal(graphqlHelper.namespace.graphqlSucceeded({ ...success, body: { ...success.body, errors: [] } }), true);
const classifiedConflict = { response: { status: 200 },
  body: { errors: [{ extensions: { code: 'CONFLICT' } }] } };
assert.equal(graphqlHelper.namespace.graphqlErrorCode(classifiedConflict), 'CONFLICT');
assert.equal(graphqlHelper.namespace.graphqlErrorCode({
  ...classifiedConflict, body: { errors: [...classifiedConflict.body.errors, ...classifiedConflict.body.errors] },
}), 'CONFLICT');
for (const invalid of [
  null, {}, success, { ...classifiedConflict, response: { status: 503 } },
  ...[null, 'invalid', { errors: [] }, { errors: [null] }, { errors: {} },
    { errors: [{ message: '{"code":"CONFLICT"}' }] },
    { errors: [...classifiedConflict.body.errors, { extensions: { code: 'FORBIDDEN' } }] }].map((body) =>
    ({ response: { status: 200 }, body })),
  ...[[], 'invalid', 42].map((data) => ({ ...classifiedConflict, body: { ...classifiedConflict.body, data } })),
]) {
  assert.equal(graphqlHelper.namespace.graphqlErrorCode(invalid), null);
}
for (const invalid of [
  null, {}, { ...success, response: { status: 503 } }, { ...success, response: { status: '200' } },
  ...[null, [], 'invalid', 1, {}, { data: null }, { data: [] },
    { ...success.body, errors: null }, { ...success.body, errors: {} },
    { ...success.body, errors: [{ message: 'fixture rejection' }] }].map((body) => ({ ...success, body })),
]) {
  assert.equal(graphqlHelper.namespace.graphqlSucceeded(invalid), false);
}
async function load(profile, extra = '') {
  const source = fs.readFileSync(`${root}/tests/capacity/profiles/${profile}.js`, 'utf8');
  const module = new vm.SourceTextModule(source + extra, { context });
  await module.link((name) => {
    const exports = stubs[name];
    assert.ok(exports, `unexpected dependency ${name}`);
    return new vm.SyntheticModule(Object.keys(exports), function () {
      for (const [key, value] of Object.entries(exports)) this.setExport(key, value);
    }, { context });
  });
  await module.evaluate();
  return module.namespace;
}
const category = await load('category-hierarchy', '\nexport { listAll, submit, waitForPath, timestampNanoseconds, productShard, productMembershipWindow, waitForProductMembership, productIntent };');
context.__ENV.CAPACITY_CATEGORY_PRODUCT_RATE = '1000000000000';
await assert.rejects(() => load('category-hierarchy'), /exceeds exact numeric precision/);
delete context.__ENV.CAPACITY_CATEGORY_PRODUCT_RATE;
const page = (names, hasNextPage = false, endCursor = null) => ({
  response: { status: 200 },
  body: { data: { categories: {
    edges: names.map((name) => ({ node: { metadata: { name } } })),
    pageInfo: { hasNextPage, endCursor },
  } } },
});
let calls = 0;
request = (_, __, ___, variables) => {
  calls++;
  assert.equal(variables.after, calls === 1 ? null : 'cursor-1');
  return calls === 1 ? page(['one'], true, 'cursor-1') : page(['two']);
};
assert.equal(category.listAll('api', null, 'query').length, 2);
assert.equal(calls, 2);
for (const invalid of [
  { response: { status: 503 }, body: { errors: [{ message: 'private-response-marker' }] } },
  { response: { status: 200 }, body: { errors: [{ message: 'private-response-marker' }] } },
  { response: { status: 200 }, body: { data: { categories: null } } },
  page(['one'], true, ''),
  page(['one'], true, null),
  page(Array.from({ length: 101 }, (_, index) => `node-${index}`)),
  page(['duplicate', 'duplicate']),
  page(['']),
]) {
  request = () => invalid;
  assert.throws(() => category.listAll('api', null, 'query'), (error) =>
    error.message.startsWith('category audit ') && !error.message.includes('private-response-marker'));
}
calls = 0;
request = () => { calls++; return page([`node-${calls}`], true, 'repeated'); };
assert.throws(() => category.listAll('api', null, 'query'), /pagination did not advance/);
assert.equal(calls, 2);
calls = 0;
request = () => { calls++; return page([], true, `cursor-${calls}`); };
assert.throws(() => category.listAll('api', null, 'query'), /page budget before completion/);
assert.equal(calls, 5000);

const ownCategory = {
  metadata: { name: 'fixture-category', revision: `main@sha1:${'a'.repeat(40)}` },
  spec: { title: 'fixture-title' },
};
for (const invalid of [
  { response: { status: 503 }, body: { data: { category: ownCategory } } },
  { response: { status: 200 }, body: { data: { category: ownCategory }, errors: [{ message: 'fixture rejection' }] } },
  ...[null, 'invalid', { data: { category: null } },
    { data: { category: { ...ownCategory, metadata: { ...ownCategory.metadata, name: 'different-name' } } } },
    { data: { category: { ...ownCategory, metadata: { ...ownCategory.metadata, revision: '' } } } },
    { data: { category: { ...ownCategory, spec: { ...ownCategory.spec, parentRef: { name: 'different-parent' } } } } },
    { data: { category: { ...ownCategory, spec: { title: 'different-title' } } } }].map((body) =>
    ({ response: { status: 200 }, body })),
]) {
  observedCounters = {};
  calls = 0;
  request = (_, __, ___, ____, tags) => {
    if (tags.operation === 'confirmCategory') return invalid;
    return { response: { status: 200 }, body: {
      errors: [{ extensions: { code: calls++ === 0 ? 'CONFLICT' : 'ALREADY_EXISTS' } }],
    } };
  };
  assert.equal(category.submit('create', 'fixture-category', 'fixture-title', '', 0, {}), null,
    'Category conflict confirmation must reject HTTP failures and partial GraphQL error data');
}
for (const errors of [undefined, []]) {
  observedCounters = {};
  calls = 0;
  request = (_, __, ___, ____, tags) => {
    if (tags.operation === 'confirmCategory') return {
      response: { status: 200 }, body: { data: { category: ownCategory }, ...(errors === undefined ? {} : { errors }) },
    };
    return { response: { status: 200 }, body: {
      errors: [{ extensions: { code: calls++ === 0 ? 'CONFLICT' : 'ALREADY_EXISTS' } }],
    } };
  };
  assert.equal(category.submit('create', 'fixture-category', 'fixture-title', '', 0, {}), ownCategory);
}
for (const result of [
  { response: { status: 503 }, body: classifiedConflict.body },
  { response: { status: 200 }, body: { errors: [...classifiedConflict.body.errors, { extensions: { code: 'FORBIDDEN' } }] } },
  { response: { status: 200 }, body: null },
]) {
  observedCounters = {};
  calls = 0;
  request = () => { calls++; return result; };
  assert.equal(category.submit('create', 'fixture-category', 'fixture-title', '', 0, {}), null);
  assert.equal(calls, 1);
}
for (const initialResult of [undefined, classifiedConflict,
  { response: { status: 503 }, body: classifiedConflict.body }]) {
  observedCounters = {};
  calls = 0;
  request = () => { calls++; return classifiedConflict; };
  assert.equal(category.submit('create', 'fixture-category', 'fixture-title', '', 0, {}, initialResult), null);
  assert.equal(calls, 4, 'batch history must not enlarge the four-attempt retry bound');
  assert.equal(observedCounters.category_mutation_conflicts, initialResult === classifiedConflict ? 5 : 4);
}
for (const initialResult of [undefined, { response: { status: 503 }, body: classifiedConflict.body }]) {
  observedCounters = {};
  calls = 0;
  request = () => { calls++; return { response: { status: 200 },
    body: { errors: [{ extensions: { code: 'ALREADY_EXISTS' } }] } }; };
  assert.equal(category.submit('create', 'fixture-category', 'fixture-title', '', 0, {}, initialResult), null);
  assert.equal(calls, 1, 'AlreadyExists without a uniform successful-HTTP conflict must fail');
}

let now = 1000;
context.Date = { now: () => now, parse: Date.parse };
const resolvedCategory = { metadata: { name: 'fixture-category' },
  status: { resolved: { path: ['fixture-root', 'fixture-category'], depth: 1 } } };
for (const invalid of [
  { response: { status: 503 }, body: { data: { category: resolvedCategory } } },
  { response: { status: 200 }, body: { data: { category: resolvedCategory }, errors: [{ message: 'fixture rejection' }] } },
  ...[null, { data: { category: null } },
    { data: { category: { ...resolvedCategory, metadata: { name: 'different-name' } } } },
    { data: { category: { metadata: resolvedCategory.metadata, status: { resolved: { path: 'fixture-category' } } } } },
    { data: { category: { metadata: resolvedCategory.metadata, status: { resolved: { path: [null, 'fixture-category'] } } } } }].map((body) =>
    ({ response: { status: 200 }, body })),
]) {
  now = 1000;
  request = () => { now += 1000; return invalid; };
  assert.equal(category.waitForPath('fixture-category', () => true, 1000), null,
    'Category convergence must not accept unsuccessful, malformed or wrong-identity reads');
}
now = 1000;
request = () => ({ response: { status: 200 }, body: { data: { category: resolvedCategory }, errors: [] } });
assert.equal(category.waitForPath('fixture-category', () => true, 1000), 0,
  'a valid zero-millisecond convergence is distinct from timeout');

context.__ENV.CAPACITY_CATEGORY_SUBTREE_SIZE = '2';
context.__ENV.CAPACITY_CATEGORY_CONVERGENCE_TIMEOUT_SECONDS = '1';
context.__ENV.CAPACITY_CATEGORY_PRODUCT_SPREAD = '1';
context.__ENV.CAPACITY_CATEGORY_PRODUCT_SKEW = '1';
context.__ENV.CAPACITY_CATEGORY_DURATION_SECONDS = '60';
const seedCategory = await load('category-hierarchy',
  '\nexport { productRecord, submitProduct, productMembershipWindow, waitForProductMembership, membershipPhase, timestampNanoseconds, productShard };');
delete context.__ENV.CAPACITY_CATEGORY_SUBTREE_SIZE;
delete context.__ENV.CAPACITY_CATEGORY_CONVERGENCE_TIMEOUT_SECONDS;
delete context.__ENV.CAPACITY_CATEGORY_PRODUCT_SPREAD;
delete context.__ENV.CAPACITY_CATEGORY_PRODUCT_SKEW;
delete context.__ENV.CAPACITY_CATEGORY_DURATION_SECONDS;
const fixtureIDs = new Map();
function fixtureID(kind, name) {
  const key = `${kind}/${name}`;
  if (!fixtureIDs.has(key)) fixtureIDs.set(key, Buffer.from(
    `gid://GitStore/${kind}/00000000-0000-0000-0000-${(fixtureIDs.size + 1).toString(16).padStart(12, '0')}`).toString('base64'));
  return fixtureIDs.get(key);
}
let fixtureRootMoved = false;
function fixturePath(name) {
  const path = /-n[1-9]\d*$/.test(name) ? [name.replace(/-n\d+$/, '-n0'), name] : [name];
  if (fixtureRootMoved && /-n\d+$/.test(name)) path.unshift(name.replace(/-n\d+$/, '-anchor'));
  return path;
}
const fixtureProducts = new Map();
let seededProductData;
const categoryResponse = (input, status = 200, errors) => ({
  status, json: () => ({ data: { createCategory: { category: {
    id: fixtureID('CategoryTaxonomy', input.metadata.name),
    metadata: { name: input.metadata.name, namespace: 'default', generation: 1, revision: `main@sha1:${'a'.repeat(40)}` },
    spec: { title: input.spec.title },
  } } }, ...(errors === undefined ? {} : { errors }) }),
});
for (const [status, errors, expectedCreates] of [
  [200, undefined, 3], [200, [], 3], [503, undefined, 5],
  [200, [{ message: 'fixture rejection' }], 5],
]) {
  observedCounters = {};
  now = 1000;
  calls = 0;
  request = (_, __, ___, variables, tags) => {
    if (tags.operation === 'preflight') return success;
    if (tags.operation === 'convergenceProbe') return {
      response: { status: 200 }, body: { data: { category: {
        metadata: { name: variables.name },
        status: { resolved: { path: [variables.name.replace(/-n2$/, '-n0'), variables.name], depth: 1 } },
      } } },
    };
    if (tags.operation === 'membershipCategoryIdentity' || tags.operation === 'categoryMembershipPhase') {
      return { response: { status: 200 }, body: { data: { category: {
        id: fixtureID('CategoryTaxonomy', variables.name),
        metadata: { name: variables.name, namespace: 'default', generation: fixtureRootMoved ? 2 : 1 },
        spec: fixtureRootMoved && /-n0$/.test(variables.name) ?
          { parentRef: { name: variables.name.replace(/-n0$/, '-anchor') } } : {},
        status: { observedGeneration: fixtureRootMoved ? 2 : 1, resolved: { path: fixturePath(variables.name) } },
      } } } };
    }
    if (tags.operation === 'createProduct') {
      const name = variables.input.metadata.name;
      const categoryName = variables.input.spec.categoryRef.name;
      const id = fixtureID('Product', name);
      const product = { id, metadata: { name, namespace: 'default', uid: id, generation: 1,
        resourceVersion: '1', creationTimestamp: '2026-10-15T00:00:00.000000001Z',
        revision: `main@sha1:${'a'.repeat(40)}` }, spec: variables.input.spec,
      status: { observedGeneration: 1, conditions: [{ type: 'Ready', status: 'TRUE', observedGeneration: 1 }],
        resolved: { category: { name: categoryName, uid: fixtureID('CategoryTaxonomy', categoryName),
          path: fixturePath(categoryName) } } } };
      fixtureProducts.set(id, product);
      return { response: { status: 200 }, body: { data: { createProduct: { product } } } };
    }
    if (tags.operation === 'categoryProducts') {
      const products = [...fixtureProducts.values()].filter((product) => product.status.resolved.category.path.includes(variables.name));
      products.sort((a, b) => Buffer.from(b.id, 'base64').toString().localeCompare(Buffer.from(a.id, 'base64').toString()));
      const edges = products.map((node) => ({ cursor: `cursor-${node.id}`, node }));
      return { response: { status: 200, timings: { duration: 0.5 } }, body: { data: { category: {
        id: fixtureID('CategoryTaxonomy', variables.name), products: { edges,
          pageInfo: { hasNextPage: false, hasPreviousPage: false,
            startCursor: edges.length ? edges[0].cursor : null, endCursor: edges.length ? edges[edges.length - 1].cursor : null },
        },
      } } } };
    }
    assert.equal(tags.operation, 'createCategory');
    calls++;
    const response = categoryResponse(variables.input);
    return { response, body: response.json() };
  };
  batches = (requests) => requests.map((entry) => categoryResponse(JSON.parse(entry.body).variables.input, status, errors));
  seededProductData = seedCategory.setup();
  assert.equal(calls, expectedCreates, 'failed batch responses require separately verified mutation acknowledgement');
  assert.equal(observedCounters.category_seed_created, 2);
}
for (const incomplete of [[], [categoryResponse({ metadata: { name: 'incomplete' }, spec: { title: 'incomplete' } })]]) {
  observedCounters = {};
  batches = () => incomplete;
  assert.throws(seedCategory.setup, /seed batch did not return every declared response/);
  assert.equal(observedCounters.category_seed_created, 0);
}

const fixtureRequest = request;
for (const outcome of ['committed', 'wrong-parent', 'wrong-title', 'invalid-revision', 'denied', 'mixed']) {
  observedCounters = {};
  now = 1000;
  const retriedSeeds = new Map();
  let confirmations = 0;
  request = (...args) => {
    const [, , , variables, tags] = args;
    if (tags.operation === 'createCategory' && /-n[12]$/.test(variables.input.metadata.name)) {
      retriedSeeds.set(variables.input.metadata.name, variables.input);
      return { response: { status: 200 }, body: {
        errors: [{ extensions: { code: 'ALREADY_EXISTS' } }],
      } };
    }
    if (tags.operation === 'confirmCategory') {
      confirmations++;
      const input = retriedSeeds.get(variables.name);
      assert.ok(input, 'confirmation must follow this seed request');
      const record = categoryResponse(input).json().data.createCategory.category;
      record.spec = { ...input.spec };
      if (outcome === 'wrong-parent') record.spec.parentRef = { name: 'different-parent' };
      if (outcome === 'wrong-title') record.spec.title = 'different-title';
      if (outcome === 'invalid-revision') record.metadata.revision = '';
      return { response: { status: 200 }, body: { data: { category: record } } };
    }
    return fixtureRequest(...args);
  };
  batches = (requests) => requests.map(() => ({
    status: 200, json: () => ({ errors: [
      { extensions: { code: outcome === 'denied' ? 'FORBIDDEN' : 'CONFLICT' } },
      ...(outcome === 'mixed' ? [{ extensions: { code: 'FORBIDDEN' } }] : []),
    ] }),
  }));
  if (outcome === 'committed') {
    seedCategory.setup();
    assert.equal(observedCounters.category_seed_created, 2,
      'a committed batch conflict must retain its history through the bounded seed retry');
    assert.equal(observedCounters.category_mutation_conflicts, 2);
    assert.equal(confirmations, 2);
    assert.equal(retriedSeeds.size, 2);
  } else {
    assert.throws(seedCategory.setup, /cannot seed/);
    assert.equal(observedCounters.category_seed_created, 0);
    assert.equal(confirmations, outcome === 'denied' || outcome === 'mixed' ? 0 : 1,
      'only a uniform conflict permits exact-record confirmation');
  }
}
request = fixtureRequest;
const initialProductData = JSON.parse(JSON.stringify(seededProductData));
const productRoot = Object.keys(initialProductData.categories).find((name) => name.endsWith('-n0'));
const productLeaf = productRoot.replace(/-n0$/, '-n2');
const productAnchor = productRoot.replace(/-n0$/, '-anchor');
const productEmpty = productRoot.replace(/-n0$/, '-empty');
const copy = (value) => JSON.parse(JSON.stringify(value));
assert.equal(seedCategory.timestampNanoseconds('2026-10-15T00:00:00.000000002Z') -
  seedCategory.timestampNanoseconds('2026-10-15T00:00:00.000000001Z'), 1n);
assert.equal(seedCategory.timestampNanoseconds('2026-10-15T01:00:00.000000001+01:00'),
  seedCategory.timestampNanoseconds('2026-10-15T00:00:00.000000001Z'));
for (const invalid of ['', null, '2026-10-15', '2026-10-15T00:00:00.0000000001Z']) {
  assert.throws(() => seedCategory.timestampNanoseconds(invalid), /invalid creation timestamp/);
}
const productID = initialProductData.products[0].id;
for (const invalid of ['invalid', productID.replace(/=+$/, ''), `${productID}\n`,
  fixtureID('CategoryTaxonomy', productLeaf)]) {
  assert.throws(() => seedCategory.productShard(invalid), /invalid Product identity/);
}

const oracleProducts = Array.from({ length: 205 }, (_, index) => {
  const product = copy(initialProductData.products[0]);
  product.id = fixtureID('Product', `oracle-${index}`);
  product.metadata.uid = product.id;
  product.metadata.name = `oracle-${index}`;
  product.metadata.creationTimestamp = `2026-10-15T00:00:00.00000000${index % 2 + 1}Z`;
  return product;
}).sort((a, b) => {
  const nanosecondsA = Number(a.metadata.creationTimestamp.split('.')[1].slice(0, -1));
  const nanosecondsB = Number(b.metadata.creationTimestamp.split('.')[1].slice(0, -1));
  if (nanosecondsA !== nanosecondsB) return nanosecondsB - nanosecondsA;
  return Buffer.from(b.id, 'base64').toString().localeCompare(Buffer.from(a.id, 'base64').toString());
});
const oracleIDs = new Set(oracleProducts.map((product) => product.id));
calls = 0;
request = () => { calls++; throw new Error('must not dispatch after the deadline'); };
assert.throws(() => seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
  initialProductData.categories, false, false, oracleIDs, now), /exceeded its convergence deadline/);
assert.equal(calls, 0);
function membershipPage(products, variables) {
  const edgeCursor = (product) => `oracle-${product.id}`;
  const backward = variables.last !== null;
  const boundary = backward ? variables.before : variables.after;
  const at = boundary === null ? (backward ? products.length : -1) :
    products.findIndex((product) => edgeCursor(product) === boundary);
  assert.ok(boundary === null || at >= 0, 'the profile must reuse a returned cursor');
  const begin = backward ? Math.max(0, at - 100) : at + 1;
  const end = backward ? at : Math.min(products.length, begin + 100);
  const edges = products.slice(begin, end).map((node) => ({ cursor: edgeCursor(node), node: copy(node) }));
  return { response: { status: 200, timings: { duration: 0.5 } }, body: { data: { category: {
    id: initialProductData.categories[variables.name],
    products: { edges, pageInfo: { hasPreviousPage: begin > 0, hasNextPage: end < products.length,
      startCursor: edges.length ? edges[0].cursor : null, endCursor: edges.length ? edges[edges.length - 1].cursor : null } },
  } } } };
}
for (const backward of [false, true]) {
  observedCounters = {};
  calls = 0;
  request = (_, __, ___, variables) => { calls++; return membershipPage(oracleProducts, variables); };
  assert.equal(seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
    initialProductData.categories, false, backward, oracleIDs), true);
  assert.equal(calls, 3, '205 independently ordered members require three bounded pages in each direction');
  assert.equal(observedCounters.category_product_pages, 3);
}
request = (_, __, ___, variables, ____, params) => {
  assert.equal(params.timeout, '250ms', 'each page request is capped by the remaining oracle deadline');
  return membershipPage(oracleProducts, variables);
};
assert.equal(seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
  initialProductData.categories, false, false, oracleIDs, now + 250), true);
assert.equal(seedCategory.options.scenarios.productMembership.gracefulStop, '0s');
observedCounters = {};
request = (_, __, ___, variables) => membershipPage([], variables);
assert.equal(seedCategory.productMembershipWindow('api', productEmpty, [],
  initialProductData.categories, false, false, oracleIDs), true);
for (const corrupt of [
  (page) => { page.response.status = 503; },
  (page) => { page.body.errors = [{ extensions: { code: 'PROJECTION_NOT_READY' } }, { extensions: { code: 'FORBIDDEN' } }]; },
  (page) => { page.response.timings.duration = -1; },
  (page) => { page.body.data.category.id = 'wrong-category'; },
  (page) => { page.body.data.category.products.edges[1].node = page.body.data.category.products.edges[0].node; },
  (page) => { page.body.data.category.products.edges[1].cursor = page.body.data.category.products.edges[0].cursor; },
  (page) => { page.body.data.category.products.edges[0].node.id = 'foreign-product'; },
  (page) => { page.body.data.category.products.pageInfo.startCursor = 'wrong-cursor'; },
]) {
  request = (_, __, ___, variables) => { const page = membershipPage(oracleProducts, variables); corrupt(page); return page; };
  assert.throws(() => seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
    initialProductData.categories, false, false, oracleIDs), /Category.products/);
}
for (const corrupt of [
  (page) => {
    const edges = page.body.data.category.products.edges;
    [edges[0], edges[1]] = [edges[1], edges[0]];
  },
  (page) => { page.body.data.category.products.pageInfo.hasNextPage = false; },
  (page) => { page.body.data.category.products.edges[0].node.metadata.uid = 'wrong-uid'; },
  (page) => { page.body.data.category.products.edges[0].node.metadata.generation++; },
  (page) => { page.body.data.category.products.edges[0].node.metadata.revision = `main@sha1:${'b'.repeat(40)}`; },
  (page) => { page.body.data.category.products.edges[0].node.metadata.creationTimestamp = '2026-10-15T00:00:00.000000003Z'; },
  (page) => { page.body.data.category.products.edges[0].node.status.observedGeneration = 0; },
  (page) => { page.body.data.category.products.edges[0].node.status.conditions = []; },
  (page) => { page.body.data.category.products.edges[0].node.status.resolved.category.uid = 'wrong-category'; },
  (page) => { page.body.data.category.products.edges[0].node.status.resolved.category.path = ['wrong-path']; },
]) {
  request = (_, __, ___, variables) => {
    const page = membershipPage(oracleProducts, variables);
    corrupt(page);
    const edges = page.body.data.category.products.edges;
    page.body.data.category.products.pageInfo.startCursor = edges[0].cursor;
    page.body.data.category.products.pageInfo.endCursor = edges[edges.length - 1].cursor;
    return page;
  };
  assert.equal(seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
    initialProductData.categories, false, false, oracleIDs), false);
}
request = () => ({ response: { status: 200 }, body: { errors: [{ extensions: { code: 'PROJECTION_NOT_READY' } }] } });
assert.equal(seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
  initialProductData.categories, false, false, oracleIDs), false);
calls = 0;
request = (_, __, ___, variables) => {
  const edges = oracleProducts.slice(calls++ * 20, calls * 20).map((node) =>
    ({ cursor: `budget-${node.id}`, node }));
  return { response: { status: 200, timings: { duration: 1 } }, body: { data: { category: {
    id: initialProductData.categories[variables.name], products: { edges, pageInfo: {
      hasPreviousPage: calls > 1, hasNextPage: true, startCursor: edges[0].cursor, endCursor: edges[edges.length - 1].cursor,
    } },
  } } } };
};
assert.throws(() => seedCategory.productMembershipWindow('api', productLeaf, oracleProducts,
  initialProductData.categories, false, false, oracleIDs), /bounded membership-page budget/);
assert.equal(calls, 4);

function resetProductFixture() {
  fixtureRootMoved = false;
  fixtureProducts.clear();
  for (const product of initialProductData.products) fixtureProducts.set(product.id, copy(product));
  now = 1000;
  observedCounters = {};
  observedChecks = [];
}
let productWriteSequence;
resetProductFixture();
let pendingMembershipReads = 0;
sleepRequest = (seconds) => { now += seconds * 1000; };
request = (...args) => {
  if (args[4].operation === 'categoryProducts' && pendingMembershipReads++ < 4) {
    return { response: { status: 200 }, body: { errors: [{ extensions: { code: 'PROJECTION_NOT_READY' } }] } };
  }
  return fixtureRequest(...args);
};
assert.equal(seedCategory.waitForProductMembership(copy(initialProductData.products),
  initialProductData.categories, 1000), 200);
assert.equal(pendingMembershipReads, 20,
  'an incomplete poll validates its pair then stops; acceptance still checks all 16 connections');
assert.equal(observedCounters.category_product_oracle_attempts, 3);
assert.equal(observedCounters.category_product_oracle_checks, 16);
for (const phase of ['categoryMembershipPhase', 'categoryProducts']) {
  resetProductFixture();
  let unavailable = true;
  request = (...args) => {
    if (args[4].operation === phase && unavailable) {
      unavailable = false;
      return { response: { status: 0 }, body: { errors: [{ message: 'non-JSON GraphQL response' }] } };
    }
    return fixtureRequest(...args);
  };
  assert.equal(seedCategory.waitForProductMembership(copy(initialProductData.products),
    initialProductData.categories, 1000), 100, 'transient reads retry inside the unchanged oracle deadline');
  assert.equal(observedCounters.category_product_oracle_transient_reads, 1);
  assert.equal(observedCounters.category_product_oracle_checks, 16, 'only complete independently verified windows count');
}
for (const status of [0, 408, 429, 502, 503, 504]) {
  resetProductFixture();
  request = () => ({ response: { status }, body: { errors: [{ message: 'non-JSON GraphQL response' }] } });
  assert.throws(() => seedCategory.waitForProductMembership(copy(initialProductData.products),
    initialProductData.categories, 250), /did not converge|exceeded its convergence deadline/);
  assert.ok(observedCounters.category_product_oracle_transient_reads > 0);
  assert.equal(observedCounters.category_product_oracle_checks ?? 0, 0);
}
for (const status of [200, 400, 401, 403, 404, 500, 503]) {
  resetProductFixture();
  request = () => ({ response: { status }, body: { errors: [{ extensions: { code: 'FORBIDDEN' } }] } });
  assert.throws(() => seedCategory.waitForProductMembership(copy(initialProductData.products),
    initialProductData.categories, 1000), /acknowledged root identity/);
  assert.equal(observedCounters.category_product_oracle_transient_reads ?? 0, 0,
    'domain, authorization and malformed acknowledgements are never disguised as transport recovery');
}
resetProductFixture();
request = (endpoint, ...args) => endpoint === 'http://api-a/graphql' ?
  { response: { status: 0 }, body: { errors: [{ message: 'non-JSON GraphQL response' }] } } :
  { response: { status: 200 }, body: { data: { category: { id: 'wrong-incarnation' } } } };
assert.throws(() => seedCategory.membershipPhase(initialProductData.categories, now + 1000),
  /acknowledged root identity/, 'one unavailable replica cannot hide a mismatching peer identity');
assert.equal(observedCounters.category_product_oracle_transient_reads, 1);
assert.ok(transportWarnings.some((message) => message.includes('awaits transport recovery (HTTP 0)')));
resetProductFixture();
request = (endpoint, token, query, variables, tags) => {
  if (tags.operation === 'categoryProducts') {
    const members = variables.name === productLeaf || variables.name === productRoot ? oracleProducts : [];
    return membershipPage(members, variables);
  }
  return fixtureRequest(endpoint, token, query, variables, tags);
};
assert.equal(seedCategory.waitForProductMembership(oracleProducts,
  initialProductData.categories, 1000), 0);
assert.equal(observedCounters.category_product_pages, 32,
  'paired first pages must not replace cursor-bound continuation pages for 205 members');
assert.equal(observedCounters.category_product_oracle_checks, 16);
resetProductFixture();
request = (endpoint, token, query, variables, tags) => {
  if (tags.operation === 'categoryProducts') {
    if (endpoint === 'http://api-a/graphql') {
      return { response: { status: 200 }, body: { errors: [{ extensions: { code: 'PROJECTION_NOT_READY' } }] } };
    }
    return { response: { status: 200 }, body: null };
  }
  return fixtureRequest(endpoint, token, query, variables, tags);
};
assert.throws(() => seedCategory.waitForProductMembership(copy(initialProductData.products),
  initialProductData.categories, 1000), /unsuccessful or malformed/,
  'a pending first replica must not hide an invalid second replica response');
resetProductFixture();
request = (...args) => {
  const result = fixtureRequest(...args);
  now += 1001;
  return result;
};
assert.throws(() => seedCategory.waitForProductMembership(copy(initialProductData.products),
  initialProductData.categories, 1000), /convergence deadline/,
  'a paired response received beyond the absolute deadline cannot be accepted');
const productWorkerRequest = (endpoint, token, query, variables, tags) => {
  fixtureRootMoved = now >= 31000;
  if (tags.operation === 'readProductBeforeUpdate' || tags.operation === 'updateProduct') {
    const name = variables.name || variables.input.metadata.name;
    let record = [...fixtureProducts.values()].find((product) => product.metadata.name === name);
    assert.ok(record, 'only the acknowledged fixture may be updated');
    record = copy(record);
    if (tags.operation === 'updateProduct') {
      assert.deepEqual(Object.keys(variables.input.metadata).sort(), ['name', 'namespace'],
        'Product spec mutations use ObjectMetaInput, not status-patch resourceVersion');
      record.metadata.generation++;
      record.metadata.resourceVersion = String(Number(record.metadata.resourceVersion) + 1);
      record.metadata.revision = `main@sha1:${(++productWriteSequence).toString(16).padStart(40, '0')}`;
      record.spec = copy(variables.input.spec);
    }
    record.status.observedGeneration = record.metadata.generation;
    record.status.conditions[0].observedGeneration = record.metadata.generation;
    record.status.resolved.category = { name: record.spec.categoryRef.name,
      uid: initialProductData.categories[record.spec.categoryRef.name], path: fixturePath(record.spec.categoryRef.name) };
    fixtureProducts.set(record.id, record);
    return { response: { status: 200 }, body: { data: tags.operation === 'updateProduct' ?
      { updateProduct: { product: record } } : { product: record } } };
  }
  if (tags.operation === 'categoryProducts') {
    for (const product of fixtureProducts.values()) product.status.resolved.category.path = fixturePath(product.spec.categoryRef.name);
  }
  return fixtureRequest(endpoint, token, query, variables, tags);
};
resetProductFixture();
productWriteSequence = 0;
sleepRequest = (seconds) => { now += seconds * 1000; };
request = productWorkerRequest;
const workerData = copy(initialProductData);
seedCategory.reassignProducts(workerData);
assert.equal(observedCounters.category_product_offered, 60);
assert.equal(observedCounters.category_product_completed, 60);
assert.equal(observedCounters.category_product_reassigned, 30);
assert.equal(observedCounters.category_product_oracle_checks, 960);
assert.equal(observedCounters.category_product_missed_schedules || 0, 0);
assert.equal(initialProductData.products[0].metadata.generation, 1,
  'the worker must receive an isolated copy of the setup ledger, as in native k6');
request = (endpoint, token, query, variables, tags) => {
  if (tags.operation === 'auditList') throw new Error('fixture Category audit boundary');
  return productWorkerRequest(endpoint, token, query, variables, tags);
};
assert.throws(() => seedCategory.teardown(copy(initialProductData)), /fixture Category audit boundary/);
assert.equal(observedCounters.category_product_oracle_checks, 976,
  'teardown must independently reconstruct all updates and require moved membership before the Category audit');

for (const corrupt of [
  (record) => { record.metadata.uid = 'wrong-uid'; },
  (record) => { record.metadata.generation++; },
  (record) => { record.spec.title = 'foreign-title'; },
  (record) => { record.metadata.creationTimestamp = '2026-10-15T00:00:01Z'; },
]) {
  resetProductFixture();
  productWriteSequence = 0;
  request = (...args) => {
    const result = productWorkerRequest(...args);
    if (args[4].operation === 'updateProduct') corrupt(result.body.data.updateProduct.product);
    return result;
  };
  assert.throws(() => seedCategory.reassignProducts(copy(initialProductData)), /did not acknowledge/);
  assert.equal(observedCounters.category_product_offered, 1);
  assert.equal(observedCounters.category_product_failed, 1);
  assert.equal(observedCounters.category_product_completed || 0, 0);
}
resetProductFixture();
productWriteSequence = 0;
request = (...args) => {
  const result = productWorkerRequest(...args);
  if (args[4].operation === 'updateProduct') {
    result.body.data.updateProduct.product.metadata.revision = `main@sha1:${'b'.repeat(40)}`;
  }
  return result;
};
assert.throws(() => seedCategory.reassignProducts(copy(initialProductData)), /same supposedly own commit twice/);
assert.equal(observedCounters.category_product_offered, 2);
assert.equal(observedCounters.category_product_completed, 1);
assert.equal(observedCounters.category_product_failed, 1);
for (const committed of [false, true]) {
  resetProductFixture();
  productWriteSequence = 0;
  let updates = 0;
  const endpoints = [];
  request = (...args) => {
    endpoints.push(args[0]);
    if (args[4].operation === 'updateProduct' && updates++ === 0) {
      if (committed) productWorkerRequest(...args);
      return { response: { status: 200 }, body: { errors: [{ extensions: { code: 'CONFLICT' } }] } };
    }
    return productWorkerRequest(...args);
  };
  const previous = copy(initialProductData.products[0]);
  const record = seedCategory.submitProduct(previous, previous.metadata.name, 'owned-retry-title', productLeaf, 0);
  assert.equal(record.metadata.generation, previous.metadata.generation + 1);
  assert.equal(record.spec.title, 'owned-retry-title');
  assert.equal(updates, committed ? 1 : 2);
  assert.equal(endpoints[0], endpoints[1]);
  assert.notEqual(endpoints[0], endpoints[2], 'typed conflicts must retry through the other API');
}
for (const rejected of [
  { response: { status: 422 }, body: { errors: [{ extensions: { code: 'GRAPHQL_VALIDATION_FAILED' } }] } },
  { response: { status: 200 }, body: { errors: [{ extensions: { code: 'CONFLICT' } }] } },
  { response: { status: 503 }, body: { errors: [{ extensions: { code: 'CONFLICT' } }] } },
  { response: { status: 200 }, body: { errors: [
    { extensions: { code: 'CONFLICT' } }, { extensions: { code: 'FORBIDDEN' } },
  ] } },
]) {
  resetProductFixture();
  calls = 0;
  request = (...args) => {
    if (args[4].operation === 'updateProduct') { calls++; return rejected; }
    return productWorkerRequest(...args);
  };
  const previous = copy(initialProductData.products[0]);
  assert.throws(() => seedCategory.submitProduct(previous, previous.metadata.name,
    'owned-retry-title', productLeaf, 0), /bounded conflict retries|did not acknowledge/);
  assert.equal(calls, rejected.response.status === 200 && rejected.body.errors.length === 1 ? 4 : 1);
}
resetProductFixture();
request = (endpoint, token, query, variables, tags) => {
  const result = fixtureRequest(endpoint, token, query, variables, tags);
  if (tags.operation === 'categoryProducts') {
    result.body.data.category.products.edges = [];
    result.body.data.category.products.pageInfo = {
      hasNextPage: false, hasPreviousPage: false, startCursor: null, endCursor: null,
    };
  }
  return result;
};
assert.throws(() => seedCategory.waitForProductMembership(initialProductData.products,
  initialProductData.categories, 1000, false), /did not converge/);
sleepRequest = () => {};
resetProductFixture();

const categoryMutation = (_, __, ___, variables) => ({
  response: { status: 200 },
  body: { data: { createCategory: { category: {
    metadata: { name: variables.input.metadata.name, revision: `main@sha1:${'a'.repeat(40)}` },
    spec: { title: variables.input.spec.title },
  } } } },
});
observedCounters = {};
observedChecks = [];
request = categoryMutation;
category.mutate();
assert.equal(observedCounters.category_mutation_offered, 1);
assert.equal(observedCounters.category_mutation_acknowledged, 1);
assert.equal(observedCounters.category_mutation_missed_schedules || 0, 0);
assert.equal(observedCounters.category_load_start_ms, 1000);
assert.equal(observedCounters.category_load_end_ms, 601000);
assert.ok(observedChecks.every(Boolean));
for (const response of [
  { response: { status: 503 }, body: {} },
  { response: { status: 200 }, body: { errors: [{ message: 'fixture rejection' }] } },
]) {
  observedCounters = {};
  observedChecks = [];
  request = () => response;
  category.mutate();
  assert.equal(observedCounters.category_mutation_offered, 1);
  assert.equal(observedCounters.category_mutation_failed, 1);
  assert.equal(observedCounters.category_mutation_acknowledged || 0, 0);
  assert.equal(observedChecks.every(Boolean), false);
}
for (const valid of [true, false]) {
  observedCounters = {};
  observedChecks = [];
  request = () => valid ? page(['one']) : { response: { status: 200 }, body: { data: { categories: null } } };
  category.readFiltered();
  assert.equal(observedCounters.category_filtered_read_offered, 1);
  assert.equal(observedCounters.category_filtered_read_completed || 0, valid ? 1 : 0);
  assert.equal(observedCounters.category_filtered_read_failed || 0, valid ? 0 : 1);
  assert.equal(observedChecks.every(Boolean), valid);
}
for (const invalid of [
  page(['duplicate', 'duplicate']), page(['']), page(['one'], true, ''),
  { response: { status: 200 }, body: { data: { categories: { edges: [null], pageInfo: { hasNextPage: false } } } } },
  { response: { status: 200 }, body: { data: { categories: { edges: [] } } } },
]) {
  observedCounters = {};
  observedChecks = [];
  request = () => invalid;
  category.readFiltered();
  assert.equal(observedCounters.category_filtered_read_completed || 0, 0);
  assert.equal(observedCounters.category_filtered_read_failed, 1);
  assert.equal(observedChecks.every(Boolean), false);
}
for (const [operation, offered, completed] of [
  [category.mutate, 'category_mutation_offered', 'category_mutation_acknowledged'],
  [category.readFiltered, 'category_filtered_read_offered', 'category_filtered_read_completed'],
]) {
  observedCounters = {};
  request = () => { throw new Error('fixture request failure'); };
  assert.throws(operation, /fixture request failure/);
  assert.equal(observedCounters[offered], 1);
  assert.equal(observedCounters[completed] || 0, 0, 'an exception must leave an observable incomplete offer');
}
for (const [operation, offered, missed, interval, response] of [
  [category.mutate, 'category_mutation_offered', 'category_mutation_missed_schedules', 50, categoryMutation],
  [category.readFiltered, 'category_filtered_read_offered', 'category_filtered_read_missed_schedules', 100, () => page(['one'])],
]) {
  for (const late of [false, true]) {
    now = 1000 + interval - (late ? 0 : 1);
    observedCounters = {};
    observedChecks = [];
    request = response;
    operation();
    assert.equal(observedCounters[missed] || 0, late ? 1 : 0, 'one full interval of lateness must be retained');
  }
  now = 601000;
  observedCounters = {};
  calls = 0;
  request = () => { calls++; return response(); };
  assert.throws(operation, /after its declared load window/);
  assert.equal(calls, 0, 'expired offers must not start GraphQL work');
  assert.equal(observedCounters[offered], 1);
  assert.equal(observedCounters[missed], 1);
}
now = 1000;

for (const [operation, offered, endpoint, planned] of [
  [category.mutate, 'category_mutation_offered', 'category_mutation_endpoint_callbacks', 12000],
  [category.readFiltered, 'category_filtered_read_offered', 'category_filtered_read_endpoint_callbacks', 6000],
]) {
  stubs['k6/execution'].default.scenario.iterationInTest = planned;
  now = 601005;
  observedCounters = {};
  calls = 0;
  request = () => { calls++; throw new Error('endpoint callback must not send a request'); };
  operation();
  assert.equal(calls, 0);
  assert.equal(observedCounters[offered] || 0, 0, 'the exclusive endpoint is not a planned offer');
  assert.equal(observedCounters[endpoint], 1);
  for (const [sequence, clock] of [[planned + 1, 601005], [planned, 600999]]) {
    stubs['k6/execution'].default.scenario.iterationInTest = sequence;
    now = clock;
    observedCounters = {};
    assert.throws(operation, /invalid declared schedule identity/);
    assert.equal(observedCounters[offered], 1, 'only the exact exclusive endpoint may be excluded');
    assert.equal(observedCounters[endpoint] || 0, 0);
    assert.equal(calls, 0);
  }
}
stubs['k6/execution'].default.scenario.iterationInTest = 0;
now = 1000;

const product = await load('product-lifecycle');
let name;
const productRequest = (_, __, query, variables) => {
  let data;
  if (query.includes('createProduct(')) {
    name = variables.input.metadata.name;
    data = { createProduct: { product: { id: 'product-id', metadata: { name } } } };
  } else if (query.includes('deleteProduct(')) {
    data = { deleteProduct: { outcome: 'TERMINATION_STARTED' } };
  } else if (query.includes('product(by:')) {
    data = { product: { id: 'product-id', metadata: { name } } };
  } else {
    data = { __typename: 'Query' };
  }
  return { response: { status: 200 }, body: { data } };
};
request = productRequest;
const deletion = (valid) => ({
  status: valid ? 200 : 503,
  json: () => ({ data: { deleteProduct: { outcome: 'ALREADY_TERMINATING' } } }),
});
for (const responses of [
  [deletion(true), deletion(true)], [deletion(true), deletion(false)],
  [deletion(false), deletion(true)], [],
]) {
  observedCounters = {};
  observedChecks = [];
  batches = () => responses;
  product.default();
  const complete = responses.length === 2 && responses.every((response) => response.status === 200);
  assert.equal(observedCounters.product_lifecycle_offered, 1);
  assert.equal(observedChecks.every(Boolean), complete);
  assert.equal(observedCounters.product_lifecycle_deletion_race_checks || 0, complete ? 1 : 0);
  assert.equal((observedCounters.product_lifecycle_deletion_replica_checks || 0) === 2, complete);
}
for (const peerID of ['different-peer-id', '', null]) {
  observedCounters = {};
  observedChecks = [];
  batches = () => [deletion(true), deletion(true)];
  request = (...args) => {
    const result = productRequest(...args);
    if (args[4].operation === 'productLifecycleCrossReplicaRead') result.body.data.product.id = peerID;
    return result;
  };
  product.default();
  assert.equal(observedChecks.every(Boolean), false, 'Product peer visibility must identify the acknowledged UID');
  assert.equal(observedCounters.product_lifecycle_deletion_race_checks || 0, 0);
}
for (const stage of [
  'productLifecyclePreflight', 'productLifecycleCreate', 'productLifecycleCrossReplicaRead', 'productLifecycleDeletionStart',
]) {
  for (const body of [null, 'invalid', {}, { data: null }, { data: {} }]) {
    observedCounters = {};
    observedChecks = [];
    request = (...args) => args[4].operation === stage ?
      { response: { status: 200 }, body } : productRequest(...args);
    batches = () => [deletion(true), deletion(true)];
    product.default();
    assert.equal(observedCounters.product_lifecycle_offered, 1);
    assert.equal(observedChecks.every(Boolean), false, `${stage} must reject malformed data without throwing`);
  }
  observedCounters = {};
  observedChecks = [];
  request = (...args) => {
    const result = productRequest(...args);
    if (args[4].operation === stage) result.body.errors = [{ message: 'fixture rejection' }];
    return result;
  };
  product.default();
  assert.equal(observedChecks.every(Boolean), false, `${stage} must reject partial GraphQL error data`);
  observedCounters = {};
  request = (...args) => {
    if (args[4].operation === stage) throw new Error('fixture request failure');
    return productRequest(...args);
  };
  assert.throws(product.default, /fixture request failure/);
  assert.equal(observedCounters.product_lifecycle_offered, 1);
  assert.equal(observedCounters.product_lifecycle_deletion_race_checks || 0, 0);
}
for (const body of [
  null, 'invalid', {}, { data: { deleteProduct: { outcome: 'ALREADY_TERMINATING' } }, errors: [{ message: 'fixture rejection' }] },
]) {
  observedCounters = {};
  observedChecks = [];
  request = productRequest;
  batches = () => [{ status: 200, json: () => body }, deletion(true)];
  product.default();
  assert.equal(observedChecks.every(Boolean), false, 'Product deletion race must reject malformed or partial responses');
  assert.equal(observedCounters.product_lifecycle_deletion_race_checks || 0, 0);
}
observedCounters = {};
observedChecks = [];
request = (...args) => {
  const result = productRequest(...args);
  result.body.errors = [];
  return result;
};
batches = () => [0, 1].map(() => ({
  status: 200, json: () => ({ data: { deleteProduct: { outcome: 'ALREADY_TERMINATING' } }, errors: [] }),
}));
product.default();
assert.equal(observedCounters.stub_checks_passes, 6);
assert.equal(observedCounters.stub_checks_fails, 0);
assert.equal(observedCounters.product_lifecycle_offered, 1);
assert.equal(observedCounters.product_lifecycle_deletion_race_checks, 1);
assert.equal(observedCounters.product_lifecycle_deletion_replica_checks, 2);

stubs['k6/execution'].default.scenario.name = 'sustained';
stubs['k6/execution'].default.scenario.startTime = 1000;
stubs['k6/execution'].default.scenario.progress = 0;
stubs.k6.sleep = (seconds) => { now += seconds * 1000; };
const namespace = await load('namespace-admission');
stubs['k6/execution'].default.test = { options: namespace.options };
request = () => success;
const namespaceData = namespace.setup();
assert.equal(namespaceData.schedules.sustained.offers, 6000);
assert.equal(namespaceData.schedules.sustained.durationNs, '600000000000');
for (const [duration, nanoseconds, offers] of [
  ['1s', '1000000000', 10],
  ['1.501s', '1501000000', 16],
  ['1.000000001s', '1000000001', 11],
  ['1h2m3.000000001s', '3723000000001', 37231],
  ['2562047h47m16.854775807s', '9223372036854775807', 92233720369],
]) {
  stubs['k6/execution'].default.test.options = { scenarios: {
    sustained: { ...namespace.options.scenarios.sustained, duration },
  } };
  const data = namespace.setup();
  assert.equal(data.schedules.sustained.durationNs, nanoseconds);
  assert.equal(data.schedules.sustained.offers, offers);
}
for (const invalid of [
  { duration: 'invalid' }, { duration: '' }, { duration: '0s' }, { duration: null },
  { duration: '1ms' }, { duration: '100000000000000000h0m0s' },
  { rate: 1 }, { rate: 10.5 }, { timeUnit: '2s' }, { executor: 'shared-iterations' },
]) {
  stubs['k6/execution'].default.test.options = { scenarios: {
    sustained: { ...namespace.options.scenarios.sustained, ...invalid },
  } };
  assert.throws(namespace.setup, /Namespace|Capacity scenario/);
}
stubs['k6/execution'].default.test.options = { scenarios: {} };
assert.throws(namespace.setup, /effective scenarios/);
stubs['k6/execution'].default.test.options = namespace.options;
const createdNamespace = (variables) => ({
  response: { status: 200 },
  body: { data: { createNamespace: { namespace: { id: 'fixture-id', metadata: { name: variables.input.metadata.name } } } } },
});
const namespaceFound = { response: { status: 200 }, body: { data: { namespace: { id: 'fixture-id' } } } };
const healthyNamespaceRequest = (_, __, ___, variables, tags) =>
  tags.operation === 'createNamespace' ? createdNamespace(variables) : namespaceFound;
request = healthyNamespaceRequest;
now = 1000;
for (const delay of [99, 100]) {
  observedCounters = {};
  observedChecks = [];
  now = 1000 + delay;
  stubs['k6/execution'].default.scenario.progress = delay / 600000;
  namespace.default(namespaceData);
  assert.equal(observedCounters.gitstore_namespace_offered, 1);
  assert.equal(observedCounters.gitstore_namespace_missed_schedules || 0, delay === 100 ? 1 : 0);
  assert.equal(observedCounters.stub_checks_passes, 3);
}
observedCounters = {};
calls = 0;
request = () => { calls++; throw new Error('exclusive endpoint must not perform HTTP work'); };
stubs['k6/execution'].default.scenario.iterationInTest = 6000;
stubs['k6/execution'].default.scenario.progress = 1;
namespace.default(namespaceData);
assert.equal(observedCounters.gitstore_namespace_endpoint_callbacks, 1);
assert.equal(observedCounters.gitstore_namespace_offered || 0, 0);
assert.equal(calls, 0);
for (const invalid of [
  { iterationInTest: 6001 }, { iterationInTest: -1 }, { iterationInTest: 0.5 },
  { iterationInTest: 6000, progress: 0.99 }, { iterationInTest: 0, startTime: 0 },
  { iterationInTest: 0, progress: undefined }, { iterationInTest: 0, progress: -0.1 },
  { iterationInTest: 0, progress: 1.1 }, { iterationInTest: 0, name: 'undeclared' },
]) {
  observedCounters = {};
  stubs['k6/execution'].default.scenario = {
    name: 'sustained', iterationInTest: 0, startTime: 1000, progress: 0, ...invalid,
  };
  assert.throws(() => namespace.default(namespaceData), /invalid declared schedule identity/);
  assert.equal(observedCounters.gitstore_namespace_offered, 1);
  assert.equal(calls, 0);
}
stubs['k6/execution'].default.scenario = {
  name: 'sustained', iterationInTest: 0, startTime: 1000, progress: 0,
};
observedCounters = {};
stubs['k6/execution'].default.scenario.progress = 1;
assert.throws(() => namespace.default(namespaceData), /after its declared load window/);
assert.equal(observedCounters.gitstore_namespace_offered, 1);
assert.equal(observedCounters.gitstore_namespace_missed_schedules, 1);
assert.equal(calls, 0);
stubs['k6/execution'].default.scenario.progress = 0;
const conflict = { response: { status: 200 },
  body: { errors: [{ extensions: { code: 'CONFLICT' } }] } };
const invalidConfirmations = [
  { ...namespaceFound, response: { status: 503 } },
  { ...namespaceFound, body: { ...namespaceFound.body, errors: [{ message: 'fixture rejection' }] } },
  { ...namespaceFound, body: { ...namespaceFound.body, errors: {} } },
  ...[null, 'invalid', {}, { data: { namespace: null } },
    { data: { namespace: { id: 42 } } }, { data: { namespace: { id: '' } } }].map((body) =>
    ({ response: { status: 200 }, body })),
];
for (const body of [{ data: { __typename: 'Query' } }, { data: { __typename: 'Query' }, errors: [] }]) {
  observedCounters = {};
  request = () => ({ response: { status: 200 }, body });
  namespace.setup();
  assert.equal(observedCounters.gitstore_namespace_offered, 0);
}
for (const invalid of invalidConfirmations) {
  observedCounters = {};
  request = () => invalid;
  assert.throws(namespace.setup, /GraphQL preflight failed/);
  for (const stage of ['confirmNamespace', 'confirmVisibility']) {
    observedCounters = {};
    observedChecks = [];
    now = 1000;
    request = (_, __, ___, variables, tags) => {
      if (tags.operation === 'createNamespace') return stage === 'confirmNamespace' ? conflict : createdNamespace(variables);
      assert.equal(tags.operation, stage);
      now += 1000;
      return invalid;
    };
    namespace.default(namespaceData);
    assert.equal(observedChecks.every(Boolean), false, `${stage} must reject partial or malformed confirmations`);
    assert.equal(observedCounters.gitstore_namespace_offered, 1);
    assert.equal(observedCounters.gitstore_namespace_graphql_failed_samples, 1);
    assert.equal(observedCounters.gitstore_namespace_admitted || 0, stage === 'confirmVisibility' ? 1 : 0);
    if (stage === 'confirmNamespace') {
      assert.equal(observedCounters.gitstore_namespace_graphql_failed, 1);
      assert.equal(observedCounters.gitstore_namespace_conflicts, 3);
    }
  }
}
for (const mutationResult of [
  { response: { status: 503 }, body: null },
  { response: { status: 200 }, body: null },
  { response: { status: 200 }, body: { data: { createNamespace: { namespace: null } } } },
  { response: { status: 200 }, body: { errors: [{ message: '{"code":"CONFLICT"}' }] } },
  { response: { status: 200 }, body: { errors: [...conflict.body.errors, { extensions: { code: 'FORBIDDEN' } }] } },
]) {
  observedCounters = {};
  observedChecks = [];
  calls = 0;
  request = () => { calls++; return mutationResult; };
  namespace.default(namespaceData);
  assert.equal(calls, 1, 'only uniformly classified conflicts may trigger confirmation retries');
  assert.equal(observedCounters.gitstore_namespace_graphql_failed, 1);
  assert.equal(observedCounters.gitstore_namespace_admitted || 0, 0);
  assert.equal(observedChecks.every(Boolean), false);
}
for (const field of ['missing-id', 'empty-id', 'numeric-id', 'wrong-name']) {
  observedCounters = {};
  observedChecks = [];
  calls = 0;
  request = (_, __, ___, variables) => {
    calls++;
    const result = createdNamespace(variables);
    const payload = result.body.data.createNamespace.namespace;
    if (field === 'missing-id') delete payload.id;
    if (field === 'empty-id') payload.id = '';
    if (field === 'numeric-id') payload.id = 42;
    if (field === 'wrong-name') payload.metadata.name = 'wrong-name';
    return result;
  };
  namespace.default(namespaceData);
  assert.equal(calls, 1);
  assert.equal(observedCounters.gitstore_namespace_graphql_failed, 1);
  assert.equal(observedCounters.gitstore_namespace_admitted || 0, 0);
  assert.equal(observedChecks.every(Boolean), false);
}
for (const conflictFirst of [false, true]) {
  observedCounters = {};
  observedChecks = [];
  request = (_, __, ___, variables, tags) =>
    tags.operation === 'createNamespace' ? (conflictFirst ? conflict : createdNamespace(variables)) : namespaceFound;
  namespace.default(namespaceData);
  assert.equal(observedCounters.gitstore_namespace_offered, 1);
  assert.equal(observedCounters.gitstore_namespace_graphql_failed_samples, 1);
  assert.equal(observedCounters.gitstore_namespace_graphql_failed, 0);
  assert.equal(observedCounters.gitstore_namespace_admitted, 1);
  assert.equal(observedCounters.stub_checks_passes, 3);
  assert.equal(observedCounters.stub_checks_fails, 0);
  assert.ok(observedChecks.every(Boolean));
}
for (const conflictFirst of [false, true]) {
  observedCounters = {};
  observedChecks = [];
  now = 1000;
  request = (endpoint, __, ___, variables, tags) => {
    if (tags.operation === 'createNamespace') return conflictFirst ? conflict : createdNamespace(variables);
    if (tags.operation === 'confirmNamespace') return namespaceFound;
    assert.equal(tags.operation, 'confirmVisibility');
    now += 1000;
    return { response: { status: 200 }, body: { data: {
      namespace: { id: endpoint === 'http://api-a/graphql' ? 'fixture-id' : 'different-peer-id' },
    } } };
  };
  namespace.default(namespaceData);
  assert.equal(observedChecks.every(Boolean), false, 'replicas must expose the same acknowledged Namespace UID');
}
for (const stage of ['createNamespace', 'confirmNamespace', 'confirmVisibility']) {
  observedCounters = {};
  observedChecks = [];
  request = (_, __, ___, variables, tags) => {
    if (tags.operation === stage) throw new Error('fixture request failure');
    return tags.operation === 'createNamespace' ? (stage === 'confirmNamespace' ? conflict : createdNamespace(variables)) : namespaceFound;
  };
  assert.throws(() => namespace.default(namespaceData), /fixture request failure/);
  assert.equal(observedCounters.gitstore_namespace_offered, 1);
  assert.equal(observedCounters.gitstore_namespace_graphql_failed_samples || 0, stage === 'confirmVisibility' ? 1 : 0);
  assert.equal(observedCounters.gitstore_namespace_visibility_ms || 0, 0);
}

context.__ENV.CAPACITY_TOKEN = 'fixture';
context.__ENV.CAPACITY_API_ENDPOINTS = 'http://api-a,http://api-b';
stubs['k6/execution'].default.scenario.name = 'readiness_0';
const authSnapshot = { auth: {
  authn: ['static-users'], authz: 'rbac-local', userdir: 'static-users', catalogDigest: 'a'.repeat(64),
  policy: { loaded: true, apiVersion: 'gitstore.dev/v1beta1', digest: 'b'.repeat(64), roles: 1, bindings: 1, rules: 1 },
} };
getRequest = () => ({ status: 200, json: () => authSnapshot });
const readiness = await load('api-readiness');
const readinessData = readiness.setup();
const allowedList = { response: { status: 200 }, body: {
  data: { namespaces: { edges: [{ node: { id: 'namespace-id' } }] } }, errors: [],
} };
const deniedList = { response: { status: 200 },
  body: { data: null, errors: [{ extensions: { code: 'FORBIDDEN' } }] } };
for (const denied of [deniedList,
  ...[null, {}, { errors: [null] }, { errors: [{ message: 'fixture rejection' }] }].map((body) =>
    ({ response: { status: 200 }, body })),
]) {
  observedCounters = {};
  observedChecks = [];
  request = (_, credential) => credential === 'fixture' ? allowedList : denied;
  readiness.default(readinessData);
  assert.equal(observedCounters.readiness_authz_attempts_0, 1);
  assert.equal(observedCounters.readiness_authz_mismatches, denied === deniedList ? 0 : 1);
  assert.equal(observedChecks.every(Boolean), denied === deniedList);
}
for (const allowed of [
  { ...allowedList, response: { status: 503 } },
  { ...allowedList, body: { ...allowedList.body, errors: [{ message: 'fixture rejection' }] } },
  { response: { status: 200 }, body: null },
]) {
  observedCounters = {};
  observedChecks = [];
  request = (_, credential) => credential === 'fixture' ? allowed : deniedList;
  readiness.default(readinessData);
  assert.equal(observedCounters.readiness_authz_attempts_0, 1);
  assert.equal(observedCounters.readiness_authz_mismatches, 1);
  assert.equal(observedChecks.every(Boolean), false);
}
JS

MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=deployed-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null
metadata="${test_dir}/evidence/namespace/watch/alpha/deployed-run/metadata.json"
jq -e '.passed == true and .postflightRequired == true and .postflightExitCode == 0 and .datastoreVerifierExitCode == 0' "${metadata}" >/dev/null
jq -e '(.topology.liveApiReplicas | length) == 2 and (.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/preflight-environment.json" >/dev/null
jq -e '(.topology.liveApiReplicas | length) == 2 and (.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/postflight-environment.json" >/dev/null
jq -e 'length == 3 and all(.[]; .memoryLimitBytes == 3221225472 and .smpPerNode == 2 and .scyllaLiveNodes == 3)' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/datastore-before.json" >/dev/null

MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=repository-deployed-run \
  CAPACITY_CONFIG_MANIFEST="${test_dir}/alpha-config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${test_dir}/alpha-environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  REPOSITORY_API_A=http://api-a.internal REPOSITORY_API_B=http://api-b.internal \
  REPOSITORY_API_REPLACEMENT=http://api-a.internal REPOSITORY_REPLACEMENT_TRIGGER_FILE="${test_dir}/repository-replacement-trigger" \
  REPOSITORY_CONTROLLER_A=http://controller-a.internal REPOSITORY_CONTROLLER_B=http://controller-b.internal \
  REPOSITORY_TOKEN=test-token REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS=256 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINERS=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/repository-local-service-containers.json" \
  MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/alpha-scylla-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=1 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=1610612736 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" repository lifecycle alpha >/dev/null
metadata="${test_dir}/evidence/repository/lifecycle/alpha/repository-deployed-run/metadata.json"
jq -e '.passed == true and .postflightRequired == true and .postflightExitCode == 0 and .datastoreVerifierExitCode == 0' "${metadata}" >/dev/null
jq -e '
  (.artifacts.releaseServiceContainers | length) == 3 and
  ([.artifacts.releaseServiceContainers[] | select(.role == "git-service")] | length) == 1 and
  ([.artifacts.releaseServiceContainers[] | select(.role == "api") | .gitServiceURI] | unique | length) == 1
' "${test_dir}/evidence/repository/lifecycle/alpha/repository-deployed-run/preflight-environment.json" >/dev/null
jq -e '(.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/repository/lifecycle/alpha/repository-deployed-run/postflight-environment.json" >/dev/null

if MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=repository-production-undersized \
  CAPACITY_CONFIG_MANIFEST="${test_dir}/alpha-config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${test_dir}/alpha-environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  REPOSITORY_API_A=http://api-a.internal REPOSITORY_API_B=http://api-b.internal \
  REPOSITORY_API_REPLACEMENT=http://api-a.internal REPOSITORY_REPLACEMENT_TRIGGER_FILE="${test_dir}/repository-production-trigger" \
  REPOSITORY_TOKEN=test-token REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS=256 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINERS=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/repository-local-service-containers.json" \
  MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/alpha-scylla-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=1 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=1610612736 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" repository lifecycle production >/dev/null 2>&1; then
  echo "production Repository lifecycle unexpectedly accepted alpha SMP, overflow, and local image references" >&2
  exit 1
fi
metadata="${test_dir}/evidence/repository/lifecycle/production/repository-production-undersized/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

export MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/scylla-containers.json"

if MOCK_SAME_START=1 MOCK_REPLACE_ALL=1 PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=all-replicas-replaced-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted replacement of every API replica" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/watch/alpha/all-replicas-replaced-run/metadata.json"
jq -e '.passed == false and .postflightExitCode == 1' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=aliased-api-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-alias.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-alias.internal \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted one live API process twice" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=unverified-service-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/unverified-service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted an unverified service image" >&2
  exit 1
fi

cp "${test_dir}/service-containers.json" "${test_dir}/replacement-service-containers.json"
if MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=unverified-replacement-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/replacement-service-containers.json" \
  MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE="${test_dir}/unverified-service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted an unverified replacement image" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/watch/alpha/unverified-replacement-run/metadata.json"
jq -e '.passed == false and .preflightExitCode == 0 and .verifierExitCode == 0 and .postflightExitCode == 1' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=duplicate-service-run \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/duplicate-service-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly accepted duplicate service container identities" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  CAPACITY_DATASTORE_EXPECTED_COUNT=3 CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 \
  CAPACITY_DATASTORE_EXPECTED_SMP=3 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/wrong-smp.json" >/dev/null 2>&1; then
  echo "runtime Scylla SMP mismatch unexpectedly passed" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_BAD_MEMBERSHIP=1 \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 CAPACITY_DATASTORE_EXPECTED_COUNT=3 \
  CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 CAPACITY_DATASTORE_EXPECTED_SMP=2 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/wrong-membership.json" >/dev/null 2>&1; then
  echo "undersized live Scylla membership unexpectedly passed" >&2
  exit 1
fi
PATH="${test_dir}/bin:${PATH}" CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/clock-valid.json"
jq -e 'length == 3 and all(.[]; .clock.passed and (.clock.containerEpochSeconds | type == "number"))' \
  "${test_dir}/clock-valid.json" >/dev/null
jq -e 'all(.[]; .nativeProcess == {pid:101,startedAt:"Tue Oct 20 22:37:20 2026"})' \
  "${test_dir}/clock-valid.json" >/dev/null
PATH="${test_dir}/bin:${PATH}" CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${repo_root}/scripts/check-capacity-containers.sh" verify \
  "${test_dir}/clock-valid.json" "${test_dir}/native-stable.json"
jq -e '.passed' "${test_dir}/native-stable.json.verification.json" >/dev/null
for state in restarted reused-pid; do
  if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_NATIVE_STATE="${state}" \
    CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
    "${repo_root}/scripts/check-capacity-containers.sh" verify "${test_dir}/clock-valid.json" \
    "${test_dir}/native-${state}.json" >/dev/null 2>&1; then
    echo "native Scylla replacement inside unchanged containers unexpectedly passed: ${state}" >&2
    exit 1
  fi
  jq -e '.passed == false and all(.containers[];
    .passed == false and .before.id == .after.id and
    .before.state == .after.state and .before.restartCount == .after.restartCount and
    .before.nativeProcess != .after.nativeProcess)' \
    "${test_dir}/native-${state}.json.verification.json" >/dev/null
done
for state in missing multiple malformed inspection-failed; do
  if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_NATIVE_STATE="${state}" \
    CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
    "${repo_root}/scripts/check-capacity-containers.sh" snapshot \
    "${test_dir}/native-${state}.json" >/dev/null 2>&1; then
    echo "uninspectable native Scylla process unexpectedly passed: ${state}" >&2
    exit 1
  fi
done
for offset in -900 900; do
  if PATH="${test_dir}/bin:${PATH}" MOCK_CLOCK_OFFSET_SECONDS="${offset}" \
    CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
    "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/clock-skewed.json" >/dev/null 2>&1; then
    echo "datastore/host clock skew unexpectedly passed" >&2
    exit 1
  fi
  jq -e '.[0].clock.passed == false' "${test_dir}/clock-skewed.json" >/dev/null
done
if PATH="${test_dir}/bin:${PATH}" MOCK_CLOCK_INVALID=1 \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/clock-invalid.json" >/dev/null 2>&1; then
  echo "unknown datastore clock unexpectedly passed" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/duplicate-scylla-containers.json" \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-1,scylla-1 CAPACITY_DATASTORE_EXPECTED_COUNT=3 \
  CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 CAPACITY_DATASTORE_EXPECTED_SMP=2 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/duplicate-containers.json" >/dev/null 2>&1; then
  echo "duplicate datastore container identities unexpectedly passed" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=skipped-replacement \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT=1 \
  "${dispatcher}" namespace watch alpha >/dev/null 2>&1; then
  echo "alpha namespace watch evidence unexpectedly skipped rolling replacement" >&2
  exit 1
fi

if PATH="${test_dir}/bin:${PATH}" FAIL_MAKE=1 CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=failed-verifier \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "failed non-k6 verifier unexpectedly produced passing evidence" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/validation/alpha/failed-verifier/metadata.json"
jq -e '.passed == false and .verifierExitCode == 1' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=unverified-dataset \
  CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" scylla soak production >/dev/null 2>&1; then
  echo "Scylla production evidence unexpectedly passed without observed dataset verification" >&2
  exit 1
fi
metadata="${test_dir}/evidence/scylla/soak/production/unverified-dataset/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

if MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" \
  CAPACITY_RUN_ID=undersized-overflow CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" NAMESPACE_WATCH_TOKEN=test-token \
  NAMESPACE_WATCH_OVERFLOW_TRANSITIONS=1 CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
  CAPACITY_GIT_SERVICE_BUILD=release CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 \
  CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" namespace recovery alpha >/dev/null 2>&1; then
  echo "namespace recovery unexpectedly accepted an undersized overflow probe" >&2
  exit 1
fi
metadata="${test_dir}/evidence/namespace/recovery/alpha/undersized-overflow/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY=invalid "${dispatcher}" namespace validation diagnostic >/dev/null 2>&1; then
  echo "invalid capacity observability mode unexpectedly succeeded" >&2
  exit 1
fi

mkdir "${test_dir}/observability-bin"
cat >"${test_dir}/observability-bin/make" <<'SH'
#!/usr/bin/env bash
printf 'make:%s\n' "$*" >>"${MOCK_OBSERVABILITY_CALLS}"
SH
cat >"${test_dir}/observability-bin/curl" <<'SH'
#!/usr/bin/env bash
printf 'curl:%s\n' "$*" >>"${MOCK_OBSERVABILITY_CALLS}"
[[ "${MOCK_OBSERVABILITY_READY_FAILURE:-0}" != 1 ]]
SH
printf '#!/usr/bin/env bash\nexit 0\n' >"${test_dir}/observability-bin/sleep"
chmod +x "${test_dir}/observability-bin/"*
for observability_mode in prometheus full; do
  for scraper_owner in managed retained external; do
    calls="${test_dir}/observability-${observability_mode}-${scraper_owner}.log"
    : >"${calls}"
    prometheus_url=""
    keep=0
    if [[ "${scraper_owner}" == external ]]; then
      prometheus_url=http://existing-prometheus.invalid:19090
    elif [[ "${scraper_owner}" == retained ]]; then
      keep=1
    fi
    PATH="${test_dir}/observability-bin:${PATH}" MOCK_OBSERVABILITY_CALLS="${calls}" \
      CAPACITY_OBSERVABILITY="${observability_mode}" CAPACITY_PROMETHEUS_URL="${prometheus_url}" \
      CAPACITY_OBSERVABILITY_KEEP="${keep}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" \
      CAPACITY_RUN_ID="observability-${observability_mode}-${scraper_owner}" \
      "${dispatcher}" api readiness diagnostic
    grep -q '_capacity-k6' "${calls}"
    if [[ "${scraper_owner}" == external ]]; then
      grep -q 'existing-prometheus.invalid:19090/-/ready' "${calls}"
      if grep -q '_capacity-observability' "${calls}"; then
        echo "explicit external Prometheus unexpectedly started or removed managed services" >&2
        exit 1
      fi
    else
      grep -q 'make:--no-print-directory _capacity-observability$' "${calls}"
      if [[ "${scraper_owner}" == managed ]]; then
        grep -q '_capacity-observability-down' "${calls}"
      elif grep -q '_capacity-observability-down' "${calls}"; then
        echo "retained managed scraper was unexpectedly removed" >&2
        exit 1
      fi
    fi
  done
done
calls="${test_dir}/observability-unready.log"
: >"${calls}"
if PATH="${test_dir}/observability-bin:${PATH}" MOCK_OBSERVABILITY_CALLS="${calls}" \
  MOCK_OBSERVABILITY_READY_FAILURE=1 CAPACITY_OBSERVABILITY=prometheus \
  CAPACITY_PROMETHEUS_URL=http://existing-prometheus.invalid:19090 CAPACITY_OBSERVABILITY_KEEP=0 \
  CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=observability-unready \
  "${dispatcher}" api readiness diagnostic >/dev/null 2>&1; then
  echo "unready external Prometheus unexpectedly started a workload" >&2
  exit 1
fi
if grep -q 'make:' "${calls}"; then
  echo "unready external Prometheus unexpectedly started or removed managed services or workload" >&2
  exit 1
fi

echo "capacity dispatcher tests passed"
