# Tasks: Implement Secret Material ADRs

**Input**: [spec.md](spec.md), [plan.md](plan.md), [research.md](research.md),
[data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md).
**Baseline**: `d065d08` or a descendant retaining #429, #432, #433, #435 and #437.
**Tests**: Required, test-first. Write the specified assertions before behavior,
run them and confirm the expected failure, then implement and rerun. Reuse
existing test helpers/runners; compiling interface scaffolds are not a
substitute for failing behavioral assertions.

## Format and scope

Each task uses `- [ ] Tnnn [P?] [USn?] Description with paths`.
`[P]` means independent of other tasks in the same test/setup batch once its
stated prerequisites are complete, not independent of earlier phases.
All paths are repository-relative. New files below are proposed unless they
already exist; extend existing equivalent test files rather than duplicating
suites. Recheck current code and orient with `graphify query` before source
exploration. Preserve unrelated worktree edits.

This checklist implements metadata validation, a reusable contract-tested
runtime resolver, production controller bootstrap/renewal and typed nested
configuration. It does **not** implement a File reconciler/source operation,
new status writer, Secret resource, remote SDK, Admin/Storefront split, CRD,
Markdown IR, publication, hook delivery or controller-fencing platform.
ADRs 0010-0018 constrain integration, not an expansion of this scope.

**Alpha scope correction**: There are no production deployments, and File may
break until Release Candidate. T014-T017 and T023 are withdrawn, not completed:
the preparation decoder, migration audit/CLI/gate, release choreography and
mixed-schema compatibility test are unnecessary. Their implementation-only
inventory API and transitional schema have been removed. IDs remain stable
for traceability; this checklist now has 54 active tasks.

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Establish the shared module and reproducible test/build entry points.

- [X] T001 Finalize exported reference/context/material/error/observer signatures in `specs/063-implement-secret-adrs/contracts/resolver.md` and scaffold `shared/secretmaterial/go.mod` plus `shared/secretmaterial/types.go`; retain Go 1.25, service-owned telemetry and non-interchangeable trusted bootstrap/runtime constructors with no new third-party dependency.
- [X] T002 Wire the local module into `go.work`, `gitstore-api/go.mod`, `gitstore-controller-manager/go.mod`, root `Makefile` build/test/lint targets and `.github/workflows/ci.yml`; document command changes in `AGENTS.md` while preserving its `CLAUDE.md` symlink.
- [X] T003 Add reproducible shared-module test fixtures in `shared/secretmaterial/testdata/` for grammar boundaries and synthetic marker material, and test helpers in `shared/secretmaterial/test_helpers_test.go` for call counts, controlled cancellation and atomic record replacement; generate private keys in tests rather than committing usable private material.

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: One bounded acquisition/validation contract shared by both stories.
No production consumer is wired until these tests pass.

### Tests first

- [X] T004 [P] Add pure reference, material-type, authorization and error contracts in `shared/secretmaterial/reference_test.go`, `shared/secretmaterial/resolver_test.go` and `shared/secretmaterial/errors_test.go`: exact grammar/boundaries, explicit empty/null optionals, seven classes, no partial result, wrong tier/principal denied before provider access, unsupported type before I/O and cancellation detectable through `errors.Is` without unsafe cause formatting.
- [X] T005 [P] Add adversarial provider/bounds contracts in `shared/secretmaterial/file_test.go`, `shared/secretmaterial/env_test.go` and `shared/secretmaterial/record_test.go`: traversal/symlink/path-swap attacks, regular-file-only reads, raw/keyed compatibility, duplicate JSON keys, malformed base64, injective environment bindings, whole/item semantics, exact byte/item limits, 32 callers/16 slots, no queue and earlier caller deadlines.

### Implementation

