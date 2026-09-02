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
	"strings"
	"sync"
	"syscall"
	"time"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/gin-gonic/gin"
	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountassertion"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountjwt"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/userdirnone"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	dsfactory "github.com/gitstore-dev/gitstore/api/internal/datastore/factory"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/githttp"
	"github.com/gitstore-dev/gitstore/api/internal/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/health"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
	"google.golang.org/grpc"
)

const version = "0.1.0-alpha.2" // x-release-please-version

// eventBusCapacity is the number of recent events retained per resource
// kind for watch-subscription resume (spec 040, research.md R3).
const eventBusCapacity = 1000

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
	httpServer       *http.Server
	gitServer        *http.Server
	grpcServer       *grpc.Server
	grpcListener     net.Listener
	authReloader     authReloader
	providerShutdown []providerShutdowner
	namespaceWatch   *namespaceWatchRuntime
}

type namespaceCDCRunner interface {
	RunNamespaceCDC(context.Context, *watchjournal.Materializer, datastore.NamespaceWatchLease, time.Duration, time.Duration, func()) error
}

type namespaceWatchRuntime struct {
	journal      datastore.NamespaceWatchJournal
	materializer *watchjournal.Materializer
	leaseManager *watchjournal.LeaseManager
	metrics      *watchjournal.Metrics
	runner       namespaceCDCRunner
	cfg          config.NamespaceWatchConfig
	log          *zap.Logger
	cancel       context.CancelFunc
	done         chan struct{}
}

func namespaceWatchMetrics(runtime *namespaceWatchRuntime) *watchjournal.Metrics {
	if runtime == nil {
		return nil
	}
	return runtime.metrics
}

