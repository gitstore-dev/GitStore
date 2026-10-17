// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Library
// Structured logging setup using tracing

// async_trait-generated trait methods return a pinned boxed Future, which
// Clippy's newer double_must_use already treats as #[must_use]; async_trait
// also marks the method #[must_use] with no message, tripping the lint on
// every async_trait trait definition in this crate (hand-written and
// buf-generated alike). Allowed crate-wide rather than per module/file,
// since new inclusions of the generated proto code keep adding occurrences.
#![allow(clippy::double_must_use)]

pub mod auth;
pub mod config;
pub mod git;
pub mod grpc;

use opentelemetry::trace::TracerProvider;
use opentelemetry_otlp::WithExportConfig;
use opentelemetry_sdk::{logs::SdkLoggerProvider, trace::SdkTracerProvider, Resource};
use std::{
    sync::atomic::{AtomicBool, Ordering},
    time::Duration,
};
use tracing_opentelemetry::OpenTelemetrySpanExt;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt, EnvFilter, Layer};

static TELEMETRY_ENABLED: AtomicBool = AtomicBool::new(false);

fn telemetry_records(signal: &str, stage: &str, count: u64) {
    static RECORDS: std::sync::OnceLock<prometheus::IntCounterVec> = std::sync::OnceLock::new();
    RECORDS
        .get_or_init(|| {
            prometheus::register_int_counter_vec!(
                "gitstore_telemetry_records_total",
                "Telemetry records admitted, exported or failed; live differences include pending work and loss",
                &["signal", "stage"]
            )
            .expect("register telemetry record counters")
        })
        .with_label_values(&[signal, stage])
        .inc_by(count);
}

#[derive(Debug)]
struct TelemetryObserver;

impl opentelemetry_sdk::trace::SpanProcessor for TelemetryObserver {
    fn on_start(&self, _: &mut opentelemetry_sdk::trace::Span, _: &opentelemetry::Context) {}
    fn on_end(&self, _: opentelemetry_sdk::trace::SpanData) {
        telemetry_records("spans", "admitted", 1);
    }
    fn force_flush(&self) -> opentelemetry_sdk::error::OTelSdkResult {
        Ok(())
    }
    fn shutdown_with_timeout(&self, _: Duration) -> opentelemetry_sdk::error::OTelSdkResult {
        Ok(())
    }
}

impl opentelemetry_sdk::logs::LogProcessor for TelemetryObserver {
    fn emit(
        &self,
        record: &mut opentelemetry_sdk::logs::SdkLogRecord,
        _: &opentelemetry::InstrumentationScope,
    ) {
        use opentelemetry::logs::{AnyValue, LogRecord};
        telemetry_records("logs", "admitted", 1);
        if let Some(AnyValue::String(body)) = record.body() {
            let body = body.as_str();
            if body.len() > 4096 {
                let mut end = 4096;
                while !body.is_char_boundary(end) {
                    end -= 1;
                }
                record.set_body(AnyValue::from(body[..end].to_owned()));
                telemetry_records("logs", "truncated", 1);
            }
        }
    }
    fn force_flush(&self) -> opentelemetry_sdk::error::OTelSdkResult {
        Ok(())
    }
    fn shutdown_with_timeout(&self, _: Duration) -> opentelemetry_sdk::error::OTelSdkResult {
        Ok(())
    }
}

#[derive(Debug)]
struct TelemetryTraceExporter<E>(E);

impl<E: opentelemetry_sdk::trace::SpanExporter> opentelemetry_sdk::trace::SpanExporter
    for TelemetryTraceExporter<E>
{
    async fn export(
        &self,
        batch: Vec<opentelemetry_sdk::trace::SpanData>,
    ) -> opentelemetry_sdk::error::OTelSdkResult {
        let count = batch.len() as u64;
        let result = self.0.export(batch).await;
        telemetry_records(
            "spans",
            if result.is_ok() { "exported" } else { "failed" },
            count,
        );
        if let Err(ref error) = result {
            tracing::error!(target: "opentelemetry.gitstore.export", %error, "trace export failed");
        }
        result
    }
    fn shutdown_with_timeout(&self, timeout: Duration) -> opentelemetry_sdk::error::OTelSdkResult {
        self.0.shutdown_with_timeout(timeout)
    }
    fn force_flush(&self) -> opentelemetry_sdk::error::OTelSdkResult {
        self.0.force_flush()
    }
    fn set_resource(&mut self, resource: &Resource) {
        self.0.set_resource(resource);
    }
}

