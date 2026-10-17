#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ "${1:-}" == --capacity-window-check || "${1:-}" == --capacity-window ]]; then
  if [[ -z "${CAPACITY_CHAOS_TARGET:-}" || "${CAPACITY_CHAOS_CONFIRM:-0}" != 1 ]]; then
    echo "integrated chaos requires CAPACITY_CHAOS_TARGET and CAPACITY_CHAOS_CONFIRM=1" >&2
    exit 2
  fi
  if [[ ! "${CAPACITY_CHAOS_PROFILE:-}" =~ ^[a-z0-9][a-z0-9-]*$ ||
        ! -f "${repo_root}/tests/chaos/profiles/${CAPACITY_CHAOS_PROFILE}.json" ]]; then
    echo "integrated chaos requires a reviewed existing profile" >&2
    exit 2
  fi
  case "${CAPACITY_TARGET:-}/${CAPACITY_SCENARIO:-}" in
    api/readiness|namespace/admission|category/hierarchy|repository/lifecycle) ;;
    *) echo "integrated chaos requires a profile with a declared sustained load window" >&2; exit 2 ;;
  esac
  if [[ "${REPOSITORY_CAPACITY_SECRET_SCENARIO:-0}" == 1 ]]; then
    echo "integrated chaos cannot overlap the full secret scenario's managed fault schedule" >&2
    exit 2
  fi
  chaos_delay="${CAPACITY_CHAOS_DELAY:-30s}"
  if [[ ! "${chaos_delay}" =~ ^[0-9]+([.][0-9]+)?[smh]?$ || ${#chaos_delay} -gt 20 ]]; then
    echo "CAPACITY_CHAOS_DELAY must be bounded nonnegative seconds, minutes or hours (for example 30s)" >&2
    exit 2
  fi
  chaos_delay_ms="$(jq -en --arg delay "${chaos_delay}" '
    $delay | capture("^(?<value>[0-9]+([.][0-9]+)?)(?<unit>[smh]?)$") |
    (.value | tonumber) * (if .unit == "h" then 3600000 elif .unit == "m" then 60000 else 1000 end) |
    select(isfinite and . >= 0 and . <= 7200000) | ceil')"
  if [[ "$1" == --capacity-window-check ]]; then exit 0; fi
  evidence_dir="${2:?capacity evidence directory required}"
  load_log="${3:?capacity load log required}"
  run_id="${CAPACITY_RUN_ID:?capacity run ID required}"
  waiter_pid=""
  cleanup_window_waiter() {
    if [[ -n "${waiter_pid}" ]] && kill -0 "${waiter_pid}" 2>/dev/null; then
      kill "${waiter_pid}" 2>/dev/null || true
      wait "${waiter_pid}" 2>/dev/null || true
    fi
  }
  trap cleanup_window_waiter EXIT
  trap 'exit 143' TERM
  trap 'exit 130' INT
  while :; do
    if [[ -s "${load_log}" ]]; then
      window_line="$(grep -m 1 -E '(^CAPACITY_LOAD_WINDOW |"msg"[[:space:]]*:[[:space:]]*"CAPACITY_LOAD_WINDOW )' "${load_log}" || true)"
      if [[ -n "${window_line}" ]]; then
        if [[ "${window_line}" == "CAPACITY_LOAD_WINDOW "* ]]; then
          window_json="${window_line#CAPACITY_LOAD_WINDOW }"
        else
          window_json="$(printf '%s\n' "${window_line}" | jq -er '.msg | ltrimstr("CAPACITY_LOAD_WINDOW ")')"
        fi
        printf '%s\n' "${window_json}" | jq -e --arg run "${run_id}" '
          select(keys == ["endMs","runId","schemaVersion","startMs"] and
            .schemaVersion == 1 and .runId == $run and
            ([.startMs,.endMs] | all(type == "number" and isfinite and
              . > 0 and . <= 9007199254740991 and floor == .)) and
            .endMs > .startMs)
        ' >"${evidence_dir}/chaos-load-window.json"
        break
      fi
    fi
    sleep 0.1
  done
  injection_ms="$(jq -er --argjson delay "${chaos_delay_ms}" '
    . as $window | .startMs + $delay | select(. < $window.endMs)
  ' "${evidence_dir}/chaos-load-window.json")"
  remaining="$(jq -n --argjson at "${injection_ms}" '[(($at - (now * 1000)) / 1000),0] | max')"
  sleep "${remaining}" &
  waiter_pid=$!
  wait "${waiter_pid}"
  waiter_pid=""
  jq -e 'now * 1000 >= .startMs and now * 1000 < .endMs' \
    "${evidence_dir}/chaos-load-window.json" >/dev/null ||
    { echo "chaos delay missed the declared load window" >&2; exit 124; }
  printf '%s\n' "${injection_ms}" >"${evidence_dir}/chaos-injection-started"
  CHAOS_PROFILE="${CAPACITY_CHAOS_PROFILE}" \
    CHAOS_TARGET="${CAPACITY_CHAOS_TARGET}" \
    CHAOS_CONFIRM=1 \
    CHAOS_COMPOSE_PROJECT="${COMPOSE_PROJECT_NAME:-}" \
    CHAOS_READINESS_URL="${CAPACITY_CHAOS_READINESS_URL:-}" \
    CHAOS_POLICY_DIR="${CAPACITY_CHAOS_POLICY_DIR:-}" \
    CHAOS_RUN_ID="${run_id}" \
    CHAOS_EVIDENCE_DIR="${evidence_dir}/chaos" \
    "${BASH_SOURCE[0]}" "${CAPACITY_CHAOS_PROFILE}"
  jq -e --slurpfile window "${evidence_dir}/chaos-load-window.json" '
    .injected == true and .exitCode == 0 and
    ([.startedAtMs,.completedAtMs] | all(type == "number" and isfinite and floor == .)) and
    .startedAtMs >= $window[0].startMs and .completedAtMs >= .startedAtMs and
    .completedAtMs < $window[0].endMs and
    (if .recovery.required then
      .recovery.passed == true and
      (.recovery.completedAtMs | type == "number" and isfinite and floor == .) and
      .recovery.completedAtMs >= .completedAtMs and
      .recovery.completedAtMs < $window[0].endMs
    else true end)
  ' "${evidence_dir}/chaos/${CAPACITY_CHAOS_PROFILE}/${run_id}/metadata.json" >/dev/null ||
    { echo "fault injection and recovery did not complete within the declared load window" >&2; exit 124; }
  exit 0
fi
profile="${1:-${CHAOS_PROFILE:-}}"
profile_file="${repo_root}/tests/chaos/profiles/${profile}.json"
if [[ -z "${profile}" || ! "${profile}" =~ ^[a-z0-9][a-z0-9-]*$ || ! -f "${profile_file}" ]]; then
  echo "usage: make chaos CHAOS_PROFILE=<profile> CHAOS_TARGET=<gitstore-container> CHAOS_CONFIRM=1" >&2
  exit 2
fi
if [[ "${CHAOS_CONFIRM:-0}" != "1" ]]; then
  echo "refusing fault injection without CHAOS_CONFIRM=1" >&2
  exit 2
fi

target="${CHAOS_TARGET:-}"
project="${CHAOS_COMPOSE_PROJECT:-}"
if [[ -n "${project}" ]]; then
  if [[ ! "${target}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]+$ ]]; then
    echo "CHAOS_TARGET must be one explicit Compose-owned container name" >&2
    exit 2
  fi
elif [[ ! "${target}" =~ ^gitstore-[a-zA-Z0-9_.-]+$ ]]; then
  echo "CHAOS_TARGET must be one explicit GitStore container name beginning with gitstore-" >&2
  exit 2
fi
if [[ -n "${project}" ]] && ! docker inspect "${target}" | jq -e --arg project "${project}" '
  length == 1 and .[0].Config.Labels["com.docker.compose.project"] == $project
' >/dev/null; then
  echo "target container does not belong to the explicitly selected Compose project" >&2
  exit 2
fi
if ! docker inspect "${target}" >/dev/null 2>&1; then
  echo "target container does not exist: ${target}" >&2
  exit 2
fi

action="$(jq -er '.action' "${profile_file}")"
duration="$(jq -r '.duration // empty' "${profile_file}")"
readiness_kind="$(jq -r '.readinessKind // empty' "${profile_file}")"
readiness_url="${CHAOS_READINESS_URL:-$(jq -r '.readinessURL // empty' "${profile_file}")}"
recovery_budget_ms=0
if [[ -n "${readiness_kind}" ]]; then
  expected_recovery="$(jq -er '.expectedRecovery' "${profile_file}")"
  if [[ ! "${expected_recovery}" =~ ^[1-9][0-9]{0,2}s$ ||
        ("${readiness_kind}" != api && "${readiness_kind}" != controller) ]]; then
    echo "readiness profiles require a supported kind and bounded expectedRecovery" >&2
    exit 2
  fi
  recovery_budget_ms=$((${expected_recovery%s} * 1000))
  python3 - "${readiness_url}" "${readiness_kind}" <<'PY'
import sys
from urllib.parse import urlsplit
try:
    url = urlsplit(sys.argv[1])
    path = "/ready" if sys.argv[2] == "api" else "/health"
    valid = (url.scheme == "http" and url.hostname in ("127.0.0.1", "localhost", "::1")
             and url.port is not None and 0 < url.port <= 65535 and url.path == path
             and url.username is None and url.password is None
             and not url.query and not url.fragment)
except ValueError:
    valid = False
if not valid:
    sys.exit("CHAOS_READINESS_URL must be a credential-free container-loopback readiness URL")
PY
elif [[ -n "${CHAOS_READINESS_URL:-}" ]]; then
  echo "this profile has no declared readiness probe" >&2
  exit 2
fi
pumba_image="${PUMBA_IMAGE:-ghcr.io/alexei-led/pumba:1.1.7}"
run_id="${CHAOS_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CHAOS_EVIDENCE_DIR:-${repo_root}/.gitstore/chaos}"
evidence_dir="${evidence_root}/${profile}/${run_id}"
mkdir -p "${evidence_root}/${profile}"
if ! mkdir "${evidence_dir}"; then
  echo "refusing to overwrite an existing fault run; choose a fresh CHAOS_RUN_ID" >&2
  exit 2
fi
cleanup_probe_files() {
  rm -f "${evidence_dir}/readiness-body.tmp" "${evidence_dir}/readiness-before.json.tmp" \
    "${evidence_dir}/readiness-after.json.tmp" "${evidence_dir}/metadata.json.tmp"
}
trap cleanup_probe_files EXIT

monotonic_ms() {
  python3 -c 'import time; print(time.monotonic_ns() // 1000000)'
}

bounded_docker() {
  python3 - "$@" <<'PY'
import subprocess
import sys
try:
    result = subprocess.run(["docker", *sys.argv[2:]], timeout=float(sys.argv[1]))
except subprocess.TimeoutExpired:
    sys.exit("bounded Docker observation timed out")
sys.exit(result.returncode)
PY
}

remaining_timeout() {
  local remaining
  remaining=$(($1 - $(monotonic_ms)))
  (( remaining > 0 )) || return 1
  jq -n --argjson remaining "${remaining}" '[$remaining / 1000,2] | min'
}

record_readiness() {
  local deadline="$1" output="$2" timeout hash
  local codes
  timeout="$(remaining_timeout "${deadline}")" || return 1
  set +e
  bounded_docker "${timeout}" exec "${target}" wget -q -T 1 -O - "${readiness_url}" \
    2>>"${evidence_dir}/recovery-probe.log" |
    head -c 1048577 >"${evidence_dir}/readiness-body.tmp"
  codes=("${PIPESTATUS[@]}")
  set -e
  if (( codes[0] != 0 || codes[1] != 0 )) ||
      (( $(wc -c <"${evidence_dir}/readiness-body.tmp") > 1048576 )); then
    rm -f "${evidence_dir}/readiness-body.tmp"
    echo "readiness observation unavailable or oversized" >>"${evidence_dir}/recovery-probe.log"
    return 1
  fi
  if ! jq -es --arg kind "${readiness_kind}" '
    def count: type == "number" and isfinite and floor == . and . >= 0;
    def digest: type == "string" and test("^[0-9a-f]{64}$");
    def provider: type == "string" and test("^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$");
    select(length == 1) | .[0] |
    if $kind == "api" then
      select(.status == "healthy" and (.auth | type == "object")) |
      .auth | {authn,authz,userdir,catalogDigest,policy:
        (if .policy == null then null else
          .policy | {loaded,apiVersion,digest,roles,bindings,rules}
        end)} |
      select((.authn | type == "array" and length > 0 and length <= 64 and
        (unique | length) == length and all(.[]; provider)) and
        (.authz | provider) and (.userdir | provider) and (.catalogDigest | digest) and
        (if .policy == null then .authz != "rbac-local" else
          (.policy | type == "object" and .loaded == true and
            (.apiVersion | type == "string" and length > 0 and length <= 128) and
            (.digest | digest) and (.roles | count) and .roles <= 200 and
            (.bindings | count) and .bindings <= 1000 and (.rules | count) and .rules <= 2000)
        end) and (if .authz == "rbac-local" then
          .policy.apiVersion == "rbac.authorization.gitstore.dev/v1beta1" else true end)) |
      {kind:$kind,auth:.}
    else
      select(.status == "ok" and .ready == true and .credentialReady == true and
        (.kinds | type == "object" and length > 0 and length <= 16) and
        any(.kinds[]; .registered == true) and
        all(.kinds[]; (.registered | type == "boolean") and
          (if .registered then .stalled == false and .recovering == false and .poisonItems == 0 and
            (.activeWorkers | count) and (.queueDepth | count) else true end))) |
      {kind:$kind,registeredKinds:([.kinds | to_entries[] | select(.value.registered) | .key] | sort)}
    end
  ' "${evidence_dir}/readiness-body.tmp" >"${output}.tmp" 2>/dev/null; then
    rm -f "${evidence_dir}/readiness-body.tmp" "${output}.tmp"
    echo "readiness observation rejected an invalid or unready contract" >>"${evidence_dir}/recovery-probe.log"
    return 1
  fi
  rm -f "${evidence_dir}/readiness-body.tmp"
  timeout="$(remaining_timeout "${deadline}")" || { rm -f "${output}.tmp"; return 1; }
  if ! hash="$(bounded_docker "${timeout}" exec "${target}" sha256sum /proc/1/exe \
      2>>"${evidence_dir}/recovery-probe.log")" ||
      [[ ! "${hash}" =~ ^[0-9a-f]{64}[[:space:]]+/proc/1/exe$ ]]; then
    rm -f "${output}.tmp"
    echo "readiness executable identity could not be verified" >>"${evidence_dir}/recovery-probe.log"
    return 1
  fi
  if ! jq --arg hash "${hash:0:64}" '. + {executableSHA256:$hash}' "${output}.tmp" >"${output}"; then
    rm -f "${output}.tmp"
    return 1
  fi
  rm -f "${output}.tmp"
}

record_target_state() {
  local output_file="$1"

  bounded_docker "${2:-5}" inspect "${target}" | jq '[.[] | {
    Id,
    Name,
    Image,
    Created,
    RestartCount,
    Mounts: [.Mounts[]? | {Type,Name,Source,Destination,RW}],
    Config: {Image: .Config.Image, Labels: {
      "com.docker.compose.project": .Config.Labels["com.docker.compose.project"]
    }},
    HostConfig: {
      Memory: .HostConfig.Memory,
      NanoCpus: .HostConfig.NanoCpus,
      CpuPeriod: .HostConfig.CpuPeriod,
      CpuQuota: .HostConfig.CpuQuota,
      CpusetCpus: .HostConfig.CpusetCpus,
      PidsLimit: .HostConfig.PidsLimit,
      RestartPolicy: .HostConfig.RestartPolicy
    },
    State: {
      Status: .State.Status,
      Running: .State.Running,
      Paused: .State.Paused,
      Restarting: .State.Restarting,
      OOMKilled: .State.OOMKilled,
      Dead: .State.Dead,
      ExitCode: .State.ExitCode,
      HasError: ((.State.Error // "") | length > 0),
      StartedAt: .State.StartedAt,
      FinishedAt: .State.FinishedAt,
      Health: (if .State.Health == null then null else {
        Status: .State.Health.Status,
        FailingStreak: .State.Health.FailingStreak
      } end)
    }
  }]' >"${output_file}"
}

record_target_state "${evidence_dir}/target-before.json"
jq -n --arg profile "${profile}" --arg target "${target}" --arg action "${action}" \
  --arg project "${project}" --argjson started_at_ms "$(jq -n 'now * 1000 | floor')" \
  --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg image "${pumba_image}" \
  '{schemaVersion:1, profile:$profile, target:$target, composeProject:$project, action:$action,
    startedAt:$started_at, startedAtMs:$started_at_ms, injector:{name:"pumba", image:$image}}' \
  >"${evidence_dir}/metadata.json"
if [[ "${action}" == reload-invalid-policy ]]; then
  jq '.injector = {name:"docker-signal",signal:"SIGHUP"}' "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
  mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"
fi

if [[ -n "${readiness_kind}" ]] &&
    ! record_readiness "$(($(monotonic_ms) + 5000))" "${evidence_dir}/readiness-before.json"; then
  echo "target is not ready with a verifiable baseline; no fault injected" >&2
  jq '. + {exitCode:2,injected:false,recovery:{required:true,passed:false,reason:"invalid baseline"}}' \
    "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
  mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"
  exit 2
fi

case "${action}" in
  restart)
    command_args=(restart "${target}")
    ;;
  pause)
    [[ -n "${duration}" ]] || { echo "pause profile requires duration" >&2; exit 2; }
    command_args=(pause --duration "${duration}" "${target}")
    ;;
  reload-invalid-policy)
    [[ -n "${project}" && -n "${CHAOS_POLICY_DIR:-}" && "${readiness_kind}" == api ]] ||
      { echo "policy reload requires CHAOS_COMPOSE_PROJECT and CHAOS_POLICY_DIR" >&2; exit 2; }
    ;;
  *)
    echo "unsupported safe chaos action: ${action}" >&2
    exit 2
    ;;
