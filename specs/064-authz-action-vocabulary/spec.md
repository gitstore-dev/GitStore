# Feature Specification: Canonical Authorization Vocabulary and rbac-local v1beta1 Reference Implementation

**Feature Branch**: `064-authz-action-vocabulary`  
**Created**: 2026-10-05  
**Status**: Draft  
**Input**: User description: "let's standardise permission actions and ship the reference implementation in rbac-loca according to ADR-0010"

## Clarifications

### Session 2026-10-05

- Policy language v1 (the flat `version: v1` allow/deny document) is **not
  supported**. No v1 loader, no v1→v1beta1 translation, no dual-format period.
  A v1 document is rejected at startup and reload with an actionable error.
- Consequently this feature **amends ADR-0010 §13**: there is no legacy-action
  compatibility shim. Legacy action strings (`.own`, `.any`, `category.*`,
  `namespace.create.organization`/`.user`, `.read.management`,
  `.list.management`, `.delete.complete`, `repository.write`) are rejected
  wherever they appear in policy and are never emitted by any service. This is
  consistent with the alpha scope (no production deployments).
- The ADR-0010 §14 ownership foundation (mutable owner annotation, transfer,
  `EffectiveOwnerSub`) has already shipped.
- Q: How is an explicit deny expressed in v1beta1? → A (revised
  2026-10-06): It is not. RBAC is allow-only (Kubernetes-faithful). The
  earlier per-rule `effect: deny` answer is withdrawn because OpenFGA cannot
  express deny and precedence becomes unnecessary under a pure union.
  Restrictive guardrails (MFA, time windows, change freezes) belong in
  engine-native policy of production providers (spec 066). ADR-0010 §11
  precedence is amended accordingly.
- Production authorization is delegated to mature policy engines. The RBAC
  manifests are a provider-neutral model that each provider translates into
  its native policy (OPA: fixed policy module + data; Cedar: policies +
  entities; OpenFGA: model + tuples). GitStore does not build its own
  production evaluator; rbac-local remains development/evaluation only.
- Q: How are `when` conditions written? → A: A closed, structured condition
  grammar (FR-007a) instead of free-form expressions, so rules translate to
  Rego, Cedar and CEL without loss.
- Q: What role does a namespace play for ServiceAccounts? A: None.
  ServiceAccounts are **cluster-scoped and identified by name only**. The
  free-text `metadata.namespace` label introduced with ServiceAccounts was
  never a GitStore Namespace (nothing validated, cascaded or scoped by it)
  and is removed. The subject is `system:serviceaccount:<name>` everywhere —
  issued tokens, binding subjects, owner encoding — which makes the existing
  owner encoding correct and removes today's mismatch with the
  `serviceaccount:<label>:<name>` token subject. Reach is decided by the
  binding: a `ClusterRoleBinding` reaches all namespaces, a `RoleBinding`
  only its namespace. The implicit group is `system:serviceaccounts` (all
  ServiceAccounts). ADR-0010 §12 is amended (no `<ns>` segment, no
  per-namespace ServiceAccount group); §6 already lists `serviceAccount` as
  cluster-scoped.
- `Principal.IsAdmin()` and role-carrying `Principal.Roles` are removed. No
  authentication provider populates roles, so `IsAdmin()` is always false
  today and the "or is admin" ownership-transfer override is dead code.
  "Unconditional grant" is decided only by `Authorize`.
- Q: Which built-in roles ship? A: Kubernetes-style: ClusterRoles
  `cluster-admin`, `admin`, `edit`, `view`, plus `namespace-owner` and the
  existing `controller`; `system:masters` stays as an empty break-glass group.
- Q: Where does user-defined group membership come from? → A: Out of scope;
  owned by spec 065 (user directory). Existing UserDir providers are `none`
  and `static-users` (whose `ListGroups` returns no groups); 065 decides
  whether groups are sourced from the UserDir provider, the OIDC provider,
  or a hybrid where operator configuration selects which provider owns
  groups. In this feature `principal.Groups` holds only groups
  authentication already supplies plus the synthesized system groups;
  user-defined group bindings validate and are evaluated, but match only
  when a principal actually carries the group. The evaluator consumes
  `principal.Groups` and is agnostic to its source.
