// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

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
	ApiURI string `mapstructure:"api__uri"`

	// DefaultMaxAttempts is the global retry limit before quarantine.
	DefaultMaxAttempts int `mapstructure:"default_max_attempts"`

	// DefaultStallThreshold is parsed at startup into StallThreshold.
	DefaultStallThresholdStr string        `mapstructure:"default_stall_threshold"`
	DefaultStallThreshold    time.Duration `mapstructure:"-"`
}

// LogConfig holds logger settings.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// Load reads configuration from environment variables and optional .env file.
func Load() (*Config, error) {
	_ = godotenv.Load()

	v := viper.New()

	v.SetDefault("controller.port", 5001)
	v.SetDefault("controller.api__uri", "http://localhost:4000/graphql")
	v.SetDefault("controller.default_max_attempts", 5)
	v.SetDefault("controller.default_stall_threshold", "5m")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")

	v.SetConfigName("config")
	v.SetConfigType("toml")
	v.AddConfigPath(".")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) {
			return nil, err
		}
	}

	v.SetEnvPrefix("GITSTORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, err
	}

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
		return fmt.Errorf("controller.api__uri must not be empty")
	}
	if cfg.Controller.DefaultMaxAttempts < 1 {
		return fmt.Errorf("controller.default_max_attempts must be >= 1")
	}

	d, err := time.ParseDuration(cfg.Controller.DefaultStallThresholdStr)
	if err != nil {
		return fmt.Errorf("controller.default_stall_threshold is not a valid duration: %w", err)
	}
	cfg.Controller.DefaultStallThreshold = d

	if err := validateLogFormat(&cfg.Log); err != nil {
		return err
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