// NewServer builds the API, Git HTTP, and catalog gRPC servers from config.
func NewServer(cfg *config.Config, log *zap.Logger) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("app: config is required")
	}
	if log == nil {
		return nil, fmt.Errorf("app: logger is required")
	}
	clock := apiruntime.SystemClock{}

	rawStore, err := dsfactory.NewDatastore(cfg.Datastore, log, cfg.Watch.Namespace)
	if err != nil {
		return nil, fmt.Errorf("create datastore: %w", err)
	}
	var namespaceWatch *namespaceWatchRuntime
	var namespaceJournal datastore.NamespaceWatchJournal
	if cfg.Watch.Namespace.ReadersEnabled || cfg.Watch.Namespace.MaterializerEnabled {
		journal, journalErr := dsfactory.NamespaceWatchJournal(rawStore)
		if journalErr != nil {
			_ = rawStore.Close()
			return nil, fmt.Errorf("create Namespace watch journal: %w", journalErr)
		}
		namespaceJournal = journal
		metrics, metricsErr := watchjournal.NewMetrics(prometheus.DefaultRegisterer)
		if metricsErr != nil {
			_ = rawStore.Close()
			return nil, fmt.Errorf("register Namespace watch metrics: %w", metricsErr)
		}
		clock := apiruntime.SystemClock{}
		namespaceWatch = &namespaceWatchRuntime{
			journal: journal,
			materializer: watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{
				EventTTL:         time.Duration(cfg.Watch.Namespace.JournalRetentionSeconds) * time.Second,
				BookmarkInterval: time.Duration(cfg.Watch.Namespace.BookmarkIntervalSeconds) * time.Second,
				Clock:            clock,
				Metrics:          metrics,
			}),
			leaseManager: watchjournal.NewLeaseManager(
				journal,
				uuid.NewString(),
				time.Duration(cfg.Watch.Namespace.LeaseTTLSeconds)*time.Second,
				time.Duration(cfg.Watch.Namespace.LeaseRenewIntervalSeconds)*time.Second,
				clock,
			),
			metrics: metrics,
			cfg:     cfg.Watch.Namespace,
			log:     log,
		}
		if runner, ok := rawStore.(namespaceCDCRunner); ok {
			namespaceWatch.runner = runner
		}
	}
	store := datastore.NewInstrumentedDatastore(rawStore, cfg.Datastore.Backend, log)
	ids := apiruntime.UUIDGenerator{}

	gitClient, err := gitclient.NewClientWithAddr(cfg.Git.Grpc.Uri, cfg.Auth.Grpc.HmacSecret)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("connect git-service: %w", err)
	}
	if err := ensureBootstrapResources(context.Background(), store, gitClient, clock, ids, log); err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("bootstrap resources: %w", err)
	}
	revocations, ok := rawStore.(staticusers.RevocationStore)
	if !ok {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("datastore does not implement shared session revocations")
	}
	registry, authReloader, providerShutdowns, err := buildProviderRegistry(cfg, store, log, revocations)
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("build auth provider registry: %w", err)
	}
	log.Info("auth providers ready",
		zap.Strings("authn_chain", cfg.Auth.AuthN.Chain),
		zap.String("authz_provider", cfg.Auth.AuthZ.Provider),
		zap.String("userdir_provider", cfg.Auth.UserDir.Provider),
	)

	// eventBus fans out CategoryTaxonomy admission events to GraphQL watch
	// subscriptions (spec 040). Shared between the gRPC admission path
	// (publisher) and the GraphQL resolvers (subscribers).
	eventBus := eventbus.New(eventBusCapacity)
	namespaceRepositoryFenceMode := resolver.NamespaceRepositoryFenceDisabled
	if cfg.NamespaceRepositoryFenceEnabled() {
		namespaceRepositoryFenceMode = resolver.NamespaceRepositoryFenceEnabled
	}

	gqlRouter, err := NewGraphQLHandler(GraphQLHandlerDeps{
		Store:                        store,
		GitWriter:                    gitClient,
		Logger:                       log,
		Registry:                     registry,
		Clock:                        clock,
		IDs:                          ids,
		EventBus:                     eventBus,
		NamespaceJournal:             namespaceJournal,
		NamespaceWatch:               cfg.Watch.Namespace,
		NamespaceMetrics:             namespaceWatchMetrics(namespaceWatch),
		NamespaceRepositoryFenceMode: namespaceRepositoryFenceMode,
		ServiceAccountAudience:       cfg.Auth.ServiceAccount.Audience,
		RateLimitPerSecond:           cfg.Api.RateLimitPerSecond,
		RateLimitBurst:               cfg.Api.RateLimitBurst,
	})
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, err
	}

	router := healthHandler(gqlRouter, store, log, clock, namespaceWatch)
	var graphQlHandler http.Handler = router

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.Port),
		Handler:      graphQlHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	gitHttpHandler := githttp.NewMuxWithStoreAndAuthz(githttp.SmartHttpDeps{
		GitClient: gitClient,
		Store:     store,
		Logger:    log,
		Ids:       ids,
		Registry:  registry,
	})
	gitHttpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Api.GitPort),
		Handler:      gitHttpHandler,
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	grpcListener, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Api.GrpcPort))
	if err != nil {
		_ = gitClient.Close()
		_ = store.Close()
		return nil, fmt.Errorf("listen on catalog gRPC port %d: %w", cfg.Api.GrpcPort, err)
	}
	grpcServer := grpc.NewServer()
	catalogServer, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store:     store,
		GitClient: gitClient,
		Logger:    log,
		Clock:     clock,
		EventBus:  eventBus,
	})
	if err != nil {
		_ = grpcListener.Close()
		_ = gitClient.Close()
		_ = store.Close()
		return nil, err
	}
	catalogv1.RegisterCatalogServiceServer(grpcServer, catalogServer)

	return &Server{
		cfg:              cfg,
		log:              log,
		store:            store,
		gitClient:        gitClient,
		httpServer:       httpServer,
		gitServer:        gitHttpServer,
		grpcServer:       grpcServer,
		grpcListener:     grpcListener,
		authReloader:     authReloader,
		providerShutdown: providerShutdowns,
		namespaceWatch:   namespaceWatch,
	}, nil
}

