// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/secret"
	"github.com/gitstore-dev/gitstore/secretmaterial"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"
)

type Config struct {
	Controller ControllerConfig `mapstructure:"controller"`
	Log        LogConfig        `mapstructure:"log"`
}

type ControllerConfig struct {
	Port            int                   `mapstructure:"port"`
	ApiURI          string                `mapstructure:"api_uri"`
	APIClient       APIClientConfig       `mapstructure:"api_client"`
	ServiceAccount  ServiceAccountConfig  `mapstructure:"serviceaccount"`
	SecretProviders SecretProvidersConfig `mapstructure:"secret_providers"`
	Checkpoint      CheckpointConfig      `mapstructure:"checkpoint"`
	Reconcile       ReconcileConfig       `mapstructure:"reconcile"`
	Watch           WatchConfig           `mapstructure:"watch"`
}

type APIClientConfig struct {
	RateLimit RateLimitConfig `mapstructure:"rate_limit"`
}

// RateLimitConfig mirrors gitstore-api's api.rate_limit shape for
// consistency across services.
type RateLimitConfig struct {
	PerSecond int `mapstructure:"per_second"`
	Burst     int `mapstructure:"burst"`
}

type ServiceAccountConfig struct {
	Namespace           string     `mapstructure:"namespace"`
	Name                string     `mapstructure:"name"`
	UID                 string     `mapstructure:"uid"`
	KeyRef              secret.Ref `mapstructure:"key_ref"`
	AssertionAudience   string     `mapstructure:"assertion_audience"`
	AccessTokenAudience string     `mapstructure:"access_token_audience"`
}

type SecretProvidersConfig struct {
	Bootstrap secret.BootstrapProviderConfig `mapstructure:"bootstrap"`
}

type CheckpointConfig struct {
	Dir                 string `mapstructure:"dir"`
	FlushIntervalEvents int    `mapstructure:"flush_interval_events"`
}

type ReconcileConfig struct {
	MaxAttempts    int           `mapstructure:"max_attempts"`
	StallThreshold time.Duration `mapstructure:"stall_threshold"`
}

type WatchConfig struct {
	MaxBackoff     time.Duration `mapstructure:"max_backoff"`
	ResyncInterval time.Duration `mapstructure:"resync_interval"`
}

type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

var defaults = map[string]any{
	"controller.port":                                    5001,
	"controller.api_uri":                                 "http://localhost:4000/graphql",
	"controller.api_client.rate_limit.per_second":        40,
	"controller.api_client.rate_limit.burst":             10,
	"controller.serviceaccount.namespace":                "",
	"controller.serviceaccount.name":                     "gitstore-controller-manager",
	"controller.serviceaccount.uid":                      "",
	"controller.serviceaccount.key_ref.kind":             "",
	"controller.serviceaccount.key_ref.name":             "",
	"controller.serviceaccount.assertion_audience":       "gitstore-api/serviceaccount-token",
	"controller.serviceaccount.access_token_audience":    "gitstore-api",
	"controller.secret_providers.bootstrap.type":         "file",
	"controller.secret_providers.bootstrap.base_path":    "/run/secrets",
	"controller.secret_providers.bootstrap.env_variable": "",
	"controller.reconcile.max_attempts":                  5,
	"controller.reconcile.stall_threshold":               "5m",
	"controller.checkpoint.dir":                          "/var/lib/gitstore/checkpoints",
	"controller.checkpoint.flush_interval_events":        100,
	"controller.watch.max_backoff":                       "30s",
	"controller.watch.resync_interval":                   "10m",
	"log.level":                                          "info",
	"log.format":                                         "json",
}

func Load() (*Config, error) { return load(nil) }

func LoadFrom(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config file path must not be empty")
	}
	return load([]string{path})
}

// LoadFromFiles loads configuration by reading paths[0] and additively
// merging each subsequent path on top (a later file's keys win), so an
// overlay only needs to specify the deltas from the base file. Each path
// is required to exist and be readable.
func LoadFromFiles(paths []string) (*Config, error) {
	if len(paths) == 0 {
		return nil, errors.New("config file path must not be empty")
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return nil, errors.New("config file path must not be empty")
		}
	}
	return load(paths)
}

