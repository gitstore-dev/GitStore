#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dispatcher="${repo_root}/scripts/run-capacity-target.sh"
source "${repo_root}/scripts/test-fixtures/shell.sh"
source "${repo_root}/scripts/test-fixtures/capacity.sh"
shell_test_setup capacity-dispatch
export CAPACITY_TEST_FIXTURES="${repo_root}/scripts/test-fixtures"

assert_dispatch() {
  local target="$1" profile="$2" expected="$3"
  local output
  output="$(CAPACITY_DRY_RUN=1 "${dispatcher}" "${target}" "${profile}" alpha)"
  [[ "${output}" == *"target=${target} profile=${profile} mode=alpha"* ]]
  [[ "${output}" == *"MODE=alpha"* ]]
  [[ "${output}" == *"${expected}"* ]]
}

assert_dispatch api readiness '_capacity-k6'
translation_output="$(CAPACITY_TRANSLATION_READINESS=1 CAPACITY_DRY_RUN=1 "${dispatcher}" api readiness alpha)"
[[ "${translation_output}" == *"CAPACITY_TRANSLATION_READINESS=1"* ]]
for selection in 'api endpoint-split' 'namespace validation' 'repository lifecycle'; do
  read -r selected_target selected_profile <<<"${selection}"
  assert_rejected "Translation mode accepted an unrelated profile" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_DRY_RUN=1 "${dispatcher}" "${selected_target}" "${selected_profile}" alpha
done
assert_rejected "Translation and identity modes were combined" "${test_dir}/output" \
  env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_IDENTITY_READINESS=1 CAPACITY_DRY_RUN=1 "${dispatcher}" api readiness alpha
assert_rejected "invalid Translation mode was accepted" "${test_dir}/output" \
  env CAPACITY_TRANSLATION_READINESS=invalid CAPACITY_DRY_RUN=1 "${dispatcher}" api readiness alpha
assert_rejected "Translation pushes silently used health-only traffic" "${test_dir}/output" \
  env CAPACITY_TRANSLATION_PUSHES=1 CAPACITY_DRY_RUN=1 "${dispatcher}" api readiness diagnostic
assert_rejected "invalid Translation push selector was accepted" "${test_dir}/output" \
  env CAPACITY_TRANSLATION_PUSHES=invalid CAPACITY_DRY_RUN=1 "${dispatcher}" api readiness diagnostic
if command -v node >/dev/null 2>&1; then
  node - "${repo_root}/tests/capacity/profiles/api-readiness.js" <<'JS'
const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const source = fs.readFileSync(process.argv[2], 'utf8')
  .replace(/^import .*;\n/gm, '').replace('export const options', 'const options')
  .replace(/^export function /gm, 'function ')
  .replace('export default function ()', 'function runIteration()');
const counters = {};
const expected = { translation: { kind: 'Translation', body: '\r\nExact body\n',
  status: { observedGeneration: 1, lastAppliedRevision: 'main@sha1:accepted' } } };
const values = {
  CAPACITY_TRANSLATION_READINESS: '1', CAPACITY_API_ENDPOINTS: 'http://api-a,http://api-b',
  CAPACITY_BASE_URL: 'http://api-a', CAPACITY_RUN_ID: 'fixture', CAPACITY_TOKEN: 'fixture',
  CAPACITY_TRANSLATION_FIXTURE_FILE: '/private-fixture',
};
let response = { data: expected };
let iteration = 0;
const context = vm.createContext({
  __ENV: values, env: name => values[name],
  integer: (name, fallback) => Number(values[name] || fallback),
  open: () => JSON.stringify({ runId: 'fixture', cases: Array.from({ length: 10 }, () =>
    ({ query: 'query{translation{kind body}}', variables: {}, expected })) }),
  SharedArray: function (_, load) { return load(); },
  Counter: function (name) { counters[name] = []; this.add = (count, tags) => counters[name].push({ count, tags }); },
  execution: { scenario: { get iterationInTest() { return iteration; } } },
  http: {
    get() { throw new Error('Translation mode fell back to health checks'); },
    post(url, body, options) {
      assert.equal(url, `http://api-${iteration % 2 ? 'b' : 'a'}/admin/graphql`);
      assert.equal(JSON.parse(body).query, 'query{translation{kind body}}');
      assert.equal(options.headers.Authorization, 'Bearer fixture');
      return { status: 200, json: () => response };
    },
  },
  check: (_, checks) => Object.values(checks).every(check => check()),
});
vm.runInContext(source + '\nthis.run = runIteration; this.options = options;', context);
assert.equal(context.options.scenarios.readiness.rate, 20);
assert.equal(context.options.scenarios.readiness.duration, '30m');
for (iteration = 0; iteration < 2; iteration++) context.run();
for (response of [
  { data: { translation: { ...expected.translation, body: '\nExact body\n' } } },
  { data: expected, errors: [{ message: 'unavailable' }] },
  { data: { translation: { ...expected.translation, status: { observedGeneration: 0 } } } },
  {},
]) { context.run(); iteration++; }
iteration = 36000;
context.run();
assert.equal(counters.translation_requests.length, 6);
assert.equal(counters.translation_correct.length, 2);
assert.equal(counters.translation_boundary_iterations.at(-1).count, 1);
assert.equal(counters.translation_correct[0].tags.replica, '0');
assert.equal(counters.translation_correct[1].tags.replica, '1');
values.CAPACITY_TRANSLATION_PUSHES = '1';
values.CAPACITY_TRANSLATION_START_FILE = '/private-start';
values.CAPACITY_DURATION = '10s';
const warmupStart = 1700000000000;
const measuredStart = warmupStart + 30000;
let now = warmupStart;
const sleeps = [];
const prepared = vm.createContext({
  ...context,
  Date: class extends Date { static now() { return now; } },
  sleep: seconds => { sleeps.push(seconds); now += seconds * 1000; },
  execution: {
    scenario: { get iterationInTest() { return iteration; } },
    vu: { metrics: { tags: {} } },
  },
  open: path => path === '/private-start' ?
    JSON.stringify({ runId: 'fixture', startAt: new Date(measuredStart).toISOString(),
      warmupStartAt: new Date(warmupStart).toISOString() }) :
    JSON.stringify({ runId: 'fixture', cases: Array.from({ length: 10 }, () =>
      ({ query: 'query{translation{kind body}}', variables: {}, expected })) }),
});
response = { data: expected };
vm.runInContext(source + '\nthis.run = runIteration; this.options = options; this.setup = setup; this.teardown = typeof teardown === "function" ? teardown : null;', prepared);
iteration = 0;
prepared.run();
assert.equal(counters.translation_requests.length, 0, 'warmup cannot inflate measured reads');
assert.equal(counters.translation_preparation_requests.length, 1);
assert.equal(counters.translation_preparation_requests[0].tags.phase, 'warmup');
iteration = 600;
now = measuredStart;
prepared.run();
assert.equal(counters.translation_requests.length, 1);
assert.equal(counters.translation_correct.length, 1);
assert.equal(counters.translation_requests[0].tags.phase, 'measured');
iteration = 800;
now = measuredStart + 10000;
prepared.run();
assert.equal(counters.translation_requests.length, 1, 'cooldown cannot extend the measured window');
assert.equal(counters.translation_preparation_requests.at(-1).tags.phase, 'cooldown');
iteration = 1100;
now = measuredStart + 25000;
prepared.run();
assert.equal(counters.translation_preparation_requests.length, 2);
assert.equal(counters.translation_boundary_iterations.at(-1).count, 1);
now = warmupStart - 10000;
prepared.setup();
assert.equal(now, warmupStart - 2000, 'setup must leave time for VU initialization before the first absolute offer');
const scenarioStart = now + 200;
for (const name of Object.keys(counters)) counters[name] = [];
for (iteration = 0; iteration < 1100; iteration++) {
  now = scenarioStart + iteration * 50;
  prepared.run();
}
assert.equal(counters.translation_preparation_requests.length, 900);
assert.equal(counters.translation_preparation_correct.length, 900,
  'executor initialization cannot shift the last warmup/cooldown reads into another phase');
assert.equal(counters.translation_requests.length, 200);
assert.equal(counters.translation_correct.length, 200,
  'all measured reads must start inside their absolute offered window');
assert(sleeps.every(seconds => seconds >= 0 && seconds <= 8));
prepared.teardown();
assert.equal(now, measuredStart + 25000, 'command completion must bracket the entire cooldown, not only its last offer');
iteration = 599;
now = measuredStart + 1;
prepared.run();
assert.equal(counters.translation_failures.at(-1).tags.reason, 'schedule');
iteration = 800;
now = measuredStart + 25001;
prepared.run();
assert.equal(counters.translation_failures.at(-1).tags.reason, 'schedule',
  'cooldown reads after its absolute end cannot silently pass');
iteration = 600;
now = measuredStart;
response = { data: { translation: { ...expected.translation, status: { observedGeneration: 0 } } } };
prepared.run();
assert.equal(counters.translation_failures.at(-1).tags.reason, 'status');
response = { data: { translation: { ...expected.translation, body: '\nExact body\n' } } };
prepared.run();
assert.equal(counters.translation_failures.at(-1).tags.reason, 'body');
console.log('Translation readiness exact-source profile contracts passed');
JS
fi
mkdir -p "${test_dir}/translation-readiness"
python3 - "${test_dir}/translation-readiness" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
cases = []
for index in range(10):
    namespace = f"translation-fixture-{index}"
    cases.append({
        "query": "query($by:TranslationBy!){translation(by:$by){kind}}",
        "variables": {"by": {"namespacePath": {"namespace": namespace, "name": "source"}}},
        "expected": {"translation": {"kind": "Translation", "metadata": {
            "namespace": namespace, "name": "source", "uid": f"uid-{index}",
            "resourceVersion": "1", "generation": 1, "revision": "main@sha1:accepted",
        }, "status": {"observedGeneration": 1, "lastAppliedRevision": "main@sha1:accepted",
                     "conditions": [{"type": "Ready", "status": "TRUE", "observedGeneration": 1}]}}},
    })
documents = {
    "fixture.json": {"runId": "fixture", "cases": cases},
    "metadata.json": {"mode": "diagnostic", "runId": "fixture", "sourceStateSha256": "a" * 64,
                      "gitRevision": "a" * 40,
                      "loadStartedAt": "2026-01-01T00:00:00Z", "loadCompletedAt": "2026-01-01T00:00:11Z"},
    "summary.json": {"metrics": {
        "checks": {"values": {"passes": 200, "fails": 0}},
        "http_reqs": {"values": {"count": 200}},
        "http_req_failed": {"values": {"rate": 0}},
        "iterations": {"values": {"count": 200}},
        "translation_boundary_iterations": {"values": {"count": 0}},
        "http_req_duration": {"values": {"p(95)": 10, "p(99)": 20}},
        "translation_requests": {"values": {"count": 200}},
        "translation_correct": {"values": {"count": 200}},
        **{f"{name}{{replica:{index}}}": {"values": {"count": 100}}
           for name in ("translation_requests", "translation_correct") for index in range(2)},
    }},
}
for name, document in documents.items():
    path = root / name
    path.write_text(json.dumps(document))
    path.chmod(0o600)
PY
translation_preflight() {
  MODE="${1:-diagnostic}" CAPACITY_TRANSLATION_READINESS=1 TRANSLATION_TEST_OWNED_DEPLOYMENT=1 \
    CAPACITY_TOKEN=fixture CAPACITY_RUN_ID="${2:-fixture}" CAPACITY_RATE=20 CAPACITY_DURATION="${3:-10s}" \
    CAPACITY_TRANSLATION_PUSHES="${4:-0}" \
    CAPACITY_API_ENDPOINTS=http://api-a,http://api-b \
    CAPACITY_TRANSLATION_FIXTURE_FILE="${test_dir}/translation-readiness/fixture.json" \
    "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/translation-readiness"
}
translation_preflight
translation_preflight diagnostic fixture 2h
assert_rejected "connected Translation window exhausted its setup/recovery deadline" "${test_dir}/output" \
  translation_preflight diagnostic fixture 2h 1
translation_preflight diagnostic fixture 90m 1
translation_preflight
for rejected_mode in alpha production; do
  assert_rejected "read-only Translation diagnostics certified ${rejected_mode}" "${test_dir}/output" \
    translation_preflight "${rejected_mode}"
done
assert_rejected "Translation preflight accepted stale source cases" "${test_dir}/output" translation_preflight diagnostic stale
chmod 644 "${test_dir}/translation-readiness/fixture.json"
assert_rejected "Translation preflight accepted an unprotected source fixture" "${test_dir}/output" translation_preflight
chmod 600 "${test_dir}/translation-readiness/fixture.json"
mkdir -p "${test_dir}/translation-topology"
cp "${test_dir}/translation-readiness/"{fixture,metadata}.json "${test_dir}/translation-topology/"
python3 - "${test_dir}/translation-topology" "${repo_root}" <<'PY'
import hashlib
import json
import pathlib
import subprocess
import sys

root, repository = pathlib.Path(sys.argv[1]), sys.argv[2]
revision = subprocess.check_output(["git", "-C", repository, "rev-parse", "HEAD"], text=True).strip()
roles = ["api", "api", "controller", "controller", "gitService", "scylla", "scylla", "scylla"]
names = ["api-1", "api-2", "controller-1", "controller-2", "git-1", "scylla-1", "scylla-2", "scylla-3"]
limits = {role: {"nanoCPUs": 1000000000, "memoryBytes": 1073741824} for role in set(roles)}
containers = []
for index, (role, name) in enumerate(zip(roles, names)):
    digest = f"{index + 1:064x}"
    mounts = []
    if role in ("controller", "gitService"):
        destination = "/var/lib/gitstore/" + ("checkpoints" if role == "controller" else "repos")
        mounts = [{"Destination": destination, "Source": f"/private/{name}", "Type": "volume",
                   "Name": f"retained-{name}", "RW": True}]
    containers.append({
        "Id": f"{index + 100:064x}", "Name": "/" + name, "Image": "sha256:" + digest,
        "Config": {"Image": "sha256:" + digest, "User": "0", "Labels": {
            "org.opencontainers.image.revision": revision, "com.docker.compose.project": "owned-translation",
            "com.docker.compose.service": role}},
        "State": {"Running": True, "Paused": False, "OOMKilled": False, "StartedAt": "2026-01-01T00:00:00Z"},
        "HostConfig": {**limits[role], "Memory": limits[role]["memoryBytes"],
                       "NanoCpus": limits[role]["nanoCPUs"], "NetworkMode": "owned-network", "PidMode": ""},
        "NetworkSettings": {"SandboxID": name + "-sandbox"}, "Mounts": mounts,
        "Args": ["--smp", "1"] if role == "scylla" else [],
    })
config = {"schemaVersion": 1, "services": {
    "api": {"build": "release", "replicas": 2}, "controller": {"build": "release", "replicas": 2},
    "gitService": {"build": "release"}, "scylla": {"nodes": 3, "smpPerNode": 1},
    "translationTopology": {"apiPorts": [4000, 4000], "controllerPorts": [5001, 5001],
        "checkpointDestination": "/var/lib/gitstore/checkpoints",
        "repositoryDestination": "/var/lib/gitstore/repos", "limits": limits},
}}
environment = {"schemaVersion": 1, "runtime": "docker", "architecture": "arm64",
    "host": {"memoryBytes": 17179869184, "logicalCPUs": 8},
    "topology": {"apiReplicas": 2, "controllerReplicas": 2, "gitServiceReplicas": 1,
                 "scyllaNodes": 3, "scyllaSmpPerNode": 1}}
for name, document in (("config.json", config), ("environment.json", environment), ("containers.json", containers)):
    (root / name).write_text(json.dumps(document))
metadata = json.loads((root / "metadata.json").read_text())
metadata.update(gitRevision=revision, gitDirty=False)
for name in ("config", "environment"):
    metadata[name + "Digest"] = hashlib.sha256((root / (name + ".json")).read_bytes()).hexdigest()
(root / "metadata.json").write_text(json.dumps(metadata))
PY
write_mock "${test_dir}/translation-topology-bin/docker" <<'MOCK'
#!/usr/bin/env bash
exec python3 - "$@" <<'PY'
import json
import os
import sys

args = sys.argv[1:]
containers = json.load(open(os.environ["MOCK_TRANSLATION_CONTAINERS"]))
if args[0] == "inspect":
    selected = [item for item in containers if item["Name"].lstrip("/") in args[1:] or item["Id"] in args[1:]]
    if "--format" in args:
        assert args[args.index("--format") + 1] == '{"id":{{json .Id}},"mounts":{{json .Mounts}}}'
        for item in selected:
            print(json.dumps({"id": item["Id"], "mounts": item["Mounts"]}))
    else:
        print(json.dumps(selected))
elif args[0] == "info":
    print(json.dumps({"MemTotal": 17179869184 if not os.environ.get("MOCK_TRANSLATION_HOST_MISMATCH") else 1,
                     "NCPU": 8, "Architecture": "aarch64", "OSType": "linux"}))
elif args[:2] == ["image", "inspect"]:
    labels = dict(containers[0]["Config"]["Labels"])
    if os.environ.get("MOCK_TRANSLATION_IMAGE_REVISION"):
        labels["org.opencontainers.image.revision"] = "b" * 40
    print(json.dumps([{"Id": args[2], "Os": "linux", "Architecture": "arm64", "Config": {"Labels": labels}}]))
elif args[0] == "ps":
    if any(value.startswith("volume=") for value in args):
        identifiers = [item["Id"] for item in containers if item["Name"] == "/git-1" and
                       item["Mounts"][0]["Type"] == "volume"]
    else:
        identifiers = [item["Id"] for item in containers if
                       not any(value.startswith("label=com.docker.compose.project=") for value in args) or
                       item["Config"]["Labels"]["com.docker.compose.project"] == "owned-translation"]
    print("\n".join(identifier if "--no-trunc" in args else identifier[:12] for identifier in identifiers))
elif args[0] == "run":
    required = ["--rm", "--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt", "--pids-limit=64"]
    assert all(value in args for value in required), "unsafe process probe"
    assert ("--cap-add=SYS_PTRACE" in args) == (args[-1] == "process-executable"), \
        "only PID executable inspection may receive the ptrace-read capability"
    target = args[args.index("--pid") + 1]
    assert target == args[args.index("--network") + 1], "process/network namespaces differ"
    name = next(item["Name"].lstrip("/") for item in containers if item["Id"] == target.removeprefix("container:"))
    role = "api" if name.startswith("api-") else "controller"
    if args[-1] == "process-executable":
        print("/app/" + ("api" if role == "api" else "controller-manager" if name.startswith("controller-") else "git-service"))
    else:
        assert "--url" in args and args[args.index("--url") + 1].startswith("http://127.0.0.1:")
        if name.startswith("controller-") and args[args.index("--url") + 1].endswith("/health"):
            print(json.dumps({"ready": True, "kinds": {"Translation": {"recovering": False, "stalled": False, "poisonItems": 0}}}))
        else:
            identity = name if os.environ.get("MOCK_TRANSLATION_ALIAS") != name else "api-1"
            print(f'process_start_time_seconds 100\ngitstore_{role}_process_instance_info{{instance_id="{identity}"}} 1')
elif args[0] == "exec" and args[-2:] == ["nodetool", "status"]:
    print("UN 10.0.0.1\nUN 10.0.0.2\nUN 10.0.0.3")
else:
    sys.exit(1)
PY
MOCK
write_mock "${test_dir}/translation-topology-bin/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
endpoint="${*: -1}"
case "${endpoint}" in
  */health|*/ready/admin) printf '{}\n' ;;
  *)
    case "${endpoint}" in *api-a*) role=api; identity=api-1 ;; *api-b*) role=api; identity=api-2 ;;
      *controller-a*) role=controller; identity=controller-1 ;; *controller-b*) role=controller; identity=controller-2 ;; *) exit 1 ;; esac
    printf 'process_start_time_seconds 100\ngitstore_%s_process_instance_info{instance_id="%s"} 1\n' "${role}" "${identity}" ;;
esac
MOCK
translation_topology_preflight() {
  env PATH="${test_dir}/translation-topology-bin:${PATH}" MODE=diagnostic \
    CAPACITY_TRANSLATION_READINESS=1 TRANSLATION_TEST_OWNED_DEPLOYMENT=1 CAPACITY_TOKEN=fixture \
    CAPACITY_RUN_ID=fixture CAPACITY_RATE=20 CAPACITY_DURATION=10s CAPACITY_OWNED_PROJECT=owned-translation \
    CAPACITY_CONFIG_MANIFEST="${test_dir}/translation-topology/config.json" \
    CAPACITY_ENVIRONMENT_MANIFEST="${test_dir}/translation-topology/environment.json" \
    CAPACITY_API_ENDPOINTS=http://api-a,http://api-b CAPACITY_API_CONTAINERS=api-1,api-2 \
    CAPACITY_CONTROLLER_A=http://controller-a CAPACITY_CONTROLLER_B=http://controller-b \
    CAPACITY_CONTROLLER_CONTAINERS=controller-1,controller-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
    CAPACITY_SCYLLA_CONTAINERS=scylla-1,scylla-2,scylla-3 \
    CAPACITY_TRANSLATION_FIXTURE_FILE="${test_dir}/translation-topology/fixture.json" \
    MOCK_TRANSLATION_CONTAINERS="${test_dir}/translation-topology/containers.json" \
    "$@" "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/translation-topology"
}
translation_topology_preflight
jq -e '.liveTopologyVerified and (.processes | length == 5) and (.passed == false)' \
  "${test_dir}/translation-topology/translation-topology.json" >/dev/null
