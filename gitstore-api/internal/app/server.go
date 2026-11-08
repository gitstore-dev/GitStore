// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/kratos"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/oidcjwt"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountassertion"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountjwt"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/userdirnone"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	dsfactory "github.com/gitstore-dev/gitstore/api/internal/datastore/factory"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/githttp"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/gitstore-dev/gitstore/api/internal/notification"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const version = "0.1.0-alpha.9" // x-release-please-version

// defaultRateLimitPerSecond/defaultRateLimitBurst mirror config.Load's
// api.rate_limit_per_second/api.rate_limit_burst defaults, used as a
// fallback when NewGraphQLHandler is called directly with a zero value
// (e.g. by tests that don't go through config.Load).
const (
	defaultRateLimitPerSecond = 50
	defaultRateLimitBurst     = 100
	namespaceWatchStopTimeout = 5 * time.Second
)

// authReloader validates and reloads the complete configured auth provider set.
type authReloader interface {
	Reload() error
}

// providerShutdowner is implemented by auth providers and runtimes that own resources.
type providerShutdowner interface {
	Shutdown()
}

// Server is the composed API runtime.
type Server struct {
	cfg              *config.Config
	log              *zap.Logger
	store            datastore.Datastore
	gitClient        *gitclient.Client
	httpServers      []httpEndpoint
	connections      *wsregistry.Registry
	grpcServer       *grpc.Server
	grpcListener     net.Listener
	authReloader     authReloader
	providerShutdown []providerShutdowner
	namespaceWatch   *namespaceWatchRuntime
	endpointCleanup  []func()
}

type namespaceCDCRunner interface {
	RunNamespaceCDC(context.Context, *watchjournal.Materializer, datastore.NamespaceWatchLease, time.Duration, time.Duration, func()) error
}

// repositoryCDCRunner is optional during the rolling upgrade: older
// datastores continue serving Namespace CDC, while Scylla implementations
// with the Repository authoritative source join the same lease and journal.
type repositoryCDCRunner interface {
	RunRepositoryCDC(context.Context, *watchjournal.Materializer, datastore.ResourceWatchLease, time.Duration, time.Duration, func()) error
}

// catalogCDCRunner serves every registered namespaced catalog kind (Product,
// File, CategoryTaxonomy, ...) from its authoritative table into the shared
// journal, one reader per kind.
type catalogCDCRunner interface {
	CatalogCDCKinds() []string
	RunCatalogCDC(context.Context, string, *watchjournal.Materializer, datastore.ResourceWatchLease, time.Duration, time.Duration, func()) error
}

type namespaceWatchRuntime struct {
	journal          datastore.NamespaceWatchJournal
	materializer     *watchjournal.Materializer
	leaseManager     *watchjournal.LeaseManager
	metrics          *watchjournal.Metrics
	runner           namespaceCDCRunner
	repositoryRunner repositoryCDCRunner
	catalogRunner    catalogCDCRunner
	cfg              config.WatchJournalConfig
	log              *zap.Logger
	cancel           context.CancelFunc
	done             chan struct{}
}

func namespaceWatchMetrics(runtime *namespaceWatchRuntime) *watchjournal.Metrics {
	if runtime == nil {
		return nil
	}
	return runtime.metrics
}

