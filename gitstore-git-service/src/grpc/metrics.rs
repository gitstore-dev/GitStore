// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Per-RPC Prometheus metrics for the gRPC server.
// Uses a Tower middleware layer that intercepts each RPC and records
// grpc_server_handled_total and grpc_server_handling_seconds.

use prometheus::{register_histogram_vec, register_int_counter_vec, HistogramVec, IntCounterVec};
use std::sync::OnceLock;

static HANDLED_TOTAL: OnceLock<IntCounterVec> = OnceLock::new();
static HANDLING_SECONDS: OnceLock<HistogramVec> = OnceLock::new();

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
