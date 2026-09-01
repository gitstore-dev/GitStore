// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config holds the complete application configuration.
type Config struct {
	Api       ApiConfig       `mapstructure:"api"`
	Git       GitConfig       `mapstructure:"git"`
	Auth      AuthConfig      `mapstructure:"auth"`
	Cache     CacheConfig     `mapstructure:"cache"`
	Datastore DatastoreConfig `mapstructure:"datastore"`
	Features  FeatureConfig   `mapstructure:"features"`
	Watch     WatchConfig     `mapstructure:"watch"`
	Log       LogConfig       `mapstructure:"log"`
}

// ApiConfig holds HTTP API server settings.
type ApiConfig struct {
	Port     int `mapstructure:"port"      validate:"min=1,max=65535"`
	GitPort  int `mapstructure:"git_port"  validate:"min=1,max=65535"`
	GrpcPort int `mapstructure:"grpc_port" validate:"min=1,max=65535"`

	// RateLimitPerSecond is the sustained per-client-IP request rate allowed
	// on /graphql before responses are rejected with HTTP 429.
	RateLimitPerSecond float64 `mapstructure:"rate_limit_per_second" validate:"gt=0"`
	// RateLimitBurst is the per-client-IP token-bucket burst size layered on
	// top of RateLimitPerSecond.
	RateLimitBurst int `mapstructure:"rate_limit_burst" validate:"min=1"`
}

// GitConfig holds addresses for the git service backends.
type GitConfig struct {
	Grpc GitEndpointConfig `mapstructure:"grpc"`
}

// GitEndpointConfig holds a single git-service endpoint URI.
type GitEndpointConfig struct {
	Uri string `mapstructure:"uri" validate:"required"`
}

// AuthConfig holds authentication and JWT settings.
type AuthConfig struct {
	Admin   UserConfig     `mapstructure:"admin"`
	JWT     JWTConfig      `mapstructure:"jwt"`
	Grpc    GrpcAuthConfig `mapstructure:"grpc"`
	AuthN   AuthNConfig    `mapstructure:"authn"`
	AuthZ   AuthZConfig    `mapstructure:"authz"`
	UserDir UserDirConfig  `mapstructure:"userdir"`
	RBAC    RBACConfig     `mapstructure:"rbac"`
}

// GrpcAuthConfig holds inter-service gRPC authentication settings.
type GrpcAuthConfig struct {
	HmacSecret string `mapstructure:"hmac_secret" validate:"required"`
}

// AuthNConfig controls the authentication provider chain.
type AuthNConfig struct {
	// Chain is the ordered list of AuthN provider names. Defaults to ["static-admin","anonymous"].
	Chain []string `mapstructure:"chain"`
}

// AuthZConfig selects the active authorization provider.
type AuthZConfig struct {
	// Provider is the AuthZ provider name. Defaults to "allow-all".
	Provider string `mapstructure:"provider"`
}

// UserDirConfig selects the active user-directory provider.
type UserDirConfig struct {
	// Provider is the UserDir provider name. Defaults to "none".
	Provider string `mapstructure:"provider"`
}

// RBACConfig holds rbac-local provider settings.
type RBACConfig struct {
	// PolicyFile is the path to the YAML policy file. Defaults to "policy.yaml".
	PolicyFile string `mapstructure:"policy_file"`
}

// JWTConfig holds JWT token settings.
type JWTConfig struct {
	Secret       string `mapstructure:"secret"   validate:"required"`
	Duration     string `mapstructure:"duration"`
	Issuer       string `mapstructure:"issuer"`
	RefreshGrace string `mapstructure:"refresh_grace"`
}

// UserConfig in-memory users
type UserConfig struct {
	Username string `mapstructure:"username" validate:"required"`
	Password string `mapstructure:"password_hash" validate:"required"`
}

// CacheConfig holds cache settings.
type CacheConfig struct {
	TTL int `mapstructure:"ttl"`
}

// FeatureConfig holds staged rollout gates.
type FeatureConfig struct {
	NamespaceRepositoryFence string `mapstructure:"namespace_repository_fence"`
}

