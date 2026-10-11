# Tasks: Canonical Authorization Vocabulary and rbac-local v1beta1

**Input**: [spec.md](spec.md), [plan.md](plan.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md).
**Branch**: `064-authz-action-vocabulary`
**Tests**: Required, test-first. Write each slice's tests and demonstrate the intended assertion failure before implementation; compilation failures alone are not the acceptance signal. Preserve passing regression tests, then rerun focused suites and race checks after implementation.
**Format**: `- [ ] Tnnn [P]? [USn]? Description with repository-relative paths`. `[P]` applies only to the disjoint-file waves listed below, after their prerequisites. All tasks are initially incomplete.

## Scope and execution guardrails

- Apply the 2026-10-11 clarification: replace the undeployed namespaced ServiceAccount definitions directly in `gitstore-api/internal/datastore/scylla/migrations/009_service_account.cql`. No migration 013, retained legacy tables, backfill, compatibility parsing, old-fleet cutover or rollback harness. Never silently reset an existing developer database; use an explicitly owned fresh test keyspace.
- Keep `AuthZProvider.Authorize` and `ResourceContext` signatures unchanged. Reuse existing packages, helpers and test files; new catalog/generated/neutral-fixture files and the shared internal RPC signing helper are justified. No new policy service or Go module. The 2026-10-11 user-authorized replay protocol permits direct Rust hmac/sha2/getrandom dependencies; never implement cryptographic primitives locally.
- The 82-action catalog and 55-decision/28-policy-validation contract fixtures are normative. Promote them without weakening expected outcomes; add regressions for uncovered boundaries. Reserved future actions do not authorize implementing their endpoints.
- Interpret concealment using FR-016 and the contracts: denied single-object reads with a string visibility other than `public` become not-found; absent/public/wrong-type visibility becomes forbidden. Lists/watches are not visibility-filtered. Do not invent visibility fields or implement ADR-0011/0012.
- Preserve optimistic concurrency checks: comparing a stale owner/resourceVersion for CAS is not a caller authorization decision. Transfer permission must come from a single matching rule, not combined permissions from different rules.
- Reuse root Make command families, existing capacity runners and Compose wiring. Two APIs/two controllers are required for replica evidence; Git remains exactly one active process with retained storage and non-overlapping replacement.
- Existing uncommitted changes are user-owned. In particular, integrate carefully with `gitstore-api/internal/middleware/security/ownership.go` and `docs/implementation/020-pluggable_auth_architecture.md`; never discard them.

## Phase 1: Setup — establish the baseline

**Purpose**: Inventory the exact operation surface and preserve pre-change access expectations; no project scaffolding is needed.

- [X] T001 Record a protected-operation-to-action/Kind/fact/source/test matrix in `specs/064-authz-action-vocabulary/contracts/service-boundaries.md`, covering GraphQL root and nested fields, node/nodes, typed/generic watches, four existing purge mutations, Git HTTP/gRPC, catalog preflight/admission, controller operations and trusted internal backing work; use this as the completeness checklist.
- [X] T002 Capture current admin/controller/internal/anonymous outcomes and explicitly new USER self-service grants in `tests/integration/authz_repository_contract_test.go` and `tests/integration/serviceaccount_auth_test.go`; distinguish absent visibility from synthetic public/private cases and preserve existing purge regressions.
- [X] T003 Promote `specs/064-authz-action-vocabulary/contracts/decision-table.json` into new `tests/fixtures/authorization/decision-table.json`, retaining all 55 decisions and 28 validation cases unchanged; document its provider-neutral adapter contract in `specs/064-authz-action-vocabulary/contracts/validation.md`.

## Phase 2: Foundational — catalog and reusable test contracts

**Purpose**: Shared prerequisites for every story. Complete this phase before story implementation.

- [X] T004 [P] Add failing catalog uniqueness, valid resource/verb projection, finite wildcard expansion, reserved-action and deterministic generator/drift tests in `gitstore-api/cmd/gitctl/main_test.go`; test Go/Rust generated-table parity without treating legacy diagnostic strings as accepted actions.
- [X] T005 [P] Extend the recording authorizer test helpers in `gitstore-api/internal/middleware/security/graphql_test.go` to assert exact action, canonical Kind, supplied facts and authorizer invocation for the operation matrix, without changing `AuthZProvider.Authorize` or `ResourceContext`.
- [X] T006 Promote the 82 entries from `specs/064-authz-action-vocabulary/contracts/action-vocabulary.json` to new authoritative `shared/schemas/authorization-actions.json`; retain scope, subresources, reserved entries, management and four purge actions, and make the planning copy a verified snapshot rather than another editable authority.
- [X] T007 Implement catalog generation through the existing dispatcher in `gitstore-api/cmd/gitctl/main.go` and `gitstore-api/generate.go`, emitting new committed `gitstore-api/internal/auth/actions_generated.go` and `gitstore-git-service/src/auth/actions_generated.rs`; register the Rust module in `gitstore-git-service/src/auth/mod.rs`, with no runtime JSON/package dependency.
- [X] T008 Add generic `make generate TARGET=authz` and `TARGET=all` dispatch/drift validation in `Makefile`, `scripts/run-make-workflow.sh` and `scripts/test-make-workflow-dispatch.sh`, writing failing selector tests before the dispatcher changes; document the command in `AGENTS.md` and verify deterministic regeneration.

