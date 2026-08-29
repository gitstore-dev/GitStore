// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type namespacePolicySpy struct {
	calls       []namespaceadmission.PolicyCheck
	decision    *namespaceadmission.Decision
	evaluateErr error
}

func (s *namespacePolicySpy) Evaluate(_ context.Context, check namespaceadmission.PolicyCheck) (*namespaceadmission.Decision, namespaceadmission.Preflight, error) {
	s.calls = append(s.calls, check)
	return s.decision, namespaceadmission.Preflight{Captured: true}, s.evaluateErr
}

func namespaceValidationRequest(oldBlobs, proposedBlobs []*catalogv1.ResourceBlob) *catalogv1.ValidateResourcesRequest {
	return &catalogv1.ValidateResourcesRequest{
		RepositoryId: testRepoID,
		Trees: []*catalogv1.ResourceValidationTree{{
			OldBlobs:      oldBlobs,
			ProposedBlobs: proposedBlobs,
		}},
	}
}

func namespaceBlob(path, name, tier string) *catalogv1.ResourceBlob {
	return &catalogv1.ResourceBlob{Path: path, Content: namespaceManifest(name, name, tier)}
}

func withNamespacePolicySpy(spy namespaceadmission.PolicyEvaluator) func(*cataloggrpc.ServerDeps) {
	return func(deps *cataloggrpc.ServerDeps) {
		deps.NamespacePolicyEvaluator = spy
	}
}

