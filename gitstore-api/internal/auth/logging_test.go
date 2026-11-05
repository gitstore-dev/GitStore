// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
	err      error
}

func (s *staticAuthZProvider) Name() string { return s.name }

func (s *staticAuthZProvider) Authorize(_ context.Context, _ *authpkg.Principal, _ string, _ authpkg.ResourceContext) (authpkg.Decision, error) {
	return s.decision, s.err
}

func TestDecisionLoggerBoundsFieldsWithoutChangingDecision(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	original := authpkg.Allow(strings.Repeat("p", 4096), strings.Repeat("r", 4096))
	original.RequestID = strings.Repeat("i", 4096)
	provider := authpkg.NewDecisionLogger(&staticAuthZProvider{name: "test", decision: original}, zap.New(core))
	decision, err := provider.Authorize(t.Context(), &authpkg.Principal{
		Subject: strings.Repeat("s", 4096),
	}, strings.Repeat("a", 4096), authpkg.ResourceContext{
		Kind: strings.Repeat("k", 4096), Name: strings.Repeat("n", 4096),
	})
	require.NoError(t, err)
	require.Equal(t, original, decision)
	require.Len(t, logs.All(), 1)
	fields := logs.All()[0].ContextMap()
	for _, field := range []string{"provider", "subject", "action", "resource_kind", "resource_name", "request_id"} {
		require.LessOrEqual(t, len(fields[field].(string)), 256, field)
	}
	require.LessOrEqual(t, len(fields["reason"].(string)), 2048)
	require.Equal(t, true, fields["fields_truncated"])
}

type afterDecisionProvider struct {
	*rbaclocal.RBACLocalProvider
	after func()
}

func (p afterDecisionProvider) Authorize(ctx context.Context, principal *authpkg.Principal, action string, resource authpkg.ResourceContext) (authpkg.Decision, error) {
	decision, err := p.RBACLocalProvider.Authorize(ctx, principal, action, resource)
	p.after()
	return decision, err
}

func TestDecisionLoggerUsesEvaluatedSnapshotAfterReload(t *testing.T) {
	for replica := range 2 {
		for _, subject := range []string{"alice", "bob"} {
			t.Run(fmt.Sprintf("replica-%d/%s", replica, subject), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "policy.yaml")
				document := `apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  reader:
    rules: [{resources: [namespace], verbs: [read]}]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: reader}
    subjects: [{kind: User, name: alice}]
`
				require.NoError(t, os.WriteFile(path, []byte(document), 0600))
				inner, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
				require.NoError(t, err)
				principal := &authpkg.Principal{Subject: subject, AuthMethod: "static-users"}
				resource := authpkg.ResourceContext{Kind: "namespace"}
				before, err := inner.Authorize(t.Context(), principal, "namespace.read", resource)
				require.NoError(t, err)
				digest := strings.TrimPrefix(strings.Fields(before.Reason)[0], "policy=")
				require.Len(t, digest, 64)
				core, logs := observer.New(zapcore.InfoLevel)
				provider := authpkg.NewDecisionLogger(afterDecisionProvider{
					RBACLocalProvider: inner,
					after: func() {
						require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(document, "name: alice", "name: bob")), 0600))
						require.NoError(t, inner.Reload())
					},
				}, zap.New(core))
				decision, err := provider.Authorize(t.Context(), principal, "namespace.read", resource)
				require.NoError(t, err)
				require.Equal(t, before.Outcome, decision.Outcome)
				require.Equal(t, digest, decision.PolicyDigest)
				current, err := inner.Authorize(t.Context(), principal, "namespace.read", resource)
				require.NoError(t, err)
				require.NotEqual(t, before.Outcome, current.Outcome)
				require.NotEqual(t, digest, current.PolicyDigest)
				require.Len(t, logs.All(), 1)
				require.Equal(t, digest, logs.All()[0].ContextMap()["policy_digest"])
			})
		}
	}
}

