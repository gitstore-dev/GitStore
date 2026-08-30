# Namespace Resource Contract

Namespace is a top-level GitStore resource represented by the
`gitstore.dev/v1beta1` declarative contract. Authors manage desired state in Git; the API hydrates system metadata and
status when reading the resource.

## Create manifest

```yaml
---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme
  labels:
    gitstore.dev/owner: platform
spec:
  title: Acme Store
  tier: USER
  repositoryDefaults:
    visibility: PRIVATE
    defaultBranch: main
  pushPolicyDefaults:
    maxPackSizeBytes: 52428800
    maxFileSizeBytes: 10485760
    receivePackHooks:
      preReceive:
        enabled: true
      update:
        enabled: false
      postReceive:
        enabled: true
      procReceive:
        enabled: false
      postUpdate:
        enabled: false
      referenceTransaction:
        enabled: false
    schemaValidation:
      phase: pre-receive
      timeoutSeconds: 10
    admissionControl:
      phase: post-receive
      branchPattern: ^refs/heads/main$
---
```

## Update manifest

```yaml
---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme
  labels:
    gitstore.dev/owner: commerce-platform
spec:
  title: Acme Commerce
  tier: USER
  repositoryDefaults:
    visibility: INTERNAL
    defaultBranch: trunk
  pushPolicyDefaults:
    maxPackSizeBytes: 41943040
    admissionControl:
      phase: post-receive
      branchPattern: ^refs/heads/trunk$
---
```

Omitted nested defaults mean that the Namespace supplies no override. Authors must omit `metadata.uid`, `metadata.resourceVersion`, `metadata.generation`,
`metadata.creationTimestamp`, `metadata.revision`,
`metadata.ownerReferences`, and `metadata.finalizers`. An authored top-level
`status` block is ignored for Namespace manifests and never persisted.

## Ownership and mutability

| Field group | Owner | Mutability |
|---|---|---|
| `apiVersion`, `kind` | Contract | Immutable |
| `metadata.name` | Author | Immutable after creation |
| `metadata.labels`, `metadata.annotations` | Author | Mutable through Git |
| `metadata.uid`, `metadata.creationTimestamp` | System | Immutable |
| Remaining metadata | System | Read-only |
| `spec` | Author | Mutable through Git |
| `status` | System | Status-write path only |

Accepted writes persist the complete authored envelope, labels, annotations,
full `spec`, and Markdown body. Any authored change advances both `generation`
and `resourceVersion`. A Git path, commit, ref, or revision change with
otherwise identical authored content advances only `resourceVersion`.

Validation, immutable-field enforcement, policy ceilings, phase validation, and
admission are defined by GH#173. Watch and resume behavior is defined by
GH#174. Repository override merging and effective policy resolution are
defined by GH#249.

## Hydrated API representation

```yaml
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: acme
  labels:
    gitstore.dev/owner: platform
  annotations: {}
  uid: 6a053cdd-1f95-47f2-b3bb-d950a52a6758
  resourceVersion: "1"
  generation: 1
  creationTimestamp: 2026-08-16T01:00:00Z
  revision: null
  ownerReferences: []
  finalizers: []
spec:
  title: Acme Store
  tier: USER
  repositoryDefaults:
    visibility: PRIVATE
    defaultBranch: main
  pushPolicyDefaults: null
status:
  observedGeneration: 1
  lastAppliedRevision: main@sha1:abc123
  conditions:
  - type: AdmissionAccepted
    status: "True"
    observedGeneration: 1
    lastTransitionTime: 2026-08-16T01:00:01Z
    reason: AdmittedByHookPipeline
    message: Namespace manifest admitted successfully.
```

Existing flat Namespace GraphQL fields remain available with deprecation
reasons during the additive migration. New consumers should use
`metadata.name`, `spec.title`, `spec.tier`, and `status`. Deprecated fields may
be removed only in a future major GraphQL API release.

## Condition vocabulary

Namespace conditions use the shared open-ended `Condition.type` string. The
initial documented vocabulary is:

- `Ready`
- `AdmissionAccepted`
- `SystemRepoReady`
- `Terminating`

Admission writes `AdmissionAccepted=True`, sets `observedGeneration` to the
admitted generation, and records the admitted Git revision in
`lastAppliedRevision`. Reconciliation preserves that revision, advances only
`resourceVersion`, and sets `SystemRepoReady` and `Ready`. Accepted deletion
sets the deletion timestamp/finalizer and exposes `Terminating`.

## Admission and deletion phase matrix

