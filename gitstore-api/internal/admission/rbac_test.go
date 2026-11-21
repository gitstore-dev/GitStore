// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type admissionModelProvider struct{ *opa.Engine }

func (*admissionModelProvider) Name() string { return "opa" }
func (*admissionModelProvider) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Deny("ordinary-cache", "must not authorize candidates"), nil
}

func candidateFixture(t *testing.T, authority []rbac.Rule) (admission.RBACValidator, admission.RBACCandidate) {
	t.Helper()
	ds, err := memdb.New()
	require.NoError(t, err)
	store := ds.(datastore.RBACCapable).RBACStore()
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	origins := map[string]rbac.SnapshotOrigin{}
	namespaceUID := uuid.NewString()
	for _, tier := range []string{rbac.ClusterTierID, namespaceUID} {
		origin := rbac.SnapshotOrigin{NamespaceUID: tier, Namespace: "shop", NamespaceVersion: "1", Active: true,
			RepositoryID: uuid.NewString(), GitRef: "refs/heads/main", GitRevision: "main@sha1:" + strings.Repeat("a", 40), TreeDigest: strings.Repeat("b", 64)}
		if tier == rbac.ClusterTierID {
			origin.NamespaceUID, origin.Namespace, origin.Root = uuid.NewString(), "root", true
		}
		origins[tier] = origin
		_, err := store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: tier,
			Projection: &rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef, GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "fixture"}})
		require.NoError(t, err)
	}
	spec, err := json.Marshal(rbac.RoleSpec{Rules: authority})
	require.NoError(t, err)
	state, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	root := origins[rbac.ClusterTierID]
	root.GitRevision, root.TreeDigest = "main@sha1:"+strings.Repeat("c", 40), strings.Repeat("d", 64)
	origins[rbac.ClusterTierID] = root
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID, ExpectedRevision: state.Revision,
		ExpectedGitRevision: state.Projection.GitRevision,
		Projection: &rbac.ProjectionIdentity{RepositoryID: root.RepositoryID, GitRef: root.GitRef,
			GitRevision: root.GitRevision, TreeDigest: root.TreeDigest, Actor: "fixture"},
		Changes: []rbac.Change{
			{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
				Metadata: rbac.Metadata{Name: "operator", UID: uuid.NewString()}, Spec: spec}},
			{Resource: &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRoleBinding,
				Metadata: rbac.Metadata{Name: "operator-access", UID: uuid.NewString()},
				Spec:     json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"operator"},"subjects":[{"kind":"User","name":"alice"}]}`)}},
		}})
	require.NoError(t, err)
	key := rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "operator-access"}
	binding, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	_, err = (rbac.References{Store: store, Builtins: builtins}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	reader := rbac.SnapshotReader{Store: store, CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtins.Digest(),
		ReadOrigin: func(_ context.Context, tier string) (rbac.SnapshotOrigin, error) { return origins[tier], nil }}
	scope := rbac.RepositoryScope{Namespace: "shop", NamespaceUID: namespaceUID}
	origin := origins[namespaceUID]
	return admission.RBACValidator{Capture: rbac.ValidationCapture{Reader: reader, Registry: auth.NewProviderRegistry(nil, &admissionModelProvider{engine}, nil)}, Builtins: builtins, ManagedStore: ds},
		admission.RBACCandidate{Scope: scope, RepositoryID: origin.RepositoryID, GitRef: origin.GitRef, ExpectedGitRevision: origin.GitRevision}
}

func roleChange(t *testing.T, candidate admission.RBACCandidate, name string, rules []rbac.Rule) admission.RBACManifestChange {
	t.Helper()
	spec, err := json.Marshal(rbac.RoleSpec{Rules: rules})
	require.NoError(t, err)
	resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.Role, Metadata: rbac.Metadata{Name: name, Namespace: candidate.Scope.Namespace}, Spec: spec}
	if candidate.Scope.Root {
		resource.Kind, resource.Metadata.Namespace = rbac.ClusterRole, ""
	}
	body, err := rbac.CanonicalManifest(resource)
	require.NoError(t, err)
	path, err := rbac.CanonicalPath(resource.Kind, name)
	require.NoError(t, err)
	return admission.RBACManifestChange{Path: path, Content: body}
}

func TestRBACReviewBindingUsesUnchangedFinalTreeRoleWithoutSelfAuthorization(t *testing.T) {
	for _, verb := range []string{"read", "purge"} {
		t.Run(verb, func(t *testing.T) {
			validator, candidate := candidateFixture(t, []rbac.Rule{
				{Resources: []string{"roleBinding"}, Verbs: []string{"create"}},
				{Resources: []string{"product"}, Verbs: []string{"read"}},
			})
			candidate.GitRef = "refs/heads/review"
			role := roleChange(t, candidate, "review-target", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{verb}}})
			binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding,
				Metadata: rbac.Metadata{Name: "review-access", Namespace: candidate.Scope.Namespace},
				Spec:     json.RawMessage(`{"roleRef":{"kind":"Role","name":"review-target"},"subjects":[{"kind":"User","name":"future-user"}]}`)}
			body, err := rbac.CanonicalManifest(binding)
			require.NoError(t, err)
			path, err := rbac.CanonicalPath(binding.Kind, binding.Metadata.Name)
			require.NoError(t, err)
			candidate.Changes = []admission.RBACManifestChange{{Path: path, Content: body}}
			candidate.FinalTree = map[string][]byte{path: body, role.Path: role.Content}
			result, err := validator.Validate(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, candidate)
			if verb == "read" {
				require.NoError(t, err)
				require.False(t, result.Canonical)
				result.Close()
			} else {
				require.Error(t, err, "an unchanged review role cannot supply the actor's authority")
				require.Nil(t, result)
			}
		})
	}
}

func TestRBACCommonAdmissionConservativeDelegation(t *testing.T) {
	base := []rbac.Rule{
		{Resources: []string{"role", "roleBinding"}, Verbs: []string{"create", "update", "delete"}},
		{Resources: []string{"product"}, Verbs: []string{"read"}},
	}
	for _, tc := range []struct {
		name     string
		verbs    []string
		when     []rbac.Condition
		explicit bool
		allowed  bool
	}{
		{"equal covered grant", []string{"read"}, nil, false, true},
		{"unconditional covers conditional candidate", []string{"read"}, []rbac.Condition{{Token: "owner"}}, false, true},
		{"purge is not covered", []string{"purge"}, nil, false, false},
		{"explicit escalation", []string{"purge"}, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			authority := append([]rbac.Rule{}, base...)
			if tc.explicit {
				authority = append(authority, rbac.Rule{Resources: []string{"role"}, Verbs: []string{"escalate"}})
			}
			validator, candidate := candidateFixture(t, authority)
			candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "delegated", []rbac.Rule{{Resources: []string{"product"}, Verbs: tc.verbs, When: tc.when}})}
			before, err := validator.Capture.Reader.Store.ReadRBACTier(t.Context(), candidate.Scope.NamespaceUID)
			require.NoError(t, err)
			result, err := validator.Validate(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, candidate)
			if tc.allowed {
				require.NoError(t, err)
				defer result.Close()
				require.Len(t, result.Write.Changes, 1)
				require.Empty(t, result.Write.Changes[0].Resource.Status)
			} else {
				require.Nil(t, result)
				var rejected *admission.Error
				require.ErrorAs(t, err, &rejected)
				require.Equal(t, admission.CodeForbidden, rejected.Code)
			}
			after, err := validator.Capture.Reader.Store.ReadRBACTier(t.Context(), candidate.Scope.NamespaceUID)
			require.NoError(t, err)
			require.Equal(t, before, after, "validation must not publish rows, receipts or reference guards")
		})
	}
}

func TestRBACCommonAdmissionAbsentRoleNeedsBindAndNeverResolvesSpeculatively(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		authority := []rbac.Rule{{Resources: []string{"roleBinding"}, Verbs: []string{"create"}}}
		if explicit {
			authority = append(authority, rbac.Rule{Resources: []string{"roleBinding"}, Verbs: []string{"bind"}})
		}
		validator, candidate := candidateFixture(t, authority)
		candidate.Changes = []admission.RBACManifestChange{{Path: "rbac/rolebindings/editors.yaml",
			Content: []byte("apiVersion: " + rbac.APIVersion + "\nkind: RoleBinding\nmetadata: {name: editors, namespace: shop}\nspec:\n  roleRef: {kind: Role, name: absent}\n  subjects: [{kind: Group, name: 'system:group:editors'}]\n")}}
		result, err := validator.Validate(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, candidate)
		if explicit {
			require.NoError(t, err)
			require.Empty(t, result.Write.Changes[0].Resource.Status)
			var owners []struct{ BlockOwnerDeletion bool }
			require.NoError(t, json.Unmarshal(result.Write.Changes[0].Resource.Metadata.OwnerReferences, &owners))
			for _, owner := range owners {
				require.False(t, owner.BlockOwnerDeletion, "unresolved targets must not acquire a blocking role link")
			}
			result.Close()
		} else {
			require.Nil(t, result)
			require.Error(t, err)
		}
	}
}

func TestRBACCommonAdmissionRejectsWholeCandidateAndSelfGrants(t *testing.T) {
	validator, candidate := candidateFixture(t, []rbac.Rule{
		{Resources: []string{"role", "roleBinding"}, Verbs: []string{"create"}},
		{Resources: []string{"product"}, Verbs: []string{"read"}},
	})
	good := roleChange(t, candidate, "reader", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})
	bad := roleChange(t, candidate, "self-admin", []rbac.Rule{{Resources: []string{"*"}, Verbs: []string{"*"}}})
	candidate.Changes = []admission.RBACManifestChange{good, bad, {Path: "rbac/rolebindings/self.yaml",
		Content: []byte("apiVersion: " + rbac.APIVersion + "\nkind: RoleBinding\nmetadata: {name: self, namespace: shop}\nspec:\n  roleRef: {kind: Role, name: self-admin}\n  subjects: [{kind: Group, name: 'system:authenticated'}]\n")}}
	result, err := validator.Validate(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}, candidate)
	require.Nil(t, result)
	require.Error(t, err)
	_, err = validator.Capture.Reader.Store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: candidate.Scope.NamespaceUID, Kind: rbac.Role, Name: "reader"})
	require.ErrorIs(t, err, rbac.ErrNotFound)
}

func commitCandidateFixture(t *testing.T, validator *admission.RBACValidator, candidate *admission.RBACCandidate, result *admission.RBACValidatedCandidate) {
	t.Helper()
	origin := result.Snapshot.Origin
	origin.GitRevision = "main@sha1:" + strings.ReplaceAll(uuid.NewString(), "-", "") + strings.Repeat("e", 8)
	origin.TreeDigest = strings.ReplaceAll(uuid.NewString(), "-", "") + strings.Repeat("f", 32)
	write := result.Write
	write.Projection = &rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
		GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: "alice"}
	committed, err := admission.ProjectCommittedRBAC(t.Context(), validator.Capture.Reader.Store, write)
	require.NoError(t, err)
	require.False(t, committed.ProjectionPending)
	readOrigin := validator.Capture.Reader.ReadOrigin
	validator.Capture.Reader.ReadOrigin = func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
		if tier == result.Write.TierID {
			return origin, nil
		}

		return readOrigin(ctx, tier)
	}
	candidate.ExpectedGitRevision = origin.GitRevision
	result.Close()
}

func TestRBACExplicitCreateAndUpdateDoNotBecomeUpserts(t *testing.T) {
	validator, candidate := candidateFixture(t, []rbac.Rule{
		{Resources: []string{"role"}, Verbs: []string{"create", "update", "escalate"}},
	})
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
	change := roleChange(t, candidate, "reader", nil)
	change.Operation = admission.OperationUpdate
	candidate.Changes = []admission.RBACManifestChange{change}
	result, err := validator.Validate(t.Context(), principal, candidate)
	require.Nil(t, result)
	var rejected *admission.Error
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, admission.CodeNotFound, rejected.Code)
	change.Operation = admission.OperationCreate
	candidate.Changes[0] = change
	result, err = validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	commitCandidateFixture(t, &validator, &candidate, result)
	result, err = validator.Validate(t.Context(), principal, candidate)
	require.Nil(t, result)
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, admission.CodeAlreadyExists, rejected.Code)
}

func TestRBACCommonAdmissionUpdateDeleteAndOptimisticIdentity(t *testing.T) {
	validator, candidate := candidateFixture(t, []rbac.Rule{
		{Resources: []string{"role"}, Verbs: []string{"create", "update", "delete"}},
		{Resources: []string{"product"}, Verbs: []string{"read"}},
	})
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	rules := []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}}
	candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "delegated", rules)}
	result, err := validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	commitCandidateFixture(t, &validator, &candidate, result)
	key := rbac.ResourceKey{TierID: candidate.Scope.NamespaceUID, Kind: rbac.Role, Name: "delegated"}
	before, err := validator.Capture.Reader.Store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	rules[0].ResourceNames = []string{"widget"}
	update := roleChange(t, candidate, key.Name, rules)
	update.ExpectedUID, update.ExpectedResourceVersion = before.Metadata.UID, "stale"
	candidate.Changes = []admission.RBACManifestChange{update}
	_, err = validator.Validate(t.Context(), principal, candidate)
	var conflict *admission.Error
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, admission.CodeConflict, conflict.Code)
	candidate.Changes[0].ExpectedResourceVersion = before.Metadata.ResourceVersion
	result, err = validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	require.Equal(t, before.Metadata.UID, result.Write.Changes[0].Resource.Metadata.UID)
	commitCandidateFixture(t, &validator, &candidate, result)
	current, err := validator.Capture.Reader.Store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	candidate.Changes = []admission.RBACManifestChange{{Path: update.Path, Content: []byte{}}}
	empty, err := validator.Validate(t.Context(), principal, candidate)
	if empty != nil {
		empty.Close()
	}
	require.Nil(t, empty, "an empty file is invalid YAML, not author deletion intent")
	require.Error(t, err)
	candidate.Changes = []admission.RBACManifestChange{{Path: update.Path, Delete: true, ExpectedUID: current.Metadata.UID, ExpectedResourceVersion: current.Metadata.ResourceVersion}}
	result, err = validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	require.True(t, result.Write.Changes[0].Terminate)
	require.Nil(t, current.Metadata.DeletionTimestamp, "validation must not close acquisition before commit")
	commitCandidateFixture(t, &validator, &candidate, result)
	deleted, err := validator.Capture.Reader.Store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	require.NotNil(t, deleted.Metadata.DeletionTimestamp)
	require.Equal(t, current.Metadata.Generation, deleted.Metadata.Generation)
}

type prooflessEvaluation struct{ auth.PinnedAuthorization }
type prooflessProvider struct{ *admissionModelProvider }

func (p *prooflessProvider) PinAuthorization(ctx context.Context, model auth.AuthorizationModel) (auth.PinnedAuthorization, error) {
	evaluation, err := p.Engine.PinAuthorization(ctx, model)
	if err != nil {
		return nil, err
	}
	return &prooflessEvaluation{evaluation}, nil
}

func TestRBACCommonAdmissionRequiresExplicitPrivilegeWithoutCoverage(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		authority := []rbac.Rule{
			{Resources: []string{"role"}, Verbs: []string{"create"}},
			{Resources: []string{"product"}, Verbs: []string{"read"}},
		}
		if explicit {
			authority = append(authority, rbac.Rule{Resources: []string{"role"}, Verbs: []string{"escalate"}})
		}
		validator, candidate := candidateFixture(t, authority)
		provider := validator.Capture.Registry.AuthZ().(*admissionModelProvider)
		validator.Capture.Registry.Swap(nil, &prooflessProvider{provider}, nil)
		candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "delegated", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})}
		result, err := validator.Validate(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, candidate)
		if explicit {
			require.NoError(t, err)
			result.Close()
		} else {
			require.Nil(t, result)
			require.Error(t, err)
		}
	}
}

func TestRBACCommonAdmissionCannotChangeBindingRoleRef(t *testing.T) {
	validator, candidate := candidateFixture(t, []rbac.Rule{{Resources: []string{"roleBinding"}, Verbs: []string{"create", "update", "bind"}}})
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	body := "apiVersion: " + rbac.APIVersion + "\nkind: RoleBinding\nmetadata: {name: editors, namespace: shop}\nspec:\n  roleRef: {kind: Role, name: absent}\n  subjects: [{kind: Group, name: 'system:group:editors'}]\n"
	candidate.Changes = []admission.RBACManifestChange{{Path: "rbac/rolebindings/editors.yaml", Content: []byte(body)}}
	result, err := validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	commitCandidateFixture(t, &validator, &candidate, result)
	candidate.Changes[0].Content = []byte(strings.Replace(body, "name: absent", "name: another", 1))
	result, err = validator.Validate(t.Context(), principal, candidate)
	require.Nil(t, result)
	var rejected *admission.Error
	require.ErrorAs(t, err, &rejected)
	require.Equal(t, "RBAC_RESOURCE_PROTECTED", rejected.Reason())
}

func TestRBACCommonAdmissionWriteCanFinishAfterIndependentRevocation(t *testing.T) {
	validator, candidate := candidateFixture(t, []rbac.Rule{
		{Resources: []string{"role"}, Verbs: []string{"create"}},
		{Resources: []string{"product"}, Verbs: []string{"read"}},
	})
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "before-revocation", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})}
	result, err := validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	defer result.Close()
	store := validator.Capture.Reader.Store
	root, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.NotEqual(t, root.Projection.RepositoryID, candidate.RepositoryID)
	binding, err := store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "operator-access"})
	require.NoError(t, err)
	binding.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"operator"},"subjects":[{"kind":"User","name":"bob"}]}`)
	projection := root.Projection
	projection.GitRevision, projection.TreeDigest = "main@sha1:"+strings.Repeat("e", 40), strings.Repeat("f", 64)
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: rbac.ClusterTierID, ExpectedRevision: root.Revision,
		ExpectedGitRevision: root.Projection.GitRevision, Projection: &projection,
		Changes: []rbac.Change{{Resource: binding, ExpectedUID: binding.Metadata.UID, ExpectedResourceVersion: binding.Metadata.ResourceVersion}}})
	require.NoError(t, err)
	readOrigin := validator.Capture.Reader.ReadOrigin
	validator.Capture.Reader.ReadOrigin = func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
		origin, err := readOrigin(ctx, tier)
		if tier == rbac.ClusterTierID {
			origin.GitRevision, origin.TreeDigest = projection.GitRevision, projection.TreeDigest
		}
		return origin, err
	}
	require.NoError(t, result.Validation.Check(t.Context()))
	commitCandidateFixture(t, &validator, &candidate, result)
	_, err = store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: candidate.Scope.NamespaceUID, Kind: rbac.Role, Name: "before-revocation"})
	require.NoError(t, err, "there is deliberately no cross-repository commit fence")
	candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "after-revocation", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})}
	next, err := validator.Validate(t.Context(), principal, candidate)
	require.Nil(t, next)
	var denied *admission.Error
	require.ErrorAs(t, err, &denied)
	require.Equal(t, admission.CodeForbidden, denied.Code)
}

