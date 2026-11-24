// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func roleTestConfig(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("GITSTORE_API__AUTH__AUTHN__CHAIN", "anonymous")
	t.Setenv("GITSTORE_API__AUTH__AUTHZ__PROVIDER", "allow-all")
	t.Setenv("GITSTORE_API__AUTH__USERDIR__PROVIDER", "none")
	t.Setenv("GITSTORE_GRPC_AUTH__HMAC_SECRET", "test-runtime-role-signing-secret")
	t.Setenv("GITSTORE_API__GIT_SERVICE__URI", "passthrough:///127.0.0.1:1")
	t.Setenv("GITSTORE_API__TRANSLATION__ENABLED", "false")
	t.Setenv("GITSTORE_API__TRANSLATION__DESCRIPTOR_DIGEST", "")
	cfg, err := config.Load()
	require.NoError(t, err)
	ports := []*int{&cfg.Api.AdminPort, &cfg.Api.StorefrontPort, &cfg.Api.GitPort, &cfg.Api.GrpcPort}
	var reservations []net.Listener
	for _, port := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		reservations = append(reservations, listener)
		*port = listener.Addr().(*net.TCPAddr).Port
	}
	for _, listener := range reservations {
		require.NoError(t, listener.Close())
	}
	return cfg
}

func TestTranslationRolloutRejectsUnsupportedConfigurationBeforeRuntime(t *testing.T) {
	for _, role := range []string{config.RoleCatalog, config.RoleAdmin, config.RoleSmartHTTP, config.RoleStorefront} {
		for _, invalid := range []struct {
			name, digest, message string
			enabled               bool
		}{
			{name: "enabled-unpinned", enabled: true, message: "descriptor"},
			{name: "enabled-mismatched", enabled: true, digest: "unsupported-descriptor", message: "capability mismatch"},
			{name: "mismatched", digest: "unsupported-descriptor", message: "capability mismatch"},
		} {
			t.Run(role+"/"+invalid.name, func(t *testing.T) {
				cfg := roleTestConfig(t)
				cfg.Api.Roles = []string{role}
				cfg.Api.Translation.Enabled = invalid.enabled
				cfg.Api.Translation.DescriptorDigest = invalid.digest
				cfg.Api.Datastore.Backend = "must-not-construct"
				server, err := NewServer(cfg, zap.NewNop())
				if server != nil {
					t.Cleanup(server.Close)
				}
				require.ErrorContains(t, err, invalid.message, "rollout validation must precede datastore/provider/listener effects")
				require.Nil(t, server)
			})
		}
	}
}

func TestTranslationPinnedCapabilityIsCompatibleWithEveryRole(t *testing.T) {
	for _, role := range []string{config.RoleCatalog, config.RoleAdmin, config.RoleSmartHTTP, config.RoleStorefront} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/enabled=%t", role, enabled), func(t *testing.T) {
				cfg := roleTestConfig(t)
				cfg.Api.Roles = []string{role}
				cfg.Api.Translation.Enabled = enabled
				cfg.Api.Translation.DescriptorDigest = translation.CapabilityDigest()
				server, err := NewServer(cfg, zap.NewNop())
				require.NoError(t, err)
				server.Close()
			})
		}
	}
}