func registerIdentityMailRoute(router *gin.Engine, cfg config.ApiConfig, directory auth.UserDirProvider) error {
	if err := cfg.Notification.Validate(); err != nil {
		return err
	}
	if cfg.Notification.Provider != "development" {
		return nil
	}
	provider, err := notification.NewDevelopment(cfg.Notification.Development.SMTPURI, cfg.Notification.Development.Sender, cfg.Notification.MaxConcurrent)
	if err != nil {
		return err
	}
	bodyBytes := cfg.Notification.MaxBodyBytes
	endpointBytes := cfg.Admin.Limits.BodyBytes
	if endpointBytes == 0 {
		endpointBytes = config.DefaultEndpoint("admin").Limits.BodyBytes
	}
	if endpointBytes < bodyBytes {
		bodyBytes = endpointBytes
	}
	handler, err := notification.NewMailHandler(provider, cfg.Notification.APIKey, bodyBytes, cfg.Notification.MaxConcurrent, cfg.Notification.Timeout)
	if err != nil {
		return err
	}
	router.Any("/internal/notifications/mail", gin.WrapH(handler))
	if invitations, ok := directory.(interface {
		SetInvitationSender(func(context.Context, *auth.UserProfile, string) error)
	}); ok {
		invitations.SetInvitationSender(func(ctx context.Context, profile *auth.UserProfile, link string) error {
			ctx, cancel := context.WithTimeout(ctx, cfg.Notification.Timeout)
			defer cancel()
			return provider.Send(ctx, notification.Message{Recipient: profile.Email, Subject: "Set up your GitStore account",
				Body: "Complete your GitStore account setup:\n\n" + link, TemplateType: "invitation"})
		})
	}
	return nil
}

