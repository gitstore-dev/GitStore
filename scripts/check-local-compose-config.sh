#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -eu

config_file=${CONFIG_FILE:-./config/config.toml}
output=$(mktemp)
trap 'rm -f "$output"' EXIT

CONFIG_FILE="$config_file" docker compose --profile local \
  -f compose.yml -f compose.local.yml config >"$output"

for service in git-service api controller-manager; do
  grep -A80 "^  $service:" "$output" | grep -q -- '--config-file'
  grep -A80 "^  $service:" "$output" | grep -q 'target: /config/gitstore.toml'
done

test "$(grep -c 'target: /config/gitstore.toml' "$output")" -eq 3
test "$(grep -c 'target: /config/policy.yaml' "$output")" -eq 1
test "$(grep -c 'read_only: true' "$output")" -ge 4
grep -q "source: $(cd "$(dirname "$config_file")" && pwd)/$(basename "$config_file")" "$output"

echo "local Compose configuration is valid for $config_file"
