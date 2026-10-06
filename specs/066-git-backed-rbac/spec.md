# Feature Specification: Production Authorization Provider with Git-Backed RBAC Resources

**Feature Branch**: `066-git-backed-rbac`  
**Created**: 2026-10-06  
**Status**: Draft  
**Input**: User description: "let's implement the first production authz provider. The current (proposed) policy file for rbac-local with the k8s inspired envelope is not suitable for a production deployment where policies are granted at runtime. The design should support a git-backed storage group for the following resources - Role - ClusterRole - RoleBinding - ClusterRoleBinding. Design doc 022 contains some initial ideas."

**Depends on**: spec 064 (canonical vocabulary, v1beta1 rule/binding
semantics, built-in roles, evaluator precedence, cluster-scoped
ServiceAccounts); spec 065 (groups and profile type from the UserDir).

## Clarifications

### Session 2026-10-06

- Design doc 022 predates ADR-0010, ADR-0012, ADR-0015, ADR-0018 and specs
  064/065; where they disagree, the later documents win and 022 is amended.
- Q: Consistency model? → A: Bounded staleness from the watch journal for
  ordinary decisions; strict authoritative reads for RBAC writes and access
  reviews (FR-012).
- Q: Is RBAC deletion subject to finalizers and ownerReferences? → A: Yes.
  Bindings hold a blocking ownerReference to their role; Roles/RoleBindings
  hold a non-blocking ownerReference to their Namespace (cascade); deletion
  revokes immediately; one controller owns the finalizer (FR-018a–d).
- Q: Which evaluation engine? → A: GitStore does not build a production
  policy evaluator. The four RBAC kinds are a provider-neutral model; each
  production provider translates the reconciled model into its engine's
  native form and answers `Authorize` at request time. This feature ships the
  embedded **OPA** provider: one fixed GitStore policy module (compiled into
  the API binary) plus the RBAC model translated into OPA data held in memory
  on each replica, rebuilt from the datastore projection and kept current
  from the watch journal. Policy source is never generated per manifest.
  Later providers (Cedar, OpenFGA) translate the same model. rbac-local
  (spec 064) stays development/evaluation only; there is no native `rbac`
  provider.
- RBAC is allow-only and `when` uses spec 064's structured condition grammar,
  so every rule translates to OPA, Cedar and OpenFGA without loss.
- Image-size reduction (stripped builds, folding `gitctl` into the API
  binary, smaller base image) is a follow-up chore issue filed after this
  spec ships, not part of it.
- Q: Do operator guardrail policies (restrict-only OPA policy) ship here? →
  A: No; only the fixed GitStore policy module. Deferred until there is
  customer demand.

## Overview

Spec 064's `rbac-local` reads one operator-edited policy file: changing a
grant means editing a file on every replica and reloading. Production needs
grants changed at runtime, by authorized people, with history, review and
audit. This feature makes the four RBAC kinds first-class **Git-backed
GitStore resources** in the `rbac.authorization.gitstore.dev/v1beta1` group,
following the same storage model as Namespace and Repository:

| Kind                 | Scope     | Desired state lives in                               |
|----------------------|-----------|------------------------------------------------------|
| `ClusterRole`        | cluster   | `gitstore-system/gitstore-system` repository         |
| `ClusterRoleBinding` | cluster   | `gitstore-system/gitstore-system` repository         |
| `Role`               | namespace | that namespace's `gitstore-system` repository        |
| `RoleBinding`        | namespace | that namespace's `gitstore-system` repository        |

Git is the source of truth; admission validates every change; the admitted
state is projected into the datastore and published on the durable watch
journal; the active production authorization provider translates that
projection into its engine's native policy and answers every decision. Changes can be made by Git push
(GitOps, pull-request review) or through GraphQL mutations that commit the
equivalent manifest — both paths go through the same admission.

