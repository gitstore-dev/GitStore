// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const accessReviewQuery = `query($input:AccessReviewInput!){
  accessReview(input:$input){subjectAccessReview{
    apiVersion kind spec{principal{subject groups} action resourceRef{id}}
    status{allowed reason hypothetical catalogDigest builtinDigest
      revisions{tierID namespaceUID gitRevision tierRevision capturedAt}}
  }}
}`

func accessReviewFixture(t *testing.T) (*Resolver, map[string]any) {
	t.Helper()
	root, _, _ := newRBACResourceFixture(t)
	node, rejected := rbacMutationRequest(t, root, "Create", "Role", map[string]any{
		"metadata": map[string]any{"name": "reader", "namespace": "shop"},
		"spec":     map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}}}},
	})
	require.Empty(t, rejected)
	return root, map[string]any{"spec": map[string]any{
		"principal": map[string]any{"subject": "alice", "groups": []string{}},
		"action":    "role.read", "resourceRef": map[string]any{"id": node["id"]},
	}}
}

func TestAccessReviewEnvelopeUsesAuthoritativeModelAndNeverChangesAuthority(t *testing.T) {
	root, input := accessReviewFixture(t)
	store, err := root.rbacStore()
	require.NoError(t, err)
	before, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	data, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Empty(t, rejected)
	review := data["accessReview"]["subjectAccessReview"].(map[string]any)
	require.Equal(t, "authorization.gitstore.dev/v1beta1", review["apiVersion"])
	require.Equal(t, "SubjectAccessReview", review["kind"])
	expected, err := json.Marshal(input["spec"])
	require.NoError(t, err)
	actual, err := json.Marshal(review["spec"])
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
	status := review["status"].(map[string]any)
	require.Equal(t, true, status["allowed"], "the ordinary-cache fixture denies: only authoritative pinning can allow")
	require.Equal(t, true, status["hypothetical"], "an explicit empty group list is hypothetical")
	require.Equal(t, auth.AuthorizationCatalogDigest(), status["catalogDigest"])
	require.Len(t, status["revisions"], 2)
	after, err := store.ReadRBACTier(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAccessReviewRejectsUnsupportedEnvelopeAndCanonicalAction(t *testing.T) {
	for _, field := range []string{"apiVersion", "kind", "action", "scope"} {
		t.Run(field, func(t *testing.T) {
			root, input := accessReviewFixture(t)
			switch field {
			case "apiVersion", "kind":
				input[field] = "unsupported"
			case "action":
				input["spec"].(map[string]any)["action"] = "unregistered.read"
			case "scope":
				input["spec"].(map[string]any)["action"] = "product.read"
			}
			_, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
			require.Len(t, rejected, 1)
			require.Equal(t, "BAD_USER_INPUT", rejected[0].Extensions["code"])
		})
	}
}

func TestAccessReviewReviewerCannotBorrowTargetGroupsOrDiscoverMissingTargets(t *testing.T) {
	root, input := accessReviewFixture(t)
	reviewer := &auth.Principal{Subject: "bob", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
	input["spec"].(map[string]any)["principal"] = map[string]any{"subject": "bob", "groups": []string{"system:masters"}}
	missing, err := EncodeNodeID("Role", uuid.NewString())
	require.NoError(t, err)
	for _, id := range []any{input["spec"].(map[string]any)["resourceRef"].(map[string]any)["id"], missing} {
		input["spec"].(map[string]any)["resourceRef"] = map[string]any{"id": id}
		_, rejected := managedContractRequest(t, root, reviewer, accessReviewQuery, input)
		require.Len(t, rejected, 1)
		require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	}
}

type reviewDirectory struct {
	auth.UserDirProvider
	calls int
	err   error
}

func (d *reviewDirectory) GetBySubject(context.Context, string) (*auth.UserProfile, error) {
	d.calls++
	return &auth.UserProfile{Subject: "alice", Username: "alice", Active: true, Kind: auth.ProfileStaff, Lifecycle: auth.ActorActive}, d.err
}
func (d *reviewDirectory) ListGroups(context.Context, string) ([]string, error) {
	return []string{"system:group:review-targets"}, d.err
}

func TestAccessReviewOmittedGroupsRequireTrustedDirectoryAndErrorsAreUnavailable(t *testing.T) {
	root, input := accessReviewFixture(t)
	directory := &reviewDirectory{}
	root.registry = auth.NewProviderRegistry(nil, root.registry.AuthZ(), directory)
	root.rbacValidator.Capture.Registry = root.registry
	delete(input["spec"].(map[string]any)["principal"].(map[string]any), "groups")
	data, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Empty(t, rejected)
	review := data["accessReview"]["subjectAccessReview"].(map[string]any)
	require.Equal(t, false, review["status"].(map[string]any)["hypothetical"])
	require.Positive(t, directory.calls)
	directory.err = errors.New("directory unavailable")
	_, rejected = rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Len(t, rejected, 1)
	require.Equal(t, "AUTHORIZATION_UNAVAILABLE", rejected[0].Extensions["code"])
	input["spec"].(map[string]any)["principal"].(map[string]any)["groups"] = []string{}
	calls := directory.calls
	_, rejected = rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Empty(t, rejected)
	require.Equal(t, calls, directory.calls, "explicit hypothetical groups do not query directory membership")
}

func TestAccessReviewReturnsRealDenialAndMarksProposedContextHypothetical(t *testing.T) {
	root, input := accessReviewFixture(t)
	spec := input["spec"].(map[string]any)
	spec["principal"] = map[string]any{"subject": "bob", "groups": []string{}}
	spec["context"] = map[string]any{"visibility": "public"}
	data, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Empty(t, rejected)
	status := data["accessReview"]["subjectAccessReview"].(map[string]any)["status"].(map[string]any)
	require.Equal(t, false, status["allowed"])
	require.Equal(t, true, status["hypothetical"])
	require.Len(t, status["revisions"], 2, "a denial still reports its actual model identity")
	for _, invalid := range []map[string]any{
		{"namespace": "other"}, {"visibility": []any{"public"}}, {"targetOwnerRef": map[string]any{"kind": "User"}},
	} {
		spec["context"] = invalid
		_, rejected = rbacGraphQLRequest(t, root, accessReviewQuery, input)
		require.Len(t, rejected, 1)
		require.Equal(t, "BAD_USER_INPUT", rejected[0].Extensions["code"])
	}
}

func TestAccessReviewNeverFallsBackFromMissingTrustedMembershipOrPolicy(t *testing.T) {
	for _, failure := range []error{auth.ErrProfileNotFound, auth.ErrNotSupported, auth.ErrDirectoryUnavailable} {
		t.Run(failure.Error(), func(t *testing.T) {
			root, input := accessReviewFixture(t)
			directory := &reviewDirectory{err: failure}
			root.registry = auth.NewProviderRegistry(nil, root.registry.AuthZ(), directory)
			root.rbacValidator.Capture.Registry = root.registry
			delete(input["spec"].(map[string]any)["principal"].(map[string]any), "groups")
			_, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
			require.Len(t, rejected, 1)
			require.Equal(t, "AUTHORIZATION_UNAVAILABLE", rejected[0].Extensions["code"])
		})
	}
	root, input := accessReviewFixture(t)
	root.rbacValidator.Capture.Reader.ReadOrigin = func(context.Context, string) (rbac.SnapshotOrigin, error) {
		return rbac.SnapshotOrigin{}, rbac.ErrUnavailable
	}
	_, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Len(t, rejected, 1)
	require.Equal(t, "AUTHORIZATION_UNAVAILABLE", rejected[0].Extensions["code"])
}

func TestAccessReviewScopedReviewerCannotReviewClusterOrSkipPermissionForSelf(t *testing.T) {
	root, input := accessReviewFixture(t)
	_, rejected := rbacMutationRequest(t, root, "Create", "Role", map[string]any{
		"metadata": map[string]any{"name": "reviewer", "namespace": "shop"},
		"spec":     map[string]any{"rules": []any{map[string]any{"resources": []string{"role/access-review"}, "verbs": []string{"create"}}}},
	})
	require.Empty(t, rejected)
	_, rejected = rbacMutationRequest(t, root, "Create", "RoleBinding", map[string]any{
		"metadata": map[string]any{"name": "reviewer", "namespace": "shop"},
		"spec": map[string]any{"roleRef": map[string]any{"kind": "ROLE", "name": "reviewer"},
			"subjects": []any{map[string]any{"kind": "USER", "name": "bob"}}},
	})
	require.Empty(t, rejected)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	store, err := root.rbacStore()
	require.NoError(t, err)
	key := rbac.ResourceKey{TierID: parent.UID, Kind: rbac.RoleBinding, Name: "reviewer"}
	binding, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	_, err = (rbac.References{Store: store, Builtins: root.rbacValidator.Builtins}).Resolve(t.Context(), key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
	require.NoError(t, err)
	reviewer := &auth.Principal{Subject: "bob", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
	_, rejected = managedContractRequest(t, root, reviewer, accessReviewQuery, input)
	require.Empty(t, rejected)
	var builtin *rbac.Resource
	for _, resource := range root.rbacBuiltinsResources {
		if resource.Kind == rbac.ClusterRole && resource.Metadata.Name == "view" {
			builtin = resource
		}
	}
	require.NotNil(t, builtin)
	id, err := EncodeNodeID("ClusterRole", builtin.Metadata.UID)
	require.NoError(t, err)
	spec := input["spec"].(map[string]any)
	spec["principal"] = map[string]any{"subject": "bob", "groups": []string{"system:masters"}}
	spec["resourceRef"], spec["action"] = map[string]any{"id": id}, "clusterRole.read"
	_, rejected = managedContractRequest(t, root, reviewer, accessReviewQuery, input)
	require.Len(t, rejected, 1)
	require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
}

type reviewLookupFailure struct{ rbac.Store }

func (s reviewLookupFailure) GetRBACByUID(context.Context, string) (*rbac.Resource, error) {
	return nil, errors.New("authoritative lookup unavailable")
}

func TestAccessReviewResourceLookupFailureIsNotAFabricatedDenial(t *testing.T) {
	root, input := accessReviewFixture(t)
	store, err := root.rbacStore()
	require.NoError(t, err)
	root.store = rbacLifecycleDatastore{Datastore: root.store, rbac: reviewLookupFailure{Store: store}}
	_, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Len(t, rejected, 1)
	require.Equal(t, "AUTHORIZATION_UNAVAILABLE", rejected[0].Extensions["code"])
}

func TestAccessReviewHasNoDurableNodeOrLifecycle(t *testing.T) {
	root, input := accessReviewFixture(t)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	store := root.store.(datastore.RBACCapable).RBACStore()
	before, err := store.ReadRBACTier(t.Context(), parent.UID)
	require.NoError(t, err)
	_, rejected := rbacGraphQLRequest(t, root, accessReviewQuery, input)
	require.Empty(t, rejected)
	after, err := store.ReadRBACTier(t.Context(), parent.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = EncodeNodeID("SubjectAccessReview", uuid.NewString())
	require.Error(t, err)
	data, rejected := rbacGraphQLRequest(t, root, `query {
	  __type(name:"SubjectAccessReview"){interfaces{name} fields{name}}
	}`, nil)
	require.Empty(t, rejected)
	require.Empty(t, data["__type"]["interfaces"])
	for _, field := range data["__type"]["fields"].([]any) {
		name := field.(map[string]any)["name"]
		require.NotContains(t, []string{"id", "metadata", "resourceVersion", "finalizers"}, name)
	}
}
