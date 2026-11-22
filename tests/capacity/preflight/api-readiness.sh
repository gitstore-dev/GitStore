#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
. "$(dirname "${BASH_SOURCE[0]}")/../lib/preflight.sh"

evidence_dir="${1:?evidence directory is required}"
mode="$(capacity_preflight_mode)"
output="${evidence_dir}/preflight-environment.json"
translation_live_topology() {
  python3 - "${evidence_dir}" <<'PY'
import datetime
import hashlib
import json
import math
import os
import pathlib
import posixpath
import re
import subprocess
import sys
from urllib.parse import urlparse

root = pathlib.Path(sys.argv[1])
output = root / "translation-topology.json"
output.unlink(missing_ok=True)

def require(condition, message):
    if not condition:
        raise ValueError(message)

def command(*arguments, timeout=15):
    result = subprocess.run(arguments, capture_output=True, timeout=timeout, check=False)
    require(result.returncode == 0, "live topology command failed: " + arguments[0])
    require(len(result.stdout) <= 16 * 1024 * 1024, "live topology command exceeded output budget")
    return result.stdout.decode("utf-8")

def document(name):
    path = root / name
    require(path.stat().st_size <= 1024 * 1024, "topology manifest exceeds input budget")
    return json.loads(path.read_text())

def names(variable, count):
    values = os.environ.get(variable, "").split(",")
    require(len(values) == len(set(values)) == count and
            all(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", value) for value in values),
            variable + " must identify distinct explicit containers")
    return values

def urls(values, count):
    require(len(values) == len(set(values)) == count, "distinct process endpoints required")
    for value in values:
        parsed = urlparse(value)
        require(parsed.scheme in ("http", "https") and parsed.hostname and
                not parsed.username and not parsed.password and parsed.path in ("", "/") and
                not parsed.query and not parsed.fragment and not re.search(r"[\s,]", value),
                "process endpoint must be an explicit Admin/health root URL")
    return [value.rstrip("/") for value in values]

def retained_mount(container, destination):
    require(isinstance(destination, str) and destination.startswith("/") and destination != "/" and
            posixpath.normpath(destination) == destination, "explicit retained mount destination required")
    mounts = [mount for mount in container["Mounts"] if mount["Destination"] == destination]
    require(len(mounts) == 1, "one retained mount is required")
    mount = mounts[0]
    source = mount.get("Source", "")
    require(mount.get("RW") and mount.get("Type") in ("bind", "volume") and
            source.startswith("/") and source != "/" and posixpath.normpath(source) == source,
            "retained mount must be private writable storage")
    return {"source": source, "volume": mount.get("Name", ""), "destination": destination}

def overlap(left, right):
    return left == right or left.startswith(right + "/") or right.startswith(left + "/")

def identity(metrics, role):
    starts = re.findall(r"^process_start_time_seconds ([^\s]+)$", metrics, re.M)
    instances = re.findall(
        rf'^gitstore_{role}_process_instance_info\{{instance_id="([^"\r\n]+)"\}} 1$', metrics, re.M)
    require(len(starts) == len(instances) == 1, "one process epoch/identity required")
    start = float(starts[0])
    require(math.isfinite(start) and start > 0 and len(instances[0]) <= 128, "invalid process epoch/identity")
    return instances[0], start

try:
    metadata, config, environment = document("metadata.json"), document("config.json"), document("environment.json")
    for key in ("config", "environment"):
        require(hashlib.sha256((root / (key + ".json")).read_bytes()).hexdigest() == metadata[key + "Digest"],
                "live topology manifests differ from tested configuration/environment")
    revision = metadata["gitRevision"]
    require(re.fullmatch(r"[a-f0-9]{40}", revision) and metadata["gitDirty"] is False and
            re.fullmatch(r"[a-f0-9]{64}", metadata["sourceStateSha256"]),
            "live immutable-source topology requires a clean source-attributed verifier")
    require(config["schemaVersion"] == environment["schemaVersion"] == 1, "unsupported topology manifest")
    services, topology = config["services"], environment["topology"]
    declaration = services["translationTopology"]
    for role, count, key in (("api", 2, "apiReplicas"), ("controller", 2, "controllerReplicas")):
        require(services[role]["replicas"] == topology[key] == count and services[role]["build"] == "release",
                "Translation requires two release APIs and two release controllers")
    require(services["gitService"]["build"] == "release" and topology["gitServiceReplicas"] == 1,
            "Translation requires a singleton release Git service")
    require(services["scylla"]["nodes"] == topology["scyllaNodes"] == 3 and
            type(services["scylla"]["smpPerNode"]) is int and
            services["scylla"]["smpPerNode"] == topology["scyllaSmpPerNode"] >= 1,
            "Translation requires a three-node Scylla topology")
    require(environment["runtime"] == "docker" and environment["architecture"] and
            type(environment["host"]["memoryBytes"]) is int and environment["host"]["memoryBytes"] > 0 and
            type(environment["host"]["logicalCPUs"]) is int and environment["host"]["logicalCPUs"] > 0,
            "bounded Docker host declarations required")
    runtime = json.loads(command("docker", "info", "--format", "{{json .}}"))
    architectures = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "amd64": "amd64"}
    architecture = architectures.get(runtime["Architecture"])
    require(runtime["OSType"] == "linux" and architecture is not None and
            architectures.get(environment["architecture"]) == architecture and
            runtime["MemTotal"] == environment["host"]["memoryBytes"] and
            runtime["NCPU"] == environment["host"]["logicalCPUs"],
            "actual Docker host differs from environment declaration")
    owner = os.environ.get("CAPACITY_OWNED_PROJECT", "")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,127}", owner), "explicit owned project required")
    api_names = names("CAPACITY_API_CONTAINERS", 2)
    controller_names = names("CAPACITY_CONTROLLER_CONTAINERS", 2)
    git_names = names("CAPACITY_GIT_SERVICE_CONTAINER", 1)
    scylla_names = names("CAPACITY_SCYLLA_CONTAINERS", 3)
    selected = api_names + controller_names + git_names + scylla_names
    require(len(set(selected)) == 8, "container roles overlap")
    inspected = json.loads(command("docker", "inspect", *selected))
    require(len(inspected) == 8 and len({item["Id"] for item in inspected}) == 8, "distinct live containers required")
    containers = {item["Name"].lstrip("/"): item for item in inspected}
    require(set(containers) == set(selected), "inspection does not match declared names")
    owned_ids = command("docker", "ps", "--no-trunc", "--filter", "label=com.docker.compose.project=" + owner, "-q").split()
    require(len(owned_ids) == len(set(owned_ids)) == 8 and set(owned_ids) == {item["Id"] for item in inspected},
            "owned project must contain exactly the declared two APIs/two controllers/one Git/three Scylla nodes")
    roles = ["api", "api", "controller", "controller", "gitService", "scylla", "scylla", "scylla"]
    sandbox_ids = set()
    for name, role in zip(selected, roles):
        container = containers[name]
        state, host = container["State"], container["HostConfig"]
        require(state["Running"] and not state["Paused"] and not state["OOMKilled"] and state["StartedAt"] and
                container["Config"]["Labels"]["com.docker.compose.project"] == owner, "container is not live/owned")
        limits = declaration["limits"][role]
        require(all(type(limits[key]) is int and limits[key] > 0 for key in ("nanoCPUs", "memoryBytes")) and
                host["NanoCpus"] == limits["nanoCPUs"] and host["Memory"] == limits["memoryBytes"],
                "actual container CPU/memory ceilings differ from configuration")
        sandbox = container["NetworkSettings"]["SandboxID"]
        require(isinstance(sandbox, str) and sandbox and sandbox not in sandbox_ids and
                host["NetworkMode"] not in ("host", "none") and not host["NetworkMode"].startswith("container:") and
                host.get("PidMode", "") in ("", "private"), "private non-overlapping process/network namespaces required")
        sandbox_ids.add(sandbox)
        image = container["Image"]
        reference = container["Config"]["Image"]
        require(re.fullmatch(r"sha256:[a-f0-9]{64}", image) and
                (reference == image or re.search(r"@sha256:[a-f0-9]{64}$", reference)), "immutable images required")
        if role != "scylla":
            images = json.loads(command("docker", "image", "inspect", image))
            require(len(images) == 1 and images[0]["Id"] == image and
                    images[0]["Os"] == "linux" and architectures.get(images[0]["Architecture"]) == architecture and
                    images[0]["Config"]["Labels"]["org.opencontainers.image.revision"] == revision,
                    "actual core image does not match tested source revision")
    api_endpoints = urls(os.environ.get("CAPACITY_API_ENDPOINTS", "").split(","), 2)
    controller_endpoints = urls([os.environ.get("CAPACITY_CONTROLLER_A", ""), os.environ.get("CAPACITY_CONTROLLER_B", "")], 2)
    require(len(set(api_endpoints + controller_endpoints)) == 4, "cross-role endpoint aliases")
    helper_image = containers[api_names[0]]["Image"]

    def probe(container, *arguments):
        user = container["Config"].get("User") or "0"
        require(re.fullmatch(r"[0-9]+(?::[0-9]+)?", user), "explicit numeric probe user required")
        target = "container:" + container["Id"]
        # /proc/1/exe requires ptrace-read permission when the target retains capabilities.
        capabilities = ["--cap-add=SYS_PTRACE"] if arguments == ("process-executable",) else []
        return command("docker", "run", "--rm", "--pull=never", "--pid", target, "--network", target,
                       "--user", user, "--entrypoint", "/app/api", "--read-only", "--cap-drop=ALL",
                       *capabilities,
                       "--security-opt", "no-new-privileges", "--cpus=0.25", "--memory=128m",
                       "--pids-limit=64", helper_image, "gitctl", *arguments)

    processes, observed = [], set()
    for index, (name, role) in enumerate(zip(selected[:5], roles[:5])):
        container = containers[name]
        executable = "/app/" + {"api": "api", "controller": "controller-manager", "gitService": "git-service"}[role]
        require(not any(overlap(executable, mount["Destination"]) for mount in container["Mounts"]),
                "runtime mount can override the immutable executable")
        require(probe(container, "process-executable").strip() == executable, "PID 1 is not the expected immutable binary")
        process = {"role": role, "container": name, "containerID": container["Id"], "imageID": container["Image"],
                   "startedAt": container["State"]["StartedAt"], "executable": executable,
                   "networkSandboxID": container["NetworkSettings"]["SandboxID"],
                   "limits": declaration["limits"][role]}
        if role != "gitService":
            offset = index if role == "api" else index - 2
            ports = declaration["apiPorts" if role == "api" else "controllerPorts"]
            require(len(ports) == 2 and all(type(port) is int and 1 <= port <= 65535 for port in ports),
                    "two explicit internal metrics ports per role required")
            endpoint = (api_endpoints if role == "api" else controller_endpoints)[offset]
            local = "http://127.0.0.1:" + str(ports[offset])
            external_metrics = command("curl", "-fsS", "--max-time", "5", "--max-filesize", "16777216", endpoint + "/metrics")
            local_metrics = probe(container, "metrics", "--url", local + "/metrics", "--timeout", "5s")
            external, internal = identity(external_metrics, role), identity(local_metrics, role)
            require(external == internal and external[0] not in observed, "endpoint does not identify a distinct declared process")
            observed.add(external[0])
            process.update(endpoint=endpoint, instanceID=external[0], processStartTimeSeconds=external[1])
            if role == "api":
                probe(container, "metrics", "--url", local + "/ready/admin", "--timeout", "5s")
            else:
                health = json.loads(probe(container, "metrics", "--url", local + "/health", "--timeout", "5s"))
                kind = health["kinds"]["Translation"]
                require(health["ready"] is True and kind["recovering"] is False and
                        kind["stalled"] is False and kind["poisonItems"] == 0, "Translation controller is not operationally ready")
                process["checkpoint"] = retained_mount(container, declaration["checkpointDestination"])
        else:
            process["repositories"] = retained_mount(container, declaration["repositoryDestination"])
            mount = process["repositories"]
            identifiers = command("docker", "ps", "--no-trunc", "-q").split()
            require(0 < len(identifiers) <= 256 and len(set(identifiers)) == len(identifiers) and
                    container["Id"] in identifiers, "running-container mount scan exceeds its bound or misses Git")
            records = [json.loads(line) for line in command(
                "docker", "inspect", "--format", '{"id":{{json .Id}},"mounts":{{json .Mounts}}}',
                *identifiers).splitlines()]
            require(len(records) == len(identifiers) and {item["id"] for item in records} == set(identifiers),
                    "incomplete running-container mount scan")
            consumers = []
            for record in records:
                if any((item.get("Source") and overlap(mount["source"], item["Source"])) or
                       (mount["volume"] and mount["volume"] == item.get("Name")) for item in record["mounts"]):
                    consumers.append(record["id"])
            require(consumers == [container["Id"]], "Git retained storage has another active consumer")
        processes.append(process)
    left, right = processes[2]["checkpoint"], processes[3]["checkpoint"]
    require(not overlap(left["source"], right["source"]) and not (left["volume"] and left["volume"] == right["volume"]),
            "controller checkpoint storage overlaps")
    members, scylla = None, []
    for name in scylla_names:
        container = containers[name]
        arguments = container["Args"]
        smp = services["scylla"]["smpPerNode"]
        require(("--smp" in arguments and arguments[arguments.index("--smp") + 1] == str(smp)) or
                "--smp=" + str(smp) in arguments, "live Scylla SMP does not match configuration")
        status = command("docker", "exec", container["Id"], "nodetool", "status", timeout=30)
        nodes = re.findall(r"^UN\s+(\S+)", status, re.M)
        require(len(nodes) == len(set(nodes)) == 3 and not re.search(r"^D[NLMJ]\s", status, re.M),
                "all three Scylla members must be up")
        require(members is None or members == set(nodes), "Scylla nodes disagree on membership")
        members = set(nodes)
        scylla.append({"container": name, "containerID": container["Id"], "imageID": container["Image"],
                      "startedAt": container["State"]["StartedAt"], "members": sorted(members),
                      "smp": smp, "limits": declaration["limits"]["scylla"]})
    def fingerprint(item):
        return {key: item[key] for key in ("Id", "Image", "HostConfig", "Mounts", "NetworkSettings")} | {
            "state": {key: item["State"][key] for key in ("StartedAt", "Running", "Paused", "OOMKilled")}}
    after = json.loads(command("docker", "inspect", *selected))
    require({item["Id"]: fingerprint(item) for item in after} ==
            {item["Id"]: fingerprint(item) for item in inspected},
            "live topology changed while being verified")
    for key in ("config", "environment"):
        require(hashlib.sha256((root / (key + ".json")).read_bytes()).hexdigest() == metadata[key + "Digest"],
                "topology manifests changed during verification")
    receipt = {key: metadata[key] for key in ("runId", "sourceStateSha256", "configDigest", "environmentDigest")}
    receipt.update(schemaVersion=1, passed=False, evidenceClass="diagnostic", liveTopologyVerified=True,
                   sourceRevision=revision, owner=owner, processes=processes, scylla=scylla,
                   verifiedAt=datetime.datetime.now(datetime.timezone.utc).isoformat(),
                   measurementScope="live immutable-image/PID/endpoint/ownership/configured-ceiling/storage/membership joins; not sustained resource or rollout certification")
    output.write_text(json.dumps(receipt, indent=2) + "\n")
    output.chmod(0o600)
