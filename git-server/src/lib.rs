// GitStore Server Library
// Structured logging setup using tracing

pub mod git;
pub mod models;
pub mod validation;
pub mod websocket;

use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt, EnvFilter};

/// Initialize structured logging with environment-based filtering
pub fn init_logging() {
    tracing_subscriber::registry()
        .with(
            EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| EnvFilter::new("info,gitstore=debug")),
        )
        .with(tracing_subscriber::fmt::layer())
        .init();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_logging_initialization() {
        // Test that logging can be initialized without panic
        init_logging();
    }
}
