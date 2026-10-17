// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"unicode"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type remoteAddrContextKey struct{}
type authorizedNamespaceDeleteContextKey struct{}
type authorizedNamespaceTransferContextKey struct{}
type authorizationLedgerContextKey struct{}
type authorizedFileNodesContextKey struct{}
type authorizedRepositoryReadContextKey struct{}
type authorizedMissingNodesContextKey struct{}

type nodeIdentity struct{ kind, uid string }
type authorizedMissingNodes struct {
	field *graphql.FieldContext
	nodes map[nodeIdentity]*gqlerror.Error
}

// AuthorizedNodeError preserves absence observed by this field's authorization.
// A later create must not be exposed by a second, unauthorized lookup.
func AuthorizedNodeError(ctx context.Context, kind, uid string) error {
	missing, _ := ctx.Value(authorizedMissingNodesContextKey{}).(*authorizedMissingNodes)
	if missing == nil || missing.field != graphql.GetFieldContext(ctx) {
		return nil
	}
	err := missing.nodes[nodeIdentity{kind, uid}]
	if err == nil {
		return nil
	}
	result := *err
	return &result
}

func preserveMissingNode(ctx context.Context, kind, uid string, cause error) {
	missing, _ := ctx.Value(authorizedMissingNodesContextKey{}).(*authorizedMissingNodes)
	if missing != nil {
		result := admission.ResourceNotFound(kind, strings.ToLower(kind)+" not found")
		var notFound *authFieldNotFoundError
		if errors.As(cause, &notFound) {
			result = notFound.render()
		}
		result.Err = cause
		missing.nodes[nodeIdentity{kind, uid}] = result
	}
}

type authorizedRepositoryRead struct {
	repository *datastore.Repository
	namespace  *datastore.Namespace
	field      *graphql.FieldContext
}

// AuthorizedRepositoryRead returns only the exact field-scoped read checked
// by repository.read; mutations and sibling fields do not share this record.
func AuthorizedRepositoryRead(ctx context.Context, by model.RepositoryBy) (*datastore.Repository, *datastore.Namespace, bool) {
	read, ok := ctx.Value(authorizedRepositoryReadContextKey{}).(*authorizedRepositoryRead)
	if !ok || read == nil || graphql.GetFieldContext(ctx) != read.field {
		return nil, nil, false
	}
	if by.ID != nil {
		uid, err := decodeGlobalIDAs("Repository", *by.ID)
		if err != nil || uid != read.repository.UID {
			return nil, nil, false
		}
	} else if by.NamespacePath == nil || by.NamespacePath.Namespace != read.namespace.Name || by.NamespacePath.Name != read.repository.Name {
		return nil, nil, false
	}
	return read.repository, read.namespace, true
}

// AuthorizedFileNode returns the exact field-scoped lookup checked by file.read.
// A present nil record preserves a missing lookup without a second datastore read.
func AuthorizedFileNode(ctx context.Context, uid string) (*datastore.File, bool) {
	files, _ := ctx.Value(authorizedFileNodesContextKey{}).(map[string]*datastore.File)
	file, ok := files[uid]
	return file, ok
}

// AuthorizedNamespaceTransfer carries the exact namespace record and encoded
// target owner subject authorized by GraphQLFieldAuthorizer's
// "transferNamespaceOwner" case (ADR-0010 §14), for the resolver to consume
// without re-fetching or re-authorizing.
type AuthorizedNamespaceTransfer struct {
	Namespace      *datastore.Namespace
	TargetOwnerSub string
}

// AuthorizedNamespaceForTransfer returns the namespace/target owner pair
// whose transfer was authorized by GraphQLFieldAuthorizer.
func AuthorizedNamespaceForTransfer(ctx context.Context) (*AuthorizedNamespaceTransfer, bool) {
	t, ok := ctx.Value(authorizedNamespaceTransferContextKey{}).(*AuthorizedNamespaceTransfer)
	return t, ok && t != nil
}

// authorizationLedger is deliberately operation-scoped. gqlgen can execute
// sibling fields concurrently, while a subscription invokes the response hook
// once per payload.
type authorizationLedger struct {
	mu        sync.Mutex
	expected  int
	completed int
	denied    int
}

func (l *authorizationLedger) begin() func(allowed bool) {
	l.mu.Lock()
	l.expected++
	l.mu.Unlock()
	return func(allowed bool) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.completed++
		if !allowed {
			l.denied++
		}
	}
}

func (l *authorizationLedger) snapshot() (expected, completed, denied int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.expected, l.completed, l.denied
}

// ContextWithRemoteAddr stores the caller IP/remote address so GraphQL auth
// middleware can pass it to providers without depending on Gin internals.
func ContextWithRemoteAddr(ctx context.Context, remoteAddr string) context.Context {
	return context.WithValue(ctx, remoteAddrContextKey{}, remoteAddr)
}

// RemoteAddrFromContext retrieves the caller address stored by
// ContextWithRemoteAddr.
func RemoteAddrFromContext(ctx context.Context) string {
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
	// transport.Websocket authenticates connection_init before accepting a
	// subscription and stores its verified principal in the connection context.
	// Re-authenticating here would discard that credential because gqlgen keeps
	// connection_init headers separate from operation headers.
	if auth.PrincipalFromContext(ctx) != nil {
		return next(ctx)
	}

	opCtx := graphql.GetOperationContext(ctx)
	if opCtx == nil {
		return graphql.OneShot(graphql.ErrorResponse(ctx, "invalid graphql operation context"))
	}

	req := auth.AuthRequest{
		Header:     opCtx.Headers,
		RemoteAddr: RemoteAddrFromContext(ctx),
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
		return unauthenticatedGraphQLResponse()
	}
	if decision.Outcome != auth.OutcomeAllow {
		return unauthenticatedGraphQLResponse()
	}
	if principal == nil {
		principal = auth.Anonymous()
	}

	return next(auth.ContextWithPrincipal(authCtx, principal))
}

// This code is emitted only before operation execution, so a credential source
// may safely invalidate the rejected token without replaying an executed mutation.
func unauthenticatedGraphQLResponse() graphql.ResponseHandler {
	return graphql.OneShot(&graphql.Response{Errors: gqlerror.List{&gqlerror.Error{
		Message: "invalid or expired credentials", Extensions: map[string]any{"code": "UNAUTHENTICATED"},
	}}})
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

	return next(context.WithValue(ctx, authorizationLedgerContextKey{}, &authorizationLedger{}))
}

// GraphQLResponseAuthorizer is the final GraphQL security boundary. Field
// middleware has already made the policy decision before a resolver can
// produce protected data; this hook records the terminal outcome for ordinary
// responses and every subscription payload without reshaping response data.
func (a *Authorize) GraphQLResponseAuthorizer(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	response := next(ctx)
	if a.logger == nil {
		a.logger = zap.NewNop()
	}
	principal := auth.PrincipalFromContext(ctx)
	subject := "anonymous"
	if principal != nil && principal.Subject != "" {
		subject = principal.Subject
	}
	expected, completed, denied := 0, 0, 0
	if ledger, ok := ctx.Value(authorizationLedgerContextKey{}).(*authorizationLedger); ok && ledger != nil {
		expected, completed, denied = ledger.snapshot()
	}
	fieldsComplete := expected == completed
	if !fieldsComplete {
		a.logger.Error("graphql response has incomplete authorization decisions",
			zap.Int("expected_protected_fields", expected),
			zap.Int("completed_protected_fields", completed),
		)
	}
	a.logger.Debug("graphql response authorization complete",
		zap.String("subject", subject),
		zap.Int("protected_fields", expected),
		zap.Int("denied_fields", denied),
		zap.Bool("protected_fields_complete", fieldsComplete),
		zap.Bool("has_errors", response != nil && len(response.Errors) != 0),
	)
	return response
}

