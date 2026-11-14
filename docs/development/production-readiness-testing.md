# Production Readiness Testing Patterns

This contributor guide describes validation requirements and reusable test
patterns, **not a declaration of production acceptance**. For maintenance and
recovery procedures, use the [operations index](../runbooks/README.md).

API and controller changes must verify concurrent-replica correctness and
rolling upgrades on affected paths. Core-service validation must cover
pluggable authentication/authorization, bounded work and sustained push or
reconciliation load where applicable. Link the tests and complete deployed
evidence that establish each claim; record remaining limitations explicitly.

The Git service is stateful and singleton-only. Preserve its repositories and
stop the old process before starting its replacement. Multiple containers,
shared volumes or disjoint volumes do not establish Git HA, replication or
sharding. Controller checkpoints are private, retained per-replica state, not
fleet-wide write fencing. Related-resource aggregates must be materialized
asynchronously into status, never calculated with live queries in resolvers.

Select the patterns below according to the changed writers, readers, storage
shape and trust boundaries. A small resource is not exempt merely because its
implementation is short.

## 1. Multi-replica correctness

Exercise independently constructed reconcilers or handlers against the same
conflicting resource and shared conditional-write boundary. Protect fake-client
state with a mutex, or use the real datastore's UID/resource-version checks.
Assert side effects and conflict outcomes, not just that both calls returned.

Current examples include:

- [Product category-resolution races](../../gitstore-controller-manager/tests/integration/product_category_resolution_race_test.go):
  `TestIntegration_ProductCategoryResolution_ExactlyOneReplicaWinsPerAttempt`
  starts two independently cached reconcilers from a shared channel, then
  requires one applied status patch, one success and one conflict requeue.
- [Product final-deletion races](../../gitstore-controller-manager/tests/integration/product_deletion_lifecycle_test.go):
  `TestIntegration_ProductDeletion_FinalRemovalOccursOnceDuringReplicaRace`
  releases two reconcilers from a shared start channel, requires exactly one
  final removal and one losing retry, then proves a post-delete retry is a
  no-op. The companion handoff test rejects stale state.
- [Category deletion restart](../../gitstore-controller-manager/tests/integration/categorytaxonomy_deletion_test.go):
  `TestIntegration_CategoryDeletionResumesAfterControllerRestart` resumes
  bounded cleanup with a second reconciler and verifies completion counts.
- [Runner/manager restart](../../gitstore-controller-manager/tests/integration/reconcile_retry_resume_test.go):
  `TestIntegration_Restart_ResumesFromCheckpoint_NoLostOrDuplicateWork`
  tears down the first generation before starting the next against retained
  checkpoints.

These prove different things. A start channel launches concurrent contenders,
but does not force both to reach a specific read/write boundary before either
finishes. Sequential restart tests prove handoff safety, not overlapping
replica safety. In-process fakes do not establish deployed datastore or
multi-process correctness.

For deterministic overlap coverage:

1. Give each instance its own cached snapshot, identity and execution context.
2. Place a barrier at the conflicting client/datastore operation so both
   contenders have arrived before releasing either write.
3. Require exactly one conditional-write winner and a typed conflict/retry
   for the loser. Assert unchanged authored spec, UID, revision and finalizers
   when a stale status write loses.
4. Test cancellation, retry, same-name recreation with a new UID, and a
   delayed acknowledgement after newer work arrives.
5. Separately test retained-checkpoint restart and actual simultaneous
   deployed replicas. For controller replacement, retain separate checkpoint
   roots and keep the peer active.

Do not substitute a sequential test or a successful restart for an unverified
overlap gate.

## 2. Capacity/soak testing

### Repository-wide capacity and fault harness

Use the canonical dispatcher, existing profiles and shared helpers:

```bash
make capacity TARGET=api PROFILE=readiness MODE=diagnostic \
  CAPACITY_ENV_FILE=/absolute/path/to/non-committed-capacity.env
```

The dispatcher supports `api/readiness`, `api/endpoint-split`,
`api/authorization`, `namespace/admission`, `namespace/validation`,
`namespace/watch`, `namespace/recovery`, `repository/lifecycle`,
`product/lifecycle`, `category/hierarchy` and `scylla/soak`.
`api/authorization` is diagnostic-only until reload-soak and zero-I/O acceptance
evidence are complete. `scylla/soak` also refuses alpha/production evidence
until its observed preloaded Product count is verified.