python3 - "${test_dir}/translation-topology" <<'PY'
import copy
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
original = json.loads((root / "containers.json").read_text())
def changed(name, update):
    containers = copy.deepcopy(original)
    update(containers)
    (root / (name + ".json")).write_text(json.dumps(containers))
changed("unowned", lambda containers: containers[2]["Config"]["Labels"].update({"com.docker.compose.project": "unrelated"}))
changed("unbounded", lambda containers: containers[0]["HostConfig"].update(Memory=0))
changed("shared-network", lambda containers: containers[2]["NetworkSettings"].update(SandboxID=containers[0]["NetworkSettings"]["SandboxID"]))
changed("host-pid", lambda containers: containers[2]["HostConfig"].update(PidMode="host"))
changed("shared-checkpoint", lambda containers: containers[3]["Mounts"][0].update(Source="/private/controller-1/child"))
changed("shared-volume", lambda containers: containers[3]["Mounts"][0].update(Name="retained-controller-1"))
changed("mutable-image", lambda containers: containers[4]["Config"].update(Image="gitstore/git-service:latest"))
changed("overridden-binary", lambda containers: containers[2]["Mounts"].append({"Destination": "/app", "Source": "/private/code", "RW": False}))
changed("wrong-smp", lambda containers: containers[5].update(Args=["--smp", "2"]))
changed("extra-git", lambda containers: containers.append({**containers[4], "Id": "extra-git-id", "Name": "/extra-git"}))
changed("bind-git", lambda containers: containers[4]["Mounts"][0].update(Type="bind", Name=""))
changed("external-git-consumer", lambda containers: containers.append({
    "Id": "outside-project", "Name": "/unrelated",
    "Config": {"Labels": {"com.docker.compose.project": "unrelated"}},
    "Mounts": [{"Type": "bind", "Source": "/private/git-1/child", "Destination": "/shared", "RW": True}],
}))
changed("unrelated-tmpfs", lambda containers: containers[0]["Mounts"].append({
    "Type": "tmpfs", "Source": "", "Destination": "/tmp", "RW": True,
}))
PY
translation_topology_preflight MOCK_TRANSLATION_CONTAINERS="${test_dir}/translation-topology/bind-git.json"
translation_topology_preflight MOCK_TRANSLATION_CONTAINERS="${test_dir}/translation-topology/unrelated-tmpfs.json"
for rejection in unowned unbounded shared-network host-pid shared-checkpoint shared-volume mutable-image overridden-binary wrong-smp extra-git external-git-consumer; do
  assert_rejected "Translation topology accepted ${rejection}" "${test_dir}/output" \
    translation_topology_preflight MOCK_TRANSLATION_CONTAINERS="${test_dir}/translation-topology/${rejection}.json"
  [[ ! -e "${test_dir}/translation-topology/translation-topology.json" ]]
done
assert_rejected "Translation topology accepted a process endpoint alias" "${test_dir}/output" \
  translation_topology_preflight MOCK_TRANSLATION_ALIAS=controller-1
assert_rejected "Translation topology trusted a container label instead of its actual image source" "${test_dir}/output" \
  translation_topology_preflight MOCK_TRANSLATION_IMAGE_REVISION=1
assert_rejected "Translation topology trusted declared host memory instead of the actual Docker runtime" "${test_dir}/output" \
  translation_topology_preflight MOCK_TRANSLATION_HOST_MISMATCH=1
translation_topology_preflight
cp "${test_dir}/translation-readiness/summary.json" "${test_dir}/translation-topology/summary.json"
CAPACITY_TRANSLATION_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-topology"
jq -e '.liveTopologyJoined and .domainChecksPassed and (.passed == false)' \
  "${test_dir}/translation-topology/translation-verifier.json" >/dev/null
cp "${test_dir}/translation-topology/translation-topology.json" "${test_dir}/translation-topology/valid-topology.json"
for rejection in source replica scylla-alias scylla-member-disagreement scylla-missing-epoch scylla-future-epoch; do
  case "${rejection}" in
    source) jq '.sourceStateSha256 = ("b" * 64)' "${test_dir}/translation-topology/valid-topology.json" ;;
    replica) jq '.processes[1].endpoint = "http://unrelated"' "${test_dir}/translation-topology/valid-topology.json" ;;
    scylla-alias) jq '.scylla[1].containerID = .scylla[0].containerID' "${test_dir}/translation-topology/valid-topology.json" ;;
    scylla-member-disagreement) jq '.scylla[1].members[0] = "10.0.0.9"' "${test_dir}/translation-topology/valid-topology.json" ;;
    scylla-missing-epoch) jq 'del(.scylla[0].startedAt)' "${test_dir}/translation-topology/valid-topology.json" ;;
    scylla-future-epoch) jq '.scylla[0].startedAt = "2099-01-01T00:00:00Z"' "${test_dir}/translation-topology/valid-topology.json" ;;
  esac >"${test_dir}/translation-topology/translation-topology.json"
  assert_rejected "Translation verifier accepted ${rejection} topology mismatches" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-topology"
done
cp "${test_dir}/translation-topology/valid-topology.json" "${test_dir}/translation-topology/translation-topology.json"
python3 - "${test_dir}/translation-topology" <<'PY'
import hashlib
import json
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])
metadata = json.loads((root / "metadata.json").read_text())
directory = root / "prometheus"
directory.mkdir()
start = 1767225600
manifest = {key: metadata[key] for key in ("runId", "sourceStateSha256", "configDigest", "environmentDigest")}
manifest.update(capacityTarget="api/readiness", source="http://prometheus", translationStepSeconds=5,
                controllerStepSeconds=5, translationTargets={}, translationResourceDigests={},
                translationScrapeTimeDigests={}, translationScrapeTimeQueries={}, translationRange={"startUnixSeconds": start - 5,
                "endUnixSeconds": start + 15, "stepSeconds": 5})
for role in ("api", "controller"):
    targets = [role + "-a:4000", role + "-b:4000"]
    manifest["translationTargets"][role] = targets
    matcher = "instance=~" + json.dumps("|".join(re.escape(target) for target in targets))
    manifest["translationTargets"][role + "Matcher"] = matcher
    metric_selector = ("up|target_info|gitstore_api_process_instance_info|gitstore_translation_.*" if role == "api"
                       else "up|target_info|gitstore_controller_.*|gitstore_translation_.*")
    query = ('{job="gitstore-' + role + '-capacity",' + matcher + ',__name__=~"' + metric_selector +
             '|process_start_time_seconds|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines|go_gc_heap_live_bytes|go_gc_cycles_total"}')
    manifest["translationQuery" if role == "api" else "controllerQuery"] = query
    manifest["translationScrapeTimeQueries"][role] = (
        'timestamp(label_replace(' + query + ', "source_metric", "$1", "__name__", "(.+)"))')
    matrix, times = [], []
    for index, target in enumerate(targets):
        values = {"up": [1, 1, 1], "process_start_time_seconds": [100, 100, 100], "process_cpu_seconds_total": [10, 12, 14],
                  "process_resident_memory_bytes": [200000000] * 3, "go_goroutines": [100, 101, 102],
                  "go_gc_heap_live_bytes": [10000000, 10200000, 10400000], "go_gc_cycles_total": [10, 11, 12],
                  f"gitstore_{role}_process_instance_info": [1, 1, 1]}
        for name, samples in values.items():
            labels = {"instance": target}
            if name.endswith("_process_instance_info"):
                labels["instance_id"] = role + "-" + str(index + 1)
            matrix.append({"metric": {**labels, "__name__": name},
                           "values": [[start + offset * 5, str(value)] for offset, value in enumerate(samples)]})
            times.append({"metric": {**labels, "source_metric": name},
                          "values": [[start + offset * 5, str(start + offset * 5)] for offset in range(3)]})
    for filename, key, content in (
        ("translation-api-series.json" if role == "api" else "controller-series.json", "translationResourceDigests", matrix),
        ("translation-" + role + "-scrape-times.json", "translationScrapeTimeDigests", times),
    ):
        path = directory / filename
        path.write_text(json.dumps({"status": "success", "data": {"resultType": "matrix", "result": content}}))
        manifest[key][role] = hashlib.sha256(path.read_bytes()).hexdigest()
(directory / "manifest.json").write_text(json.dumps(manifest))
PY
translation_full_resources() {
  CAPACITY_TRANSLATION_READINESS=1 CAPACITY_PROMETHEUS_URL=http://prometheus \
    CAPACITY_PROMETHEUS_TARGETS=api-a:4000,api-b:4000 \
    CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=controller-a:4000,controller-b:4000 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "$1"
}
translation_full_resources "${test_dir}/translation-topology"
jq -e '.resourceGatesVerified and .domainChecksPassed and (.passed == false)' \
  "${test_dir}/translation-topology/translation-verifier.json" >/dev/null
python3 - "${test_dir}/translation-topology" <<'PY'
import hashlib
import json
import pathlib
import shutil
import sys

original = pathlib.Path(sys.argv[1])
for failure in ("lookback-gap", "cpu-reset", "cpu-limit", "memory-limit", "heap-growth", "goroutine-drift", "heap-without-gc", "missing-gc", "raw-query", "scrape-query",
                "missing-native-start", "epoch-change", "mismatched-start"):
    root = original.parent / ("translation-resource-" + failure)
    shutil.copytree(original, root)
    manifest = json.loads((root / "prometheus/manifest.json").read_text())
    if failure == "raw-query":
        manifest["translationQuery"] = '{job="unrelated"}'
    if failure == "scrape-query":
        manifest["translationScrapeTimeQueries"]["api"] = "timestamp(" + manifest["translationQuery"] + ")"
    for filename, digest_key in (("translation-api-series.json", "translationResourceDigests"),
                                 ("translation-api-scrape-times.json", "translationScrapeTimeDigests")):
        path = root / "prometheus" / filename
        matrix = json.loads(path.read_text())
        for series in matrix["data"]["result"]:
            metric = series["metric"].get("__name__", series["metric"].get("source_metric"))
            if series["metric"]["instance"] != "api-a:4000":
                continue
            if filename.endswith("scrape-times.json"):
                if failure == "lookback-gap":
                    series["values"][1][1] = series["values"][0][1]
                continue
            values = series["values"]
            if failure == "lookback-gap":
                values[1][1] = values[0][1]
                if metric == "process_cpu_seconds_total":
                    values[2][1] = "13"
            if failure == "cpu-reset" and metric == "process_cpu_seconds_total":
                values[2][1] = "0"
            if failure == "cpu-limit" and metric == "process_cpu_seconds_total":
                values[1][1] = "14"
            if failure == "memory-limit" and metric == "process_resident_memory_bytes":
                values[1][1] = "1073741825"
            if failure == "heap-growth" and metric == "go_gc_heap_live_bytes":
                values[2][1] = "11000000"
            if failure == "goroutine-drift" and metric == "go_goroutines":
                values[2][1] = "106"
            if failure == "heap-without-gc" and metric == "go_gc_cycles_total":
                values[1][1] = "10"
            if failure == "epoch-change" and metric == "process_start_time_seconds":
                values[2][1] = "101"
            if failure == "mismatched-start" and metric == "process_start_time_seconds":
                for point in values:
                    point[1] = "101"
        if failure == "missing-gc":
            matrix["data"]["result"] = [series for series in matrix["data"]["result"]
                if series["metric"].get("__name__", series["metric"].get("source_metric")) != "go_gc_heap_live_bytes"]
        if failure == "missing-native-start":
            matrix["data"]["result"] = [series for series in matrix["data"]["result"]
                if series["metric"].get("__name__", series["metric"].get("source_metric")) != "process_start_time_seconds"]
        path.write_text(json.dumps(matrix))
        manifest[digest_key]["api"] = hashlib.sha256(path.read_bytes()).hexdigest()
    (root / "prometheus/manifest.json").write_text(json.dumps(manifest))
PY
for rejection in lookback-gap cpu-reset cpu-limit memory-limit heap-growth goroutine-drift heap-without-gc missing-gc raw-query scrape-query \
  missing-native-start epoch-change mismatched-start; do
  assert_rejected "Translation resources accepted ${rejection}" "${test_dir}/output" \
    translation_full_resources "${test_dir}/translation-resource-${rejection}"
done
CAPACITY_TRANSLATION_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-readiness"
jq -e '.passed == false and .domainChecksPassed and .evidenceClass == "diagnostic" and .requests == 200' \
  "${test_dir}/translation-readiness/translation-verifier.json" >/dev/null
python3 - "${test_dir}/translation-readiness" <<'PY'
import hashlib
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
target = root.parent / "translation-resources"
shutil.copytree(root, target)
(target / "prometheus").mkdir()
metadata = json.loads((root / "metadata.json").read_text())
manifest = {key: metadata.get(key) for key in ("runId", "sourceStateSha256", "configDigest", "environmentDigest")}
manifest.update(capacityTarget="api/readiness", source="http://prometheus", translationStepSeconds=5,
                controllerStepSeconds=5, translationTargets={}, translationResourceDigests={})
for role, filename in (("api", "translation-api-series.json"), ("controller", "controller-series.json")):
    targets = [f"{role}-{replica}:4000" for replica in ("a", "b")]
    manifest["translationTargets"][role] = targets
    raw = json.dumps({"status": "success", "data": {"resultType": "matrix", "result": [
        {"metric": {"__name__": f"gitstore_{role}_process_instance_info", "instance": address,
                    "instance_id": f"{role}-{index}"}, "values": [[1, "1"], [6, "1"]]}
        for index, address in enumerate(targets)
    ]}}).encode()
    (target / "prometheus" / filename).write_bytes(raw)
    manifest["translationResourceDigests"][role] = hashlib.sha256(raw).hexdigest()
(target / "prometheus/manifest.json").write_text(json.dumps(manifest))
for failure in ("source", "config", "collector", "target", "raw-digest", "alias", "missing-epoch", "malformed"):
    case = root.parent / f"translation-resources-{failure}"
    shutil.copytree(target, case)
    captured = json.loads(json.dumps(manifest))
    if failure == "source":
        captured["sourceStateSha256"] = "b" * 64
    elif failure == "config":
        captured["configDigest"] = "c" * 64
    elif failure == "collector":
        captured["source"] = "http://unrelated"
    elif failure == "target":
        captured["translationTargets"]["api"][1] = "unrelated:4000"
    else:
        path = case / "prometheus/controller-series.json"
        data = json.loads(path.read_text())
        if failure == "alias":
            data["data"]["result"][1]["metric"]["instance_id"] = "api-0"
        elif failure == "missing-epoch":
            del data["data"]["result"][1]["metric"]["instance_id"]
        elif failure == "malformed":
            data["data"]["result"][1]["metric"] = None
        path.write_text(json.dumps(data) + "\n")
        if failure != "raw-digest":
            captured["translationResourceDigests"]["controller"] = hashlib.sha256(path.read_bytes()).hexdigest()
    (case / "prometheus/manifest.json").write_text(json.dumps(captured))
PY
translation_resource_verifier() {
  CAPACITY_TRANSLATION_READINESS=1 CAPACITY_PROMETHEUS_URL=http://prometheus \
    CAPACITY_PROMETHEUS_TARGETS=api-a:4000,api-b:4000 \
    CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=controller-a:4000,controller-b:4000 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "$1"
}
translation_resource_verifier "${test_dir}/translation-resources"
jq -e '.resourceCaptureJoined and .domainChecksPassed and .passed == false' \
  "${test_dir}/translation-resources/translation-verifier.json" >/dev/null
for failure in source config collector target raw-digest alias missing-epoch malformed; do
  assert_rejected "Translation read verifier accepted ${failure} resource capture" "${test_dir}/output" \
    translation_resource_verifier "${test_dir}/translation-resources-${failure}"
done
assert_rejected "Translation reads silently ignored a requested resource capture" "${test_dir}/output" \
  translation_resource_verifier "${test_dir}/translation-readiness"
python3 - "${test_dir}/translation-readiness" <<'PY'
import copy
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
original = json.loads((root / "summary.json").read_text())
for failure in ("partial", "dropped", "health-only", "extra-reads", "missing-replica", "tail", "nonfinite", "classified-schedule"):
    target = root.parent / f"translation-{failure}"
    shutil.copytree(root, target)
    summary = copy.deepcopy(original)
    metrics = summary["metrics"]
    if failure == "partial":
        metrics["translation_correct"]["values"]["count"] -= 1
    elif failure == "dropped":
        metrics["dropped_iterations"] = {"values": {"count": 1}}
    elif failure == "health-only":
        del metrics["translation_requests"]
    elif failure == "extra-reads":
        metrics["http_reqs"]["values"]["count"] += 1
    elif failure == "missing-replica":
        metrics["translation_requests{replica:1}"]["values"]["count"] = 0
    elif failure == "classified-schedule":
        metrics["checks"]["values"]["fails"] = 2
        metrics["translation_failures{reason:schedule}"] = {"values": {"count": 2}}
    else:
        metrics["http_req_duration"]["values"]["p(99)"] = float("nan") if failure == "nonfinite" else 3000
    (target / "summary.json").write_text(json.dumps(summary))
PY
for failure in partial dropped health-only extra-reads missing-replica tail nonfinite classified-schedule; do
  assert_rejected "Translation read verifier accepted ${failure} evidence" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-${failure}"
done
jq -e '.domainChecksPassed == false and (.reason | contains("\"schedule\": 2"))' \
  "${test_dir}/translation-classified-schedule/translation-verifier.json" >/dev/null
python3 - "${test_dir}/translation-readiness" <<'PY'
import datetime
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
target = root.parent / "translation-connected"
shutil.copytree(root, target)
preflight = json.loads((root / "translation-preflight.json").read_text())
start = datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc)
at = lambda delta: (start + datetime.timedelta(seconds=delta)).isoformat()
expected = {key: "a" * 64 for key in ("SourceDigest", "BodyDigest", "PatchDigest")}
receipt = {
    **{key: preflight[key] for key in ("runId", "sourceStateSha256", "fixtureSha256")},
    "scope": "connected-translation", "evidenceClass": "diagnostic",
    "sourceRevision": "a" * 40,
    "configDigest": "", "environmentDigest": "", "start": at(0), "end": at(10),
    "intervalNanos": 1000000000, "workers": 2, "offered": 10, "completed": 10, "dropped": 0,
    "verifiedRetainedDocuments": 1009, "originalHeads": {str(index): "a" * 40 for index in range(10)},
    "materialBefore": "a" * 64, "materialAfter": "a" * 64, "baselineProducts": 10,
    "expected": {str(index): expected for index in range(10)}, "pushes": [],
}
for sequence in range(10):
    commit = f"{sequence + 1:040x}"
    receipt["pushes"].append({
        "Sequence": sequence, "Commit": commit, "Documents": 1,
        "OfferedAt": at(sequence), "AcknowledgedAt": at(sequence + .001),
        "Observations": [{
            **expected, "Replica": endpoint, "Ready": True, "Historical": False,
            "UID": f"source-{sequence}", "Version": "1", "WatchVersion": "1", "WatchCursor": "observed",
            "Generation": 1, "ObservedGeneration": 1, "Revision": "main@sha1:" + commit,
            "LastAppliedRevision": "main@sha1:" + commit, "VisibleAt": at(sequence + .002), "ReadyAt": at(sequence + .003),
            "ReadVisibleAt": at(sequence + .002), "ReadReadyAt": at(sequence + .003), "WatchAt": at(sequence + .002),
        } for endpoint in preflight["apiEndpoints"]],
    })
(target / "translation-connected.json").write_text(json.dumps(receipt))
(target / "translation-load-start.json").write_text(json.dumps({"runId": preflight["runId"], "startAt": at(0)}))
connected_failures = ("source", "revision", "missing-revision", "body", "patch", "material", "drop", "barrier", "false-ready", "string-ready",
                      "numeric-history", "boolean-generation", "numeric-identity",
                      "stale-applied", "watch-version", "historical-ready", "duplicate-uid",
                      "duplicate-commit", "duplicate-sequence", "stale-window", "unjoined-fault",
                      "missing-read-clock", "missing-component-clocks", "early-read-clock", "late-watch-clock", "hidden-watch-clock")
