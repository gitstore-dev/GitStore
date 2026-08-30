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
- short capacity-harness smoke with 50 subscribers for 3 seconds;
- `make pr-ready` across Go, Rust, static analysis, formatting, and license
  checks;
- `graphify update .` followed by a query that surfaced the Namespace CDC
  progress, materializer, journal cursor, readiness, and typed/generic watch
  nodes.

Commands exercised:

```bash
make test-scylla-hardening
make test-scylla-integration SCYLLA_TEST_ADDR=127.0.0.1:9042
make test-namespace-watch-capacity \
  NAMESPACE_WATCH_CAPACITY_DURATION=3s \
  NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS=50
STATICCHECK_CACHE=/private/tmp/gitstore-050-staticcheck \
  GOCACHE=/private/tmp/gitstore-050-gocache make pr-ready
graphify update .
graphify query "How does Namespace CDC flow through the durable watch journal materializer to typed and generic GraphQL subscriptions, including readiness and cursors?"
```

Operational gates still requiring the deployment-shaped environment:

- the live two-replica rolling-replacement probe, including cross-replica
  resume and forced materializer lease handoff;
- the full 60-minute, 1,000-subscriber capacity run and threshold capture.

Run those gates before production rollout with:

```bash
NAMESPACE_WATCH_API_A=https://api-a.example/graphql \
NAMESPACE_WATCH_API_B=https://api-b.example/graphql \
NAMESPACE_WATCH_TOKEN="$TOKEN" \
  make test-namespace-watch-recovery

make test-namespace-watch-capacity
```
