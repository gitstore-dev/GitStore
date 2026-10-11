# Research: Canonical Authorization Vocabulary

**Date**: 2026-10-10
**Branch**: `064-authz-action-vocabulary`
**Baseline**: `origin/main` at `b11da97`; feature head `e727ce8`.

Research used the repository knowledge graph followed by source inspection of
the API policy/authentication paths, controller credentials, Rust authorization,
datastore implementations, command helpers and deployed validation runners.
Three parallel research agents covered policy evaluation, identity migration,
and enforcement/tooling. This is design evidence, not runtime test evidence.

## 1. One finite vocabulary, generated consumers

**Decision:** Define the catalog in `shared/schemas/authorization-actions.json`
during implementation, using `contracts/action-vocabulary.json` as its reviewed
input. Generate committed Go and Rust lookup tables/constants from it. Extend
the existing `gitctl` command dispatcher and `gitstore-api/generate.go`;
expose a generic root `make generate TARGET=authz` command and include a drift
check in existing build/test validation. No new Go module or third-party library.

**Rationale:** `AuthZProvider` takes strings, Rust enforces signed actions, and
policy tooling needs the same resource/verb projection. A finite catalog prevents
partial wildcards or unknown workflow capabilities becoming implicit authority.
`gitstore-git-service/build.rs` is a no-op and container builds copy the crate's
generated sources; committed generated tables preserve that arrangement.

**Alternatives considered:** Separate hand-maintained language lists drift;
runtime loading adds an operational dependency; a new shared Go module plus a
Rust build-time JSON dependency adds unnecessary packaging work. The catalog
reserves future domain actions without creating corresponding endpoints.

## 2. Strict v1beta1 and bounded immutable snapshots

**Decision:** Replace the v1 structures in
`gitstore-api/internal/auth/provider/rbaclocal/{policy,provider,policyfile}.go`.
Use the existing YAML dependency with strict fields, duplicate-key detection,
exactly one document and validation before publication. Compile bindings to
subject-kind/name, scope and action indexes; retain role/rule references rather
than multiplying copies for each subject. Compile sets for names and operands.
Use immutable snapshots and a brief pointer-swap lock (or atomic pointer), with
serialized reload publication and no evaluation lock held during parsing.

**Rationale:** The current evaluator discards ResourceContext, uses flat subject
bindings, and recognizes only exact action strings or `*`. Its parse-before-swap
reload is reusable. Evaluation must do zero datastore/network I/O and visit only
candidate bindings/rules. Bounds are specified in `contracts/rbac-policy.md`.

**Alternatives considered:** An expression evaluator, global policy scans per
decision, per-request user-directory lookups, and mutable shared rule objects
violate the closed grammar or bounded-work requirements. Production policy
engines and their translators remain spec 066.

## 3. Normative conflict resolutions

**Decision:** Use numbered requirements over older examples and prose:

- FR-016 controls concealment: any denied single-object read with a present,
  non-public visibility string returns not-found. Missing/public visibility
  yields forbidden; the response mapper never parses provider reason strings.
- FR-007/FR-010 and PR-002 require an actual grant. Reject `defaultDeny: false`;
  omission means true. Do not retain the old provider's default-allow mode.
- FR-007a requires a list for `when`. Permit `[owner]`,
  `[owner, targetOwnerMember]` and typed attribute objects; reject scalar
  `when: owner` and free-form expressions, including stale ADR examples.
- A Role definition has no namespace; a RoleBinding determines its scope.
  Reject namespaced Role rules whose wildcard expansion includes cluster kinds.
- Missing request namespace allows only cluster bindings for namespaced kinds.
  Kind scope comes from the catalog, not the presence of an attribute.
- FR-020 is an effective-permission check for each configured active local user,
  including synthesized group bindings. Merely finding one bound administrator
  or a role with no satisfiable grant is insufficient.

**Rationale:** These choices preserve deny-by-default and make the shared
decision table deterministic. They are recorded explicitly so implementation
does not perpetuate incompatible examples.

**Alternatives considered:** Rule-failure-dependent concealment cannot be
implemented consistently across providers; ignoring defaultDeny:false is
misleading; silently accepting old expression syntax creates a second grammar.

## 4. Transfer is an authorizer decision

**Decision:** For each matching transfer rule require
`(owner || (clusterBinding && unconditional)) &&
(targetOwnerMember || unconditional)`, where unconditional means an empty
condition list. Scope, action and resourceNames still constrain that rule. A
valid target is mandatory even for administrative reassignment. Caller code
validates target shape and supplies facts; it does not compare the caller to an
owner. Provider conformance fixtures carry this operation semantic explicitly.

**Rationale:** `security/ownership.go` currently implements both checks using
`IsAdmin`, although authentication never assigns that role. FR-015 distinguishes
the cluster-only current-owner override from the unconditional target override.

**Alternatives considered:** A role-name shortcut, parsing decision reasons, or
merging partial grants from unrelated rules would either retain dead code or
broaden authority. Keep stale-owner/resource-version checks in
`graph/resolver/service.go`: those prevent a write racing a transfer and are not
caller-side authorization comparisons.

## 5. Identity boundary and internal API authorization

**Decision:** Normalize successful authentication centrally in
`internal/auth/registry.go`, including reserved subject checks and exclusive
system groups. Only verified ServiceAccount providers may create canonical SA
principals. External providers cannot supply `system:api`, legacy SA subjects or
reserved User identities. Preserve already trusted groups; add no group source.
Remove Principal.Roles/IsAdmin and protobuf AuthContext.roles; reserve its field
number and name. Explicit in-process internal identity construction must call
the configured authorizer before creating a signed Git authorization.

