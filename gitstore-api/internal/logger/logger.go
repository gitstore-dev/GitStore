// Structured logging setup using zap

package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"os"
)

var Log *zap.Logger

// InitLogger initializes the global structured logger
func InitLogger() error {
	config := zap.NewProductionConfig()

	// Set log level from environment or default to info
	logLevel := os.Getenv("GITSTORE_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}

	level, err := zapcore.ParseLevel(logLevel)
	if err != nil {
		level = zapcore.InfoLevel
	}
	config.Level = zap.NewAtomicLevelAt(level)

	// JSON encoding for structured logs
	config.Encoding = "json"
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
