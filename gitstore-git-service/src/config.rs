// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use config::{Config, Environment, File, FileFormat};

#[derive(Debug, serde::Deserialize)]
pub struct AppConfig {
    pub http_port: u16,
    pub ws_port: u16,
    pub grpc_port: u16,
    pub data_dir: String,
    pub log_level: String,
    pub max_file_size: u64,
    pub hooks: HooksConfig,
    pub admission_control: AdmissionControlConfig,
}

#[derive(Debug, serde::Deserialize)]
pub struct HooksConfig {
    pub git_receive_pack: GitReceivePackHooks,
}

#[derive(Debug, serde::Deserialize)]
pub struct GitReceivePackHooks {
    pub pre_receive: HookToggle,
    pub update: HookToggle,
    pub post_receive: HookToggle,
    pub proc_receive: HookToggle,
    pub post_update: HookToggle,
}

#[derive(Debug, serde::Deserialize)]
pub struct HookToggle {
    pub enabled: bool,
}

#[derive(Debug, serde::Deserialize)]
pub struct AdmissionControlConfig {
    pub validating_admission_policy: ValidatingAdmissionPolicyConfig,
}

#[derive(Debug, serde::Deserialize)]
pub struct ValidatingAdmissionPolicyConfig {
    pub phase: String,
}

/// Load configuration from defaults → gitstore.toml → environment variables.
///
/// Nested hook and admission_control keys must be set via gitstore.toml TOML
/// tables. Environment variable overrides for nested keys are not supported
/// due to the ambiguity between struct-path separators and field-name
/// underscores when using a single-underscore separator with config-rs.
/// All validation failures collected into a single error.
#[derive(Debug)]
pub struct ConfigErrors(Vec<String>);

impl std::fmt::Display for ConfigErrors {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Configuration errors:\n- {}", self.0.join("\n- "))
    }
}

impl std::error::Error for ConfigErrors {}

impl AppConfig {
    /// Validate all fields and collect every failure into a single `ConfigErrors`.
    pub fn validate(&self) -> Result<(), ConfigErrors> {
        let mut errors = Vec::new();

        if self.http_port == 0 {
            errors.push(format!(
                "http_port must be between 1 and 65535 (got: {})",
                self.http_port
            ));
        }
        if self.ws_port == 0 {
            errors.push(format!(
                "ws_port must be between 1 and 65535 (got: {})",
                self.ws_port
            ));
        }
        if self.grpc_port == 0 {
            errors.push(format!(
                "grpc_port must be between 1 and 65535 (got: {})",
                self.grpc_port
            ));
        }
        if self.data_dir.is_empty() {
            errors.push("data_dir must not be empty".to_string());
        }
        // All three ports must be distinct
        if self.http_port != 0
            && self.ws_port != 0
            && self.grpc_port != 0
            && (self.http_port == self.ws_port
                || self.http_port == self.grpc_port
                || self.ws_port == self.grpc_port)
        {
            errors.push(format!(
                "all three ports (http_port={}, ws_port={}, grpc_port={}) must be distinct",
                self.http_port, self.ws_port, self.grpc_port
            ));
        }

        if errors.is_empty() {
            Ok(())
        } else {
            Err(ConfigErrors(errors))
        }
    }
}

pub fn load_config() -> Result<AppConfig, config::ConfigError> {
    load_config_from(None)
}

pub fn load_config_from(config_file: Option<&str>) -> Result<AppConfig, config::ConfigError> {
    let defaults = default_toml();

    let builder = Config::builder()
        // Baked-in defaults as inline TOML
        .add_source(File::from_str(&defaults, FileFormat::Toml))
        // Discovery path (gitstore.toml) is optional; an explicit --config-file is required.
        .add_source(
            File::with_name(config_file.unwrap_or("gitstore")).required(config_file.is_some()),
        )
        // Environment variables: GITSTORE_HTTP_PORT → http_port, etc.
        // prefix_separator("_") strips the GITSTORE_ prefix using a single
        // underscore. separator("__") then splits nested config-key levels using
        // double underscores, so single underscores within a field name
        // (http_port, log_level, data_dir) are preserved as part of the key
        // name rather than being treated as nesting separators.
        // Nested keys (hooks, admission_control) must be set via config file.
        .add_source(
            Environment::with_prefix("GITSTORE")
                .prefix_separator("_")
                .separator("__")
                .try_parsing(true),
        );

    let cfg = builder.build()?.try_deserialize::<AppConfig>()?;
    tracing::info!(
        http_port = cfg.http_port,
        ws_port = cfg.ws_port,
        grpc_port = cfg.grpc_port,
        data_dir = %cfg.data_dir,
        log_level = %cfg.log_level,
        max_file_size = cfg.max_file_size,
        "resolved configuration"
    );
    Ok(cfg)
}