The runner uses pinned k6 unless a local binary or `K6_BIN` is supplied.
Evidence is written under
`.gitstore/capacity/<target>/<profile>/<mode>/<run-id>/`. Keep tokens, assertions,
private keys and raw provider records outside logs and evidence.
Use [make help and command reference](make-reference.md) for selectors and the
[capacity guide](../../tests/capacity/README.md) for the maintained topology,
profile-specific variables, thresholds and evidence format.

- **Diagnostic:** bounded investigation only; never passing capacity evidence.
- **Alpha:** provisional local-environment acceptance, not production
  certification. Namespace watch permits visibility p95 up to two seconds,
  with a warning above the unchanged one-second production target.
- **Production:** the full declared topology, workload, duration, dataset and
  fault/recovery contract. Namespace watch keeps visibility p95 at most one
  second and p99 at most three seconds.

Do not generalize one profile's topology or exemption to another.
Namespace watch/recovery and Repository lifecycle require at least two APIs,
three Scylla nodes, release services and alpha at least one shard per node
(production at least two). Controllers remain independent; CDC materializer
leases do not fence controller status writes. API readiness has its own
preflight; its non-mutating smoke exemption does not waive a mutating
profile's domain verifier.

A complete run must retain:

1. The exact source revision and clean verifier checkout for alpha/production,
   sanitized effective configuration, environment, tool and release-artifact
   identities, positive resource bounds and declared authentication mode.
2. Independently verified live API/process identities, not aliases of one
   endpoint. Compare process-instance UUIDs; use process start time for
   replacement proof, not as a collision-safe identity.
3. Actual dataset/preload observations and a nonzero acknowledged workload.
   A configured count, empty audit result or bare test `PASS` is insufficient.
4. Offered versus completed work, missed schedules, drops, latency including
   queueing, errors, domain correctness, retained memory, CPU, goroutines,
   queues and datastore pressure.
5. Continuous observations across sustained load and declared bursts,
   bounded replay, slow-consumer overflow, replacement and cursor recovery.
   Watch gates retain the 1,000-subscriber and 10,000-event replay envelope;
   production recovery retains its 1,000-transition overflow floor.
6. Before/after datastore health, no unexpected restarts/OOMs, peer health
   during replacement and a successful domain verifier. Readiness must include
   its boolean state and credential readiness, not HTTP status alone.

Fault injection uses the existing reviewed profiles, for example:

```bash
make chaos CHAOS_PROFILE=api-restart \
  CHAOS_TARGET=gitstore-capacity-api-a CHAOS_CONFIRM=1
```

Only target explicitly owned disposable fixtures. Pumba's successful exit
proves injection, not recovery. Preserve retained datasets, Git histories and
other users' containers/volumes; do not turn a failed gate into broad cleanup.
See [chaos instructions](../../tests/chaos/README.md).

### Scylla diagnostic pattern and open scale-verification gate

[Scylla capacity tests](../../gitstore-api/internal/datastore/scylla/capacity_test.go)
use the `scylla` build tag, a reachable `SCYLLA_TEST_ADDR` (or
`GITSTORE_TEST_SCYLLA_ADDR`) and the separate
`GITSTORE_SCYLLA_CAPACITY_RUN=1` opt-in. They enforce a declared minimum of
five million Products and at least two clients, bound pages and recorded large
partitions, run concurrent reservation mutations and optionally soak.

The test has no preload phase or observed Product-count assertion.
Empty tables can satisfy its bounded-page and recorded-partition checks;
reservation mutation throughput does not establish Product dataset size.
`gitctl scylla-projection-audit` verifies consistency, not row count.
Consequently, use only:

```bash
make capacity TARGET=scylla PROFILE=soak MODE=diagnostic
```

The outstanding code gate is an explicit bounded preload observation, logged
and compared with the declared scale before load. A manual count can accompany
diagnostics but cannot turn the current harness into alpha/production evidence.
New resource capacity tests must include this verification rather than copying
the gap.

