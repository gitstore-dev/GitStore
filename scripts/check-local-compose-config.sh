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
# The strict parser/effective-grant tests validate policy semantics. Do not
# grow a second YAML parser from grep expressions here.
grep -q '^apiVersion: rbac.authorization.gitstore.dev/v1beta1$' "$policy_file" || fail "use a v1beta1 policy (v1 is rejected)"
if grep -Eq '^[[:space:]]*(api_token|signing_key)[[:space:]]*=' "$config_file"; then
  fail "shared config must reference isolated identity material, not contain tokens or signing keys"
fi

set -- --profile local
case "${OBSERVABILITY:-none}" in
  none) ;;
  otel)
    set -- "$@" --profile observability
    export GITSTORE_OBSERVABILITY__OTLP__URI=http://otel-collector:4318
    ;;
  *) fail "OBSERVABILITY must be none or otel" ;;
esac
output=$(CONFIG_FILE="$config_file" docker compose "$@" \
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
  .services.api.command == ["/app/api", "--config-file", "/etc/gitstore/gitstore.toml",
    "--serviceaccount-signing-key-file", "/run/secrets/serviceaccount-signing-key"] and
  .services.api.healthcheck.test == ["CMD", "/app/api", "gitctl", "healthcheck"] and
  (.services["credential-bootstrap"].build.target // "runtime") == "runtime" and
  (.services["controller-credential-bootstrap"].build.target // "runtime") == "runtime" and
  .services["credential-bootstrap"].user == "0:0" and
  .services["controller-credential-bootstrap"].user == "0:0" and
  .services["credential-bootstrap"].image == .services.api.image and
  .services["controller-credential-bootstrap"].image == .services.api.image and
  .services["credential-bootstrap"].command[:4] == ["/app/api", "gitctl", "bootstrap", "api"] and
  .services["controller-credential-bootstrap"].command[:4] == ["/app/api", "gitctl", "bootstrap", "controller"] and
  .services["credential-bootstrap"].network_mode == "none" and
  .services["controller-credential-bootstrap"].network_mode == "none" and
  (.services["serviceaccount-enrollment"].command[:2] == ["/app/api", "gitctl"])
' "API startup, probing and independent preparation must use the shell-less API image"
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
  (mounts("controller-credential-bootstrap"; "/run/controller-secrets") | length == 1 and .[0].source == $controller[0].source) and
  (.services["credential-bootstrap"].volumes | length == 1) and
  (.services["controller-credential-bootstrap"].volumes | length == 1) and
  (mounts("serviceaccount-enrollment"; "/run/controller-secrets") |
    length == 1 and .[0].source == $controller[0].source and .[0].read_only == true) and
  ([$root.services[].volumes[]? | select(.source == $api[0].source)] | length == 2) and
  ([$root.services[].volumes[]? | select(.source == $controller[0].source)] | length == 3)
' "API/controller key mounts must be distinct, read-only for consumers and restricted to their provisioning services"

check '
  [.services["controller-manager"].volumes[]? | select(.target == "/run/controller-bootstrap")] as $bootstrap |
  [.services["serviceaccount-enrollment"].volumes[]? | select(.target == "/run/controller-bootstrap")] as $enrollment |
  ($bootstrap | length == 1 and .[0].read_only == true) and ($enrollment | length == 1) and
  ($bootstrap[0].source | type == "string" and length > 0) and
  ($bootstrap[0].source == $enrollment[0].source)
' "only enrollment writes the controller identity overlay"

check '
  .services.api.depends_on["credential-bootstrap"] != null and
  .services["controller-manager"].depends_on["serviceaccount-enrollment"] != null and
  .services["serviceaccount-enrollment"].depends_on.api != null and
  .services["serviceaccount-enrollment"].depends_on["controller-credential-bootstrap"] != null and
  (.services["serviceaccount-enrollment"].networks | has("gitstore-network")) and
  .services["controller-manager"].command == ["/app/controller-manager", "--config-file", "/etc/gitstore/gitstore.toml", "--config-file", "/run/controller-bootstrap/serviceaccount.toml"] and
  (.services["serviceaccount-enrollment"].command | index("--identity-output-format") != null and index("toml") != null and index("--replace-identity") != null and index("--replace-existing-key") != null)
' "bootstrap/enrollment dependencies, network and explicit TOML identity replacement must be wired"

check '
  (.services["serviceaccount-enrollment"].command | index("--namespace") == null) and
  (.services["serviceaccount-enrollment"].command | index("--name") != null) and
  ([.services[].environment // {} | keys[] | select(contains("SERVICEACCOUNT__NAMESPACE"))] | length == 0)
' "enrollment and controller configuration must use cluster-scoped ServiceAccounts"

if [ "${OBSERVABILITY:-none}" = otel ]; then
  check '
    [.services["git-service"], .services.api, .services["controller-manager"]] |
    all(.[]; .environment.GITSTORE_OBSERVABILITY__OTLP__URI == "http://otel-collector:4318")
  ' "all core telemetry must route to the collector"
  check '
    (.services["git-service"].ports | all(.[]; .target == 50051)) and
    (.services["otel-collector"].depends_on | has("loki") and has("tempo")) and
    (.services.prometheus.depends_on | has("otel-collector"))
  ' "Git must not expose an HTTP scrape port; all backends must use the collector topology"
fi

echo "local Compose configuration is valid for $config_file"