- Q: How is the API's internal background identity `system:api` modeled? →
  A: A reserved internal identity produced only by in-process API code;
  bindable as `{kind: User, name: system:api}` solely because it is on a
  fixed internal allowlist; granted a narrow `system:api-internal`
  ClusterRole (repository create/delete, `repository/contents` read/write).
- Q: Can ordinary authenticated users create namespaces? → A: Yes, USER tier
  only, modelled after GitHub personal accounts: at most one USER-tier
  namespace per user, and its name MUST equal the user's username.
  ORGANIZATION tier stays `cluster-admin`-only. The bootstrap `default`
  namespace is ORGANIZATION tier.
- Q: Which denied reads surface as not-found? → A: Only reads denied
  because a visibility condition failed; every other denial is forbidden.
  Wider concealment would require filtering hidden resources from lists,
  which stays deferred (per ADR-0011's deferred authorization mode) until
  resource visibility is built. This feature is the follow-up the
  §14 addendum names: it makes the authorizer, not caller code, decide
  ownership.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator grants least-privilege access with one vocabulary (Priority: P1)

An operator writes a v1beta1 `RbacLocalConfiguration` document (cluster
roles, namespaced roles, cluster role bindings, role bindings) using the
canonical `<kind>[.<subresource>].<verb>` vocabulary, loads it, and every
GitStore operation is authorized against exactly those rules: a binding in
`acme-store` grants nothing in `other-store`, a cluster binding reaches every
namespace, an instance rule reaches only the named objects, and an unmatched
request is denied.

**Why this priority**: This is the reference implementation the ADR promises.
Without it the canonical vocabulary is documentation only and the shipped
policy keeps relying on the drifted strings.

**Independent Test**: Load a v1beta1 policy with one cluster role, one
namespaced role and one instance-scoped role; issue requests as three
principals across two namespaces and verify each allow/deny against the
expected decision table.

**Acceptance Scenarios**:

1. **Given** a `RoleBinding` in `acme-store` binding role `merchandiser`
   (`product` × `create,read,list,update,delete`) to group
   `system:group:merchandiser`, **When** member `bob` creates a product in
   `acme-store`, **Then** it is allowed; **When** `bob` creates a product in
   `other-store`, **Then** it is denied.
2. **Given** a `ClusterRoleBinding` granting `product.read`, **When** the
   bound subject reads a product in any namespace, **Then** it is allowed.
3. **Given** a rule with `resourceNames: ["catalog", "spring-sale"]` on
   `repository/contents` × `read`, **When** the subject reads contents of
   `catalog`, **Then** allowed; **When** it reads `drafts`, **Then** denied.
4. **Given** a `RoleBinding` that references a `ClusterRole`, **When** the
   subject acts in the binding's namespace, **Then** the cluster role's rules
   apply there only.
5. **Given** `namespace` (cluster-scoped kind) and a `RoleBinding` granting
   `namespace.delete`, **When** the subject deletes a namespace, **Then** it
   is denied — only cluster-tier bindings govern cluster-scoped kinds.
6. **Given** a subject that is a member of `system:masters`, **When** it
   performs any action, **Then** it is allowed.
7. **Given** no matching rule, **When** any action is requested, **Then** it
   is denied.

---

### User Story 2 - Every service emits only canonical actions (Priority: P1)

Every authorization check made by the API (GraphQL operations, watches, Git
HTTP, admission) and every action carried in signed API→Git-service
authorizations uses the canonical vocabulary from the ADR-0010 table. The
caller no longer chooses an action based on whether it owns the resource:
`namespace.delete` is checked once and the authorizer decides, using the
resource's owner, whether the caller's grants apply.

**Why this priority**: A policy written in the canonical vocabulary is
useless if services still ask for `namespace.delete.any` or
`repository.read.any`; both halves must ship together.

**Independent Test**: Exercise every protected operation once and record the
action string presented to the authorizer and to the Git service; every
recorded value is in the canonical vocabulary table, and none carries an
`.own`/`.any` suffix or a legacy slug.

**Acceptance Scenarios**:

1. **Given** a namespace owned by group `system:group:platform`, **When**
   member `alice` deletes it with a grant of `namespace.delete` carrying
   `when: owner`, **Then** it is allowed; **When** non-member `carol` with
   the same grant deletes it, **Then** it is denied.
2. **Given** a cluster-tier unconditional grant of `namespace.delete`,
   **When** an administrator deletes a namespace they do not own, **Then** it
   is allowed.
3. **Given** a CategoryTaxonomy operation, **When** it is authorized, **Then**
   the action uses the `categoryTaxonomy` slug; `category.*` is never
   presented.
4. **Given** a Git push, **When** it is authorized, **Then** the API checks
   `repository.contents.write` and the Git service accepts only canonical
   actions in the signed authorization.
5. **Given** a namespace create request of tier `USER`, **When** it is
   authorized, **Then** the action is `namespace.create` with
   `attrs.tier = USER`, and a rule conditioned on
   `{ attr: tier, op: eq, value: USER }`
   decides it; **Given** authenticated user `bob` with only the shipped
   policy, **When** `bob` creates USER-tier namespace `bob`, **Then** it is
   allowed and `bob` owns it; **When** `bob` creates ORGANIZATION-tier `acme`,
   **Then** it is denied.
6. **Given** a product hard-delete, **When** it is authorized, **Then** the
   action is `product.purge`, and a grant of `product.delete` alone does not
   allow it.

---

### User Story 3 - Conditions decide ownership, transfer, visibility and tier (Priority: P2)

A rule may carry `when` conditions from a closed, structured set: `owner`,
`targetOwnerMember`, or a comparison on an allowlisted resource attribute
(`namespace`, `tier`, `visibility`). The authorizer evaluates the condition inside the
matching rule; a false condition contributes no allow. Ownership transfer is
permitted only when the caller owns the resource (or holds an unconditional
cluster grant) and belongs to the target owner (or holds an unconditional
grant).

**Why this priority**: Moves the last Go-level ownership pre-computation into
the authorizer, as ADR-0010 §14 requires, and gives operators ABAC
conditions; it depends on Stories 1 and 2.

**Independent Test**: With `when: owner` and `attrs.*` rules loaded, run
owner/non-owner, member/non-member transfer and visibility-band requests and
compare against the expected decision table, with no caller-side ownership
comparison remaining.

**Acceptance Scenarios**:

1. **Given** a resource owned by user `alice`, **When** `alice` updates it
   under a `when: owner` grant, **Then** allowed; **When** `bob` does,
   **Then** denied.
2. **Given** `namespace.transfer` under `when: owner`, **When** owner
   `alice` transfers to group `system:group:platform` of which she is a
   member, **Then** allowed; **When** she transfers to a group she is not a
   member of, **Then** denied.
3. **Given** a rule `repository/contents` × `read` with
   `when: [{ attr: visibility, op: eq, value: public }]` bound to
   `system:unauthenticated`, **When** an anonymous caller reads a repository
   whose visibility attribute is absent, **Then** denied (fail closed) as
   forbidden; **When** the attribute is `private`, **Then** denied and
   reported as not-found.
4. **Given** a condition that references an attribute the request does not
   carry, or cannot be evaluated, **When** the rule is evaluated, **Then** it
   contributes no allow and the decision reason records why.

---

### User Story 4 - Operators manage v1beta1 policy with the existing tools (Priority: P2)

The existing `make add-role`, `make assign-role`, `make check TARGET=config`
commands and the shipped development `config/policy.yaml` work with the
v1beta1 document. An operator can create a cluster or namespaced role from
canonical action strings, bind it to a User, Group or ServiceAccount at the
cluster tier or in one namespace, and validate the result before startup.

**Why this priority**: Without tooling the reference implementation can only
be hand-edited; it is needed for local development but not for correctness.

**Independent Test**: Starting from an empty file, use the tools to add a
cluster role, a namespaced role and two bindings, then validate; the file is a
valid v1beta1 document that the API loads and enforces.

**Acceptance Scenarios**:

1. **Given** an action list such as `repository.contents.read,product.purge`,
   **When** an operator adds a role, **Then** the tool writes the
   `resources × verbs` rule projection (`repository/contents` × `read`,
   `product` × `purge`).
2. **Given** a legacy or non-canonical action, **When** an operator adds a
   role or validates a file containing it, **Then** the command fails and
   names the canonical replacement where one exists.
3. **Given** an existing identical binding, **When** an operator assigns the
   role again, **Then** the file is unchanged (idempotent).
4. **Given** the shipped development policy, **When** `make compose` starts
   the stack, **Then** the admin, controller ServiceAccount, internal API
   identity and anonymous public reads behave as before this feature.

---

### Edge Cases

- A v1 (`version: v1`) or unknown `apiVersion`/`kind` document → startup
  fails; reload rejects the file and keeps serving the last valid policy,
  logging the rejection.
- A binding referencing an undefined role, a `RoleBinding` without a
  namespace, a `ClusterRoleBinding` with a namespace, or a `roleRef.kind`
  that does not match the referenced map → validation error.
- A namespaced kind rule placed in a cluster role bound only via
  `ClusterRoleBinding` → applies to every namespace (expected, not an error).
- A rule naming a cluster-scoped kind inside a namespaced `Role` → validation
  error (it could never match).
- A request for a namespaced kind that carries no namespace attribute → is
  evaluated at cluster scope only; namespaced bindings never match it.
- Wildcards: `*` in `resources` or `verbs` matches all; partial wildcards
  (e.g. `*.status.write`) are not part of the grammar and are rejected.
- Subject spoofing: a User named `system:masters`, or a User binding whose
  name begins with a reserved `system:` prefix (other than allowlisted
  `system:api`) → rejected at validation; an external token or login whose
  subject is `system:api` → rejected at authentication;
  reserved groups are only honoured via `principal.groups` synthesized by
  authentication.
- User `bob` creates USER-tier namespace `alice` → rejected (name must be
  `bob`); `bob` creates `bob` a second time → rejected as already existing;
  a ServiceAccount creates any USER-tier namespace → rejected.
- A username that is not a valid namespace name → that user cannot have a
  USER-tier namespace; the rejection names the constraint.
- Transfer, rename or tier change of a USER-tier namespace → rejected.
- The bootstrap `default` namespace → bootstrapped as ORGANIZATION tier
  (it matches no username).
- A ServiceAccount request carrying `metadata.namespace` → rejected.
- A token whose subject is `serviceaccount:<label>:<name>` → rejected.
- A `ServiceAccount` binding subject carrying a namespace → validation error.
- An `admin`/`edit`/`view` binding attempting to reach a cluster-scoped kind
  (`namespace`, `serviceAccount`) via `RoleBinding` → never matches.
- Owner is a group and the caller is the creator but not a group member →
  `when: owner` is false.
- Unrecognized verb for a kind (e.g. `namespace.purge`) → validation error.
- An old API replica sends a legacy action to an upgraded Git service during
  rollout → the Git service denies it with an explicit error (fail closed);
  it is never treated as allowed.

## Requirements *(mandatory)*

### Functional Requirements

**Vocabulary**

- **FR-001**: The system MUST define one authoritative, machine-readable
  canonical action vocabulary covering the ADR-0010 §3 table for
  `namespace`, `repository`, `categoryTaxonomy`, `product`, `productVariant`,
  `collection`, `file` and `serviceAccount`, plus the existing system actions
  still required (e.g. namespace system-repository provisioning), each
  expressed in the `<kind>[.<subresource>].<verb>` grammar with the verb as
  the last segment.
- **FR-002**: Every API authorization check, watch authorization, Git HTTP
  check and signed API→Git-service authorization MUST use only actions from
  that vocabulary. No service may emit or accept `.own`/`.any` suffixes,
  `category.*`, `namespace.create.organization`/`.user`,
  `.read.management`, `.list.management`, `.delete.complete` or
  `repository.read`/`repository.write` meaning Git contents.
- **FR-003**: Callers MUST NOT choose an action based on ownership, tier or
  visibility. They MUST pass the facts in `ResourceContext` (`OwnerSub`,
  `Attrs.namespace`, `Attrs.tier`, `Attrs.visibility`,
  `Attrs.targetOwnerRef`) and let the authorizer decide. Existing caller-side
  ownership comparisons MUST be removed.
- **FR-004**: `ResourceContext.Kind` MUST use the canonical kind slug on
  every call (no mixed casing such as `File`/`Product`).
- **FR-005**: The `AuthZProvider.Authorize` signature and `ResourceContext`
  shape MUST remain unchanged.

**Policy document**

- **FR-006**: rbac-local MUST load only
  `apiVersion: rbac.authorization.gitstore.dev/v1beta1`,
  `kind: RbacLocalConfiguration` documents with `clusterRoles`, `roles`,
  `clusterRoleBindings`, `roleBindings` and `defaultDeny` as shaped in
  ADR-0010 §10. Any other version, including v1, MUST be rejected with an
  error that names the supported version. No v1 parsing or translation.
- **FR-007**: Rules MUST carry `resources` (kind or `kind/subresource`),
  `verbs`, optional `resourceNames` and optional `when`. Rules only grant;
  there is no `effect` field and no deny rule. A request is allowed iff some
  bound rule matches (or `system:masters` applies); otherwise denied.
- **FR-007a**: `when` MUST be a list of structured conditions, all of which
  must hold (logical AND). The closed condition set is: `owner` (FR-013);
  `targetOwnerMember` (the caller is, or is a member of, the transfer
  target; FR-015); and attribute comparisons
  `{ attr: <allowlisted attribute>, op: eq | neq | in | notIn, value | values }`
  over `namespace`, `tier`, `visibility`. Free-form expression strings MUST
  be rejected. The grammar exists so that every rule can be translated
  without loss into the native policy of production engines (OPA, Cedar,
  OpenFGA).
- **FR-008**: Validation MUST reject: unknown kinds, subresources or verbs for
  a kind; legacy action spellings (with the canonical replacement in the
  message); partial wildcards; undefined `roleRef` targets; `roleRef.kind`
  mismatches; missing/extra binding namespaces; cluster-scoped kinds in
  namespaced roles; User subjects using reserved `system:` names other than
  the fixed internal allowlist (`system:api`); an `effect` field; unknown
  condition types, operators or attributes in `when`.
- **FR-008a**: Reserved internal identities on the allowlist MUST only be
  produced by in-process API code; any authentication provider presenting
  such a subject MUST be rejected. The internal identity MUST be authorized
  through `Authorize` like any other principal, and the shipped policy MUST
  bind `system:api` to a `system:api-internal` ClusterRole granting only
  `repository` `create`/`delete` and `repository/contents` `read`/`write`.
- **FR-009**: `defaultDeny` absent MUST mean `true`.

**Evaluation**

- **FR-010**: rbac-local MUST implement ADR-0010 §6 scope resolution with
  allow-only union semantics: `system:masters` membership allows; otherwise
  the request is allowed iff any matching cluster binding, namespace binding
  or instance rule allows it; otherwise default deny. rbac-local is a
  development/evaluation provider, not a production policy evaluator;
  production authorization is delegated to mature engines that translate
  the same RBAC model into their native policy (spec 066).
- **FR-010a**: The system MUST provide a provider-neutral decision test table
  (principal facts, action, resource context, RBAC model → expected
  decision) covering every FR-007–FR-016 behaviour; rbac-local and every
  later provider MUST pass it unchanged.
- **FR-011**: Binding subjects MUST match by kind: `User` against
  `principal.Subject`; `ServiceAccount` `{name}` against a principal
  authenticated as ServiceAccount `system:serviceaccount:<name>`; `Group`
  against `principal.Groups` plus the synthesized `system:authenticated` /
  `system:unauthenticated` and, for ServiceAccount principals,
  `system:serviceaccounts`.
- **FR-012**: The principal MUST NOT carry roles. `Principal.IsAdmin()` and
  the role list on `Principal` MUST be removed, including from signed
  API→Git-service authorizations; every "is administrator" decision
  (including the ownership-transfer override) MUST be made by `Authorize`.

**ServiceAccount identity**

- **FR-012a**: ServiceAccounts MUST be cluster-scoped and identified by name
  alone; names are unique across the installation. Create/read/update/
  delete and key operations MUST reject a `metadata.namespace` value.
- **FR-012b**: Issued and accepted ServiceAccount token subjects, binding
  subjects and owner encodings MUST all use `system:serviceaccount:<name>`.
  Tokens bearing the old `serviceaccount:<label>:<name>` subject MUST be
  rejected (no compatibility parsing).
- **FR-012c**: Controller ServiceAccount configuration MUST identify the
  account by name only; the namespace setting is removed and rejected with
  an actionable error if present.

**Built-in roles**

- **FR-012d**: The shipped policy MUST define these ClusterRoles:
  `cluster-admin` (all actions; intended only for `ClusterRoleBinding`);
  `admin` (everything in `edit` plus `purge` and `transfer` on namespaced
  kinds); `edit` (`create`/`read`/`list`/`watch`/`update`/`delete` on
  namespaced kinds plus `management` `read`/`list`); `view`
  (`read`/`list`/`watch` plus `management` `read`/`list`); `namespace-owner`
  (`namespace` `read`/`update`/`delete`/`transfer` with `when: owner`, bound
  to `system:authenticated`); and `controller` (the FR-022 set). `admin`,
  `edit` and `view` grant no `status` writes, no ServiceAccount or
  namespace-creation actions, and are bound per namespace via `RoleBinding`.
- **FR-012f**: The shipped policy MUST define ClusterRole
  `namespace-creator` granting `namespace.create` with
  `when: [{ attr: tier, op: eq, value: USER }]`, bound to
  `system:authenticated`.
  ORGANIZATION-tier creation is granted only by `cluster-admin`.
- **FR-012g**: Namespace admission MUST enforce, for every caller including
  `cluster-admin`, that a USER-tier namespace's name equals its owner's
  username and that its owner is a `User` (never a Group or
  ServiceAccount). Because namespace names are unique this yields at most
  one USER-tier namespace per user. A self-service create whose name differs
  from the caller's username MUST be rejected with an actionable reason.
  USER-tier namespaces MUST NOT be transferred or renamed, and a namespace's
  tier MUST NOT change after creation.
- **FR-012e**: The local development `admin` user MUST be bound to
  `cluster-admin` via a `ClusterRoleBinding`; `system:masters` MUST have no
  default members.
- **FR-013**: `when: owner` MUST be true iff `ResourceContext.OwnerSub`
  equals `principal.Subject` or is in the principal's group set; empty
  `OwnerSub` MUST be false.
- **FR-014**: A condition referencing an attribute the request does not carry
  MUST be false (fail closed).
- **FR-015**: `<kind>.transfer` MUST be allowed only when both ADR-0010 §14
  conditions hold (caller owns, or holds an unconditional cluster grant; and
  caller is a member of the target owner, or holds an unconditional grant).
- **FR-016**: A denied single-object read of a resource whose `visibility`
  attribute is present and not `public` MUST surface as not-found; every
  other denial MUST surface as forbidden. The choice depends only on the
  resource attribute and the deny outcome, never on which rule failed, so
  every provider can honour it. Lists are not filtered by visibility in this
  feature.
- **FR-017**: Every decision MUST carry a reason, naming the matched binding,
  role and rule where the provider can supply it (rbac-local always does),
  and MUST be loggable without exposing secret material.

**Operations**

- **FR-018**: Policy reload MUST be atomic: a valid document replaces the
  active policy in full; an invalid one is rejected and the previous policy
  stays active.
- **FR-019**: `make add-role`, `make assign-role` and `make check
  TARGET=config` MUST read and write v1beta1 documents: add a cluster or
  namespaced role from canonical action strings, bind a User/Group/
  ServiceAccount at cluster tier or in one namespace, idempotently, via an
  atomic YAML-aware write.
- **FR-020**: Startup validation that local users have at least one
  effective permission MUST be evaluated against v1beta1 bindings.
- **FR-021**: The shipped development `config/policy.yaml` MUST be rewritten
  to v1beta1 preserving today's effective access for `admin`, the controller
  ServiceAccount, the internal API identity and anonymous public reads, and
  MUST add no broader access.
- **FR-022**: The controller-manager's required permission set (list, watch,
  status write, product management reads, purge, token issue) MUST be
  expressed in canonical actions and verified against the shipped policy.
