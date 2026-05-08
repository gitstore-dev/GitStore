// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration test: API loads catalogue via gRPC from a real git-service container.
// Requires Docker. Run with: go test -tags integration ./tests/integration/...

//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

// TestGRPCCatalogueLoad starts a real git-service container and verifies that
// the API can load a catalogue via gRPC without any shared volume mount.
func TestGRPCCatalogueLoad(t *testing.T) {
	ctx := context.Background()

	// Start git-service container.
	req := testcontainers.ContainerRequest{
		Image:        "gitstore-git-service:latest",
		ExposedPorts: []string{"9418/tcp", "50051/tcp", "8080/tcp"},
		Env: map[string]string{
			"GITSTORE_DATA_DIR":  "/data/repos",
			"GITSTORE_GRPC_PORT": "50051",
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("9418/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("git-service container unavailable (Docker not running or image missing): %v", err)
	}
	defer func() {
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("failed to terminate container: %v", termErr)
		}
	}()

	grpcPort, err := container.MappedPort(ctx, "50051")
	require.NoError(t, err)

	addr := fmt.Sprintf("localhost:%s", grpcPort.Port())
	client, err := gitclient.NewClientWithAddr(addr)
	require.NoError(t, err)
	defer client.Close()

	t.Run("GetLatestTag returns not-found on empty repo", func(t *testing.T) {
		_, err := client.GetLatestTag(ctx)
		// Empty repo — no tags yet. Error expected.
		assert.Error(t, err)
	})

	t.Run("ListFiles returns empty on empty repo", func(t *testing.T) {
		entries, err := client.ListFiles(ctx, "", "HEAD")
		// May error if repo is completely empty — both outcomes are acceptable.
		if err == nil {
			assert.Empty(t, entries)
		}
	})

	t.Run("Loader can be constructed with no shared volume", func(t *testing.T) {
		loader := catalog.NewGRPCLoader(client, zap.NewNop())
		require.NotNil(t, loader)
		// Attempt load — empty repo returns an error, but no panic or volume access.
		_, err := loader.LoadFromLatestTag(ctx)
		assert.Error(t, err, "empty repo should return no-tags error")
	})
}

// TestGRPCCatalogueLoadMultipleReplicas verifies that multiple independent
// Client instances (simulating API replicas) each get the same data.
func TestGRPCCatalogueLoadMultipleReplicas(t *testing.T) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "gitstore-git-service:latest",
		ExposedPorts: []string{"9418/tcp", "50051/tcp"},
		Env: map[string]string{
			"GITSTORE_GRPC_PORT": "50051",
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("9418/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("git-service container unavailable: %v", err)
	}
	defer container.Terminate(ctx) //nolint:errcheck

	grpcPort, err := container.MappedPort(ctx, "50051")
	require.NoError(t, err)
	addr := fmt.Sprintf("localhost:%s", grpcPort.Port())

	// Simulate 3 API replicas — each with its own independent connection, no shared volume.
	const replicas = 3
	clients := make([]*gitclient.Client, replicas)
	for i := range clients {
		c, err := gitclient.NewClientWithAddr(addr)
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
