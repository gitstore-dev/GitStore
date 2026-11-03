// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use opentelemetry_http::{HttpClient, HttpError};
use std::time::Duration;
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
        self.0.send_bytes(request).await
    }
}