type terminatingTargetProvider struct {
	*admissionModelProvider
	afterPin func()
}

func (p *terminatingTargetProvider) PinAuthorization(ctx context.Context, model auth.AuthorizationModel) (auth.PinnedAuthorization, error) {
	evaluation, err := p.Engine.PinAuthorization(ctx, model)
	if err == nil {
		p.afterPin()
	}
	return evaluation, err
}

func TestRBACCommonAdmissionTargetTerminatingAfterCaptureStaysUnresolved(t *testing.T) {
	validator, candidate := candidateFixture(t, []rbac.Rule{
		{Resources: []string{"role", "roleBinding"}, Verbs: []string{"create"}},
		{Resources: []string{"product"}, Verbs: []string{"read"}},
	})
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users"}
	candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "reader", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})}
	result, err := validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	commitCandidateFixture(t, &validator, &candidate, result)
	store := validator.Capture.Reader.Store
	key := rbac.ResourceKey{TierID: candidate.Scope.NamespaceUID, Kind: rbac.Role, Name: "reader"}
	role, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	beforeGuard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	provider := validator.Capture.Registry.AuthZ().(*admissionModelProvider)
	validator.Capture.Registry.Swap(nil, &terminatingTargetProvider{provider, func() {
		state, err := store.ReadRBACTier(t.Context(), key.TierID)
		require.NoError(t, err)
		now := time.Now().UTC()
		role.Metadata.DeletionTimestamp = &now
		_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
			Changes: []rbac.Change{{Resource: role, ExpectedUID: role.Metadata.UID, ExpectedResourceVersion: role.Metadata.ResourceVersion}}})
		require.NoError(t, err)
	}}, nil)
	candidate.Changes = []admission.RBACManifestChange{{Path: "rbac/rolebindings/editors.yaml",
		Content: []byte("apiVersion: " + rbac.APIVersion + "\nkind: RoleBinding\nmetadata: {name: editors, namespace: shop}\nspec:\n  roleRef: {kind: Role, name: reader}\n  subjects: [{kind: Group, name: 'system:group:editors'}]\n")}}
	result, err = validator.Validate(t.Context(), principal, candidate)
	require.NoError(t, err)
	require.Empty(t, result.Write.Changes[0].Resource.Status)
	commitCandidateFixture(t, &validator, &candidate, result)
	bindingKey := rbac.ResourceKey{TierID: key.TierID, Kind: rbac.RoleBinding, Name: "editors"}
	binding, err := store.GetRBAC(t.Context(), bindingKey)
	require.NoError(t, err)
	binding, err = (rbac.References{Store: store, Builtins: validator.Builtins}).Resolve(t.Context(), bindingKey, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	uid, err := rbac.ResolvedRoleUID(binding)
	require.NoError(t, err)
	require.Empty(t, uid)
	guard, err := store.ReadRBACRoleGuard(t.Context(), role.Metadata.UID)
	require.NoError(t, err)
	require.Equal(t, beforeGuard, guard, "validation must not pre-acquire a target claim")
}
func TestRBACCommonAdmissionProtectsScopeWithoutDirectoryExistenceChecks(t *testing.T) {
	authority := []rbac.Rule{
		{Resources: []string{"roleBinding"}, Verbs: []string{"create", "bind"}},
		{Resources: []string{"clusterRole"}, Verbs: []string{"create", "escalate"}},
	}
	for _, test := range []string{"virtual builtin", "wrong repository", "missing directory", "managed identity"} {
		t.Run(test, func(t *testing.T) {
			validator, candidate := candidateFixture(t, authority)
			subject := "{kind: Group, name: 'system:group:editors'}"
			name := "editors"
			if test == "missing directory" {
				subject = "{kind: User, name: 'missing-user'}"
			}
			if test == "managed identity" {
				name = rbac.ManagedBindingName
			}
			candidate.Changes = []admission.RBACManifestChange{{Path: "rbac/rolebindings/" + name + ".yaml",
				Content: []byte("apiVersion: " + rbac.APIVersion + "\nkind: RoleBinding\nmetadata: {name: " + name + ", namespace: shop}\nspec:\n  roleRef: {kind: Role, name: absent}\n  subjects: [" + subject + "]\n")}}
			if test == "wrong repository" {
				candidate.RepositoryID = uuid.NewString()
			}
			if test == "virtual builtin" {
				root, err := validator.Capture.Reader.ReadOrigin(t.Context(), rbac.ClusterTierID)
				require.NoError(t, err)
				candidate.Scope = rbac.RepositoryScope{Root: true, Namespace: root.Namespace, NamespaceUID: root.NamespaceUID}
				candidate.RepositoryID, candidate.ExpectedGitRevision = root.RepositoryID, root.GitRevision
				candidate.Changes = []admission.RBACManifestChange{roleChange(t, candidate, "edit", []rbac.Rule{})}
			}
			result, err := validator.Validate(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, candidate)
			if test == "missing directory" {
				require.NoError(t, err, "ordinary subjects do not require directory existence")
				result.Close()
				return
			}
			require.Nil(t, result)
			require.Error(t, err)
			if test == "managed identity" {
				require.ErrorIs(t, err, rbac.ErrUnavailable)
			}
		})
	}
}
