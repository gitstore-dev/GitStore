// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type neutralFixture struct {
	Policies map[string]map[string]any
	Cases    []struct {
		ID, Policy, Action string
		Principal          struct {
			Subject, AuthMethod, ServiceAccountUID string
			Groups                                 []string
		}
		Resource struct {
			Kind, Name, OwnerSub string
			Attrs                map[string]any
		}
		Expected struct{ Outcome, Response string }
	}
}

func testUID(value string) string { return uuid.NewSHA1(uuid.NameSpaceOID, []byte(value)).String() }

func TestReleaseBuiltinsPreserveTrustedAndScopedAuthority(t *testing.T) {
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	cluster, err := Translate(Tier{ID: rbac.ClusterTierID, Active: true, Builtins: builtins})
	require.NoError(t, err)
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	for _, test := range []struct {
		name, subject, group, username, action, kind, resource string
		profile                                                auth.ProfileKind
		attrs                                                  map[string]any
		allow                                                  bool
	}{
		{name: "staff-own-user-namespace", subject: "idp-alice", group: "system:staff", username: "alice", profile: auth.ProfileStaff,
			action: "namespace.create", kind: "namespace", resource: "idp-alice", attrs: map[string]any{"tier": "USER"}, allow: true},
		{name: "not-directory-username", subject: "idp-alice", group: "system:staff", username: "alice", profile: auth.ProfileStaff,
			action: "namespace.create", kind: "namespace", resource: "alice", attrs: map[string]any{"tier": "USER"}},
		{name: "not-another-username", subject: "idp-alice", group: "system:staff", username: "alice", profile: auth.ProfileStaff,
			action: "namespace.create", kind: "namespace", resource: "bob", attrs: map[string]any{"tier": "USER"}},
		{name: "not-organization", subject: "idp-alice", group: "system:staff", username: "alice", profile: auth.ProfileStaff,
			action: "namespace.create", kind: "namespace", resource: "idp-alice", attrs: map[string]any{"tier": "ORGANIZATION"}},
		{name: "authenticated-is-not-staff", subject: "idp-alice", group: "system:authenticated", username: "alice", profile: auth.ProfileCustomer,
			action: "namespace.create", kind: "namespace", resource: "idp-alice", attrs: map[string]any{"tier": "USER"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := engine.Evaluate(t.Context(), cluster, nil,
				&auth.Principal{Subject: test.subject, AuthMethod: "static-users", Groups: []string{test.group},
					Username: test.username, ProfileKind: test.profile},
				test.action, auth.ResourceContext{Kind: test.kind, Name: test.resource, Attrs: test.attrs})
			require.NoError(t, err)
			require.Equal(t, test.allow, decision.Outcome == auth.OutcomeAllow)
		})
	}
}

func TestReleaseBuiltinNamespaceDelegationAndQuotaBoundaries(t *testing.T) {
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	cluster, err := Translate(Tier{ID: rbac.ClusterTierID, Active: true, Builtins: builtins})
	require.NoError(t, err)
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	for _, test := range []struct {
		role, action, kind string
		allow              bool
	}{
		{"admin", "role.create", "role", true},
		{"admin", "role.access-review.create", "role", true},
		{"admin", "role.escalate", "role", false},
		{"admin", "roleBinding.bind", "roleBinding", false},
		{"admin", "clusterRole.read", "clusterRole", false},
		{"view", "role.read", "role", false},
		{"view", "role.access-review.create", "role", false},
		{"edit", "role.create", "role", false},
		{"namespace-full-control", "roleBinding.bind", "roleBinding", true},
		{"namespace-full-control", "namespace.read", "namespace", false},
	} {
		role, err := builtins.Get(rbac.ClusterRole, test.role)
		require.NoError(t, err)
		tier := Tier{ID: testUID("shop"), Namespace: "shop", Active: true}
		spec, _ := json.Marshal(rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: rbac.ClusterRole, Name: test.role},
			Subjects: []rbac.Subject{{Kind: "User", Name: "alice"}}})
		status, _ := json.Marshal(map[string]any{"resolved": map[string]any{"roleRef": map[string]string{"uid": role.Metadata.UID}}})
		tier.Resources = []*rbac.Resource{{APIVersion: rbac.APIVersion, Kind: rbac.RoleBinding, NamespaceUID: tier.ID,
			Metadata: rbac.Metadata{Name: "alice-access", Namespace: tier.Namespace, UID: testUID("binding")},
			Spec:     spec, Status: status}}
		namespace, err := Translate(tier)
		require.NoError(t, err)
		decision, err := engine.Evaluate(t.Context(), cluster, namespace, &auth.Principal{Subject: "alice", AuthMethod: "static-users"},
			test.action, auth.ResourceContext{Kind: test.kind, Name: "reader", Attrs: map[string]any{"namespace": "shop"}})
		require.NoError(t, err)
		require.Equal(t, test.allow, decision.Outcome == auth.OutcomeAllow, "%s %s", test.role, test.action)
	}
	tier := Tier{ID: rbac.ClusterTierID, Active: true, Builtins: builtins}
	for index := 0; index < rbac.MaxRolesPerTier; index++ {
		name := fmt.Sprintf("custom-%03d", index)
		tier.Resources = append(tier.Resources, &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
			Metadata: rbac.Metadata{Name: name, UID: testUID(name)}, Spec: json.RawMessage(`{"rules":[]}`)})
	}
	_, err = Translate(tier)
	require.NoError(t, err, "release definitions must not consume operator quotas")
	forged, err := builtins.Get(rbac.ClusterRole, "system:api-internal")
	require.NoError(t, err)
	forged.Metadata.Labels = map[string]string{"builtin": "true"}
	_, err = Translate(Tier{ID: rbac.ClusterTierID, Active: true, Resources: []*rbac.Resource{forged}})
	require.ErrorIs(t, err, ErrUnavailable, "labels cannot activate the trusted release-only parser path")
}

