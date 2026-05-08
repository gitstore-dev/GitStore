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
	Api      ApiConfig   `mapstructure:"api"`
	Git      GitConfig   `mapstructure:"git"`
	Auth     AuthConfig  `mapstructure:"auth"`
	Cache    CacheConfig `mapstructure:"cache"`
	LogLevel string      `mapstructure:"log_level"`
}

// ApiConfig holds HTTP API server settings.
type ApiConfig struct {
	Port int `mapstructure:"port" validate:"min=1,max=65535"`
}

// GitConfig holds addresses for the git service backends.
type GitConfig struct {
	GRPC    string `mapstructure:"grpc"     validate:"required"`
	WS      string `mapstructure:"ws"       validate:"required"`
	HttpURL string `mapstructure:"http_url" validate:"required"`
}

// AuthConfig holds authentication and JWT settings.
type AuthConfig struct {
	AdminUsername     string `mapstructure:"admin_username"     validate:"required"`
	AdminPasswordHash string `mapstructure:"admin_password_hash" validate:"required"`
	JWTSecret         string `mapstructure:"jwt_secret"          validate:"required"`
	JWTDuration       string `mapstructure:"jwt_duration"`
	JWTIssuer         string `mapstructure:"jwt_issuer"`
}

// CacheConfig holds cache settings.
type CacheConfig struct {
	TTL int `mapstructure:"ttl"`
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
	v.SetDefault("git.grpc", "localhost:50051")
	v.SetDefault("git.ws", "ws://localhost:8080")
	v.SetDefault("git.http_url", "http://localhost:9418")
	v.SetDefault("cache.ttl", 300)
	v.SetDefault("log_level", "info")
	v.SetDefault("auth.admin_username", "")
	v.SetDefault("auth.admin_password_hash", "")
	v.SetDefault("auth.jwt_secret", "")
	v.SetDefault("auth.jwt_duration", "24h")
	v.SetDefault("auth.jwt_issuer", "gitstore")

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
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
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
		"api.port": true, "git.grpc": true, "git.ws": true, "git.http_url": true,
		"cache.ttl": true, "log_level": true,
		"auth.admin_username": true, "auth.admin_password_hash": true,
		"auth.jwt_secret": true, "auth.jwt_duration": true, "auth.jwt_issuer": true,
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
	return nil
}

// MarshalLogObject implements zap.ObjectMarshaler for structured startup logging.
// Sensitive fields are always redacted.
func (c *Config) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddInt("api.port", c.Api.Port)
	enc.AddString("git.grpc", c.Git.GRPC)
	enc.AddString("git.ws", c.Git.WS)
	enc.AddString("git.http_url", c.Git.HttpURL)
	enc.AddString("auth.admin_username", c.Auth.AdminUsername)
	enc.AddString("auth.admin_password_hash", redact(c.Auth.AdminPasswordHash))
	enc.AddString("auth.jwt_secret", redact(c.Auth.JWTSecret))
	enc.AddString("auth.jwt_duration", c.Auth.JWTDuration)
	enc.AddString("auth.jwt_issuer", c.Auth.JWTIssuer)
	enc.AddInt("cache.ttl", c.Cache.TTL)
	enc.AddString("log_level", c.LogLevel)
	return nil
}

// redact returns "<redacted>" if the value is non-empty, "<unset>" if empty.
func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}