for failure in connected_failures:
    invalid = root.parent / f"translation-connected-{failure}"
    shutil.copytree(target, invalid)
    changed = json.loads(json.dumps(receipt))
    if failure == "source":
        changed["pushes"][0]["Observations"][1]["SourceDigest"] = "b" * 64
    elif failure == "revision":
        changed["sourceRevision"] = "b" * 40
    elif failure == "missing-revision":
        del changed["sourceRevision"]
    elif failure in ("body", "patch"):
        changed["pushes"][0]["Observations"][1][failure.title() + "Digest"] = "b" * 64
    elif failure == "material":
        changed["materialAfter"] = "b" * 64
    elif failure == "drop":
        changed["dropped"] = 1
    elif failure == "barrier":
        changed["start"] = at(.001)
    elif failure == "false-ready":
        changed["pushes"][0]["Observations"][1]["Ready"] = False
    elif failure == "stale-applied":
        changed["pushes"][0]["Observations"][1]["LastAppliedRevision"] = "main@sha1:" + "b" * 40
    elif failure == "watch-version":
        changed["pushes"][0]["Observations"][1]["WatchVersion"] = "older"
    elif failure == "historical-ready":
        changed["pushes"][0]["Observations"][1]["Historical"] = True
    elif failure == "duplicate-uid":
        for observation in changed["pushes"][1]["Observations"]:
            observation["UID"] = "source-0"
    elif failure == "duplicate-commit":
        commit = changed["pushes"][0]["Commit"]
        changed["pushes"][1]["Commit"] = commit
        for observation in changed["pushes"][1]["Observations"]:
            observation["Revision"] = observation["LastAppliedRevision"] = "main@sha1:" + commit
    elif failure == "duplicate-sequence":
        changed["pushes"][1] = changed["pushes"][0]
    elif failure == "stale-window":
        def shift(value):
            if isinstance(value, dict):
                return {key: shift(item) for key, item in value.items()}
            if isinstance(value, list):
                return [shift(item) for item in value]
            if isinstance(value, str) and value.startswith("2026-01-01T"):
                return (datetime.datetime.fromisoformat(value) + datetime.timedelta(seconds=100)).isoformat()
            return value
        changed = shift(changed)
        (invalid / "translation-load-start.json").write_text(json.dumps({
            "runId": preflight["runId"], "startAt": changed["start"],
        }))
    elif failure == "unjoined-fault":
        changed["faults"] = [{"kind": "physical", "injected": True, "restored": True}]
    elif failure == "string-ready":
        changed["pushes"][0]["Observations"][0]["Ready"] = "FALSE"
    elif failure == "numeric-history":
        changed["pushes"][0]["Observations"][0]["Historical"] = 0
    elif failure == "boolean-generation":
        changed["pushes"][0]["Observations"][0]["Generation"] = True
    elif failure == "numeric-identity":
        for observation in changed["pushes"][0]["Observations"]:
            observation["UID"] = 123
    elif failure == "missing-read-clock":
        del changed["pushes"][0]["Observations"][0]["ReadVisibleAt"]
    elif failure == "missing-component-clocks":
        for key in ("ReadVisibleAt", "ReadReadyAt", "WatchAt"):
            del changed["pushes"][0]["Observations"][0][key]
    elif failure == "early-read-clock":
        changed["pushes"][0]["Observations"][0]["ReadVisibleAt"] = at(0)
    elif failure == "late-watch-clock":
        changed["pushes"][0]["Observations"][0]["WatchAt"] = at(1)
    elif failure == "hidden-watch-clock":
        changed["pushes"][0]["Observations"][0]["VisibleAt"] = at(.001)
    (invalid / "translation-connected.json").write_text(json.dumps(changed))
PY
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-connected"
jq -e '.domainChecksPassed and .connectedPushes == 10 and .passed == false' \
  "${test_dir}/translation-connected/translation-verifier.json" >/dev/null
for failure in source revision missing-revision body patch material drop barrier false-ready stale-applied watch-version historical-ready \
  duplicate-uid duplicate-commit duplicate-sequence stale-window unjoined-fault \
  string-ready numeric-history boolean-generation numeric-identity \
  missing-read-clock missing-component-clocks early-read-clock late-watch-clock hidden-watch-clock; do
  assert_rejected "connected Translation verifier accepted ${failure} evidence" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-connected-${failure}"
done
translation_journal_regressions() {
python3 - "${test_dir}/translation-callback-fault" "${test_dir}/translation-topology" <<'PY'
import copy
import datetime
import hashlib
import json
import pathlib
import shutil
import sys

original, resources = map(pathlib.Path, sys.argv[1:])
root = original.parent / "translation-journal-fault"
shutil.copytree(original, root)
shutil.copytree(resources / "prometheus", root / "prometheus")
config = json.loads((resources / "config.json").read_text())
config["services"]["translationFaults"] = {"callback-loss": "simulated"}
config["services"]["translationPhysicalFaults"] = {"journal-interruption": "physical"}
raw = json.dumps(config).encode()
(root / "config.json").write_bytes(raw)
digest = hashlib.sha256(raw).hexdigest()
metadata = json.loads((root / "metadata.json").read_text())
resource_metadata = json.loads((resources / "metadata.json").read_text())
metadata.update(configDigest=digest, environmentDigest=resource_metadata["environmentDigest"],
                gitRevision=resource_metadata["gitRevision"])
(root / "metadata.json").write_text(json.dumps(metadata))
topology = json.loads((resources / "translation-topology.json").read_text())
topology["configDigest"] = digest
(root / "translation-topology.json").write_text(json.dumps(topology))
manifest = json.loads((root / "prometheus/manifest.json").read_text())
manifest["configDigest"] = digest
(root / "prometheus/manifest.json").write_text(json.dumps(manifest))
receipt = json.loads((root / "translation-connected.json").read_text())
receipt.update(configDigest=digest, environmentDigest=metadata["environmentDigest"],
               sourceRevision=metadata["gitRevision"])
fault = receipt["faults"][0]
start = datetime.datetime.fromisoformat(receipt["start"])
millis = int(start.timestamp() * 1000)
fault["restored"] = (start + datetime.timedelta(milliseconds=2)).isoformat()
fault["delivery"]["at"] = (start + datetime.timedelta(microseconds=2500)).isoformat()
source = topology["scylla"][0]
target = {"Id": source["containerID"], "Name": "/" + source["container"], "Image": source["imageID"],
          "State": {"Running": True, "Paused": False, "OOMKilled": False, "StartedAt": source["startedAt"]}}
physical = {
    "schemaVersion": 1, "runId": receipt["runId"], "domain": "translation", "kind": "physical",
    "profile": "journal-interruption", "action": "pause", "target": source["container"],
    "targetId": source["containerID"], "ownership": {"label": "com.docker.compose.project", "owner": topology["owner"]},
    "injectorExitCode": 0, "exitCode": 0, "injected": True, "faultObserved": True,
    "restorationRequired": True, "restored": True, "domainRecoveryVerified": False,
    "sourceScope": "shared-journal-source",
    "faultWindow": {"startMillis": millis, "endMillis": millis + 30000,
                    "injectionStartedMillis": millis + 1, "completedMillis": millis + 2, "confirmed": True},
    "processEpochs": {"before": source["startedAt"], "after": source["startedAt"]},
    "targetBefore": [copy.deepcopy(target)], "targetDuring": [copy.deepcopy(target)], "targetAfter": [copy.deepcopy(target)],
}
physical["targetDuring"][0]["State"]["Paused"] = True
fault["physicalFault"] = physical
(root / "translation-connected.json").write_text(json.dumps(receipt))
for failure in ("missing-pause", "wrong-owner", "wrong-source-container", "changed-image", "changed-epoch",
                "unrestored", "oom", "late-physical-restoration", "failed-injector", "health-only",
                "missing-target-capture", "missing-physical-declaration", "missing-resources"):
    case = root.parent / ("translation-journal-fault-" + failure)
    shutil.copytree(root, case)
    changed = copy.deepcopy(receipt)
    sample = changed["faults"][0]["physicalFault"]
    if failure == "missing-pause":
        sample["targetDuring"][0]["State"]["Paused"] = False
    elif failure == "wrong-owner":
        sample["ownership"]["owner"] = "another-owner"
    elif failure == "wrong-source-container":
        sample["targetId"] = "f" * 64
    elif failure == "changed-image":
        sample["targetAfter"][0]["Image"] = "sha256:" + "f" * 64
    elif failure == "changed-epoch":
        sample["targetAfter"][0]["State"]["StartedAt"] = "2026-01-01T00:00:01Z"
    elif failure == "unrestored":
        sample["targetAfter"][0]["State"]["Paused"] = True
    elif failure == "oom":
        sample["targetAfter"][0]["State"]["OOMKilled"] = True
    elif failure == "late-physical-restoration":
        sample["faultWindow"]["completedMillis"] = millis + 3
    elif failure == "failed-injector":
        sample["injectorExitCode"] = 1
    elif failure == "health-only":
        sample["faultObserved"] = False
    elif failure == "missing-target-capture":
        del sample["targetDuring"]
    elif failure == "missing-physical-declaration":
        declaration = copy.deepcopy(config)
        del declaration["services"]["translationPhysicalFaults"]
        raw = json.dumps(declaration).encode()
        (case / "config.json").write_bytes(raw)
        new_digest = hashlib.sha256(raw).hexdigest()
        changed["configDigest"] = new_digest
        for filename in ("metadata.json", "translation-topology.json", "prometheus/manifest.json"):
            value = json.loads((case / filename).read_text())
            value["configDigest"] = new_digest
            (case / filename).write_text(json.dumps(value))
    elif failure == "missing-resources":
        shutil.rmtree(case / "prometheus")
    (case / "translation-connected.json").write_text(json.dumps(changed))
PY
translation_journal_resources() {
  CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 CAPACITY_PROMETHEUS_URL=http://prometheus \
    CAPACITY_PROMETHEUS_TARGETS=api-a:4000,api-b:4000 \
    CAPACITY_PROMETHEUS_CONTROLLER_TARGETS=controller-a:4000,controller-b:4000 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "$1" || {
      cat "$1/translation-verifier.json" >&2
      return 1
    }
}
translation_journal_resources "${test_dir}/translation-journal-fault"
jq -e '.resourceGatesVerified and .physicalFaultRecovery[0].name == "journal-interruption" and
  .physicalFaultRecovery[0].kind == "physical" and .passed == false' \
  "${test_dir}/translation-journal-fault/translation-verifier.json" >/dev/null
for failure in missing-pause wrong-owner wrong-source-container changed-image changed-epoch unrestored oom \
  late-physical-restoration failed-injector health-only missing-target-capture missing-physical-declaration missing-resources; do
  assert_rejected "Translation journal recovery accepted ${failure}" "${test_dir}/output" \
    translation_journal_resources "${test_dir}/translation-journal-fault-${failure}"
done
}
python3 - "${test_dir}/translation-connected" <<'PY'
import copy
import datetime
import hashlib
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
target = root.parent / "translation-callback-fault"
shutil.copytree(root, target)
receipt = json.loads((target / "translation-connected.json").read_text())
start = datetime.datetime.fromisoformat(receipt["start"])
at = lambda seconds: (start + datetime.timedelta(seconds=seconds)).isoformat()
push = receipt["pushes"][0]
fault = {
    "stage": "callback-loss", "kind": "simulated", "started": at(0),
    "faultAt": at(.0005), "restored": at(.001), "observed": at(5.005),
    "trafficFrom": at(.004), "trafficUntil": at(5.004),
    "recoveryDeadline": at(30.0005), "correctReads": 100,
    "commit": push["Commit"], "sourceSha256": "a" * 64,
    "referencesBeforeRestoration": "a" * 64, "referencesAfterRestoration": "a" * 64,
    "referencesSha256": "a" * 64,
    "physicalFault": None, "physicalRestart": None,
    "interception": {
        "runId": receipt["runId"], "kind": "simulated", "stage": "callback-loss",
        "newCommit": push["Commit"], "oldCommit": "a" * 40, "ref": "refs/heads/main",
        "at": at(.0005), "upstreamCompleted": False,
    },
    "delivery": {
        "runId": receipt["runId"], "kind": "forwarded", "stage": "",
        "newCommit": push["Commit"], "oldCommit": "a" * 40, "ref": "refs/heads/main",
        "at": at(.0015), "upstreamCompleted": True,
    },
    "trafficProof": {
        "schemaVersion": 1, "runId": receipt["runId"], "evidenceClass": "diagnostic",
        "faultAt": at(.0005), "trafficFrom": at(.004), "trafficUntil": at(5.004),
        "observed": at(5.005), "recoveryDeadline": at(30.0005),
        "rate": 20, "workers": 4, "commit": push["Commit"], "sourceSha256": "a" * 64,
        "sourceID": "source-0", "sourceVersion": "1",
        "recoveryReads": copy.deepcopy(push["Observations"]),
        "traffic": {
            "Offered": 100, "Completed": 100, "Dropped": 0, "UnexpectedErrors": 0,
            "CompletedSequences": list(range(100)), "Latencies": [1000000] * 100,
            "DispatchDelays": [0] * 100,
        },
    },
}
references = push["Commit"] + "\trefs/heads/main\n" + "a" * 40 + "\trefs/tags/retained\n"
reference_digest = hashlib.sha256(references.encode()).hexdigest()
for key in ("referencesBeforeRestoration", "referencesAfterRestoration", "referencesSha256"):
    fault[key] = reference_digest
fault["referenceSnapshotBeforeRestoration"] = fault["referenceSnapshotAfterRestoration"] = references
receipt["faults"] = [fault]
receipt["faultWindows"] = [{
    "name": "callback-loss", "kind": "simulated", "start": at(0),
    "end": at(30), "declaredAt": at(0),
}]
config = {"schemaVersion": 1, "services": {"translationFaults": {"callback-loss": "simulated"}}}
raw = json.dumps(config).encode()
(target / "config.json").write_bytes(raw)
receipt["configDigest"] = hashlib.sha256(raw).hexdigest()
metadata = json.loads((target / "metadata.json").read_text())
metadata["configDigest"] = receipt["configDigest"]
(target / "metadata.json").write_text(json.dumps(metadata))
(target / "translation-connected.json").write_text(json.dumps(receipt))
for failure in ("missing-window", "late-declaration", "unknown-commit", "wrong-source",
                "wrong-run", "missing-traffic", "one-health-read", "duplicate-sequences",
                "missing-latencies", "bad-read-tail", "late-restoration", "short-traffic",
                "late-recovery", "physical-claim", "missing-declaration", "missing-interception",
                "wrong-interception", "missing-replay", "stale-replay", "boolean-proof-version",
                "missing-reference-snapshot", "wrong-reference-head", "unjoined-reference-digest",
                "unsorted-references", "duplicate-references", "missing-recovery-reads",
                "reused-recovery-api", "stale-recovery-read", "changed-recovery-source",
                "changed-recovery-body", "historical-recovery-ready"):
    invalid = root.parent / ("translation-callback-fault-" + failure)
    shutil.copytree(target, invalid)
    changed = copy.deepcopy(receipt)
    sample = changed["faults"][0]
    if failure == "missing-window":
        changed["faultWindows"] = []
    elif failure == "late-declaration":
        changed["faultWindows"][0]["declaredAt"] = at(1)
    elif failure == "unknown-commit":
        sample["commit"] = "f" * 40
    elif failure == "wrong-source":
        sample["trafficProof"]["sourceID"] = "another-source"
    elif failure == "wrong-run":
        sample["trafficProof"]["runId"] = "previous-run"
    elif failure == "missing-traffic":
        del sample["trafficProof"]
    elif failure == "one-health-read":
        sample["trafficProof"]["traffic"]["Completed"] = 1
    elif failure == "duplicate-sequences":
        sample["trafficProof"]["traffic"]["CompletedSequences"][1] = 0
    elif failure == "missing-latencies":
        sample["trafficProof"]["traffic"]["Latencies"] = []
    elif failure == "bad-read-tail":
        sample["trafficProof"]["traffic"]["Latencies"] = [3000000000] * 100
    elif failure == "late-restoration":
        sample["restored"] = at(6)
    elif failure == "short-traffic":
        sample["trafficUntil"] = at(1)
    elif failure == "late-recovery":
        sample["observed"] = at(31)
    elif failure == "physical-claim":
        sample["physicalRestart"] = {"Kind": "physical"}
    elif failure == "missing-declaration":
        raw = json.dumps({"schemaVersion": 1, "services": {}}).encode()
        (invalid / "config.json").write_bytes(raw)
        changed["configDigest"] = hashlib.sha256(raw).hexdigest()
        changed_metadata = dict(metadata, configDigest=changed["configDigest"])
        (invalid / "metadata.json").write_text(json.dumps(changed_metadata))
    elif failure == "missing-interception":
        del sample["interception"]
    elif failure == "wrong-interception":
        sample["interception"]["upstreamCompleted"] = True
    elif failure == "missing-replay":
        sample["delivery"]["upstreamCompleted"] = False
    elif failure == "stale-replay":
        sample["delivery"]["at"] = at(0)
    elif failure == "boolean-proof-version":
        sample["trafficProof"]["schemaVersion"] = True
    elif failure == "missing-recovery-reads":
        del sample["trafficProof"]["recoveryReads"]
    elif failure == "reused-recovery-api":
        sample["trafficProof"]["recoveryReads"][1] = copy.deepcopy(sample["trafficProof"]["recoveryReads"][0])
    elif failure == "stale-recovery-read":
        sample["trafficProof"]["recoveryReads"][0]["VisibleAt"] = at(0)
    elif failure == "changed-recovery-source":
        sample["trafficProof"]["recoveryReads"][0]["UID"] = "replacement-source"
    elif failure == "changed-recovery-body":
        sample["trafficProof"]["recoveryReads"][0]["BodyDigest"] = "b" * 64
    elif failure == "historical-recovery-ready":
        sample["trafficProof"]["recoveryReads"][0]["Historical"] = True
    elif failure == "missing-reference-snapshot":
        del sample["referenceSnapshotBeforeRestoration"]
    elif failure == "unjoined-reference-digest":
        for key in ("referencesBeforeRestoration", "referencesAfterRestoration", "referencesSha256"):
            sample[key] = "a" * 64
    elif failure in ("wrong-reference-head", "unsorted-references", "duplicate-references"):
        snapshot = references
        if failure == "wrong-reference-head":
            snapshot = snapshot.replace(push["Commit"], "f" * 40)
        elif failure == "unsorted-references":
            snapshot = "".join(reversed(snapshot.splitlines(keepends=True)))
        else:
            snapshot += snapshot.splitlines(keepends=True)[-1]
        sample["referenceSnapshotBeforeRestoration"] = sample["referenceSnapshotAfterRestoration"] = snapshot
        for key in ("referencesBeforeRestoration", "referencesAfterRestoration", "referencesSha256"):
            sample[key] = hashlib.sha256(snapshot.encode()).hexdigest()
    (invalid / "translation-connected.json").write_text(json.dumps(changed))
reply = root.parent / "translation-reply-fault"
shutil.copytree(target, reply)
changed = copy.deepcopy(receipt)
changed["faults"][0]["stage"] = "reply-loss"
changed["faults"][0]["interception"]["stage"] = "reply-loss"
changed["faults"][0]["interception"]["upstreamCompleted"] = True
changed["faultWindows"][0]["name"] = "reply-loss"
raw = json.dumps({"schemaVersion": 1, "services": {"translationFaults": {"reply-loss": "simulated"}}}).encode()
(reply / "config.json").write_bytes(raw)
changed["configDigest"] = hashlib.sha256(raw).hexdigest()
(reply / "metadata.json").write_text(json.dumps(dict(metadata, configDigest=changed["configDigest"])))
(reply / "translation-connected.json").write_text(json.dumps(changed))
PY
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-callback-fault"
jq -e '.domainChecksPassed and (.faultRecovery | length) == 1 and
  .faultRecovery[0].kind == "simulated" and .faultRecovery[0].correctReads == 100 and .passed == false' \
  "${test_dir}/translation-callback-fault/translation-verifier.json" >/dev/null
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-reply-fault"
jq -e '.domainChecksPassed and .faultRecovery[0].name == "reply-loss" and .passed == false' \
  "${test_dir}/translation-reply-fault/translation-verifier.json" >/dev/null
for failure in missing-window late-declaration unknown-commit wrong-source wrong-run missing-traffic one-health-read \
  duplicate-sequences missing-latencies bad-read-tail late-restoration short-traffic late-recovery physical-claim missing-declaration \
  missing-interception wrong-interception missing-replay stale-replay boolean-proof-version \
  missing-reference-snapshot wrong-reference-head unjoined-reference-digest unsorted-references duplicate-references \
  missing-recovery-reads reused-recovery-api stale-recovery-read changed-recovery-source changed-recovery-body historical-recovery-ready; do
  assert_rejected "Translation callback recovery accepted ${failure}" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-callback-fault-${failure}"