func fixtureTiers(t *testing.T, raw map[string]any) map[string]Tier {
	t.Helper()
	data, err := yaml.Marshal(raw)
	require.NoError(t, err)
	var policy rbaclocal.Policy
	require.NoError(t, yaml.Unmarshal(data, &policy))
	tiers := map[string]Tier{"": {ID: rbac.ClusterTierID, Active: true}}
	role := func(namespace, name string, definition rbaclocal.RoleDefinition, cluster bool) {
		// Static fixture identifiers are not Kubernetes-style resource names.
		name = strings.ToLower(name)
		if _, ok := tiers[namespace]; !ok {
			tiers[namespace] = Tier{ID: testUID(namespace), Namespace: namespace, Active: true}
		}
		tier := tiers[namespace]
		kind := rbac.Role
		if cluster {
			kind = rbac.ClusterRole
		}
		spec := rbac.RoleSpec{Rules: []rbac.Rule{}}
		for _, rule := range definition.Rules {
			value := rbac.Rule{Resources: rule.Resources, Verbs: rule.Verbs, ResourceNames: rule.ResourceNames}
			for _, condition := range rule.When {
				value.When = append(value.When, rbac.Condition(condition))
			}
			spec.Rules = append(spec.Rules, value)
		}
		body, err := json.Marshal(spec)
		require.NoError(t, err)
		resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: kind,
			Metadata: rbac.Metadata{Name: name, Namespace: namespace, UID: testUID(namespace + "/" + string(kind) + "/" + name)},
			Spec:     body}
		if !cluster {
			resource.NamespaceUID = tier.ID
		}
		tier.Resources = append(tier.Resources, resource)
		tiers[namespace] = tier
	}
	for name, definition := range policy.ClusterRoles {
		role("", name, definition, true)
	}
	namespaces := map[string]bool{}
	for _, binding := range policy.RoleBindings {
		require.NotNil(t, binding.Namespace)
		namespaces[*binding.Namespace] = true
	}
	for namespace := range namespaces {
		tiers[namespace] = Tier{ID: testUID(namespace), Namespace: namespace, Active: true}
		for name, definition := range policy.Roles {
			role(namespace, name, definition, false)
		}
	}
	binding := func(index int, value rbaclocal.Binding, cluster bool) {
		namespace, roleNamespace := "", ""
		kind := rbac.ClusterRoleBinding
		if !cluster {
			namespace, kind = *value.Namespace, rbac.RoleBinding
		}
		if value.RoleRef.Kind == "Role" {
			roleNamespace = namespace
		}
		roleName := strings.ToLower(value.RoleRef.Name)
		spec := rbac.BindingSpec{RoleRef: rbac.RoleRef{Kind: rbac.Kind(value.RoleRef.Kind), Name: roleName}}
		for _, subject := range value.Subjects {
			spec.Subjects = append(spec.Subjects, rbac.Subject{Kind: subject.Kind, Name: subject.Name})
		}
		body, err := json.Marshal(spec)
		require.NoError(t, err)
		name := "binding-" + testUID(namespace + string(kind) + strconv.Itoa(index))[:8]
		roleUID := testUID(roleNamespace + "/" + value.RoleRef.Kind + "/" + roleName)
		status, err := json.Marshal(map[string]any{"resolved": map[string]any{"roleRef": map[string]any{"uid": roleUID}}})
		require.NoError(t, err)
		resource := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: kind, Spec: body, Status: status,
			Metadata: rbac.Metadata{Name: name, Namespace: namespace, UID: testUID(namespace + "/" + string(kind) + "/" + name)}}
		tier := tiers[namespace]
		if !cluster {
			resource.NamespaceUID = tier.ID
		}
		tier.Resources = append(tier.Resources, resource)
		tiers[namespace] = tier
	}
	for index, value := range policy.ClusterRoleBindings {
		binding(index, value, true)
	}
	for index, value := range policy.RoleBindings {
		binding(index, value, false)
	}
	return tiers
}

