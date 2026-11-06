// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use opentelemetry_http::{HttpClient, HttpError};
use prost::Message;
use std::{io::Read, time::Duration};
use tonic::codegen::{http, Bytes};

#[derive(Debug)]
pub struct CollectorClient(reqwest::blocking::Client);

impl CollectorClient {
    pub fn new() -> anyhow::Result<Self> {
        let client = std::thread::spawn(|| {
            reqwest::blocking::Client::builder()
                .timeout(Duration::from_secs(2))
                .connect_timeout(Duration::from_secs(2))
                .pool_max_idle_per_host(1)
                .redirect(reqwest::redirect::Policy::none())
                .build()
        })
        .join()
        .map_err(|_| anyhow::anyhow!("could not initialize collector transport"))??;
        Ok(Self(client))
    }
}

#[async_trait::async_trait]
impl HttpClient for CollectorClient {
    async fn send_bytes(
        &self,
        mut request: http::Request<Bytes>,
    ) -> Result<http::Response<Bytes>, HttpError> {
        // The SDK merges ambient OTEL_*_HEADERS even with explicit config.
        // Application/provider authentication material must never ride along.
        let content_type = request.headers().get(http::header::CONTENT_TYPE).cloned();
        let encoding = request
            .headers()
            .get(http::header::CONTENT_ENCODING)
            .cloned();
        request.headers_mut().clear();
        if let Some(value) = content_type {
            request
                .headers_mut()
                .insert(http::header::CONTENT_TYPE, value);
        }
        if let Some(value) = encoding {
            request
                .headers_mut()
                .insert(http::header::CONTENT_ENCODING, value);
        }
        if request.uri().path().ends_with("/v1/metrics") {
            let (parts, body) = request.into_parts();
            let response = self
                .0
                .request(parts.method, parts.uri.to_string())
                .headers(parts.headers)
                .body(body)
                .send()?;
            let status = response.status();
            let mut builder = http::Response::builder().status(status);
            *builder.headers_mut().expect("response headers") = response.headers().clone();
            let mut body = Vec::new();
            response.take(65537).read_to_end(&mut body)?;
            if body.len() > 65536 {
                return Err(
                    std::io::Error::other("collector metrics response exceeds size limit").into(),
                );
            }
            // The Rust SDK treats malformed/partially rejected 2xx metric
            // replies as log-only success. Preserve complete-snapshot health
            // accounting without exposing collector response text to logs.
            if status.is_success() {
                let reply = opentelemetry_proto::tonic::collector::metrics::v1::ExportMetricsServiceResponse::decode(body.as_slice())
                    .map_err(|_| std::io::Error::other("collector metrics response is invalid"))?;
                if reply
                    .partial_success
                    .is_some_and(|partial| partial.rejected_data_points > 0)
                {
                    return Err(std::io::Error::other("collector rejected metric points").into());
                }
            }
            body.clear();
            return Ok(builder.body(Bytes::from(body))?);
        }
        self.0.send_bytes(request).await
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        io::{Read, Write},
        net::TcpListener,
    };

    async fn reply(body: Vec<u8>, status: u16) -> Result<http::Response<Bytes>, HttpError> {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let server = std::thread::spawn(move || {
            let (mut socket, _) = listener.accept().unwrap();
            socket
                .set_read_timeout(Some(Duration::from_secs(3)))
                .unwrap();
            let mut request = [0; 4096];
            let _ = socket.read(&mut request).unwrap();
            write!(
                socket,
                "HTTP/1.1 {status} OK\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                body.len()
            )
            .unwrap();
            let _ = socket.write_all(&body);
        });
        let client = CollectorClient::new().unwrap();
        let result = client
            .send_bytes(
                http::Request::post(format!("http://{address}/v1/metrics"))
                    .body(Bytes::new())
                    .unwrap(),
            )
            .await;
        // reqwest's blocking client must be dropped outside the async runtime.
        std::thread::spawn(move || drop(client)).join().unwrap();
        server.join().unwrap();
        result
    }

    #[test]
    fn metrics_transport_rejects_partial_malformed_and_oversized_replies() {
        // This transport is polled on the SDK's synchronous periodic-reader
        // thread, outside a Tokio runtime.
        let future = async {
            use opentelemetry_proto::tonic::collector::metrics::v1::{
                ExportMetricsPartialSuccess, ExportMetricsServiceResponse,
            };
            let partial = ExportMetricsServiceResponse {
                partial_success: Some(ExportMetricsPartialSuccess {
                    rejected_data_points: 1,
                    error_message: "private collector detail".into(),
                }),
            }
            .encode_to_vec();
            let error = reply(partial, 200).await.unwrap_err().to_string();
            assert!(error.contains("rejected metric points"));
            assert!(!error.contains("private collector detail"));
            assert!(reply(vec![255], 200)
                .await
                .unwrap_err()
                .to_string()
                .contains("invalid"));
            assert!(reply(vec![0; 65537], 200)
                .await
                .unwrap_err()
                .to_string()
                .contains("size limit"));
            assert!(reply(Vec::new(), 200).await.unwrap().status().is_success());
            let response = reply(b"private rejection detail".to_vec(), 503)
                .await
                .unwrap();
            assert_eq!(response.status(), 503);
            assert!(response.body().is_empty());
        };
        let mut future = std::pin::pin!(future);
        assert!(std::future::Future::poll(
            future.as_mut(),
            &mut std::task::Context::from_waker(std::task::Waker::noop())
        )
        .is_ready());
    }
}
