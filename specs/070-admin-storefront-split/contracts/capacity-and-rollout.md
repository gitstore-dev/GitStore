# Capacity, Recovery and Cutover Contract

## Evidence levels

| Level | What it proves | What it cannot prove |
| --- | --- | --- |
| Boundary | Paths, disjoint generated schemas, fixed Storefront 503, Admin behavior under ingress load | Successful Storefront catalog reads or publication integrity |
| Publication fixture | Direct contract tests with two targets, a newer draft, published/absent resources and a release change | A production reader, target binding, datastore capacity or deployment readiness |
| Published catalog | Real authoritative snapshots and target binding under connected load | Git replication or unsupported version compatibility |

The implementing split must pass boundary and fixture checks. Production Storefront catalog acceptance is dependency-blocked until real publication contracts ship. Do not turn 503 responses into successful reads, lower a production dataset to fixtures or report an overall published-catalog pass when its components did not run. Planning resolves this dependency; it does not certify the unavailable functionality.

## Repeatable runner

Register proposed `make capacity TARGET=api PROFILE=endpoint-split MODE=<diagnostic|alpha|production>` in `scripts/run-capacity-target.sh`, its dispatch tests and capacity README. Reuse `_capacity-k6` with `CAPACITY_PROFILE=api-endpoint-split`, existing stack ownership/evidence helpers, and the connected repository push/watch driver. No new public feature-specific Make target or Compose overlay.

Keep existing `api/readiness` and `repository/lifecycle` profiles and their thresholds. `api/authorization` remains diagnostic-only until its own acceptance gaps are closed; do not silently promote it in this feature.

Use existing mode/fixture selectors where sufficient. The evidence manifest records each evidence level and per-component outcome (`passed`, `failed`, `blocked`), measured duration, attempted/admitted/rejected/completed counts, actual topology, source/config revision and dataset receipt. Test fixtures must not make a production run appear complete. Owned provisioning reuses `compose.capacity.yml` and repository stack helpers with two APIs, two controllers and exactly one Git service using retained storage.

## Dataset and workload

Production prerequisites: at least 5,000,000 Products and 10,000 Categories in the preserved baseline, two isolated namespaces/targets, known acknowledged revisions and an independently verifiable fixture manifest. When real Storefront exists, publication-backed projections must contain the declared published cohort and a deliberately differing current draft. Verify counts/digests with existing bounded offline traversal helpers, never a routine resolver count or full request-time scan.

- Duration: 30 minutes after setup and warmup; measurements exclude setup.
- Public: 100 requests/s, up to 100 concurrent clients; mix direct/node/relationship and pages of 100 items where those operations exist. Boundary mode checks exact 503 shape separately.
- Admin: 10 operations/s, up to 20 clients, plus 20 durable watches. Verify authorized read/author/status/control operations and denied cross-scope cases on both replicas.
- Git: one bounded manifest push/s through the existing driver, preserving its payload/resource gates and same-resource concurrency safeguards. The default single writer has three pending scheduled offers; a full budget or missed producer deadline counts as dropped. Record the pending policy, dispatch delay and scheduled-offer-to-acknowledgement latency, including waiting and the actual completion/drain window. Do not add retries or exclude queue time to conceal overload. Verify every acknowledged change through the management view and durable cursor, within 60 seconds.
- Overload: twice the public offered rate for five minutes, then restore baseline. Do not introduce retries that hide dropped schedules.
- Successful real public reads: p95 ≤500 ms, p99 ≤1 s. Admin p95 ≤2 s. Unexpected errors <0.1% per endpoint, classified separately from deliberately denied/throttled/unavailable cases. Do not omit failed offered operations from the denominator without reporting them.
- Recovery: baseline performance within 30 seconds after overload; owned API restart readiness within 30 seconds; observer recovery with every acknowledged change within 60 seconds.

Record achieved throughput and payload mix, process CPU/RSS/GC, admitted/rejected/canceled work, registry entries, limiter cardinality, in-flight saturation and correctness receipts. A capacity gate must fail on exceeded bounds even if latency passes. Evidence is attributable to exact tested source/config and process identities; secrets/query bodies are redacted using existing collection rules.

## Faults and rollback

Use `make chaos CHAOS_PROFILE=api-restart CHAOS_TARGET=<explicit-owned-api> CHAOS_CONFIRM=1`. Verify ownership before mutation. Restart only one API replica while the other serves traffic; keep Git singleton and do not replace it for a GraphQL listener test. Verify durable cursor recovery rather than simply counting live sockets.

Controlled contract tests inject publication/target unavailability through test dependencies only, not a shipped flag. During faults there must be no management fallback. Once real dependencies are supplied, restoration must permit eligible reads within 30 seconds; that real-data recovery result remains blocked for this split alone.

The pre-GA initial cutover is a coordinated breaking operation:

1. Record old API/client image versions, routing and complete configured URLs. Preserve data and Git storage; this feature has no storage migration.
2. Prepare updated consumers, named `api.admin_port`/`api.storefront_port` configuration and explicit Admin-root operational URLs; remove generic `api.port`. Verify external Admin/quickstart consumers. In owned two-replica tests, map each replica's Admin and Storefront container ports explicitly to distinct host ports instead of assuming every public port is an Admin endpoint.
3. Quiesce authoring/consumers as required, remove pre-split API replicas from application routing, deploy the new API set and probe the full route matrix directly on every replica.
4. Activate matching clients and new routing. Resume management work; verify admitted revisions and durable observers.
5. If cutover fails, restore API, client URLs and routing together. Do not route new clients to old replicas or add an alias as a recovery shortcut. Do not roll back unrelated Git storage or authored resources.

After cutover, exercise rolling API/controller replacement within the new contract using concurrent replicas. Pre-split/post-split mixed routing and zero-downtime initial migration are not supported claims. Release notes must identify the endpoint change as breaking. No later release is reserved solely for alias removal.

## Acceptance completion

The split implementation can mark the schema/listener boundary complete after its applicable tests and full `make pr-ready` pass; it must list publication activation and real public-read production evidence as outstanding dependencies. The implementing PR updates ADR-0012 from Proposed to Accepted with the date prescribed by `docs/ADRs/README.md`, updates its index/open items and closes this spec's implemented boundary scope. It must not imply the whole publication lifecycle or all Storefront fields are accepted or implemented.

The earlier sustained runs remain failed evidence, including native-push scheduling/convergence gaps and a later management-queue failure. The 2026-11-06 follow-up passed the complete diagnostic/alpha boundary and fixture acceptance with unchanged rates, durations, zero-drop, convergence, resource and recovery gates. All 4,320 management operations and 2,160 native pushes were admitted/acknowledged and verified, with zero failures, drops or retries, across physical API/controller replacements. This closes T060–T061's applicable alpha scope. It does not certify publication-backed reads or production capacity: visibility p95 was 1.157 s against the separate 1 s production target, and publication/target support remains absent. The overall spec remains In progress for those and the documented WebSocket/external-consumer dependencies.
