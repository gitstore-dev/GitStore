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
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

type remoteAddrContextKey struct{}
type authorizedNamespaceDeleteContextKey struct{}
type authorizationLedgerContextKey struct{}

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
	if fc.Object != "Mutation" && fc.Object != "Subscription" && !isRepositoryQueryField(fc) {
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
	if err := a.authorizeRepositoryField(ctx, fc, principal); err != nil {
		return nil, err
	}

	switch fc.Field.Name {
	case "createNamespace":
		tier, ok := nestedStringPath(fc.Args, "input", "spec", "tier")
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
			OwnerSub: ns.CreationActor,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
			return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
		}
		ctx = context.WithValue(ctx, authorizedNamespaceDeleteContextKey{}, ns)
	case "completeNamespaceDeletion":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		identifier, _ := nestedStringArg(fc.Args, "input", "identifier")
		decision, err := authz.Authorize(ctx, principal, "namespace.status.write", auth.ResourceContext{
			Kind: "namespace",
			Name: identifier,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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
		decision, err := authz.Authorize(ctx, principal, "category.status.write", auth.ResourceContext{
			Kind: "categoryTaxonomy",
			Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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
		if decision.Outcome == auth.OutcomeDeny {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("permission denied: %s", decision.Reason), Extensions: map[string]any{"code": "FORBIDDEN"}}
		}
	case "deleteCategory":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		encodedID, _ := nestedStringArg(fc.Args, "input", "id")
		uid, err := decodeCategoryID(encodedID)
		if err != nil {
			return nil, gqlerror.Errorf("invalid category ID")
		}
		category, err := a.store.GetCategoryTaxonomy(ctx, uid)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				return nil, gqlerror.Errorf("category not found")
			}
			return nil, gqlerror.Errorf("authorization error")
		}
		decision, err := authz.Authorize(ctx, principal, "category.delete", auth.ResourceContext{
			Kind: "categoryTaxonomy", Name: category.Name, OwnerSub: category.CreationActor,
			Attrs: map[string]any{"namespace": category.Namespace, "repositoryID": category.RepositoryID},
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
			return nil, &gqlerror.Error{Message: fmt.Sprintf("permission denied: %s", decision.Reason), Extensions: map[string]any{"code": "FORBIDDEN"}}
		}
	case "updateResourceStatus":
		if authz == nil {
			return nil, gqlerror.Errorf("authorization service unavailable")
		}
		kind, _ := nestedStringArg(fc.Args, "input", "kind")
		name, _ := nestedStringArg(fc.Args, "input", "name")
		action := lowerCamelFirst(kind) + ".status.write"
		decision, err := authz.Authorize(ctx, principal, action, auth.ResourceContext{
			Kind: kind,
			Name: name,
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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
		expectedSubject := datastore.ServiceAccountSubject(namespace, name)
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
		decision, err := authz.Authorize(ctx, principal, "serviceaccount.token.issue", auth.ResourceContext{
			Kind: "serviceAccount",
			Name: datastore.ServiceAccountSubject(namespace, name),
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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
		decision, err := authz.Authorize(ctx, principal, "serviceaccount.create", auth.ResourceContext{
			Kind: "serviceAccount",
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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
		decision, err := authz.Authorize(ctx, principal, "serviceaccount.key.rotate", auth.ResourceContext{
			Kind: "serviceAccount",
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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
		decision, err := authz.Authorize(ctx, principal, "serviceaccount.delete", auth.ResourceContext{
			Kind: "serviceAccount",
		})
		if err != nil {
			return nil, gqlerror.Errorf("authorization error")
		}
		if decision.Outcome == auth.OutcomeDeny {
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

func isRepositoryQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && (fc.Field.Name == "repository" || fc.Field.Name == "repositories" || fc.Field.Name == "node")
}

func graphqlFieldRequiresAuthorization(fc *graphql.FieldContext) bool {
	if fc == nil {
		return false
	}
	switch fc.Object {
	case "Query":
		return fc.Field.Name == "repository" || fc.Field.Name == "repositories" || fc.Field.Name == "node"
	case "Mutation":
		switch fc.Field.Name {
		case "createRepository", "renameRepository", "transferRepository", "deleteRepository", "deleteNamespace", "completeNamespaceDeletion", "updateCategoryStatus", "updateProductStatus", "deleteCategory", "updateResourceStatus", "issueServiceAccountToken", "createServiceAccount", "rotateServiceAccountKey", "deleteServiceAccount":
			return true
		case "createNamespace":
			tier, ok := nestedStringPath(fc.Args, "input", "spec", "tier")
			return ok && tier == "ORGANIZATION"
		}
	case "Subscription":
		return fc.Field.Name == "watchFiles" || fc.Field.Name == "watchNamespaces" || fc.Field.Name == "watchResources"
	}
	return false
}

func (a *Authorize) authorizeRepositoryField(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal) error {
	if fc == nil || a.store == nil {
		return nil
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
			return err
		}
		ns, err := a.store.GetNamespaceByName(ctx, repo.Namespace)
		if err != nil {
			return err
		}
		namespaces = append(namespaces, ns)
		return nil
	}

	switch fc.Object + "." + fc.Field.Name {
	case "Mutation.createRepository":
		name, nameOK := nestedStringArg(fc.Args, "input", "name")
		namespace, namespaceOK := nestedStringArg(fc.Args, "input", "namespace")
		if !nameOK || !namespaceOK {
			return nil
		}
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return err
		}
		operation, namespaces, repo = "create", []*datastore.Namespace{ns}, &datastore.Repository{Name: name}
	case "Mutation.renameRepository", "Mutation.deleteRepository":
		encodedID, ok := nestedStringArg(fc.Args, "input", "repositoryID")
		if !ok {
			return nil
		}
		if err := loadRepository(encodedID); err != nil {
			return err
		}
		if fc.Field.Name == "renameRepository" {
			operation = "rename"
		} else {
			operation = "delete"
		}
	case "Mutation.transferRepository":
		encodedRepositoryID, repositoryOK := nestedStringArg(fc.Args, "input", "repositoryID")
		encodedNamespaceID, namespaceOK := nestedStringArg(fc.Args, "input", "targetNamespaceID")
		if !repositoryOK || !namespaceOK {
			return nil
		}
		if err := loadRepository(encodedRepositoryID); err != nil {
			return err
		}
		targetID, err := decodeGlobalIDAs("Namespace", encodedNamespaceID)
		if err != nil {
			return err
		}
		target, err := a.store.GetNamespace(ctx, targetID)
		if err != nil {
			return err
		}
		operation, namespaces = "transfer", append(namespaces, target)
	case "Query.repository":
		if encodedID, ok := nestedStringPath(fc.Args, "by", "id"); ok && encodedID != "" {
			if err := loadRepository(encodedID); err != nil {
				return err
			}
		} else {
			namespace, namespaceOK := nestedStringPath(fc.Args, "by", "namespacePath", "namespace")
			name, nameOK := nestedStringPath(fc.Args, "by", "namespacePath", "name")
			if !namespaceOK || !nameOK {
				return nil
			}
			ns, err := a.store.GetNamespaceByName(ctx, namespace)
			if err != nil {
				return err
			}
			mapping, err := a.store.LookupRepository(ctx, ns.Name, name)
			if err != nil {
				return err
			}
			repo, err = a.store.GetRepository(ctx, mapping.RepositoryID)
			if err != nil {
				return err
			}
			namespaces = []*datastore.Namespace{ns}
		}
		operation = "read"
	case "Query.repositories":
		namespace, ok := directStringArg(fc.Args, "namespace")
		if !ok {
			return nil
		}
		ns, err := a.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return err
		}
		operation, namespaces, repo = "read", []*datastore.Namespace{ns}, &datastore.Repository{Name: namespace}
	case "Query.node":
		encodedID, ok := directStringArg(fc.Args, "id")
		if !ok {
			return nil
		}
		kind, _, err := decodeGlobalID(encodedID)
		if err != nil || kind != "Repository" {
			return err
		}
		if err := loadRepository(encodedID); err != nil {
			return err
		}
		operation = "read"
	default:
		return nil
	}

	if operation == "" || repo == nil || len(namespaces) == 0 {
		return nil
	}
	if _, err := a.authorizeRepositoryTenant(ctx, principal, operation, repo, namespaces...); err != nil {
		return gqlerror.Errorf("%v", err)
	}
	return nil
}

func decodeGlobalIDAs(expectedKind, encoded string) (string, error) {
	kind, rawID, err := decodeGlobalID(encoded)
	if err != nil {
		return "", gqlerror.Errorf("invalid global ID: %v", err)
	}
	if kind != expectedKind {
		return "", gqlerror.Errorf("invalid global ID kind: expected %s, got %s", expectedKind, kind)
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
	case "watchResources":
		kind, _ = directStringArg(fc.Args, "kind")
		if kind != "File" && kind != "Namespace" {
			return next(ctx)
		}
	default:
		return next(ctx)
	}
	if authz == nil {
		return nil, gqlerror.Errorf("authorization service unavailable")
	}
	action := lowerCamelFirst(kind) + ".watch"
	resource := auth.ResourceContext{Kind: kind, Attrs: map[string]any{}}
	if kind == "File" {
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
	if decision.Outcome == auth.OutcomeDeny {
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
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(string(decoded))
	if err != nil || u.Scheme != "gid" || u.Host != "GitStore" {
		return "", fmt.Errorf("invalid global ID")
	}
	parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 2)
	if len(parts) != 2 || parts[0] != "Category" {
		return "", fmt.Errorf("invalid category global ID")
	}
	uid, err := url.PathUnescape(parts[1])
	if err != nil || uid == "" {
		return "", fmt.Errorf("invalid category UID")
	}
	return uid, nil
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

func (a *Authorize) namespaceDeleteAction(ctx context.Context, name string, principal *auth.Principal) (ns *datastore.Namespace, action string, err error) {
	if a.store == nil {
		return nil, "", fmt.Errorf("authorization store is not configured")
	}
	ns, err = a.store.GetNamespaceByName(ctx, name)
	if err != nil {
		return nil, "", err
	}
	if ns.CreationActor == principal.Subject {
		return ns, "namespace.delete.own", nil
	}
	return ns, "namespace.delete.any", nil
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
