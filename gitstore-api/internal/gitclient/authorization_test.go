// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestAuthorizationAllowsOnlyPolicyApprovedAnonymousRead(t *testing.T) {
	ctx := auth.ContextWithPrincipal(context.Background(), auth.Anonymous())

	_, err := RequestAuthorization(ctx, "repository.read.any", "repo-id")
	require.Error(t, err)

	ctx = auth.ContextWithAuthorizedAnonymous(ctx)
	authorization, err := RequestAuthorization(ctx, "repository.read.any", "repo-id")
	require.NoError(t, err)
	assert.Equal(t, "anon", authorization.Actor.Subject)
	assert.Equal(t, "none", authorization.Actor.AuthMethod)

	_, err = RequestAuthorization(ctx, "repository.write.any", "repo-id")
	require.Error(t, err)
}