// NewServer builds the API, Git HTTP, and catalog gRPC servers from config.
func NewServer(cfg *config.Config, log *zap.Logger) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("app: config is required")
	}
	if log == nil {
		return nil, fmt.Errorf("app: logger is required")
	}
	if err := cfg.Api.ValidatePorts(); err != nil {
		return nil, err
	}
	for kind, endpoint := range map[string]config.EndpointConfig{"admin": cfg.Api.Admin, "storefront": cfg.Api.Storefront} {
		if endpoint.Limits.BodyBytes != 0 {
			if err := endpoint.Validate(); err != nil {
				return nil, fmt.Errorf("%s profile: %w", kind, err)
			}
		}
	}
	clock := apiruntime.SystemClock{}
	instanceID := uuid.NewString()

	rawStore, err := dsfactory.NewDatastore(cfg.Api.Datastore, log, cfg.Api.Watch.Journal)
	if err != nil {
		return nil, fmt.Errorf("create datastore: %w", err)
	}
	constructed := false
	var namespaceWatch *namespaceWatchRuntime
	var resourceJournal datastore.ResourceWatchJournal
	{
		// The durable watch journal reader and CDC materializer are always
		// on — there is no watch mechanism besides the journal.
		journal, journalErr := dsfactory.ResourceWatchJournal(rawStore)
		if journalErr != nil {
			_ = rawStore.Close()
			return nil, fmt.Errorf("create resource watch journal: %w", journalErr)
		}
		resourceJournal = journal
		metrics, metricsErr := watchjournal.NewMetrics(otel.Meter("gitstore/watchjournal"))
		if metricsErr != nil {
			_ = rawStore.Close()
			return nil, fmt.Errorf("register Namespace watch metrics: %w", metricsErr)
		}
		defer func() {
			if !constructed {
				_ = metrics.Close()
			}
		}()
		clock := apiruntime.SystemClock{}
		namespaceWatch = &namespaceWatchRuntime{
			journal: journal,
			materializer: watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{
				EventTTL:         cfg.Api.Watch.Journal.Retention,
				BookmarkInterval: cfg.Api.Watch.Journal.BookmarkInterval,
				Clock:            clock,
				Metrics:          metrics,
			}),
			leaseManager: watchjournal.NewLeaseManager(
				journal,
				instanceID,
				cfg.Api.Watch.Journal.Materializer.LeaseTTL,
				cfg.Api.Watch.Journal.Materializer.LeaseRenewInterval,
				clock,
			),
			metrics: metrics,
			cfg:     cfg.Api.Watch.Journal,
			log:     log,
		}
		if runner, ok := rawStore.(namespaceCDCRunner); ok {
			namespaceWatch.runner = runner
		}
		if runner, ok := rawStore.(repositoryCDCRunner); ok {
			namespaceWatch.repositoryRunner = runner
		}
		if runner, ok := rawStore.(catalogCDCRunner); ok {
			namespaceWatch.catalogRunner = runner
		}
	}
	store := datastore.NewInstrumentedDatastore(rawStore, cfg.Api.Datastore.Backend, log)
	ids := apiruntime.UUIDGenerator{}

	gitClient, err := gitclient.NewClientWithAddr(cfg.Api.GitService.Uri, cfg.GrpcAuth.HmacSecret)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("connect git-service: %w", err)
	}
	revocations, ok := rawStore.(staticusers.RevocationStore)
	if !ok {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("datastore does not implement shared session revocations")
	}
	registry, authReloader, providerShutdowns, err := buildProviderRegistry(cfg, store, log, revocations, resourceJournal)
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("build auth provider registry: %w", err)
	}
	defer func() {
		if !constructed {
			for _, provider := range providerShutdowns {
				provider.Shutdown()
			}
			_ = gitClient.Close()
			_ = store.Close()
		}
	}()
	log.Info("auth providers ready",
		zap.Strings("authn_chain", cfg.Api.Auth.AuthN.Chain),
		zap.String("authz_provider", cfg.Api.Auth.AuthZ.Provider),
		zap.String("userdir_provider", cfg.Api.Auth.UserDir.Provider),
	)
	gitClient.Registry = registry
	gitClient.Store = store
	if err := ensureBootstrapResources(context.Background(), store, gitClient, clock, ids, log); err != nil {
		return nil, fmt.Errorf("bootstrap resources: %w", err)
	}

	// The same admission runtime serves post-receive batches and synchronous
	// GraphQL committed-manifest convergence. Build it before the GraphQL schema
	// so resolvers never acquire a datastore-only authoring path.
	catalogServer, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Registry:              registry,
		ServiceSecret:         cfg.GrpcAuth.HmacSecret,
		ServiceSecretPrevious: cfg.GrpcAuth.HmacSecretPrevious,
		Store:                 store,
		GitClient:             gitClient,
		Logger:                log,
		Clock:                 clock,
	})
	if err != nil {
		return nil, err
	}
	connections := wsregistry.New()
	if runtime, ok := authReloader.(*providerRegistryRuntime); ok {
		runtime.setInvalidationHandler(connections.Invalidate)
	}
	var endpointCleanup []func()
	registerCleanup := func(close func()) { endpointCleanup = append(endpointCleanup, close) }
	defer func() {
		if !constructed {
			connections.Close()
			for _, close := range endpointCleanup {
				close()
			}
		}
	}()
	gqlRouter, err := NewGraphQLHandler(GraphQLHandlerDeps{
		RegisterCleanup:           registerCleanup,
		ConnectionRegistry:        connections,
		Endpoint:                  cfg.Api.Admin,
		TrustedProxies:            cfg.Api.TrustedProxies,
		Store:                     store,
		GitWriter:                 gitClient,
		Logger:                    log,
		Registry:                  registry,
		Clock:                     clock,
		IDs:                       ids,
		CommittedManifestAdmitter: catalogServer,
		ResourceJournal:           resourceJournal,
		NamespaceWatch:            cfg.Api.Watch.Journal,
		NamespaceMetrics:          namespaceWatchMetrics(namespaceWatch),
		ServiceAccountAudience:    cfg.Api.Auth.ServiceAccount.Audience,
		RateLimitPerSecond:        cfg.Api.RateLimit.PerSecond,
		RateLimitBurst:            cfg.Api.RateLimit.Burst,
		PushLimits:                cfg.PushLimits,
	})
	if err != nil {
		return nil, err
	}

	if err := registerIdentityMailRoute(gqlRouter, cfg.Api, registry.UserDir()); err != nil {
		return nil, err
	}
	router := healthHandler(gqlRouter, store, log, clock, namespaceWatch, instanceID, registerCleanup)
	var graphQlHandler http.Handler = router

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.AdminPort),
		Handler:      graphQlHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	gitHttpHandler := githttp.NewMux(githttp.SmartHttpDeps{
		GitClient: gitClient,
		Store:     store,
		Logger:    log,
		Ids:       ids,
		Registry:  registry,
	})
	gitHttpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.GitPort),
		Handler:      observability.HTTPHandler(gitHttpHandler, log),
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Api.GrpcPort))
	if err != nil {
		return nil, fmt.Errorf("listen on catalog gRPC port %d: %w", cfg.Api.GrpcPort, err)
	}
	publicRouter, err := NewStorefrontHandler(cfg.Api.Storefront, cfg.Api.StorefrontRateLimit, cfg.Api.TrustedProxies, registerCleanup)
	if err != nil {
		_ = grpcListener.Close()
		return nil, err
	}
	publicServer := &http.Server{Addr: fmt.Sprintf(":%d", cfg.Api.StorefrontPort), Handler: publicRouter, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second}
	httpEndpoints := []httpEndpoint{{name: "admin", server: httpServer}, {name: "storefront", server: publicServer}, {name: "git-http", server: gitHttpServer}}
	webhooks, err := kratosWebhookServer(cfg.Api.Auth.Kratos, registry)
	if err != nil {
		_ = grpcListener.Close()
		return nil, err
	}
	if webhooks != nil {
		httpEndpoints = append(httpEndpoints, httpEndpoint{name: "kratos-webhooks", server: webhooks})
	}
	endpoints, err := bindHTTPServers(httpEndpoints)
	if err != nil {
		_ = grpcListener.Close()
		return nil, err
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(catalogTelemetry))
	catalogv1.RegisterCatalogServiceServer(grpcServer, catalogServer)
	constructed = true

	return &Server{
		cfg:              cfg,
		log:              log,
		store:            store,
		gitClient:        gitClient,
		httpServers:      endpoints,
		connections:      connections,
		grpcServer:       grpcServer,
		grpcListener:     grpcListener,
		authReloader:     authReloader,
		providerShutdown: providerShutdowns,
		namespaceWatch:   namespaceWatch,
		endpointCleanup:  endpointCleanup,
	}, nil
}

