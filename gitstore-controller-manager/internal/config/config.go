// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"
)

// Config holds the complete controller-manager configuration.
type Config struct {
	Controller ControllerConfig `mapstructure:"controller"`
	Log        LogConfig        `mapstructure:"log"`
}

// ControllerConfig holds runtime settings for the controller manager.
type ControllerConfig struct {
	// Port is the HTTP listen port for /health, /metrics, and /controller/v1/*.
	Port int `mapstructure:"port" validate:"min=1,max=65535"`

	// ApiURI is the gitstore-api GraphQL URI used as the Watch event source.
	ApiURI string `mapstructure:"api_uri"`

	// ServiceAccountNamespace identifies the controller's ServiceAccount.
	ServiceAccountNamespace string `mapstructure:"serviceaccount_namespace"`
	// ServiceAccountName identifies the controller's ServiceAccount.
	ServiceAccountName string `mapstructure:"serviceaccount_name"`
	// ServiceAccountKeyID identifies the enrolled public key used to sign
	// client assertions.
	ServiceAccountKeyID string `mapstructure:"serviceaccount_key_id"`
	// ServiceAccountUID prevents a deleted and recreated ServiceAccount from
	// being mistaken for its predecessor.
	ServiceAccountUID string `mapstructure:"serviceaccount_uid"`
	// ServiceAccountKeyRef resolves the private key through the bootstrap
	// SecretResolver; it is deliberately not a filesystem path.
	ServiceAccountKeyRef secret.Ref `mapstructure:"serviceaccount_key_ref"`
	// ServiceAccountAssertionAudience is the audience for the signed client
	// assertion sent to the API token exchange endpoint.
	ServiceAccountAssertionAudience string `mapstructure:"serviceaccount_assertion_audience"`
	// ServiceAccountAccessTokenAudience is the audience requested for the
	// exchanged controller access token.
	ServiceAccountAccessTokenAudience string `mapstructure:"serviceaccount_access_token_audience"`
	// SecretProviderBootstrap selects the local bootstrap secret provider.
	SecretProviderBootstrap secret.BootstrapProviderConfig `mapstructure:"secret_provider_bootstrap"`

	// DefaultMaxAttempts is the global retry limit before quarantine.
	DefaultMaxAttempts int `mapstructure:"default_max_attempts"`

	// DefaultStallThreshold is parsed at startup into StallThreshold.
	DefaultStallThresholdStr string        `mapstructure:"default_stall_threshold"`
	DefaultStallThreshold    time.Duration `mapstructure:"-"`

	// CheckpointDir is the directory used by the filesystem CheckpointStore
	// (one JSON file per registered kind).
	CheckpointDir string `mapstructure:"checkpoint_dir"`

	// CheckpointFlushIntervalEvents is the number of watch events processed
	// between checkpoint persists (an event count, not a duration).
	CheckpointFlushIntervalEvents int `mapstructure:"checkpoint_flush_interval_events"`

	// MaxWatchBackoffStr is parsed at startup into MaxWatchBackoff.
	MaxWatchBackoffStr string        `mapstructure:"max_watch_backoff"`
	MaxWatchBackoff    time.Duration `mapstructure:"-"`
}

// LogConfig holds logger settings.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load reads configuration from environment variables and optional .env file.
func Load() (*Config, error) {
	return load("")
}

// LoadFrom loads the explicitly selected configuration file. The file is
// required; Load retains optional current-directory discovery compatibility.
func LoadFrom(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config file path must not be empty")
	}
	return load(path)
}

