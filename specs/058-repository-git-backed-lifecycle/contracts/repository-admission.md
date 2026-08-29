# Contract: Repository Manifest Admission

## Authoring target

The sole valid repository for a `Repository` manifest is the *target* namespace's own `gitstore-system` repository (`<namespace>/gitstore-system`) — never another namespace's `gitstore-system`, and never any other repository. A `Repository`-kind manifest pushed anywhere else is rejected at pre-receive, before admission runs. This differs from Namespace's single global authoring target (`gitstore-system/gitstore-system`) because `Repository` is namespace-scoped by definition.

## File path convention

```
repositories/<name>.md
```

matching ADR-0003's documented convention, e.g. `repositories/catalog.md` inside `acme-store/gitstore-system`.

## Manifest shape

Unchanged from `docs/repository/repository-spec.md`'s hydrated representation and ADR-0003's example — `apiVersion`, `kind: Repository`, `metadata.{name,namespace}`, `spec.{defaultBranch,visibility,storageClass,settings.pushPolicy}`. Authors omit `metadata.uid`/`resourceVersion`/`generation`/`creationTimestamp`/`revision`/`ownerReferences`/`finalizers` and `status` entirely; any submitted `status` content is ignored (FR-009).

```markdown
---
apiVersion: core.gitstore.dev/v1beta1
kind: Repository
metadata:
  name: catalog
  namespace: acme-store
spec:
  defaultBranch: main
  visibility: private
  storageClass: standard
---

Long-form repository description.
```

## Pre-receive rules (new)

| Check | Outcome on failure |
|---|---|
| `kind == "Repository"` implies the push repository is `<metadata.namespace>/gitstore-system` | Push rejected; no commit reaches admission |
| `metadata.namespace` matches the namespace that owns the target `gitstore-system` repository | Push rejected; no commit reaches admission (prevents a manifest declaring a different namespace than the repository it was pushed to) |

All other pre-receive structural rules (envelope validity, `metadata.name` format, `spec.defaultBranch` ref-name validity) follow the future "Repository Validation and Admission Matrix" spec's rule catalogue (out of scope here; see spec.md Assumptions).

## Admission outcomes

| Scenario | Outcome |
|---|---|
| New name, valid manifest, correct namespace-scoped repository, namespace exists and is not `Terminating` | Repository record created; `AdmissionAccepted=True`; `Generation=1`, `ResourceVersion="1"` on first admission. |
| Existing name, valid manifest changing only mutable fields, correct namespace-scoped repository | Repository record updated to the manifest's mutable spec values; `Generation` and `ResourceVersion` both advance. |
| Existing name, manifest attempts to change `metadata.name` or `metadata.namespace` | Rejected; no record change. Reason is distinguishable from other admission failures. |
| Existing name, manifest attempts to downgrade `spec.storageClass` | Rejected; no record change. Reason is distinguishable from other admission failures. |
| Manifest targets the reserved bootstrap name `gitstore-system` | Rejected; no record change. |
| Manifest's owning namespace does not exist, or is `Terminating` | Rejected; no record change. |
| Manifest is structurally invalid per the future validation-matrix spec's rules | Rejected at the appropriate phase (pre-receive or admission), out of scope for this contract to fully enumerate. |

## Mutation delegation contract

`createRepository`/`updateRepository` (for any non-bootstrap repository) accept the declarative resource envelope, e.g.:

```graphql
mutation {
  createRepository(input: {
    apiVersion: "core.gitstore.dev/v1beta1"
    kind: "Repository"
    metadata: { name: "catalog", namespace: "acme-store" }
    spec: { defaultBranch: "main", visibility: PRIVATE, storageClass: "standard" }
  }) {
    repository { metadata { name namespace } }
  }
}
```

The resolver then:

1. Resolve/construct the equivalent `Repository` manifest from the mutation input.
2. Call `GitWriter.CommitFile` against `<namespace>/gitstore-system` at `repositories/<name>.md`.
3. Await admission of that commit (synchronous from the caller's perspective).
4. On admission success: return the resulting repository.
5. On admission failure: return the equivalent GraphQL error; the caller never receives a partially-applied repository.

`createRepository`/`updateRepository` targeting the bootstrap repository name `gitstore-system` within any namespace: rejected immediately, no commit attempted.

`renameRepository`/`transferRepository`: unconditionally return `Unimplemented`; no manifest is constructed, no commit is attempted, no datastore record is touched.

`deleteRepository`: unchanged trigger (GraphQL mutation, not a manifest deletion), but now sets `DeletionTimestamp`/`Finalizers` instead of hard-deleting synchronously, per the deletion state machine in `data-model.md`.
