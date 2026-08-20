# Scylla Projection Audit and Repair

Use this runbook when GitStore reports projection-write, compensation, dangling,
stale, duplicate, or missing-projection signals. Run the commands from
`gitstore-api/` with credentials supplied through environment variables. Command
output is JSON and never includes the configured password.

## Safety invariants

- Keep `gc_grace_seconds` at the default 10 days.
- Complete Scylla anti-entropy repair within 7 days, leaving at least three days
  of safety margin before tombstone garbage collection.
- Keep partitions below the 100 MiB hard ceiling. Treat 10 MiB as the soft
  target for hot partitions.
- Keep the existing general-purpose compaction strategy until table-level
  measurements justify a change. Do not use TWCS for Repository, Namespace,
  mapping, or catalogue tables because they receive updates and explicit
  deletes.
- Projection repair does not replace Scylla anti-entropy repair. Run both.

## 1. Triage

1. Alert immediately on any increase in
   `gitstore_datastore_compensation_failures_total`.
2. Investigate sustained increases in
   `gitstore_datastore_projection_write_failures_total` and
   `gitstore_datastore_projection_findings_total`.
3. Record the affected `operation`, `resource_kind`, `projection`, and
   `finding_type` labels. Resource names and UIDs are intentionally present only
   in structured logs, not metric labels.
4. Check `system.large_partitions`, table tombstone warnings, read latency, and
   compaction backlog. A partition above 100 MiB is an incident; a hot
   partition above 10 MiB requires capacity review.
5. Confirm the last successful cluster repair completed less than seven days
   ago. If not, prioritize anti-entropy repair before reducing
   `gc_grace_seconds` or changing compaction.

## 2. Audit

Configure the target without putting the password on the command line:

```bash
export GITSTORE_DATASTORE__SCYLLA__HOSTS=scylla-1:9042,scylla-2:9042
export GITSTORE_DATASTORE__SCYLLA__KEYSPACE=gitstore
export GITSTORE_DATASTORE__SCYLLA__USERNAME=gitstore_operator
export GITSTORE_DATASTORE__SCYLLA__PASSWORD='<from-secret-manager>'

go run ./cmd/gitctl scylla-projection-audit > projection-audit.json
jq '{findings: (.findings | length), actions: (.actions | length)}' projection-audit.json
```

The audit compares authoritative rows with Namespace, Repository, mapping, and
catalogue query projections. Findings are deterministic:

- `missing`: an authoritative row requires a projection that is absent;
- `dangling`: a projection owner has no authoritative row;
- `stale`: the projection key or values disagree with the authoritative row;
- `duplicate`: the correct projection exists and an extra projection points to
  the same resource.

`repairable:false` means a valid competing authoritative resource claims the
same unique key. Do not delete or overwrite that owner. Resolve the
authoritative conflict first.

## 3. Dry run

```bash
go run ./cmd/gitctl scylla-projection-repair --dry-run > projection-repair-plan.json
jq '.findings[] | select(.repairable == false)' projection-repair-plan.json
jq '.actions[] | {type,kind,uid,table: (.after.table // .before.table)}' projection-repair-plan.json
```

Review every unrepairable finding and compare the plan with recent mutation
logs. Preserve the audit and plan as incident evidence.

## 4. Apply

Stop concurrent administrative renames/transfers when practical, then run:

```bash
go run ./cmd/gitctl scylla-projection-repair --confirm > projection-repair-result.json
jq '{plannedActions,appliedActions,remaining: (.verification.findings | length)}' projection-repair-result.json
```

Apply is confirmation-gated. It checks the authoritative resource version (or
continued absence for dangling owners), uses identity-conditional deletes and
updates, and uses `IF NOT EXISTS` inserts. A conditional miss or concurrent
writer is an error; it is never silently skipped. Re-audit and generate a fresh
plan rather than replaying a stale plan.

## 5. Verify

Successful apply includes an empty post-repair verification plan. Independently
verify:

```bash
go run ./cmd/gitctl scylla-projection-audit | tee projection-audit-after.json
jq -e '(.findings | length) == 0 and (.actions | length) == 0' projection-audit-after.json
```

Also exercise direct UID lookup, path lookup, reverse mapping, and a bounded
page for affected resource kinds. Confirm finding and repair-backlog alerts
return to baseline.

## 6. Rollback and failed apply

The tool does not overwrite a valid competing owner and does not emit a blind
inverse plan. If apply stops:

1. Keep the partial JSON result and application logs.
2. Do not manually restore stale projection values over a newer authoritative
   version.
3. Re-run audit; the authoritative rows remain the source of truth and produce
   a new convergence plan.
4. If an operator mistake changed authoritative data outside this tool, restore
   that data from the approved snapshot/backup first, then audit and repair
   projections.
5. Escalate any conditional mutation failure that persists after writers are
   quiesced, or any non-empty post-repair verification.

## 7. Capacity and compaction follow-up

Run the gated capacity suite in a dedicated, preloaded environment:

```bash
SCYLLA_TEST_ADDR=127.0.0.1:9042 \
GITSTORE_SCYLLA_CAPACITY_RUN=1 \
GITSTORE_SCYLLA_CAPACITY_PRODUCTS=5000000 \
go test -tags scylla -run 'TestScyllaCapacity' -count=1 -timeout 30m \
  ./internal/datastore/scylla
```

Set `GITSTORE_SCYLLA_CAPACITY_SOAK_DURATION=2h` only for an intentional soak.
Review partition size, tombstones, pending compactions, and read/write latency
before changing compaction. Prefer measured table-specific changes; never adopt
TWCS for update/delete-heavy projection tables.
