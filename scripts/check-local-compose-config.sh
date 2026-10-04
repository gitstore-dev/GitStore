#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -eu

fail() {
  echo "local Compose: $1" >&2
  exit 1
}

config_file=${CONFIG_FILE:-./config/config.toml}
policy_file=${POLICY_FILE:-./config/policy.yaml}
command -v jq >/dev/null 2>&1 || fail "jq is required to validate rendered mounts"
grep -q '"serviceaccount-assertion"' "$config_file" || fail "enable serviceaccount-assertion"
grep -q '"serviceaccount-jwt"' "$config_file" || fail "enable serviceaccount-jwt"
grep -q '^\[controller.serviceaccount\]' "$config_file" || fail "use controller.serviceaccount settings"
grep -q '^key_ref = ' "$config_file" || fail "configure controller.serviceaccount.key_ref"
grep -A1 '^  serviceaccount:controllers:gitstore-controller-manager:$' "$policy_file" |
  grep -q '^    - controller$' || fail "bind the controller ServiceAccount to its role"
if grep -Eq '^[[:space:]]*(api_token|signing_key)[[:space:]]*=' "$config_file"; then
  fail "shared config must reference isolated identity material, not contain tokens or signing keys"
fi

output=$(CONFIG_FILE="$config_file" docker compose --profile local \
  -f compose.yml -f compose.local.yml config --format json) || fail "cannot render configuration"
config_path="$(cd "$(dirname "$config_file")" && pwd)/$(basename "$config_file")"

check() {
  printf '%s\n' "$output" |
    jq -e --arg config "$config_path" "$1" >/dev/null 2>&1 || fail "$2"
}

check '
  [.services["git-service"], .services.api, .services["controller-manager"]] |
  all(.[];
    (.command | tostring | contains("--config-file")) and
    ([.volumes[] | select(.target == "/etc/gitstore/gitstore.toml")] |
      length == 1 and all(.[]; .source == $config and .read_only == true)))
' "each core service must mount the selected config read-only and use --config-file"

check '
  [.services[].volumes[]? | select(.target == "/etc/gitstore/gitstore.toml")] | length == 3
' "shared config must be mounted only by the three core services"
check '
  [.services[].volumes[]? | select(.target == "/etc/gitstore/policy.yaml")] |
  length == 1 and all(.[]; .read_only == true)
' "policy must have one read-only mount"

check '
  def mounts($service; $target):
    [.services[$service].volumes[]? | select(.target == $target)];
  . as $root |
  mounts("api"; "/run/secrets") as $api |
  mounts("controller-manager"; "/run/secrets") as $controller |
  ($api | length == 1) and ($controller | length == 1) and
  ($api[0].read_only == true) and ($controller[0].read_only == true) and
  ($api[0].source | type == "string" and length > 0) and
  ($controller[0].source | type == "string" and length > 0) and
  ($api[0].source != $controller[0].source) and
  (mounts("credential-bootstrap"; "/run/api-issuer") | length == 1 and .[0].source == $api[0].source) and
  (mounts("credential-bootstrap"; "/run/controller-secrets") | length == 1 and .[0].source == $controller[0].source) and
  (mounts("serviceaccount-enrollment"; "/run/controller-secrets") |
    length == 1 and .[0].source == $controller[0].source) and
  ([$root.services[].volumes[]? | select(.source == $api[0].source)] | length == 2) and
  ([$root.services[].volumes[]? | select(.source == $controller[0].source)] | length == 3)
' "API/controller key mounts must be distinct, read-only for consumers and restricted to their provisioning services"

check '
  [.services["credential-bootstrap"].volumes[]? | select(.target == "/run/controller-bootstrap")] as $bootstrap |
  [.services["serviceaccount-enrollment"].volumes[]? | select(.target == "/run/controller-bootstrap")] as $enrollment |
  ($bootstrap | length == 1) and ($enrollment | length == 1) and
  ($bootstrap[0].source | type == "string" and length > 0) and
  ($bootstrap[0].source == $enrollment[0].source)
' "bootstrap and enrollment must share the identity-output mount"

check '
  .services.api.depends_on["credential-bootstrap"] != null and
  .services["controller-manager"].depends_on["serviceaccount-enrollment"] != null and
  .services["serviceaccount-enrollment"].depends_on.api != null and
  (.services["serviceaccount-enrollment"].networks | has("gitstore-network")) and
  (.services["credential-bootstrap"].command | tostring | contains("rm -f /run/controller-bootstrap/serviceaccount.env")) and
  (.services["serviceaccount-enrollment"].command | tostring | contains("--replace-existing-key"))
' "bootstrap/enrollment dependencies, network and identity replacement must be wired"

echo "local Compose configuration is valid for $config_file"
