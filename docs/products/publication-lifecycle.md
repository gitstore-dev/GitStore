# Product and Variant Publication Lifecycle

**Status:** 🟡 Proposed. The decision is recorded in
[ADR 0014 — Catalog Release and Publication](../ADRs/0014-catalog-release-and-publication.md).  
**Scope:** an author-facing reference for publishing, scheduling and unpublishing Products and
ProductVariants  
**Audience:** catalog authors and release managers

> This page explains how to use the model. ADR 0014 is authoritative for trust rules, service
> responsibilities, fencing and alternatives. Where the two disagree, the ADR wins.

## In one paragraph

Pushing to `main` puts a Product or ProductVariant into the **private** catalog that the Admin
API shows. Nothing becomes public until you:

1. write a `CatalogRelease` that pins an admitted commit and lists what to release;
2. write a `Publication` that puts that release on a channel/market at a time.

The storefront only ever serves the active release for its target. Editing `main` afterwards
doesn't change what shoppers see until a new release is published.

## Where a resource stands

These states are derived **per target**, such as `web/eu`. They aren't stored on the Product.

| State       | Meaning                                                                                                                  |
|-------------|--------------------------------------------------------------------------------------------------------------------------|
| `Draft`     | Admitted, but in no prepared release for the target.                                                                     |
| `Candidate` | In a prepared release that isn't active for the target.                                                                  |
| `Scheduled` | In a publication whose `effectiveFromTime` is in the future.                                                             |
| `Published` | In the target's active publication.                                                                                      |
| `Retiring`  | Marked `RETIRED` in Git, but still in the active snapshot.                                                               |
| `Retired`   | Marked `RETIRED`, and the active snapshot no longer contains it.                                                         |
| `Withdrawn` | Removed from the target by a derived release, a deleted publication or a suppression.                                    |
| `Expired`   | The publication window ended, and the target serves nothing until another publication activates.                         |

A ProductVariant is `Published` only when its parent Product is in the same snapshot. A Product
can be published as presentation content with no sellable variants, but it can't be added to a
cart.

## Common tasks

### Release and schedule a campaign

```markdown
---
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
---
Black Friday catalog snapshot
```

```markdown
---
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
  effectiveUntilTime: "2026-11-30T23:59:59Z"
---
```

- You choose a branch, not a SHA. GitStore pins the commit once the release is admitted and
  shows it in `status.resolved.pinnedCommitSHA`. The Admin UI offers the same flow with a branch
  picker.
- The selection must be explicit. Selecting a variant brings its Product's presentation with it.
  Selecting a Product does **not** select its variants.
- GitStore prepares the snapshot as soon as the release is admitted, and reports any problems on
  the release long before `effectiveFromTime`.
- You may push the tag yourself or let GitStore create it at go-live. Either way it must point at
  the pinned commit, and it can never be moved.
- A release is blocked until every `ReleaseEligible` gate for the selected resources is satisfied,
  for example a merchandising approval ([ADR 0015](../ADRs/0015-resource-lifecycle-hooks.md)).

### Replace or roll back what is live

Publish a new `Publication` for the same target with `supersedesRef` naming the active one. The
switch is atomic, so there is no gap, and the old snapshot keeps serving if the new one fails. To
roll back, point the new publication at an earlier release.

### Unpublish

| I want to…                                   | Do this                                                                                                                                             |
|----------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------|
| End a campaign on schedule                   | Set `effectiveUntilTime`. Afterwards the target serves **nothing**. It doesn't fall back to the previous publication.                               |
| Take a target offline now                    | Delete the `Publication` manifest, or shorten `effectiveUntilTime` to now.                                                                          |
| Remove one product or variant, keep the rest | Write a derived release (`derivedFrom` + `exclude`) and a `Publication` that supersedes the active one.                                             |
| Stop selling something permanently           | Set `spec.lifecycle.state: RETIRED`. It is left out of every **future** release. It still needs a replacement publication to leave the current one. |
| Legal, safety or fraud takedown              | Ask an operator with `publication.suppress` for an emergency suppression. Don't use `RETIRED` for this.                                             |

```markdown
---
apiVersion: catalog.gitstore.dev/v1beta1
kind: CatalogRelease
metadata:
  name: black-friday-2026-r1
  namespace: acme-store
spec:
  derivedFrom:
    name: black-friday-2026
  exclude:
    variantRefs:
    - name: winter-coat-blue-m
  tagRef: refs/tags/v2026.11.27-r1
---
```

After unpublishing:

- shoppers get "not found" for the removed items;
- carts holding them fail at checkout with `OFFER_UNAVAILABLE`;
- existing orders are unaffected.

## What is frozen and what is live

The release freezes content (titles, copy, media references, variant membership and price
templates). Some things stay live and are evaluated per request:

- price-template date windows, priority and eligibility;
- inventory;
- the shopper's market, locale and currency.

A Black Friday price can therefore sit in a release ahead of time and apply only inside its
window. If two eligible prices have the same priority, validation rejects the release. GitStore
never picks one arbitrarily.

## Common failures

| Symptom                                                  | Cause and fix                                                                                                                                                                            |
|----------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Release `SnapshotReady=False`                            | A selected resource or its dependency (category, collection, file, translation) is missing or not ready at the pinned commit. Fix it on `main`, then write a new release with a new tag. |
| Release `GatesSatisfied=False`                           | A lifecycle gate is pending, such as an approval for this exact content. Any content change needs a fresh approval.                                                                      |
| Release `TagObserved=False` with a tag mismatch          | The tag points at a different commit. Tags are never moved, so use a new tag name.                                                                                                       |
| Publication rejected for overlap                         | Another publication for the same target overlaps the window. Add `supersedesRef` or change the window.                                                                                   |
| A Product edit on `main` isn't visible on the storefront | This is expected. Publish a new release.                                                                                                                                                 |

## Carried forward to the implementation spec

The previous proposal's delivery order and acceptance tests remain the starting checklist for the
feature spec that implements ADR 0014:

- datastore contracts (memDB and Scylla) for receipts, tag observations, snapshots, public indexes
  and fenced target pointers;
- #139 `GitEvent` replay, plus tag reconciliation until it ships;
- `PUBLIC`/`MANAGEMENT` query plans on every access path (lookup, Relay lists, node,
  relationships, counts and watches), without filtering after pagination;
- tests:
  - drafts never leak;
  - pinned snapshots survive `main` moving;
  - untrusted, moved or unsigned tags are rejected;
  - selection closure rules;
  - uncertain ensure-tag outcomes;
  - controller restart between scheduling and activation;
  - pricing tie and boundary cases;
  - two API and two controller replicas racing leases;
  - rolling upgrades with storefront publication disabled;
  - unpublish (expiry, deactivate finalizer, derived release, suppression);
  - sustained snapshot preparation and public-read load;
- metrics: tag trust failures, snapshot duration/size/failure, publication states, fencing
  conflicts, overdue schedules, snapshot age, and public not-found counts. Audit records correlate
  tag, commit, receipt, digest, release, publication, actor and target.
