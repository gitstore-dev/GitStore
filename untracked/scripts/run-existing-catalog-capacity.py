#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

"""Bounded diagnostic against an explicitly owned, already populated catalog."""

import concurrent.futures
from email.utils import parsedate_to_datetime
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


ROOT = Path(__file__).resolve().parent.parent
if sys.flags.optimize:
    raise RuntimeError("capacity safety checks require Python without optimization")
GIB = 1024**3
PROM_QUERY = ('{job=~"gitstore-(api|controller|git|scylla)-capacity",'
              '__name__=~"up|process_.*|go_memstats_heap_(alloc|inuse)_bytes|'
              'gitstore_.*(queue_depth|reconcile_total|reconcile_duration_seconds.*|'
              'graphql_http_request_duration_seconds.*|git_http_stage_duration_seconds.*|'
              'datastore_operation_duration_seconds.*|datastore_operation_errors_total|'
              'resource_watch_.*|authz_.*)|grpc_server_(handled_total|handling_seconds_.*)|'
              'scylla_reactor_utilization|scylla_memory_(allocated|free)_memory|'
              'scylla_storage_proxy_coordinator_(read|write)_latency.*"}')
PRODUCT_FIELDS = ("metadata{name uid generation creationTimestamp revision} "
                  "spec{title categoryRef{name}} status{observedGeneration "
                  "conditions{type status observedGeneration} resolved{category{name uid path}}}")


class HealthCheckError(RuntimeError):
    pass


def health_failure_summary(value):
    if not isinstance(value, dict):
        return {}
    summary = {key: value[key] for key in ("ready", "credentialReady") if type(value.get(key)) is bool}
    if value.get("status") in ("ok", "healthy", "degraded", "unhealthy"):
        summary["status"] = value["status"]
    kinds = value.get("kinds")
    if isinstance(kinds, dict):
        summary["kinds"] = {}
        for kind in ("Namespace", "Repository", "CategoryTaxonomy", "Product"):
            state = kinds.get(kind)
            if not isinstance(state, dict):
                continue
            safe = {key: state[key] for key in ("stalled", "registered", "recovering")
                    if type(state.get(key)) is bool}
            safe.update({key: state[key] for key in ("activeWorkers", "queueDepth", "poisonItems")
                         if type(state.get(key)) is int and 0 <= state[key] < 2**63})
            summary["kinds"][kind] = safe
    return summary


def atomic_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, sort_keys=True, indent=2))
    temporary.replace(path)


def bounded_json(url, body=None, token=None, timeout=10, limit=32 * 1024**2):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None,
                                     headers=headers)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read(limit + 1)
    if len(raw) > limit:
        raise RuntimeError("response exceeded capture bound")
    return json.loads(raw)


def load_plan(path):
    plan = json.loads(path.read_text())
    assert plan["owned"] is True
    assert isinstance(plan.get("preparationOnly", False), bool)
    assert isinstance(plan.get("soakOnly", False), bool)
    assert not (plan.get("preparationOnly") and plan.get("soakOnly")), "conflicting phase selections"
    assert len(plan["controllerHealth"]) == 2
    for endpoint in [plan[k] for k in ("apiA", "apiB", "gitRemote", "prometheus")] + plan["controllerHealth"]:
        url = urllib.parse.urlsplit(endpoint)
        assert url.scheme == "http" and url.hostname in ("127.0.0.1", "localhost")
        assert not url.username and not url.password and not url.query and not url.fragment
    assert plan["apiA"] != plan["apiB"]
    assert plan["gitRemote"].endswith("/" + plan["namespace"] + "/" + plan["repository"] + ".git")
    assert all(re.fullmatch(r"[a-z0-9][a-z0-9-]*", plan[k])
               for k in ("namespace", "repository", "prefix", "runID"))
    assert len(plan["runtime"]) == 8
    assert len({r["id"] for r in plan["runtime"]}) == 8
    assert all(re.fullmatch(r"[0-9a-f]{64}", r["id"]) for r in plan["runtime"])
    assert sum("capacity-git-service" in r["name"] for r in plan["runtime"]) == 1
    for key in ("tokenFile", "seedClone", "originalClone", "driverBinary", "output"):
        assert Path(plan[key]).is_absolute()
    assert 0 < plan["hardStopUnix"] - time.time() <= 13 * 3600
    assert plan["launchByUnix"] < plan["hardStopUnix"]
    assert plan["baselineSeconds"] == 1800 and plan["rampStepSeconds"] == 900
    assert plan["confirmationSeconds"] == 1800 and plan["soakSeconds"] == 21600
    assert len(plan["readRates"]) == len(plan["pushRates"]) == 4
    assert all(0 < r <= 100 for r in plan["readRates"])
    assert all(0 < r <= 20 for r in plan["pushRates"])
    return plan


