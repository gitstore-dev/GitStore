// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

use config::{Config, Environment, File, FileFormat};
use regex::Regex;
use std::time::Duration;

/// Config grouped by owning service. gitstore-git-service owns everything
/// under `[git_service]`; `[log]`, `[grpc_auth]` and `[push_limits]` are
/// shared with gitstore-api (and, for `log`, every service reading this
/// file).
#[derive(Debug, serde::Deserialize)]
pub struct AppConfig {
    pub git_service: GitServiceConfig,
    pub log: LogConfig,
    pub grpc_auth: GrpcAuthConfig,
    pub push_limits: PushLimitsConfig,
}

#[derive(Debug, serde::Deserialize)]
pub struct GitServiceConfig {
    pub grpc_port: u16,
    pub data_dir: String,
    pub catalog: CatalogConfig,
    pub validation: ValidationConfig,
    pub admission: AdmissionConfig,
}

#[derive(Debug, serde::Deserialize)]
pub struct CatalogConfig {
    /// Configured as `dns:///host:port`; resolved once at load time into an
    /// `http://host:port` URI tonic can dial directly (tonic has no dns
    /// resolver, so unlike Go's grpc.NewClient this does not round-robin
    /// across resolved addresses — it connects to the first result once).
    pub uri: String,
}

#[derive(Debug, serde::Deserialize)]
pub struct ValidationConfig {
    pub timeout: GsDuration,
}

#[derive(Debug, serde::Deserialize)]
pub struct AdmissionConfig {
    pub branch_pattern: String,
}

#[derive(Debug, serde::Deserialize)]
pub struct GrpcAuthConfig {
    pub hmac_secret: String,
    pub hmac_secret_previous: Option<String>,
}

/// Shared static platform push-size ceiling enforced by both gitstore-api
/// (admission) and gitstore-git-service (the actual clamp during
/// receive-pack).
#[derive(Debug, serde::Deserialize)]
pub struct PushLimitsConfig {
    pub max_pack_size: IecSize,
    pub max_file_size: IecSize,
}

#[derive(Debug, serde::Deserialize)]
pub struct LogConfig {
    pub level: String,
    pub format: String,
}

/// Duration string restricted to a single magnitude with unit ms|s|m|h
/// (rejecting humantime's otherwise-permissive `d`/`w` units and multi-unit
/// compounds like "1h30m"), so values are portable with Go's
/// time.ParseDuration grammar.
#[derive(Debug, Clone, Copy)]
pub struct GsDuration(pub Duration);

impl From<GsDuration> for Duration {
    fn from(value: GsDuration) -> Duration {
        value.0
    }
}

static DURATION_PATTERN: std::sync::LazyLock<Regex> =
    std::sync::LazyLock::new(|| Regex::new(r"^[0-9]+(\.[0-9]+)?(ms|s|m|h)$").unwrap());

impl<'de> serde::Deserialize<'de> for GsDuration {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let raw = String::deserialize(deserializer)?;
        if !DURATION_PATTERN.is_match(raw.trim()) {
            return Err(serde::de::Error::custom(format!(
                "{raw:?} is not a valid duration: expected a single magnitude with unit ms, s, m, or h, e.g. \"30s\""
            )));
        }
        let parsed: humantime::Duration = raw.trim().parse().map_err(|e| {
            serde::de::Error::custom(format!("{raw:?} is not a valid duration: {e}"))
        })?;
        Ok(GsDuration(parsed.into()))
    }
}

/// Size string restricted to IEC units (plain "B" or binary-multiple
/// KiB/MiB/GiB/TiB), so values are portable with Go's
/// github.com/docker/go-units parsing.
#[derive(Debug, Clone, Copy)]
pub struct IecSize(pub u64);

static SIZE_PATTERN: std::sync::LazyLock<Regex> =
    std::sync::LazyLock::new(|| Regex::new(r"^[0-9]+(\.[0-9]+)?(B|KiB|MiB|GiB|TiB)$").unwrap());