func healthHandler(router *gin.Engine, store datastore.Datastore, log *zap.Logger, clock apiruntime.Clock, namespaceWatch *namespaceWatchRuntime, instanceID string, registerCleanup func(func())) *gin.Engine {
	var resourceWatchReady func(context.Context) error
	if namespaceWatch != nil {
		resourceWatchReady = func(ctx context.Context) error {
			return namespaceWatchReadiness(ctx, namespaceWatch, clock.Now())
		}
	}
	healthHandler := health.NewHandler(health.HandlerDeps{
		Store:              store,
		Logger:             log,
		Version:            version,
		Clock:              clock,
		ResourceWatchReady: resourceWatchReady,
		InstanceID:         instanceID,
		RegisterCleanup:    registerCleanup,
	})

	router.GET("/health", healthHandler.Health)
	router.GET("/ready", healthHandler.Ready)
	router.GET("/metrics", healthHandler.Metrics)
	return router
}

func namespaceWatchReadiness(ctx context.Context, runtime *namespaceWatchRuntime, now time.Time) error {
	bounds, err := runtime.journal.Bounds(ctx)
	if err != nil {
		return err
	}
	// Bounds are shared durable state. Refreshing metrics here keeps follower
	// replicas aligned with the leader even though they do not receive the
	// materializer's process-local observation callbacks.
	runtime.metrics.SetBounds(bounds, now)
	maxLag := runtime.cfg.Materializer.MaxLag
	if bounds.ProgressAt.IsZero() || now.Sub(bounds.ProgressAt) > maxLag {
		return watchjournal.ErrMaterializerNotReady
	}
	return nil
}

// serviceAccountStore is the narrow datastore seam shared by the
// ServiceAccount AuthN providers. Assertion authentication additionally needs
// the durable replay operation while JWT authentication uses only the lookup.
type serviceAccountStore interface {
	GetServiceAccountBySubject(ctx context.Context, name string) (*datastore.ServiceAccount, error)
	TryConsumeServiceAccountAssertion(ctx context.Context, jtiDigest string, expiresAt time.Time) (bool, error)
}