- **FR-023**: User-facing documentation (`docs/`) MUST describe the v1beta1
  document, canonical vocabulary and the removal of v1; ADR-0010 MUST be
  updated to record the §13 amendment, allow-only rules (§10/§11, no deny),
  the structured `when` grammar, built-in roles, and the §12 cluster-scoped ServiceAccount subject, and move to
  Accepted.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Every API replica evaluates the same policy
  document deterministically; decisions depend only on the loaded document,
  the principal and the `ResourceContext`, with no per-replica state.
  Rolling upgrade: because v1 is unsupported, the v1beta1 policy file MUST be
  delivered with the new release; an old replica that is handed a v1beta1
  file on reload rejects it and keeps its previous policy, and a new replica
  handed a v1 file refuses to start. Rollout guidance MUST state this
  ordering. Old API replicas presenting legacy actions to an upgraded Git
  service are denied (fail closed); the Git service is singleton and is
  replaced non-overlapping, so the denial window is bounded by the API
  rollout. The ServiceAccount subject change means tokens issued by old API
  replicas are rejected by new ones; controllers re-acquire a token on
  rejection through their existing credential renewal, so convergence is
  bounded by one token exchange per controller after the API rollout. The
  enrolled controller ServiceAccount record MUST be re-created by name only
  as part of the upgrade (documented).
