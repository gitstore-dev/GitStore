# Research: Admin and Storefront Endpoint Split

## Evidence boundary

Inspected source: `origin/main` at `50f2da6c`, plus this branch's proposed ADR-0012 amendment. This is source/design evidence, not deployed or load-test evidence. `graphify-out/graph.json` is absent in this worktree. The Go modules pin Go 1.27.2, gqlgen 0.17.90, gqlparser 2.5.33, Gin 1.12.0, gin-contrib/cors 1.7.7 and Prometheus 1.25.0. No dependency upgrade is required for the split.

## 1. One coordinated pre-GA cutover

**Decision**: Replace the old `/graphql` endpoint in one release with fixed separate listeners: `api.admin_port` serves `/admin/graphql` and `api.storefront_port` serves `/storefront/graphql`. Remove generic `api.port`. No shared mode, path relocation, alias, redirect, enablement switch or fallback.

**Rationale**: The user's explicit single-cutover direction supersedes ADR-0012's original three-release proposal. It avoids temporary middleware, flags and cleanup releases. New-contract replicas still require concurrent and rolling-replacement verification.

**Alternatives considered**: Three-release deprecation and a two-release alias both retain unnecessary compatibility states. Keeping `/graphql` as Admin on the shared listener contradicts the final unambiguous path contract.

**Governance**: Constitution III/VI normally prefer additive deprecation and independently compatible releases. Record this user-authorized, pre-GA exception in the plan, use a breaking Conventional Commit/release note, and coordinate client/routing rollback. This is not permission to waive non-negotiable security, bounded-work or replica requirements. Do not rewrite the constitution globally for one exception.

## 2. Separate generated schemas, shared process

**Decision**: Move current SDL to `shared/schemas/admin/`; extract only actually used shared values to `common/`, initially Node. Rename `gqlgen.yml` to `gqlgen.admin.yml`, move Admin Go sources to `internal/admin/graph/` and generate Storefront separately from `gqlgen.storefront.yml` into `internal/storefront/graph/`. Explicit generation commands select each config; do not depend on gqlgen's default filename discovery.

**Rationale**: Current `gitstore-api/gqlgen.yml` loads one flat schema glob. `generate.go` runs one gqlgen invocation. Separate source sets prevent accidental exposure; a second schema builder is necessary even while the public HTTP route returns unavailable. Reuse existing generation commands, not a new public Make target.

**Alternatives considered**: Two reverse-proxy aliases do not separate schemas. Sharing resource types retains management fields and caller-dependent data-plane selection. A second binary/process belongs to #512.

**Admin package tradeoff**: Roughly 80 files outside generated outputs reference `internal/graph/`. Moving it risks stale imports, generator scalar/model bindings and cross-package tests, but makes endpoint ownership/import-boundary checks explicit before GA. Keep package names, mechanically move/update/regenerate, compile and run existing regressions as a separate prerequisite slice before behavior changes. Retaining the old neutral path saves immediate churn but leaves asymmetric ownership and a later migration; no alias/shim package is justified. Extract shared pure helpers only when actually needed.

**Details**: `internal/middleware/security/graphql.go` imports Admin `graph/model`; retain it for Admin rather than applying it to Storefront. Reuse schema-independent authentication/registry infrastructure. No `@authorize` SDL directive currently exists; do not assume the ADR's directive prose is implemented or introduce that independent authorization redesign here. Correct the obsolete Node ID docstring while extracting it: actual IDs are base64-encoded `gid://GitStore/<kind>/<uid>` URIs.

## 3. Deliver an unavailable Storefront boundary without fake commerce

**Decision**: Generate an identity-only Storefront contract (`contracts/storefront.graphqls`). Its production route returns 503 with `STOREFRONT_UNAVAILABLE` and `Cache-Control: no-store` before data execution because no reviewed publication reader or target-binding implementation exists. No runtime availability flag, synthetic production reader, new status query or dynamic capability registry.

