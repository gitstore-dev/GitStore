// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package allowall

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"go.uber.org/zap"
)

// AllowAllProvider is an AuthZProvider that unconditionally allows every action.
// It emits a security warning at construction time so operators are never surprised.
type AllowAllProvider struct{}

func New(logger *zap.Logger) *AllowAllProvider {
	logger.Warn("SECURITY: authz provider is allow-all — ALL authorization checks are disabled. DO NOT use in production.")
	return &AllowAllProvider{}
}

func (p *AllowAllProvider) Name() string { return "allow-all" }

func (p *AllowAllProvider) Authorize(_ context.Context, _ *auth.Principal, _ string, _ auth.ResourceContext) (auth.Decision, error) {
	return auth.Allow("allow-all", "allow-all provider permits everything"), nil
}