// buildProviderRegistry constructs a ProviderRegistry from the application config.
// It reads authn chain, authz provider, and userdir provider from the resolved config.
// The second return value validates and atomically swaps the complete auth set on
// SIGHUP. The third return value lists resources that must be shut down with the server.
func buildProviderRegistry(cfg *config.Config, store serviceAccountStore, log *zap.Logger, revocations staticusers.RevocationStore, journals ...datastore.ResourceWatchJournal) (*auth.ProviderRegistry, authReloader, []providerShutdowner, error) {
	registry, shutdowns, err := constructProviderRegistry(cfg, store, log, revocations, journals...)
	if err != nil {
		return nil, nil, nil, err
	}
	runtime := &providerRegistryRuntime{
		cfg: cfg, store: store, log: log, revocations: revocations, registry: registry, shutdowns: shutdowns,
	}
	if len(journals) > 0 {
		runtime.journal = journals[0]
	}
	return registry, runtime, []providerShutdowner{runtime}, nil
}

type providerRegistryRuntime struct {
	mu                    sync.Mutex
	cfg                   *config.Config
	store                 serviceAccountStore
	log                   *zap.Logger
	revocations           staticusers.RevocationStore
	registry              *auth.ProviderRegistry
	shutdowns             []providerShutdowner
	closed                bool
	journal               datastore.ResourceWatchJournal
	invalidateConnections func()
}

func (r *providerRegistryRuntime) setInvalidationHandler(handler func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidateConnections = handler
	if directory, ok := r.registry.UserDir().(interface{ SetInvalidationHandler(func()) }); ok {
		directory.SetInvalidationHandler(handler)
	}
}

func (r *providerRegistryRuntime) Reload() (err error) {
	started := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() {
		auth.ObserveProviderRegistryReload(err == nil)
		diagnostics := r.registry.Diagnostics()
		fields := []zap.Field{
			zap.String("catalog_digest", diagnostics.CatalogDigest),
			zap.String("authz_provider", diagnostics.AuthZ),
			zap.Duration("duration", time.Since(started)),
		}
		if diagnostics.Policy != nil {
			fields = append(fields, zap.String("policy_digest", diagnostics.Policy.Digest))
		}
		if err != nil {
			r.log.Warn("auth provider registry reload rejected", fields...)
		} else {
			r.log.Info("auth provider registry reload accepted", fields...)
		}
	}()
	if r.closed {
		return errors.New("auth provider registry is closed")
	}
	newRegistry, newShutdowns, err := constructProviderRegistry(r.cfg, r.store, r.log, r.revocations, r.journal)
	if err != nil {
		return err
	}
	oldShutdowns := r.shutdowns
	if directory, ok := newRegistry.UserDir().(interface{ SetInvalidationHandler(func()) }); ok {
		directory.SetInvalidationHandler(r.invalidateConnections)
	}
	r.registry.Swap(newRegistry.AuthN(), newRegistry.AuthZ(), newRegistry.UserDir())
	r.shutdowns = newShutdowns
	for _, provider := range oldShutdowns {
		provider.Shutdown()
	}
	return nil
}

func (r *providerRegistryRuntime) Shutdown() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	shutdowns := r.shutdowns
	r.shutdowns = nil
	r.mu.Unlock()
	for _, provider := range shutdowns {
		provider.Shutdown()
	}
}

