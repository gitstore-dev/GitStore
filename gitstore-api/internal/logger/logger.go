// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Structured logging setup using zap

package logger

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Log *zap.Logger

// InitLogger initializes the global structured logger with the given log level and format.
// If logLevel is empty or invalid, INFO level is used.
func InitLogger(logLevel, logFormat string) error {
	config := zap.NewProductionConfig()

	level, err := zapcore.ParseLevel(logLevel)
	if err != nil {
		level = zapcore.InfoLevel
	}
	config.Level = zap.NewAtomicLevelAt(level)

	switch strings.ToLower(logFormat) {
	case "", "json":
		config.Encoding = "json"
	case "text":
		config.Encoding = "console"
	default:
		return fmt.Errorf("invalid log format %q; valid values: json, text", logFormat)
	}

	config.EncoderConfig.TimeKey = "timestamp"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	logger, err := config.Build()
	if err != nil {
		return err
	}

	Log = logger
	return nil
}

// Sync flushes any buffered log entries
func Sync() {
	if Log != nil {
		_ = Log.Sync()
	}
}
