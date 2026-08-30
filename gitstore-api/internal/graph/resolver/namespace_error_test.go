// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

type resolverNamespacePolicySpy struct {
	calls []namespaceadmission.PolicyCheck
}

func (s *resolverNamespacePolicySpy) Evaluate(_ context.Context, check namespaceadmission.PolicyCheck) (*namespaceadmission.Decision, namespaceadmission.Preflight, error) {
	s.calls = append(s.calls, check)
	return nil, namespaceadmission.Preflight{Captured: true}, nil
}

func requireNamespaceErrorExtensions(t *testing.T, err error, code string, phase namespaceadmission.Phase, reason namespaceadmission.Reason) {
	t.Helper()
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, code, graphErr.Extensions["code"])
	if phase != "" {
		assert.Equal(t, string(phase), graphErr.Extensions["phase"])
	}
	if reason != "" {
		assert.Equal(t, string(reason), graphErr.Extensions["reason"])
	}
}

func TestNamespaceErrorConstructorsExposeStableExtensions(t *testing.T) {
	requireNamespaceErrorExtensions(t,
		resolver.NewNamespaceStructuralError(namespaceadmission.ReasonInvalidIdentifier, "invalid identifier"),
		namespaceadmission.CodeStructuralValidationFailed, namespaceadmission.PhaseStructural, namespaceadmission.ReasonInvalidIdentifier)
	requireNamespaceErrorExtensions(t,
		resolver.NewNamespaceImmutableError(namespaceadmission.ReasonImmutableName, "name is immutable"),
		namespaceadmission.CodeImmutableField, namespaceadmission.PhaseStructural, namespaceadmission.ReasonImmutableName)
	requireNamespaceErrorExtensions(t,
		resolver.NewNamespacePolicyError(namespaceadmission.ReasonTierDemotion, "tier demotion"),
		namespaceadmission.CodePolicyRejected, namespaceadmission.PhasePolicy, namespaceadmission.ReasonTierDemotion)
	requireNamespaceErrorExtensions(t,
		resolver.NewNamespaceConflictError(namespaceadmission.ReasonResourceVersionConflict, "conflict"),
		namespaceadmission.CodeConflict, namespaceadmission.PhasePolicy, namespaceadmission.ReasonResourceVersionConflict)

	blocked := resolver.NewNamespaceDeletionBlockedError([]namespaceadmission.Reason{
		namespaceadmission.ReasonNamespaceNotEmpty,
		namespaceadmission.ReasonBootstrapNamespace,
	}, "blocked")
	var graphErr *gqlerror.Error
	require.ErrorAs(t, blocked, &graphErr)
	assert.Equal(t, namespaceadmission.CodeDeletionBlocked, graphErr.Extensions["code"])
	assert.Equal(t, []string{"BOOTSTRAP_NAMESPACE", "NAMESPACE_NOT_EMPTY"}, graphErr.Extensions["reasons"])
}

func TestNamespaceCreateUpdateStructuralSchemaFailuresSkipPolicy(t *testing.T) {
	tests := map[string]func(*resolver.Service) error{
		"create title too long": func(svc *resolver.Service) error {
			input := createNamespaceInput("too-long-title", model.NamespaceTierUser)
			title := strings.Repeat("x", 201)
			input.Spec.Title = &title
			_, err := svc.CreateNamespace(context.Background(), input, "alice")
			return err
		},
		"update invalid label": func(svc *resolver.Service) error {
			input := updateNamespaceInput("invalid-label", model.NamespaceTierUser)
			input.Metadata.Labels = map[string]any{strings.Repeat("x", 64): "value"}
			_, err := svc.UpdateNamespace(context.Background(), input, "alice")
			return err
		},
	}

	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			spy := &resolverNamespacePolicySpy{}
			svc, err := resolver.NewService(resolver.ServiceDeps{
				Store:                    store,
				Logger:                   zap.NewNop(),
				NamespacePolicyEvaluator: spy,
			})
			require.NoError(t, err)

			err = run(svc)

			requireNamespaceErrorExtensions(t, err, namespaceadmission.CodeStructuralValidationFailed, namespaceadmission.PhaseStructural, namespaceadmission.ReasonInvalidEnvelope)
			assert.Empty(t, spy.calls)
		})
	}
}

func TestNamespaceCreateUpdateErrorsUseStableExtensions(t *testing.T) {
	t.Run("structural", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		_, err := svc.CreateNamespace(context.Background(), createNamespaceInput("invalid name", model.NamespaceTierUser), "alice")
		requireNamespaceErrorExtensions(t, err, namespaceadmission.CodeStructuralValidationFailed, namespaceadmission.PhaseStructural, namespaceadmission.ReasonInvalidIdentifier)
	})

	t.Run("duplicate create", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		input := createNamespaceInput("duplicate", model.NamespaceTierUser)
		_, err := svc.CreateNamespace(context.Background(), input, "alice")
		require.NoError(t, err)
		_, err = svc.CreateNamespace(context.Background(), input, "alice")
		requireNamespaceErrorExtensions(t, err, namespaceadmission.CodePolicyRejected, namespaceadmission.PhasePolicy, namespaceadmission.ReasonNamespaceAlreadyExists)
	})

	t.Run("update not found", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		_, err := svc.UpdateNamespace(context.Background(), updateNamespaceInput("missing", model.NamespaceTierUser), "alice")
		var graphErr *gqlerror.Error
		require.ErrorAs(t, err, &graphErr)
		assert.Equal(t, "NOT_FOUND", graphErr.Extensions["code"])
		assert.Equal(t, string(namespaceadmission.PhasePolicy), graphErr.Extensions["phase"])
		assert.Equal(t, "NAMESPACE_NOT_FOUND", graphErr.Extensions["reason"])
	})

	t.Run("tier demotion", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		_, err := svc.CreateNamespace(context.Background(), createNamespaceInput("demotion", model.NamespaceTierOrganization), "alice")
		require.NoError(t, err)
		_, err = svc.UpdateNamespace(context.Background(), updateNamespaceInput("demotion", model.NamespaceTierUser), "alice")
		requireNamespaceErrorExtensions(t, err, namespaceadmission.CodePolicyRejected, namespaceadmission.PhasePolicy, namespaceadmission.ReasonTierDemotion)
	})

	t.Run("terminating target", func(t *testing.T) {
		ctx := context.Background()
		svc := newTestSvc(t, &mockGitWriter{})
		created, err := svc.CreateNamespace(ctx, createNamespaceInput("terminating", model.NamespaceTierUser), "alice")
		require.NoError(t, err)
		_, err = svc.DeleteNamespace(ctx, created)
		require.NoError(t, err)

		_, err = svc.UpdateNamespace(ctx, updateNamespaceInput("terminating", model.NamespaceTierOrganization), "alice")
		requireNamespaceErrorExtensions(t, err, namespaceadmission.CodePolicyRejected, namespaceadmission.PhasePolicy, namespaceadmission.ReasonNamespaceTerminating)
	})
}
