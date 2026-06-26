// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build grpc

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testHmacSecret = "ci-test-grpc-hmac-secret"

var (
	sharedGRPCAddr string
	sharedRepoID   string
)

// startSharedClient returns a client targeting the shared repository provisioned in TestMain.
func startSharedClient(t *testing.T) (*gitclient.Client, error) {
	t.Helper()
	if sharedGRPCAddr == "" {
		return nil, fmt.Errorf("shared gRPC test container is not initialized")
	}
	c, err := gitclient.NewClientWithAddr(sharedGRPCAddr, testHmacSecret)
	if err != nil {
		return nil, err
	}
	c.RepositoryID = sharedRepoID
	return c, nil
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	req := testcontainers.ContainerRequest{
		Image:        "gitstore-git-service:latest",
		ExposedPorts: []string{"50051/tcp"},
		Env: map[string]string{
			"GITSTORE_GIT__DATA_DIR":           "/data/repos",
			"GITSTORE_GRPC__PORT":              "50051",
			"GITSTORE_AUTH__GRPC__HMAC_SECRET": testHmacSecret,
		},
		WaitingFor: wait.ForListeningPort("50051/tcp").
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: git-service container unavailable: %v\n", err)
		os.Exit(1)
	}

	grpcPort, err := container.MappedPort(ctx, "50051")
	if err != nil {
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "failed to resolve mapped gRPC port: %v\n", err)
		os.Exit(1)
	}

	sharedGRPCAddr = fmt.Sprintf("localhost:%s", grpcPort.Port())
	sharedRepoID = uuid.New().String()

	// Provision the shared repository used by catalogue load and reload tests.
	setupClient, err := gitclient.NewClientWithAddr(sharedGRPCAddr, testHmacSecret)
	if err == nil {
		_, _ = setupClient.CreateRepository(ctx, sharedRepoID, "default")
		_ = setupClient.Close()
	}

	code := m.Run()

	if termErr := container.Terminate(ctx); termErr != nil {
		fmt.Fprintf(os.Stderr, "failed to terminate shared test container: %v\n", termErr)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
