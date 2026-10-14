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
a queued generation and checks explicit stop behavior. No CDC ordering,
checkpoint, retry, schema or API semantics are changed. The API module uses a
local Go module replacement; its container includes this directory. The module
uses the API's existing Scylla gocql v1.18.0 replacement for consistent standalone
and workspace tests, rather than upstream's older v1.17.3 replacement.
An existing upstream `nolint:unused` annotation also has an equivalent
`lint:ignore U1000` annotation for the root staticcheck command.

Remove the replacement and local copy when an upstream release includes the
same fix, after rerunning these regressions and live replacement tests. No
upstream issue, branch or release has been published by this change.
