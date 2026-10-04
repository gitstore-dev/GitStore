// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// GraphQL API Server Main Entry Point

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/app"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/logger"
	"go.uber.org/zap"
)

func main() {
	configFiles, err := parseConfigFiles(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to parse arguments: %v\n", err)
		os.Exit(2)
	}
	var cfg *config.Config
	if len(configFiles) == 0 {
		cfg, err = config.Load()
	} else {
		cfg, err = config.LoadFromFiles(configFiles)
	}
	gin.SetMode(gin.ReleaseMode)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	log, err := logger.InitLogger(cfg.Log.Level, cfg.Log.Format)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = log.Sync() }()

	log.Info("Starting GitStore GraphQL API",
		zap.Int("port", cfg.Api.Port),
		zap.String("git.grpc.uri", cfg.Git.Grpc.Uri),
		zap.String("datastore_backend", cfg.Datastore.Backend),
	)

	server, err := app.NewServer(cfg, log)
	if err != nil {
		log.Fatal("Failed to create server", zap.Error(err))
	}
	defer server.Close()
	server.Start()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Info("Shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	server.Shutdown(shutdownCtx)

	log.Info("Server stopped")
}

// configFileFlags collects repeated --config-file occurrences in the order
// given; each one after the first is merged additively on top of the
// previous ones (see config.LoadFromFiles).
type configFileFlags []string

func (f *configFileFlags) String() string { return strings.Join(*f, ",") }

func (f *configFileFlags) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func parseConfigFiles(args []string) ([]string, error) {
	flags := flag.NewFlagSet("gitstore-api", flag.ContinueOnError)
	var paths configFileFlags
	flags.Var(&paths, "config-file", "path to a TOML configuration file; repeat to layer additive overlays on top of the first")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	return paths, nil
}