func TestEveryRuntimeRoleCombinationStartsDuringGitOutage(t *testing.T) {
	all := []string{config.RoleSmartHTTP, config.RoleCatalog, config.RoleAdmin, config.RoleStorefront}
	for mask := 1; mask < 16; mask++ {
		for _, layout := range []string{"dedicated", "shared"} {
			t.Run(fmt.Sprintf("%d/%s", mask, layout), func(t *testing.T) {
				cfg := roleTestConfig(t)
				cfg.Api.Roles = []string{}
				for i, role := range all {
					if mask&(1<<i) != 0 {
						cfg.Api.Roles = append(cfg.Api.Roles, role)
					}
				}
				cfg.Api.GraphQLLayout = layout
				started := time.Now()
				server, err := NewServer(cfg, zap.NewNop())
				require.NoError(t, err)
				defer server.Close()
				require.Less(t, time.Since(started), 2*time.Second, "construction must never wait for Git")
				require.Equal(t, cfg.Api.HasRole(config.RoleCatalog), server.grpcListener != nil)
				if len(cfg.Api.Roles) == 1 && cfg.Api.HasRole(config.RoleStorefront) {
					require.Nil(t, server.store)
					require.Nil(t, server.gitClient)
					require.Nil(t, server.namespaceWatch)
					require.Nil(t, server.authReloader)
				}
				if !cfg.Api.HasRole(config.RoleCatalog) && !cfg.Api.HasRole(config.RoleAdmin) {
					require.Nil(t, server.bootstrap)
					require.Nil(t, server.namespaceWatch)
				}
				expected := 0
				for _, role := range []string{config.RoleAdmin, config.RoleStorefront, config.RoleSmartHTTP} {
					if cfg.Api.HasRole(role) {
						expected++
					}
				}
				if layout == "shared" && cfg.Api.HasRole(config.RoleAdmin) && cfg.Api.HasRole(config.RoleStorefront) {
					expected--
				}
				require.Len(t, server.httpServers, expected)
				server.Start()
				client := &http.Client{Timeout: 2 * time.Second}
				for _, endpoint := range server.httpServers {
					port := endpoint.listener.Addr().(*net.TCPAddr).Port
					base := "http://127.0.0.1:" + strconv.Itoa(port)
					for path, want := range map[string]int{"/health": 200, "/ready": 503, "/ready/missing": 404} {
						response, err := client.Get(base + path)
						require.NoError(t, err)
						_ = response.Body.Close()
						require.Equal(t, want, response.StatusCode, strings.Join(cfg.Api.Roles, ",")+" "+path)
					}
					if cfg.Api.HasRole(config.RoleStorefront) && (endpoint.name == "storefront" || layout == "shared" && endpoint.name == "admin") {
						response, err := client.Post(base+"/storefront/graphql", "application/json", strings.NewReader(`{"query":"{products{edges{node{id}}}}"}`))
						require.NoError(t, err)
						_ = response.Body.Close()
						require.Equal(t, 503, response.StatusCode, "publication prerequisites remain fail closed")
					}
				}
				if server.grpcListener != nil {
					connection, err := grpc.NewClient("passthrough:///127.0.0.1:"+strconv.Itoa(cfg.Api.GrpcPort), grpc.WithTransportCredentials(insecure.NewCredentials()))
					require.NoError(t, err)
					defer connection.Close()
					client := healthpb.NewHealthClient(connection)
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					live, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: "liveness"})
					require.NoError(t, err)
					require.Equal(t, healthpb.HealthCheckResponse_SERVING, live.Status)
					ready, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: config.RoleCatalog})
					require.NoError(t, err)
					require.Equal(t, healthpb.HealthCheckResponse_NOT_SERVING, ready.Status)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				server.Shutdown(ctx)
			})
		}
	}
}

func TestBootstrapRetriesOnlyOneBoundedAttemptAndCancels(t *testing.T) {
	var attempts, active, peak atomic.Int32
	runtime := newBootstrapRuntime(func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			return errors.New("missing deadline")
		}
		n := active.Add(1)
		peak.Store(max(peak.Load(), n))
		defer active.Add(-1)
		if attempts.Add(1) < 2 {
			return errors.New("transient outage")
		}
		return nil
	}, zap.NewNop())
	require.Error(t, runtime.ready(t.Context()))
	runtime.start()
	runtime.start()
	require.Eventually(t, func() bool { return runtime.ready(t.Context()) == nil }, 3*time.Second, 10*time.Millisecond)
	runtime.close()
	require.EqualValues(t, 2, attempts.Load())
	require.EqualValues(t, 1, peak.Load())
	blocked := newBootstrapRuntime(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }, zap.NewNop())
	blocked.start()
	start := time.Now()
	blocked.close()
	require.Less(t, time.Since(start), time.Second)
}

