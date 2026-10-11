// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestSignedRequestNeverForwardsSigningKey(t *testing.T) {
	request := &gitv1.CreateRepositoryRequest{RepositoryId: "repository"}
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer user-token"))
	ctx, err := signedContext(ctx, "mysecret", gitv1.GitService_CreateRepository_FullMethodName, request)
	require.NoError(t, err)
	md, _ := metadata.FromOutgoingContext(ctx)
	require.Empty(t, md.Get("authorization"))
	require.Len(t, md.Get(auth.RPCSignatureMetadata), 1)
	envelope := md.Get(auth.RPCSignatureMetadata)[0]
	require.NotContains(t, envelope, "mysecret")
	_, _, err = auth.VerifyRPC("mysecret", "gitstore-git", gitv1.GitService_CreateRepository_FullMethodName, envelope, request, time.Now())
	require.NoError(t, err)
	request.RepositoryId = "another"
	_, _, err = auth.VerifyRPC("mysecret", "gitstore-git", gitv1.GitService_CreateRepository_FullMethodName, envelope, request, time.Now())
	require.Error(t, err)
}
