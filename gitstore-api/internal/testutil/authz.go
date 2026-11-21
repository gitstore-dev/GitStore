// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package testutil

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// denyAllPolicy contains no grants, so ordinary principals are denied through
// the real strict v1beta1 evaluator rather than a stub decision path.
const denyAllPolicy = `apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
defaultDeny: true
clusterRoles: {}
clusterRoleBindings: []
`

// RecordingAuthZ wraps a real auth.AuthZProvider, delegating every decision
// to it while recording the last action and resource passed to Authorize.
// It lets tests assert that a caller computed the correct authorization
// inputs without needing a hand-rolled stub decision.
type RecordingAuthZ struct {
	auth.AuthZProvider
	mu       sync.Mutex
	Action   string
	Resource auth.ResourceContext
	Calls    []AuthorizationCall
}

// AuthorizationCall is one observed boundary decision, in invocation order.
type AuthorizationCall struct {
	Subject  string
	Action   string
	Resource auth.ResourceContext
}

// NewRecordingAuthZ wraps provider with input recording.
func NewRecordingAuthZ(provider auth.AuthZProvider) *RecordingAuthZ {
	return &RecordingAuthZ{AuthZProvider: provider}
}

func (r *RecordingAuthZ) Authorize(ctx context.Context, p *auth.Principal, action string, res auth.ResourceContext) (auth.Decision, error) {
	r.mu.Lock()
	r.Action = action
	r.Resource = res
	snapshot := res
	snapshot.Attrs = maps.Clone(res.Attrs)
	for key, value := range snapshot.Attrs {
		if nested, ok := value.(map[string]any); ok {
			snapshot.Attrs[key] = maps.Clone(nested)
		}
	}
	subject := ""
	if p != nil {
		subject = p.Subject
	}
	r.Calls = append(r.Calls, AuthorizationCall{Subject: subject, Action: action, Resource: snapshot})
	r.mu.Unlock()
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
