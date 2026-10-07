// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"errors"
	"fmt"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

// ErrAuthorizationDenied wraps every rejection RequestAuthorization makes on
// its own defense-in-depth invariants (e.g. anonymous principals are never
// approved for a write action, regardless of what the configured AuthZ
// provider decided upstream). Callers use errors.Is against this sentinel to
// distinguish an authorization denial — which should surface as 401 so Git
// can prompt for credentials — from a genuine transport/service failure.
var ErrAuthorizationDenied = errors.New("git service request authorization denied")

// RequestAuthorization builds the non-secret authorization envelope carried by
// GitService RPCs. Authentication credentials remain exclusively in gRPC
// metadata; this message is an auditable snapshot of the API-approved actor,
// action, and repository scope.
func RequestAuthorization(ctx context.Context, action, repositoryID string) (*gitv1.RequestAuthorization, error) {
	principal := auth.PrincipalFromContext(ctx)
	// Background API work (bootstrap and controller reconciliation) has no
	// end-user transport principal. It still sends an explicit, auditable actor
	// rather than inheriting privilege from the API's HMAC credential.
	if principal == nil {
		principal = &auth.Principal{Subject: "system:api", Issuer: "gitstore-api", AuthMethod: "internal"}
	}
	approvedAnonymous := principal.AuthMethod == "none" && auth.AuthorizedAnonymousFromContext(ctx)
	if principal.Subject == "" || principal.AuthMethod == "" || (principal.AuthMethod == "none" && !approvedAnonymous) {
		return nil, fmt.Errorf("%w: requires an authenticated principal", ErrAuthorizationDenied)
	}
	if approvedAnonymous && action != "repository.read.any" {
		return nil, fmt.Errorf("%w: permits anonymous actors only for repository.read.any", ErrAuthorizationDenied)
	}
	if action == "" || repositoryID == "" {
		return nil, fmt.Errorf("git service request authorization requires action and repository ID")
	}
	return &gitv1.RequestAuthorization{
		Actor: &gitv1.AuthContext{
			Subject: principal.Subject, Issuer: principal.Issuer, AuthMethod: principal.AuthMethod,
			Roles: principal.Roles, Groups: principal.Groups, Scopes: principal.Scopes,
		},
		Action:       action,
		ResourceKind: "repository",
		RepositoryId: repositoryID,
	}, nil
}
