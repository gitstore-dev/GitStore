#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
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
if [[ ! "${target}" =~ ^gitstore-[a-zA-Z0-9_.-]+$ ]]; then
  echo "CHAOS_TARGET must be one explicit GitStore container name beginning with gitstore-" >&2
  exit 2
fi
case "${CHAOS_RBAC:-0}:${CHAOS_TRANSLATION:-0}" in
  0:0) ;;
  1:0|0:1)
    exec python3 - "${repo_root}" "${profile}" "${profile_file}" "${target}" <<'PY'
import json
import os
from pathlib import Path
import posixpath
import re
import signal
import subprocess
import sys
import time

os.umask(0o077)
root, profile, profile_file, target = sys.argv[1:]
domain = "translation" if os.environ.get("CHAOS_TRANSLATION", "0") == "1" else "rbac"
display = "Translation" if domain == "translation" else "RBAC"
owner = os.environ.get("CHAOS_OWNER", "")
label = os.environ.get("CHAOS_OWNERSHIP_LABEL", "com.docker.compose.project")
patterns = {
    "api-restart": r"gitstore-(?:[A-Za-z0-9_.-]+-)?api-[A-Za-z0-9_.-]+",
    "controller-restart": r"gitstore-(?:[A-Za-z0-9_.-]+-)?controller-[A-Za-z0-9_.-]+",
    "journal-interruption": r"gitstore-(?:[A-Za-z0-9_.-]+-)?(?:journal|scylla)(?:-[A-Za-z0-9_.-]+)?",
}

def require(condition, message):
    if not condition:
        raise RuntimeError(message)

def now():
    return time.time_ns() // 1_000_000

def docker(*args, timeout=5):
    return subprocess.check_output(["docker", *args], stderr=subprocess.PIPE, timeout=timeout)

def inspect(selector, ready=False, injector=False):
    values = json.loads(docker("inspect", selector))
    require(len(values) == 1, "one explicit container is required")
    value = values[0]
    required_label = "dev.gitstore.chaos-owner" if injector else label
    require(value.get("Id") and value.get("Config", {}).get("Labels", {}).get(required_label) == owner,
            "container ownership does not match")
    require(value.get("State", {}).get("StartedAt"), "process epoch is missing")
    if ready:
        require(value["State"].get("Running") and not value["State"].get("Paused"),
                "container must already be running and unpaused")
    return value

def checkpoint(value):
    destination = os.environ.get("CHAOS_CHECKPOINT_DESTINATION", "/var/lib/gitstore/checkpoints")
    require(destination.startswith("/") and not destination.startswith("//") and destination != "/" and posixpath.normpath(destination) == destination,
            "controller checkpoint destination must be an explicit absolute directory")
    mounts = [mount for mount in value.get("Mounts", []) if mount.get("Destination") == destination]
    require(len(mounts) == 1, "one explicit controller checkpoint mount is required")
    mount = mounts[0]
    source = mount.get("Source", "")
    require(mount.get("RW") and mount.get("Type") in ("volume", "bind") and source.startswith("/") and not source.startswith("//") and
            source != "/" and posixpath.normpath(source) == source,
            "controller checkpoint mount is not private writable retained storage")
    return source, mount.get("Name", "")

def sanitized(value):
    result = {key: value.get(key) for key in ("Id", "Name", "Image", "RestartCount")} | {
        "State": {key: value["State"].get(key) for key in ("Running", "Paused", "StartedAt", "OOMKilled")},
    }
    if profile == "controller-restart":
        source, volume = checkpoint(value)
        result["Checkpoint"] = {
            "source": source, "volume": volume, "writable": True,
            "destination": os.environ.get("CHAOS_CHECKPOINT_DESTINATION", "/var/lib/gitstore/checkpoints"),
        }
    return result

def write(path, value):
    with path.open("x", encoding="utf-8") as output:
        json.dump(value, output, indent=2)

