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
PATH="${test_dir}/bin:${PATH}" \
  CAPACITY_GIT_REVISION=0123456789abcdef0123456789abcdef01234567 \
  REPOSITORY_CAPACITY_STATE_DIR="${test_dir}/state" \
  REPOSITORY_CAPACITY_PROJECT=capacity-test \
  "${repo_root}/scripts/repository-capacity-stack.sh" config

grep -q -- '-p capacity-test --profile capacity-stack' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'compose.capacity.yml config' "${REPOSITORY_CAPACITY_DOCKER_LOG}"

: >"${REPOSITORY_CAPACITY_DOCKER_LOG}"
touch "${test_dir}/replace"
PATH="${test_dir}/bin:${PATH}" \
  "${repo_root}/scripts/watch-repository-capacity-replacement.sh" \
  "${test_dir}/replace" capacity-test 0123456789abcdef0123456789abcdef01234567

[[ "$(wc -l <"${REPOSITORY_CAPACITY_DOCKER_LOG}" | tr -d ' ')" == 3 ]]
grep -q -- 'stop api-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'rm -f api-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
grep -q -- 'up -d --no-deps api-b$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
if grep -Eq '(api-a|controller-manager)' "${REPOSITORY_CAPACITY_DOCKER_LOG}"; then
  echo "replacement watcher touched a non-target service" >&2
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
    .services["scylla-1"].container_name == "gitstore-capacity-scylla-1" and
    .services["scylla-2"].container_name == "gitstore-capacity-scylla-2" and
    .services["scylla-3"].container_name == "gitstore-capacity-scylla-3" and
    ((.services["scylla-1"].ports // []) | length) == 0 and
    .services["api-b"].depends_on["api-a"].condition == "service_healthy" and
    .services["api-b"].depends_on["capacity-credential-bootstrap"].condition == "service_completed_successfully" and
    .services["api-b"].depends_on["scylla-init"].condition == "service_completed_successfully" and
    .services["api-b"].depends_on["capacity-git-service"].condition == "service_started" and
    .services["capacity-git-service"].environment.GITSTORE_CATALOG_SERVICE__URI == "http://api-a:6000" and
    ((.services["capacity-credential-bootstrap"].ports // []) | length) == 0 and
    ((.services["capacity-serviceaccount-enrollment"].ports // []) | length) == 0 and
    (.services["api-a"].depends_on["api-b"] == null)
  ' "${rendered}" >/dev/null
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
grep -q -- 'down -v --remove-orphans$' "${REPOSITORY_CAPACITY_DOCKER_LOG}"
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