#[derive(Debug)]
struct TelemetryLogExporter<E>(E);

impl<E: opentelemetry_sdk::logs::LogExporter> opentelemetry_sdk::logs::LogExporter
    for TelemetryLogExporter<E>
{
    async fn export(
        &self,
        batch: opentelemetry_sdk::logs::LogBatch<'_>,
    ) -> opentelemetry_sdk::error::OTelSdkResult {
        let count = batch.iter().count() as u64;
        let result = self.0.export(batch).await;
        telemetry_records(
            "logs",
            if result.is_ok() { "exported" } else { "failed" },
            count,
        );
        if let Err(ref error) = result {
            tracing::error!(target: "opentelemetry.gitstore.export", %error, "log export failed");
        }
        result
    }
    fn shutdown_with_timeout(&self, timeout: Duration) -> opentelemetry_sdk::error::OTelSdkResult {
        self.0.shutdown_with_timeout(timeout)
    }
    fn event_enabled(
        &self,
        level: opentelemetry::logs::Severity,
        target: &str,
        name: Option<&str>,
    ) -> bool {
        self.0.event_enabled(level, target, name)
    }
    fn set_resource(&mut self, resource: &Resource) {
        self.0.set_resource(resource);
    }
}

pub struct Telemetry {
    traces: Option<SdkTracerProvider>,
    logs: Option<SdkLoggerProvider>,
}

impl Telemetry {
    pub fn shutdown(&self) -> anyhow::Result<()> {
        TELEMETRY_ENABLED.store(false, Ordering::Relaxed);
        let trace_result = self.traces.as_ref().map(|p| p.shutdown());
        let log_result = self.logs.as_ref().map(|p| p.shutdown());
        if let Some(Err(error)) = trace_result {
            return Err(anyhow::anyhow!(
                "trace shutdown failed: {error}; log shutdown: {log_result:?}"
            ));
        }
        if let Some(Err(error)) = log_result {
            return Err(anyhow::anyhow!("log shutdown failed: {error}"));
        }
        Ok(())
    }
}

fn telemetry_resource() -> anyhow::Result<Resource> {
    static INSTANCE_ID: std::sync::OnceLock<String> = std::sync::OnceLock::new();
    let instance_id = if let Some(id) = INSTANCE_ID.get() {
        id
    } else {
        let mut random = [0u8; 16];
        getrandom::fill(&mut random)
            .map_err(|error| anyhow::anyhow!("generate telemetry process identity: {error}"))?;
        let generated = random.iter().map(|byte| format!("{byte:02x}")).collect();
        INSTANCE_ID.get_or_init(|| generated)
    };
    Ok(Resource::builder_empty()
        .with_attributes([
            opentelemetry::KeyValue::new("service.name", "gitstore-git-service"),
            opentelemetry::KeyValue::new("service.version", env!("CARGO_PKG_VERSION")),
            opentelemetry::KeyValue::new("service.instance.id", instance_id.clone()),
        ])
        .build())
}