// GraphQLHandlerDeps are the dependencies for NewGraphQLHandler.
type GraphQLHandlerDeps struct {
	Store     datastore.Datastore
	GitWriter resolver.GitWriter
	Logger    *zap.Logger
	Registry  *auth.ProviderRegistry
	Clock     apiruntime.Clock
	IDs       apiruntime.IDGenerator
	// EventBus backs the watchCategories/watchResources subscription
	// resolvers (spec 040). Optional — nil disables watch subscriptions.
	EventBus                     *eventbus.Bus
	NamespaceJournal             datastore.NamespaceWatchJournal
	NamespaceWatch               config.NamespaceWatchConfig
	NamespaceMetrics             *watchjournal.Metrics
	NamespaceRepositoryFenceMode resolver.NamespaceRepositoryFenceMode
	// ServiceAccountAudience is the configured audience value that the server
	// issues tokens for. Must be provided to the resolver (spec 061).
	ServiceAccountAudience string
	// RateLimitPerSecond/RateLimitBurst configure the per-client-IP token
	// bucket guarding /graphql. A zero RateLimitPerSecond falls back to
	// defaultRateLimitPerSecond/defaultRateLimitBurst (the same defaults
	// config.Load applies), so callers that leave this zero-valued (e.g.
	// tests) keep working unchanged.
	RateLimitPerSecond float64
	RateLimitBurst     int
	// ConnectionRegistry tracks authenticated ServiceAccount WebSockets for
	// immediate local revocation. A fresh registry is created when omitted.
	ConnectionRegistry *wsregistry.Registry
}

// NewGraphQLHandler builds a GraphQL HTTP handler.
func NewGraphQLHandler(deps GraphQLHandlerDeps) (*gin.Engine, error) {
	if deps.Registry == nil || deps.Registry.AuthN() == nil || deps.Registry.AuthZ() == nil {
		return nil, fmt.Errorf("app: authn and authz provider registry is required")
	}
	connectionRegistry := deps.ConnectionRegistry
	if connectionRegistry == nil {
		connectionRegistry = wsregistry.New()
	}
	rootResolver, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:                        deps.Store,
		GitWriter:                    deps.GitWriter,
		Registry:                     deps.Registry,
		Logger:                       deps.Logger,
		Clock:                        deps.Clock,
		IDGenerator:                  deps.IDs,
		EventBus:                     deps.EventBus,
		NamespaceJournal:             deps.NamespaceJournal,
		NamespaceWatch:               deps.NamespaceWatch,
		NamespaceMetrics:             deps.NamespaceMetrics,
		NamespaceRepositoryFenceMode: deps.NamespaceRepositoryFenceMode,
		ServiceAccountAudience:       deps.ServiceAccountAudience,
		ConnectionRegistry:           connectionRegistry,
	})
	if err != nil {
		return nil, err
	}
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: rootResolver})
	gqlServer := gqlhandler.New(schema)

	gqlServer.AddTransport(transport.Websocket{
		KeepAlivePingInterval: 10 * time.Second,
		InitFunc:              webSocketInitFunc(deps.Registry, connectionRegistry, deps.Store),
		CloseFunc: func(ctx context.Context, _ int) {
			cancelWebSocketLifetime(ctx)
			connectionRegistry.Unregister(ctx)
		},
	})
	gqlServer.AddTransport(transport.Options{})
	gqlServer.AddTransport(transport.GET{})
	gqlServer.AddTransport(transport.POST{})
	gqlServer.AddTransport(transport.MultipartForm{})

	gqlServer.SetQueryCache(lru.New[*ast.QueryDocument](1000))

	gqlServer.Use(extension.Introspection{})
	gqlServer.Use(extension.AutomaticPersistedQuery{
		Cache: lru.New[string](100),
	})

	authenticateMiddleware := security.NewAuthenticate(deps.Registry, deps.Logger)
	authorizeMiddleware := security.NewAuthorizeWithStore(deps.Registry, deps.Store, deps.Logger)
	gqlServer.AroundOperations(authenticateMiddleware.GraphQLAuthenticator)
	gqlServer.AroundOperations(authorizeMiddleware.GraphQLAuthorizer)
	gqlServer.AroundFields(authorizeMiddleware.GraphQLFieldAuthorizer)

	gqlHandler := gin.HandlerFunc(func(c *gin.Context) {
		ctx := security.ContextWithRemoteAddr(c.Request.Context(), c.RemoteIP())
		c.Request = c.Request.WithContext(ctx)
		gqlServer.ServeHTTP(c.Writer, c.Request)
	})

	rateLimitPerSecond := deps.RateLimitPerSecond
	if rateLimitPerSecond <= 0 {
		rateLimitPerSecond = defaultRateLimitPerSecond
	}
	rateLimitBurst := deps.RateLimitBurst
	if rateLimitBurst <= 0 {
		rateLimitBurst = defaultRateLimitBurst
	}
	rateLimitMiddleware := security.NewRateLimit(rateLimitPerSecond, rateLimitBurst)
	requestIdMiddleware := middleware.NewRequestId(deps.IDs)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(security.CorsConfiguration())
	r.Use(rateLimitMiddleware.RateLimiter)
	r.GET("/playground", playgroundHandler)
	r.POST("/graphql", security.SecureHeaders, gqlHandler)
	// GET /graphql serves the graphql-transport-ws WebSocket upgrade for
	// subscriptions (spec 040) — transport.Websocket handles the upgrade
	// itself; a plain GET without the Upgrade header falls through to
	// gqlgen's own "must be POST" response.
	r.GET("/graphql", security.SecureHeaders, gqlHandler)
	return r, nil
}

