# Data Model: Dynamic RBAC

## Resource identity and envelopes

All four kinds use `apiVersion: rbac.authorization.gitstore.dev/v1beta1`
and the existing metadata/status envelope: name, UID, namespace where
applicable, generation, resourceVersion, Git revision, timestamps,
labels/annotations, finalizers, ownerReferences and conditions. Server-owned
fields cannot be forged in pushed YAML. Generation tracks desired-state
changes; resourceVersion advances for every conditional mutation. Resource
IDs encode kind and immutable UID using existing Node conventions.

| Kind | Scope | Spec | Canonical path |
|---|---|---|---|
| Role | Namespace UID | `rules[]` | `rbac/roles/<name>.yaml` |
| RoleBinding | Namespace UID | `roleRef`, `subjects[]` | `rbac/rolebindings/<name>.yaml` |
| ClusterRole | Cluster | `rules[]` | `rbac/clusterroles/<name>.yaml` |
| ClusterRoleBinding | Cluster | `roleRef`, `subjects[]` | `rbac/clusterrolebindings/<name>.yaml` |

Cluster kinds live in the root system repository; namespaced kinds in their
own namespace's system repository. No path aliases, duplicate names, or
same-name JSON/YAML duplicates. Names obey existing resource-name validation;
the reserved built-in and managed-binding names are protected independently.

## Rules

`resources[]`, `verbs[]`, optional `resourceNames[]` and optional structured
`when[]` preserve v1beta1 spelling and conjunction/union semantics. No
`effect` field. Expand wildcards only over registered catalog pairs; never
create authority for unknown actions. Namespaced Roles cannot include
cluster-scoped kinds. Namespaced bindings constrain ClusterRoles to
namespaced actions in that namespace.

Use the existing closed condition vocabulary, including provider-owned
owner/target-owner/username checks and allowed attribute comparisons.
Do not introduce a new ownership expression language. Canonicalization
sorts sets and preserves logical meaning for deterministic digests.

## Subjects and references

Subjects remain `{kind: User|Group|ServiceAccount, name}`. A User matches the
authenticated subject, not a guessed display name; mapping the namespace
owner to its subject uses trusted UserDir identity. An example where
`alice` is both subject and username is not a requirement that every IdP
uses usernames as subjects. ServiceAccounts retain their cluster-scoped,
UID-authoritative identity contract. Directory existence is not checked
for ordinary user/group subjects.

RoleBinding `roleRef` permits Role in its own namespace or ClusterRole.
ClusterRoleBinding permits ClusterRole only. `roleRef` is immutable.
An unresolved target requires explicit bind authority and grants nothing.
Once resolved, status records the target UID and observed generation, and
the binding carries a blocking ownerReference to it. A same-name role with
a different UID never silently satisfies the old resolved link.

All Roles/RoleBindings have non-blocking Namespace UID references.
Managed `namespace-owner-access` bindings additionally carry protected
managed provenance, Namespace UID, verified owner subject, and provisioning
revision; arbitrary client annotations cannot establish managed identity.

## Lifecycle

```text
Admitted -> Active/Unresolved -> Terminating -> Finalized
```

Unresolved bindings are visible with `RoleResolved=False`; resolved bindings
record `RoleResolved=True` and target UID. A terminating binding, role or
namespace contributes no grants after the ordinary propagation bound.
Binding deletion removes its role reference, allowing the role finalizer
to finish when no live reference remains. Namespace deletion cascades
without being blocked by these children. Completion is UID/resourceVersion
conditional and requires cataloged purge authority.

One RBAC controller group owns `rbac.authorization.gitstore.dev/lifecycle`,
its status fields and lifecycle ownerReferences. Its replicas may repeat
work safely; a stale CAS must reload and recompute. New resources/relations
must be rechecked before final removal, not inferred from stale checkpoint
counts. Never treat lookup errors as absence.

Built-ins are virtual immutable resources identified by kind/name and
release digest. They do not enter deletion or acquire mutable Git manifests.
The initial-admin binding is an ordinary persisted resource with a
once-only root Namespace bootstrap receipt; it is not a system binding.

## Durable projection

One provider-neutral `RBACStore` owns the four discriminated kinds. Plan
014 for the tables and operation audit. The owned development keyspace applied
015 before its explicitly approved consolidation into 014 and ledger
reconciliation; preserve every other deployment's applied migration history.