**Rationale:** The authentication chain currently returns successful provider
principals unchanged. `gitclient/authorization.go` fabricates `system:api` when
no principal is present but does not authorize that identity. Its current
`repository.*.any` policy string does not work as a partial wildcard.

**Alternatives considered:** Trusting a subject prefix, token roles, or caller
metadata lets identities cross trust boundaries. An implicit internal bypass
violates FR-008a. Authentication proof checks for token issuance stay separate
from authorization and cannot be replaced by a role grant.

## 6. Cluster-scoped ServiceAccounts need real storage work

**Decision (updated 2026-10-11):** Replace the namespaced ServiceAccount table
definitions directly in `009_service_account.cql` with name-keyed authority and
cluster UID/bucket lookup tables. The user confirmed that this is undeployed
alpha software with no released ServiceAccount data contract to preserve.
Remove the superseded definitions and code paths; no migration 013, parallel
legacy tables, backfill, dual reads or compatibility layer. Use conditional
name/UID uniqueness and optimistic resource-version writes, with matching memdb
transactions. Fresh development/test schemas enroll accounts by name.

**Rationale:** `scylla/serviceaccount.go` and migration 009 key both authoritative
and name lookup rows by namespace; UID and bucket rows also carry it. Removing
only GraphQL metadata would not enforce global uniqueness. Direct baseline
replacement is now explicitly authorized; preserving old schema/data is not a
requirement. Existing replay-digest and revocation contracts remain in force;
freshly enrolled records get new UIDs. Projection recovery and UID verification
must handle interrupted multi-table writes without false authorization.

**Alternatives considered:** A sentinel namespace, additive migration retaining
old tables, or backfill/rollback machinery preserves an identity model with no
deployed consumers. Those are unnecessary for the confirmed scope. This
feature-specific baseline replacement does not authorize edits to deployed
migration history or deletion of a developer's existing database.

## 7. Rejection-driven credential recovery is not implemented yet

**Decision:** Extend existing controller CredentialSource/client paths with
token-generation-specific invalidation on authentication rejection, one
coalesced reacquisition per rejected token, and a bounded retry for a proven
pre-execution authentication failure. Do not retry ambiguous mutations or
refresh on FORBIDDEN/NOT_FOUND. Apply the same rule to WebSocket setup and
reconnect using the existing durable cursor/backoff path.

**Rationale:** Current CredentialSource only exposes Current, which caches until
expiry minus its refresh margin; HTTP/GraphQL errors do not invalidate it.
The earlier PR-001 assumption that rejection renewal already existed was stale.
The revised requirement covers bounded recovery within the new contract; no
old/new deployment convergence mechanism is needed.

**Alternatives considered:** Waiting for expiry does not meet the recovery
contract; refreshing on every error creates storms; replaying an ambiguously
completed mutation threatens data integrity.

## 8. Enforce invariants at GraphQL and Git admission

**Decision:** Canonicalize every existing check and close missing checks on
touched protected operations. Pass resource facts from existing lookups, keeping
the evaluator I/O-free. Namespace name/owner/tier invariants apply at pre-receive
validation and admission, including create, update, transfer and rename-as-
delete/create. Carry authenticated actor facts through existing signed request
and catalog callout contracts; never trust commit author or manifest actor text.
Bootstrap `default` as ORGANIZATION and reject tier changes in both directions.

**Rationale:** `namespace/admission.go` currently permits arbitrary USER names
and tier promotion; CatalogService validation lacks principal facts. GraphQL
USER creation skips authorization. PascalCase resource kinds and repository
list-as-read still exist. Conversely, all four completion mutations already
emit canonical purge actions on `b11da97`; preserve and test that work.

**Alternatives considered:** GraphQL-only validation is bypassable by push;
requiring a new directive framework or endpoint split exceeds scope (ADRs
0011/0012). A second policy engine in admission is unnecessary.

## 9. Preserve observed access, not ineffective policy spellings

**Decision:** Pin baseline request outcomes before rewriting the shipped policy.
Ship explicit controller and internal grants, the required built-in roles,
USER-tier creator and owner bindings, and a shipped conditional public-reader rule whose
visibility condition fails closed. Do not invent a public visibility attribute
or unconditional anonymous access.

**Rationale:** Current `config/policy.yaml` has no anonymous binding and no
visibility field is being added. Its partial wildcard grants are inert. FR-021's
preservation claim therefore needs measured operation fixtures and an explicit
distinction between existing flows and the newly required self-service flows.

**Alternatives considered:** Translating partial wildcards into broad new grants
would add access; treating all repositories as public would violate PR-002.

## 10. Validation must exercise authorization, not just readiness

**Decision:** Reuse existing capacity and chaos families, adding expected-decision
probes and a sampled decision-latency distribution to their evidence. Run the
specified two-API/two-controller/single-Git topology; preserve current lifecycle
and readiness thresholds. Separate legacy-input rejection tests from healthy
same-version availability evidence. Detailed envelope is in
`contracts/validation.md`.

**Rationale:** `api-readiness.js` only checks health/ready and cannot prove
authorization correctness; average Go benchmark ns/op cannot prove p99. Current
restart chaos requires recovery within 30 seconds. The local policy provider is
dev/eval only: identical documents yield identical decisions, but independently
reloading different documents is not a distributed policy rollout protocol.

**Alternatives considered:** A new authz-only public capacity target, a second
Compose topology, or a test exit status without domain verification duplicates
existing machinery and obscures correctness.

All technical research questions are resolved for planning. No implementation,
capacity pass, production-provider readiness or successful cross-version service
continuity is claimed by these artifacts.
