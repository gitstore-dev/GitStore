# Contract: Namespace Manifest

Namespace is a top-level resource stored as YAML frontmatter in the trusted control-plane Git location defined by GH#172. Authors provide `apiVersion`, `kind`, author-writable metadata, and `spec`; the API hydrates system metadata and `status`.

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

Authors MUST omit:

- `metadata.uid`
- `metadata.resourceVersion`
- `metadata.generation`
- `metadata.creationTimestamp`
- `metadata.revision`
- `metadata.ownerReferences`
- `metadata.finalizers`
- `status`

## Update manifest

The immutable identity and kind remain unchanged. Mutable desired state is changed through Git:

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

Omitted nested default fields mean "no Namespace override"; they do not mean false, zero, or deletion of operator policy.

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
  observedGeneration: 0
  lastAppliedRevision: null
  conditions: []
```

## Ownership and mutability

| Field group | Owner | Mutability |
|---|---|---|
| `apiVersion`, `kind` | Contract | Immutable |
| `metadata.name` | Author | Immutable after creation |
| `metadata.labels`, `metadata.annotations` | Author | Mutable through Git |
| Remaining metadata | System | Read-only |
| `spec` | Author | Mutable through Git |
| `status` | System | Status-write path only |

Validation, admission, Git placement, policy ceilings, and status transitions are defined by GH#172/GH#173/GH#249 rather than this schema contract.

The current flat GraphQL Namespace fields remain available with deprecation reasons during this additive migration. They may be removed only in a future major GraphQL API release.