except (OSError, ValueError, KeyError, TypeError, IndexError, subprocess.SubprocessError) as error:
    print("Translation topology refused: " + str(error), file=sys.stderr)
    sys.exit(2)
PY
}
case "${CAPACITY_TRANSLATION_READINESS:-0}" in
  0) ;;
  1)
    [[ "${CAPACITY_IDENTITY_READINESS:-0}" == 0 ]] || { echo "select one readiness domain" >&2; exit 2; }
    [[ "${mode}" == diagnostic ]] || {
      echo "Translation acceptance is unavailable until the connected push collector and full-window verifier are wired; diagnostic reads cannot certify acceptance" >&2
      exit 2
    }
    [[ "${TRANSLATION_TEST_OWNED_DEPLOYMENT:-0}" == 1 ]] || { echo "Translation reads require an explicitly owned deployment" >&2; exit 2; }
    python3 - "${CAPACITY_TRANSLATION_FIXTURE_FILE:-}" "${CAPACITY_RUN_ID:-}" "${CAPACITY_API_ENDPOINTS:-}" "${evidence_dir}" <<'PY'
import hashlib
import json
import os
import pathlib
import re
import stat
import sys
from urllib.parse import urlparse

try:
    path, run_id, raw_endpoints, evidence = sys.argv[1:]
    fixture_path = pathlib.Path(path)
    attributes = fixture_path.stat()
    assert stat.S_ISREG(attributes.st_mode) and not attributes.st_mode & 0o077, "Translation fixture must be a protected regular file"
    assert attributes.st_uid == os.getuid(), "Translation fixture must belong to this runner"
    assert attributes.st_size <= 16 * 1024 * 1024, "Translation fixture exceeds its bounded input budget"
    fixture = json.loads(fixture_path.read_text())
    assert run_id and fixture["runId"] == run_id, "Translation fixture must belong to this run"
    endpoints = raw_endpoints.split(",")
    assert len(set(endpoints)) == len(endpoints) == 2, "two distinct API endpoints required"
    for endpoint in endpoints:
        parsed = urlparse(endpoint)
        assert parsed.scheme in ("http", "https") and parsed.hostname and not parsed.username and not parsed.password
        assert parsed.path in ("", "/") and not parsed.query and not parsed.fragment, "Admin root URLs required"
    cases = fixture["cases"]
    assert len(cases) == 10, "ten independently verified source cases required"
    namespaces = set()
    for case in cases:
        assert case["query"].lstrip().startswith("query("), "only typed source queries are allowed"
        source = case["expected"]["translation"]
        namespace = case["variables"]["by"]["namespacePath"]["namespace"]
        name = case["variables"]["by"]["namespacePath"]["name"]
        assert namespace.startswith("translation-") and source["metadata"]["namespace"] == namespace
        assert source["kind"] == "Translation" and source["metadata"]["name"] == name
        assert source["metadata"]["uid"] and source["metadata"]["resourceVersion"]
        generation, revision = source["metadata"]["generation"], source["metadata"]["revision"]
        assert generation > 0 and revision and source["status"]["observedGeneration"] == generation
        assert source["status"]["lastAppliedRevision"] == revision
        assert any(condition["type"] == "Ready" and condition["status"] == "TRUE" and
                   condition["observedGeneration"] == generation for condition in source["status"]["conditions"])
        namespaces.add(namespace)
    assert len(namespaces) == 10, "source cases must cover ten distinct owned namespaces"
    rate = int(os.environ.get("CAPACITY_RATE", "20"))
    assert 1 <= rate <= 1000, "Translation read rate must be between 1 and 1000"
    duration = re.fullmatch(r"([1-9][0-9]*)(s|m|h)", os.environ.get("CAPACITY_DURATION", "30m"))
    assert duration, "Translation duration must use whole seconds, minutes or hours"
    seconds = int(duration[1]) * {"s": 1, "m": 60, "h": 3600}[duration[2]]
    assert seconds <= 7200, "Translation diagnostic window exceeds two hours"
    if os.environ.get("CAPACITY_TRANSLATION_PUSHES", "0") == "1":
        assert seconds <= 5400, "connected Translation windows cannot exceed ninety minutes; the two-hour child deadline must retain setup and recovery time"
    metadata = json.loads((pathlib.Path(evidence) / "metadata.json").read_text())
    assert metadata["runId"] == run_id and metadata["mode"] == "diagnostic"
    assert re.fullmatch(r"[a-f0-9]{64}", metadata["sourceStateSha256"]), "tested source attribution required"
    receipt = {
        "schemaVersion": 1, "evidenceClass": "diagnostic", "runId": run_id,
        "sourceStateSha256": metadata["sourceStateSha256"], "rate": rate, "durationSeconds": seconds,
        "fixtureSha256": hashlib.sha256(fixture_path.read_bytes()).hexdigest(),
        "apiEndpoints": endpoints, "cases": len(cases),
    }
    (pathlib.Path(evidence) / "translation-preflight.json").write_text(json.dumps(receipt, indent=2) + "\n")