**Checkpoint**: Catalog generation/parity and reusable recorder assertions are testable. The application still uses the old provider until US1.

## Phase 3: User Story 1 — Operator grants least-privilege access with one vocabulary (P1)

**Goal**: A strict, bounded v1beta1 reference evaluator with deterministic allow-union semantics and atomic reload.
**Independent test**: Load cluster, namespace and instance roles; exercise three principals across two namespaces against all neutral decision rows, malformed-policy cases and concurrent reloads. This is the provider-level MVP, not a deployable mixed-contract stack.

### Tests first

- [X] T009 [P] [US1] Add failing provider-neutral fixture adapter and parser/evaluator tests in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`: all 55 decisions/28 validation cases, subject-kind distinctions, namespace isolation, cluster-kind filtering, resourceNames with empty list/watch names, default deny, masters, every limit at boundary and boundary-plus-one, exact condition grammar and single-rule transfer semantics.
- [X] T010 [P] [US1] Add failing secret-safe matched binding/role/rule reason tests and bounded-label assertions in `gitstore-api/internal/auth/logging_test.go`, including missing/wrong-type conditions and default-deny diagnostics without tokens or assertion material.
- [X] T011 [US1] Replace the v1 document model/parser in `gitstore-api/internal/auth/provider/rbaclocal/policy.go` with strict `RbacLocalConfiguration` v1beta1: one YAML document, unique keys, no aliases/cycles, unknown fields/effect/legacy formats rejected, absent defaultDeny=true, explicit false rejected, empty collections valid; report canonical replacements for legacy vocabulary.
- [X] T012 [US1] Implement catalog-backed resources×verbs validation, finite literal `*` expansion, roleRef/subject/namespace validation and all limits in `gitstore-api/internal/auth/provider/rbaclocal/policy.go`: 4 MiB input, 200 roles, 1,000 bindings, 2,000 rules, 64 subjects/binding, 16 conditions/rule, 64 operand values, 256 resourceNames, 128 resources/32 verbs per rule and 256-byte strings; reject overflow without truncation.
- [X] T013 [US1] Compile immutable subject-kind/name, binding-scope and action candidate indexes in `gitstore-api/internal/auth/provider/rbaclocal/provider.go`; share compiled rules, deduplicate binding/rule candidates without losing scope provenance, and enforce the 256-group principal bound before bounded evaluation.
- [X] T014 [US1] Implement allow-only cluster/namespace/instance matching in `gitstore-api/internal/auth/provider/rbaclocal/provider.go`, filtering cluster-scoped pairs from RoleBinding-referenced ClusterRoles, ignoring attrs.namespace for cluster-kind scope, denying namespace binding matches when namespace is absent, and applying instance restrictions to list/watch without inspecting collections.
- [X] T015 [US1] Implement AND-list owner/targetOwnerMember and allowlisted eq/neq/in/notIn conditions in `gitstore-api/internal/auth/provider/rbaclocal/provider.go`; absent or wrong-type facts always fail, empty owner never matches, and transfer requires a valid target plus `(owner || clusterBinding && unconditional) && (targetOwnerMember || unconditional)` within the same matching rule, including target validation before masters.
- [X] T016 [US1] Add failing simultaneous valid/invalid reload, overlapping-binding and no-reader-blocking/race tests in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`; verify rejected v1/oversized documents preserve exactly the previous snapshot.
- [X] T017 [US1] Serialize builders, fully parse/validate/compile before atomic snapshot publication and retain the prior policy on failure in `gitstore-api/internal/auth/provider/rbaclocal/provider.go`; readers must not take a reload lock or perform datastore/network I/O.
- [X] T018 [US1] Write failing active-local-user effective-permission tests in `gitstore-api/internal/auth/chain_test.go`, then update startup validation in `gitstore-api/internal/auth/registry.go` and provider support in `gitstore-api/internal/auth/provider/rbaclocal/provider.go` to consider satisfiable grants through direct/system-group bindings rather than merely finding an admin role.
- [X] T019 [US1] Implement bounded, secret-safe reasons in `gitstore-api/internal/auth/provider/rbaclocal/provider.go` and decision logging in `gitstore-api/internal/auth/logging.go`; run the complete neutral adapter and focused race suite, recording results in `specs/064-authz-action-vocabulary/quickstart.md`.

**Checkpoint**: US1 passes unchanged neutral expectations, strict rejection, scope/condition and reload tests. Run the wrapper-level visibility tests in US3 before claiming FR-016 end-to-end completion.

## Phase 4: User Story 2 — Every service emits only canonical actions (P1)

**Goal**: Canonical enforcement across all services, backed by trusted role-free identities and cluster-scoped ServiceAccounts.
**Independent test**: Exercise every operation in the boundary matrix with a recording provider and signed-wire assertions; every action/Kind is canonical, no caller role bypass remains, legacy inputs fail closed, and account uniqueness/token recovery survive two-API faults.

### Tests first — independent test-file wave

