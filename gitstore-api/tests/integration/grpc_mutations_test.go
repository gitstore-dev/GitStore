// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration test: CommitFile and DeleteFile via gRPC against a real git-service container.
// Exercises concurrent mutations and asserts no filesystem artefacts remain on the API side.
// Requires Docker. Run with: go test -tags integration ./tests/integration/...

//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startMutationContainer starts a git-service container and returns a connected client.
// Skips the test if Docker is unavailable or the image is missing.
func startMutationContainer(t *testing.T) (*gitclient.Client, func()) {
	t.Helper()
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
		t.Skipf("git-service container unavailable (Docker not running or image missing): %v", err)
	}

	grpcPort, err := container.MappedPort(ctx, "50051")
	require.NoError(t, err)

	addr := fmt.Sprintf("localhost:%s", grpcPort.Port())
	client, err := gitclient.NewClientWithAddr(addr)
	require.NoError(t, err)

	cleanup := func() {
		client.Close()
		if termErr := container.Terminate(ctx); termErr != nil {
			t.Logf("failed to terminate container: %v", termErr)
		}
	}
	return client, cleanup
}

// TestGRPCCommitFile verifies CommitFile creates a commit and returns a SHA.
func TestGRPCCommitFile(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()

	ctx := context.Background()

	sha, err := client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/test-product.md",
		Content:       []byte("---\nsku: TEST-001\ntitle: Integration Widget\n---\n"),
		CommitMessage: "Create product: Integration Widget",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, sha, "expected a non-empty commit SHA")
	assert.Len(t, sha, 40, "expected a 40-char hex SHA")

	// Verify the file is readable via GetFile.
	data, err := client.ReadFile(ctx, "products/test-product.md", "HEAD")
	require.NoError(t, err)
	assert.Contains(t, string(data), "TEST-001")
}

// TestGRPCDeleteFile verifies DeleteFile removes a previously committed file.
func TestGRPCDeleteFile(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()

	ctx := context.Background()

	// First create the file.
	_, err := client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/delete-me.md",
		Content:       []byte("---\nsku: DEL-001\ntitle: Deletable Product\n---\n"),
		CommitMessage: "Create product: Deletable Product",
	})
	require.NoError(t, err)

	// Now delete it.
	sha, err := client.DeleteFile(ctx, gitclient.DeleteFileParams{
		Path:          "products/delete-me.md",
		CommitMessage: "Delete product: Deletable Product",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, sha)

	// File must no longer be readable.
	_, err = client.ReadFile(ctx, "products/delete-me.md", "HEAD")
	assert.Error(t, err, "file should be absent after DeleteFile")
}

// TestGRPCConcurrentCommitFile submits 10 concurrent CommitFile RPCs and asserts
// all succeed with distinct SHAs and that no temp artefacts remain on the API side.
func TestGRPCConcurrentCommitFile(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()

	ctx := context.Background()

	const workers = 10
	type result struct {
		sha string
		err error
	}
	results := make([]result, workers)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sha, err := client.CommitFile(ctx, gitclient.CommitFileParams{
				Path:          fmt.Sprintf("products/concurrent-%02d.md", idx),
				Content:       []byte(fmt.Sprintf("---\nsku: CONC-%02d\ntitle: Concurrent Product %d\n---\n", idx, idx)),
				CommitMessage: fmt.Sprintf("Create product: Concurrent Product %d", idx),
			})
			results[idx] = result{sha: sha, err: err}
		}(i)
	}
	wg.Wait()

	// All commits must succeed with non-empty SHAs.
	shas := make(map[string]bool)
	for i, r := range results {
		require.NoError(t, r.err, "worker %d failed", i)
		assert.NotEmpty(t, r.sha, "worker %d returned empty SHA", i)
		shas[r.sha] = true
	}
	// All SHAs must be distinct (each commit advances the tree).
	assert.Len(t, shas, workers, "expected %d distinct commit SHAs", workers)

	// Assert no temp artefacts left in the system temp dir by inspecting common patterns.
	// The API never writes to disk for git operations — all writes go through gRPC.
	assertNoGitArtefacts(t)
}

// TestGRPCCreateTag verifies CreateTag creates an annotated tag on HEAD.
func TestGRPCCreateTag(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()

	ctx := context.Background()

	// Need at least one commit before tagging.
	_, err := client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/tagged-product.md",
		Content:       []byte("---\nsku: TAG-001\ntitle: Tagged Product\n---\n"),
		CommitMessage: "Create product: Tagged Product",
	})
	require.NoError(t, err)

	sha, err := client.CreateTag(ctx, gitclient.CreateTagParams{
		Name:    "v1.0.0",
		Message: "Release v1.0.0",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, sha)

	// GetLatestTag must now return v1.0.0.
	tag, err := client.GetLatestTag(ctx)
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", tag.Name)
}

// assertNoGitArtefacts checks that no unexpected git working directories or
// temp clone directories exist in the standard temp location, which would
// indicate the API side mistakenly wrote to local disk.
func assertNoGitArtefacts(t *testing.T) {
	t.Helper()
	tmpDir := os.TempDir()
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return // can't check — don't fail
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Fail if any gitstore-specific temp clone directory leaked.
		name := e.Name()
		if len(name) > 0 {
			gitDir := filepath.Join(tmpDir, name, ".git")
			if _, statErr := os.Stat(gitDir); statErr == nil {
				t.Errorf("unexpected git working directory found in %s: %s (API must not clone locally)",
					tmpDir, name)
			}
		}
	}
}