func load(path string) (*Config, error) {
	_ = godotenv.Load()

	v := viper.New()

	v.SetDefault("controller.port", 5001)
	v.SetDefault("controller.api_uri", "http://localhost:4000/graphql")
	v.SetDefault("controller.serviceaccount_namespace", "")
	v.SetDefault("controller.serviceaccount_name", "gitstore-controller-manager")
	v.SetDefault("controller.serviceaccount_key_id", "")
	v.SetDefault("controller.serviceaccount_uid", "")
	v.SetDefault("controller.serviceaccount_key_ref.kind", "")
	v.SetDefault("controller.serviceaccount_key_ref.name", "")
	v.SetDefault("controller.serviceaccount_key_ref.key", "")
	v.SetDefault("controller.serviceaccount_assertion_audience", "gitstore-api/serviceaccount-token")
	v.SetDefault("controller.serviceaccount_access_token_audience", "gitstore-api")
	v.SetDefault("controller.secret_provider_bootstrap.type", "file")
	v.SetDefault("controller.secret_provider_bootstrap.base_path", "/run/secrets")
	v.SetDefault("controller.secret_provider_bootstrap.env_prefix", "GITSTORE_SECRET__")
	v.SetDefault("controller.default_max_attempts", 5)
	v.SetDefault("controller.default_stall_threshold", "5m")
	v.SetDefault("controller.checkpoint_dir", "/var/lib/gitstore/checkpoints")
	v.SetDefault("controller.checkpoint_flush_interval_events", 100)
	v.SetDefault("controller.max_watch_backoff", "30s")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

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

	v.SetEnvPrefix("GITSTORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()
	bindServiceAccountEnvironment(v)

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}
	readServiceAccountConfig(v, &cfg.Controller)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validate(cfg *Config) error {
	if cfg.Controller.Port < 1 || cfg.Controller.Port > 65535 {
		return fmt.Errorf("controller.port must be between 1 and 65535, got %d", cfg.Controller.Port)
	}
	if cfg.Controller.ApiURI == "" {
		return fmt.Errorf("controller.api_uri must not be empty")
	}
	if err := validateServiceAccountConfig(&cfg.Controller); err != nil {
		return err
	}
	if !hasServiceAccountKeyRef(cfg.Controller.ServiceAccountKeyRef) {
		return fmt.Errorf("controller.serviceaccount_key_ref must be configured")
	}
	if cfg.Controller.DefaultMaxAttempts < 1 {
		return fmt.Errorf("controller.default_max_attempts must be >= 1")
	}

	d, err := time.ParseDuration(cfg.Controller.DefaultStallThresholdStr)
	if err != nil {
		return fmt.Errorf("controller.default_stall_threshold is not a valid duration: %w", err)
	}
	cfg.Controller.DefaultStallThreshold = d

	if cfg.Controller.CheckpointDir == "" {
		return fmt.Errorf("controller.checkpoint_dir must not be empty")
	}
	if cfg.Controller.CheckpointFlushIntervalEvents < 1 {
		return fmt.Errorf("controller.checkpoint_flush_interval_events must be >= 1")
	}

	maxBackoff, err := time.ParseDuration(cfg.Controller.MaxWatchBackoffStr)
	if err != nil {
		return fmt.Errorf("controller.max_watch_backoff is not a valid duration: %w", err)
	}
	cfg.Controller.MaxWatchBackoff = maxBackoff

	if err := validateLogFormat(&cfg.Log); err != nil {
		return err
	}
	return nil
}

func bindServiceAccountEnvironment(v *viper.Viper) {
	for key, env := range map[string]string{
		"controller.serviceaccount_namespace":             "GITSTORE_CONTROLLER__SERVICEACCOUNT__NAMESPACE",
		"controller.serviceaccount_name":                  "GITSTORE_CONTROLLER__SERVICEACCOUNT__NAME",
		"controller.serviceaccount_key_id":                "GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_ID",
		"controller.serviceaccount_uid":                   "GITSTORE_CONTROLLER__SERVICEACCOUNT__UID",
		"controller.serviceaccount_key_ref.kind":          "GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KIND",
		"controller.serviceaccount_key_ref.name":          "GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__NAME",
		"controller.serviceaccount_key_ref.key":           "GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__KEY",
		"controller.serviceaccount_assertion_audience":    "GITSTORE_CONTROLLER__SERVICEACCOUNT__ASSERTION_AUDIENCE",
		"controller.serviceaccount_access_token_audience": "GITSTORE_CONTROLLER__SERVICEACCOUNT__ACCESS_TOKEN_AUDIENCE",
		"controller.secret_provider_bootstrap.type":       "GITSTORE_CONTROLLER__SECRET_PROVIDER_BOOTSTRAP__TYPE",
		"controller.secret_provider_bootstrap.base_path":  "GITSTORE_CONTROLLER__SECRET_PROVIDER_BOOTSTRAP__BASE_PATH",
		"controller.secret_provider_bootstrap.env_prefix": "GITSTORE_CONTROLLER__SECRET_PROVIDER_BOOTSTRAP__ENV_PREFIX",
	} {
		_ = v.BindEnv(key, env)
	}
}

func readServiceAccountConfig(v *viper.Viper, controller *ControllerConfig) {
	controller.ServiceAccountNamespace = v.GetString("controller.serviceaccount_namespace")
	controller.ServiceAccountName = v.GetString("controller.serviceaccount_name")
	controller.ServiceAccountKeyID = v.GetString("controller.serviceaccount_key_id")
	controller.ServiceAccountUID = v.GetString("controller.serviceaccount_uid")
	controller.ServiceAccountKeyRef = secret.Ref{
		Kind: v.GetString("controller.serviceaccount_key_ref.kind"),
		Name: v.GetString("controller.serviceaccount_key_ref.name"),
		Key:  v.GetString("controller.serviceaccount_key_ref.key"),
	}
	controller.ServiceAccountAssertionAudience = v.GetString("controller.serviceaccount_assertion_audience")
	controller.ServiceAccountAccessTokenAudience = v.GetString("controller.serviceaccount_access_token_audience")
	controller.SecretProviderBootstrap = secret.BootstrapProviderConfig{
		Type:      v.GetString("controller.secret_provider_bootstrap.type"),
		BasePath:  v.GetString("controller.secret_provider_bootstrap.base_path"),
		EnvPrefix: v.GetString("controller.secret_provider_bootstrap.env_prefix"),
	}
}

func hasServiceAccountKeyRef(ref secret.Ref) bool {
	return ref.Kind != "" || ref.Name != "" || ref.Key != ""
}

func validateServiceAccountConfig(controller *ControllerConfig) error {
	if !hasServiceAccountKeyRef(controller.ServiceAccountKeyRef) {
		return nil
	}
	if controller.ServiceAccountKeyRef.Kind != "SecretRef" ||
		strings.TrimSpace(controller.ServiceAccountKeyRef.Name) == "" ||
		strings.TrimSpace(controller.ServiceAccountKeyRef.Key) == "" {
		return fmt.Errorf("controller.serviceaccount_key_ref must be a complete SecretRef")
	}
	for _, required := range []struct {
		key   string
		value string
	}{
		{"controller.serviceaccount_namespace", controller.ServiceAccountNamespace},
		{"controller.serviceaccount_name", controller.ServiceAccountName},
		{"controller.serviceaccount_key_id", controller.ServiceAccountKeyID},
		{"controller.serviceaccount_uid", controller.ServiceAccountUID},
		{"controller.serviceaccount_assertion_audience", controller.ServiceAccountAssertionAudience},
		{"controller.serviceaccount_access_token_audience", controller.ServiceAccountAccessTokenAudience},
	} {
		key, value := required.key, required.value
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty when controller.serviceaccount_key_ref is configured", key)
		}
	}
	return nil
}

func validateLogFormat(log *LogConfig) error {
	switch strings.ToLower(log.Format) {
	case "json":
		log.Format = "json"
	case "text":
		log.Format = "text"
	default:
		return fmt.Errorf("invalid log format %q; valid values: json, text", log.Format)
	}
	_, err := zapcore.ParseLevel(log.Level)
	if err != nil {
		return fmt.Errorf("invalid log level %q: %w", log.Level, err)
	}
	return nil
}
