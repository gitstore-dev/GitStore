// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Base GraphQL resolver

package graph

import (
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"go.uber.org/zap"
)

// Resolver is the root GraphQL resolver
type Resolver struct {
	logger         *zap.Logger
	store          datastore.Datastore
	service        *Service
	authMiddleware *middleware.AuthMiddleware
	storageDataDir string // data_dir used to build storagePath in responses; defaults to "/data"
}

// NewResolver creates a new GraphQL resolver.
// writer is the GitWriter backed by the gRPC client; pass nil to disable writes.
func NewResolver(store datastore.Datastore, writer GitWriter, logger *zap.Logger) *Resolver {
	SetConverterLogger(logger)
	svc := NewServiceWithWriter(store, writer, logger)
	return &Resolver{
		logger:         logger,
		store:          store,
		service:        svc,
		storageDataDir: "/data",
	}
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
