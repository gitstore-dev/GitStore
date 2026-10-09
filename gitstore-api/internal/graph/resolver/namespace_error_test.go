// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
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

// requireNamespaceError asserts the shared four-key envelope: the code, the
// phase (only for ADMISSION_REJECTED) and the ordered diagnostic reasons.
func requireNamespaceError(t *testing.T, err error, code admission.Code, phase admission.Phase, reasons ...namespaceadmission.Reason) {
	t.Helper()
	var graphErr *gqlerror.Error
	require.ErrorAs(t, err, &graphErr)
	assert.Equal(t, string(code), graphErr.Extensions["code"])
	for key := range graphErr.Extensions {
		assert.Contains(t, []string{"code", "diagnostics", "phase", "commit"}, key)
	}
	if phase != "" {
		assert.Equal(t, string(phase), graphErr.Extensions["phase"])
	} else {
		assert.NotContains(t, graphErr.Extensions, "phase")
	}
	if len(reasons) == 0 {
		return
	}
	diagnostics, ok := graphErr.Extensions["diagnostics"].([]map[string]any)
	require.True(t, ok, "diagnostics must be present")
	got := make([]string, 0, len(diagnostics))
	for _, d := range diagnostics {
		got = append(got, d["reason"].(string))
	}
	want := make([]string, 0, len(reasons))
	for _, r := range reasons {
		want = append(want, string(r))
	}
	assert.Equal(t, want, got)
}

func TestNamespaceErrorConstructorsUseSharedEnvelope(t *testing.T) {
	requireNamespaceError(t,
		resolver.NewNamespaceStructuralError(namespaceadmission.ReasonInvalidIdentifier, "invalid identifier"),
		admission.CodeAdmissionRejected, admission.PhasePreReceive, namespaceadmission.ReasonInvalidIdentifier)
	requireNamespaceError(t,
		resolver.NewNamespaceImmutableError(namespaceadmission.ReasonImmutableName, "name is immutable"),
		admission.CodeAdmissionRejected, admission.PhasePreReceive, namespaceadmission.ReasonImmutableName)
	requireNamespaceError(t,
		resolver.NewNamespacePolicyError(admission.PhasePreReceive, "", namespaceadmission.ReasonTierDemotion, "tier demotion"),
		admission.CodeAdmissionRejected, admission.PhasePreReceive, namespaceadmission.ReasonTierDemotion)
	postReceive := resolver.NewNamespacePolicyError(admission.PhasePostReceive, "abc123", namespaceadmission.ReasonTierDemotion, "tier demotion")
	requireNamespaceError(t, postReceive, admission.CodeAdmissionRejected, admission.PhasePostReceive, namespaceadmission.ReasonTierDemotion)
	var graphErr *gqlerror.Error
	require.ErrorAs(t, postReceive, &graphErr)
	assert.Equal(t, "abc123", graphErr.Extensions["commit"])

	for reason, code := range map[namespaceadmission.Reason]admission.Code{
		namespaceadmission.ReasonBootstrapNamespace:     admission.CodeFailedPrecondition,
		namespaceadmission.ReasonNamespaceTerminating:   admission.CodeFailedPrecondition,
		namespaceadmission.ReasonNamespaceAlreadyExists: admission.CodeAlreadyExists,
		namespaceadmission.ReasonNamespaceNotFound:      admission.CodeNotFound,
	} {
		requireNamespaceError(t,
			resolver.NewNamespacePolicyError(admission.PhasePostReceive, "abc123", reason, "rejected"),
			code, "", reason)
	}
	requireNamespaceError(t,
		resolver.NewNamespaceConflictError(namespaceadmission.ReasonResourceVersionConflict, "conflict"),
		admission.CodeConflict, "", namespaceadmission.ReasonResourceVersionConflict)
	requireNamespaceError(t,
		resolver.NewNamespaceConflictError(namespaceadmission.ReasonSuperseded, "superseded"),
		admission.CodeConflict, "", namespaceadmission.ReasonSuperseded)
	requireNamespaceError(t,
		resolver.NewNamespaceNotFoundError("missing"),
		admission.CodeNotFound, "", namespaceadmission.ReasonNamespaceNotFound)

	blocked := resolver.NewNamespaceDeletionBlockedError([]namespaceadmission.Reason{
		namespaceadmission.ReasonNamespaceNotEmpty,
		namespaceadmission.ReasonBootstrapNamespace,
	}, "blocked")
	requireNamespaceError(t, blocked, admission.CodeFailedPrecondition, "",
		namespaceadmission.ReasonBootstrapNamespace, namespaceadmission.ReasonNamespaceNotEmpty)
	require.ErrorAs(t, blocked, &graphErr)
	assert.Equal(t, "blocked", graphErr.Message)
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

			requireNamespaceError(t, err, admission.CodeAdmissionRejected, admission.PhasePreReceive, namespaceadmission.ReasonInvalidEnvelope)
			assert.Empty(t, spy.calls)
		})
	}
}

func TestNamespaceCreateUpdateErrorsUseStableExtensions(t *testing.T) {
	t.Run("structural", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		_, err := svc.CreateNamespace(context.Background(), createNamespaceInput("invalid name", model.NamespaceTierUser), "alice")
		requireNamespaceError(t, err, admission.CodeAdmissionRejected, admission.PhasePreReceive, namespaceadmission.ReasonInvalidIdentifier)
	})

	t.Run("duplicate create", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		input := createNamespaceInput("duplicate", model.NamespaceTierUser)
		_, err := svc.CreateNamespace(context.Background(), input, "alice")
		require.NoError(t, err)
		_, err = svc.CreateNamespace(context.Background(), input, "alice")
		requireNamespaceError(t, err, admission.CodeAlreadyExists, "", namespaceadmission.ReasonNamespaceAlreadyExists)
	})

	t.Run("update not found", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		_, err := svc.UpdateNamespace(context.Background(), updateNamespaceInput("missing", model.NamespaceTierUser), "alice")
		requireNamespaceError(t, err, admission.CodeNotFound, "", namespaceadmission.ReasonNamespaceNotFound)
	})

	t.Run("tier demotion", func(t *testing.T) {
		svc := newTestSvc(t, &mockGitWriter{})
		_, err := svc.CreateNamespace(context.Background(), createNamespaceInput("demotion", model.NamespaceTierOrganization), "alice")
		require.NoError(t, err)
		_, err = svc.UpdateNamespace(context.Background(), updateNamespaceInput("demotion", model.NamespaceTierUser), "alice")
		requireNamespaceError(t, err, admission.CodeAdmissionRejected, admission.PhasePreReceive, namespaceadmission.ReasonTierDemotion)
	})

	t.Run("terminating target", func(t *testing.T) {
		ctx := context.Background()
		svc := newTestSvc(t, &mockGitWriter{})
		created, err := svc.CreateNamespace(ctx, createNamespaceInput("terminating", model.NamespaceTierUser), "alice")
		require.NoError(t, err)
		_, err = svc.DeleteNamespace(ctx, created)
		require.NoError(t, err)

		_, err = svc.UpdateNamespace(ctx, updateNamespaceInput("terminating", model.NamespaceTierOrganization), "alice")
		requireNamespaceError(t, err, admission.CodeFailedPrecondition, "", namespaceadmission.ReasonNamespaceTerminating)
	})
}
