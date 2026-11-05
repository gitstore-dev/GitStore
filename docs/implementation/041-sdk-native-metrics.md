# SDK-native metrics

GitStore records each measurement once through the OpenTelemetry Metrics API.
Go uses one SDK provider with independent Prometheus and cumulative OTLP
readers. Rust uses its SDK periodic reader and OTLP exporter. There is no
Prometheus bridge, global Prometheus instrumentation registry, or handwritten
metric-protobuf conversion. Prometheus dependencies remain only for SDK
exposition and test assertions.

Providers belong to the service telemetry lifecycle. Watch and health
callbacks belong to their owning server and unregister on close. Controllers
retain process-lifetime instruments and their stable per-process identity.
SDK resources carry service, version, host and process identity during export.
Replica replacement starts new cumulative series; scrape identity gauges retain
their existing single `instance_id` label. Watch lag/bookmark ages are evaluated
at collection time so a stalled producer cannot freeze reported age.

Application counters are monotonic cumulative sums and reset with the process.
Runtime observable counters report the underlying runtime/OS cumulative value;
Linux network counters belong to the process's network namespace and can
survive a process replacement. Up/down
counters are nonmonotonic cumulative sums. Gauges retain the latest observation
(or read current state in their registered callback). Histograms retain
cumulative count, sum and explicit bucket boundaries across independent reads.
Trace exemplars remain SDK exemplars rather than metric attributes.
Synchronous instruments with no observations are absent until first recorded,
as required by SDK collection. In particular, an unused scalar histogram no
longer fabricates an initial zero-count Prometheus family. Consumers should
handle an unobserved series as no data; recording a fake zero-duration sample
would corrupt histogram count and sum. Explicit zero counter/gauge recordings
at existing lifecycle boundaries retain their usual meaning.

## Application inventory

Names in the table are unchanged in OTLP and Prometheus. Unit `1` means a
dimensionless count/value (SDK unit metadata is omitted for these legacy
signals). `s` means seconds. Histograms use explicit buckets. The named
bucket sets are:

- default: .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10.
- capacity: .001, .005, .01, .025, .05, .1, .25, .5, .75, 1, 1.5, 2, 3, 5.

The owner column denotes the service/provider lifecycle. Instrument declarations
remain beside the datastore, admission, watch, resolver, auth, controller,
credential and secret code that records them. Attributes use the existing
bounded operational vocabularies. Process identity is the deliberate
process-scoped exception; never attach user, resource, repository or IP identity.

