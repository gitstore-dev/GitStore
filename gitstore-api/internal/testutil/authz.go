// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// denyAllPolicy defines exactly one role, "no-access", so it satisfies
// rbaclocal's "at least one role" validation without ever matching a test
// principal's actual roles. With default_deny: true, every action is denied
// for every principal, mirroring the "always deny" behavior of the old
// stubAuthZProvider — but through the real rbac-local decision path.
const denyAllPolicy = `version: v1
default_deny: true
roles:
  no-access:
    deny:
      - "*"
role_bindings: {}
`

// RecordingAuthZ wraps a real auth.AuthZProvider, delegating every decision
// to it while recording the last action and resource passed to Authorize.
// It lets tests assert that a caller computed the correct authorization
// inputs without needing a hand-rolled stub decision.
type RecordingAuthZ struct {
	auth.AuthZProvider
	Action   string
	Resource auth.ResourceContext
}

// NewRecordingAuthZ wraps provider with input recording.
func NewRecordingAuthZ(provider auth.AuthZProvider) *RecordingAuthZ {
	return &RecordingAuthZ{AuthZProvider: provider}
}

func (r *RecordingAuthZ) Authorize(ctx context.Context, p *auth.Principal, action string, res auth.ResourceContext) (auth.Decision, error) {
	r.Action = action
	r.Resource = res
	return r.AuthZProvider.Authorize(ctx, p, action, res)
}

// NewAllowAllAuthZ returns a recording AuthZProvider backed by the real
// allow-all provider, i.e. every action is allowed regardless of principal.
func NewAllowAllAuthZ() *RecordingAuthZ {
	return NewRecordingAuthZ(allowall.New(zap.NewNop()))
}

// NewDenyAllAuthZ returns a recording AuthZProvider backed by the real
// rbac-local provider configured with a policy that denies every action
// regardless of principal.
func NewDenyAllAuthZ(t testing.TB) *RecordingAuthZ {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(denyAllPolicy), 0o600))
	provider, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
	require.NoError(t, err)
	return NewRecordingAuthZ(provider)
}
