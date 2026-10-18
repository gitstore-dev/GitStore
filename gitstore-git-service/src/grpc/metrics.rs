// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Per-RPC Prometheus metrics for the gRPC server.
// Uses a Tower middleware layer that intercepts each RPC and records
// grpc_server_handled_total and grpc_server_handling_seconds.

use opentelemetry::trace::{SpanContext, TraceContextExt};
use prometheus::{register_histogram, register_int_gauge, Histogram, IntGauge};
use prometheus::{
    register_histogram_vec, register_int_counter_vec, HistogramTimer, HistogramVec, IntCounterVec,
};
use std::collections::BTreeMap;
use std::sync::{Mutex, OnceLock};
use std::{
    future::Future,
    pin::Pin,
    task::{Context, Poll},
    time::{Instant, SystemTime, UNIX_EPOCH},
};
use tonic::codegen::{http, Service};
use tracing_opentelemetry::OpenTelemetrySpanExt;

const RPC_LATENCY_BUCKETS: [f64; 12] = [
    0.001,
    0.005,
    0.01,
    0.025,
    0.05,
    0.1,
    0.25,
    0.5,
    1.0,
    2.5,
    5.0,
    f64::INFINITY,
];
const OPEN_METRICS_CONTENT_TYPE: &str =
    "application/openmetrics-text; version=1.0.0; charset=utf-8";
const METRICS_MAX_BYTES: usize = 1024 * 1024;

#[derive(Clone)]
struct RpcExemplar {
    context: SpanContext,
    value: f64,
    timestamp: f64,
}

#[derive(Default)]
struct RpcExemplars(Mutex<BTreeMap<(&'static str, usize), RpcExemplar>>);

impl RpcExemplars {
    fn observe(&self, method: &str, value: f64, context: &SpanContext) -> std::io::Result<()> {
        if !context.is_valid() || !context.is_sampled() || !value.is_finite() || value < 0.0 {
            return Ok(());
        }
        let bucket = RPC_LATENCY_BUCKETS
            .iter()
            .position(|upper| value <= *upper)
            .expect("finite RPC duration fits infinity bucket");
        let timestamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_err(std::io::Error::other)?
            .as_secs_f64();
        self.0
            .lock()
            .map_err(|_| std::io::Error::other("RPC exemplar storage poisoned"))?
            .insert(
                (rpc_method(method), bucket),
                RpcExemplar {
                    context: context.clone(),
                    value,
                    timestamp,
                },
            );
        Ok(())
    }

    fn snapshot(&self) -> std::io::Result<BTreeMap<String, RpcExemplar>> {
        let exemplars = self
            .0
            .lock()
            .map_err(|_| std::io::Error::other("RPC exemplar storage poisoned"))?;
        Ok(exemplars
            .iter()
            .map(|((method, bucket), exemplar)| {
                let upper = RPC_LATENCY_BUCKETS[*bucket];
                let bound = if upper.is_infinite() {
                    "+Inf".to_owned()
                } else {
                    upper.to_string()
                };
                (
                    format!(
                        "grpc_server_handling_seconds_bucket{{grpc_method=\"{method}\",le=\"{bound}\"}} "
                    ),
                    exemplar.clone(),
                )
            })
            .collect())
    }
}

fn rpc_exemplars() -> &'static RpcExemplars {
    static EXEMPLARS: OnceLock<RpcExemplars> = OnceLock::new();
    EXEMPLARS.get_or_init(RpcExemplars::default)
}

fn wants_open_metrics(headers: &http::HeaderMap) -> bool {
    let mut open_quality = 0.0_f32;
    let mut text_quality = 0.0_f32;
    for accept in headers.get_all(http::header::ACCEPT) {
        let Ok(accept) = accept.to_str() else {
            continue;
        };
        for item in accept.split(',') {
            let mut parts = item.trim().split(';');
            let media = parts.next().unwrap_or("").trim();
            let mut quality = 1.0_f32;
            let mut supported_version = true;
            for parameter in parts {
                if let Some((key, value)) = parameter.trim().split_once('=') {
                    match key.trim() {
                        "q" => {
                            quality = value
                                .trim()
                                .parse::<f32>()
                                .ok()
                                .filter(|value| value.is_finite() && (0.0..=1.0).contains(value))
                                .unwrap_or(0.0);
                        }
                        "version" if media == "application/openmetrics-text" => {
                            supported_version = value.trim().trim_matches('"') == "1.0.0";
                        }
                        _ => {}
                    }
                }
            }
            match media {
                "application/openmetrics-text" if supported_version => {
                    open_quality = open_quality.max(quality);
                }
                "text/plain" => text_quality = text_quality.max(quality),
                _ => {}
            }
        }
    }
    open_quality > 0.0 && open_quality >= text_quality
}

fn encode_metrics(
    families: &[prometheus::proto::MetricFamily],
    open_metrics: bool,
    exemplars: &BTreeMap<String, RpcExemplar>,
) -> std::io::Result<Vec<u8>> {
    let encoder = prometheus::TextEncoder::new();
    let mut body = String::new();
    for family in families {
        let text = encoder
            .encode_to_string(std::slice::from_ref(family))
            .map_err(std::io::Error::other)?;
        if !open_metrics {
            body.push_str(&text);
        } else {
            let name = family.name();
            // OpenMetrics counter metadata names omit the sample's _total suffix.
            let counter_base = if family.get_field_type() == prometheus::proto::MetricType::COUNTER
            {
                Some(name.strip_suffix("_total").ok_or_else(|| {
                    std::io::Error::other("OpenMetrics counter name must end with _total")
                })?)
            } else {
                None
            };
            for line in text.lines() {
                if let Some(base) = counter_base {
                    if let Some(help) = line.strip_prefix(&format!("# HELP {name} ")) {
                        body.push_str(&format!("# HELP {base} {help}\n"));
                        continue;
                    }
                    if line == format!("# TYPE {name} counter") {
                        body.push_str(&format!("# TYPE {base} counter\n"));
                        continue;
                    }
                }
                body.push_str(line);
                if line.starts_with("grpc_server_handling_seconds_bucket{") {
                    if let Some((sample, _)) = line.rsplit_once(' ') {
                        if let Some(exemplar) = exemplars.get(&format!("{sample} ")) {
                            body.push_str(&format!(
                                " # {{trace_id=\"{}\",span_id=\"{}\"}} {} {}",
                                exemplar.context.trace_id(),
                                exemplar.context.span_id(),
                                exemplar.value,
                                exemplar.timestamp
                            ));
                        }
                    }
                }
                body.push('\n');
            }
        }
        if body.len() > METRICS_MAX_BYTES {
            return Err(std::io::Error::other("metrics encoding exceeded bound"));
        }
    }
    if open_metrics {
        body.push_str("# EOF\n");
    }
    if body.len() > METRICS_MAX_BYTES {
        return Err(std::io::Error::other("metrics encoding exceeded bound"));
    }
    Ok(body.into_bytes())
}

pub fn rpc_method(path: &str) -> &'static str {
    match path.rsplit('/').next().unwrap_or("") {
        "CreateRepository" => "CreateRepository",
        "DeleteRepository" => "DeleteRepository",
        "GetFile" => "GetFile",
        "GetFiles" => "GetFiles",
        "GetFileStream" => "GetFileStream",
        "ListFiles" => "ListFiles",
        "CommitFile" => "CommitFile",
        "DeleteFile" => "DeleteFile",
        "CreateTag" => "CreateTag",
        "ListTags" => "ListTags",
        "GetLatestTag" => "GetLatestTag",
        "InfoRefs" => "InfoRefs",
        "ReceivePack" => "ReceivePack",
        "UploadPack" => "UploadPack",
        _ => "unknown",
    }
}

