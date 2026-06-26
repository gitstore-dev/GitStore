// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package anonymous provides an AuthNProvider that issues an anonymous Principal
// only when no credential signals are present on the request. It MUST be placed
// last in the provider chain — earlier placement would shadow subsequent providers.
package anonymous

import (
	"context"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

// AnonymousProvider claims a request only when no credentials are present.
type AnonymousProvider struct{}

func New() *AnonymousProvider { return &AnonymousProvider{} }

func (p *AnonymousProvider) Name() string { return "anonymous" }

func (p *AnonymousProvider) Capabilities() auth.Capability { return auth.CapAuthenticate }

func (p *AnonymousProvider) Authenticate(_ context.Context, req auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	hasCredentials := req.Header.Get("Authorization") != "" || req.ForwardedSubject != ""
	if hasCredentials {
		// Credentials were presented but no prior provider accepted them.
		// Return Challenge so the chain fallthrough becomes a Deny.
		return nil, auth.Challenge("anonymous", "credentials present but unrecognized"), nil
	}
	return auth.Anonymous(), auth.Allow("anonymous", "no credentials presented"), nil
}

func (p *AnonymousProvider) RevokeSession(_ context.Context, _ string, _ time.Time) error {
	return auth.ErrNotSupported
}

func (p *AnonymousProvider) RefreshSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}

func (p *AnonymousProvider) IssueSession(_ context.Context, _ string) (string, time.Time, error) {
	return "", time.Time{}, auth.ErrNotSupported
}
