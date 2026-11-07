// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use opentelemetry_otlp::{WithExportConfig, WithHttpConfig};
use opentelemetry_sdk::{
    error::OTelSdkResult,
    metrics::{
        data::ResourceMetrics, exporter::PushMetricExporter, PeriodicReader, SdkMeterProvider,
        Stream, Temporality,
    },
    Resource,
};
use std::{sync::Arc, time::Duration};

#[derive(Debug)]
struct ObservedExporter(opentelemetry_otlp::MetricExporter);

fn bounded_stream(_: &opentelemetry_sdk::metrics::Instrument) -> Option<Stream> {
    Some(
        Stream::builder()
            .with_cardinality_limit(2048)
            .build()
            .expect("constant metric cardinality limit"),
    )
}

impl PushMetricExporter for ObservedExporter {
    async fn export(&self, metrics: &ResourceMetrics) -> OTelSdkResult {
        let result = self.0.export(metrics).await;
        super::telemetry_records(
            "metrics",
            if result.is_ok() { "exported" } else { "failed" },
            1,
        );
        result
    }
    fn force_flush(&self) -> OTelSdkResult {
        self.0.force_flush()
    }
    fn shutdown_with_timeout(&self, timeout: Duration) -> OTelSdkResult {
        self.0.shutdown_with_timeout(timeout)
    }
    fn temporality(&self) -> Temporality {
        Temporality::Cumulative
    }
}

/// Owns the supported SDK periodic reader and exporter. Request handlers only
/// record instruments; collection and bounded HTTP export run on the SDK worker.
pub struct MetricExport {
    provider: Option<SdkMeterProvider>,
}

impl MetricExport {
    pub fn start(endpoint: &str, resource: &Resource) -> anyhow::Result<Self> {
        let exporter = opentelemetry_otlp::MetricExporter::builder()
            .with_http()
            .with_shared_http_client(Arc::new(super::otlp_http::CollectorClient::new()?))
            .with_endpoint(format!("{}/v1/metrics", endpoint.trim_end_matches('/')))
            .with_protocol(opentelemetry_otlp::Protocol::HttpBinary)
            .with_timeout(Duration::from_secs(2))
            .with_max_request_body_size(2 << 20)
            .with_retry_policy(opentelemetry_otlp::RetryPolicy::disabled())
            .with_temporality(Temporality::Cumulative)
            .build()?;
        let reader = PeriodicReader::builder(ObservedExporter(exporter))
            .with_interval(Duration::from_secs(10))
            .build();
        let builder = SdkMeterProvider::builder()
            .with_resource(resource.clone())
            .with_reader(reader)
            .with_view(bounded_stream);
        #[cfg(test)]
        let builder = builder.with_reader(test_observer::reader());
        let provider = builder.build();
        super::runtime_metrics::register(&provider);
        opentelemetry::global::set_meter_provider(provider.clone());
        #[cfg(test)]
        test_observer::set_provider(provider.clone());
        Ok(Self {
            provider: Some(provider),
        })
    }

    pub fn shutdown(mut self) -> anyhow::Result<()> {
        if let Some(provider) = self.provider.take() {
            provider.shutdown_with_timeout(Duration::from_secs(3))?;
        }
        Ok(())
    }
}

impl Drop for MetricExport {
    fn drop(&mut self) {
        if let Some(provider) = self.provider.take() {
            let _ = provider.shutdown_with_timeout(Duration::from_secs(3));
        }
    }
}

#[cfg(test)]
pub(crate) mod test_observer {
    use super::*;
    use opentelemetry_sdk::metrics::{
        data::{AggregatedMetrics, MetricData},
        InMemoryMetricExporter,
    };
    use std::sync::{Mutex, OnceLock};

    static EXPORTER: OnceLock<InMemoryMetricExporter> = OnceLock::new();
    static PROVIDER: Mutex<Option<SdkMeterProvider>> = Mutex::new(None);
    pub fn reader() -> PeriodicReader<InMemoryMetricExporter> {
        PeriodicReader::builder(
            EXPORTER
                .get_or_init(InMemoryMetricExporter::default)
                .clone(),
        )
        .with_interval(Duration::from_secs(3600))
        .build()
    }
    pub fn set_provider(provider: SdkMeterProvider) {
        *PROVIDER.lock().unwrap() = Some(provider);
    }
    pub fn ensure_provider() {
        let mut current = PROVIDER.lock().unwrap();
        if current.is_none() {
            let provider = SdkMeterProvider::builder().with_reader(reader()).build();
            opentelemetry::global::set_meter_provider(provider.clone());
            *current = Some(provider);
        }
    }
    pub fn count(name: &str, labels: &[(&str, &str)]) -> u64 {
        ensure_provider();
        let current = PROVIDER.lock().unwrap();
        let _ = current.as_ref().unwrap().force_flush();
        let batches = EXPORTER.get().unwrap().get_finished_metrics().unwrap();
        for batch in batches.iter().rev() {
            for scope in batch.scope_metrics() {
                for metric in scope.metrics() {
                    if metric.name() != name {
                        continue;
                    }
                    if let AggregatedMetrics::U64(MetricData::Sum(sum)) = metric.data() {
                        for point in sum.data_points() {
                            if labels.iter().all(|(key, value)| {
                                point.attributes().any(|kv| {
                                    kv.key.as_str() == *key && kv.value.as_str() == *value
                                })
                            }) {
                                return point.value();
                            }
                        }
                    }
                }
            }
        }
        0
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use opentelemetry::metrics::MeterProvider;
    use opentelemetry_sdk::metrics::{
        data::{AggregatedMetrics, MetricData},
        InMemoryMetricExporter,
    };

    #[test]
    fn sdk_histograms_preserve_cumulative_counts_sums_and_boundaries() {
        let exporter = InMemoryMetricExporter::default();
        let provider = SdkMeterProvider::builder()
            .with_view(bounded_stream)
            .with_reader(
                PeriodicReader::builder(exporter.clone())
                    .with_interval(Duration::from_secs(3600))
                    .build(),
            )
            .build();
        let histogram = provider
            .meter("gitstore/metric-contract")
            .f64_histogram("duration_seconds")
            .with_unit("s")
            .with_boundaries(vec![1.0, 2.0])
            .build();
        histogram.record(0.5, &[]);
        histogram.record(3.0, &[]);
        for _ in 0..2 {
            provider.force_flush().unwrap();
            let batches = exporter.get_finished_metrics().unwrap();
            let metric = batches
                .last()
                .unwrap()
                .scope_metrics()
                .flat_map(|scope| scope.metrics())
                .find(|metric| metric.name() == "duration_seconds")
                .unwrap();
            let AggregatedMetrics::F64(MetricData::Histogram(data)) = metric.data() else {
                panic!("expected SDK histogram")
            };
            assert_eq!(data.temporality(), Temporality::Cumulative);
            let point = data.data_points().next().unwrap();
            assert_eq!(point.count(), 2);
            assert_eq!(point.sum(), 3.5);
            assert_eq!(point.bounds().collect::<Vec<_>>(), vec![1.0, 2.0]);
            assert_eq!(point.bucket_counts().collect::<Vec<_>>(), vec![1, 0, 1]);
        }
        provider.shutdown().unwrap();
    }
}