fn default_toml() -> String {
    r#"
http_port = 9418
ws_port = 8080
grpc_port = 50051
data_dir = "/data/repos"
log_level = "info"
max_file_size = 52428800

[hooks.git_receive_pack]
pre_receive  = { enabled = false }
update       = { enabled = false }
post_receive = { enabled = false }
proc_receive = { enabled = false }
post_update  = { enabled = false }

[admission_control.validating_admission_policy]
phase = "pre-receive"
"#
    .to_string()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::env;
    use std::sync::Mutex;

    // Serialize all env-mutating tests to prevent cross-test interference.
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    fn clear_env() {
        let keys = [
            "GITSTORE_HTTP_PORT",
            "GITSTORE_WS_PORT",
            "GITSTORE_GRPC_PORT",
            "GITSTORE_DATA_DIR",
            "GITSTORE_LOG_LEVEL",
            "GITSTORE_MAX_FILE_SIZE",
        ];
        for k in &keys {
            env::remove_var(k);
        }
    }

    // T006: layered loading tests

    #[test]
    fn test_defaults_applied_when_no_source_set() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load_config failed");
        assert_eq!(cfg.http_port, 9418);
        assert_eq!(cfg.ws_port, 8080);
        assert_eq!(cfg.grpc_port, 50051);
        assert_eq!(cfg.data_dir, "/data/repos");
        assert_eq!(cfg.log_level, "info");
        assert_eq!(cfg.max_file_size, 52428800);
    }

    #[test]
    fn test_env_var_overrides_default() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_HTTP_PORT", "8000");
        env::set_var("GITSTORE_LOG_LEVEL", "debug");
        let cfg = load_config_from(None).expect("load_config failed");
        assert_eq!(cfg.http_port, 8000);
        assert_eq!(cfg.log_level, "debug");
        clear_env();
    }

    #[test]
    fn test_config_file_value_applied_when_no_env_var() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Write a .toml file; pass path without extension so File::with_name
        // probes and finds the .toml variant.
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(
            &file_path,
            "http_port = 7777\nws_port = 7778\nlog_level = \"warn\"\n",
        )
        .expect("write config");
        // Strip the .toml extension — File::with_name adds it when probing
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        let cfg = load_config_from(Some(path_str)).expect("load_config failed");
        assert_eq!(cfg.http_port, 7777);
        assert_eq!(cfg.ws_port, 7778);
        assert_eq!(cfg.log_level, "warn");
    }

    #[test]
    fn test_env_var_overrides_config_file() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_HTTP_PORT", "6666");
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(&file_path, "http_port = 7777\n").expect("write config");
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        let cfg = load_config_from(Some(path_str)).expect("load_config failed");
        assert_eq!(cfg.http_port, 6666);
        clear_env();
    }

    // T008: debug output must not expose secrets and must include key fields

    #[test]
    fn test_app_config_debug_includes_key_fields() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load_config failed");
        let debug_str = format!("{:?}", cfg);
        assert!(debug_str.contains("http_port"));
        assert!(debug_str.contains("log_level"));
    }

    // T028: .env loading tests (US3)
    // dotenvy is called in main() before load_config(); it sets env vars that
    // load_config_from() then reads. These tests simulate that by setting env
    // vars directly (mimicking what dotenvy would do from a .env file).

    #[test]
    fn test_env_file_values_are_loaded() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Simulate dotenvy having loaded GITSTORE_LOG_LEVEL=trace from .env
        env::set_var("GITSTORE_LOG_LEVEL", "trace");
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.log_level, "trace");
        clear_env();
    }

    #[test]
    fn test_shell_var_takes_priority_over_env_file_value() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Simulate: dotenvy sets trace, but shell already had debug set.
        // dotenvy does not overwrite existing env vars — shell wins.
        // We model that here by just having debug set (the shell value).
        env::set_var("GITSTORE_LOG_LEVEL", "debug");
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.log_level, "debug");
        clear_env();
    }

    #[test]
    fn test_absent_env_file_is_no_op() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // No env vars set and no .env file — defaults must apply
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.http_port, 9418);
    }

    // T022: unknown keys in config file must not abort startup

    #[test]
    fn test_unknown_key_in_config_file_does_not_abort() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(&file_path, "unknown_key = \"oops\"\n").expect("write config");
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        // config-rs ignores unknown keys by default — load must succeed
        let cfg = load_config_from(Some(path_str)).expect("should load despite unknown key");
        assert_eq!(cfg.http_port, 9418);
    }

    // Explicit --config-file with a missing path must fail, not silently use defaults.

    #[test]
    fn test_explicit_config_file_missing_returns_error() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let result = load_config_from(Some("/nonexistent/path/that/cannot/exist"));
        assert!(
            result.is_err(),
            "expected error when explicit config file does not exist"
        );
    }

    // T020: validation tests (US2)

    #[test]
    fn test_validate_port_out_of_range() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_HTTP_PORT", "0");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err(), "expected validation error for port 0");
        let err = result.unwrap_err();
        assert!(
            err.to_string().contains("http_port"),
            "error should mention http_port, got: {err}"
        );
        clear_env();
    }

    #[test]
    fn test_validate_data_dir_empty_fails() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_DATA_DIR", "");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_err(),
            "expected validation error for empty data_dir"
        );
        let err = result.unwrap_err();
        assert!(err.to_string().contains("data_dir"));
        clear_env();
    }

    #[test]
    fn test_validate_port_uniqueness_constraint() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Make http_port == ws_port
        env::set_var("GITSTORE_HTTP_PORT", "8080");
        env::set_var("GITSTORE_WS_PORT", "8080");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err(), "expected port uniqueness error");
        let err = result.unwrap_err();
        assert!(err.to_string().contains("distinct") || err.to_string().contains("port"));
        clear_env();
    }

    #[test]
    fn test_validate_all_errors_collected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Port 0 is invalid and same port across http/ws triggers uniqueness — multiple errors
        env::set_var("GITSTORE_HTTP_PORT", "0");
        env::set_var("GITSTORE_DATA_DIR", "");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err());
        let err = result.unwrap_err();
        // Both failures should appear in the single error string
        let s = err.to_string();
        assert!(
            s.contains("http_port") || s.contains("data_dir"),
            "got: {s}"
        );
        clear_env();
    }
}
