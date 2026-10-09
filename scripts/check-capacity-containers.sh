#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

mode="${1:-}"
before_file="${2:-}"
after_file="${3:-}"
targets_csv="${CAPACITY_DATASTORE_CONTAINERS:-}"
expected_count="${CAPACITY_DATASTORE_EXPECTED_COUNT:-0}"
expected_memory="${CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES:-0}"
expected_smp="${CAPACITY_DATASTORE_EXPECTED_SMP:-0}"

if [[ -z "${targets_csv}" ]]; then
  echo "CAPACITY_DATASTORE_CONTAINERS is required" >&2
  exit 2
fi
IFS=',' read -r -a targets <<<"${targets_csv}"
for target in "${targets[@]}"; do
  if [[ ! "${target}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]]; then
    echo "invalid capacity datastore container name: ${target}" >&2
    exit 2
  fi
done

snapshot() {
  local output_file="$1"
  docker inspect "${targets[@]}" | jq '[.[] |
    (((.Config.Entrypoint // []) + (.Config.Cmd // [])) | map(tostring) | join(" ")) as $command |
    {
      id:.Id,
      name:(.Name | ltrimstr("/")),
      image:.Config.Image,
      command:$command,
      smpPerNode:(try ($command | capture("(?:^|[[:space:]])--smp(?:=|[[:space:]])(?<smp>[1-9][0-9]*)(?:$|[[:space:]])").smp | tonumber) catch null),
      restartCount:.RestartCount,
      memoryLimitBytes:.HostConfig.Memory,
      nanoCPUs:.HostConfig.NanoCpus,
      cpusetCPUs:.HostConfig.CpusetCpus,
      state:{running:.State.Running, oomKilled:.State.OOMKilled, startedAt:.State.StartedAt}
    }
  ] | sort_by(.name)' >"${output_file}"
  if (( expected_count > 0 )); then
    for target in "${targets[@]}"; do
      membership="$(docker exec "${target}" nodetool status 2>/dev/null)" || {
        echo "cannot inspect Scylla membership through ${target}" >&2
        exit 1
      }
      live_nodes="$(awk '$1 == "UN" { count++ } END { print count + 0 }' <<<"${membership}")"
      jq --arg name "${target}" --argjson live_nodes "${live_nodes}" \
        'map(if .name == $name then . + {scyllaLiveNodes:$live_nodes} else . end)' \
        "${output_file}" >"${output_file}.tmp"
      mv "${output_file}.tmp" "${output_file}"
    done
  fi
  jq -e --argjson expected_count "${expected_count}" --argjson expected_memory "${expected_memory}" --argjson expected_smp "${expected_smp}" '
    ($expected_count == 0 or length == $expected_count) and
    ([.[].id] | unique | length) == length and
    ([.[].name] | unique | length) == length and
    all(.[];
      .state.running and (.state.oomKilled | not) and
      ($expected_memory == 0 or .memoryLimitBytes == $expected_memory) and
      ($expected_smp == 0 or .smpPerNode == $expected_smp) and
      ($expected_count == 0 or .scyllaLiveNodes == $expected_count)
    )
  ' "${output_file}" >/dev/null || {
    echo "datastore container identity, count, health, membership, memory limit, or runtime Scylla SMP does not match the declared environment" >&2
    exit 1
  }
}

case "${mode}" in
  snapshot)
    [[ -n "${before_file}" ]] || { echo "snapshot requires an output file" >&2; exit 2; }
    snapshot "${before_file}"
    ;;
  verify)
    [[ -r "${before_file}" && -n "${after_file}" ]] || { echo "verify requires readable before and output after files" >&2; exit 2; }
    snapshot "${after_file}"
    jq -n --slurpfile before "${before_file}" --slurpfile after "${after_file}" '
      ($before[0]) as $b | ($after[0]) as $a |
      [range(0; $b|length) as $i |
        ($b[$i]) as $old |
        ($a[] | select(.name == $old.name)) as $new |
        {name:$old.name,
         passed:($new.state.running and ($new.state.oomKilled|not) and
                 $new.id == $old.id and
                 $new.restartCount == $old.restartCount and
                 $new.state.startedAt == $old.state.startedAt),
         before:$old, after:$new}
      ] as $containers |
      {schemaVersion:1, passed:(($containers|length) == ($b|length) and ([$containers[].passed] | all)), containers:$containers}
    ' >"${after_file}.verification.json"
    jq -e '.passed' "${after_file}.verification.json" >/dev/null || {
      echo "datastore container OOM, restart, disappearance, or stopped state detected" >&2
      exit 1
    }
    ;;
  *)
    echo "usage: check-capacity-containers.sh snapshot <output> | verify <before> <after>" >&2
    exit 2
    ;;
esac