- **PR-002 Multi-User Security**: Default deny; no permission without a
  binding; namespace bindings never leak across namespaces; reserved
  `system:` identities only from authentication; conditions fail closed;
  enumeration protection via not-found; every decision auditable with its
  matched rule. Applies to every AuthN provider (static users, OIDC,
  ServiceAccount, anonymous).
- **PR-003 Capacity**: Authorization MUST add no datastore or network I/O.
  For a policy of up to 200 roles, 1,000 bindings and 2,000 rules and a
  principal with up to 50 groups, a single decision MUST complete in under
  1 ms at p99, and the existing API readiness and repository lifecycle
  latency gates MUST still pass unchanged.
- **PR-004 Backpressure**: Evaluation work per decision is bounded by the
  indexed binding/rule set for the principal's subject and groups;
  conditions are a closed structured set with constant cost per condition
  and a per-rule count limit enforced at validation. Reload never blocks in-flight decisions.
- **PR-005 Capacity Evidence**: `make capacity TARGET=api PROFILE=readiness
  MODE=alpha` and `make capacity TARGET=repository PROFILE=lifecycle
  MODE=alpha` run against the v1beta1 shipped policy, plus a benchmark of the
  PR-003 policy size recorded in the evidence bundle. Domain verifier: every
  request completes with the expected allow/deny, no unexpected
  `FORBIDDEN`/`NOT_FOUND`.
