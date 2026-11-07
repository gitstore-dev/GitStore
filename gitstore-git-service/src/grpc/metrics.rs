// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Per-RPC OpenTelemetry SDK metrics for the gRPC server.
// Uses a Tower middleware layer that intercepts each RPC and records
// grpc_server_handled_total and grpc_server_handling_seconds.

use opentelemetry::trace::TraceContextExt;
use opentelemetry::{
    global,
    metrics::{Counter, Histogram},
    KeyValue,
};
use std::{
    future::Future,
    pin::Pin,
    task::{Context, Poll},
    time::Instant,
};
use tonic::codegen::{http, Service};
use tracing_opentelemetry::OpenTelemetrySpanExt;

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

pub fn handled_total() -> Counter<u64> {
    global::meter("gitstore/grpc")
        .u64_counter("grpc_server_handled_total")
        .with_description("Total number of RPCs completed on the server")
        .build()
}

pub fn handling_seconds() -> Histogram<f64> {
    global::meter("gitstore/grpc")
        .f64_histogram("grpc_server_handling_seconds")
        .with_description("Histogram of RPC response latency in seconds")
        .with_unit("s")
        .with_boundaries(vec![
            0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0,
        ])
        .build()
}

/// Record a completed RPC call through the process-owned SDK provider.
pub fn record_rpc(method: &str, code: &str, duration_secs: f64) {
    handled_total().add(
        1,
        &[
            KeyValue::new("grpc_method", method.to_owned()),
            KeyValue::new("grpc_code", code.to_owned()),
        ],
    );
    handling_seconds().record(
        duration_secs,
        &[KeyValue::new("grpc_method", method.to_owned())],
    );
}

struct RpcCompletion {
    method: &'static str,
    started: Instant,
    completed: bool,
    span: tracing::Span,
}

impl RpcCompletion {
    fn finish(&mut self, code: tonic::Code) {
        if self.completed {
            return;
        }
        self.completed = true;
        let duration = self.started.elapsed().as_secs_f64();
        record_rpc(self.method, &format!("{code:?}"), duration);
        if self.span.context().span().span_context().is_sampled() {
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
            Poll::Ready(Some(Err(error))) => this.completion.finish(error.code()),
            Poll::Ready(None) => this.completion.finish(tonic::Code::Unknown),
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
        let mut completion = RpcCompletion {
            method: rpc_method(request.uri().path()),
            started: Instant::now(),
            completed: false,
            span: tracing::Span::current(),
        };
        let future = self.inner.call(request);
        Box::pin(async move {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn completion_records_once_and_cancellation_does_not_count_success() {
        crate::metrics_export::test_observer::ensure_provider();
        let value = |code| {
            crate::metrics_export::test_observer::count(
                "grpc_server_handled_total",
                &[("grpc_method", "unknown"), ("grpc_code", code)],
            )
        };
        let ok = value("Ok");
        let cancelled = value("Cancelled");
        {
            let mut completion = RpcCompletion {
                method: "unknown",
                started: Instant::now(),
                completed: false,
                span: tracing::Span::none(),
            };
            completion.finish(tonic::Code::Ok);
            completion.finish(tonic::Code::Unknown);
        }
        assert_eq!(value("Ok"), ok + 1);
        assert_eq!(value("Cancelled"), cancelled);
        drop(RpcCompletion {
            method: "unknown",
            started: Instant::now(),
            completed: false,
            span: tracing::Span::none(),
        });
        assert_eq!(value("Cancelled"), cancelled + 1);
    }
}
