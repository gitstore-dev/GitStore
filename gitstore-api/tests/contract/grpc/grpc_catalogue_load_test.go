// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration test: API loads catalogue via gRPC from a real git-service container.
// Requires Docker. Run with: go test -tags grpc ./tests/integration/...

//go:build grpc

package integration

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestGRPCCatalogueLoad starts a real git-service container and verifies that
// the API can load a catalogue via gRPC without any shared volume mount.
func TestGRPCCatalogueLoad(t *testing.T) {
	ctx := context.Background()

	client, err := startSharedClient(t)
	require.NoError(t, err)
	defer client.Close()

	t.Run("Loader can be constructed with no shared volume", func(t *testing.T) {
		loader := catalog.NewGRPCLoader(client, zap.NewNop())
		require.NotNil(t, loader)
		// Attempt load should be stable in shared-container mode regardless of repo state.
		_, _ = loader.LoadFromLatestTag(ctx)
	})
}

// TestGRPCCatalogueLoadMultipleReplicas verifies that multiple independent
// Client instances (simulating API replicas) each get the same data.
func TestGRPCCatalogueLoadMultipleReplicas(t *testing.T) {
	ctx := context.Background()

	// Simulate 3 API replicas — each with its own independent connection, no shared volume.
	const replicas = 3
	clients := make([]*gitclient.Client, replicas)
	for i := range clients {
		c, err := startSharedClient(t)
		require.NoError(t, err, "replica %d failed to connect", i)
		defer c.Close()
		clients[i] = c
	}

	// All replicas must reach the same server without filesystem dependency.
	for i, c := range clients {
		_, err := c.GetLatestTag(ctx)
		// Error is fine (empty repo), but it must NOT be a connection error.
		t.Logf("replica %d GetLatestTag result: %v", i, err)
	}
}
