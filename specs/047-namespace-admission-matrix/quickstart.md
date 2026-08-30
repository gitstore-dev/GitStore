# Quickstart: Namespace Validation and Admission Matrix

## Test-first sequence

1. Add failing `cataloggrpc` tests proving structural failures, including
   immutable-name failures, suppress policy checks across the whole validation
   request.
2. Add failing tests for stable structural constraints, the distinct immutable
   structural reason, and policy constraints.
3. Add failing Namespace admission tests for tier demotion and updates targeting
   a terminating row, including concurrent resource-version changes.
4. Add failing resolver tests for bootstrap-only, non-empty-only, combined
   blockers, newly-started termination, and already-terminating idempotency.
5. Add the additive GraphQL deletion outcome contract and regenerate gqlgen.
6. Implement the ordered decision pipeline and shared reason helpers.
7. Implement blocker aggregation and outcome propagation.
8. Verify `AdmissionAccepted.observedGeneration` and derived `Terminating`
   remain independently visible.

## Focused checks

```bash
cd gitstore-api
go test ./internal/cataloggrpc ./internal/namespace ./internal/graph/resolver
```

Verified on 2026-08-29:

- request-wide structural failures suppress Namespace policy evaluation;
- non-Namespace resources never invoke Namespace policy;
- same-path name changes return `metadata.name` / `immutable`;
- simultaneous path-and-name changes are evaluated as new declarations;
- reintroduced manifests with an existing durable Namespace identity are
  evaluated as updates even when the old commit has no same-path blob;
- bootstrap, duplicate-name, tier-demotion, and terminating-target policy
  rejections return stable reasons;
- GraphQL structural, immutable, policy, not-found, and conflict errors expose
  stable extensions;
- durable resource-version conflicts remain authoritative.

US2 verification on 2026-08-29:

- the schema exposes required `DeleteNamespacePayload.outcome` with
  `TERMINATION_STARTED` and `ALREADY_TERMINATING`;
- bootstrap-only, non-empty-only, and combined blockers return one
  `NAMESPACE_DELETION_BLOCKED` error with deterministic reasons;
- eligible deletion starts termination with the expected resource-version
  write, while repeated deletion is a no-write successful no-op;
- recreated identifiers cannot cause an authorized stale UID to delete a
  replacement Namespace;
- denied callers cannot observe blocker or lifecycle details;
- deletion metrics and structured logs use only bounded outcome/reason fields.

US3 verification on 2026-08-29:

- accepted create and update admission persist `AdmissionAccepted=True` with
  `status.observedGeneration` and condition `observedGeneration` matching the
  accepted Namespace generation;
- rejected tier-demotion and terminating-target updates preserve the last
  accepted generation, resource version, status, and Git revision;
- a later Namespace query reads persisted admission status without relying on
  the originating mutation response;
- GraphQL exposes persisted `AdmissionAccepted` and derived `Terminating` as
  separate simultaneous conditions;
- Namespace reconciliation preserves the complete `AdmissionAccepted`
  condition while updating `SystemRepoReady` and `Ready`.

US3 focused commands:

```bash
cd gitstore-api
go test ./internal/namespace ./internal/graph/resolver

cd ../gitstore-controller-manager
go test ./internal/namespace
```

Focused command:

```bash
cd gitstore-api
go test ./internal/cataloggrpc ./internal/namespace ./internal/graph/resolver ./internal/middleware/security
```

Run GraphQL generation and contract checks through the repository's existing
Makefile target if the shared schema changes.

## Manual scenarios

The scenarios below are automated as regression tests so their evidence is
repeatable without exposing production Namespace contents.

### Structural failure

Submit a Namespace with an invalid identifier. Confirm the response carries
`NAMESPACE_STRUCTURAL_VALIDATION_FAILED` and no policy reason.

### Policy failure

Attempt to demote an existing `ORGANIZATION` Namespace to `USER`. Confirm the
response carries `NAMESPACE_POLICY_REJECTED` with `TIER_DEMOTION`.

### Combined deletion blockers

Ensure bootstrap namespace `default` owns at least one repository, then attempt
deletion. Confirm the GraphQL error extensions include both
`BOOTSTRAP_NAMESPACE` and `NAMESPACE_NOT_EMPTY`.

### Idempotent deletion

Delete an eligible empty Namespace twice. Confirm the first payload returns
`TERMINATION_STARTED`, the second returns `ALREADY_TERMINATING`, and the second
request does not advance `resourceVersion`.