- [X] T020 [P] [US2] Add failing reserved-subject, authenticated/anonymous exclusivity, trusted-group and ServiceAccount-group normalization tests in `gitstore-api/internal/auth/chain_test.go`; cover static/OIDC attempts to present system:api or forge a ServiceAccount subject/UID.
- [ ] T021 [P] [US2] Extend `tests/integration/serviceaccount_auth_test.go` and backend-neutral coverage in `gitstore-api/internal/datastore/memdb/backend_test.go` with failing global name/UID uniqueness, namespace rejection, fresh-schema enrollment, stale UID/delete-recreate, partial-write fault repair and two-API token/replay/key-rotation cases; retain cryptographic proof requirements even with token.issue grants or masters.
- [X] T022 [P] [US2] Add failing credential-generation invalidation/coalescing tests in `gitstore-controller-manager/internal/graphqlclient/credential_test.go` and transport tests in `gitstore-controller-manager/internal/graphqlclient/client_test.go` for HTTP 401, pre-execution GraphQL UNAUTHENTICATED and WebSocket authentication rejection; cover stale errors, forbidden responses and ambiguous mutation completion.
- [X] T023 [P] [US2] Add failing canonical action/Kind and role-free signed-envelope tamper/replay tests in `gitstore-api/internal/gitclient/authorization_test.go` and `gitstore-api/internal/gitclient/auth_test.go`, including method/action mismatch and absence of implicit system:api when no principal exists.
- [ ] T024 [P] [US2] Add failing semantic kind authorization and trusted-actor callback tests in `gitstore-api/internal/cataloggrpc/server_test.go` and `gitstore-api/internal/cataloggrpc/admission_operations_test.go`; bind actor facts to receive/ref/commit/repository and reject forged, missing or stale callback authority.
- [X] T025 [P] [US2] Add failing live-provider denial/reload and bootstrap-order tests in `gitstore-api/internal/app/bootstrap_test.go` and `gitstore-api/internal/gitclient/write_test.go`; distinguish the domain actor from explicitly authorized internal backing writes and prove no new bypass is introduced.

### Implementation — identity, storage and credentials

Treat protobuf regeneration and its consumers as one compile-safe slice. Do not introduce temporary role or legacy-subject compatibility shims.

- [X] T026 [US2] Centralize successful-AuthN principal normalization and reserved external subject rejection in `gitstore-api/internal/auth/registry.go` and `gitstore-api/internal/auth/types.go`; synthesize exclusive system groups and ServiceAccount membership only from verified provider/UID facts, preserve only trusted existing groups, and enforce group bounds.
- [X] T027 [US2] Add provider-specific failing tests before rejecting reserved usernames in `gitstore-api/internal/auth/provider/staticusers/provider_test.go` and `gitstore-api/internal/auth/provider/oidcjwt/provider_test.go`, then enforce them in their `provider.go` files including mapped OIDC username/userinfo paths; do not add a group-membership source.
- [X] T028 [US2] Add failing fresh-schema assertions in `gitstore-api/internal/datastore/scylla/migration_schema_test.go`, then directly replace namespaced SA definitions in `gitstore-api/internal/datastore/scylla/migrations/009_service_account.cql` with `service_accounts_by_cluster_name`, `service_accounts_by_cluster_uid` and bounded `service_accounts_by_cluster_bucket`; preserve replay/revocation schema, with no additional migration or legacy tables.
- [X] T029 [US2] Remove ServiceAccount Namespace and namespaced lookup signatures from `gitstore-api/internal/datastore/entities.go`, `gitstore-api/internal/datastore/serviceaccount.go` and `gitstore-api/internal/datastore/datastore.go`; update `gitstore-api/internal/datastore/instrumented.go`, affected decorators/test doubles and transactional global name/UID indexes in `gitstore-api/internal/datastore/memdb/schema.go` and `gitstore-api/internal/datastore/memdb/backend.go`.
- [X] T030 [US2] Implement authoritative global-name LWT plus conditional UID-to-name reservation and bounded listing projection in `gitstore-api/internal/datastore/scylla/serviceaccount.go`; authenticate only after name authority and UID agree, fail closed on incomplete enrollment, and condition cleanup/deletion on the owned reservation identity so stale work cannot remove or resurrect a replacement account.
- [X] T031 [US2] Add failing interrupted-create/delete/list-projection repair tests in `gitstore-api/internal/datastore/scylla/repair_test.go` and retain replay tests in `gitstore-api/internal/datastore/scylla/serviceaccount_replay_test.go`; extend bounded repair in `gitstore-api/internal/datastore/scylla/repair.go` to converge only current authoritative UID records and rerun the two-API store fault cases.
- [X] T032 [US2] Replace subject parsing/issuance with exact `system:serviceaccount:<name>` in `gitstore-api/internal/auth/provider/serviceaccountjwt/provider.go`, `gitstore-api/internal/auth/provider/serviceaccountassertion/provider.go` and `gitstore-api/internal/graph/resolver/auth_service.go`; update their existing tests to reject all old subject forms, wrong UID/key/assertion and replay while retaining separate token.issue authorization and enrolled-key proof.
- [X] T033 [US2] Write failing namespace-input/output and key/token operation tests in `gitstore-api/internal/graph/resolver/serviceaccount_service_test.go`, then make `serviceaccount_service.go` and `serviceaccount.resolvers.go` cluster-scoped; update `shared/schemas/serviceaccount.graphqls` and regenerate existing GraphQL models/resolvers, retaining shared ObjectMeta but rejecting nonempty namespace on every SA operation and leaving output namespace unset.
- [X] T034 [US2] Write failing removed TOML/env/CLI namespace-setting tests in `gitstore-controller-manager/internal/config/config_test.go`, `gitstore-controller-manager/internal/config/nested_test.go` and `gitstore-api/cmd/gitctl/enroll_serviceaccount_test.go`; update `gitstore-controller-manager/internal/config/config.go`, `gitstore-api/cmd/gitctl/enroll_serviceaccount.go` and controller signers in `gitstore-controller-manager/internal/graphqlclient/private_key_signer.go` and `resolving_signer.go` for name+UID+secret reference with actionable legacy-setting rejection.
- [X] T035 [US2] Implement generation-specific credential invalidation and one coalesced exchange under existing backoff/renewal budgets in `gitstore-controller-manager/internal/graphqlclient/credential.go` and `client.go`; only definitive HTTP/GraphQL/WS authentication rejection triggers refresh, stale failures cannot evict newer tokens, and forbidden/ambiguous mutation failures are not replayed.
- [X] T036 [US2] Remove `Principal.Roles`/`IsAdmin` in `gitstore-api/internal/auth/types.go` and all Go/Rust role copies; remove `AuthContext.roles` and reserve protobuf tag 4 and name `roles` in `shared/proto/gitstore/git/v1/git_service.proto`, regenerate existing bindings, and update `gitstore-api/internal/gitclient/auth.go`, `gitstore-git-service/src/auth/mod.rs`, `interceptor.rs` and affected constructors/tests together; replace every IsAdmin consumer with a live provider decision in this slice so the tree compiles, leaving only non-role fact/presentation cleanup for US3.
- [X] T037 [US2] Extend existing ValidateResources, ValidateResourceDeletions and AdmitResources requests in `shared/proto/gitstore/catalog/v1/catalog_service.proto` with a sanitized trusted actor snapshot (subject, groups, authentication method, ServiceAccount UID) and receive/ref/commit/repository binding; regenerate existing bindings and update `gitstore-git-service/src/git/hooks/mod.rs` so verified envelopes, not arbitrary headers or raw tokens, populate HookContext.