type webSocketLifetimeCancelContextKey struct{}

func webSocketInitFunc(registry *auth.ProviderRegistry, connectionRegistry *wsregistry.Registry, store datastore.Datastore) transport.WebsocketInitFunc {
	return func(ctx context.Context, initPayload transport.InitPayload) (context.Context, *transport.InitPayload, error) {
		if registry == nil || registry.AuthN() == nil {
			return nil, nil, errors.New("unauthorized")
		}
		authorization := initPayload.Authorization()
		if authorization == "" {
			return nil, nil, errors.New("unauthorized")
		}
		headers := make(http.Header)
		headers.Set("Authorization", authorization)

		authCtx := ctx
		if bearer, ok := strings.CutPrefix(authorization, "Bearer "); ok {
			authCtx = auth.ContextWithRawToken(authCtx, bearer)
		}
		principal, decision, err := registry.AuthN().Authenticate(authCtx, auth.AuthRequest{
			Header:     headers,
			RemoteAddr: security.RemoteAddrFromContext(authCtx),
		})
		if err != nil || decision.Outcome != auth.OutcomeAllow || principal == nil || principal.AuthMethod == "none" {
			return nil, nil, errors.New("unauthorized")
		}
		if !principal.ExpiresAt.IsZero() && !principal.ExpiresAt.After(time.Now()) {
			return nil, nil, errors.New("unauthorized")
		}

		if principal.ServiceAccountUID != "" {
			var cancel context.CancelFunc
			if principal.ExpiresAt.IsZero() {
				authCtx, cancel = context.WithCancel(authCtx)
			} else {
				authCtx, cancel = context.WithDeadline(authCtx, principal.ExpiresAt)
			}
			authCtx = context.WithValue(authCtx, webSocketLifetimeCancelContextKey{}, cancel)
			authCtx = connectionRegistry.Register(authCtx, principal.ServiceAccountUID, cancel)
		}
		if principal.ServiceAccountUID != "" {
			if store == nil {
				cancelWebSocketLifetime(authCtx)
				connectionRegistry.Unregister(authCtx)
				return nil, nil, errors.New("unauthorized")
			}
			account, err := store.GetServiceAccountByUID(authCtx, principal.ServiceAccountUID)
			if err != nil || account == nil || account.Disabled || account.DeletionTimestamp != nil {
				cancelWebSocketLifetime(authCtx)
				connectionRegistry.Unregister(authCtx)
				return nil, nil, errors.New("unauthorized")
			}
		}
		return auth.ContextWithPrincipal(authCtx, principal), nil, nil
	}
}

