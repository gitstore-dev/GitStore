#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

# Both HTTP and container exec must describe the same process epoch.
mock_api_metrics() {
  local replica="$1" start=100 instance="instance-$1" replaced=0
  if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" &&
        -e "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}" ]]; then
    replaced=1
  fi
  case "${replica}" in
    a) [[ "${replaced}" != 1 ]] || instance=instance-a-replacement ;;
    b)
      [[ "${MOCK_SAME_START:-0}" == 1 ]] || start=200
      if [[ "${MOCK_REPLACE_ALL:-0}" == 1 && "${replaced}" == 1 ]]; then
        instance=instance-b-replacement
      fi
      ;;
    *) return 1 ;;
  esac
  printf 'process_start_time_seconds %s\ngitstore_api_process_instance_info{instance_id="%s"} 1\n' \
    "${start}" "${instance}"
}

capacity_service_containers() {
  jq -n --arg revision "$1" --arg digest "$2" '
    [ {Id:"api-id-1",Name:"/api-1",Path:"/app/api"},
      {Id:"api-id-2",Name:"/api-2",Path:"/app/api"},
      {Id:"git-id-1",Name:"/git-1",Path:"/app/git-service"} ] |
    map(. + {
      Image:("sha256:"+$digest),
      Config:{
        Image:("ghcr.io/gitstore-dev/" + (if .Name == "/git-1" then "git-service" else "api" end) + "@sha256:"+$digest),
        Labels:{"org.opencontainers.image.revision":$revision}
      },
      State:{Running:true}
    })'
}