pub fn init_observability(
    log: &config::LogConfig,
    cfg: &config::OtlpConfig,
) -> anyhow::Result<Telemetry> {
    if cfg.uri.is_empty() {
        init_logging(&log.level, &log.format).map_err(|e| anyhow::anyhow!("{e}"))?;
        return Ok(Telemetry {
            traces: None,
            logs: None,
        });
    }
    let endpoint = cfg.uri.trim_end_matches('/');
    let resource = telemetry_resource()?;
    let trace_exporter = opentelemetry_otlp::SpanExporter::builder()
        .with_http()
        .with_protocol(opentelemetry_otlp::Protocol::HttpBinary)
        .with_endpoint(format!("{endpoint}/v1/traces"))
        .with_timeout(Duration::from_secs(2))
        .build()?;
    let log_exporter = opentelemetry_otlp::LogExporter::builder()
        .with_http()
        .with_protocol(opentelemetry_otlp::Protocol::HttpBinary)
        .with_endpoint(format!("{endpoint}/v1/logs"))
        .with_timeout(Duration::from_secs(2))
        .build()?;
    let traces = SdkTracerProvider::builder()
        .with_resource(resource.clone())
        .with_span_processor(TelemetryObserver)
        .with_sampler(opentelemetry_sdk::trace::Sampler::ParentBased(Box::new(
            opentelemetry_sdk::trace::Sampler::TraceIdRatioBased(cfg.trace_sample_ratio),
        )))
        .with_span_limits(opentelemetry_sdk::trace::SpanLimits {
            max_events_per_span: 32,
            max_attributes_per_span: 32,
            max_links_per_span: 8,
            max_attributes_per_event: 8,
            max_attributes_per_link: 8,
        })
        .with_span_processor(
            opentelemetry_sdk::trace::BatchSpanProcessor::builder(TelemetryTraceExporter(
                trace_exporter,
            ))
            .with_batch_config(
                opentelemetry_sdk::trace::BatchConfigBuilder::default()
                    .with_max_queue_size(2048)
                    .with_max_export_batch_size(256)
                    .with_scheduled_delay(Duration::from_secs(1))
                    .build(),
            )
            .build(),
        )
        .build();
    let logs = SdkLoggerProvider::builder()
        .with_resource(resource)
        .with_log_processor(TelemetryObserver)
        .with_log_processor(
            opentelemetry_sdk::logs::BatchLogProcessor::builder(TelemetryLogExporter(log_exporter))
                .with_batch_config(
                    opentelemetry_sdk::logs::BatchConfigBuilder::default()
                        .with_max_queue_size(2048)
                        .with_max_export_batch_size(256)
                        .with_scheduled_delay(Duration::from_secs(1))
                        .build(),
                )
                .build(),
        )
        .build();
    let trace_layer = tracing_opentelemetry::layer().with_tracer(traces.tracer("gitstore"));
    let log_layer = opentelemetry_appender_tracing::layer::OpenTelemetryTracingBridge::new(&logs)
        .with_filter(tracing_subscriber::filter::filter_fn(|metadata| {
            !metadata.target().starts_with("opentelemetry")
        }));
    let filter =
        EnvFilter::try_from_default_env().unwrap_or_else(|_| configured_filter(&log.level));
    match log.format.as_str() {
        "json" => tracing_subscriber::registry()
            .with(filter)
            .with(trace_layer)
            .with(log_layer)
            .with(tracing_subscriber::fmt::layer().json())
            .try_init()?,
        "text" => tracing_subscriber::registry()
            .with(filter)
            .with(trace_layer)
            .with(log_layer)
            .with(tracing_subscriber::fmt::layer())
            .try_init()?,
        _ => anyhow::bail!("invalid log format"),
    }
    TELEMETRY_ENABLED.store(true, Ordering::Relaxed);
    Ok(Telemetry {
        traces: Some(traces),
        logs: Some(logs),
    })
}

pub fn rpc_span(headers: &tonic::codegen::http::HeaderMap, path: &str) -> tracing::Span {
    if !TELEMETRY_ENABLED.load(Ordering::Relaxed) {
        return tracing::Span::none();
    }
    let method = grpc::metrics::rpc_method(path);
    struct Headers<'a>(&'a tonic::codegen::http::HeaderMap);
    impl opentelemetry::propagation::Extractor for Headers<'_> {
        fn get(&self, key: &str) -> Option<&str> {
            if key != "traceparent" && key != "tracestate" {
                return None;
            }
            self.0
                .get(key)
                .and_then(|v| v.to_str().ok())
                .filter(|v| v.len() <= 256)
        }
        fn keys(&self) -> Vec<&str> {
            vec!["traceparent", "tracestate"]
        }
    }
    use opentelemetry::propagation::TextMapPropagator;
    let parent =
        opentelemetry_sdk::propagation::TraceContextPropagator::new().extract(&Headers(headers));
    let span = tracing::info_span!("git.rpc", otel.kind = "server", rpc.method = method);
    if span.is_disabled() {
        return span;
    }
    if let Err(error) = span.set_parent(parent) {
        tracing::warn!(%error, "trace parent could not be attached");
    }
    span
}

