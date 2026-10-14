#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT
mkdir -p "${test_dir}/bin"

cat >"${test_dir}/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${REPOSITORY_CAPACITY_DOCKER_LOG}"
case "$*" in
  "ps -aq --filter label=com.docker.compose.project="*) printf '%s' "${TEST_CAPACITY_EXISTING_CONTAINER:-}" ;;
  "volume ls -q --filter label=com.docker.compose.project="*) printf '%s' "${TEST_CAPACITY_EXISTING_VOLUME:-}" ;;
  "inspect --format "*)
    printf '%s' "${TEST_CAPACITY_INSPECT_LABELS:-{}}"
    ;;
esac
EOF
chmod +x "${test_dir}/bin/docker"

cat >"${test_dir}/bin/curl" <<'EOF'
#!/usr/bin/env bash
case "${*: -1}" in
  */health) printf '%s\n' '{"credentialReady":true,"kinds":{"Repository":{"registered":true}}}' ;;
  *) printf '\n' ;;
esac
EOF
chmod +x "${test_dir}/bin/curl"

export REPOSITORY_CAPACITY_DOCKER_LOG="${test_dir}/docker.log"
: >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
for flag in 1 true 2 -1; do
  if PATH="${test_dir}/bin:${PATH}" CHAOS_CONFIRM=0 REPOSITORY_CAPACITY_SECRET_SCENARIO="${flag}" \
    "${repo_root}/scripts/repository-capacity-stack.sh" local-alpha >"${test_dir}/secret-preflight.log" 2>&1; then
    echo "managed stack accepted unavailable or invalid secret scenario" >&2
    exit 1
  fi
done
[[ ! -s "${REPOSITORY_CAPACITY_DOCKER_LOG}" ]] || {
  echo "secret scenario guard ran Docker before rejecting the request" >&2
  exit 1
}
PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_GIT_REVISION=0123456789abcdef0123456789abcdef01234567 \
  REPOSITORY_CAPACITY_STATE_DIR="${test_dir}/state" \
  REPOSITORY_CAPACITY_PROJECT=capacity-test \
  "${repo_root}/scripts/repository-capacity-stack.sh" config

grep -q -- '-p capacity-test --profile capacity-stack' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'compose.capacity.yml config' "${REPOSITORY_CAPACITY_DOCKER_LOG}"

for existing in container volume; do
  : >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
  if PATH="${test_dir}/bin:${PATH}" \
    TEST_CAPACITY_EXISTING_CONTAINER="$([[ "${existing}" == container ]] && printf foreign || true)" \
    TEST_CAPACITY_EXISTING_VOLUME="$([[ "${existing}" == volume ]] && printf retained || true)" \
    REPOSITORY_CAPACITY_STATE_DIR="${test_dir}/refused-${existing}" REPOSITORY_CAPACITY_PROJECT=capacity-test \
    "${repo_root}/scripts/repository-capacity-stack.sh" local-alpha >"${test_dir}/ownership.log" 2>&1; then
    echo "managed stack reused existing project ${existing} data" >&2
    exit 1
  fi
  if grep -Eq '(^| )(up|stop|down|rm|logs)( |$)' "${REPOSITORY_CAPACITY_DOCKER_LOG}"; then
    echo "failed ownership preflight modified or captured an unowned stack" >&2
    exit 1
  fi
done

for setting in 'CAPACITY_SCYLLA_KEYSPACE=bad;name' 'CAPACITY_API_A_PORT=65536' 'CAPACITY_API_B_PORT=4000'; do
  if env PATH="${test_dir}/bin:${PATH}" "${setting}" \
    "${repo_root}/scripts/repository-capacity-stack.sh" config >"${test_dir}/invalid.log" 2>&1; then
    echo "managed stack accepted invalid keyspace/port setting" >&2
    exit 1
  fi
done

