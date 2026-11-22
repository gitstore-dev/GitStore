#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
if [[ "${CAPACITY_TRANSLATION_READINESS:-0}" == 1 ]]; then
  python3 - "${1:?evidence directory is required}" <<'PY'
import datetime
import hashlib
import json
import math
import os
import pathlib
import posixpath
import re
import sys

root = pathlib.Path(sys.argv[1])
try:
    metadata = json.loads((root / "metadata.json").read_text())
    preflight = json.loads((root / "translation-preflight.json").read_text())
    summary = json.loads((root / "summary.json").read_text())["metrics"]
    barrier = (json.loads((root / "translation-load-start.json").read_text())
               if os.environ.get("CAPACITY_TRANSLATION_PUSHES", "0") == "1" else None)
    prepared = bool(barrier and barrier.get("warmupStartAt"))
    assert metadata["mode"] == "diagnostic", "read-only Translation diagnostics cannot certify acceptance"
    assert preflight["runId"] == metadata["runId"] and preflight["sourceStateSha256"] == metadata["sourceStateSha256"], "stale or unattributed read fixture"
    def values(name):
        metric = summary[name]
        return metric.get("values", metric)
    def at(value):
        assert isinstance(value, str) and 0 < len(value) <= 64, "invalid evidence timestamp"
        return datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    def measured_values(name):
        return values(name + "{phase:measured}" if prepared else name)
    assert at(metadata["loadCompletedAt"]) > at(metadata["loadStartedAt"]), "missing observed read window"
    failure_reasons = {}
    for reason in ("schedule", "http", "malformed", "graphql", "missing-source",
                   "body", "spec", "metadata", "status", "source"):
        metric = "translation_failures{reason:" + reason + "}"
        if metric in summary:
            count = values(metric)["count"]
            assert type(count) in (int, float) and math.isfinite(count) and count >= 0
            if count:
                failure_reasons[reason] = count
    assert values("checks")["fails"] == 0, "source or current-status mismatch" + (
        ": " + json.dumps(failure_reasons, sort_keys=True) if failure_reasons else "")
    assert "dropped_iterations" not in summary or values("dropped_iterations")["count"] == 0, "offered reads were dropped"
    requests = values("translation_requests")["count"]
    assert requests == preflight["rate"] * preflight["durationSeconds"] and requests > 0, "incomplete offered read window"
    assert requests == values("translation_correct")["count"], "incomplete source joins"
    preparation_requests = values("translation_preparation_requests")["count"] if prepared else 0
    if prepared:
        assert barrier["runId"] == metadata["runId"]
        assert at(barrier["startAt"]) - at(barrier["warmupStartAt"]) == datetime.timedelta(seconds=30)
        assert at(metadata["loadStartedAt"]) <= at(barrier["warmupStartAt"]), "read command omits preparation start"
        assert at(metadata["loadCompletedAt"]) >= at(barrier["startAt"]) + datetime.timedelta(seconds=preflight["durationSeconds"] + 15), "read command does not bracket the complete cooldown"
        for phase, seconds in (("warmup", 30), ("cooldown", 15)):
            expected = preflight["rate"] * seconds
            assert values("translation_preparation_requests{phase:" + phase + "}")["count"] == expected
            assert values("translation_preparation_correct{phase:" + phase + "}")["count"] == expected
        assert preparation_requests == preflight["rate"] * 45
        assert values("translation_preparation_correct")["count"] == preparation_requests
        assert measured_values("http_reqs")["count"] == measured_values("checks")["passes"] == requests
        assert measured_values("iterations")["count"] == requests
    assert values("http_reqs")["count"] == requests + preparation_requests and \
        values("checks")["passes"] == requests + preparation_requests, "unjoined or health-only read samples"
    boundary = values("translation_boundary_iterations")["count"]
    assert 0 <= boundary <= 1 and values("iterations")["count"] == requests + preparation_requests + boundary, "unaccounted scheduler boundary iterations"
    replicas = [values(f"translation_requests{{replica:{index}}}")["count"] for index in range(2)]
    for index, count in enumerate(replicas):
        assert count > 0 and count == values(f"translation_correct{{replica:{index}}}")["count"], "both APIs require correct reads"
    assert sum(replicas) == requests, "unexpected or missing read replica"
    timings = measured_values("http_req_duration")
    assert all(math.isfinite(timings[key]) and timings[key] >= 0 for key in ("p(95)", "p(99)"))
    assert timings["p(95)"] < 1000 and timings["p(99)"] < 3000, "read latency gate failed"
    failures = measured_values("http_req_failed")
    failure_rate = failures.get("rate", failures.get("value"))
    assert math.isfinite(failure_rate) and 0 <= failure_rate < .001, "unexpected read errors"
    verdict = {
        "schemaVersion": 1, "passed": False, "domainChecksPassed": True, "evidenceClass": "diagnostic",
        "runId": metadata["runId"], "sourceStateSha256": metadata["sourceStateSha256"],
        "fixtureSha256": preflight["fixtureSha256"], "durationSeconds": preflight["durationSeconds"],
        "requests": requests, "replicaRequests": replicas,
        "schedulerBoundaryIterations": boundary,
        "preparationRequests": preparation_requests,
        "readFailureReasons": failure_reasons,
        "measurementScope": "authenticated exact-source/current-status reads only; no push, fault, sustained or production certification",
    }
    topology_config = {}
    if (root / "config.json").exists():
        config_path = root / "config.json"
        assert config_path.stat().st_size <= 1024 * 1024, "topology configuration exceeds input budget"
        config_bytes = config_path.read_bytes()
        assert hashlib.sha256(config_bytes).hexdigest() == metadata["configDigest"], "tested topology configuration changed"
        topology_config = json.loads(config_bytes)
        assert isinstance(topology_config, dict) and isinstance(topology_config.get("services", {}), dict), "malformed topology configuration"
    if topology_config.get("services", {}).get("translationTopology") is not None:
        topology = json.loads((root / "translation-topology.json").read_text())
        for key in ("runId", "sourceStateSha256", "configDigest", "environmentDigest"):
            assert topology[key] == metadata.get(key), "topology belongs to a different run/source/configuration/environment"
        assert topology["liveTopologyVerified"] is True and topology["passed"] is False
        assert topology["evidenceClass"] == "diagnostic" and topology["sourceRevision"] == metadata["gitRevision"]
        processes = topology["processes"]
        assert [process["role"] for process in processes] == ["api", "api", "controller", "controller", "gitService"]
        assert len({process["containerID"] for process in processes}) == 5, "topology aliases a container"
        assert [process["endpoint"] for process in processes[:2]] == preflight["apiEndpoints"], "topology API endpoints differ"
        assert len({process["instanceID"] for process in processes[:4]}) == 4, "topology aliases a process"
        assert len(topology["scylla"]) == 3
        scylla = topology["scylla"]
        all_containers = processes + scylla
        assert all(isinstance(process["container"], str) and process["container"] and
                   isinstance(process["containerID"], str) and re.fullmatch(r"[a-f0-9]{64}", process["containerID"])
                   for process in all_containers), "missing journal-source container identity"
        assert len({process["containerID"] for process in all_containers}) == \
            len({process["container"] for process in all_containers}) == 8, "topology aliases a core or journal-source container"
        for process in scylla:
            assert at(process["startedAt"]) <= at(metadata["loadStartedAt"]), "journal-source epoch postdates preflight/load"
            members = process["members"]
            assert isinstance(members, list) and len(members) == 3 and \
                all(isinstance(member, str) and member for member in members) and \
                members == sorted(set(members)) and members == scylla[0]["members"], "journal-source membership differs or aliases"
            assert process["limits"] == topology_config["services"]["translationTopology"]["limits"]["scylla"] and \
                process["smp"] == topology_config["services"]["scylla"]["smpPerNode"], "journal-source configured ceilings differ"
            assert re.fullmatch(r"sha256:[a-f0-9]{64}", process["imageID"]), "mutable journal-source image"
        verdict["liveTopologyJoined"] = True
        verdict["topologyMeasurementScope"] = topology["measurementScope"]
    replacement_resources = {}
    resource_gap_declarations = topology_config.get("services", {}).get("translationResourceGaps", [])
    assert isinstance(resource_gap_declarations, list) and len(resource_gap_declarations) <= 2, "invalid declared replacement resource gaps"
    used_resource_gaps = set()
    resource_faults = []
    if barrier:
        connected_path = root / "translation-connected.json"
        assert connected_path.stat().st_size <= 32 * 1024 * 1024, "connected receipt exceeds input budget"
        resource_faults = json.loads(connected_path.read_text()).get("faults", [])
        assert isinstance(resource_faults, list) and len(resource_faults) <= 2, "unbounded resource fault evidence"
    if os.environ.get("CAPACITY_PROMETHEUS_URL"):
        capture = json.loads((root / "prometheus/manifest.json").read_text())
        for key in ("runId", "sourceStateSha256", "configDigest", "environmentDigest"):
            assert capture[key] == metadata.get(key), "resource capture belongs to different run/source/configuration/environment"
        assert capture["capacityTarget"] == "api/readiness"
        assert capture["source"] == os.environ["CAPACITY_PROMETHEUS_URL"].rstrip("/"), "different resource collector"
        assert capture["translationStepSeconds"] == capture["controllerStepSeconds"] == 5
        identities = set()
        for role, filename, variable in (
            ("api", "translation-api-series.json", "CAPACITY_PROMETHEUS_TARGETS"),
            ("controller", "controller-series.json", "CAPACITY_PROMETHEUS_CONTROLLER_TARGETS"),
        ):
            targets = os.environ.get(variable, "").split(",")
            assert len(targets) == len(set(targets)) == 2 and capture["translationTargets"][role] == targets, "different resource scrape targets"
            path = root / "prometheus" / filename
            assert path.stat().st_size <= 256 * 1024 * 1024, "resource history exceeds its bounded input budget"
            raw = path.read_bytes()
            assert hashlib.sha256(raw).hexdigest() == capture["translationResourceDigests"][role], "resource history digest mismatch"
            history = json.loads(raw)
            assert history["status"] == "success" and history["data"]["resultType"] == "matrix"
            series = history["data"]["result"]
            assert isinstance(series, list) and all(isinstance(sample, dict) and
                isinstance(sample.get("metric"), dict) and isinstance(sample.get("values"), list)
                for sample in series), "malformed resource history"
            for target in targets:
                info = [sample for sample in series if sample["metric"].get("instance") == target and
                        sample["metric"].get("__name__") == f"gitstore_{role}_process_instance_info" and
                        any(point[1] == "1" for point in sample["values"])]
                epochs = {sample["metric"].get("instance_id") for sample in info}
                assert epochs and all(isinstance(epoch, str) and epoch for epoch in epochs), "missing observed resource process identity"
                if verdict.get("liveTopologyJoined"):
                    role_processes = [process for process in topology["processes"] if process["role"] == role]
                    assert role_processes[targets.index(target)]["instanceID"] in epochs, "resource history omits the preflight process identity"
                assert not identities.intersection(epochs), "resource endpoints alias the same process"
                identities.update(epochs)
        verdict["resourceCaptureJoined"] = True
        verdict["resourceMeasurementScope"] = "source/target/process/digest joins only; not full-window coverage or resource-limit certification"
        if verdict.get("liveTopologyJoined"):
            window_start = at(metadata["loadStartedAt"]).timestamp()
            if os.environ.get("CAPACITY_TRANSLATION_PUSHES", "0") == "1":
                barrier = json.loads((root / "translation-load-start.json").read_text())
                assert barrier["runId"] == metadata["runId"], "unattributed resource load barrier"
                window_start = at(barrier["startAt"]).timestamp()
            window_end = window_start + preflight["durationSeconds"]
            capture_range = capture["translationRange"]
            assert capture_range["stepSeconds"] == 5 and capture_range["startUnixSeconds"] <= window_start
            assert capture_range["endUnixSeconds"] >= window_end, "resource capture does not span the offered window"
            maximum_gap = 6

            def resource_matrix(filename, digest):
                path = root / "prometheus" / filename
                assert path.stat().st_size <= 256 * 1024 * 1024, "resource history exceeds input budget"
                raw = path.read_bytes()
                assert hashlib.sha256(raw).hexdigest() == digest, "resource provenance digest mismatch"
                response = json.loads(raw)
                assert response["status"] == "success" and response["data"]["resultType"] == "matrix"
                assert not response.get("warnings"), "partial resource history"
                result = response["data"]["result"]
                assert isinstance(result, list) and result, "missing resource history"
                return result

            def points(sample):
                result = []
                for point in sample["values"]:
                    assert isinstance(point, list) and len(point) == 2 and not isinstance(point[0], bool)
                    assert isinstance(point[0], (int, float)) and isinstance(point[1], str)
                    moment, value = point[0], float(point[1])
                    assert math.isfinite(moment) and moment >= 0 and math.isfinite(value) and value >= 0
                    assert not result or moment > result[-1][0], "unordered/duplicate resource evaluations"
                    result.append((moment, value))
                assert result, "empty resource samples"
                return result

            resources = []
            for role, filename in (("api", "translation-api-series.json"), ("controller", "controller-series.json")):
                matcher = "instance=~" + json.dumps("|".join(
                    re.escape(target) for target in capture["translationTargets"][role]))
                selector = ("up|target_info|gitstore_api_process_instance_info|gitstore_translation_.*" if role == "api"
                            else "up|target_info|gitstore_controller_.*|gitstore_translation_.*")
                query = ('{job="gitstore-' + role + '-capacity",' + matcher + ',__name__=~"' + selector +
                         '|process_start_time_seconds|process_cpu_seconds_total|process_resident_memory_bytes|go_goroutines|go_gc_heap_live_bytes|go_gc_cycles_total"}')
                assert capture["translationTargets"][role + "Matcher"] == matcher, "resource matcher differs from declared targets"
                assert capture["translationQuery" if role == "api" else "controllerQuery"] == query, "resource query differs from canonical role/target selector"
                assert capture["translationScrapeTimeQueries"][role] == (
                    'timestamp(label_replace(' + query + ', "source_metric", "$1", "__name__", "(.+)"))'
                ), "original-scrape query differs from canonical metric-preserving timestamp expression"
                raw_series = resource_matrix(filename, capture["translationResourceDigests"][role])
                time_series = resource_matrix("translation-" + role + "-scrape-times.json",
                                              capture["translationScrapeTimeDigests"][role])
                for index, target in enumerate(capture["translationTargets"][role]):
                    process = [item for item in topology["processes"] if item["role"] == role][index]
                    replacements = [fault["physicalFault"] for fault in resource_faults if
                                    isinstance(fault.get("physicalFault"), dict) and
                                    fault["physicalFault"].get("profile") == role + "-restart" and
                                    fault["physicalFault"].get("targetId") == process["containerID"]]
                    assert len(replacements) <= 1, "duplicate replacement of one resource target"
                    replacement = replacements[0] if replacements else None
                    process_ids = {}
                    def samples(name):
                        selected = [sample for sample in raw_series if sample["metric"].get("instance") == target and
                                    sample["metric"].get("__name__") == name]
                        observed = []
                        is_identity = name == f"gitstore_{role}_process_instance_info"
                        assert 1 <= len(selected) <= (2 if replacement and is_identity else 1), "missing/ambiguous resource metric or undeclared process replacement"
                        for series in selected:
                            labels = {key: value for key, value in series["metric"].items() if key != "__name__"}
                            provenance = [sample for sample in time_series if
                                          {key: value for key, value in sample["metric"].items() if key != "source_metric"} == labels and
                                          sample["metric"].get("source_metric") == name]
                            assert len(provenance) == 1, "missing/ambiguous original scrape timestamps"
                            timestamps = dict(points(provenance[0]))
                            original = []
                            for evaluated_at, value in points(series):
                                scraped_at = timestamps.get(evaluated_at)
                                assert scraped_at is not None and 0 <= evaluated_at - scraped_at <= maximum_gap, "stale/lookback-filled resource sample"
                                if original and scraped_at == original[-1][0]:
                                    assert value == original[-1][1], "one original scrape has inconsistent values"
                                    continue
                                assert not original or scraped_at > original[-1][0], "original scrape timestamps moved backwards"
                                original.append((scraped_at, value))
                                if is_identity:
                                    identity = labels.get("instance_id")
                                    assert isinstance(identity, str) and identity, "missing original-scrape process identity"
                                    assert scraped_at not in process_ids, "overlapping process identities on one original scrape"
                                    process_ids[scraped_at] = identity
                            observed.extend(original)
                        observed.sort()
                        assert all(left[0] < right[0] for left, right in zip(observed, observed[1:])), "overlapping original resource scrapes"
                        before = [position for position, point in enumerate(observed) if point[0] <= window_start]
                        after = [position for position, point in enumerate(observed) if point[0] >= window_end]
                        assert before and after, "resource samples do not bracket the complete offered window"
                        first = max(0, before[-1] - 1) if replacement else before[-1]
                        bounded = observed[first:after[0] + 1]
                        assert len(bounded) >= 2
                        return bounded, labels

                    required = ["up", "process_start_time_seconds", "process_cpu_seconds_total", "process_resident_memory_bytes", "go_goroutines",
                                "go_gc_heap_live_bytes", "go_gc_cycles_total", f"gitstore_{role}_process_instance_info"]
                    histories = {name: samples(name) for name in required}
                    moments = [point[0] for point in histories["up"][0]]
                    assert all([point[0] for point in history[0]] == moments for history in histories.values()), "resource/GC/process samples are not from the same original scrapes"
                    assert all(value == 1 for _, value in histories["up"][0]), "resource target was down"
                    info, _ = histories[f"gitstore_{role}_process_instance_info"]
                    assert all(value == 1 for _, value in info), "unjoined resource process epoch"
                    process_start = process["processStartTimeSeconds"]
                    assert type(process_start) in (int, float) and math.isfinite(process_start) and 0 < process_start <= window_start
                    starts = [value for _, value in histories["process_start_time_seconds"][0]]
                    groups = []
                    for position, native_start in enumerate(starts):
                        identity = process_ids.get(moments[position])
                        assert identity, "process identity does not join every original resource scrape"
                        if not groups or native_start != starts[groups[-1][0]]:
                            groups.append([position])
                        else:
                            groups[-1].append(position)
                        assert identity == process_ids[moments[groups[-1][0]]], "process identity changed without a native start boundary"
                    assert starts[0] == process_start and process_ids[moments[0]] == process["instanceID"], "initial resource process differs from live preflight"
                    assert len(groups) == (2 if replacement else 1), "native process epoch changed or differs from live topology"
                    if replacement:
                        before_group, after_group = groups
                        assert len(before_group) >= 2 and len(after_group) >= 2, "replacement epochs require independently bracketed CPU/GC/resource samples"
                        old_id, new_id = process_ids[moments[before_group[0]]], process_ids[moments[after_group[0]]]
                        assert old_id != new_id, "replacement reused the previous native process identity"
                        physical_window = replacement["faultWindow"]
                        for key in ("injectionStartedMillis", "completedMillis"):
                            assert type(physical_window[key]) is int and physical_window[key] > 0
                        injected = physical_window["injectionStartedMillis"] / 1000
                        completed = physical_window["completedMillis"] / 1000
                        gap_start, gap_end = moments[before_group[-1]], moments[after_group[0]]
                        matches = [position for position, gap in enumerate(resource_gap_declarations) if
                                   isinstance(gap, dict) and gap.get("role") == role and
                                   gap.get("container") == process["container"] and gap.get("fault") == role + "-restart"]
                        assert len(matches) == 1 and matches[0] not in used_resource_gaps, "replacement lacks one independently declared resource gap"
                        gap = resource_gap_declarations[matches[0]]
                        assert type(gap["maximumSeconds"]) is int and 0 < gap["maximumSeconds"] <= 30
                        assert gap_start <= injected < completed <= gap_end and \
                            gap_end - gap_start <= gap["maximumSeconds"] and \
                            injected - gap_start <= maximum_gap and gap_end - completed <= maximum_gap, "resource gap exceeds the observed bounded replacement window"
                        used_resource_gaps.add(matches[0])
                        restored_start = at(replacement["targetAfter"][0]["State"]["StartedAt"]).timestamp()
                        new_start = starts[after_group[0]]
                        assert math.floor(restored_start) <= new_start <= completed and new_start > process_start, "new native process start does not join physical restoration"
                        replacement_resources[process["containerID"]] = {
                            "role": role, "beforeInstanceID": old_id, "afterInstanceID": new_id,
                            "afterProcessStartTimeSeconds": new_start,
                        }
                    cpu_limit = process["limits"]["nanoCPUs"] / 1000000000
                    memory_limit = process["limits"]["memoryBytes"]
                    assert cpu_limit > 0 and memory_limit > 0
                    for group in groups:
                        assert 0 < starts[group[0]] <= moments[group[0]], "resource scrape predates its native process start"
                        assert len(group) >= 2 and all(0 < moments[right] - moments[left] <= maximum_gap
                                                     for left, right in zip(group, group[1:])), "undeclared original-scrape gap"
                        cpu = [histories["process_cpu_seconds_total"][0][position] for position in group]
                        utilization = [(right[1] - left[1]) / (right[0] - left[0]) / cpu_limit
                                       for left, right in zip(cpu, cpu[1:])]
                        assert all(0 <= value < .8 for value in utilization), "CPU ceiling or unexplained counter reset"
                        resident = [histories["process_resident_memory_bytes"][0][position][1] for position in group]
                        assert all(0 < value <= memory_limit for value in resident), "resident memory exceeds configured ceiling"
                        retained = [histories["go_gc_heap_live_bytes"][0][position][1] for position in group]
                        cycles = [histories["go_gc_cycles_total"][0][position][1] for position in group]
                        assert all(value > 0 and value.is_integer() for value in cycles) and all(left <= right for left, right in zip(cycles, cycles[1:])), "missing/reset completed-GC provenance"
                        assert all(0 < value <= memory_limit for value in retained) and max(retained) < retained[0] * 1.1, "retained-memory growth gate"
                        assert all(left_cycle != right_cycle or left_heap == right_heap for
                                   left_cycle, right_cycle, left_heap, right_heap in zip(cycles, cycles[1:], retained, retained[1:])), "retained heap changed without a completed GC"
                        goroutines = [histories["go_goroutines"][0][position][1] for position in group]
                        assert all(value > 0 and value.is_integer() for value in goroutines)
                        assert max(abs(value - goroutines[0]) for value in goroutines) <= goroutines[0] * .05, "goroutine drift gate"
                        resources.append({"role": role, "instanceID": process_ids[moments[group[0]]],
                                          "processStartTimeSeconds": starts[group[0]], "originalScrapes": len(group),
                                          "maximumCPUUtilization": max(utilization), "maximumResidentBytes": max(resident),
                                          "maximumRetainedHeapBytes": max(retained), "initialGoroutines": goroutines[0]})
            verdict["resourceGatesVerified"] = True
            verdict["resources"] = resources
            verdict["maximumOriginalScrapeGapSeconds"] = maximum_gap
            verdict["resourceMeasurementScope"] = "fault-free offered-window original-scrape coverage, configured CPU/RSS ceilings, completed-GC retained heap and goroutine drift; no physical-fault/rollout/production certification"
    assert len(used_resource_gaps) == len(resource_gap_declarations), "declared replacement gaps lack independent original-scrape/process evidence"
    if os.environ.get("CAPACITY_TRANSLATION_PUSHES", "0") == "1":
        assert (root / "translation-connected.json").stat().st_size <= 32 * 1024 * 1024, "connected receipt exceeds input budget"
        connected = json.loads((root / "translation-connected.json").read_text())
        barrier = json.loads((root / "translation-load-start.json").read_text())
        assert connected["scope"] == "connected-translation" and connected["evidenceClass"] == "diagnostic"
        for key in ("runId", "sourceStateSha256", "fixtureSha256"):
            assert connected[key] == preflight[key], "unattributed push/source/read receipt"
        assert re.fullmatch(r"[a-f0-9]{40}", connected["sourceRevision"]) and \
            connected["sourceRevision"] == metadata["gitRevision"], "connected receipt belongs to a different verifier revision"
        verdict["sourceRevision"] = connected["sourceRevision"]
        for key in ("configDigest", "environmentDigest"):
            assert connected[key] == metadata.get(key, ""), "different push configuration or environment"
        start, end = at(connected["start"]), at(connected["end"])
        assert barrier["runId"] == metadata["runId"] and at(barrier["startAt"]) == start, "unjoined load barrier"
        assert at(metadata["loadStartedAt"]) <= start < end <= at(metadata["loadCompletedAt"]), "connected receipt is outside the observed read window"
        faults, fault_windows = connected.get("faults", []), connected.get("faultWindows", [])
        fault_declarations = topology_config.get("services", {}).get("translationFaults", {})
        physical_declarations = topology_config.get("services", {}).get("translationPhysicalFaults", {})
        assert isinstance(faults, list) and isinstance(fault_windows, list) and isinstance(fault_declarations, dict), "malformed Translation fault declarations"
        assert isinstance(physical_declarations, dict) and all(
            name in ("journal-interruption", "api-restart", "controller-restart") and kind == "physical" for name, kind in physical_declarations.items()
        ), "replacement faults require independently joined process-segment resources"
        assert len(faults) == len(fault_windows) == len(fault_declarations) <= 2, "missing declared Translation fault/restoration evidence"
        assert all(name in ("callback-loss", "reply-loss") and kind == "simulated"
                   for name, kind in fault_declarations.items()), "physical fault support requires independent process/restoration/resource joins"
        seconds = preflight["durationSeconds"]
        assert (end - start).total_seconds() == seconds and connected["intervalNanos"] == 1000000000
        assert connected["workers"] == 2 and connected["offered"] == connected["completed"] == seconds
        assert connected["dropped"] == 0 and len(connected["pushes"]) == len(connected["expected"]) == seconds
        assert connected["verifiedRetainedDocuments"] == 1009 and len(connected["originalHeads"]) == 10
        material = connected["materialBefore"]
        assert re.fullmatch(r"[a-f0-9]{64}", material) and material == connected["materialAfter"], "target or retained baseline mutation"
        assert connected["baselineProducts"] > 0
        source_sizes = connected.get("sourceSizes")
        traffic_samples = {}
        if source_sizes is not None:
            assert isinstance(source_sizes, dict) and set(source_sizes) == {str(index) for index in range(seconds)}, "incomplete measured source-size joins"
            assert all(type(size) is int and 0 < size <= 1024 * 1024 for size in source_sizes.values()), "measured source exceeds its byte budget"
            assert any(size == 1024 * 1024 for size in source_sizes.values()), "measured schedule omitted the near-limit corpus"
            traffic_samples = {kind: {"acknowledgement": [], "visibility": [], "convergence": []}
                               for kind in ("normal", "near-limit")}
        windows = [(connected, seconds, start, end)]
        if prepared:
            preparation = connected["preparation"]
            warmup_start, warmup_end = at(preparation["start"]), at(preparation["end"])
            assert warmup_start == at(barrier["warmupStartAt"]) and warmup_end == warmup_start + datetime.timedelta(seconds=20)
            assert warmup_end <= at(preparation["completedAt"]) <= start
            assert preparation["warmupSeconds"] == 30 and preparation["cooldownSeconds"] == 15
            assert preparation["intervalNanos"] == 1000000000 and preparation["workers"] == connected["workers"]
            assert preparation["offered"] == preparation["completed"] == preparation["verified"] == 20
            assert preparation["dropped"] == 0
            windows.append((preparation, 20, warmup_start, warmup_end))
        commits, source_uids = set(), set()
        measured_sources = {}
        component_clock_count = 0
        for window, count, source_start, source_end in windows:
            assert len(window["pushes"]) == len(window["expected"]) == count
            sequences = set()
            for push in window["pushes"]:
                sequence, commit = push["Sequence"], push["Commit"]
                assert 0 <= sequence < count and sequence not in sequences and re.fullmatch(r"[a-f0-9]{40}", commit) and commit not in commits
                sequences.add(sequence)
                commits.add(commit)
                offered, acknowledged = at(push["OfferedAt"]), at(push["AcknowledgedAt"])
                assert offered == source_start + datetime.timedelta(seconds=sequence) and offered <= acknowledged <= offered + datetime.timedelta(seconds=30)
                samples = None
                if window is connected and source_sizes is not None:
                    kind = "near-limit" if source_sizes[str(sequence)] == 1024 * 1024 else "normal"
                    samples = traffic_samples[kind]
                    samples["acknowledgement"].append((acknowledged - offered).total_seconds())
                assert push["Documents"] == 1 and len(push["Observations"]) == 2
                expected = window["expected"][str(sequence)]
                if window is connected:
                    measured_sources[commit] = (push, expected)
                assert all(re.fullmatch(r"[a-f0-9]{64}", expected[key]) for key in ("SourceDigest", "BodyDigest", "PatchDigest"))
                replicas, identities = set(), set()
                for observation in push["Observations"]:
                    assert observation["Replica"] in preflight["apiEndpoints"] and observation["Replica"] not in replicas
                    replicas.add(observation["Replica"])
                    assert observation["Ready"] is True and observation["Historical"] is False, "current Ready/history flags must be typed booleans"
                    assert all(isinstance(observation[key], str) and observation[key]
                               for key in ("UID", "Version", "WatchCursor")), "source identity/version/watch cursor must be nonempty strings"
                    assert observation["WatchVersion"] == observation["Version"]
                    assert type(observation["Generation"]) is int and type(observation["ObservedGeneration"]) is int, "source generations must be integers"
                    assert observation["Generation"] == observation["ObservedGeneration"] > 0
                    assert observation["Revision"] == observation["LastAppliedRevision"] == "main@sha1:" + commit
                    assert all(observation[key] == expected[key] for key in expected), "partial source/body/patch receipt"
                    assert acknowledged <= at(observation["VisibleAt"]) <= at(observation["ReadyAt"]) <= source_end + datetime.timedelta(seconds=30)
                    component_clocks = ("ReadVisibleAt", "ReadReadyAt", "WatchAt")
                    if any(key in observation for key in component_clocks):
                        component_clock_count += 1
                        read_visible, read_ready, watched = (at(observation[key]) for key in component_clocks)
                        assert acknowledged <= read_visible <= read_ready <= at(observation["ReadyAt"]), "unjoined independent read clocks"
                        assert offered <= watched <= at(observation["VisibleAt"]), "unjoined actual watch-delivery clock"
                        assert at(observation["VisibleAt"]) == max(read_visible, watched), "combined visibility omits or rewrites a read/watch boundary"
                    if samples is not None:
                        samples["visibility"].append((at(observation["VisibleAt"]) - acknowledged).total_seconds())
                        samples["convergence"].append((at(observation["ReadyAt"]) - acknowledged).total_seconds())
                        if all(key in observation for key in component_clocks):
                            for name, moment in (("readVisibility", read_visible), ("guardedReadiness", read_ready),
                                                 ("watchDelivery", watched)):
                                samples.setdefault(name, []).append(max(0, (moment - acknowledged).total_seconds()))
                    identities.add((observation["UID"], observation["Version"]))
                assert len(identities) == 1, "replica source identities or versions differ"
                uid = next(iter(identities))[0]
                assert uid not in source_uids, "independent authored documents alias a source UID"
                source_uids.add(uid)
        if component_clock_count:
            assert component_clock_count == sum(count * 2 for _, count, _, _ in windows), "incomplete independent read/watch clocks"
        if faults:
            declared = {}
            for window in fault_windows:
                name = window["name"]
                assert name in fault_declarations and name not in declared and window["kind"] == fault_declarations[name]
                lower, upper, declared_at = (at(window[key]) for key in ("start", "end", "declaredAt"))
                assert start <= lower < end and lower < upper <= lower + datetime.timedelta(seconds=30)
                assert at(metadata["loadStartedAt"]) <= declared_at <= lower, "fault window was not declared before injection"
                declared[name] = (lower, upper)
            recovered, physical_recovered = [], []
            seen = set()
            for fault in faults:
                name, commit = fault["stage"], fault["commit"]
                assert name in declared and name not in seen and fault["kind"] == "simulated", "unjoined or duplicate callback fault"
                seen.add(name)
                assert fault.get("physicalRestart") is None, "unjoined physical restart cannot be promoted from a simulated receipt"
                assert commit in measured_sources, "fault does not identify an acknowledged measured source"
                push, expected = measured_sources[commit]
                fault_at, restored, observed = (at(fault[key]) for key in ("faultAt", "restored", "observed"))
                traffic_from, traffic_until, deadline = (at(fault[key]) for key in ("trafficFrom", "trafficUntil", "recoveryDeadline"))
                lower, upper = declared[name]
                assert lower <= at(fault["started"]) <= fault_at < upper
                assert at(push["OfferedAt"]) <= fault_at and start <= fault_at < end
                assert fault_at <= restored <= traffic_from < traffic_until <= observed <= min(deadline, end)
                assert deadline == fault_at + datetime.timedelta(seconds=30), "callback recovery deadline differs from the unchanged gate"
                duration = (traffic_until - traffic_from).total_seconds()
                assert duration >= 5 and restored <= upper, "incomplete predeclared fault/restoration window or sustained recovery"
                assert fault["sourceSha256"] == expected["SourceDigest"], "fault source differs from the acknowledged authored bytes"
                interception, delivery = fault["interception"], fault["delivery"]
                assert interception["runId"] == delivery["runId"] == metadata["runId"] and \
                    interception["newCommit"] == delivery["newCommit"] == commit
                assert isinstance(interception["oldCommit"], str) and re.fullmatch(r"[a-f0-9]{40}", interception["oldCommit"]) and \
                    delivery["oldCommit"] == interception["oldCommit"] and interception["ref"] == delivery["ref"] == "refs/heads/main"
                assert interception["kind"] == "simulated" and interception["stage"] == name and \
                    interception["upstreamCompleted"] is (name == "reply-loss") and at(interception["at"]) == fault_at, "fault lacks an actual matching transport interception"
                assert delivery["kind"] == "forwarded" and delivery["stage"] == "" and delivery["upstreamCompleted"] is True and \
                    restored <= at(delivery["at"]) <= traffic_from, "fault lacks successful post-restoration replay without resubmission"
                references = fault["referencesBeforeRestoration"]
                assert isinstance(references, str) and re.fullmatch(r"[a-f0-9]{64}", references) and \
                    references == fault["referencesAfterRestoration"] == fault["referencesSha256"], "fault recovery changed canonical references or required resubmission"
                snapshot = fault["referenceSnapshotBeforeRestoration"]
                assert isinstance(snapshot, str) and 0 < len(snapshot.encode()) <= 4 * 1024 * 1024 and \
                    snapshot == fault["referenceSnapshotAfterRestoration"] and snapshot.endswith("\n"), "missing, changed or unbounded canonical reference snapshots"
                lines = snapshot.splitlines()
                assert 0 < len(lines) <= 4096, "canonical reference snapshot exceeds its entry budget"
                names, canonical = [], []
                main = None
                for line in lines:
                    fields = line.split("\t")
                    assert len(fields) == 2 and re.fullmatch(r"[a-f0-9]{40}|[a-f0-9]{64}", fields[0]) and \
                        fields[1].startswith("refs/") and not any(character.isspace() for character in fields[1])
                    assert not names or fields[1] > names[-1], "unordered or duplicate canonical references"
                    names.append(fields[1])
                    canonical.append(fields[0] + "\t" + fields[1] + "\n")
                    if fields[1] == "refs/heads/main":
                        main = fields[0]
                assert main == commit and "".join(canonical) == snapshot and \
                    hashlib.sha256(snapshot.encode()).hexdigest() == references, "canonical references do not join the actual acknowledged head and digest"
                proof = fault["trafficProof"]
                assert type(proof["schemaVersion"]) is int and proof["schemaVersion"] == 1 and \
                    proof["runId"] == metadata["runId"] and proof["evidenceClass"] == "diagnostic"
                for key in ("faultAt", "trafficFrom", "trafficUntil", "observed", "recoveryDeadline"):
                    assert at(proof[key]) == at(fault[key]), "traffic proof belongs to a different recovery window"
                assert proof["commit"] == commit and proof["sourceSha256"] == expected["SourceDigest"]
                assert all(proof["sourceID"] == item["UID"] and proof["sourceVersion"] == item["Version"]
                           for item in push["Observations"]), "recovery reads do not join both replicas' source identity/version"
                recovery_reads = proof["recoveryReads"]
                assert isinstance(recovery_reads, list) and len(recovery_reads) == 2, "missing independent both-API recovery reads"
                recovery_replicas = set()
                accepted_reads = {item["Replica"]: item for item in push["Observations"]}
                for read in recovery_reads:
                    replica = read["Replica"]
                    assert replica in accepted_reads and replica not in recovery_replicas, "recovery reads alias or omit an API"
                    recovery_replicas.add(replica)
                    accepted = accepted_reads[replica]
                    assert read["Ready"] is True and read["Historical"] is False, "historical evidence cannot authorize current recovery"
                    assert all(read[key] == accepted[key] for key in ("UID", "Version", "Revision", "LastAppliedRevision")), "recovery read source identity/version/revision changed"
                    assert type(read["Generation"]) is int and type(read["ObservedGeneration"]) is int and \
                        read["Generation"] == read["ObservedGeneration"] == accepted["Generation"], "unreconciled recovery source generation"
                    assert all(read[key] == expected[key] for key in expected), "recovery read lost or changed source/body/patch content"
                    assert restored <= at(read["VisibleAt"]) <= at(read["ReadyAt"]) <= traffic_from, "recovery reads do not follow restoration and precede sustained traffic"
                assert all(at(item["ReadyAt"]) <= traffic_from for item in push["Observations"]), "recovery traffic predates current both-API readiness"
                if name == "callback-loss":
                    assert all(at(item["VisibleAt"]) >= restored for item in push["Observations"]), "dropped callback source appeared before restoration"
                rate, workers = proof["rate"], proof["workers"]
                assert type(rate) is int and rate == preflight["rate"] and type(workers) is int and 1 <= workers <= 32
                count = math.ceil(duration * rate)
                assert 0 < count <= 30000, "recovery traffic exceeds bounded window/rate budget"
                traffic = proof["traffic"]
                assert all(type(traffic[key]) is int for key in ("Offered", "Completed", "Dropped", "UnexpectedErrors"))
                assert traffic["Offered"] == traffic["Completed"] == fault["correctReads"] == count and \
                    type(fault["correctReads"]) is int and traffic["Dropped"] == traffic["UnexpectedErrors"] == 0, "incomplete or incorrect domain recovery traffic"
                sequences, latencies, delays = (traffic[key] for key in ("CompletedSequences", "Latencies", "DispatchDelays"))
                assert all(isinstance(items, list) and len(items) == count for items in (sequences, latencies, delays))
                assert all(type(sequence) is int for sequence in sequences) and set(sequences) == set(range(count)), "recovery requests were reused or missing"
                assert all(type(value) is int and value >= 0 for value in latencies + delays), "invalid native recovery timing samples"
                assert all(delay <= latency for delay, latency in zip(delays, latencies))
                assert all(traffic_from + datetime.timedelta(seconds=sequence / rate + latency / 1e9) <= observed
                           for sequence, latency in zip(sequences, latencies)), "native recovery samples extend beyond observed completion"
                ordered = sorted(latencies)
                assert ordered[math.ceil(count * .95) - 1] < 1000000000 and \
                    ordered[math.ceil(count * .99) - 1] < 3000000000, "recovery read latency gate failed"
                physical = fault.get("physicalFault")
                if physical is not None:
                    assert isinstance(physical, dict) and name == "callback-loss", "physical fault must join held callback source recovery"
                    profile = physical["profile"]
                    assert physical_declarations == {profile: "physical"} and not physical_recovered, "undeclared or duplicate physical fault"
                    assert verdict.get("liveTopologyJoined") and verdict.get("resourceGatesVerified"), "physical recovery requires independently joined full-window resources"
                    assert type(physical["schemaVersion"]) is int and physical["schemaVersion"] == 1 and \
                        physical["runId"] == metadata["runId"] and physical["domain"] == "translation"
                    assert physical["kind"] == "physical" and profile in ("journal-interruption", "api-restart", "controller-restart")
                    assert (physical["action"], physical["sourceScope"]) == (
                        ("pause", "shared-journal-source") if profile == "journal-interruption" else ("restart", "one-replica"))
                    assert physical["ownership"] == {"label": "com.docker.compose.project", "owner": topology["owner"]}, "physical source ownership differs from preflight"
                    for flag in ("injected", "faultObserved", "restorationRequired", "restored"):
                        assert physical[flag] is True, "physical pause/restoration was not independently observed"
                    assert physical["domainRecoveryVerified"] is False, "injector restoration alone is not domain recovery"
                    assert all(type(physical[key]) is int and physical[key] == 0 for key in ("exitCode", "injectorExitCode")), "physical injector or restoration failed"
                    replacement_role = "controller" if profile == "controller-restart" else "api"
                    candidates = topology["scylla"] if profile == "journal-interruption" else [
                        process for process in topology["processes"] if process["role"] == replacement_role]
                    sources = [source for source in candidates if source["containerID"] == physical["targetId"]]
                    assert len(sources) == 1 and physical["target"] == sources[0]["container"], "physical fault targets an unjoined owned source"
                    source = sources[0]
                    epoch = physical["processEpochs"]
                    assert at(epoch["before"]) == at(source["startedAt"]), "physical fault initial epoch differs from preflight"
                    if profile == "journal-interruption":
                        assert at(epoch["before"]) == at(epoch["after"]), "journal pause changed its process epoch"
                        capture_modes = (("targetBefore", False), ("targetDuring", True), ("targetAfter", False))
                    else:
                        assert at(epoch["after"]) > at(epoch["before"]), "physical restart reused its container process epoch"
                        assert source["containerID"] in replacement_resources, "physical restart lacks original-scrape native process evidence"
                        peers = [process for process in candidates if process["containerID"] != source["containerID"]]
                        assert len(peers) == 1, "physical replacement requires one independently owned surviving same-role peer"
                        peer = peers[0]
                        for capture_name in ("peerBefore", "peerAfter"):
                            captures = physical[capture_name]
                            assert isinstance(captures, list) and len(captures) == 1, "missing independent surviving-peer capture"
                            captured_peer = captures[0]
                            assert captured_peer["Id"] == peer["containerID"] and captured_peer["Name"] == "/" + peer["container"] and \
                                captured_peer["Image"] == peer["imageID"], "surviving peer lifetime or immutable image changed"
                            state = captured_peer["State"]
                            assert state["Running"] is True and state["Paused"] is False and state["OOMKilled"] is False and \
                                at(state["StartedAt"]) == at(peer["startedAt"]), "surviving same-role peer was replaced or unavailable"
                        capture_modes = (("targetBefore", False), ("targetAfter", False))
                    for capture_name, paused in capture_modes:
                        captures = physical[capture_name]
                        assert isinstance(captures, list) and len(captures) == 1, "missing independent physical target capture"
                        capture = captures[0]
                        assert capture["Id"] == source["containerID"] and capture["Image"] == source["imageID"] and \
                            capture["Name"] == "/" + source["container"], "physical target lifetime or immutable image changed"
                        state = capture["State"]
                        expected_start = epoch["after"] if capture_name == "targetAfter" else epoch["before"]
                        assert state["Running"] is True and state["Paused"] is paused and state["OOMKilled"] is False and \
                            at(state["StartedAt"]) == at(expected_start), "physical target capture does not join its observed process epoch"
                    if profile == "controller-restart":
                        destination = topology_config["services"]["translationTopology"]["checkpointDestination"]
                        for field, expected in (("targetBefore", source), ("targetAfter", source),
                                                ("peerBefore", peer), ("peerAfter", peer)):
                            checkpoint = physical[field][0]["Checkpoint"]
                            assert isinstance(checkpoint, dict) and checkpoint["writable"] is True, "controller checkpoint capture lacks writable retained storage"
                            for key in ("source", "destination"):
                                path = checkpoint[key]
                                assert isinstance(path, str) and 1 < len(path) <= 4096 and path.startswith("/") and not path.startswith("//") and \
                                    posixpath.normpath(path) == path, "invalid controller checkpoint identity"
                            assert isinstance(checkpoint["volume"], str) and len(checkpoint["volume"]) <= 256
                            assert checkpoint["destination"] == destination and \
                                all(checkpoint[key] == expected["checkpoint"][key] for key in ("source", "destination", "volume")), "controller checkpoint differs from retained preflight storage"
                        left, right = source["checkpoint"], peer["checkpoint"]
                        assert left["source"] != right["source"] and \
                            not left["source"].startswith(right["source"] + "/") and \
                            not right["source"].startswith(left["source"] + "/") and \
                            not (left["volume"] and left["volume"] == right["volume"]), "controller checkpoint storage overlaps"
                        verdict["controllerCheckpointStorageJoined"] = True
                    physical_window = physical["faultWindow"]
                    assert physical_window["confirmed"] is True
                    for key in ("startMillis", "endMillis", "injectionStartedMillis", "completedMillis"):
                        assert type(physical_window[key]) is int and physical_window[key] > 0, "invalid physical fault clock"
                    def millis_at(key):
                        return datetime.datetime.fromtimestamp(physical_window[key] / 1000, datetime.timezone.utc)
                    physical_start, physical_end = millis_at("startMillis"), millis_at("endMillis")
                    injected, completed = millis_at("injectionStartedMillis"), millis_at("completedMillis")
                    assert lower <= physical_start <= injected < completed <= physical_end and \
                        physical_end - physical_start <= datetime.timedelta(seconds=30), "physical injection is outside its predeclared bounded window"
                    assert fault_at <= injected and completed <= restored and physical_end <= deadline, "physical restoration does not precede source replay/recovery"
                    if profile != "journal-interruption":
                        assert injected <= at(epoch["after"]) <= completed, "replacement container start is outside observed injection/restoration"
                    physical_recovered.append({"name": profile, "kind": "physical",
                                              "sourceScope": physical["sourceScope"], "correctReads": count,
                                              "processEpochUnchanged": profile == "journal-interruption",
                                              "recoverySourceReads": len(recovery_reads)})
                recovered.append({"name": name, "kind": "simulated", "correctReads": count, "recoverySourceReads": len(recovery_reads),
                                  "trafficSeconds": duration, "recoverySeconds": (observed - fault_at).total_seconds()})
            assert len(physical_recovered) == len(physical_declarations), "declared physical faults lack independent restoration/source/resource evidence"
            if physical_recovered:
                verdict["physicalFaultRecovery"] = physical_recovered
                verdict["physicalFaultMeasurementScope"] = "owned journal pause or one API/controller replacement joined to original-scrape per-process resources, declared bounded gaps, unchanged same-role peer and retained controller checkpoints where applicable, ordinary source replay and five seconds of correct domain reads; no sustained/production acceptance"
                if replacement_resources:
                    verdict["resourceMeasurementScope"] = "original-scrape per-process CPU/RSS, completed-GC retained heap and goroutine gates with independently declared bounded API/controller replacement gaps; no sustained/production certification"
            verdict["faultRecovery"] = recovered
            verdict["faultMeasurementScope"] = "declared simulated callback/reply loss joined to acknowledged sources, unchanged references and five seconds of correct native domain reads; not physical-fault or sustained acceptance"
        else:
            assert not physical_declarations, "declared physical fault omitted its acknowledged-source recovery window"
        negative = connected.get("expectedRejectionTraffic")
        if negative is not None:
            assert isinstance(negative, dict) and negative["phase"] == "preparation", "rejection traffic requires its own declared phase"
            count = negative["offered"]
            assert type(count) is int and 0 < count <= 32
            assert all(type(negative[key]) is int for key in ("completed", "rejected")) and \
                negative["completed"] == negative["rejected"] == len(negative["pushes"]) == count, "incomplete expected-rejection traffic"
            rejection_durations, rejection_names = [], set()
            before_load = at(barrier["warmupStartAt"]) if prepared else start
            for push in negative["pushes"]:
                assert push["Case"] == "locale-underscore" and push["Reason"] == "InvalidLocale", "unverified rejection classification"
                assert re.fullmatch(r"[a-f0-9]{40}", push["Commit"]) and push["Commit"] not in commits, "rejected commit aliases an accepted or reused commit"
                commits.add(push["Commit"])
                assert connected["originalHeads"].get(push["Namespace"]) == push["Parent"], "rejection does not join original canonical history"
                reference_digest = hashlib.sha256((push["Parent"] + "\trefs/heads/main\n").encode()).hexdigest()
                assert push["BeforeReferences"] == push["AfterReferences"] == reference_digest, "rejected candidate advanced canonical refs"
                assert all(re.fullmatch(r"[a-f0-9]{64}", push[key]) for key in ("SourceDigest", "SentinelDigest")), "missing rejected source/sentinel joins"
                assert push["SourceDigest"] != push["SentinelDigest"], "invalid and valid sentinel sources alias"
                for key in ("InvalidName", "SentinelName"):
                    identity = (push["Namespace"], push[key])
                    assert re.fullmatch(r"[a-z0-9][a-z0-9-]{0,62}", push[key]) and identity not in rejection_names, "rejection source identities alias"
                    rejection_names.add(identity)
                offered, completed = at(push["OfferedAt"]), at(push["CompletedAt"])
                assert before_load - datetime.timedelta(minutes=10) <= offered <= completed <= before_load, "stale or incorrectly classified rejection phase"
                assert (completed - offered).total_seconds() <= 30, "rejection exceeded its bounded deadline"
                observations = push["Observations"]
                assert len(observations) == 2 and {item["Replica"] for item in observations} == set(preflight["apiEndpoints"]), "missing or aliased rejection replica"
                assert all(item["InvalidAbsent"] is True and item["SentinelAbsent"] is True and
                           completed <= at(item["CheckedAt"]) <= before_load for item in observations), "rejected multi-document candidate partially published"
                final_observations = push["FinalObservations"]
                assert len(final_observations) == 2 and \
                    {item["Replica"] for item in final_observations} == set(preflight["apiEndpoints"]), "missing or aliased final rejection replica"
                final_boundary = end + datetime.timedelta(seconds=15 if prepared else 0)
                companion_completed = at(connected["completedAt"])
                assert final_boundary <= companion_completed <= at(metadata["translationCompletedAt"]), "final rejection evidence is outside the observed companion window"
                assert all(item["InvalidAbsent"] is True and item["SentinelAbsent"] is True and
                           final_boundary <= at(item["CheckedAt"]) <= companion_completed
                           for item in final_observations), "rejected source or sentinel appeared after traffic, or final absence proof is stale"
                rejection_durations.append((completed - offered).total_seconds())
            rejection_durations.sort()
            verdict["expectedRejectionTraffic"] = {
                "phase": "preparation", "pushes": count, "documents": count * 2,
                "replicaObservations": count * 2,
                "finalReplicaObservations": count * 2,
                "rejectionSeconds": {
                    "p95": rejection_durations[math.ceil(count * .95) - 1],
                    "p99": rejection_durations[math.ceil(count * .99) - 1],
                },
                "measurementScope": "independent rejected real Git candidates with valid sentinels, unchanged refs and both-API absence before preparation and after offered traffic/cooldown; excluded from measured accepted-push percentiles",
            }
        if traffic_samples:
            statistics = {}
            for kind, samples in traffic_samples.items():
                assert samples["acknowledgement"], "normal and near-limit traffic require independent measured samples"
                statistics[kind] = {"pushes": len(samples["acknowledgement"]),
                                    "replicaObservations": len(samples["visibility"])}
                for boundary, durations in samples.items():
                    ordered = sorted(durations)
                    statistics[kind][boundary + "Seconds"] = {
                        "p95": ordered[math.ceil(len(ordered) * .95) - 1],
                        "p99": ordered[math.ceil(len(ordered) * .99) - 1],
                    }
            verdict["measuredPushTraffic"] = statistics
            verdict["pushLatencyMeasurementScope"] = "independently reported normal and exact-one-MiB source joins; preparation excluded; diagnostic percentiles are not acceptance"
        verdict.update({
            "connectedPushes": connected["completed"], "baselineProducts": connected["baselineProducts"],
            "materialDigest": material,
            "preparationPushes": 20 if prepared else 0,
            "measurementScope": "simultaneous real Git pushes and authenticated reads, both-replica source/watch/current-status joins and unchanged targets/baseline/history; diagnostic, not sustained/fault/production acceptance",
        })
    (root / "translation-verifier.json").write_text(json.dumps(verdict, indent=2) + "\n")