try:
    require(profile in patterns and re.fullmatch(patterns[profile], target), display + " profile target role is invalid")
    require(owner and len(owner) <= 128 and re.fullmatch(r"[A-Za-z0-9_.-]+", label),
            display + " faults require an explicit owner and ownership label")
    start_text, end_text = (os.environ.get(name, "") for name in
                           ("CHAOS_WINDOW_START_MILLIS", "CHAOS_WINDOW_END_MILLIS"))
    require(re.fullmatch(r"[0-9]{1,16}", start_text) and re.fullmatch(r"[0-9]{1,16}", end_text),
            display + " faults require a predeclared millisecond window")
    start, end = int(start_text), int(end_text)
    require(0 < end - start <= 30_000 and start <= now() < end, display + " fault window is invalid or expired")
    config = json.loads(Path(profile_file).read_text())
    action = config["action"]
    require(action == ("pause" if profile == "journal-interruption" else "restart"), "unexpected fault action")
    duration = os.environ.get("CHAOS_DURATION", config.get("duration", ""))
    if action == "pause":
        parsed = re.fullmatch(r"([1-9][0-9]{0,4})(ms|s)", duration)
        require(parsed, "journal interruption requires a bounded millisecond/second duration")
        duration_ms = int(parsed[1]) * (1000 if parsed[2] == "s" else 1)
        require(duration_ms <= 20_000 and now() + duration_ms + 5000 < end,
                "journal interruption must leave restoration time inside its declared window")
    before = inspect(target, ready=True)
    target_id = before["Id"]
    peer_name, peer_before = None, None
    if profile == "controller-restart" or (domain == "translation" and profile == "api-restart"):
        peer_name = os.environ.get("CHAOS_PEER_TARGET", "")
        require(re.fullmatch(patterns[profile], peer_name) and peer_name != target, "an independent same-role peer is required")
        peer_before = inspect(peer_name, ready=True)
        require(peer_before["Id"] != target_id, "surviving peer aliases the target")
        if profile == "controller-restart":
            source, volume = checkpoint(before)
            peer_source, peer_volume = checkpoint(peer_before)
            require(source != peer_source and not source.startswith(peer_source + "/") and
                    not peer_source.startswith(source + "/") and not (volume and volume == peer_volume),
                    "controller checkpoint mounts overlap")
    run_id = os.environ.get("CHAOS_RUN_ID", time.strftime("%Y%m%dT%H%M%SZ", time.gmtime()))
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", run_id), "invalid fault run ID")
    directory = Path(os.environ.get("CHAOS_EVIDENCE_DIR", str(Path(root) / ".gitstore/chaos"))) / profile / run_id
    directory.parent.mkdir(parents=True, exist_ok=True)
    directory.mkdir(mode=0o700)
    write(directory / "target-before.json", [sanitized(before)])
    if peer_before:
        write(directory / "peer-before.json", [sanitized(peer_before)])
except (RuntimeError, ValueError, OSError, subprocess.SubprocessError) as error:
    print(display + " chaos preflight refused: " + str(error), file=sys.stderr)
    sys.exit(2)

process = None
after = None
restored = observed = False
status = injection_status = 1
injection_started = now()
image = os.environ.get("PUMBA_IMAGE", "ghcr.io/alexei-led/pumba:1.1.7")
cid_file = directory / "injector.cid"

def interrupted(signum, _frame):
    raise RuntimeError("fault runner interrupted by signal " + str(signum))

signal.signal(signal.SIGINT, interrupted)
signal.signal(signal.SIGTERM, interrupted)
try:
    require(start <= now() < end - 5000, "fault window expired before injection")
    args = [action]
    if action == "pause":
        args += ["--duration", duration]
    args += [target_id]
    with (directory / "pumba.log").open("xb") as output:
        process = subprocess.Popen(["docker", "run", "--rm", "--cidfile", str(cid_file),
            "--label", "dev.gitstore.chaos-owner=" + owner, "--label", "dev.gitstore.chaos-run=" + run_id,
            "-v", "/var/run/docker.sock:/var/run/docker.sock", image, "--log-level", "info", *args],
            stdout=output, stderr=output)
        deadline = min(end - 5000, injection_started + 20_000)
        while process.poll() is None:
            require(now() < deadline, "injector exceeded the bounded fault window")
            if action == "pause":
                current = inspect(target_id)
                require(current["Id"] == target_id, "target identity changed during injection")
                if current["State"].get("Paused") and not observed:
                    observed = True
                    write(directory / "target-during.json", [sanitized(current)])
            time.sleep(0.05)
        injection_status = process.returncode
        status = injection_status
