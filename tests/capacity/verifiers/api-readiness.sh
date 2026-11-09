#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail
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
