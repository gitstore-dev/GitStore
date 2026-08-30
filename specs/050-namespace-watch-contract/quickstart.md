# Quickstart: Namespace Watch Contract

## Test-first implementation sequence

1. Add failing schema tests for `watchNamespaces` and
   `NamespaceWatchEvent`.
2. Add failing authorization tests proving typed and generic Namespace watches
   require `namespace.watch` before cursor validation.
3. Add failing journal cursor/replay/expiry/overflow tests against the
   backend-neutral `WatchJournal` contract.
4. Add failing Scylla migration and CDC integration tests proving successful
   base writes have CDC rows and rejected/conflicting/no-op spec-047 operations
   do not create data events.
5. Add failing materializer tests for CDC classification, append-before-progress,
   duplicate recovery, lease fencing, journal bookmarks, and bounded batches.
6. Add failing resolver tests for typed payloads and identical generic/typed
   cursor streams.
7. Add failing two-replica transport tests: mutate through replica A, watch and
   resume through B, replace A, then reverse roles.
8. Implement migration 006, journal backends, materializer, resolver, AuthZ,
   metrics/readiness, and docs in that order.
9. Run the 60-minute sustained envelope and rolling replacement probe.
10. Run `make pr-ready`.

## Focused schema and resolver checks

```bash
cd gitstore-api
go generate ./...
go test -count=1 ./internal/graph/resolver ./internal/middleware/security
```

Expected contract coverage:

- `watchNamespaces` has no namespace argument;
- event field `namespace` is typed and nullable;
- generic Namespace path remains available;
- both paths use `namespace.watch`;
- denied callers receive `FORBIDDEN` before cursor errors;
- ADDED/MODIFIED contain full spec-047 Namespace state;
- DELETED/BOOKMARK contain no resource payload.

## Backend-neutral journal checks

```bash
cd gitstore-api
go test -count=1 ./internal/watchjournal/...
```

Cover:

- opaque cursor parsing and epoch mismatch;
- bootstrap high-water capture;
- resume strictly after cursor;
- seven-day/event-count expiry;
- 256-event batches and 100,000-event replay cap;
- subscriber overflow -> `WATCH_EXPIRED`;
- durable advanced BOOKMARK;
- identical typed/generic order;
- duplicate events accepted.

## Scylla CDC and materializer checks

Start the repository's Scylla test services, then:

```bash
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042
```

Required assertions:

- migration 006 enables full preimage/postimage CDC with 14-day TTL;
- CDC log and base writes share acknowledged success;
- successful create/update/status/finalizer/final-delete classify correctly;
- repeated `ALREADY_TERMINATING`, conflicts, and rejected admission do not
  create false data events;
- progress advances only after journal append;
- crash after append produces a safe duplicate;
- stale lease token cannot advance progress;
- migration 006 remains in supported rollback artifacts.

## Cross-replica GraphQL scenario

1. Start shared Scylla plus API replicas A and B.
2. Open typed watch on A using the bootstrap sentinel.
3. Receive BOOKMARK C, keep the socket open, and list all Namespaces through B.
4. Create through B, update through A, start termination through B, clear the
   finalizer/status through A, and complete deletion through B.
5. Verify ordered ADDED/MODIFIED/DELETED events on A with complete/null payload
   rules.
6. Persist the last cursor, close A, and resume through B.
7. Replace B under continued mutations and resume through the replacement.
8. Force retention expiry and subscriber overflow; verify `WATCH_EXPIRED`.
9. Repeat through `watchResources(kind: "Namespace")` and compare cursors.

## Consumer example

Bootstrap subscription:

```graphql
subscription BootstrapNamespaces {
  watchNamespaces(resourceVersion: "__namespace_watch_bootstrap__") {
    type
    name
    resourceVersion
    namespace {
      apiVersion
      kind
      metadata {
        name
        uid
        resourceVersion
        generation
        finalizers
      }
      spec {
        title
        tier
      }
      status {
        observedGeneration
        lastAppliedRevision
        conditions {
          type
          status
          reason
          message
        }
      }
      body
    }
  }
}
```