: >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
touch "${test_dir}/replace"
mkdir -p "${test_dir}/watcher-state"
jq -n '{schemaVersion:1,project:"capacity-test",runID:"owned-run",revision:"0123456789abcdef0123456789abcdef01234567",keyspace:"gitstore",ports:[4000,4001,9000,9001,6000,6001,5001,5002]}' \
  >"${test_dir}/watcher-state/owned-stack.json"
PATH="${test_dir}/bin:${PATH}" CAPACITY_RUN_ID=owned-run \
  REPOSITORY_CAPACITY_STATE_DIR="${test_dir}/watcher-state" \
  "${repo_root}/scripts/watch-repository-capacity-replacement.sh" \
  "${test_dir}/replace" capacity-test 0123456789abcdef0123456789abcdef01234567

[[ "$(wc -l <"${REPOSITORY_CAPACITY_DOCKER_LOG}" | tr -d ' ')" == 4 ]]
grep -q -- 'stop api-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'rm -f api-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'up -d --no-deps api-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
if grep -Eq '(api-a|controller-manager)' "${REPOSITORY_CAPACITY_DOCKER_LOG}"; then
  echo "replacement watcher touched a non-target service" >&2
  exit 1
fi

for run in missing foreign-run; do
  : >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
  state="${test_dir}/watcher-state"
  [[ "${run}" != missing ]] || state="${test_dir}/missing-state"
  if PATH="${test_dir}/bin:${PATH}" CAPACITY_RUN_ID="${run}" \
    REPOSITORY_CAPACITY_PROJECT=capacity-test REPOSITORY_CAPACITY_STATE_DIR="${state}" \
    CAPACITY_GIT_REVISION=0123456789abcdef0123456789abcdef01234567 \
    "${repo_root}/scripts/repository-capacity-stack.sh" down >"${test_dir}/manifest.log" 2>&1; then
    echo "shutdown accepted a missing or mismatched ownership manifest" >&2
    exit 1
  fi
  [[ ! -s "${REPOSITORY_CAPACITY_DOCKER_LOG}" ]] || {
    echo "ownership mismatch reached Docker lifecycle operations" >&2
    exit 1
  }
done

: >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
if PATH="${test_dir}/bin:${PATH}" CAPACITY_RUN_ID=owned-run \
  REPOSITORY_CAPACITY_PROJECT=capacity-test REPOSITORY_CAPACITY_STATE_DIR="${test_dir}/watcher-state" \
  CAPACITY_GIT_REVISION=0123456789abcdef0123456789abcdef01234567 \
  TEST_CAPACITY_EXISTING_CONTAINER=abcdef123456 \
  TEST_CAPACITY_INSPECT_LABELS='{"com.docker.compose.project":"capacity-test","dev.gitstore.capacity.run":"foreign-run"}' \
  "${repo_root}/scripts/repository-capacity-stack.sh" down >"${test_dir}/foreign-label.log" 2>&1; then
  echo "shutdown accepted a foreign-run project container" >&2
  exit 1