Design doc 022's direction is carried over where it still holds (embedded
evaluation, no network hop per decision, revisioned IAM snapshots, namespace
isolation, fail-closed on uncertainty, built-in roles immutable) and amended
where ADR-0010 and specs 064/065 superseded it (Role/RoleBinding instead of
`IAMSubjectGrant`/`IAMGroupRoleBinding`, Git instead of datastore as the
authority, structured subjects, no `.own`/`.any`).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Grant and revoke access at runtime (Priority: P1)

A namespace administrator grants a group the `edit` role in `acme-store` by
creating a RoleBinding, and the group's members can edit there within
seconds on every API replica, without any restart or file edit. Deleting the
binding revokes access just as quickly.

**Why this priority**: This is the reason the feature exists.

**Independent Test**: With two API replicas, create a RoleBinding binding
`edit` to `system:group:merchandiser` in `acme-store`; verify a member's
product create succeeds on both replicas within the propagation bound; delete
the binding and verify denial on both replicas within the same bound.

**Acceptance Scenarios**:

1. **Given** no binding, **When** a member of `merchandiser` creates a product
   in `acme-store`, **Then** it is denied.
2. **Given** an authorized administrator creates a RoleBinding in
   `acme-store` for that group to ClusterRole `edit`, **When** the change is
   admitted, **Then** within the propagation bound the same request is
   allowed on every API replica.
3. **Given** the binding is deleted, **When** the propagation bound elapses,
   **Then** the request is denied on every replica, including over
   long-lived subscriptions.
4. **Given** the binding is in `acme-store`, **When** the member acts in
   `other-store`, **Then** it is denied.

---

### User Story 2 - Manage RBAC as code with review and history (Priority: P1)

An operator keeps RBAC manifests in the `gitstore-system` repositories, changes
them by pull request or push, and gets the same validation, history and
rollback as any other Git-backed GitStore resource. GraphQL mutations for the
four kinds produce the same commits.

**Why this priority**: Git-backed authority is the explicit requirement and
gives review, history and rollback for free.

**Independent Test**: Push a commit adding a Role and RoleBinding to
`acme-store/gitstore-system`; verify both are admitted and effective; push an
invalid Role and verify the push is rejected with a reason; revert the first
commit and verify access is removed.

**Acceptance Scenarios**:

1. **Given** a valid Role manifest pushed to a namespace's `gitstore-system`
   repository, **When** admission runs, **Then** it is accepted, projected and
   effective.
2. **Given** a manifest with an unknown kind/verb, a cluster-scoped kind in a
   Role, a legacy action spelling, or a reserved subject, **When** pushed,
   **Then** the push is rejected with the spec 064 validation reason.
3. **Given** a `Role` pushed to `gitstore-system/gitstore-system`, or a
   `ClusterRole` pushed to a namespace repository, **When** admission runs,
   **Then** it is rejected (wrong scope for location).
4. **Given** a GraphQL `createRoleBinding`, **When** it succeeds, **Then** the
   equivalent manifest is committed with the caller as author and is
   indistinguishable from a pushed one.
5. **Given** a revert commit, **When** admitted, **Then** the projection and
   effective permissions return to the earlier state.

---

### User Story 3 - No privilege escalation through RBAC (Priority: P1)

A person allowed to manage Roles and RoleBindings can only grant permissions
they themselves hold in that scope, unless they hold the explicit `escalate`
(for roles) or `bind` (for bindings) permission.

**Why this priority**: Runtime-grantable RBAC without escalation prevention
lets any namespace editor make themselves cluster-admin.

**Independent Test**: As a namespace admin holding `edit`-level product
permissions but not `purge`, create a Role granting `product.purge` (denied),
bind `cluster-admin` to themselves in the namespace (denied), and bind `edit`
to a colleague (allowed).

**Acceptance Scenarios**:

1. **Given** caller lacks `product.purge` in `acme-store` and lacks
   `role.escalate`, **When** they create or update a Role granting it,
   **Then** it is rejected at admission for both Git push and GraphQL.