func TestNeutralDecisionParity(t *testing.T) {
	data, err := os.ReadFile("../../../../../tests/fixtures/authorization/decision-table.json")
	require.NoError(t, err)
	original, err := os.ReadFile("../../../../../specs/064-authz-action-vocabulary/contracts/decision-table.json")
	require.NoError(t, err)
	require.Equal(t, original, data)
	var fixture neutralFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.Len(t, fixture.Cases, 55)
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	policies := map[string]map[string]*Translation{}
	for name, policy := range fixture.Policies {
		policies[name] = map[string]*Translation{}
		for namespace, tier := range fixtureTiers(t, policy) {
			policies[name][namespace], err = Translate(tier)
			require.NoError(t, err, "policy=%s namespace=%s", name, namespace)
		}
	}
	for _, test := range fixture.Cases {
		t.Run(test.ID, func(t *testing.T) {
			namespace, _ := test.Resource.Attrs["namespace"].(string)
			selected := policies[test.Policy][namespace]
			if namespace != "" && selected == nil {
				selected, err = Translate(Tier{ID: testUID(namespace), Namespace: namespace, Active: true})
				require.NoError(t, err)
			}
			if namespace == "" {
				selected = nil
			}
			decision, err := engine.Evaluate(t.Context(), policies[test.Policy][""], selected,
				&auth.Principal{Subject: test.Principal.Subject, AuthMethod: test.Principal.AuthMethod,
					ServiceAccountUID: test.Principal.ServiceAccountUID, Groups: test.Principal.Groups},
				test.Action, auth.ResourceContext{Kind: test.Resource.Kind, Name: test.Resource.Name,
					OwnerSub: test.Resource.OwnerSub, Attrs: test.Resource.Attrs})
			require.NoError(t, err)
			require.Equal(t, test.Expected.Outcome == "allow", decision.Outcome == auth.OutcomeAllow)
			require.NotEmpty(t, decision.Reason)
			if decision.Outcome == auth.OutcomeAllow && test.ID != "masters-trusted-group" {
				for _, diagnostic := range []string{"binding=", "role=", "rule="} {
					require.Contains(t, decision.Reason, diagnostic)
				}
			}
		})
	}
}

func TestUnavailableNeverAllowsMasters(t *testing.T) {
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	cluster, err := Translate(Tier{ID: rbac.ClusterTierID, Active: true})
	require.NoError(t, err)
	inactive, err := Translate(Tier{ID: testUID("acme"), Namespace: "acme"})
	require.NoError(t, err)
	for _, test := range []struct {
		root, namespace *Translation
		name            string
	}{{nil, nil, ""}, {cluster, nil, "acme"}, {cluster, inactive, "acme"}} {
		decision, err := engine.Evaluate(t.Context(), test.root, test.namespace,
			&auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:masters"}},
			"product.read", auth.ResourceContext{Kind: "product", Attrs: map[string]any{"namespace": test.name}})
		require.ErrorIs(t, err, ErrUnavailable)
		require.Equal(t, auth.OutcomeDeny, decision.Outcome)
	}
}

