# Scylla CDC dependency patch

This directory contains the production Go sources of
[`github.com/scylladb/scylla-cdc-go`](https://github.com/scylladb/scylla-cdc-go)
at **v1.2.1**, copied using Go's vendoring tool. The original Apache-2.0
license is preserved in `LICENSE.txt`; original source and README attribution
remain unchanged.

GitStore modifies `Reader.Run` in `reader.go` to stop the generation fetcher
on every exit of its stop-monitor goroutine. Upstream stops it only for explicit
`Reader.Stop`. Context cancellation or consumer errors can otherwise leave
`pushGeneration` blocked on an unconsumed channel and prevent the reader's
errgroup from finishing.

`reader_shutdown_test.go` reproduces cancellation and consumer-error exits with
a queued generation and checks explicit stop behavior.

The optional `QueryCompletedNotificationConsumer` callback in `change.go` and
`stream_batch.go` acknowledges every successfully consumed and closed query
window. GitStore's consumers use it to advance quiet streams even when another
stream in the same batch contains changes. Existing consumers retain their
original `Empty` behavior. Queries that fail during iteration, closure or change
consumption do not issue completed-window notifications; an error from a
completion callback stops the reader. `stream_batch_test.go` covers mixed
active/quiet streams, empty windows and query/consumer failures.

CDC ordering, retry and schema semantics are preserved. The API module uses a
local Go module replacement; its container includes this directory. The module
uses the API's existing Scylla gocql v1.18.0 replacement for consistent standalone
and workspace tests, rather than upstream's older v1.17.3 replacement.
The standalone compression dependency matches the API's v1.20.1 pin, avoiding
the dictionary-processing integer overflow in v1.18.5 (CVE-2026-63209).
An existing upstream `nolint:unused` annotation also has an equivalent
`lint:ignore U1000` annotation for the root staticcheck command.

Remove the replacement and local copy when an upstream release includes the
same fixes, after rerunning these regressions and live replacement tests.

The opt-in `MaxEmptyQueryTimeWindowSize` setting accelerates historical
catch-up without widening change-delivery windows. After successful empty
queries, the reader doubles its probe width up to that limit, bounded by the
confidence window and any known generation end. A probe that finds a row is
closed and reread at `QueryTimeWindowSize` before any change is delivered.
Failed queries or iterator closure never acknowledge the probed interval.
The API enables probes up to one hour for its existing journal sources;
probing is disabled by default. `stream_batch_test.go`
covers empty-range continuity, the cap, nonempty fallback and probe failures.