| Previous signal → SDK instrument | Kind | Unit | Attributes | Buckets | Owner |
| --- | --- | --- | --- | --- | --- |
| `gitstore_authz_decision_duration_seconds` (same) | Float64Histogram | s | action, outcome | .000001,.000005,.00001,.00005,.0001,.0005,.001,.005,.01,.05,.1,.5,1 | API |
| `gitstore_authz_registry_reloads_total` (same) | Float64Counter | 1 | outcome: accepted, rejected | — | API |
| `gitstore_api_process_instance_info` (same) | Float64ObservableGauge | 1 | instance_id | — | API |
| `gitstore_namespace_validation_rejections_total` (same) | Float64Counter | 1 | code, reason | — | API |
| `gitstore_namespace_deletion_rejections_total` (same) | Float64Counter | 1 | reason | — | API |
| `gitstore_namespace_deletion_outcomes_total` (same) | Float64Counter | 1 | outcome | — | API |
| `gitstore_namespace_validation_duration_seconds` (same) | Float64Histogram | s | stage | capacity | API |
| `gitstore_namespace_admission_stage_duration_seconds` (same) | Float64Histogram | s | stage | capacity | API |
| `gitstore_category_deletion_dependent_lookup_duration_seconds` (same) | Float64Histogram | s | — | default | API |
| `gitstore_category_deletion_blocked_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_product_deletion_dependent_lookup_duration_seconds` (same) | Float64Histogram | s | — | default | API |
| `gitstore_product_deletion_blocked_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_admission_rejections_total` (same) | Float64Counter | 1 | kind, phase | — | API |
| `gitstore_datastore_operation_duration_seconds` (same) | Float64Histogram | s | operation, backend | default | API |
| `gitstore_datastore_operation_errors_total` (same) | Float64Counter | 1 | operation, backend | — | API |
| `gitstore_datastore_projection_write_failures_total` (same) | Float64Counter | 1 | operation, backend, resource_kind, projection | — | API |
| `gitstore_datastore_compensation_attempts_total` (same) | Float64Counter | 1 | operation, backend, resource_kind, projection | — | API |
| `gitstore_datastore_compensation_failures_total` (same) | Float64Counter | 1 | operation, backend, resource_kind, projection | — | API |
| `gitstore_datastore_projection_findings_total` (same) | Float64Counter | 1 | operation, backend, resource_kind, projection, finding_type | — | API |
| `gitstore_category_ancestor_index_writes_total` (same) | Float64Counter | 1 | result | — | API |
| `gitstore_category_ancestor_index_repair_required_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_resource_watch_materializer_leader` (same) | Float64Gauge | 1 | — | — | API |
| `gitstore_resource_watch_journal_oldest_sequence` (same) | Float64Gauge | 1 | — | — | API |
| `gitstore_resource_watch_journal_high_water_sequence` (same) | Float64Gauge | 1 | — | — | API |
| `gitstore_resource_watch_subscribers` (same) | Int64ObservableGauge | 1 | path | — | API |
| `gitstore_resource_watch_expired_total` (same) | Float64Counter | 1 | reason | — | API |
| `gitstore_resource_watch_overflow_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_resource_watch_append_errors_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_resource_watch_duplicates_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_resource_watch_replay_events_total` (same) | Float64Counter | 1 | — | — | API |
| `gitstore_resource_watch_replay_duration_seconds` (same) | Float64Histogram | s | — | capacity | API |
| `gitstore_resource_watch_delivery_latency_seconds` (same) | Float64Histogram | s | — | capacity | API |
| `gitstore_resource_watch_cdc_discovery_seconds` (same) | Float64Histogram | s | — | capacity | API |
| `gitstore_resource_watch_materializer_stage_duration_seconds` (same) | Float64Histogram | s | stage | capacity | API |
| `gitstore_resource_watch_materializer_batch_size` (same) | Float64Histogram | 1 | — | 1,2,4,8,16,32,64,128,256 | API |
| `gitstore_resource_watch_cdc_lag_seconds` (same) | Float64ObservableGauge | s | — | — | API |
| `gitstore_resource_watch_bookmark_age_seconds` (same) | Float64ObservableGauge | s | — | — | API |
| `gitstore_status_write_conflicts_total` (same) | Float64Counter | 1 | kind | — | API |
| `gitstore_product_deletion_outcomes_total` (same) | Float64Counter | 1 | outcome | — | API |
| `gitstore_category_mutation_total` (same) | Float64Counter | 1 | operation, outcome | — | API |
| `gitstore_category_mutation_duration_seconds` (same) | Float64Histogram | s | operation | .01,.025,.05,.1,.25,.5,.75,1,1.5,2,3,5,10 | API |
| `gitstore_git_http_auth_requests_total` (same) | Float64Counter | 1 | outcome, service | — | API |
| `gitstore_api_authn_requests_total` (same) | Float64Counter | 1 | provider, outcome | — | API |
| `gitstore_controller_recovery_in_progress` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_recovery_pages` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_recovery_rows` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_recovery_last_progress_timestamp_seconds` (same) | Float64Gauge | s | kind | — | controller |
| `gitstore_controller_conflict_requeues_total` (same) | Float64Counter | 1 | kind | — | controller |
| `gitstore_controller_process_instance_info` (same) | Float64ObservableGauge | 1 | instance_id | — | controller |
| `gitstore_controller_queue_depth` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_active_workers` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_poison_items_total` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_stalled_workers` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_controller_reconcile_total` (same) | Float64Counter | 1 | kind, result | — | controller |
| `gitstore_controller_checkpoint_last_write_timestamp_seconds` (same) | Float64Gauge | s | kind | — | controller |
| `gitstore_controller_checkpoint_write_failures_total` (same) | Float64Counter | 1 | kind | — | controller |
| `gitstore_controller_checkpoint_replay_backlog` (same) | Float64Gauge | 1 | kind | — | controller |
| `gitstore_category_deletion_product_pages_total` (same) | Float64Counter | 1 | — | — | controller |
| `gitstore_category_deletion_completion_conflicts_total` (same) | Float64Counter | 1 | — | — | controller |
| `gitstore_category_deletion_retries_total` (same) | Float64Counter | 1 | — | — | controller |
| `gitstore_controller_credential_exchange_total` (same) | Float64Counter | 1 | result | — | controller |
| `gitstore_controller_credential_exchange_duration_seconds` (same) | Float64Histogram | s | — | .001,.005,.01,.05,.1,.5,1,2,5,10 | controller |
| `gitstore_controller_credential_exchange_inflight` (same) | Float64Gauge | 1 | — | — | controller |
| `gitstore_controller_credential_exchange_peak_inflight` (same) | Float64Gauge | 1 | — | — | controller |
| `gitstore_controller_credential_retry_max_seconds` (same) | Float64Gauge | s | — | — | controller |
| `gitstore_secret_resolution_total` (same) | Float64Counter | 1 | consumer, purpose, tier, provider, reason | — | controller |
| `gitstore_secret_resolution_duration_seconds` (same) | Float64Histogram | s | consumer, purpose, tier, provider | 0.0001,0.0005,0.001,0.005,0.01,0.05,0.1,0.5,1,2 | controller |
| `gitstore_secret_resolution_inflight` (same) | Float64UpDownCounter | 1 | consumer, purpose, tier, provider | — | controller |
| `gitstore_telemetry_records_total` (same) | Float64Counter | 1 | signal, stage | — | shared telemetry |

## Endpoint, RPC and exporter signals

| Previous signal → SDK instrument | Kind / aggregation | Unit | Attributes | Owner |
| --- | --- | --- | --- | --- |
| `gitstore_api_endpoint_requests_total` → `gitstore.api.endpoint.requests_total` | Int64Counter / monotonic sum | 1 | endpoint, outcome, reason | API endpoint runtime |
| `gitstore_api_endpoint_duration_seconds` → `gitstore.api.endpoint.duration_seconds` | Float64Histogram / SDK default explicit buckets | s | endpoint, reason | API endpoint runtime |
| `gitstore_api_endpoint_in_flight` → `gitstore.api.endpoint.in_flight` | Int64UpDownCounter / nonmonotonic sum | 1 | endpoint, reason | API endpoint runtime |
| `gitstore_api_endpoint_limiter_keys` → `gitstore.api.endpoint.limiter_keys` | Int64ObservableGauge | 1 | endpoint, reason | API endpoint runtime |
| `gitstore_api_endpoint_connections` → `gitstore.api.endpoint.connections` | Int64ObservableGauge | 1 | endpoint | API endpoint runtime |
| `gitstore_api_endpoint_limits` → `gitstore.api.endpoint.limits` | Int64ObservableGauge | 1 | endpoint, reason | API endpoint runtime |
| `grpc_client_started_total`, `grpc_client_msg_sent_total`, `grpc_client_msg_received_total` (same) | Int64Counter / monotonic sum | 1 | grpc_service, grpc_method, grpc_type | API Git client |
| `grpc_client_handled_total` (same) | Int64Counter / monotonic sum | 1 | grpc_service, grpc_method, grpc_type, grpc_code | API Git client |
| `grpc_server_handled_total` (same) | u64 Counter / monotonic sum | 1 | grpc_method, grpc_code | native Git |
| `grpc_server_handling_seconds` (same) | f64 Histogram / cumulative | s | grpc_method | native Git |
| `gitstore_schema_validation_total` (same; formerly unregistered) | u64 Counter / monotonic sum | 1 | result | native Git |
| `gitstore_telemetry_records_total` (same) | u64 Counter / monotonic sum | 1 | signal, stage | native Git |
| `promhttp_metric_handler_requests_total` (same) | Int64Counter / monotonic sum | 1 | code | shared Go scrape handler |
| `promhttp_metric_handler_requests_in_flight` (same) | Int64UpDownCounter / nonmonotonic sum | 1 | — | shared Go scrape handler |

Native RPC histogram boundaries remain .001, .005, .01, .025, .05, .1,
.25, .5, 1, 2.5, 5 seconds. Endpoint SDK histograms retain SDK default
boundaries (0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500,
5000, 7500, 10000); this migration does not retune admission instrumentation.

The old converter exported flattened endpoint names over OTLP. The official
SDK exporter sends the dotted instrument names above. Source declarations for
requests/duration now explicitly include their `_total`/`_seconds` suffixes;
SDK and collector Prometheus translation suppress automatic suffix addition.
All six endpoint Prometheus names remain unchanged. Existing capacity/chaos
queries use those flat names; raw-OTLP consumers must adopt the dotted names.
Integer SDK instruments also retain integer OTLP points instead of converting
them through Prometheus floating-point values. Instrumentation scope is retained in OTLP;
compatibility scrapes omit scope labels to preserve existing label shapes.
Converted Go signals move from the synthetic `gitstore/native` scope into
their declaring component's SDK scope; native Git signals gain their RPC,
schema-validation or telemetry scope. Consumers filtering OTLP scopes must
use those component scopes. Repository capacity/chaos queries do not filter
the former synthetic scope.
Reserve a unique instrument name across scopes within each process whenever
the instrument is exposed by compatibility scraping. OTLP can distinguish
same-named instruments by scope; a flat scrape without scope labels cannot.

The SDK-native Git client fixes two former library accounting errors:
successful unary replies increment received messages (failed replies do not),
and client-streaming success completes on its final response. Repeated EOF
cannot double-count terminal handling. Method/service/type/status attributes
remain fixed vocabularies; unknown methods become `unknown`.

## Runtime inventory

Go runtime observations belong to the shared Go provider's `gitstore/runtime`
scope. Native Linux process observations belong to the native provider's
`gitstore/runtime` scope. No third-party Prometheus collector is bridged or
separately scraped. The following table describes the Go sources.

| Previous signal → SDK instrument | Kind / aggregation | Unit | Attributes |
| --- | --- | --- | --- |
| `go_goroutines`, `go_threads`, `go_sched_gomaxprocs_threads`, `go_gc_gogc_percent`, `go_gc_gomemlimit_bytes` (same) | Int64ObservableGauge | count, percent, bytes as named | — |
| `go_info` (same) | Int64ObservableGauge | 1 | version |
| `go_gc_duration_seconds` summary → same-named quantile gauge | Float64ObservableGauge | s | quantile: 0, .25, .5, .75, 1 |
| GC summary count → `go_gc_duration_seconds_count` | Int64ObservableCounter / monotonic sum | count | — |
| GC summary sum → `go_gc_duration_seconds_sum` | Float64ObservableCounter / monotonic sum | s | — |
| `go_memstats_alloc_bytes_total`, `go_memstats_mallocs_total`, `go_memstats_frees_total` (same) | Float64ObservableCounter / monotonic sum | bytes or count as named | — |
| Remaining `go_memstats_*` below (same) | Float64ObservableGauge | bytes, count, timestamp as named | — |
| `process_cpu_seconds_total` (same) | Float64ObservableCounter / monotonic sum | s | — |
| `process_network_receive_bytes_total`, `process_network_transmit_bytes_total` (same, Linux) | Float64ObservableCounter / monotonic sum | By | — |
| `process_resident_memory_bytes`, `process_virtual_memory_bytes`, `process_virtual_memory_max_bytes` (same) | Float64ObservableGauge | By | — |
| `process_start_time_seconds` (same) | Float64ObservableGauge | s | — |
| `process_open_fds`, `process_max_fds` (same) | Float64ObservableGauge | {file} | — |

Remaining memory suffixes: `alloc_bytes`, `sys_bytes`, `heap_alloc_bytes`,
`heap_sys_bytes`, `heap_idle_bytes`, `heap_inuse_bytes`,
`heap_released_bytes`, `heap_objects`, `stack_inuse_bytes`,
`stack_sys_bytes`, `mspan_inuse_bytes`, `mspan_sys_bytes`,
`mcache_inuse_bytes`, `mcache_sys_bytes`, `buck_hash_sys_bytes`,
`gc_sys_bytes`, `other_sys_bytes`, `next_gc_bytes`,
`last_gc_time_seconds`. Memory/GC snapshots are cached for one second;
current goroutine/thread and runtime-limit observations remain live.

Process sources use procfs on Linux, rusage/sysctl/Mach on macOS, and Windows
process APIs on Windows. Windows file counts represent handles and its fixed
system handle limit. macOS RSS/virtual memory require CGO, matching the former
collector's platform support. Unsupported platforms retain portable Go runtime
signals. No current memory reading is replaced with a historical peak.
Native Git preserves the former Rust default registry's Linux-only process
contract: `process_cpu_seconds_total` is a u64 observable monotonic counter;
`process_open_fds`, `process_max_fds`, `process_virtual_memory_bytes`,
`process_resident_memory_bytes`, `process_start_time_seconds` and
`process_threads` are u64 observable gauges. Units are seconds, `{file}`,
`By` and `{thread}` as named, with no attributes. CPU and start timestamps
retain the former collector's whole-second resolution. Native OS snapshots
are cached for one second, descriptor enumeration has a two-second deadline,
and failed/overdue observations are omitted rather than fabricated. macOS and
Windows native Git did not previously export these process signals.
GC summary conversion is intentionally a schema change to gauges plus two
counters: there is no SDK summary instrument and no replay of synthetic
histogram samples.

## Bounds and verification

Both SDKs collect/export every ten seconds, use cumulative temporality, limit
attribute cardinality to 2,048 per instrument, disable metric-export retries,
and limit requests to 2 MiB. HTTP transport/export deadlines are two seconds,
metric replies are capped at 64 KiB, and rejected or malformed responses fail
snapshot accounting. Recording never waits for network export. Shutdown owns
the provider and bounded final export. Collector memory, batching, queue and
retry bounds remain in the existing observability configuration.

Tests independently read SDK collection and exposition, verify cumulative
histogram count/sum/buckets and exemplars, exclude the default Prometheus
registry, compare legacy runtime signal names, and exercise rejected,
oversized and partial responses. Existing opt-in collector smoke tests cover
Go service identities and native Git metrics. A successful HTTP export is not
backend delivery: query the configured Prometheus backend independently,
including service/process attribution. Process replacement and sustained
capacity/recovery acceptance require their existing deployed runners; unit
fixtures and collector smoke do not establish those guarantees.