| Logical table | Key and purpose |
|---|---|
| `rbac_resources_by_scope` | Partition `tierID`; clustering `kind,name`; canonical full envelopes plus a reserved revision/count sentinel in the same partition |
| `rbac_resources_by_id` | Query-first UID lookup projection for Node/get |
| `rbac_bindings_by_role` | Bounded reverse-reference projection keyed by referenced role UID |
| `rbac_role_guards_by_id` | Role UID partition with lifecycle/version sentinel and binding-UID reference claims for cross-tier finalization safety |

The canonical `rbac_resources_by_scope` rows also hold `creation_timestamp`,
`creation_actor`, `update_timestamp` and `update_actor`, written atomically
with their payload and tier revision. These audit authenticated GitStore
operations, not Git author/committer metadata. Creation facts are immutable;
unknown legacy creators are not guessed or backfilled. Repair writes identify
the repair service and skip unchanged resources. Audit values remain internal
to persistence, apart from the existing typed creation timestamp. Optional
resource Git provenance belongs in `extensions.provenance`, never a top-level
resource field.

`tierID` is cluster or Namespace UID, never only namespace name. Secondary
tables are repairable projections, not independent authority. Add indexes
only for documented list/reference queries, using existing repair helpers.
The role guard is durable lifecycle coordination, not a policy copy:
register a resolved reference before publishing it, and condition final
removal on the same guard having no reference claims. A cross-tier binding
cannot race a role's completion merely because its lookup index is delayed.
Claims are idempotent by binding UID and reconciled after crashes.

All writes to the authoritative tier, including status/finalizer changes,
use a same-partition conditional batch with expected tier revision and
resource versions. Increment the sentinel, quota counts and affected rows
atomically. Stable snapshots page the authoritative partition between two
equal sentinel reads. Use consistency levels that cannot miss a completed
CAS; native tests must establish that property.

The authoritative collection uses full-preimage/postimage CDC with 14-day
TTL through the existing catalog CDC registration and shared journal.
Discriminated rows map to the appropriate kind payload; sentinel changes
are progress/rebuild signals, never exposed as user RBAC resources.

## RBAC model snapshot v1

An immutable snapshot contains:

- schema version, tier identity and optional namespace name;
- tier revision, namespace UID/lifecycle observation and canonical Git tip;
- built-in and finite action catalog versions/digests;
- canonical role/binding envelopes and resolved references;
- content digest and source/watch continuity metadata.

Cluster and namespace snapshots compose as an explicitly identified vector,
not one globally atomic revision. OPA receives only selected tier data and
subject-indexed candidate bindings; it must not linearly scan every tenant
per decision. Namespace lifecycle changes invalidate the matching UID even
without a child-binding event.

OPA representation and readiness remain in memory. Restart discards them
and reconstructs from complete durable projections and the watch stream.
Do not persist a provider-native policy copy.

## Access review

`SubjectAccessReview` is a transient envelope in
`authorization.gitstore.dev/v1beta1`, separate from the four Git-backed
kinds. `AccessReviewInput` carries `apiVersion`, `kind` and `spec`;
`AccessReviewPayload.subjectAccessReview` returns the versioned envelope
with echoed `spec` and evaluated `status`. No durable metadata, Node ID,
Git manifest, projection row, lifecycle or watch is created.

`spec`: target `principal.subject`, optional `principal.groups`, canonical
`action` and `resourceRef` selecting authoritative resource identity/context.
The trusted reviewer comes from authentication, not the target principal.
`status`: allow/deny, bounded reason, captured revision vector,
catalog/built-in digests and a flag identifying hypothetical inputs.
Supplied groups cannot impersonate the reviewer or authorize a write.
If groups are omitted, use the configured UserDir's trusted membership;
failure is unavailability, not an empty membership assumption. Explicit
groups, even an empty list, make the result hypothetical.

Any audit event is persisted separately. A distinct
`SelfSubjectAccessReview` API is not introduced.

## Limits

Defaults, enforced together rather than independently promising every
combination fits:

| Bound | Default |
|---|---:|
| Roles per tier | 500 |
| Bindings per tier | 5,000 |
| Rules per role | 100 |
| Subjects per binding | 1,000 |
| Serialized manifest | 64 KiB |
| Changed resources per admitted candidate | 256 |
| Candidate RBAC bytes | 4 MiB |
| Serialized complete tier snapshot | 64 MiB |
| List page | 1,000 resources |

Reject oversize input before staging unbounded work. Built-ins and internal
sentinels do not consume operator resource quotas. Built-in expansion and
subject indexes have their own bounded, measured memory accounting.
