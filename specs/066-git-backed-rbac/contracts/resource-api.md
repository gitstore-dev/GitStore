# Resource and Admin API Contract

This is a design contract, not an already-generated GraphQL schema.

## Git resources

Canonical example in `acme-store/gitstore-system` at
`rbac/rolebindings/catalog-editors.yaml`:

```yaml
apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RoleBinding
metadata:
  name: catalog-editors
  namespace: acme-store
spec:
  roleRef:
    kind: ClusterRole
    name: edit
  subjects:
    - kind: Group
      name: system:group:merchandiser
```

The namespace name is validated against the repository's indexed Namespace
UID. It is not accepted as authority from the document. Cluster manifests
omit namespace. Rules use `spec.rules`; bindings use `spec.roleRef` and
`spec.subjects`. Status, UID, counters and managed provenance are
server-owned. Unknown fields, deny/effect, partial wildcards, legacy
actions and unallowlisted reserved identities fail common admission.

Only the canonical repository branch feeds effective RBAC. All changed
manifests in one candidate are checked together. Reverts are new writes,
subject to current validation and managed/built-in protections.

## Admin GraphQL

Use `/admin/graphql` over the existing POST and `graphql-transport-ws`
transports. Do not add these resources to Storefront.

Each of the four Git-backed kinds implements the existing Node and resource metadata/status
interfaces and follows existing action-specific input/payload conventions.
Extend existing `node`/`nodes` dispatch and unified lookup diagnostics.

Planned field families:

| Operation | Fields / behavior |
|---|---|
| Single lookup | `role(by:)`, `clusterRole(by:)`, `roleBinding(by:)`, `clusterRoleBinding(by:)`; scope-aware existing selector shape |
| Lists | `roles`, `clusterRoles`, `roleBindings`, `clusterRoleBindings`; Relay edges/cursors/PageInfo, bounded pages |
| Mutations | `create<Kind>`, `update<Kind>`, `delete<Kind>`, `complete<Kind>Deletion`; action-specific payload and expected resourceVersion |
| Status | Extend common status mutation for registered kinds; controller-owned fields only |
| Watch | Extend existing generic resource-kind dispatch and typed payload union |
| Review | `accessReview(input: AccessReviewInput!): AccessReviewPayload!`; transient `SubjectAccessReview`, see below |

Names above describe schema additions, not new public HTTP routes. Reuse
existing pagination support/error behavior rather than adding a second
pagination contract.

Create/update mutations return the admitted Git revision and resource
metadata. They do not promise every API cache has consumed it. Projection
failure after Git commit returns existing structured partial-outcome
diagnostics with commit identity; clients read/retry idempotently rather
than assume rollback.

Delete initiates termination; complete deletion is finalizer-gated,
UID/resourceVersion-conditional and authorized as `<kind>.purge`.
Reads/lists include visible virtual built-ins and persisted resources,
without duplicates or pagination instability within the returned revision.
Virtual IDs/revisions are deterministic within a release.

## Finite actions

Register these kinds with the existing generated catalog:

- `role`, `roleBinding`: namespaced.
- `clusterRole`, `clusterRoleBinding`: cluster-scoped.

Each gains `create`, `read`, `list`, `watch`, `update`, `delete`, `purge`,
and `status.write` where the corresponding controller endpoint exists.
Additionally register `role.escalate`, `clusterRole.escalate`,
`roleBinding.bind` and `clusterRoleBinding.bind`.

Register `role.access-review.create` for reviewing one namespace and
`clusterRole.access-review.create` for cluster-wide review. These are
subresource/verb catalog pairs, not implied by read/list. They grant
diagnostic access only and are included in cluster-admin; `admin` includes
the namespaced review action, bounded by its RoleBinding. `view` does not gain RBAC
reads or reviews; `edit` does not gain RBAC writes.

`namespace-full-control` expands the registered catalog only inside its
binding namespace. It cannot grant namespace/ServiceAccount/cluster-role
operations through a RoleBinding.

## Access review semantics

