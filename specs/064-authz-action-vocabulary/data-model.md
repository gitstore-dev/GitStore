# Data Model: Canonical Authorization Vocabulary

Catalog desired-state writes for Product, CategoryTaxonomy, Collection and ProductVariant take an explicit expected resource version. The authoritative write compares that version atomically and advances it; stale writers return `ErrConflict` without replacing ownership or other fields. The precondition comes from the row used for authorization, not a fresh datastore read after authorization. Any retry reloads and reauthorizes.

## Policy and request entities

| Entity                 | Fields and invariants                                                                                                       | Relationships / lifetime                                                                                     |
|------------------------|-----------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------|
| ActionCatalogEntry     | action, kind, scope (cluster/namespace), resource, verb, optional reserved flag; unique exact action and resource/verb pair | Compile-time authority shared by Go/Rust generation, tools and provider validators                           |
| Rule                   | resources[], verbs[], optional resourceNames[], optional when[]; all-only grant, strict finite grammar and bounded sizes    | Belongs to one Role/ClusterRole; refers to catalog pairs                                                     |
| Role / ClusterRole     | map key is name; rules[]; no definition namespace                                                                           | Reusable rule bundle referenced by binding; Role forbids cluster-kind rules                                  |
| RoleBinding            | namespace, roleRef.kind/name, subjects[]                                                                                    | Confines Role/ClusterRole to one namespace                                                                   |
| ClusterRoleBinding     | roleRef ClusterRole/name, subjects[]; no namespace                                                                          | Grants cluster and namespaced actions installation-wide                                                      |
| SubjectRef             | kind User/Group/ServiceAccount, name; no namespace                                                                          | User maps to trusted User subject; SA maps to verified canonical SA identity; Group maps to principal groups |
| Condition              | closed token owner/targetOwnerMember, or attr/op/value(s) tagged union                                                      | Evaluated against one request; all conditions on a rule must hold                                            |
| RbacLocalConfiguration | fixed apiVersion/kind, four definition/binding collections, optional defaultDeny=true                                       | Whole-file replacement only; no datastore-backed IAM entities in 064                                         |
| CompiledPolicy         | immutable indexes, shared compiled rule sets, content digest and source positions                                           | One snapshot per decision; atomic publication on valid reload                                                |
| Principal              | existing subject/issuer/auth method/groups/claims/UID and expiry fields; no Roles or IsAdmin                                | Constructed by AuthN or explicit internal code; no new user-group source                                     |
| ResourceContext        | unchanged Kind, Name, OwnerSub, Attrs                                                                                       | Canonical kind and authoritative resource facts; no caller-computed ownership boolean                        |
| Decision               | unchanged Outcome/Reason/RequestID/At/Provider                                                                              | Provider reason loggable; response mapping does not parse reason text                                        |

Policy bounds, wildcard rules, condition operand types and transfer semantics
are normative in [rbac-policy.md](contracts/rbac-policy.md). Each request can
match multiple bindings, but authority is the union of complete rule matches.
No persisted group, role or policy synchronization service is introduced.

## Ownership and Namespace

Reuse the reserved owner annotation and existing `EffectiveOwnerSub` codec;
keep immutable CreationActor history separate. ResourceContext.OwnerSub comes
from effective ownership, not the authoring actor. Prefix encodings remain User
name, `system:group:<name>` and `system:serviceaccount:<name>`.

Namespace has no new columns. Enforce USER name=User owner username, unique name,
no USER transfer/rename and immutable tier at both preflight and apply. A
cluster-admin grant does not bypass those data invariants. The known
system-managed default namespace uses ORGANIZATION. Preserve generation,
resource-version, finalizer and status behavior from the current mainline.

## Cluster ServiceAccount persistence

Change the ServiceAccount entity and store lookup to name-only identity;
remove its Namespace field/arguments and update decorators and test doubles.
Do not remove namespace from generic metadata or unrelated Principal tenant
fields just because SAs stop using it.

| Table / index                             | Key                                                      | Stored facts / consistency                                                                                                                                          |
|-------------------------------------------|----------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| service_accounts_by_cluster_name          | global name partition                                    | Authoritative UID, timestamps, generation/resource version as currently supported, enrolled public keys, disabled/status metadata; conditional create/update/delete |
| service_accounts_by_cluster_uid           | unique UID                                               | Conditional UID-to-name reservation/routing; lookup verifies authoritative UID/name and completed enrollment                                                        |
| service_accounts_by_cluster_bucket        | bounded listing bucket + existing stable ordering fields | Name/UID listing projection with bounded pages; validate/repair stale projection entries                                                                            |
| Existing replay-digest / revocation state | unchanged                                                | Preserve TTL, single-use assertion and token revocation semantics                                                                                                   |
| Memdb ServiceAccount-specific schema      | unique name plus UID/list indexes                        | Same uniqueness and resource-version contract within one transaction                                                                                                |

Replace the namespaced table definitions in the undeployed alpha baseline
`009_service_account.cql` with these cluster-scoped definitions, as explicitly
confirmed on 2026-10-11. Do not add migration 013 or retain legacy tables/code.
No backfill, identity merge, dual reads or old-schema rollback is required.
Fresh development/test schemas enroll accounts by name with new UIDs; stale
tokens and identity files cannot silently identify a newly enrolled account.
Existing replay/revocation definitions and their semantics remain intact.

Both name and UID uniqueness are enforced under concurrent create, including
same-UID/different-name attempts. Partial-create cleanup is conditional on its
own reservation identity; incomplete enrollment cannot authenticate.

Name authority changes and projection updates are not a cross-partition
transaction. Authentication must verify authoritative state; reads distinguish
I/O failure from not-found; existing bounded audit/repair paths restore missing
or stale projections. Delete/recreate and interrupted update tests must prove a
stale projection cannot authorize a replacement with an old UID.

## State transitions

**Policy:** candidate bytes → strict parse → structural/catalog validation →
effective-user validation → compile/index → atomic active snapshot. Any failure
before publication retains the old snapshot. In-flight readers finish against
the snapshot they already hold. Replica consistency requires the same digest;
independent filesystem reloads do not constitute coordinated production policy
distribution.

**ServiceAccount:** absent → conditional enrollment with new UID → enabled →
key rotation/disabled updates using expected resource version → deletion.
Re-enrollment under the same name creates a different UID. Subject format is
always canonical on new issuance and acceptance; old-format subjects fail.

**Controller token:** no token → one coalesced exchange → cached generation →
expiry-margin refresh or definitive authentication rejection → invalidate only
that generation → bounded coalesced exchange. Stale rejection cannot evict a
new generation. Authorization denial is not a token transition.

**Transfer:** validate target syntax → authorize current owner/target facts →
conditional owner update. If the owner/resource version changes between check
and write, return conflict/retry through the existing bounded path; do not reuse
an old authorization against a new owner.