- [X] T006 Implement pure `SecretRef`/`CredentialsRef` validation and safe material/error formatting in `shared/secretmaterial/reference.go`, `shared/secretmaterial/types.go` and `shared/secretmaterial/errors.go`; reject unknown properties and preserve omitted versus explicit-null/empty fields without provider-specific authored fields or default material serialization.
- [X] T007 Implement the trusted resolver, typed `aws-access-key/v1` checks and finite observation interface in `shared/secretmaterial/resolver.go` and `shared/secretmaterial/credentials.go`; enforce authorization before I/O, no permissive authorizer, <=16 in-flight calls, zero wait queue, <=2s/caller budget, one attempt, no caches, safe cancellation and zero partial output.
- [X] T008 Implement bounded closed-envelope parsing and containment-safe local file resolution in `shared/secretmaterial/record.go` and `shared/secretmaterial/file.go`; enforce 256 KiB encoded, 128 KiB total decoded, 64 KiB/item and 32 items, handle atomic replacement without mixed revisions and reject nonregular files/escapes without timeout goroutine leaks.
- [X] T009 Implement explicit one-to-one environment bindings and the existing raw/keyed bootstrap mapping in `shared/secretmaterial/env.go`; isolate runtime/bootstrap bindings, reject ambiguous whole-record raw usage and lossy name normalization, enforce the same bounds, and document process-replacement-only environment rotation in `specs/063-implement-secret-adrs/contracts/resolver.md`.
- [X] T010 Run focused shared-module contract/race tests and verify `Makefile` test aggregation; complete leakage/observer assertions in `shared/secretmaterial/resolver_test.go` and `shared/secretmaterial/observer_test.go`: all material/error formatters, JSON/YAML attempts, fixed label categories, denied-call counts, cancellation, saturation and bounded operation-local lifetimes; do not claim guaranteed Go-memory erasure.

**Checkpoint**: Both stories can use a tested shared boundary. The API will
import pure validation only; production runtime construction remains absent.

**Execution note (2026-10-03)**: T001-T010 are complete. The standalone module
passes Go 1.25 and current-toolchain race tests; root aggregation is wired.
T039's image packaging was advanced because T002's local replacements broke
the flattened Docker build immediately. Both independent images now build
with `GOWORK=off`; no production consumer has been changed.

**Strict File checkpoint**: The ten active US1 tasks are complete.
Bare-reference rejection and strict schema tests failed against the former
decoder and now pass. Catalog, validate, GraphQL resolver, catalog-gRPC, security
and shared runtime-operation suites pass with race detection; all nine Rust
validation-handler tests pass. The API builds with `GOWORK=off`. Gqlgen output
retains the pre-existing Namespace input defaults. No migration, deployment or
capacity completion is claimed.

**Controller implementation checkpoint**: Canonical nested configuration and
all known native/Compose/example producers are wired; obsolete source paths
fail even when overridden. The shared bootstrap adapter replaces the divergent
provider implementation. Atomic signing records, fresh exchange-local signing,
the total exchange deadline, post-jitter retry cap and fixed-category acquisition
metrics are implemented. Full controller race/vet suites and independent
API/controller builds pass; API enrollment/config/service-account regressions
remain green. Rotation evidence uses in-process independent sources and HTTP
verifiers, not deployed API/controller processes. T029 reuses existing
source-provenance/AuthN/AuthZ tests rather than duplicating those contracts.
External integration is now demonstrated by the combined deployed bootstrap/
rotation harness (see the later acceptance checkpoint). The capacity scenario
extension remains open.

**Production-evidence boundary**: T058 requires a clean committed verifier,
matching release images, the specified owned topology, five-million-row dataset
and full-duration run. Local commit and isolated deployment/fault preparation
are authorized; certification remains pending actual observations.
T052-T056 implementation is connected and locally validated, including owned
fixtures, scheduled faults, process resource/log capture and immutable whole-run
assembly/scanning. Missing prerequisites still fail closed, rather than running
Repository-only load. No full scheduled capacity deployment or passing production
bundle has been produced; functional integration and image builds cannot replace
that evidence.

## Phase 3: User Story 1 - Reference Integration Credentials Safely (P1)

**Goal**: Portable explicit File credentials and provider-free admission through
a direct alpha breaking change.
**Independent test**: Valid same-namespace metadata round-trips through normal
Git admission and GraphQL; malformed/cross-namespace/value-bearing documents
fail with zero provider calls and no durable write. A contract-only runtime
consumer rejects unsupported/incomplete credentials without performing its
dependent operation.

### Tests first