func TestValidateResourcesStructuralFailureSuppressesNamespacePolicyForWholeRequest(t *testing.T) {
	tests := map[string]*catalogv1.ResourceBlob{
		"invalid Namespace": namespaceBlob("namespaces/invalid.md", "Invalid Name", "USER"),
		"invalid other kind": {
			Path: "products/invalid.md",
			Content: []byte(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: invalid
spec: {}
status:
  conditions: []
---
`),
		},
	}
	for name, invalid := range tests {
		t.Run(name, func(t *testing.T) {
			spy := &namespacePolicySpy{}
			srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil, withNamespacePolicySpy(spy))
			valid := namespaceBlob("namespaces/acme.md", "acme", "USER")

			resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(nil, []*catalogv1.ResourceBlob{invalid, valid}))

			require.NoError(t, err)
			assert.False(t, resp.Accepted)
			assert.Empty(t, spy.calls)
		})
	}
}

func TestValidateResourcesCountsOnlyMalformedNamespaceParserFailures(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := namespaceadmission.NewMetrics(registry)
	srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil, func(deps *cataloggrpc.ServerDeps) {
		deps.NamespaceMetrics = metrics
	})
	invalidNamespace := &catalogv1.ResourceBlob{
		Path: "namespaces/invalid.md",
		Content: []byte(`---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: invalid
spec:
  title: [
  tier: USER
---
`),
	}
	invalidProduct := &catalogv1.ResourceBlob{
		Path: "products/invalid.md",
		Content: []byte(`---
apiVersion: catalog.gitstore.dev/v1beta1
kind: Product
metadata:
  name: invalid
spec:
  title: [
---
`),
	}

	resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(nil, []*catalogv1.ResourceBlob{
		invalidNamespace,
		invalidProduct,
	}))

	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	assert.Equal(t, float64(1), namespaceStructuralRejectionCount(t, registry))
}

func TestValidateResourcesNonNamespaceKindsNeverInvokeNamespacePolicy(t *testing.T) {
	spy := &namespacePolicySpy{}
	srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil, withNamespacePolicySpy(spy))

	resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(nil, []*catalogv1.ResourceBlob{{
		Path:    "products/widget.md",
		Content: []byte(validProduct),
	}}))

	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	assert.Empty(t, spy.calls)
}

func TestValidateResourcesNamespaceIdentifierRulesMatchGraphQL(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		constraint string
	}{
		{name: "malformed DNS label", identifier: "bad_name", constraint: "dns-label"},
		{name: "reserved identifier", identifier: "admin", constraint: "reserved"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil)
			path := "namespaces/" + test.identifier + ".md"

			resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(nil, []*catalogv1.ResourceBlob{
				namespaceBlob(path, test.identifier, "USER"),
			}))

			require.NoError(t, err)
			assert.False(t, resp.Accepted)
			require.Contains(t, validationConstraints(resp), "metadata.name:"+test.constraint)
		})
	}
}

func TestAdmitResourcesNamespaceUpdateMissingAfterConflictDoesNotCreate(t *testing.T) {
	base := newNamespacePolicyDatastore(t)
	store := &namespaceDeletedDuringUpdateStore{Datastore: base, name: "other"}
	registry := prometheus.NewRegistry()
	metrics := namespaceadmission.NewMetrics(registry)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	current := b
	path := "namespaces/other.md"
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		a: {path: namespaceManifest("other", "Other", "USER")},
		b: {path: namespaceManifest("other", "Updated", "ORGANIZATION")},
	})
	srv := newCatalogServer(t, store, git, func(deps *cataloggrpc.ServerDeps) {
		deps.NamespaceMetrics = metrics
	})

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: a,
		NewCommitSha: b,
		CommitSha:    b,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, store.updateCalls)
	assert.Equal(t, 0, store.createCalls)
	_, err = base.GetNamespaceByName(context.Background(), "other")
	require.ErrorIs(t, err, datastore.ErrNotFound)
	assert.Equal(t, float64(1), namespaceRejectionCount(t, registry, namespaceadmission.PhasePolicy, namespaceadmission.ReasonNamespaceNotFound))
}

func TestAdmitResourcesNamespaceUpdateMissingInitiallyDoesNotCreate(t *testing.T) {
	base := newNamespacePolicyDatastore(t)
	existing, err := base.GetNamespaceByName(context.Background(), "other")
	require.NoError(t, err)
	require.NoError(t, base.DeleteNamespace(context.Background(), existing.ID))

	store := &namespaceDeletedDuringUpdateStore{Datastore: base, name: "other"}
	registry := prometheus.NewRegistry()
	metrics := namespaceadmission.NewMetrics(registry)
	a := strings.Repeat("c", 40)
	b := strings.Repeat("d", 40)
	current := b
	path := "namespaces/other.md"
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		a: {path: namespaceManifest("other", "Other", "USER")},
		b: {path: namespaceManifest("other", "Updated", "ORGANIZATION")},
	})
	srv := newCatalogServer(t, store, git, func(deps *cataloggrpc.ServerDeps) {
		deps.NamespaceMetrics = metrics
	})

	_, err = srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID,
		OldCommitSha: a,
		NewCommitSha: b,
		CommitSha:    b,
		RefName:      "refs/heads/main",
		ChangedPaths: []string{path},
	})

	require.NoError(t, err)
	assert.Equal(t, 0, store.updateCalls)
	assert.Equal(t, 0, store.createCalls)
	_, err = base.GetNamespaceByName(context.Background(), "other")
	require.ErrorIs(t, err, datastore.ErrNotFound)
	assert.Equal(t, float64(1), namespaceRejectionCount(t, registry, namespaceadmission.PhasePolicy, namespaceadmission.ReasonNamespaceNotFound))
}

func TestValidateResourcesSamePathNamespaceNameChangeIsImmutable(t *testing.T) {
	spy := &namespacePolicySpy{}
	srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil, withNamespacePolicySpy(spy))
	path := "namespaces/old-name.md"

	resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(
		[]*catalogv1.ResourceBlob{namespaceBlob(path, "old-name", "USER")},
		[]*catalogv1.ResourceBlob{namespaceBlob(path, "new-name", "USER")},
	))

	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	assert.Empty(t, spy.calls)
	require.Contains(t, validationConstraints(resp), "metadata.name:immutable")
}

func TestValidateResourcesPathAndNameChangeIsNewDeclaration(t *testing.T) {
	spy := &namespacePolicySpy{}
	srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil, withNamespacePolicySpy(spy))

	resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(
		[]*catalogv1.ResourceBlob{namespaceBlob("namespaces/old-name.md", "old-name", "USER")},
		[]*catalogv1.ResourceBlob{namespaceBlob("namespaces/new-name.md", "new-name", "USER")},
	))

	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	require.Len(t, spy.calls, 1)
	assert.Equal(t, admission.OperationCreate, spy.calls[0].Operation)
}

func TestValidateResourcesNamespacePolicyUsesStableConstraint(t *testing.T) {
	spy := &namespacePolicySpy{decision: &namespaceadmission.Decision{
		Phase:   namespaceadmission.PhasePolicy,
		Reason:  namespaceadmission.ReasonTierDemotion,
		Field:   "spec.tier",
		Message: "namespace tier demotion is not allowed",
	}}
	srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil, withNamespacePolicySpy(spy))
	path := "namespaces/acme.md"

	resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(
		[]*catalogv1.ResourceBlob{namespaceBlob(path, "acme", "ORGANIZATION")},
		[]*catalogv1.ResourceBlob{namespaceBlob(path, "acme", "USER")},
	))

	require.NoError(t, err)
	assert.False(t, resp.Accepted)
	require.Len(t, spy.calls, 1)
	require.Contains(t, validationConstraints(resp), "spec.tier:policy/tier-demotion")
}

func TestValidateResourcesNamespacePolicyMatrix(t *testing.T) {
	tests := map[string]struct {
		prepare    func(t *testing.T, store datastore.Datastore)
		old        []*catalogv1.ResourceBlob
		proposed   *catalogv1.ResourceBlob
		constraint string
	}{
		"bootstrap": {
			old:        []*catalogv1.ResourceBlob{namespaceBlob("namespaces/default.md", "default", "USER")},
			proposed:   namespaceBlob("namespaces/default.md", "default", "ORGANIZATION"),
			constraint: "metadata.name:policy/bootstrap-namespace",
		},
		"duplicate create": {
			proposed:   namespaceBlob("namespaces/other.md", "other", "USER"),
			constraint: "metadata.name:policy/namespace-already-exists",
		},
		"tier demotion": {
			prepare: func(t *testing.T, store datastore.Datastore) {
				namespace, err := store.GetNamespaceByName(context.Background(), "other")
				require.NoError(t, err)
				expected := namespace.ResourceVersion
				namespace.Tier = datastore.NamespaceTierOrganization
				require.NoError(t, store.UpdateNamespace(context.Background(), namespace, expected))
			},
			old:        []*catalogv1.ResourceBlob{namespaceBlob("namespaces/other.md", "other", "ORGANIZATION")},
			proposed:   namespaceBlob("namespaces/other.md", "other", "USER"),
			constraint: "spec.tier:policy/tier-demotion",
		},
		"terminating target": {
			prepare: func(t *testing.T, store datastore.Datastore) {
				namespace, err := store.GetNamespaceByName(context.Background(), "other")
				require.NoError(t, err)
				expected := namespace.ResourceVersion
				deletedAt := time.Now().UTC()
				namespace.DeletionTimestamp = &deletedAt
				require.NoError(t, store.UpdateNamespace(context.Background(), namespace, expected))
			},
			old:        []*catalogv1.ResourceBlob{namespaceBlob("namespaces/other.md", "other", "USER")},
			proposed:   namespaceBlob("namespaces/other.md", "other", "USER"),
			constraint: "metadata.name:policy/namespace-terminating",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			store := newNamespacePolicyDatastore(t)
			if test.prepare != nil {
				test.prepare(t, store)
			}
			srv := newCatalogServer(t, store, nil)

			resp, err := srv.ValidateResources(context.Background(), namespaceValidationRequest(test.old, []*catalogv1.ResourceBlob{test.proposed}))

			require.NoError(t, err)
			assert.False(t, resp.Accepted)
			require.Contains(t, validationConstraints(resp), test.constraint)
		})
	}
}

func TestValidateResourcesLegacyBlobShapePreservesNamespaceUpsert(t *testing.T) {
	srv := newCatalogServer(t, newNamespacePolicyDatastore(t), nil)

	resp, err := srv.ValidateResources(context.Background(), &catalogv1.ValidateResourcesRequest{
		RepositoryId: testRepoID,
		Blobs: []*catalogv1.ResourceBlob{
			namespaceBlob("namespaces/other.md", "other", "USER"),
		},
	})

	require.NoError(t, err)
	assert.True(t, resp.Accepted)
}

type namespaceDeletedDuringUpdateStore struct {
	datastore.Datastore
	name        string
	updateCalls int
	createCalls int
}

func (s *namespaceDeletedDuringUpdateStore) CreateNamespace(ctx context.Context, namespace *datastore.Namespace) error {
	s.createCalls++
	return s.Datastore.CreateNamespace(ctx, namespace)
}

func (s *namespaceDeletedDuringUpdateStore) UpdateNamespace(ctx context.Context, namespace *datastore.Namespace, expectedResourceVersion string) error {
	s.updateCalls++
	current, err := s.Datastore.GetNamespaceByName(ctx, s.name)
	if err != nil {
		return err
	}
	if err := s.Datastore.DeleteNamespace(ctx, current.ID); err != nil {
		return err
	}
	return datastore.ErrConflict
}

func namespaceStructuralRejectionCount(t *testing.T, registry *prometheus.Registry) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	var total float64
	for _, family := range families {
		if family.GetName() != "gitstore_namespace_validation_rejections_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "phase" && label.GetValue() == string(namespaceadmission.PhaseStructural) {
					total += metric.GetCounter().GetValue()
				}
			}
		}
	}
	return total
}

func namespaceRejectionCount(t *testing.T, registry *prometheus.Registry, phase namespaceadmission.Phase, reason namespaceadmission.Reason) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != "gitstore_namespace_validation_rejections_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			matchesPhase := false
			matchesReason := false
			for _, label := range metric.GetLabel() {
				matchesPhase = matchesPhase || label.GetName() == "phase" && label.GetValue() == string(phase)
				matchesReason = matchesReason || label.GetName() == "reason" && label.GetValue() == string(reason)
			}
			if matchesPhase && matchesReason {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

func validationConstraints(resp *catalogv1.ValidateResourcesResponse) []string {
	out := make([]string, 0, len(resp.GetErrors()))
	for _, validationErr := range resp.GetErrors() {
		out = append(out, validationErr.GetField()+":"+validationErr.GetConstraint())
	}
	return out
}
