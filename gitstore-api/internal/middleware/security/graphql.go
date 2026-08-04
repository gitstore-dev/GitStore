// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/vektah/gqlparser/v2/ast"
	"go.uber.org/zap"
)

type remoteAddrContextKey struct{}

// ContextWithRemoteAddr stores the caller IP/remote address so GraphQL auth
// middleware can pass it to providers without depending on Gin internals.
func ContextWithRemoteAddr(ctx context.Context, remoteAddr string) context.Context {
	return context.WithValue(ctx, remoteAddrContextKey{}, remoteAddr)
}

func remoteAddrFromContext(ctx context.Context) string {
	remoteAddr, _ := ctx.Value(remoteAddrContextKey{}).(string)
	return remoteAddr
}

// GraphQLAuthenticator authenticates each GraphQL operation via the active
// authn chain and injects the resulting Principal into operation context.
func (a *Authenticate) GraphQLAuthenticator(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}
	if a.registry == nil || a.registry.AuthN() == nil {
		return graphql.OneShot(graphql.ErrorResponse(ctx, "authentication service unavailable"))
	}

	opCtx := graphql.GetOperationContext(ctx)
	if opCtx == nil {
		return graphql.OneShot(graphql.ErrorResponse(ctx, "invalid graphql operation context"))
	}

	req := auth.AuthRequest{
		Header:     opCtx.Headers,
		RemoteAddr: remoteAddrFromContext(ctx),
	}
	if req.Header == nil {
		req.Header = http.Header{}
	}

	authCtx := ctx
	if authHeader := req.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		authCtx = auth.ContextWithRawToken(authCtx, strings.TrimPrefix(authHeader, "Bearer "))
	}

	principal, decision, err := a.registry.AuthN().Authenticate(authCtx, req)
	if err != nil {
		a.logger.Warn("graphql auth chain error", zap.Error(err))
		return graphql.OneShot(graphql.ErrorResponse(ctx, "invalid or expired credentials"))
	}
	if decision.Outcome != auth.OutcomeAllow {
		return graphql.OneShot(graphql.ErrorResponse(ctx, "invalid or expired credentials"))
	}
	if principal == nil {
		principal = auth.Anonymous()
	}

	return next(auth.ContextWithPrincipal(authCtx, principal))
}

// GraphQLAuthorizer enforces cross-cutting GraphQL authz guardrails and
// provides a centralized middleware seam for future operation-level policies.
func (a *Authorize) GraphQLAuthorizer(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}

	opCtx := graphql.GetOperationContext(ctx)
	if opCtx == nil {
		return graphql.OneShot(graphql.ErrorResponse(ctx, "invalid graphql operation context"))
	}

	principal := auth.PrincipalFromContext(ctx)
	if principal == nil {
		principal = auth.Anonymous()
		ctx = auth.ContextWithPrincipal(ctx, principal)
	}

	if requiresAuthenticatedPrincipal(opCtx.Operation) && principal.AuthMethod == "none" {
		return graphql.OneShot(graphql.ErrorResponse(ctx, "authentication required"))
	}

	return next(ctx)
}

func requiresAuthenticatedPrincipal(op *ast.OperationDefinition) bool {
	if op == nil || op.Operation != ast.Mutation {
		return false
	}
	return firstOperationFieldName(op) != "login"
}

func firstOperationFieldName(op *ast.OperationDefinition) string {
	if op == nil {
		return ""
	}
	for _, selection := range op.SelectionSet {
		if field, ok := selection.(*ast.Field); ok {
			return field.Name
		}
	}
	return ""
}