2. **Given** caller lacks permissions contained in ClusterRole `admin` and
   lacks `roleBinding.bind`, **When** they create a RoleBinding to `admin`,
   **Then** it is rejected.
3. **Given** caller holds every permission in `edit` in `acme-store`, **When**
   they bind `edit` to another subject there, **Then** it is admitted.
4. **Given** any non-cluster-admin, **When** they create or change a
   ClusterRoleBinding, **Then** it is rejected unless they hold
   `clusterRoleBinding.create`/`update` and every permission of the
   referenced role cluster-wide (or `clusterRoleBinding.bind`).

---

### User Story 4 - Safe bootstrap and immutable built-ins (Priority: P2)

A fresh production deployment starts with spec 064's built-in ClusterRoles
and system bindings already effective, plus one operator-configured initial
cluster administrator, so there is always a way in and nobody can delete the
roles the platform depends on.

**Why this priority**: Without a bootstrap path, a Git-backed RBAC store
starts empty and nobody can write the first binding.

**Independent Test**: Start a fresh deployment with an initial-admin subject
configured; verify that subject is cluster-admin, the controller and internal
identities work, and attempts to modify or delete a built-in ClusterRole are
rejected.

**Acceptance Scenarios**:

1. **Given** a fresh deployment, **When** it starts, **Then** built-in
   ClusterRoles (`cluster-admin`, `admin`, `edit`, `view`,
   `namespace-owner`, `namespace-creator`, `controller`,
   `system:api-internal`) and the system ClusterRoleBindings (controller
   ServiceAccount, `system:api`, `namespace-owner` and `namespace-creator` to
   `system:authenticated`) are effective.
2. **Given** a configured initial cluster-admin subject, **When** the
   deployment starts, **Then** that subject holds `cluster-admin`, and the
   binding appears as a normal, removable ClusterRoleBinding once an
   operator chooses to remove it.
3. **Given** any caller, **When** they modify or delete a built-in
   ClusterRole or system binding, **Then** it is rejected.
4. **Given** a release that changes a built-in role, **When** it rolls out,
   **Then** the new definition is effective without manual edits.

---

### User Story 5 - Inspect who can do what (Priority: P3)

An auditor lists Roles, ClusterRoles and bindings, sees each binding's
status (whether its role reference resolves), and asks whether a given
subject may perform an action on a resource.

**Why this priority**: Operability and audit; not required to enforce.

**Independent Test**: Create a binding to a non-existent Role; verify its
status reports the unresolved reference; run an access check for a member
and confirm the answer matches enforcement.

**Acceptance Scenarios**:

1. **Given** a RoleBinding whose `roleRef` does not exist, **When** read,
   **Then** status reports the reference as unresolved and the binding grants
   nothing.
2. **Given** an auditor with `read`/`list` on the RBAC kinds, **When** they
   list them, **Then** results are namespace-isolated per their grants.
3. **Given** an access-review request for (subject, action, resource),
   **When** evaluated, **Then** the answer and reason match what enforcement
   would decide.

---

### Edge Cases

- Projection lag beyond the staleness bound, a lost watch cursor, or an
  unreadable snapshot → affected decisions fail closed (deny, retryable)
  until the replica is current again; never evaluated against known-stale
  RBAC data.
- Role deleted while bindings still reference it → the role enters
  Terminating, grants nothing from that moment, and is removed once no
  binding references it (blockOwnerDeletion).
- Binding whose role is Terminating → grants nothing; deleting or
  re-pointing the binding releases the role.
- RoleBinding references a ClusterRole → permitted (scoped to the binding's
  namespace), per spec 064.
- Namespace deleted → its Roles and RoleBindings are removed with its
  `gitstore-system` repository and stop granting immediately on deletion
  start, not at finalization.
- Concurrent edits of the same manifest via push and GraphQL → resolved by
  Git (non-fast-forward rejected; GraphQL retries on conflict).
- A push touching many RBAC manifests → admitted or rejected as a unit.
- A user removes their own last administrative binding → allowed (not
  blocked), but the initial-admin/break-glass path still exists.