- [X] T011 [P] [US1] Add strict-shape fixtures/tests in `gitstore-api/internal/catalog/file_test.go`, `gitstore-api/internal/validate/file_credentials_test.go` and `gitstore-api/internal/validate/testdata/file/`; cover every discriminator/length/namespace/null/unknown-field boundary, absent optional credentials and syntactically valid unsupported types without provider access.
- [X] T012 [P] [US1] Add validation/admission parity and unchanged File lifecycle/auth tests in `gitstore-api/internal/cataloggrpc/file_isolation_test.go`; use `new_commit_sha` and `authorization.actor`, assert no provider I/O or writes on rejection, preserve reserved owner annotations and test current ownership rather than assuming creator equals owner.
- [X] T013 [P] [US1] Add output/schema contracts in `gitstore-api/internal/graph/resolver/file_credentials_test.go` and `gitstore-api/internal/validate/schema_contract_test.go`; cover nested metadata-only output, strict required fields, no bare-reference alternative and rejection of obsolete projections without inferred types.

### Strict implementation

- [X] T018 [US1] Require strict explicit `CredentialsRef` in `gitstore-api/internal/catalog/file.go` and existing validation/admission wiring in `gitstore-api/internal/cataloggrpc/server.go`; reject bare references directly, with no compatibility switch, mutating admission or payload-fact gate.
- [X] T019 [US1] Align `schemas/gitstore/v1beta1/file.schema.json` and `shared/schemas/file.graphqls` with the strict contract, wire `gitstore-api/internal/graph/resolver/converters.go`, and regenerate `gitstore-api/internal/graph/model/` and `gitstore-api/internal/graph/generated/` using existing gqlgen tooling; if ADR-0012 has relocated schemas, use only admin/common inputs and preserve the configured current Admin endpoint.
- [X] T020 [US1] Update `gitstore-api/internal/validate/testdata/file/product-hero.md` and directly related File fixtures/examples for the explicit wrapper; run Go/schema parity assertions from T011-T013 and verify unsupported but well-formed types still pass provider-free admission.
- [X] T021 [US1] Extend hook rejection/acceptance tests in `gitstore-git-service/src/git/hooks/validation_handler.rs` using the same File fixtures and current protobuf fields; prove the hook delegates to Go, returns safe field reasons and performs no credential resolution, manifest writeback or secret-byte handling.
- [X] T022 [US1] Add a contract-only dependent-operation consumer in `shared/secretmaterial/runtime_contract_test.go`; prove required AWS items, optional session token, whole/item behavior, denied namespace/principal/binding, unsupported types and provider failures block the operation with no File status writes.
- [X] T024 [US1] Document strict File metadata and deferred runtime behavior in `docs/resource-storage/lfs-object-storage.md` and `docs/implementation/034-file-media-lifecycle-architecture.md`; distinguish external inline SecretRef from CRD ownership/readiness and exclude resolved bytes from future IR/publication/Storefront projections.
- [X] T025 [US1] Run the focused US1 unit/schema/hook/catalog-gRPC integration slices using the existing Go/Rust runners, reconcile failures against `specs/063-implement-secret-adrs/contracts/references.md`, and record actual results and the alpha breaking-change/client instructions in `specs/063-implement-secret-adrs/quickstart.md`; no migration audit or deployment prerequisite applies.

**Checkpoint**: US1 is independently demonstrable without a File runtime
controller or production bootstrap changes. No separate preparation artifact
or deployed-data migration is required.

## Phase 4: User Story 2 - Bootstrap a Process Identity Privately (P1)

**Goal**: Typed, canonical configuration and owning-process-only bootstrap
through the shared resolver, without changing the normalized identity protocol.
**Independent test**: TOML-only/env-only/mixed configuration yields equivalent
typed settings; obsolete or unsafe sources fail before authenticated work.
A valid controller obtains its own token; missing/denied material and another
subject fail closed without unauthenticated fallback or leakage.

### Tests first