### Implementation — canonical service boundaries

- [ ] T038 [US2] Complete failing recorder coverage in `gitstore-api/internal/middleware/security/graphql_test.go`, `graphql_subscription_transport_test.go` and `graphql_file_status_test.go` for CRUD, list-versus-read, node/nodes/nested namespace routes, typed/generic watches and status; add assertions that all four existing complete*Deletion mutations require purge independently of delete/status grants.
- [ ] T039 [US2] Migrate every protected field and missing namespace/node/nodes check in `gitstore-api/internal/middleware/security/graphql.go` and `secure.go` to catalog actions/canonical Kind plus existing owner/namespace/tier/visibility facts; retain mandatory authorizer invocation and deny unsupported kinds rather than guessing an action.
- [ ] T040 [US2] Migrate remaining watch/status checks in `gitstore-api/internal/graph/resolver/watch.go`, `repository_watch.go`, `product_watch.go` and the resolver files identified by the boundary matrix; preserve typed/generic fail-closed durable-journal behavior and all four already-canonical purge checks, with no process-local fan-out or list/read substitution.
- [X] T041 [US2] Migrate Git HTTP/read/write authorization in `gitstore-api/internal/gitclient/read.go`, `write.go`, `authorization.go` and `stream.go` to repository.contents.read/write or explicit metadata operations as appropriate; inject the live registry-backed authorizer in `grpc_client.go` and remove hard-coded allow/admin paths while preserving sanitized domain actor provenance.
- [X] T042 [US2] Initialize authentication/authorization before bootstrap in `gitstore-api/internal/app/server.go`; explicitly construct trusted system:api only for in-process backing work in `bootstrap.go` and `gitstore-api/internal/gitclient/write.go`, authorize its narrow repository create/delete/contents read/write permissions through the live provider, and never promote a missing principal.
- [X] T043 [US2] Add failing canonical-method and spoofed-actor tests alongside existing Rust auth/GRPC tests, then enforce generated action membership and method/resource matching in `gitstore-git-service/src/auth/interceptor.rs` and `gitstore-git-service/src/grpc/server.rs`; reject legacy actions and never authorize based on caller-supplied roles.
- [ ] T044 [US2] Carry verified HookContext actor facts through `gitstore-git-service/src/git/hooks/validation_handler.rs`, `category_taxonomy_deletion_handler.rs` and `admission_handler.rs`; in `gitstore-api/internal/cataloggrpc/context.go`, `server.go` and `admission_operations.go`, require service authentication plus live semantic per-kind authorization, preserve receive/CAS safeguards and make post-receive policy-change failures observable rather than bypassed.
- [ ] T045 [US2] Run the full boundary recorder/signed-wire matrix, AuthN provider negatives, fresh-schema SA fault/replay cases and controller HTTP/WS rejection recovery in `tests/integration/authz_repository_contract_test.go`, `serviceaccount_auth_test.go` and `secret_bootstrap_test.go`; record exact covered/unsupported paths in `specs/064-authz-action-vocabulary/contracts/service-boundaries.md`, including canonical purge and internal denied-policy checks.

**Checkpoint**: Canonical actions and role-free identity facts traverse every real boundary. Use explicit v1beta1 test policies until US4 replaces shipped fixtures; do not claim default Compose readiness yet.

## Phase 5: User Story 3 — Conditions decide ownership, transfer, visibility and tier (P2)

**Goal**: Remove caller authorization shortcuts while retaining domain invariants and CAS safety.
**Independent test**: Owner/non-owner, target membership, unconditional transfer and visibility matrices pass through real API/Git boundaries on both APIs; USER namespace invariants apply even to admins.