pub fn inject_trace(metadata: &mut tonic::metadata::MetadataMap) {
    if !TELEMETRY_ENABLED.load(Ordering::Relaxed) {
        return;
    }
    struct Metadata<'a>(&'a mut tonic::metadata::MetadataMap);
    impl opentelemetry::propagation::Injector for Metadata<'_> {
        fn set(&mut self, key: &str, value: String) {
            if !matches!(key, "traceparent" | "tracestate") || value.len() > 256 {
                return;
            }
            if let (Ok(key), Ok(value)) = (
                key.parse::<tonic::metadata::MetadataKey<tonic::metadata::Ascii>>(),
                value.parse(),
            ) {
                self.0.insert(key, value);
            }
        }
    }
    use opentelemetry::propagation::TextMapPropagator;
    opentelemetry_sdk::propagation::TraceContextPropagator::new()
        .inject_context(&tracing::Span::current().context(), &mut Metadata(metadata));
}

pub fn work_span(name: &'static str) -> tracing::Span {
    if TELEMETRY_ENABLED.load(Ordering::Relaxed) {
        tracing::info_span!("git.work", otel.name = name)
    } else {
        tracing::Span::none()
    }
}

pub fn spawn_blocking<F, R>(work: F) -> tokio::task::JoinHandle<R>
where
    F: FnOnce() -> R + Send + 'static,
    R: Send + 'static,
{
    let parent = tracing::Span::current();
    let mut task = grpc::metrics::BlockingTask::queued();
    tokio::task::spawn_blocking(move || {
        parent.in_scope(|| {
            task.start();
            let span = work_span("git.blocking");
            let result = span.in_scope(work);
            drop(task);
            result
        })
    })
}

/// Initialize structured logging with configured defaults and optional RUST_LOG filtering.
pub fn init_logging(
    log_level: &str,
    log_format: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let default_filter = configured_filter(log_level);
    let filter = EnvFilter::try_from_default_env().unwrap_or(default_filter);

    match log_format.to_ascii_lowercase().as_str() {
        "json" => tracing_subscriber::registry()
            .with(filter)
            .with(tracing_subscriber::fmt::layer().json())
            .try_init()
            .or_else(ignore_already_initialized),
        "text" => tracing_subscriber::registry()
            .with(filter)
            .with(tracing_subscriber::fmt::layer())
            .try_init()
            .or_else(ignore_already_initialized),
        _ => Err(format!("invalid log format {log_format:?}; valid values: json, text").into()),
    }
}

fn configured_filter(log_level: &str) -> EnvFilter {
    EnvFilter::try_new(log_level).unwrap_or_else(|_| EnvFilter::new("info"))
}