// GraphQLFieldAuthorizer runs fine-grained GraphQL authorization checks for
// root fields that require policy evaluation against resource context.
func (a *Authorize) GraphQLFieldAuthorizer(ctx context.Context, next graphql.Resolver) (any, error) {
	if a.logger == nil {
		a.logger = zap.NewNop()
	}
	fc := graphql.GetFieldContext(ctx)
	if fc == nil {
		return next(ctx)
	}
	if fc.Object == "Query" && (fc.Field.Name == "node" || fc.Field.Name == "nodes") {
		ctx = context.WithValue(ctx, authorizedMissingNodesContextKey{}, &authorizedMissingNodes{
			field: fc, nodes: make(map[nodeIdentity]*gqlerror.Error),
		})
	}
	var finishDecision func(bool)
	if ledger, ok := ctx.Value(authorizationLedgerContextKey{}).(*authorizationLedger); ok && ledger != nil && graphqlFieldRequiresAuthorization(fc) {
		finishDecision = ledger.begin()
		defer func() {
			if finishDecision != nil {
				finishDecision(false)
			}
		}()
	}

	principal := auth.PrincipalFromContext(ctx)
	if principal == nil {
		principal = auth.Anonymous()
	}
	if (fc.Object == "Query" || fc.Object == "Mutation" || fc.Object == "Subscription") &&
		principal.AuthMethod == "serviceaccount-assertion" &&
		(fc.Object != "Mutation" || fc.Field.Name != "issueServiceAccountToken") {
		return nil, &gqlerror.Error{
			Message:    "service account assertions may only issue access tokens",
			Extensions: map[string]any{"code": "FORBIDDEN"},
		}
	}
	if fc.Object != "Mutation" && fc.Object != "Subscription" && !isAdditionalCatalogField(fc) && !isNamespaceQueryField(fc) && !isRepositoryQueryField(fc) && !isProductQueryField(fc) && !isCategoryProductsField(fc) && !isFileQueryField(fc) && !isCategoryQueryField(fc) {
		return next(ctx)
	}
	var authz auth.AuthZProvider
	if a.registry != nil {
		authz = a.registry.AuthZ()
	}
	if fc.Object == "Subscription" {
		return a.authorizeSubscription(ctx, next, fc, principal, authz, func() {
			if finishDecision != nil {
				finishDecision(true)
				finishDecision = nil
			}
		})
	}
	if isFileQueryField(fc) {
		namespace, _ := directStringArg(fc.Args, "namespace")
		name, _ := directStringArg(fc.Args, "name")
		if err := a.authorizeFileRead(ctx, principal, authz, namespace, name); err != nil {
			return nil, err
		}
	} else if isProductQueryField(fc) {
		if err := a.authorizeProductQueryField(ctx, fc, principal, authz); err != nil {
			return nil, err
		}
	} else if isCategoryProductsField(fc) {
		if err := a.authorizeCategoryProductsField(ctx, fc, principal, authz); err != nil {
			return nil, err
		}
	} else if isCategoryQueryField(fc) {
		if err := a.authorizeCategoryQueryField(ctx, fc, principal, authz); err != nil {
			return nil, err
		}
	} else {
		repositoryCtx, err := a.authorizeRepositoryField(ctx, fc, principal)
		if err != nil {
			var notFound *authFieldNotFoundError
			if errors.As(err, &notFound) {
				return nil, notFound.render()
			}
			return nil, err
		}
		ctx = repositoryCtx
	}
	if err := a.authorizeProductNodeField(ctx, fc, principal, authz); err != nil {
		return nil, err
	}
	fileCtx, err := a.authorizeFileNodeField(ctx, fc, principal, authz)
	if err != nil {
		return nil, err
	}
	ctx = fileCtx
	if err := a.authorizeCategoryNodeField(ctx, fc, principal, authz); err != nil {
		return nil, err
	}
	if err := a.authorizeNamespaceField(ctx, fc, principal, authz); err != nil {
		return nil, err
	}
	if err := a.authorizeAdditionalCatalogField(ctx, fc, principal, authz); err != nil {
		return nil, err
	}

	switch fc.Field.Name {
	case "createProduct":
		namespace, _ := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		owner, err := a.namespaceOwnerFact(ctx, namespace)
		if err != nil {
			return nil, err
		}
		if err := authorizeProductAction(ctx, authz, principal, "product.create", name, namespace, owner); err != nil {
			return nil, err
		}
	case "updateProduct":
		namespace, _ := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		if err := a.authorizeStoredProductAction(ctx, authz, principal, "product.update", namespace, name, ""); err != nil {
			return nil, err
		}
	case "deleteProduct":
		encodedID, _ := nestedStringArg(fc.Args, "input", "id")
		rawID, err := decodeGlobalIDAs("Product", encodedID)
		if err != nil {
			return nil, gqlerror.Errorf("invalid product ID")
		}
		if err := a.authorizeStoredProductAction(ctx, authz, principal, "product.delete", "", "", rawID); err != nil {
			return nil, err
		}
	case "completeProductDeletion":
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		name, _ := nestedStringArg(fc.Args, "input", "name")
		if err := a.authorizeStoredProductAction(ctx, authz, principal, "product.purge", namespace, name, ""); err != nil {
			return nil, err
		}
	case "createNamespace":
		tier, ok := nestedStringPath(fc.Args, "input", "spec", "tier")
		if !ok || tier == "" {
			tier = "USER"
		}
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		decision, err := authz.Authorize(ctx, principal, auth.ActionNamespaceCreate, auth.ResourceContext{
			Kind: "namespace", Name: name,
			Attrs: map[string]any{
				"tier": tier,
			},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
	case "deleteNamespace":
		encodedID, ok := nestedStringArg(fc.Args, "input", "id")
		if !ok || encodedID == "" {
			return nil, gqlerror.Errorf("invalid namespace ID")
		}
		uid, err := decodeGlobalIDAs("Namespace", encodedID)
		if err != nil {
			return nil, gqlerror.Errorf("invalid namespace ID")
		}
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		ns, err := a.store.GetNamespace(ctx, uid)
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		decision, err := authz.Authorize(ctx, principal, auth.ActionNamespaceDelete, auth.ResourceContext{
			Kind:     "namespace",
			Name:     ns.Name,
			OwnerSub: ns.EffectiveOwnerSub(),
			Attrs:    namespaceAuthorizationAttrs(ns),
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
		ctx = context.WithValue(ctx, authorizedNamespaceDeleteContextKey{}, ns)
	case "transferNamespaceOwner":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		encodedID, ok := nestedStringArg(fc.Args, "input", "namespaceId")
		if !ok || encodedID == "" {
			return nil, gqlerror.Errorf("invalid namespace ID")
		}
		uid, err := decodeGlobalIDAs("Namespace", encodedID)
		if err != nil {
			return nil, gqlerror.Errorf("invalid namespace ID")
		}
		ns, err := a.store.GetNamespace(ctx, uid)
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		targetKind, _ := nestedStringPath(fc.Args, "input", "targetOwnerRef", "kind")
		targetName, _ := nestedStringPath(fc.Args, "input", "targetOwnerRef", "name")
		targetOwnerSub, err := normalizeTransferTarget(targetKind, targetName)
		if err != nil {
			return nil, gqlerror.Errorf("%s", err.Error())
		}
		ownerKind, ownerName := datastore.DecodeOwnerSubject(targetOwnerSub)
		attrs := namespaceAuthorizationAttrs(ns)
		attrs["targetOwnerRef"] = map[string]any{"kind": string(ownerKind), "name": ownerName}
		decision, err := authz.Authorize(ctx, principal, "namespace.transfer", auth.ResourceContext{
			Kind: "namespace", Name: ns.Name, OwnerSub: ns.EffectiveOwnerSub(),
			Attrs: attrs,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
		ctx = context.WithValue(ctx, authorizedNamespaceTransferContextKey{}, &AuthorizedNamespaceTransfer{Namespace: ns, TargetOwnerSub: targetOwnerSub})
	case "completeNamespaceDeletion":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		targetName, ok := nestedStringArg(fc.Args, "input", "name")
		if !ok {
			return nil, &gqlerror.Error{Message: "name is required", Extensions: map[string]any{"code": "BAD_USER_INPUT"}}
		}
		if strings.TrimSpace(targetName) == "" {
			return nil, &gqlerror.Error{Message: "name must not be empty", Extensions: map[string]any{"code": "BAD_USER_INPUT"}}
		}
		decision, err := authz.Authorize(ctx, principal, "namespace.purge", auth.ResourceContext{
			Kind: "namespace",
			Name: targetName,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "provisionNamespaceSystemRepository":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, auth.ActionNamespaceSystemRepositoryProvision, auth.ResourceContext{
			Kind: "namespace", Name: namespace,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "completeRepositoryDeletion":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, "repository.purge", auth.ResourceContext{
			Kind: "repository", Name: name, Attrs: map[string]any{"namespace": namespace},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "provisionRepositoryStorage":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, "repository.status.write", auth.ResourceContext{
			Kind: "repository", Name: name, Attrs: map[string]any{"namespace": namespace},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "updateCategoryStatus":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, auth.ActionCategoryTaxonomyStatusWrite, auth.ResourceContext{
			Kind:  "categoryTaxonomy",
			Name:  name,
			Attrs: map[string]any{"namespace": namespace},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "updateProductStatus":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, "product.status.write", auth.ResourceContext{
			Kind: "product", Name: name, Attrs: map[string]any{"namespace": namespace},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("permission denied: %s", decision.Reason), Extensions: map[string]any{"code": "FORBIDDEN"}}
		}
	case "updateRepositoryStatus":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, "repository.status.write", auth.ResourceContext{
			Kind: "repository", Name: name, Attrs: map[string]any{"namespace": namespace},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("permission denied: %s", decision.Reason), Extensions: map[string]any{"code": "FORBIDDEN"}}
		}
	case "updateNamespaceStatus":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		decision, err := authz.Authorize(ctx, principal, "namespace.status.write", auth.ResourceContext{
			Kind: "namespace", Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("permission denied: %s", decision.Reason), Extensions: map[string]any{"code": "FORBIDDEN"}}
		}
	case "createCategory":
		namespace, _ := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		owner, err := a.namespaceOwnerFact(ctx, namespace)
		if err != nil {
			return nil, err
		}
		if err := authorizeCategoryAction(ctx, authz, principal, "categoryTaxonomy.create", auth.ResourceContext{
			Kind: "categoryTaxonomy", Name: name, OwnerSub: owner, Attrs: map[string]any{"namespace": namespace},
		}); err != nil {
			return nil, err
		}
	case "updateCategory":
		namespace, _ := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		if err := a.authorizeStoredCategoryAction(ctx, authz, principal, "categoryTaxonomy.update", func() (*datastore.CategoryTaxonomy, error) {
			return a.store.GetCategoryTaxonomyByName(ctx, namespace, name)
		}); err != nil {
			return nil, err
		}
	case "deleteCategory":
		encodedID, _ := nestedStringArg(fc.Args, "input", "id")
		uid, err := decodeCategoryID(encodedID)
		if err != nil {
			return nil, gqlerror.Errorf("invalid category ID")
		}
		if err := a.authorizeStoredCategoryAction(ctx, authz, principal, "categoryTaxonomy.delete", func() (*datastore.CategoryTaxonomy, error) {
			return a.store.GetCategoryTaxonomy(ctx, uid)
		}); err != nil {
			return nil, err
		}
	case "completeCategoryDeletion":
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		name, _ := nestedStringArg(fc.Args, "input", "name")
		if err := authorizeCategoryAction(ctx, authz, principal, "categoryTaxonomy.purge", auth.ResourceContext{
			Kind: "categoryTaxonomy", Name: name, Attrs: map[string]any{"namespace": namespace},
		}); err != nil {
			return nil, err
		}
	case "updateResourceStatus":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		kind, _ := nestedStringArg(fc.Args, "input", "kind")
		// Match the concrete generic status backends. Catalog membership alone
		// does not expose an operation (Product has only a typed status mutation).
		if kind != "Namespace" && kind != "Repository" && kind != "CategoryTaxonomy" && kind != "File" {
			return nil, &gqlerror.Error{Message: "unsupported status kind", Extensions: map[string]any{"code": "UNSUPPORTED_KIND"}}
		}
		name, _ := nestedStringArg(fc.Args, "input", "name")
		action := lowerCamelFirst(kind) + ".status.write"
		definition, found := auth.LookupAction(action)
		if !found || definition.Reserved {
			return nil, &gqlerror.Error{Message: "unsupported status kind", Extensions: map[string]any{"code": "UNSUPPORTED_KIND"}}
		}
		namespace, _ := nestedStringArg(fc.Args, "input", "namespace")
		decision, err := authz.Authorize(ctx, principal, action, auth.ResourceContext{
			Kind:  definition.Kind,
			Name:  name,
			Attrs: map[string]any{"namespace": namespace},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "issueServiceAccountToken":
		if principal.AuthMethod != "serviceaccount-assertion" {
			return nil, &gqlerror.Error{
				Message:    "service account assertion authentication is required",
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
		namespace, _ := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		if namespace != "" {
			return nil, gqlerror.Errorf("ServiceAccount is cluster-scoped; omit metadata.namespace")
		}
		expectedSubject := datastore.ServiceAccountSubject(name)
		if principal.Subject != expectedSubject {
			return nil, &gqlerror.Error{
				Message:    "service account can only issue tokens for itself",
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
		// RBAC authorization for token issuance
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		decision, err := authz.Authorize(ctx, principal, auth.ActionServiceAccountTokenIssue, auth.ResourceContext{
			Kind: "serviceAccount",
			Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "createServiceAccount":
		// Service account assertions cannot create service accounts
		if principal.AuthMethod == "serviceaccount-assertion" {
			return nil, &gqlerror.Error{
				Message:    "service account assertions cannot create service accounts",
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		decision, err := authz.Authorize(ctx, principal, auth.ActionServiceAccountCreate, auth.ResourceContext{
			Kind: "serviceAccount",
			Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "rotateServiceAccountKey":
		// Service account assertions cannot rotate other accounts' keys
		if principal.AuthMethod == "serviceaccount-assertion" {
			return nil, &gqlerror.Error{
				Message:    "service account assertions cannot rotate keys",
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		decision, err := authz.Authorize(ctx, principal, auth.ActionServiceAccountKeyRotate, auth.ResourceContext{
			Kind: "serviceAccount",
			Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	case "deleteServiceAccount":
		// Service account assertions cannot delete accounts
		if principal.AuthMethod == "serviceaccount-assertion" {
			return nil, &gqlerror.Error{
				Message:    "service account assertions cannot delete service accounts",
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		decision, err := authz.Authorize(ctx, principal, auth.ActionServiceAccountDelete, auth.ResourceContext{
			Kind: "serviceAccount",
			Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, &gqlerror.Error{
				Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
				Extensions: map[string]any{"code": "FORBIDDEN"},
			}
		}
	}

	if finishDecision != nil {
		finishDecision(true)
		finishDecision = nil
	}
	return next(ctx)
}

// authorizeCategoryProductsField preserves the category parent's namespace
// and namespace owner when checking product.list. gqlgen exposes the resolved
// parent through FieldContext.Parent.Result before child field middleware is
// invoked, so this check remains ahead of the membership-index read.
func (a *Authorize) authorizeCategoryProductsField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	var category *model.Category
	if fc != nil && fc.Parent != nil {
		category, _ = fc.Parent.Result.(*model.Category)
	}
	if category == nil || category.Metadata == nil || category.Metadata.Namespace == "" {
		return gqlerror.Errorf("authorization error")
	}
	namespace := category.Metadata.Namespace
	owner := ""
	if a.store != nil {
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return gqlerror.Errorf("authorization error")
		}
		if ns != nil {
			owner = ns.EffectiveOwnerSub()
		}
	}
	return authorizeProductAction(ctx, authz, principal, "product.list", "", namespace, owner)
}

func isAdditionalCatalogField(fc *graphql.FieldContext) bool {
	if fc == nil {
		return false
	}
	switch fc.Object + "." + fc.Field.Name {
	case "Query.collection", "Query.collections", "Query.productVariant", "Query.productVariants", "Collection.products", "Product.productVariants":
		return true
	}
	return false
}

// These existing catalog reads use the same action/fact contract as Product.
// A parent read never grants permission to enumerate its related resources.
func (a *Authorize) authorizeAdditionalCatalogField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	if fc == nil {
		return nil
	}
	list := func(kind, namespace string) error {
		if a.store == nil || namespace == "" {
			return gqlerror.Errorf("authorization context is incomplete")
		}
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return gqlerror.Errorf("authorization error")
		}
		return authorizeCategoryAction(ctx, authz, principal, kind+".list", auth.ResourceContext{Kind: kind, OwnerSub: ns.EffectiveOwnerSub(), Attrs: map[string]any{"namespace": namespace}})
	}
	if fc.Object == "Collection" && fc.Field.Name == "products" {
		if fc.Parent == nil {
			return gqlerror.Errorf("authorization context is incomplete")
		}
		parent, ok := fc.Parent.Result.(*model.Collection)
		if !ok || parent == nil || parent.Metadata == nil {
			return gqlerror.Errorf("authorization context is incomplete")
		}
		return list("product", parent.Metadata.Namespace)
	}
	if fc.Object == "Product" && fc.Field.Name == "productVariants" {
		if fc.Parent == nil {
			return gqlerror.Errorf("authorization context is incomplete")
		}
		parent, ok := fc.Parent.Result.(*model.Product)
		if !ok || parent == nil || parent.Metadata == nil {
			return gqlerror.Errorf("authorization context is incomplete")
		}
		return list("productVariant", parent.Metadata.Namespace)
	}
	if fc.Object != "Query" {
		return nil
	}
	if fc.Field.Name == "collections" || fc.Field.Name == "productVariants" {
		namespace, _ := directStringArg(fc.Args, "namespace")
		return list(strings.TrimSuffix(fc.Field.Name, "s"), namespace)
	}
	type lookup struct{ kind, uid, namespace, name string }
	var lookups []lookup
	if fc.Field.Name == "node" || fc.Field.Name == "nodes" {
		ids, _ := directStringsArg(fc.Args, "ids")
		if fc.Field.Name == "node" {
			id, _ := directStringArg(fc.Args, "id")
			ids = []string{id}
		}
		for _, id := range ids {
			kind, uid, err := decodeGlobalID(id)
			if err == nil && (kind == "Collection" || kind == "ProductVariant") {
				lookups = append(lookups, lookup{kind: lowerCamelFirst(kind), uid: uid})
			}
		}
	} else if fc.Field.Name == "collection" || fc.Field.Name == "productVariant" {
		item := lookup{kind: fc.Field.Name}
		if id, ok := nestedStringPath(fc.Args, "by", "id"); ok && id != "" {
			kind, uid, err := decodeGlobalID(id)
			if err != nil || lowerCamelFirst(kind) != item.kind {
				return admission.NewError(admission.CodeBadUserInput, "INVALID_GLOBAL_ID", "invalid resource ID").ToGQLError()
			}
			item.uid = uid
		} else {
			item.namespace, _ = nestedStringPath(fc.Args, "by", "namespacePath", "namespace")
			item.name, _ = nestedStringPath(fc.Args, "by", "namespacePath", "name")
		}
		lookups = append(lookups, item)
	}
	for _, item := range lookups {
		store, ok := a.store.(interface {
			GetCollection(context.Context, string) (*datastore.Collection, error)
			GetCollectionByName(context.Context, string, string) (*datastore.Collection, error)
			GetProductVariant(context.Context, string) (*datastore.ProductVariant, error)
			GetProductVariantByName(context.Context, string, string) (*datastore.ProductVariant, error)
		})
		if !ok {
			return gqlerror.Errorf("authorization service unavailable")
		}
		resource := auth.ResourceContext{Kind: item.kind}
		var err error
		if item.kind == "collection" {
			var entity *datastore.Collection
			if item.uid != "" {
				entity, err = store.GetCollection(ctx, item.uid)
			} else {
				entity, err = store.GetCollectionByName(ctx, item.namespace, item.name)
			}
			if err == nil && entity != nil {
				resource.Name = entity.Name
				resource.OwnerSub = entity.EffectiveOwnerSub()
				resource.Attrs = map[string]any{"namespace": entity.Namespace}
			}
		} else {
			var entity *datastore.ProductVariant
			if item.uid != "" {
				entity, err = store.GetProductVariant(ctx, item.uid)
			} else {
				entity, err = store.GetProductVariantByName(ctx, item.namespace, item.name)
			}
			if err == nil && entity != nil {
				resource.Name = entity.Name
				resource.OwnerSub = entity.EffectiveOwnerSub()
				resource.Attrs = map[string]any{"namespace": entity.Namespace}
			}
		}
		if errors.Is(err, datastore.ErrNotFound) {
			if fc.Field.Name != "node" && fc.Field.Name != "nodes" {
				return admission.ResourceNotFound(item.kind, item.kind+" not found")
			}
			kind := "Collection"
			if item.kind == "productVariant" {
				kind = "ProductVariant"
			}
			preserveMissingNode(ctx, kind, item.uid, err)
			continue
		}
		if err != nil || resource.Name == "" {
			return gqlerror.Errorf("authorization error")
		}
		if err := authorizeCategoryAction(ctx, authz, principal, item.kind+".read", resource); err != nil {
			return err
		}
	}
	return nil
}

func isNamespaceQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && (fc.Field.Name == "namespace" || fc.Field.Name == "namespaces")
}

// Stored tiers use lowercase encoding; authorization facts use the same enum
// spelling as GraphQL and Git manifests. Unknown tiers remain missing facts.
func namespaceAuthorizationAttrs(ns *datastore.Namespace) map[string]any {
	attrs := map[string]any{}
	if ns != nil {
		switch ns.Tier {
		case datastore.NamespaceTierUser:
			attrs["tier"] = "USER"
		case datastore.NamespaceTierOrganization:
			attrs["tier"] = "ORGANIZATION"
		}
	}
	return attrs
}

// Namespace reads must be checked through every selector, including Node.
func (a *Authorize) authorizeNamespaceField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	if fc == nil {
		return nil
	}
	check := func(action string, ns *datastore.Namespace) error {
		resource := auth.ResourceContext{Kind: "namespace"}
		if ns != nil {
			resource.Name = ns.Name
			resource.OwnerSub = ns.EffectiveOwnerSub()
			resource.Attrs = namespaceAuthorizationAttrs(ns)
		}
		return authorizeCategoryAction(ctx, authz, principal, action, resource)
	}
	if fc.Object == "Query" && fc.Field.Name == "namespaces" {
		return check(auth.ActionNamespaceList, nil)
	}
	var names, ids []string
	switch fc.Object + "." + fc.Field.Name {
	case "Query.namespace":
		if id, ok := nestedStringPath(fc.Args, "by", "id"); ok && id != "" {
			ids = []string{id}
		} else {
			name, _ := nestedStringPath(fc.Args, "by", "name")
			names = []string{name}
		}
	case "Query.node":
		id, _ := directStringArg(fc.Args, "id")
		ids = []string{id}
	case "Query.nodes":
		ids, _ = directStringsArg(fc.Args, "ids")
	case "Mutation.updateNamespace":
		name, _ := nestedStringPath(fc.Args, "input", "metadata", "name")
		names = []string{name}
	default:
		return nil
	}
	action := auth.ActionNamespaceRead
	if fc.Object == "Mutation" {
		action = auth.ActionNamespaceUpdate
	}
	for _, id := range ids {
		kind, uid, err := decodeGlobalID(id)
		if err != nil || kind != "Namespace" {
			continue
		}
		if a.store == nil {
			return gqlerror.Errorf("authorization service unavailable")
		}
		ns, err := a.store.GetNamespace(ctx, uid)
		if errors.Is(err, datastore.ErrNotFound) {
			if fc.Field.Name == "namespace" {
				return namespaceNotFoundError(uid)
			}
			preserveMissingNode(ctx, "Namespace", uid, err)
			continue
		}
		if err != nil {
			return gqlerror.Errorf("authorization error")
		}
		if err := check(action, ns); err != nil {
			return err
		}
	}
	for _, name := range names {
		if a.store == nil {
			return gqlerror.Errorf("authorization service unavailable")
		}
		ns, err := a.store.GetNamespaceByName(ctx, name)
		if errors.Is(err, datastore.ErrNotFound) {
			return namespaceNotFoundError(name)
		}
		if err != nil {
			return gqlerror.Errorf("authorization error")
		}
		if err := check(action, ns); err != nil {
			return err
		}
	}
	return nil
}

func isRepositoryQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && (fc.Field.Name == "repository" || fc.Field.Name == "repositories" || fc.Field.Name == "node" || fc.Field.Name == "nodes")
}

func isProductQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && (fc.Field.Name == "product" || fc.Field.Name == "products")
}

// The parent Category has already been authorized for read. gqlgen field
// middleware does not expose its hydrated parent value, so this action check
// deliberately uses the global list scope; resource-aware Category scoping is
// enforced at the parent read boundary.
func isCategoryProductsField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Category" && fc.Field.Name == "products"
}

func isCategoryQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && (fc.Field.Name == "category" || fc.Field.Name == "categories")
}

// authorizeCategoryQueryField checks categoryTaxonomy.list for lists (filtered
// or not) and categoryTaxonomy.read for single reads, scoped to the namespace.
// A by-name read is checked against the requested name, so a denied caller
// learns nothing about whether the category exists.
func (a *Authorize) authorizeCategoryQueryField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	if fc.Field.Name == "categories" {
		namespace, _ := directStringArg(fc.Args, "namespace")
		owner := ""
		if a.store != nil && namespace != "" {
			ns, err := a.store.GetNamespaceByName(ctx, namespace)
			if err != nil && !errors.Is(err, datastore.ErrNotFound) {
				return gqlerror.Errorf("authorization error")
			}
			if ns != nil {
				owner = ns.EffectiveOwnerSub()
			}
		}
		return authorizeCategoryAction(ctx, authz, principal, "categoryTaxonomy.list", auth.ResourceContext{
			Kind: "categoryTaxonomy", OwnerSub: owner, Attrs: map[string]any{"namespace": namespace},
		})
	}
	if encodedID, ok := nestedStringPath(fc.Args, "by", "id"); ok && encodedID != "" {
		uid, err := decodeCategoryID(encodedID)
		if err != nil {
			return err
		}
		if a.store == nil {
			return gqlerror.Errorf("authorization service unavailable")
		}
		category, err := a.store.GetCategoryTaxonomy(ctx, uid)
		if errors.Is(err, datastore.ErrNotFound) {
			return admission.ResourceNotFound("Category", "category not found")
		}
		if err != nil {
			return gqlerror.Errorf("authorization error")
		}
		return authorizeCategoryAction(ctx, authz, principal, "categoryTaxonomy.read", auth.ResourceContext{
			Kind: "categoryTaxonomy", Name: category.Name, OwnerSub: category.EffectiveOwnerSub(),
			Attrs: map[string]any{"namespace": category.Namespace, "repositoryID": category.RepositoryID},
		})
	}
	namespace, _ := nestedStringPath(fc.Args, "by", "namespacePath", "namespace")
	name, _ := nestedStringPath(fc.Args, "by", "namespacePath", "name")
	if a.store == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	category, err := a.store.GetCategoryTaxonomyByName(ctx, namespace, name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return gqlerror.Errorf("authorization error")
	}
	owner := ""
	if category != nil {
		owner = category.EffectiveOwnerSub()
	}
	if authErr := authorizeCategoryAction(ctx, authz, principal, "categoryTaxonomy.read", auth.ResourceContext{
		Kind: "categoryTaxonomy", Name: name, OwnerSub: owner, Attrs: map[string]any{"namespace": namespace},
	}); authErr != nil {
		return authErr
	}
	if errors.Is(err, datastore.ErrNotFound) {
		return admission.ResourceNotFound("Category", "category not found")
	}
	return nil
}

// authorizeCategoryNodeField applies categoryTaxonomy.read to Category IDs
// fetched through node/nodes, so the Node interface is not a read bypass.
func (a *Authorize) authorizeCategoryNodeField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	if fc.Object != "Query" || (fc.Field.Name != "node" && fc.Field.Name != "nodes") {
		return nil
	}
	ids, _ := directStringsArg(fc.Args, "ids")
	if fc.Field.Name == "node" {
		id, _ := directStringArg(fc.Args, "id")
		ids = []string{id}
	}
	for _, id := range ids {
		uid, err := decodeCategoryID(id)
		if err != nil {
			continue
		}
		if a.store == nil {
			return gqlerror.Errorf("authorization service unavailable")
		}
		category, err := a.store.GetCategoryTaxonomy(ctx, uid)
		if errors.Is(err, datastore.ErrNotFound) {
			preserveMissingNode(ctx, "Category", uid, err)
			continue
		}
		if err != nil {
			return gqlerror.Errorf("authorization error")
		}
		if err := authorizeCategoryAction(ctx, authz, principal, "categoryTaxonomy.read", auth.ResourceContext{
			Kind: "categoryTaxonomy", Name: category.Name, OwnerSub: category.EffectiveOwnerSub(),
			Attrs: map[string]any{"namespace": category.Namespace, "repositoryID": category.RepositoryID},
		}); err != nil {
			return err
		}
	}
	return nil
}

func isFileQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && fc.Field.Name == "file"
}

func (a *Authorize) authorizeFileRead(ctx context.Context, principal *auth.Principal, authz auth.AuthZProvider, namespace, name string) error {
	if authz == nil || a.store == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	owner := ""
	store, ok := a.store.(interface {
		GetFileByName(context.Context, string, string) (*datastore.File, error)
	})
	if !ok {
		return gqlerror.Errorf("authorization service unavailable")
	}
	file, err := store.GetFileByName(ctx, namespace, name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return gqlerror.Errorf("authorization error")
	}
	if file != nil {
		owner = file.EffectiveOwnerSub()
	}
	return authorizeFileReadResource(ctx, principal, authz, namespace, name, owner)
}

func authorizeFileReadResource(ctx context.Context, principal *auth.Principal, authz auth.AuthZProvider, namespace, name, owner string) error {
	if authz == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	decision, err := authz.Authorize(ctx, principal, "file.read", auth.ResourceContext{
		Kind: "file", Name: name, OwnerSub: owner, Attrs: map[string]any{"namespace": namespace},
	})
	if err != nil {
		return gqlerror.Errorf("authorization error")
	}
	if decision.Outcome != auth.OutcomeAllow {
		return &gqlerror.Error{Message: "permission denied: file.read", Extensions: map[string]any{"code": "FORBIDDEN"}}
	}
	return nil
}

func (a *Authorize) namespaceOwnerFact(ctx context.Context, name string) (string, error) {
	if a.store == nil {
		return "", gqlerror.Errorf("authorization service unavailable")
	}
	ns, err := a.store.GetNamespaceByName(ctx, name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return "", gqlerror.Errorf("authorization error")
	}
	if ns == nil {
		return "", nil
	}
	return ns.EffectiveOwnerSub(), nil
}

func (a *Authorize) authorizeFileNodeField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) (authorizedCtx context.Context, resultErr error) {
	if fc.Object != "Query" || (fc.Field.Name != "node" && fc.Field.Name != "nodes") {
		return ctx, nil
	}
	ids, _ := directStringsArg(fc.Args, "ids")
	if fc.Field.Name == "node" {
		id, _ := directStringArg(fc.Args, "id")
		ids = []string{id}
	}
	files := make([]*datastore.File, len(ids))
	uids := make([]string, len(ids))
	errs := make([]error, len(ids))
	filePositions := make([]int, 0, len(ids))
	for i, id := range ids {
		if ctx.Err() != nil {
			return ctx, gqlerror.Errorf("authorization error")
		}
		kind, uid, err := decodeGlobalID(id)
		if err != nil || kind != "File" {
			continue
		}
		uids[i] = uid
		filePositions = append(filePositions, i)
	}
	if len(filePositions) == 0 {
		return ctx, nil
	}
	readCtx := ctx
	if trace.SpanFromContext(ctx).IsRecording() {
		var span trace.Span
		readCtx, span = otel.Tracer("gitstore/graphql").Start(ctx, "graphql.file_node_authorization")
		defer func() {
			if resultErr != nil {
				span.SetStatus(codes.Error, "file node authorization failed")
			}
			span.End()
		}()
	}
	if fc.Field.Name == "nodes" {
		fileStore, ok := a.store.(interface {
			GetFileSnapshots(context.Context, []string) ([]*datastore.File, error)
		})
		if !ok {
			return ctx, gqlerror.Errorf("authorization service unavailable")
		}
		fileUIDs := make([]string, len(filePositions))
		for i, position := range filePositions {
			fileUIDs[i] = uids[position]
		}
		for start := 0; start < len(fileUIDs); start += datastore.FileSnapshotBatchLimit {
			end := min(start+datastore.FileSnapshotBatchLimit, len(fileUIDs))
			snapshots, err := fileStore.GetFileSnapshots(readCtx, fileUIDs[start:end])
			if err != nil || len(snapshots) != end-start {
				return ctx, gqlerror.Errorf("authorization error")
			}
			for i, snapshot := range snapshots {
				files[filePositions[start+i]] = snapshot
			}
		}
	} else {
		fileStore, ok := a.store.(interface {
			GetFile(context.Context, string) (*datastore.File, error)
		})
		if !ok {
			return ctx, gqlerror.Errorf("authorization service unavailable")
		}
		file, err := fileStore.GetFile(readCtx, uids[0])
		if !errors.Is(err, datastore.ErrNotFound) {
			if err == nil && file == nil {
				err = errors.New("file authorization lookup returned no record")
			}
			files[0], errs[0] = file, err
		}
	}
	if ctx.Err() != nil {
		return ctx, gqlerror.Errorf("authorization error")
	}
	authorized := make(map[string]*datastore.File)
	for i, file := range files {
		if errs[i] != nil {
			return ctx, gqlerror.Errorf("authorization error")
		}
		if file == nil {
			if uids[i] != "" {
				authorized[uids[i]] = nil
			}
			continue
		}
		if file.UID != uids[i] {
			return ctx, gqlerror.Errorf("authorization error")
		}
		if err := authorizeFileReadResource(readCtx, principal, authz, file.Namespace, file.Name, file.EffectiveOwnerSub()); err != nil {
			return ctx, err
		}
		authorized[uids[i]] = file
	}
	return context.WithValue(ctx, authorizedFileNodesContextKey{}, authorized), nil
}

// authorizeStoredCategoryAction scopes the decision by the stored record, never
// by caller-supplied fields.
func (a *Authorize) authorizeStoredCategoryAction(ctx context.Context, authz auth.AuthZProvider, principal *auth.Principal, action string, load func() (*datastore.CategoryTaxonomy, error)) error {
	if a.store == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	category, err := load()
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return admission.ResourceNotFound("Category", "category not found")
		}
		return gqlerror.Errorf("authorization error")
	}
	return authorizeCategoryAction(ctx, authz, principal, action, auth.ResourceContext{
		Kind: "categoryTaxonomy", Name: category.Name, OwnerSub: category.EffectiveOwnerSub(),
		Attrs: map[string]any{"namespace": category.Namespace, "repositoryID": category.RepositoryID},
	})
}

func authorizeCategoryAction(ctx context.Context, authz auth.AuthZProvider, principal *auth.Principal, action string, resource auth.ResourceContext) error {
	if authz == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	decision, err := authz.Authorize(ctx, principal, action, resource)
	if err != nil {
		return gqlerror.Errorf("authorization error")
	}
	// The same endpoint can be read using either ordinary or management
	// capability. There is no role-name shortcut and no requirement to hold both.
	if decision.Outcome != auth.OutcomeAllow && (resource.Kind == "product" || resource.Kind == "productVariant") && (action == resource.Kind+".read" || action == resource.Kind+".list") {
		verb := strings.TrimPrefix(action, resource.Kind+".")
		decision, err = authz.Authorize(ctx, principal, resource.Kind+".management."+verb, resource)
		if err != nil {
			return gqlerror.Errorf("authorization error")
		}
	}
	if decision.Outcome != auth.OutcomeAllow {
		return deniedResourceError(action, resource)
	}
	return nil
}

func graphqlFieldRequiresAuthorization(fc *graphql.FieldContext) bool {
	if fc == nil {
		return false
	}
	if isAdditionalCatalogField(fc) || isCategoryProductsField(fc) {
		return true
	}
	switch fc.Object {
	case "Query":
		return isNamespaceQueryField(fc) || isProductQueryField(fc) || isFileQueryField(fc) || isCategoryQueryField(fc) || isRepositoryQueryField(fc)
	case "Mutation":
		switch fc.Field.Name {
		case "createProduct", "updateProduct", "deleteProduct", "completeProductDeletion", "createRepository", "deleteRepository", "deleteNamespace", "completeNamespaceDeletion", "provisionNamespaceSystemRepository", "completeRepositoryDeletion", "provisionRepositoryStorage", "updateCategoryStatus", "updateProductStatus", "updateNamespaceStatus", "createCategory", "updateCategory", "deleteCategory", "completeCategoryDeletion", "updateResourceStatus", "issueServiceAccountToken", "createServiceAccount", "rotateServiceAccountKey", "deleteServiceAccount":
			return true
		case "createNamespace", "updateNamespace", "transferNamespaceOwner", "updateRepository", "updateRepositoryStatus":
			return true
		}
	case "Subscription":
		return fc.Field.Name == "watchFiles" || fc.Field.Name == "watchNamespaces" || fc.Field.Name == "watchRepositories" || fc.Field.Name == "watchProducts" || fc.Field.Name == "watchCategories" || fc.Field.Name == "watchResources"
	}
	return false
}

func (a *Authorize) authorizeStoredProductAction(ctx context.Context, authz auth.AuthZProvider, principal *auth.Principal, action, namespace, name, uid string) error {
	if a.store == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	var (
		product *datastore.Product
		err     error
	)
	if uid != "" {
		product, err = a.store.GetProduct(ctx, uid)
	} else {
		product, err = a.store.GetProductByName(ctx, namespace, name)
	}
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return admission.ResourceNotFound("Product", "product not found")
		}
		return gqlerror.Errorf("authorization error")
	}
	return authorizeProductAction(ctx, authz, principal, action, product.Name, product.Namespace, product.EffectiveOwnerSub())
}

func authorizeProductAction(ctx context.Context, authz auth.AuthZProvider, principal *auth.Principal, action, name, namespace, ownerSub string) error {
	return authorizeCategoryAction(ctx, authz, principal, action, auth.ResourceContext{
		Kind: "product", Name: name, OwnerSub: ownerSub,
		Attrs: map[string]any{"namespace": namespace},
	})
}

func (a *Authorize) authorizeProductQueryField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	if fc == nil {
		return nil
	}
	if fc.Field.Name == "products" {
		namespace, _ := directStringArg(fc.Args, "namespace")
		// A list selector names a namespace rather than a Product, so preserve
		// the namespace owner's subject in the resource context.  Without this
		// lookup an owner-aware provider cannot distinguish a caller listing its
		// own namespace from a caller probing somebody else's Product names.
		owner := ""
		if a.store != nil && namespace != "" {
			ns, err := a.store.GetNamespaceByName(ctx, namespace)
			if err != nil && !errors.Is(err, datastore.ErrNotFound) {
				return gqlerror.Errorf("authorization error")
			}
			if ns != nil {
				owner = ns.EffectiveOwnerSub()
			}
		}
		return authorizeProductAction(ctx, authz, principal, "product.list", "", namespace, owner)
	}
	if a.store == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}
	var (
		product *datastore.Product
		err     error
	)
	if encodedID, ok := nestedStringPath(fc.Args, "by", "id"); ok && encodedID != "" {
		rawID, decodeErr := decodeGlobalIDAs("Product", encodedID)
		if decodeErr != nil {
			return decodeErr
		}
		product, err = a.store.GetProduct(ctx, rawID)
	} else {
		namespace, namespaceOK := nestedStringPath(fc.Args, "by", "namespacePath", "namespace")
		name, nameOK := nestedStringPath(fc.Args, "by", "namespacePath", "name")
		if !namespaceOK || !nameOK {
			return gqlerror.Errorf("invalid product selector")
		}
		product, err = a.store.GetProductByName(ctx, namespace, name)
	}
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return admission.ResourceNotFound("Product", "product not found")
		}
		return gqlerror.Errorf("authorization error")
	}
	return authorizeProductAction(ctx, authz, principal, "product.read", product.Name, product.Namespace, product.EffectiveOwnerSub())
}

// authorizeProductNodeField adds Product checks to mixed Node/Nodes queries.
// Repository checks run first, so a mixed batch must pass every resource's
// policy before any resolver receives the request.
func (a *Authorize) authorizeProductNodeField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) error {
	if fc == nil || fc.Object != "Query" || (fc.Field.Name != "node" && fc.Field.Name != "nodes") || a.store == nil {
		return nil
	}
	ids := make([]string, 0, 1)
	if fc.Field.Name == "node" {
		if id, ok := directStringArg(fc.Args, "id"); ok {
			ids = append(ids, id)
		}
	} else if values, ok := directStringsArg(fc.Args, "ids"); ok {
		ids = append(ids, values...)
	}
	for _, encodedID := range ids {
		kind, rawID, err := decodeGlobalID(encodedID)
		if err != nil || kind != "Product" {
			continue
		}
		product, err := a.store.GetProduct(ctx, rawID)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				preserveMissingNode(ctx, "Product", rawID, err)
				continue
			}
			return gqlerror.Errorf("authorization error")
		}
		if err := authorizeProductAction(ctx, authz, principal, "product.read", product.Name, product.Namespace, product.EffectiveOwnerSub()); err != nil {
			return err
		}
	}
	return nil
}

// repositoryNotFoundError mirrors the resolver-layer convention (see
// queryResolver.Repository in graph/resolver/repository.resolvers.go) so
// that GraphQL clients — including gitstore-controller-manager's
// isNotFoundError probe used by EnsureSystemRepository — see the same
// "repository not found"/NOT_FOUND shape regardless of whether the
// not-found outcome is detected here (at the authorization layer, before
// the resolver runs) or inside the resolver itself. Without this, a raw
// datastore.ErrNotFound ("datastore: not found") leaked straight past
// authorization, which callers could not recognize as a normal not-found
// response.
func repositoryNotFoundError() *gqlerror.Error {
	return admission.ResourceNotFound("Repository", "repository not found")
}

// namespaceNotFoundError mirrors resolver.Service.GetNamespaceByName's
// not-found shape (graph/resolver/service.go) for the same reason as
// repositoryNotFoundError above.
func namespaceNotFoundError(name string) *gqlerror.Error {
	return admission.ResourceNotFound("Namespace", fmt.Sprintf("namespace %q not found", name))
}

// authFieldNotFoundError marks a datastore.ErrNotFound observed while
// authorizing a GraphQL field, carrying the resolver-equivalent GraphQL
// error shape to render once authorization finishes. Unwrap keeps
// errors.Is(err, datastore.ErrNotFound) working for existing in-function
// callers (e.g. the Query.nodes loop below) that need to distinguish
// not-found from other failures before authorizeRepositoryField returns.
type authFieldNotFoundError struct {
	cause  error
	render func() *gqlerror.Error
}

func (e *authFieldNotFoundError) Error() string { return e.cause.Error() }
func (e *authFieldNotFoundError) Unwrap() error { return e.cause }

// asAuthorizeFieldError normalizes a datastore.ErrNotFound into an
// authFieldNotFoundError carrying the resolver-equivalent gqlerror shape;
// any other error is returned unchanged.
func asAuthorizeFieldError(err error, notFound func() *gqlerror.Error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, datastore.ErrNotFound) {
		return &authFieldNotFoundError{cause: err, render: notFound}
	}
	return err
}

func (a *Authorize) authorizeRepositoryField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal) (context.Context, error) {
	if fc == nil || a.store == nil {
		return ctx, nil
	}
	var operation string
	var namespaces []*datastore.Namespace
	var repo *datastore.Repository

	loadRepository := func(encodedID string) error {
		rawID, err := decodeGlobalIDAs("Repository", encodedID)
		if err != nil {
			return err
		}
		repo, err = a.store.GetRepository(ctx, rawID)
		if err != nil {
			return asAuthorizeFieldError(err, repositoryNotFoundError)
		}
		ns, err := a.store.GetNamespaceByName(ctx, repo.Namespace)
		if err != nil {
			return asAuthorizeFieldError(err, func() *gqlerror.Error { return namespaceNotFoundError(repo.Namespace) })
		}
		namespaces = append(namespaces, ns)
		return nil
	}

	switch fc.Object + "." + fc.Field.Name {
	case "Mutation.createRepository":
		name, nameOK := nestedStringPath(fc.Args, "input", "metadata", "name")
		namespace, namespaceOK := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		if !nameOK || !namespaceOK {
			return ctx, nil
		}
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return ctx, asAuthorizeFieldError(err, func() *gqlerror.Error { return namespaceNotFoundError(namespace) })
		}
		operation, namespaces, repo = "create", []*datastore.Namespace{ns}, &datastore.Repository{Name: name}
	case "Mutation.updateRepository":
		name, nameOK := nestedStringPath(fc.Args, "input", "metadata", "name")
		namespace, namespaceOK := nestedStringPath(fc.Args, "input", "metadata", "namespace")
		if !nameOK || !namespaceOK {
			return ctx, nil
		}
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return ctx, asAuthorizeFieldError(err, func() *gqlerror.Error { return namespaceNotFoundError(namespace) })
		}
		mapping, err := a.store.LookupRepository(ctx, ns.Name, name)
		if err != nil {
			return ctx, asAuthorizeFieldError(err, repositoryNotFoundError)
		}
		repo, err = a.store.GetRepository(ctx, mapping.RepositoryID)
		if err != nil {
			return ctx, asAuthorizeFieldError(err, repositoryNotFoundError)
		}
		operation, namespaces = "update", []*datastore.Namespace{ns}
	case "Mutation.deleteRepository":
		encodedID, ok := nestedStringArg(fc.Args, "input", "id")
		if !ok {
			return ctx, nil
		}
		if err := loadRepository(encodedID); err != nil {
			return ctx, err
		}
		operation = "delete"
	case "Query.repository":
		if encodedID, ok := nestedStringPath(fc.Args, "by", "id"); ok && encodedID != "" {
			if err := loadRepository(encodedID); err != nil {
				return ctx, err
			}
		} else {
			namespace, namespaceOK := nestedStringPath(fc.Args, "by", "namespacePath", "namespace")
			name, nameOK := nestedStringPath(fc.Args, "by", "namespacePath", "name")
			if !namespaceOK || !nameOK {
				return ctx, nil
			}
			ns, err := a.store.GetNamespaceByName(ctx, namespace)
			if err != nil {
				return ctx, asAuthorizeFieldError(err, func() *gqlerror.Error { return namespaceNotFoundError(namespace) })
			}
			mapping, err := a.store.LookupRepository(ctx, ns.Name, name)
			if err != nil {
				return ctx, asAuthorizeFieldError(err, repositoryNotFoundError)
			}
			repo, err = a.store.GetRepository(ctx, mapping.RepositoryID)
			if err != nil {
				return ctx, asAuthorizeFieldError(err, repositoryNotFoundError)
			}
			namespaces = []*datastore.Namespace{ns}
		}
		operation = "read"
	case "Query.repositories":
		namespace, ok := directStringArg(fc.Args, "namespace")
		if !ok {
			return ctx, nil
		}
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return ctx, asAuthorizeFieldError(err, func() *gqlerror.Error { return namespaceNotFoundError(namespace) })
		}
		operation, namespaces, repo = "list", []*datastore.Namespace{ns}, &datastore.Repository{}
	case "Query.node":
		encodedID, ok := directStringArg(fc.Args, "id")
		if !ok {
			return ctx, nil
		}
		kind, _, err := decodeGlobalID(encodedID)
		if err != nil {
			return ctx, admission.NewError(admission.CodeBadUserInput, "INVALID_GLOBAL_ID", "invalid global ID").ToGQLError()
		}
		if kind != "Repository" {
			return ctx, nil
		}
		if err := loadRepository(encodedID); err != nil {
			return ctx, err
		}
		operation = "read"
	case "Query.nodes":
		encodedIDs, ok := directStringsArg(fc.Args, "ids")
		if !ok {
			return ctx, nil
		}
		for _, encodedID := range encodedIDs {
			kind, _, err := decodeGlobalID(encodedID)
			if err != nil || kind != "Repository" {
				// Match the resolver's node semantics for malformed and non-repository
				// IDs. Only repository objects need repository authorization here.
				continue
			}
			if err := loadRepository(encodedID); err != nil {
				if errors.Is(err, datastore.ErrNotFound) {
					_, uid, _ := decodeGlobalID(encodedID)
					preserveMissingNode(ctx, "Repository", uid, err)
					continue
				}
				return ctx, err
			}
			if _, err := a.authorizeRepositoryTenant(ctx, principal, "read", repo, namespaces[len(namespaces)-1]); err != nil {
				return ctx, err
			}
		}
		return ctx, nil
	default:
		return ctx, nil
	}

	if repo == nil || len(namespaces) == 0 {
		return ctx, nil
	}
	if _, err := a.authorizeRepositoryTenant(ctx, principal, operation, repo, namespaces...); err != nil {
		return ctx, err
	}
	if fc.Object == "Query" && fc.Field.Name == "repository" {
		ctx = context.WithValue(ctx, authorizedRepositoryReadContextKey{}, &authorizedRepositoryRead{
			repository: repo,
			namespace:  namespaces[0],
			field:      fc,
		})
	}
	return ctx, nil
}