esac

recovery_started_monotonic_ms="$(monotonic_ms)"
recovery_deadline=$((recovery_started_monotonic_ms + recovery_budget_ms))
jq '. + {startedAt:(now | strftime("%Y-%m-%dT%H:%M:%SZ")),startedAtMs:(now * 1000 | floor)}' \
  "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"
if [[ "${action}" == reload-invalid-policy ]]; then
  set +e
  python3 -I - "${target}" "${project}" "${CHAOS_POLICY_DIR}" "${evidence_dir}" "${readiness_url}" "${repo_root}" "${recovery_deadline}" <<'PY'
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import signal
import sys
import time

target, project, directory, evidence, readiness_url, repository, deadline_ms = sys.argv[1:]
directory = Path(directory)
evidence = Path(evidence)
deadline = int(deadline_ms) / 1000 - 10
def docker(*args):
    remaining = deadline - time.monotonic()
    assert remaining > 0, "policy reload deadline exceeded"
    output = subprocess.check_output(["docker", *args], timeout=min(5, remaining), stderr=subprocess.STDOUT)
    assert len(output) <= 1048576, "Docker observation exceeds evidence bound"
    return output
def inspect():
    records = json.loads(docker("inspect", target))
    assert len(records) == 1, "expected exactly one target"
    return records[0]