fn ignore_already_initialized(
    err: tracing_subscriber::util::TryInitError,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    if err
        .to_string()
        .contains("global default trace dispatcher has already been set")
    {
        Ok(())
    } else {
        Err(Box::new(err))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn resource_attribute(resource: &Resource, key: &str) -> String {
        resource
            .iter()
            .find(|(name, _)| name.as_str() == key)
            .map(|(_, value)| value.to_string())
            .unwrap_or_else(|| panic!("missing telemetry resource attribute {key}"))
    }

    #[test]
    fn telemetry_resource_identity_is_process_scoped() {
        const CHILD: &str = "GITSTORE_TEST_TELEMETRY_IDENTITY_CHILD";
        if std::env::var(CHILD).ok().as_deref() == Some("1") {
            let workers: Vec<_> = (0..8)
                .map(|_| {
                    std::thread::spawn(|| {
                        let resource = telemetry_resource().unwrap();
                        assert_eq!(
                            resource_attribute(&resource, "service.name"),
                            "gitstore-git-service"
                        );
                        assert_eq!(
                            resource_attribute(&resource, "service.version"),
                            env!("CARGO_PKG_VERSION")
                        );
                        resource_attribute(&resource, "service.instance.id")
                    })
                })
                .collect();
            let identities: Vec<_> = workers
                .into_iter()
                .map(|worker| worker.join().unwrap())
                .collect();
            assert!(identities.iter().all(|id| id == &identities[0]));
            println!("TELEMETRY_INSTANCE_ID={}", identities[0]);
            return;
        }
        let identities: Vec<_> = (0..2)
            .map(|_| {
                let output = std::process::Command::new(std::env::current_exe().unwrap())
                    .args([
                        "--exact",
                        "tests::telemetry_resource_identity_is_process_scoped",
                        "--nocapture",
                    ])
                    .env(CHILD, "1")
                    .env("HOSTNAME", "reused-telemetry-contract-host")
                    .output()
                    .unwrap();
                assert!(
                    output.status.success(),
                    "isolated telemetry identity failed: {}",
                    String::from_utf8_lossy(&output.stderr)
                );
                let stdout = String::from_utf8(output.stdout).unwrap();
                let ids: Vec<_> = stdout
                    .lines()
                    .filter_map(|line| line.strip_prefix("TELEMETRY_INSTANCE_ID="))
                    .collect();
                assert_eq!(ids.len(), 1, "child identity marker must occur once");
                let id = ids[0].to_owned();
                assert_eq!(id.len(), 32);
                assert!(id
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte)));
                id
            })
            .collect();
        assert_ne!(
            identities[0], identities[1],
            "fresh processes sharing a hostname must not share telemetry identity"
        );
    }

    fn telemetry_count(signal: &str, stage: &str) -> u64 {
        prometheus::gather()
            .iter()
            .filter(|family| family.name() == "gitstore_telemetry_records_total")
            .flat_map(|family| family.get_metric())
            .find(|metric| {
                metric
                    .get_label()
                    .iter()
                    .any(|label| label.name() == "signal" && label.value() == signal)
                    && metric
                        .get_label()
                        .iter()
                        .any(|label| label.name() == "stage" && label.value() == stage)
            })
            .map(|metric| metric.get_counter().value() as u64)
            .unwrap_or(0)
    }

    #[derive(Debug)]
    struct TestTelemetryExporter(bool);

    impl TestTelemetryExporter {
        fn result(&self) -> opentelemetry_sdk::error::OTelSdkResult {
            if self.0 {
                Err(opentelemetry_sdk::error::OTelSdkError::InternalFailure(
                    "contract export failure".to_owned(),
                ))
            } else {
                Ok(())
            }
        }
    }

    impl opentelemetry_sdk::trace::SpanExporter for TestTelemetryExporter {
        async fn export(
            &self,
            _: Vec<opentelemetry_sdk::trace::SpanData>,
        ) -> opentelemetry_sdk::error::OTelSdkResult {
            self.result()
        }
    }

    impl opentelemetry_sdk::logs::LogExporter for TestTelemetryExporter {
        async fn export(
            &self,
            _: opentelemetry_sdk::logs::LogBatch<'_>,
        ) -> opentelemetry_sdk::error::OTelSdkResult {
            self.result()
        }
    }

    #[test]
    fn telemetry_accounting_tracks_success_failure_and_utf8_body_bounds() {
        const CHILD: &str = "GITSTORE_TEST_TELEMETRY_ACCOUNTING_CHILD";
        if std::env::var(CHILD).ok().as_deref() != Some("1") {
            let status = std::process::Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "tests::telemetry_accounting_tracks_success_failure_and_utf8_body_bounds",
                    "--nocapture",
                ])
                .env(CHILD, "1")
                .status()
                .unwrap();
            assert!(status.success(), "isolated telemetry accounting failed");
            return;
        }
        use opentelemetry::{
            logs::{AnyValue, LogRecord, Logger, LoggerProvider},
            trace::{Span, Tracer},
        };
        for failed in [false, true] {
            let traces = SdkTracerProvider::builder()
                .with_span_processor(TelemetryObserver)
                .with_simple_exporter(TelemetryTraceExporter(TestTelemetryExporter(failed)))
                .build();
            traces.tracer("contract").start("accounted").end();
            traces.shutdown().unwrap();
            let logs = SdkLoggerProvider::builder()
                .with_log_processor(TelemetryObserver)
                .with_simple_exporter(TelemetryLogExporter(TestTelemetryExporter(failed)))
                .build();
            let logger = logs.logger("contract");
            logger.emit(logger.create_log_record());
            logs.shutdown().unwrap();
        }
        for signal in ["logs", "spans"] {
            assert_eq!(telemetry_count(signal, "admitted"), 2);
            assert_eq!(telemetry_count(signal, "exported"), 1);
            assert_eq!(telemetry_count(signal, "failed"), 1);
        }
        let logs = SdkLoggerProvider::builder().build();
        let logger = logs.logger("body-contract");
        let scope = opentelemetry::InstrumentationScope::builder("body-contract").build();
        for (body, expected) in [
            ("short".to_owned(), "short".to_owned()),
            ("x".repeat(4096), "x".repeat(4096)),
            ("x".repeat(5000), "x".repeat(4096)),
            (format!("{}€extra", "x".repeat(4095)), "x".repeat(4095)),
            ("€".repeat(2000), "€".repeat(1365)),
        ] {
            let mut record = logger.create_log_record();
            record.set_body(AnyValue::from(body));
            record.add_attribute("phase", "preserved");
            opentelemetry_sdk::logs::LogProcessor::emit(&TelemetryObserver, &mut record, &scope);
            assert_eq!(record.body(), Some(&AnyValue::from(expected)));
            assert_eq!(record.attributes_iter().count(), 1);
        }
        assert_eq!(telemetry_count("logs", "truncated"), 3);
        logs.shutdown().unwrap();
    }

    #[test]
    fn telemetry_preserves_signed_context_across_blocking_work() {
        const CHILD: &str = "GITSTORE_TEST_TELEMETRY_CHILD";
        if std::env::var(CHILD).ok().as_deref() != Some("1") {
            let status = std::process::Command::new(std::env::current_exe().unwrap())
                .args([
                    "--exact",
                    "tests::telemetry_preserves_signed_context_across_blocking_work",
                    "--nocapture",
                ])
                .env(CHILD, "1")
                .status()
                .unwrap();
            assert!(status.success(), "isolated telemetry contract failed");
            return;
        }
        let runtime = tokio::runtime::Builder::new_multi_thread()
            .worker_threads(2)
            .enable_all()
            .build()
            .unwrap();
        runtime.block_on(async {
            let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
            let address = listener.local_addr().unwrap();
            let traces = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
            let logs = std::sync::Arc::new(std::sync::atomic::AtomicUsize::new(0));
            let trace_count = traces.clone();
            let log_count = logs.clone();
            let trace_batches = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
            let captured_traces = trace_batches.clone();
            let log_batches = std::sync::Arc::new(std::sync::Mutex::new(Vec::new()));
            let captured_logs = log_batches.clone();
            let app = axum::Router::new()
                .route(
                    "/v1/traces",
                    axum::routing::post(move |body: axum::body::Bytes| {
                        let count = trace_count.clone();
                        let captured = captured_traces.clone();
                        async move {
                            assert!(body.len() <= 256 * 1024);
                            captured.lock().unwrap().push(body.to_vec());
                            count.fetch_add(body.len(), Ordering::Relaxed);
                            axum::http::StatusCode::OK
                        }
                    }),
                )
                .route(
                    "/v1/logs",
                    axum::routing::post(move |body: axum::body::Bytes| {
                        let count = log_count.clone();
                        let captured = captured_logs.clone();
                        async move {
                            assert!(body.len() <= 256 * 1024);
                            captured.lock().unwrap().push(body.to_vec());
                            count.fetch_add(body.len(), Ordering::Relaxed);
                            axum::http::StatusCode::OK
                        }
                    }),
                );
            let server = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
            let telemetry = init_observability(
                &config::LogConfig {
                    level: "info".to_string(),
                    format: "json".to_string(),
                },
                &config::OtlpConfig {
                    uri: format!("http://{address}"),
                    trace_sample_ratio: 1.0,
                },
            )
            .unwrap();
            let mut headers = tonic::codegen::http::HeaderMap::new();
            headers.insert(
                "traceparent",
                "00-b71bb94adb59498d8a3a6f1079a3fdaa-893c48c0a0b61a2e-01"
                    .parse()
                    .unwrap(),
            );
            let span = rpc_span(&headers, "/gitstore.git.v1.GitService/ReceivePack");
            use opentelemetry::trace::TraceContextExt;
            let rpc_context = span.context().span().span_context().clone();
            let blocking_span_id = std::sync::Arc::new(std::sync::Mutex::new(None));
            let captured_span_id = blocking_span_id.clone();
            use tracing::Instrument;
            async {
                let request = spawn_blocking(move || {
                    *captured_span_id.lock().unwrap() = Some(
                        tracing::Span::current()
                            .context()
                            .span()
                            .span_context()
                            .span_id(),
                    );
                    tracing::info!("blocking telemetry correlation contract");
                    let mut metadata = tonic::metadata::MetadataMap::new();
                    inject_trace(&mut metadata);
                    assert!(metadata
                        .get("traceparent")
                        .unwrap()
                        .to_str()
                        .unwrap()
                        .contains("b71bb94adb59498d8a3a6f1079a3fdaa"));
                    auth::interceptor::sign_request(
                        grpc::server::proto::CreateRepositoryRequest::default(),
                        "contract-signing-secret",
                        "gitstore-git",
                        "/gitstore.git.v1.GitService/CreateRepository",
                    )
                    .unwrap()
                })
                .await
                .unwrap();
                assert!(request
                    .metadata()
                    .contains_key(auth::interceptor::SIGNATURE_HEADER));
                assert!(request
                    .metadata()
                    .get("traceparent")
                    .unwrap()
                    .to_str()
                    .unwrap()
                    .contains("b71bb94adb59498d8a3a6f1079a3fdaa"));
                tracing::info!("bounded telemetry contract log");
            }
            .instrument(span)
            .await;
            tokio::task::spawn_blocking(move || telemetry.shutdown())
                .await
                .unwrap()
                .unwrap();
            assert!(traces.load(Ordering::Relaxed) > 0);
            assert!(logs.load(Ordering::Relaxed) > 0);
            let trace_batches = trace_batches.lock().unwrap();
            let log_batches = log_batches.lock().unwrap();
            let instance_key = b"service.instance.id";
            for batches in [&*trace_batches, &*log_batches] {
                assert!(
                    batches.iter().any(|batch| batch
                        .windows(instance_key.len())
                        .any(|bytes| bytes == instance_key)),
                    "exported signal has no service.instance.id resource attribute"
                );
            }
            let instance_id =
                resource_attribute(&telemetry_resource().unwrap(), "service.instance.id");
            assert_eq!(instance_id.len(), 32);
            let mut attribute = vec![0x0a, instance_key.len() as u8];
            attribute.extend_from_slice(instance_key);
            attribute.extend_from_slice(&[
                0x12,
                instance_id.len() as u8 + 2,
                0x0a,
                instance_id.len() as u8,
            ]);
            attribute.extend_from_slice(instance_id.as_bytes());
            for batches in [&*trace_batches, &*log_batches] {
                assert!(
                    batches.iter().any(|batch| batch
                        .windows(attribute.len())
                        .any(|bytes| bytes == attribute)),
                    "exported trace and log resources must share the process identity"
                );
            }
            let has_native_id = |tag, id: &[u8]| {
                let mut field = vec![tag, id.len() as u8];
                field.extend_from_slice(id);
                log_batches
                    .iter()
                    .any(|batch| batch.windows(field.len()).any(|bytes| bytes == field))
            };
            assert!(has_native_id(0x4a, &rpc_context.trace_id().to_bytes()));
            assert!(has_native_id(0x52, &rpc_context.span_id().to_bytes()));
            assert!(has_native_id(
                0x52,
                &blocking_span_id.lock().unwrap().unwrap().to_bytes()
            ));
            drop(trace_batches);
            drop(log_batches);
            for signal in ["logs", "spans"] {
                assert!(telemetry_count(signal, "admitted") > 0);
                assert_eq!(
                    telemetry_count(signal, "admitted"),
                    telemetry_count(signal, "exported")
                );
                assert_eq!(telemetry_count(signal, "failed"), 0);
            }
            server.abort();
        });
    }

    #[test]
    fn test_json_logging_initialization() {
        init_logging("info", "json").expect("json logging should initialize");
    }

    #[test]
    fn test_text_logging_initialization() {
        init_logging("debug", "text").expect("text logging should initialize");
    }

    #[test]
    fn configured_filter_does_not_promote_gitstore_targets() {
        assert_eq!(configured_filter("info").to_string(), "info");
        assert_eq!(configured_filter("warn").to_string(), "warn");
    }

    #[test]
    fn configured_filter_falls_back_to_info_for_invalid_levels() {
        assert_eq!(configured_filter("gitstore[invalid").to_string(), "info");
    }
}