func constructProviderRegistry(cfg *config.Config, store serviceAccountStore, log *zap.Logger, revocations staticusers.RevocationStore, journals ...datastore.ResourceWatchJournal) (*auth.ProviderRegistry, []providerShutdowner, error) {
	// Build AuthN providers in chain order.
	chain := cfg.Api.Auth.AuthN.Chain
	if len(chain) == 0 {
		chain = []string{"static-users", "anonymous"}
	}

	var authnProviders []auth.AuthNProvider
	var shutdowns []providerShutdowner
	cleanup := func() {
		for _, provider := range shutdowns {
			provider.Shutdown()
		}
	}
	var staticUsersProvider *staticusers.StaticUsersProvider
	for _, name := range chain {
		switch name {
		case "static-users":
			p, err := staticusers.NewWithRevocationStore(cfg.Api.Auth, log, revocations)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init static-users provider: %w", err)
			}
			staticUsersProvider = p
			authnProviders = append(authnProviders, p)
			shutdowns = append(shutdowns, p)
		case "anonymous":
			authnProviders = append(authnProviders, anonymous.New())
		case "serviceaccount-assertion":
			p, err := serviceaccountassertion.New(cfg.Api.Auth.ServiceAccount, store, log)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init serviceaccount-assertion provider: %w", err)
			}
			authnProviders = append(authnProviders, p)
		case "serviceaccount-jwt":
			p, err := serviceaccountjwt.New(cfg.Api.Auth.ServiceAccount, store, log)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init serviceaccount-jwt provider: %w", err)
			}
			authnProviders = append(authnProviders, p)
			shutdowns = append(shutdowns, p)
		case "oidc-jwt":
			p, err := oidcjwt.New(context.Background(), cfg.Api.Auth.OIDC, log)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init oidc-jwt provider: %w", err)
			}
			authnProviders = append(authnProviders, p)
			shutdowns = append(shutdowns, p)
		default:
			cleanup()
			return nil, nil, fmt.Errorf("unknown authn provider %q", name)
		}
	}

	// Build AuthZ provider.
	var authzProvider auth.AuthZProvider
	var rbacProvider *rbaclocal.RBACLocalProvider
	switch cfg.Api.Auth.AuthZ.Provider {
	case "rbac-local":
		p, err := rbaclocal.New(cfg.Api.Auth.RBACLocal, log)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init rbac-local authz provider: %w", err)
		}
		authzProvider = p
		rbacProvider = p
	case "allow-all":
		authzProvider = allowall.New(log)
	default:
		cleanup()
		return nil, nil, fmt.Errorf("unknown authz provider %q", cfg.Api.Auth.AuthZ.Provider)
	}
	// DecisionLogger is the required middleware that keeps every AuthZ decision
	// consistent with the pluggable auth architecture audit contract.
	authzProvider = auth.NewDecisionLogger(authzProvider, log)

	// Build UserDir provider.
	var userdirProvider auth.UserDirProvider
	switch cfg.Api.Auth.UserDir.Provider {
	case "none", "":
		userdirProvider = userdirnone.New()
	case "static-users":
		if staticUsersProvider == nil {
			p, err := staticusers.NewDirectory(cfg.Api.Auth.StaticUsers, log)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init static-users directory: %w", err)
			}
			staticUsersProvider = p
		}
		userdirProvider = staticUsersProvider
	case "kratos":
		p, err := kratos.New(cfg.Api.Auth.Kratos, log)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init kratos userdir provider: %w", err)
		}
		shutdowns = append(shutdowns, p)
		if len(journals) > 0 && journals[0] != nil {
			source, ok := store.(kratos.ServiceAccountSource)
			if !ok {
				cleanup()
				return nil, nil, fmt.Errorf("workload mirror source does not support UID lookup")
			}
			if err := p.StartServiceAccountConsumer(journals[0], source); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("start workload directory consumer: %w", err)
			}
		}
		userdirProvider = p
	default:
		cleanup()
		return nil, nil, fmt.Errorf("unknown userdir provider %q", cfg.Api.Auth.UserDir.Provider)
	}

	if staticUsersProvider != nil && rbacProvider != nil {
		profiles := make([]*auth.UserProfile, 0)
		for _, username := range staticUsersProvider.Usernames() {
			profile, err := staticUsersProvider.GetBySubject(context.Background(), username)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("validate local user: %w", err)
			}
			profiles = append(profiles, profile)
		}
		if err := rbacProvider.SetActiveUsers(profiles); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("startup failed: %w (policy %s)", err, cfg.Api.Auth.RBACLocal.PolicyFile)
		}
	}
	return auth.NewProviderRegistry(auth.NewChainedAuthN(authnProviders...), authzProvider, userdirProvider), shutdowns, nil
}

