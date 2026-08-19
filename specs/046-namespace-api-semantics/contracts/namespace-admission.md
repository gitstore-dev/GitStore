# Contract: Namespace Manifest Admission

## Authoring target

The sole valid repository for `Namespace` manifests is `gitstore-system/gitstore-system` (the `gitstore-system` bootstrap namespace's own `gitstore-system` repository). A `Namespace`-kind manifest pushed to any other repository is rejected at pre-receive, before admission runs.

## File path convention

```
namespaces/<name>.md
```

matching ADR-0002's documented convention, e.g. `namespaces/acme-store.md`.

## Manifest shape

Unchanged from `docs/namespace/namespace-spec.md`'s create/update manifest examples — `apiVersion`, `kind: Namespace`, `metadata.name`, `spec.{title,tier,repositoryDefaults,pushPolicyDefaults}`. Authors omit `metadata.uid`/`resourceVersion`/`generation`/`creationTimestamp`/`revision`/`ownerReferences`/`finalizers` and `status` entirely; any submitted `status` content is ignored (FR-009).

## Pre-receive rule (new)

| Check | Outcome on failure |
|---|---|
| `kind == "Namespace"` implies repository is `gitstore-system/gitstore-system` | Push rejected; no commit reaches admission |

All other pre-receive structural rules (envelope validity, `metadata.name` format) follow spec 047's validation matrix.

## Admission outcomes

| Scenario | Outcome |
|---|---|
| New name, valid manifest, correct repository | Namespace record created; `AdmissionAccepted=True`; `Generation=1`, `ResourceVersion="1"` on first admission, or advanced from the prior value if the row already existed from bootstrap/failed-retry recovery. |
| Existing name, valid manifest, correct repository | Namespace record updated to the manifest's spec values; `Generation` and `ResourceVersion` both advance. |
| Manifest targets a bootstrap namespace name (`gitstore-system` or `default`) | Rejected; no record change. |
| Manifest attempts to demote `spec.tier` (e.g. `ORGANIZATION` → `USER`) | Rejected; no record change. Reason is distinguishable from other admission failures (spec 047). |
| Manifest is structurally invalid per spec 047's rules | Rejected at the appropriate phase (pre-receive or admission) per spec 047's matrix. |

## Mutation delegation contract

`createNamespace`/`updateNamespace` (for any non-bootstrap namespace):

1. Resolve/construct the equivalent `Namespace` manifest from the mutation input.
2. Call `GitWriter.CommitFile` against `gitstore-system/gitstore-system` at `namespaces/<name>.md`.
3. Await admission of that commit (synchronous from the caller's perspective).
4. On admission success: return the resulting namespace.
5. On admission failure: return the equivalent GraphQL error; the caller never receives a partially-applied namespace.

`createNamespace`/`updateNamespace` targeting either bootstrap namespace name: rejected immediately, no commit attempted.

`deleteNamespace`: unchanged trigger (GraphQL mutation, not a manifest deletion), but now sets `DeletionTimestamp`/`Finalizers` instead of hard-deleting synchronously, per the deletion state machine in `data-model.md`.