- [X] T026 [P] [US2] Expand `gitstore-controller-manager/internal/config/config_test.go` for every relocation in `specs/063-implement-secret-adrs/contracts/configuration.md`: canonical env-only leaves, one decode, TOML/env precedence, defaults/units, zero resync, malformed durations, legacy keys even when overridden, duplicated `controller.controller`, unknown service-owned keys and valid sibling/provider-material sections.
- [X] T027 [P] [US2] Add bootstrap-adapter/startup/renewal-boundary tests in `gitstore-controller-manager/internal/secret/resolver_test.go` and `gitstore-controller-manager/cmd/controller/main_test.go`; assert configured owner/tier isolation, raw/keyed semantics, no anonymous work on failure, classified fatal startup, unchanged usable-token readiness and cancellation during the existing tracked-runner/final-flush shutdown.
- [X] T028 [P] [US2] Add enrollment producer and deployment checks in `gitstore-api/cmd/gitctl/enroll_serviceaccount_test.go` and `scripts/test-secret-config.sh`; test canonical emitted keys, shared-config/private-key rejection, cross-service mount overlap, non-sensitive diagnostics and version-matched old/new config snapshots.
- [X] T029 [P] [US2] Add API source-provenance and pluggable-auth regression cases in `gitstore-api/internal/config/config_test.go` and `gitstore-api/internal/graph/resolver/serviceaccount_service_test.go`; retain pre-env signing-key provenance checks, normalized token-request payloads, owning-subject issuance and denial without provider/private-key exposure or persisted token status.

### Configuration before bootstrap integration

- [X] T030 [US2] Refactor `gitstore-controller-manager/internal/config/config.go` to nested `ServiceAccount`, `SecretProviders.Bootstrap`, `Checkpoint`, `Reconcile` and `Watch` structs; use `GITSTORE` with `.` to `__`, leaf defaults and one typed decode, remove `bindServiceAccountEnvironment`/`readServiceAccountConfig`, and reject old/unknown owned source keys before defaults erase presence without a generic shared config framework.
- [X] T031 [US2] Migrate consumers in `gitstore-controller-manager/cmd/controller/main.go` and their tests to typed settings; preserve port/API URI, identity, audiences, retry/watch/default units, checkpoint location, tracked runners and 5s final-flush shutdown rather than re-reading Viper or changing behavior.
- [X] T032 [US2] Migrate `config/config.toml`, `compose.local.yml` and `compose.capacity.yml` to canonical nested keys and all directly referencing environment fixtures; maintain separate read-only controller identity mounts and binary-matched config snapshots, keep `/graphql` until the independent ADR-0012 rollout allows a change.
- [X] T033 [US2] Migrate emitted config/environment instructions in `gitstore-api/cmd/gitctl/enroll_serviceaccount.go`, startup/bootstrap config producers, and checkpoint cleanup/config consumers in `Makefile`; preserve effective values and make cleanup resolve the renamed checkpoint path without broad deletion.
- [X] T034 [US2] Update `scripts/check-local-compose-config.sh`, `scripts/repository-capacity-stack.sh`, `scripts/test-repository-capacity-stack.sh` and `scripts/test-capacity-dispatch.sh` for canonical keys and effective-config evidence; run T026/T028/T029 and root config/Compose checks, preserve Rust prefix/separator parity, and reject any unwired old spelling rather than accepting an alias.
- [X] T035 [US2] Replace divergent bootstrap loading with a shared-module adapter in `gitstore-controller-manager/internal/secret/resolver.go`; bind owning-process identity and allowed bootstrap reference at construction, preserve raw/keyed material under canonical config, apply bounded acquisition/classification and reject ambiguous or runtime-tier configuration without a production runtime provider block.
- [X] T036 [US2] Wire the adapter into `gitstore-controller-manager/cmd/controller/main.go` and `gitstore-controller-manager/internal/graphqlclient/private_key_signer.go`; retain authorized #429 token exchange/startup failure semantics, no GitStore credential dependency for resolution, no health-only fallback, and service-owned parsing of supported private keys.
- [X] T037 [US2] Add fixed-label Prometheus/log observations in `gitstore-controller-manager/internal/secret/metrics.go` and tests in `gitstore-controller-manager/internal/secret/metrics_test.go`; register with the existing process registry, cover success/seven classes/cancel/deadline and never label names, namespaces, paths, IDs or secret bytes.
- [X] T038 [US2] Add checkpoint/config replacement regressions in `gitstore-controller-manager/cmd/controller/main_test.go` and the existing `gitstore-controller-manager/tests/checkpoint/filesystem_test.go`; prove separate deployment/group roots, preserved snapshots/cursors/related replay keys and final flush under cancellation, with duplicate replay protected by existing conditional/idempotent reconciliation rather than a new lease.
- [X] T039 [US2] Fix relative-module Docker build layouts in `docker/api.Dockerfile` and `docker/controller-manager.Dockerfile` after first reproducing isolated-build failures; retain service subdirectories plus `shared/secretmaterial`, prove independent `GOWORK=off` builds, preserve runtime binary/schema paths and never copy provider material into images.
- [X] T040 [US2] Document canonical config renames, source isolation, existing primary/secondary watch ownership and checkpoint namespace allocation in `docs/implementation/021-controller_service_account_auth.md` and `docs/development/secret-material.md`; identify RBAC/finalizer ownership, explain that atomic checkpoints/materializer leases do not fence controller writes, and preserve ServiceAccount audit-only ownership.
- [X] T041 [US2] Run focused US2 tests and two-API/two-controller bootstrap cases in `tests/integration/secret_bootstrap_test.go`; prove independent token issuance/replacement with current pluggable AuthN/AuthZ, denied subjects and no material/token/assertion leakage except authorized token delivery, without introducing a group leader or Storefront identity surface.