### Choosing defaults

Hold workload, topology, dataset and hardware fixed. Vary one related group of
configuration values, repeat each candidate at least three times, and compare
latency, errors, recovery and resource pressure as well as throughput. Choose
the smallest setting meeting the envelope with headroom and bounded overload.
Do not rewrite defaults from one passing run or hide an undersized datastore
by lowering workload or weakening gates.

## 3. Rolling-upgrade compatibility

Verify both directions on every affected backend:

1. New code reads old-shaped records safely, including omitted fields and
   bounded pagination.
2. Old code reads new-shaped records and performs its real read-modify-write
   without dropping new fields or changing unrelated state.

Current examples for the first direction are
[memdb owner-reference tests](../../gitstore-api/internal/datastore/memdb/owner_references_test.go)
and [Scylla owner-reference tests](../../gitstore-api/internal/datastore/scylla/owner_references_test.go).
They cover omitted owner references, additive decoding and safe defaults.
The Scylla suite also demonstrates named-step failure injection and roll-forward
recovery without repeating an already completed authoritative write.

Those same-binary fixtures do not establish reverse-direction or mixed-binary
rolling safety. Preserve that acceptance gate until an affected path has
explicit old/new-version evidence. Exercise the older deserializer and its
actual write, or prove column-scoped updates preserve unknown fields. Test
simultaneous versions against shared storage when claiming deployed rolling
compatibility.

Check configuration decoding, authentication/provider contracts, retained
checkpoint formats, watch cursors and finalizer behavior as well as record
shape. If an upgrade requires a fresh keyspace or cannot read an older
checkpoint, document that restriction instead of claiming a seamless rolling
upgrade. Do not clear production state to conceal incompatibility.

## 4. Namespace-isolation / security-boundary testing

Cover both service-boundary dispatch and the actual configured policy:

- [Repository field-authorizer tests](../../gitstore-api/internal/admin/graph/resolver/repository_authorization_test.go)
  exercise canonical actions for create/update/delete, reads by ID and
  namespace path, node/nodes and lists, including cross-namespace denial and
  controller status-write actions. Their fake authorizer proves boundary
  wiring, not production policy acceptance.
- [Two-user Repository integration](../../tests/integration/authz_repository_contract_test.go)
  creates separate namespaces, rejects a cross-user Namespace deletion and
  verifies own-namespace lists. The harness uses `namespaceOwnerAuthZ`, not
  the shipped RBAC policy. These assertions do not establish a cross-user
  direct-read denial or the deployed policy's behavior.

For every changed entry point, require a positive authorized case and a
negative request explicitly naming another tenant's object, by ID/path and
through status/watch paths where exposed. Check denial before side effects and
concealment where required. Same-namespace list filtering is not a substitute
for an explicit cross-user read.

File credential references use the shared
`CredentialsRef { type, secretRef }` contract. Containment validation applies to
`secretRef.namespace` against the File's resource namespace, with strict decoding
of unknown/duplicate fields and explicit null optionals. Use the current
[File validation](../../gitstore-api/internal/catalog/file.go) and
[secret-material guide](secret-material.md), not the removed flat credentials
reference shape.

Test admission rejection separately from authenticated post-admission
read/status/watch exposure. Include an already-stored invalid reference so a
legacy or bypassed admission path cannot hide a read-boundary gap. Runtime
secret consumers must bind trusted principals and authorization, stay bounded,
fail closed and redact material. File currently has metadata validation and a
contract-only runtime consumer, not a production File reconciler.

Production provider activation, sustained policy reload/replacement and
secret-rotation capacity remain separate acceptance requirements. A local
fixture, allow-all/fake authorizer, successful key read or functional token
renewal cannot certify them.

## Related

- [Capacity profiles and evidence](../../tests/capacity/README.md)
- [Category and Product lifecycle operations](../runbooks/catalog-lifecycle.md)
- [Scylla projection repair](../runbooks/scylla-projection-repair.md)
- [Controller recovery](../runbooks/controller-recovery.md)
- [Controller signing-material rotation](../runbooks/secret-material-rotation.md)
- [Authorization providers and acceptance limits](../authorization.md)