// WatchConfig holds per-kind durable watch settings.
type WatchConfig struct {
	Namespace NamespaceWatchConfig `mapstructure:"namespace"`
}

// NamespaceWatchConfig bounds the Namespace CDC materializer and journal.
// Integer time values keep TOML/environment configuration explicit and are
// converted to durations at the watch boundary.
type NamespaceWatchConfig struct {
	ReadersEnabled               bool `mapstructure:"readers_enabled"`
	MaterializerEnabled          bool `mapstructure:"materializer_enabled"`
	JournalRetentionSeconds      int  `mapstructure:"journal_retention_seconds" validate:"min=1"`
	CDCRetentionSeconds          int  `mapstructure:"cdc_retention_seconds" validate:"min=1"`
	CDCConfidenceWindowMillis    int  `mapstructure:"cdc_confidence_window_millis" validate:"min=1"`
	BucketSize                   int  `mapstructure:"bucket_size" validate:"min=1,max=4096"`
	ReadBatchSize                int  `mapstructure:"read_batch_size" validate:"min=1"`
	MaxReplayEvents              int  `mapstructure:"max_replay_events" validate:"min=1,max=100000"`
	SubscriberBuffer             int  `mapstructure:"subscriber_buffer" validate:"min=1,max=256"`
	SubscriberBackpressureMillis int  `mapstructure:"subscriber_backpressure_millis" validate:"min=1"`
	PollMinMillis                int  `mapstructure:"poll_min_millis" validate:"min=1"`
	PollMaxMillis                int  `mapstructure:"poll_max_millis" validate:"min=1"`
	BookmarkIntervalSeconds      int  `mapstructure:"bookmark_interval_seconds" validate:"min=1"`
	LeaseTTLSeconds              int  `mapstructure:"lease_ttl_seconds" validate:"min=1"`
	LeaseRenewIntervalSeconds    int  `mapstructure:"lease_renew_interval_seconds" validate:"min=1"`
	MaxMaterializerLagSeconds    int  `mapstructure:"max_materializer_lag_seconds" validate:"min=1"`
}

const namespaceWatchCDCRetentionSeconds = 14 * 24 * 60 * 60

const namespaceWatchJournalRetentionSeconds = 7 * 24 * 60 * 60

// LogConfig holds logger settings.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// DatastoreConfig selects the active storage backend.
type DatastoreConfig struct {
	Backend string       `mapstructure:"backend"`
	Scylla  ScyllaConfig `mapstructure:"scylla"`
}

// ScyllaConfig holds ScyllaDB connection parameters.
// Credentials and TLS are optional (FR-013).
type ScyllaConfig struct {
	Hosts                 []string `mapstructure:"hosts"`
	Keyspace              string   `mapstructure:"keyspace"`
	Username              string   `mapstructure:"username"`
	Password              string   `mapstructure:"password"`
	TLS                   bool     `mapstructure:"tls"`
	DisableShardAwarePort bool     `mapstructure:"disable_shard_aware_port"`
	IgnorePeerAddr        bool     `mapstructure:"ignore_peer_addr"`
	// AddressTranslator is an optional runtime-only field (not populated from config files).
	// Set it when Scylla runs behind a NAT (e.g. Docker) to redirect peer addresses.
	AddressTranslator interface{} `mapstructure:"-"`
}

// Load reads configuration from all sources (defaults → config file → env vars)
// and returns the resolved, validated Config.
func Load() (*Config, error) {
	return load("")
}

// LoadFrom loads configuration from path. Unlike Load's current-directory
// discovery, an explicitly selected file is required to exist and be readable.
func LoadFrom(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config file path must not be empty")
	}
	return load(path)
}