struct RpcCompletion {
    method: &'static str,
    started: Instant,
    completed: bool,
    span: tracing::Span,
}

impl RpcCompletion {
    fn new(method: &'static str) -> Self {
        Self {
            method,
            started: Instant::now(),
            completed: false,
            span: tracing::Span::current(),
        }
    }
    fn finish(&mut self, code: tonic::Code) {
        if !self.completed {
            self.completed = true;
            let duration = self.started.elapsed().as_secs_f64();
            let parent = self.span.context();
            let parent_span = parent.span();
            let context = parent_span.span_context();
            record_rpc_with_context(self.method, &format!("{code:?}"), duration, context);
            if context.is_valid() && context.is_sampled() {
                self.span.in_scope(|| {
                    tracing::info!(
                        rpc.method = self.method,
                        rpc.code = ?code,
                        duration_seconds = duration,
                        "Git RPC completed"
                    );
                });
            }
        }
    }
}

impl Drop for RpcCompletion {
    fn drop(&mut self) {
        self.finish(tonic::Code::Cancelled);
    }
}

fn rpc_status(headers: &http::HeaderMap) -> Option<tonic::Code> {
    headers.get("grpc-status").map(|value| {
        value
            .to_str()
            .ok()
            .and_then(|value| value.parse::<i32>().ok())
            .map(tonic::Code::from_i32)
            .unwrap_or(tonic::Code::Unknown)
    })
}

struct RpcResponseBody {
    inner: Pin<Box<tonic::body::Body>>,
    completion: RpcCompletion,
}

impl http_body::Body for RpcResponseBody {
    type Data = <tonic::body::Body as http_body::Body>::Data;
    type Error = <tonic::body::Body as http_body::Body>::Error;