**Checkpoint**: US2 works without US1's File consumer changes. New instances
use new config snapshots; old binaries retain their compatible snapshots.
Private-key refresh across later exchanges is completed by US3, not inferred
from startup success.

## Phase 5: User Story 3 - Rotate Material Behind Stable References (P2)

**Goal**: Every dependent operation/renewal resolves fresh material; atomic
private-key/key-ID records enable enrolled-key overlap without resource edits.
**Independent test**: Replace an enrolled file record behind the same logical
reference, observe a new assertion/token on both controllers, then retire the
old key after overlap. Runtime contract retries see a new provider revision;
environment-provider changes require process replacement.

### Tests first

- [X] T042 [P] [US3] Add renewal/rotation/backoff tests in `gitstore-controller-manager/internal/graphqlclient/credential_test.go` and `gitstore-controller-manager/internal/graphqlclient/private_key_signer_test.go`; require fresh resolution per exchange, one exchange in flight, 10s total including resolution, 45s assertions, 30s refresh margin, final jitter <=30s, canceled calls not retried and no retained private signer.
- [X] T043 [P] [US3] Extend `shared/secretmaterial/runtime_contract_test.go` and `env_test.go` with real atomic runtime revision replacement and subprocess environment replacement semantics; block dependent work and return no partial material on failure. Reuse atomic `serviceaccount-signing-key/v1` key/ID and mismatched/partial/unsupported record cases in controller `internal/secret/resolver_test.go` and `internal/graphqlclient/resolving_signer_test.go`; crypto-specific pairing does not belong in the shared record decoder.
- [X] T044 [P] [US3] Add real rotation/outage scenarios in the combined `tests/integration/secret_bootstrap_test.go` harness: distinct arbitrary enrolled IDs, wrong/revoked/mismatched keys, outage spanning token expiry, unaffected peer authenticated availability, new process bootstrap and authorized-only token delivery with marker-secret scanning. Reuse one test-owned deployment lifecycle rather than duplicating it in another test file.

### Implementation and integration

- [X] T045 [US3] Implement the typed atomic signing-record parser in `gitstore-controller-manager/internal/secret/signing_record.go` using `shared/secretmaterial/record.go`; require paired `privateKey`/`keyID` from one whole-record read, prohibit selected-item records bypassing the pair and retain configured static ID only for raw/keyed compatibility.
- [X] T046 [US3] Replace process-lifetime key capture in `gitstore-controller-manager/internal/graphqlclient/private_key_signer.go` and `gitstore-controller-manager/cmd/controller/main.go` with exchange-local resolve/parse/sign using T045; drop key references after each exchange, clear mutable buffers where feasible and keep existing token caching only.
- [X] T047 [US3] Preserve single-flight and update `gitstore-controller-manager/internal/graphqlclient/credential.go` so resolution/signing/exchange share the total deadline, provider calls have no internal retries, jittered exponential delay is capped at 30s and usable-expiry/readiness rules never extend stale tokens; make T042 pass under race testing.
- [X] T048 [US3] Add safe atomic-record provisioning/rotation instructions to `gitstore-api/cmd/gitctl/enroll_serviceaccount.go` and tests in `gitstore-api/cmd/gitctl/enroll_serviceaccount_test.go`; use enrolled key IDs rather than derived-hash assumptions, never print private records, and document enroll-new/replace/observe-all/retire-old ordering in `docs/runbooks/secret-material-rotation.md`.
- [X] T049 [US3] Run T043/T044 and document measured recovery/overlap/replacement results and environment-provider limitations in `docs/runbooks/secret-material-rotation.md`; prove retries resolve fresh material, both replicas recover within 60s, reference/config snapshots remain unchanged and no File readiness/status operation or implicit lease is added.

