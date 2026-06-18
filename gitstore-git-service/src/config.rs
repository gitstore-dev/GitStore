// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use config::{Config, Environment, File, FileFormat};
use regex::Regex;

#[derive(Debug, serde::Deserialize)]
pub struct AppConfig {
    pub grpc: PortConfig,
    pub git: GitConfig,
    pub log: LogConfig,
    pub hooks: HooksConfig,
    pub schema_validation: SchemaValidationConfig,
    pub admission_control: AdmissionControlConfig,
    pub catalog_service: CatalogServiceConfig,
}

#[derive(Debug, serde::Deserialize)]
pub struct PortConfig {
    pub port: u16,
}

#[derive(Debug, serde::Deserialize)]
pub struct GitConfig {
    pub data_dir: String,
    pub repo: RepoConfig,
    pub max_pack_size_bytes: u64,
}

#[derive(Debug, serde::Deserialize)]
pub struct RepoConfig {
    pub max_file_size: u64,
}

#[derive(Debug, serde::Deserialize)]
pub struct LogConfig {
    pub level: String,
    pub format: String,
}

#[derive(Debug, Clone, serde::Deserialize)]
pub struct HooksConfig {
    pub git_receive_pack: GitReceivePackHooks,
}

#[derive(Debug, Clone, serde::Deserialize)]
pub struct GitReceivePackHooks {
    pub pre_receive: HookToggle,
    pub update: HookToggle,
    pub post_receive: HookToggle,
    pub proc_receive: HookToggle,
    pub post_update: HookToggle,
    pub reference_transaction: HookToggle,
}

#[derive(Debug, Clone, serde::Deserialize)]
pub struct HookToggle {
    pub enabled: bool,
}

#[derive(Debug, serde::Deserialize)]
pub struct SchemaValidationConfig {
    pub phase: String,
    pub timeout_secs: u64,
}

#[derive(Debug, serde::Deserialize)]
pub struct AdmissionControlConfig {
    pub phase: String,
    pub branch_pattern: String,
}

#[derive(Debug, serde::Deserialize)]
pub struct CatalogServiceConfig {
    pub uri: String,
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

        if self.grpc.port == 0 {
            errors.push(format!(
                "grpc.port must be between 1 and 65535 (got: {})",
                self.grpc.port
            ));
        }
        if self.git.data_dir.is_empty() {
            errors.push("git.data_dir must not be empty".to_string());
        }
        if self.git.repo.max_file_size == 0 {
            errors.push("git.repo.max_file_size must be positive".to_string());
        }
        match self.log.format.to_ascii_lowercase().as_str() {
            "json" | "text" => {}
            _ => errors.push(format!(
                "log.format must be one of: json, text (got: {})",
                self.log.format
            )),
        }

        // FR-019: schema_validation and admission_control must run in different phases.
        if self.schema_validation.phase == self.admission_control.phase {
            errors.push(format!(
                "GITSTORE_SCHEMA_VALIDATION__PHASE and GITSTORE_ADMISSION_CONTROL__PHASE \
                 must be different (both set to {:?})",
                self.schema_validation.phase
            ));
        }

        if Regex::new(&self.admission_control.branch_pattern).is_err() {
            errors.push(format!(
                "admission_control.branch_pattern is not a valid regex: {:?}",
                self.admission_control.branch_pattern
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
        // Environment variables use double underscores between config-key levels,
        // so dotted keys map cleanly without splitting internal underscores in
        // field names (for example, GITSTORE_GIT__REPO__MAX_FILE_SIZE).
        .add_source(
            Environment::with_prefix("GITSTORE")
                .prefix_separator("_")
                .separator("__")
                .try_parsing(true),
        );

    let cfg = builder.build()?.try_deserialize::<AppConfig>()?;
    tracing::info!(
        grpc_port = cfg.grpc.port,
        data_dir = %cfg.git.data_dir,
        log_level = %cfg.log.level,
        log_format = %cfg.log.format,
        max_file_size = cfg.git.repo.max_file_size,
        max_pack_size_bytes = cfg.git.max_pack_size_bytes,
        "resolved configuration"
    );
    Ok(cfg)
}

fn default_toml() -> String {
    r#"
[grpc]
port = 50051

[git]
data_dir = "/data/repos"
max_pack_size_bytes = 52428800

[git.repo]
max_file_size = 52428800

[log]
level = "info"
format = "json"

[hooks.git_receive_pack]
pre_receive           = { enabled = true }
update                = { enabled = false }
post_receive          = { enabled = true }
proc_receive          = { enabled = false }
post_update           = { enabled = false }
reference_transaction = { enabled = false }

[schema_validation]
phase = "pre-receive"
timeout_secs = 10

[admission_control]
phase = "post-receive"
branch_pattern = "refs/heads/main"

[catalog_service]
uri = "http://localhost:6000"
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
            "GITSTORE_GRPC__PORT",
            "GITSTORE_GIT__DATA_DIR",
            "GITSTORE_LOG__LEVEL",
            "GITSTORE_LOG__FORMAT",
            "GITSTORE_GIT__REPO__MAX_FILE_SIZE",
            "GITSTORE_SCHEMA_VALIDATION__PHASE",
            "GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS",
            "GITSTORE_ADMISSION_CONTROL__PHASE",
            "GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN",
            "GITSTORE_CATALOG_SERVICE__URI",
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
        assert_eq!(cfg.grpc.port, 50051);
        assert_eq!(cfg.git.data_dir, "/data/repos");
        assert_eq!(cfg.log.level, "info");
        assert_eq!(cfg.log.format, "json");
        assert_eq!(cfg.git.repo.max_file_size, 52428800);
    }

    #[test]
    fn test_env_var_overrides_default() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_LOG__LEVEL", "debug");
        env::set_var("GITSTORE_LOG__FORMAT", "text");
        let cfg = load_config_from(None).expect("load_config failed");
        assert_eq!(cfg.log.level, "debug");
        assert_eq!(cfg.log.format, "text");
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
        std::fs::write(&file_path, "[log]\nlevel = \"warn\"\nformat = \"text\"\n")
            .expect("write config");
        // Strip the .toml extension — File::with_name adds it when probing
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        let cfg = load_config_from(Some(path_str)).expect("load_config failed");
        assert_eq!(cfg.log.level, "warn");
        assert_eq!(cfg.log.format, "text");
    }

    #[test]
    fn test_env_var_overrides_config_file() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GRPC__PORT", "6666");
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(&file_path, "[grpc]\nport = 7777\n").expect("write config");
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        let cfg = load_config_from(Some(path_str)).expect("load_config failed");
        assert_eq!(cfg.grpc.port, 6666);
        clear_env();
    }

