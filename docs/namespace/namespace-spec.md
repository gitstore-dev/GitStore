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