done
translation_journal_regressions
translation_replacement_regressions() {
local role="$1"
python3 - "${test_dir}/translation-journal-fault" "${role}" <<'PY'
import copy
import datetime
import hashlib
import json
import pathlib
import shutil
import sys

original = pathlib.Path(sys.argv[1])
role = sys.argv[2]
root = original.parent / ("translation-" + role + "-replacement")
shutil.copytree(original, root)
topology = json.loads((root / "translation-topology.json").read_text())
offset = 0 if role == "api" else 2
process, peer = topology["processes"][offset:offset + 2]
config = json.loads((root / "config.json").read_text())
profile = role + "-restart"
config["services"]["translationPhysicalFaults"] = {profile: "physical"}
config["services"]["translationResourceGaps"] = [{
    "role": role, "container": process["container"], "fault": profile, "maximumSeconds": 30,
}]
raw = json.dumps(config).encode()
(root / "config.json").write_bytes(raw)
digest = hashlib.sha256(raw).hexdigest()
for filename in ("metadata.json", "translation-topology.json", "prometheus/manifest.json", "translation-connected.json"):
    value = json.loads((root / filename).read_text())
    value["configDigest"] = digest
    (root / filename).write_text(json.dumps(value))
receipt = json.loads((root / "translation-connected.json").read_text())
physical = receipt["faults"][0]["physicalFault"]
start = datetime.datetime.fromisoformat(receipt["start"])
replacement_at = (start + datetime.timedelta(milliseconds=1)).isoformat()
def capture(value):
    result = {"Id": value["containerID"], "Name": "/" + value["container"], "Image": value["imageID"],
              "State": {"Running": True, "Paused": False, "OOMKilled": False, "StartedAt": value["startedAt"]}}
    if role == "controller":
        result["Checkpoint"] = value["checkpoint"] | {"writable": True}
    return result
physical.update(profile=profile, action="restart", sourceScope="one-replica",
                target=process["container"], targetId=process["containerID"],
                targetBefore=[capture(process)], targetAfter=[capture(process)],
                peerBefore=[capture(peer)], peerAfter=[capture(peer)])
physical.pop("targetDuring")
physical["targetAfter"][0]["State"]["StartedAt"] = replacement_at
physical["processEpochs"] = {"before": process["startedAt"], "after": replacement_at}
(root / "translation-connected.json").write_text(json.dumps(receipt))
manifest = json.loads((root / "prometheus/manifest.json").read_text())
resource_file = "translation-api-series.json" if role == "api" else "controller-series.json"
timestamp_file = "translation-" + role + "-scrape-times.json"
for filename, digest_key in ((resource_file, "translationResourceDigests"),
                             (timestamp_file, "translationScrapeTimeDigests")):
    path = root / "prometheus" / filename
    matrix = json.loads(path.read_text())
    extra = []
    for series in matrix["data"]["result"]:
        name = series["metric"].get("__name__", series["metric"].get("source_metric"))
        values = series["values"]
        before = copy.deepcopy(values[0])
        before[0] -= 5
        if filename.endswith("-series.json") and name == "process_cpu_seconds_total":
            before[1] = "8"
        elif filename.endswith("-times.json"):
            before[1] = str(before[0])
        values.insert(0, before)
        if series["metric"]["instance"] != role + "-a:4000":
            continue
        if name == f"gitstore_{role}_process_instance_info":
            new = copy.deepcopy(series)
            series["values"] = values[:2]
            new["values"] = values[2:]
            new["metric"]["instance_id"] = role + "-replacement"
            extra.append(new)
        elif filename.endswith("-series.json"):
            if name == "process_start_time_seconds":
                for point in values[2:]:
                    point[1] = str(int(start.timestamp()))
            elif name == "process_cpu_seconds_total":
                values[2][1], values[3][1] = "1", "1.2"
            elif name == "go_gc_cycles_total":
                values[2][1], values[3][1] = "1", "2"
            elif name == "go_gc_heap_live_bytes":
                values[2][1], values[3][1] = "20000000", "20400000"
            elif name == "go_goroutines":
                values[2][1], values[3][1] = "500", "501"
    matrix["data"]["result"].extend(extra)
    path.write_text(json.dumps(matrix))
    manifest[digest_key][role] = hashlib.sha256(path.read_bytes()).hexdigest()
(root / "prometheus/manifest.json").write_text(json.dumps(manifest))
for failure in ("undeclared-gap", "wrong-gap-role", "oversized-gap", "duplicate-gap",
                "changed-peer", "wrong-replacement-image", "unchanged-process",
                "unconfirmed-restart", "late-replacement", "aliased-process",
                "reused-process", "counter-reset", "gc-reset", "heap-growth", "goroutine-drift",
                "missing-new-epoch-scrape", "overlapping-identities"):
    case = root.parent / ("translation-" + role + "-replacement-" + failure)
    shutil.copytree(root, case)
    changed = copy.deepcopy(receipt)
    physical = changed["faults"][0]["physicalFault"]
    if failure in ("undeclared-gap", "wrong-gap-role", "oversized-gap", "duplicate-gap"):
        declaration = copy.deepcopy(config)
        if failure == "undeclared-gap":
            declaration["services"]["translationResourceGaps"] = []
        elif failure == "wrong-gap-role":
            declaration["services"]["translationResourceGaps"][0]["role"] = "controller" if role == "api" else "api"
        elif failure == "oversized-gap":
            declaration["services"]["translationResourceGaps"][0]["maximumSeconds"] = 4
        else:
            declaration["services"]["translationResourceGaps"].append(
                copy.deepcopy(declaration["services"]["translationResourceGaps"][0]))
        raw = json.dumps(declaration).encode()
        (case / "config.json").write_bytes(raw)
        new_digest = hashlib.sha256(raw).hexdigest()
        changed["configDigest"] = new_digest
        for filename in ("metadata.json", "translation-topology.json", "prometheus/manifest.json"):
            value = json.loads((case / filename).read_text())
            value["configDigest"] = new_digest
            (case / filename).write_text(json.dumps(value))
    elif failure == "changed-peer":
        physical["peerAfter"][0]["State"]["StartedAt"] = replacement_at
    elif failure == "wrong-replacement-image":
        physical["targetAfter"][0]["Image"] = "sha256:" + "f" * 64
    elif failure == "unchanged-process":
        physical["processEpochs"]["after"] = physical["processEpochs"]["before"]
    elif failure == "unconfirmed-restart":
        physical["faultWindow"]["confirmed"] = False
    elif failure == "late-replacement":
        physical["targetAfter"][0]["State"]["StartedAt"] = (start + datetime.timedelta(seconds=1)).isoformat()
    elif failure in ("aliased-process", "reused-process", "counter-reset", "gc-reset",
                     "heap-growth", "goroutine-drift", "missing-new-epoch-scrape",
                     "overlapping-identities"):
        path = case / "prometheus" / resource_file
        matrix = json.loads(path.read_text())
        for series in matrix["data"]["result"]:
            metric = series["metric"]
            if metric["instance"] != role + "-a:4000":
                continue
            if failure in ("aliased-process", "reused-process") and metric.get("instance_id") == role + "-replacement":
                metric["instance_id"] = peer["instanceID"] if failure == "aliased-process" else process["instanceID"]
            elif failure == "counter-reset" and metric["__name__"] == "process_cpu_seconds_total":
                series["values"][-1][1] = ".5"
            elif failure == "gc-reset" and metric["__name__"] == "go_gc_cycles_total":
                series["values"][-2][1], series["values"][-1][1] = "2", "1"
            elif failure == "heap-growth" and metric["__name__"] == "go_gc_heap_live_bytes":
                series["values"][-1][1] = "23000000"
            elif failure == "goroutine-drift" and metric["__name__"] == "go_goroutines":
                series["values"][-1][1] = "526"
            elif failure == "missing-new-epoch-scrape":
                series["values"] = [point for point in series["values"] if point[0] != start.timestamp() + 5]
            elif failure == "overlapping-identities" and metric.get("instance_id") == role + "-replacement":
                series["values"].insert(0, [start.timestamp(), "1"])
        if failure == "reused-process":
            merged = {}
            for series in matrix["data"]["result"]:
                key = json.dumps(series["metric"], sort_keys=True)
                if key in merged:
                    merged[key]["values"].extend(series["values"])
                    merged[key]["values"].sort()
                else:
                    merged[key] = series
            matrix["data"]["result"] = list(merged.values())
        path.write_text(json.dumps(matrix))
        captured = json.loads((case / "prometheus/manifest.json").read_text())
        captured["translationResourceDigests"][role] = hashlib.sha256(path.read_bytes()).hexdigest()
        if failure in ("reused-process", "overlapping-identities"):
            times_path = case / "prometheus" / timestamp_file
            timestamps = json.loads(times_path.read_text())
            for series in timestamps["data"]["result"]:
                if series["metric"].get("instance_id") == role + "-replacement":
                    if failure == "reused-process":
                        series["metric"]["instance_id"] = process["instanceID"]
                    else:
                        series["values"].insert(0, [start.timestamp(), str(start.timestamp())])
            if failure == "reused-process":
                merged = {}
                for series in timestamps["data"]["result"]:
                    key = json.dumps(series["metric"], sort_keys=True)
                    if key in merged:
                        merged[key]["values"].extend(series["values"])
                        merged[key]["values"].sort()
                    else:
                        merged[key] = series
                timestamps["data"]["result"] = list(merged.values())
            times_path.write_text(json.dumps(timestamps))
            captured["translationScrapeTimeDigests"][role] = hashlib.sha256(times_path.read_bytes()).hexdigest()
        (case / "prometheus/manifest.json").write_text(json.dumps(captured))
    (case / "translation-connected.json").write_text(json.dumps(changed))
if role == "controller":
    for failure in ("missing-checkpoint", "changed-checkpoint", "shared-checkpoint", "nested-checkpoint",
                    "readonly-checkpoint", "wrong-checkpoint-destination", "aliased-checkpoint-volume",
                    "double-root-checkpoint"):
        case = root.parent / ("translation-controller-replacement-" + failure)
        shutil.copytree(root, case)
        changed, changed_topology = copy.deepcopy(receipt), copy.deepcopy(topology)
        sample = changed["faults"][0]["physicalFault"]
        if failure == "missing-checkpoint":
            del sample["targetBefore"][0]["Checkpoint"]
        elif failure == "changed-checkpoint":
            sample["targetAfter"][0]["Checkpoint"]["source"] += "/replacement"
        elif failure == "readonly-checkpoint":
            sample["targetAfter"][0]["Checkpoint"]["writable"] = False
        elif failure == "wrong-checkpoint-destination":
            changed_topology["processes"][offset]["checkpoint"]["destination"] = "/another-checkpoint"
            for field in ("targetBefore", "targetAfter"):
                sample[field][0]["Checkpoint"]["destination"] = "/another-checkpoint"
        elif failure in ("shared-checkpoint", "nested-checkpoint", "double-root-checkpoint"):
            source = process["checkpoint"]["source"] + ("/child" if failure == "nested-checkpoint" else "")
            if failure == "double-root-checkpoint":
                source = "/" + source
            changed_topology["processes"][offset + 1]["checkpoint"]["source"] = source
            for field in ("peerBefore", "peerAfter"):
                sample[field][0]["Checkpoint"]["source"] = source
        else:
            for index in (offset, offset + 1):
                changed_topology["processes"][index]["checkpoint"]["volume"] = "same-retained-volume"
            for field in ("targetBefore", "targetAfter", "peerBefore", "peerAfter"):
                sample[field][0]["Checkpoint"]["volume"] = "same-retained-volume"
        (case / "translation-topology.json").write_text(json.dumps(changed_topology))
        (case / "translation-connected.json").write_text(json.dumps(changed))
PY
translation_journal_resources "${test_dir}/translation-${role}-replacement"
jq -e --arg role "${role}" '.resourceGatesVerified and .physicalFaultRecovery[0].name == ($role + "-restart") and
  ([.resources[] | select(.role == $role)] | length) == 3 and .passed == false' \
  "${test_dir}/translation-${role}-replacement/translation-verifier.json" >/dev/null
for failure in undeclared-gap wrong-gap-role oversized-gap duplicate-gap changed-peer wrong-replacement-image \
  unchanged-process unconfirmed-restart late-replacement aliased-process reused-process counter-reset \
  gc-reset heap-growth goroutine-drift missing-new-epoch-scrape overlapping-identities; do
  assert_rejected "Translation ${role} replacement accepted ${failure}" "${test_dir}/output" \
    translation_journal_resources "${test_dir}/translation-${role}-replacement-${failure}"
done
if [[ "${role}" == controller ]]; then
  for failure in missing-checkpoint changed-checkpoint shared-checkpoint nested-checkpoint readonly-checkpoint \
    wrong-checkpoint-destination aliased-checkpoint-volume double-root-checkpoint; do
    assert_rejected "Translation controller replacement accepted ${failure}" "${test_dir}/output" \
      translation_journal_resources "${test_dir}/translation-controller-replacement-${failure}"
  done
fi
}
translation_replacement_regressions api
translation_replacement_regressions controller
python3 - "${test_dir}/translation-connected" <<'PY'
import copy
import datetime
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
target = root.parent / "translation-classified"
shutil.copytree(root, target)
receipt = json.loads((root / "translation-connected.json").read_text())
receipt["sourceSizes"] = {str(index): 100 for index in range(10)}
receipt["sourceSizes"]["0"] = 1024 * 1024
start = datetime.datetime.fromisoformat(receipt["start"])
for observation in receipt["pushes"][0]["Observations"]:
    observation["VisibleAt"] = (start + datetime.timedelta(seconds=20)).isoformat()
    observation["WatchAt"] = observation["VisibleAt"]
    observation["ReadyAt"] = (start + datetime.timedelta(seconds=21)).isoformat()
(target / "translation-connected.json").write_text(json.dumps(receipt))
for failure in ("missing-size", "oversized", "boolean-size", "missing-near-limit"):
    invalid = root.parent / ("translation-classified-" + failure)
    shutil.copytree(target, invalid)
    changed = copy.deepcopy(receipt)
    if failure == "missing-size":
        del changed["sourceSizes"]["1"]
    elif failure == "oversized":
        changed["sourceSizes"]["0"] += 1
    elif failure == "boolean-size":
        changed["sourceSizes"]["1"] = True
    else:
        changed["sourceSizes"]["0"] = 100
    (invalid / "translation-connected.json").write_text(json.dumps(changed))
PY
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-classified"
jq -e '.measuredPushTraffic.normal.pushes == 9 and .measuredPushTraffic["near-limit"].pushes == 1 and
  .measuredPushTraffic.normal.replicaObservations == 18 and
  .measuredPushTraffic.normal.convergenceSeconds.p95 < 0.003 and
  .measuredPushTraffic["near-limit"].readVisibilitySeconds.p95 < 0.003 and
  .measuredPushTraffic["near-limit"].watchDeliverySeconds.p95 > 19 and
  .measuredPushTraffic["near-limit"].convergenceSeconds.p95 > 20 and .passed == false' \
  "${test_dir}/translation-classified/translation-verifier.json" >/dev/null
for failure in missing-size oversized boolean-size missing-near-limit; do
  assert_rejected "classified Translation traffic accepted ${failure}" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-classified-${failure}"
done
python3 - "${test_dir}/translation-classified" <<'PY'
import copy
import datetime
import hashlib
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
target = root.parent / "translation-expected-rejections"
shutil.copytree(root, target)
receipt = json.loads((root / "translation-connected.json").read_text())
receipt["originalHeads"]["translation-negative"] = receipt["originalHeads"].pop("0")
preflight = json.loads((root / "translation-preflight.json").read_text())
start = datetime.datetime.fromisoformat(receipt["start"])
at = lambda seconds: (start + datetime.timedelta(seconds=seconds)).isoformat()
receipt["completedAt"] = at(11)
metadata = json.loads((target / "metadata.json").read_text())
metadata["translationCompletedAt"] = at(11)
(target / "metadata.json").write_text(json.dumps(metadata))
receipt["expectedRejectionTraffic"] = {
    "phase": "preparation", "offered": 1, "completed": 1, "rejected": 1,
    "pushes": [{
        "Case": "locale-underscore", "Reason": "InvalidLocale",
        "Commit": "e" * 40, "Parent": "a" * 40,
        "Namespace": "translation-negative", "InvalidName": "negative-invalid", "SentinelName": "negative-sentinel",
        "BeforeReferences": hashlib.sha256(("a" * 40 + "\trefs/heads/main\n").encode()).hexdigest(),
        "AfterReferences": hashlib.sha256(("a" * 40 + "\trefs/heads/main\n").encode()).hexdigest(),
        "SourceDigest": "d" * 64, "SentinelDigest": "e" * 64,
        "OfferedAt": at(-2), "CompletedAt": at(-1),
        "Observations": [{
            "Replica": endpoint, "InvalidAbsent": True, "SentinelAbsent": True, "CheckedAt": at(-.5),
        } for endpoint in preflight["apiEndpoints"]],
        "FinalObservations": [{
            "Replica": endpoint, "InvalidAbsent": True, "SentinelAbsent": True, "CheckedAt": receipt["end"],
        } for endpoint in preflight["apiEndpoints"]],
    }],
}
(target / "translation-connected.json").write_text(json.dumps(receipt))
for failure in ("accepted", "partial", "refs", "missing-replica", "duplicate-replica",
                "stale", "count", "source", "accepted-commit", "classification",
                "late-partial", "final-missing", "final-alias", "final-stale", "final-future"):
    invalid = root.parent / ("translation-rejection-" + failure)
    shutil.copytree(target, invalid)
    changed = copy.deepcopy(receipt)
    traffic = changed["expectedRejectionTraffic"]
    push = traffic["pushes"][0]
    if failure == "accepted":
        traffic["rejected"] = 0
    elif failure == "partial":
        push["Observations"][0]["SentinelAbsent"] = False
    elif failure == "refs":
        push["AfterReferences"] = "b" * 64
    elif failure == "missing-replica":
        push["Observations"].pop()
    elif failure == "duplicate-replica":
        push["Observations"][1] = push["Observations"][0]
    elif failure == "stale":
        push["CompletedAt"] = at(1)
    elif failure == "count":
        traffic["completed"] = 2
    elif failure == "source":
        push["SourceDigest"] = ""
    elif failure == "accepted-commit":
        push["Commit"] = receipt["pushes"][0]["Commit"]
    elif failure == "late-partial":
        push["FinalObservations"][0]["SentinelAbsent"] = False
    elif failure == "final-missing":
        del push["FinalObservations"]
    elif failure == "final-alias":
        push["FinalObservations"][1] = push["FinalObservations"][0]
    elif failure == "final-stale":
        push["FinalObservations"][0]["CheckedAt"] = at(-.5)
    elif failure == "final-future":
        push["FinalObservations"][0]["CheckedAt"] = at(100)
    else:
        push["Reason"] = "Unavailable"
    (invalid / "translation-connected.json").write_text(json.dumps(changed))
PY
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-expected-rejections"
jq -e '.expectedRejectionTraffic.pushes == 1 and .expectedRejectionTraffic.documents == 2 and
  .expectedRejectionTraffic.rejectionSeconds.p95 == 1 and .measuredPushTraffic.normal.pushes == 9 and
  .measuredPushTraffic["near-limit"].pushes == 1 and .passed == false' \
  "${test_dir}/translation-expected-rejections/translation-verifier.json" >/dev/null
for failure in accepted partial refs missing-replica duplicate-replica stale count source accepted-commit classification \
  late-partial final-missing final-alias final-stale final-future; do
  assert_rejected "expected-rejection Translation traffic accepted ${failure}" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-rejection-${failure}"
done
python3 - "${test_dir}/translation-connected" <<'PY'
import copy
import datetime
import json
import pathlib
import shutil
import sys

root = pathlib.Path(sys.argv[1])
target = root.parent / "translation-prepared"
shutil.copytree(root, target)
at = lambda delta: (datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc) +
                    datetime.timedelta(seconds=delta)).isoformat()
metadata = json.loads((target / "metadata.json").read_text())
metadata.update(loadStartedAt=at(-40), loadCompletedAt=at(25))
(target / "metadata.json").write_text(json.dumps(metadata))
summary = json.loads((target / "summary.json").read_text())
metrics = summary["metrics"]
for name in ("http_reqs", "http_req_failed", "http_req_duration", "checks", "iterations"):
    metrics[name + "{phase:measured}"] = copy.deepcopy(metrics[name])
for name in ("http_reqs", "iterations"):
    metrics[name]["values"]["count"] = 1100
metrics["checks"]["values"]["passes"] = 1100
for name in ("translation_preparation_requests", "translation_preparation_correct"):
    metrics[name] = {"values": {"count": 900}}
    for phase, count in (("warmup", 600), ("cooldown", 300)):
        metrics[name + "{phase:" + phase + "}"] = {"values": {"count": count}}
(target / "summary.json").write_text(json.dumps(summary))
receipt = json.loads((target / "translation-connected.json").read_text())
preparation = {
    "start": at(-30), "end": at(-10), "completedAt": at(-9),
    "warmupSeconds": 30, "cooldownSeconds": 15, "workers": 2,
    "intervalNanos": 1000000000, "offered": 20, "completed": 20, "verified": 20, "dropped": 0,
    "expected": {str(index): receipt["expected"]["0"] for index in range(20)}, "pushes": [],
}
for sequence in range(20):
    push = copy.deepcopy(receipt["pushes"][0])
    commit = f"{sequence + 11:040x}"
    push.update(Sequence=sequence, Commit=commit, OfferedAt=at(sequence - 30), AcknowledgedAt=at(sequence - 29.999))
    for observation in push["Observations"]:
        observation.update(UID=f"warmup-{sequence}", Revision="main@sha1:" + commit,
                           LastAppliedRevision="main@sha1:" + commit,
                           VisibleAt=at(sequence - 29.998), ReadyAt=at(sequence - 29.997),
                           ReadVisibleAt=at(sequence - 29.998), ReadReadyAt=at(sequence - 29.997),
                           WatchAt=at(sequence - 29.998))
    preparation["pushes"].append(push)
receipt["preparation"] = preparation
(target / "translation-connected.json").write_text(json.dumps(receipt))
barrier = json.loads((target / "translation-load-start.json").read_text())
barrier["warmupStartAt"] = at(-30)
(target / "translation-load-start.json").write_text(json.dumps(barrier))
for failure in ("source", "missing", "stale", "diluted"):
    invalid = root.parent / ("translation-prepared-" + failure)
    shutil.copytree(target, invalid)
    changed = copy.deepcopy(receipt)
    if failure == "source":
        changed["preparation"]["pushes"][0]["Observations"][1]["BodyDigest"] = "b" * 64
    elif failure == "missing":
        del changed["preparation"]
    elif failure == "stale":
        broken = dict(barrier, warmupStartAt=at(-29))
        (invalid / "translation-load-start.json").write_text(json.dumps(broken))
    else:
        broken = copy.deepcopy(summary)
        broken["metrics"]["http_req_duration{phase:measured}"]["values"]["p(99)"] = 3001
        (invalid / "summary.json").write_text(json.dumps(broken))
    (invalid / "translation-connected.json").write_text(json.dumps(changed))
