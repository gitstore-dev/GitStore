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
  make check TARGET=<all|config|compose|licenses|credentials>
  make clean TARGET=<git-data|controller-checkpoints> CONFIRM=1
  make bootstrap TARGET=<all|token|namespace|repository>
  make secret TARGET=<jwt|grpc-hmac>
EOF
}

case "${family}/${target}" in
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
