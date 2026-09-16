// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GitStore Server Library
// Structured logging setup using tracing

pub mod auth;
pub mod config;
pub mod git;
pub mod grpc;

use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt, EnvFilter};

/// Initialize structured logging with configured defaults and optional RUST_LOG filtering.
pub fn init_logging(
    log_level: &str,
    log_format: &str,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let default_filter = configured_filter(log_level);
    let filter = EnvFilter::try_from_default_env().unwrap_or(default_filter);

    match log_format.to_ascii_lowercase().as_str() {
        "json" => tracing_subscriber::registry()
            .with(filter)
            .with(tracing_subscriber::fmt::layer().json())
            .try_init()
            .or_else(ignore_already_initialized),
        "text" => tracing_subscriber::registry()
            .with(filter)
            .with(tracing_subscriber::fmt::layer())
            .try_init()
            .or_else(ignore_already_initialized),
        _ => Err(format!("invalid log format {log_format:?}; valid values: json, text").into()),
    }
}

fn configured_filter(log_level: &str) -> EnvFilter {
    EnvFilter::try_new(log_level).unwrap_or_else(|_| EnvFilter::new("info"))
}

fn ignore_already_initialized(
    err: tracing_subscriber::util::TryInitError,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    if err
        .to_string()
        .contains("global default trace dispatcher has already been set")
    {
        Ok(())
    } else {
        Err(Box::new(err))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_json_logging_initialization() {
        init_logging("info", "json").expect("json logging should initialize");
    }

    #[test]
    fn test_text_logging_initialization() {
        init_logging("debug", "text").expect("text logging should initialize");
    }

    #[test]
    fn configured_filter_does_not_promote_gitstore_targets() {
        assert_eq!(configured_filter("info").to_string(), "info");
        assert_eq!(configured_filter("warn").to_string(), "warn");
    }

    #[test]
    fn configured_filter_falls_back_to_info_for_invalid_levels() {
        assert_eq!(configured_filter("gitstore[invalid").to_string(), "info");
    }
}
