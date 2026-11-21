# Operations, Compatibility and Acceptance (Proposed)

These are implementation obligations, not current runtime procedures or evidence of a passing deployment.

## Publication and Replica Contract

Publication is an explicit operator-owned profile distributed through the existing deployment configuration. `requiredFeatures` is a finite versioned floor such as `discovery@1`, `admin.self-access-review@1`, and selected login/bootstrap descriptor support. Each new API validates its compiled implementations and selected provider adapters against the floor before ready. Missing mandatory support/invalid config makes `/ready` fail with a bounded safe reason. Public discovery bypasses authorization-readiness failure but not its own config validity/admission checks. Mount it outside the existing global RBAC serving wrapper or restructure that wrapper narrowly; do not bypass AuthZ on management routes.

No API infers fleet membership or writes a local cache as deployment authority. Operator preflight must prove every endpoint's eligible serving replica meets the same advertised floor/profile; it fails if a replica cannot be enumerated or its profile is incompatible. Direct per-replica checks and actual process manifests accompany acceptance. A cached provider observation may reduce local flow availability; it can never add features/endpoints absent from common publication config. An upstream outage can cause conservative availability differences; equal effective documents have equal ETags.

Maintain a dedicated discovery-valid state distinct from aggregate API readiness. Invalid profile/floor at startup or reload returns 503/no-store; retained prior bytes are diagnostic-only until corrected. AuthZ unavailability must not flip discovery-valid; provider metadata unavailability affects its own flow states only. Config withdrawal/reload tests must prove obsolete advertisements are not served after local invalidation (already cached responses follow their original bounded TTL).

### Activation

1. New code starts with discovery/new advertisements disabled. Roll all API replicas to code understanding the floor and additive schema. Keep existing clients operational.
2. Verify every eligible target and provider prerequisite directly; old/unverifiable targets must leave the route before activation.
3. Apply a compatible common publication profile; ready replicas must support all advertised operations. During a config rollout, old/new profiles must be mutually safe for both sets of advertised endpoints. Additive fields are allowed only where all operation recipients already support them.
4. If no safe overlap exists, remove affected advertisements, wait the previous public TTL (max 60 seconds), drain old recipients, then activate the new profile. Explicit temporary unavailability is preferable to false support.
5. Before rollback, withdraw affected ads, drain caches, then replace code. Retain prior routes/behaviors for the TTL and documented in-flight request deadline, or deliberately return unavailable. Never remove an advertised operation while old ads remain valid.

Old binaries that do not enforce the floor cannot be made safe by new config alone. This procedure is the source of rollout safety, not a claim of distributed consensus. Controller semantics remain unchanged; evidence still verifies two connected replicas. Git remains one process; no overlapping replacement or cleanup of retained storage.

## Provider Refresh and HTTP Cache

- Expected public metadata subset is operator-configured; validated discovery must match endpoints/issuer and contain required grants/methods. Remote additions never automatically publish new capability. Changed incompatible metadata disables dependent flows until operator-approved configuration/validation catches up.
- Approved HTTPS discovery destinations only, including DNS/dial validation; no redirects; explicit isolated local-dev exceptions. Never fetch caller-supplied issuer/client metadata URLs. Public projection is allowlisted, not raw upstream JSON.
- <=16 provider observations; two workers; one queued/in-flight item/provider; three-second deadline; 256 KiB body; five-minute refresh plus <=30-second jitter. Retry delays 5/15/30/60 seconds, then normal refresh; no queue accumulation. Cancellation on shutdown. Keep validated observation 15 minutes; stale data never enables flows after expiry.
- Conditional provider fetches may use ETag/Last-Modified. A 304 extends freshness only after validating the pinned issuer/profile association. Invalid or partial bytes do not replace the last coherent observation. Failed refresh within its original validity is not immediate support loss; expiry is explicit.
- Canonical public payload SHA-256 revision excludes its own revision field. Sort keys and identity sets, preserve AuthN ordering. No volatile generated time in payload. Public cache TTL <=60 seconds and <=earliest relevant observation expiry. Recompute unavailable state on expiry before 200/304. Bound serialized output at 256 KiB. No last-good stale response beyond validity.

## Finite Budgets and Saturation

| Boundary | Default/gate |
| --- | --- |
| Public discovery | 128 in flight; rate 200/s burst 400 per API; existing bounded limiter cardinality; 256 KiB document |
| Registration inventory | 16 endpoints, 16 providers, 32 profiles, 128 extension IDs, 64 kinds x64 search terms; all also constrained by encoded size |
| Self-review | <=32 checks; one cluster/namespace capture; sequential resource resolution; ten-second total deadline; existing Admin body/complexity/concurrency budgets |
| Private probes | One-second bounded component probes under three-second overall diagnostic deadline; <=32 returned components/entries; no remote probe per returned config field |
| Metadata | Bounds above; no fetch on discovery or review request paths |

Reject over-limit configuration before activation; input limits produce structured bad-input errors. Existing admission controls reject excess work; public throttling is 429, Admin retains its existing throttling contract. No unbounded queue/goroutine, wildcard config traversal, raw provider-response cache or catalogue enumeration. Process CPU/RSS/GC, in-flight requests, limiter cardinality, refresh activity and queues must stay within configured/preflight ceilings; all ceilings are captured in evidence, and any breach fails the run.

