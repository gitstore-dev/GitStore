#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -eu

config_file=${CONFIG_FILE:-./config/config.toml}
policy_file=${POLICY_FILE:-./config/policy.yaml}
grep -q '"serviceaccount-assertion"' "$config_file"
grep -q '"serviceaccount-jwt"' "$config_file"
grep -q '^serviceaccount_key_ref = ' "$config_file"
grep -A1 '^  serviceaccount:controllers:gitstore-controller-manager:$' "$policy_file" | grep -q '^    - controller$'
if grep -q '^api_token = ' "$config_file"; then
  echo "local Compose must bootstrap the controller ServiceAccount instead of setting controller.api_token" >&2
  exit 1
fi
if grep -q '^signing_key = ' "$config_file"; then
  echo "local Compose must source the API issuer key from its isolated mount" >&2
  exit 1
fi

output=$(CONFIG_FILE="$config_file" docker compose --profile local \
  -f compose.yml -f compose.local.yml config)

service_block() {
  awk -v service="$1" '
    $0 == "  " service ":" { printing = 1 }
    printing && $0 ~ /^  [[:alnum:]_-]+:$/ && $0 != "  " service ":" { exit }
    printing { print }
  '
}

mount_source() {
  awk -v target="$1" '
    $1 == "source:" { source = $2 }
    $1 == "target:" && $2 == target { print source; exit }
  '
}

count_source() {
  awk -v source="$1" '$1 == "source:" && $2 == source { count++ } END { print count + 0 }'
}

for service in git-service api controller-manager; do
  service_config=$(printf '%s\n' "$output" | service_block "$service")
  printf '%s\n' "$service_config" | grep -q -- '--config-file'
  printf '%s\n' "$service_config" | grep -q 'target: /etc/gitstore/gitstore.toml'
done

test "$(printf '%s\n' "$output" | grep -c 'target: /etc/gitstore/gitstore.toml')" -eq 3
test "$(printf '%s\n' "$output" | grep -c 'target: /etc/gitstore/policy.yaml')" -eq 1
test "$(printf '%s\n' "$output" | grep -c 'read_only: true')" -ge 7
printf '%s\n' "$output" | grep -q "source: $(cd "$(dirname "$config_file")" && pwd)/$(basename "$config_file")"

api_config=$(printf '%s\n' "$output" | service_block api)
controller_config=$(printf '%s\n' "$output" | service_block controller-manager)
bootstrap_config=$(printf '%s\n' "$output" | service_block credential-bootstrap)
enrollment_config=$(printf '%s\n' "$output" | service_block serviceaccount-enrollment)
git_config=$(printf '%s\n' "$output" | service_block git-service)
api_key_source=$(printf '%s\n' "$api_config" | mount_source /run/secrets)
controller_key_source=$(printf '%s\n' "$controller_config" | mount_source /run/secrets)
bootstrap_identity_source=$(printf '%s\n' "$bootstrap_config" | mount_source /run/controller-bootstrap)
enrollment_identity_source=$(printf '%s\n' "$enrollment_config" | mount_source /run/controller-bootstrap)

test -n "$api_key_source"
test -n "$controller_key_source"
test -n "$bootstrap_identity_source"
test "$bootstrap_identity_source" = "$enrollment_identity_source"
test "$api_key_source" != "$controller_key_source"
test "$(printf '%s\n' "$api_config" | grep -c 'target: /run/secrets')" -eq 1
test "$(printf '%s\n' "$controller_config" | grep -c 'target: /run/secrets')" -eq 1
test "$(printf '%s\n' "$bootstrap_config" | mount_source /run/api-issuer)" = "$api_key_source"
test "$(printf '%s\n' "$bootstrap_config" | mount_source /run/controller-secrets)" = "$controller_key_source"
test "$(printf '%s\n' "$enrollment_config" | mount_source /run/controller-secrets)" = "$controller_key_source"
test "$(printf '%s\n' "$api_config" | count_source "$controller_key_source")" -eq 0
test "$(printf '%s\n' "$controller_config" | count_source "$api_key_source")" -eq 0
test "$(printf '%s\n' "$git_config" | count_source "$api_key_source")" -eq 0
test "$(printf '%s\n' "$git_config" | count_source "$controller_key_source")" -eq 0
test "$(printf '%s\n' "$output" | count_source "$api_key_source")" -eq 2
test "$(printf '%s\n' "$output" | count_source "$controller_key_source")" -eq 3
printf '%s\n' "$api_config" | grep -A2 -q 'credential-bootstrap:'
printf '%s\n' "$controller_config" | grep -A2 -q 'serviceaccount-enrollment:'
printf '%s\n' "$enrollment_config" | grep -A2 -q 'api:'
printf '%s\n' "$enrollment_config" | grep -A1 '^    networks:$' | grep -q '^      gitstore-network: null$'
printf '%s\n' "$bootstrap_config" | grep -q 'rm -f /run/controller-bootstrap/serviceaccount.env'
printf '%s\n' "$enrollment_config" | grep -q -- '--replace-existing-key'

echo "local Compose configuration is valid for $config_file"
