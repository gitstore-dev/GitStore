// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use opentelemetry_proto::tonic::{
    collector::metrics::v1::{ExportMetricsServiceRequest, ExportMetricsServiceResponse},
    common::v1::{any_value, AnyValue, KeyValue},
    metrics::v1::{
        metric, number_data_point, AggregationTemporality, Gauge, Histogram, HistogramDataPoint,
        Metric, NumberDataPoint, ResourceMetrics, ScopeMetrics, Sum,
    },
    resource::v1::Resource,
};
use prost::Message;
use std::{
    io::Read,
    sync::mpsc,
    thread::JoinHandle,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

fn attribute(key: &str, value: &str) -> KeyValue {
    KeyValue {
        key: key.into(),
        value: Some(AnyValue {
            value: Some(any_value::Value::StringValue(value.into())),
        }),
        ..Default::default()
    }
}

fn collect(resource: Resource, started: u64) -> ExportMetricsServiceRequest {
    use prometheus::proto::MetricType;
    let now = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos() as u64;
    let mut metrics = Vec::new();
    let mut series = 0;
    for family in prometheus::gather() {
        let mut numbers = Vec::new();
        let mut histograms = Vec::new();
        for sample in family.get_metric() {
            if series >= 2048
                || sample.get_label().iter().any(|label| {
                    !matches!(
                        label.name(),
                        "grpc_method" | "grpc_code" | "signal" | "stage"
                    ) || label.value().len() > 128
                })
            {
                super::telemetry_records("metrics", "dropped", 1);
                continue;
            }
            series += 1;
            let attributes = sample
                .get_label()
                .iter()
                .map(|label| attribute(label.name(), label.value()))
                .collect();
            if family.get_field_type() == MetricType::HISTOGRAM {
                let h = sample.get_histogram();
                let mut previous = 0;
                let mut buckets = Vec::new();
                let mut bounds = Vec::new();
                for bucket in h.get_bucket() {
                    if bucket.upper_bound().is_finite() {
                        bounds.push(bucket.upper_bound());
                        buckets.push(bucket.cumulative_count() - previous);
                        previous = bucket.cumulative_count();
                    }
                }
                buckets.push(h.sample_count() - previous);
                histograms.push(HistogramDataPoint {
                    attributes,
                    start_time_unix_nano: started,
                    time_unix_nano: now,
                    count: h.sample_count(),
                    sum: Some(h.sample_sum()),
                    explicit_bounds: bounds,
                    bucket_counts: buckets,
                    ..Default::default()
                });
            } else {
                let value = match family.get_field_type() {
                    MetricType::COUNTER => sample.get_counter().value(),
                    MetricType::GAUGE => sample.get_gauge().value(),
                    _ => continue,
                };
                numbers.push(NumberDataPoint {
                    attributes,
                    start_time_unix_nano: started,
                    time_unix_nano: now,
                    value: Some(number_data_point::Value::AsDouble(value)),
                    ..Default::default()
                });
            }
        }
        let data = match family.get_field_type() {
            MetricType::HISTOGRAM => metric::Data::Histogram(Histogram {
                data_points: histograms,
                aggregation_temporality: AggregationTemporality::Cumulative as i32,
            }),
            MetricType::COUNTER => metric::Data::Sum(Sum {
                data_points: numbers,
                aggregation_temporality: AggregationTemporality::Cumulative as i32,
                is_monotonic: true,
            }),
            MetricType::GAUGE => metric::Data::Gauge(Gauge {
                data_points: numbers,
            }),
            _ => continue,
        };
        metrics.push(Metric {
            name: family.name().into(),
            description: family.help().into(),
            data: Some(data),
            ..Default::default()
        });
    }
    ExportMetricsServiceRequest {
        resource_metrics: vec![ResourceMetrics {
            resource: Some(resource),
            scope_metrics: vec![ScopeMetrics {
                metrics,
                ..Default::default()
            }],
            ..Default::default()
        }],
    }
}

pub struct MetricExport {
    stop: mpsc::Sender<()>,
    worker: JoinHandle<anyhow::Result<()>>,
}

impl MetricExport {
    pub fn start(endpoint: &str, resource: &opentelemetry_sdk::Resource) -> Self {
        let endpoint = format!("{}/v1/metrics", endpoint.trim_end_matches('/'));
        let resource = Resource {
            attributes: resource
                .iter()
                .map(|(key, value)| attribute(key.as_str(), &value.to_string()))
                .collect(),
            ..Default::default()
        };
        let (stop, receiver) = mpsc::channel();
        let worker = std::thread::spawn(move || {
            let client = reqwest::blocking::Client::builder()
                .timeout(Duration::from_secs(2))
                .connect_timeout(Duration::from_secs(2))
                .pool_max_idle_per_host(1)
                .redirect(reqwest::redirect::Policy::none())
                .build()?;
            let started = SystemTime::now().duration_since(UNIX_EPOCH)?.as_nanos() as u64;
            loop {
                let stopping = receiver.recv_timeout(Duration::from_secs(10))
                    != Err(mpsc::RecvTimeoutError::Timeout);
                let batch = collect(resource.clone(), started);
                let result = if batch.encoded_len() > 2 << 20 {
                    Err(anyhow::anyhow!("metrics export exceeds size limit"))
                } else {
                    (|| -> anyhow::Result<()> {
                        let response = client
                            .post(&endpoint)
                            .header("Content-Type", "application/x-protobuf")
                            .body(batch.encode_to_vec())
                            .send()
                            .and_then(|response| response.error_for_status())
                            .map_err(|_| anyhow::anyhow!("collector rejected metrics"))?;
                        let mut reply = Vec::new();
                        response.take(65537).read_to_end(&mut reply)?;
                        if reply.len() > 65536 {
                            anyhow::bail!("metrics response exceeds size limit");
                        }
                        let result = ExportMetricsServiceResponse::decode(reply.as_slice())?;
                        if result
                            .partial_success
                            .is_some_and(|partial| partial.rejected_data_points > 0)
                        {
                            anyhow::bail!("collector rejected metrics data points");
                        }
                        Ok(())
                    })()
                };
                super::telemetry_records(
                    "metrics",
                    if result.is_ok() { "exported" } else { "failed" },
                    1,
                );
                if stopping {
                    return result;
                }
            }
        });
        Self { stop, worker }
    }

    pub fn shutdown(self) -> anyhow::Result<()> {
        let _ = self.stop.send(());
        self.worker
            .join()
            .map_err(|_| anyhow::anyhow!("metrics exporter stopped unexpectedly"))?
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn native_histograms_remain_cumulative_without_a_listener() {
        let histogram =
            prometheus::register_histogram!("otel_export_test_seconds", "test", vec![1.0, 2.0])
                .unwrap();
        histogram.observe(0.5);
        histogram.observe(3.0);
        let batch = collect(Resource::default(), 1);
        let metric = batch.resource_metrics[0].scope_metrics[0]
            .metrics
            .iter()
            .find(|metric| metric.name == "otel_export_test_seconds")
            .unwrap();
        let Some(metric::Data::Histogram(data)) = &metric.data else {
            panic!("expected histogram")
        };
        assert_eq!(data.data_points[0].bucket_counts, vec![1, 0, 1]);
        assert_eq!(data.data_points[0].explicit_bounds, vec![1.0, 2.0]);
        assert_eq!(data.data_points[0].sum, Some(3.5));
    }
}