### Tests first

- [ ] T046 [P] [US3] Add failing User/Group/ServiceAccount owner and targetOwnerRef normalization/transfer tests in `gitstore-api/internal/middleware/security/ownership_test.go`; cover empty facts, enum encoding, no double prefixes, unconditional cluster versus namespace grants, split-rule non-composition and stale-owner concurrency conflicts.
- [ ] T047 [P] [US3] Add failing denied single-read/contents/management concealment and independent management-grant tests in `tests/integration/product_authorization_test.go` and `gitstore-api/internal/graph/resolver/repository_authorization_test.go`; verify no-grant private reads become NOT_FOUND, absent/public/wrong-type visibility stays FORBIDDEN, reason text is irrelevant and lists/watches retain existing semantics.
- [X] T048 [P] [US3] Add failing USER namespace name/owner/self-service, forbidden Group/SA owner, rename/transfer/tier-change and concurrent duplicate-create tests in `gitstore-api/internal/namespace/admission_test.go` and `gitstore-api/internal/cataloggrpc/namespace_validation_test.go`; include admin/masters and Git rename expressed as delete+create.
- [X] T049 [US3] Remove caller permission comparisons/action selection from `gitstore-api/internal/middleware/security/ownership.go` and touched resolver transfer paths, using existing owner codecs and tests in `gitstore-api/internal/graph/model/owner_reference_test.go` for normalized facts (regenerate models instead of hand-editing generated definitions); pass targetOwnerRef to the provider and retain syntax/existence plus stale-owner/resourceVersion CAS guards.
- [X] T050 [US3] Implement provider-independent denial presentation in `gitstore-api/internal/middleware/security/secure.go` and `graphql.go`: derive NOT_FOUND only from denied single-object read plus present nonpublic string visibility, never from a reason; keep list/watch results unfiltered and add no resource visibility schema.
- [X] T051 [US3] Replace remaining role-name/admin management-read decisions in `gitstore-api/internal/middleware/security/graphql.go`, `gitstore-api/internal/graph/resolver/product.resolvers.go` and `product_variant.resolvers.go` with independently sufficient product/productVariant.management.read/list provider checks; preserve existing public filtering and the single endpoint.
- [X] T052 [US3] Implement immutable USER name=User owner username, self-service caller-name equality, unique-name limit, no USER transfer/rename and no tier changes in `gitstore-api/internal/namespace/admission.go`; reuse validation from `gitstore-api/internal/graph/resolver/namespace.resolvers.go`, catalog preflight and `gitstore-api/internal/cataloggrpc/admission_operations.go`, including CAS/rename-delete-create bypass prevention.
- [X] T053 [US3] Add a failing fresh-bootstrap tier regression in `gitstore-api/internal/app/bootstrap_test.go`, then initialize the default bootstrap namespace as ORGANIZATION in `gitstore-api/internal/app/bootstrap.go` and align `Makefile`/`scripts/run-make-workflow.sh` defaults; do not add old-row repair or mutation.
- [ ] T054 [US3] Exercise all owner kinds, exact transfer formula, concealment, missing-fact denials and namespace invariants through GraphQL and real Git preflight/CAS on both replicas in `tests/integration/namespace_contract_test.go` and `tests/integration/admission_operations_test.go`; verify zero caller-side permission comparisons while retaining documented concurrency-only comparisons in `specs/064-authz-action-vocabulary/contracts/service-boundaries.md`.

**Checkpoint**: US3 is independently verifiable with its fixture policy and real boundaries; domain invariants cannot be overridden by unconditional grants.

## Phase 6: User Story 4 — Operators manage v1beta1 policy with the existing tools (P2)

**Goal**: Atomic v1beta1 role tooling and coherent shipped alpha configuration with least-privilege built-ins.
**Independent test**: Starting from an empty file, add cluster/namespaced roles and User/Group/ServiceAccount bindings, validate and enforce them; repeated assignment is byte-identical and invalid operations preserve the file.

### Tests first

