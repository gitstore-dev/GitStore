// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package kratosclient is a thin wrapper over github.com/ory/client-go's Kratos
// public (/sessions/whoami) and Admin (/admin/identities) API surfaces, exposing
// exactly the operations gitstore-oidc-bridge's route handlers need.
package kratosclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ory "github.com/ory/client-go"
)

// ErrNoSession is returned by WhoAmI when the browser carries no valid, current
// Kratos session (Kratos answers 401/403).
var ErrNoSession = errors.New("kratos: no valid session")

// Identity is the subset of a Kratos identity the bridge maps onto OIDC claims.
type Identity struct {
	ID       string
	Email    string
	Username string
}

// Client is the Kratos API surface the bridge depends on.
type Client interface {
	// WhoAmI resolves the browser's Kratos session cookie to an identity.
	WhoAmI(ctx context.Context, cookieHeader string) (*Identity, error)
	// GetIdentity looks up an identity by id via the Admin API.
	GetIdentity(ctx context.Context, id string) (*Identity, error)
}

type oryClient struct {
	public *ory.APIClient
	admin  *ory.APIClient
}

// New returns a Client using publicURL for /sessions/whoami and adminURL for
// /admin/identities lookups.
func New(publicURL, adminURL string) Client {
	pubCfg := ory.NewConfiguration()
	pubCfg.Servers = ory.ServerConfigurations{{URL: publicURL}}
	admCfg := ory.NewConfiguration()
	admCfg.Servers = ory.ServerConfigurations{{URL: adminURL}}
	return &oryClient{public: ory.NewAPIClient(pubCfg), admin: ory.NewAPIClient(admCfg)}
}

func identityFromTraits(id string, traits map[string]interface{}) *Identity {
	out := &Identity{ID: id}
	if v, ok := traits["email"].(string); ok {
		out.Email = v
	}
	if v, ok := traits["username"].(string); ok {
		out.Username = v
	}
	return out
}

func (c *oryClient) WhoAmI(ctx context.Context, cookieHeader string) (*Identity, error) {
	session, resp, err := c.public.FrontendAPI.ToSession(ctx).Cookie(cookieHeader).Execute()
	if err != nil {
		if resp != nil && (resp.StatusCode == 401 || resp.StatusCode == 403) {
			return nil, ErrNoSession
		}
		var genErr *ory.GenericOpenAPIError
		if errors.As(err, &genErr) {
			var body struct {
				Error struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if jsonErr := json.Unmarshal(genErr.Body(), &body); jsonErr == nil &&
				(body.Error.Code == 401 || body.Error.Code == 403) {
				return nil, ErrNoSession
			}
		}
		return nil, fmt.Errorf("kratos: whoami: %w", err)
	}
	traits, _ := session.Identity.Traits.(map[string]interface{})
	return identityFromTraits(session.Identity.Id, traits), nil
}

func (c *oryClient) GetIdentity(ctx context.Context, id string) (*Identity, error) {
	identity, _, err := c.admin.IdentityAPI.GetIdentity(ctx, id).Execute()
	if err != nil {
		return nil, fmt.Errorf("kratos: get identity %q: %w", id, err)
	}
	traits, _ := identity.Traits.(map[string]interface{})
	return identityFromTraits(identity.Id, traits), nil
}
