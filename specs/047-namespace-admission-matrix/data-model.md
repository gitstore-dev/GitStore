# Data Model: Namespace Validation and Admission Matrix

This feature adds decision types, one GraphQL enum, and two internal Scylla
coordination column. It adds no public Namespace field or new table.

## ValidationDecision

| Field | Type | Rules |
|---|---|---|
| `phase` | `STRUCTURAL`, `POLICY` | Exactly one phase per reason; evaluation order is fixed. |
| `reason` | stable string code | Machine-readable and safe to expose. |
| `field` | dotted field path | Empty only for request-wide rules. |
| `message` | string | Human-readable; never used as the machine contract. |
| `filePath` | string | Present for Git validation failures. |

### Phase transitions

```text
STRUCTURAL
  failure -> reject; do not evaluate later phases
  pass    -> POLICY

POLICY
  failure -> reject without resource mutation
  pass    -> conditional create/update
```

Representative stable reasons:

- Structural: `INVALID_ENVELOPE`, `INVALID_IDENTIFIER`,
  `RESERVED_IDENTIFIER`, `INVALID_TIER`, `INVALID_AUTHORING_TARGET`,
  `DUPLICATE_IDENTITY`.
- Immutable structural reason: `IMMUTABLE_NAME`.
- Policy: `BOOTSTRAP_NAMESPACE`, `TIER_DEMOTION`,
  `NAMESPACE_TERMINATING`, `NAMESPACE_ALREADY_EXISTS`.

Tier demotion is policy, not immutable identity: upgrades are allowed, so the
decision depends on the persisted current tier.

`IMMUTABLE_NAME` has `phase=STRUCTURAL` and applies only when the old and
proposed Namespace manifests share a repository path but have different
`metadata.name` values. A simultaneous path-and-name change is a new declaration
because the manifests have no stable writable identity for rename correlation.

`BOOTSTRAP_NAMESPACE` on create/update names the existing spec 046
system-managed rejection. It is not a new protected-name mechanism and is
distinct from the unchanged reserved-identifier structural blocklist.

## NamespaceDeletionDecision

| Field | Type | Rules |
|---|---|---|
| `outcome` | `TERMINATION_STARTED`, `ALREADY_TERMINATING`, or rejected | Successful outcomes are returned in the payload; rejected decisions are GraphQL errors. |
| `blockers` | set of deletion reason codes | Contains every applicable blocker; empty on success. |
| `resourceVersion` | string | Expected version used only when starting termination. |

Deletion blocker reasons:

- `BOOTSTRAP_NAMESPACE`
- `NAMESPACE_NOT_EMPTY`

### State transition

```text
Active
  already has deletionTimestamp -> ALREADY_TERMINATING (no write)
  blockers present              -> rejected with all blockers (no write)
  no blockers                   -> conditionally require unchanged creation epoch
                                  and pending creations = 0,
                                  then set deletionTimestamp + finalizer using expected resourceVersion
                                  -> TERMINATION_STARTED

Terminating
  repeated delete               -> ALREADY_TERMINATING (no version advance)
```

## Namespace status

The persisted Namespace status remains unchanged:

| Condition | Authority | Rule |
|---|---|---|
| `AdmissionAccepted` | admission | `True` after a successful create/update, with `observedGeneration` equal to the admitted generation. |
| `SystemRepoReady` | controller | Reflects per-namespace system repository readiness. |
| `Ready` | controller | True only when admission and repository readiness are satisfied. |
| `Terminating` | derived read projection | Derived from deletion timestamp/finalizer, distinct from admission acceptance. |

Rejected input is not persisted as Namespace status because it never becomes the
resource's current configuration.

## Authored Namespace state

The persisted Namespace candidate retains every accepted authored field:

| Group | Persisted fields | Version effect |
|---|---|---|
| Envelope | `apiVersion`, `kind`, `metadata.name` | Name remains immutable; other accepted changes advance generation and resourceVersion. |
| Authored metadata | `labels`, `annotations` | Any change advances generation and resourceVersion. |
| Desired state | Full `spec`, including `repositoryDefaults` and `pushPolicyDefaults` | Any change advances generation and resourceVersion. |
| Content | Markdown `body` | Any change advances generation and resourceVersion. |
| Provenance | `revision`, `sourcePath`, `gitCommitSHA`, `gitRef` | Provenance-only change advances resourceVersion only. |

System-owned status, UID, creation fields, deletion state, owner references, and
finalizers are not treated as authored generation inputs.

## Namespace repository lifecycle fence

Scylla `namespaces_by_uid` gains:

| Column | Type | Public | Meaning |
|---|---|---|---|
| `repository_creation_epoch` | nullable `bigint` | No | Monotonic sequence incremented whenever a repository creation reserves the active Namespace. |
| `pending_repository_creations` | nullable `bigint` | No | In-flight creation reservations. Incremented before repository commit and decremented after success or definitive compensation. |

New Namespace rows initialize both values to `0`. Null values on a legacy row
are treated as the zero pre-fence state; the existing indexed repository
existence check remains authoritative for repositories created before the
columns existed.

memdb does not persist a separate field because its Namespace check and
repository insert, and its emptiness check and termination mark, occur in one
serializable write transaction.

## Concurrency invariants

- Validation decisions carry no process-local state.
- Create/update policy is rechecked against the durable Namespace row.
- A repository must reserve against an active durable Namespace before its row
  can commit.
- A repository transfer must reserve the active target Namespace before its
  authoritative row or mappings can move.
- Termination starts only when the expected resourceVersion and repository
  fence/transactional emptiness proof both succeed.
- A conflict returns a conflict outcome; it never silently overwrites a newer
  generation or deletion state.
- Descendant Git admission reads the current manifest at the same path;
  disjoint resources converge with the request audit identity, while a stale
  same-resource handler never attributes descendant content to its stale actor
  or timestamp.
- Repeated delete of the same terminating UID is idempotent.
- The authorized UID/name pair is revalidated so recreation cannot redirect an
  authorized delete to a replacement Namespace.