- [X] T055 [P] [US4] Add failing v1beta1 YAML editing, exact action grouping, scope/roleRef/subject validation, duplicate-idempotence and failed-write preservation tests in `gitstore-api/internal/auth/provider/rbaclocal/policyfile_test.go`; prove resources×verbs grouping cannot introduce any additional action.
- [X] T056 [P] [US4] Add failing CLI selector/DENY/legacy-action diagnostics tests in `gitstore-api/cmd/gitctl/main_test.go` and `scripts/test-make-workflow-dispatch.sh`, including explicitly required namespace for namespace assign-role, no namespace on role definitions, and no accidental use of the bootstrap NAMESPACE default.
- [X] T057 [P] [US4] Add failing shipped-policy effective-grant tests in `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`: all built-ins, no default masters, controller operation inventory plus own token.issue only, narrow internal role, namespace-owner/creator self-service, and anonymous contents read only with an existing public visibility fact.
- [X] T058 [US4] Replace v1 policy editing in `gitstore-api/internal/auth/provider/rbaclocal/policyfile.go` with validated atomic v1beta1 writes; project canonical actions into exact resources×verbs groups, support cluster/namespaced roles and typed bindings, preserve identical files byte-for-byte, and reject invalid candidate documents before replacement.
- [X] T059 [US4] Extend existing runRBAC in `gitstore-api/cmd/gitctl/main.go`, `Makefile` and `scripts/run-make-workflow.sh` with SCOPE=cluster default or namespace, SUBJECT_KIND=User default and ROLE_KIND=ClusterRole default; require explicit namespace only for namespace bindings, reject DENY including Make variable usage, and route make check TARGET=config through the same strict parser.
- [X] T060 [US4] Rewrite `config/policy.yaml` and `gitstore-api/policy.yaml.example` to v1beta1 with cluster-admin, admin/edit/view, namespace-owner, namespace-creator, controller, a separate instance-restricted controller-token role, system:api-internal and conditional public-reader; use exact registered pairs, preserve the baseline matrix, give admin only a cluster-admin binding and no default masters membership.
- [X] T061 [US4] Replace namespaced SA/legacy policy wiring in `config/config.toml`, `compose.yml`, `compose.capacity.yml`, `gitstore-api/cmd/gitctl/enroll_serviceaccount.go` and `scripts/repository-capacity-stack.sh`, updating other existing config/Compose fixtures found by the inventory without adding overlays; retain secret-reference/UID enrollment and no compatibility settings.
- [ ] T062 [US4] Update name-only enrollment and least-privilege expectations in `tests/integration/secret_bootstrap_test.go`, `tests/integration/secret_capacity_contract_test.go` and `scripts/check-enroll-serviceaccount-output.sh`; verify bootstrap, key overlap/retirement and no-secret output against fresh owned fixtures without weakening spec 063 protections.
- [X] T063 [US4] Run the empty-policy CLI workflow and idempotence/invalid-write scenarios in `gitstore-api/cmd/gitctl/main_test.go` and `gitstore-api/internal/auth/provider/rbaclocal/policyfile_test.go`, then validate `config/policy.yaml` through make check TARGET=config; record commands/results in `specs/064-authz-action-vocabulary/quickstart.md`.
- [ ] T064 [US4] Exercise the shipped-policy admin/controller/internal/anonymous baseline and explicitly new self-service cases in `tests/integration/authz_repository_contract_test.go`, `serviceaccount_auth_test.go` and `namespace_contract_test.go`; assert no unintended allow/deny, controller purge coverage and forbidden namespace leakage, retaining negative legacy fixtures separately from shipped examples.

**Checkpoint**: All four story acceptance suites pass using the shipped policy and name-only credentials. The breaking policy/wire/schema baseline is coherent; operational gates remain.

## Phase 7: Polish — operational evidence, documentation and closure

**Purpose**: Prove bounded authorization, replica/recovery behavior and existing latency gates; document readiness precisely.

