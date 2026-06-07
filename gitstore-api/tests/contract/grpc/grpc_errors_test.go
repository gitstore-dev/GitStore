// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration test: gRPC error-path scenarios against a real git-service container.
// Covers: file not found, ref not found, commit on missing file (delete), tag already exists.
// Requires Docker. Run with: go test -tags grpc ./tests/integration/...

//go:build grpc

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestGRPCReadFileNotFound asserts GetFile returns an error when the path does not exist.
func TestGRPCReadFileNotFound(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()
	ctx := context.Background()

	// Create initial commit so HEAD exists.
	_, err := client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/exists.md",
		Content:       []byte("---\nsku: OK-001\n---\n"),
		CommitMessage: "Create exists product",
	})
	require.NoError(t, err)

	_, err = client.ReadFile(ctx, "products/nonexistent.md", "HEAD")
	assert.Error(t, err, "reading a nonexistent file should return an error")
}

// TestGRPCReadFileRefNotFound asserts GetFile returns an error when the ref does not exist.
func TestGRPCReadFileRefNotFound(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()
	ctx := context.Background()

	_, err := client.ReadFile(ctx, "products/any.md", "refs/tags/nonexistent-tag")
	assert.Error(t, err, "reading with unknown ref should return an error")
}

// TestGRPCDeleteFileNotFound asserts DeleteFile returns an error for a path that does not exist.
func TestGRPCDeleteFileNotFound(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()
	ctx := context.Background()

	// Establish HEAD with at least one commit.
	_, err := client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/anchor.md",
		Content:       []byte("---\nsku: ANCHOR-001\n---\n"),
		CommitMessage: "Create anchor product",
	})
	require.NoError(t, err)

	_, err = client.DeleteFile(ctx, gitclient.DeleteFileParams{
		Path:          "products/does-not-exist.md",
		CommitMessage: "Delete nonexistent product",
	})
	assert.Error(t, err, "deleting a nonexistent file should return an error")
}

// TestGRPCCreateTagAlreadyExists asserts CreateTag returns an error when the tag already exists.
func TestGRPCCreateTagAlreadyExists(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()
	ctx := context.Background()

	_, err := client.CommitFile(ctx, gitclient.CommitFileParams{
		Path:          "products/tagged.md",
		Content:       []byte("---\nsku: TAG-EXIST-001\n---\n"),
		CommitMessage: "Create tagged product",
	})
	require.NoError(t, err)

	_, err = client.CreateTag(ctx, gitclient.CreateTagParams{
		Name:    "v1.0.0-dup",
		Message: "First tag",
	})
	require.NoError(t, err)

	_, err = client.CreateTag(ctx, gitclient.CreateTagParams{
		Name:    "v1.0.0-dup",
		Message: "Duplicate tag",
	})
	assert.Error(t, err, "creating a duplicate tag should return an error")
}

// TestGRPCGetLatestTagEmptyRepo asserts GetLatestTag returns an error on a repo with no tags.
func TestGRPCGetLatestTagEmptyRepo(t *testing.T) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "gitstore-git-service:latest",
		ExposedPorts: []string{"50051/tcp"},
		Env: map[string]string{
			"GITSTORE_GRPC__PORT": "50051",
		},
		WaitingFor: wait.ForListeningPort("50051/tcp").
			WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	if err != nil {
		t.Fatalf("git-service container unavailable (ensure Docker is running): %v", err)
	}
	defer container.Terminate(ctx) //nolint:errcheck

	grpcPort, err := container.MappedPort(ctx, "50051")
	require.NoError(t, err)

	c, err := gitclient.NewClientWithAddr(fmt.Sprintf("localhost:%s", grpcPort.Port()))
	require.NoError(t, err)
	defer c.Close()

	// Provision a fresh (empty) repository — no tags in it.
	emptyRepoID := uuid.New().String()
	c.RepositoryID = emptyRepoID
	_, err = c.CreateRepository(ctx, emptyRepoID, "default")
	require.NoError(t, err)

	_, err = c.GetLatestTag(ctx)
	assert.Error(t, err, "GetLatestTag on empty repo should return an error")
}