- **PR-006 Fault Recovery**: `make chaos CHAOS_PROFILE=api-restart`: after an
  API replica restart it reloads the same policy and resumes identical
  decisions within the existing readiness deadline; a malformed reload
  during load leaves decisions unchanged.

### Key Entities *(include if feature involves data)*

- **Action (Permission)**: canonical `<kind>[.<subresource>].<verb>` string;
  the only value passed to `Authorize`.
- **Rule**: `resources × verbs` plus optional `resourceNames` and structured
  `when` conditions; grants only.
- **ClusterRole / Role**: named rule sets; scope-agnostic definitions.
- **ClusterRoleBinding / RoleBinding**: `roleRef` (`kind`, `name`) +
  `subjects` (`kind` User/Group/ServiceAccount, `name`); RoleBinding carries
  the namespace that bounds the grant.
- **RbacLocalConfiguration**: the v1beta1 document holding all of the above
  plus `defaultDeny`.
- **ResourceContext**: unchanged carrier of kind, name, owner subject and
  attributes (`namespace`, `tier`, `visibility`, `targetOwnerRef`).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of action strings presented to the authorizer or the Git
  service across a full exercise of every protected operation are members of
  the canonical vocabulary; zero legacy spellings remain in shipped code,
  policy, fixtures or user-facing docs.