- [ ] T065 [P] Add an independently sampled p99 benchmark harness and zero-I/O spies to `gitstore-api/internal/auth/provider/rbaclocal/rbaclocal_test.go`, following `specs/064-authz-action-vocabulary/contracts/validation.md`: 200 roles/1,000 bindings/2,000 rules, 50 groups, worst-hit/miss and overlapping candidates, 60-second warmup and 10 minutes each at concurrency 1/8/32; use bounded histograms, not a mean-only Go benchmark.
- [ ] T066 [P] Add failing semantic-probe/evidence-verifier and dispatcher regression tests in `scripts/test-capacity-dispatch.sh`, `scripts/test-repository-capacity-stack.sh` and `tests/integration/repository_lifecycle_capacity_test.go`; require per-replica exact expected decisions and treat deliberately denied probes separately from unexpected HTTP/domain errors.
- [ ] T067 Extend bounded auth decision/reload diagnostics in `gitstore-api/internal/auth/logging.go` and existing capacity exports in `scripts/export-capacity-prometheus.sh`; include catalog/policy/fixture digests, replica identity, reload outcomes and credential recovery evidence without subject/resource-name metric labels, unbounded snapshots or secret-bearing logs.
- [ ] T068 Extend `tests/capacity/profiles/api-readiness.js`, `scripts/repository-capacity-stack.sh`, `scripts/run-capacity.sh` and `scripts/validate-capacity-evidence.sh` with real allow/deny semantic probes, consistent policy digests and domain-verifier output; reuse make capacity selectors and `compose.capacity.yml` with two APIs/two controllers/one Git/three Scylla nodes, retaining all existing lifecycle work/watch/pool/overflow/replacement defaults and latency thresholds.
- [ ] T069 Run the benchmark plus 30-minute concurrent decision/reload soak specified in `specs/064-authz-action-vocabulary/contracts/validation.md`; record sampled p99 <1 ms, zero authorization I/O, bounded allocations/queues and post-GC heap within 10% of stabilized baseline with hardware/runtime metadata in the existing capacity evidence bundle, and link evidence from `specs/064-authz-action-vocabulary/quickstart.md`.
- [ ] T070 Run `make capacity TARGET=api PROFILE=readiness MODE=alpha` against each API using the shipped v1beta1 policy and `tests/capacity/profiles/api-readiness.js`: 10 RPS for 10 minutes per replica, 10 preallocated/100 maximum VUs, p95 <1 s, p99 <3 s, unexpected error rate <0.001, no dropped iterations and exact semantic checks; retain evidence links in `specs/064-authz-action-vocabulary/quickstart.md`.
- [ ] T071 Run `make capacity TARGET=repository PROFILE=lifecycle MODE=alpha` via `scripts/repository-capacity-stack.sh` with sustained real pushes/reconciliation, replay/overflow and the unchanged current lifecycle defaults from `specs/064-authz-action-vocabulary/contracts/validation.md`; require all domain probes, zero unexpected FORBIDDEN/NOT_FOUND, alpha watch p95 ≤2 s (warn above 1 s), retaining the unchanged production p95 ≤1 s/p99 ≤3 s thresholds and complete evidence.
- [ ] T072 Extend and run same-version replacement cases in `gitstore-api/internal/cataloggrpc/namespace_rolling_upgrade_test.go`, `gitstore-api/internal/graph/resolver/namespace_rolling_upgrade_test.go` and `tests/integration/secret_bootstrap_test.go`: two APIs/two controllers retain identical decisions and resume bounded token reacquisition, while retained-storage Git replacement is non-overlapping; test fresh-schema SA deletion/recreation and never simulate an old-fleet compatibility rollout.
- [ ] T073 Run `make chaos CHAOS_PROFILE=api-restart CHAOS_TARGET=<owned-target> CHAOS_CONFIRM=1` through `scripts/run-chaos.sh` during the capacity workload; verify identical policy/decisions and readiness within 30 seconds, inject a malformed/v1 reload on one replica and prove decisions stay unchanged, retaining timing and matrix evidence in `specs/064-authz-action-vocabulary/quickstart.md`.
- [ ] T074 Update `docs/implementation/019-pluggable_auth_design.md`, `docs/implementation/020-pluggable_auth_architecture.md`, `docs/implementation/021-controller_service_account_auth.md`, `docs/runbooks/controller-auth.md`, `docs/development/secret-material.md` and `AGENTS.md` for canonical actions, v1 rejection, structured conditions, no roles, cluster SAs, generic commands and explicitly owned fresh-schema setup; document operator reload diagnostics and flag a gitstore-dev/quickstart follow-up for copied policy/config/Compose/enrollment changes.
- [ ] T075 Amend `docs/ADRs/0010-authorization-model.md` sections 6 and 8–14 plus examples for allow-only rules, condition lists, canonical pairs, trusted identities, cluster SAs, built-ins, no shim and completed authorizer ownership; in the implementing PR, flip Status to `Accepted (YYYY-MM-DD)` using the dating rule in `docs/ADRs/README.md` and update that index in the same PR.
- [ ] T076 Audit the boundary matrix in `specs/064-authz-action-vocabulary/contracts/service-boundaries.md` against code, generated artifacts, shipped policy and docs; fail on emitted/accepted legacy action strings, role-based bypasses, caller authorization comparisons or namespaced SA settings, exempting explicit rejection fixtures and historical explanatory text; verify zero catalog/codegen drift and unchanged neutral expectations.
- [ ] T077 Run every applicable local workflow in `specs/064-authz-action-vocabulary/quickstart.md`, backend-neutral and owned Scylla datastore tests, Go race suites, Rust tests and `make pr-ready` via `Makefile`; after implementation code changes run `graphify update .` for `graphify-out/graph.json`, inspect its diff and record exact test/evidence results without claiming skipped gates passed.
- [ ] T078 Complete the acceptance/evidence ledger in `specs/064-authz-action-vocabulary/quickstart.md` and close `specs/064-authz-action-vocabulary/spec.md` only after required tests and alpha evidence pass; state rbac-local remains dev/eval and record unverified production readiness explicitly—any production claim additionally requires the existing acknowledged 5-million-Product/60-minute/20-worker/1,000-subscription real-push gate from `specs/064-authz-action-vocabulary/contracts/validation.md`, not a new harness or inferred success.

## Dependencies and execution order

Default implementation order is the numbered order. Each phase's test tasks precede its implementation; a later test refinement is written before its corresponding change. Every story has its own acceptance checkpoint, but shared wire/storage boundaries make these **not independently deployable releases**.

```text
Setup T001–T003
  └─ Foundation T004–T008
       └─ US1 T009–T019 (provider MVP)
            ├─ US2 T020–T045 (identity/storage/wire + canonical boundaries)
            │    └─ US3 T046–T054 (real boundary/domain behavior)
            └─ US4 tool-only T055–T059 may start after US1
                 └─ US4 shipped-policy/config/integration T060–T064
                      waits for US2 + US3
                           └─ Polish T065–T078
```

Specific dependency edges:

- T004 and T005 are independent after Setup. T006 → T007 → T008 complete the shared foundation.
- US1 parser/validation T011–T012 precede indexes/evaluation T013–T015; reload tests T016 precede T017; T019 is the full US1 checkpoint.
- US2 tests T020–T025 may be authored in parallel after US1. T026–T027 establish trusted facts; T028–T031 establish storage; T032–T035 establish identity consumers/recovery. T036–T037 must regenerate/update all affected consumers before enforcement integration.
- T038 precedes T039–T040; T041 precedes T042; T037 and T043 precede T044. T045 waits for every US2 path. Run story tests with explicit v1beta1 fixtures until T060 lands.
- US3 depends on US1 condition semantics and US2 authenticated boundaries. T046–T048 precede T049–T053; T054 proves the integrated story.
- US4 tooling T055–T059 can proceed after US1 against fixture documents, but do not concurrently edit `cmd/gitctl/main_test.go` or `scripts/run-make-workflow.sh` with another slice. T060–T064 require US2 identities and US3 domain behavior.
- Polish harness tests T065–T066 precede diagnostics/harness changes T067–T068; T069–T073 need a coherent, clean committed implementation revision and owned provisioned environments. No authorization to commit existing user changes or reset a database is implied by this task list.
- T074–T076 feed T077; T078 requires the preceding acceptance results and required alpha evidence. If an environment is unavailable, keep its task incomplete and record the gap rather than replacing deployed evidence with unit tests.

