# Contract: Namespace Validation and Admission Matrix

## Ordered phases

| Operation | Structural/pre-receive phase | Policy phase | Success effect |
|---|---|---|---|
| Create | Envelope, API version/kind, shared DNS/reserved identifier rules, required spec, tier enum, authoring repository/path, duplicate request identity | Existing spec 046 bootstrap-name rejection and durable name uniqueness | Create complete authored row; set `AdmissionAccepted=True` |
| Update | Same shape checks as create; same-path `metadata.name` changes receive `IMMUTABLE_NAME` | Existing row required; tier demotion rejected; terminating target rejected | Conditional complete-authored update; advance generation only for authored changes; set `AdmissionAccepted=True` |
| Delete | Identifier, authorization context, and authorized UID/name continuity | Already terminating is idempotent; bootstrap and non-empty blockers are evaluated together | Conditional repository-fenced termination marker/finalizer write |

If any structural error exists in a validation request, including
`IMMUTABLE_NAME`, policy checks are not evaluated.

Git admission classifies a declaration as an update when its durable Namespace
identity already exists, even if the old commit has no same-path manifest. This
allows a previously deleted or cross-ref manifest to be reintroduced. Explicit
create APIs still reject duplicate durable names.

The bootstrap create/update policy entry documents behavior already owned by
spec 046. It does not change FR-010's existing reserved-identifier validation or
introduce an additional protected-namespace mechanism.

GraphQL and Git call the same identifier helper. Malformed names use
`metadata.name:dns-label`; reserved non-bootstrap names use
`metadata.name:reserved`. Bootstrap names pass structural validation and retain
the policy constraint `metadata.name:policy/bootstrap-namespace`.

## Git pre-receive error contract

The existing `ValidationError` protobuf remains unchanged:

```text
file_path: repository-relative path
field: dotted field path
constraint: stable rule code
message: human-readable explanation
```

Constraint conventions:

- structural: the concrete schema/shape constraint (`required`, `reserved`,
  `authoring-target`, `duplicate`, and similar);
- immutable structural reason: `immutable`;
- policy: `policy/<reason>`, for example `policy/tier-demotion` or
  `policy/namespace-terminating`.

No stateful policy check executes when the request has a structural or immutable
failure.

`metadata.name` immutability is evaluated only for old/proposed Namespace
manifests at the same repository path. A simultaneous path-and-name change is
not inferred to be a rename.

## Descendant commit convergence contract

- Exact-head admission remains the fast path.
- If the ref points to a descendant, GraphQL and catalog gRPC read the
  Namespace manifest from the current head at the original changed path.
- Only Namespace paths are converged from a stale request; stale
  non-Namespace resources remain skipped.
- Disjoint commits changing X then Y materialize both X and Y.
- If the descendant preserves X exactly, the stale X handler may materialize X
  with the stale request's actor and timestamp.
- If both commits change X, the stale handler writes nothing; only the
  exact-head handler may write the current-head content and its actor/timestamp.
- A ref advance during parse or conditional write retries from the new head;
  an ancestor manifest never overwrites descendant content.

Successful persistence includes API version, kind, labels, annotations, full
spec, body, revision, source path, commit SHA, and ref. Any authored
metadata/spec/body change advances generation and resourceVersion.
Provenance-only movement advances resourceVersion only.

## Repository creation/deletion coordination contract

- `CreateRepositoryInActiveNamespace` must durably prove the Namespace is
  active before repository commit.
- `TransferRepository` must reserve the target Namespace through the same
  active-Namespace fence before moving either the authoritative repository row
  or namespace mappings.
- `MarkNamespaceDeletion` must durably prove no repository creation crossed the
  emptiness decision before writing the termination marker.
- memdb satisfies create, transfer, and termination requirements in one write
  transaction per operation.
- Scylla increments `namespaces_by_uid.repository_creation_epoch` and
  `pending_repository_creations` with LWT while `deletion_timestamp` is null,
  then decrements only the pending counter after completion. The termination
  LWT requires the expected resourceVersion, unchanged epoch, and zero pending
  creations.
- If creation wins, deletion returns/retries as `NAMESPACE_NOT_EMPTY`; if
  termination wins, repository creation returns Namespace-terminating.
- Transfer versus target termination has the same one-winner rule.
- No process-local lock is part of the contract.

## Production rollout gate contract

`GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE` accepts:

- `auto` (default): enabled for memdb development/test, disabled for Scylla;
- `disabled`: reject Namespace deletion/completion and repository
  create/transfer;
- `enabled`: use the durable fence paths.

Gate rejection is a GraphQL error with:

```json
{
  "extensions": {
    "code": "NAMESPACE_REPOSITORY_FENCE_DISABLED",
    "reason": "ROLLOUT_GATE_DISABLED",
    "operation": "CREATE_REPOSITORY"
  }
}
```

Before the first mixed-version production replacement, operators MUST install a
fleet-wide ingress/AuthZ deny for `deleteNamespace`,
`completeNamespaceDeletion`, `createRepository`, and `transferRepository`.
Legacy binaries do not understand this gate and therefore cannot themselves
provide the mixed-window deny.

## GraphQL error contract

Rejected create/update operations return a GraphQL error with:

```json
{
  "extensions": {
    "code": "NAMESPACE_POLICY_REJECTED",
    "reason": "TIER_DEMOTION",
    "phase": "POLICY"
  }
}
```

Stable category codes:

- `NAMESPACE_STRUCTURAL_VALIDATION_FAILED`
- `NAMESPACE_IMMUTABLE_FIELD`
- `NAMESPACE_POLICY_REJECTED`
- `NAMESPACE_DELETION_BLOCKED`
- `NAMESPACE_CONFLICT`

Deletion blockers use:

```json
{
  "extensions": {
    "code": "NAMESPACE_DELETION_BLOCKED",
    "reasons": ["BOOTSTRAP_NAMESPACE", "NAMESPACE_NOT_EMPTY"]
  }
}
```

Reason ordering is deterministic: `BOOTSTRAP_NAMESPACE`, then
`NAMESPACE_NOT_EMPTY`.

## Compatibility

- The protobuf wire shape does not change.
- GraphQL error extensions are additive to existing messages.
- The deletion payload change is additive; clients that do not select `outcome`
  continue to operate.
- Old Git-service replicas continue to reject `accepted=false` validation
  responses and display the returned messages.
- Rollout is server-first: clients do not select `outcome` until every API
  replica exposes the field. Rollback disables new client selections before
  reverting API replicas.
- For the fence rollout, migration 005 is applied before activation; all new
  replicas are first deployed with the gate disabled; only after full fleet
  convergence is the gate enabled everywhere and the external deny removed.
- A supported behavior rollback artifact retains every forward migration
  through `005_namespace_repository_fence.cql`. A binary embedding only
  migrations 001-004 is not a supported rollback after 005 and gocqlx is
  expected to reject it with `database is ahead`.
- Safe rollback restores the external mutation deny before replacing replicas
  and leaves it in place until a forward-capable fenced fleet is restored.