**Checkpoint**: All three stories have independent acceptance evidence.
Production rollout still depends on the cross-cutting gates below.

**Deployed acceptance checkpoint (2026-10-03)**: The combined real-process
harness passed twice against two APIs sharing three Scylla nodes and one Git
service. The expanded run took 293.56s and recovered from the isolated 90s
provider outage in 6.103361708s. It additionally rejected a mismatched live
record after token expiry, preserved the peer's authenticated availability,
rotated both controllers through key overlap/retirement and replaced one
process. Controller logs and all four process metrics passed credential scans.
The isolated deployment, private records, enrolled test keys and token file
were removed. Sanitized functional evidence is recorded in quickstart.md;
this is not production capacity evidence.

## Phase 6: Cross-Cutting Evidence and Operational Delivery

**Purpose**: Prove the exact workload/topology/thresholds, not a proxy.
Tests for new dispatcher/verifier behavior precede their implementation.

- [X] T050 [P] Add secret-scenario dispatch/preflight/evidence negative tests in `scripts/test-capacity-dispatch.sh`, `scripts/test-repository-capacity-stack.sh` and `scripts/test-secret-capacity-evidence.sh`; reject missing scenario flags/fault proofs, multiple Git instances or any Git HA claim, raw-secret artifacts and diagnostic evidence presented as a passing gate. File-based JSON, digest, completeness and leakage fixtures live in `tests/integration/secret_capacity_contract_test.go` and reuse T051's assertions; `make test` runs both shell and race suites. No deployed collector or gate pass is claimed.
- [X] T051 [P] Add bounded workload/fault-verifier assertions in the focused `tests/integration/secret_capacity_contract_test.go` companion to `repository_lifecycle_capacity_test.go` for the exact tables in `specs/063-implement-secret-adrs/contracts/capacity-and-rollout.md`; cover 5,000,000 titled Product fixtures with offline pagination/count proof, real File pushes, required latency/resource thresholds and fail-closed evidence completeness before implementing the scenario. Typed observation fixtures exercise rejection boundaries; they are not deployed evidence. `make test` runs this matrix; T052-T055 connect the real collectors, with deployed certification tracked separately by T058.
- [X] T052 Wire `REPOSITORY_CAPACITY_SECRET_SCENARIO=1` through `Makefile`, `scripts/run-capacity-target.sh`, `scripts/repository-capacity-stack.sh`, `compose.capacity.yml` and `scripts/validate-capacity-evidence.sh`; preserve old-profile meaning and existing singleton Git URI/callback checks, record >=2 API/controller process identities and exactly one active Git instance in every mode, and include sanitized scenario/build provenance. Git sharding/replication/HA is out of scope, not a prerequisite.
- [X] T053 Implement the contract's sustained and burst File workload, local resolver measurements, faults and domain verifier in `tests/integration/repository_lifecycle_capacity_test.go`; enforce 100-File pool, <=10 Files/128 KiB per push, 32 workers/256 queue, 10 pushes/s for 60m and 100-push/1s minute bursts, measure drops, integrity, bounded calls/memory/goroutines, expiry-spanning outage, atomic rotation, replacement and <=60s recovery on both controllers.
- [X] T054 Add `tests/chaos/profiles/controller-restart.json` with the supported restart action and 60s objective, verify explicit-target handling in `scripts/run-chaos.sh`, and connect the minute-45 experiment plus minute-15 isolated-record withdrawal/restore and minute-30 rotation to the domain verifier in `tests/integration/repository_lifecycle_capacity_test.go`; clean up only test-owned records on cancellation and preserve existing API replacement/overflow coverage.
- [X] T055 Extend evidence scanning/validation in `scripts/validate-capacity-evidence.sh` and `scripts/test-secret-capacity-evidence.sh` to cover every new artifact, logs/errors/metrics/traces and unauthorized API output; exclude private records, tokens, assertions, raw environment/config dumps, fail on any marker leakage and require authorized issuance success plus unauthorized denial without storing response bodies.
- [X] T056 Update `tests/capacity/README.md`, `docs/development/secret-material.md`, `docs/runbooks/secret-material-rotation.md`, `AGENTS.md` and `specs/063-implement-secret-adrs/quickstart.md` with all root commands, alert/readiness interpretation, matching binary/config snapshots, the direct alpha File breaking change, deferred ADR boundaries and required sanitized evidence locations; diagnostic runs do not establish production readiness.
- [X] T057 Run the documented unit/race/schema/hook/config/build slices via `Makefile`, independently build API/controller images with `GOWORK=off`, and run `make pr-ready`; reconcile failures caused by this feature and record actual outcomes in `specs/063-implement-secret-adrs/quickstart.md`, leaving unrelated existing failures explicitly identified rather than silently changing their code.
- [ ] T058 Run `make capacity TARGET=repository PROFILE=lifecycle MODE=production REPOSITORY_CAPACITY_SECRET_SCENARIO=1` with the scheduled confirmed controller-restart chaos experiment; retain the sanitized passing bundle under the configured `tests/capacity/` evidence location and reference it from `specs/063-implement-secret-adrs/quickstart.md`; if clean release images, topology, data, duration or permissions are unavailable, leave this task blocked, never replace the gate with alpha/diagnostic evidence.
- [X] T059 Perform the release-readiness review against `specs/063-implement-secret-adrs/contracts/capacity-and-rollout.md` and record artifact versions, evidence digests and remaining limitations in `specs/063-implement-secret-adrs/quickstart.md`; run `graphify update .` after implementation without claiming unperformed deployments or capacity gates. Outcome: implementation connected, not production-capacity-certified; T058 remains blocked. No preparation release, inventory audit or File-write restriction is required.