func load(path string) (*Config, error) {
	// .env file is optional; ignore error if absent
	_ = godotenv.Load()

	v := viper.New()

	// Defaults — all known keys must have a default so AutomaticEnv populates them
	// during Unmarshal, even if the default is an empty string.
	v.SetDefault("api.port", 4000)
	v.SetDefault("api.git_port", 5000)
	v.SetDefault("api.grpc_port", 6000)
	v.SetDefault("api.rate_limit_per_second", 50)
	v.SetDefault("api.rate_limit_burst", 100)
	v.SetDefault("git.grpc.uri", "dns:///localhost:50051")
	v.SetDefault("cache.ttl", 300)
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("auth.admin.username", "")
	v.SetDefault("auth.admin.password_hash", "")
	v.SetDefault("auth.jwt.secret", "")
	v.SetDefault("auth.jwt.duration", "24h")
	v.SetDefault("auth.jwt.issuer", "gitstore")
	v.SetDefault("auth.jwt.refresh_grace", "60s")
	v.SetDefault("auth.grpc.hmac_secret", "")
	v.SetDefault("auth.authn.chain", []string{"static-admin", "anonymous"})
	v.SetDefault("auth.authz.provider", "allow-all")
	v.SetDefault("auth.userdir.provider", "none")
	v.SetDefault("auth.rbac.policy_file", "policy.yaml")
	v.SetDefault("datastore.backend", "memdb")
	v.SetDefault("datastore.scylla.hosts", []string{"localhost:9042"})
	v.SetDefault("datastore.scylla.keyspace", "gitstore")
	v.SetDefault("datastore.scylla.username", "")
	v.SetDefault("datastore.scylla.password", "")
	v.SetDefault("datastore.scylla.tls", false)
	v.SetDefault("datastore.scylla.disable_shard_aware_port", false)
	v.SetDefault("datastore.scylla.ignore_peer_addr", false)
	v.SetDefault("features.namespace_repository_fence", "auto")
	v.SetDefault("watch.namespace.readers_enabled", true)
	v.SetDefault("watch.namespace.materializer_enabled", true)
	v.SetDefault("watch.namespace.journal_retention_seconds", 7*24*60*60)
	v.SetDefault("watch.namespace.cdc_retention_seconds", 14*24*60*60)
	v.SetDefault("watch.namespace.cdc_confidence_window_millis", 500)
	v.SetDefault("watch.namespace.bucket_size", 4096)
	v.SetDefault("watch.namespace.read_batch_size", 256)
	v.SetDefault("watch.namespace.max_replay_events", 100000)
	v.SetDefault("watch.namespace.subscriber_buffer", 64)
	v.SetDefault("watch.namespace.subscriber_backpressure_millis", 30000)
	v.SetDefault("watch.namespace.poll_min_millis", 100)
	v.SetDefault("watch.namespace.poll_max_millis", 2000)
	v.SetDefault("watch.namespace.bookmark_interval_seconds", 30)
	v.SetDefault("watch.namespace.lease_ttl_seconds", 30)
	v.SetDefault("watch.namespace.lease_renew_interval_seconds", 10)
	v.SetDefault("watch.namespace.max_materializer_lag_seconds", 60)

	// Config discovery is optional for compatibility; an explicit path is not.
	if path != "" {
		v.SetConfigFile(path)
	} else {
		v.SetConfigName("config")
		v.SetConfigType("toml")
		v.AddConfigPath(".")
	}
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if path != "" || !errors.As(err, &notFound) {
			return nil, err
		}
	}

	// Environment variables
	v.SetEnvPrefix("GITSTORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}

	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	// Warn about keys present in the config file that are not in the known schema.
	knownKeys := map[string]bool{
		"api.port": true, "api.git_port": true, "api.grpc_port": true,
		"api.rate_limit_per_second": true, "api.rate_limit_burst": true,
		"git.grpc.uri": true,
		"cache.ttl":    true, "log.level": true, "log.format": true,
		"auth.admin.username": true, "auth.admin.password_hash": true,
		"auth.jwt.secret": true, "auth.jwt.duration": true, "auth.jwt.issuer": true, "auth.jwt.refresh_grace": true,
		"auth.grpc.hmac_secret": true,
		"auth.authn.chain":      true, "auth.authz.provider": true,
		"auth.userdir.provider": true, "auth.rbac.policy_file": true,
		"datastore.backend": true, "datastore.scylla.hosts": true,
		"datastore.scylla.keyspace": true, "datastore.scylla.username": true,
		"datastore.scylla.password": true, "datastore.scylla.tls": true,
		"datastore.scylla.disable_shard_aware_port": true, "datastore.scylla.ignore_peer_addr": true,
		"features.namespace_repository_fence": true,
		"watch.namespace.readers_enabled":     true, "watch.namespace.materializer_enabled": true,
		"watch.namespace.journal_retention_seconds": true, "watch.namespace.cdc_retention_seconds": true,
		"watch.namespace.cdc_confidence_window_millis": true,
		"watch.namespace.bucket_size":                  true, "watch.namespace.read_batch_size": true,
		"watch.namespace.max_replay_events": true, "watch.namespace.subscriber_buffer": true,
		"watch.namespace.subscriber_backpressure_millis": true,
		"watch.namespace.poll_min_millis":                true, "watch.namespace.poll_max_millis": true,
		"watch.namespace.bookmark_interval_seconds": true, "watch.namespace.lease_ttl_seconds": true,
		"watch.namespace.lease_renew_interval_seconds": true, "watch.namespace.max_materializer_lag_seconds": true,
	}
	sharedServiceKey := func(k string) bool {
		for _, prefix := range []string{"controller.", "grpc.", "hooks.", "schema_validation.", "admission_control.", "catalog_service."} {
			if strings.HasPrefix(k, prefix) {
				return true
			}
		}
		return strings.HasPrefix(k, "git.") && !strings.HasPrefix(k, "git.grpc.")
	}
	for _, k := range v.AllKeys() {
		if !knownKeys[k] && !sharedServiceKey(k) {
			logger.Warn("unknown configuration key", zap.String("key", k))
		}
	}

	logger.Info("Configuration loaded", zap.Object("config", &cfg))

	return &cfg, nil
}

