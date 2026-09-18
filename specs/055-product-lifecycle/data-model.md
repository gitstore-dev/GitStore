# Data Model: Product Lifecycle

## Product

The existing `datastore.Product` remains the persisted hydrated resource. No
new Product table is introduced.

| Area | Fields / rule |
|---|---|
| Identity | `uid`, `metadata.namespace`, and `metadata.name`; UID is allocated once and remains stable across mutable updates. Namespace/name changes are rejected; a rename is delete then create. |
| Author-owned envelope | `apiVersion`, `kind`, labels, annotations, `spec`, and Markdown body. `spec.lifecycle.state` is `ACTIVE` (default) or `RETIRED`. |
| Provenance | `repositoryID`, `sourcePath`, `gitCommitSHA`, `gitRef`, and revision identify the admitted Git source. GraphQL create targets namespace `gitstore-system` implicitly; GraphQL update resolves this stored repository and source path, so a Product admitted from a non-system repository is updated in its original file. |
| Versioning | `generation` advances only for admitted desired-state changes. `resourceVersion` advances for desired-state and system-state writes and is used for optimistic concurrency. |
| System-owned metadata | UID, resource/generation versions, timestamps/actors, status, owner references, finalizers, and deletion timestamp cannot be authored in a manifest or Product mutation input. |
| Lifecycle | Active has no deletion timestamp. Terminating has a timestamp and the foreground-deletion finalizer. Final removal occurs only after the finalizer is removed following a fresh blocker check. |

### Product state transitions

```text
absent --admitted create--> active
active --admitted mutable update--> active
active --retire/activate update--> active
active --eligible delete--> terminating
terminating --fresh zero-blocker check + finalizer completion--> absent
```

Deletion with a live blocking variant is rejected before the `active →
terminating` transition. A repeated deletion while terminating is idempotent.

## ProductVariant blocking dependent

| Area | Rule |
|---|---|
| Resolved Product owner reference | System-owned reference to the resolved Product UID with `blockOwnerDeletion: true`. |
| Admission invariant | A new or newly resolved ProductVariant whose target Product has a deletion timestamp is rejected. |
| Existing blocker | A previously resolved live variant continues blocking final Product removal until separately deleted or no longer resolved. |
| Cascade behavior | Product deletion never deletes variants. |
| Retirement | ProductVariant has no independent lifecycle field in this feature. A retired parent excludes it from newly prepared releases. |

The existing Product-to-CategoryTaxonomy relationship remains non-blocking and
continues to drive only the CategoryTaxonomy enqueue fan-out.

## Product lifecycle status

Status is controller/admission owned and merged by condition type with an
expected `resourceVersion`. The exact condition vocabulary is additive and
includes at least `AdmissionAccepted` and `Terminating`; readiness/resolution
conditions retain their existing ownership. `Terminating` is derived from the
deletion timestamp/finalizer state and must not become an independent,
author-writable truth.

## Durable Product Resource Watch event

Product uses the existing backend-neutral `ResourceWatchEvent` model.

| Field | Semantics |
|---|---|
| `kind` | Always `Product` for the typed Product projection. |
| `epoch`, `sequence` | Opaque, shared-journal cursor identity. Never reuse Product resource version as a watch cursor. |
| `type` | `ADDED`, `MODIFIED`, `DELETED`, or `BOOKMARK`. |
| `payload` | Full committed Product postimage for `ADDED`/`MODIFIED`; absent for `DELETED`/`BOOKMARK`. |
| selector labels | Postimage labels plus prior labels for selector-enter/leave handling. |
| event sources | Admitted create/update, controller status update, deletion marker/finalizer write, and final hard delete. Rejected/no-op operations produce no successful transition. |

The journal remains bounded: retention, replay, subscriber buffer, tailer,
bookmark, lease, and recovery limits reuse the Resource Watch contract already
shipped for Namespace and Repository. Product adds a source/CDC adapter and
kind projection, not a new watch architecture.

## Authorization subjects and actions

All Product access is private current-catalog access. Policies distinguish
resource-scoped read, author/create, update, delete, watch, controller status,
and controller deletion-completion capabilities. A denial happens before any
existence, node, relationship/count, cursor, journal, or payload disclosure.
Configured human, service-account, and controller authentication providers use
the existing authentication boundary; audit records associate the decision and
lifecycle-changing request with its authenticated actor without secret values.

## Publication compatibility boundary

`RETIRED` stops a Product and child variants from participating in a newly
prepared release. If a release candidate explicitly contains either, release
preparation rejects the candidate. Product retirement/deletion never rewrites
an immutable release tag, snapshot, or active public projection. Those entities
are not created or reconciled by this feature.
