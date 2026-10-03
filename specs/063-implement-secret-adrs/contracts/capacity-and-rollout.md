# Contract: Alpha Delivery, Replica Safety and Capacity Evidence

## Alpha contract and supported baseline

Controller configuration has a canonical rename contract defined in
[configuration.md](configuration.md): nested typed settings, no legacy aliases,
and binary-matched config snapshots for rolling replacement and rollback.
Preserve identity and checkpoint values when renaming fields.

GitStore is alpha and has no production deployments. File breaking changes are
permitted until Release Candidate. Every entry point rejects bare references
directly; update development fixtures and GraphQL clients to the typed wrapper.
No preparation release, transitional schema, migration audit, mirror scan,
File inventory API, write freeze or production cutover gate is required.
This supersedes the earlier migration design; it does not waive authorization,
bounded work, independent builds or multi-replica correctness.

Use the reviewed post-plan baseline `d065d08` or a compatible descendant.
The identity prerequisite below does not permit reverting #432 protobuf,
#433 ownership, #435 shutdown or #437 title/pagination changes. Endpoint/schema
relocation follows ADR-0012 independently; current `/graphql` remains valid.

The normalized #429 identity protocol remains required; no bridge to older
exchange fields is added. GraphQL selections use
`credentialsRef { kind type secretRef { kind name key namespace } }`.
The former `credentialsRef { name }` query is intentionally incompatible.
Historical Git commits are retained; resubmitted bare manifests fail admission.
Do not automatically rewrite development data or Git history.

## Capacity interface

Extend the existing public command:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=production \
  REPOSITORY_CAPACITY_SECRET_SCENARIO=1