precise = root.parent / "translation-prepared-fractional"
shutil.copytree(target, precise)
def shift(value):
    if isinstance(value, dict):
        return {key: shift(item) for key, item in value.items()}
    if isinstance(value, list):
        return [shift(item) for item in value]
    if isinstance(value, str) and len(value) > 10 and value[4] == "-" and value[10] == "T":
        return (datetime.datetime.fromisoformat(value.replace("Z", "+00:00")) +
                datetime.timedelta(milliseconds=250)).isoformat()
    return value
for filename in ("metadata.json", "translation-load-start.json", "translation-connected.json"):
    path = precise / filename
    path.write_text(json.dumps(shift(json.loads(path.read_text()))))
rounded = root.parent / "translation-prepared-rounded"
shutil.copytree(precise, rounded)
path = rounded / "metadata.json"
metadata = json.loads(path.read_text())
metadata["loadCompletedAt"] = datetime.datetime.fromisoformat(
    metadata["loadCompletedAt"]).replace(microsecond=0).isoformat()
path.write_text(json.dumps(metadata))
PY
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-prepared"
jq -e '.domainChecksPassed and .preparationRequests == 900 and .preparationPushes == 20 and .requests == 200 and .passed == false' \
  "${test_dir}/translation-prepared/translation-verifier.json" >/dev/null
for failure in source missing stale diluted; do
  assert_rejected "prepared Translation verifier accepted ${failure} evidence" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-prepared-${failure}"
done
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-prepared-fractional"
assert_rejected "prepared Translation verifier accepted truncated cooldown coverage" "${test_dir}/output" \
  env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-prepared-rounded"
jq -e '.reason == "read command does not bracket the complete cooldown"' \
  "${test_dir}/translation-prepared-rounded/translation-verifier.json" >/dev/null
python3 - "${test_dir}/translation-prepared" "${test_dir}/translation-expected-rejections" <<'PY'
import copy
import datetime
import json
import pathlib
import shutil
import sys

root, negative = map(pathlib.Path, sys.argv[1:])
target = root.parent / "translation-prepared-rejection"
shutil.copytree(root, target)
at = lambda delta: (datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc) +
                    datetime.timedelta(seconds=delta)).isoformat()
receipt = json.loads((target / "translation-connected.json").read_text())
rejected = json.loads((negative / "translation-connected.json").read_text())
receipt["originalHeads"] = rejected["originalHeads"]
receipt["expectedRejectionTraffic"] = rejected["expectedRejectionTraffic"]
receipt["completedAt"] = at(26)
push = receipt["expectedRejectionTraffic"]["pushes"][0]
push.update(OfferedAt=at(-32), CompletedAt=at(-31))
for observation in push["Observations"]:
    observation["CheckedAt"] = at(-30.5)
for observation in push["FinalObservations"]:
    observation["CheckedAt"] = at(25.5)
metadata = json.loads((target / "metadata.json").read_text())
metadata["translationCompletedAt"] = at(26.5)
(target / "metadata.json").write_text(json.dumps(metadata))
(target / "translation-connected.json").write_text(json.dumps(receipt))
for failure in ("cooldown", "completion"):
    invalid = root.parent / ("translation-prepared-rejection-" + failure)
    shutil.copytree(target, invalid)
    changed = copy.deepcopy(receipt)
    if failure == "cooldown":
        changed["expectedRejectionTraffic"]["pushes"][0]["FinalObservations"][0]["CheckedAt"] = at(24.5)
    else:
        changed["completedAt"] = at(27)
    (invalid / "translation-connected.json").write_text(json.dumps(changed))
PY
CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
  "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-prepared-rejection"
jq -e '.domainChecksPassed and .expectedRejectionTraffic.finalReplicaObservations == 2 and .passed == false' \
  "${test_dir}/translation-prepared-rejection/translation-verifier.json" >/dev/null
for failure in cooldown completion; do
  assert_rejected "prepared Translation verifier accepted ${failure} final rejection evidence" "${test_dir}/output" \
    env CAPACITY_TRANSLATION_READINESS=1 CAPACITY_TRANSLATION_PUSHES=1 \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-prepared-rejection-${failure}"
done
jq '.metrics |= with_entries(.value = .value.values) | .metrics.http_req_failed = {value:0,passes:0,fails:200}' \
  "${test_dir}/translation-readiness/summary.json" >"${test_dir}/translation-readiness/summary.tmp"
mv "${test_dir}/translation-readiness/summary.tmp" "${test_dir}/translation-readiness/summary.json"
CAPACITY_TRANSLATION_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/translation-readiness"
assert_dispatch api endpoint-split 'CAPACITY_PROFILE=api-endpoint-split'
authz_output="$(CAPACITY_DRY_RUN=1 "${dispatcher}" api authorization diagnostic)"
[[ "$authz_output" == *"_capacity-authorization"* ]]
for mode in alpha production; do
  if CAPACITY_DRY_RUN=1 "${dispatcher}" api authorization "$mode" >/dev/null 2>&1; then
    echo "incomplete authorization evidence was promoted to $mode" >&2
    exit 1
  fi
done
assert_dispatch namespace admission 'CAPACITY_PROFILE=namespace-admission'
assert_dispatch namespace validation '_capacity-namespace-admission'
assert_dispatch namespace watch '_capacity-namespace-watch'
assert_dispatch namespace recovery '_capacity-namespace-recovery'
assert_dispatch repository lifecycle '_capacity-repository-lifecycle'
assert_dispatch product lifecycle 'CAPACITY_PROFILE=product-lifecycle'
assert_dispatch category hierarchy 'CAPACITY_PROFILE=category-hierarchy'
assert_dispatch scylla soak '_capacity-scylla-soak'

rbac_command="$(CAPACITY_RBAC_WORKLOAD=1 make --no-print-directory -n -C "${repo_root}" _capacity-namespace-watch MODE=diagnostic)"
[[ "${rbac_command}" == *"go test -v -count=1 -timeout=4h -run '^TestNamespaceWatchDeploymentCapacity"* ]] || {
  echo "RBAC driver must allow bounded full-geometry setup plus the declared workload, not terminate at fifteen minutes" >&2
  exit 1
}

for target_profile in 'api readiness' 'namespace watch'; do
  read -r rbac_target rbac_profile <<<"${target_profile}"
  for mode in diagnostic alpha; do
    output="$(CAPACITY_RBAC_WORKLOAD=1 CAPACITY_DRY_RUN=1 "${dispatcher}" "${rbac_target}" "${rbac_profile}" "${mode}")"
    [[ "${output}" == *"_capacity-namespace-watch"* && "${output}" == *"CAPACITY_RBAC_WORKLOAD=1"* ]]
  done
  if CAPACITY_RBAC_WORKLOAD=1 CAPACITY_RBAC_SEED_FILE= CAPACITY_RBAC_TOPOLOGY_FILE= \
    CAPACITY_EVIDENCE_DIR="${test_dir}/rbac-missing-fixture" \
    "${dispatcher}" "${rbac_target}" "${rbac_profile}" diagnostic >/dev/null 2>&1; then
    echo "public RBAC load ran without its owned seed and topology preflight" >&2
    exit 1
  fi
done
if CAPACITY_RBAC_WORKLOAD=1 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle diagnostic >/dev/null 2>&1; then
  echo "unsupported RBAC family fell back to an ordinary driver" >&2
  exit 1
fi

for target_profile in 'api readiness' 'namespace watch' 'repository lifecycle'; do
  read -r rbac_target rbac_profile <<<"${target_profile}"
  if CAPACITY_RBAC_WORKLOAD=1 CAPACITY_DRY_RUN=1 "${dispatcher}" "${rbac_target}" "${rbac_profile}" production >/dev/null 2>&1; then
    echo "unsupported production RBAC selection silently dispatched a workload" >&2
    exit 1
  fi
done
if CAPACITY_RBAC_WORKLOAD=1 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle alpha >/dev/null 2>&1; then
  echo "unsupported RBAC alpha family silently dispatched a workload" >&2
  exit 1
fi
if CAPACITY_RBAC_WORKLOAD=invalid CAPACITY_DRY_RUN=1 "${dispatcher}" api readiness alpha >/dev/null 2>&1; then
  echo "invalid RBAC selector unexpectedly succeeded" >&2
  exit 1
fi
for mode in diagnostic alpha; do
  if CAPACITY_RBAC_WORKLOAD=1 MODE="${mode}" \
    "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}" >/dev/null 2>&1; then
    echo "incomplete or static RBAC evidence unexpectedly certified capacity" >&2
    exit 1
  fi
done

python3 - "${repo_root}/scripts/capacity-build-receipt.py" "${test_dir}" <<'PY'
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("capacity_build_receipt", sys.argv[1])
receipt = importlib.util.module_from_spec(spec)
spec.loader.exec_module(receipt)
root = Path(sys.argv[2]) / "build-source"
root.mkdir(mode=0o700)
for directory in ("gitstore-api", "gitstore-controller-manager", "shared", "docs", "owned"):
    (root / directory).mkdir(mode=0o700)
(root / "gitstore-api/go.mod").write_text("module fixture\n")
(root / "gitstore-controller-manager/go.mod").write_text("module controllerfixture\n")
source = root / "gitstore-api/main.go"
source.write_text("package main\n")
(root / "shared/input.go").write_text("package shared\n")
(root / "docs/readme.md").write_text("initial documentation\n")
receipt.git(root, "init", "-q")
def commit():
    receipt.git(root, "add", "gitstore-api", "gitstore-controller-manager", "shared", "docs")
    receipt.git(root, "-c", "core.hooksPath=/dev/null", "-c", "user.name=Capacity test",
                "-c", "user.email=capacity@example.invalid", "commit", "-qm", "test fixture")
commit()
artifact = root / "owned/api"
artifact.write_bytes(b"synthetic artifact, not a compiled service")
record_path = root / "owned/api.build.json"
digest, size = receipt.artifact_digest(artifact)
record = {
    "schemaVersion": 1, "role": "api", "sourceRevision": receipt.git(root, "rev-parse", "HEAD").decode().strip(),
    "sourceInputsSha256": receipt.source_digest(root, "api"), "artifactSha256": digest, "artifactBytes": size,
    "toolchain": "synthetic test fixture", "profiled": False, "buildKind": "repository-release",
    "command": ["go", "build", "-trimpath", "-o", str(artifact), "./cmd/server"],
}
def write_record(value):
    record_path.write_text(json.dumps(value))
    record_path.chmod(0o600)
def rejected(callback):
    try:
        callback()
    except (ValueError, OSError):
        return
    raise AssertionError("altered capacity build evidence was accepted")
write_record(record)
# Artifact/receipt files are deliberately ignored, as in a real private build directory.
(root / ".git/info/exclude").write_text("owned/\n")
receipt.verify(root, "api", artifact, record_path)
(root / "docs/readme.md").write_text("new unrelated documentation\n")
commit()
assert receipt.git(root, "rev-parse", "HEAD").decode().strip() != record["sourceRevision"]
receipt.verify(root, "api", artifact, record_path)
# Existing matching artifacts are verified and reused, never rebuilt or overwritten.
receipt.build(root, "api", artifact, record_path)
owned = {**record, "buildKind": "repository-owned-rbac",
         "command": ["go", "test", "-c", "-trimpath", "-o", str(artifact), "./internal/app"]}
write_record(owned)
receipt.verify(root, "api", artifact, record_path)
receipt.build(root, "api", artifact, record_path, "owned-rbac")
rejected(lambda: receipt.build(root, "api", artifact, record_path, "release"))
rejected(lambda: receipt.build(root, "controller", artifact, record_path, "owned-rbac"))
controller_owned = {
    **owned, "role": "controller", "sourceInputsSha256": receipt.source_digest(root, "controller"),
    "command": ["go", "test", "-c", "-trimpath", "-o", str(artifact), "./cmd/controller"],
}
write_record(controller_owned)
receipt.verify(root, "controller", artifact, record_path)
receipt.build(root, "controller", artifact, record_path, "owned-rbac")
rejected(lambda: receipt.build(root, "controller", artifact, record_path, "release"))
write_record({**controller_owned, "command": owned["command"]})
rejected(lambda: receipt.verify(root, "controller", artifact, record_path))
rejected(lambda: receipt.build(root, "git", artifact, record_path, "owned-rbac"))
write_record({**owned, "command": ["go", "test", "-c", "-race", "-o", str(artifact), "./internal/app"]})
rejected(lambda: receipt.verify(root, "api", artifact, record_path))
write_record(record)
original = artifact.read_bytes()
artifact.write_bytes(original + b"drift")
rejected(lambda: receipt.verify(root, "api", artifact, record_path))
artifact.write_bytes(original)
source.write_text("package changed\n")
commit()
rejected(lambda: receipt.verify(root, "api", artifact, record_path))
source.write_text("package main\n")
commit()
receipt.verify(root, "api", artifact, record_path)
write_record({**record, "profiled": True})
rejected(lambda: receipt.verify(root, "api", artifact, record_path))
write_record({**record, "command": ["go", "build", "-race"]})
rejected(lambda: receipt.verify(root, "api", artifact, record_path))
write_record(record)
record_path.chmod(0o644)
rejected(lambda: receipt.verify(root, "api", artifact, record_path))
record_path.chmod(0o600)
receipt.verify(root, "api", artifact, record_path)

# Exercise the producer's fixed owned-host recipe without compiling or starting a service.
real_run, real_output = receipt.subprocess.run, receipt.subprocess.check_output
def compiler(command, **kwargs):
    if command[0] != "go":
        return real_run(command, **kwargs)
    assert command == ["go", "test", "-c", "-trimpath", "-o", str(compiled), package]
    assert kwargs["env"]["GOWORK"] == "off" and kwargs["cwd"] == root / module
    compiled.write_bytes(b"synthetic compiler output, not a service")
    return subprocess.CompletedProcess(command, 0)
def version(command, **kwargs):
    if command == ["go", "version"]:
        return b"go version synthetic-test-toolchain"
    return real_output(command, **kwargs)
for name in ("GOFLAGS", "GOEXPERIMENT", "RUSTFLAGS", "CARGO_ENCODED_RUSTFLAGS"):
    os.environ.pop(name, None)
for role, module, package in (
    ("api", "gitstore-api", "./internal/app"),
    ("controller", "gitstore-controller-manager", "./cmd/controller"),
):
    compiled, compiled_receipt = root / ("owned/compiled-" + role), root / ("owned/compiled-" + role + ".json")
    try:
        receipt.subprocess.run, receipt.subprocess.check_output = compiler, version
        receipt.build(root, role, compiled, compiled_receipt, "owned-rbac")
    finally:
        receipt.subprocess.run, receipt.subprocess.check_output = real_run, real_output
    produced = receipt.verify(root, role, compiled, compiled_receipt)
    assert produced["buildKind"] == "repository-owned-rbac" and not produced["profiled"]
PY

node - "${repo_root}/tests/capacity/profiles/api-readiness.js" <<'JS'
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const crypto = require('node:crypto');
const source = fs.readFileSync(process.argv[2], 'utf8')
  .replace(/^import .*;\r?\n/gm, '')
  .replace('export const options', 'const options')
  .replace('export default function ()', 'function runIteration()');
const cases = Array.from({ length: 10 }, (_, index) => ({
  subject: `reader-${index}`, namespace: `owned-${index}`,
  action: index % 2 ? 'role.list' : 'product.list',
  expected: index < 6 ? 'allow' : index < 9 ? 'deny' : 'isolation',
  token: 'private-fixture-token',
}));
function load(overrides = {}, fixtureCases = cases) {
  const metrics = {};
  const requests = [];
  const failures = [];
  const receipts = [];
  const execution = { scenario: { name: 'rbac_0', iterationInTest: 0 } };
  const fixture = { owner: 'owned-rbac', cases: fixtureCases };
  let response;
  const variables = {
    CAPACITY_BASE_URL: 'http://api-a', CAPACITY_RBAC_WORKLOAD: '1',
    CAPACITY_API_ENDPOINTS: 'http://api-a,http://api-b',
    CAPACITY_RBAC_FIXTURE_FILE: 'private-fixture.json',
    CAPACITY_IDENTITY_FIXTURE_FILE: 'private-identity.json', ...overrides,
  };
  const context = {
    __ENV: variables, execution,
    crypto: { sha256: (body) => crypto.createHash('sha256').update(body).digest('hex') },
    console: { error: (message) => receipts.push(JSON.parse(message)) },
    env: (name) => { assert.ok(variables[name], `missing ${name}`); return variables[name]; },
    integer: (name, fallback) => variables[name] === undefined ? fallback : Number(variables[name]),
    open: () => JSON.stringify(fixture),
    SharedArray: class { constructor(_, create) { return create(); } },
    Counter: class {
      constructor(name) { this.name = name; metrics[name] = 0; }
      add(value) { metrics[this.name] += value; }
    },
    check: (_, checks) => {
      for (const [name, predicate] of Object.entries(checks)) {
        if (!predicate(_)) failures.push(name);
      }
    },
    http: {
      post: (url, body, params) => { requests.push({ url, body: JSON.parse(body), params }); return response; },
      get: (url) => { requests.push({ url }); return { status: 200 }; },
    },
  };
  const profile = vm.runInNewContext(`${source}\n({options, runIteration})`, context);
  return {
    ...profile, metrics, requests, failures, execution, receipts,
    respond(status, body) {
      response = { status, body: JSON.stringify(body), json: () => body };
    },
    rawResponse(value) { response = value; },
  };
}
const workload = load();
assert.deepEqual(Object.keys(workload.options.scenarios), ['rbac_0', 'rbac_1']);
for (const scenario of Object.values(workload.options.scenarios)) {
  assert.equal(scenario.rate, 500);
  assert.equal(scenario.duration, '30m');
}
assert.equal(workload.options.thresholds['rbac_requests{replica:0}'][0], 'count==900000');
assert.equal(workload.options.thresholds['rbac_completed{replica:1}'][0], 'count==900000');
for (let replica = 0; replica < 2; replica++) {
  for (let index = 0; index < cases.length; index++) {
    const sample = cases[index];
    const field = sample.action === 'role.list' ? 'roles' : 'products';
    workload.execution.scenario.name = `rbac_${replica}`;
    workload.execution.scenario.iterationInTest = index;
    workload.respond(200, sample.expected === 'allow'
      ? { data: { [field]: { edges: [] } } }
      : { data: { [field]: null }, errors: [{ extensions: { code: 'FORBIDDEN' } }] });
    workload.runIteration();
    const request = workload.requests.at(-1);
    assert.equal(request.url, `http://api-${replica ? 'b' : 'a'}/admin/graphql`);
    assert.equal(request.body.variables.namespace, sample.namespace);
    assert.equal(request.params.headers.Authorization, `Bearer ${sample.token}`);
    assert.ok(request.params.headers['X-Request-ID']);
    assert.equal(request.params.timeout, '5s');
    assert.equal(request.params.redirects, 0);
  }
}
assert.equal(new Set(workload.requests.map((r) => r.params.headers['X-Request-ID'])).size, 20);
assert.equal(workload.metrics.rbac_requests, 20);
assert.equal(workload.metrics.rbac_completed, 20);
assert.equal(workload.metrics.rbac_allowed, 12);
assert.equal(workload.metrics.rbac_denied, 6);
assert.equal(workload.metrics.rbac_isolation, 2);
const boundary = load({ CAPACITY_DURATION: '6s', CAPACITY_RATE: '30' });
boundary.respond(200, { errors: [{ extensions: { code: 'FORBIDDEN' } }] });
boundary.execution.scenario.iterationInTest = 179;
boundary.runIteration();
assert.equal(boundary.requests.length, 1);
boundary.execution.scenario.iterationInTest = 180;
boundary.runIteration();
boundary.execution.scenario.iterationInTest = 181;
boundary.runIteration();
assert.equal(boundary.requests.length, 1, 'k6 boundary iterations must not introduce undeclared ordinary offers');
assert.equal(boundary.metrics.rbac_requests, 1);
assert.equal(boundary.metrics.rbac_completed, 1);
assert.equal(workload.failures.length, 0);
assert.equal(workload.receipts.length, 0, 'successful ordinary traffic must not emit response receipts');
for (const body of [
  { data: { products: { edges: [] } } },
  { errors: [{ extensions: { code: 'FORBIDDEN' } }, { extensions: { code: 'AUTHORIZATION_UNAVAILABLE' } }] },
  { errors: [{ extensions: { code: 'FORBIDDEN' } }], data: { products: { edges: [] } } },
]) {
  const denied = load();
  denied.execution.scenario.iterationInTest = 8;
  denied.respond(200, body);
  denied.runIteration();
  assert.equal(denied.metrics.rbac_wrong, 1);
  assert.equal(denied.metrics.rbac_denied, 0);
  assert.equal(denied.failures.length, 1);
}
const unavailable = load();
unavailable.respond(503, {});
unavailable.runIteration();
assert.equal(unavailable.metrics.rbac_unavailable, 1);
assert.equal(unavailable.metrics.rbac_completed, 1);
assert.equal(unavailable.failures.length, 1);
assert.equal(unavailable.receipts[0].reason, 'http_status');
for (const [status, code, reason, classification] of [
  [200, 'AUTHORIZATION_UNAVAILABLE', 'graphql_authorization_unavailable', 'unavailable'],
  [200, 'DIRECTORY_UNAVAILABLE', 'graphql_directory_unavailable', 'unavailable'],
  [200, 'UNAUTHENTICATED', 'graphql_unauthenticated', 'unavailable'],
  [200, 'INTERNAL_SERVER_ERROR', 'graphql_error', 'wrong'],
  [200, 'RESOURCE_EXHAUSTED', 'graphql_quota', 'wrong'],
  [429, 'RATE_LIMITED', 'http_throttled', 'unavailable'],
]) {
  const failed = load();
  failed.execution.scenario.name = 'rbac_1';
  failed.respond(status, { errors: [{ message: 'Authorization: Bearer private-fixture-token', extensions: { code } }] });
  failed.runIteration();
  const receipt = failed.receipts[0];
  assert.equal(receipt.correlationID, 'owned-rbac-ordinary-1-0');
  assert.equal(receipt.replica, 1);
  assert.equal(receipt.action, 'product.list');
  assert.equal(receipt.namespace, 'owned-0');
  assert.equal(receipt.reason, reason);
  assert.equal(receipt.classification, classification);
  assert.equal(receipt.errorCodes[0], code);
  assert.match(receipt.responseDigest, /^[0-9a-f]{64}$/);
  assert.ok(!JSON.stringify(receipt).includes('private-fixture-token'));
  assert.ok(!JSON.stringify(receipt).includes('Authorization'));
}
for (const [response, reason] of [
  [{ status: 0, body: '', error_code: 1050, error: 'Bearer private-fixture-token',
    json: () => { throw new Error('transport'); } }, 'transport'],
  [{ status: 200, body: '{private-fixture-token', json: () => { throw new Error('secret parser detail'); } }, 'invalid_json'],
  [{ status: 200, body: 'x'.repeat(65537), json: () => { throw new Error('must not parse oversized body'); } }, 'oversized'],
]) {
  const failed = load();
  failed.rawResponse(response);
  failed.runIteration();
  assert.equal(failed.receipts[0].reason, reason);
  assert.ok(!JSON.stringify(failed.receipts[0]).includes('private-fixture-token'));
  if (reason === 'oversized') assert.equal(failed.receipts[0].responseDigest, null);
}
const secretCodes = load();
secretCodes.respond(200, { errors: Array.from({ length: 20 }, () => ({
  extensions: { code: 'private-fixture-token' }, message: 'raw private response',
})) });
secretCodes.runIteration();
assert.equal(secretCodes.receipts[0].errorCodes.length, 8);
assert.ok(secretCodes.receipts[0].errorCodes.every((code) => code === 'UNKNOWN'));
assert.equal(secretCodes.receipts[0].errorCodesTruncated, true);
assert.ok(!JSON.stringify(secretCodes.receipts[0]).includes('private-fixture-token'));
const mixedBeyondCap = load();
mixedBeyondCap.respond(200, { errors: [
  ...Array.from({ length: 8 }, () => ({ extensions: { code: 'AUTHORIZATION_UNAVAILABLE' } })),
  { extensions: { code: 'INTERNAL_SERVER_ERROR' } },
] });
mixedBeyondCap.runIteration();
assert.equal(mixedBeyondCap.receipts[0].classification, 'wrong');
assert.equal(mixedBeyondCap.receipts[0].reason, 'graphql_error', 'truncated codes must not fabricate a uniform unavailable cause');
const taintedNamespace = load({}, cases.map((sample) => ({ ...sample, namespace: sample.token })));
taintedNamespace.respond(503, {});
taintedNamespace.runIteration();
assert.equal(taintedNamespace.receipts[0].namespace, '<redacted>');
assert.ok(JSON.stringify(taintedNamespace.receipts[0]).length < 1400);
const capped = load();
capped.respond(503, {});
for (let index = 0; index < 20; index++) {
  capped.execution.scenario.iterationInTest = index;
  capped.runIteration();
}
assert.equal(capped.receipts.length, 16);
assert.equal(capped.metrics.rbac_failure_receipts_suppressed, 4);
assert.equal(capped.metrics.rbac_unavailable, 20, 'receipt caps must never suppress failed gate accounting');
assert.throws(() => load({ CAPACITY_IDENTITY_READINESS: '1' }), /RBAC.*identity|identity.*RBAC/i);
assert.throws(() => load({ CAPACITY_API_ENDPOINTS: 'http://api-a,http://api-a/' }), /distinct/);
assert.throws(() => load({}, cases.slice(0, 9)), /60\/30\/10|ten|10/);
assert.throws(() => load({}, cases.map((sample) => ({ ...sample, expected: 'allow' }))), /60\/30\/10/);
const health = load({ CAPACITY_RBAC_WORKLOAD: '0' });
health.runIteration();
assert.deepEqual(health.requests.map((r) => r.url), ['http://api-a/health', 'http://api-a/ready']);
assert.equal(health.metrics.rbac_requests, 0);
console.log('RBAC k6 ordinary-request and unchanged health regressions passed');
JS