func cancelWebSocketLifetime(ctx context.Context) {
	if cancel, ok := ctx.Value(webSocketLifetimeCancelContextKey{}).(context.CancelFunc); ok {
		cancel()
	}
}

func playgroundHandler(c *gin.Context) {
	h := playground.Handler("GraphQL Playground", "/graphql")
	h.ServeHTTP(c.Writer, c.Request)
}

func healthHandler(router *gin.Engine, store datastore.Datastore, log *zap.Logger, clock apiruntime.Clock, namespaceWatch *namespaceWatchRuntime) *gin.Engine {
	var namespaceWatchReady func(context.Context) error
	if namespaceWatch != nil {
		namespaceWatchReady = func(ctx context.Context) error {
			return namespaceWatchReadiness(ctx, namespaceWatch, clock.Now())
		}
	}
	healthHandler := health.NewHandler(health.HandlerDeps{
		Store:               store,
		Logger:              log,
		Version:             version,
		Clock:               clock,
		NamespaceWatchReady: namespaceWatchReady,
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
	maxLag := time.Duration(runtime.cfg.MaxMaterializerLagSeconds) * time.Second
	if bounds.ProgressAt.IsZero() || now.Sub(bounds.ProgressAt) > maxLag {
		return watchjournal.ErrMaterializerNotReady
	}
	return nil
}

// serviceAccountStore is the narrow datastore seam shared by the
// ServiceAccount AuthN providers. Assertion authentication additionally needs
// the durable replay operation while JWT authentication uses only the lookup.
type serviceAccountStore interface {
	GetServiceAccountBySubject(ctx context.Context, namespace, name string) (*datastore.ServiceAccount, error)
	TryConsumeServiceAccountAssertion(ctx context.Context, jtiDigest string, expiresAt time.Time) (bool, error)
}

// buildProviderRegistry constructs a ProviderRegistry from the application config.
// It reads authn chain, authz provider, and userdir provider from the resolved config.
// The second return value validates and atomically swaps the complete auth set on
// SIGHUP. The third return value lists resources that must be shut down with the server.
func buildProviderRegistry(cfg *config.Config, store serviceAccountStore, log *zap.Logger, revocations staticusers.RevocationStore) (*auth.ProviderRegistry, authReloader, []providerShutdowner, error) {
	registry, shutdowns, err := constructProviderRegistry(cfg, store, log, revocations)
	if err != nil {
		return nil, nil, nil, err
	}
	runtime := &providerRegistryRuntime{
		cfg: cfg, store: store, log: log, revocations: revocations, registry: registry, shutdowns: shutdowns,
	}
	return registry, runtime, []providerShutdowner{runtime}, nil
}

type providerRegistryRuntime struct {
	mu          sync.Mutex
	cfg         *config.Config
	store       serviceAccountStore
	log         *zap.Logger
	revocations staticusers.RevocationStore
	registry    *auth.ProviderRegistry
	shutdowns   []providerShutdowner
	closed      bool
}

func (r *providerRegistryRuntime) Reload() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("auth provider registry is closed")
	}
	newRegistry, newShutdowns, err := constructProviderRegistry(r.cfg, r.store, r.log, r.revocations)
	if err != nil {
		return err
	}
	oldShutdowns := r.shutdowns
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

func constructProviderRegistry(cfg *config.Config, store serviceAccountStore, log *zap.Logger, revocations staticusers.RevocationStore) (*auth.ProviderRegistry, []providerShutdowner, error) {
	// Build AuthN providers in chain order.
	chain := cfg.Auth.AuthN.Chain
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
			p, err := staticusers.NewWithRevocationStore(cfg.Auth, log, revocations)
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
			p, err := serviceaccountassertion.New(cfg.Auth.ServiceAccount, store, log)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init serviceaccount-assertion provider: %w", err)
			}
			authnProviders = append(authnProviders, p)
		case "serviceaccount-jwt":
			p, err := serviceaccountjwt.New(cfg.Auth.ServiceAccount, store, log)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init serviceaccount-jwt provider: %w", err)
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
	switch cfg.Auth.AuthZ.Provider {
	case "rbac-local":
		p, err := rbaclocal.New(cfg.Auth.RBAC, log)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init rbac-local authz provider: %w", err)
		}
		authzProvider = p
		rbacProvider = p
	case "allow-all", "":
		// Default to allow-all so existing deployments without explicit config are unaffected.
		authzProvider = allowall.New(log)
	default:
		cleanup()
		return nil, nil, fmt.Errorf("unknown authz provider %q", cfg.Auth.AuthZ.Provider)
	}
	// DecisionLogger is the required middleware that keeps every AuthZ decision
	// consistent with the pluggable auth architecture audit contract.
	authzProvider = auth.NewDecisionLogger(authzProvider, log)

	// Build UserDir provider.
	var userdirProvider auth.UserDirProvider
	switch cfg.Auth.UserDir.Provider {
	case "none", "":
		userdirProvider = userdirnone.New()
	case "static-users":
		if staticUsersProvider == nil {
			cleanup()
			return nil, nil, errors.New("auth.userdir.provider=static-users requires static-users in auth.authn.chain")
		}
		userdirProvider = staticUsersProvider
	default:
		cleanup()
		return nil, nil, fmt.Errorf("unknown userdir provider %q", cfg.Auth.UserDir.Provider)
	}

	if staticUsersProvider != nil && rbacProvider != nil && !rbacProvider.HasAnyRoleBindingFor(staticUsersProvider.Usernames()) {
		usernames := staticUsersProvider.Usernames()
		first := ""
		if len(usernames) > 0 {
			first = usernames[0]
		}
		cleanup()
		return nil, nil, fmt.Errorf("startup failed: static-users + rbac-local migration safety check\n\n  Problem: static-users is configured with %d user(s) (%s), but rbac-local's policy.yaml has no usable role_bindings entry for any of them. A usable binding's complete role set leaves at least one allowed action after explicit denies are applied\n\n  To fix, do ONE of the following:\n    1. Add a role_bindings entry in %s for at least one of the usernames above, e.g. role_bindings: %s: [admin]\n    2. If you don't want rbac-local enforcement yet, set GITSTORE_AUTH__AUTHZ__PROVIDER=allow-all instead\n\n  See specs/060-local-multiuser-authn/quickstart.md for a worked example", len(usernames), strings.Join(usernames, ", "), cfg.Auth.RBAC.PolicyFile, first)
	}
	return auth.NewProviderRegistry(auth.NewChainedAuthN(authnProviders...), authzProvider, userdirProvider), shutdowns, nil
}

