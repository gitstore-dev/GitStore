// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package testutil

import (
	"context"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RepositoryFixture returns a complete, admitted Repository suitable for API
// lifecycle, watch, and resolver tests. Callers may mutate the returned value
// for the condition under test without repeating contract defaults.
func RepositoryFixture(uid, namespace, name string) *datastore.Repository {
	return &datastore.Repository{
		UID: uid, ID: uid, RepositoryID: uid,
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
		Namespace: namespace, NamespaceID: namespace, Name: name,
		DefaultBranch: "main", StorageClass: "standard",
		Spec:       []byte(`{"defaultBranch":"main","visibility":"PRIVATE","storageClass":"standard"}`),
		Generation: 1, ResourceVersion: "1",
		Status:            []byte(`{"observedGeneration":0,"conditions":[]}`),
		CreationTimestamp: time.Now().UTC(),
	}
}

// SystemRepositoryFixture returns the sole datastore-only Repository fixture.
func SystemRepositoryFixture(uid, namespace string) *datastore.Repository {
	repository := RepositoryFixture(uid, namespace, "gitstore-system")
	repository.CreationActor = "system:namespace-controller"
	repository.UpdateActor = repository.CreationActor
	return repository
}

// NativeGitTestConfiguration reads bounded signing input only after explicit
// owned-deployment opt-in. It never generates or normalizes secret material.
func NativeGitTestConfiguration(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("GIT_TEST_RUN") != "1" {
		t.Skip("use make test TARGET=git-integration against an explicitly owned Git service")
	}
	require.Equal(t, "1", os.Getenv("GIT_TEST_OWNED_DEPLOYMENT"))
	uri := os.Getenv("GIT_TEST_URI")
	host, port, err := net.SplitHostPort(uri)
	require.NoError(t, err, "GIT_TEST_URI must be a credential-free host:port")
	require.NotEmpty(t, host)
	require.NotContains(t, host, "@")
	number, err := strconv.Atoi(port)
	require.NoError(t, err)
	require.Greater(t, number, 0)
	require.LessOrEqual(t, number, 65535)
	file, err := os.Open(os.Getenv("GIT_TEST_HMAC_SECRET_FILE"))
	require.NoError(t, err)
	defer file.Close()
	info, err := file.Stat()
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular(), "RPC signing material must be a regular file")
	secret, err := io.ReadAll(io.LimitReader(file, 4097))
	require.NoError(t, err)
	require.LessOrEqual(t, len(secret), 4096)
	require.NotEmpty(t, strings.TrimSpace(string(secret)))
	signing := string(secret)
	clear(secret)
	return uri, signing
}

// LoseFirstSuccessfulGitReply discards one real success, without retrying or
// masking an upstream failure. The receipt is visible only to test assertions.
func LoseFirstSuccessfulGitReply(method string) (grpc.UnaryClientInterceptor, <-chan string) {
	var armed atomic.Bool
	armed.Store(true)
	receipts := make(chan string, 1)
	return func(ctx context.Context, called string, req, reply any, conn *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
		if err := invoke(ctx, called, req, reply, conn, options...); err != nil {
			return err
		}
		if called != method || !armed.CompareAndSwap(true, false) {
			return nil
		}
		switch response := reply.(type) {
		case *gitv1.DeleteFileResponse:
			receipts <- response.CommitSha
		case *gitv1.DeleteRepositoryResponse:
			receipts <- response.RepositoryId
		default:
			return status.Error(codes.Internal, "reply-loss fixture received an unexpected response type")
		}
		return status.Error(codes.Unavailable, "test discarded successful native RPC response")
	}, receipts
}