Adapt the existing `IssueServiceAccountTokenInput` / `TokenRequest` envelope
pattern, not a flat decision object. The
[transient storage-group catalogue](../../../docs/storage-groups/transient.md#authn-and-authz-checks)
already identifies `SubjectAccessReview` as a request/response contract.
Keep `accessReview` on Admin Query; evaluation itself creates no resource.

The input and payload are:

```text
AccessReviewInput {
  apiVersion = "authorization.gitstore.dev/v1beta1"
  kind = "SubjectAccessReview"
  spec: SubjectAccessReviewSpecInput!
}
AccessReviewPayload {
  subjectAccessReview: SubjectAccessReview
}
SubjectAccessReview {
  apiVersion, kind,
  spec: SubjectAccessReviewSpec!,
  status: SubjectAccessReviewStatus!
}
```

The executable SDL is in `shared/schemas/admin/rbac.graphqls`. `apiVersion` and `kind` are
non-null strings with input defaults; reject unsupported values rather than
silently overriding them. The payload follows existing action-specific
payload/error conventions. Do not copy durable `ObjectMeta` requirements:
reviews need no name, UID, resourceVersion, generation or ownerReferences.
They do not implement Node or gain CRUD, list, watch, finalizers or storage.
Any required durable audit record is a separate resource.

`spec` contains `principal` (target `subject`, optional `groups`), `action`
and `resourceRef`. Reuse existing Node IDs and scoped resource lookup
semantics; a resource reference selects a target, never supplies its trusted
attributes. For example, the input envelope can select an existing product:

```json
{
  "apiVersion": "authorization.gitstore.dev/v1beta1",
  "kind": "SubjectAccessReview",
  "spec": {
    "principal": {"subject": "user-123"},
    "action": "product.read",
    "resourceRef": {"id": "<product-node-id>"}
  }
}
```

This adapts the Kubernetes envelope, not its entire API:
GitStore uses its canonical action catalog and resource references rather
than Kubernetes verbs/API resource attributes or non-resource URL checks.
A separate `SelfSubjectAccessReview` operation and permission model are
not introduced; targeting oneself still requires scope review permission.

Authorize the reviewer before resolving target identity or returning policy
detail. Scope/resource facts come from authoritative server lookup;
arbitrary client resource attributes are not accepted as actual resource
state. Optional proposed context is explicitly hypothetical.

The reviewer is always the authenticated caller, never `spec.principal`.
If `spec.principal.groups` is omitted, resolve trusted membership through
the configured UserDir; lookup failure is unavailability, not an empty
group set. Supplied groups, including an explicit empty list, are
hypothetical and cannot impersonate the reviewer or authorize a write.

The response echoes the accepted parameters in `subjectAccessReview.spec`.
`subjectAccessReview.status` carries:

```text
allowed, reason, hypothetical,
revisions[{tierID,namespaceUID,gitRevision,tierRevision,capturedAt}],
catalogDigest, builtinDigest
```

A denial is a valid review result, not an infrastructure error.
Missing authoritative state or an unavailable provider yields retryable
authorization unavailability, never a guessed allow/deny.
Infrastructure errors use the existing GraphQL error envelope, not a
fabricated `status.allowed: false` or a partially evaluated review.
Review results use validation-start snapshots and may differ from
simultaneous ordinary decisions using older, still-within-bound caches;
they match enforcement when evaluated at the same revision/input.

### Verified review boundary

The Admin Query now implements this transient envelope with version/kind
defaults, canonical action/kind validation and existing Node-ID references.
Optional proposed context is limited to `tier`, `visibility` and a validated
`targetOwnerRef`; callers cannot override namespace identity, names or ownership.
Accepted spec parameters are echoed without substituting resolved membership
for an omitted group list.

The reviewer is authorized in an authoritative pinned model before target
membership is resolved. Namespaced review requires `role.access-review.create`
in the target Namespace; cluster review requires
`clusterRole.access-review.create`. Unknown target scopes require cluster
review permission before returning absence or lookup diagnostics, so missing
targets do not become an existence oracle for unauthorized callers.

Omitted groups require a complete trusted directory result. Unlike ordinary
authentication enrichment, missing profiles or unsupported group resolution
cannot silently become empty memberships. Explicit empty groups remain
hypothetical and do not request directory membership. The evaluated target
never replaces the actor pinned for admission.

The selected provider's same pinned evaluator supplies both the decision and
its exact revision/digest identity. Capture and directory failures return
retryable authorization unavailability, not a review denial. Each revision
identifies its tier, Namespace UID, Git revision and numeric-string tier
revision; `capturedAt` is the start of the bounded validation-start capture,
not evidence of a globally atomic vector.

Race-enabled wire/component tests cover the envelope, spec echo, scoped
reviewers, self-target privilege, explicit versus omitted groups, proposed
context, denial versus infrastructure failure, unchanged tier state and the
absence of Node/metadata/lifecycle support. They also prove that ordinary
cache denial cannot replace the authoritative review result. Deployed
identity/provider parity and two-API acceptance remain separate gates.

### Verified inspection boundary

Race-enabled Admin tests exercise scoped auditor lists, immutable virtual
built-ins, unresolved conditions, immutable resolved role UIDs and denied
policy access for the catalog `view` role. Both typed and generic inspection
watches use the durable journal, and a resumed typed watch replays the same
condition update at the same cursor. Stored Kubernetes-style condition values
are converted through the shared condition converter before GraphQL output.
Unauthorized lookups return the same forbidden envelope for existing and
missing IDs; namespaced auditors cannot inspect or watch another Namespace
or cluster policy. These component tests do not establish deployed identity,
replica or propagation acceptance.

### On-demand author provenance

Resources expose their admitted Git revision, and decision reasons identify
the binding UID, role UID and rule index. A tier's signed projection actor is
the actor of that tier publication, not necessarily the author who last changed
each unchanged resource. Whole-tier repair can coalesce intervening commits.
Do not present the publication actor as per-resource author provenance.
The implementation partially adopts #488 through header-opt-in
`extensions.provenance`, without a new datastore provenance projection.
Resource envelopes contain only `id`, `apiVersion`, `kind`, `metadata`,
`spec` and `status`; no `source` field or `RBACSource` type remains.
Send `GitStore-Provenance-Include: true` to receive an array whose entries
associate Node `id` and GraphQL response `path` (aliases and list indices)
with `repositoryRef`, `ref`, `file`, `commit`, `changeCommit`, `author`
and `authoredAt`. Commits are algorithm-qualified `sha1:<hex>` values:
`commit` identifies the admitted snapshot and `changeCommit` the last
first-parent path-blob change at that snapshot. A merge introducing a different blob is attributed to the merge
commit; a revert is a new change. Git author metadata is not authenticated
identity. No email is returned. Named virtual built-ins instead report
`builtinName` and the release-owned `builtinDigest`, without Git reads.

Each lookup independently requires the resource read permission. Ordinary
decisions and reads without the provenance header never traverse history.
Entries are omitted for denied/null response objects. At most 64 returned
resources are inspected per response; one final
`{"error":"PROVENANCE_LIMIT_EXCEEDED"}` record reports truncation.
Reads use the authoritative repository/Namespace UID and a receiver-bound
signed exact-commit RPC. API requests have a five-second deadline and four
source-read slots; native history work retains its repository lock and
four-slot permit even if the request is cancelled. Traversal examines at
most 256 commits within a 30-second native work bound. History gaps,
unsupported peers, saturation, malformed responses or unavailable Git yield
an associated `{"error":"PROVENANCE_UNAVAILABLE"}` entry, leaving ordinary
data readable; there is no fallback to the tier publication actor. Existing
response extensions are preserved. Other resource kinds and the remaining
editor/provenance initiative are not implemented here.

Authenticated operation audit is not Git authorship. The authoritative
CDC-enabled table atomically persists creation/update timestamps and actors
alongside the payload and tier CAS. Actors come from the winning authenticated
subject, the verified committed publication, or an explicitly named repair
service. Server planning overwrites forged input audit values, preserves
immutable creation facts and does not restamp replayed or rejected writes.
Unchanged resources skipped during whole-tier repair retain their existing
audit. Legacy unknown creators remain unknown rather than being inferred from
the latest publication actor. Consolidated migration 014 includes the audit
columns. With explicit approval, 015 was first applied to the owned development
keyspace, then folded into 014 and only that keyspace's ledger was reconciled
under the migration lock. Existing payloads and rows were unchanged; there is
no backfill or fabricated identity and no blanket permission to rewrite
another deployment's applied history.
Audit fields are not newly exposed by typed GraphQL metadata. Git author and
committer persistence/caching/unification is deferred to a separate spec.

Wire/component tests cover opt-in authorization, built-in diagnostics,
signed reads, revert/deletion/first-parent merge attribution, missing parent
objects, history exhaustion and malformed/busy/unavailable responses.
Decision logs retain bounded binding/role UID and rule-index reasons,
the exact evaluated revision/digest vector and the resource Namespace.
They exclude arbitrary resource attributes, raw policy and full memberships.
These checks do not replace deployed identity/replica acceptance.

## Errors and information boundaries

Reuse existing admission `RejectedError` diagnostics, common GraphQL error
envelope and `extensions.admission` for nonfatal reports. Extend their
registered codes rather than inventing a parallel error format.

Required stable distinctions: invalid manifest/scope; forbidden operation;
unprovable grant coverage; immutable built-in/managed binding; conflict;
unresolved role; quota exceeded; and retryable authorization unavailable.
An unresolved reference is normally a status condition, not a failed read.

Logs bound subject/name/reason/revision detail and exclude credentials,
raw manifests and full groups. Unauthorized queries do not disclose target
existence through a different lookup envelope.