func decodeGlobalIDAs(expectedKind, encoded string) (string, error) {
	kind, rawID, err := decodeGlobalID(encoded)
	if err != nil {
		return "", admission.NewError(admission.CodeBadUserInput, "INVALID_GLOBAL_ID", fmt.Sprintf("invalid global ID: %v", err)).ToGQLError()
	}
	if kind != expectedKind {
		return "", admission.NewError(admission.CodeBadUserInput, "INVALID_GLOBAL_ID", fmt.Sprintf("invalid global ID kind: expected %s, got %s", expectedKind, kind)).ToGQLError()
	}
	return rawID, nil
}

func decodeGlobalID(encoded string) (kind, rawID string, err error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(string(decoded))
	if err != nil || u.Scheme != "gid" || u.Host != "GitStore" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("invalid global ID")
	}
	parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("global ID must include kind and raw ID")
	}
	kind, err = url.PathUnescape(parts[0])
	if err != nil {
		return "", "", err
	}
	rawID, err = url.PathUnescape(parts[1])
	if err != nil || kind == "" || rawID == "" {
		return "", "", fmt.Errorf("invalid global ID")
	}
	return kind, rawID, nil
}

func (a *Authorize) authorizeSubscription(
	ctx context.Context,
	next graphql.Resolver,
	fc *graphql.FieldContext,
	principal *auth.Principal,
	authz auth.AuthZProvider,
	onAllowed func(),
) (any, error) {
	var kind string
	switch fc.Field.Name {
	case "watchFiles":
		kind = "File"
	case "watchNamespaces":
		kind = "Namespace"
	case "watchRepositories":
		kind = "Repository"
	case "watchProducts":
		kind = "Product"
	case "watchCategories":
		kind = "CategoryTaxonomy"
	case "watchResources":
		kind, _ = directStringArg(fc.Args, "kind")
		if kind != "File" && kind != "Namespace" && kind != "Repository" && kind != "Product" && kind != "CategoryTaxonomy" {
			// No other kind has a durable watch source; the resolver
			// rejects it with UNSUPPORTED_KIND without opening a stream.
			return next(ctx)
		}
	default:
		return next(ctx)
	}
	if authz == nil {
		return nil, gqlerror.Errorf("authorization service unavailable")
	}
	// Every kind, including CategoryTaxonomy, uses the ADR 0010 slug
	// grammar: lowerCamel(kind).watch.
	action := lowerCamelFirst(kind) + ".watch"
	definition, found := auth.LookupAction(action)
	if !found || definition.Reserved {
		return nil, &gqlerror.Error{Message: "unsupported watch kind", Extensions: map[string]any{"code": "UNSUPPORTED_KIND"}}
	}
	resource := auth.ResourceContext{Kind: definition.Kind, Attrs: map[string]any{}}
	if kind == "File" || kind == "Repository" || kind == "Product" || kind == "CategoryTaxonomy" {
		namespace, _ := directStringArg(fc.Args, "namespace")
		resource.Attrs["namespace"] = namespace
	}
	decision, err := authz.Authorize(ctx, principal, action, auth.ResourceContext{
		Kind:  resource.Kind,
		Attrs: resource.Attrs,
	})
	if err != nil {
		return nil, gqlerror.Errorf("authorization error")
	}
	if decision.Outcome != auth.OutcomeAllow {
		return nil, &gqlerror.Error{
			Message:    fmt.Sprintf("permission denied: %s", decision.Reason),
			Extensions: map[string]any{"code": "FORBIDDEN"},
		}
	}
	if onAllowed != nil {
		onAllowed()
	}
	return next(ctx)
}

