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
type authorizedNamespaceDeleteContextKey struct{}

// ContextWithRemoteAddr stores the caller IP/remote address so GraphQL auth
// middleware can pass it to providers without depending on Gin internals.
func ContextWithRemoteAddr(ctx context.Context, remoteAddr string) context.Context {
	return context.WithValue(ctx, remoteAddrContextKey{}, remoteAddr)
}

func remoteAddrFromContext(ctx context.Context) string {
	remoteAddr, _ := ctx.Value(remoteAddrContextKey{}).(string)
	return remoteAddr
}

// AuthorizedNamespaceForDeletion returns the exact namespace record whose
// deletion was authorized by GraphQLFieldAuthorizer.
func AuthorizedNamespaceForDeletion(ctx context.Context) (*datastore.Namespace, bool) {
	ns, ok := ctx.Value(authorizedNamespaceDeleteContextKey{}).(*datastore.Namespace)
	return ns, ok && ns != nil
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

	if requiresAuthenticatedPrincipal(opCtx) && principal.AuthMethod == "none" {
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
	var authz auth.AuthZProvider
	if a.registry != nil {
		authz = a.registry.AuthZ()
	}

	switch fc.Field.Name {
	case "createNamespace":
		tier, ok := nestedStringArg(fc.Args, "input", "tier")
		if !ok || tier != "ORGANIZATION" {
			return next(ctx)
		}
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
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
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		ns, action, err := a.namespaceDeleteAction(ctx, identifier, principal)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				return nil, gqlerror.Errorf("namespace %q not found", identifier)
			}
			return nil, gqlerror.Errorf("authorization error")
		}

		decision, err := authz.Authorize(ctx, principal, action, auth.ResourceContext{
			Kind:     "namespace",
			Name:     identifier,
			OwnerSub: ns.CreatedBy,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
		ctx = context.WithValue(ctx, authorizedNamespaceDeleteContextKey{}, ns)
	case "refreshToken":
		if auth.RawTokenFromContext(ctx) == "" {
			return nil, gqlerror.Errorf("refresh token requires bearer authentication")
		}
	}

	return next(ctx)
}

func (a *Authorize) namespaceDeleteAction(ctx context.Context, identifier string, principal *auth.Principal) (ns *datastore.Namespace, action string, err error) {
	if a.store == nil {
		return nil, "", fmt.Errorf("authorization store is not configured")
	}
	ns, err = a.store.GetNamespaceByIdentifier(ctx, identifier)
	if err != nil {
		return nil, "", err
	}
	if ns.CreatedBy == principal.Subject {
		return ns, "namespace.delete.own", nil
	}
	return ns, "namespace.delete.any", nil
}

// nestedStringArg reads a string field off a gqlgen-generated input struct
// (e.g. args["input"].tier). gqlgen always unmarshals GraphQL input objects
// into their generated struct type before FieldContext.Args is populated, so
// only the struct-based representation is handled here.
func nestedStringArg(args map[string]any, parent, key string) (string, bool) {
	parentVal, ok := args[parent]
	if !ok || parentVal == nil {
		return "", false
	}
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

// requiresAuthenticatedPrincipal reports whether the operation executes any
// root mutation field other than "login"/"__typename". It delegates to
// graphql.CollectFields — the same field-collection gqlgen itself uses to
// decide what will actually run — so fragment spreads, inline fragments, and
// @skip/@include directives are honored instead of re-derived by hand.
func requiresAuthenticatedPrincipal(opCtx *graphql.OperationContext) bool {
	if opCtx == nil || opCtx.Operation == nil || opCtx.Operation.Operation != ast.Mutation {
		return false
	}
	for _, field := range graphql.CollectFields(opCtx, opCtx.Operation.SelectionSet, []string{"Mutation"}) {
		if field.Name != "login" && field.Name != "__typename" {
			return true
		}
	}
	return false
}
