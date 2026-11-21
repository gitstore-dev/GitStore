// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Base GraphQL resolver

package resolver

import (
	"context"
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

var errMissingLogger = errors.New("resolver: logger is required")

func rbacUnavailableError() error {
	return &gqlerror.Error{
		Message:    "dynamic authorization is unavailable",
		Extensions: map[string]any{"code": "AUTHORIZATION_UNAVAILABLE", "retryable": true},
	}
}

// Resolver is the root GraphQL resolver
type Resolver struct {
	logger                 *zap.Logger
	store                  datastore.Datastore
	service                *Service
	registry               *auth.ProviderRegistry
	storageDataDir         string // data_dir used to build storagePath in responses; defaults to "/data"
	clock                  apiruntime.Clock
	resourceJournal        datastore.ResourceWatchJournal
	namespaceSubscriber    *watchjournal.Subscriber
	namespaceWatch         config.WatchJournalConfig
	namespaceMetrics       *watchjournal.Metrics
	serviceAccountAudience string
	connectionRegistry     *wsregistry.Registry
	rbacBuiltinsResources  []*rbac.Resource
	rbacBuiltinDigest      string
	rbacValidator          *admission.RBACValidator
	watchRechecks          chan struct{}
	rbacSourceReads        chan struct{}
}

// ResolverDeps contains dependencies for the root GraphQL resolver.
type ResolverDeps struct {
	ProvisionNamespaceRepository func(context.Context, string) error
	Store                        datastore.Datastore
	GitWriter                    GitWriter
	Registry                     *auth.ProviderRegistry
	Logger                       *zap.Logger
	Clock                        apiruntime.Clock
	IDGenerator                  apiruntime.IDGenerator
	CommittedManifestAdmitter    admission.CommittedManifestAdmitter
	ResourceJournal              datastore.ResourceWatchJournal
	NamespaceWatch               config.WatchJournalConfig
	NamespaceMetrics             *watchjournal.Metrics
	// PushLimits is the shared static platform push-size ceiling; see
	// config.PushLimitsConfig.
	PushLimits config.PushLimitsConfig
	// ServiceAccountAudience is the configured audience value for service
	// account token issuance (spec 061).
	ServiceAccountAudience string
	ConnectionRegistry     *wsregistry.Registry
	RBACValidator          *admission.RBACValidator
}

// NewResolver creates a new GraphQL resolver.
func NewResolver(deps ResolverDeps) (*Resolver, error) {
	if deps.Logger == nil {
		return nil, errMissingLogger
	}
	if deps.RBACValidator != nil && deps.RBACValidator.Capture.Registry != deps.Registry {
		return nil, errors.New("resolver: RBAC validator must use the selected provider registry")
	}
	builtins, err := rbac.ReleaseBuiltins()
	if err != nil {
		return nil, err
	}
	builtinResources, err := builtins.Resources()
	if err != nil {
		return nil, err
	}
	var namespaceSubscriber *watchjournal.Subscriber
	if deps.ResourceJournal != nil {
		namespaceSubscriber = watchjournal.NewSubscriber(deps.ResourceJournal, watchjournal.SubscriberConfig{
			ReadBatchSize:       deps.NamespaceWatch.Read.BatchSize,
			MaxReplayEvents:     deps.NamespaceWatch.Read.MaxReplayEvents,
			BufferSize:          deps.NamespaceWatch.Subscriber.Buffer,
			BackpressureTimeout: deps.NamespaceWatch.Subscriber.Backpressure,
			PollMin:             deps.NamespaceWatch.Poll.Min,
			PollMax:             deps.NamespaceWatch.Poll.Max,
			MaxMaterializerLag:  deps.NamespaceWatch.Materializer.MaxLag,
			Metrics:             deps.NamespaceMetrics,
		})
	}
	SetConverterLogger(deps.Logger)
	svc, err := NewService(ServiceDeps{
		EnableRBACNamespaceCleanup:   deps.RBACValidator != nil,
		ProvisionNamespaceRepository: deps.ProvisionNamespaceRepository,
		Store:                        deps.Store,
		GitWriter:                    deps.GitWriter,
		Logger:                       deps.Logger,
		Clock:                        deps.Clock,
		IDGenerator:                  deps.IDGenerator,
		CommittedManifestAdmitter:    deps.CommittedManifestAdmitter,
		PushLimits:                   deps.PushLimits,
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
		resourceJournal:        deps.ResourceJournal,
		namespaceSubscriber:    namespaceSubscriber,
		namespaceWatch:         deps.NamespaceWatch,
		namespaceMetrics:       deps.NamespaceMetrics,
		serviceAccountAudience: deps.ServiceAccountAudience,
		connectionRegistry:     deps.ConnectionRegistry,
		rbacBuiltinsResources:  builtinResources,
		rbacBuiltinDigest:      builtins.Digest(),
		rbacValidator:          deps.RBACValidator,
		watchRechecks:          make(chan struct{}, 32),
		rbacSourceReads:        make(chan struct{}, 4),
	}, nil
}

// WithStorageDataDir sets the data directory for deriving storage paths.
func (r *Resolver) WithStorageDataDir(dir string) *Resolver {
	r.storageDataDir = dir
	return r
}