**Rationale**: Source contains publication design documents and action names, but no authoritative publication-backed Storefront datastore interface. A fixed unavailable handler is the smallest honest implementation. The separate generated executor is exercised directly in schema/ID boundary tests; it is not publicly activated by a test switch.

**Alternatives considered**: Exposing current Product resolvers would leak drafts. Creating a provisional projection or anonymous target selector expands into publication lifecycle. An enabled-by-default switch is still an unnecessary switch.

**Future dependency**: Actual activation requires the publication/target-binding feature to supply reviewed reader, authorization, cache and failure contracts and pass real-data tests. The split delivers the boundary; it cannot claim successful Storefront catalog reads or publication capacity. Test fixtures establish isolation only.

## 4. Listener ownership and operational paths

**Decision**: Use four distinct named ports: Admin, Storefront, Git HTTP and catalog. The first two are `api.admin_port`/`api.storefront_port`, default 4000/4001; zero and collisions are invalid. Remove generic `api.port` in this release. Pre-bind all listeners before serving; one owner closes resources on failure/shutdown. No four-layout resolver or fifth operational listener.

**Rationale**: `internal/app/server.go` has fixed `httpServer`, `gitServer`, `grpcServer` fields and asynchronously calls HTTP `ListenAndServe`. Extra dedicated listeners otherwise introduce partial startup and duplicated cleanup. Explicit bind errors remain necessary even after configuration validation.

**Alternatives considered**: Shared/dedicated modes with variable paths multiply config/docs/test states without changing controller list/watch semantics; the client already accepts a complete URL for both HTTP and WS. Fixed prefixed paths simplify Makefile/CLI/Admin consumers, and a reverse proxy can consolidate public origins. Four copied lifecycles drift; role frameworks introduce #512 scope.

**Operational contract**: `/health`, `/ready` and existing compatibility `/metrics` live at the Admin listener root. Existing datastore/watch/auth readiness remains authoritative; absent Storefront support does not poison Admin readiness. Preserve Git HTTP's intentional zero read/write timeout. Bound gRPC graceful shutdown by the caller deadline and force stop on expiration.

## 5. Limits are an implementation gap, not an available subsystem

**Decision**: Extend the existing security middleware and use pinned gqlgen primitives for endpoint profiles; implement the minimum bounded pipeline in `contracts/security-and-limits.md`. No remote limiter service, broker or second request scheduler.

**Rationale**: `NewGraphQLHandler` currently has a per-IP limiter, parsed-query cache of 1,000 and APQ cache of 100. It does not enforce parser, structural, operation-cost or principal-cost limits. `security.NewRateLimit` has an uncapped IP map and an unowned cleanup goroutine. Current introspection is unconditionally enabled. The spec's phrase “retains its principal cost budget” describes the desired contract, not shipped behavior.

**Pinned-library evidence**: gqlgen `Server.SetParserTokenLimit`, `Server.SetValidationRulesFn`, `extension.FixedComplexityLimit` and `OperationContext.DisableIntrospection` are available in the locally installed pinned source. Generated complexity callbacks need explicit pagination multipliers; default scalar/child complexity alone does not bound cardinality. WebSocket frames need their own transport bound; an HTTP body limit is insufficient.

**Alternatives considered**: Claiming doc 038's full design is already implemented is false. Implementing all future checkout/search/cost-provider features expands scope. Disabling all Admin subscriptions breaks controllers. Keep existing auth/revocation/watch machinery and add only the request bounds needed by this contract.

**WebSocket decision**: Do not fork the pinned protocol transport. Local source inspection found no successful-upgrade connection hook, so `SetReadLimit` cannot be added through configuration. A fork needs seven files plus helper/context-key changes, including both existing `AddSubscriptionError` call sites. Preserve working Admin replay/auth and explicitly leave pre-decode frame-allocation bounding as a production-hardening dependency. Connection counts and post-decode parser limits still apply. No full production bounded-memory claim is allowed until a supported upstream integration and adversarial-message tests close the gap.