// Start starts all servers in background goroutines.
func (s *Server) Start() {
	if s.namespaceWatch != nil && s.namespaceWatch.cfg.MaterializerEnabled {
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
					s.log.Error("auth provider reload failed; previous configuration remains active", zap.Error(err))
				} else {
					s.log.Info("auth providers reloaded atomically")
				}
			}
		}()
	}

	go func() {
		s.log.Info("GraphQL API server starting", zap.Int("port", s.cfg.Api.Port))
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Fatal("Server error", zap.Error(err))
		}
	}()

	go func() {
		s.log.Info("Git smart HTTP server starting", zap.Int("port", s.cfg.Api.GitPort))
		if err := s.gitServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("Git HTTP server error", zap.Error(err))
		}
	}()

	go func() {
		s.log.Info("CatalogService gRPC server starting", zap.Int("port", s.cfg.Api.GrpcPort))
		if err := s.grpcServer.Serve(s.grpcListener); err != nil {
			s.log.Error("CatalogService gRPC server error", zap.Error(err))
		}
	}()

	s.log.Info("Server ready",
		zap.String("graphql", fmt.Sprintf("http://localhost:%d/graphql", s.cfg.Api.Port)),
		zap.String("playground", fmt.Sprintf("http://localhost:%d/playground", s.cfg.Api.Port)),
		zap.String("health", fmt.Sprintf("http://localhost:%d/health", s.cfg.Api.Port)),
		zap.String("ready", fmt.Sprintf("http://localhost:%d/ready", s.cfg.Api.Port)),
		zap.String("git_http", fmt.Sprintf("http://localhost:%d", s.cfg.Api.GitPort)),
		zap.String("catalog_grpc", fmt.Sprintf("localhost:%d", s.cfg.Api.GrpcPort)),
	)
}