secret_output="$(REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle production)"
[[ "${secret_output}" == *"_capacity-repository-lifecycle"* ]]
[[ "${secret_output}" == *"REPOSITORY_CAPACITY_SECRET_SCENARIO=1"* ]]
normal_output="$(REPOSITORY_CAPACITY_SECRET_SCENARIO=0 CAPACITY_DRY_RUN=1 "${dispatcher}" repository lifecycle production)"
[[ "${normal_output}" != *"REPOSITORY_CAPACITY_SECRET_SCENARIO=1"* ]]
for target_profile in 'namespace watch' 'product lifecycle' 'api readiness' 'scylla soak'; do
  read -r other_target other_profile <<<"${target_profile}"
  if REPOSITORY_CAPACITY_SECRET_SCENARIO=1 CAPACITY_DRY_RUN=1 "${dispatcher}" "${other_target}" "${other_profile}" production >/dev/null 2>&1; then
    echo "secret scenario flag accepted on an unrelated capacity profile" >&2
    exit 1
  fi
done

assert_rejected "invalid target/profile combination unexpectedly succeeded" "${test_dir}/output" \
  env CAPACITY_DRY_RUN=1 "${dispatcher}" namespace unknown alpha
assert_rejected "legacy gate mode unexpectedly succeeded" "${test_dir}/output" \
  env CAPACITY_DRY_RUN=1 "${dispatcher}" namespace watch gate

mkdir -p "${test_dir}/bin" "${test_dir}/readiness-valid" "${test_dir}/readiness-missing"
mkdir -p "${test_dir}/identity-verifier"
python3 - "${test_dir}/identity-verifier" <<'PY'
import json
from pathlib import Path
import sys
root = Path(sys.argv[1])
documents = {
    'metadata.json': {'gitDirty': False, 'loadStartedAt': '2026-01-01T00:00:00Z', 'loadCompletedAt': '2026-01-01T00:30:01Z'},
    'preflight-environment.json': {'passed': True, 'topology': {'apiReplicas': 2}},
    'identity-preflight.json': {'passed': True, 'counts': {'staff': 1000, 'customers': 100000, 'serviceAccounts': 100}, 'durationSeconds': 1800, 'ratePerReplica': 500},
    'identity-connected.json': {'events': 90, 'sourceAuthorityPassed': True, 'groupsPassed': True, 'laterDeliveryPassed': True, 'gitProcessUnchanged': True, 'sharedStoragePreserved': True},
    'summary.json': {'metrics': {'checks': {'values': {'fails': 0}}, 'dropped_iterations': {'values': {'count': 0}}}},
}
for index in range(2):
    for name in ['identity_requests', 'identity_decisions', 'identity_groups']:
        documents['summary.json']['metrics'][f'{name}{{replica:{index}}}'] = {'values': {'count': 900000}}
    (root / f'identity-before-{index}.prom').write_text('')
    (root / f'identity-after-{index}.prom').write_text('''gitstore_userdir_requests_total{outcome="success",method="GET"} 9000
gitstore_userdir_resolution_duration_seconds_count{cache="hit",outcome="success"} 900000
gitstore_userdir_resolution_duration_seconds_bucket{cache="hit",outcome="success",le="0.01"} 900000
gitstore_userdir_resolution_duration_seconds_count{cache="miss",outcome="success"} 9000
gitstore_userdir_resolution_duration_seconds_bucket{cache="miss",outcome="success",le="0.1"} 9000
gitstore_userdir_cache_entries_count 909000
gitstore_userdir_cache_entries_bucket{le="256"} 909000
gitstore_userdir_serviceaccount_buffer_depth_count 90
gitstore_userdir_serviceaccount_buffer_depth_bucket{le="64"} 90
gitstore_userdir_serviceaccount_delivery_total{outcome="applied"} 90
''')
for name, value in documents.items():
    (root / name).write_text(json.dumps(value))
PY
CAPACITY_IDENTITY_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/identity-verifier"
jq -e '.passed and (.replicas | length) == 2' "${test_dir}/identity-verifier/identity-verifier.json" >/dev/null
jq '.metrics |= with_entries(.value = .value.values)' "${test_dir}/identity-verifier/summary.json" >"${test_dir}/identity-verifier/summary.tmp"
mv "${test_dir}/identity-verifier/summary.tmp" "${test_dir}/identity-verifier/summary.json"
CAPACITY_IDENTITY_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/identity-verifier"
jq -e '.passed' "${test_dir}/identity-verifier/identity-verifier.json" >/dev/null
python3 - "${test_dir}" <<'PY'
import datetime
import json
from pathlib import Path
import shutil
import sys
root = Path(sys.argv[1])
target = root / 'identity-chaos-verifier'
shutil.copytree(root / 'identity-verifier', target)
identity = json.loads((target / 'identity-preflight.json').read_text())
epoch = int(datetime.datetime(2026, 1, 1, tzinfo=datetime.timezone.utc).timestamp() * 1000)
identity['faultWindows'] = [
    {'replicas': [0, 1], 'startMillis': epoch + 120000, 'endMillis': epoch + 150000},
    {'replicas': [0], 'startMillis': epoch + 240000, 'endMillis': epoch + 270000},
]
(target / 'identity-preflight.json').write_text(json.dumps(identity))
companion = json.loads((target / 'identity-connected.json').read_text())
companion['faults'] = [
    {'profile': profile, 'injected': True, 'processChanged': True, 'recoveredOnBoth': True,
     'injectedMillis': window['startMillis'] + 100, 'dependencyReadyMillis': window['startMillis'] + 500,
     'recoveredMillis': window['startMillis'] + 1000}
    for profile, window in zip(['identity-restart', 'api-restart'], identity['faultWindows'])
]
old_metrics = (target / 'identity-after-0.prom').read_text()
companion['apiRestartMetrics'] = {
    'before': old_metrics + 'process_start_time_seconds 1\n',
    'after': 'process_start_time_seconds 2\n',
}
(target / 'identity-before-0.prom').write_text('process_start_time_seconds 1\n')
(target / 'identity-after-0.prom').write_text(old_metrics + 'process_start_time_seconds 2\n')
(target / 'identity-connected.json').write_text(json.dumps(companion))
summary = json.loads((target / 'summary.json').read_text())
for index, unavailable in enumerate([100, 0]):
    summary['metrics'][f'identity_unavailable{{replica:{index}}}'] = {'count': unavailable}
    for name in ['identity_decisions', 'identity_groups']:
        summary['metrics'][f'{name}{{replica:{index}}}']['count'] -= unavailable
(target / 'summary.json').write_text(json.dumps(summary))
for failure in ['receipt', 'reset', 'membership', 'recovery']:
    invalid = root / f'identity-chaos-{failure}'
    shutil.copytree(target, invalid)
    if failure == 'membership':
        broken = json.loads(json.dumps(summary))
        broken['metrics']['identity_groups{replica:0}']['count'] -= 1
        (invalid / 'summary.json').write_text(json.dumps(broken))
    else:
        broken = json.loads(json.dumps(companion))
        if failure == 'receipt':
            broken['faults'][0]['injected'] = False
        elif failure == 'reset':
            broken['apiRestartMetrics']['after'] = 'process_start_time_seconds 1\n'
        else:
            broken['faults'][0]['recoveredMillis'] = identity['faultWindows'][0]['endMillis'] + 1
        (invalid / 'identity-connected.json').write_text(json.dumps(broken))
PY
CAPACITY_IDENTITY_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/identity-chaos-verifier"
jq -e '.passed and .chaos and .replicas[0].unavailable == 100' "${test_dir}/identity-chaos-verifier/identity-verifier.json" >/dev/null
for failure in receipt reset membership recovery; do
  if CAPACITY_IDENTITY_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" \
    "${test_dir}/identity-chaos-${failure}" >/dev/null 2>&1; then
    echo "identity chaos verifier accepted invalid ${failure} evidence" >&2
    exit 1
  fi
done
jq '.metrics["identity_groups{replica:1}"].count -= 1' "${test_dir}/identity-verifier/summary.json" >"${test_dir}/identity-verifier/summary.tmp"
mv "${test_dir}/identity-verifier/summary.tmp" "${test_dir}/identity-verifier/summary.json"
if CAPACITY_IDENTITY_READINESS=1 "${repo_root}/tests/capacity/verifiers/api-readiness.sh" "${test_dir}/identity-verifier" >/dev/null 2>&1; then
  echo "identity verifier accepted a request without matching membership evidence" >&2
  exit 1
fi
jq -e '.passed == false' "${test_dir}/identity-verifier/identity-verifier.json" >/dev/null
write_mock "${test_dir}/chaos-bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
case "$1" in
  inspect)
    id=owned-identity
    source=/private/checkpoints/controller-a
    owner="${MOCK_CHAOS_OWNER:-owned-test}"
    running=true
    paused="${MOCK_CHAOS_PAUSED:-false}"
    if [[ -e "${MOCK_CHAOS_MARKER}.paused" ]]; then paused=true; fi
    started=old-epoch
    if [[ "$2" == *controller-b* || "$2" == *api-b* ]]; then
      id=owned-peer
      source="${MOCK_CHAOS_PEER_CHECKPOINT:-/private/checkpoints/controller-b}"
      owner="${MOCK_CHAOS_PEER_OWNER:-$owner}"
      paused="${MOCK_CHAOS_PEER_PAUSED:-$paused}"
    fi
    if [[ -n "${MOCK_CHAOS_INJECTOR_ID:-}" && "$2" == "${MOCK_CHAOS_INJECTOR_ID}" ]]; then
      jq -n --arg id "$2" --arg owner "${owner}" \
        '[{Id:$id,Config:{Labels:{"dev.gitstore.chaos-owner":$owner,"dev.gitstore.chaos-run":"timeout"}},
          State:{Running:true,Paused:false,StartedAt:"injector-epoch"}}]'
      exit
    fi
    if [[ -e "${MOCK_CHAOS_MARKER}" ]]; then
      if [[ "${MOCK_CHAOS_REPLACEMENT:-0}" == 1 ]]; then id=replacement; fi
      if [[ "${MOCK_CHAOS_FAILURE:-0}" == 1 && ! -e "${MOCK_CHAOS_MARKER}.restored" ]]; then running=false; fi
      if [[ "${MOCK_CHAOS_RESTART:-0}" == 1 && "$2" != *controller-b* && "$2" != *api-b* ]]; then started=new-epoch; fi
      if [[ "${MOCK_CHAOS_PEER_RESTART:-0}" == 1 && "$2" == *api-b* ]]; then started=changed-peer-epoch; fi
      if [[ "${MOCK_CHAOS_PAUSE_FAILURE:-0}" == 1 && ! -e "${MOCK_CHAOS_MARKER}.restored" ]]; then running=true; paused=true; fi
      if [[ "$2" == *controller-b* ]]; then
        source="${MOCK_CHAOS_PEER_CHECKPOINT_AFTER:-$source}"
      else
        source="${MOCK_CHAOS_TARGET_CHECKPOINT_AFTER:-$source}"
      fi
    fi
    jq -n --arg id "${id}" --arg owner "${owner}" --arg source "${source}" --arg started "${started}" \
      --argjson running "${running}" --argjson paused "${paused}" \
      '[{Id:$id,Config:{Labels:{"com.docker.compose.project":$owner,"dev.gitstore.test_owner":$owner}},
        State:{Running:$running,Paused:$paused,StartedAt:$started},
        Mounts:[{Type:"bind",Source:$source,Destination:"/var/lib/gitstore/checkpoints",RW:true}]}]'
    ;;
  run)
    touch "${MOCK_CHAOS_MARKER}"
    if [[ "${MOCK_CHAOS_HANG:-0}" == 1 ]]; then
      while [[ "$1" != --cidfile ]]; do shift; done
      printf '%s' "${MOCK_CHAOS_INJECTOR_ID}" >"$2"
      touch "${MOCK_CHAOS_MARKER}.paused"
      exec sleep 3
    fi
    if [[ " $* " == *" pause "* && "${MOCK_CHAOS_PAUSE_FAILURE:-0}" != 1 && "${MOCK_CHAOS_NO_PAUSE:-0}" != 1 ]]; then
      touch "${MOCK_CHAOS_MARKER}.paused"
      sleep 0.4
      rm "${MOCK_CHAOS_MARKER}.paused"
    fi
    exit "${MOCK_CHAOS_FAILURE:-0}"
    ;;
  start)
    [[ "$2" == owned-identity ]]
    touch "${MOCK_CHAOS_MARKER}.restored"
    ;;
  unpause)
    [[ "$2" == owned-identity ]]
    if [[ -e "${MOCK_CHAOS_MARKER}.paused" ]]; then rm "${MOCK_CHAOS_MARKER}.paused"; fi
    touch "${MOCK_CHAOS_MARKER}.restored"
    ;;
  stop)
    [[ "$2" == --time && "$3" == 1 && "$4" == "${MOCK_CHAOS_INJECTOR_ID}" ]]
    touch "${MOCK_CHAOS_MARKER}.injector-stopped"
    ;;
  *) exit 2 ;;
esac
MOCK
export MOCK_CHAOS_MARKER="${test_dir}/chaos-marker"
for rejection in confirmation missing-owner owner paused target; do
  confirm=1; owner=owned-test; paused=false; target=gitstore-userdir-kratos-test
  case "${rejection}" in
    confirmation) confirm=0 ;;
    missing-owner) owner="" ;;
    owner) owner=someone-else ;;
    paused) paused=true ;;
    target) target=gitstore-api-test ;;
  esac
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM="${confirm}" CHAOS_OWNER="${owner}" \
    CHAOS_TARGET="${target}" MOCK_CHAOS_PAUSED="${paused}" \
    CHAOS_EVIDENCE_DIR="${test_dir}/chaos" "${repo_root}/scripts/run-chaos.sh" identity-restart >/dev/null 2>&1; then
    echo "identity chaos unexpectedly accepted ${rejection}" >&2
    exit 1
  fi
  [[ ! -e "${MOCK_CHAOS_MARKER}" ]]
done
PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_OWNER=owned-test \
  CHAOS_OWNERSHIP_LABEL=dev.gitstore.test_owner CHAOS_TARGET=gitstore-userdir-kratos-test \
  CHAOS_RUN_ID=owned CHAOS_EVIDENCE_DIR="${test_dir}/chaos" \
  "${repo_root}/scripts/run-chaos.sh" identity-restart >/dev/null
jq -e '.injected and .restorationRequired and .restored and .exitCode == 0' \
  "${test_dir}/chaos/identity-restart/owned/metadata.json" >/dev/null
for failure in injector replacement; do
  injector_failure=0; replacement=0
  case "${failure}" in injector) injector_failure=1 ;; replacement) replacement=1 ;; esac
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_OWNER=owned-test \
    CHAOS_TARGET=gitstore-userdir-kratos-test MOCK_CHAOS_FAILURE="${injector_failure}" \
    MOCK_CHAOS_MARKER="${test_dir}/chaos-marker-${failure}" \
    MOCK_CHAOS_REPLACEMENT="${replacement}" CHAOS_RUN_ID="${failure}" \
    CHAOS_EVIDENCE_DIR="${test_dir}/chaos" "${repo_root}/scripts/run-chaos.sh" identity-restart >/dev/null 2>&1; then
    echo "identity chaos unexpectedly passed ${failure}" >&2
    exit 1
  fi
done
jq -e '.restored and (.injected | not) and .exitCode != 0' \
  "${test_dir}/chaos/identity-restart/injector/metadata.json" >/dev/null
jq -e '(.restored | not) and .injected and .exitCode != 0' \
  "${test_dir}/chaos/identity-restart/replacement/metadata.json" >/dev/null

window_start="$(python3 -c 'import time; print(time.time_ns() // 1000000 - 1000)')"
window_end="$((window_start + 30000))"
for rejection in missing-owner owner window expired peer shared-checkpoint nested-checkpoint paused-peer singleton-git; do
  owner=owned-test; peer=gitstore-controller-b; peer_checkpoint=/private/checkpoints/controller-b
  peer_paused=false
  start="${window_start}"; end="${window_end}"; profile=api-restart; target=gitstore-api-a
  case "${rejection}" in
    missing-owner) owner="" ;;
    owner) owner=someone-else ;;
    window) end="$((start + 30001))" ;;
    expired) start=1; end=2 ;;
    peer) profile=controller-restart; target=gitstore-controller-a; peer="" ;;
    shared-checkpoint) profile=controller-restart; target=gitstore-controller-a; peer_checkpoint=/private/checkpoints/controller-a ;;
    nested-checkpoint) profile=controller-restart; target=gitstore-controller-a; peer_checkpoint=/private/checkpoints/controller-a/nested ;;
    paused-peer) profile=controller-restart; target=gitstore-controller-a; peer_paused=true ;;
    singleton-git) target=gitstore-git-service ;;
  esac
  marker="${test_dir}/rbac-reject-${rejection}"
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=1 CHAOS_OWNER="${owner}" \
    CHAOS_TARGET="${target}" CHAOS_PEER_TARGET="${peer}" MOCK_CHAOS_PEER_CHECKPOINT="${peer_checkpoint}" MOCK_CHAOS_PEER_PAUSED="${peer_paused}" \
    CHAOS_WINDOW_START_MILLIS="${start}" CHAOS_WINDOW_END_MILLIS="${end}" MOCK_CHAOS_MARKER="${marker}" \
    CHAOS_EVIDENCE_DIR="${test_dir}/chaos" "${repo_root}/scripts/run-chaos.sh" "${profile}" >/dev/null 2>&1; then
    echo "RBAC chaos accepted invalid ${rejection} preflight" >&2
    exit 1
  fi
  [[ ! -e "${marker}" ]]