func TestUndefinedMalformedAndErrorResultsAreUnavailable(t *testing.T) {
	cluster, err := Translate(Tier{ID: rbac.ClusterTierID, Active: true})
	require.NoError(t, err)
	for _, query := range []string{
		"data.absent.decision", `decision := {"allow":"true"}`, `decision := {"allow":true,"binding":1}`, `decision := 1 / 0`,
		`decision := {"allow":true,"masters":false,"binding":"invalid","role":"invalid","rule":0}`,
		`decision := {"allow":false,"masters":false,"binding":"invalid","role":"","rule":-1}`,
	} {
		t.Run(query, func(t *testing.T) {
			prepared, err := rego.New(rego.Query(query), rego.StrictBuiltinErrors(true)).PrepareForEval(t.Context())
			require.NoError(t, err)
			engine := &Engine{query: prepared}
			decision, err := engine.Evaluate(t.Context(), cluster, nil,
				&auth.Principal{Subject: "alice", AuthMethod: "static-users"},
				"product.read", auth.ResourceContext{Kind: "product"})
			require.ErrorIs(t, err, ErrUnavailable)
			require.Equal(t, auth.OutcomeDeny, decision.Outcome)
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	_, err = engine.Evaluate(ctx, cluster, nil, &auth.Principal{Subject: "alice", AuthMethod: "static-users"}, "product.read", auth.ResourceContext{Kind: "product"})
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestCapabilitiesExcludeNondeterministicBuiltins(t *testing.T) {
	for _, query := range []string{
		`http.send({"method":"GET","url":"https://example.invalid"})`,
		`time.now_ns()`, `rand.intn("key", 10)`, `uuid.rfc4122("key")`,
		`net.lookup_ip_addr("example.invalid")`, `opa.runtime()`,
	} {
		t.Run(query, func(t *testing.T) {
			_, err := rego.New(rego.Query(query), rego.Capabilities(policyCapabilities())).PrepareForEval(t.Context())
			require.Error(t, err)
		})
	}
}

func readTier(t *testing.T, cluster bool) Tier {
	t.Helper()
	tier := Tier{ID: rbac.ClusterTierID, Active: true}
	roleKind, bindingKind := rbac.ClusterRole, rbac.ClusterRoleBinding
	if !cluster {
		tier.ID, tier.Namespace = testUID("acme"), "acme"
		roleKind, bindingKind = rbac.Role, rbac.RoleBinding
	}
	role := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: roleKind,
		Metadata: rbac.Metadata{Name: "reader", Namespace: tier.Namespace, UID: testUID(string(roleKind))},
		Spec:     json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`)}
	binding := &rbac.Resource{APIVersion: rbac.APIVersion, Kind: bindingKind,
		Metadata: rbac.Metadata{Name: "readers", Namespace: tier.Namespace, UID: testUID(string(bindingKind))}}
	var err error
	binding.Spec, err = json.Marshal(rbac.BindingSpec{
		RoleRef: rbac.RoleRef{Kind: roleKind, Name: "reader"}, Subjects: []rbac.Subject{{Kind: "User", Name: "alice"}},
	})
	require.NoError(t, err)
	binding.Status = json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + role.Metadata.UID + `"}}}`)
	if !cluster {
		role.NamespaceUID, binding.NamespaceUID = tier.ID, tier.ID
	}
	tier.Resources = []*rbac.Resource{role, binding}
	return tier
}

func TestLifecycleAndUIDSuppression(t *testing.T) {
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	for _, cluster := range []bool{true, false} {
		for _, change := range []string{"none", "terminating-role", "terminating-binding", "unresolved", "recreated-role", "recreated-namespace", "terminating-namespace"} {
			t.Run(strconv.FormatBool(cluster)+"/"+change, func(t *testing.T) {
				tier := readTier(t, cluster)
				now := time.Now()
				switch change {
				case "terminating-role":
					tier.Resources[0].Metadata.DeletionTimestamp = &now
				case "terminating-binding":
					tier.Resources[1].Metadata.DeletionTimestamp = &now
				case "unresolved":
					tier.Resources[1].Status = nil
				case "recreated-role":
					tier.Resources[0].Metadata.UID = testUID("replacement")
				case "recreated-namespace":
					if cluster {
						return
					}
					tier.ID = testUID("replacement-namespace")
				case "terminating-namespace":
					if cluster {
						return
					}
					tier.Active = false
				}
				translated, err := Translate(tier)
				if change == "recreated-namespace" {
					require.ErrorIs(t, err, ErrUnavailable)
					return
				}
				require.NoError(t, err)
				root, namespace := translated, (*Translation)(nil)
				attrs := map[string]any{}
				if !cluster {
					root, err = Translate(Tier{ID: rbac.ClusterTierID, Active: true})
					require.NoError(t, err)
					namespace, attrs["namespace"] = translated, tier.Namespace
				}
				decision, err := engine.Evaluate(t.Context(), root, namespace,
					&auth.Principal{Subject: "alice", AuthMethod: "static-users"},
					"product.read", auth.ResourceContext{Kind: "product", Attrs: attrs})
				if change == "terminating-namespace" {
					require.ErrorIs(t, err, ErrUnavailable)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, change == "none", decision.Outcome == auth.OutcomeAllow)
			})
		}
	}
}

func TestSubjectUsernameRequiresTrustedProfile(t *testing.T) {
	tier := readTier(t, true)
	tier.Resources[0].Spec = json.RawMessage(`{"rules":[{"resources":["namespace"],"verbs":["create"],"when":["subjectUsername",{"attr":"tier","op":"eq","value":"USER"}]}]}`)
	cluster, err := Translate(tier)
	require.NoError(t, err)
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	for _, test := range []struct {
		username string
		profile  auth.ProfileKind
		name     string
		allow    bool
	}{
		{"alice", auth.ProfileStaff, "alice", true},
		{"directory-name", auth.ProfileStaff, "alice", true},
		{"directory-name", auth.ProfileStaff, "directory-name", false},
		{"alice", auth.ProfileCustomer, "alice", true},
		{"alice", "", "alice", false},
		{"", auth.ProfileStaff, "alice", false},
		{"alice", auth.ProfileStaff, "bob", false},
		{"system:api", auth.ProfileStaff, "system:api", false},
	} {
		decision, err := engine.Evaluate(t.Context(), cluster, nil,
			&auth.Principal{Subject: "alice", AuthMethod: "static-users", Username: test.username, ProfileKind: test.profile},
			"namespace.create", auth.ResourceContext{Kind: "namespace", Name: test.name, Attrs: map[string]any{"tier": "USER"}})
		require.NoError(t, err)
		require.Equal(t, test.allow, decision.Outcome == auth.OutcomeAllow, "%+v", test)
	}
}

func TestTranslationIsImmutableDeterministicAndConcurrent(t *testing.T) {
	tier := readTier(t, true)
	first, err := Translate(tier)
	require.NoError(t, err)
	slices.Reverse(tier.Resources)
	second, err := Translate(tier)
	require.NoError(t, err)
	require.Equal(t, first.digest, second.digest)
	tier.Resources[1].Spec = json.RawMessage(`{"rules":[]}`)
	revoked, err := Translate(tier)
	require.NoError(t, err)
	require.NotEqual(t, first.digest, revoked.digest)
	engine, err := NewEngine(t.Context())
	require.NoError(t, err)
	for index := range 32 {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			for _, test := range []struct {
				data  *Translation
				allow bool
			}{{first, true}, {second, true}, {revoked, false}} {
				decision, err := engine.Evaluate(t.Context(), test.data, nil,
					&auth.Principal{Subject: "alice", AuthMethod: "static-users"},
					"product.read", auth.ResourceContext{Kind: "product"})
				require.NoError(t, err)
				require.Equal(t, test.allow, decision.Outcome == auth.OutcomeAllow)
				sum := sha256.Sum256([]byte(engine.digest + ":" + test.data.digest))
				require.Equal(t, fmt.Sprintf("%x", sum), decision.PolicyDigest)
			}
		})
	}
}

func TestTranslationRejectsInvalidOrOversizedModels(t *testing.T) {
	for _, invalid := range []string{"inactive-cluster", "unknown-spec", "duplicate-uid", "wrong-tier", "oversized-expansion"} {
		t.Run(invalid, func(t *testing.T) {
			tier := readTier(t, true)
			switch invalid {
			case "inactive-cluster":
				tier.Active = false
			case "unknown-spec":
				tier.Resources[0].Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"],"deny":true}]}`)
			case "duplicate-uid":
				tier.Resources[1].Metadata.UID = tier.Resources[0].Metadata.UID
			case "wrong-tier":
				tier.Resources[0].NamespaceUID = testUID("unexpected-namespace")
			case "oversized-expansion":
				names := make([]string, 128)
				for index := range names {
					names[index] = strings.Repeat("x", 250) + strconv.Itoa(index)
				}
				spec, err := json.Marshal(rbac.RoleSpec{Rules: []rbac.Rule{{Resources: []string{"*"}, Verbs: []string{"*"}, ResourceNames: names}}})
				require.NoError(t, err)
				require.Less(t, len(spec), rbac.MaxManifestBytes)
				tier.Resources = nil
				for index := range rbac.MaxRolesPerTier {
					tier.Resources = append(tier.Resources, &rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRole,
						Metadata: rbac.Metadata{Name: "role-" + strconv.Itoa(index), UID: testUID(strconv.Itoa(index))}, Spec: spec})
				}
			}
			_, err := Translate(tier)
			require.ErrorIs(t, err, ErrUnavailable)
		})
	}
}
