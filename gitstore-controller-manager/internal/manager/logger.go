// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// InitLogger initialises and returns a structured zap logger.
// Matches the logger pattern in gitstore-api/internal/logger.
func InitLogger(logLevel, logFormat string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()

	level, err := zapcore.ParseLevel(logLevel)
	if err != nil {
		level = zapcore.InfoLevel
	}
	cfg.Level = zap.NewAtomicLevelAt(level)

	switch strings.ToLower(logFormat) {
	case "", "json":
		cfg.Encoding = "json"
	case "text":
		cfg.Encoding = "console"
	default:
		return nil, fmt.Errorf("invalid log format %q; valid values: json, text", logFormat)
	}

	cfg.EncoderConfig.TimeKey = "timestamp"
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	return cfg.Build()
}
