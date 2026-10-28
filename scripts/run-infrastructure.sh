#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
action="${1:-plan}"
[[ "${PROFILE:-digitalocean}" == digitalocean ]] || { echo 'PROFILE must be digitalocean' >&2; exit 2; }
tofu="${TOFU_BIN:-tofu}"
command -v "$tofu" >/dev/null || { echo 'Install OpenTofu or set TOFU_BIN to its executable path.' >&2; exit 127; }
directory="${repo_root}/deploy/digitalocean"
state_dir="${repo_root}/.gitstore/infrastructure/digitalocean"
plan="${state_dir}/deployment.tfplan"
umask 077

case "$action" in
  check)
    "$tofu" -chdir="$directory" fmt -check -recursive
    "$tofu" -chdir="$directory" init -backend=false -input=false
    "$tofu" -chdir="$directory" validate
    exec "$tofu" -chdir="$directory" test
    ;;
  init)
    [[ "${INFRA_BACKEND_CONFIG:-}" == /* && -f "$INFRA_BACKEND_CONFIG" ]] || {
      echo 'Set INFRA_BACKEND_CONFIG to an absolute path to a protected backend configuration file.' >&2; exit 2;
    }
    exec "$tofu" -chdir="$directory" init -input=false "-backend-config=$INFRA_BACKEND_CONFIG"
    ;;
  plan)
    [[ "${INFRA_VAR_FILE:-}" == /* && -f "$INFRA_VAR_FILE" ]] || {
      echo 'Set INFRA_VAR_FILE to an absolute path to your private deployment .tfvars file.' >&2; exit 2;
    }
    mkdir -p "$state_dir"
    exec "$tofu" -chdir="$directory" plan -input=false "-var-file=$INFRA_VAR_FILE" "-out=$plan"
    ;;
  show)
    [[ -f "$plan" ]] || { echo 'Create a plan first.' >&2; exit 2; }
    exec "$tofu" -chdir="$directory" show "$plan"
    ;;
  apply)
    [[ -f "$plan" ]] || { echo 'Create and review a plan first.' >&2; exit 2; }
    # Applying a saved plan is explicit authorization; there is no auto-apply
    # path from init/check/plan and no hidden destroy or replacement command.
    exec "$tofu" -chdir="$directory" apply "$plan"
    ;;
  output) exec "$tofu" -chdir="$directory" output -json inventory ;;
  *) echo 'ACTION must be init, plan, show, apply or output (check is used by make check).' >&2; exit 2 ;;
esac