```

`REPOSITORY_CAPACITY_SECRET_SCENARIO` selects the connected workload, owned fault
driver, resource collector and final bundle validation. It requires explicit
`CHAOS_CONFIRM=1`, private provisioned owned fixtures and the acknowledged dataset.
Missing prerequisites or failed observations are rejected; local assertion
fixtures do not constitute deployed evidence. Require the flag for spec-063
acceptance. Preserve its wiring through Makefile,
dispatcher, stack setup, sanitized manifests, evidence validation, domain
verifier and docs. Existing runs without it retain their existing meaning and
cannot count as spec-063 passes.

Use `tests/integration/repository_lifecycle_capacity_test.go` and its existing
helpers. Add actual Git pushes of File manifests; Repository metadata-only
mutations cannot prove reference validation. Also run focused Go contract/race
tests for runtime material consumers, which have no deployed File consumer.

## Workload and topology

| Dimension                 | Production requirement                                                                                                            |
|---------------------------|-----------------------------------------------------------------------------------------------------------------------------------|
| Dataset                   | At least 5,000,000 Product rows in durable datastore; seeded and counted before offered load                                      |
| Foreground                | Bounded rotating pool of 100 Files; batches of <=10 Files and <=128 KiB/push, explicit typed references                           |
| Sustained load            | 10 Git pushes/s for 60m; no hidden unbounded client queue                                                                         |
| Burst                     | 100 pushes over 1s once per minute, drain within 30s                                                                              |
| Client bounds             | 32 push workers, queue <=256 batches; shed/drop is measured and fails the normal-load gate                                        |
| APIs/controllers          | >=2 real processes each, unique process IDs and shared durable identity/projection state                                          |
| Git service               | Exactly one active instance, shared by both APIs; existing singleton routing/callback checks remain mandatory; no Git HA claim    |
| Resource-runtime resolver | 32 contract-consumer callers contend on 16 in-flight slots; explicit saturation/deadline checks                                   |
| Identity renewal          | Test-issued access TTL 60s, subject to configured server bounds, forcing repeated exchanges on both controllers                   |
| Stabilization             | 5m before and 10m after offered load                                                                                              |

Every admitted Product fixture includes valid `spec.title`. Establish dataset
counts through acknowledged fixture manifests and an offline bounded paginated
verifier, not a synchronous GraphQL count or Scylla counter (ADR-0017).

Use replica-owned regular local provider files on controlled mounts. Do not
point failure injection at an operator's real credentials. Negative namespace/
type/auth cases are labeled separate correctness traffic, not blended into the
success error rate.

Git service is stateful and singleton-only in every mode, including production.
Repository sharding and placement-aware routing are not implemented. Preserve
the existing lifecycle preflight's single Git endpoint, common API Git URI and
CatalogService callback to API A. Do not add a second Git service or loosen
those checks. Disjoint volumes are not a supported multi-Git topology.
This corrects the earlier two-Git requirement under constitution v3.0.0; it does
not weaken API/controller replica evidence or the workload/latency thresholds.

## Thresholds

| Signal                        | Required result                                                                                            |
|-------------------------------|------------------------------------------------------------------------------------------------------------|
| Pure reference validation     | p95 <=5ms, p99 <=20ms per File                                                                             |
| Healthy local resolution      | p95 <=10ms, p99 <=50ms                                                                                     |
| Git push acknowledgment       | p95 <=2s, p99 <=30s including bursts                                                                       |
| Existing lifecycle visibility | Preserve production p95 <=1s/p99 <=3s; alpha p95 <=2s/p99 <=3s                                             |
| Unexpected healthy errors     | <0.1%; zero dropped offered batches                                                                        |
| Data integrity                | Zero acknowledged projection loss, cross-namespace success or secret leakage                               |
| Provider concurrency          | <=16 per instance; zero internal queued operations                                                         |
| Identity exchange             | <=1 in flight per source; retry delay <=30s including jitter                                               |
| Resource saturation           | Normalized CPU <80%, warmed RSS growth <10%; post-load goroutines return within 10% of stabilized baseline |
| Recovery                      | Both controllers regain authenticated progress <=60s after valid record restoration                        |

Separately measure fault windows: intentional denied operations are expected
and must fail closed, while no unaffected healthy traffic may be silently
excluded. Preserve the existing lifecycle gate's stricter invariants where
applicable. These are targets, not measured results.

## Fault schedule and domain verifier

1. Prove fresh baseline token issuance and active reconciliation on both
   controllers. Verify a typed File projection reads identically from both APIs.
2. At minute 15, withdraw only the test-owned signing record for controller A
   for 90s, spanning the configured token lifetime; controller B remains usable.
   Atomic rename/restore is performed by the fixture owner, with cleanup on
   cancellation. No service gets provider write privileges.
3. Verify bounded attempts, classified failure, no usable-expiry extension,
   false credential readiness after exhaustion, and no token/PEM in telemetry.
   Restore the record and prove A obtains a fresh token and resumes work <=60s.
4. At minute 30, enroll a new public key, atomically replace its complete
   key/ID record, observe both relevant replicas renew, then remove the old key
   after overlap. Run both authorized and wrong-subject issuance tests.
5. At minute 45, invoke planned
   `make chaos CHAOS_PROFILE=controller-restart CHAOS_TARGET=<explicit-container> CHAOS_CONFIRM=1`.
   Require changed process identity, fresh bootstrap authentication, and resumed
   reconciliation <=60s. Keep existing API replacement/overflow checks too.
   Preserve group-specific checkpoint scope, related replay keys and #435's
   final-flush behavior; stale replay must remain idempotent/version-checked.
6. Replace API replicas while exercising the supported strict File contract;
   assert no projection field loss or authorization regression. Do not claim
   compatibility with the obsolete bare-reference alpha schema.

Add `tests/chaos/profiles/controller-restart.json` with existing `restart`
action and 60s recovery objective. Provider record outage is a domain fixture,
not a fabricated network fault or unsupported Pumba action. Fault injection
success alone is never a pass.

Store sanitized counts, timings, provider categories/reasons, replica/build
identities, workload configuration, fault/recovery
markers and correctness results under the existing evidence directory.
Do not store private records, token responses, assertions, raw environment or
full config dumps. Reuse token-file handling and credential scanning.

Diagnostic evidence cannot pass; alpha cannot claim the five-million-row,
60-minute production gate. Non-diagnostic runs require the existing clean
checkout, release-image, topology and datastore health evidence plus the new
secret-scenario fields. Missing fields/fault proofs fail closed.
