// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Base GraphQL resolver

package resolver

import (
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"go.uber.org/zap"
)

var errMissingLogger = errors.New("resolver: logger is required")

// Resolver is the root GraphQL resolver
type Resolver struct {
	logger         *zap.Logger
	store          datastore.Datastore
	service        *Service
	authMiddleware *middleware.AuthMiddleware
	registry       *auth.ProviderRegistry
	storageDataDir string // data_dir used to build storagePath in responses; defaults to "/data"
	clock          apiruntime.Clock
}

// ResolverDeps contains dependencies for the root GraphQL resolver.
type ResolverDeps struct {
	Store       datastore.Datastore
	GitWriter   GitWriter
	AuthZ       auth.AuthZProvider
	Registry    *auth.ProviderRegistry
	Logger      *zap.Logger
	Clock       apiruntime.Clock
	IDGenerator apiruntime.IDGenerator
}

// NewResolver creates a new GraphQL resolver.
func NewResolver(deps ResolverDeps) (*Resolver, error) {
	if deps.Logger == nil {
		return nil, errMissingLogger
	}
	SetConverterLogger(deps.Logger)
	svc, err := NewService(ServiceDeps{
		Store:       deps.Store,
		GitWriter:   deps.GitWriter,
		AuthZ:       deps.AuthZ,
		Logger:      deps.Logger,
		Clock:       deps.Clock,
		IDGenerator: deps.IDGenerator,
	})
	if err != nil {
		return nil, err
	}
	clock := deps.Clock
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	return &Resolver{
		logger:         deps.Logger,
		store:          deps.Store,
		service:        svc,
		registry:       deps.Registry,
		storageDataDir: "/data",
		clock:          clock,
	}, nil
}

// WithStorageDataDir sets the data directory for deriving storage paths.
func (r *Resolver) WithStorageDataDir(dir string) *Resolver {
	r.storageDataDir = dir
	return r
}

// WithAuthMiddleware wires the auth middleware into the resolver (called from main.go).
func (r *Resolver) WithAuthMiddleware(am *middleware.AuthMiddleware) {
	r.authMiddleware = am
}
