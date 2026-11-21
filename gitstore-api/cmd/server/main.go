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
	"github.com/gitstore-dev/gitstore/api/internal/gitctl"
	"github.com/gitstore-dev/gitstore/api/internal/logger"
	"github.com/gitstore-dev/gitstore/observability"
	"go.uber.org/zap"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "gitctl" {
		os.Exit(gitctl.Run(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	options, err := parseOptions(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to parse arguments: %v\n", err)
		os.Exit(2)
	}
	if options.roles != "" {
		if err := os.Setenv("GITSTORE_API__ROLES", options.roles); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "Failed to select runtime roles")
			os.Exit(1)
		}
	} else if options.all {
		if err := os.Setenv("GITSTORE_API__ROLES", strings.Join(config.DefaultRoles(), ",")); err != nil {
			os.Exit(1)
		}
	}
	if err := loadSigningKeyFile(options.signingKeyFile); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "Failed to load service-account signing key: %v\n", err)
		os.Exit(1)
	}
	configFiles := options.configFiles
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
	log, shutdownTelemetry, err := app.InitTelemetry(context.Background(), cfg.Observability.OTLP, log)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Failed to initialize telemetry")
		os.Exit(1)
	}
	logger.Log = log
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(ctx); err != nil {
			log.Error("telemetry shutdown failed")
		}
	}()

	shutdownProfiler, err := observability.StartCapacityProfiler(cfg.Api.Profiling, log)
	if err != nil {
		log.Fatal("Failed to initialize capacity profiler", zap.Error(err))
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownProfiler(ctx); err != nil {
			log.Error("capacity profiler shutdown failed", zap.Error(err))
		}
	}()

	log.Info("Starting GitStore API",
		zap.Strings("roles", cfg.Api.Roles),
		zap.String("graphql_layout", cfg.Api.GraphQLLayout),
		zap.String("endpoint_config_digest", cfg.Api.Advertised.Digest()),
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

type serverOptions struct {
	configFiles    configFileFlags
	signingKeyFile string
	roles          string
	all            bool
}

func parseOptions(args []string) (serverOptions, error) {
	if len(args) > 0 && args[0] == "serve" {
		args = args[1:]
	}
	flags := flag.NewFlagSet("gitstore-api", flag.ContinueOnError)
	var options serverOptions
	flags.Var(&options.configFiles, "config-file", "path to a TOML configuration file; repeat to layer additive overlays on top of the first")
	flags.StringVar(&options.signingKeyFile, "serviceaccount-signing-key-file", "", "API-only file containing the service-account issuer private key")
	flags.StringVar(&options.roles, "roles", "", "comma-separated runtime roles: smart-http,catalog,admin,storefront")
	flags.BoolVar(&options.all, "all", false, "select all enabled roles (Storefront publication is not yet available)")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("unexpected argument: %s", flags.Arg(0))
	}
	if options.all && options.roles != "" {
		return options, fmt.Errorf("--all and --roles are mutually exclusive")
	}
	emptyRoles := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "roles" && options.roles == "" {
			emptyRoles = true
		}
	})
	if emptyRoles {
		return options, fmt.Errorf("--roles must not be empty")
	}
	if options.roles != "" {
		if err := (config.ApiConfig{Roles: strings.Split(options.roles, ",")}).ValidateRuntime(); err != nil {
			return options, err
		}
	}
	return options, nil
}

func loadSigningKeyFile(path string) error {
	if path == "" {
		return nil
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(key)) == "" {
		return fmt.Errorf("signing key file is empty")
	}
	// Match the former container wrapper: an explicitly mounted key overrides
	// the environment, while config.Load still enforces the shared-file guard.
	return os.Setenv("GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY", string(key))
}