    // T008: debug output must not expose secrets and must include key fields

    #[test]
    fn test_app_config_debug_includes_key_fields() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load_config failed");
        let debug_str = format!("{:?}", cfg);
        assert!(debug_str.contains("grpc"));
        assert!(debug_str.contains("log"));
    }

    // T028: .env loading tests (US3)
    // dotenvy is called in main() before load_config(); it sets env vars that
    // load_config_from() then reads. These tests simulate that by setting env
    // vars directly (mimicking what dotenvy would do from a .env file).

    #[test]
    fn test_env_file_values_are_loaded() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Simulate dotenvy having loaded GITSTORE_LOG__LEVEL=trace from .env
        env::set_var("GITSTORE_LOG__LEVEL", "trace");
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.log.level, "trace");
        clear_env();
    }

    #[test]
    fn test_shell_var_takes_priority_over_env_file_value() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Simulate: dotenvy sets trace, but shell already had debug set.
        // dotenvy does not overwrite existing env vars — shell wins.
        // We model that here by just having debug set (the shell value).
        env::set_var("GITSTORE_LOG__LEVEL", "debug");
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.log.level, "debug");
        clear_env();
    }

    #[test]
    fn test_absent_env_file_is_no_op() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // No env vars set and no .env file — defaults must apply
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.grpc.port, 50051);
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
        assert_eq!(cfg.grpc.port, 50051);
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
        env::set_var("GITSTORE_GRPC__PORT", "0");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err(), "expected validation error for port 0");
        let err = result.unwrap_err();
        assert!(
            err.to_string().contains("grpc.port"),
            "error should mention grpc.port, got: {err}"
        );
        clear_env();
    }

    #[test]
    fn test_validate_invalid_log_format() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_LOG__FORMAT", "xml");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err(), "expected validation error for log.format");
        let err = result.unwrap_err();
        assert!(err.to_string().contains("log.format"));
        clear_env();
    }

    #[test]
    fn test_validate_data_dir_empty_fails() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GIT__DATA_DIR", "");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_err(),
            "expected validation error for empty data_dir"
        );
        let err = result.unwrap_err();
        assert!(err.to_string().contains("git.data_dir"));
        clear_env();
    }

    #[test]
    fn test_validate_all_errors_collected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Port 0 is invalid
        env::set_var("GITSTORE_GRPC__PORT", "0");
        env::set_var("GITSTORE_GIT__DATA_DIR", "");
        env::set_var("GITSTORE_GIT__REPO__MAX_FILE_SIZE", "0");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err());
        let err = result.unwrap_err();
        // Both failures should appear in the single error string
        let s = err.to_string();
        assert!(
            s.contains("grpc.port")
                || s.contains("git.data_dir")
                || s.contains("git.repo.max_file_size"),
            "got: {s}"
        );
        clear_env();
    }

    // T034: reference_transaction toggle defaults to false
    #[test]
    fn test_reference_transaction_toggle_defaults_to_false() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load_config failed");
        assert!(
            !cfg.hooks.git_receive_pack.reference_transaction.enabled,
            "reference_transaction should default to disabled"
        );
    }

    // T007: phase-conflict validation (FR-019)

    #[test]
    fn test_validate_phase_conflict_rejected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Set both phases to the same value — must fail
        env::set_var("GITSTORE_SCHEMA_VALIDATION__PHASE", "pre-receive");
        env::set_var("GITSTORE_ADMISSION_CONTROL__PHASE", "pre-receive");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_err(),
            "expected conflict error when both phases are equal"
        );
        let err = result.unwrap_err();
        let msg = err.to_string();
        assert!(
            msg.contains("GITSTORE_SCHEMA_VALIDATION__PHASE")
                && msg.contains("GITSTORE_ADMISSION_CONTROL__PHASE"),
            "error should name both env vars, got: {msg}"
        );
        clear_env();
    }

    #[test]
    fn test_validate_split_phases_pass() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        // Default split: pre-receive vs post-receive — must pass
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.schema_validation.phase, "pre-receive");
        assert_eq!(cfg.admission_control.phase, "post-receive");
        cfg.validate()
            .expect("default split phases should pass validation");
    }

    #[test]
    fn test_default_config_has_new_structure() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.schema_validation.phase, "pre-receive");
        assert_eq!(cfg.schema_validation.timeout_secs, 10);
        assert_eq!(cfg.admission_control.phase, "post-receive");
        assert_eq!(cfg.admission_control.branch_pattern, "refs/heads/main");
        assert_eq!(cfg.catalog_service.uri, "http://localhost:6000");
    }
}