    fn poll_frame(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<http_body::Frame<Self::Data>, Self::Error>>> {
        let this = self.get_mut();
        let result = this.inner.as_mut().poll_frame(cx);
        match &result {
            Poll::Ready(Some(Ok(frame))) => {
                if let Some(trailers) = frame.trailers_ref() {
                    this.completion
                        .finish(rpc_status(trailers).unwrap_or(tonic::Code::Unknown));
                } else if this.inner.is_end_stream() {
                    this.completion.finish(tonic::Code::Unknown);
                }
            }
            Poll::Ready(Some(Err(error))) => {
                this.completion.finish(error.code());
            }
            Poll::Ready(None) => {
                this.completion.finish(tonic::Code::Unknown);
            }
            Poll::Pending => {}
        }
        result
    }
    fn is_end_stream(&self) -> bool {
        self.inner.is_end_stream()
    }
    fn size_hint(&self) -> http_body::SizeHint {
        self.inner.size_hint()
    }
}

#[derive(Clone)]
pub struct RpcMetricsService<S> {
    inner: S,
}

impl<S> RpcMetricsService<S> {
    pub fn new(inner: S) -> Self {
        Self { inner }
    }
}

impl<S: tonic::server::NamedService> tonic::server::NamedService for RpcMetricsService<S> {
    const NAME: &'static str = S::NAME;
}

impl<S, B> Service<http::Request<B>> for RpcMetricsService<S>
where
    S: Service<http::Request<B>, Response = http::Response<tonic::body::Body>>,
    S::Future: Send + 'static,
{
    type Response = http::Response<tonic::body::Body>;
    type Error = S::Error;
    type Future = Pin<Box<dyn Future<Output = Result<Self::Response, Self::Error>> + Send>>;

    fn poll_ready(&mut self, cx: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
        self.inner.poll_ready(cx)
    }
    fn call(&mut self, request: http::Request<B>) -> Self::Future {
        let mut completion = RpcCompletion::new(rpc_method(request.uri().path()));
        let future = self.inner.call(request);
        Box::pin(async move {
            // Tonic enters its request span while polling, not while constructing this future.
            let span = tracing::Span::current();
            if !span.is_disabled() {
                completion.span = span;
            }
            match future.await {
                Ok(response) => {
                    if let Some(code) = rpc_status(response.headers()) {
                        completion.finish(code);
                    } else if http_body::Body::is_end_stream(response.body()) {
                        completion.finish(tonic::Code::Unknown);
                    }
                    Ok(response.map(|body| {
                        tonic::body::Body::new(RpcResponseBody {
                            inner: Box::pin(body),
                            completion,
                        })
                    }))
                }
                Err(error) => {
                    completion.finish(tonic::Code::Unknown);
                    Err(error)
                }
            }
        })
    }
}

static BLOCKING_PENDING: OnceLock<IntGauge> = OnceLock::new();
static BLOCKING_ACTIVE: OnceLock<IntGauge> = OnceLock::new();
static BLOCKING_QUEUE: OnceLock<Histogram> = OnceLock::new();

pub struct BlockingTask {
    started: std::time::Instant,
    active: bool,
}

impl BlockingTask {
    pub fn queued() -> Self {
        blocking_pending().inc();
        Self {
            started: std::time::Instant::now(),
            active: false,
        }
    }
    pub fn start(&mut self) {
        blocking_pending().dec();
        blocking_active().inc();
        self.active = true;
        BLOCKING_QUEUE
            .get_or_init(|| {
                register_histogram!(
                    "gitstore_git_blocking_queue_seconds",
                    "Native blocking-pool queue wait",
                    vec![0.0001, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 5.0]
                )
                .expect("register blocking queue histogram")
            })
            .observe(self.started.elapsed().as_secs_f64());
    }
}
impl Drop for BlockingTask {
    fn drop(&mut self) {
        if self.active {
            blocking_active().dec();
        } else {
            blocking_pending().dec();
        }
    }
}
fn blocking_pending() -> &'static IntGauge {
    BLOCKING_PENDING.get_or_init(|| {
        register_int_gauge!(
            "gitstore_git_blocking_pending",
            "Queued owned blocking jobs"
        )
        .expect("register blocking pending gauge")
    })
}
fn blocking_active() -> &'static IntGauge {
    BLOCKING_ACTIVE.get_or_init(|| {
        register_int_gauge!(
            "gitstore_git_blocking_active",
            "Executing owned blocking jobs"
        )
        .expect("register blocking active gauge")
    })
}

pub async fn start_metrics(
    config: &crate::config::MetricsConfig,
) -> anyhow::Result<Option<tokio::task::JoinHandle<std::io::Result<()>>>> {
    let Some(address) = config.listen_address().map_err(anyhow::Error::msg)? else {
        return Ok(None);
    };
    let listener = tokio::net::TcpListener::bind(address).await?;
    let permits = std::sync::Arc::new(tokio::sync::Semaphore::new(4));
    let app = axum::Router::new().route(
        "/metrics",
        axum::routing::get(move |headers: http::HeaderMap| {
            let permits = permits.clone();
            async move {
                let Ok(_permit) = permits.try_acquire_owned() else {
                    return (
                        axum::http::StatusCode::SERVICE_UNAVAILABLE,
                        "metrics capture busy".to_string(),
                    )
                        .into_response();
                };
                use axum::response::IntoResponse;
                let runtime = tokio::runtime::Handle::current().metrics();
                static TASKS: OnceLock<IntGauge> = OnceLock::new();
                static QUEUE: OnceLock<IntGauge> = OnceLock::new();
                TASKS
                    .get_or_init(|| {
                        register_int_gauge!("gitstore_git_runtime_tasks", "Live Tokio tasks")
                            .expect("register runtime tasks")
                    })
                    .set(runtime.num_alive_tasks() as i64);
                QUEUE
                    .get_or_init(|| {
                        register_int_gauge!(
                            "gitstore_git_runtime_global_queue",
                            "Runnable Tokio global queue"
                        )
                        .expect("register runtime queue")
                    })
                    .set(runtime.global_queue_depth() as i64);
                let open_metrics = wants_open_metrics(&headers);
                // Capture exemplars before counts, so each exemplar's observation is included.
                let body = if open_metrics {
                    rpc_exemplars().snapshot().and_then(|exemplars| {
                        encode_metrics(&prometheus::gather(), true, &exemplars)
                    })
                } else {
                    encode_metrics(&prometheus::gather(), false, &BTreeMap::new())
                };
                let body = match body {
                    Ok(body) => body,
                    Err(error) => {
                        tracing::error!(%error, "metrics encoding failed");
                        return (
                            axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                            "metrics encoding failed".to_string(),
                        )
                            .into_response();
                    }
                };
                (
                    [(
                        axum::http::header::CONTENT_TYPE,
                        if open_metrics {
                            OPEN_METRICS_CONTENT_TYPE
                        } else {
                            "text/plain; version=0.0.4"
                        },
                    )],
                    body,
                )
                    .into_response()
            }
        }),
    );
    Ok(Some(tokio::spawn(async move {
        let result = axum::serve(listener, app).await;
        if let Err(ref error) = result {
            tracing::error!(%error, "metrics listener failed");
        }
        result
    })))
}

static HANDLED_TOTAL: OnceLock<IntCounterVec> = OnceLock::new();
static HANDLING_SECONDS: OnceLock<HistogramVec> = OnceLock::new();
static FILE_READ_PHASE_SECONDS: OnceLock<HistogramVec> = OnceLock::new();
static RPC_VERIFICATION_PHASE_SECONDS: OnceLock<HistogramVec> = OnceLock::new();

