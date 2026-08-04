// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
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

// GraphQLFieldAuthorizer runs fine-grained GraphQL authorization checks for
// mutation fields that require policy evaluation against resource context.
func (a *Authorize) GraphQLFieldAuthorizer(ctx context.Context, next graphql.Resolver) (any, error) {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}
	fc := graphql.GetFieldContext(ctx)
	if fc == nil || fc.Object != "Mutation" {
		return next(ctx)
	}

	principal := auth.PrincipalFromContext(ctx)
	if principal == nil {
		principal = auth.Anonymous()
	}
	authz := a.registry.AuthZ()
	if authz == nil {
		return next(ctx)
	}

	switch fc.Field.Name {
	case "createNamespace":
		tier, ok := nestedStringArg(fc.Args, "input", "tier")
		if !ok || tier != "ORGANIZATION" {
			return next(ctx)
		}
		decision, err := authz.Authorize(ctx, principal, "namespace.create.organization", auth.ResourceContext{
			Kind: "namespace",
			Attrs: map[string]any{
				"tier": tier,
			},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
	case "deleteNamespace":
		identifier, ok := nestedStringArg(fc.Args, "input", "identifier")
		if !ok || identifier == "" {
			return next(ctx)
		}
		action, ownerSub, err := a.namespaceDeleteAction(ctx, identifier, principal)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				// Preserve existing resolver/service behavior for unknown namespaces.
				return next(ctx)
			}
			return nil, gqlerror.Errorf("authorization error")
		}

		decision, err := authz.Authorize(ctx, principal, action, auth.ResourceContext{
			Kind:     "namespace",
			Name:     identifier,
			OwnerSub: ownerSub,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
	case "refreshToken":
		if auth.RawTokenFromContext(ctx) == "" {
			return nil, gqlerror.Errorf("refresh token requires bearer authentication")
		}
	}

	return next(ctx)
}

func (a *Authorize) namespaceDeleteAction(ctx context.Context, identifier string, principal *auth.Principal) (action string, ownerSub string, err error) {
	if a.store == nil {
		return "", "", fmt.Errorf("authorization store is not configured")
	}
	ns, err := a.store.GetNamespaceByIdentifier(ctx, identifier)
	if err != nil {
		return "", "", err
	}
	ownerSub = ns.CreatedBy
	if ownerSub == principal.Subject {
		return "namespace.delete.own", ownerSub, nil
	}
	return "namespace.delete.any", ownerSub, nil
}

func nestedStringArg(args map[string]any, parent, key string) (string, bool) {
	parentVal, ok := args[parent]
	if !ok || parentVal == nil {
		return "", false
	}
	// map-based argument representation
	if m, ok := parentVal.(map[string]any); ok {
		v, ok := m[key]
		if !ok || v == nil {
			return "", false
		}
		return fmt.Sprint(v), true
	}
	// struct-based argument representation (gqlgen generated input types)
	rv := reflect.ValueOf(parentVal)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return "", false
	}
	field := rv.FieldByNameFunc(func(name string) bool { return strings.EqualFold(name, key) })
	if !field.IsValid() {
		return "", false
	}
	for field.Kind() == reflect.Pointer {
		if field.IsNil() {
			return "", false
		}
		field = field.Elem()
	}
	if field.Kind() != reflect.String {
		return "", false
	}
	return field.String(), true
}

func requiresAuthenticatedPrincipal(op *ast.OperationDefinition) bool {
	if op == nil || op.Operation != ast.Mutation {
		return false
	}
	for _, selection := range op.SelectionSet {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		if field.Name != "login" {
			return true
		}
	}
	return false
}