## 6. Client migration includes schema loaders and health URLs

**Decision**: Migrate complete URLs and schema source sets. All API consumers use the invariant Admin path, regardless of configured ports/proxy origin. No dedicated `/graphql` exception remains in the new contract. Contextually update current examples/tests without rewriting historical specifications or introducing URL auto-fallback.

**Rationale / concrete sites**:

- `Makefile` API URL default and `API_HEALTH_URL` derivation: suffix replacement would create `/admin/health`; configure Admin's root operational URL explicitly.
- Controller `internal/config/config.go`, `.env.example`, `config.toml.example`, `config/config.toml`, README and tests; `internal/graphqlclient` already preserves arbitrary HTTP paths when converting to WebSocket URLs.
- `gitstore-api/cmd/gitctl/enroll_serviceaccount.go`, `compose.local.yml`, `compose.capacity.yml`, integration helpers, k6 defaults and `scripts/repository-capacity-stack.sh`.
- `gitstore-controller-manager/internal/schemavalidate/schemavalidate.go` currently reads a flat schema glob. It must read Admin plus common only.
- There is no `gitstore-admin/` directory in this checkout despite the ADR reference. Coordinate the actual external Admin consumer and quickstart repository in the same cutover; do not create a nonexistent local UI just to satisfy that old path.

**Alternatives considered**: Client auto-fallback hides configuration errors. Deriving health from the last path segment fails under the prefixed Admin path and reverse-proxy routing. Configuring one full Admin URL preserves HTTP/WS client behavior without schema/path detection.

## 6a. Playground and configurable browser access

**Decision**: Use the existing playground UI with an Admin/Storefront selector and editable endpoint URLs. Default the current choice to its same-origin prefixed path; require explicit peer URL input when unknown. Maintain distinct editor/schema sessions, close old subscriptions and clear auth/custom headers when endpoint/URL changes; do not auto-run a document. A private Admin URL is never advertised by the public Storefront page. This is not #472 discovery or #512 advertised deployment configuration.

**CORS**: Independent `api.admin.cors`/`api.storefront.cors` policy uses existing middleware: origins, credentials, allowed/exposed headers and max-age. Both default to empty origins/credentials false. Same-origin access works without configuration; cross-origin playground switching requires an explicit allow-list. Reject wildcard credentials and validate WS origins consistently.

**Alternatives considered**: A new UI framework or automatic public-URL inference adds unnecessary work and can leak internal addressing. Reusing one auth/editor session risks sending management credentials to a different endpoint. The picker cannot authorize requests or modify server CORS.

## 6b. Metrics authoring follows OpenTelemetry

**Decision**: New instrumentation uses OpenTelemetry Metrics SDK through `shared/observability`; no new direct Prometheus collectors or registry. This checkout still contains legacy native counters and compatibility export, so do not describe their retirement as completed source evidence or migrate every old metric as part of the split. Preserve signal semantics and collect/export SDK metrics in tests.

**Rationale**: The shared runtime already supports `otel.Meter` instruments and provider/export lifecycle. The user's intended authoring standard takes precedence over older scoped wording about native metrics. Bounded endpoint/outcome/reason attributes remain required.

## 7. Evidence reuses existing owned runners

**Decision**: Add `api/endpoint-split` to the existing capacity dispatcher, backed by a k6 HTTP profile and the existing connected repository push/watch driver. Reuse the two-API/two-controller owned stack and singleton Git storage. Reuse `api-restart` chaos.

**Rationale**: `api/readiness` is a one-minute operational smoke, not catalog load evidence. `api/authorization` is currently diagnostic-only. `repository/lifecycle` already provides ownership, fixture and recovery mechanisms; do not add a feature-specific public Make target or Compose overlay.

**Evidence outcomes**: Explicitly separate `boundary`, `publication-fixture` and `published-catalog` observations. A 503-only public workload can pass unavailable-boundary assertions but must not satisfy public-read latency/throughput acceptance. Production public-read acceptance remains dependency-blocked until real projections exist. Dataset, source/config revision, per-process identity and correctness receipts must accompany measurements.