// validateConfig runs all struct validations and returns a combined error.
func validateConfig(cfg *Config) error {
	validate := validator.New()
	if err := validate.Struct(cfg); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) {
			msgs := make([]string, 0, len(ve))
			for _, fe := range ve {
				msgs = append(msgs, fmt.Sprintf(
					"%s: constraint %q violated (value: %q)",
					fe.StructNamespace(), fe.Tag(), fe.Value(),
				))
			}
			return fmt.Errorf("invalid configuration (%d error(s)):\n  %s", len(msgs), strings.Join(msgs, "\n  "))
		}
		return err
	}
	if err := validateDatastoreConfig(&cfg.Datastore); err != nil {
		return err
	}
	if err := validateFeatureConfig(&cfg.Features); err != nil {
		return err
	}
	if err := validateNamespaceWatchConfig(&cfg.Watch.Namespace); err != nil {
		return err
	}
	return validateLogFormat(&cfg.Log)
}

func validateNamespaceWatchConfig(w *NamespaceWatchConfig) error {
	if w.CDCRetentionSeconds != namespaceWatchCDCRetentionSeconds {
		return fmt.Errorf("invalid Namespace watch CDC retention: migration 006 fixes CDC retention at %d seconds", namespaceWatchCDCRetentionSeconds)
	}
	if w.JournalRetentionSeconds > namespaceWatchJournalRetentionSeconds {
		return fmt.Errorf("invalid Namespace watch journal retention: migration 006 limits journal retention to %d seconds", namespaceWatchJournalRetentionSeconds)
	}
	if w.CDCRetentionSeconds < w.JournalRetentionSeconds {
		return fmt.Errorf("invalid Namespace watch bounds: CDC retention must be at least journal retention")
	}
	if w.MaxMaterializerLagSeconds >= w.CDCRetentionSeconds {
		return fmt.Errorf("invalid Namespace watch bounds: maximum materializer lag must be less than CDC retention")
	}
	if int64(w.CDCConfidenceWindowMillis) >= int64(w.MaxMaterializerLagSeconds)*1000 {
		return fmt.Errorf("invalid Namespace watch bounds: CDC confidence window must be less than maximum materializer lag")
	}
	if w.ReadBatchSize > w.BucketSize {
		return fmt.Errorf("invalid Namespace watch bounds: read batch size must not exceed bucket size")
	}
	if w.PollMinMillis > w.PollMaxMillis {
		return fmt.Errorf("invalid Namespace watch bounds: minimum poll interval must not exceed maximum")
	}
	if int64(w.PollMaxMillis) >= int64(w.JournalRetentionSeconds)*1000 {
		return fmt.Errorf("invalid Namespace watch bounds: maximum poll interval must be less than journal retention")
	}
	if w.LeaseRenewIntervalSeconds >= w.LeaseTTLSeconds {
		return fmt.Errorf("invalid Namespace watch bounds: lease renewal interval must be less than lease TTL")
	}
	return nil
}

