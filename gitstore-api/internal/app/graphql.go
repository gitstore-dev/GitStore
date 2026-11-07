// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/99designs/gqlgen/graphql"

	gqlhandler "github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/resolver"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/admissionreport"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/storefront"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gorilla/websocket"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// GraphQLHandlerDeps are the dependencies for NewGraphQLHandler.
type GraphQLHandlerDeps struct {
	RegisterCleanup           func(func())
	Endpoint                  config.EndpointConfig
	TrustedProxies            []string
	Store                     datastore.Datastore
	GitWriter                 resolver.GitWriter
	Logger                    *zap.Logger
	Registry                  *auth.ProviderRegistry
	Clock                     apiruntime.Clock
	IDs                       apiruntime.IDGenerator
	CommittedManifestAdmitter admission.CommittedManifestAdmitter
	ResourceJournal           datastore.ResourceWatchJournal
	NamespaceWatch            config.WatchJournalConfig
	NamespaceMetrics          *watchjournal.Metrics
	// PushLimits is the shared static platform push-size ceiling; see
	// config.PushLimitsConfig.
	PushLimits config.PushLimitsConfig
	// ServiceAccountAudience is the configured audience value that the server
	// issues tokens for. Must be provided to the resolver (spec 061).
	ServiceAccountAudience string
	// RateLimitPerSecond/RateLimitBurst configure the per-client-IP token
	// bucket guarding /admin/graphql. A zero RateLimitPerSecond falls back to
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
	if deps.Endpoint.Limits.BodyBytes == 0 {
		policy := deps.Endpoint.CORS
		deps.Endpoint = config.DefaultEndpoint("admin")
		if policy.AllowedOrigins != nil {
			deps.Endpoint.CORS = policy
		}
	}
	if err := deps.Endpoint.Validate(); err != nil {
		return nil, err
	}
	if deps.Registry == nil || deps.Registry.AuthN() == nil || deps.Registry.AuthZ() == nil {
		return nil, fmt.Errorf("app: authn and authz provider registry is required")
	}
	connectionRegistry := deps.ConnectionRegistry
	if connectionRegistry == nil {
		connectionRegistry = wsregistry.New()
	}
	rootResolver, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:                     deps.Store,
		GitWriter:                 deps.GitWriter,
		Registry:                  deps.Registry,
		Logger:                    deps.Logger,
		Clock:                     deps.Clock,
		IDGenerator:               deps.IDs,
		CommittedManifestAdmitter: deps.CommittedManifestAdmitter,
		ResourceJournal:           deps.ResourceJournal,
		NamespaceWatch:            deps.NamespaceWatch,
		NamespaceMetrics:          deps.NamespaceMetrics,
		PushLimits:                deps.PushLimits,
		ServiceAccountAudience:    deps.ServiceAccountAudience,
		ConnectionRegistry:        connectionRegistry,
	})
	if err != nil {
		return nil, err
	}
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: rootResolver})
	limits := deps.Endpoint.Limits
	gqlServer := gqlhandler.New(security.LimitSchema(schema, limits))
	gqlServer.SetParserTokenLimit(limits.ParserTokens)
	gqlServer.SetValidationRulesFn(security.BoundedValidationRules(limits.ValidationErrors))

	gqlServer.AddTransport(transport.Websocket{
		InitTimeout: deps.Endpoint.Limits.Deadline,
		Upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			return deps.Endpoint.CORS.Allows(r.Header.Get("Origin"), scheme+"://"+r.Host)
		}},
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
	gqlServer.Use(admissionreport.Extension{})
	gqlServer.Use(extension.AutomaticPersistedQuery{
		Cache: lru.New[string](100),
	})
	gqlServer.Use(security.OperationBounds{Limits: limits})
	gqlServer.Use(extension.FixedComplexityLimit(limits.Complexity, security.ComplexityOptions(schema.Schema())...))

	authenticateMiddleware := security.NewAuthenticate(deps.Registry, deps.Logger)
	authorizeMiddleware := security.NewAuthorizeWithStore(deps.Registry, deps.Store, deps.Logger)
	gqlServer.AroundOperations(authenticateMiddleware.GraphQLAuthenticator)
	control := newOperationAdmission(deps.Endpoint)
	gqlServer.AroundOperations(control.middleware)
	gqlServer.AroundOperations(func(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
		principal := auth.PrincipalFromContext(ctx)
		if principal == nil || principal.AuthMethod == "none" {
			graphql.GetOperationContext(ctx).DisableIntrospection = true
		}
		return next(ctx)
	})
	gqlServer.AroundOperations(authorizeMiddleware.GraphQLAuthorizer)
	gqlServer.AroundFields(authorizeMiddleware.GraphQLFieldAuthorizer)
	gqlServer.AroundFields(func(ctx context.Context, next graphql.Resolver) (any, error) {
		field := graphql.GetFieldContext(ctx)
		principal := auth.PrincipalFromContext(ctx)
		if field != nil && field.Object == "Query" && (field.Field.Name == "__schema" || field.Field.Name == "__type") && (principal == nil || principal.AuthMethod == "none") {
			return nil, &gqlerror.Error{Message: "authentication is required for introspection", Extensions: map[string]any{"code": "UNAUTHENTICATED"}}
		}
		return next(ctx)
	})
	gqlServer.AroundResponses(authorizeMiddleware.GraphQLResponseAuthorizer)

	operationHandler := observability.HTTPHandler(gqlServer, deps.Logger)
	gqlHandler := gin.HandlerFunc(func(c *gin.Context) {
		if c.Request.Method == http.MethodGet && !websocket.IsWebSocketUpgrade(c.Request) {
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
		if !authenticateMiddleware.AuthenticateWebSocketUpgrade(c) {
			return
		}
		ctx := security.ContextWithRemoteAddr(c.Request.Context(), c.RemoteIP())
		c.Request = c.Request.WithContext(ctx)
		operationHandler.ServeHTTP(c.Writer, c.Request)
	})

	rateLimitPerSecond := deps.RateLimitPerSecond
	if rateLimitPerSecond <= 0 {
		rateLimitPerSecond = defaultRateLimitPerSecond
	}
	rateLimitBurst := deps.RateLimitBurst
	if rateLimitBurst <= 0 {
		rateLimitBurst = defaultRateLimitBurst
	}
	rateLimitMiddleware := security.NewBoundedRateLimit(rateLimitPerSecond, rateLimitBurst, limits.LimiterKeys, limits.IdleTTL)
	if deps.RegisterCleanup != nil {
		deps.RegisterCleanup(rateLimitMiddleware.Close)
		deps.RegisterCleanup(control.budget.Close)
		stop, err := observeEndpointResources("admin", limits, rateLimitMiddleware, control.budget, connectionRegistry)
		if err != nil {
			return nil, err
		}
		deps.RegisterCleanup(stop)
	}
	admission := newEndpointAdmission("admin", limits)
	requestIdMiddleware := middleware.NewRequestId(deps.IDs)

	r := gin.New()
	if err := r.SetTrustedProxies(deps.TrustedProxies); err != nil {
		return nil, err
	}
	r.Use(gin.Recovery())
	r.Use(requestIdMiddleware.RequestIdInserter)
	r.Use(admission.observeIngress())
	r.Use(security.EndpointCORS(deps.Endpoint.CORS, "/admin/graphql"))
	r.Use(rateLimitMiddleware.RateLimiter)
	r.GET("/admin/playground", playgroundHandler)
	r.POST("/admin/graphql", security.SecureHeaders, admission.middleware(), gqlHandler)
	// GET /admin/graphql serves the graphql-transport-ws WebSocket upgrade for
	// subscriptions (spec 040) — transport.Websocket handles the upgrade
	// itself; a plain GET without the Upgrade header falls through to
	// gqlgen's own "must be POST" response.
	r.GET("/admin/graphql", security.SecureHeaders, admission.middleware(), gqlHandler)
	r.OPTIONS("/admin/graphql", func(c *gin.Context) { c.Status(http.StatusNoContent) })
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

		{
			var cancel context.CancelFunc
			if principal.ExpiresAt.IsZero() {
				authCtx, cancel = context.WithCancel(authCtx)
			} else {
				authCtx, cancel = context.WithDeadline(authCtx, principal.ExpiresAt)
			}
			authCtx = context.WithValue(authCtx, webSocketLifetimeCancelContextKey{}, cancel)
			key := principal.ServiceAccountUID
			if key == "" {
				key = "principal:" + principalKey(principal)
			}
			authCtx = connectionRegistry.Register(authCtx, key, cancel)
			if authCtx.Err() != nil {
				return nil, nil, errors.New("unauthorized")
			}
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

func playgroundHandler(c *gin.Context) { endpointPlayground("admin").ServeHTTP(c.Writer, c.Request) }

// NewStorefrontHandler mounts only the fixed public endpoint. It never constructs
// a management resolver or accepts datastore/Git dependencies.
func NewStorefrontHandler(options config.EndpointConfig, limits config.RateLimitConfig, trusted []string, cleanup ...func(func())) (*gin.Engine, error) {
	if options.Limits.BodyBytes == 0 {
		options = config.DefaultEndpoint("storefront")
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	r := gin.New()
	if err := r.SetTrustedProxies(trusted); err != nil {
		return nil, err
	}
	r.Use(gin.Recovery())
	admission := newEndpointAdmission("storefront", options.Limits)
	r.Use(admission.observeIngress())
	r.Use(security.EndpointCORS(options.CORS, "/storefront/graphql"))
	if limits.PerSecond <= 0 {
		limits.PerSecond = 200
		limits.Burst = 400
	}
	limiter := security.NewBoundedRateLimit(limits.PerSecond, limits.Burst, options.Limits.LimiterKeys, options.Limits.IdleTTL)
	for _, register := range cleanup {
		register(limiter.Close)
		stop, err := observeEndpointResources("storefront", options.Limits, limiter, nil, nil)
		if err != nil {
			return nil, err
		}
		register(stop)
	}
	r.Use(limiter.RateLimiter)
	unavailable := gin.WrapH(storefront.NewHandler())
	r.POST("/storefront/graphql", admission.middleware(), unavailable)
	r.GET("/storefront/graphql", admission.middleware(), unavailable)
	r.OPTIONS("/storefront/graphql", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/storefront/playground", gin.WrapH(endpointPlayground("storefront")))
	return r, nil
}