// Shutdown gracefully stops all network servers.
func (s *Server) Shutdown(ctx context.Context) {
	if err := s.httpServer.Shutdown(ctx); err != nil {
		s.log.Error("Server shutdown error", zap.Error(err))
	}
	if err := s.gitServer.Shutdown(ctx); err != nil {
		s.log.Error("Git HTTP server shutdown error", zap.Error(err))
	}
	s.grpcServer.GracefulStop()
}

// Close releases non-server resources.
func (s *Server) Close() {
	if s.namespaceWatch != nil && s.namespaceWatch.cancel != nil {
		s.namespaceWatch.cancel()
		if s.namespaceWatch.done != nil {
			timer := time.NewTimer(namespaceWatchStopTimeout)
			defer timer.Stop()
			select {
			case <-s.namespaceWatch.done:
			case <-timer.C:
				s.log.Warn("Namespace materializer shutdown timed out")
			}
		}
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
			r.log.Error("Namespace materializer lease acquisition failed", zap.Error(err))
		} else if acquired {
			err = r.runAsLeader(ctx, lease)
			if errors.Is(err, datastore.ErrNamespaceWatchDiscontinuity) {
				r.log.Error("Namespace materializer stopped after an ordering discontinuity; operator repair is required", zap.Error(err))
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

	errCh := make(chan error, 2)
	workers.Add(1)
	go func() {
		defer workers.Done()
		errCh <- r.leaseManager.Maintain(ctx, lease)
	}()
	if r.runner != nil {
		ready := make(chan struct{})
		workers.Add(1)
		go func() {
			defer workers.Done()
			errCh <- r.runner.RunNamespaceCDC(
				ctx, r.materializer, lease,
				time.Duration(r.cfg.CDCRetentionSeconds)*time.Second,
				time.Duration(r.cfg.CDCConfidenceWindowMillis)*time.Millisecond,
				func() { close(ready) },
			)
		}()
		select {
		case <-parent.Done():
			return parent.Err()
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				r.log.Warn("Namespace materializer failed before CDC readiness", zap.Error(err))
			}
			return err
		case <-ready:
		}
	}
	if _, err := r.materializer.AppendBookmark(ctx, lease); err != nil {
		r.log.Error("Namespace materializer initial bookmark failed", zap.Error(err))
		return err
	}
	bookmark := time.NewTicker(time.Duration(r.cfg.BookmarkIntervalSeconds) * time.Second)
	defer bookmark.Stop()
	for {
		select {
		case <-parent.Done():
			return parent.Err()
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				r.log.Warn("Namespace materializer leadership ended", zap.Error(err))
			}
			return err
		case <-bookmark.C:
			if _, err := r.materializer.AppendBookmark(ctx, lease); err != nil {
				r.log.Error("Namespace materializer bookmark failed", zap.Error(err))
				return err
			}
		}
	}
}