| Operation | Structural/pre-receive phase | Stateful policy phase | Successful result |
|---|---|---|---|
| Create | Validates the envelope, API version/kind, identifier, reserved names, required spec, tier, authoring target, and duplicate request identity. | Rejects bootstrap targets and an existing Namespace with the same name. | Persists generation 1 with `AdmissionAccepted=True`. |
| Update | Applies the create checks and rejects a same-path `metadata.name` change as immutable. | Requires an existing active Namespace and rejects bootstrap targets, tier demotion, and terminating targets. | Conditionally advances generation and writes `AdmissionAccepted=True` for that generation. |
| Delete | Validates the identifier and authorized UID/name continuity. | Returns an idempotent outcome for an already-terminating Namespace; otherwise evaluates bootstrap and non-empty blockers together. | Marks an eligible Namespace for foreground deletion. |
| Reconcile | Not a request-time validation phase. | Waits for accepted admission before provisioning the system repository. | Updates `SystemRepoReady` and `Ready` without replacing `AdmissionAccepted`. |

Any structural failure short-circuits the request before stateful policy
evaluation. Rejected creates and updates do not persist the rejected manifest.
In particular, a rejected update leaves the last accepted generation,
`AdmissionAccepted` condition, and `lastAppliedRevision` unchanged.

## Stable failure reasons

GraphQL errors use stable category codes and reason extensions:

| Category code | Stable reasons |
|---|---|
| `NAMESPACE_STRUCTURAL_VALIDATION_FAILED` | `INVALID_ENVELOPE`, `INVALID_IDENTIFIER`, `RESERVED_IDENTIFIER`, `INVALID_TIER`, `INVALID_AUTHORING_TARGET`, `DUPLICATE_IDENTITY` |
| `NAMESPACE_IMMUTABLE_FIELD` | `IMMUTABLE_NAME` |
| `NAMESPACE_POLICY_REJECTED` | `BOOTSTRAP_NAMESPACE`, `NAMESPACE_ALREADY_EXISTS`, `NAMESPACE_NOT_FOUND`, `TIER_DEMOTION`, `NAMESPACE_TERMINATING` |
| `NAMESPACE_CONFLICT` | `RESOURCE_VERSION_CONFLICT` |
| `NAMESPACE_DELETION_BLOCKED` | `BOOTSTRAP_NAMESPACE`, `NAMESPACE_NOT_EMPTY` |

Git pre-receive validation keeps the existing protobuf shape. Structural
failures use their concrete `constraint`, immutable name changes use
`immutable`, and policy failures use `policy/<kebab-case-reason>`.
GraphQL and Git use the same DNS-label and reserved-identifier validator.
Bootstrap identifiers remain a policy rejection rather than a structural
reserved-name error.

## Descendant commit convergence

Namespace admission is ordered by the current authoring ref, not by arrival
time of post-receive work. If a submitted commit already has a descendant,
GraphQL and catalog gRPC read the Namespace manifest at the current head and the
same path. Disjoint X/Y commits therefore both materialize; if both change X,
only the newest content is eligible to win. Stale non-Namespace admission work
continues to be skipped.

## Deletion outcomes

- `TERMINATION_STARTED`: the Namespace was eligible and the request wrote its
  deletion timestamp and `gitstore.dev/foreground-deletion` finalizer.
- `ALREADY_TERMINATING`: deletion was already in progress; the request succeeds
  as a no-op and does not advance `resourceVersion`.
- `NAMESPACE_DELETION_BLOCKED`: deletion is rejected. The `reasons` extension
  contains every applicable blocker in deterministic order:
  `BOOTSTRAP_NAMESPACE`, then `NAMESPACE_NOT_EMPTY`.

Repository creation and the empty-to-terminating transition are coordinated in
the datastore. memdb performs each decision in one write transaction. Scylla
uses a per-Namespace monotonic creation epoch plus pending-reservation counter,
so a repository create and a termination marker cannot both win across API
replicas. Repository creation against a terminating Namespace is rejected.

## Status ownership

| Status field or condition | Owner | Contract |
|---|---|---|
| `AdmissionAccepted` | Namespace admission path | Persisted only after an accepted create/update; `True` and tied to the accepted generation. |
| `status.observedGeneration` and `lastAppliedRevision` | Namespace admission path | Identify the most recently accepted generation and Git revision. Rejected updates leave both unchanged. |
| `SystemRepoReady` | Namespace controller | Reports per-Namespace system repository provisioning. |
| `Ready` | Namespace controller | Reports the conjunction of admission acceptance and system repository readiness. |
| `Terminating` | GraphQL read projection from lifecycle metadata | Derived separately from the deletion timestamp; it does not replace or reinterpret `AdmissionAccepted`. |

Controller status updates merge their owned conditions with existing status and
must preserve `AdmissionAccepted` and the admission revision.