func load(paths []string) (*Config, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("could not read controller environment file")
	}
	v := viper.New()
	if len(paths) == 0 {
		v.SetConfigName("config")
		v.SetConfigType("toml")
		v.AddConfigPath(".")
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return nil, errors.New("could not read controller configuration file")
			}
		}
	} else {
		for i, p := range paths {
			v.SetConfigFile(p)
			if i == 0 {
				if err := v.ReadInConfig(); err != nil {
					return nil, errors.New("could not read controller configuration file")
				}
			} else if err := v.MergeInConfig(); err != nil {
				return nil, errors.New("could not read controller configuration file")
			}
		}
	}
	for _, root := range []string{"controller", "log"} {
		if v.IsSet(root) {
			if err := validateSource(root, v.Get(root)); err != nil {
				return nil, err
			}
		}
	}
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if name == "GITSTORE_CONTROLLER" || name == "GITSTORE_LOG" ||
			strings.HasPrefix(name, "GITSTORE_CONTROLLER_") || strings.HasPrefix(name, "GITSTORE_LOG_") {
			path := strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(name, "GITSTORE_")), "__", ".")
			if _, ok := defaults[path]; !ok {
				return nil, sourceError(path, name)
			}
		}
	}
	for key, value := range defaults {
		v.SetDefault(key, value)
	}
	v.SetEnvPrefix("GITSTORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AllowEmptyEnv(true)
	v.AutomaticEnv()
	var cfg Config
	if err := v.Unmarshal(&cfg, viper.DecodeHook(decodeValue)); err != nil {
		return nil, err
	}
	if err := validate(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func validateSource(path string, value any) error {
	if _, ok := defaults[path]; ok {
		return nil
	}
	knownGroup := false
	for leaf := range defaults {
		if strings.HasPrefix(leaf, path+".") {
			knownGroup = true
			break
		}
	}
	values, ok := value.(map[string]any)
	if !knownGroup || !ok {
		return sourceError(path, "")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		if err := validateSource(path+"."+key, values[key]); err != nil {
			return err
		}
	}
	return nil
}

func sourceError(path, env string) error {
	replacement := strings.Replace(path, "controller.controller.", "controller.", 1)
	for old, next := range map[string]string{
		"api_client.requests_per_second":       "api_client.rate_limit.per_second",
		"api_client.burst":                     "api_client.rate_limit.burst",
		"serviceaccount_namespace":             "serviceaccount.namespace",
		"serviceaccount_name":                  "serviceaccount.name",
		"serviceaccount_uid":                   "serviceaccount.uid",
		"serviceaccount_key_id":                "serviceaccount.key_id",
		"serviceaccount_key_ref":               "serviceaccount.key_ref",
		"serviceaccount_assertion_audience":    "serviceaccount.assertion_audience",
		"serviceaccount_access_token_audience": "serviceaccount.access_token_audience",
		"secret_provider_bootstrap":            "secret_providers.bootstrap",
		"checkpoint_dir":                       "checkpoint.dir",
		"checkpoint_flush_interval_events":     "checkpoint.flush_interval_events",
		"default_max_attempts":                 "reconcile.max_attempts",
		"default_stall_threshold":              "reconcile.stall_threshold",
		"max_watch_backoff":                    "watch.max_backoff",
		"resync_interval":                      "watch.resync_interval",
		"api_token":                            "serviceaccount.key_ref",
	} {
		prefix := "controller." + old
		if replacement == prefix || strings.HasPrefix(replacement, prefix+".") {
			replacement = "controller." + next + strings.TrimPrefix(replacement, prefix)
			break
		}
	}
	if env != "" {
		path = env
		replacement = "GITSTORE_" + strings.ToUpper(strings.ReplaceAll(replacement, ".", "__"))
	}
	if replacement != path {
		return fmt.Errorf("obsolete configuration path %s; use %s", path, replacement)
	}
	return fmt.Errorf("unknown configuration path %s; use canonical nested settings", path)
}

// Decode errors contain paths and expectations, never supplied values.
// durationPattern restricts duration strings to a single magnitude with one
// of the ms|s|m|h units (rejecting Go's ns/us and multi-unit compounds like
// "1h30m"), matching gitstore-api's and gitstore-git-service's grammar.
var durationPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(ms|s|m|h)$`)

func decodeValue(from, to reflect.Type, value any) (any, error) {
	if to == reflect.TypeFor[time.Duration]() {
		if from.Kind() != reflect.String {
			return nil, errors.New("must be a duration string")
		}
		raw, _ := value.(string)
		if !durationPattern.MatchString(strings.TrimSpace(raw)) {
			return nil, errors.New("must be a duration with a single magnitude and unit ms, s, m, or h, e.g. \"30s\"")
		}
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return nil, errors.New("must be a valid duration")
		}
		return duration, nil
	}
	switch to.Kind() {
	case reflect.String:
		if from.Kind() != reflect.String {
			return nil, errors.New("must be a string")
		}
	case reflect.Int:
		if from.Kind() == reflect.String {
			n, err := strconv.Atoi(value.(string))
			if err != nil {
				return nil, errors.New("must be an integer")
			}
			return n, nil
		}
		if from.Kind() != reflect.Int && from.Kind() != reflect.Int64 {
			return nil, errors.New("must be an integer")
		}
	case reflect.Struct:
		if from.Kind() != reflect.Map {
			return nil, errors.New("must be nested settings")
		}
	}
	return value, nil
}

func validate(cfg *Config) error {
	c := &cfg.Controller
	if c.Port < 1 || c.Port > 65535 {
		return errors.New("controller.port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.ApiURI) == "" {
		return errors.New("controller.api_uri must not be empty")
	}
	if c.APIClient.RateLimit.PerSecond < 1 || c.APIClient.RateLimit.Burst < 1 {
		return errors.New("controller.api_client.rate_limit.per_second and burst must be positive")
	}
	ref := c.ServiceAccount.KeyRef.SecretRef()
	if err := secretmaterial.ValidateSecretRef(ref, ""); err != nil {
		return errors.New("controller.serviceaccount.key_ref must be a valid SecretRef")
	}
	provider := c.SecretProviders.Bootstrap
	for _, required := range []struct{ path, value string }{
		{"controller.serviceaccount.namespace", c.ServiceAccount.Namespace},
		{"controller.serviceaccount.name", c.ServiceAccount.Name},
		{"controller.serviceaccount.uid", c.ServiceAccount.UID},
		{"controller.serviceaccount.assertion_audience", c.ServiceAccount.AssertionAudience},
		{"controller.serviceaccount.access_token_audience", c.ServiceAccount.AccessTokenAudience},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s must not be empty", required.path)
		}
	}
	if ref.Key != nil {
		return errors.New("controller.serviceaccount.key_ref.key must be omitted for atomic signing records")
	}
	if provider.Type != "file" && provider.Type != "env" {
		return errors.New("controller.secret_providers.bootstrap.type must be file or env")
	}
	if provider.Type == "file" && strings.TrimSpace(provider.BasePath) == "" {
		return errors.New("controller.secret_providers.bootstrap.base_path must not be empty")
	}
	if provider.Type == "env" && strings.TrimSpace(provider.EnvVariable) == "" {
		return errors.New("controller.secret_providers.bootstrap.env_variable must not be empty")
	}
	if c.Reconcile.MaxAttempts < 1 {
		return errors.New("controller.reconcile.max_attempts must be >= 1")
	}
	if c.Checkpoint.Dir == "" {
		return errors.New("controller.checkpoint.dir must not be empty")
	}
	if c.Checkpoint.FlushIntervalEvents < 1 {
		return errors.New("controller.checkpoint.flush_interval_events must be >= 1")
	}
	if c.Watch.ResyncInterval < 0 {
		return errors.New("controller.watch.resync_interval must not be negative")
	}
	cfg.Log.Format = strings.ToLower(cfg.Log.Format)
	if cfg.Log.Format != "json" && cfg.Log.Format != "text" {
		return errors.New("invalid log format at log.format; use json or text")
	}
	if _, err := zapcore.ParseLevel(cfg.Log.Level); err != nil {
		return errors.New("invalid log level at log.level")
	}
	return nil
}