except (RuntimeError, ValueError, OSError, subprocess.SubprocessError) as error:
    print(display + " chaos injection failed: " + str(error), file=sys.stderr)
    status = 1
finally:
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    if process is not None and process.poll() is None:
        try:
            if cid_file.exists():
                cid = cid_file.read_text().strip()
                require(re.fullmatch(r"[a-f0-9]{64}", cid), "invalid owned injector ID")
                injector = inspect(cid, injector=True)
                labels = injector["Config"]["Labels"]
                require(injector["Id"] == cid and labels.get("dev.gitstore.chaos-owner") == owner and
                        labels.get("dev.gitstore.chaos-run") == run_id, "injector ownership changed")
                docker("stop", "--time", "1", cid)
        except (RuntimeError, ValueError, OSError, subprocess.SubprocessError):
            status = 1
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)
    try:
        after = inspect(target_id)
        require(after["Id"] == target_id, "replacement target must never be restored or adopted")
        observed = observed or (action == "pause" and after["State"].get("Paused", False))
        if after["State"].get("Paused"):
            docker("unpause", target_id)
        if not after["State"].get("Running"):
            docker("start", target_id)
        after = inspect(target_id, ready=True)
        require(after["Id"] == target_id, "restored target identity changed")
        restored = True
        write(directory / "target-after.json", [sanitized(after)])
        if action == "restart":
            observed = after["State"]["StartedAt"] != before["State"]["StartedAt"]
            require(observed, "successful injector did not replace the target process epoch")
        else:
            require(after["State"]["StartedAt"] == before["State"]["StartedAt"], "source pause unexpectedly restarted its process")
        if profile == "controller-restart":
            require(checkpoint(after) == checkpoint(before), "restored controller checkpoint changed")
        if peer_before:
            peer_after = inspect(peer_name, ready=True)
            write(directory / "peer-after.json", [sanitized(peer_after)])
            require(peer_after["Id"] == peer_before["Id"] and
                    peer_after["State"]["StartedAt"] == peer_before["State"]["StartedAt"], "surviving peer changed")
            if profile == "controller-restart":
                require(checkpoint(peer_after) == checkpoint(peer_before), "surviving controller checkpoint changed")
    except (RuntimeError, ValueError, OSError, subprocess.SubprocessError) as error:
        print(display + " chaos restoration failed: " + str(error), file=sys.stderr)
        status = 1
    completed = now()
    confirmed = injection_status == 0 and status == 0 and observed and restored and start <= injection_started < completed <= end
    if not confirmed:
        status = status or 1
    write(directory / "metadata.json", {
        "schemaVersion": 1, "runId": run_id, "domain": domain, "kind": "physical", "profile": profile, "target": target, "targetId": target_id,
        "action": action, "ownership": {"label": label, "owner": owner},
        "injector": {"name": "pumba", "image": image}, "injectorExitCode": injection_status,
        "injected": injection_status == 0 and observed, "faultObserved": observed,
        "restorationRequired": True, "restored": restored, "exitCode": status,
        "faultWindow": {"startMillis": start, "endMillis": end, "injectionStartedMillis": injection_started,
                        "completedMillis": completed, "confirmed": confirmed},
        "processEpochs": {"before": before["State"]["StartedAt"], "after": after["State"]["StartedAt"] if after else ""},
        "sourceScope": "shared-journal-source" if action == "pause" else "one-replica",
        "domainRecoveryVerified": False,
    })
print("chaos evidence: " + str(directory))
sys.exit(status)
PY
    ;;
  *) echo "CHAOS_RBAC and CHAOS_TRANSLATION must each be 0 or 1 and cannot both be enabled" >&2; exit 2 ;;
esac
if [[ "$(jq -r '.requiresRBAC // false' "${profile_file}")" == true ]]; then
  echo "this profile requires CHAOS_RBAC=1 or CHAOS_TRANSLATION=1 and a predeclared owned fault window" >&2
  exit 2
fi
if ! docker inspect "${target}" >/dev/null 2>&1; then
  echo "target container does not exist: ${target}" >&2
  exit 2
fi