func TestRuntimeBudgetsDoNotStarveCatalogCallbacksOrHealth(t *testing.T) {
	budget := config.RoleBudget{InFlight: 1, Deadline: time.Second}
	catalog := boundedCatalog(budget)
	request := &catalogv1.ValidateResourcesRequest{Trees: []*catalogv1.ResourceValidationTree{{
		ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "translations/product-de.md", Content: []byte("---\nkind: Translation\n---\nExact authored body\r\n")}},
	}}}
	info := &grpc.UnaryServerInfo{FullMethod: catalogv1.CatalogService_ValidateResources_FullMethodName}
	smart := boundedHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The originating operation owns its Smart HTTP slot while catalog
		// executes a callback that must retain its independent capacity.
		_, err := catalog(r.Context(), request, info, func(ctx context.Context, got any) (any, error) {
			require.Same(t, request, got)
			response, err := catalog(ctx, request, info, func(context.Context, any) (any, error) {
				t.Error("overloaded callback must not execute")
				return nil, nil
			})
			require.Equal(t, codes.ResourceExhausted, status.Code(err))
			require.Nil(t, response, "overload must not publish a partial validation result")
			called := false
			_, err = catalog(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}, func(context.Context, any) (any, error) {
				called = true
				return nil, nil
			})
			require.NoError(t, err)
			require.True(t, called)
			return nil, nil
		})
		require.NoError(t, err)
		w.WriteHeader(204)
	}), budget)
	recorder := httptest.NewRecorder()
	smart.ServeHTTP(recorder, httptest.NewRequest("POST", "/repo/git-receive-pack", nil))
	require.Equal(t, 204, recorder.Code)
	_, err := catalog(t.Context(), request, info, func(_ context.Context, got any) (any, error) {
		require.Same(t, request, got)
		return nil, nil
	})
	require.NoError(t, err, "completion must release the shared Catalog slot")
	timeout := boundedCatalog(config.RoleBudget{InFlight: 1, Deadline: 10 * time.Millisecond})
	start := time.Now()
	response, err := timeout(t.Context(), request, info, func(ctx context.Context, got any) (any, error) {
		require.Same(t, request, got)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, response)
	require.Less(t, time.Since(start), time.Second)
}

func TestRuntimeReadinessRoutesPreserveReadyNamespace(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("bootstrap-pending=%t", pending), func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Repository-Path", r.URL.Path)
				w.WriteHeader(http.StatusNoContent)
			})
			readiness := &roleReadiness{cfg: config.ApiConfig{Roles: []string{config.RoleStorefront}}}
			handler := bootstrapServingHandler(runtimeHTTPHandler(next, readiness), func(context.Context) error {
				if pending {
					return errRoleUnavailable
				}
				return nil
			})
			for _, request := range []struct{ method, path string }{
				{http.MethodGet, "/ready/myrepo/info/refs?service=git-upload-pack"},
				{http.MethodPost, "/ready/myrepo/git-upload-pack"},
				{http.MethodPost, "/ready/myrepo/git-receive-pack"},
			} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(request.method, request.path, nil))
				if pending {
					require.Equal(t, http.StatusServiceUnavailable, response.Code, request.path)
					require.Empty(t, response.Header().Get("X-Repository-Path"))
				} else {
					require.Equal(t, http.StatusNoContent, response.Code, request.path)
					require.Equal(t, strings.Split(request.path, "?")[0], response.Header().Get("X-Repository-Path"))
				}
			}
			for path, want := range map[string]int{"/health": 200, "/ready": 503, "/ready/missing": 404} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				require.Equal(t, want, response.Code, path)
			}
		})
	}
}

