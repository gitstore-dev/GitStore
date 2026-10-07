// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import "context"

type principalContextKey struct{}
type rawTokenContextKey struct{}
type authorizedAnonymousContextKey struct{}

// ContextWithPrincipal stores p in ctx under the package-private principalContextKey.
func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// PrincipalFromContext retrieves the Principal stored by ContextWithPrincipal.
// Returns nil if no principal has been stored.
func PrincipalFromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalContextKey{}).(*Principal)
	return p
}

// ContextWithRawToken stores the raw Bearer token string in ctx.
// Called by ChainAuthMiddleware after extracting the Authorization header value.
func ContextWithRawToken(ctx context.Context, rawToken string) context.Context {
	return context.WithValue(ctx, rawTokenContextKey{}, rawToken)
}

// RawTokenFromContext retrieves the raw Bearer token stored by ContextWithRawToken.
// Returns "" if no raw token is present (unauthenticated requests, Basic Auth sessions).
func RawTokenFromContext(ctx context.Context) string {
	s, _ := ctx.Value(rawTokenContextKey{}).(string)
	return s
}

// ContextWithAuthorizedAnonymous records that the API's AuthZ middleware has
// explicitly approved an anonymous request. It is intentionally separate from
// the anonymous principal: AuthN alone must never authorize a GitService RPC.
func ContextWithAuthorizedAnonymous(ctx context.Context) context.Context {
	return context.WithValue(ctx, authorizedAnonymousContextKey{}, true)
}

// AuthorizedAnonymousFromContext reports whether AuthZ explicitly approved an
// anonymous request for the current API operation.
func AuthorizedAnonymousFromContext(ctx context.Context) bool {
	approved, _ := ctx.Value(authorizedAnonymousContextKey{}).(bool)
	return approved
}