assert directory.is_absolute() and directory.resolve() == directory, "policy directory must be absolute and non-symlinked"
assert directory.is_dir() and not directory.stat().st_mode & 0o022, "policy directory must not be group/world writable"
assert not directory.is_relative_to(Path(repository).resolve()), "repository policy directories are not fault fixtures"
assert directory.stat().st_uid == os.getuid(), "policy directory must belong to the invoking user"
assert {p.name for p in directory.iterdir()} == {"policy.yaml", "owned-policy.json"}, "policy directory must be dedicated to this fixture"
lock = os.open(directory, os.O_RDONLY)
fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
owner_path = directory / "owned-policy.json"
assert not owner_path.is_symlink() and owner_path.is_file()
assert 0 < owner_path.stat().st_size <= 4096 and owner_path.stat().st_uid == os.getuid()
owner_bytes = owner_path.read_bytes()
assert len(owner_bytes) <= 4096
owner = json.loads(owner_bytes)
before = inspect()
assert owner == {"schemaVersion": 1, "composeProject": project, "containerId": before["Id"], "purpose": "policy-reload"}, "policy ownership marker does not match target"
assert before["Config"]["Labels"]["com.docker.compose.project"] == project
env = [v.split("=", 1)[1] for v in before["Config"]["Env"] if v.startswith("GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE=")]
assert len(env) == 1
policy_path = PurePosixPath(env[0])
assert policy_path.is_absolute() and policy_path.name == "policy.yaml"
mounts = [m for m in before["Mounts"] if m["Destination"] == str(policy_path.parent)]
assert len(mounts) == 1 and mounts[0]["Type"] == "bind" and mounts[0]["RW"] is False
source = mounts[0]["Source"]
if source.startswith("/host_mnt/"):
    assert docker("info", "--format", "{{.OperatingSystem}}").decode().strip() == "Docker Desktop"
    source = source.removeprefix("/host_mnt")
