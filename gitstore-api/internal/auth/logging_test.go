// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestV1Beta1DecisionLogsContainRuleNotSecretFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  owner:
    rules:
      - resources: [namespace]
        verbs: [read]
        when: [owner]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: owner}
    subjects: [{kind: User, name: alice}]
`), 0600))
	inner, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
	require.NoError(t, err)
	core, logs := observer.New(zapcore.InfoLevel)
	provider := authpkg.NewDecisionLogger(inner, zap.New(core))
	for _, owner := range []string{"alice", "bob", ""} {
		d, err := provider.Authorize(t.Context(), &authpkg.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}, Claims: map[string]any{"token": "super-secret"}}, "namespace.read", authpkg.ResourceContext{Kind: "namespace", Name: "acme", OwnerSub: owner, Attrs: map[string]any{"assertion": "super-secret"}})
		require.NoError(t, err)
		require.NotEmpty(t, d.Reason)
		if owner == "alice" {
			for _, field := range []string{"binding=clusterRoleBindings[0]", "role=ClusterRole/owner", "rule=0"} {
				assert.Contains(t, d.Reason, field)
			}
		} else {
			assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
		}
	}
	require.Len(t, logs.All(), 3)
	for _, entry := range logs.All() {
		fields := entry.ContextMap()
		assert.NotContains(t, fmt.Sprint(fields), "super-secret")
		assert.NotContains(t, fields, "claims")
		assert.NotContains(t, fields, "attrs")
		assert.NotContains(t, fields, "roles")
	}
}

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
		Subject: "alice",

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

// The logger preserves an authenticated cluster-scoped ServiceAccount subject;
// identity validation belongs to AuthN, not log presentation.
func TestDecisionLogger_ServiceAccountSubjectRendersUnmodified(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	provider := authpkg.NewDecisionLogger(&staticAuthZProvider{
		name:     "rbac-local",
		decision: authpkg.Allow("rbac-local", "allowed by policy"),
	}, logger)

	_, err := provider.Authorize(context.Background(), &authpkg.Principal{
		Subject: "system:serviceaccount:gitstore-controller-manager",

		AuthMethod: "serviceaccount-jwt",
	}, "categoryTaxonomy.read", authpkg.ResourceContext{Kind: "categoryTaxonomy", Name: "demo"})
	require.NoError(t, err)

	entries := logs.All()
	require.Len(t, entries, 1)
	fields := entries[0].ContextMap()
	assert.Equal(t, "system:serviceaccount:gitstore-controller-manager", fields["subject"])
}
