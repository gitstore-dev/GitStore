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

def metric_samples(name):
    samples = []
    for line in (root / name).read_text().splitlines():
        match = re.match(r'^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{(.*?)\})?\s+([0-9.eE+-]+)(?:\s|$)', line)
        if match:
            labels = dict(re.findall(r'([a-zA-Z0-9_]+)="([^"]*)"', match[2] or ''))
            samples.append((match[1], labels, float(match[3])))
    return samples

def total(samples, name, labels=None):
    return sum(value for sample_name, attributes, value in samples
               if sample_name == name and all(attributes.get(key) == wanted for key, wanted in (labels or {}).items()))

def duration(at):
    return datetime.datetime.fromisoformat(at.replace('Z', '+00:00'))

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
    require(summary['checks']['values']['fails'] == 0, 'identity group/action mismatch')
    require(summary.get('dropped_iterations', {'values': {'count': 0}})['values']['count'] == 0, 'offered identity work was dropped')
    replicas = preflight['topology']['apiReplicas']
    results = []
    for index in range(replicas):
        before = metric_samples(f'identity-before-{index}.prom')
        after = metric_samples(f'identity-after-{index}.prom')
        requests = summary[f'identity_requests{{replica:{index}}}']['values']['count']
        decisions = summary[f'identity_decisions{{replica:{index}}}']['values']['count']
        groups = summary[f'identity_groups{{replica:{index}}}']['values']['count']
        require(requests >= identity['ratePerReplica'] * seconds, 'principal throughput below declared rate')
        require(requests == decisions == groups, 'not every principal request has matching groups and decision')
        call_rate = (total(after, 'gitstore_userdir_requests_total') - total(before, 'gitstore_userdir_requests_total')) / elapsed
        require(0 <= call_rate < 50, 'directory sustained call rate exceeded the per-replica bound')
        for cache, limit in [('hit', '0.01'), ('miss', '0.1')]:
            labels = {'cache': cache, 'outcome': 'success'}
            count = total(after, 'gitstore_userdir_resolution_duration_seconds_count', labels) - total(before, 'gitstore_userdir_resolution_duration_seconds_count', labels)
            bucket = total(after, 'gitstore_userdir_resolution_duration_seconds_bucket', {**labels, 'le': limit}) - total(before, 'gitstore_userdir_resolution_duration_seconds_bucket', {**labels, 'le': limit})
            require(count > 0 and bucket >= count * .99, f'{cache} resolution p99 exceeds the declared bound')
        for name, bound in [('gitstore_userdir_cache_entries', '256'), ('gitstore_userdir_serviceaccount_buffer_depth', '64')]:
            count = total(after, name + '_count') - total(before, name + '_count')
            bounded = total(after, name + '_bucket', {'le': bound}) - total(before, name + '_bucket', {'le': bound})
            require(count > 0 and bounded == count, f'{name} missing or exceeded its observed bound')
        delivered = total(after, 'gitstore_userdir_serviceaccount_delivery_total', {'outcome': 'applied'}) - total(before, 'gitstore_userdir_serviceaccount_delivery_total', {'outcome': 'applied'})
        require(delivered > 0, 'replica did not handle live workload events during load')
        results.append({'replica': index, 'principalRequests': requests, 'directoryCallsPerSecond': call_rate, 'liveDeliveries': delivered})
    require(companion['sourceAuthorityPassed'] and companion['groupsPassed'] and companion['laterDeliveryPassed'], 'live source/group/delivery correctness failed')
    require(companion['gitProcessUnchanged'] and companion['sharedStoragePreserved'], 'singleton Git or shared identity storage changed')
    require(companion['events'] > 0, 'no live event workload')
    verdict = {'schemaVersion': 1, 'passed': True, 'counts': identity['counts'], 'replicas': results,
               'measurementScope': 'SDK full profile lookup histograms; sustained per-replica native directory request rate'}
    (root / 'identity-verifier.json').write_text(json.dumps(verdict, indent=2) + '\n')
except (OSError, ValueError, KeyError, TypeError) as error:
    (root / 'identity-verifier.json').write_text(json.dumps({'schemaVersion': 1, 'passed': False, 'reason': str(error)}) + '\n')
    print(str(error), file=sys.stderr)
    sys.exit(1)
PY
