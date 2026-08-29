// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// authorizeRepositoryTenant checks the caller's access to every tenant
// participating in a repository operation. An operation within tenants owned by
// the caller uses the .own action; cross-tenant operations require .any.
func (r *Resolver) authorizeRepositoryTenant(
	ctx context.Context,
	operation string,
	repositoryName string,
	namespaces ...*datastore.Namespace,
) error {
	if r.registry == nil {
		return nil
	}
	authz := r.registry.AuthZ()
	if authz == nil {
		return gqlerror.Errorf("authorization service unavailable")
	}

	principal := auth.PrincipalFromContext(ctx)
	if principal == nil {
		principal = auth.Anonymous()
	}

	scope := "own"
	attrs := make(map[string]any, len(namespaces))
	ownerSub := ""
	for i, namespace := range namespaces {
		if namespace == nil {
			return gqlerror.Errorf("authorization error")
		}
		if i == 0 {
			ownerSub = namespace.CreationActor
			attrs["namespace"] = namespace.Name
		} else {
			attrs["targetNamespace"] = namespace.Name
			attrs["targetOwnerSub"] = namespace.CreationActor
		}
		if namespace.CreationActor == "" || namespace.CreationActor != principal.Subject {
			scope = "any"
		}
	}

	action := "repository." + operation + "." + scope
	decision, err := authz.Authorize(ctx, principal, action, auth.ResourceContext{
		Kind:     "repository",
		Name:     repositoryName,
		OwnerSub: ownerSub,
		Attrs:    attrs,
	})
	if err != nil {
		r.logger.Error("repository authorization failed",
			zap.String("action", action),
			zap.String("repository", repositoryName),
			zap.Error(err),
		)
		return gqlerror.Errorf("authorization error")
	}
	if decision.Outcome != auth.OutcomeAllow {
		return gqlerror.Errorf("permission denied: %s", decision.Reason)
	}
	return nil
}