// validateDatastoreConfig validates backend selection and ScyllaDB settings.
func validateDatastoreConfig(ds *DatastoreConfig) error {
	switch strings.ToLower(ds.Backend) {
	case "memdb":
		ds.Backend = "memdb"
		return nil
	case "scylla":
		ds.Backend = "scylla"
		return nil
	default:
		return fmt.Errorf("invalid datastore backend %q; valid values: memdb, scylla", ds.Backend)
	}
}

func validateFeatureConfig(features *FeatureConfig) error {
	mode := strings.ToLower(strings.TrimSpace(features.NamespaceRepositoryFence))
	if mode == "" {
		mode = "auto"
	}
	switch mode {
	case "auto", "enabled", "disabled":
		features.NamespaceRepositoryFence = mode
		return nil
	default:
		return fmt.Errorf(
			"invalid namespace repository fence mode %q; valid values: auto, enabled, disabled",
			features.NamespaceRepositoryFence,
		)
	}
}

// NamespaceRepositoryFenceEnabled resolves the rollout gate. Development
// memdb keeps the existing behavior; Scylla requires explicit activation.
func (c *Config) NamespaceRepositoryFenceEnabled() bool {
	mode := strings.ToLower(strings.TrimSpace(c.Features.NamespaceRepositoryFence))
	switch mode {
	case "enabled":
		return true
	case "disabled":
		return false
	default:
		return strings.EqualFold(c.Datastore.Backend, "memdb")
	}
}

// validateLogFormat validates and normalizes the configured log encoding.
func validateLogFormat(log *LogConfig) error {
	switch strings.ToLower(log.Format) {
	case "json":
		log.Format = "json"
		return nil
	case "text":
		log.Format = "text"
		return nil
	default:
		return fmt.Errorf("invalid log format %q; valid values: json, text", log.Format)
	}
}

// MarshalLogObject implements zap.ObjectMarshaler for structured startup logging.
// Sensitive fields are always redacted.
func (c *Config) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddInt("api.port", c.Api.Port)
	enc.AddInt("api.git_port", c.Api.GitPort)
	enc.AddInt("api.grpc_port", c.Api.GrpcPort)
	enc.AddString("git.grpc.uri", c.Git.Grpc.Uri)
	enc.AddString("auth.admin.username", c.Auth.Admin.Username)
	enc.AddString("auth.admin.password_hash", redact(c.Auth.Admin.Password))
	enc.AddString("auth.jwt.secret", redact(c.Auth.JWT.Secret))
	enc.AddString("auth.jwt.duration", c.Auth.JWT.Duration)
	enc.AddString("auth.jwt.issuer", c.Auth.JWT.Issuer)
	enc.AddString("auth.jwt.refresh_grace", c.Auth.JWT.RefreshGrace)
	enc.AddString("auth.grpc.hmac_secret", redact(c.Auth.Grpc.HmacSecret))
	enc.AddInt("cache.ttl", c.Cache.TTL)
	enc.AddString("log.level", c.Log.Level)
	enc.AddString("log.format", c.Log.Format)
	enc.AddString("datastore.backend", c.Datastore.Backend)
	enc.AddString("datastore.scylla.password", redact(c.Datastore.Scylla.Password))
	enc.AddString("features.namespace_repository_fence", c.Features.NamespaceRepositoryFence)
	enc.AddBool("watch.namespace.readers_enabled", c.Watch.Namespace.ReadersEnabled)
	enc.AddBool("watch.namespace.materializer_enabled", c.Watch.Namespace.MaterializerEnabled)
	return nil
}

// redact returns "<redacted>" if the value is non-empty, "<unset>" if empty.
func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}