requires_ownership="$(jq -r '.requiresOwnership // false' "${profile_file}")"
owner="${CHAOS_OWNER:-}"
ownership_label="${CHAOS_OWNERSHIP_LABEL:-com.docker.compose.project}"
target_id=""
if [[ "${requires_ownership}" == true ]]; then
  pattern="$(jq -er '.targetPattern' "${profile_file}")"
  [[ "${target}" =~ ${pattern} ]] || { echo "profile requires an explicit identity-service target" >&2; exit 2; }
  [[ -n "${owner}" && "${ownership_label}" =~ ^[a-zA-Z0-9_.-]+$ ]] || {
    echo "identity faults require CHAOS_OWNER and a valid CHAOS_OWNERSHIP_LABEL" >&2
    exit 2
  }
  ownership="$(docker inspect "${target}")"
  jq -e --arg label "${ownership_label}" --arg owner "${owner}" \
    'length == 1 and .[0].Config.Labels[$label] == $owner and .[0].State.Running and (.[0].State.Paused | not)' \
    <<<"${ownership}" >/dev/null || { echo "identity target ownership or running state does not match" >&2; exit 2; }
  target_id="$(jq -er '.[0].Id' <<<"${ownership}")"
fi

action="$(jq -er '.action' "${profile_file}")"
duration="$(jq -r '.duration // empty' "${profile_file}")"
pumba_image="${PUMBA_IMAGE:-ghcr.io/alexei-led/pumba:1.1.7}"
run_id="${CHAOS_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
evidence_root="${CHAOS_EVIDENCE_DIR:-${repo_root}/.gitstore/chaos}"
evidence_dir="${evidence_root}/${profile}/${run_id}"
mkdir -p "${evidence_dir}"

record_target_state() {
  local output_file="$1"

  docker inspect "${target}" | jq '[.[] | {
    Id,
    Name,
    Image,
    Created,
    RestartCount,
    Config: {Image: .Config.Image},
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
      Error: .State.Error,
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
  --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg image "${pumba_image}" \
  '{schemaVersion:1, profile:$profile, target:$target, action:$action, startedAt:$started_at, injector:{name:"pumba", image:$image}}' \
  >"${evidence_dir}/metadata.json"

case "${action}" in
  restart)
    command_args=(restart "${target}")
    ;;
  pause)
    [[ -n "${duration}" ]] || { echo "pause profile requires duration" >&2; exit 2; }
    command_args=(pause --duration "${duration}" "${target}")
    ;;
  *)
    echo "unsupported safe chaos action: ${action}" >&2
    exit 2
    ;;
esac

set +e
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock "${pumba_image}" --log-level info "${command_args[@]}" 2>&1 | tee "${evidence_dir}/pumba.log"
status=${PIPESTATUS[0]}
set -e
injection_status="${status}"

restored=false
if [[ "${requires_ownership}" == true ]]; then
  if current="$(docker inspect "${target}" 2>/dev/null)" &&
    jq -e --arg label "${ownership_label}" --arg owner "${owner}" --arg id "${target_id}" \
      'length == 1 and .[0].Id == $id and .[0].Config.Labels[$label] == $owner' <<<"${current}" >/dev/null; then
    if [[ "$(jq -r '.[0].State.Running' <<<"${current}")" != true ]]; then
      docker start "${target_id}" >/dev/null || status=1
    fi
    if record_target_state "${evidence_dir}/target-after.json" &&
      jq -e --arg id "${target_id}" \
        'length == 1 and .[0].Id == $id and .[0].State.Running and (.[0].State.Paused | not)' \
        "${evidence_dir}/target-after.json" >/dev/null; then
      restored=true
    fi
  fi
  if [[ "${restored}" != true && "${status}" == 0 ]]; then status=1; fi
else
  record_target_state "${evidence_dir}/target-after.json" 2>/dev/null || true
fi
jq --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --argjson exit_code "${status}" \
  --argjson injection_exit_code "${injection_status}" \
  --argjson restoration_required "${requires_ownership}" --argjson restored "${restored}" \
  '. + {completedAt:$completed_at, exitCode:$exit_code, injectorExitCode:$injection_exit_code,
    injected:($injection_exit_code == 0),
    restorationRequired:$restoration_required, restored:$restored}' \
  "${evidence_dir}/metadata.json" >"${evidence_dir}/metadata.json.tmp"
mv "${evidence_dir}/metadata.json.tmp" "${evidence_dir}/metadata.json"

echo "chaos evidence: ${evidence_dir}"
exit "${status}"
