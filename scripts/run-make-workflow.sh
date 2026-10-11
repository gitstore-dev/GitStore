#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
family="${1:-}"
target="${2:-}"

usage() {
  cat >&2 <<'EOF'
usage:
  make generate TARGET=<authz|all>
  make check TARGET=<all|config|compose|licenses|credentials>
  make clean TARGET=<git-data|controller-checkpoints> CONFIRM=1
  make bootstrap TARGET=<all|token|namespace|repository>
  make secret TARGET=<jwt|grpc-hmac|signing-key> [DESTINATION_PATH=<path>] [RECORD_OUTPUT_PATH=<path>]
    signing-key defaults to controller PEM + JSON; RECORD_OUTPUT_PATH= requests PEM only
  make add-role ROLE=<role> ALLOW=<canonical-actions> [SCOPE=cluster|namespace]
  make assign-role ROLE=<role> SUBJECT=<name> [SUBJECT_KIND=User|Group|ServiceAccount] [ROLE_KIND=ClusterRole|Role] [SCOPE=cluster|namespace] [NAMESPACE=<explicit-binding-namespace>]
EOF
}

case "${family}/${target}" in
  rbac/role-add|rbac/binding-add)
    [[ -z "${DENY+x}" ]] || { echo 'DENY is unsupported; v1beta1 policy is allow-only' >&2; exit 2; }
    scope="${SCOPE:-cluster}"
    [[ "${scope}" == cluster || "${scope}" == namespace ]] || { echo 'SCOPE must be cluster or namespace' >&2; exit 2; }
    [[ -n "${ROLE:-}" ]] || { echo 'ROLE is required' >&2; exit 2; }
    policy_file="${POLICY_FILE:-${repo_root}/config/policy.yaml}"
    if [[ "${target}" == role-add ]]; then
      [[ -n "${ALLOW:-}" ]] || { echo 'ALLOW canonical actions are required' >&2; exit 2; }
      [[ -z "${RBAC_NAMESPACE:-}" ]] || { echo 'role definitions have no namespace; set namespace only on bindings' >&2; exit 2; }
      command=(go -C "${repo_root}/gitstore-api" run ./cmd/gitctl rbac role add --file "${policy_file}" --name "${ROLE}" --allow "${ALLOW}" --scope "${scope}")
    else
      [[ -n "${SUBJECT:-}" ]] || { echo 'SUBJECT is required' >&2; exit 2; }
      command=(go -C "${repo_root}/gitstore-api" run ./cmd/gitctl rbac binding add --file "${policy_file}" --subject "${SUBJECT}" --role "${ROLE}" --scope "${scope}" --subject-kind "${SUBJECT_KIND:-User}" --role-kind "${ROLE_KIND:-ClusterRole}")
      if [[ "${scope}" == namespace ]]; then
        [[ -n "${RBAC_NAMESPACE:-}" ]] || { echo 'namespace binding requires an explicit NAMESPACE (bootstrap default is not used)' >&2; exit 2; }
        command+=(--namespace "${RBAC_NAMESPACE}")
      else
        [[ -z "${RBAC_NAMESPACE:-}" ]] || { echo 'cluster binding must not set NAMESPACE' >&2; exit 2; }
      fi
    fi
    ;;
  generate/authz) command=(make --no-print-directory _generate-authz) ;;
  generate/all) command=(make --no-print-directory _generate-all) ;;
  check/all) command=(make --no-print-directory _check-all) ;;
  check/config) command=(make --no-print-directory _check-local-config) ;;
  check/compose) command=(make --no-print-directory _check-compose-config) ;;
  check/licenses) command=(make --no-print-directory _check-licenses) ;;
  check/credentials) command=(make --no-print-directory _check-credentials) ;;
  clean/git-data) command=(make --no-print-directory _clean-git-data) ;;
  clean/controller-checkpoints) command=(make --no-print-directory _clean-controller-checkpoints) ;;
  bootstrap/all) command=(make --no-print-directory _bootstrap-all) ;;
  bootstrap/token) command=(make --no-print-directory _bootstrap-token) ;;
  bootstrap/namespace) command=(make --no-print-directory _bootstrap-namespace) ;;
  bootstrap/repository) command=(make --no-print-directory _bootstrap-repository) ;;
  secret/jwt) command=(make --no-print-directory _secret-jwt) ;;
  secret/grpc-hmac) command=(make --no-print-directory _secret-grpc-hmac) ;;
  secret/signing-key) command=(make --no-print-directory _secret-signing-key) ;;
  *) usage; exit 2 ;;
esac

if [[ "${WORKFLOW_DRY_RUN:-0}" == "1" ]]; then
  printf 'family=%s target=%s command=' "${family}" "${target}"
  printf '%q ' "${command[@]}"
  printf '\n'
  exit 0
fi

cd "${repo_root}"
exec "${command[@]}"