After receiving the first BOOKMARK cursor, keep the subscription open and page
the current snapshot:

```graphql
query ListNamespaces($first: Int, $after: String) {
  namespaces(first: $first, after: $after) {
    edges {
      cursor
      node {
        apiVersion
        kind
        metadata {
          name
          uid
          resourceVersion
          generation
          finalizers
        }
        spec {
          title
          tier
        }
        status {
          observedGeneration
          lastAppliedRevision
          conditions {
            type
            status
            reason
            message
          }
        }
        body
      }
    }
    pageInfo {
      hasNextPage
      endCursor
    }
  }
}
```

Resume after restart:

```graphql
subscription ResumeNamespaces($cursor: String!) {
  watchNamespaces(resourceVersion: $cursor) {
    type
    name
    resourceVersion
    namespace {
      metadata {
        name
        uid
        resourceVersion
        generation
        finalizers
      }
      spec {
        title
        tier
      }
      status {
        observedGeneration
        conditions {
          type
          status
          reason
        }
      }
    }
  }
}
```

On `extensions.code == "WATCH_EXPIRED"`, discard the cursor and repeat
bootstrap/list. On `WATCH_UNAVAILABLE`, retry with bounded backoff without
trusting the old cache as current.

## Capacity and recovery validation

Run for 60 minutes with:

- two API replicas;
- 10 sustained Namespace transitions/second;
- 100 transitions/second bursts;
- 1,000 concurrent subscriptions split across replicas;
- one rolling replica replacement;
- forced CDC reader restart and lease handoff;
- 10,000-event replay probes.

Pass thresholds:

- event visibility p95 ≤1 second and p99 ≤3 seconds;
- replay of 10,000 events p95 ≤5 seconds;
- internal errors <0.1%;
- zero missing acknowledged transitions;
- recovery within 30 seconds;
- CPU <80% per replica;
- retained-memory growth <10%;
- no normal stream closure after overflow/discontinuity.

## Documentation and final gate

Update:

- `docs/namespace/namespace-watch.md`;
- `docs/configuration.md`;
- the controller watch/status runbook with Namespace-specific expiry,
  materializer lag, and recovery procedures;
- deployment/migration guidance for migration 006 and mixed-version deny.

Then run:

```bash
make pr-ready
```

## Validation evidence (2026-08-30)

Completed:

- focused schema, authorization, resolver, journal, memdb, and Scylla-hardening
  suites;
- tagged Scylla datastore integration, including the official
  `scylla-cdc-go` reader materializing an acknowledged Namespace mutation;
- the deployment-shaped two-replica recovery gate on a fresh Scylla keyspace:
  migration 006 completed in 887 ms with its corrected physical schema, all
  three probes passed in 5.702 seconds (6.13 seconds wall), cross-replica event
  visibility was 872.7 ms, fenced lease handoff advanced token 1 to 2 in about
  4.975 seconds, and the replacement replica became ready in 48 ms;
- the original in-process `internal/watchjournal` capacity smoke and 60-minute
  run completed, but review correctly determined that they did not exercise
  deployed GraphQL/WebSocket transports, two API processes, or Scylla and
  therefore do **not** count as PR-003/T061 evidence;
- the replacement gate is deployment-driven from
  `tests/integration/namespace_watch_capacity_test.go`: it requires two
  distinct API URLs, a replacement endpoint naming one of them, a trigger file
  watched by the deployment harness, and an authenticated token;
  creates acknowledged Namespace transitions through both GraphQL endpoints;
  splits real WebSocket subscriptions across both replicas; replays 10,000
  retained events through GraphQL; checks sustained and burst scheduling,
  missing transitions, stream errors, p95/p99 visibility, replacement recovery,
  requires the triggered endpoint to become unavailable and return with a new
  `process_start_time_seconds`, resumes a peer-issued cursor through that new
  process, reconnects replacement-disrupted subscribers from each socket's last
  observed cursor while preserving GraphQL terminal errors, and enforces
  GOMAXPROCS-normalized per-process CPU and resident memory from `/metrics` (a
  short smoke may explicitly relax only the CPU/resident assertions);
