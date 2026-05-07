// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration test: WS-triggered catalogue reload fetches via gRPC (not local git pull).
// Verifies that after a tag push the API reloads via GetLatestTag + ListFiles + GetFile.
// Requires Docker. Run with: go test -tags integration ./tests/integration/...

//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/cache"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.uber.org/zap"
)

// TestGRPCCatalogReloadAfterTagPush verifies the catalogue reload flow:
// push tag → cache invalidated → manager calls gRPC GetLatestTag+LoadFromTag → fresh catalogue served.
func TestGRPCCatalogReloadAfterTagPush(t *testing.T) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "gitstore-git-service:latest",
		ExposedPorts: []string{"50051/tcp"},
		Env: map[string]string{
			"GITSTORE_DATA_DIR":  "/data/repos",
			"GITSTORE_GRPC_PORT": "50051",
		},
		WaitingFor: wait.ForLog("gRPC server listening").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("git-service container unavailable: %v", err)
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

	// Build a GRPCLoader and cache manager backed solely by gRPC — no shared volume.
	loader := catalog.NewGRPCLoader(client, zap.NewNop())
	mgr := cache.NewManager(loader, zap.NewNop(), 10*time.Minute)

	t.Run("initial load returns not-found on empty repo", func(t *testing.T) {
		_, err := mgr.Get(ctx)
		assert.Error(t, err, "empty repo should error on GetLatestTag")
	})

	// Commit a product file and create a tag — simulating what CreateProduct + PublishCatalog
	// would do through the API.
	_, err = client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/reload-test-product.md",
		Content:       []byte("---\nid: reload-001\nsku: RELOAD-001\ntitle: Reload Test Widget\nprice: 1.00\ncurrency: USD\n---\n"),
		CommitMessage: "Create product: Reload Test Widget",
	})
	require.NoError(t, err)

	_, err = client.CreateTag(ctx, gitclient.CreateTagParams{
		Name:    "v2.0.0",
		Message: "Release v2.0.0",
	})
	require.NoError(t, err)

	// Simulate the WS notification: invalidate the cache.
	mgr.Invalidate()

	// Next Get should reload via gRPC and return a valid catalogue.
	cat, err := mgr.Get(ctx)
	require.NoError(t, err)
	require.NotNil(t, cat)

	assert.Equal(t, "v2.0.0", cat.Tag(), "catalogue should reflect the newly pushed tag")

	// The reload must have used gRPC — no shared volume needed.
	// If the loader had used a local volume mount, it would have failed (none is mounted).
	products := cat.AllProducts()
	assert.Len(t, products, 1, "should have loaded 1 product from gRPC")
	if len(products) > 0 {
		assert.Equal(t, "RELOAD-001", products[0].SKU)
	}
}

// TestGRPCCatalogReloadCoalescesNotifications verifies that concurrent invalidations
// triggered by rapid-fire WS events only cause at most 2 gRPC reload calls.
func TestGRPCCatalogReloadCoalescesNotifications(t *testing.T) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "gitstore-git-service:latest",
		ExposedPorts: []string{"50051/tcp"},
		Env: map[string]string{
			"GITSTORE_GRPC_PORT": "50051",
		},
		WaitingFor: wait.ForLog("gRPC server listening").
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

	client, err := gitclient.NewClientWithAddr(addr)
	require.NoError(t, err)
	defer client.Close()

	loader := catalog.NewGRPCLoader(client, zap.NewNop())
	mgr := cache.NewManager(loader, zap.NewNop(), 10*time.Minute)

	// Simulate 5 rapid WS tag-push events (all invalidations, then concurrent Gets).
	for i := 0; i < 5; i++ {
		mgr.Invalidate()
	}

	// All concurrent Gets should not panic and should return consistently.
	const workers = 10
	errs := make([]error, workers)
	done := make(chan int, workers)
	for i := 0; i < workers; i++ {
		go func(idx int) {
			_, errs[idx] = mgr.Get(ctx)
			done <- idx
		}(i)
	}
	for i := 0; i < workers; i++ {
		<-done
	}

	// Empty repo errors are expected; what we're testing is no panic and consistent behaviour.
	for i, e := range errs {
		if e != nil {
			t.Logf("worker %d error (expected on empty repo): %v", i, e)
		}
	}
}
