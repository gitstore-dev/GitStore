// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use opentelemetry_sdk::metrics::SdkMeterProvider;

// The former Rust Prometheus default registry supplied process metrics only on
// Linux. Preserve that platform contract using SDK observations of OS values.
pub fn register(provider: &SdkMeterProvider) {
    #[cfg(target_os = "linux")]
    process::register_linux(provider);
    #[cfg(not(target_os = "linux"))]
    let _ = provider;
}

#[cfg(any(target_os = "linux", test))]
mod process {
    use super::*;
    use opentelemetry::metrics::MeterProvider;
    use std::sync::Arc;

    const SIGNALS: [(&str, &str); 7] = [
        ("process_cpu_seconds_total", "s"),
        ("process_open_fds", "{file}"),
        ("process_max_fds", "{file}"),
        ("process_virtual_memory_bytes", "By"),
        ("process_resident_memory_bytes", "By"),
        ("process_start_time_seconds", "s"),
        ("process_threads", "{thread}"),
    ];

    #[derive(Clone, Copy, Default)]
    struct Snapshot([Option<u64>; 7]);

    fn register_source(
        provider: &SdkMeterProvider,
        source: impl Fn() -> Snapshot + Send + Sync + 'static,
    ) {
        let meter = provider.meter("gitstore/runtime");
        let source = Arc::new(source);
        for (index, (name, unit)) in SIGNALS.into_iter().enumerate() {
            let source = source.clone();
            if index == 0 {
                meter
                    .u64_observable_counter(name)
                    .with_unit(unit)
                    .with_callback(move |observer| {
                        if let Some(value) = source().0[index] {
                            observer.observe(value, &[]);
                        }
                    })
                    .build();
            } else {
                meter
                    .u64_observable_gauge(name)
                    .with_unit(unit)
                    .with_callback(move |observer| {
                        if let Some(value) = source().0[index] {
                            observer.observe(value, &[]);
                        }
                    })
                    .build();
            }
        }
    }

    #[cfg(target_os = "linux")]
    pub(super) fn register_linux(provider: &SdkMeterProvider) {
        use std::{
            sync::Mutex,
            time::{Duration, Instant},
        };
        // One bounded OS snapshot per collection rather than seven scans.
        let cache = Mutex::new(None::<(Instant, Snapshot)>);
        register_source(provider, move || {
            let Ok(mut cache) = cache.lock() else {
                return Snapshot::default();
            };
            if let Some((at, snapshot)) = *cache {
                if at.elapsed() < Duration::from_secs(1) {
                    return snapshot;
                }
            }
            let snapshot = read_linux();
            *cache = Some((Instant::now(), snapshot));
            snapshot
        });
    }

    #[cfg(target_os = "linux")]
    fn read_linux() -> Snapshot {
        use procfs::process::{LimitValue, Process};
        let mut values = Snapshot::default();
        let Ok(process) = Process::myself() else {
            return values;
        };
        if let Ok(stat) = process.stat() {
            let ticks = procfs::ticks_per_second().max(1);
            // Retain the former collector's whole-second CPU/start values.
            values.0[0] = Some((stat.utime + stat.stime) / ticks);
            values.0[3] = Some(stat.vsize);
            values.0[4] = Some(stat.rss.saturating_mul(procfs::page_size()));
            values.0[5] = procfs::boot_time_secs()
                .ok()
                .map(|boot| boot + stat.starttime / ticks);
            values.0[6] = u64::try_from(stat.num_threads).ok();
        }
        if let Ok(limits) = process.limits() {
            values.0[2] = Some(match limits.max_open_files.soft_limit {
                LimitValue::Value(value) => value,
                LimitValue::Unlimited => 0,
            });
        }
        values.0[1] = fd_count(std::time::Instant::now() + std::time::Duration::from_secs(2));
        values
    }

    #[cfg(target_os = "linux")]
    fn fd_count(deadline: std::time::Instant) -> Option<u64> {
        let entries = std::fs::read_dir("/proc/self/fd").ok()?;
        let mut count = 0_u64;
        for entry in entries {
            if count.is_multiple_of(256) && std::time::Instant::now() >= deadline {
                return None;
            }
            entry.ok()?;
            count += 1;
        }
        // The directory descriptor used by this observation is not workload.
        Some(count.saturating_sub(1))
    }

    #[cfg(test)]
    mod tests {
        use super::*;
        use opentelemetry_sdk::metrics::{
            data::{AggregatedMetrics, MetricData},
            InMemoryMetricExporter, PeriodicReader,
        };
        use std::time::Duration;

        #[test]
        fn sdk_process_observations_preserve_names_values_and_monotonicity() {
            let exporter = InMemoryMetricExporter::default();
            let provider = SdkMeterProvider::builder()
                .with_reader(
                    PeriodicReader::builder(exporter.clone())
                        .with_interval(Duration::from_secs(3600))
                        .build(),
                )
                .build();
            let values = [12, 4, 2048, 4096, 1024, 100, 3];
            register_source(&provider, move || Snapshot(values.map(Some)));
            for _ in 0..2 {
                provider.force_flush().unwrap();
                let batches = exporter.get_finished_metrics().unwrap();
                let metrics: Vec<_> = batches
                    .last()
                    .unwrap()
                    .scope_metrics()
                    .flat_map(|scope| scope.metrics())
                    .collect();
                assert_eq!(metrics.len(), SIGNALS.len());
                for (index, (name, unit)) in SIGNALS.iter().enumerate() {
                    let metric = metrics
                        .iter()
                        .find(|metric| metric.name() == *name)
                        .unwrap();
                    assert_eq!(metric.unit(), *unit);
                    if index == 0 {
                        let AggregatedMetrics::U64(MetricData::Sum(sum)) = metric.data() else {
                            panic!("expected CPU counter")
                        };
                        assert!(sum.is_monotonic());
                        assert_eq!(sum.data_points().next().unwrap().value(), values[index]);
                    } else {
                        let AggregatedMetrics::U64(MetricData::Gauge(gauge)) = metric.data() else {
                            panic!("expected process gauge")
                        };
                        let point = gauge.data_points().next().unwrap();
                        assert_eq!(point.value(), values[index]);
                        assert_eq!(point.attributes().count(), 0);
                    }
                }
            }
            provider.shutdown().unwrap();
        }

        #[cfg(target_os = "linux")]
        #[test]
        fn linux_snapshot_reads_current_process_and_bounds_fd_work() {
            let snapshot = read_linux();
            assert!(snapshot.0.iter().all(Option::is_some));
            assert!(snapshot.0[3].unwrap() > 0);
            assert!(snapshot.0[4].unwrap() > 0);
            assert!(snapshot.0[5].unwrap() > 0);
            assert!(snapshot.0[6].unwrap() > 0);
            assert!(fd_count(std::time::Instant::now()).is_none());
        }
    }
}