- the focused integration test compile and `go vet ./...` passed;
- a short deployed smoke against two API containers and a fresh shared Scylla
  keyspace acknowledged 50 Namespace mutations split across both GraphQL
  endpoints, then failed its bounded replay with
  `WATCH_UNAVAILABLE/MATERIALIZER_NOT_READY`; both `/health` and `/ready`
  subsequently reported healthy, but the authoritative Namespace rows were
  `watch_committed=true` while the corresponding journal window contained only
  `BOOKMARK` records. No replacement was triggered and this failed smoke is not
  T061 evidence;
- after commit `4e7a38f` corrected false-to-true commit promotion, a 12-second
  functional deployment smoke with 20 subscribers and 50-event replay reached
  the final latency assertion after passing real API-A outage/restart identity,
  cursor-resumed subscriber failover, zero-missing delivery, replay, mutation
  error, and recovery assertions. Its p95 was 5.47 seconds because the forced
  replacement and lease-handoff interval dominated the intentionally tiny
  sample; this is functional smoke evidence only, and the production latency
  threshold remains unchanged for the 60-minute gate;
- the full replacement 60-minute/1,000-subscriber deployed gate remains
  pending and T061 is intentionally open until its emitted metrics are recorded
  here;
- `make pr-ready` across Go, Rust, static analysis, formatting, and license
  checks;
- `graphify update .` followed by a query that surfaced the Namespace CDC
  progress, materializer, journal cursor, readiness, and typed/generic watch
  nodes.

Commands exercised:

```bash
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042
NAMESPACE_WATCH_API_A=http://127.0.0.1:4100 \
NAMESPACE_WATCH_API_B=http://127.0.0.1:4101 \
NAMESPACE_WATCH_API_REPLACEMENT=http://127.0.0.1:4100 \
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=/private/tmp/gitstore-watch-smoke-replace \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
  make test-namespace-watch-recovery
NAMESPACE_WATCH_API_A=http://127.0.0.1:4100 \
NAMESPACE_WATCH_API_B=http://127.0.0.1:4101 \
NAMESPACE_WATCH_API_REPLACEMENT=http://127.0.0.1:4100 \
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=/private/tmp/gitstore-watch-capacity-smoke-replace \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
  make test-namespace-watch-capacity \
    NAMESPACE_WATCH_CAPACITY_DURATION=12s \
    NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS=20 \
    NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS=50 \
    NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES=3 \
    NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY=5s \
    NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL=2s \
    NAMESPACE_WATCH_CAPACITY_BURST_SIZE=10
STATICCHECK_CACHE=/private/tmp/gitstore-050-staticcheck \
  GOCACHE=/private/tmp/gitstore-050-gocache make pr-ready
graphify update .
graphify query "How does Namespace CDC flow through the durable watch journal materializer to typed and generic GraphQL subscriptions, including readiness and cursors?"
```

Run the still-pending PR-003/T061 gate before production rollout against two
distinct API processes backed by the same Scylla deployment. The deployment
harness must watch `NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE` and restart the
process addressed by `NAMESPACE_WATCH_API_REPLACEMENT` in place after the test
writes that file. The gate must observe both the outage and a changed
`process_start_time_seconds`; merely pointing at an already-live endpoint is
not replacement evidence:

```bash
NAMESPACE_WATCH_API_A=http://api-a:4000 \
NAMESPACE_WATCH_API_B=http://api-b:4000 \
NAMESPACE_WATCH_API_REPLACEMENT=http://api-a:4000 \
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=/var/run/gitstore/watch-capacity-replace \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
  make test-namespace-watch-capacity
```
