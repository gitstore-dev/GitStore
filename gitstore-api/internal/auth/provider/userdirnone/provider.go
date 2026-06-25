// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package userdirnone

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

// NoneProvider returns ErrNotSupported for all UserDirProvider methods.
type NoneProvider struct{}

func New() *NoneProvider { return &NoneProvider{} }

func (p *NoneProvider) Name() string { return "none" }

func (p *NoneProvider) GetBySubject(_ context.Context, _ string) (*auth.UserProfile, error) {
	return nil, auth.ErrNotSupported
}

func (p *NoneProvider) ListGroups(_ context.Context, _ string) ([]string, error) {
	return nil, auth.ErrNotSupported
}

func (p *NoneProvider) SearchUsers(_ context.Context, _ string, _ int) ([]*auth.UserProfile, error) {
	return nil, auth.ErrNotSupported
}

func (p *NoneProvider) UpsertProfile(_ context.Context, _ *auth.UserProfile) error {
	return auth.ErrNotSupported
}

func (p *NoneProvider) Deactivate(_ context.Context, _ string) error {
	return auth.ErrNotSupported
}