func TestSharedGraphQLKeepsDisjointPathsAndLocalLiveness(t *testing.T) {
	cfg := roleTestConfig(t)
	cfg.Api.Roles = []string{config.RoleAdmin, config.RoleStorefront}
	cfg.Api.GraphQLLayout = "shared"
	cfg.Api.StorefrontPort = cfg.Api.AdminPort
	server, err := NewServer(cfg, zap.NewNop())
	require.NoError(t, err)
	defer server.Close()
	require.Len(t, server.httpServers, 1)
	handler := server.httpServers[0].server.Handler
	for path, want := range map[string]int{"/graphql": 503, "/storefront/graphql": 503, "/ready/storefront": 503, "/health": 200} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		require.Equal(t, want, response.Code, path)
	}
	server.bootstrap.complete.Store(true)
	for path, want := range map[string]int{"/graphql": 404, "/storefront/graphql": 503, "/admin/playground": 200} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		require.Equal(t, want, response.Code, path)
	}
}

func TestSmartHTTPDynamicAuthorizationDoesNotOwnBootstrap(t *testing.T) {
	cfg := roleTestConfig(t)
	cfg.Api.Roles = []string{config.RoleSmartHTTP}
	cfg.Api.Auth.AuthZ.Provider = "opa"
	server, err := newServer(cfg, zap.NewNop(), true)
	require.NoError(t, err)
	defer server.Close()
	require.Nil(t, server.bootstrap)
	require.Nil(t, server.namespaceWatch)
	require.Nil(t, server.connections)
	server.Start()
	require.Never(t, func() bool {
		namespaces, err := server.store.ListNamespaces(t.Context(), datastore.PageParams{})
		require.NoError(t, err)
		return len(namespaces.Items) != 0
	}, 150*time.Millisecond, 10*time.Millisecond, "authorization observers must not provision repositories or namespaces")
}

type unavailablePeerHealth struct {
	healthpb.UnimplementedHealthServer
	calls atomic.Int32
}

func (h *unavailablePeerHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	h.calls.Add(1)
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_NOT_SERVING}, nil
}

func TestRoleReadinessIsolatesJournalFailureAndRecoversGitTransport(t *testing.T) {
	address := "127.0.0.1:0"
	peerHealth := &unavailablePeerHealth{}
	startPeer := func() *grpc.Server {
		listener, err := net.Listen("tcp", address)
		require.NoError(t, err)
		address = listener.Addr().String()
		peer := grpc.NewServer()
		healthpb.RegisterHealthServer(peer, peerHealth)
		go func() { _ = peer.Serve(listener) }()
		t.Cleanup(peer.Stop)
		return peer
	}
	peer := startPeer()
	cfg := roleTestConfig(t)
	cfg.Api.GitService.Uri = "passthrough:///" + address
	server, err := NewServer(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(server.Close)
	server.bootstrap.complete.Store(true)
	var journalReady atomic.Bool
	journalReady.Store(true)
	server.roleReady.journal = func(context.Context) error {
		if !journalReady.Load() {
			return errRoleUnavailable
		}
		return nil
	}
	code := func(path string) int {
		response := httptest.NewRecorder()
		server.httpServers[0].server.Handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		return response.Code
	}
	require.Eventually(t, func() bool {
		return code("/ready") == http.StatusOK
	}, 3*time.Second, 10*time.Millisecond)
	journalReady.Store(false)
	require.Equal(t, http.StatusOK, code("/ready/smart-http"))
	require.Equal(t, http.StatusServiceUnavailable, code("/ready/catalog"))
	require.Equal(t, http.StatusServiceUnavailable, code("/ready/admin"))
	require.Equal(t, http.StatusServiceUnavailable, code("/ready"))
	require.Equal(t, http.StatusOK, code("/health"))
	journalReady.Store(true)
	require.Equal(t, http.StatusOK, code("/ready"))
	peer.Stop()
	require.Eventually(t, func() bool {
		return code("/ready") == http.StatusServiceUnavailable
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, http.StatusOK, code("/health"))
	startPeer()
	require.Eventually(t, func() bool {
		return code("/ready") == http.StatusOK
	}, 3*time.Second, 10*time.Millisecond)
	require.Zero(t, peerHealth.calls.Load(), "peer dependency health must never participate in callback-cycle readiness")
}