**Connected collector and reuse checkpoint**: Owned key generation, atomic regular-file
records, signing/issuance probes, cancellation restoration, process-identified
recovery and the minute-15/30/45 schedule now live in the existing
`tests/integration/secret_bootstrap_test.go`. The existing Repository lifecycle
capacity runner contains the File workload and dataset helpers; evidence
contracts and negative fixtures remain in `secret_capacity_contract_test.go`.
Controller exchange telemetry and its tests were folded into the existing
credential files. No new Go helper file or secret-specific Compose overlay is
retained. `compose.capacity.yml` uses parameterized controller config/provider
mounts while retaining normal shared-key defaults and singleton Git routing.
Capacity regressions run in the ordinary `make test` suite, not a second
capacity entry point. CPU/RSS/goroutine accounting and original/replacement log
capture feed the finalizer after verifier/postflight writers close. The finalizer
checks component/run/source consistency and scans/hashes the complete artifact
set. Dataset counts/digests must agree across both APIs before load begins.
Positive component-to-bundle fixtures, malformed/partial/mixed/contaminated
observations, bounded logs, ownership and metric parsing are covered locally.
The full `make pr-ready` workflow passed; no deployed scheduled-fault run or
production-capacity pass is claimed.

## Dependencies and Execution Order

```text
T001 -> T002 -> T003
                  |
          T004 + T005 (tests)
                  |
          T006 -> T007 -> T008 -> T009 -> T010
                  |
         +--------+------------------+
         |                           |
   US1 T011-T025               US2 T026-T041
   strict File contract       config -> bootstrap
         |                           |
         +--------------+------------+
                        |
                 US3 T042-T049
                        |
                 T050-T059 evidence
```

- Tests within each marked batch may run in parallel; implementations are
  sequential unless their dependencies and file ownership are explicitly split.
  T004/T005 follow T003; T011-T013 follow T010; T026-T029 follow T010;
  T042-T044 follow both P1 stories; T050/T051 follow US3.
- US1 and US2 are independently testable after foundation. Prioritize
  T026-T034 before adding production bootstrap options in T035. US1 metadata
  work need not wait for the controller config refactor.
- US1 order is tests -> strict enforcement/schema -> fixtures ->
  hooks/runtime contract -> docs and focused evidence. Withdrawn task IDs
  in the former US1 range do not represent dependencies or release gates.
