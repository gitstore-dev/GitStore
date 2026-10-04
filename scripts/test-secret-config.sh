#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT
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
    api: {command:["--config-file","/etc/gitstore/gitstore.toml"], depends_on:{"credential-bootstrap":{}}, volumes:[
      {source:$config,target:"/etc/gitstore/gitstore.toml",read_only:true},
      {source:"policy",target:"/etc/gitstore/policy.yaml",read_only:true},
      {source:"issuer",target:"/run/secrets",read_only:true}]},
    "controller-manager": {command:["--config-file","/etc/gitstore/gitstore.toml"],
      depends_on:{"serviceaccount-enrollment":{}}, volumes:[
      {source:$config,target:"/etc/gitstore/gitstore.toml",read_only:true},
      {source:"controller",target:"/run/secrets",read_only:true}]},
    "credential-bootstrap": {command:["rm -f /run/controller-bootstrap/serviceaccount.env"], volumes:[
      {source:"issuer",target:"/run/api-issuer"},
      {source:"controller",target:"/run/controller-secrets"},
      {source:"identity",target:"/run/controller-bootstrap"}]},
    "serviceaccount-enrollment": {command:["--replace-existing-key"], depends_on:{api:{}},
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
