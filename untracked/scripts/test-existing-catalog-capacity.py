#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import threading
import time
import traceback
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location(
    "owned_capacity", Path(__file__).with_name("run-existing-catalog-capacity.py"))
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


class OwnedCatalogCapacityTests(unittest.TestCase):
    @contextmanager
    def http_responses(self, responses):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def respond(self):
                requests.append(json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                                if self.command == "POST" else self.path)
                status, headers, value = responses.pop(0) if responses else (500, {}, {})
                body = json.dumps(value).encode()
                self.send_response(status)
                for name, value in headers.items():
                    self.send_header(name, value)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            do_GET = respond
            do_POST = respond

            def log_message(self, *_):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=lambda: server.serve_forever(poll_interval=.01))
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}", requests
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)

    def fixture(self, root):
        return {
            "owned": True, "apiA": "http://127.0.0.1:4000", "apiB": "http://127.0.0.1:4001",
            "gitRemote": "http://127.0.0.1:9000/owned/catalog.git", "prometheus": "http://127.0.0.1:19090",
            "controllerHealth": ["http://127.0.0.1:5001/health", "http://127.0.0.1:5002/health"],
            "namespace": "owned", "repository": "catalog", "prefix": "owned-fixture", "runID": "owned-test",
            "runtime": [{"name": "capacity-git-service" if i == 0 else f"service-{i}",
                         "id": f"{i:064x}"} for i in range(8)],
            "tokenFile": str(root / "token"), "seedClone": str(root / "seed"),
            "originalClone": str(root / "original"), "driverBinary": str(root / "driver"),
            "output": str(root / "output"), "launchByUnix": time.time() + 60,
            "hardStopUnix": time.time() + 10 * 3600, "baselineSeconds": 1800,
            "rampStepSeconds": 900, "confirmationSeconds": 1800, "soakSeconds": 21600,
            "readRates": [1, 4, 16, 64], "pushRates": [.25, .5, 1, 2],
        }

    def test_plan_rejects_unowned_remote_and_unbounded_work(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "plan.json"
            original = self.fixture(root)
            path.write_text(json.dumps(original))
            RUNNER.load_plan(path)
            for key, value in (("owned", False), ("apiA", "http://example.com"),
                               ("hardStopUnix", time.time() + 14 * 3600),
                               ("soakSeconds", 0), ("pushRates", [.25, .5, 1, 21]),
                               ("preparationOnly", "false"), ("soakOnly", "false")):
                with self.subTest(key=key):
                    path.write_text(json.dumps({**original, key: value}))
                    with self.assertRaises(AssertionError):
                        RUNNER.load_plan(path)
            path.write_text(json.dumps({**original, "preparationOnly": True, "soakOnly": True}))
            with self.assertRaisesRegex(AssertionError, "conflicting phase selections"):
                RUNNER.load_plan(path)

    def test_walk_fully_pages_and_rejects_duplicates_or_foreign_members(self):
        with tempfile.TemporaryDirectory() as temporary:
            benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
            names = sorted(benchmark.expected)[:3]

            def page(batch, more=False, cursor=None):
                return {"category": {"products": {
                    "edges": [{"node": {"metadata": {"name": name, "uid": name}}} for name in batch],
                    "pageInfo": {"hasNextPage": more, "hasPreviousPage": more,
                                 "endCursor": cursor, "startCursor": cursor}}}}

            try:
                for backward in (False, True):
                    with mock.patch.object(benchmark, "graphql", side_effect=[
                            page(names[:2], True, "next"), page(names[2:])]):
                        self.assertEqual(set(benchmark.walk("http://127.0.0.1:4000", "hot", backward)), set(names))
                for pages in ([page([names[0]], True, "next"), page([names[0]])],
                              [page(["foreign-product"])],
                              [page([], True, "same"), page([], True, "same")]):
                    with mock.patch.object(benchmark, "graphql", side_effect=pages):
                        with self.assertRaises(ValueError):
                            benchmark.walk("http://127.0.0.1:4000", "hot")
            finally:
                benchmark.events.close()

    def test_shutdown_refuses_replaced_containers_before_any_stop(self):
        plan = {"runtime": [{"name": "owned", "id": "expected"}]}
        with mock.patch.object(RUNNER.subprocess, "check_output",
                               return_value=json.dumps([{"Id": "replacement"}]).encode()):
            with mock.patch.object(RUNNER.subprocess, "run") as stop:
                with self.assertRaises(AssertionError):
                    RUNNER.stop_owned(plan)
                stop.assert_not_called()

    def test_runtime_refuses_changed_mounted_configuration_before_sampling(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config = root / "config"
            config.write_text("original configuration")
            plan = {**self.fixture(root), "configurationFilesSHA256": {
                str(config): RUNNER.hashlib.sha256(config.read_bytes()).hexdigest()}}
            benchmark = RUNNER.Benchmark(plan)
            config.write_text("changed configuration")
            try:
                with mock.patch.object(RUNNER.subprocess, "check_output") as inspect:
                    with self.assertRaisesRegex(AssertionError, "mounted configuration changed"):
                        benchmark.runtime()
                    inspect.assert_not_called()
            finally:
                benchmark.events.close()

    def test_baseline_fence_refuses_changed_protected_index_before_fetch(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            original = root / "original/.git"
            original.mkdir(parents=True)
            index = original / "index"
            index.write_bytes(b"protected index")
            plan = {**self.fixture(root), "originalHead": "protected-head",
                    "originalIndexSHA256": RUNNER.hashlib.sha256(index.read_bytes()).hexdigest()}
            benchmark = RUNNER.Benchmark(plan)
            index.write_bytes(b"changed index")
            try:
                with mock.patch.object(benchmark, "git", return_value="protected-head") as git:
                    with self.assertRaisesRegex(AssertionError, "protected original index changed"):
                        benchmark.baseline_fence()
                    git.assert_called_once_with("rev-parse", "HEAD", original=True)
            finally:
                benchmark.events.close()

    def test_reads_cover_git_preparation_and_complete_offered_window(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            benchmark = RUNNER.Benchmark(self.fixture(root))
            directory = root / "fake-driver"
            directory.mkdir()
            (directory / "driver.log").write_text("capacity-push-offer {}\n")
            (directory / "push-evidence.json").write_text(json.dumps(
                {"cancelled": False, "drainTimedOut": False}))
            started = time.monotonic()

            class PreparingDriver:
                returncode = 0

                def poll(self):
                    return 0 if time.monotonic() - started >= .07 else None

            log = (directory / "driver.log").open()
            try:
                with mock.patch.object(benchmark, "push_start", return_value=(PreparingDriver(), log, directory)), \
                        mock.patch.object(benchmark, "walk"), mock.patch.object(benchmark, "churn"), \
                        mock.patch.object(benchmark, "oracle"), mock.patch.object(benchmark, "check"):
                    result = benchmark.phase_run("coverage", .02, read_rate=10, push_rate=1)
                self.assertGreaterEqual(result["readOfferedDurationSeconds"], .07)
                self.assertEqual(result["reads"]["offered"], result["reads"]["scheduled"])
                self.assertEqual(result["reads"]["failed"], 0)
            finally:
                benchmark.events.close()

    def test_shutdown_stops_only_pinned_containers_in_dependency_order(self):
        roles = ("api-a", "api-b", "controller-manager-a", "controller-manager-b",
                 "capacity-git-service", "scylla-1", "scylla-2", "scylla-3")
        rows = [{"Id": str(i), "Name": "/owned-" + role, "State": {"Running": True}}
                for i, role in enumerate(roles)]
        final = [{**row, "State": {"Running": False}} for row in rows]
        plan = {"runtime": [{"name": r["Name"], "id": r["Id"]} for r in rows]}
        with mock.patch.object(RUNNER.subprocess, "check_output",
                               side_effect=[json.dumps(rows).encode(), json.dumps(final).encode()]), \
                mock.patch.object(RUNNER.subprocess, "run") as stop:
            stopped = RUNNER.stop_owned(plan)
            self.assertEqual(set(stopped), {r["Id"] for r in rows})
            self.assertEqual([call.args[0][4:] for call in stop.call_args_list],
                             [["2", "3"], ["0", "1"], ["4"], ["5", "6", "7"]])

    def test_abort_stops_owned_git_process_group_not_just_driver_parent(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            benchmark = RUNNER.Benchmark(self.fixture(root))
            process = mock.Mock(pid=12345)
            process.poll.return_value = None
            log = mock.Mock()
            try:
                with mock.patch.object(benchmark, "push_start", return_value=(process, log, root)), \
                        mock.patch.object(benchmark, "check", side_effect=[None, RuntimeError("stop")]), \
                        mock.patch.object(RUNNER.os, "killpg") as kill:
                    with self.assertRaisesRegex(RuntimeError, "stop"):
                        benchmark.phase_run("abort", 1, push_rate=1)
                    self.assertEqual(kill.call_args_list, [
                        mock.call(12345, RUNNER.signal.SIGTERM),
                        mock.call(12345, RUNNER.signal.SIGKILL)])
                    log.close.assert_called_once()
            finally:
                benchmark.events.close()

    def test_real_http_control_reads_retry_429_but_load_and_mutations_do_not(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "token").write_text("local-test-token")
            benchmark = RUNNER.Benchmark(self.fixture(root))
            responses = [(429, {"Retry-After": "1"}, {}),
                         (200, {}, {"data": {"ok": True}}),
                         (429, {"Retry-After": "1"}, {}),
                         (429, {"Retry-After": "1"}, {})]
            try:
                with self.http_responses(responses) as (endpoint, requests):
                    self.assertEqual(benchmark.graphql(endpoint, "{ok}", retry_rate_limit=True), {"ok": True})
                    for query in ("{ok}", "mutation{update}"):
                        with self.assertRaises(RUNNER.urllib.error.HTTPError) as raised:
                            benchmark.graphql(endpoint, query)
                        self.assertEqual(raised.exception.code, 429)
                        raised.exception.close()
                    self.assertEqual(len(requests), 4)
            finally:
                benchmark.events.close()

    def test_control_recovery_respects_deadline_permanent_errors_and_stop(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "token").write_text("local-test-token")
            benchmark = RUNNER.Benchmark(self.fixture(root))
            try:
                for code in (401, 403, 503, 429):
                    with self.subTest(code=code):
                        error = RUNNER.urllib.error.HTTPError(
                            benchmark.plan["apiA"], code, "test", {"Retry-After": "9999"}, None)
                        benchmark.local.deadline = time.monotonic() + .05
                        with mock.patch.object(RUNNER, "bounded_json", side_effect=error) as request:
                            with self.assertRaises(TimeoutError if code == 429 else RUNNER.urllib.error.HTTPError):
                                benchmark.graphql(benchmark.plan["apiA"], "{ok}", retry_rate_limit=True)
                            request.assert_called_once()
                benchmark.stop.set()
                with mock.patch.object(RUNNER, "bounded_json") as request:
                    with self.assertRaisesRegex(RuntimeError, "stop requested"):
                        benchmark.graphql(benchmark.plan["apiA"], "{ok}", retry_rate_limit=True)
                    request.assert_not_called()
            finally:
                benchmark.events.close()

    def test_health_http_failure_retains_redacted_reason_and_stops_sampler(self):
        with tempfile.TemporaryDirectory() as temporary:
            benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
            health = {"status": "degraded", "ready": False, "credentialReady": True,
                      "message": "private-response-content", "token": "private-response-content",
                      "kinds": {"CategoryTaxonomy": {"poisonItems": 1, "stalled": False, "queueDepth": 3,
                                                     "lastError": "private-response-content"},
                                "Product": {"queueDepth": 5000000, "activeWorkers": 4},
                                "private-response-content": {"poisonItems": 1}}}
            responses = [(200, {}, {"status": "healthy"}), (200, {}, {"status": "healthy"}),
                         (503, {"X-Private": "private-response-content"}, health)]
            try:
                with self.http_responses(responses) as (endpoint, requests):
                    benchmark.plan.update(apiA=endpoint, apiB=endpoint,
                                          controllerHealth=[endpoint + "/controller-a", endpoint + "/controller-b"])
                    with mock.patch.object(benchmark, "runtime"), mock.patch.object(benchmark, "check"), \
                            mock.patch.object(benchmark, "export_metrics") as export:
                        benchmark.sample()
                    self.assertEqual(requests, ["/health", "/health", "/controller-a"])
                    export.assert_called_once()
                self.assertTrue(benchmark.stop.is_set())
                self.assertEqual(benchmark.errors, [
                    f"telemetry/safety: health check failed: {endpoint}/controller-a (HTTPError, HTTP 503)"])
                events = (benchmark.output / "client-events.jsonl").read_text()
                self.assertNotIn("private-response-content", events)
                failure = json.loads(events.splitlines()[-1])
                self.assertEqual(failure["operation"], "health-failure")
                self.assertEqual(failure["httpStatus"], 503)
                self.assertEqual(failure["health"], {
                    "status": "degraded", "ready": False, "credentialReady": True,
                    "kinds": {"CategoryTaxonomy": {"poisonItems": 1, "stalled": False, "queueDepth": 3},
                              "Product": {"queueDepth": 5000000, "activeWorkers": 4}}})
            finally:
                benchmark.events.close()

    def test_health_failure_capture_bounds_and_closes_invalid_http_bodies(self):
        for body in (b"private-invalid-json", b"x" * (64 * 1024 + 2)):
            with self.subTest(length=len(body)), tempfile.TemporaryDirectory() as temporary:
                benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
                stream = io.BytesIO(body)
                error = RUNNER.urllib.error.HTTPError(
                    benchmark.plan["apiA"], 503, "private-error", {}, stream)
                try:
                    with mock.patch.object(stream, "read", wraps=stream.read) as read, \
                            mock.patch.object(RUNNER, "bounded_json", side_effect=error):
                        with self.assertRaises(RUNNER.HealthCheckError):
                            benchmark.scrape_health()
                        read.assert_called_once_with(64 * 1024 + 1)
                    self.assertTrue(stream.closed)
                    events = (benchmark.output / "client-events.jsonl").read_text()
                    self.assertNotIn("private-", events)
                    failure = json.loads(events)
                    self.assertEqual(failure["health"], {})
                    self.assertEqual(failure["bodyTruncated"], len(body) > 64 * 1024)
                    if len(body) < 64 * 1024:
                        self.assertEqual(failure["bodyFailureClass"], "JSONDecodeError")
                finally:
                    benchmark.events.close()

    def test_health_validation_and_transport_failures_keep_endpoint_without_error_text(self):
        for response in ({"ready": False, "kinds": {}}, TimeoutError("private-error")):
            with self.subTest(response=response), tempfile.TemporaryDirectory() as temporary:
                benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
                request = ({"side_effect": response} if isinstance(response, Exception)
                           else {"return_value": response})
                try:
                    with mock.patch.object(RUNNER, "bounded_json", **request):
                        with self.assertRaises(RUNNER.HealthCheckError) as raised:
                            benchmark.scrape_health()
                    self.assertIn(benchmark.plan["apiA"] + "/health", str(raised.exception))
                    self.assertNotIn("private-error", str(raised.exception))
                    self.assertNotIn("private-error", "".join(traceback.format_exception(raised.exception)))
                    events = (benchmark.output / "client-events.jsonl").read_text()
                    self.assertNotIn("private-error", events)
                    failure = json.loads(events)
                    self.assertEqual(failure["operation"], "health-failure")
                    self.assertNotIn("httpStatus", failure)
                    self.assertEqual(failure["failureClass"],
                                     "TimeoutError" if isinstance(response, Exception) else "AssertionError")
                finally:
                    benchmark.events.close()

    def test_control_recovery_honors_retry_after_and_bounds_invalid_headers(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "token").write_text("local-test-token")
            benchmark = RUNNER.Benchmark(self.fixture(root))
            try:
                for retry_after, expected_delay in (("2", 2), ("invalid", 1), ("NaN", 1), ("-1", 1)):
                    with self.subTest(retry_after=retry_after):
                        error = RUNNER.urllib.error.HTTPError(
                            benchmark.plan["apiA"], 429, "test", {"Retry-After": retry_after}, None)
                        with mock.patch.object(RUNNER, "bounded_json", side_effect=[
                                error, {"data": {"ok": True}}]) as request, \
                                mock.patch.object(benchmark.stop, "wait", return_value=False) as wait:
                            self.assertEqual(benchmark.graphql(
                                benchmark.plan["apiA"], "{ok}", retry_rate_limit=True), {"ok": True})
                            wait.assert_called_once_with(expected_delay)
                            self.assertEqual(request.call_count, 2)
            finally:
                benchmark.events.close()

    def test_full_oracle_and_restore_readback_recover_after_rate_limiting(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "token").write_text("local-test-token")
            benchmark = RUNNER.Benchmark(self.fixture(root))
            name = "owned-fixture-hot-000"
            benchmark.expected = {name: "owned-fixture-hot"}
            product = {
                "metadata": {"name": name, "uid": "owned-uid", "generation": 1,
                             "creationTimestamp": "2026-01-01T00:00:00Z"},
                "spec": {"title": "Owned", "categoryRef": {"name": "owned-fixture-hot"}},
                "status": {"observedGeneration": 1,
                           "conditions": [{"type": "Ready", "status": "TRUE", "observedGeneration": 1}],
                           "resolved": {"category": {"name": "owned-fixture-hot",
                                                     "path": ["owned-fixture-hot"]}}},
            }
            throttled, replicas = set(), set()

            def response(url, body, *_, **__):
                query = body["query"]
                replicas.add(url)
                self.assertFalse(query.startswith("mutation"))
                hot = "owned-fixture-hot" in query
                if "p0:product" in query:
                    kind, data = "batch", {"p0": product}
                elif "product(by:" in query:
                    kind, data = "readback", {"product": product}
                elif "products(" in query:
                    edges = [{"node": {"metadata": product["metadata"]}}] if hot else []
                    kind, data = "page", {"category": {"products": {
                        "edges": edges, "pageInfo": {"hasNextPage": False, "hasPreviousPage": False}}}}
                else:
                    kind, data = "category", {"category": {
                        "metadata": {"generation": 1},
                        "status": {"observedGeneration": 1, "resolved": {"productCount": int(hot)},
                                   "conditions": [{"type": "Ready", "status": "TRUE"}]}}}
                if kind not in throttled:
                    throttled.add(kind)
                    raise RUNNER.urllib.error.HTTPError(url, 429, "test", {}, None)
                return {"data": data}

            try:
                with mock.patch.object(RUNNER, "bounded_json", side_effect=response), \
                        mock.patch.object(benchmark.stop, "wait", return_value=False):
                    benchmark.oracle(allow_wait=True)
                    benchmark.churn(restore=True)
                self.assertEqual(set(benchmark.products), {name})
                self.assertEqual(throttled, {"batch", "page", "category", "readback"})
                self.assertEqual(replicas, {benchmark.plan["apiA"] + "/graphql",
                                            benchmark.plan["apiB"] + "/graphql"})
            finally:
                benchmark.events.close()

    def test_phase_preserves_overload_counts_before_failed_correctness_check(self):
        for verified in (True, False):
            with self.subTest(verified=verified), tempfile.TemporaryDirectory() as temporary:
                benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
                path = benchmark.output / "overload-result.json"
                failure = RUNNER.urllib.error.HTTPError("http://127.0.0.1", 429, "test", {}, None)

                def oracle(**_):
                    saved = json.loads(path.read_text())
                    self.assertFalse(saved["correctnessVerified"])
                    self.assertEqual(saved["reads"]["failed"], 1)
                    if not verified:
                        raise RUNNER.urllib.error.HTTPError("http://127.0.0.1", 401, "test", {}, None)

                try:
                    with mock.patch.object(benchmark, "walk", side_effect=failure) as walk, \
                            mock.patch.object(benchmark, "churn"), mock.patch.object(benchmark, "oracle", side_effect=oracle):
                        if verified:
                            benchmark.phase_run("overload", .02, read_rate=1)
                        else:
                            with self.assertRaises(RUNNER.urllib.error.HTTPError):
                                benchmark.phase_run("overload", .02, read_rate=1)
                        walk.assert_called_once()
                    saved = json.loads(path.read_text())
                    self.assertEqual(saved["reads"]["offered"], 1)
                    self.assertEqual(saved["reads"]["completed"], 1)
                    self.assertEqual(saved["readFailureClasses"], {"HTTP429": 1})
                    self.assertEqual(saved["correctnessVerified"], verified)
                    if not verified:
                        self.assertEqual(saved["validationError"], "HTTP401")
                finally:
                    benchmark.events.close()

    def test_oracle_does_not_retry_a_stop_request(self):
        with tempfile.TemporaryDirectory() as temporary:
            benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
            benchmark.stop.set()
            try:
                with mock.patch.object(benchmark, "graphql", side_effect=[
                        RuntimeError("stop requested"), ValueError("unexpected retry")]) as request:
                    with self.assertRaisesRegex(RuntimeError, "stop requested"):
                        benchmark.oracle(allow_wait=True)
                    request.assert_called_once()
            finally:
                benchmark.events.close()

    def test_run_cannot_qualify_an_overloaded_setting_or_failed_final_oracle(self):
        for final_failure in (False, True):
            with self.subTest(final_failure=final_failure), tempfile.TemporaryDirectory() as temporary:
                benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
                benchmark.products = {"owned-product": {}}
                phases = []

                def phase(name, seconds, **rates):
                    phases.append(name)
                    reads = int(seconds * rates.get("read_rate", 0))
                    pushes = int(seconds * rates.get("push_rate", 0))
                    return {"reads": {"failed": 0 if final_failure else 1, "dropped": 0, "missed": 0,
                                      "scheduled": reads, "offered": reads, "completed": reads},
                            "pushes": {"failures": 0, "dropped": 0, "missedSchedules": 0, "cancelled": False,
                                       "drainTimedOut": False, "scheduled": pushes, "offered": pushes,
                                       "pushes": pushes, "offeredDurationNs": seconds * 10**9},
                            "driverExitCode": 0, "readOfferedDurationSeconds": seconds}

                oracle_error = RuntimeError("final correctness failed") if final_failure else None
                with mock.patch.object(benchmark, "preflight"), mock.patch.object(benchmark, "sample"), \
                        mock.patch.object(benchmark, "phase_run", side_effect=phase), \
                        mock.patch.object(benchmark, "churn"), mock.patch.object(benchmark, "baseline_fence"), \
                        mock.patch.object(benchmark, "oracle", side_effect=oracle_error), \
                        mock.patch.object(RUNNER, "stop_owned", return_value=["owned-container"]):
                    self.assertEqual(benchmark.run(), 1)
                result = json.loads((benchmark.output / "result.json").read_text())
                self.assertFalse(result["complete"])
                self.assertTrue(result["ownedStackStopped"])
                if final_failure:
                    self.assertIn("soak", phases)
                    self.assertIn("final oracle: RuntimeError", result["errors"])
                else:
                    self.assertEqual(phases, ["baseline", "read-0", "read-1", "read-2", "read-3"])

    def test_preparation_requires_clean_trial_and_confirmation_without_running_soak(self):
        for failing_phase in (None, "preparation", "confirmation"):
            for failed_counter in ("read-failed", "read-dropped", "read-missed",
                                   "push-failures", "push-dropped", "push-missedSchedules"):
                with self.subTest(phase=failing_phase, counter=failed_counter), \
                        tempfile.TemporaryDirectory() as temporary:
                    plan = {**self.fixture(Path(temporary)), "preparationOnly": True}
                    benchmark = RUNNER.Benchmark(plan)

                    def phase(name, *_args, **_kwargs):
                        row = {"reads": {"failed": 0, "dropped": 0, "missed": 0},
                               "pushes": {"failures": 0, "dropped": 0, "missedSchedules": 0}}
                        if name == failing_phase:
                            kind, counter = failed_counter.split("-")
                            row["reads" if kind == "read" else "pushes"][counter] = 1
                        return row

                    with mock.patch.object(benchmark, "preflight"), mock.patch.object(benchmark, "sample"), \
                            mock.patch.object(benchmark, "phase_run", side_effect=phase) as phases, \
                            mock.patch.object(RUNNER, "stop_owned", return_value=["owned-container"]):
                        self.assertEqual(benchmark.run(), 0 if failing_phase is None else 1)
                    expected = [mock.call("preparation", 900, read_rate=1, push_rate=.25, churn=True)]
                    if failing_phase != "preparation":
                        expected.append(mock.call("confirmation", 1800, read_rate=1, push_rate=.25, churn=True))
                    self.assertEqual(phases.call_args_list, expected)
                    result = json.loads((benchmark.output / "result.json").read_text())
                    self.assertEqual(result["complete"], failing_phase is None)
                    self.assertTrue(result["preparationOnly"])
                    self.assertTrue(result["ownedStackStopped"])
                    self.assertFalse(result["performanceAcceptanceClaimed"])

    def test_soak_only_keeps_full_window_and_rejects_incomplete_or_failed_work(self):
        failures = (None, "read-failed", "read-dropped", "read-missed", "read-completed",
                    "push-failures", "push-dropped", "push-missedSchedules", "push-pushes",
                    "push-cancelled", "push-drainTimedOut", "push-offeredDurationNs",
                    "readOfferedDurationSeconds", "driverExitCode", "final-oracle")
        for failure in failures:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                plan = {**self.fixture(root), "soakOnly": True}
                path = root / "plan.json"
                path.write_text(json.dumps(plan))
                benchmark = RUNNER.Benchmark(RUNNER.load_plan(path))
                benchmark.products = {"owned-product": {}}
                row = {"reads": {"failed": 0, "dropped": 0, "missed": 0, "scheduled": 21600,
                                 "offered": 21600, "completed": 21600},
                       "pushes": {"failures": 0, "dropped": 0, "missedSchedules": 0, "cancelled": False,
                                  "drainTimedOut": False, "scheduled": 5400, "offered": 5400,
                                  "pushes": 5400, "offeredDurationNs": 21600 * 10**9},
                       "readOfferedDurationSeconds": 21600, "driverExitCode": 0}
                if failure and failure.startswith(("read-", "push-")):
                    kind, counter = failure.split("-")
                    row["reads" if kind == "read" else "pushes"][counter] += 1
                    if counter == "offeredDurationNs":
                        row["pushes"][counter] -= 2
                elif failure == "readOfferedDurationSeconds":
                    row[failure] -= 1
                elif failure == "driverExitCode":
                    row[failure] = 1
                with mock.patch.object(benchmark, "preflight") as preflight, \
                        mock.patch.object(benchmark, "sample"), \
                        mock.patch.object(benchmark, "phase_run", return_value=row) as phases, \
                        mock.patch.object(benchmark, "churn") as restore, \
                        mock.patch.object(benchmark, "baseline_fence") as fence, \
                        mock.patch.object(benchmark, "oracle", side_effect=(
                            RuntimeError("final oracle failed") if failure == "final-oracle" else None)), \
                        mock.patch.object(RUNNER, "stop_owned", return_value=["owned-container"]) as stop:
                    self.assertEqual(benchmark.run(), 0 if failure is None else 1)
                    preflight.assert_called_once()
                    phases.assert_called_once_with("soak", 21600, read_rate=1, push_rate=.25, churn=True)
                    restore.assert_called_once_with(restore=True)
                    stop.assert_called_once_with(plan)
                    if failure != "final-oracle":
                        fence.assert_called_once()
                result = json.loads((benchmark.output / "result.json").read_text())
                self.assertEqual(result["complete"], failure is None)
                self.assertTrue(result["soakOnly"])
                self.assertFalse(result["preparationOnly"])
                self.assertTrue(result["ownedStackStopped"])
                self.assertFalse(result["warmSteadyStateClaimed"])
                self.assertFalse(result["performanceAcceptanceClaimed"])

    def test_watchdog_waits_for_deadline_and_bounded_cleanup_before_stopping(self):
        with tempfile.TemporaryDirectory() as temporary:
            plan = {"output": str(Path(temporary) / "soak"), "hardStopUnix": 100}
            clock = [10.]

            def sleep(seconds):
                clock[0] += seconds

            with mock.patch.object(RUNNER.time, "time", side_effect=lambda: clock[0]), \
                    mock.patch.object(RUNNER.time, "sleep", side_effect=sleep), \
                    mock.patch.object(RUNNER, "stop_owned", return_value=["owned-container"]) as stop:
                RUNNER.watchdog(plan)
            self.assertEqual(clock[0], 700)
            stop.assert_called_once_with(plan)
            self.assertTrue((Path(plan["output"]) / "STOP").exists())
            receipt = json.loads((Path(plan["output"]) / "watchdog-shutdown.json").read_text())
            self.assertFalse(receipt["volumesRemoved"])

    def test_phase_does_not_offer_after_slow_result_collection_exceeds_window(self):
        with tempfile.TemporaryDirectory() as temporary:
            benchmark = RUNNER.Benchmark(self.fixture(Path(temporary)))
            clock = [100.]

            def submit(*_):
                future = RUNNER.concurrent.futures.Future()
                future.set_result((1, None))
                return future

            def wait(delay):
                clock[0] += delay

            def event(operation, **_):
                if operation == "membership-walk":
                    clock[0] += .011

            try:
                with mock.patch.object(RUNNER.time, "monotonic", side_effect=lambda: clock[0]), \
                        mock.patch.object(RUNNER.concurrent.futures.ThreadPoolExecutor, "submit", side_effect=submit), \
                        mock.patch.object(benchmark.stop, "wait", side_effect=wait), \
                        mock.patch.object(benchmark, "event", side_effect=event), \
                        mock.patch.object(benchmark, "churn"), mock.patch.object(benchmark, "oracle"):
                    result = benchmark.phase_run("late-collection", .015, read_rate=100)
                self.assertEqual(result["reads"]["scheduled"], 2)
                self.assertEqual(result["reads"]["offered"], 1)
                self.assertEqual(result["reads"]["completed"], 1)
                self.assertEqual(result["reads"]["missed"], 1)
            finally:
                benchmark.events.close()


if __name__ == "__main__":
    unittest.main()