def stop_owned(plan):
    rows = json.loads(subprocess.check_output(
        ["docker", "inspect", *(r["name"] for r in plan["runtime"])], timeout=20))
    expected = {r["id"] for r in plan["runtime"]}
    assert {r["Id"] for r in rows} == expected, "refusing to stop replaced containers"
    groups = (("controller-manager", 60), ("-api-", 60),
              ("capacity-git-service", 120), ("-scylla-", 300))
    stopped = []
    for marker, grace in groups:
        ids = [r["Id"] for r in rows if marker in r["Name"] and r["State"]["Running"]]
        if ids:
            subprocess.run(["docker", "stop", "--time", str(grace), *ids],
                           check=True, timeout=grace + 45, stdout=subprocess.DEVNULL)
            stopped.extend(ids)
    final = json.loads(subprocess.check_output(["docker", "inspect", *expected], timeout=20))
    assert all(not r["State"]["Running"] for r in final)
    if "collector" in plan:
        collector = json.loads(subprocess.check_output(
            ["docker", "inspect", plan["collector"]["name"]], timeout=15))[0]
        assert collector["Id"] == plan["collector"]["id"], "refusing to stop replaced collector"
        if collector["State"]["Running"]:
            subprocess.run(["docker", "stop", "--time", "30", collector["Id"]],
                           check=True, timeout=60, stdout=subprocess.DEVNULL)
        stopped.append(collector["Id"])
    return stopped


