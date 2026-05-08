// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Shared bufconn test helper for gitclient unit tests.

package gitclient_test

import (
	"context"
	"net"
	"testing"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const bufSize = 1024 * 1024

// startBufconn starts an in-process gRPC server and returns a Client backed by bufconn.
func startBufconn(t *testing.T, svc gitv1.GitServiceServer) *gitclient.Client {
	t.Helper()
	lis := bufconn.Listen(bufSize)
	srv := grpc.NewServer()
	gitv1.RegisterGitServiceServer(srv, svc)

	go func() {
		if err := srv.Serve(lis); err != nil {
			t.Logf("bufconn server stopped: %v", err)
		}
	}()
	t.Cleanup(func() {
		srv.Stop()
		lis.Close()
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return gitclient.NewClientFromConn(conn)
}
