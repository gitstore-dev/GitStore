// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"testing"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type staticAuthZProvider struct {
	name     string
	decision authpkg.Decision
}

func (s *staticAuthZProvider) Name() string { return s.name }

func (s *staticAuthZProvider) Authorize(_ context.Context, _ *authpkg.Principal, _ string, _ authpkg.ResourceContext) (authpkg.Decision, error) {
	return s.decision, nil
}

func TestDecisionLogger_EmitsStructuredFieldsAndRequestID(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	provider := authpkg.NewDecisionLogger(&staticAuthZProvider{
		name:     "rbac-local",
		decision: authpkg.Allow("rbac-local", "allowed by policy"),
	}, logger)

	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, "req-123")
	decision, err := provider.Authorize(ctx, &authpkg.Principal{
		Subject:    "alice",
		Roles:      []string{"reader"},
		AuthMethod: "test",
	}, "namespace.read", authpkg.ResourceContext{Kind: "namespace", Name: "demo"})
	require.NoError(t, err)
	assert.Equal(t, "req-123", decision.RequestID)

	entries := logs.All()
	require.Len(t, entries, 1)
	assert.Equal(t, "authz decision", entries[0].Message)

	fields := entries[0].ContextMap()
	assert.Equal(t, "rbac-local", fields["provider"])
	assert.Equal(t, "alice", fields["subject"])
	assert.Equal(t, "namespace.read", fields["action"])
	assert.Equal(t, "namespace", fields["resource_kind"])
	assert.Equal(t, "demo", fields["resource_name"])
	assert.Equal(t, "req-123", fields["request_id"])
	assert.Equal(t, "allow", fields["outcome"])
}

// TestDecisionLogger_ServiceAccountSubjectRendersUnmodified confirms (spec
// 061 T013) that DecisionLogger requires no code changes to correctly log
// service-account principals: subject is passed through verbatim from
// Principal.Subject, so the "serviceaccount:<namespace>:<name>" convention
// established by datastore.ServiceAccountSubject renders with no
// truncation, escaping, or reformatting.
func TestDecisionLogger_ServiceAccountSubjectRendersUnmodified(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	provider := authpkg.NewDecisionLogger(&staticAuthZProvider{
		name:     "rbac-local",
		decision: authpkg.Allow("rbac-local", "allowed by policy"),
	}, logger)

	_, err := provider.Authorize(context.Background(), &authpkg.Principal{
		Subject:    "serviceaccount:controllers:gitstore-controller-manager",
		Roles:      nil,
		AuthMethod: "serviceaccount-jwt",
	}, "categoryTaxonomy.read", authpkg.ResourceContext{Kind: "categorytaxonomy", Name: "demo"})
	require.NoError(t, err)

	entries := logs.All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	assert.Equal(t, "serviceaccount:controllers:gitstore-controller-manager", fields["subject"])
}
