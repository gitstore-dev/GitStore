# ADR 0014: Catalog Release and Publication

**Status**: Proposed

**Date**: 2026-09-30

**Audience**: catalog authors; Git service, API, and controller-manager maintainers.

**Supersedes**: the proposal previously held in
[Product and Variant Publication Lifecycle](../products/publication-lifecycle.md), which is now an
author-facing reference for this ADR. Follow-up design for
[#314](https://github.com/gitstore-dev/GitStore/issues/314).

## Context

A push to an admitted branch makes a Git-authored `Product` or `ProductVariant` part of the
private, current catalog read model ([ADR 0004](0004-product-lifecycle.md),
[ADR 0005](0005-product-variant-lifecycle.md)). Nothing today decides what a buyer may see.
Earlier designs proposed a source-resource `Published` condition, set when a release tag targets
the resource:

- [specs 014](../../specs/014-product-frontmatter/spec.md) and
  [017](../../specs/017-product-spec-validation/data-model.md);
- [architecture overview](../architecture/README.md).

That condition was never implemented. No code defines or writes it, and status conditions are
stored as opaque types. A condition like it couldn't answer the questions a storefront needs
answered anyway:

- Which channel or market is the resource published to?
- Which revision is published after `main` has moved on?
- Is it live at this request's time?
- Which dependencies (categories, collections, files, translations) were released with it?
- How is it taken down again, for one target, one product, or everything?

The authoritative current rows are replaced every time the admitted branch advances, so a flag on
those rows can't hold a revision-, target- and time-specific answer.

Other platforms separate these concerns in the following ways:

- **commercetools** keeps a `staged` projection ("the draft data") and a `current` projection
  ("the live version of the product data visible to customers"). Publishing copies `staged` to
  `current`. Unpublishing "only sets the `published` flag to `false` … it does not alter the data
  stored in the `current` or `staged` representations"
  ([Product Projections](https://docs.commercetools.com/api/projects/productProjections)).
- **Shopify** publishes a resource "to one or more publications" (sales channels), and "only
  online store channels support scheduled publishing"
  ([publishablePublish](https://shopify.dev/docs/api/admin-graphql/latest/mutations/publishablePublish)).
- **Sanity Content Releases** "organize and schedule updates across multiple documents". A release
  can also unpublish a live document, and publishing can be undone by "reverting the release"
  ([Content Releases](https://www.sanity.io/docs/content-releases)).

GitStore adopts the channel-scoped model (Shopify), the release as a unit of change across
documents (Sanity), and an immutable live projection that stays separate from drafts
(commercetools). Two properties are specific to GitStore: the unit of release is an immutable Git
revision, and every write stays reviewable in Git.

## Decision

**Git admission and public publication are separate state machines.**

1. Admission makes a resource part of the private, current catalog. It never makes it public.
2. A `CatalogRelease` pins an admitted commit and a resource selection, then builds an
   **immutable snapshot** from the tree at that commit.
3. A `Publication` binds one release snapshot to one **target** `(namespace, channel, market)` and
   an activation window.
4. The Storefront API ([ADR 0012](0012-admin-storefront-graphql-endpoints.md)) reads only the
   target's active snapshot plus explicit request-time values. It never falls back to the mutable
   current catalog rows. **Expiry is enforced on the read path** (§5). It doesn't depend on the
   controller reaching `effectiveUntilTime` on time.
5. Unpublishing is a first-class operation with defined semantics at three granularities (§6).

### 1. Resources and storage

| Record                                      | Storage group               | Writer                                |
|---------------------------------------------|-----------------------------|---------------------------------------|
| `Product`, `ProductVariant`                 | Git + datastore             | existing admission path               |
| `CatalogRelease`                            | Git + datastore             | release author, admitted by the API   |
| `Publication`                               | Git + datastore             | release author, admitted by the API   |
| Admission receipt                           | Datastore only, durable     | API admission path                    |
| Observed Git tag event                      | Datastore only, append-only | API, from the Git event stream (#139) |
| Release snapshot and public indexes         | Datastore only, immutable   | API, on controller request            |
| Target pointer (active snapshot per target) | Datastore only              | API, via fenced compare-and-swap      |
| Reconcile lease and side-effect record      | Datastore only              | API, on controller request            |
| Emergency suppression                       | Datastore only              | API, separately authorized (§7)       |

`CatalogRelease` and `Publication` are admitted only from the configured admission branch. Their
`status`, tag observations and snapshots are system-managed and never authored in frontmatter.

### 2. `CatalogRelease`

```yaml
apiVersion: catalog.gitstore.dev/v1beta1
kind: CatalogRelease
metadata:
  name: black-friday-2026
  namespace: acme-store
spec:
  repositoryRef:
    name: storefront-catalog
  tagRef: refs/tags/v2026.11.27
  sourceRef: refs/heads/main
  selection:
    productRefs:
    - name: winter-coat
    variantRefs:
    - name: winter-coat-blue-m
```

- **Pinning.** At admission the API resolves `sourceRef` once, checks that the commit has an
  admission receipt, and records `status.resolved.pinnedCommitSHA`. The controller never resolves a
  moving ref later. Editing `sourceRef` creates a new generation and requires a new, distinct
  `tagRef`. A release tag is never moved.
- **Explicit selection.** The selection must be non-empty and explicit. An omitted selector never
  means "everything". A future `matchLabels` selector requires an opt-in label such as
  `catalog.gitstore.dev/releaseable: "true"`.
- **Dependency closure.** Selecting a variant includes its parent Product's presentation but not
  its sibling variants. Selecting a Product does not select its variants. Required category,
  collection, translation and File dependencies are recorded in the snapshot. A missing or unready
  dependency makes preparation fail closed.
- **Resolved bodies.** Each selected body is stored as its resolved IR
  ([ADR 0013 §4](0013-markdown-body-intermediate-representation.md#4-pipeline-parse-at-admission-resolve-at-publication)).
  The snapshot digest covers it.
- **Lifecycle gates.** Preparation requires every `ReleaseEligible` lifecycle gate registered for
  a selected resource to be satisfied ([ADR 0015](0015-resource-lifecycle-hooks.md)). This is the
  seam where seller workflows
  ([037](../implementation/037-custom-commerce-workflows.md)) attach. Such a gate can block a
  release, but it can never write a public projection.
- **Snapshot boundary.** The snapshot freezes catalog content: identity, source revision,
  frontmatter, resolved body, dependency closure and price templates. Some things are **not**
  frozen and are evaluated per request:
  - price-template time windows, priority and CEL eligibility;
  - inventory;
  - caller, market, locale and currency.
- **Status.** Conditions `SourcePinned`, `GatesSatisfied`, `SnapshotReady`, `TagObserved`,
  `TargetTrusted` and `Ready`. The status also records the snapshot digest, admission receipt ID,
  observed tag object ID and selected identities.

### 3. Prepare early, verify the tag at activation

Snapshot preparation **does not wait for the tag**. It reads the tree at the pinned commit as soon
as the release is admitted and its gates are satisfied. Failures therefore surface days ahead of
a go-live, not at midnight on launch day.

The tag is release **evidence**, checked at activation:

1. If the tag already exists, the API must have durably observed it, and its peeled commit must
   equal the pinned commit.
2. If the tag is missing at `effectiveFromTime`, the lease holder asks the API to ensure it
   exists at the pinned commit. The command is idempotent, and an existing tag at the same commit
   counts as success.
3. A tag pointing at a different commit is a terminal failure. It is never overwritten.
4. After the first successful observation the tag is immutable. A delete or move is recorded as a
   rejected observation and never changes a snapshot or target.
5. When signature policy is enabled, an automatic tag uses the repository's release-signing
   identity. An external tag must be annotated, signed, and verified against the trusted-signer
   policy.

**Until #139 ships** a replayable `GitEvent` stream, the API reconciles registered release tags by
reading them through the Git service before every activation. The stream is an optimization for
observation latency, never a correctness dependency.

### 4. `Publication`

```yaml
apiVersion: catalog.gitstore.dev/v1beta1
kind: Publication
metadata:
  name: web-eu-black-friday-2026
  namespace: acme-store
spec:
  releaseRef:
    name: black-friday-2026
  target:
    channel: web
    market: eu
  effectiveFromTime: "2026-11-27T00:00:00Z"
  effectiveUntilTime: "2026-11-30T23:59:59Z"   # optional
  supersedesRef:                               # optional; see §5
    name: web-eu-autumn-2026
```

- At most one publication is active per target. A candidate whose window overlaps another
  publication for the same target is rejected unless it names that publication in
  `supersedesRef`.
- `status` records `ReleaseReady`, `Scheduled`, `Active`, `Expired`, `Superseded`, `Withdrawn`
  or `Failed`. It also records the snapshot ID, fencing token, and predecessor/replacement
  identities.

### 5. Atomic replacement and rollback

`supersedesRef` is valid only when the predecessor has the identical target key and is that
target's current active or scheduled publication at admission. At `effectiveFromTime` the API
performs one fenced compare-and-swap on the target pointer. In the same durable operation it marks
the predecessor `Superseded` and records both snapshot IDs. If the replacement fails, the old
snapshot keeps serving. A rollback is a new Publication that points at an earlier prepared
release and supersedes the active one. Overlay semantics (two publications composed on one
target) are out of scope.

**The pointer carries its own window.** A target pointer stores the snapshot ID, the publication
identity and that publication's `effectiveUntilTime`.

- A Storefront read whose request time is at or after `effectiveUntilTime` treats the target as
  empty, exactly as if the pointer had been cleared.
- Shortening `effectiveUntilTime` updates the pointer's copy in the same fenced compare-and-swap
  that admits the change.
- The controller's later deactivation only finalizes state: it clears the pointer, marks the
  publication `Expired` and emits watch events. A late or unavailable controller can delay that
  bookkeeping, but it can never extend public visibility.
- Activation is symmetric: the API refuses to swap a pointer in before `effectiveFromTime`.

### 6. Unpublishing

Unpublishing never rewrites a snapshot and never deletes one that may still be referenced. It
either moves a target's pointer to empty or swaps in a smaller snapshot.

| Intent                                   | Mechanism                                                                 | Result                                                                                                                                                            |
|------------------------------------------|---------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Take a whole target offline on schedule  | `effectiveUntilTime` reached                                              | Reads stop serving the target at that instant (§5). The controller then marks the publication `Expired` and clears the pointer. The target serves **nothing**; there is no implicit fallback to a predecessor. |
| Take a whole target offline now          | Delete the `Publication` manifest, or shorten `effectiveUntilTime` to now | The finalizer `publication.gitstore.dev/deactivate` makes the API fence-swap the pointer to empty before the row is removed. The publication becomes `Withdrawn`. |
| Remove specific products or variants now | A **derived release** plus a `supersedesRef` Publication (below)          | The target swaps atomically to a snapshot without them. There is no storefront gap.                                                                               |
| Retire permanently                       | `spec.lifecycle.state: RETIRED` on the Product or ProductVariant          | Excluded from every **new** snapshot. It does **not** unpublish an active snapshot by itself.                                                                     |
| Legal, safety or fraud takedown          | Emergency suppression (§7)                                                | Overrides the active snapshot for the named identities, without Git review.                                                                                       |

Shortening `effectiveUntilTime` is allowed at any time. Extending it is allowed only when no
other publication for the target overlaps the new window.

**Derived release.** A derived release removes items without re-selecting the whole catalog:

```yaml
kind: CatalogRelease
metadata:
  name: black-friday-2026-r1
spec:
  derivedFrom:
    name: black-friday-2026
  exclude:
    variantRefs:
    - name: winter-coat-blue-m
  tagRef: refs/tags/v2026.11.27-r1
```

The API builds the snapshot from the parent snapshot minus the excluded identities and their
now-orphaned closure. It inherits the parent's pinned commit and gets a new digest. Its tag points
at the same commit, and tags are cheap. A derived release can only remove items. Adding content
requires a normal release from an admitted commit.

**Effects of unpublishing:**

- **Storefront reads.** Direct lookups of an unpublished identity return `NOT_FOUND`/`null`,
  indistinguishable from a resource that never existed. Connections, counts and collection
  membership derive from the new pointer.
- **Storefront watches.** Removal events are emitted after the pointer swap. They are never
  inferred from mutable Product watches.
- **Carts and checkout.** A cart line holds a snapshot ID. Checkout re-validates every line
  against the target's active snapshot, and a line whose identity is absent fails with a typed
  `OFFER_UNAVAILABLE` error. The API never substitutes a line silently.
- **Orders, returns and refunds.** Unaffected, because they carry their own order-time snapshot
  ([037](../implementation/037-custom-commerce-workflows.md)).
- **Deletion safety.** A `Namespace` or `Repository` cannot be deleted while any Publication
  under it is `Active` or `Scheduled` (`blockOwnerDeletion`, per ADRs
  [0002](0002-namespace-lifecycle.md)/[0003](0003-repository-lifecycle.md)).

### 7. Emergency suppression

Emergency suppression is a datastore-only `PublicationSuppression` record. It removes named
identities from a target's served results without a Git change. It requires the dedicated
`publicationSuppression.create` action ([ADR 0010](0010-authorization-model.md)) and a reason code, and it
is audited. It must carry an expiry of at most 7 days, and it is cleared automatically once a
Publication that excludes the identities becomes active. It fails closed: if suppression state
can't be read, the suppressed identities are not served. Its full contract belongs in the
implementation spec. This ADR fixes only that it exists, is separate from `RETIRED`, and is never
a workflow transition that a Git edit could undo.

### 8. No `Published` condition on source resources

GitStore does not introduce a `Published` condition on Product, ProductVariant, or any other
source resource. Publication state is derived per target from the relations above
(`Draft`…`Expired` in the [author reference](../products/publication-lifecycle.md)), not stored
on source rows. `AdmissionAccepted`, `Ready` and the reference conditions remain source-health
signals only. The earlier design documents that list `Published` as a condition type are
superseded on this point. The implementation spec removes it from their enumerations, and from
the `converters_test.go` fixture that uses it as a sample type.

### 9. Service responsibilities and multi-replica safety

| Service                         | Owns                                                                                                                                                         | Must not                                                                           |
|---------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------|
| **gitstore-git-service**        | Emitting ref/tag `GitEvent`s; peeling tags; read-only tree and signature checks; creating an immutable tag only on an authorized API command                 | Persist catalog state, evaluate selections, schedule, or move/delete a release tag |
| **gitstore-api**                | Admission and pinning; receipts and tag observations; snapshot builds; fenced pointer swaps; suppression; Admin and Storefront contracts                     | Poll timers, choose an implicit tag target, or treat a tag as an admitted branch   |
| **gitstore-controller-manager** | Level-triggered reconciliation of `CatalogRelease`/`Publication`; deadline requeues; per-key leases; requesting prepare, ensure-tag, activate and deactivate | Write the datastore directly, trust tag data without the API, or mutate refs       |

Concurrency follows [ADR 0018](0018-controller-ownership-concurrency-and-fencing.md):

- **Safety** (§3):
  - Tag creation is a Git create-if-absent CAS at the pinned commit.
  - Every target-pointer change is a Scylla LWT on the expected prior pointer and carries the
    lease's fencing token.
  - Every command carries a durable idempotency key.
- **Liveness** (§4): on-demand leases keyed `release/<uid>` and `publication/<target-key>` stop
  replicas from duplicating snapshot builds and activations.

On startup the controller lists incomplete releases and scheduled publications. No in-memory timer
is a correctness dependency.

Storefront publication is gated per namespace/target by a feature flag. Rollout deploys readers
everywhere first. Rollback disables `PUBLIC` readers and never deletes snapshots.

## Consequences

Positive:

- A draft can never leak. Every storefront path reads one target-specific immutable projection.
- A published offer is reproducible from a tag, a commit and a digest, and every change to it is
  reviewable in Git.
- Early preparation turns launch-time failures into review-time failures.
- Unpublishing at target, identity and emergency granularity has explicit, gap-free semantics.

Negative:

- Removing one product from a live target requires a derived release plus a Publication (two
  manifests), unless it is an emergency.
- Snapshots duplicate content per release. Retention must be tied to references from
  Publications, carts and orders.
- A new datastore surface (receipts, observations, snapshots, pointers, leases) is needed before
  any public read ships.

## Cross-references

- [ADR 0004](0004-product-lifecycle.md), [ADR 0005](0005-product-variant-lifecycle.md): source
  resources, and `spec.lifecycle.state`.
- [ADR 0010](0010-authorization-model.md): `catalogRelease.*`, `publication.*` and
  `publicationSuppression.create` actions.
- [ADR 0012](0012-admin-storefront-graphql-endpoints.md): Admin endpoints read `MANAGEMENT`;
  Storefront endpoints read `PUBLIC`.
- [ADR 0013](0013-markdown-body-intermediate-representation.md): resolved bodies in snapshots.
- [ADR 0015](0015-resource-lifecycle-hooks.md): `ReleaseEligible` lifecycle gates.
- [022 OPA data authorization](../implementation/022-opa-data-authorization.md): the
  `catalog.visibility` semantic scope.
- [037 Custom seller and buyer workflows](../implementation/037-custom-commerce-workflows.md):
  seller gates and order snapshots.

## Alternatives considered

### A `Published` flag or condition on source rows

Rejected. It can't express per-target, per-revision or time-windowed state, and it is overwritten
whenever the admitted branch advances (see Context).

### A separate public branch as the publication source

Rejected. It creates two admission sources, and merge order would decide visibility. Tags pin an
exact revision without competing with the admission branch.

### Unpublish by flipping a flag while keeping the live projection (commercetools model)

Rejected as the only mechanism. It fits one mutable live projection per product, but GitStore
serves immutable per-target snapshots. A per-identity flag over a snapshot is exactly what
emergency suppression is, and it is kept narrow and audited instead of becoming the normal path.

### Mutating an active snapshot in place to remove an item

Rejected. The digest would no longer describe what was served, and rollback would stop being
exact. Derived releases give the same outcome with a new digest.

### Building the snapshot at activation time

Rejected. Validation failures would surface at go-live (§3).