func authorizationMetricSamples(t *testing.T, action, outcome string) uint64 {
	t.Helper()
	families, err := sdkTestMetrics.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "gitstore_authz_decision_duration_seconds" {
			continue
		}
		for _, metric := range family.Metric {
			labels := map[string]string{}
			for _, label := range metric.Label {
				labels[label.GetName()] = label.GetValue()
			}
			require.Len(t, labels, 2, "no principal, resource, policy or request labels")
			require.Contains(t, labels, "action")
			require.Contains(t, labels, "outcome")
			if labels["action"] != "unknown" {
				_, registered := authpkg.LookupAction(labels["action"])
				require.True(t, registered, "unregistered action must not become a metric label")
			}
			if labels["action"] == action && labels["outcome"] == outcome {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func TestDecisionLoggerBoundsMetricCardinalityAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  authpkg.Outcome
		err      error
		metric   string
		errClass string
	}{
		{"allow", authpkg.OutcomeAllow, nil, "allow", ""},
		{"deny", authpkg.OutcomeDeny, nil, "deny", ""},
		{"challenge", authpkg.OutcomeChallenge, nil, "challenge", ""},
		{"invalid", authpkg.Outcome(255), nil, "invalid", ""},
		{"error", authpkg.OutcomeAllow, errors.New("credential-must-not-be-logged"), "error", "provider_error"},
		{"cancel", authpkg.OutcomeDeny, fmt.Errorf("wrapped: %w", context.Canceled), "error", "canceled"},
		{"deadline", authpkg.OutcomeDeny, context.DeadlineExceeded, "error", "deadline_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			original := authpkg.Decision{Outcome: tc.outcome, Provider: "test", PolicyDigest: "invalid-secret-digest"}
			provider := authpkg.NewDecisionLogger(&staticAuthZProvider{name: "test", decision: original, err: tc.err}, zap.New(core))
			before := authorizationMetricSamples(t, "unknown", tc.metric)
			for i := range 3 {
				decision, err := provider.Authorize(t.Context(), &authpkg.Principal{Subject: fmt.Sprint(i)},
					fmt.Sprintf("unregistered.%d", i), authpkg.ResourceContext{Name: fmt.Sprint(i)})
				require.ErrorIs(t, err, tc.err)
				require.Equal(t, original, decision)
			}
			require.Equal(t, before+3, authorizationMetricSamples(t, "unknown", tc.metric))
			require.Len(t, logs.All(), 3)
			for _, entry := range logs.All() {
				fields := entry.ContextMap()
				require.NotContains(t, fields, "policy_digest")
				require.Equal(t, true, fields["invalid_policy_digest"])
				require.NotContains(t, fmt.Sprint(fields), "invalid-secret-digest")
				require.NotContains(t, fmt.Sprint(fields), "credential-must-not-be-logged")
				if tc.err != nil {
					require.Equal(t, tc.errClass, fields["error_class"])
					require.Equal(t, zapcore.WarnLevel, entry.Level)
				}
			}
		})
	}
}

func TestDecisionLoggerUTF8BoundsAndKnownMetric(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	original := authpkg.Allow("test", strings.Repeat("\u00e9", 2049))
	provider := authpkg.NewDecisionLogger(&staticAuthZProvider{name: "test", decision: original}, zap.New(core))
	before := authorizationMetricSamples(t, "namespace.read", "allow")
	decision, err := provider.Authorize(t.Context(), nil, "namespace.read", authpkg.ResourceContext{
		Kind: "namespace", Name: strings.Repeat("\u00e9", 127) + "x\u00e9",
	})
	require.NoError(t, err)
	require.Equal(t, original, decision)
	require.Equal(t, before+1, authorizationMetricSamples(t, "namespace.read", "allow"))
	fields := logs.All()[0].ContextMap()
	for _, key := range []string{"resource_name", "reason"} {
		require.True(t, utf8.ValidString(fields[key].(string)))
	}
	require.Len(t, fields["resource_name"], 256)
	require.Len(t, fields["reason"], 2048)

	provider = authpkg.NewDecisionLogger(&staticAuthZProvider{name: "test", decision: authpkg.Deny("test", "denied")}, zap.NewNop())
	before = authorizationMetricSamples(t, "namespace.read", "deny")
	_, err = provider.Authorize(t.Context(), nil, "namespace.read", authpkg.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Equal(t, before+1, authorizationMetricSamples(t, "namespace.read", "deny"), "metrics must not depend on log level")
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
