#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Private helpers sourced by shell regressions, not standalone test entrypoints.
shell_test_setup() {
  local name="$1"
  test_dir="${repo_root}/.gitstore/shell-tests/${name}.$$"
  mkdir -p "${repo_root}/.gitstore/shell-tests"
  mkdir -m 700 "${test_dir}"
  trap 'rm -rf "${test_dir}"' EXIT
}

write_mock() {
  local destination="$1"
  mkdir -p "$(dirname "${destination}")"
  cat >"${destination}"
  bash -n "${destination}"
  chmod +x "${destination}"
}

assert_rejected() {
  local message="$1" output="$2"
  shift 2
  if "$@" >"${output}" 2>&1; then
    echo "${message}" >&2
    return 1
  fi
}
