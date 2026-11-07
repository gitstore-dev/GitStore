// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

// Helper functions for GraphQL resolvers

// callerUsernameOrAnon extracts the caller username from auth context, or returns "anon".
func callerUsernameOrAnon(ctx context.Context, _ *mutationResolver) string {
	if p := auth.PrincipalFromContext(ctx); p != nil {
		return p.Subject
	}
	return "anon"
}