Each batch has 1–32 selector-only reviews, no caller scope. Resolve all to CLUSTER or one actual Namespace incarnation before one capture. Reject mixtures/unknown scope without partitioning or leaking inaccessible facts. Test id/path/namespace parity, cluster target omission only where valid, missing-resource errors, context-required creation checks and recreation races. New kinds add adapters, not SDL branches.

Use shared SDK instruments with finite endpoint/provider-index/outcome labels. Never label by subject, namespace, client ID, URL, query or credential. Logs contain request correlation and safe reasons, no provider body/secret/token. Readiness identifies config/floor failures; protected diagnostics expose redacted current availability. Cache hits, expiry, refresh failures, throttles, input limits and recovery need explicit counters/timings.

## Repeatable Validation

Extend `make capacity TARGET=api PROFILE=endpoint-split MODE=<diagnostic|alpha|production>`; do not introduce public feature-specific Make targets. Extend the current JS scenario, preflight, verifier and connected Go driver with an explicitly configured discovery/review probe mode. New selectors/variables are implementation additions and must be documented/tested before use. Preserve the existing Storefront unavailable boundary probe and connected oracle; 503 throughput is never successful public catalogue traffic.

Topology/dataset: two physical APIs, two controllers, one Git service; verified retained >=5,000,000 products/>=10,000 categories; isolated new authoring/push fixtures outside retained catalogue assets. Verify ownership and finite-workload availability before touching a retained fixture. Receipts identify source/config, exact profile/floor, real process identities, dataset digest, timing window, thresholds and domain oracle results. Preserve unrelated deployments, volumes, histories and evidence.

Load: add 100 public discovery reads/s and 20 authenticated review batches/s (cover 1-check and worst-case 32-check payloads; gate each separately) for 30 minutes alongside the existing connected authoring/watch/native-push workload. Follow with five minutes at twice the new rates and 30 seconds recovery. Keep full existing endpoint-split durations and stricter domain gates. At baseline: discovery p95 <=250 ms/p99 <=1 s, review p95 <=1 s/p99 <=2 s, unexpected error fraction <1% per class/replica. Record throttles separately; baseline throttles count as unsuccessful work, not success. During overload permit bounded throttling but zero false grants/leaks/corruption; recovery's final ten seconds restore latency/success gates.

Domain verifier: every advertised endpoint maps to approved publication; no placeholder/internal addresses; grants/methods reflect fixture and profile; API-compatible resource targets; equal bytes/equal ETags; expiry/304 correctness; two-namespace isolation; review/operation parity at the same identity/policy state; create/existing targets; no subject/group overrides; diagnostics/config redaction; no arbitrary-review regression; mixed-version safe activation and rollback. A tool exit code or `__typename` alone is insufficient.

Faults: reuse `make chaos CHAOS_PROFILE=api-restart` for one explicitly owned API and `make chaos CHAOS_PROFILE=identity-restart` for the supported owned identity target. Existing profiles require 30-second recovery; do not weaken to the spec's outer 60-second ceiling. Separately hold a controlled metadata fixture beyond its deadline/validity and then restore it; unrelated discovery must remain usable, dependent flows unavailable at expiry, and availability restore <=60 seconds after provider readiness. Use virtual clocks for TTL boundary contracts and a real delayed fixture in deployed acceptance; no need to wait 15 minutes in every unit test. The identity profile targets Kratos, not arbitrary OIDC providers; do not pretend it tests a Hydra/device endpoint outage.

Full-load replacement, sustained pushes, policy invalidation and isolated reviews cover the declared window. Explicitly reject anonymous/invalid identities. Verify exact existing-resource context parity and safe REVIEW_CONTEXT_REQUIRED for proposal-dependent checks. Mixed-version coverage includes incapable floor enforcement, unsupported descriptors, compatible profiles and withdrawal/drain rollback; unexecuted cases are not passes.

## Release Gates

Authenticate through each advertised endpoint credential binding in owned fixtures: Git Smart HTTP with supported Basic/password and Bearer header paths, GraphQL HTTP headers, and supported WebSocket init/upgrade channels. Verify incompatible scheme/provider/audience and OIDC-token-as-Basic-password rejection. The latter is not a conversion supported by the current chain. These are credential-usage checks; login acquisition flows stay in shared profiles. Include explicit context-required reviews when tier/name/proposal facts are absent; do not manufacture exact create authorization from namespace-only selectors.

Contract-family coverage must enforce top-level apiVersion/kind/metadata, exclusive spec/status versus direct body placement, and applicable #40 Condition/resolved status contracts. Envelope/default/validation-before-effects, shared review/operation context parity and registry-to-implementation coverage are mandatory in the normal test path. Include negative mutations proving these gates fail on corresponding structural/behavioral drift; generated-source checks alone are insufficient. Enumerate every new root operation/input family and reject unclassified additions. A projection label is no exemption. No skipped optional drift suite or follow-up normalization PR. See [authorization contract](authorization.md#convention-ownership-and-drift-prevention). Promote proposed SDL once and remove the maintained planning duplicate with the runtime change.

Contract and failing tests first; focused Go/backend tests; schema/catalog regeneration; existing aggregate checks; `graphify update .` after code changes; `make pr-ready` before a PR. Production claims additionally require the unprofiled owned capacity/fault receipts above. No live or capacity execution is implied by this planning command. Update supported operator/client docs with actual config/procedures during implementation; keep contributor planning history out of runbooks.