except (OSError, ValueError, KeyError, TypeError, AssertionError) as error:
    (root / "translation-verifier.json").write_text(json.dumps({
        "schemaVersion": 1, "passed": False, "domainChecksPassed": False, "reason": str(error),
    }) + "\n")
    print(str(error), file=sys.stderr)
    sys.exit(1)
PY
  exit $?
fi
if [[ "${CAPACITY_IDENTITY_READINESS:-0}" != 1 ]]; then exit 0; fi
python3 - "${1:?evidence directory is required}" <<'PY'
import datetime
import json
import pathlib
import re
import sys

root = pathlib.Path(sys.argv[1])

def document(name):
    return json.loads((root / name).read_text())

def require(condition, message):
    if not condition:
        raise ValueError(message)

def parse_samples(text):
    samples = []
    for line in text.splitlines():
        match = re.match(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*?)\})?\s+([0-9.eE+-]+)(?:\s|$)', line)
        if match:
            labels = dict(re.findall(r'([a-zA-Z0-9_]+)="([^"]*)"', match[2] or ''))
            samples.append((match[1], labels, float(match[3])))
    return samples

def metric_samples(name):
    return parse_samples((root / name).read_text())

def total(samples, name, labels=None):
    return sum(value for sample_name, attributes, value in samples
               if sample_name == name and all(attributes.get(key) == wanted for key, wanted in (labels or {}).items()))