done
for profile in api-restart controller-restart journal-interruption; do
  target=gitstore-api-a
  case "${profile}" in controller-restart) target=gitstore-controller-a ;; journal-interruption) target=gitstore-scylla-source ;; esac
  restart=1; if [[ "${profile}" == journal-interruption ]]; then restart=0; fi
  PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=1 CHAOS_OWNER=owned-test \
    CHAOS_TARGET="${target}" CHAOS_PEER_TARGET=gitstore-controller-b \
    CHAOS_WINDOW_START_MILLIS="${window_start}" CHAOS_WINDOW_END_MILLIS="${window_end}" \
    MOCK_CHAOS_RESTART="${restart}" MOCK_CHAOS_MARKER="${test_dir}/rbac-${profile}" \
    CHAOS_RUN_ID=rbac CHAOS_EVIDENCE_DIR="${test_dir}/chaos" \
    "${repo_root}/scripts/run-chaos.sh" "${profile}" >/dev/null
  jq -e '.injected and .restored and .exitCode == 0 and .faultWindow.confirmed and
    (.processEpochs.before | length > 0) and (.processEpochs.after | length > 0) and
    (.domainRecoveryVerified == false)' "${test_dir}/chaos/${profile}/rbac/metadata.json" >/dev/null
done
if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=1 CHAOS_OWNER=owned-test \
  CHAOS_TARGET=gitstore-scylla-source CHAOS_WINDOW_START_MILLIS="${window_start}" CHAOS_WINDOW_END_MILLIS="${window_end}" \
  MOCK_CHAOS_FAILURE=1 MOCK_CHAOS_PAUSE_FAILURE=1 MOCK_CHAOS_MARKER="${test_dir}/rbac-pause-failed" \
  CHAOS_RUN_ID=failed CHAOS_EVIDENCE_DIR="${test_dir}/chaos" \
  "${repo_root}/scripts/run-chaos.sh" journal-interruption >/dev/null 2>&1; then
  echo "failed RBAC pause injector unexpectedly passed" >&2
  exit 1
fi
jq -e '.restored and (.injected | not) and .exitCode != 0' \
  "${test_dir}/chaos/journal-interruption/failed/metadata.json" >/dev/null
for profile in api-restart journal-interruption; do
  target=gitstore-api-a
  if [[ "${profile}" == journal-interruption ]]; then target=gitstore-scylla-source; fi
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=1 CHAOS_OWNER=owned-test \
    CHAOS_TARGET="${target}" CHAOS_WINDOW_START_MILLIS="${window_start}" CHAOS_WINDOW_END_MILLIS="${window_end}" \
    MOCK_CHAOS_NO_PAUSE=1 MOCK_CHAOS_MARKER="${test_dir}/rbac-no-effect-${profile}" \
    CHAOS_RUN_ID=no-effect CHAOS_EVIDENCE_DIR="${test_dir}/chaos" \
    "${repo_root}/scripts/run-chaos.sh" "${profile}" >/dev/null 2>&1; then
    echo "RBAC chaos accepted a successful injector without a real ${profile} effect" >&2
    exit 1
  fi
  jq -e '.restored and (.faultObserved | not) and (.injected | not) and .exitCode != 0' \
    "${test_dir}/chaos/${profile}/no-effect/metadata.json" >/dev/null
done
timeout_start="$(python3 -c 'import time; print(time.time_ns() // 1000000)')"
if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=1 CHAOS_OWNER=owned-test \
  CHAOS_TARGET=gitstore-scylla-source CHAOS_DURATION=1ms \
  CHAOS_WINDOW_START_MILLIS="${timeout_start}" CHAOS_WINDOW_END_MILLIS="$((timeout_start + 6500))" \
  MOCK_CHAOS_HANG=1 MOCK_CHAOS_INJECTOR_ID="$(printf 'b%.0s' {1..64})" MOCK_CHAOS_MARKER="${test_dir}/rbac-timeout" \
  CHAOS_RUN_ID=timeout CHAOS_EVIDENCE_DIR="${test_dir}/chaos" \
  "${repo_root}/scripts/run-chaos.sh" journal-interruption >/dev/null 2>&1; then
  echo "RBAC chaos accepted a timed-out injector" >&2
  exit 1
fi
[[ -e "${test_dir}/rbac-timeout.injector-stopped" && -e "${test_dir}/rbac-timeout.restored" ]]
jq -e '.restored and .faultObserved and (.faultWindow.confirmed | not) and .exitCode != 0' \
  "${test_dir}/chaos/journal-interruption/timeout/metadata.json" >/dev/null

for rejection in mixed-domain missing-owner missing-peer peer-owner singleton-git shared-checkpoint double-root-checkpoint invalid-selector; do
  profile=api-restart; target=gitstore-api-a; peer=gitstore-api-b
  owner=owned-test; peer_owner=owned-test; rbac=0; translation=1
  peer_checkpoint=/private/checkpoints/controller-b
  case "${rejection}" in
    mixed-domain) rbac=1 ;;
    missing-owner) owner="" ;;
    missing-peer) peer="" ;;
    peer-owner) peer_owner=another-owner ;;
    singleton-git) target=gitstore-git-service ;;
    shared-checkpoint) profile=controller-restart; target=gitstore-controller-a; peer=gitstore-controller-b; peer_checkpoint=/private/checkpoints/controller-a ;;
    double-root-checkpoint) profile=controller-restart; target=gitstore-controller-a; peer=gitstore-controller-b; peer_checkpoint=//private/checkpoints/controller-a ;;
    invalid-selector) translation=true ;;
  esac
  start="$(python3 -c 'import time; print(time.time_ns() // 1000000)')"
  marker="${test_dir}/translation-chaos-reject-${rejection}"
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC="${rbac}" CHAOS_TRANSLATION="${translation}" CHAOS_OWNER="${owner}" \
    CHAOS_TARGET="${target}" CHAOS_PEER_TARGET="${peer}" MOCK_CHAOS_PEER_OWNER="${peer_owner}" \
    MOCK_CHAOS_PEER_CHECKPOINT="${peer_checkpoint}" CHAOS_WINDOW_START_MILLIS="${start}" CHAOS_WINDOW_END_MILLIS="$((start + 30000))" \
    MOCK_CHAOS_MARKER="${marker}" CHAOS_RUN_ID="translation-${rejection}" CHAOS_EVIDENCE_DIR="${test_dir}/chaos" \
    "${repo_root}/scripts/run-chaos.sh" "${profile}" >/dev/null 2>&1; then
    echo "Translation chaos accepted invalid ${rejection} preflight" >&2
    exit 1
  fi
  [[ ! -e "${marker}" ]]
done
for profile in api-restart controller-restart journal-interruption; do
  target=gitstore-api-a; peer=gitstore-api-b; restart=1
  case "${profile}" in
    controller-restart) target=gitstore-controller-a; peer=gitstore-controller-b ;;
    journal-interruption) target=gitstore-scylla; restart=0 ;;
  esac
  start="$(python3 -c 'import time; print(time.time_ns() // 1000000)')"
  PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=0 CHAOS_TRANSLATION=1 CHAOS_OWNER=owned-test \
    CHAOS_TARGET="${target}" CHAOS_PEER_TARGET="${peer}" MOCK_CHAOS_RESTART="${restart}" \
    CHAOS_WINDOW_START_MILLIS="${start}" CHAOS_WINDOW_END_MILLIS="$((start + 30000))" \
    MOCK_CHAOS_MARKER="${test_dir}/translation-chaos-${profile}" CHAOS_RUN_ID=translation \
    CHAOS_EVIDENCE_DIR="${test_dir}/chaos" "${repo_root}/scripts/run-chaos.sh" "${profile}" >/dev/null
  jq -e '.runId == "translation" and .domain == "translation" and .kind == "physical" and .injected and .restored and .faultWindow.confirmed and
    .exitCode == 0 and (.domainRecoveryVerified == false)' "${test_dir}/chaos/${profile}/translation/metadata.json" >/dev/null
done

jq -e '.[0].Checkpoint == {source:"/private/checkpoints/controller-a",volume:"",
  destination:"/var/lib/gitstore/checkpoints",writable:true}' \
  "${test_dir}/chaos/controller-restart/translation/target-before.json" >/dev/null
jq -e '.[0].Checkpoint == {source:"/private/checkpoints/controller-b",volume:"",
  destination:"/var/lib/gitstore/checkpoints",writable:true}' \
  "${test_dir}/chaos/controller-restart/translation/peer-after.json" >/dev/null
for changed in target peer; do
  start="$(python3 -c 'import time; print(time.time_ns() // 1000000)')"
  target_after=/private/checkpoints/controller-a
  peer_after=/private/checkpoints/controller-b
  if [[ "${changed}" == target ]]; then target_after=/private/checkpoints/replaced-target; else peer_after=/private/checkpoints/replaced-peer; fi
  if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=0 CHAOS_TRANSLATION=1 CHAOS_OWNER=owned-test \
    CHAOS_TARGET=gitstore-controller-a CHAOS_PEER_TARGET=gitstore-controller-b MOCK_CHAOS_RESTART=1 \
    MOCK_CHAOS_TARGET_CHECKPOINT_AFTER="${target_after}" MOCK_CHAOS_PEER_CHECKPOINT_AFTER="${peer_after}" \
    CHAOS_WINDOW_START_MILLIS="${start}" CHAOS_WINDOW_END_MILLIS="$((start + 30000))" \
    MOCK_CHAOS_MARKER="${test_dir}/translation-chaos-checkpoint-${changed}" CHAOS_RUN_ID="checkpoint-${changed}" \
    CHAOS_EVIDENCE_DIR="${test_dir}/chaos" "${repo_root}/scripts/run-chaos.sh" controller-restart >/dev/null 2>&1; then
    echo "Translation controller restart accepted changed ${changed} checkpoint storage" >&2
    exit 1
  fi
  jq -e '.restored and (.faultWindow.confirmed | not) and .exitCode != 0' \
    "${test_dir}/chaos/controller-restart/checkpoint-${changed}/metadata.json" >/dev/null
done

start="$(python3 -c 'import time; print(time.time_ns() // 1000000)')"
if PATH="${test_dir}/chaos-bin:${PATH}" CHAOS_CONFIRM=1 CHAOS_RBAC=0 CHAOS_TRANSLATION=1 CHAOS_OWNER=owned-test \
  CHAOS_TARGET=gitstore-api-a CHAOS_PEER_TARGET=gitstore-api-b MOCK_CHAOS_RESTART=1 MOCK_CHAOS_PEER_RESTART=1 \
  CHAOS_WINDOW_START_MILLIS="${start}" CHAOS_WINDOW_END_MILLIS="$((start + 30000))" \
  MOCK_CHAOS_MARKER="${test_dir}/translation-chaos-peer-changed" CHAOS_RUN_ID=peer-changed \
  CHAOS_EVIDENCE_DIR="${test_dir}/chaos" "${repo_root}/scripts/run-chaos.sh" api-restart >/dev/null 2>&1; then
  echo "Translation chaos accepted a changed surviving API peer" >&2
  exit 1
fi
jq -e '.restored and (.faultWindow.confirmed | not) and .exitCode != 0' \
  "${test_dir}/chaos/api-restart/peer-changed/metadata.json" >/dev/null

source_revision="$(git -C "${repo_root}" rev-parse HEAD)"
image_digest="$(printf 'a%.0s' {1..64})"
write_mock "${test_dir}/readiness-bin/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
source "${CAPACITY_TEST_FIXTURES}/capacity.sh"
if [[ "${MOCK_BAD_READY:-0}" == 1 && "$*" == *api-b.internal/ready* ]]; then exit 22; fi
if [[ "${MOCK_AGGREGATE_NOT_READY:-0}" == 1 && "${*: -1}" == */ready ]]; then exit 22; fi
case "$*" in
  *api-a*|*api-alias*) mock_api_metrics a ;;
  *api-b*) mock_api_metrics b ;;
  *) exit 1 ;;
esac
MOCK
capacity_service_containers "${source_revision}" "${image_digest}" >"${test_dir}/base-service-containers.json"
jq 'map(select(.Name != "/git-1"))' "${test_dir}/base-service-containers.json" >"${test_dir}/readiness-containers.json"
jq '.[0].Path = "/bin/sh"' "${test_dir}/readiness-containers.json" >"${test_dir}/shell-readiness-containers.json"
jq '.[].Config.Image = ("sha256:" + ("a" * 64))' "${test_dir}/readiness-containers.json" >"${test_dir}/local-digest-readiness-containers.json"
jq '.[0].Config.Image = "ghcr.io/gitstore-dev/api:latest"' "${test_dir}/readiness-containers.json" >"${test_dir}/unverified-readiness-containers.json"
write_mock "${test_dir}/readiness-bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
source "${CAPACITY_TEST_FIXTURES}/capacity.sh"
if [[ "$1" == exec && "${*:3}" == "/app/api gitctl process-executable" ]]; then
  printf '%s\n' "${MOCK_API_EXECUTABLE:-/app/api}"
elif [[ "$1" == exec && "${*:3}" == "/app/api gitctl metrics" ]]; then
  case "$2" in
    api-1) mock_api_metrics a container ;;
    api-2) mock_api_metrics b container ;;
    *) exit 1 ;;
  esac
elif [[ "$1" == exec ]]; then
  exit 127
else
  cat "${MOCK_READINESS_CONTAINERS_FILE}"
fi
MOCK
export MOCK_READINESS_CONTAINERS_FILE="${test_dir}/readiness-containers.json"
cp "${repo_root}/tests/capacity/examples/config-manifest.json" "${test_dir}/readiness-valid/config.json"
cp "${repo_root}/tests/capacity/examples/environment-manifest.json" "${test_dir}/readiness-valid/environment.json"
run_readiness() (
  local evidence="$1"
  shift
  env PATH="${test_dir}/readiness-bin:${PATH}" MODE=production CAPACITY_BASE_URL=http://api-a.internal \
    CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release \
    CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_RUNTIME_MEMORY_BYTES=17179869184 \
    "$@" "${repo_root}/tests/capacity/preflight/api-readiness.sh" "${test_dir}/${evidence}"
)

run_readiness readiness-valid
jq -e '.passed == true and .topology.apiReplicas == 2 and (.topology.liveReplicas | length) == 2 and .resources.runtimeMemoryBytes == 17179869184' \
  "${test_dir}/readiness-valid/preflight-environment.json" >/dev/null
run_readiness readiness-valid MOCK_READINESS_CONTAINERS_FILE="${test_dir}/shell-readiness-containers.json"
run_readiness readiness-valid MOCK_READINESS_CONTAINERS_FILE="${test_dir}/local-digest-readiness-containers.json"
assert_rejected "readiness accepted a shell as the live API process" "${test_dir}/output" \
  run_readiness readiness-valid MOCK_API_EXECUTABLE=/bin/sh
assert_rejected "production API readiness unexpectedly passed without deployment manifests" "${test_dir}/output" \
  run_readiness readiness-missing
assert_rejected "production API readiness unexpectedly accepted one live process twice" "${test_dir}/output" \
  run_readiness readiness-valid CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-a.internal
run_readiness readiness-valid MOCK_SAME_START=1
for mismatch in start instance; do
  assert_rejected "readiness accepted mismatched container ${mismatch}" "${test_dir}/output" \
    run_readiness readiness-valid MOCK_CONTAINER_IDENTITY_MISMATCH="${mismatch}"
done
assert_rejected "production API readiness unexpectedly accepted an unrelated workload URL" "${test_dir}/output" \
  run_readiness readiness-valid CAPACITY_BASE_URL=http://unverified.internal
assert_rejected "production API readiness unexpectedly accepted an unready secondary replica" "${test_dir}/output" \
  run_readiness readiness-valid MOCK_BAD_READY=1
assert_rejected "aggregate readiness ignored an unavailable selected role" "${test_dir}/output" \
  run_readiness readiness-valid MOCK_AGGREGATE_NOT_READY=1
run_readiness readiness-valid MOCK_AGGREGATE_NOT_READY=1 CAPACITY_API_READINESS_ROLE=admin
assert_rejected "Admin role readiness ignored an unready secondary replica" "${test_dir}/output" \
  run_readiness readiness-valid MOCK_BAD_READY=1 CAPACITY_API_READINESS_ROLE=admin
assert_rejected "role readiness accepted an arbitrary operational path" "${test_dir}/output" \
  run_readiness readiness-valid CAPACITY_API_READINESS_ROLE=health
assert_rejected "production API readiness unexpectedly accepted a mutable live image" "${test_dir}/output" \
  run_readiness readiness-valid MOCK_READINESS_CONTAINERS_FILE="${test_dir}/unverified-readiness-containers.json"

make_output="$(make --no-print-directory -C "${repo_root}" capacity TARGET=namespace PROFILE=watch MODE=alpha CAPACITY_DRY_RUN=1)"
[[ "${make_output}" == *"target=namespace profile=watch mode=alpha"* ]]

for removed_target in capacity-observability capacity-observability-down test-scylla-capacity test-namespace-admission-capacity test-namespace-watch-capacity test-namespace-watch-recovery; do
  if rg -q "^${removed_target}:" "${repo_root}/Makefile"; then
    echo "removed public target ${removed_target} is still defined" >&2
    exit 1
  fi
done

mkdir -p "${test_dir}/evidence"
# The deployment phase has its own command directory; never rewrite readiness mocks.
cp "${test_dir}/readiness-bin/curl" "${test_dir}/bin/curl"
write_mock "${test_dir}/bin/make" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
echo "mock capacity command: $*"
if [[ -n "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE:-}" ]]; then
  cp "${MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE}" "${MOCK_SERVICE_CONTAINERS_FILE}"
fi
if [[ -n "${MOCK_REPLACEMENT_MARKER_PREFIX:-}" ]]; then
  touch "${MOCK_REPLACEMENT_MARKER_PREFIX}-${CAPACITY_RUN_ID:-}"
fi
[[ "${FAIL_MAKE:-0}" != 1 ]]
MOCK
write_mock "${test_dir}/bin/docker" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
source "${CAPACITY_TEST_FIXTURES}/capacity.sh"
if [[ "$1" == exec && "${*:3}" == "/app/api gitctl process-executable" ]]; then
  printf '%s\n' "${MOCK_API_EXECUTABLE:-/app/api}"
elif [[ "$1" == exec ]]; then
  case "$2" in
    api-1|api-2)
      [[ "${*:3}" == "/app/api gitctl metrics" ]] || exit 127
      if [[ "$2" == api-1 ]]; then mock_api_metrics a container; else mock_api_metrics b container; fi
      ;;
    *)
      for ((padding=0; padding<${MOCK_SCYLLA_STATUS_PADDING_LINES:-0}; padding++)); do
        printf 'membership diagnostic padding 0123456789012345678901234567890123456789\n'
      done
      if [[ "${MOCK_SCYLLA_BAD_MEMBERSHIP:-0}" == 1 ]]; then
        printf 'UN node-1\n'
      else
        printf 'UN node-1\nUN node-2\nUN node-3\n'
      fi
      ;;
  esac
elif [[ "$*" == *api-1* ]]; then
  cat "${MOCK_SERVICE_CONTAINERS_FILE}"
else
  cat "${MOCK_SCYLLA_CONTAINERS_FILE}"
fi
MOCK
write_mock "${test_dir}/bin/git" <<'MOCK'
#!/usr/bin/env bash
case "$1" in
  rev-parse) printf '%s\n' "${MOCK_SOURCE_REVISION}" ;;
  status|diff|ls-files) exit 0 ;;
  *) exit 1 ;;
esac
MOCK
export MOCK_SOURCE_REVISION="${source_revision}"
jq '.[0].Path = "/bin/sh" |
  .[0].Args = ["-ec","export GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY=\"$(cat /run/secrets/serviceaccount-signing-key)\"; exec /app/api --config-file /etc/gitstore/gitstore.toml"]' \
  "${test_dir}/base-service-containers.json" >"${test_dir}/service-containers.json"
