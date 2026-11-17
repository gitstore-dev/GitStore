#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${repo_root}/scripts/test-fixtures/shell.sh"
shell_test_setup secret-config
mkdir -p "$test_dir/bin"
cat >"$test_dir/bin/docker" <<'EOF'
#!/bin/sh
cat "$SECRET_CONFIG_FIXTURE"
EOF
chmod +x "$test_dir/bin/docker"
export PATH="$test_dir/bin:$PATH"
export SECRET_CONFIG_FIXTURE="$test_dir/compose.json"
export CONFIG_FILE="$repo_root/config/config.toml"
export POLICY_FILE="$repo_root/config/policy.yaml"

jq -n --arg config "$CONFIG_FILE" '{
  services: {
    "git-service": {command:["--config-file","/etc/gitstore/gitstore.toml"], volumes:[
      {source:$config,target:"/etc/gitstore/gitstore.toml",read_only:true}]},
    api: {command:["/app/api","--config-file","/etc/gitstore/gitstore.toml","--serviceaccount-signing-key-file","/run/secrets/serviceaccount-signing-key"],
      healthcheck:{test:["CMD","/app/api","gitctl","healthcheck"]},
      depends_on:{"credential-bootstrap":{}}, volumes:[
      {source:$config,target:"/etc/gitstore/gitstore.toml",read_only:true},
      {source:"policy",target:"/etc/gitstore/policy.yaml",read_only:true},
      {source:"issuer",target:"/run/secrets",read_only:true}]},
    "controller-manager": {command:["--config-file","/etc/gitstore/gitstore.toml"],
      depends_on:{"serviceaccount-enrollment":{}}, volumes:[
      {source:$config,target:"/etc/gitstore/gitstore.toml",read_only:true},
      {source:"controller",target:"/run/secrets",read_only:true}]},
    "credential-bootstrap": {build:{target:"debug"},user:"0:0",
      command:["rm -f /run/controller-bootstrap/serviceaccount.env"], volumes:[
      {source:"issuer",target:"/run/api-issuer"},
      {source:"controller",target:"/run/controller-secrets"},
      {source:"identity",target:"/run/controller-bootstrap"}]},
    "serviceaccount-enrollment": {command:["/app/api","gitctl","--name","gitstore-controller-manager","--replace-existing-key"], depends_on:{api:{}},
      networks:{"gitstore-network":null}, volumes:[
      {source:"controller",target:"/run/controller-secrets"},
      {source:"identity",target:"/run/controller-bootstrap"}]}
  }
}' >"$test_dir/valid.json"
cp "$test_dir/valid.json" "$SECRET_CONFIG_FIXTURE"
bash "$repo_root/scripts/check-local-compose-config.sh" >/dev/null

reject() {
  local name="$1" filter="$2"
  jq "$filter" "$test_dir/valid.json" >"$SECRET_CONFIG_FIXTURE"
  if bash "$repo_root/scripts/check-local-compose-config.sh" >"$test_dir/output" 2>&1; then
    echo "unsafe deployment accepted: $name" >&2
    exit 1
  fi
  if ! grep -q 'local Compose:' "$test_dir/output" || grep -q 'PRIVATE-MARKER' "$test_dir/output"; then
    echo "missing or sensitive deployment diagnostic: $name" >&2
    exit 1
  fi
}
reject shared-key-source '.services["controller-manager"].volumes[1].source = "issuer"'
reject writable-controller-key '.services["controller-manager"].volumes[1].read_only = false'
reject git-controller-key '.services["git-service"].volumes += [{source:"controller",target:"/private"}]'
reject api-controller-key '.services.api.volumes += [{source:"controller",target:"/private"}]'
reject wrong-config '.services.api.volumes[0].source = "PRIVATE-MARKER"'
reject wrong-identity '.services["serviceaccount-enrollment"].volumes[1].source = "other"'
reject duplicate-key-mount '.services.api.volumes += [.services.api.volumes[2]]'
reject missing-enrollment '.services["controller-manager"].depends_on = {}'
reject shell-api-command '.services.api.command = ["/bin/sh","-c","/app/api"]'
reject shell-api-healthcheck '.services.api.healthcheck.test = ["CMD","wget","http://localhost:4000/ready"]'
reject shellless-bootstrap '.services["credential-bootstrap"].build.target = "runtime"'
reject obsolete-cli-binary '.services["serviceaccount-enrollment"].command[0] = "/app/gitctl"'
reject legacy-enrollment-namespace '.services["serviceaccount-enrollment"].command += ["--namespace","controllers"]'
reject missing-account-name '.services["serviceaccount-enrollment"].command = ["--replace-existing-key"]'
reject legacy-controller-namespace '.services["controller-manager"].environment = {"GITSTORE_CONTROLLER__SERVICEACCOUNT__NAMESPACE":""}'
cp "$test_dir/valid.json" "$SECRET_CONFIG_FIXTURE"
cp "$CONFIG_FILE" "$test_dir/private.toml"
printf '\n[unsafe]\nsigning_key = "PRIVATE-MARKER"\n' >>"$test_dir/private.toml"
if CONFIG_FILE="$test_dir/private.toml" bash "$repo_root/scripts/check-local-compose-config.sh" >"$test_dir/output" 2>&1; then
  echo "shared private key accepted" >&2
  exit 1
fi
if grep -q 'PRIVATE-MARKER' "$test_dir/output"; then
  echo "shared-config rejection exposed material" >&2
  exit 1
fi
echo "secret configuration isolation checks passed"

# Both Scylla overlays must allow baseline migration/schema agreement before
# treating a not-yet-listening API as unhealthy. The base memdb grace is short.
for overlay in compose.scylla.yml compose.scylla.cluster.yml; do
  awk '/^  api:/{api=1; next} api && /^  [a-zA-Z]/{api=0} api{print}' "$repo_root/$overlay" |
    # Consume all input: grep -q can SIGPIPE awk under pipefail after a match.
    grep 'start_period: 5m' >/dev/null || {
      echo "missing first-boot migration health grace: $overlay" >&2
      exit 1
    }
done