assert Path(source).resolve() == directory, "target must mount this dedicated policy directory"
policy = directory / "policy.yaml"
assert not policy.is_symlink() and policy.is_file()
assert 0 < policy.stat().st_size <= 1048576
original = policy.read_bytes()
mode = policy.stat().st_mode & 0o777
assert not mode & 0o022, "policy must not be group/world writable"
baseline = json.loads((evidence / "readiness-before.json").read_text())
assert baseline["auth"]["authz"] == "rbac-local" and baseline["auth"]["policy"]["loaded"] is True
fault = {"injected": False, "restored": False, "originalPolicySHA256": hashlib.sha256(original).hexdigest()}
def install(content):
    temporary = directory / "policy.yaml.chaos-candidate"
    created = False
    try:
        with temporary.open("xb") as stream:
            created = True
            stream.write(content)
            stream.flush()
            os.fsync(stream.fileno())
        temporary.chmod(mode)
        blocked = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGTERM, signal.SIGINT})
        try:
            temporary.replace(policy)
            if content == b"apiVersion: [\n":
                fault["injected"] = True
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, blocked)
    finally:
        if created:
            temporary.unlink(missing_ok=True)
    mounted = docker("exec", target, "sha256sum", str(policy_path)).decode().split()[0]
    assert mounted == hashlib.sha256(content).hexdigest(), "mounted candidate differs"
