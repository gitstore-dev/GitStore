# Quickstart: implementing and validating spec 064

The core v1beta1, name-only ServiceAccount and signed-RPC implementation is in
local commit `eff9e28`. Use an isolated development deployment; old policy,
configuration and namespaced ServiceAccount schemas are not supported.

## Current acceptance scope (2026-10-19)

The user assigned the remaining capacity work to
[#451](https://github.com/gitstore-dev/GitStore/issues/451#issuecomment-6107662533),
including unfinished harness instrumentation, sustained load, reload/heap soak,
and deployed multi-replica replacement/fault evidence. The evaluator-only
benchmark/soak (T069) and per-replica readiness/baseline authorization (T070)
passed on 2026-10-18. T065–T068, T071–T073 and deployment portions of
T021/T045/T054 remain unchecked. T062's standalone deployed credential suite
passed on 2026-10-19. These narrow alpha and functional passes do not
establish overall alpha or production capacity acceptance; `rbac-local` remains
development/evaluation-only. Do not close the overall specification until the
remaining required evidence exists.

The original handoff's stopped capacity-container collision is historical.
The passing runs below used isolated fixtures; their evidence records the
retained diagnostic runtime unchanged. Isolation and ownership checks remain
required before cleanup or chaos. No compatibility reader or old-fleet rollout
is required.

Earlier ledger entries below are chronological checkpoints, not current claims
that their then-uncommitted files remain uncommitted.

## Implementation evidence ledger

### Reconciled alpha evidence (2026-10-19)

The following are completed 2026-10-18 runs, not new executions against the
current branch head. Their sealed proof hashes and all 27 referenced artifact
hashes were reverified on 2026-10-19.

| Task | Run / tested revision | Result and scope |
| --- | --- | --- |
| T069 | `451-authz-alpha-6926146-20261018` / `6926146c92a0630e9bdbc5b7c7101b1b4edb8eb6` | Evaluator-only alpha: 60s warmup and 600s measurement at concurrency 1/8/32; all 18 case/concurrency sampled p99 bounds below 1ms. Largest per-level p99 bounds: 35/103/225us. |
| T070 | `451-readiness-alpha-5924c69-20261018` / `5924c69a29c47dd8b74558969cef75488e40565d` | Two-API readiness/baseline authorization alpha: 600.012s, 10 offered iterations/s per replica, 10 preallocated/100 maximum VUs; 12,001 completed iterations, 60,005 passing checks, zero failed checks, dropped iterations or unexpected HTTP failures. |

T069 used native macOS arm64, Go 1.26.4, 11 logical CPUs and 36GiB host memory.
Its concurrent reload phase ran 1,800.102s with 32 decision workers:
283,176,365 decisions, 856 valid reloads, 2,568 rejected reloads and zero invalid
outcomes. Post-GC heap fell from 10,257,792 to 9,120,152 bytes (88.91% of
baseline). The synchronous evaluator has no decision queue; allocation results
and bounded histograms are retained in the run bundle. Its failing transitive
call-graph audit excludes policy-file, datastore-client and network-client
dependencies from evaluation. This is not runtime-spy coverage, so the exact
T065 instrumentation task remains open. The largest sampled individual decision
was 588.379ms; the sub-millisecond claim is p99, not a maximum-latency guarantee.
The proof `authorization-alpha-20261018-verified.json` has SHA-256
`fba75d7566b8767ae3e3e622bf3df220f8a9b29cc4f0e27ddeb369f38f9059e4`
and binds 13 evidence artifacts.

T070 used shipped v1beta1 policy digest
`6a833c03b6daa7ee2d6cf2555144ad8c0c4d57e8ab5c3f5c22e31b666ea9a898`;
both APIs reported matching policy/catalog/provider identities and exact
allow/anonymous-deny results. Readiness p95/p99 was 1.045/1.556ms on API A and
1.054/1.611ms on API B. The proof
`readiness-alpha-20261018-verified.json` has SHA-256
`64495d170f2ab2ef667a592598b2ddb7be7591284f386a1ab6e886dc1ef2b922`
and binds 14 evidence artifacts. The offered 10/s is not a measured sustainable
service throughput ceiling.

T062's fresh owned two-API/Scylla credential suite ran on
`3cd4e03700aa97eaa306de2b8b4dff68d622f016`: bootstrap, isolated provider
outage, mismatched-key denial, key overlap/retirement, controller process
replacement and cross-replica ServiceAccount lifecycle tests passed. The
bootstrap test scans both APIs' and controllers' metrics plus controller logs
for PEM/JWT/credential markers. The additional reincarnation check denied
still-unexpired old access tokens after same-name/same-key recreation on both
unchanged target APIs. Its sealed proof
`secret-replica-20261019c-sealed-proof.json` has SHA-256
`b33a89d2adc1417ac00542b12e5dc364f8c81c07109a376e21417eec0d48db68`;
all 28 referenced artifact hashes were reverified. The suite is standalone
functional evidence, not a sustained-load fault or rolling-upgrade pass.
`make check TARGET=credentials` and the owned-fixture/signing-record,
artifact-scan/manifest and strict production-evidence contract tests passed
again on 2026-10-19, including name-only subjects and sanitized failure output.

Current task ledger: **71/82 complete, 11 unchecked**. These results do not establish
the remaining boundary matrix, sustained Git/lifecycle success, rolling
replacement/fault acceptance or acknowledged five-million-Product production
gate. In particular, the later full Namespace runs failed provenance contention
before replacement; the short direct-routing diagnostic was not a fix or an
acceptance pass. T078 and the overall specification remain open.

### Lifecycle semantic-harness follow-up (2026-10-19)

Repository lifecycle alpha/production now requires bounded before/after
authorization evidence from both APIs: authenticated Namespace-list allow,
exact unauthenticated deny without partial data, and matching unchanged loaded
provider/policy/catalog snapshots. The artifact starts incomplete, is persisted
atomically and cannot pass after a failed/incomplete lifecycle path. Postflight
ties its run/mode/endpoints to distinct preflight processes and rejects missing,
partial, legacy local-policy, mismatched or secret-bearing evidence.
The Go probe rejects redirects, bounds response sizes/time and preserves
pluggable provider identity and anonymous-disabled denial semantics.

This is additional T066/T068 implementation coverage, not a new deployed alpha
pass. Their broader workload/domain/fault obligations remain unchecked.
Existing load, retry, deadline, replacement and resource gates are unchanged;
the before/after probes do not establish the under-load authorization matrix.

### Non-capacity completion pass (2026-10-11, after `eff9e28`)

- Added exact allow/deny watch/status recorder coverage and all-five-kind typed/
  generic WebSocket denial coverage. Unsupported generic status kinds now fail
  before policy/resolver execution; catalog membership does not expose a backend.
- Added real shipped-policy GraphQL baseline checks, management-only Product/
  Variant boundary tests and all-three-owner-kind transfer tests. Field-level
  policy allowance is not a domain-invariant override: USER identity/transfer
  validation remains independently enforced in admission/resolvers.
- A failing namespace boundary regression exposed lowercase stored tier facts
  (`user` versus `USER`). Reads, node/nodes, deletion and transfer now project
  the canonical enum spelling; unknown tiers are omitted, not invented.
- Added signed semantic callback create/update/delete checks for all five
  catalog kinds, including live policy revocation. Focused middleware and
  callback suites passed after these changes.
- Updated ADR 0010 and its index together for the implemented strict allow-only
  baseline, no shim, cluster SAs and provider-owned authorization. Acceptance
  date is the flip date under the ADR dating rule; no merge/push is claimed.
  Operator SIGHUP/digest/rejection diagnostics are in controller-auth.md.
- Boundary audit details and intentionally unexposed schema paths are recorded
  in contracts/service-boundaries.md. Deployment portions of T021/T045/T054/T062
  and T065–T073 remain with #451. T078 stays open; no capacity pass is inferred.

Final local verification after the boundary fixes:

- `make pr-ready` passed (Rust/Go lint, builds and default test suites, license
  and credential-output/leak checks). Private local log:
  `/private/tmp/gitstore-064-final-pr-ready.log`.
- `go test -race ./gitstore-api/internal/middleware/security ./gitstore-api/internal/cataloggrpc ./gitstore-api/internal/auth/... ./gitstore-controller-manager/internal/graphqlclient -count=1`
  passed; log `/private/tmp/gitstore-064-final-race.log`.
- `make _check-generated` passed with zero authorization catalog drift;
  `git diff --check` passed. The neutral fixture expectations were not changed.
- `make check TARGET=all` passed, including shipped configuration/RBAC, Compose,
  licenses, credential leakage and catalog generation checks; log
  `/private/tmp/gitstore-064-final-check.log`.
- `graphify update .` passed (21,992 nodes / 49,351 edges). Graph artifacts are
  ignored, with no tracked source diff; its zero-node/nonsemantic-document
  warnings do not replace source/test verification.

Historical task ledger at this checkpoint: 68/82 complete. The 14 unchecked items retained the deferred
capacity/deployed-acceptance work and final acceptance closure. Previous owned
Scylla contracts and Compose/root-integration results remain recorded below;
neither they nor this local run are a multi-replica capacity certificate.

The original Compose validation evidence below covers memdb, single-node Scylla,
three-node Scylla and optional OIDC service wiring on `eff9e28`; the OIDC browser
login flow was not exercised. Those images predate the final middleware audit
fixes, which are covered by the final local test/readiness run, not a new image
deployment. The copied `gitstore-dev/quickstart` policy/config/Compose/enrollment
files still require a follow-up update outside this repository.

2026-10-11, foundation and provider slices (uncommitted working tree):

- T001 protected-operation inventory is in contracts/service-boundaries.md.
- T002 actual shipped v1 provider decisions were pinned and passed before
  replacement. Existing repository-isolation and two ServiceAccount integration
  tests passed; their custom test authorizer is explicitly not shipped-policy
  proof. The observed outcomes remain in contracts/validation.md; v1 positive
  provider tests have been replaced with v1-rejection tests and the neutral suite.
- T003 promoted fixture is byte-identical to the reviewed seed.
- T004–T008 generator and recorder tests failed first (missing generate command
  and missing history), then passed with the race detector. Make dispatch tests
  passed after their expected missing-selector failure. Generated Rust passes
  rustfmt; the reviewed catalog snapshot and generated Go/Rust tables are checked
  by TestCommittedAuthorizationCatalogParity. graphify update completed.
- T009–T019: all 55 decisions, 28 validation cases, additional strict grammar and
  exact/boundary-plus-one limits, invalid principal/masters cases, concurrent
  reload retention, secret-safe reason logging and startup/reload tests passed
  with Go's race detector. The all-active-users regression failed first because
  the prior check accepted one bound admin alongside an unbound user.
  Command: `go test ./gitstore-api/internal/auth/provider/rbaclocal
  ./gitstore-api/internal/auth ./gitstore-api/internal/app -run
  'TestV1Beta1|TestProviderRegistryReloadPreservesUsableBindingInvariant'
  -count=1 -race` (run as one shell line from the repository root).
- The existing DecisionLogger already preserves reasons while excluding claims
  and attrs; the new provider supplies bounded policy/binding/role/rule reasons.
- T020/T026/T027: successful-AuthN normalization and reserved static/OIDC
  identity tests passed with the race detector, including username and userinfo
  mapping, exclusive synthesized groups and provider-owned principal immutability.
- T022/T035: controller invalidation, coalesced refresh, bounded HTTP retry,
  pre-execution-only GraphQL retry and WS 4401 rejection tests passed with the
  full graphqlclient race suite. Forbidden, partial-data, resolver-path and
  ambiguous server failures are not replayed.
- T028/T029 and the T030/T031 storage slice: direct replacement of the alpha
  migration 009 passed fresh-schema tests. Tagged memdb and isolated Scylla
  ServiceAccount/pagination contracts passed. Two independent Scylla stores
  passed concurrent global name and UID reservations, fail-closed interrupted
  enrollment, repair, interrupted deletion/recreation and stale listing refill.
  Stale listing refill failed first with an incorrectly empty page. Listing
  fails explicitly beyond 4096 record lookups/256 queries; the existing audit
  path now bounds ServiceAccount snapshots to 10000 rows per table.
  Scylla tests used a disposable container without mounted developer data at
  127.0.0.1:19042 and test-owned automatically removed keyspaces. These are
  store-level checks, not full two-API/two-controller deployment evidence.
- The ServiceAccount store/entity no longer has namespace; both authentication
  providers issue/accept canonical system:serviceaccount:name and retain UID,
  enrolled-key, replay and token audience tests. Resolver operations reject
  nonempty metadata.namespace; generated GraphQL artifacts were refreshed.
- Controller signer/config/enrollment now use name+UID+key references. Removed
  namespace TOML/environment/CLI settings fail with actionable diagnostics.
- Full application/default-policy/CLI tests are not a passing readiness claim at
  this intermediate stage: those consumers still require the later US2/US4
  replacement slices. No deployed capacity/chaos, p99 or production gate passed.

## 1. Read the contracts and generate tasks

Start with [plan.md](plan.md), [research.md](research.md) and the
[policy contract](contracts/rbac-policy.md). Generate dependency-ordered tasks
with `/speckit-tasks`. Preserve the test-first sequence and existing service
layout. No feature implementation or tasks.md is created by `/speckit-plan`.

## 2. Establish the action and policy baseline

Promote `contracts/action-vocabulary.json` to the shared schema catalog and
`contracts/decision-table.json` to the existing test fixtures. Add tests that
fail against old loading/actions before implementation. Once generation is
wired through gitctl and the root Makefile:

```sh
make generate TARGET=authz
make test
make check TARGET=config
```

Generation must be deterministic. Go/Rust tables and CLI projections match the
same catalog. Verify the four purge actions already on main stay independent
of delete/status.write.

## 3. Exercise local role tooling after implementation

Use a scratch policy path:

```sh
make add-role POLICY_FILE=/private/tmp/gitstore-064-policy.yaml ROLE=reader ALLOW=product.read,product.management.list SCOPE=namespace
make assign-role POLICY_FILE=/private/tmp/gitstore-064-policy.yaml ROLE=reader ROLE_KIND=Role SUBJECT=bob SUBJECT_KIND=User SCOPE=namespace NAMESPACE=acme-store
```

An absent file becomes v1beta1. Repeating the binding leaves it unchanged.
Validate it through the existing gitctl validation command or a scratch config
pointing at it: `make check TARGET=config CONFIG_FILE=<scratch-config>`.
POLICY_FILE alone does not override the configuration checker's selected config.

Try legacy actions, DENY, unknown conditions and invalid scope references: each
must fail without changing the file. A v1 file fails with an error naming the
supported apiVersion. An invalid reload retains the last valid policy under
simultaneous authorization requests.

## 4. Enroll cluster-scoped identities

Follow the [schema and identity contract](contracts/service-boundaries.md).
Replace the ServiceAccount table definitions in baseline migration 009 and
initialize a fresh isolated development/test schema; no migration 013, legacy
tables, backfill or compatibility layer. Enroll the controller by name via
the updated existing gitctl/Make enrollment flow, using its managed secret
reference and new UID. Remove controller namespace configuration.

Do not silently overwrite an identity file with a different UID. The new subject
is `system:serviceaccount:gitstore-controller-manager`; old subjects fail. A
definite authentication rejection invalidates that token
generation and triggers one coalesced exchange. Policy denial must not trigger
an exchange loop or automatic replay of ambiguous mutations.

## 5. Verify real boundaries

Start the existing development stack after rewriting its config/policy:

```sh
make compose
```

Check admin, controller and authorized system:api flows. Anonymous protected
reads remain denied without a public visibility fact. With shipped grants,
ordinary users create only their username's USER namespace. SA/group USER
ownership, USER transfer/rename and tier changes fail through GraphQL and Git.
Verify a namespace binding in two namespaces and prove no cross-namespace grant.
Repository metadata read must not imply Git content write.

Run focused Go/Rust/Scylla/race tests from tasks, then aggregates. Existing
datastore contract commands are:

```sh
make test TARGET=datastore
make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<owned-host:port>
```

## 6. Collect deployed evidence

After implementation is committed, supply config/environment manifests,
owned endpoints, release images, private credential files and runtime limits
documented in `tests/capacity/README.md`:

```sh
make capacity TARGET=api PROFILE=readiness MODE=alpha
make capacity TARGET=repository PROFILE=lifecycle MODE=alpha
make chaos CHAOS_PROFILE=api-restart CHAOS_TARGET=<owned-api-container> CHAOS_CONFIRM=1
```

Replace placeholders before execution. Repository alpha manages its isolated
topology through the existing runner. Keep two APIs/two controllers and exactly
one Git process. Readiness health probes need the planned authorization verifier
before they count as feature evidence. Retain sampled p99, expected decision
matrix and policy digests. See [validation.md](contracts/validation.md) for
workloads, thresholds, soak and faults. Alpha evidence does not prove production
policy-provider readiness or the full 5,000,000-product envelope.

## 7. Close implementation

Update `docs/`, amend and accept ADR-0010 with its index in the implementing PR,
flag the `gitstore-dev/quickstart` follow-up, then run:

```sh
graphify update .
make pr-ready
```

Use a breaking Conventional Commit title. Retain explicit legacy-negative tests
and migration explanations, but no accepted/emitted legacy actions.

## Implementation checkpoint — authenticated boundaries and operator tooling

Completed locally (2026-10-11; no deployed readiness claim):

- Full Rust `cargo test`: 147 library tests and 8 integration tests pass. Git methods enforce canonical repository actions and bounded, role-free actors. Production catalog handler construction fails closed instead of substituting a no-op handler.
- Go race suites for `internal/auth`, `internal/app`, `internal/middleware/security` and controller `internal/graphqlclient` passed after identity normalization and recovery changes. Malformed subject/group/UID/method facts are rejected at authentication. Non-browser WebSocket clients send upgrade credentials; invalid credentials receive pre-upgrade HTTP 401, avoiding gqlgen's ambiguous normal-close rejection. GraphQL pre-execution credential failures carry UNAUTHENTICATED with no field path.
- Catalog callback transport tests pass under `-race`: all three RPCs reject missing service credentials; repository/ref/actor mismatches fail; a valid callback observes a live policy reload. Domain fixture wrappers supply explicit authenticated contexts and are never registered on gRPC. Resolver, Git HTTP and Product durable-watch contract suites pass with updated actor fixtures.
- Policy editor and CLI tests validate strict v1beta1 writes, exact resource/verb projection, typed bindings, file-mode/comment preservation, invalid-write preservation and byte-identical idempotence. The Make dispatcher suite passes; DENY is rejected, Role definitions have no namespace and namespace bindings cannot inherit the bootstrap default.
- GraphQL metadata permits namespace omission for cluster-scoped ServiceAccounts; nonempty ServiceAccount namespaces remain rejected. Regenerated using the existing gqlgen workflow.

Open design/evidence gap: the baseline Git RPC implementation uses a static
shared bearer secret, not the signed/expiring/audience-bound/replay-protected
envelope assumed by T023 and the boundary contract. Canonical method/resource
matching and sanitized facts are implemented, but they are not cryptographic
message signing or replay protection. T023 remains incomplete; do not claim
signed-wire parity from these tests. A signing protocol and bounded replay
state need an explicit design decision, including key rotation and singleton
process replacement semantics.

The shipped policy/config, USER namespace invariants, complete owner-transfer
CAS checks, all-path acceptance matrix and capacity/chaos evidence are still
incomplete. The spec and ADR remain open. No user files have been committed.

Final local checkpoint verification: `go test ./gitstore-api/...
./gitstore-controller-manager/...` passed for both complete modules. Focused
race suites additionally pass for catalog admission, resolvers, middleware,
Git client, policy editor/provider and gitctl. Management capabilities have
independent read/list evaluation; denied single-object reads use present
nonpublic visibility facts for NOT_FOUND (never decision prose), and Repository
facts come from its existing spec field. Product/Variant currently expose no
separate management-view selector or public-visibility filter; no such schema
or filtering was invented. Create Product/Category uses namespace ownership;
File/Category reads use the stored resource owner. Removing the owner annotation
requires transfer authorization to the immutable creation actor.

`graphify update .` completed (AST-only; generated graph is ignored), and
`git diff --check` passed. The full `make pr-ready` and deployed alpha gates
have **not** passed or been substituted by these local suites. The signing
design choice was requested before proceeding beyond the incomplete US2 wire
dependency. Required tasks remain unchecked.

## Replay/configuration follow-up (2026-10-11)

The user explicitly chose replay protection and strict rejection of old
configuration, with working shipped Compose setups. This supersedes the signing
decision blocker above. Implemented `x-gitstore-rpc` HMAC-SHA256 requests in
both directions: full message/method/audience binding, random nonce, 60s expiry,
no bearer fallback. Git retains bounded fsynced replay reservations on its data
volume; callbacks use domain-separated shared datastore replay digests. Previous
keys support explicit overlap; the singleton Git replacement remains
non-overlapping. See `contracts/service-boundaries.md` for the wire/bounds.

- Go/Rust deterministic signature golden agrees; tamper, expiry, audience,
  method, concurrent replay, key retirement, retained-storage restart, clock
  rollback, storage failure and capacity tests pass. Go callback tests reject
  replay on a second server and replacement sharing the datastore.
- Both full Go service suites and focused signing/auth/catalog/app/controller
  config race suites pass. Rust: 145 library and 8 integration tests pass.
- Shipped policy effective grants are tested across all 82 actions, including
  four controller purges, restricted own-account token issuance, explicit
  core-kind admin/edit/view, internal backing actions and visibility-dependent
  anonymous contents read. Old v1/namespace configuration is rejected, including
  empty obsolete environment/Make variables. No compatibility reader was added.
- `make check TARGET=compose`, workflow dispatcher and capacity-stack dispatcher
  tests pass; `make lint` passes after formatting and error-style corrections.
- Built the actual Compose images and started isolated project
  `gitstore064verify` on ports 14064/19064/15064/16064/15065, using only new
  project-owned volumes. Local memdb boot/enrollment and signed-wire replacement
  both passed API readiness and controller `credentialReady`/`ready`, including
  completed list/watch recovery. A real signed Git push created Product
  `authz064-probe/replay-probe`; GraphQL returned its exact title.
- Started the shipped single-node Scylla overlay with a fresh project-owned
  keyspace. Baseline migrations, name-only enrollment, API readiness and all
  registered controller list/watch recovery passed. Cluster/OIDC startup and
  capacity gates are not implied by this single-node check.
- Name-only ServiceAccount integration tests and the local secret capacity
  fixture suite pass. A full root integration run initially failed because its
  external stack variables pointed at unused default ports and its embedded
  admission fixture omitted the now-required authorizer. Wiring that existing
  fixture authorizer and making projection-only tests authenticate are follow-up
  fixes; do not count the failed complete run as passing deployed evidence.

Worktree remains uncommitted. USER namespace invariants, remaining transfer/CAS
and all-path coverage, broader operational gates and final documentation/ADR
closure are still outstanding. The temporary Compose project remains test-owned;
never remove any existing developer/capacity project's volumes during cleanup.

## CAS and dependency-upgrade checkpoint (2026-10-11)

The following supersedes the earlier USER/CAS and failed integration notes,
without closing the remaining all-path and deployed capacity gates:

- USER namespace invariants now have GraphQL/admission regressions. General
  organization fixtures explicitly use ORGANIZATION; personal-name constraints
  remain enforced rather than bypassed for admin fixtures.
- User-approved Product, CategoryTaxonomy, Collection and ProductVariant update
  methods require a caller-supplied expected resource version. Memdb checks it
  inside its write transaction; Scylla uses that same version in its LWT.
  Stale writes preserve the winning owner, body and status. All four
  backend-neutral OwnerCAS contracts pass on memdb and real single-node Scylla,
  including eight-way concurrent writers and malformed version transitions.
  Admission repair reloads and reauthorizes the winning owner for every kind.
- Complete API tests/vet, controller tests, affected Go race suites, Scylla
  contracts/backend tests and `make check TARGET=all` pass. The full root
  integration suite passed against the final rebuilt API/Git images on the
  owned single-node Scylla stack (177.160 seconds), using `gitstore-test/cas-catalog`.
- User-approved Rust dependency upgrades remain in place. Adapted gix 0.89
  pack/object/error APIs, removed its obsolete tree-editor feature and imported
  HMAC 0.13 KeyInit. Reference conflicts use typed errors, including actual
  concurrent deletion tests. All 145 library and 8 integration tests and
  clippy with warnings denied pass. Both Compose images build and their
  non-overlapping replacement passes the complete integration run above;
  signed-RPC/replay checks were not relaxed.
- The local readiness aggregate is still being rerun after correcting stale
  Compose enrollment and nested-Make test fixtures. No passing `make pr-ready`,
  multi-replica capacity, cluster/OIDC runtime, or production claim is implied
  by this checkpoint. No GitStore changes have been committed.

## Compose and sampled-evaluator checkpoint (2026-10-11)

- `make pr-ready` passed after the enrollment and Make fixture corrections.
  The four finalization mutations now additionally have a real-policy recorder
  matrix: delete-only and status-write-only grants deny; purge-only grants
  allow. Middleware and catalog race suites passed afterward.
- Fresh three-node startup exposed the inherited memdb health grace expiring
  while baseline migrations were still running. Both Scylla overlays now give
  the API five minutes of startup grace; readiness still releases dependents
  immediately on success. A second fresh, isolated project
  `gitstore064clusterfresh` passed the complete startup/enrollment path and all
  four controller recoveries. Earlier project volumes were retained.
- The optional OIDC overlay also passed Hydra/Kratos migrations and client
  registration, discovery at the configured issuer, bridge/API health and
  controller readiness. `docker compose up --wait` reported the successful
  standalone client-setup job's exit (0); direct status/endpoint checks verified
  the long-running services. No interactive OIDC browser login was tested.
- The full root integration suite passed on this cluster/OIDC setup in 171.070s,
  using `gitstore-test/cluster-catalog`. The suite combines embedded policy
  contract servers with external API/real-Git tests: it is not a two-API
  acceptance matrix. Test-owned containers were stopped after this run, with
  their containers and volumes retained; no developer deployment was reset.
- Added an opt-in, fixed-bucket independently sampled authorization harness
  under the existing `make capacity` interface (`TARGET=api`,
  `PROFILE=authorization`, `MODE=diagnostic`). It exercises 200 roles, 1,000
  bindings, 2,000 rules and 50 overlapping groups, separating early/last allow,
  no-binding/worst deny, owner transfer and missing attributes at concurrency
  1/8/32. Unit tests verify fixture semantics and histogram quantiles. Full
  defaults are 60s warm-up and 10m measurement per concurrency; short runs
  cannot be promoted to alpha/production evidence.
- Two short diagnostic bundles are under
  `.gitstore/capacity/api/authorization/diagnostic/20261011T092148Z` and
  `20261011T092325Z`. They motivated replacing candidate expansion/sorting with
  bounded bitset unions and per-rule provenance bits. Neutral fixtures, maximum
  bounds and concurrent reloads passed under `-race`. Observed average allocation
  fell from about 24,400 bytes/decision to 153–159 bytes/decision, but some sampled
  concurrency-32 tails still exceeded 1ms. These 2s runs overlapped other local
  work and do **not** satisfy T069 or establish the p99 acceptance target.
- With the test containers stopped, the 30s-per-level diagnostic bundle
  `20261011T092716Z` recorded all six cases below 1ms sampled p99 at concurrency
  1/8/32 (worst per-level: 72µs, 175µs, 370µs), with about 150 allocated
  bytes/decision. Large individual scheduling outliers remain visible in the
  retained maxima. Warm-up was only 1s; this remains diagnostic, not T069's
  60s/10m measurements or 30-minute reload/heap soak.

The full readiness aggregate passed again after that optimization; the final
canonical logging fixture and shared bound constants also pass the complete
auth provider suite. The user explicitly authorized a local Conventional Commit
after these checks, with no push. This is an implementation checkpoint, not
feature closure: 56 of 82 tasks are checked complete. Long
reload/heap soak, zero-I/O acceptance instrumentation, replica probes, alpha
load/replacement/chaos and remaining boundary coverage are still open. The
spec/ADR remain open. The optional Spec Kit auto-commit hook stays disabled;
the local checkpoint commit is explicitly user-authorized.