- Duplicate manifest names within a scope → rejected.
- Manifest for a subject that does not exist in the UserDir → admitted
  (subjects are not validated against the directory); it grants nothing until
  such a subject authenticates.
- Very large policies → bounded by FR-019 limits; exceeding them is rejected
  at admission.
- `rbac-local` file policy still configured alongside the new provider → only
  the configured provider is active; no merging.

## Requirements *(mandatory)*

### Functional Requirements

**Resources and storage**

- **FR-001**: The system MUST define `Role`, `ClusterRole`, `RoleBinding`
  and `ClusterRoleBinding` as Git-backed resources in
  `rbac.authorization.gitstore.dev/v1beta1`, using spec 064's rule and
  binding shapes (`rules` with `resources`, `verbs`, `resourceNames`, `when`,
  `effect`; `roleRef` and structured `subjects`).
- **FR-002**: `ClusterRole`/`ClusterRoleBinding` manifests MUST live only in
  `gitstore-system/gitstore-system`; `Role`/`RoleBinding` manifests only in
  their own namespace's `gitstore-system` repository, at a fixed per-kind
  path. Manifests in the wrong location MUST be rejected.
- **FR-003**: Every change, by Git push or GraphQL, MUST pass the same
  admission: spec 064 validation, scope/location checks, built-in protection
  and escalation prevention, evaluated as the pushing or calling principal.
  A push is admitted or rejected as a whole.
- **FR-004**: GraphQL MUST offer create, read, list, update and delete for
  all four kinds; mutations commit the equivalent manifest with the caller as
  author and return after admission, like Namespace and Repository.
- **FR-005**: Admitted RBAC resources MUST be projected into the datastore,
  carry generation, resource version and status like other resources, and be
  published on the durable watch journal.

**Authorization of RBAC changes**

- **FR-006**: The canonical vocabulary MUST gain kinds `role`,
  `roleBinding` (namespaced) and `clusterRole`, `clusterRoleBinding`
  (cluster-scoped) with verbs `create`, `read`, `list`, `watch`, `update`,
  `delete`, plus `role.escalate`, `clusterRole.escalate`, `roleBinding.bind`,
  `clusterRoleBinding.bind`.
- **FR-007**: Creating or updating a Role/ClusterRole MUST be rejected unless
  the caller holds every permission the role grants, in the target scope, or
  holds the corresponding `escalate` permission. "Holds" is decided by
  expanding the role's rules over the closed vocabulary and asking the active
  provider's `Authorize` for each resulting permission, so the check works
  with any provider.
- **FR-008**: Creating or updating a binding MUST be rejected unless the
  caller holds every permission of the referenced role in the binding's
  scope, or holds the corresponding `bind` permission.
- **FR-009**: Built-in ClusterRole `admin` MUST include managing Roles and
  RoleBindings in its namespace (subject to FR-007/FR-008); `edit` and `view`
  MUST NOT include RBAC writes; `view` MUST NOT include reading RBAC
  resources.

**Provider and evaluation**

- **FR-010**: The system MUST provide an embedded OPA AuthZ provider: a fixed
  GitStore policy module shipped inside the API binary that encodes spec 064
  semantics, evaluated against OPA data translated from the reconciled RBAC
  model plus built-ins, with no network call per decision. It MUST pass spec
  064's provider-neutral decision table unchanged.
- **FR-010a**: The translation from the RBAC model to provider-native form
  MUST be a defined, versioned contract (the RBAC model snapshot and its
  revisions) that later Cedar and OpenFGA providers consume unchanged.
- **FR-011**: Each replica MUST hold the translated OPA data in memory, built
  at startup from the datastore projection and kept current from the watch
  journal, replaced atomically per tier (cluster, each namespace) and tagged
  with the tier revision. The datastore holds only the reconciled RBAC
  resources, never a translated copy.
