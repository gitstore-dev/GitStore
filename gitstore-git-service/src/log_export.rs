// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use opentelemetry::logs::{AnyValue, LogRecord, Logger, LoggerProvider};
use opentelemetry_sdk::logs::{LogProcessor, SdkLogRecord, SdkLogger, SdkLoggerProvider};
use std::{sync::OnceLock, time::Duration};

#[derive(Debug)]
pub struct SafeLogs;

impl LogProcessor for SafeLogs {
    fn emit(&self, record: &mut SdkLogRecord, _: &opentelemetry::InstrumentationScope) {
        static LOGGER: OnceLock<SdkLogger> = OnceLock::new();
        let logger =
            LOGGER.get_or_init(|| SdkLoggerProvider::builder().build().logger("sanitizer"));
        let mut safe = logger.create_log_record();
        safe.set_body(AnyValue::from("operational event"));
        if let Some(value) = record.timestamp() {
            safe.set_timestamp(value);
        }
        if let Some(value) = record.observed_timestamp() {
            safe.set_observed_timestamp(value);
        }
        if let Some(value) = record.severity_number() {
            safe.set_severity_number(value);
        }
        if let Some(value) = record.severity_text() {
            safe.set_severity_text(value);
        }
        if let Some(value) = record.trace_context() {
            safe.set_trace_context(value.trace_id, value.span_id, value.trace_flags);
        }
        for (key, value) in record.attributes_iter().take(32) {
            if !matches!(
                key.as_str(),
                "rpc.method"
                    | "rpc.code"
                    | "duration_seconds"
                    | "phase"
                    | "stage"
                    | "result"
                    | "count"
            ) {
                continue;
            }
            match value {
                AnyValue::String(value) => {
                    let value: String = value.as_str().chars().take(256).collect();
                    safe.add_attribute(key.clone(), value);
                }
                AnyValue::Int(_) | AnyValue::Double(_) | AnyValue::Boolean(_) => {
                    safe.add_attribute(key.clone(), value.clone());
                }
                _ => {}
            }
        }
        *record = safe;
    }

    fn force_flush(&self) -> opentelemetry_sdk::error::OTelSdkResult {
        Ok(())
    }
    fn shutdown_with_timeout(&self, _: Duration) -> opentelemetry_sdk::error::OTelSdkResult {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn drops_dynamic_messages_and_sensitive_fields_before_queueing() {
        let provider = SdkLoggerProvider::builder().build();
        let logger = provider.logger("test");
        let mut record = logger.create_log_record();
        record.set_body("token=sensitive-resource-payload".into());
        record.add_attribute("Authorization", "sensitive-secret");
        record.add_attribute("rpc.method", "ReceivePack");
        record.set_trace_context(
            opentelemetry::trace::TraceId::from(1u128),
            opentelemetry::trace::SpanId::from(2u64),
            None,
        );
        SafeLogs.emit(&mut record, &opentelemetry::InstrumentationScope::default());
        assert_eq!(record.body(), Some(&AnyValue::from("operational event")));
        assert_eq!(record.attributes_iter().count(), 1);
        assert_eq!(
            record.trace_context().unwrap().span_id,
            opentelemetry::trace::SpanId::from(2u64)
        );
    }
}
