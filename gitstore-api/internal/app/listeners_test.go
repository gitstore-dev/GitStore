// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestOperationalRoutesStayOnAdminWhileStorefrontIsUnavailable(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	healthHandler(env.server.Config.Handler.(*gin.Engine), env.store, zap.NewNop(), apiruntime.SystemClock{}, nil, "route-fixture")
	public, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	s := httptest.NewServer(public)
	defer s.Close()
	for _, path := range []string{"/health", "/ready", "/metrics"} {
		response, err := env.server.Client().Get(env.server.URL + path)
		require.NoError(t, err)
		require.Equal(t, 200, response.StatusCode)
		_ = response.Body.Close()
		response, err = s.Client().Get(s.URL + path)
		require.NoError(t, err)
		require.Equal(t, 404, response.StatusCode)
		_ = response.Body.Close()
	}
	response, err := s.Client().Post(s.URL+"/storefront/graphql", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, 503, response.StatusCode)
	_ = response.Body.Close()
}

type blockingHealthRPC struct {
	healthpb.UnimplementedHealthServer
	started chan struct{}
}

func (s *blockingHealthRPC) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestShutdownForceStopsCatalogRPCAtCallerDeadline(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	rpc := grpc.NewServer()
	handler := &blockingHealthRPC{started: make(chan struct{})}
	healthpb.RegisterHealthServer(rpc, handler)
	go rpc.Serve(listener)
	defer rpc.Stop()
	defer listener.Close()
	client, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		_, err := healthpb.NewHealthClient(client).Check(t.Context(), &healthpb.HealthCheckRequest{})
		done <- err
	}()
	select {
	case <-handler.started:
	case <-time.After(2 * time.Second):
		t.Fatal("catalog RPC did not start")
	}
	s := &Server{grpcServer: rpc, grpcListener: listener, log: zap.NewNop()}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	s.Shutdown(ctx)
	require.Less(t, time.Since(start), time.Second)
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("catalog RPC leaked after force-stop")
	}
}

func TestAdminUsesFixedPrefixedPath(t *testing.T) {
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	for path, want := range map[string]int{"/admin/playground": http.StatusOK, "/playground": http.StatusNotFound, "/graphql": http.StatusNotFound, "/storefront/graphql": http.StatusNotFound} {
		response, err := env.server.Client().Get(env.server.URL + path)
		require.NoError(t, err)
		_ = response.Body.Close()
		require.Equal(t, want, response.StatusCode, path)
	}
	for _, path := range []string{"/graphql", "/storefront/graphql", "/admin/graphql/unknown"} {
		r, err := http.NewRequest(http.MethodOptions, env.server.URL+path, nil)
		require.NoError(t, err)
		r.Header.Set("Origin", "https://untrusted.example")
		r.Header.Set("Access-Control-Request-Method", "POST")
		response, err := env.server.Client().Do(r)
		require.NoError(t, err)
		require.Equal(t, 404, response.StatusCode, path)
		_ = response.Body.Close()
	}
}

func TestBindHTTPServersRollsBackEarlierListeners(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := reservation.Addr().String()
	require.NoError(t, reservation.Close())
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	_, err = bindHTTPServers([]httpEndpoint{
		{name: "admin", server: &http.Server{Addr: address}},
		{name: "storefront", server: &http.Server{Addr: occupied.Addr().String()}},
	})
	require.Error(t, err)
	probe, err := net.Listen("tcp", address)
	require.NoError(t, err, "failed construction leaked the Admin listener")
	_ = probe.Close()
}