- **FR-012**: Ordinary decisions MUST use watch-driven bounded staleness: no
  per-decision datastore I/O; a tier whose snapshot lags the journal by more
  than a configured bound (default 5 s) or whose cursor is lost MUST deny
  until resynced. RBAC writes (FR-007/FR-008 escalation checks) and access
  reviews (FR-015) MUST instead evaluate against the authoritative current
  revision of every tier they touch, so a stale snapshot can never be used to
  escalate.
- **FR-013**: Any uncertainty — evaluation error, missing tier snapshot,
  lag beyond bound, corrupted or partial snapshot — MUST deny with a stable,
  retryable authorization error and log the cause.
- **FR-014**: Long-lived subscriptions MUST be re-authorized when RBAC
  affecting them changes, within the propagation bound.
- **FR-015**: An access-review query MUST return the decision and reason the
  provider would produce for (subject, groups, action, resource), restricted
  to callers allowed to review that scope.

**Built-ins and bootstrap**

- **FR-016**: Built-in ClusterRoles and system ClusterRoleBindings (spec 064)
  MUST ship with the release, be effective without Git manifests, be visible
  through reads, and be immutable: any create/update/delete targeting their
  names MUST be rejected. A release update to them takes effect on upgrade.
- **FR-017**: The deployment MUST accept a configured initial cluster-admin
  subject that is bound to `cluster-admin` at first start as a normal,
  removable ClusterRoleBinding manifest. `system:masters` remains an empty
  break-glass group.
- **FR-018**: Bindings referencing absent roles MUST be admitted, grant
  nothing and report an unresolved status condition.
- **FR-018a**: Once a binding's `roleRef` resolves, the RBAC controller MUST
  write an `ownerReferences` entry on the binding pointing at its Role or
  ClusterRole with `blockOwnerDeletion: true` (a RoleBinding may reference a
  cluster-scoped ClusterRole). Deleting a referenced role enters Terminating
  and completes only when no binding references it.
- **FR-018b**: Every Role and RoleBinding MUST carry an `ownerReferences`
  entry to its Namespace with `blockOwnerDeletion: false`; namespace deletion
  cascades to them and is not blocked by them.
- **FR-018c**: A Role, ClusterRole or binding MUST stop contributing
  permissions the moment it enters deletion (deletion timestamp set), not
  when finalization completes.
- **FR-018d**: One primary controller group owns the RBAC kinds' finalizer,
  status and ownerReferences (ADR-0018), with a unique finalizer name.
  Built-in roles and system bindings never enter deletion.

**Limits and observability**

- **FR-019**: Admission MUST enforce limits per scope (e.g. ≤500 roles, ≤5,000
  bindings, ≤100 rules per role, ≤1,000 subjects per binding) and reject
  changes exceeding them.
- **FR-020**: Every decision log MUST include the tier revisions evaluated
  against; metrics MUST expose decision outcomes, evaluation latency,
  snapshot lag per tier (bounded cardinality) and fail-closed counts.
- **FR-021**: Every admitted RBAC change MUST be attributable to its Git
  commit and author.

**Coexistence**

- **FR-022**: `rbac-local` (spec 064) remains the development/evaluation
  provider and must pass the same decision table. The provider is selected by
  configuration; there is no merging of providers.
- **FR-023**: The OPA provider MUST load only the fixed GitStore policy
  module; operator-supplied policy (including restrict-only guardrails) MUST
  NOT be accepted in this feature.
- **FR-024**: Design doc 022 MUST be amended to record Git-backed authority,
  the four kinds replacing its IAM entities, and the chosen engine and
  consistency model.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Every API replica converges on the same RBAC
  snapshot revisions from the watch journal; no correctness depends on
  process-local invalidation. Admission is serialized by Git per repository;
  concurrent GraphQL and push writers resolve via Git conflicts. Rolling
  upgrade: replicas on the old provider (`rbac-local`) and new provider must
  not be mixed in one deployment for longer than the rollout; the cutover
  procedure (seed manifests equivalent to the old policy, verify parity via
  access review, switch provider) MUST be documented. The Git service stays
  singleton; RBAC adds repositories content, not Git topology.
