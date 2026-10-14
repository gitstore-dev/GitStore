// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Per-RPC Prometheus metrics for the gRPC server.
// Uses a Tower middleware layer that intercepts each RPC and records
// grpc_server_handled_total and grpc_server_handling_seconds.

use prometheus::{register_histogram, register_int_gauge, Encoder, Histogram, IntGauge};
use prometheus::{
    register_histogram_vec, register_int_counter_vec, HistogramTimer, HistogramVec, IntCounterVec,
};
use std::sync::OnceLock;
use std::{
    future::Future,
    pin::Pin,
    task::{Context, Poll},
    time::Instant,
};
use tonic::codegen::{http, Service};

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
}

impl RpcCompletion {
    fn new(method: &'static str) -> Self {
        Self {
            method,
            started: Instant::now(),
            completed: false,
        }
    }
    fn finish(&mut self, code: tonic::Code) {
        if !self.completed {
            self.completed = true;
            record_rpc(
                self.method,
                &format!("{code:?}"),
                self.started.elapsed().as_secs_f64(),
            );
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
    address: &str,
) -> anyhow::Result<Option<tokio::task::JoinHandle<std::io::Result<()>>>> {
    if address.is_empty() {
        return Ok(None);
    }
    let listener = tokio::net::TcpListener::bind(address).await?;
    let permits = std::sync::Arc::new(tokio::sync::Semaphore::new(4));
    let app = axum::Router::new().route(
        "/metrics",
        axum::routing::get(move || {
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
                let mut body = Vec::new();
                if prometheus::TextEncoder::new()
                    .encode(&prometheus::gather(), &mut body)
                    .is_err()
                    || body.len() > 1024 * 1024
                {
                    tracing::error!("metrics encoding failed or exceeded bound");
                    return (
                        axum::http::StatusCode::INTERNAL_SERVER_ERROR,
                        "metrics encoding failed".to_string(),
                    )
                        .into_response();
                }
                (
                    [(
                        axum::http::header::CONTENT_TYPE,
                        "text/plain; version=0.0.4",
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
    ReplayPersist,
    ReplayComplete,
    DirectoryBarrier,
    ReplayInode,
    ReplayDirectory,
}

impl RPCVerificationPhase {
    fn label(&self) -> &'static str {
        match self {
            Self::CapacityWait => "capacity_wait",
            Self::BlockingWait => "blocking_wait",
            Self::ReplayLock => "replay_lock",
            Self::EnvelopeCheck => "envelope_check",
            Self::ReplayReserve => "replay_reserve",
            Self::ReplayPersist => "replay_persist",
            Self::ReplayComplete => "replay_complete",
            Self::DirectoryBarrier => "directory_barrier",
            Self::ReplayInode => "replay_inode",
            Self::ReplayDirectory => "replay_directory",
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
}

pub(super) fn file_read_phase_seconds() -> &'static HistogramVec {
    FILE_READ_PHASE_SECONDS.get_or_init(|| {
        register_histogram_vec!(
            "gitstore_git_file_read_phase_seconds",
            "GetFile server phase latency in seconds",
            &["phase"],
            vec![0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0]
        )
        .expect("failed to register gitstore_git_file_read_phase_seconds")
    })
}

pub fn start_file_read_phase(phase: FileReadPhase) -> HistogramTimer {
    file_read_phase_seconds()
        .with_label_values(&[phase.label()])
        .start_timer()
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
    handled_total().with_label_values(&[method, code]).inc();
    handling_seconds()
        .with_label_values(&[method])
        .observe(duration_secs);
}

#[cfg(test)]
mod tests {
    use super::*;
    use http_body::Body;
    use prometheus::core::Collector;

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
    fn rpc_verification_phases_have_only_fixed_labels() {
        for phase in [
            RPCVerificationPhase::CapacityWait,
            RPCVerificationPhase::BlockingWait,
            RPCVerificationPhase::ReplayLock,
            RPCVerificationPhase::EnvelopeCheck,
            RPCVerificationPhase::ReplayReserve,
            RPCVerificationPhase::ReplayPersist,
            RPCVerificationPhase::ReplayComplete,
            RPCVerificationPhase::DirectoryBarrier,
            RPCVerificationPhase::ReplayInode,
            RPCVerificationPhase::ReplayDirectory,
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
                    | "replay_persist"
                    | "replay_complete"
                    | "directory_barrier"
                    | "replay_inode"
                    | "replay_directory"
            ));
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
