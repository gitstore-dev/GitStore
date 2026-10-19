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
	"github.com/prometheus/client_golang/prometheus"
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
	registry := authpkg.NewProviderRegistry(authpkg.NewChainedAuthN(), provider, nil)
	diagnostics := registry.Diagnostics()
	require.NotNil(t, diagnostics.Policy, "the deployed logging wrapper must preserve optional snapshot metadata")
	require.True(t, diagnostics.Policy.Loaded)
	require.Len(t, diagnostics.Policy.Digest, 64)
	require.Equal(t, 1, diagnostics.Policy.Roles)
	require.NoError(t, os.Remove(path))
	require.Equal(t, diagnostics, registry.Diagnostics(), "diagnostics must not reread the policy file")
	require.Empty(t, logs.All(), "collecting metadata must not emit a synthetic authorization decision")
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
		assert.Equal(t, diagnostics.CatalogDigest, fields["catalog_digest"])
		assert.Equal(t, diagnostics.Policy.Digest, fields["policy_digest"])
		require.NotNil(t, fields["latency_ns"])
		assert.Positive(t, fields["latency_ns"])
		assert.NotContains(t, fmt.Sprint(fields), "super-secret")
		assert.NotContains(t, fields, "claims")
		assert.NotContains(t, fields, "attrs")
		assert.NotContains(t, fields, "roles")
	}

}

func TestDecisionLoggerBoundsFieldsWithoutChangingDecision(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	reason := strings.Repeat("r", 4096)
	inner := &staticAuthZProvider{name: "test", decision: authpkg.Allow("test", reason)}
	provider := authpkg.NewDecisionLogger(inner, zap.New(core))
	decision, err := provider.Authorize(t.Context(), &authpkg.Principal{
		Subject: strings.Repeat("s", 4096),
	}, strings.Repeat("a", 4096), authpkg.ResourceContext{
		Kind: strings.Repeat("k", 4096), Name: strings.Repeat("n", 4096),
	})
	require.NoError(t, err)
	require.Equal(t, inner.decision, decision, "diagnostics must not rewrite the returned decision")
	require.Len(t, logs.All(), 1)
	fields := logs.All()[0].ContextMap()
	for _, field := range []string{"subject", "action", "resource_kind", "resource_name"} {
		require.LessOrEqual(t, len(fields[field].(string)), 256, field)
	}
	require.LessOrEqual(t, len(fields["reason"].(string)), 2048)
	require.Equal(t, true, fields["fields_truncated"])
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
	oldSnapshot, _ := inner.AuthorizationSnapshot()
	core, logs := observer.New(zapcore.InfoLevel)
	provider := authpkg.NewDecisionLogger(afterDecisionProvider{
		RBACLocalProvider: inner,
		after: func() {
			require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(document, "name: alice", "name: bob")), 0600))
			require.NoError(t, inner.Reload())
		},
	}, zap.New(core).With(zap.String("instance_id", "replica-a")))
	decision, err := provider.Authorize(t.Context(), &authpkg.Principal{
		Subject: "alice", AuthMethod: "static-users",
	}, "namespace.read", authpkg.ResourceContext{Kind: "namespace"})
	require.NoError(t, err)
	require.Equal(t, authpkg.OutcomeAllow, decision.Outcome)
	current, _ := inner.AuthorizationSnapshot()
	require.NotEqual(t, oldSnapshot.Digest, current.Digest)
	require.Equal(t, oldSnapshot.Digest, decision.PolicyDigest)
	require.Len(t, logs.All(), 1)
	require.Equal(t, oldSnapshot.Digest, logs.All()[0].ContextMap()["policy_digest"])
	require.Equal(t, "replica-a", logs.All()[0].ContextMap()["instance_id"])
}

func authorizationMetricSamples(t *testing.T, action, outcome string) uint64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
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
			if labels["action"] == action && labels["outcome"] == outcome {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

func TestProviderRegistryReloadCountersExistBeforeFirstAttempt(t *testing.T) {
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "gitstore_authz_registry_reloads_total" {
			continue
		}
		outcomes := []string{}
		for _, metric := range family.Metric {
			require.Len(t, metric.Label, 1)
			require.Equal(t, "outcome", metric.Label[0].GetName())
			outcomes = append(outcomes, metric.Label[0].GetValue())
			require.Zero(t, metric.GetCounter().GetValue())
		}
		require.ElementsMatch(t, []string{"accepted", "rejected"}, outcomes)
		return
	}
	t.Fatal("zero baselines must exist so the first reload increment is observable")
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
		{"cancel", authpkg.OutcomeDeny, context.Canceled, "error", "canceled"},
		{"deadline", authpkg.OutcomeDeny, context.DeadlineExceeded, "error", "deadline_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			original := authpkg.Decision{Outcome: tc.outcome, PolicyDigest: "invalid-secret-digest"}
			inner := &staticAuthZProvider{name: "test", decision: original, err: tc.err}
			provider := authpkg.NewDecisionLogger(inner, zap.New(core))
			before := authorizationMetricSamples(t, "unknown", tc.metric)
			for i := range 3 {
				decision, err := provider.Authorize(t.Context(), &authpkg.Principal{Subject: fmt.Sprint(i)},
					fmt.Sprintf("unregistered.%d", i), authpkg.ResourceContext{Name: fmt.Sprint(i)})
				require.ErrorIs(t, err, tc.err)
				require.Equal(t, original.Outcome, decision.Outcome)
				require.Equal(t, original.PolicyDigest, decision.PolicyDigest)
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
	inner := &staticAuthZProvider{name: "test", decision: authpkg.Allow("test", strings.Repeat("\u00e9", 2049))}
	provider := authpkg.NewDecisionLogger(inner, zap.New(core))
	before := authorizationMetricSamples(t, "namespace.read", "allow")
	_, err := provider.Authorize(t.Context(), nil, "namespace.read", authpkg.ResourceContext{
		Kind: "namespace", Name: strings.Repeat("\u00e9", 127) + "x\u00e9",
	})
	require.NoError(t, err)
	require.Equal(t, before+1, authorizationMetricSamples(t, "namespace.read", "allow"))
	fields := logs.All()[0].ContextMap()
	for _, key := range []string{"resource_name", "reason"} {
		require.True(t, utf8.ValidString(fields[key].(string)))
	}
	require.Len(t, fields["resource_name"], 256)
	require.Len(t, fields["reason"], 2048)
}

func TestDecisionLogger_EmitsStructuredFieldsAndRequestID(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core)
	provider := authpkg.NewDecisionLogger(&staticAuthZProvider{
		name:     "rbac-local",
		decision: authpkg.Allow("rbac-local", "allowed by policy"),
	}, logger)
	registry := authpkg.NewProviderRegistry(authpkg.NewChainedAuthN(), provider, nil)
	require.Nil(t, registry.Diagnostics().Policy, "unsupported wrapped providers must not invent a snapshot")

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
