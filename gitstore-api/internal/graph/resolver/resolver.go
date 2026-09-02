// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Base GraphQL resolver

package resolver

import (
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"go.uber.org/zap"
)

var errMissingLogger = errors.New("resolver: logger is required")

// Resolver is the root GraphQL resolver
type Resolver struct {
	logger                 *zap.Logger
	store                  datastore.Datastore
	service                *Service
	registry               *auth.ProviderRegistry
	storageDataDir         string // data_dir used to build storagePath in responses; defaults to "/data"
	clock                  apiruntime.Clock
	eventBus               *eventbus.Bus
	namespaceJournal       datastore.NamespaceWatchJournal
	namespaceSubscriber    *watchjournal.Subscriber
	namespaceWatch         config.NamespaceWatchConfig
	namespaceMetrics       *watchjournal.Metrics
	serviceAccountAudience string
	connectionRegistry     *wsregistry.Registry
}

// ResolverDeps contains dependencies for the root GraphQL resolver.
type ResolverDeps struct {
	Store                        datastore.Datastore
	GitWriter                    GitWriter
	Registry                     *auth.ProviderRegistry
	Logger                       *zap.Logger
	Clock                        apiruntime.Clock
	IDGenerator                  apiruntime.IDGenerator
	NamespaceRepositoryFenceMode NamespaceRepositoryFenceMode
	// EventBus backs the watchCategories/watchResources subscription
	// resolvers (spec 040). Optional — nil disables watch subscriptions.
	EventBus         *eventbus.Bus
	NamespaceJournal datastore.NamespaceWatchJournal
	NamespaceWatch   config.NamespaceWatchConfig
	NamespaceMetrics *watchjournal.Metrics
	// ServiceAccountAudience is the configured audience value for service
	// account token issuance (spec 061).
	ServiceAccountAudience string
	ConnectionRegistry     *wsregistry.Registry
}

// NewResolver creates a new GraphQL resolver.
func NewResolver(deps ResolverDeps) (*Resolver, error) {
	if deps.Logger == nil {
		return nil, errMissingLogger
	}
	var namespaceSubscriber *watchjournal.Subscriber
	if deps.NamespaceJournal != nil && deps.NamespaceWatch.ReadersEnabled {
		namespaceSubscriber = watchjournal.NewSubscriber(deps.NamespaceJournal, watchjournal.SubscriberConfig{
			ReadBatchSize:       deps.NamespaceWatch.ReadBatchSize,
			MaxReplayEvents:     deps.NamespaceWatch.MaxReplayEvents,
			BufferSize:          deps.NamespaceWatch.SubscriberBuffer,
			BackpressureTimeout: time.Duration(deps.NamespaceWatch.SubscriberBackpressureMillis) * time.Millisecond,
			PollMin:             time.Duration(deps.NamespaceWatch.PollMinMillis) * time.Millisecond,
			PollMax:             time.Duration(deps.NamespaceWatch.PollMaxMillis) * time.Millisecond,
			MaxMaterializerLag:  time.Duration(deps.NamespaceWatch.MaxMaterializerLagSeconds) * time.Second,
			Metrics:             deps.NamespaceMetrics,
		})
	}
	SetConverterLogger(deps.Logger)
	svc, err := NewService(ServiceDeps{
		Store:                        deps.Store,
		GitWriter:                    deps.GitWriter,
		Logger:                       deps.Logger,
		Clock:                        deps.Clock,
		IDGenerator:                  deps.IDGenerator,
		NamespaceRepositoryFenceMode: deps.NamespaceRepositoryFenceMode,
	})
	if err != nil {
		return nil, err
	}
	clock := deps.Clock
	if clock == nil {
		clock = apiruntime.SystemClock{}
	}
	return &Resolver{
		logger:                 deps.Logger,
		store:                  deps.Store,
		service:                svc,
		registry:               deps.Registry,
		storageDataDir:         "/data",
		clock:                  clock,
		eventBus:               deps.EventBus,
		namespaceJournal:       deps.NamespaceJournal,
		namespaceSubscriber:    namespaceSubscriber,
		namespaceWatch:         deps.NamespaceWatch,
		namespaceMetrics:       deps.NamespaceMetrics,
		serviceAccountAudience: deps.ServiceAccountAudience,
		connectionRegistry:     deps.ConnectionRegistry,
	}, nil
}

// WithStorageDataDir sets the data directory for deriving storage paths.
func (r *Resolver) WithStorageDataDir(dir string) *Resolver {
	r.storageDataDir = dir
	return r
}