### Server-first rollout

Validate the legacy `deletedIdentifier` selection against both old and new
schemas. Confirm an `outcome` selection fails against the old schema and is not
activated until every API replica exposes the field.

### Two-phase repository-fence rollout

1. Install a fleet-wide ingress/AuthZ deny for `deleteNamespace`,
   `completeNamespaceDeletion`, `createRepository`, and `transferRepository`.
2. Apply migration 005 and roll every API replica with
   `GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE=disabled`.
3. Confirm the four mutations receive
   `NAMESPACE_REPOSITORY_FENCE_DISABLED` from new replicas and cannot reach old
   replicas because of the external deny.
4. After every replica and the migration ledger converge, set the gate to
   `enabled` everywhere, run create/delete and transfer/delete race probes, then
   remove the external deny.

For rollback, restore the external deny first and use only an artifact that
retains migration 005. Verify that a simulated 001-004 migration bundle reports
`database is ahead`, while a supported rollback bundle retaining 005 boots.

### Automated scenario evidence

Verified on 2026-08-29:

```bash
cd gitstore-api
go test -count=1 ./internal/graph/resolver \
  -run 'TestNamespaceCreateUpdateErrorsUseStableExtensions|TestDeleteNamespaceBlockerMatrix|TestDeleteNamespaceOutcomeMatrix|TestNamespaceGraphQLServerFirstRolloutPreservesLegacySelections|TestNamespaceOutcomeActivationWaitsForFullAPIFleetConvergence|TestNamespaceRepositoryFenceRolloutGateRejectsUnsafeMutations'
go test -count=1 ./internal/cataloggrpc \
  -run 'TestValidateResourcesNamespacePolicyMatrix|TestValidateResourcesSamePathNamespaceNameChangeIsImmutable'
go test -count=1 ./internal/middleware/security \
  -run 'TestNamespaceAdmissionAuthorizationHides'
```

All commands passed. They cover structural and policy rejection, combined
deletion blockers, idempotent deletion, server-first rollout gating, and
authorization-first non-disclosure.

## Production-readiness checks

- Exercise validation against two API replicas with concurrent updates and
  deletes; stale writes must conflict rather than overwrite.
- Run a 30-minute focused admission soak with 500-file requests containing at
  most 50 Namespace manifests, 10 requests/second through 20 active workers
  across two API replicas. Continue traffic while replacing one replica, and
  require under-load throughput/error recovery within 30 seconds, p95 ≤100 ms,
  p99 ≤250 ms, internal errors <0.1%, zero incorrect decisions, CPU <80%,
  retained-memory growth <10%, and post-soak goroutines within 5% of baseline.
- During rollout, keep clients on legacy selections until every API replica
  exposes `outcome`. Keep the four unsafe mutations denied fleet-wide until
  migration 005, the new binary, and the enabled fence gate have converged.
- During rollback, disable `outcome`, restore the mutation deny, and use a
  rollback artifact that retains migration 005; do not deploy an arbitrary
  001-004 binary.
- Verify authorization denial occurs before blocker/policy details are exposed.
- Confirm logs include operation, phase, stable reason, Namespace name, and
  blocker count without manifest bodies or credentials.

## Final validation

```bash
make pr-ready
```

Verified on 2026-08-29: `make pr-ready` completed successfully from the
repository root.

Final-review corrections were also verified on 2026-08-29:

```bash
cd gitstore-api
go test -count=1 -race ./...
go test -count=1 -tags=memdb ./tests/contract/datastore

cd ..
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9142
graphify update .
make pr-ready
```

The live Scylla run covered two datastore instances, 100 concurrent
repository-create versus Namespace-termination races, migration 005, and the
legacy-null fence path. Descendant GraphQL/catalog gRPC convergence, complete
authored-state versioning, shared Git/GraphQL identifier validation, and memdb
transactional lifecycle coordination all passed.

Final-audit corrections were verified on 2026-08-29:

```bash
cd gitstore-api
go test -count=1 ./...
go test -count=1 -race ./...
go test -count=1 -race -tags=memdb ./tests/contract/datastore

cd ..
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042
graphify update .
make pr-ready
```

The live Scylla suite additionally proved the supported rollback migration
bundle boots after 005, an 001-004-only bundle is rejected as database-ahead,
and 100 transfer-versus-target-termination races have exactly one winner.