// Start starts all servers in background goroutines.
func (s *Server) Start() {
	// The CDC materializer is always on — there is no watch mechanism besides
	// the durable journal.
	if s.namespaceWatch != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.namespaceWatch.cancel = cancel
		s.namespaceWatch.done = make(chan struct{})
		go func() {
			defer close(s.namespaceWatch.done)
			s.namespaceWatch.run(ctx)
		}()
	}
	// Listen for SIGHUP to validate and atomically swap the complete auth set.
	if s.authReloader != nil {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		go func() {
			for range sigCh {
				if err := s.authReloader.Reload(); err != nil {
					s.log.Error("auth provider reload failed; previous configuration remains active")
				} else {
					s.log.Info("auth providers reloaded atomically")
				}
			}
		}()
	}

	for _, endpoint := range s.httpServers {
		go func() {
			s.log.Info("HTTP listener starting", zap.String("listener", endpoint.name), zap.String("address", endpoint.listener.Addr().String()))
			if err := endpoint.server.Serve(endpoint.listener); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				s.log.Error("HTTP server stopped", zap.String("listener", endpoint.name), zap.Error(err))
			}
		}()
	}

	go func() {
		s.log.Info("CatalogService gRPC server starting", zap.Int("port", s.cfg.Api.GrpcPort))
		if err := s.grpcServer.Serve(s.grpcListener); err != nil {
			s.log.Error("CatalogService gRPC server error", zap.Error(err))
		}
	}()

	s.log.Info("Server ready",
		zap.String("graphql", fmt.Sprintf("http://localhost:%d/admin/graphql", s.cfg.Api.AdminPort)),
		zap.String("playground", fmt.Sprintf("http://localhost:%d/admin/playground", s.cfg.Api.AdminPort)),
		zap.String("health", fmt.Sprintf("http://localhost:%d/health", s.cfg.Api.AdminPort)),
		zap.String("ready", fmt.Sprintf("http://localhost:%d/ready", s.cfg.Api.AdminPort)),
		zap.String("storefront", fmt.Sprintf("http://localhost:%d/storefront/graphql", s.cfg.Api.StorefrontPort)),
		zap.String("git_http", fmt.Sprintf("http://localhost:%d", s.cfg.Api.GitPort)),
		zap.String("catalog_grpc", fmt.Sprintf("localhost:%d", s.cfg.Api.GrpcPort)),
	)
}

// Shutdown gracefully stops all network servers.
func (s *Server) Shutdown(ctx context.Context) {
	if s.connections != nil {
		s.connections.Close()
	}
	var workers sync.WaitGroup
	for _, endpoint := range s.httpServers {
		workers.Go(func() {
			if err := endpoint.server.Shutdown(ctx); err != nil {
				_ = endpoint.server.Close()
				s.log.Error("HTTP shutdown", zap.String("listener", endpoint.name), zap.Error(err))
			}
		})
	}
	if s.grpcServer != nil {
		done := make(chan struct{})
		go func() { s.grpcServer.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			s.grpcServer.Stop()
			<-done
		}
	}
	workers.Wait()
}

// Close releases non-server resources.
func (s *Server) Close() {
	for _, close := range s.endpointCleanup {
		close()
	}
	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}
	if s.connections != nil {
		s.connections.Close()
	}
	for _, endpoint := range s.httpServers {
		_ = endpoint.server.Close()
		_ = endpoint.listener.Close()
	}
	if s.namespaceWatch != nil && s.namespaceWatch.cancel != nil {
		s.namespaceWatch.cancel()
		if s.namespaceWatch.done != nil {
			timer := time.NewTimer(namespaceWatchStopTimeout)
			defer timer.Stop()
			select {
			case <-s.namespaceWatch.done:
			case <-timer.C:
				s.log.Warn("Resource watch materializer shutdown timed out")
			}
		}
	}
	if s.namespaceWatch != nil && s.namespaceWatch.metrics != nil {
		_ = s.namespaceWatch.metrics.Close()
	}
	for _, p := range s.providerShutdown {
		p.Shutdown()
	}
	if s.grpcListener != nil {
		_ = s.grpcListener.Close()
	}
	if s.gitClient != nil {
		_ = s.gitClient.Close()
	}
	if s.store != nil {
		_ = s.store.Close()
	}
}