def duration(at):
    return datetime.datetime.fromisoformat(at.replace('Z', '+00:00'))

def metric_values(name):
    metric = summary[name]
    return metric.get('values', metric)

try:
    metadata = document('metadata.json')
    preflight = document('preflight-environment.json')
    identity = document('identity-preflight.json')
    companion = document('identity-connected.json')
    summary = document('summary.json')['metrics']
    require(not metadata['gitDirty'] and preflight['passed'] and identity['passed'], 'clean source and native identity preflight required')
    elapsed = (duration(metadata['loadCompletedAt']) - duration(metadata['loadStartedAt'])).total_seconds()
    seconds = identity['durationSeconds']
    require(seconds >= 1800 and elapsed >= seconds, 'full identity duration not observed')
    require(metric_values('checks')['fails'] == 0, 'identity group/action mismatch')
    require('dropped_iterations' not in summary or metric_values('dropped_iterations')['count'] == 0, 'offered identity work was dropped')
    replicas = preflight['topology']['apiReplicas']
    windows = identity.get('faultWindows', [])
    chaos = bool(windows)
    if chaos:
        faults = companion['faults']
        require(len(windows) == len(faults) == 2, 'both planned faults need physical receipts')
        require({fault['profile'] for fault in faults} == {'identity-restart', 'api-restart'}, 'identity and API faults required')
        for window, fault in zip(windows, faults):
            require(0 < window['endMillis'] - window['startMillis'] <= 30000, 'fault allowance exceeds its bounded window')
            require(duration(metadata['loadStartedAt']).timestamp() * 1000 <= window['startMillis'] <
                    window['endMillis'] <= duration(metadata['loadCompletedAt']).timestamp() * 1000,
                    'fault allowance is outside the actual load')
            require(fault['injected'] and fault['processChanged'] and fault['recoveredOnBoth'], 'physical injection/recovery proof missing')
            require(window['startMillis'] <= fault['injectedMillis'] <= fault['recoveredMillis'] <= window['endMillis'], 'fault/recovery outside the planned window')
            require(0 <= fault['recoveredMillis'] - fault['dependencyReadyMillis'] <= 30000, 'authentication recovery exceeds 30 seconds')
        require(sum(metric_values(f'identity_unavailable{{replica:{index}}}')['count'] for index in range(replicas)) > 0,
                'API fault did not exercise actual unavailability')
    results = []
    for index in range(replicas):
        before = metric_samples(f'identity-before-{index}.prom')
        after = metric_samples(f'identity-after-{index}.prom')
        segments = [(before, after)]
        if chaos and index == 0:
            restart = companion['apiRestartMetrics']
            before_restart = parse_samples(restart['before'])
            after_restart = parse_samples(restart['after'])
            start = lambda samples: total(samples, 'process_start_time_seconds')
            require(start(before) == start(before_restart) and start(after) == start(after_restart) and
                    start(before) != start(after), 'metrics must bracket the actual old/new processes')
            segments = [(before, before_restart), ([], after)]
        def delta(name, labels=None):
            values = [total(end, name, labels) - total(start, name, labels) for start, end in segments]
            require(all(value >= 0 for value in values), 'counter reset inside a process segment')
            return sum(values)
        requests = metric_values(f'identity_requests{{replica:{index}}}')['count']
        decisions = metric_values(f'identity_decisions{{replica:{index}}}')['count']
        groups = metric_values(f'identity_groups{{replica:{index}}}')['count']
        require(requests >= identity['ratePerReplica'] * seconds, 'principal throughput below declared rate')
        unavailable = metric_values(f'identity_unavailable{{replica:{index}}}')['count'] if chaos else 0
        budget = sum((window['endMillis'] - window['startMillis']) / 1000 * identity['ratePerReplica']
                     for window in windows if index in window['replicas'])
        require(0 <= unavailable <= budget, 'unavailability exceeds confirmed fault budget')
        require(requests == decisions + unavailable == groups + unavailable, 'not every principal request has matching groups and decision')
        call_rate = delta('gitstore_userdir_requests_total') / elapsed
        require(0 <= call_rate < 50, 'directory sustained call rate exceeded the per-replica bound')
        for cache, limit in [('hit', '0.01'), ('miss', '0.1')]:
            labels = {'cache': cache, 'outcome': 'success'}
            count = delta('gitstore_userdir_resolution_duration_seconds_count', labels)
            bucket = delta('gitstore_userdir_resolution_duration_seconds_bucket', {**labels, 'le': limit})
            require(count > 0 and bucket >= count * .99, f'{cache} resolution p99 exceeds the declared bound')
        for name, bound in [('gitstore_userdir_cache_entries', '256'), ('gitstore_userdir_serviceaccount_buffer_depth', '64')]:
            count = delta(name + '_count')
            bounded = delta(name + '_bucket', {'le': bound})
            require(count > 0 and bounded == count, f'{name} missing or exceeded its observed bound')
        delivered = delta('gitstore_userdir_serviceaccount_delivery_total', {'outcome': 'applied'})
        require(delivered > 0, 'replica did not handle live workload events during load')
        results.append({'replica': index, 'principalRequests': requests, 'unavailable': unavailable,
                        'directoryCallsPerSecond': call_rate, 'liveDeliveries': delivered,
                        'deliveryDrops': sum(delta('gitstore_userdir_serviceaccount_delivery_total', {'outcome': outcome})
                                             for outcome in ['overflow', 'exhausted', 'discontinuity'])})
    require(companion['sourceAuthorityPassed'] and companion['groupsPassed'] and companion['laterDeliveryPassed'], 'live source/group/delivery correctness failed')
    require(companion['gitProcessUnchanged'] and companion['sharedStoragePreserved'], 'singleton Git or shared identity storage changed')
    require(companion['events'] > 0, 'no live event workload')
    verdict = {'schemaVersion': 1, 'passed': True, 'counts': identity['counts'], 'replicas': results, 'chaos': chaos,
               'measurementScope': 'SDK full profile lookup histograms; process-segmented counters when replaced; whole-run mean directory request rate'}
    (root / 'identity-verifier.json').write_text(json.dumps(verdict, indent=2) + '\n')
except (OSError, ValueError, KeyError, TypeError) as error:
    (root / 'identity-verifier.json').write_text(json.dumps({'schemaVersion': 1, 'passed': False, 'reason': str(error)}) + '\n')
    print(str(error), file=sys.stderr)
    sys.exit(1)
PY