def signal_and_observe(marker):
    since = f"{time.time():.6f}"
    docker("kill", "--signal", "HUP", target)
    observation_deadline = min(deadline, time.monotonic() + 5)
    while True:
        output = docker("logs", "--since", since, "--tail", "500", target)
        assert len(output) <= 1048576, "native reload log exceeds evidence bound"
        matched = []
        for line in output.splitlines():
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(record, dict) and (record.get("msg") == marker.decode() or record.get("message") == marker.decode()):
                matched.append(line)
        if matched:
            assert len(matched) == 1, "ambiguous concurrent registry reloads"
            retained = b"\n".join(matched) + b"\n"
            name = "restoration" if marker == b"auth providers reloaded atomically" else "rejection"
            (evidence / ("policy-" + name + ".jsonl")).write_bytes(retained)
            return hashlib.sha256(retained).hexdigest()
        assert time.monotonic() < observation_deadline, "missing native JSON registry reload outcome"
        time.sleep(0.05)
def check_active():
    observed = json.loads(docker("exec", target, "wget", "-q", "-T", "1", "-O", "-", readiness_url))
    active = observed["auth"]
    normalized = {key: active[key] for key in baseline["auth"] if key != "policy"}
    normalized["policy"] = {key: active["policy"][key] for key in baseline["auth"]["policy"]}
    assert observed["status"] == "healthy" and normalized == baseline["auth"], "active providers differ from baseline"