## Parallel opportunities and examples

These are implementation scheduling examples, not instructions to spawn agents automatically. Only the following disjoint-file waves carry `[P]`; all have satisfied phase prerequisites and no within-wave dependencies.

| Wave | Tasks | Parallel example |
| --- | --- | --- |
| Foundation | T004, T005 | Catalog generator tests and GraphQL recording helper tests in separate packages. |
| US1 | T009, T010 | Strict/neutral provider tests alongside secret-safe decision-log tests. |
| US2 | T020–T025 | AuthN normalization, SA storage/integration, controller credential, signed-wire, callback and bootstrap test files. |
| US3 | T046–T048 | Ownership/transfer tests alongside concealment and namespace-domain tests. |
| US4 | T055–T057 | Atomic YAML helper tests alongside CLI selector and shipped-policy expectation tests. |
| Polish | T065, T066 | Evaluator performance harness alongside capacity semantic-verifier regression tests. |

Serialize edits to shared production files (`provider.go`, `graphql.go`, proto consumers, Makefile and workflow dispatcher), even when their test tasks were parallel. Do not run destructive/fault capacity scenarios concurrently against one fixture deployment.

## Requirement and evidence traceability

| Requirement | Primary tasks / acceptance evidence |
| --- | --- |
| FR-001, FR-002, FR-004, SC-001 | T001, T004–T008, T036–T045, T076 |
| FR-002a (four existing purges) | T002, T038–T040, T057, T060, T064 |
| FR-003, FR-005, FR-012, SC-002 | T005, T036, T039–T044, T046–T054, T076 |
| FR-006–FR-010a, FR-013–FR-015, FR-018, SC-003/004 | T003, T009–T019, T046, T048, T054, T073 |
| FR-008a, FR-011, PR-002 trusted identities | T020, T023–T027, T032, T036–T045, T060 |
| FR-012a–FR-012c, PR-001 storage/credentials | T021–T022, T028–T037, T045, T061–T062, T072 |
| FR-012d–FR-012f, FR-019–FR-022, SC-005 | T002, T018, T055–T064 |
| FR-012g namespace invariants | T048, T052–T054, T064 |
| FR-016 concealment / management | T047, T050–T051, T054 |
| FR-017 diagnostics | T010, T019, T067, T074 |
| FR-023 docs / ADR acceptance | T074–T075, T077–T078 |
| PR-001, PR-002, SC-007 replica/security | T021, T045, T054, T064, T072–T073 |
| PR-003/004, SC-006 bounded performance | T012–T013, T016–T017, T065, T069–T071 |
| PR-005/006 operational evidence | T066–T073, T077–T078 |

## Implementation strategy

1. **MVP: US1 in a provider harness.** Finish Setup/Foundation and US1; demonstrate the neutral scope/instance/condition matrix, strict parser and atomic reload. This is a reviewable evaluation increment, not a runnable mixed-version deployment.
2. **Canonical stack increment: US2.** Replace identities and storage directly, then wire/API/Git checks; verify with explicit test policies. Keep the schema/policy/wire break coherent, never add a transitional reader or translator.
3. **Domain increment: US3.** Verify ownership/transfer/visibility and immutable USER namespace rules independently through the now-canonical boundaries.
4. **Operator increment: US4.** Validate tools from an empty file and replace shipped policy/config; compare baseline outcomes and the explicitly authorized new self-service behavior.
5. **Readiness increment: Polish.** Run sampled p99/soak, actual alpha gates, same-version replacement and chaos with exact decision verifiers; update docs and ADR status only in the implementing PR. Do not claim production capacity from alpha evidence.

## Approved concurrency extension

The user explicitly approved this extension after an audit found that a concurrent ownership change could be overwritten by a stale catalog writer.

- [X] T079 Add backend-neutral stale-writer and concurrent-owner regression coverage for Product, CategoryTaxonomy, Collection and ProductVariant in `gitstore-api/tests/contract/datastore/contract_test.go`.
- [X] T080 Require explicit expected resource versions for all four datastore update methods, enforce them atomically in memdb and Scylla, and update wrappers, admission callers and fixtures. Conflicts must preserve owner, authored fields, status and projections; retries must reload and reauthorize current facts.
- [X] T081 Verify both backends, race tests and admission integration; document the concurrency contract and retain unfinished deployment gates as unchecked.

- [X] T082 User-approved dependency compatibility: retain the upgraded Rust dependency versions in `gitstore-git-service/Cargo.toml`, adapt removed features/API changes, regenerate the lockfile, and rerun Rust tests, clippy and Compose image checks. Do not weaken signed-RPC/replay behavior.

**Task counts**: 82 total — original 78 plus 3 explicitly approved concurrency tasks and 1 approved dependency compatibility task. Eighteen original tasks are marked `[P]`.

**Generation note**: `.specify/scripts/bash/setup-tasks.sh --json` failed because `feature_json_matches_feature_dir` and `resolve_template` were unavailable. The successful `check-prerequisites.sh --json` result and the existing `.specify/templates/tasks-template.md` supplied the feature path/template; no Spec Kit scripts were changed.