impl<'de> serde::Deserialize<'de> for IecSize {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        let raw = String::deserialize(deserializer)?;
        if !SIZE_PATTERN.is_match(raw.trim()) {
            return Err(serde::de::Error::custom(format!(
                "{raw:?} is not a valid size: expected IEC units (B, KiB, MiB, GiB, TiB), e.g. \"512KiB\""
            )));
        }
        let parsed: bytesize::ByteSize = raw.trim().parse().map_err(|e: String| {
            serde::de::Error::custom(format!("{raw:?} is not a valid size: {e}"))
        })?;
        Ok(IecSize(parsed.as_u64()))
    }
}

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

        if self.git_service.grpc_port == 0 {
            errors.push(format!(
                "git_service.grpc_port must be between 1 and 65535 (got: {})",
                self.git_service.grpc_port
            ));
        }
        if self.git_service.data_dir.is_empty() {
            errors.push("git_service.data_dir must not be empty".to_string());
        }
        match self.log.format.to_ascii_lowercase().as_str() {
            "json" | "text" => {}
            _ => errors.push(format!(
                "log.format must be one of: json, text (got: {})",
                self.log.format
            )),
        }

        if Regex::new(&self.git_service.admission.branch_pattern).is_err() {
            errors.push(format!(
                "git_service.admission.branch_pattern is not a valid regex: {:?}",
                self.git_service.admission.branch_pattern
            ));
        }

        if self.grpc_auth.hmac_secret.is_empty() {
            errors.push("grpc_auth.hmac_secret must not be empty".to_string());
        }
        if matches!(&self.grpc_auth.hmac_secret_previous, Some(s) if s.is_empty()) {
            errors.push(
                "grpc_auth.hmac_secret_previous must not be empty when set; \
                 unset or remove it to disable the rotation window"
                    .to_string(),
            );
        }

        if self.push_limits.max_pack_size.0 == 0 {
            errors.push("push_limits.max_pack_size must be greater than zero".to_string());
        }
        if self.push_limits.max_file_size.0 == 0 {
            errors.push("push_limits.max_file_size must be greater than zero".to_string());
        }

        if errors.is_empty() {
            Ok(())
        } else {
            Err(ConfigErrors(errors))
        }
    }
}

/// Every TOML path (or env var) gitstore-git-service used to own before the
/// config-grouping refactor. Checked independently of the final merged
/// config state so a renamed/removed key never silently resolves to a
/// default. One file is mounted into every service, so this cannot use a
/// global strict/deny-unknown-fields check — each service checks only the
/// keys it used to own. Ordered most-specific-first so the most helpful
/// match wins.
const LEGACY_KEYS: &[(&str, &str)] = &[
    ("grpc.port", "git_service.grpc_port"),
    ("grpc", "git_service.grpc_port"),
    ("git.data_dir", "git_service.data_dir"),
    (
        "git.repo.max_file_size",
        "push_limits.max_file_size (removed; now a shared static ceiling)",
    ),
    (
        "git.repo.max_pack_size_bytes",
        "push_limits.max_pack_size (removed; now a shared static ceiling)",
    ),
    ("git.repo", "push_limits"),
    ("git", "git_service.data_dir / push_limits"),
    (
        "hooks",
        "removed; pre-receive validation and post-receive admission are always on",
    ),
    (
        "schema_validation.phase",
        "removed; schema validation always runs at pre-receive",
    ),
    (
        "schema_validation.timeout_secs",
        "git_service.validation.timeout",
    ),
    ("schema_validation", "git_service.validation.timeout"),
    (
        "admission_control.phase",
        "removed; admission control always runs at post-receive",
    ),
    (
        "admission_control.branch_pattern",
        "git_service.admission.branch_pattern",
    ),
    ("admission_control", "git_service.admission.branch_pattern"),
    ("catalog_service.uri", "git_service.catalog.uri"),
    ("catalog_service", "git_service.catalog.uri"),
    ("auth.grpc.hmac_secret", "grpc_auth.hmac_secret"),
    (
        "auth.grpc.hmac_secret_previous",
        "grpc_auth.hmac_secret_previous",
    ),
    ("auth.grpc", "grpc_auth"),
    ("auth", "grpc_auth"),
];