except (OSError, ValueError, KeyError, TypeError, AssertionError) as error:
    print(str(error) or "invalid Translation source fixture", file=sys.stderr)
    sys.exit(2)
PY
    [[ -n "${CAPACITY_TOKEN:-}" ]] || { echo "Translation reads require authenticated CAPACITY_TOKEN or CAPACITY_TOKEN_FILE" >&2; exit 2; }
    if [[ -r "${evidence_dir}/config.json" ]] &&
      jq -e '.services.translationTopology != null' "${evidence_dir}/config.json" >/dev/null; then
      translation_live_topology
    fi
    ;;
  *) echo "CAPACITY_TRANSLATION_READINESS must be 0 or 1" >&2; exit 2 ;;
esac
case "${CAPACITY_API_READINESS_ROLE:-}" in
  "") readiness_path=/ready ;;
  admin) readiness_path=/ready/admin ;;
  *) echo "CAPACITY_API_READINESS_ROLE must be empty or admin" >&2; exit 2 ;;
esac

if [[ "${mode}" == "diagnostic" ]]; then
  jq -n --arg mode "${mode}" \
    '{schemaVersion:1,mode:$mode,passed:true,note:"diagnostic evidence does not assert deployment topology"}' \
    >"${output}"
  exit 0
fi

config="${evidence_dir}/config.json"
environment="${evidence_dir}/environment.json"
[[ -r "${config}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_CONFIG_MANIFEST" >&2; exit 2; }
[[ -r "${environment}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_ENVIRONMENT_MANIFEST" >&2; exit 2; }
[[ -n "${CAPACITY_BASE_URL:-}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_BASE_URL" >&2; exit 2; }
[[ -n "${CAPACITY_API_ENDPOINTS:-}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_API_ENDPOINTS" >&2; exit 2; }
[[ -n "${CAPACITY_API_CONTAINERS:-}" ]] || { echo "${mode} API readiness evidence requires CAPACITY_API_CONTAINERS" >&2; exit 2; }

config_replicas="$(jq -er '.services.api.replicas | select(type == "number" and . >= 2)' "${config}")" || {
  echo "${mode} API readiness config must declare at least two API replicas" >&2
  exit 2
}
config_build="$(jq -er '.services.api.build | select(. == "release")' "${config}")" || {
  echo "${mode} API readiness config must identify a release API build" >&2
  exit 2
}
environment_replicas="$(jq -er '.topology.apiReplicas | select(type == "number" and . >= 2)' "${environment}")" || {
  echo "${mode} API readiness environment must declare at least two API replicas" >&2
  exit 2
}
environment_memory="$(jq -er '.host.memoryBytes | select(type == "number" and . > 0)' "${environment}")" || {
  echo "${mode} API readiness environment must declare positive host memory" >&2
  exit 2
}
jq -e '
  .schemaVersion == 1 and
  (.runtime | strings | length) > 0 and
  (.architecture | strings | length) > 0 and
  (.host.logicalCPUs | numbers) > 0
' "${environment}" >/dev/null || {
  echo "${mode} API readiness environment must declare runtime, architecture, and logical CPUs" >&2
  exit 2
}
(( config_replicas == environment_replicas )) || { echo "API replica counts differ between manifests" >&2; exit 2; }
[[ "${CAPACITY_API_BUILD:-}" == "${config_build}" ]] || {
  echo "CAPACITY_API_BUILD=release must match the config manifest" >&2
  exit 2
}
[[ "${CAPACITY_API_REPLICAS:-}" =~ ^[1-9][0-9]*$ ]] && (( CAPACITY_API_REPLICAS == config_replicas )) || {
  echo "CAPACITY_API_REPLICAS must match the running topology declared by both manifests" >&2
  exit 2
}
[[ "${CAPACITY_RUNTIME_MEMORY_BYTES:-}" =~ ^[1-9][0-9]*$ ]] && (( CAPACITY_RUNTIME_MEMORY_BYTES == environment_memory )) || {
  echo "CAPACITY_RUNTIME_MEMORY_BYTES must match the environment manifest" >&2
  exit 2
}

IFS=',' read -r -a api_endpoints <<<"${CAPACITY_API_ENDPOINTS}"
IFS=',' read -r -a api_containers <<<"${CAPACITY_API_CONTAINERS}"
(( ${#api_endpoints[@]} == config_replicas )) || {
  echo "CAPACITY_API_ENDPOINTS count must match the declared API replicas" >&2
  exit 2
}
(( ${#api_containers[@]} == config_replicas )) || {
  echo "CAPACITY_API_CONTAINERS count must match the declared API replicas" >&2
  exit 2
}
for container in "${api_containers[@]}"; do
  [[ "${container}" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || { echo "invalid API container name: ${container}" >&2; exit 2; }
done
inspection="$(docker inspect "${api_containers[@]}")" || {
  echo "cannot inspect live API containers" >&2
  exit 1
}
source_revision="$(git rev-parse HEAD)"
container_json="$(jq -c '[.[] | {
  id:.Id,name:(.Name | ltrimstr("/")),running:.State.Running,
  imageReference:.Config.Image,imageID:.Image,revision:(.Config.Labels["org.opencontainers.image.revision"] // ""),executable:.Path
}]' <<<"${inspection}")"
jq -e --argjson expected "${config_replicas}" --arg revision "${source_revision}" '
  length == $expected and all(.[]; .running) and
  ([.[].id] | unique | length) == length and
  ([.[].name] | unique | length) == length and
  all(.[];
    .revision == $revision and
    ((.imageReference | test("@sha256:[0-9a-f]{64}$")) or
      (.imageReference == .imageID and (.imageID | test("^sha256:[0-9a-f]{64}$")))))
' <<<"${container_json}" >/dev/null || {
  echo "CAPACITY_API_CONTAINERS must identify distinct running digest-pinned release containers for the tested revision" >&2
  exit 1
}
identity_json='[]'
for index in "${!api_endpoints[@]}"; do
  endpoint="${api_endpoints[$index]}"
  container="${api_containers[$index]}"
  # Inspect live PID 1 rather than trusting Docker's configured command.
  # The static runtime intentionally has neither a shell nor readlink.
  running_executable="$(docker exec "${container}" /app/api gitctl process-executable)"
  [[ "${running_executable}" == /app/api ]] || {
    echo "API container PID 1 must run the release /app/api binary: ${container}" >&2
    exit 1
  }
  [[ "${endpoint}" =~ ^https?://[^[:space:],]+$ ]] || { echo "invalid live API endpoint: ${endpoint}" >&2; exit 2; }
  curl -fsS --max-time 5 "${endpoint%/}/health" >/dev/null || {
    echo "live API endpoint failed health probe: ${endpoint}" >&2
    exit 1
  }
  curl -fsS --max-time 5 "${endpoint%/}${readiness_path}" >/dev/null || {
    echo "live API endpoint failed readiness probe: ${endpoint}" >&2
    exit 1
  }
  metrics="$(curl -fsS --max-time 5 "${endpoint%/}/metrics")" || {
    echo "cannot scrape live API endpoint: ${endpoint}" >&2
    exit 1
  }
  process_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${metrics}")"
  [[ "${process_start}" =~ ^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$ ]] || {
    echo "live API endpoint does not expose process_start_time_seconds: ${endpoint}" >&2
    exit 1
  }
  instance_id="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${metrics}" | head -1)"
  [[ -n "${instance_id}" ]] || {
    echo "live API endpoint does not expose gitstore_api_process_instance_info: ${endpoint}" >&2
    exit 1
  }
  container_metrics="$(docker exec "${container}" /app/api gitctl metrics)" || {
    echo "cannot scrape API metrics inside container: ${container}" >&2
    exit 1
  }
  container_start="$(awk '$1 == "process_start_time_seconds" { print $2; exit }' <<<"${container_metrics}")"
  container_instance="$(sed -n 's/^gitstore_api_process_instance_info{instance_id="\([^"]*\)"} [01]$/\1/p' <<<"${container_metrics}" | head -1)"
  [[ "${process_start}" == "${container_start}" && "${instance_id}" == "${container_instance}" ]] || {
    echo "API endpoint ${endpoint} does not map to container ${container}" >&2
    exit 1
  }
  container_id="$(jq -r --arg name "${container}" '.[] | select(.name == $name) | .id' <<<"${container_json}")"
  identity_json="$(jq -c --arg endpoint "${endpoint%/}" --arg process_start "${process_start}" \
    --arg instance_id "${instance_id}" --arg container "${container}" --arg container_id "${container_id}" \
    '. + [{endpoint:$endpoint,instanceID:$instance_id,container:$container,containerID:$container_id,processStartTimeSeconds:($process_start|tonumber)}]' <<<"${identity_json}")"
done
[[ "$(jq '[.[].instanceID] | unique | length' <<<"${identity_json}")" == "${config_replicas}" ]] || {
  echo "CAPACITY_API_ENDPOINTS must identify distinct live API process instances" >&2
  exit 1
}
base_url="${CAPACITY_BASE_URL%/}"
jq -e --arg base_url "${base_url}" '([.[].endpoint] | index($base_url)) != null' <<<"${identity_json}" >/dev/null || {
  echo "CAPACITY_BASE_URL must identify one of the verified CAPACITY_API_ENDPOINTS" >&2
  exit 2
}

if [[ "${CAPACITY_IDENTITY_READINESS:-0}" == 1 ]]; then
  fixture="${CAPACITY_IDENTITY_FIXTURE_FILE:-}"
  [[ -r "${fixture}" ]] || { echo "identity readiness requires a private CAPACITY_IDENTITY_FIXTURE_FILE" >&2; exit 2; }
  jq -e '
    (.cases | type == "array" and length >= 3) and
    all(.cases[];
      (.kind == "Staff" or .kind == "Customer" or .kind == "ServiceAccount") and
      (.allowed | type == "boolean") and (.token | strings | length > 0) and
      (.actorID | strings | length > 0) and (.groups | type == "array") and
      (.groups | unique | length) == (.groups | length)) and
    ([.cases[].kind] | unique | length) == 3 and
    any(.cases[]; .kind == "Customer" and .allowed == false) and
    any(.cases[]; .kind == "Staff" and .allowed == true) and
    any(.cases[]; .kind == "Staff" and .allowed == false)
  ' "${fixture}" >/dev/null || { echo "identity fixture lacks the typed grant/denial/group matrix" >&2; exit 2; }
  identity_config="$(jq -ec '.services.identity' "${config}")"
  jq -e '
    (.owner | strings | length > 0) and
    (.staff | numbers) >= 1000 and (.customers | numbers) >= 100000 and
    (.serviceAccounts | numbers) >= 100 and .cacheEntries == 256 and
    (.kratosContainers | length) == 2 and
    all([.postgresContainer,.scyllaContainer,.gitContainer] + .kratosContainers | .[];
      type == "string" and test("^gitstore-[a-zA-Z0-9_.-]+$"))
  ' <<<"${identity_config}" >/dev/null || { echo "identity config requires an owned bounded seeded topology" >&2; exit 2; }
  [[ "${CAPACITY_RATE:-500}" =~ ^[0-9]+$ ]] && (( ${CAPACITY_RATE:-500} >= 500 )) || {
    echo "identity acceptance requires at least 500 principal requests/s per API" >&2; exit 2;
  }
  duration="${CAPACITY_DURATION:-30m}"
  [[ "${duration}" =~ ^([0-9]+)(s|m|h)$ ]] || { echo "identity duration must use seconds, minutes or hours" >&2; exit 2; }
  duration_seconds="${BASH_REMATCH[1]}"
  case "${BASH_REMATCH[2]}" in m) duration_seconds=$((duration_seconds * 60)) ;; h) duration_seconds=$((duration_seconds * 3600)) ;; esac
  (( duration_seconds >= 1800 )) || { echo "identity acceptance requires the full thirty-minute duration" >&2; exit 2; }
  owner="$(jq -r '.owner' <<<"${identity_config}")"
  postgres="$(jq -r '.postgresContainer' <<<"${identity_config}")"
  scylla="$(jq -r '.scyllaContainer' <<<"${identity_config}")"
  dependency_containers=()
  while IFS= read -r dependency; do dependency_containers+=("${dependency}"); done \
    < <(jq -r '[.postgresContainer,.scyllaContainer,.gitContainer] + .kratosContainers | .[]' <<<"${identity_config}")
  dependency_inspection="$(docker inspect "${dependency_containers[@]}")"
  jq -e --arg owner "${owner}" '
    length == 5 and ([.[].Id] | unique | length) == 5 and
    all(.[]; .Config.Labels["dev.gitstore.test_owner"] == $owner and .State.Running and
      (.State.Paused | not) and (.State.OOMKilled | not) and .HostConfig.Memory > 0 and .HostConfig.NanoCpus > 0)
  ' <<<"${dependency_inspection}" >/dev/null || { echo "identity dependencies must be distinct live bounded owned containers" >&2; exit 2; }
  counts="$(docker exec "${postgres}" psql -U kratos -d kratos -At -v ON_ERROR_STOP=1 -c \
    "SELECT json_build_object('staff',count(*) FILTER(WHERE schema_id='staff'),'customers',count(*) FILTER(WHERE schema_id='customer'),'serviceAccounts',count(*) FILTER(WHERE schema_id='serviceAccount')) FROM identities;")"
  source_count="$(docker exec "${scylla}" cqlsh -e 'SELECT count(*) FROM gitstore.service_accounts_by_cluster_name;' |
    awk 'NF == 1 && $1 ~ /^[0-9]+$/ {print $1}')"
  jq -e --argjson declared "${identity_config}" --argjson source "${source_count}" '
    .staff == $declared.staff and .customers == $declared.customers and
    .serviceAccounts == $declared.serviceAccounts and .serviceAccounts == $source
  ' <<<"${counts}" >/dev/null || { echo "native directory/source counts do not match the declared identity envelope" >&2; exit 2; }
  fault_windows='[]'
  if [[ "${CAPACITY_IDENTITY_CHAOS:-0}" == 1 ]]; then
    fault_windows="$(jq -ec '
      .faultWindows | select(length == 2 and
        all(.[];
          (.replicas | length > 0) and all(.replicas[]; . == 0 or . == 1) and
          (.startMillis | numbers) > 0 and
          (.endMillis - .startMillis) > 0 and (.endMillis - .startMillis) <= 30000) and
        .[0].endMillis < .[1].startMillis)
    ' "${fixture}")" || { echo "identity chaos requires two disjoint bounded fault windows" >&2; exit 2; }
  fi
  if [[ "${CAPACITY_IDENTITY_ENVELOPE:-alpha}" == production ]]; then
    jq -e '.customers >= 1000000 and .serviceAccounts >= 1000' <<<"${counts}" >/dev/null || {
      echo "production identity envelope requires one million customers and one thousand ServiceAccounts" >&2; exit 2;
    }
  fi
  jq -n --argjson counts "${counts}" --argjson duration "${duration_seconds}" \
    --argjson rate "${CAPACITY_RATE:-500}" --argjson dependencies "${dependency_inspection}" \
    --argjson fault_windows "${fault_windows}" \
    '{schemaVersion:1,passed:true,counts:$counts,durationSeconds:$duration,ratePerReplica:$rate,
      faultWindows:$fault_windows,dependencies:[$dependencies[] | {id:.Id,name:.Name,image:.Image,startedAt:.State.StartedAt,
        memoryBytes:.HostConfig.Memory,nanoCPUs:.HostConfig.NanoCpus}]}' >"${evidence_dir}/identity-preflight.json"
fi

jq -n \
  --arg mode "${mode}" --arg base_url "${base_url}" --arg api_build "${config_build}" \
  --arg readiness_path "${readiness_path}" \
  --argjson api_replicas "${config_replicas}" --argjson runtime_memory_bytes "${environment_memory}" \
  --argjson live_replicas "${identity_json}" --argjson api_containers "${container_json}" \
  --slurpfile config "${config}" --slurpfile environment "${environment}" \
  '{schemaVersion:1,mode:$mode,passed:true,baseURL:$base_url,readinessPath:$readiness_path,topology:{apiReplicas:$api_replicas,liveReplicas:$live_replicas},resources:{runtimeMemoryBytes:$runtime_memory_bytes},builds:{api:$api_build},artifacts:{apiContainers:$api_containers},config:$config[0],environment:$environment[0]}' \
  >"${output}"