- **PR-002 Multi-User Security**: Escalation prevention on every write path;
  built-ins immutable; namespace isolation of Roles/RoleBindings; reads of
  RBAC resources authorized per kind; fail closed on any staleness or
  evaluation uncertainty; audit via Git history and decision logs; no
  secrets in manifests or logs.
- **PR-003 Capacity**: 1,000 namespaces, 50,000 bindings and 10,000 roles in
  total; decision p99 ≤2 ms (embedded engine, prepared once);
  grant/revoke propagation to every replica p95 ≤2 s, p99 ≤5 s; sustained 50
  RBAC writes/min without affecting decision latency.
- **PR-004 Backpressure**: Snapshot rebuilds are bounded per replica (one
  in-flight rebuild per tier, singleflight); watch consumption uses the
  existing bounded journal reader; admission rejects over-limit changes;
  overload in projection never queues decisions — it fails them closed.
- **PR-005 Capacity Evidence**: Reuse `make capacity TARGET=namespace
  PROFILE=watch` for propagation (RBAC kinds as watched kinds) and `make
  capacity TARGET=api PROFILE=readiness MODE=alpha` with the PR-003 dataset;
  the verifier checks each decision against the expected policy at that
  revision and that no grant persists beyond the revocation bound.
- **PR-006 Fault Recovery**: `make chaos CHAOS_PROFILE=api-restart` and a
  watch-journal interruption: affected replicas deny for the gap, resync from
  a full snapshot, and resume correct decisions within 30 s; no replica
  admits a revoked grant after recovery.

### Key Entities *(include if feature involves data)*

- **ClusterRole / Role**: named rule set; cluster or namespace scope; built-in
  flag for release-shipped roles.
- **ClusterRoleBinding / RoleBinding**: `roleRef` + structured `subjects`;
  status condition for reference resolution.
- **RBAC Model Snapshot**: provider-neutral, revisioned view of one tier
  (cluster, or one namespace) of reconciled RBAC resources plus built-ins; the
  translation input for every provider.
- **OPA Data**: the provider's in-memory translation of the snapshots on each
  replica, evaluated by the fixed GitStore policy module.
- **Access Review**: (subject, groups, action, resource) → decision + reason.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Grants and revocations take effect on every API replica within
  2 s p95 / 5 s p99 of admission, with no restarts or file edits.
- **SC-002**: 0 successful privilege escalations across an escalation test
  matrix covering Roles, ClusterRoles, both binding kinds, Git push and
  GraphQL paths.
- **SC-003**: The OPA provider and rbac-local both pass 100% of spec 064's
  provider-neutral decision table.
- **SC-004**: 0 decisions are allowed on a known-stale or missing snapshot
  across fault-injection runs; recovery within 30 s.
- **SC-005**: Every effective permission is traceable to a Git commit, author
  and manifest, or to a named built-in.
- **SC-006**: A fresh deployment is administrable by the configured initial
  admin with no manual Git edits; built-ins cannot be modified (100% of
  attempts rejected).
- **SC-007**: Decision latency stays ≤2 ms p99 at PR-003 dataset size during
  sustained RBAC writes.

## Assumptions

- Groups and profile type come from spec 065's UserDir; RBAC subjects are not
  validated against the directory.
- Persisted RBAC is Role/ClusterRole/RoleBinding/ClusterRoleBinding only;
  doc 022's `IAMSubjectGrant`, `IAMGroupRoleBinding` and `IAMResourceGrant`
  are subsumed (subject or group bindings; instance grants via
  `resourceNames`).
- The four kinds are watched through the durable watch journal like other
  catalog kinds.
- Admin/Storefront endpoint split, `@authorize` directive and visibility-aware
  list filtering remain separate features.
- No role aggregation, role inheritance or nested groups.
- Operator guardrail policies and the extra decision inputs they would need
  (MFA, network, time) are deferred until there is customer demand.
