// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package hydraclient is a thin wrapper over github.com/ory/client-go's Hydra
// Admin API surface, exposing exactly the operations gitstore-oidc-bridge's
// route handlers need (see specs/059-optional-oidc-provider/contracts/oidc-bridge-routes.md).
package hydraclient

import (
	"context"
	"fmt"

	ory "github.com/ory/client-go"
)

// LoginRequest is the subset of Hydra's OAuth2 login request the bridge acts on.
type LoginRequest struct {
	Challenge string
}

// ConsentRequest is the subset of Hydra's OAuth2 consent request the bridge acts on.
type ConsentRequest struct {
	Challenge         string
	Subject           string
	RequestedScope    []string
	RequestedAudience []string
}

// Client is the Hydra Admin API surface the bridge depends on.
type Client interface {
	GetLoginRequest(ctx context.Context, challenge string) (*LoginRequest, error)
	AcceptLoginRequest(ctx context.Context, challenge, subject string) (redirectTo string, err error)
	RejectLoginRequest(ctx context.Context, challenge, errCode, errDescription string) (redirectTo string, err error)
	GetConsentRequest(ctx context.Context, challenge string) (*ConsentRequest, error)
	AcceptConsentRequest(ctx context.Context, challenge string, grantScope, grantAudience []string, idTokenClaims map[string]interface{}) (redirectTo string, err error)
	RejectConsentRequest(ctx context.Context, challenge, errCode, errDescription string) (redirectTo string, err error)
}

type oryClient struct {
	api *ory.APIClient
}

// New returns a Client backed by Hydra's Admin API at adminURL.
func New(adminURL string) Client {
	cfg := ory.NewConfiguration()
	cfg.Servers = ory.ServerConfigurations{{URL: adminURL}}
	return &oryClient{api: ory.NewAPIClient(cfg)}
}

func (c *oryClient) GetLoginRequest(ctx context.Context, challenge string) (*LoginRequest, error) {
	req, _, err := c.api.OAuth2API.GetOAuth2LoginRequest(ctx).LoginChallenge(challenge).Execute()
	if err != nil {
		return nil, fmt.Errorf("hydra: get login request: %w", err)
	}
	return &LoginRequest{Challenge: req.Challenge}, nil
}

func (c *oryClient) AcceptLoginRequest(ctx context.Context, challenge, subject string) (string, error) {
	body := ory.AcceptOAuth2LoginRequest{
		Subject:  subject,
		Remember: ory.PtrBool(true),
	}
	res, _, err := c.api.OAuth2API.AcceptOAuth2LoginRequest(ctx).LoginChallenge(challenge).AcceptOAuth2LoginRequest(body).Execute()
	if err != nil {
		return "", fmt.Errorf("hydra: accept login request: %w", err)
	}
	return res.RedirectTo, nil
}

func (c *oryClient) RejectLoginRequest(ctx context.Context, challenge, errCode, errDescription string) (string, error) {
	body := ory.RejectOAuth2Request{
		Error:            ory.PtrString(errCode),
		ErrorDescription: ory.PtrString(errDescription),
	}
	res, _, err := c.api.OAuth2API.RejectOAuth2LoginRequest(ctx).LoginChallenge(challenge).RejectOAuth2Request(body).Execute()
	if err != nil {
		return "", fmt.Errorf("hydra: reject login request: %w", err)
	}
	return res.RedirectTo, nil
}

func (c *oryClient) GetConsentRequest(ctx context.Context, challenge string) (*ConsentRequest, error) {
	req, _, err := c.api.OAuth2API.GetOAuth2ConsentRequest(ctx).ConsentChallenge(challenge).Execute()
	if err != nil {
		return nil, fmt.Errorf("hydra: get consent request: %w", err)
	}
	consent := &ConsentRequest{
		Challenge:         req.Challenge,
		RequestedScope:    req.RequestedScope,
		RequestedAudience: req.RequestedAccessTokenAudience,
	}
	if req.Subject != nil {
		consent.Subject = *req.Subject
	}
	return consent, nil
}

func (c *oryClient) AcceptConsentRequest(ctx context.Context, challenge string, grantScope, grantAudience []string, idTokenClaims map[string]interface{}) (string, error) {
	body := ory.AcceptOAuth2ConsentRequest{
		GrantScope:               grantScope,
		GrantAccessTokenAudience: grantAudience,
		Remember:                 ory.PtrBool(true),
	}
	if idTokenClaims != nil {
		body.Session = &ory.AcceptOAuth2ConsentRequestSession{IdToken: idTokenClaims}
	}
	res, _, err := c.api.OAuth2API.AcceptOAuth2ConsentRequest(ctx).ConsentChallenge(challenge).AcceptOAuth2ConsentRequest(body).Execute()
	if err != nil {
		return "", fmt.Errorf("hydra: accept consent request: %w", err)
	}
	return res.RedirectTo, nil
}

func (c *oryClient) RejectConsentRequest(ctx context.Context, challenge, errCode, errDescription string) (string, error) {
	body := ory.RejectOAuth2Request{
		Error:            ory.PtrString(errCode),
		ErrorDescription: ory.PtrString(errDescription),
	}
	res, _, err := c.api.OAuth2API.RejectOAuth2ConsentRequest(ctx).ConsentChallenge(challenge).RejectOAuth2Request(body).Execute()
	if err != nil {
		return "", fmt.Errorf("hydra: reject consent request: %w", err)
	}
	return res.RedirectTo, nil
}