jq 'map(.Config.Image |= sub("@sha256:[0-9a-f]+$"; ":local-alpha"))' "${test_dir}/service-containers.json" >"${test_dir}/local-service-containers.json"
jq 'map(.Config.Env = [
  if .Name == "/git-1" then "GITSTORE_GIT_SERVICE__CATALOG__URI=dns:///api-a:6000"
  else "GITSTORE_API__GIT_SERVICE__URI=dns:///git-1:50051" end
])' "${test_dir}/service-containers.json" >"${test_dir}/repository-service-containers.json"
jq 'map(.Config.Image |= sub("@sha256:[0-9a-f]+$"; ":local-alpha"))' "${test_dir}/repository-service-containers.json" >"${test_dir}/repository-local-service-containers.json"
jq '.[0].Config.Labels["org.opencontainers.image.revision"] = "wrong-revision"' "${test_dir}/service-containers.json" >"${test_dir}/unverified-service-containers.json"
jq '.[1].Id = .[0].Id' "${test_dir}/service-containers.json" >"${test_dir}/duplicate-service-containers.json"
jq -n '[range(1;4) as $i | {Id:("scylla-id-"+($i|tostring)),Name:("/scylla-"+($i|tostring)),Config:{Image:"scylla",Cmd:["--smp=2"]},RestartCount:0,HostConfig:{Memory:3221225472,NanoCpus:0,CpusetCpus:""},State:{Running:true,OOMKilled:false,StartedAt:"start"}}]' >"${test_dir}/scylla-containers.json"
jq 'map(.Config.Cmd = ["--smp=1"] | .HostConfig.Memory = 1610612736)' "${test_dir}/scylla-containers.json" >"${test_dir}/alpha-scylla-containers.json"
jq '.services.scylla.smpPerNode = 1 | .services.gitService.replicas = 1' "${repo_root}/tests/capacity/examples/config-manifest.json" >"${test_dir}/alpha-config-manifest.json"
jq '.datastore.memoryBytesPerNode = 1610612736 | .topology.gitServiceReplicas = 1' "${repo_root}/tests/capacity/examples/environment-manifest.json" >"${test_dir}/alpha-environment-manifest.json"
jq '[.[0], .[0], .[0]]' "${test_dir}/scylla-containers.json" >"${test_dir}/duplicate-scylla-containers.json"
export MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/scylla-containers.json"
export MOCK_REPLACEMENT_MARKER_PREFIX="${test_dir}/replacement"
export NAMESPACE_WATCH_API_REPLACEMENT=http://api-a.internal
export NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE="${test_dir}/replacement-trigger"

run_capacity() (
  local run_id="$1" target="$2" profile="$3" mode="$4"
  shift 4
  env PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID="${run_id}" \
    CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
    CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
    "$@" "${dispatcher}" "${target}" "${profile}" "${mode}"
)

run_deployed_capacity() {
  local run_id="$1" target="$2" profile="$3" mode="$4"
  shift 4
  run_capacity "${run_id}" "${target}" "${profile}" "${mode}" \
    CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
    NAMESPACE_WATCH_API_A=http://api-a.internal NAMESPACE_WATCH_API_B=http://api-b.internal \
    CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
    CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
    CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
    "$@"
}

mkdir -p "${test_dir}/rbac-bin"
printf '{}\n' >"${test_dir}/rbac-seed.json"
printf '{}\n' >"${test_dir}/rbac-topology.json"
write_mock "${test_dir}/rbac-bin/go" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${RBAC_TEST_CAPACITY_VERIFY_K6:-0}" == 1 ]]; then
  if [[ "${MOCK_RBAC_SKIP_VERDICT:-0}" != 1 ]]; then
    umask 077
    jq -n --arg revision "${MOCK_SOURCE_REVISION}" \
      '{Passed:true,SourceRevision:$revision,Window:{Owner:"owned-rbac",ProfileDigest:("a"*64)}}' \
      >"${CAPACITY_RBAC_EVIDENCE_DIR}/rbac-alpha-verifier.json"
  fi
  if [[ "${MOCK_RBAC_SOURCE_DRIFT:-0}" == 1 ]]; then
    touch "${MOCK_RBAC_SOURCE_MARKER}"
  fi
fi
MOCK
write_mock "${test_dir}/rbac-bin/git" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == status && -f "${MOCK_RBAC_SOURCE_MARKER:-}" ]]; then
  printf ' M fixture-source.go\n'
else
  exec "${MOCK_RBAC_BASE_GIT}" "$@"
fi
MOCK
run_rbac_dispatch() {
  local run_id="$1" target="$2" profile="$3"
  shift 3
  env PATH="${test_dir}/rbac-bin:${test_dir}/bin:${PATH}" CAPACITY_RBAC_WORKLOAD=1 \
    CAPACITY_EVIDENCE_DIR="${test_dir}/rbac-evidence" CAPACITY_RUN_ID="${run_id}" \
    CAPACITY_CONFIG_MANIFEST="${repo_root}/tests/capacity/examples/config-manifest.json" \
    CAPACITY_ENVIRONMENT_MANIFEST="${repo_root}/tests/capacity/examples/environment-manifest.json" \
    CAPACITY_RBAC_SEED_FILE="${test_dir}/rbac-seed.json" CAPACITY_RBAC_TOPOLOGY_FILE="${test_dir}/rbac-topology.json" \
    MOCK_RBAC_SOURCE_MARKER="${test_dir}/rbac-source-drift" MOCK_RBAC_BASE_GIT="${test_dir}/bin/git" \
    "$@" "${dispatcher}" "${target}" "${profile}" alpha
}
for target_profile in 'api readiness' 'namespace watch'; do
  read -r rbac_target rbac_profile <<<"${target_profile}"
  run_rbac_dispatch complete "${rbac_target}" "${rbac_profile}" >/dev/null
  jq -e '.passed == true and .postflightRequired == true and .postflightExitCode == 0 and
    .sourceStateUnchanged == true' \
    "${test_dir}/rbac-evidence/${rbac_target}/${rbac_profile}/alpha/complete/metadata.json" >/dev/null
  before_digest="$(shasum -a 256 "${test_dir}/rbac-evidence/${rbac_target}/${rbac_profile}/alpha/complete/metadata.json")"
  if run_rbac_dispatch complete "${rbac_target}" "${rbac_profile}" >/dev/null 2>&1; then
    echo "RBAC alpha overwrote an existing run directory" >&2
    exit 1
  fi
  [[ "${before_digest}" == "$(shasum -a 256 "${test_dir}/rbac-evidence/${rbac_target}/${rbac_profile}/alpha/complete/metadata.json")" ]]
  if run_rbac_dispatch missing-domain "${rbac_target}" "${rbac_profile}" MOCK_RBAC_SKIP_VERDICT=1 >/dev/null 2>&1; then
    echo "RBAC alpha accepted a skipped domain verifier" >&2
    exit 1
  fi
  jq -e '.passed == false and .postflightExitCode == 2' \
    "${test_dir}/rbac-evidence/${rbac_target}/${rbac_profile}/alpha/missing-domain/metadata.json" >/dev/null
done
if run_rbac_dispatch changed-source api readiness MOCK_RBAC_SOURCE_DRIFT=1 >/dev/null 2>&1; then
  echo "RBAC alpha accepted source drift during the run" >&2
  exit 1
fi
jq -e '.passed == false and .sourceStateUnchanged == false and .sourceStateExitCode == 2' \
  "${test_dir}/rbac-evidence/api/readiness/alpha/changed-source/metadata.json" >/dev/null

PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=test-run \
  "${dispatcher}" namespace validation diagnostic >/dev/null
metadata="${test_dir}/evidence/namespace/validation/diagnostic/test-run/metadata.json"
jq -e '.target == "namespace" and .profile == "validation" and .mode == "diagnostic" and .passed == false and .verifierExitCode == 0' "${metadata}" >/dev/null

if PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=missing-manifests \
  "${dispatcher}" namespace validation alpha >/dev/null 2>&1; then
  echo "alpha non-k6 capacity evidence unexpectedly passed without manifests" >&2
  exit 1
fi

run_capacity validated-run namespace validation alpha >/dev/null
metadata="${test_dir}/evidence/namespace/validation/alpha/validated-run/metadata.json"
jq -e '
  .passed == true and .preflightRequired == true and .preflightExitCode == 0 and
  .verifierRequired == true and .verifierExitCode == 0 and
  (.configDigest | length) == 64 and (.environmentDigest | length) == 64
' "${metadata}" >/dev/null

jq -e \
  --arg corpus_digest "$(shasum -a 256 "${repo_root}/gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json" | awk '{print $1}')" \
  --arg harness_digest "$(shasum -a 256 "${repo_root}/gitstore-api/internal/cataloggrpc/namespace_capacity_test.go" | awk '{print $1}')" '
  .scope == "catalog-service-test-helpers" and .connectedIngestion == false and
  .corpusSha256 == $corpus_digest and .harnessSha256 == $harness_digest and
  (.translationValidCases | length) > 0 and
  (.targetKinds | sort) == ["CategoryTaxonomy","Collection","File","Product","ProductVariant"] and
  .workload.filesPerRequest == 500 and .workload.namespaceManifestsPerRequest == 50 and
  .workload.helperProcesses == 2 and .workload.requestsPerSecond == 10 and
  .workload.workers == 20 and .workload.minimumDurationSeconds == 1800
' "${test_dir}/evidence/namespace/validation/alpha/validated-run/validation-corpus.json" >/dev/null

assert_rejected "alpha capacity evidence unexpectedly accepted a dirty verifier checkout" "${test_dir}/output" \
  run_capacity dirty-verifier namespace validation alpha CAPACITY_TEST_FORCE_DIRTY=1
metadata="${test_dir}/evidence/namespace/validation/alpha/dirty-verifier/metadata.json"
jq -e '.passed == false and .worktreeDirty == true and .sourceStateExitCode == 2' "${metadata}" >/dev/null

if MODE=alpha CAPACITY_TEST_FORCE_DIRTY=1 CAPACITY_TARGET=api CAPACITY_SCENARIO=readiness \
  CAPACITY_RUN_ID=dirty-k6-verifier CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" \
  "${repo_root}/scripts/run-capacity.sh" api-readiness >/dev/null 2>&1; then
  echo "alpha k6 capacity evidence unexpectedly accepted a dirty verifier checkout" >&2
  exit 1
fi
metadata="${test_dir}/evidence/api/readiness/alpha/dirty-k6-verifier/metadata.json"
jq -e '.passed == false and .gitDirty == true and .sourceStateExitCode == 2' "${metadata}" >/dev/null

mkdir -p "${test_dir}/admission-verifier"
jq -n '{metrics:{gitstore_namespace_admitted:{values:{count:10}},checks:{values:{fails:0}},gitstore_namespace_graphql_failed:{values:{rate:0}},gitstore_namespace_visibility_ms:{values:{"p(95)":100,"p(99)":200}}}}' \
  >"${test_dir}/admission-verifier/summary.json"
"${repo_root}/tests/capacity/verifiers/namespace-admission.sh" "${test_dir}/admission-verifier"
jq -e '.passed == true and .admitted == 10 and .visibilityP99Milliseconds == 200' \
  "${test_dir}/admission-verifier/domain-verifier.json" >/dev/null

run_deployed_capacity deployed-run namespace watch alpha MOCK_SAME_START=1 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" >/dev/null
metadata="${test_dir}/evidence/namespace/watch/alpha/deployed-run/metadata.json"
jq -e '.passed == true and .postflightRequired == true and .postflightExitCode == 0 and .datastoreVerifierExitCode == 0' "${metadata}" >/dev/null
jq -e '(.topology.liveApiReplicas | length) == 2 and (.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/preflight-environment.json" >/dev/null
jq -e '(.topology.liveApiReplicas | length) == 2 and (.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/postflight-environment.json" >/dev/null
jq -e 'length == 3 and all(.[]; .memoryLimitBytes == 3221225472 and .smpPerNode == 2 and .scyllaLiveNodes == 3)' \
  "${test_dir}/evidence/namespace/watch/alpha/deployed-run/datastore-before.json" >/dev/null

MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=repository-deployed-run \
  CAPACITY_CONFIG_MANIFEST="${test_dir}/alpha-config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${test_dir}/alpha-environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  REPOSITORY_API_A=http://api-a.internal REPOSITORY_API_B=http://api-b.internal \
  REPOSITORY_API_REPLACEMENT=http://api-a.internal REPOSITORY_REPLACEMENT_TRIGGER_FILE="${test_dir}/repository-replacement-trigger" \
  REPOSITORY_CONTROLLER_A=http://controller-a.internal REPOSITORY_CONTROLLER_B=http://controller-b.internal \
  REPOSITORY_TOKEN=test-token REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS=256 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINERS=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/repository-local-service-containers.json" \
  MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/alpha-scylla-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=1 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=1610612736 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" repository lifecycle alpha >/dev/null
metadata="${test_dir}/evidence/repository/lifecycle/alpha/repository-deployed-run/metadata.json"
jq -e '.passed == true and .postflightRequired == true and .postflightExitCode == 0 and .datastoreVerifierExitCode == 0' "${metadata}" >/dev/null
jq -e '
  (.artifacts.releaseServiceContainers | length) == 3 and
  ([.artifacts.releaseServiceContainers[] | select(.role == "git-service")] | length) == 1 and
  ([.artifacts.releaseServiceContainers[] | select(.role == "api") | .gitServiceURI] | unique | length) == 1
' "${test_dir}/evidence/repository/lifecycle/alpha/repository-deployed-run/preflight-environment.json" >/dev/null
jq -e '(.artifacts.releaseServiceContainers | length) == 3' \
  "${test_dir}/evidence/repository/lifecycle/alpha/repository-deployed-run/postflight-environment.json" >/dev/null

if MOCK_SAME_START=1 PATH="${test_dir}/bin:${PATH}" CAPACITY_EVIDENCE_DIR="${test_dir}/evidence" CAPACITY_RUN_ID=repository-production-undersized \
  CAPACITY_CONFIG_MANIFEST="${test_dir}/alpha-config-manifest.json" \
  CAPACITY_ENVIRONMENT_MANIFEST="${test_dir}/alpha-environment-manifest.json" \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-b.internal \
  REPOSITORY_API_A=http://api-a.internal REPOSITORY_API_B=http://api-b.internal \
  REPOSITORY_API_REPLACEMENT=http://api-a.internal REPOSITORY_REPLACEMENT_TRIGGER_FILE="${test_dir}/repository-production-trigger" \
  REPOSITORY_TOKEN=test-token REPOSITORY_CAPACITY_OVERFLOW_TRANSITIONS=256 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINERS=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/repository-local-service-containers.json" \
  MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/alpha-scylla-containers.json" \
  CAPACITY_API_REPLICAS=2 CAPACITY_API_BUILD=release CAPACITY_GIT_SERVICE_BUILD=release \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=1 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=1610612736 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  "${dispatcher}" repository lifecycle production >/dev/null 2>&1; then
  echo "production Repository lifecycle unexpectedly accepted alpha SMP, overflow, and local image references" >&2
  exit 1
fi
metadata="${test_dir}/evidence/repository/lifecycle/production/repository-production-undersized/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

export MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/scylla-containers.json"

assert_rejected "alpha namespace watch evidence unexpectedly accepted replacement of every API replica" "${test_dir}/output" \
  run_deployed_capacity all-replicas-replaced-run namespace watch alpha MOCK_SAME_START=1 MOCK_REPLACE_ALL=1 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json"
metadata="${test_dir}/evidence/namespace/watch/alpha/all-replicas-replaced-run/metadata.json"
jq -e '.passed == false and .postflightExitCode == 1' "${metadata}" >/dev/null

assert_rejected "alpha namespace watch evidence unexpectedly accepted one live API process twice" "${test_dir}/output" \
  run_deployed_capacity aliased-api-run namespace watch alpha \
  CAPACITY_API_ENDPOINTS=http://api-a.internal,http://api-alias.internal \
  NAMESPACE_WATCH_API_B=http://api-alias.internal

assert_rejected "alpha namespace watch evidence unexpectedly accepted an unverified service image" "${test_dir}/output" \
  run_deployed_capacity unverified-service-run namespace watch alpha \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/unverified-service-containers.json"

cp "${test_dir}/service-containers.json" "${test_dir}/replacement-service-containers.json"
assert_rejected "alpha namespace watch evidence unexpectedly accepted an unverified replacement image" "${test_dir}/output" \
  run_deployed_capacity unverified-replacement-run namespace watch alpha MOCK_SAME_START=1 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/replacement-service-containers.json" \
  MOCK_POSTFLIGHT_SERVICE_CONTAINERS_FILE="${test_dir}/unverified-service-containers.json"
metadata="${test_dir}/evidence/namespace/watch/alpha/unverified-replacement-run/metadata.json"
jq -e '.passed == false and .preflightExitCode == 0 and .verifierExitCode == 0 and .postflightExitCode == 1' "${metadata}" >/dev/null

assert_rejected "alpha namespace watch evidence unexpectedly accepted duplicate service container identities" "${test_dir}/output" \
  run_deployed_capacity duplicate-service-run namespace watch alpha \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/duplicate-service-containers.json"

for mismatch in start instance; do
  assert_rejected "release evidence accepted mismatched container ${mismatch}" "${test_dir}/output" \
    run_deployed_capacity "container-mismatch-${mismatch}" namespace watch alpha \
    CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
    MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" \
    MOCK_CONTAINER_IDENTITY_MISMATCH="${mismatch}"
done

# Diagnostic preambles must not block the membership reader at pipe-buffer sizes.
PATH="${test_dir}/bin:${PATH}" CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  CAPACITY_DATASTORE_EXPECTED_COUNT=3 CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 \
  CAPACITY_DATASTORE_EXPECTED_SMP=2 python3 - "${repo_root}" "${test_dir}" <<'PYTEST'
import os, signal, subprocess, sys
from pathlib import Path
root, output = map(Path, sys.argv[1:])
for padding in [400, 4000]:
    env = os.environ.copy()
    env['MOCK_SCYLLA_STATUS_PADDING_LINES'] = str(padding)
    process = subprocess.Popen([str(root / 'scripts/check-capacity-containers.sh'), 'snapshot', str(output / f'membership-{padding}.json')], env=env, start_new_session=True)
    try:
        status = process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        os.killpg(process.pid, signal.SIGKILL)
        process.wait()
        raise AssertionError('membership reader blocked on diagnostic preamble')
    assert status == 0, 'valid membership with diagnostic preamble was rejected'
PYTEST

if PATH="${test_dir}/bin:${PATH}" CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 \
  CAPACITY_DATASTORE_EXPECTED_COUNT=3 CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 \
  CAPACITY_DATASTORE_EXPECTED_SMP=3 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/wrong-smp.json" >/dev/null 2>&1; then
  echo "runtime Scylla SMP mismatch unexpectedly passed" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_BAD_MEMBERSHIP=1 \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 CAPACITY_DATASTORE_EXPECTED_COUNT=3 \
  CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 CAPACITY_DATASTORE_EXPECTED_SMP=2 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/wrong-membership.json" >/dev/null 2>&1; then
  echo "undersized live Scylla membership unexpectedly passed" >&2
  exit 1
fi
if PATH="${test_dir}/bin:${PATH}" MOCK_SCYLLA_CONTAINERS_FILE="${test_dir}/duplicate-scylla-containers.json" \
  CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-1,scylla-1 CAPACITY_DATASTORE_EXPECTED_COUNT=3 \
  CAPACITY_DATASTORE_EXPECTED_MEMORY_BYTES=3221225472 CAPACITY_DATASTORE_EXPECTED_SMP=2 \
  "${repo_root}/scripts/check-capacity-containers.sh" snapshot "${test_dir}/duplicate-containers.json" >/dev/null 2>&1; then
  echo "duplicate datastore container identities unexpectedly passed" >&2
  exit 1
fi

assert_rejected "alpha namespace watch evidence unexpectedly skipped rolling replacement" "${test_dir}/output" \
  run_deployed_capacity skipped-replacement namespace watch alpha NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT=1

assert_rejected "failed non-k6 verifier unexpectedly produced passing evidence" "${test_dir}/output" \
  run_capacity failed-verifier namespace validation alpha FAIL_MAKE=1
metadata="${test_dir}/evidence/namespace/validation/alpha/failed-verifier/metadata.json"
jq -e '.passed == false and .verifierExitCode == 1' "${metadata}" >/dev/null

if run_capacity unverified-dataset scylla soak production \
  CAPACITY_SCYLLA_NODES=3 CAPACITY_SCYLLA_SMP=2 CAPACITY_SCYLLA_MEMORY_BYTES_PER_NODE=3221225472 \
  CAPACITY_SCYLLA_AUTH_MODE=local-unauthenticated CAPACITY_DATASTORE_CONTAINERS=scylla-1,scylla-2,scylla-3 >/dev/null 2>&1; then
  echo "Scylla production evidence unexpectedly passed without observed dataset verification" >&2
  exit 1
fi
metadata="${test_dir}/evidence/scylla/soak/production/unverified-dataset/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

assert_rejected "namespace recovery unexpectedly accepted an undersized overflow probe" "${test_dir}/output" \
  run_deployed_capacity undersized-overflow namespace recovery alpha MOCK_SAME_START=1 \
  CAPACITY_API_CONTAINERS=api-1,api-2 CAPACITY_GIT_SERVICE_CONTAINER=git-1 \
  MOCK_SERVICE_CONTAINERS_FILE="${test_dir}/service-containers.json" NAMESPACE_WATCH_TOKEN=test-token \
  NAMESPACE_WATCH_OVERFLOW_TRANSITIONS=1
metadata="${test_dir}/evidence/namespace/recovery/alpha/undersized-overflow/metadata.json"
jq -e '.passed == false and .preflightExitCode == 2' "${metadata}" >/dev/null

assert_rejected "invalid capacity observability mode unexpectedly succeeded" "${test_dir}/output" \
  env PATH="${test_dir}/bin:${PATH}" CAPACITY_OBSERVABILITY=invalid "${dispatcher}" namespace validation diagnostic

echo "capacity dispatcher tests passed"