pub enum RPCVerificationPhase {
    CapacityWait,
    BlockingWait,
    ReplayLock,
    EnvelopeCheck,
    ReplayReserve,
    ReplayClock,
    ReplayCleanup,
    ReplayPersist,
    ReplayComplete,
    DirectoryBarrier,
    ReplayInode,
    ReplayDirectory,
    DirectoryPublish,
    DirectoryReport,
}

impl RPCVerificationPhase {
    fn label(&self) -> &'static str {
        match self {
            Self::CapacityWait => "capacity_wait",
            Self::BlockingWait => "blocking_wait",
            Self::ReplayLock => "replay_lock",
            Self::EnvelopeCheck => "envelope_check",
            Self::ReplayReserve => "replay_reserve",
            Self::ReplayClock => "replay_clock",
            Self::ReplayCleanup => "replay_cleanup",
            Self::ReplayPersist => "replay_persist",
            Self::ReplayComplete => "replay_complete",
            Self::DirectoryBarrier => "directory_barrier",
            Self::ReplayInode => "replay_inode",
            Self::ReplayDirectory => "replay_directory",
            Self::DirectoryPublish => "directory_publish",
            Self::DirectoryReport => "directory_report",
        }
    }
}

fn rpc_verification_phase_seconds() -> &'static HistogramVec {
    RPC_VERIFICATION_PHASE_SECONDS.get_or_init(|| {
        register_histogram_vec!(
            "gitstore_git_rpc_verification_phase_seconds",
            "Signed Git RPC verification phase latency in seconds",
            &["phase"],
            vec![0.0001, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0]
        )
        .expect("failed to register gitstore_git_rpc_verification_phase_seconds")
    })
}

pub fn record_rpc_verification_phase(phase: RPCVerificationPhase, duration_secs: f64) {
    rpc_verification_phase_seconds()
        .with_label_values(&[phase.label()])
        .observe(duration_secs);
}

pub fn start_rpc_verification_phase(phase: RPCVerificationPhase) -> HistogramTimer {
    rpc_verification_phase_seconds()
        .with_label_values(&[phase.label()])
        .start_timer()
}

pub enum FileReadPhase {
    Authentication,
    RepositoryLock,
    BlockingQueue,
    Payload,
}

impl FileReadPhase {
    fn label(&self) -> &'static str {
        match self {
            Self::Authentication => "authentication",
            Self::RepositoryLock => "repository_lock",
            Self::BlockingQueue => "blocking_queue",
            Self::Payload => "payload",
        }
    }

    fn trace_attribute(&self) -> &'static str {
        match self {
            Self::Authentication => "git.file_read.authentication.seconds",
            Self::RepositoryLock => "git.file_read.repository_lock.seconds",
            Self::BlockingQueue => "git.file_read.blocking_queue.seconds",
            Self::Payload => "git.file_read.payload.seconds",
        }
    }
}

pub struct FileReadPhaseTimer {
    timer: Option<HistogramTimer>,
    phase: FileReadPhase,
    span: tracing::Span,
}

impl FileReadPhaseTimer {
    pub fn observe_duration(self) {
        drop(self);
    }
}

impl Drop for FileReadPhaseTimer {
    fn drop(&mut self) {
        if let Some(timer) = self.timer.take() {
            let seconds = timer.stop_and_record();
            self.span
                .set_attribute(self.phase.trace_attribute(), seconds);
        }
    }
}

pub(super) fn file_read_phase_seconds() -> &'static HistogramVec {
    FILE_READ_PHASE_SECONDS.get_or_init(|| {
        register_histogram_vec!(
            "gitstore_git_file_read_phase_seconds",
            "GetFile server phase latency in seconds",
            &["phase"],
            RPC_LATENCY_BUCKETS[..RPC_LATENCY_BUCKETS.len() - 1].to_vec()
        )
        .expect("failed to register gitstore_git_file_read_phase_seconds")
    })
}

pub fn start_file_read_phase(phase: FileReadPhase) -> FileReadPhaseTimer {
    start_file_read_phase_in_span(phase, tracing::Span::current())
}

pub fn start_file_read_phase_in_span(
    phase: FileReadPhase,
    span: tracing::Span,
) -> FileReadPhaseTimer {
    let timer = file_read_phase_seconds()
        .with_label_values(&[phase.label()])
        .start_timer();
    FileReadPhaseTimer {
        timer: Some(timer),
        phase,
        span,
    }
}

pub fn handled_total() -> &'static IntCounterVec {
    HANDLED_TOTAL.get_or_init(|| {
        register_int_counter_vec!(
            "grpc_server_handled_total",
            "Total number of RPCs completed on the server",
            &["grpc_method", "grpc_code"]
        )
        .expect("failed to register grpc_server_handled_total")
    })
}

pub fn handling_seconds() -> &'static HistogramVec {
    HANDLING_SECONDS.get_or_init(|| {
        register_histogram_vec!(
            "grpc_server_handling_seconds",
            "Histogram of RPC response latency in seconds",
            &["grpc_method"],
            vec![0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0]
        )
        .expect("failed to register grpc_server_handling_seconds")
    })
}

/// Record a completed RPC call.
pub fn record_rpc(method: &str, code: &str, duration_secs: f64) {
    let context = tracing::Span::current().context();
    record_rpc_with_context(method, code, duration_secs, context.span().span_context());
}