- **SC-002**: Zero caller-side ownership comparisons remain; all ownership,
  transfer, tier and visibility decisions come from the authorizer.
- **SC-003**: The ADR-0010 §10 example document, rewritten with structured
  `when` conditions, loads and produces the expected decision for every row
  of the provider-neutral decision table (FR-010a) covering cluster,
  namespace, instance, owner, transfer, visibility, tier, `system:masters`
  and default-deny cases.
- **SC-004**: A v1 policy document is rejected 100% of the time at startup
  and on reload, with a message naming the supported version.
- **SC-005**: The local development stack behaves identically for the
  admin, controller and anonymous flows before and after the policy
  rewrite (no new denials, no new allows).
- **SC-006**: Authorization decisions for the PR-003 policy size complete
  in under 1 ms at p99, and the API readiness and repository lifecycle alpha
  gates pass.
- **SC-007**: With two API replicas and two controllers on the same policy,
  identical requests receive identical decisions; an invalid reload on one
  replica changes no decisions.

## Assumptions

- GitStore is pre-release with no production deployments, so breaking the
  policy format and wire action strings without a compatibility window is
  acceptable (consistent with the alpha scope correction recorded for File).
- rbac-local remains the dev/eval reference; the OPA Rego encoding, persisted
  cluster-tier IAM entities and a ReBAC provider stay out of scope.
