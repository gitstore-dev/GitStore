# Contract: Scylla Operational Signals

## Structured logs

Projection inconsistencies and compensation failures MUST include:

- operation
- resource kind
- stable resource identity
- projection/table
- lookup key
- finding type
- primary error
- compensation outcome

No dangling or stale lookup may be skipped silently.

## Metrics

The API metrics surface MUST expose counters or histograms equivalent to:

- projection write failures by operation/resource/projection
- compensation attempts and failures
- dangling/stale/duplicate projection findings
- mutation latency by operation

Metric labels MUST remain bounded; stable resource IDs and names belong in logs, not labels.

## Operational thresholds

- Partition hard ceiling: 100 MB.
- Hot-partition soft target: 10 MB.
- Repair must complete within 7 days while `gc_grace_seconds` remains 10 days.
- Alert on any compensation failure.
- Alert on sustained growth in dangling findings or repair backlog.

## Runbook requirements

The runbook MUST document:

1. checking large partitions and tombstone pressure,
2. verifying repair cadence,
3. auditing authoritative rows against query projections,
4. distinguishing missing, dangling, stale, and duplicate projections,
5. dry-run repair,
6. applying conditional repair without overwriting concurrent valid state,
7. validating consistency after repair.

## Compaction guardrails

- Keep the existing general-purpose strategy unless measurements justify a change.
- Do not use TWCS for tables with explicit deletes or updates.
- Any `gc_grace_seconds` reduction requires a repair cadence shorter than the new value and a documented maximum node-outage assumption.
