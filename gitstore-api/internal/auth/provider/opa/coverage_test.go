// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/stretchr/testify/require"
)

func coverageFixture(t *testing.T, clusterBinding bool, rules []rbac.Rule) (*Translation, *Translation) {
	t.Helper()
	cluster := Tier{ID: rbac.ClusterTierID, Active: true}
	namespace := Tier{ID: testUID("shop"), Namespace: "shop", Active: true}
	tier := &namespace
	roleKind, bindingKind := rbac.Role, rbac.RoleBinding
	if clusterBinding {
		tier, roleKind, bindingKind = &cluster, rbac.ClusterRole, rbac.ClusterRoleBinding
	}
	spec, err := json.Marshal(rbac.RoleSpec{Rules: rules})
	require.NoError(t, err)
	role := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: roleKind,
		Metadata: rbac.Metadata{Name: "delegator", Namespace: tier.Namespace, UID: testUID("delegator")}, Spec: spec}
	spec, err = json.Marshal(rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: roleKind, Name: "delegator"},
		Subjects: []rbac.Subject{{Kind: "User", Name: "alice"}}})
	require.NoError(t, err)
	binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: bindingKind,
		Metadata: rbac.Metadata{Name: "alice-access", Namespace: tier.Namespace, UID: testUID("alice-access")},
		Spec:     spec, Status: json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + role.Metadata.UID + `"}}}`)}
	if !clusterBinding {
		role.NamespaceUID, binding.NamespaceUID = namespace.ID, namespace.ID
	}
	tier.Resources = []*rbac.Resource{role, binding}
	c, err := Translate(cluster)
	require.NoError(t, err)
	n, err := Translate(namespace)
	require.NoError(t, err)
	return c, n
}

func TestConservativeCoverageScopeActionsNamesAndConditions(t *testing.T) {
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	for _, tc := range []struct {
		name        string
		cluster     bool
		global      bool
		rules       []rbac.Rule
		permissions []auth.AuthorizationPermission
		covered     bool
	}{
		{"equal namespace", false, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}},
			[]auth.AuthorizationPermission{{Action: "product.read"}}, true},
		{"broader cluster", true, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}},
			[]auth.AuthorizationPermission{{Action: "product.read"}}, true},
		{"namespace cannot prove global", false, true, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}},
			[]auth.AuthorizationPermission{{Action: "product.read"}}, false},
		{"all finite actions", true, false, []rbac.Rule{{Resources: []string{"*"}, Verbs: []string{"read"}}},
			[]auth.AuthorizationPermission{{Action: "product.read"}, {Action: "productVariant.read"}}, true},
		{"one uncovered action", true, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}},
			[]auth.AuthorizationPermission{{Action: "product.read"}, {Action: "product.purge"}}, false},
		{"name superset", false, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}, ResourceNames: []string{"widget", "gadget"}}},
			[]auth.AuthorizationPermission{{Action: "product.read", ResourceNames: []string{"widget"}}}, true},
		{"one uncovered name", false, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}, ResourceNames: []string{"widget"}}},
			[]auth.AuthorizationPermission{{Action: "product.read", ResourceNames: []string{"widget", "gadget"}}}, false},
		{"finite names cannot prove unrestricted", false, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}, ResourceNames: []string{"widget"}}},
			[]auth.AuthorizationPermission{{Action: "product.read"}}, false},
		{"owner is not transferable proof", false, false, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}, When: []rbac.Condition{{Token: "owner"}}}},
			[]auth.AuthorizationPermission{{Action: "product.read", ResourceNames: []string{"widget"}}}, false},
		{"named authority may be combined", false, false, []rbac.Rule{
			{Resources: []string{"product"}, Verbs: []string{"read"}, ResourceNames: []string{"widget"}},
			{Resources: []string{"product"}, Verbs: []string{"read"}, ResourceNames: []string{"gadget"}},
		}, []auth.AuthorizationPermission{{Action: "product.read", ResourceNames: []string{"widget", "gadget"}}}, true},
		{"invalid action", true, true, []rbac.Rule{{Resources: []string{"*"}, Verbs: []string{"*"}}},
			[]auth.AuthorizationPermission{{Action: "unknown.read"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cluster, namespace := coverageFixture(t, tc.cluster, tc.rules)
			request := auth.AuthorizationCoverageRequest{Permissions: tc.permissions}
			if !tc.global {
				request.Namespace, request.NamespaceUID = "shop", testUID("shop")
			}
			proof, err := engine.Covers(t.Context(), cluster, namespace,
				&auth.Principal{Subject: "alice", AuthMethod: "static-users"}, request)
			require.NoError(t, err)
			require.Equal(t, tc.covered, proof.Covered)
			require.Len(t, proof.PolicyDigest, 64)
		})
	}
}

func TestCoverageFailsClosedBeforeTrustedMastersAndSupportsConcurrentPins(t *testing.T) {
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	principal := &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	request := auth.AuthorizationCoverageRequest{Permissions: []auth.AuthorizationPermission{{Action: "product.read"}}}
	result, err := engine.Covers(t.Context(), nil, nil, principal, request)
	require.ErrorIs(t, err, ErrUnavailable)
	require.False(t, result.Covered)
	cluster, namespace := coverageFixture(t, true, []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = engine.Covers(ctx, cluster, namespace, principal, request)
	require.ErrorIs(t, err, ErrUnavailable)
	require.False(t, result.Covered)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			result, err := engine.Covers(t.Context(), cluster, namespace, principal, request)
			if err != nil || !result.Covered {
				t.Errorf("concurrent coverage: %v, %v", result, err)
			}
		})
	}
	wg.Wait()
}