## Resolved scope

The choices above resolve planning unknowns without new user questions. Real publication readers, target identity and successful published-catalog capacity are named external dependencies, not unresolved choices or hidden work for this implementation. ADR acceptance applies to the implemented schema/listener/security boundary; its publication follow-up must remain explicitly listed as unimplemented.

## Implementation constraint: cost compatibility

Pinned gqlgen ignores custom complexity callbacks returning zero. Use its supported fixed-scalar and ignored-field options for inline Node metadata/spec/status and pageInfo values, which contain no resource nodes. Keep resource/relationship cardinality cost and the 1,000-unit ceiling. A test reads the actual controller Go query constants and checks unchanged 250-row Product and other recovery pages against that ceiling; nested resource fan-out must still fail. A schema invariant test rejects a resource-node dependency introduced into an ignored envelope.

## External consumer inventory (read-only, 2026-11-05)

- [Standalone CLI transport](https://github.com/gitstore-dev/cli/blob/0af81622213ec11e6341c0548f9ec3e926ee060d/crates/gs/src/graphql.rs) posts directly to its configured endpoint; [context configuration](https://github.com/gitstore-dev/cli/blob/0af81622213ec11e6341c0548f9ec3e926ee060d/crates/gs/src/config.rs) retains the complete URL and separates Git selection. Configure the new Admin URL and deliberately select credentials for that endpoint identity; update examples/contract fixtures and verify against the release. No endpoint inference or old-path fallback is required. This inventory did not modify or execute the external CLI.
- [Quickstart README](https://github.com/gitstore-dev/quickstart/blob/main/README.md), file blob SHA `5ecde7dc9aac1647a0a7b94aa229ce7c9f9bc8a3`, still describes the old GraphQL route, proxy upstream and unchanged internal enrollment/controller URLs. Coordinate base named ports, Storefront mapping, internal URLs, health origin and proxy routing with the breaking API release. Its current published-image setup cannot be assumed compatible merely by changing the image tag.
- The ADR's external Admin codegen location is absent locally; fetching `gitstore-dev/gitstore-admin/codegen.yml` returned 404. Its actual repository/source set remains unverified and must be identified before release. Do not invent a local Admin project or claim its generation has migrated.

External changes and release compatibility tests remain coordination prerequisites. No external repository was changed and no messages were sent.

## Retained-data validation constraint (2026-11-05)

The completed external soak restored its controlled Product specs/UIDs and protected original Git ref/index, then stopped its owned deployment. Its zero-drop performance gate failed; its results are not evidence for this feature.

Read-only startup of this feature against the retained keyspace rejected migration compatibility. The retained `002_namespace.cql` adds catalog fencing, and `005_product.cql` replaces the namespace-partitioned Product tables with resource-sized partitions and sharded listings. Their recorded checksums match the primary checkout's unmerged storage changes rather than this feature's source. This is a substantive storage-contract difference, not a checksum-only problem. No migrations, checksum rewrites, sustained workload or fault injection were performed against that keyspace; the temporary validation processes were stopped and storage retained.

Connected acceptance must use a compatible verified dataset, or wait for the storage changes to land and rebase before rebuilding and validating. Do not copy experimental storage code into this endpoint split, weaken migration checks or claim the retained dataset's receipts establish compatibility. The Category-count prerequisite also remains unverified. Ordinary checks and local boundary/fixture evidence do not close these connected-load/recovery gates.

**Resolved source dependency**: #527 merged as `e84c66cb`; this worktree was rebased onto that commit on 2026-11-05. All twelve recorded retained migration checksums match the rebased source. Rebuild and rerun checks against this source; the earlier schema rejection is historical evidence, not a current blocker. Live startup, dataset counts and connected acceptance still require fresh verification.

## Connected-run corrections

Live startup succeeded after the rebase. Independent offline listing counts verified more than five million retained Products; additional owned Category fixtures brought the verified baseline above ten thousand without rewriting the retained Product records or Git repository.

The native service returns invalid shallow/have negotiation responses. Owned diagnostic clones confirmed ordinary full clones and Git's standard `fetch.negotiationAlgorithm=noop` work; fixture helpers use those paths with existing time/output limits. This does not certify the unsupported protocol paths. Keep the baseline outside an empty writable workload namespace, use separate Admin-authoring/native-push repositories, and use one push worker for one branch at one offer per second. Competing clones and baseline-sized authoring trees introduced client contention and queue shedding in a completed failed run. HTTP pools are preallocated to their existing declared maxima (100 public, 20 Admin); rates and zero-drop gates are unchanged.

The completed run also exposed Category status conflicts consuming the generic failure budget and quarantining otherwise healthy concurrent work. Upstream [#529](https://github.com/gitstore-dev/GitStore/pull/529) covers this exact fix: a 100 ms timed requeue retains conditional versions and durable obligations while the watch catches up; other failures retain the normal retry/quarantine policy. Its regression includes restart of the losing replica and delayed delivery of the winning status. As directed, the overlapping local prototype was preserved privately and removed. #529 merged as `4648798e`, and this worktree was rebased onto it on 2026-11-05. The fix and tests are inherited from main, with no duplicate feature changes to those files. Failed/aborted attempts remain failed evidence; applicable sustained-load and recovery checks must be repeated after integration.

## Validation after #529 and separate dependency

Rebuilt validation source `ef5bc52f` includes the upstream conflict fix. The full HTTP workload completed 260,040 iterations with zero dropped schedules; Admin HTTP p95 was 5.29 ms, unexpected errors were below 0.1% independently on both endpoints, recovery checks passed, and no unsafe Storefront response was observed. The connected Admin driver passed 4,320 acknowledged authoring operations (p95 257 ms), twenty durable observers and replay across physical API/controller replacement. Both controllers had zero quarantined items after the run. These are unavailable-boundary and management observations, not published-catalog reads.

The combined alpha run **failed**: of 2,160 offered native pushes, 1,851 were acknowledged and 309 schedules were dropped; 361 acknowledged-push convergence oracles failed. A bounded comparison against the committed Git tree found actual committed fixtures absent from both Admin replicas. Other sampled committed fixtures were Ready with the original commit revision. This establishes an admission/convergence gap distinct from Category conflict quarantine, without identifying its internal cause or attributing all oracle failures to missing admission. Push p95 was 1.197 s, exceeding the single worker's one-second offer interval. No gate or offered rate was lowered.

The user explicitly directed keeping the push scheduling/admission/convergence work as a **separate dependency** while finishing this endpoint boundary. Preserve the failed aggregate result and repeat sustained acceptance only after that dependency is resolved; a short recovery fixture cannot replace it. Publication reads, adversarial WebSocket pre-decode bounds and external consumer release coordination also remain open.

The matching-version cutover/rollback rehearsal passed on both API/controller pairs, restoring old and new routes and credential readiness without replacing Git or changing retained volumes. Final endpoint review additionally found and corrected custom page limits below the datastore default and mixed `first`/`last` cost undercharging, with red/green and race regressions. Those corrections follow the sustained snapshot above; they do not turn its failed native-push result into a pass. The namespace recovery probe was updated to use the current journal cursor version and tolerate legitimate earlier status replay while still requiring the acknowledged target creation.

The separate physical `make chaos` restart check passed: readiness returned in 2.66 seconds and all 180 offered native pushes were acknowledged and verified through both Admin replicas and durable watches, with zero failures, drops or retries. This isolated three-minute fixture proves functional restart recovery, not sustained acceptance. The root namespace recovery selector passed bootstrap/resume, documented-consumer, forced-epoch and physical-replacement checks; the optional slow-consumer overflow subprobe was not enabled. Protected original Git ref/index and the unrelated Rust lockfile edit were verified unchanged.


## Sustained follow-up and synchronous authoring fence (2026-11-06)

A fresh sustained run on clean `0cd61a1f` preserved 5,000,257 Products. The HTTP workload completed 260,039 iterations with zero dropped schedules. All 2,160 offered native pushes were acknowledged and verified through both Admin replicas and durable watches, with zero failures, drops or retries (scheduled-offer-to-acknowledgement p95 345 ms). Both controllers were physically replaced and completed fresh reconciliation in 10.05 and 30.71 seconds; API B was also physically replaced. The aggregate **failed** because the connected management queue shed 29 operations, all in the last two minutes. Its early assertion prevented a final connected/resource receipt; no component result overrides that failure. Original server HEAD/refs and every retained Product shard count were unchanged. The original client directory had already been removed, as confirmed by the user; its index could not be reverified.

Read-only reconstruction of the test-owned system repository found the 29 absent management sequences and dispatch waits up to approximately eleven seconds. API logs showed repeated admission supersession and synchronous repairs during the same period. A separate quiet three-minute management diagnostic admitted all 360 operations without drops; it does not establish sustained acceptance. Deterministic regressions reproduced rejection of an unchanged Repository manifest after an unrelated ref advance, including during a CAS retry. Synchronous Repository updates now verify the complete live manifest and retain their accepted source revision instead of requiring a globally idle system branch. Changed, removed, unreadable or identity-mismatched manifests remain fenced; create and deletion semantics are unchanged. The failure diagnostics separately record queue saturation and missed schedules, plus at most 32 slow-operation timings, before a failed assertion. T060–T061 remain open pending a complete rerun with unchanged gates.


## Validated alpha boundary benchmark (2026-11-06)

Clean API/controller source `5e58b68f` and unchanged native Git inputs from `0cd61a1f` passed the full canonical alpha aggregate, including final source, datastore, telemetry/resource and domain verification. Native image reuse was verified against identical native build inputs. The retained baseline contained 5,000,257 Products and 25,319 Categories. Docker Desktop had eight vCPUs and 16,787,881,984 bytes of runtime memory, three Scylla nodes (two SMP / 4 GiB container limit each), two APIs, two controllers and one native Git process. The actual generator was local k6 v2.3.0 (darwin/arm64, Go 1.27.1); the runner's nominal fallback image field was not the binary used. Its actual process and version were recorded separately.

The unprofiled workload offered 100 public ingress requests/s for thirty minutes, 200/s for five minutes, then restored 100/s for thirty seconds; Admin combined eight HTTP probes/s with two authoring operations/s and twenty durable watches. One native writer offered one push/s for thirty-six minutes with the unchanged three-pending-offer policy and inclusive scheduled-offer latency. HTTP completed 260,039 iterations with zero dropped schedules; all 4,320 management operations and 2,160 native pushes were admitted/acknowledged and verified with zero failures, drops, retries or missing durable-watch oracles. HTTP Admin p95 was 4.956 ms, actual authoring p95 145 ms, push p95/p99 295/361 ms and maximum push latency 861 ms. API B recovered after physical replacement in 1.024 s; both controllers were physically replaced and completed fresh reconciliation in 10.83/29.94 s. Replay p95 was 11 ms. Clock continuity passed (maximum wall/monotonic drift 34.21 ms).

All unchanged alpha CPU/RSS and endpoint resource ceilings passed. API RSS ended at 106,631,168 / 103,555,072 bytes (12.33% / 20.51% growth); normalized CPU was 3.46% / 0.77%. Management visibility p95/p99 was 1.157/1.283 s: the separate production p95 target of one second remains unmet, so this is not production-capacity certification. Storefront responses remained the unavailable boundary, never successful publication-backed reads. Publication/target activation, adversarial WebSocket pre-decode bounds and external consumer release coordination remain open. T060–T061's applicable alpha scope is complete; overall spec status stays In progress. Earlier failed/interrupted runs were not reclassified. Original server HEAD/refs and every retained Product shard count were independently verified unchanged; the already removed historical client index remains unverifiable.