def interrupt(signum, frame):
    raise SystemExit("policy reload interrupted; restoring original policy")
signal.signal(signal.SIGTERM, interrupt)
signal.signal(signal.SIGINT, interrupt)
try:
    install(b"apiVersion: [\n")
    fault["rejectionLogSHA256"] = signal_and_observe(b"auth provider reload failed; previous configuration remains active")
    check_active()
finally:
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    # Cleanup has its own bound, but never extends the outer acceptance deadline.
    deadline = time.monotonic() + 10
    try:
        if fault["injected"]:
            install(original)
            fault["restorationLogSHA256"] = signal_and_observe(b"auth providers reloaded atomically")
            after = inspect()
            assert after["Id"] == before["Id"] and after["State"]["StartedAt"] == before["State"]["StartedAt"]
            assert after["State"]["Pid"] == before["State"]["Pid"] and after["Config"] == before["Config"]
            assert after["HostConfig"] == before["HostConfig"] and after["Mounts"] == before["Mounts"]
            assert policy.read_bytes() == original
            check_active()
            fault["restored"] = True
    finally:
        (evidence / "policy-reload.json").write_text(json.dumps(fault, indent=2) + "\n")
PY
  status=$?
  set -e
else
  set +e
  docker run --rm -v /var/run/docker.sock:/var/run/docker.sock "${pumba_image}" --log-level info "${command_args[@]}" 2>&1 | tee "${evidence_dir}/pumba.log"
  status=${PIPESTATUS[0]}
  set -e
fi

jq --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson exit_code "${status}" \
  '. + {completedAt:$completed_at,completedAtMs:(now * 1000 | floor),exitCode:$exit_code,
    injectorExitCode:$exit_code,injected:($exit_code == 0)}' \
  "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"