func decodeCategoryID(encoded string) (string, error) {
	return decodeGlobalIDAs("Category", encoded)
}

// lowerCamelFirst lowercases the first rune of s, matching GraphQL's
// convention for deriving an authorization action string from a
// PascalCase resource kind (e.g. "CategoryTaxonomy" -> "categoryTaxonomy",
// research.md R5's generic-path extension).
func lowerCamelFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// nestedStringArg reads a string field off a gqlgen-generated input struct
// (e.g. args["input"].tier). gqlgen always unmarshals GraphQL input objects
// into their generated struct type before FieldContext.Args is populated, so
// only the struct-based representation is handled here.
func nestedStringArg(args map[string]any, parent, key string) (string, bool) {
	return nestedStringPath(args, parent, key)
}

func directStringArg(args map[string]any, key string) (string, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", false
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.String {
		return "", false
	}
	return rv.String(), true
}

func directStringsArg(args map[string]any, key string) ([]string, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, false
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	values := make([]string, 0, rv.Len())
	for i := range rv.Len() {
		item := rv.Index(i)
		for item.Kind() == reflect.Interface || item.Kind() == reflect.Pointer {
			if item.IsNil() {
				return nil, false
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, false
		}
		values = append(values, item.String())
	}
	return values, true
}

func nestedStringPath(args map[string]any, path ...string) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	current, ok := args[path[0]]
	if !ok || current == nil {
		return "", false
	}
	rv := reflect.ValueOf(current)
	for _, key := range path[1:] {
		for rv.Kind() == reflect.Pointer {
			if rv.IsNil() {
				return "", false
			}
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			return "", false
		}
		rv = rv.FieldByNameFunc(func(name string) bool { return strings.EqualFold(name, key) })
		if !rv.IsValid() {
			return "", false
		}
	}
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.String {
		return "", false
	}
	return rv.String(), true
}

// requiresAuthenticatedPrincipal reports whether the operation executes any
// root mutation field other than "login", "refreshToken", or "__typename".
// It delegates to
// graphql.CollectFields — the same field-collection gqlgen itself uses to
// decide what will actually run — so fragment spreads, inline fragments, and
// @skip/@include directives are honored instead of re-derived by hand.
func requiresAuthenticatedPrincipal(opCtx *graphql.OperationContext) bool {
	if opCtx == nil || opCtx.Operation == nil || opCtx.Operation.Operation != ast.Mutation {
		return false
	}
	for _, field := range graphql.CollectFields(opCtx, opCtx.Operation.SelectionSet, []string{"Mutation"}) {
		if field.Name != "login" && field.Name != "refreshToken" && field.Name != "__typename" {
			return true
		}
	}
	return false
}
