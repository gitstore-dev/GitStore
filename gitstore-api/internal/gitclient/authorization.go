// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"errors"
	"fmt"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// ErrAuthorizationDenied wraps every rejection RequestAuthorization makes on
// its own defense-in-depth invariants (e.g. anonymous principals are never
// approved for a write action, regardless of what the configured AuthZ
// provider decided upstream). Callers use errors.Is against this sentinel to
// distinguish an authorization denial — which should surface as 401 so Git
// can prompt for credentials — from a genuine transport/service failure.
var ErrAuthorizationDenied = errors.New("git service request authorization denied")

type pinnedBackingKey struct{}

type pinnedBackingAuthorization struct {
	repositoryID string
	authorize    func(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error)
}

// ContextWithPinnedBackingAuthorization retains a validation-start evaluator
// for one repository's internal content write, never an external Git request.
func ContextWithPinnedBackingAuthorization(ctx context.Context, repositoryID string,
	authorize func(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error)) context.Context {
	return context.WithValue(ctx, pinnedBackingKey{}, pinnedBackingAuthorization{repositoryID, authorize})
}

// authorizeRequest evaluates the current or explicitly captured provider before
// constructing a signed RPC actor snapshot. Internal work stays policy-controlled;
// an absent principal or authorizer never becomes an implicit system identity.
func (c *Client) authorizeRequest(ctx context.Context, action, repositoryID string) (*gitv1.RequestAuthorization, error) {
	envelope, err := RequestAuthorization(ctx, action, repositoryID)
	if err != nil {
		return nil, err
	}
	if c.Registry == nil {
		return nil, fmt.Errorf("%w: authorizer is required", ErrAuthorizationDenied)
	}
	provider := c.Registry.AuthZ()
	if provider == nil {
		return nil, fmt.Errorf("%w: authorizer is required", ErrAuthorizationDenied)
	}
	principal := auth.PrincipalFromContext(ctx)
	resource := auth.ResourceContext{Kind: "repository", Attrs: map[string]any{}}
	if c.Store != nil {
		repository, lookupErr := c.Store.GetRepository(ctx, repositoryID)
		if lookupErr != nil && !(errors.Is(lookupErr, datastore.ErrNotFound) && principal.Subject == "system:api") {
			return nil, fmt.Errorf("git authorization resource lookup: %w", lookupErr)
		}
		if repository != nil {
			resource.Name, resource.OwnerSub = repository.Name, repository.EffectiveOwnerSub()
			resource.Attrs["namespace"] = repository.Namespace
			if action == auth.ActionRepositoryDelete && repository.DeletionTimestamp != nil {
				if namespaces, ok := c.Store.(interface {
					GetNamespaceByName(context.Context, string) (*datastore.Namespace, error)
				}); ok {
					parent, lookupErr := namespaces.GetNamespaceByName(ctx, repository.Namespace)
					if lookupErr != nil {
						return nil, fmt.Errorf("git authorization namespace lookup: %w", lookupErr)
					}
					if parent != nil && parent.DeletionTimestamp != nil && datastore.IsNamespaceSystemRepository(parent, repository) {
						resource.Attrs["namespaceCleanup"] = true
					}
				}
			}
			if visibility, present := auth.VisibilityFromSpec(repository.Spec); present {
				resource.Attrs["visibility"] = visibility
			}
		} else if principal.Subject != "system:api" {
			return nil, fmt.Errorf("git authorization resource is unavailable")
		}
	} else if principal.Subject != "system:api" {
		return nil, fmt.Errorf("git authorization resource lookup is unavailable")
	}
	evaluate := provider.Authorize
	if pinned, ok := ctx.Value(pinnedBackingKey{}).(pinnedBackingAuthorization); ok &&
		principal.Subject == "system:api" && principal.AuthMethod == "internal" &&
		action == auth.ActionRepositoryContentsWrite && repositoryID == pinned.repositoryID {
		if pinned.authorize == nil {
			return nil, fmt.Errorf("%w: captured authorizer is required", ErrAuthorizationDenied)
		}
		evaluate = pinned.authorize
	}
	decision, err := evaluate(ctx, principal, action, resource)
	if err != nil {
		return nil, fmt.Errorf("git authorization evaluation: %w", err)
	}
	if decision.Outcome != auth.OutcomeAllow {
		return nil, fmt.Errorf("%w: policy denied operation", ErrAuthorizationDenied)
	}
	return envelope, nil
}

// RequestAuthorization builds the non-secret authorization envelope carried by
// GitService RPCs. Authentication credentials remain exclusively in gRPC
// metadata; this message is an auditable snapshot of the API-approved actor,
// action, and repository scope.
func RequestAuthorization(ctx context.Context, action, repositoryID string) (*gitv1.RequestAuthorization, error) {
	principal := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(principal) || principal.AuthMethod == "serviceaccount-assertion" {
		return nil, fmt.Errorf("%w: an explicit actor is required", ErrAuthorizationDenied)
	}
	definition, registered := auth.LookupAction(action)
	if !registered || definition.Kind != "repository" || (action != auth.ActionRepositoryContentsRead && action != auth.ActionRepositoryContentsWrite && action != auth.ActionRepositoryCreate && action != auth.ActionRepositoryDelete) {
		return nil, fmt.Errorf("%w: unregistered repository action", ErrAuthorizationDenied)
	}
	approvedAnonymous := principal.AuthMethod == "none" && auth.AuthorizedAnonymousFromContext(ctx)
	if principal.Subject == "" || principal.AuthMethod == "" || (principal.AuthMethod == "none" && !approvedAnonymous) {
		return nil, fmt.Errorf("%w: requires an authenticated principal", ErrAuthorizationDenied)
	}
	if approvedAnonymous && action != "repository.contents.read" {
		return nil, fmt.Errorf("%w: permits anonymous actors only for repository.contents.read", ErrAuthorizationDenied)
	}
	if action == "" || repositoryID == "" {
		return nil, fmt.Errorf("git service request authorization requires action and repository ID")
	}
	return &gitv1.RequestAuthorization{
		Actor: &gitv1.AuthContext{
			Subject: principal.Subject, Issuer: principal.Issuer, AuthMethod: principal.AuthMethod,
			Groups: principal.Groups, Scopes: principal.Scopes, ServiceAccountUid: principal.ServiceAccountUID,
		},
		Action:       action,
		ResourceKind: definition.Kind,
		RepositoryId: repositoryID,
	}, nil
}
