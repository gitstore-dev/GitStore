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

for removed_target in capacity-observability capacity-observability-down test-scylla-capacity test-namespace-admission-capacity test-namespace-watch-capacity test-namespace-watch-recovery; do
  if rg -q "^${removed_target}:" "${repo_root}/Makefile"; then
    echo "removed public target ${removed_target} is still defined" >&2
    exit 1
  fi
done

mkdir -p "${test_dir}/evidence"
printf '#!/usr/bin/env bash\necho "mock capacity command: $*"\nif [[ -n "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE:-}" ]]; then cp "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE}" "${MOCK_SERVICE_CONTAINERS_FILE}"; fi\nif [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" ]]; then touch "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}"; fi\n[[ "${FAIL_MAKE:-0}" != "1" ]]\n' >"${test_dir}/bin/make"
cat >"${test_dir}/bin/docker" <<'EOF'
#!/usr/bin/env bash
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

mkdir -p "${test_dir}/admission-verifier"
jq -n '{metrics:{gitstore_namespace_admitted:{count:10,rate:10},
  checks:{passes:20,fails:0,value:1,thresholds:{"rate==1":false}},
  gitstore_namespace_graphql_failed:{passes:0,fails:10,value:0,thresholds:{"rate<0.001":false}},
  gitstore_namespace_visibility_ms:{count:10,"p(95)":100,"p(99)":200,thresholds:{"p(95)<=1000":false,"p(99)<=3000":false}}}}' \
  >"${test_dir}/admission-verifier/summary.json"
"${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier"
jq -e '.passed == true and .admitted == 10 and .visibilityP99Milliseconds == 200' \
  "${test_dir}/admission-verifier/domain-verifier.json" >/dev/null
cp "${test_dir}/admission-verifier/summary.json" "${test_dir}/admission-verifier/native-summary.json"
for mutation in \
  'del(.metrics.checks)' \
  '.metrics.checks.passes = 0' \
  '.metrics.checks.fails = 1' \
  'del(.metrics.gitstore_namespace_admitted)' \
  '.metrics.gitstore_namespace_admitted.count = 0' \
  '.metrics.gitstore_namespace_admitted.count = "10"' \
  '.metrics.gitstore_namespace_graphql_failed.value = 0.001' \
  '.metrics.gitstore_namespace_graphql_failed.value = -1' \
  '.metrics.gitstore_namespace_graphql_failed.fails = 0' \
  '.metrics.gitstore_namespace_visibility_ms["p(95)"] = 1001' \
  '.metrics.gitstore_namespace_visibility_ms["p(99)"] = 3001' \
  'del(.metrics.gitstore_namespace_visibility_ms["p(99)"])' \
  'del(.metrics.gitstore_namespace_visibility_ms.count)' \
  '.metrics.gitstore_namespace_visibility_ms.count = 0' \
  '.metrics.gitstore_namespace_visibility_ms.count = 9' \
  '.metrics.gitstore_namespace_visibility_ms["p(95)"] = -1' \
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

echo "capacity dispatcher tests passed"