- Visibility-aware list filtering and a configurable authorization mode
  remain deferred (ADR-0011) until resource visibility exists.
- The declarative `@authorize` directive (ADR-0011) and the Admin/Storefront
  endpoint split (ADR-0012) are separate features; this feature changes the
  actions existing checks emit, and those features adopt the same
  vocabulary.
- Adding a visibility field to resources is out of scope; visibility
  conditions evaluate against whatever `attrs.visibility` callers already
  supply, and fail closed when absent.
- Authentication synthesizes `system:authenticated` /
  `system:unauthenticated` (and `system:serviceaccounts` for ServiceAccount
  principals) into the principal's groups if it does not already.
- User-defined group membership (e.g. `system:group:merchandiser`) has no
  production source in this feature; acceptance tests for group bindings use
  principals constructed with groups. Group sourcing (UserDir, OIDC, or
  operator-selected hybrid) is spec 065.
- Existing Git history and audit fields keep the old
  `serviceaccount:<label>:<name>` actor string; it is not rewritten. New
  actions are recorded under `system:serviceaccount:<name>`.
- The existing domain verbs (workflow, publication, `iam.manage`) are not
  emitted by shipped code today; the vocabulary reserves them but this
  feature adds no new protected operations.
- `make add-role` keeps its `ROLE`/`ALLOW` interface and drops `DENY` (no
  deny rules); adding a scope
  selector (cluster vs namespaced) and binding namespace follows the existing
  selector-flag convention.