fi
if grep -Eq '(^| )(stop|down|rm)( |$)' "${REPOSITORY_CAPACITY_DOCKER_LOG}"; then
  echo "foreign-run container reached a destructive lifecycle action" >&2
  exit 1
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  default_observability_render="${test_dir}/compose-observability.json"
  env -u CAPACITY_PROMETHEUS_TARGETS_FILE docker compose --profile capacity \
    -f "${repo_root}/compose.yml" \
    -f "${repo_root}/compose.local.yml" \
    -f "${repo_root}/compose.capacity.yml" config --format json >"${default_observability_render}"
  jq -e '
    .services["capacity-prometheus"].volumes[] |
    select(.target == "/etc/prometheus/targets.json") |
    .source | endswith("/tests/capacity/prometheus/empty-targets.json")
  ' "${default_observability_render}" >/dev/null

  rendered="${test_dir}/compose.json"
  CAPACITY_GIT_REVISION=0123456789abcdef0123456789abcdef01234567 \
    CONFIG_FILE="${repo_root}/config/config.toml" \
    CAPACITY_PROMETHEUS_TARGETS_FILE="${test_dir}/prometheus-targets.json" \
    SCYLLA_CLUSTER_SMP=1 SCYLLA_CLUSTER_MEMORY_LIMIT=1536m \
    docker compose -p capacity-render-test --profile capacity-stack \
      -f "${repo_root}/compose.yml" \
      -f "${repo_root}/compose.scylla.cluster.yml" \
      -f "${repo_root}/compose.capacity.yml" config --format json >"${rendered}"
  jq -e '
    .services["scylla-1"].container_name == "capacity-render-test-scylla-1" and
    .services["api-a"].labels["dev.gitstore.capacity.run"] != null and
    (.services["api-b"].labels == .services["api-a"].labels) and
    (.services["controller-manager-b"].labels == .services["api-a"].labels) and
    .services["scylla-2"].container_name == "capacity-render-test-scylla-2" and
    .services["scylla-3"].container_name == "capacity-render-test-scylla-3" and
    ((.services["scylla-1"].ports // []) | length) == 0 and
    .services["api-b"].depends_on["api-a"].condition == "service_healthy" and
    .services["api-b"].depends_on["capacity-credential-bootstrap"].condition == "service_completed_successfully" and
    .services["api-b"].depends_on["scylla-init"].condition == "service_completed_successfully" and
    .services["api-b"].depends_on["capacity-git-service"].condition == "service_started" and
    .services["capacity-git-service"].environment.GITSTORE_GIT_SERVICE__CATALOG__URI == "dns:///api-a:6000" and
    ((.services["capacity-credential-bootstrap"].ports // []) | length) == 0 and
    ((.services["capacity-serviceaccount-enrollment"].ports // []) | length) == 0 and
    (.services["api-a"].depends_on["api-b"] == null)
  ' "${rendered}" >/dev/null

  CAPACITY_SCYLLA_KEYSPACE=capacity_owned CAPACITY_API_A_PORT=14000 CAPACITY_API_B_PORT=14001 \
    CAPACITY_GIT_HTTP_A_PORT=19000 CAPACITY_GIT_HTTP_B_PORT=19001 \
    CAPACITY_CATALOG_A_PORT=16000 CAPACITY_CATALOG_B_PORT=16001 \
    CAPACITY_CONTROLLER_A_PORT=15001 CAPACITY_CONTROLLER_B_PORT=15002 \
    POLICY_FILE="${test_dir}/policy.yaml" USERS_FILE="${test_dir}/users.yaml" \
    docker compose -p another-capacity --profile capacity-stack \
      -f "${repo_root}/compose.yml" -f "${repo_root}/compose.scylla.cluster.yml" \
      -f "${repo_root}/compose.capacity.yml" config --format json >"${test_dir}/custom-compose.json"
  jq -e --arg root "${test_dir}" '
    .services["api-a"].container_name == "another-capacity-api-a" and
    .services["api-b"].container_name == "another-capacity-api-b" and
    .services["api-a"].ports[0].published == "14000" and
    .services["api-b"].ports[0].published == "14001" and
    .services["controller-manager-a"].ports[0].published == "15001" and
    .services["controller-manager-b"].ports[0].published == "15002" and
    .services["api-a"].environment.GITSTORE_API__DATASTORE__SCYLLA__KEYSPACE == "capacity_owned" and
    (.services["scylla-init"].command[-1] | contains("EXISTS capacity_owned ")) and
    ([.services["api-a"].volumes[] | select(.target == "/etc/gitstore/policy.yaml")][0].source == ($root + "/policy.yaml")) and
    ([.services["api-b"].volumes[] | select(.target == "/etc/gitstore/users.yaml")][0].source == ($root + "/users.yaml"))
  ' "${test_dir}/custom-compose.json" >/dev/null

  secret_rendered="${test_dir}/compose-secret.json"
  REPOSITORY_CAPACITY_SECRET_SCENARIO=1 \
    CAPACITY_API_IMAGE="localhost/api@sha256:$(printf '%064d' 1)" \
    CAPACITY_CONTROLLER_IMAGE="localhost/controller@sha256:$(printf '%064d' 2)" \
    CAPACITY_GIT_SERVICE_IMAGE="localhost/git@sha256:$(printf '%064d' 3)" \
    CAPACITY_CONTROLLER_A_CONFIG_FILE="${test_dir}/owned-fixture/controller-a/config.toml" \
    CAPACITY_CONTROLLER_B_CONFIG_FILE="${test_dir}/owned-fixture/controller-b/config.toml" \
    CAPACITY_CONTROLLER_A_SECRET_SOURCE="${test_dir}/owned-fixture/controller-a/provider" \
    CAPACITY_CONTROLLER_B_SECRET_SOURCE="${test_dir}/owned-fixture/controller-b/provider" \
    docker compose -p capacity-render-test --profile capacity-stack \
      -f "${repo_root}/compose.yml" \
      -f "${repo_root}/compose.scylla.cluster.yml" \
      -f "${repo_root}/compose.capacity.yml" config --format json >"${secret_rendered}"
  jq -e --arg root "${test_dir}/owned-fixture" '
    (.services["api-a"].image | startswith("localhost/api@sha256:")) and
    (.services["api-b"].image == .services["api-a"].image) and
    (.services["controller-manager-a"].image | startswith("localhost/controller@sha256:")) and
    (.services["controller-manager-b"].image == .services["controller-manager-a"].image) and
    (.services["capacity-git-service"].image | startswith("localhost/git@sha256:")) and
    (.services["api-a"].environment.REPOSITORY_CAPACITY_SECRET_SCENARIO == "1") and
    (.services["api-b"].environment.REPOSITORY_CAPACITY_SECRET_SCENARIO == "1") and
    (.services["api-a"].command[-1] | contains("GITSTORE_API__AUTH__SERVICEACCOUNT__MAX_TTL=60s")) and
    (.services["capacity-git-service"].environment.GITSTORE_GIT_SERVICE__CATALOG__URI == "dns:///api-a:6000") and
    ([.services["controller-manager-a"], .services["controller-manager-b"]] |
      all((.volumes | length) == 4 and
          ([.volumes[] | select(.target == "/run/secrets" and .read_only == true)] | length) == 1 and
          ([.volumes[] | select(.target == "/etc/gitstore/gitstore.toml" and .read_only == true)] | length) == 1 and
          ([.volumes[] | select(.source == "controller-serviceaccount-secrets")] | length) == 0)) and
    ([.services["controller-manager-a"].volumes[] | select(.target == "/run/secrets")][0].source == ($root + "/controller-a/provider")) and
    ([.services["controller-manager-b"].volumes[] | select(.target == "/run/secrets")][0].source == ($root + "/controller-b/provider")) and
    ([.services["controller-manager-a"].volumes[] | select(.target == "/etc/gitstore/gitstore.toml")][0].source == ($root + "/controller-a/config.toml")) and
    ([.services["controller-manager-b"].volumes[] | select(.target == "/etc/gitstore/gitstore.toml")][0].source == ($root + "/controller-b/config.toml")) and
    ([.services["controller-manager-a"].volumes[] | select(.target == "/var/lib/gitstore/checkpoints")][0].source !=
     [.services["controller-manager-b"].volumes[] | select(.target == "/var/lib/gitstore/checkpoints")][0].source)
  ' "${secret_rendered}" >/dev/null
fi

# A verifier failure before the replacement trigger must reap the watcher and
# must not reference a function-local PID after run_alpha returns.
cat >"${test_dir}/bin/capacity-runner" <<'EOF'
#!/usr/bin/env bash
until [[ -r "${REPOSITORY_CAPACITY_WATCHER_READY_FILE}" ]]; do sleep 0.01; done
exit 17
EOF
chmod +x "${test_dir}/bin/capacity-runner"
cat >"${test_dir}/bin/replacement-watcher" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$$" >"${REPOSITORY_CAPACITY_WATCHER_PID_FILE}"
trap 'printf "%s\n" stopped >"${REPOSITORY_CAPACITY_WATCHER_STOP_FILE}"; exit 0' TERM INT
printf '%s\n' ready >"${REPOSITORY_CAPACITY_WATCHER_READY_FILE}"
while :; do sleep 1; done
EOF
chmod +x "${test_dir}/bin/replacement-watcher"
mkdir -p "${test_dir}/failed-state"
printf 'token\n' >"${test_dir}/failed-state/token"
: >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
set +e
failure_output="$(PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_GIT_REVISION=0123456789abcdef0123456789abcdef01234567 \
  REPOSITORY_CAPACITY_HOST_MEMORY_BYTES=17179869184 \
  REPOSITORY_CAPACITY_RUNNER="${test_dir}/bin/capacity-runner" \
  REPOSITORY_CAPACITY_REPLACEMENT_WATCHER="${test_dir}/bin/replacement-watcher" \
  REPOSITORY_CAPACITY_WATCHER_PID_FILE="${test_dir}/watcher.pid" \
  REPOSITORY_CAPACITY_WATCHER_READY_FILE="${test_dir}/watcher.ready" \
  REPOSITORY_CAPACITY_WATCHER_STOP_FILE="${test_dir}/watcher.stopped" \
  REPOSITORY_CAPACITY_STATE_DIR="${test_dir}/failed-state" \
  REPOSITORY_CAPACITY_PROJECT=capacity-test \
  "${repo_root}/scripts/repository-capacity-stack.sh" local-alpha 2>&1)"
failure_status=$?
set -e
[[ "${failure_status}" == 17 ]]
if grep -q 'unbound variable' <<<"${failure_output}"; then
  echo "replacement watcher cleanup referenced an unbound variable" >&2
  exit 1
fi
[[ -r "${test_dir}/watcher.pid" ]]
[[ -r "${test_dir}/watcher.stopped" ]]
grep -q -- 'up -d --build api-a api-b controller-manager-a controller-manager-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'ps --all$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'logs --no-color$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'stop api-a api-b controller-manager-a controller-manager-b capacity-git-service scylla-1 scylla-2 scylla-3$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
if grep -Eq '(^| )(down|--remove-orphans|-v)( |$)' "${REPOSITORY_CAPACITY_DOCKER_LOG}"; then
  echo "failed stack cleanup destroyed retained volumes or touched orphan services" >&2
  exit 1
fi
[[ -f "${test_dir}/failed-state/token" ]]
[[ -f "${test_dir}/failed-state/compose-failure-ps.log" ]]
[[ -f "${test_dir}/failed-state/compose-failure.log" ]]

alpha_recipe="$(make --no-print-directory -C "${repo_root}" -n capacity TARGET=repository PROFILE=lifecycle MODE=alpha)"
grep -q 'repository-capacity-stack.sh local-alpha' <<<"${alpha_recipe}"
if grep -q 'run-capacity-target.sh' <<<"${alpha_recipe}"; then
  echo "Repository alpha capacity command bypassed the local stack owner" >&2
  exit 1
fi
production_recipe="$(make --no-print-directory -C "${repo_root}" -n capacity TARGET=repository PROFILE=lifecycle MODE=production)"
grep -q 'run-capacity-target.sh "repository" "lifecycle" "production"' <<<"${production_recipe}"
if grep -q 'repository-capacity-stack.sh' <<<"${production_recipe}"; then
  echo "Repository production capacity command must use an external deployment" >&2
  exit 1
fi
for removed_target in repository-capacity-up repository-capacity-wait repository-capacity-token repository-capacity-alpha repository-capacity-down; do
  if make --no-print-directory -C "${repo_root}" -n "${removed_target}" >/dev/null 2>&1; then
    echo "removed public target still exists: ${removed_target}" >&2
    exit 1
  fi
done

echo "repository capacity stack dispatch tests passed"