fn record_rpc_with_context(method: &str, code: &str, duration_secs: f64, context: &SpanContext) {
    handled_total().with_label_values(&[method, code]).inc();
    handling_seconds()
        .with_label_values(&[method])
        .observe(duration_secs);
    if let Err(error) = rpc_exemplars().observe(method, duration_secs, context) {
        crate::telemetry_records("metrics", "failed", 1);
        tracing::error!(%error, "RPC exemplar recording failed");
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use http_body::Body;
    use prometheus::core::Collector;

    fn sampled_context() -> SpanContext {
        SpanContext::new(
            opentelemetry::trace::TraceId::from_bytes([0x71; 16]),
            opentelemetry::trace::SpanId::from_bytes([0x82; 8]),
            opentelemetry::trace::TraceFlags::SAMPLED,
            false,
            opentelemetry::trace::TraceState::default(),
        )
    }

    #[test]
    fn metrics_negotiates_open_metrics_without_changing_legacy_requests() {
        for (accept, expected) in [
            ("", false),
            ("*/*", false),
            ("text/plain; version=0.0.4", false),
            ("application/openmetrics-text; version=1.0.0", true),
            ("application/openmetrics-text; version=\"1.0.0\"", true),
            ("application/openmetrics-text; version=9.0.0", false),
            ("application/openmetrics-text; q=0", false),
            ("application/openmetrics-text; q=NaN", false),
            ("application/openmetrics-text; q=2", false),
            ("application/openmetrics-text; q=invalid", false),
            (
                "application/openmetrics-text; q=0.5, text/plain; q=1",
                false,
            ),
            ("application/openmetrics-text; q=1, text/plain; q=0.5", true),
        ] {
            let mut headers = http::HeaderMap::new();
            headers.insert(http::header::ACCEPT, accept.parse().unwrap());
            assert_eq!(wants_open_metrics(&headers), expected, "{accept}");
        }
    }

    #[test]
    fn rpc_exemplars_preserve_native_samples_and_only_attach_sampled_context() {
        let exemplars = RpcExemplars::default();
        let context = sampled_context();
        for context in [
            SpanContext::empty_context(),
            SpanContext::new(
                context.trace_id(),
                context.span_id(),
                opentelemetry::trace::TraceFlags::default(),
                false,
                opentelemetry::trace::TraceState::default(),
            ),
        ] {
            exemplars.observe("GetFiles", 0.004, &context).unwrap();
        }
        for duration in [f64::NAN, f64::INFINITY, -0.001] {
            exemplars.observe("GetFiles", duration, &context).unwrap();
        }
        assert!(exemplars.snapshot().unwrap().is_empty());
        exemplars.observe("GetFiles", 0.004, &context).unwrap();
        let histogram = prometheus::HistogramVec::new(
            prometheus::HistogramOpts::new("grpc_server_handling_seconds", "Native RPC latency")
                .buckets(RPC_LATENCY_BUCKETS[..RPC_LATENCY_BUCKETS.len() - 1].to_vec()),
            &["grpc_method"],
        )
        .unwrap();
        histogram.with_label_values(&["GetFiles"]).observe(0.004);
        let counter = prometheus::IntCounter::new("correlation_total", "Counter help").unwrap();
        counter.inc();
        let families = [counter.collect(), histogram.collect()].concat();
        let snapshot = exemplars.snapshot().unwrap();
        let legacy =
            String::from_utf8(encode_metrics(&families, false, &snapshot).unwrap()).unwrap();
        let open = String::from_utf8(encode_metrics(&families, true, &snapshot).unwrap()).unwrap();
        assert!(legacy.contains("# TYPE correlation_total counter\n"));
        assert!(!legacy.contains("trace_id"));
        assert!(!legacy.contains("# EOF"));
        assert!(open.contains("# TYPE correlation counter\n"));
        assert!(open.contains("# HELP correlation Counter help\n"));
        assert!(open.contains("correlation_total 1\n"));
        assert!(open.contains(
            "grpc_server_handling_seconds_bucket{grpc_method=\"GetFiles\",le=\"0.005\"} 1 # {trace_id=\"71717171717171717171717171717171\",span_id=\"8282828282828282\"} 0.004 "
        ));
        assert!(open.ends_with("# EOF\n"));
        let native_samples = |text: &str| {
            text.lines()
                .filter(|line| !line.starts_with('#'))
                .map(|line| line.split(" # ").next().unwrap().to_owned())
                .collect::<Vec<_>>()
        };
        assert_eq!(native_samples(&legacy), native_samples(&open));
    }

    #[test]
    fn rpc_exemplars_retain_at_most_one_sample_per_fixed_method_bucket() {
        let exemplars = RpcExemplars::default();
        let context = sampled_context();
        for index in 0..1000 {
            for (bucket, upper) in RPC_LATENCY_BUCKETS.iter().enumerate() {
                let duration = if upper.is_infinite() { 6.0 } else { *upper };
                exemplars
                    .observe(&format!("unbounded-{index}"), duration, &context)
                    .unwrap();
                assert!(
                    exemplars.0.lock().unwrap().len() <= RPC_LATENCY_BUCKETS.len(),
                    "arbitrary method strings must collapse into the fixed unknown method"
                );
                let key = ("unknown", bucket);
                assert!(exemplars.0.lock().unwrap().contains_key(&key));
            }
        }
        let snapshot = exemplars.snapshot().unwrap();
        assert_eq!(snapshot.len(), RPC_LATENCY_BUCKETS.len());
        assert!(snapshot
            .keys()
            .all(|key| key.contains("grpc_method=\"unknown\"")));
        assert!(snapshot.keys().any(|key| key.contains("le=\"+Inf\"")));
        let counter = prometheus::IntCounter::new("oversized_total", "Bounded help").unwrap();
        counter.inc();
        let mut families = counter.collect();
        families[0].set_help("x".repeat(METRICS_MAX_BYTES));
        assert!(encode_metrics(&families, true, &snapshot).is_err());
    }

    #[tokio::test]
    async fn metrics_listener_uses_resolved_uri() {
        assert!(
            start_metrics(&crate::config::MetricsConfig { uri: String::new() })
                .await
                .unwrap()
                .is_none()
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let config = crate::config::MetricsConfig {
            uri: format!("http://{}", listener.local_addr().unwrap()),
        };
        let error = start_metrics(&config)
            .await
            .expect_err("occupied URI must fail binding");
        assert_eq!(
            error.downcast_ref::<std::io::Error>().unwrap().kind(),
            std::io::ErrorKind::AddrInUse
        );
    }

    struct TestBody {
        frames:
            std::collections::VecDeque<Result<http_body::Frame<axum::body::Bytes>, tonic::Status>>,
    }

    impl Body for TestBody {
        type Data = axum::body::Bytes;
        type Error = tonic::Status;
        fn poll_frame(
            self: Pin<&mut Self>,
            _: &mut Context<'_>,
        ) -> Poll<Option<Result<http_body::Frame<Self::Data>, Self::Error>>> {
            Poll::Ready(self.get_mut().frames.pop_front())
        }
    }

    struct TestService {
        response: Option<Result<http::Response<tonic::body::Body>, &'static str>>,
    }

    impl Service<http::Request<()>> for TestService {
        type Response = http::Response<tonic::body::Body>;
        type Error = &'static str;
        type Future = Pin<Box<dyn Future<Output = Result<Self::Response, Self::Error>> + Send>>;
        fn poll_ready(&mut self, _: &mut Context<'_>) -> Poll<Result<(), Self::Error>> {
            Poll::Ready(Ok(()))
        }
        fn call(&mut self, _: http::Request<()>) -> Self::Future {
            match self.response.take() {
                Some(response) => Box::pin(std::future::ready(response)),
                None => Box::pin(std::future::pending()),
            }
        }
    }

    #[test]
    fn rpc_exemplar_keeps_poll_time_span_through_response_body_completion() {
        use opentelemetry::trace::TracerProvider;
        use tracing::Instrument;
        use tracing_subscriber::prelude::*;
        let provider = opentelemetry_sdk::trace::SdkTracerProvider::builder()
            .with_sampler(opentelemetry_sdk::trace::Sampler::AlwaysOn)
            .build();
        let subscriber = tracing_subscriber::registry().with(
            tracing_opentelemetry::layer().with_tracer(provider.tracer("RPC exemplar contract")),
        );
        tracing::subscriber::with_default(subscriber, || {
            let runtime = tokio::runtime::Builder::new_current_thread()
                .enable_all()
                .build()
                .unwrap();
            runtime.block_on(async {
                let mut trailers = http::HeaderMap::new();
                trailers.insert("grpc-status", "0".parse().unwrap());
                let response = http::Response::new(tonic::body::Body::new(TestBody {
                    frames: [
                        Ok(http_body::Frame::data(axum::body::Bytes::from_static(
                            b"body",
                        ))),
                        Ok(http_body::Frame::trailers(trailers)),
                    ]
                    .into(),
                }));
                let mut service = RpcMetricsService::new(TestService {
                    response: Some(Ok(response)),
                });
                let mut request = http::Request::new(());
                *request.uri_mut() = "/gitstore.git.v1.GitService/CreateTag".parse().unwrap();
                let future = service.call(request);
                let span = tracing::info_span!("poll-time RPC");
                let expected = span.context().span().span_context().clone();
                assert!(expected.is_sampled());
                let response = future.instrument(span).await.unwrap();
                let mut body = Box::pin(response.into_body());
                while let Some(frame) =
                    std::future::poll_fn(|cx| body.as_mut().poll_frame(cx)).await
                {
                    frame.unwrap();
                }
                drop(body);
                let snapshot = rpc_exemplars().snapshot().unwrap();
                assert!(snapshot.iter().any(|(key, exemplar)| {
                    key.contains("grpc_method=\"CreateTag\"")
                        && exemplar.context.trace_id() == expected.trace_id()
                        && exemplar.context.span_id() == expected.span_id()
                }));
            });
        });
        provider.shutdown().unwrap();
    }

    #[tokio::test]
    async fn rpc_completion_preserves_frames_and_counts_final_status_once() {
        for code in [tonic::Code::Ok, tonic::Code::ResourceExhausted] {
            let before = handled_total()
                .with_label_values(&["unknown", &format!("{code:?}")])
                .get();
            let mut trailers = http::HeaderMap::new();
            trailers.insert("grpc-status", (code as i32).to_string().parse().unwrap());
            trailers.insert("receipt", "preserved".parse().unwrap());
            let response = http::Response::new(tonic::body::Body::new(TestBody {
                frames: [
                    Ok(http_body::Frame::data(axum::body::Bytes::from_static(
                        b"payload",
                    ))),
                    Ok(http_body::Frame::trailers(trailers)),
                ]
                .into(),
            }));
            let mut service = RpcMetricsService::new(TestService {
                response: Some(Ok(response)),
            });
            let response = service.call(http::Request::new(())).await.unwrap();
            assert_eq!(
                handled_total()
                    .with_label_values(&["unknown", &format!("{code:?}")])
                    .get(),
                before
            );
            let mut body = Box::pin(response.into_body());
            let data = std::future::poll_fn(|cx| body.as_mut().poll_frame(cx))
                .await
                .unwrap()
                .unwrap();
            assert_eq!(data.data_ref().unwrap().as_ref(), b"payload");
            let trailer = std::future::poll_fn(|cx| body.as_mut().poll_frame(cx))
                .await
                .unwrap()
                .unwrap();
            assert_eq!(trailer.trailers_ref().unwrap()["receipt"], "preserved");
            assert!(std::future::poll_fn(|cx| body.as_mut().poll_frame(cx))
                .await
                .is_none());
            drop(body);
            assert_eq!(
                handled_total()
                    .with_label_values(&["unknown", &format!("{code:?}")])
                    .get(),
                before + 1
            );
        }
        let count = |code: tonic::Code| {
            handled_total()
                .with_label_values(&["unknown", &format!("{code:?}")])
                .get()
        };
        let before = count(tonic::Code::PermissionDenied);
        let mut response = http::Response::new(tonic::body::Body::empty());
        response
            .headers_mut()
            .insert("grpc-status", "7".parse().unwrap());
        let mut service = RpcMetricsService::new(TestService {
            response: Some(Ok(response)),
        });
        drop(service.call(http::Request::new(())).await.unwrap());
        assert_eq!(count(tonic::Code::PermissionDenied), before + 1);

        let before = count(tonic::Code::Unknown);
        let mut service = RpcMetricsService::new(TestService {
            response: Some(Err("transport failure")),
        });
        assert_eq!(
            service.call(http::Request::new(())).await.unwrap_err(),
            "transport failure"
        );
        assert_eq!(count(tonic::Code::Unknown), before + 1);
        let before = count(tonic::Code::Internal);
        let response = http::Response::new(tonic::body::Body::new(TestBody {
            frames: [Err(tonic::Status::internal("body failure"))].into(),
        }));
        let mut service = RpcMetricsService::new(TestService {
            response: Some(Ok(response)),
        });
        let mut body = Box::pin(
            service
                .call(http::Request::new(()))
                .await
                .unwrap()
                .into_body(),
        );
        assert!(std::future::poll_fn(|cx| body.as_mut().poll_frame(cx))
            .await
            .unwrap()
            .is_err());
        drop(body);
        assert_eq!(count(tonic::Code::Internal), before + 1);

        let before = count(tonic::Code::Cancelled);
        let mut service = RpcMetricsService::new(TestService { response: None });
        drop(service.call(http::Request::new(())));
        let response = http::Response::new(tonic::body::Body::new(TestBody { frames: [].into() }));
        let mut service = RpcMetricsService::new(TestService {
            response: Some(Ok(response)),
        });
        drop(service.call(http::Request::new(())).await.unwrap());
        assert_eq!(count(tonic::Code::Cancelled), before + 2);
    }

    #[test]
    fn rpc_method_labels_ignore_unbounded_paths() {
        assert_eq!(
            rpc_method("/gitstore.git.v1.GitService/GetFiles"),
            "GetFiles"
        );
        assert_eq!(
            rpc_method("/gitstore.git.v1.GitService/attacker-controlled"),
            "unknown"
        );
        assert_eq!(rpc_method(&"x".repeat(10000)), "unknown");
    }

    #[test]
    fn rpc_verification_capacity_timer_records_canceled_waits() {
        let histogram = rpc_verification_phase_seconds().with_label_values(&["capacity_wait"]);
        let before = histogram.get_sample_count();
        let mut wait = Box::pin(async {
            let _timer = start_rpc_verification_phase(RPCVerificationPhase::CapacityWait);
            std::future::pending::<()>().await;
        });
        let waker = std::task::Waker::noop();
        let mut context = Context::from_waker(waker);
        assert!(wait.as_mut().poll(&mut context).is_pending());
        drop(wait);
        assert!(histogram.get_sample_count() > before);
    }

    #[test]
    fn rpc_verification_phases_have_only_fixed_labels() {
        for phase in [
            RPCVerificationPhase::CapacityWait,
            RPCVerificationPhase::BlockingWait,
            RPCVerificationPhase::ReplayLock,
            RPCVerificationPhase::EnvelopeCheck,
            RPCVerificationPhase::ReplayReserve,
            RPCVerificationPhase::ReplayClock,
            RPCVerificationPhase::ReplayCleanup,
            RPCVerificationPhase::ReplayPersist,
            RPCVerificationPhase::ReplayComplete,
            RPCVerificationPhase::DirectoryBarrier,
            RPCVerificationPhase::ReplayInode,
            RPCVerificationPhase::ReplayDirectory,
            RPCVerificationPhase::DirectoryPublish,
            RPCVerificationPhase::DirectoryReport,
        ] {
            let histogram = rpc_verification_phase_seconds().with_label_values(&[phase.label()]);
            let before = histogram.get_sample_count();
            record_rpc_verification_phase(phase, 0.001);
            assert!(histogram.get_sample_count() > before);
        }
        let families = rpc_verification_phase_seconds().collect();
        assert_eq!(families.len(), 1);
        for metric in families[0].get_metric() {
            assert_eq!(metric.get_label().len(), 1);
            let label = &metric.get_label()[0];
            assert_eq!(label.name(), "phase");
            assert!(matches!(
                label.value(),
                "capacity_wait"
                    | "blocking_wait"
                    | "replay_lock"
                    | "envelope_check"
                    | "replay_reserve"
                    | "replay_clock"
                    | "replay_cleanup"
                    | "replay_persist"
                    | "replay_complete"
                    | "directory_barrier"
                    | "replay_inode"
                    | "replay_directory"
                    | "directory_publish"
                    | "directory_report"
            ));
        }
    }

    #[test]
    fn file_read_phase_timers_keep_originating_span_across_worker_scopes() {
        use opentelemetry::trace::TracerProvider;
        use opentelemetry_sdk::trace::{SpanData, SpanExporter};
        use tracing_subscriber::prelude::*;

        #[derive(Clone, Debug, Default)]
        struct CapturedSpans(std::sync::Arc<Mutex<Vec<SpanData>>>);

        impl SpanExporter for CapturedSpans {
            async fn export(
                &self,
                batch: Vec<SpanData>,
            ) -> opentelemetry_sdk::error::OTelSdkResult {
                self.0.lock().unwrap().extend(batch);
                Ok(())
            }
        }

        let captured = CapturedSpans::default();
        let provider = opentelemetry_sdk::trace::SdkTracerProvider::builder()
            .with_sampler(opentelemetry_sdk::trace::Sampler::AlwaysOn)
            .with_simple_exporter(captured.clone())
            .build();
        let subscriber = tracing_subscriber::registry().with(
            tracing_opentelemetry::layer().with_tracer(provider.tracer("file read phase contract")),
        );
        let mut expected = std::collections::BTreeSet::new();
        tracing::subscriber::with_default(subscriber, || {
            for (mode, observe) in [("dropped", false), ("explicit", true)] {
                let span = tracing::info_span!("file.read", mode);
                span.in_scope(|| {
                    for phase in [
                        FileReadPhase::Authentication,
                        FileReadPhase::RepositoryLock,
                        FileReadPhase::BlockingQueue,
                        FileReadPhase::Payload,
                    ] {
                        let label = phase.label();
                        let timer = (label != "payload").then(|| start_file_read_phase(phase));
                        let parent = tracing::Span::current();
                        let dispatch = tracing::dispatcher::get_default(Clone::clone);
                        std::thread::spawn(move || {
                            tracing::dispatcher::with_default(&dispatch, || {
                                tracing::info_span!("unrelated.worker").in_scope(|| {
                                    let timer = timer.unwrap_or_else(|| {
                                        start_file_read_phase_in_span(
                                            FileReadPhase::Payload,
                                            parent,
                                        )
                                    });
                                    if observe {
                                        timer.observe_duration();
                                    } else {
                                        drop(timer);
                                    }
                                })
                            })
                        })
                        .join()
                        .unwrap();
                        expected.insert(format!("git.file_read.{label}.seconds"));
                    }
                });
            }
            for observe in [false, true] {
                let histogram = Histogram::with_opts(prometheus::HistogramOpts::new(
                    "isolated_traced_file_read_phase_seconds",
                    "test",
                ))
                .unwrap();
                let span = tracing::info_span!("isolated.file.read");
                span.in_scope(|| {
                    let timer = FileReadPhaseTimer {
                        timer: Some(histogram.start_timer()),
                        phase: FileReadPhase::Payload,
                        span: tracing::Span::current(),
                    };
                    if observe {
                        timer.observe_duration();
                    } else {
                        drop(timer);
                    }
                });
                assert_eq!(histogram.get_sample_count(), 1);
                span.set_attribute("test.histogram.seconds", histogram.get_sample_sum());
            }
        });
        provider.force_flush().unwrap();
        provider.shutdown().unwrap();
        let spans = captured.0.lock().unwrap();
        let parents: Vec<_> = spans
            .iter()
            .filter(|span| span.name == "file.read")
            .collect();
        assert_eq!(parents.len(), 2);
        for parent in parents {
            let phases: BTreeMap<_, _> = parent
                .attributes
                .iter()
                .filter_map(|attribute| {
                    if attribute.key.as_str().starts_with("git.file_read.") {
                        let opentelemetry::Value::F64(value) = attribute.value else {
                            panic!("phase duration must be a numeric trace attribute");
                        };
                        assert!(value.is_finite() && value >= 0.0);
                        Some((attribute.key.as_str().to_owned(), value))
                    } else {
                        None
                    }
                })
                .collect();
            assert_eq!(
                phases.keys().collect::<Vec<_>>(),
                expected.iter().collect::<Vec<_>>(),
                "all four phase timers must annotate their originating RPC span"
            );
            assert_eq!(phases.len(), 4);
        }
        let workers: Vec<_> = spans
            .iter()
            .filter(|span| span.name == "unrelated.worker")
            .collect();
        assert_eq!(workers.len(), 8);
        for worker in workers {
            assert!(
                worker
                    .attributes
                    .iter()
                    .all(|attribute| { !attribute.key.as_str().starts_with("git.file_read.") }),
                "dropping a timer on another worker must not attribute its phase to that worker"
            );
        }
        let isolated: Vec<_> = spans
            .iter()
            .filter(|span| span.name == "isolated.file.read")
            .collect();
        assert_eq!(isolated.len(), 2);
        for span in isolated {
            let value = |key| {
                &span
                    .attributes
                    .iter()
                    .find(|attribute| attribute.key.as_str() == key)
                    .unwrap()
                    .value
            };
            assert_eq!(
                value("git.file_read.payload.seconds"),
                value("test.histogram.seconds"),
                "trace and histogram must use the exact same observation"
            );
        }
    }

    #[test]
    fn file_read_phase_timer_records_once_without_tracing() {
        for observe in [false, true] {
            let histogram = Histogram::with_opts(prometheus::HistogramOpts::new(
                "isolated_file_read_phase_seconds",
                "test",
            ))
            .unwrap();
            let timer = FileReadPhaseTimer {
                timer: Some(histogram.start_timer()),
                phase: FileReadPhase::Payload,
                span: tracing::Span::none(),
            };
            if observe {
                timer.observe_duration();
            } else {
                drop(timer);
            }
            assert_eq!(histogram.get_sample_count(), 1);
            assert!(histogram.get_sample_sum().is_finite());
            assert!(histogram.get_sample_sum() >= 0.0);
        }
    }

    #[test]
    fn file_read_phases_have_only_fixed_labels_and_record_dropped_timers() {
        let phases = [
            FileReadPhase::Authentication,
            FileReadPhase::RepositoryLock,
            FileReadPhase::BlockingQueue,
            FileReadPhase::Payload,
        ];
        for phase in phases {
            let histogram = file_read_phase_seconds().with_label_values(&[phase.label()]);
            let before = histogram.get_sample_count();
            drop(start_file_read_phase(phase));
            assert!(histogram.get_sample_count() > before);
        }
        let families = file_read_phase_seconds().collect();
        assert_eq!(families.len(), 1);
        for metric in families[0].get_metric() {
            assert_eq!(metric.get_label().len(), 1);
            let label = &metric.get_label()[0];
            assert_eq!(label.name(), "phase");
            assert!(matches!(
                label.value(),
                "authentication" | "repository_lock" | "blocking_queue" | "payload"
            ));
        }
    }
}