fn check_legacy_keys(cfg: &Config) -> Result<(), config::ConfigError> {
    for (path, replacement) in LEGACY_KEYS {
        if cfg.get::<config::Value>(path).is_ok() {
            return Err(config::ConfigError::Message(format!(
                "config: {path} is no longer recognised; use {replacement} instead"
            )));
        }
    }
    Ok(())
}

/// Resolves a configured `dns:///host:port` gRPC URI into a URI tonic can
/// dial directly. tonic has no dns resolver, so git-service resolves once
/// at connect — unlike Go clients (grpc.NewClient), there is no
/// client-side round-robin across resolved addresses.
fn resolve_grpc_uri(field: &str, raw: &str) -> Result<String, config::ConfigError> {
    match raw.strip_prefix("dns:///") {
        Some(host_port) if !host_port.is_empty() => Ok(format!("http://{host_port}")),
        _ => Err(config::ConfigError::Message(format!(
            "{field} {raw:?} is not a valid gRPC URI: expected the \"dns:///host:port\" scheme"
        ))),
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
        // field names (for example, GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE).
        .add_source(
            Environment::with_prefix("GITSTORE")
                .prefix_separator("_")
                .separator("__")
                .try_parsing(true),
        );

    let built = builder.build()?;
    check_legacy_keys(&built)?;
    let mut cfg = built.try_deserialize::<AppConfig>()?;

    cfg.git_service.catalog.uri =
        resolve_grpc_uri("git_service.catalog.uri", &cfg.git_service.catalog.uri)?;

    tracing::info!(
        grpc_port = cfg.git_service.grpc_port,
        data_dir = %cfg.git_service.data_dir,
        log_level = %cfg.log.level,
        log_format = %cfg.log.format,
        max_pack_size = cfg.push_limits.max_pack_size.0,
        max_file_size = cfg.push_limits.max_file_size.0,
        "resolved configuration"
    );
    Ok(cfg)
}

fn default_toml() -> String {
    r#"
[git_service]
grpc_port = 50051
data_dir = "/var/lib/gitstore/repos"

[git_service.catalog]
uri = "dns:///localhost:6000"

[git_service.validation]
timeout = "10s"

[git_service.admission]
branch_pattern = "^refs/heads/main$"

[log]
level = "info"
format = "json"

[grpc_auth]
hmac_secret = ""

[push_limits]
max_pack_size = "512MiB"
max_file_size = "100MiB"
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
            "GITSTORE_GIT_SERVICE__GRPC_PORT",
            "GITSTORE_GIT_SERVICE__DATA_DIR",
            "GITSTORE_LOG__LEVEL",
            "GITSTORE_LOG__FORMAT",
            "GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT",
            "GITSTORE_GIT_SERVICE__ADMISSION__BRANCH_PATTERN",
            "GITSTORE_GIT_SERVICE__CATALOG__URI",
            "GITSTORE_GRPC_AUTH__HMAC_SECRET",
            "GITSTORE_GRPC_AUTH__HMAC_SECRET_PREVIOUS",
            "GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE",
            "GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE",
            // Legacy keys exercised by rejection tests.
            "GITSTORE_GRPC__PORT",
            "GITSTORE_GIT__DATA_DIR",
            "GITSTORE_GIT__REPO__MAX_FILE_SIZE",
            "GITSTORE_GIT__REPO__MAX_PACK_SIZE_BYTES",
            "GITSTORE_SCHEMA_VALIDATION__PHASE",
            "GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS",
            "GITSTORE_ADMISSION_CONTROL__PHASE",
            "GITSTORE_ADMISSION_CONTROL__BRANCH_PATTERN",
            "GITSTORE_CATALOG_SERVICE__URI",
            "GITSTORE_HOOKS__GIT_RECEIVE_PACK__PRE_RECEIVE__ENABLED",
            "GITSTORE_AUTH__GRPC__HMAC_SECRET",
            "GITSTORE_AUTH__GRPC__HMAC_SECRET_PREVIOUS",
        ];
        for k in &keys {
            env::remove_var(k);
        }
    }

    #[test]
    fn test_defaults_applied_when_no_source_set() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load_config failed");
        assert_eq!(cfg.git_service.grpc_port, 50051);
        assert_eq!(cfg.git_service.data_dir, "/var/lib/gitstore/repos");
        assert_eq!(cfg.log.level, "info");
        assert_eq!(cfg.log.format, "json");
        assert_eq!(cfg.push_limits.max_pack_size.0, 512 * 1024 * 1024);
        assert_eq!(cfg.push_limits.max_file_size.0, 100 * 1024 * 1024);
        assert_eq!(
            cfg.git_service.validation.timeout.0,
            std::time::Duration::from_secs(10)
        );
    }

    #[test]
    fn test_catalog_uri_dns_scheme_resolved_to_http() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load_config failed");
        assert_eq!(cfg.git_service.catalog.uri, "http://localhost:6000");
    }

    #[test]
    fn test_catalog_uri_rejects_non_dns_scheme() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var(
            "GITSTORE_GIT_SERVICE__CATALOG__URI",
            "http://localhost:6000",
        );
        let err = load_config_from(None).expect_err("expected scheme rejection");
        assert!(err.to_string().contains("dns:///"), "got: {err}");
        clear_env();
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
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(&file_path, "[log]\nlevel = \"warn\"\nformat = \"text\"\n")
            .expect("write config");
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
        env::set_var("GITSTORE_GIT_SERVICE__GRPC_PORT", "6666");
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(&file_path, "[git_service]\ngrpc_port = 7777\n").expect("write config");
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        let cfg = load_config_from(Some(path_str)).expect("load_config failed");
        assert_eq!(cfg.git_service.grpc_port, 6666);
        clear_env();
    }

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

    #[test]
    fn test_validate_port_out_of_range() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GIT_SERVICE__GRPC_PORT", "0");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(result.is_err(), "expected validation error for port 0");
        let err = result.unwrap_err();
        assert!(
            err.to_string().contains("git_service.grpc_port"),
            "error should mention git_service.grpc_port, got: {err}"
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
        env::set_var("GITSTORE_GIT_SERVICE__DATA_DIR", "");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_err(),
            "expected validation error for empty data_dir"
        );
        let err = result.unwrap_err();
        assert!(err.to_string().contains("git_service.data_dir"));
        clear_env();
    }

    #[test]
    fn test_validate_hmac_secret_empty_fails() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_err(),
            "expected validation error for empty hmac_secret"
        );
        let err = result.unwrap_err();
        assert!(
            err.to_string().contains("grpc_auth.hmac_secret"),
            "error should mention grpc_auth.hmac_secret, got: {err}"
        );
    }

    #[test]
    fn test_validate_hmac_secret_nonempty_passes() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GRPC_AUTH__HMAC_SECRET", "some-secret");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_ok(),
            "expected no validation error for non-empty hmac_secret, got: {:?}",
            result.err()
        );
        clear_env();
    }

    #[test]
    fn test_hmac_secret_previous_env_var() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GRPC_AUTH__HMAC_SECRET", "new-secret");
        env::set_var("GITSTORE_GRPC_AUTH__HMAC_SECRET_PREVIOUS", "old-secret");
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.grpc_auth.hmac_secret, "new-secret");
        assert_eq!(
            cfg.grpc_auth.hmac_secret_previous,
            Some("old-secret".to_string())
        );
        clear_env();
    }

    #[test]
    fn test_validate_hmac_secret_previous_empty_fails() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GRPC_AUTH__HMAC_SECRET", "primary-secret");
        env::set_var("GITSTORE_GRPC_AUTH__HMAC_SECRET_PREVIOUS", "");
        let cfg = load_config_from(None).expect("load failed");
        let result = cfg.validate();
        assert!(
            result.is_err(),
            "expected validation error for empty hmac_secret_previous"
        );
        let err = result.unwrap_err();
        assert!(
            err.to_string().contains("hmac_secret_previous"),
            "error should mention hmac_secret_previous, got: {err}"
        );
        clear_env();
    }

    #[test]
    fn test_default_config_has_new_structure() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(
            cfg.git_service.admission.branch_pattern,
            "^refs/heads/main$"
        );
        assert_eq!(cfg.git_service.catalog.uri, "http://localhost:6000");
    }

    // --- duration/size grammar restriction tests ---

    #[test]
    fn test_duration_rejects_compound_and_day_week_units() {
        let _lock = ENV_LOCK.lock().unwrap();
        for bad in ["1h30m", "1d", "1w", "10ns", "10us"] {
            clear_env();
            env::set_var("GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT", bad);
            let err = load_config_from(None).expect_err(&format!("{bad} should be rejected"));
            assert!(
                err.to_string().contains("valid duration"),
                "got: {err} for input {bad}"
            );
        }
        clear_env();
    }

    #[test]
    fn test_duration_accepts_ms_s_m_h() {
        let _lock = ENV_LOCK.lock().unwrap();
        for good in ["500ms", "30s", "10m", "1h"] {
            clear_env();
            env::set_var("GITSTORE_GIT_SERVICE__VALIDATION__TIMEOUT", good);
            let cfg =
                load_config_from(None).unwrap_or_else(|e| panic!("{good} should be accepted: {e}"));
            assert!(cfg.git_service.validation.timeout.0.as_nanos() > 0);
        }
        clear_env();
    }

    #[test]
    fn test_size_rejects_decimal_units_and_bare_numbers() {
        let _lock = ENV_LOCK.lock().unwrap();
        for bad in ["50MB", "1024", "1 GiB"] {
            clear_env();
            env::set_var("GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE", bad);
            let err = load_config_from(None).expect_err(&format!("{bad} should be rejected"));
            assert!(
                err.to_string().contains("valid size"),
                "got: {err} for input {bad}"
            );
        }
        clear_env();
    }

    #[test]
    fn test_size_accepts_iec_units_and_resolves_bytes() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE", "1GiB");
        let cfg = load_config_from(None).expect("load failed");
        assert_eq!(cfg.push_limits.max_pack_size.0, 1024 * 1024 * 1024);
        clear_env();
    }

    // --- legacy key rejection tests ---

    #[test]
    fn test_legacy_grpc_port_env_var_rejected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_GRPC__PORT", "9999");
        let err = load_config_from(None).expect_err("legacy grpc.port must be rejected");
        assert!(
            err.to_string().contains("git_service.grpc_port"),
            "got: {err}"
        );
        clear_env();
    }

    #[test]
    fn test_legacy_hooks_table_rejected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        let dir = tempfile::tempdir().expect("tempdir");
        let file_path = dir.path().join("custom_config.toml");
        std::fs::write(
            &file_path,
            "[hooks.git_receive_pack]\npre_receive = { enabled = true }\n",
        )
        .expect("write config");
        let stem = dir.path().join("custom_config");
        let path_str = stem.to_str().expect("path str");
        let err =
            load_config_from(Some(path_str)).expect_err("legacy hooks table must be rejected");
        assert!(err.to_string().contains("always on"), "got: {err}");
    }

    #[test]
    fn test_legacy_auth_grpc_hmac_secret_env_var_rejected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_AUTH__GRPC__HMAC_SECRET", "legacy-secret");
        let err =
            load_config_from(None).expect_err("legacy auth.grpc.hmac_secret must be rejected");
        assert!(
            err.to_string().contains("grpc_auth.hmac_secret"),
            "got: {err}"
        );
        clear_env();
    }

    #[test]
    fn test_legacy_catalog_service_uri_env_var_rejected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_CATALOG_SERVICE__URI", "http://localhost:6000");
        let err = load_config_from(None).expect_err("legacy catalog_service.uri must be rejected");
        assert!(
            err.to_string().contains("git_service.catalog.uri"),
            "got: {err}"
        );
        clear_env();
    }

    #[test]
    fn test_legacy_schema_validation_timeout_secs_rejected() {
        let _lock = ENV_LOCK.lock().unwrap();
        clear_env();
        env::set_var("GITSTORE_SCHEMA_VALIDATION__TIMEOUT_SECS", "5");
        let err = load_config_from(None)
            .expect_err("legacy schema_validation.timeout_secs must be rejected");
        assert!(
            err.to_string().contains("git_service.validation.timeout"),
            "got: {err}"
        );
        clear_env();
    }
}