class Benchmark:
    def __init__(self, plan):
        self.plan = plan
        self.output = Path(plan["output"])
        self.output.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.stop = threading.Event()
        self.lock = threading.Lock()
        self.phase = "preflight"
        self.child = None
        self.expected = {f'{plan["prefix"]}-hot-{i:03}': f'{plan["prefix"]}-hot' for i in range(256)}
        self.expected[f'{plan["prefix"]}-cold-000'] = f'{plan["prefix"]}-cold'
        self.products = {}
        self.results = []
        self.errors = []
        self.bytes_written = 0
        self.events = (self.output / "client-events.jsonl").open("x", buffering=1)
        self.started = None
        self.finish = None
        self.local = threading.local()
        atomic_json(self.output / "status.json", {"pid": os.getpid(), "phase": self.phase,
                                                "heartbeatUnix": time.time()})

    def event(self, operation, **values):
        row = dict(operation=operation, unixNS=time.time_ns(), phase=self.phase, **values)
        line = json.dumps(row, separators=(",", ":")) + "\n"
        with self.lock:
            self.bytes_written += len(line)
            if self.bytes_written > GIB:
                self.stop.set()
                raise RuntimeError("client evidence exceeded 1 GiB bound")
            self.events.write(line)

    def check(self):
        if self.stop.is_set() or (self.output / "STOP").exists():
            raise RuntimeError("stop requested")
        if time.time() >= self.plan["hardStopUnix"]:
            self.stop.set()
            raise RuntimeError("morning offered-load cutoff")
        if shutil.disk_usage(self.output).free <= 12 * GIB:
            self.stop.set()
            raise RuntimeError("disk safety reserve reached before the 10 GiB floor")

    def graphql(self, endpoint, query, variables=None, retry_rate_limit=False):
        assert not retry_rate_limit or query.lstrip().startswith(("{", "query"))
        token = Path(self.plan["tokenFile"]).read_text().strip()
        deadline = getattr(self.local, "deadline", time.monotonic() + 10)
        backoff = 1
        while True:
            if retry_rate_limit and self.phase != "finalizing":
                self.check()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("operation deadline")
            try:
                value = bounded_json(endpoint + "/graphql", {"query": query, "variables": variables or {}},
                                     token, timeout=min(10, remaining))
                break
            except urllib.error.HTTPError as error:
                error.close()
                if not retry_rate_limit or error.code != 429:
                    raise
                retry_after = error.headers.get("Retry-After", "") if error.headers else ""
                try:
                    delay = float(retry_after)
                except ValueError:
                    try:
                        delay = parsedate_to_datetime(retry_after).timestamp() - time.time()
                    except (ValueError, TypeError, OverflowError):
                        delay = backoff
                if not math.isfinite(delay) or delay < 0:
                    delay = backoff
                delay = max(1, delay)
                if delay >= deadline - time.monotonic():
                    raise TimeoutError("rate-limit recovery exceeded operation deadline") from error
                self.event("control-rate-limit", replica=endpoint, httpStatus=429, retryAfterSeconds=delay)
                if self.phase == "finalizing":
                    time.sleep(delay)
                else:
                    if self.stop.wait(delay):
                        raise RuntimeError("stop requested")
                backoff = min(5, backoff * 2)
        if value.get("errors"):
            codes = sorted({str(e.get("extensions", {}).get("code", "UNCLASSIFIED"))
                            for e in value["errors"]})
            raise RuntimeError("GraphQL " + ",".join(codes))
        return value["data"]

    def selector(self, name):
        return "{namespacePath:{namespace:" + json.dumps(self.plan["namespace"]) + ",name:" + json.dumps(name) + "}}"

    def git(self, *args, original=False):
        env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        env.update(GIT_CONFIG_COUNT="1", GIT_CONFIG_KEY_0="http.extraHeader",
                   GIT_CONFIG_VALUE_0="Authorization: Bearer " + Path(self.plan["tokenFile"]).read_text().strip(),
                   GIT_TERMINAL_PROMPT="0", GIT_CONFIG_NOSYSTEM="1", GIT_CONFIG_GLOBAL=os.devnull)
        directory = self.plan["originalClone" if original else "seedClone"]
        result = subprocess.run(["git", "--no-optional-locks", "-c", "gc.auto=0", "-C", directory, *args],
                                env=env, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=120)
        if result.returncode:
            raise RuntimeError("bounded owned Git preparation failed")
        return result.stdout.decode().strip()

    def walk(self, endpoint, suffix, backward=False, retry_rate_limit=False):
        name = self.plan["prefix"] + "-" + suffix
        members, cursors, cursor = {}, set(), None
        for _ in range(5):
            continuation = (",before:" if backward else ",after:") + json.dumps(cursor) if cursor else ""
            size = "last:100" if backward else "first:100"
            started = time.monotonic_ns()
            value = self.graphql(endpoint, "{category(by:" + self.selector(name) + "){products(" +
                                 size + continuation + "){edges{node{metadata{name uid}}} "
                                 "pageInfo{hasNextPage hasPreviousPage startCursor endCursor}}}}",
                                 retry_rate_limit=retry_rate_limit)
            self.event("membership-page", elapsedNS=time.monotonic_ns() - started,
                       replica=endpoint, category=suffix, backward=backward)
            page = value["category"]["products"]
            for edge in page["edges"]:
                metadata = edge["node"]["metadata"]
                product_name = metadata["name"]
                if product_name not in self.expected or product_name in members:
                    raise ValueError("unexpected or duplicated fixture member")
                if self.products and metadata["uid"] != self.products[product_name]["metadata"]["uid"]:
                    raise ValueError("fixture Product identity changed")
                members[product_name] = metadata["uid"]
            info = page["pageInfo"]
            if not info["hasPreviousPage" if backward else "hasNextPage"]:
                return members
            cursor = info["startCursor" if backward else "endCursor"]
            if not cursor or cursor in cursors:
                raise ValueError("invalid or repeated membership cursor")
            cursors.add(cursor)
        raise ValueError("fixture page budget exceeded")

    def oracle(self, allow_wait=False):
        deadline = time.monotonic() + (120 if allow_wait else 20)
        self.local.deadline = deadline
        while True:
            try:
                snapshots = []
                for endpoint in (self.plan["apiA"], self.plan["apiB"]):
                    products = {}
                    names = sorted(self.expected)
                    for start in range(0, len(names), 16):
                        batch = names[start:start + 16]
                        query = "{" + " ".join(
                            f"p{i}:product(by:{self.selector(name)}){{{PRODUCT_FIELDS}}}"
                            for i, name in enumerate(batch)) + "}"
                        data = self.graphql(endpoint, query, retry_rate_limit=True)
                        products.update({name: data[f"p{i}"] for i, name in enumerate(batch)})
                    for name, value in products.items():
                        assert value and value["spec"]["categoryRef"]["name"] == self.expected[name]
                        status = value["status"]
                        generation = value["metadata"]["generation"]
                        assert status["observedGeneration"] == generation
                        assert any(c["type"] == "Ready" and c["status"] == "TRUE" and
                                   c.get("observedGeneration") == generation for c in status["conditions"])
                        assert status["resolved"]["category"]["name"] == self.expected[name]
                        assert status["resolved"]["category"]["path"] == [self.expected[name]]
                        if self.products:
                            old = self.products[name]
                            assert value["metadata"]["uid"] == old["metadata"]["uid"]
                            assert value["metadata"]["creationTimestamp"] == old["metadata"]["creationTimestamp"]
                    for suffix in ("hot", "cold", "empty"):
                        category = self.plan["prefix"] + "-" + suffix
                        intended = {n for n, parent in self.expected.items() if parent == category}
                        for backward in (False, True):
                            assert set(self.walk(endpoint, suffix, backward, retry_rate_limit=True)) == intended
                        data = self.graphql(endpoint, "{category(by:" + self.selector(category) +
                                            "){metadata{generation} status{observedGeneration "
                                            "conditions{type status} resolved{productCount}}}}",
                                            retry_rate_limit=True)["category"]
                        assert data["status"]["observedGeneration"] == data["metadata"]["generation"]
                        assert data["status"]["resolved"]["productCount"] == len(intended)
                        assert any(c["type"] == "Ready" and c["status"] == "TRUE"
                                   for c in data["status"]["conditions"])
                    snapshots.append(products)
                assert snapshots[0] == snapshots[1]
                self.products = snapshots[0]
                self.event("correctness-oracle", products=len(self.products),
                           hot=sum(c.endswith("-hot") for c in self.expected.values()))
                del self.local.deadline
                return
            except (AssertionError, RuntimeError):
                if not allow_wait or time.monotonic() >= deadline or (
                        self.phase != "finalizing" and self.stop.is_set()):
                    raise
                time.sleep(1)

    def churn(self, restore=False):
        name = self.plan["prefix"] + "-hot-000"
        self.local.deadline = time.monotonic() + 150
        if restore:
            observed = self.graphql(self.plan["apiA"], "{product(by:" + self.selector(name) +
                                    "){" + PRODUCT_FIELDS + "}}", retry_rate_limit=True)["product"]
            assert observed["metadata"]["uid"] == self.products[name]["metadata"]["uid"]
            assert observed["spec"]["categoryRef"]["name"] in (
                self.plan["prefix"] + "-hot", self.plan["prefix"] + "-cold")
            self.expected[name] = observed["spec"]["categoryRef"]["name"]
            self.products[name] = observed
        previous = self.products[name]
        category = self.plan["prefix"] + ("-hot" if restore or self.expected[name].endswith("-cold") else "-cold")
        if category == self.expected[name]:
            del self.local.deadline
            return
        started = time.monotonic_ns()
        result = self.graphql(self.plan["apiA"], "mutation($input:UpdateProductInput!){"
                              "updateProduct(input:$input){product{" + PRODUCT_FIELDS + "}}}",
                              {"input": {"metadata": {"name": name, "namespace": self.plan["namespace"]},
                                         "spec": {"title": previous["spec"]["title"], "categoryRef": {"name": category}}}})
        record = result["updateProduct"]["product"]
        assert record["metadata"]["uid"] == previous["metadata"]["uid"]
        assert record["metadata"]["generation"] == previous["metadata"]["generation"] + 1
        assert record["spec"]["title"] == previous["spec"]["title"]
        assert record["spec"]["categoryRef"]["name"] == category
        self.expected[name] = category
        self.oracle(allow_wait=True)
        revision = record["metadata"]["revision"].split("@sha1:")[-1]
        assert re.fullmatch(r"[0-9a-f]{40,64}", revision)
        self.git("fetch", "--quiet", "origin", "main")
        self.git("merge-base", "--is-ancestor", revision, "origin/main")
        self.event("membership-change", elapsedNS=time.monotonic_ns() - started,
                   product=name, category=category, generation=record["metadata"]["generation"], revision=revision)

    def runtime(self):
        for path, expected_sha in self.plan.get("configurationFilesSHA256", {}).items():
            assert hashlib.sha256(Path(path).read_bytes()).hexdigest() == expected_sha, "mounted configuration changed"
        for relative, expected_sha in self.plan["sourceSHA256"].items():
            assert hashlib.sha256((ROOT / relative).read_bytes()).hexdigest() == expected_sha, "runner source changed"
        rows = json.loads(subprocess.check_output(
            ["docker", "inspect", *(r["name"] for r in self.plan["runtime"])], timeout=15))
        expected = {r["id"]: r for r in self.plan["runtime"]}
        assert {r["Id"] for r in rows} == set(expected)
        for row in rows:
            prior = expected[row["Id"]]
            assert row["State"]["Running"] and not row["State"]["OOMKilled"] and row["RestartCount"] == 0
            assert row["Image"] == prior["image"]
            digest = hashlib.sha256(json.dumps(row["Config"], sort_keys=True).encode()).hexdigest()
            assert digest == prior["configurationSHA256"]
        return rows

    def scrape_health(self):
        for url in (self.plan["apiA"] + "/health", self.plan["apiB"] + "/health",
                    *self.plan["controllerHealth"]):
            value = None
            try:
                value = bounded_json(url, timeout=5)
                if "kinds" in value:
                    assert value["ready"]
                    assert all(not k["poisonItems"] and not k["stalled"] for k in value["kinds"].values())
                else:
                    assert value["status"] in ("healthy", "ok")
            except Exception as error:
                failure = {"endpoint": url, "failureClass": type(error).__name__}
                if isinstance(error, urllib.error.HTTPError):
                    failure["httpStatus"] = error.code
                    try:
                        raw = error.read(64 * 1024 + 1)
                        failure["bodyTruncated"] = len(raw) > 64 * 1024
                        if not failure["bodyTruncated"]:
                            value = json.loads(raw)
                    except Exception as body_error:
                        failure["bodyFailureClass"] = type(body_error).__name__
                    finally:
                        error.close()
                failure["health"] = health_failure_summary(value)
                self.event("health-failure", **failure)
                status = f", HTTP {error.code}" if isinstance(error, urllib.error.HTTPError) else ""
                raise HealthCheckError(f"health check failed: {url} ({type(error).__name__}{status})") from None
            self.event("health", endpoint=url, health=value)

    def export_metrics(self, start, end):
        query = urllib.parse.urlencode({"query": PROM_QUERY, "start": start, "end": end, "step": "5s"})
        data = bounded_json(self.plan["prometheus"] + "/api/v1/query_range?" + query, timeout=60)
        assert data["status"] == "success" and data["data"]["result"]
        atomic_json(self.output / f"prometheus-{int(start)}-{int(end)}.json", data)

    def sample(self):
        exported = time.time()
        try:
            with (self.output / "resources.jsonl").open("x", buffering=1) as stream:
                while not self.stop.is_set():
                    self.check()
                    self.runtime()
                    self.scrape_health()
                    value = {"unixNS": time.time_ns(), "freeBytes": shutil.disk_usage(self.output).free,
                             "phase": self.phase}
                    value["dockerStats"] = subprocess.check_output(
                        ["docker", "stats", "--no-stream", "--format", "{{json .}}",
                         *(r["name"] for r in self.plan["runtime"])], timeout=15, text=True).splitlines()
                    value["hostMemory"] = subprocess.check_output(["vm_stat"], timeout=5, text=True)
                    value["hostSwap"] = subprocess.check_output(["sysctl", "-n", "vm.swapusage"], timeout=5, text=True)
                    value["hostIO"] = subprocess.check_output(["iostat", "-d", "-c", "2", "-w", "1"],
                                                              timeout=5, text=True)
                    stream.write(json.dumps(value) + "\n")
                    atomic_json(self.output / "status.json",
                                {"pid": os.getpid(), "phase": self.phase, "startedUnix": self.started,
                                 "heartbeatUnix": time.time(), "hardStopUnix": self.plan["hardStopUnix"],
                                 "childPID": self.child.pid if self.child else None,
                                 "freeBytes": value["freeBytes"], "completedPhases": len(self.results)})
                    if time.time() - exported >= 900:
                        self.export_metrics(exported, time.time())
                        exported = time.time()
                    self.stop.wait(15)
        except Exception as error:
            detail = str(error) if isinstance(error, HealthCheckError) else type(error).__name__
            self.errors.append("telemetry/safety: " + detail)
            self.stop.set()
        finally:
            try:
                self.export_metrics(exported, time.time())
            except Exception as error:
                self.errors.append("final metrics: " + type(error).__name__)

    def push_start(self, name, duration, rate):
        self.git("pull", "--ff-only", "origin", "main")
        directory = self.output / name
        directory.mkdir(mode=0o700)
        environment = dict(os.environ)
        environment.update(CAPACITY_PUSH_DRIVER="1", CAPACITY_PUSH_REMOTE=self.plan["gitRemote"],
                           CAPACITY_PUSH_TOKEN_FILE=self.plan["tokenFile"],
                           CAPACITY_PUSH_SEED_CLONE=self.plan["seedClone"],
                           CAPACITY_PUSH_NAMESPACE=self.plan["namespace"],
                           CAPACITY_PUSH_REPOSITORY=self.plan["repository"], CAPACITY_PUSH_KIND="category",
                           CAPACITY_PUSH_DURATION=str(duration) + "s", CAPACITY_PUSH_INTERVAL=str(1 / rate) + "s",
                           CAPACITY_PUSH_WORKERS="4", CAPACITY_PUSH_EVIDENCE=str(directory / "push-evidence.json"),
                           CAPACITY_RUN_ID=self.plan["runID"] + "-" + name, TMPDIR=str(directory))
        log = (directory / "driver.log").open("x")
        process = subprocess.Popen([self.plan["driverBinary"], "-test.run=^TestCapacityGitPushDriver$",
                                    "-test.v", f"-test.timeout={duration + 600}s"],
                                   env=environment, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        self.child = process
        return process, log, directory

    def phase_run(self, name, duration, read_rate=0, push_rate=0, churn=False):
        self.check()
        self.phase = name
        assert time.time() + duration + (600 if push_rate else 0) < self.plan["hardStopUnix"], "phase cannot fit before cutoff"
        process, log, directory = self.push_start(name, duration, push_rate) if push_rate else (None, None, None)
        begin = time.monotonic()
        read_duration = duration
        next_offer, next_change = begin, begin + 60
        counters = {"scheduled": 0, "offered": 0, "completed": 0, "failed": 0, "dropped": 0, "missed": 0}
        latencies, pending = [], set()
        failure_types = {}
        changer = concurrent.futures.ThreadPoolExecutor(max_workers=1)
        change = None
        push_active = False
        next_progress = begin

        def read(sequence):
            started = time.monotonic_ns()
            endpoint = self.plan["apiA" if sequence % 2 == 0 else "apiB"]
            try:
                self.walk(endpoint, ("hot", "cold", "empty")[sequence % 3], (sequence // 3) % 2 == 1)
                return time.monotonic_ns() - started, None
            except urllib.error.HTTPError as error:
                error.close()
                return time.monotonic_ns() - started, f"HTTP{error.code}"
            except Exception as error:
                return time.monotonic_ns() - started, type(error).__name__

        def collect(done):
            for future in done:
                elapsed, error = future.result()
                counters["completed"] += 1
                if error:
                    counters["failed"] += 1
                    failure_types[error] = failure_types.get(error, 0) + 1
                    if error == "ValueError":
                        self.stop.set()
                else:
                    latencies.append(elapsed)
                self.event("membership-walk", elapsedNS=elapsed, failureClass=error)

        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
                while True:
                    now = time.monotonic()
                    if now - begin >= read_duration:
                        if process is not None and process.poll() is None:
                            read_duration += 1
                            assert read_duration <= duration + 600, "bounded push preparation/drain exceeded"
                        else:
                            break
                    self.check()
                    if process is not None and process.poll() is not None:
                        assert (directory / "push-evidence.json").exists(), "push client failed before evidence"
                    if process is not None and not push_active and now >= next_progress:
                        next_progress = now + 1
                        with (directory / "driver.log").open("rb") as progress:
                            progress.seek(max(0, progress.seek(0, os.SEEK_END) - 8192))
                            push_active = b"capacity-push-offer " in progress.read()
                        if push_active:
                            next_change = time.monotonic() + 60
                    done = {f for f in pending if f.done()}
                    pending -= done
                    collect(done)
                    now = time.monotonic()
                    if read_rate and now >= next_offer and now < begin + read_duration and (
                            counters["scheduled"] < math.ceil(read_rate * read_duration)):
                        sequence = counters["scheduled"]
                        counters["scheduled"] += 1
                        if now - next_offer >= 1 / read_rate:
                            counters["missed"] += 1
                        elif len(pending) >= 16:
                            counters["dropped"] += 1
                        else:
                            counters["offered"] += 1
                            pending.add(pool.submit(read, sequence))
                        next_offer = begin + counters["scheduled"] / read_rate
                    if churn and push_active and now >= next_change:
                        if change is None or change.done():
                            if change:
                                change.result()
                            change = changer.submit(self.churn)
                        else:
                            self.event("membership-change-busy")
                        next_change = now + 60
                    if not read_rate:
                        self.stop.wait(min(1, max(0, begin + duration - now)))
                    else:
                        self.stop.wait(min(0.005, max(0, next_offer - time.monotonic())))
                done, unfinished = concurrent.futures.wait(pending, timeout=30)
                collect(done)
                assert not unfinished, "read drain exceeded 30 seconds"
            planned = math.ceil(read_rate * read_duration)
            counters["missed"] += planned - counters["scheduled"]
            counters["scheduled"] = planned
            if process is not None:
                while process.poll() is None:
                    self.check()
                    assert time.monotonic() - begin < duration + 600, "bounded push preparation/drain exceeded"
                    time.sleep(1)
            self.child = None
            push = json.loads((directory / "push-evidence.json").read_text()) if directory else None
            latencies.sort()
            percentiles = {label: latencies[max(0, math.ceil(len(latencies) * q) - 1)] / 1e6
                           for label, q in (("p50", .5), ("p95", .95), ("p99", .99))} if latencies else {}
            row = {"phase": name, "durationSeconds": duration, "elapsedSeconds": time.monotonic() - begin,
                   "readOfferedDurationSeconds": read_duration,
                   "readRate": read_rate, "pushRate": push_rate, "reads": counters,
                   "readWalkLatencyMS": percentiles, "readFailureClasses": failure_types,
                   "pushes": push, "driverExitCode": process.returncode if process is not None else None,
                   "correctnessVerified": False, "performanceAcceptanceClaimed": False}
            self.results.append(row)
            atomic_json(self.output / (name + "-result.json"), row)
            try:
                if change:
                    change.result(timeout=300)
                if push:
                    assert not push["cancelled"] and not push["drainTimedOut"]
                self.churn(restore=True)
                self.oracle(allow_wait=True)
                row["correctnessVerified"] = True
            except Exception as error:
                row["validationError"] = f"HTTP{error.code}" if isinstance(
                    error, urllib.error.HTTPError) else type(error).__name__
                if isinstance(error, urllib.error.HTTPError):
                    error.close()
                raise
            finally:
                row["elapsedSeconds"] = time.monotonic() - begin
                atomic_json(self.output / (name + "-result.json"), row)
            return row
        finally:
            changer.shutdown(wait=True, cancel_futures=True)
            if process is not None and process.poll() != 0:
                try:
                    os.killpg(process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    process.wait(timeout=45)
                except subprocess.TimeoutExpired:
                    pass
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                if process.poll() is None:
                    process.wait(timeout=10)
            self.child = None
            if log:
                log.close()

    def preflight(self):
        assert time.time() < self.plan["launchByUnix"]
        self.runtime()
        self.scrape_health()
        self.check()
        binary_sha = hashlib.sha256(Path(self.plan["driverBinary"]).read_bytes()).hexdigest()
        assert binary_sha == self.plan["driverSHA256"]
        assert self.git("rev-parse", "HEAD", original=True) == self.plan["originalHead"]
        repository_query = "{repository(by:" + self.selector(self.plan["repository"]) + "){metadata{uid}}}"
        for endpoint in (self.plan["apiA"], self.plan["apiB"]):
            assert self.graphql(endpoint, repository_query)["repository"]["metadata"]["uid"] == self.plan["repositoryUID"]
        targets = bounded_json(self.plan["prometheus"] + "/api/v1/targets")["data"]["activeTargets"]
        core = [t for t in targets if t["labels"].get("job") in (
            "gitstore-api-capacity", "gitstore-controller-capacity", "gitstore-git-capacity", "gitstore-scylla-capacity")]
        assert len(core) == 8 and all(t["health"] == "up" for t in core)
        self.oracle()
        atomic_json(self.output / "initial-products.json", self.products)
        self.churn()
        self.churn(restore=True)
        self.baseline_fence()
        assert time.time() < self.plan["launchByUnix"]
        self.export_metrics(time.time() - 30, time.time())

    def baseline_fence(self):
        if "originalIndexSHA256" in self.plan:
            assert self.git("rev-parse", "HEAD", original=True) == self.plan["originalHead"]
            assert hashlib.sha256((Path(self.plan["originalClone"]) / ".git/index").read_bytes()).hexdigest() == (
                self.plan["originalIndexSHA256"]), "protected original index changed"
        self.git("fetch", "--quiet", "origin", "main")
        changed = self.git("diff", "--name-status", self.plan["originalHead"], "origin/main", "--", "products")
        allowed = {"products/" + name + ".md" for name in self.expected}
        seen = set()
        for line in changed.splitlines():
            action, path = line.split("\t")
            assert action in ("A", "M") and path in allowed, "baseline Product tree changed"
            seen.add(path)
        assert seen == allowed, "owned fixture missing from baseline comparison"
        self.event("baseline-product-fence", originalHead=self.plan["originalHead"],
                   baselineProducts=self.plan["baselineProducts"], allowedOwnedFiles=len(seen))

    def run(self):
        preparation_only = self.plan.get("preparationOnly", False)
        soak_only = self.plan.get("soakOnly", False)
        outcome = {"complete": False, "preparationOnly": preparation_only, "soakOnly": soak_only,
                   "performanceAcceptanceClaimed": False, "plan": self.plan}
        sampler = None
        try:
            self.preflight()
            self.started = time.time()
            atomic_json(self.output / "started.json", {"startedUnix": self.started, "pid": os.getpid()})
            sampler = threading.Thread(target=self.sample)
            sampler.start()
            if preparation_only or soak_only:
                read_rate, push_rate = self.plan["readRates"][0], self.plan["pushRates"][0]
                if preparation_only:
                    trial = self.phase_run("preparation", self.plan["rampStepSeconds"], read_rate=read_rate,
                                           push_rate=push_rate, churn=True)
                    assert not any(trial["reads"][k] for k in ("failed", "dropped", "missed")), "preparation reads failed"
                    assert not any(trial["pushes"][k] for k in ("failures", "dropped", "missedSchedules")), "preparation pushes failed"
            else:
                self.phase_run("baseline", self.plan["baselineSeconds"])
                read_rate = None
                clean_reads = []
                for i, rate in enumerate(self.plan["readRates"]):
                    row = self.phase_run(f"read-{i}", self.plan["rampStepSeconds"], read_rate=rate)
                    if not any(row["reads"][k] for k in ("failed", "dropped", "missed")):
                        read_rate = rate
                        clean_reads.append(rate)
                assert read_rate is not None, "no complete read setting for soak"
                push_rate = None
                for i, rate in enumerate(self.plan["pushRates"]):
                    row = self.phase_run(f"mixed-{i}", self.plan["rampStepSeconds"], read_rate=read_rate,
                                         push_rate=rate, churn=True)
                    evidence = row["pushes"]
                    if not any(evidence[k] for k in ("failures", "dropped", "missedSchedules")) and not any(
                            row["reads"][k] for k in ("failed", "dropped", "missed")):
                        push_rate = rate
                    elif any(row["reads"][k] for k in ("failed", "dropped", "missed")):
                        lower = [r for r in clean_reads if r < read_rate]
                        if lower:
                            read_rate = max(lower)
                assert push_rate is not None, "no complete mixed setting for continuous soak"
            if not soak_only:
                confirmation = self.phase_run("confirmation", self.plan["confirmationSeconds"], read_rate=read_rate,
                                              push_rate=push_rate, churn=True)
                assert not any(confirmation["reads"][k] for k in ("failed", "dropped", "missed")), "read setting did not confirm"
                assert not any(confirmation["pushes"][k] for k in ("failures", "dropped", "missedSchedules")), "push setting did not confirm"
            if not preparation_only:
                soak = self.phase_run("soak", self.plan["soakSeconds"], read_rate=read_rate, push_rate=push_rate, churn=True)
                reads, pushes = soak["reads"], soak["pushes"]
                assert not any(reads[k] for k in ("failed", "dropped", "missed")), "soak reads failed"
                assert not any(pushes[k] for k in ("failures", "dropped", "missedSchedules",
                                                  "cancelled", "drainTimedOut")), "soak pushes failed"
                assert soak["driverExitCode"] == 0, "soak push client failed"
                assert soak["readOfferedDurationSeconds"] >= self.plan["soakSeconds"], "short soak read window"
                assert reads["scheduled"] == reads["offered"] == reads["completed"] == math.ceil(
                    read_rate * soak["readOfferedDurationSeconds"]), "incomplete soak reads"
                assert pushes["offeredDurationNs"] >= self.plan["soakSeconds"] * 10**9, "short soak push window"
                assert pushes["scheduled"] == pushes["offered"] == pushes["pushes"] == math.ceil(
                    push_rate * self.plan["soakSeconds"]), "incomplete soak pushes"
            outcome["complete"] = True
        except Exception as error:
            self.errors.append("benchmark: " + type(error).__name__ + ": " + str(error)[:200])
        finally:
            self.phase = "finalizing"
            try:
                if self.products:
                    self.churn(restore=True)
                    self.oracle(allow_wait=True)
                    self.baseline_fence()
                    atomic_json(self.output / "final-products.json", self.products)
            except Exception as error:
                self.errors.append("final oracle: " + type(error).__name__)
            self.stop.set()
            if sampler:
                sampler.join(timeout=180)
            try:
                outcome["stoppedContainers"] = stop_owned(self.plan)
                outcome["ownedStackStopped"] = True
            except Exception as error:
                self.errors.append("shutdown: " + type(error).__name__)
            outcome["complete"] = outcome["complete"] and not self.errors
            outcome.update(startedUnix=self.started, completedUnix=time.time(), phases=self.results,
                           errors=self.errors, freeBytes=shutil.disk_usage(self.output).free,
                           volumesRemoved=False, warmSteadyStateClaimed=False)
            atomic_json(self.output / "result.json", outcome)
            self.events.close()
        return 0 if outcome["complete"] and not self.errors else 1


def watchdog(plan):
    output = Path(plan["output"])

    def finished():
        path = output / "result.json"
        return path.exists() and json.loads(path.read_text()).get("ownedStackStopped") is True

    while time.time() < plan["hardStopUnix"]:
        if finished():
            return
        time.sleep(min(15, plan["hardStopUnix"] - time.time()))
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    (output / "STOP").touch()
    until = plan["hardStopUnix"] + 600
    while time.time() < until:
        if finished():
            return
        time.sleep(5)
    stopped = stop_owned(plan)
    atomic_json(output / "watchdog-shutdown.json",
                {"unix": time.time(), "stoppedContainers": stopped, "volumesRemoved": False})


def main():
    os.umask(0o077)
    path = Path(sys.argv[2] if sys.argv[1].startswith("--") else sys.argv[1]).resolve()
    plan = load_plan(path)
    if "--watchdog" in sys.argv:
        watchdog(plan)
        return 0
    benchmark = Benchmark(plan)
    signal.signal(signal.SIGTERM, lambda *_: benchmark.stop.set())
    signal.signal(signal.SIGINT, lambda *_: benchmark.stop.set())
    if "--preflight" in sys.argv:
        try:
            benchmark.preflight()
            atomic_json(benchmark.output / "preflight.json", {"passed": True, "unix": time.time()})
            return 0
        finally:
            benchmark.events.close()
    return benchmark.run()


if __name__ == "__main__":
    sys.exit(main())
