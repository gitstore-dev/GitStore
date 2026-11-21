// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"strings"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/stretchr/testify/require"
)

func TestRPCSignatureBindsBodyMethodAudienceAndLifetime(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	message := &gitv1.CreateRepositoryRequest{RepositoryId: "repository", StorageClass: "standard", Authorization: &gitv1.RequestAuthorization{Actor: &gitv1.AuthContext{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}, Action: "repository.create", ResourceKind: "repository", RepositoryId: "repository"}}
	const method = "/gitstore.git.v1.GitService/CreateRepository"
	envelope, err := signRPCAt("shared-test-key", "gitstore-git", method, message, now, strings.Repeat("a", 32))
	require.NoError(t, err)
	digest, expiry, err := VerifyRPC("shared-test-key", "gitstore-git", method, envelope, message, now)
	require.NoError(t, err)
	require.Len(t, digest, 64)
	require.Equal(t, now.Add(time.Minute), expiry)
	for _, tc := range []struct {
		key, audience, method, signature string
		at                               time.Time
	}{
		{"wrong", "gitstore-git", method, envelope, now},
		{"shared-test-key", "gitstore-api", method, envelope, now},
		{"shared-test-key", "gitstore-git", method + "Wrong", envelope, now},
		{"shared-test-key", "gitstore-git", method, envelope, now.Add(time.Minute)},
		{"shared-test-key", "gitstore-git", method, envelope, now.Add(-6 * time.Second)},
		{"shared-test-key", "gitstore-git", method, "Bearer shared-test-key", now},
	} {
		_, _, err := VerifyRPC(tc.key, tc.audience, tc.method, tc.signature, message, tc.at)
		require.Error(t, err)
	}
	message.Authorization.Actor.Subject = "admin"
	_, _, err = VerifyRPC("shared-test-key", "gitstore-git", method, envelope, message, now)
	require.Error(t, err)
}

func TestRPCWireGolden(t *testing.T) {
	message := &gitv1.InfoRefsRequest{RepositoryId: "repository", Service: gitv1.Service_SERVICE_GIT_UPLOAD_PACK,
		Authorization: &gitv1.RequestAuthorization{Actor: &gitv1.AuthContext{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}, Action: "repository.contents.read", ResourceKind: "repository", RepositoryId: "repository"}}
	envelope, err := signRPCAt("shared-test-key", "gitstore-git", gitv1.GitService_InfoRefs_FullMethodName, message, time.UnixMilli(1700000000000), strings.Repeat("a", 32))
	require.NoError(t, err)
	require.Equal(t, "1700000000000:1700000060000:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa:4d4460c75ee3464f10c2597296caab56c7335b9f68f1275d8f90abab79485b26:072605f8daa33d0e3cb6aa7012742936c852b3d2b3495f689e2da3e07b370a76", envelope)
}
