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
	Log       LogConfig       `mapstructure:"log"`
}

// ApiConfig holds HTTP API server settings.
type ApiConfig struct {
	Port     int `mapstructure:"port"      validate:"min=1,max=65535"`
	GitPort  int `mapstructure:"git_port"  validate:"min=1,max=65535"`
	GrpcPort int `mapstructure:"grpc_port" validate:"min=1,max=65535"`
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
	// .env file is optional; ignore error if absent
	_ = godotenv.Load()

	v := viper.New()

	// Defaults — all known keys must have a default so AutomaticEnv populates them
	// during Unmarshal, even if the default is an empty string.
	v.SetDefault("api.port", 4000)
	v.SetDefault("api.git_port", 5000)
	v.SetDefault("api.grpc_port", 6000)
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

	// Config file (optional)
	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.AddConfigPath(".")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
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
		"api.port": true, "api.git_port": true, "api.grpc_port": true, "git.grpc.uri": true,
		"cache.ttl": true, "log.level": true, "log.format": true,
		"auth.admin.username": true, "auth.admin.password_hash": true,
		"auth.jwt.secret": true, "auth.jwt.duration": true, "auth.jwt.issuer": true, "auth.jwt.refresh_grace": true,
		"auth.grpc.hmac_secret": true,
		"auth.authn.chain":      true, "auth.authz.provider": true,
		"auth.userdir.provider": true, "auth.rbac.policy_file": true,
		"datastore.backend": true, "datastore.scylla.hosts": true,
		"datastore.scylla.keyspace": true, "datastore.scylla.username": true,
		"datastore.scylla.password": true, "datastore.scylla.tls": true,
	}
	for _, k := range v.AllKeys() {
		if !knownKeys[k] {
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
	return validateLogFormat(&cfg.Log)
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
	return nil
}

// redact returns "<redacted>" if the value is non-empty, "<unset>" if empty.
func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}