func (r *namespaceWatchRuntime) run(ctx context.Context) {
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	for {
		lease, acquired, err := r.leaseManager.Acquire(ctx)
		if err != nil {
			r.log.Error("Resource watch materializer lease acquisition failed", zap.Error(err))
		} else if acquired {
			err = r.runAsLeader(ctx, lease)
			if errors.Is(err, datastore.ErrNamespaceWatchDiscontinuity) {
				r.log.Error("Resource watch materializer stopped after an ordering discontinuity; operator repair is required", zap.Error(err))
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-retry.C:
		}
	}
}

func (r *namespaceWatchRuntime) runAsLeader(parent context.Context, lease datastore.NamespaceWatchLease) error {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()
	r.metrics.SetLeader(true)
	defer r.metrics.SetLeader(false)

	var catalogKinds []string
	if r.catalogRunner != nil {
		catalogKinds = r.catalogRunner.CatalogCDCKinds()
	}
	sources := len(catalogKinds) + 2
	errCh := make(chan error, sources+1)
	workers.Go(func() {
		errCh <- r.leaseManager.Maintain(ctx, lease)
	})
	if r.runner != nil || r.repositoryRunner != nil || len(catalogKinds) > 0 {
		ready := make(chan struct{}, sources)
		readyCount := 0
		changeAgeLimit := config.JournalCDCRetention
		confidenceWindow := r.cfg.CDC.ConfidenceWindow
		markReady := func() { ready <- struct{}{} }
		if r.runner != nil {
			readyCount++
			workers.Go(func() {
				errCh <- r.runner.RunNamespaceCDC(ctx, r.materializer, lease, changeAgeLimit, confidenceWindow, markReady)
			})
		}
		if r.repositoryRunner != nil {
			readyCount++
			workers.Go(func() {
				errCh <- r.repositoryRunner.RunRepositoryCDC(ctx, r.materializer, lease, changeAgeLimit, confidenceWindow, markReady)
			})
		}
		for _, kind := range catalogKinds {
			readyCount++
			workers.Go(func() {
				errCh <- r.catalogRunner.RunCatalogCDC(ctx, kind, r.materializer, lease, changeAgeLimit, confidenceWindow, markReady)
			})
		}
		for range readyCount {
			select {
			case <-parent.Done():
				return parent.Err()
			case err := <-errCh:
				if err != nil && !errors.Is(err, context.Canceled) {
					r.log.Warn("Resource watch materializer failed before CDC readiness", zap.Error(err))
				}
				return err
			case <-ready:
			}
		}
	}
	if _, err := r.materializer.AppendBookmark(ctx, lease); err != nil {
		// Shutdown releases the lease (LeaseManager.Maintain), so a bookmark
		// racing cancellation fails as a stale lease; report the cancellation.
		if parent.Err() != nil {
			return parent.Err()
		}
		r.log.Error("Resource watch materializer initial bookmark failed", zap.Error(err))
		return err
	}
	bookmark := time.NewTicker(r.cfg.BookmarkInterval)
	defer bookmark.Stop()
	for {
		select {
		case <-parent.Done():
			return parent.Err()
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				r.log.Warn("Resource watch materializer leadership ended", zap.Error(err))
			}
			return err
		case <-bookmark.C:
			if _, err := r.materializer.AppendBookmark(ctx, lease); err != nil {
				if parent.Err() != nil {
					return parent.Err()
				}
				r.log.Error("Resource watch materializer bookmark failed", zap.Error(err))
				return err
			}
		}
	}
}