if [[ "${action}" == reload-invalid-policy && -f "${evidence_dir}/policy-reload.json" ]]; then
  jq --slurpfile fault "${evidence_dir}/policy-reload.json" \
    '. + {injected:$fault[0].injected,policyReload:$fault[0]}' \
    "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
  mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"
fi

recovery_passed=false
recovery_reason="not requested"
recovery_elapsed_ms=0
recovery_attempts=0
if [[ -n "${readiness_kind}" ]]; then
  recovery_reason="injector failed"
  if (( status == 0 )); then
    recovery_reason="readiness deadline exceeded"
    while (( $(monotonic_ms) < recovery_deadline )); do
      recovery_attempts=$((recovery_attempts + 1))
      if record_readiness "${recovery_deadline}" "${evidence_dir}/readiness-after.json"; then
        jq -nc --argjson attempt "${recovery_attempts}" \
          '{attempt:$attempt,atMs:(now * 1000 | floor),ready:true}' >>"${evidence_dir}/recovery-observations.jsonl"
        timeout="$(remaining_timeout "${recovery_deadline}")" || break
        if ! record_target_state "${evidence_dir}/target-after.json" "${timeout}"; then
          recovery_reason="target state unavailable"
          break
        fi
        if ! jq -e --slurpfile before "${evidence_dir}/readiness-before.json" \
            'length == 1 and .[0] == $before[0]' \
            --slurp "${evidence_dir}/readiness-after.json" >/dev/null ||
            ! jq -e --arg action "${action}" --slurpfile before "${evidence_dir}/target-before.json" '
              select(length == 1) | .[0] as $after | $before[0][0] as $before |
              ($before.Id | type == "string" and length > 0) and
              ($before.Image | type == "string" and length > 0) and
              $after.Id == $before.Id and $after.Image == $before.Image and
              $after.Config == $before.Config and $after.HostConfig == $before.HostConfig and
              $after.Mounts == $before.Mounts and $after.State.Running == true and
              $after.State.Paused == false and $after.State.OOMKilled == false and
              $after.State.Restarting == false and $after.State.Dead == false and
              $after.State.HasError == false and
              (if $action == "restart" then
                ($before.State.StartedAt | type == "string" and length > 0) and
                ($after.State.StartedAt | type == "string" and length > 0) and
                $after.State.StartedAt != $before.State.StartedAt
              else $after.State.StartedAt == $before.State.StartedAt end)
            ' "${evidence_dir}/target-after.json" >/dev/null; then
          recovery_reason="readiness, executable or retained-container identity changed"
          break
        fi
        recovery_elapsed_ms=$(($(monotonic_ms) - recovery_started_monotonic_ms))
        if (( recovery_elapsed_ms <= recovery_budget_ms )); then
          recovery_passed=true
          recovery_reason="ready with unchanged snapshot, executable and retained container"
        fi
        break
      fi
      jq -nc --argjson attempt "${recovery_attempts}" \
        '{attempt:$attempt,atMs:(now * 1000 | floor),ready:false}' >>"${evidence_dir}/recovery-observations.jsonl"
      sleep 0.1
    done
  fi
  recovery_elapsed_ms=$(($(monotonic_ms) - recovery_started_monotonic_ms))
  if (( recovery_elapsed_ms > recovery_budget_ms )); then
    recovery_passed=false
    recovery_reason="readiness deadline exceeded"
  fi
  if [[ "${recovery_passed}" != true ]]; then
    echo "fault recovery failed: ${recovery_reason}" >&2
    if (( status == 0 )); then status=1; fi
  fi
else
  if ! record_target_state "${evidence_dir}/target-after.json"; then
    echo "post-fault container observation failed" >&2
    if (( status == 0 )); then status=1; fi
  fi
fi
jq --argjson status "${status}" --arg kind "${readiness_kind}" --arg reason "${recovery_reason}" \
  --argjson budget "${recovery_budget_ms}" --argjson elapsed "${recovery_elapsed_ms}" \
  --argjson attempts "${recovery_attempts}" \
  --argjson passed "${recovery_passed}" \
  '. + {exitCode:$status,recovery:{required:($kind != ""),kind:$kind,budgetMilliseconds:$budget,
    elapsedMilliseconds:$elapsed,attempts:$attempts,passed:$passed,reason:$reason,
    completedAtMs:(now * 1000 | floor)}}' \
  "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"

echo "chaos evidence: ${evidence_dir}"
exit "${status}"