- US2 config producers/consumers T030-T034 are one coordinated change, not
  independently deployable partial renames. T035-T039 depend on that boundary;
  T041 requires the packaging and observation changes.
- US3 requires US2's adapter/client and US1's runtime contract consumer.
  T045 -> T046 -> T047 -> T048 -> T049; all three test tasks precede changes.
- T052 needs T050, T053 needs T051/T052, T054 needs T053 and T055 needs
  T050/T052-T054. T056-T059 then proceed in order.
- Shared edits to `Makefile`, CLI enrollment, generated schemas and capacity
  scripts must be serialized even when US1/US2 work proceeds concurrently.
  Do not run concurrent generators or apply overlapping patches.

## Parallel Examples by Story

| Story | Safe parallel batch                                                                               | Join before implementation |
|-------|---------------------------------------------------------------------------------------------------|----------------------------|
| US1   | T011 catalog/fixture tests, T012 gRPC tests, T013 output/schema tests                             | T018                       |
| US2   | T026 config tests, T027 bootstrap/startup tests, T028 deployment/enrollment tests, T029 API tests | T030                       |
| US3   | T042 client tests, T043 shared record/runtime tests, T044 deployed tests                          | T045                       |

Foundational T004/T005 and cross-cutting T050/T051 are also independent test
batches. `[P]` is not permission to implement before the expected failing tests
or to modify shared fixtures concurrently.

## Requirement Coverage

| Requirement                                  | Tasks supplying implementation and acceptance  |
|----------------------------------------------|------------------------------------------------|
| FR-001 canonical references                  | T004-T006, T011-T012, T018-T021                |
| FR-002 explicit typed wrapper                | T007, T011-T025                                |
| FR-003 seven classified failures             | T004-T010, T027, T035-T037                     |
| FR-004 no material/private-key/token leakage | T006, T010, T013, T029, T037, T041, T044, T055 |
| FR-005 shared boundary/separate contexts     | T001-T010, T022, T035-T036                     |
| FR-006 bootstrap without credential cycle    | T027-T029, T035-T036, T041                     |
| FR-007 owning-service source isolation       | T028-T029, T032-T035, T039-T041                |
| FR-008 bounded material lifetime             | T005-T010, T042-T049, T051-T055                |
| FR-009 safe observations                     | T010, T027, T037, T055-T056                    |
| FR-010 File integration/deferred runtime     | T011-T025, T043, T049                          |
| FR-011 typed nested config/migration         | T026-T034, T038-T040, T056                     |
| PR-001 replica safety                        | T038-T041, T044, T049-T059                     |
| PR-002 multi-user security                   | T004, T012, T022, T027-T029, T041, T044, T055  |
| PR-003 keyed admission at scale              | T012, T021, T051-T053                          |
| PR-004 bounded backpressure                  | T005-T010, T042, T047, T051-T055               |
| PR-005 capacity evidence                     | T050-T058                                      |
| PR-006 fault recovery                        | T038, T042-T049, T053-T058                     |
| SC-001 classified fail-closed cases          | T004-T013, T022, T027-T029, T042-T049          |
| SC-002 exhaustive exercised leakage cases    | T010, T013, T029, T037, T041, T044, T055       |
| SC-003 compatible rolling replacement        | T038, T041, T044, T053-T059                    |
| SC-004 bounded outage/recovery               | T042-T049, T050-T058                           |

## Implementation Strategy and Release Gates

1. **MVP**: Foundation plus US1 establishes safe authored metadata and the
   contract-only runtime consumer. Ship the strict alpha contract directly.
2. **P1 bootstrap increment**: Complete US2, migrating config once before
   deploying it. Keep the #429 issuance shape and current Admin URI.
3. **Rotation increment**: Complete US3 with atomic enrolled key/ID pairs and
   bounded fresh renewal, then prove the full deployment/fault workload.
4. **Production gate**: T058 requires real production-scale evidence.
   T059 separates implementation/release readiness from actual deployment. Do not
   auto-commit, tag, publish, deploy or mutate production data from this
   checklist. Missing infrastructure/approval is a blocker, not a passing
   task. Historical manifests and unrelated worktree edits remain untouched.

No task is completed merely by adding a test or documenting a threshold.
Mark it complete only when its behavior and required verification are present;
retain explicit blockers for unperformed integration/capacity/release gates.
