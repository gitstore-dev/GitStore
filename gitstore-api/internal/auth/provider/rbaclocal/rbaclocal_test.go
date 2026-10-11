// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gopkg.in/yaml.v3"
)

type neutralAuthorizationFixture struct {
	Policies map[string]map[string]any `json:"policies"`
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
	ValidationCases []struct {
		ID, Operation, Path, Expected string
		Value                         any
	}
	RawValidationCases []struct{ ID, YAML, Expected string }
}

// The shipped configuration is an executable contract, not a parser-only example.
func TestShippedPolicyEffectiveGrants(t *testing.T) {
	for _, path := range []string{"../../../../../config/policy.yaml", "../../../../policy.yaml.example"} {
		t.Run(path, func(t *testing.T) {
			policy, err := loadPolicy(path)
			require.NoError(t, err)
			provider := &RBACLocalProvider{}
			provider.snapshot.Store(compilePolicy(policy))
			for _, binding := range append(append([]Binding{}, policy.ClusterRoleBindings...), policy.RoleBindings...) {
				for _, subject := range binding.Subjects {
					require.NotEqual(t, "system:masters", subject.Name, "no shipped masters membership")
				}
			}
			controller := &authpkg.Principal{Subject: "system:serviceaccount:gitstore-controller-manager", ServiceAccountUID: "enrolled-uid", AuthMethod: "serviceaccount-jwt", Groups: []string{"system:authenticated", "system:serviceaccounts"}}
			internal := &authpkg.Principal{Subject: "system:api", AuthMethod: "internal", Groups: []string{"system:authenticated"}}
			admin := &authpkg.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
			controllerActions := strings.Fields("namespace.read namespace.list namespace.watch namespace.status.write namespace.purge namespace.system-repository.provision repository.create repository.read repository.list repository.watch repository.status.write repository.purge categoryTaxonomy.read categoryTaxonomy.list categoryTaxonomy.watch categoryTaxonomy.status.write categoryTaxonomy.purge product.read product.list product.watch product.management.read product.management.list product.status.write product.purge file.read file.list file.watch file.status.write serviceAccount.token.issue")
			internalActions := strings.Fields("repository.create repository.delete repository.contents.read repository.contents.write")
			contains := func(actions []string, action string) bool {
				for _, candidate := range actions {
					if candidate == action {
						return true
					}
				}
				return false
			}
			for _, action := range authpkg.AuthorizationActions() {
				resource := authpkg.ResourceContext{Kind: action.Kind, Name: "gitstore-controller-manager", Attrs: map[string]any{"namespace": "acme", "tier": "ORGANIZATION", "targetOwnerRef": map[string]any{"kind": "User", "name": "alice"}}}
				for _, subject := range []struct {
					principal *authpkg.Principal
					want      bool
				}{{admin, true}, {controller, contains(controllerActions, action.Action)}, {internal, contains(internalActions, action.Action)}} {
					decision, err := provider.Authorize(t.Context(), subject.principal, action.Action, resource)
					require.NoError(t, err)
					assert.Equal(t, subject.want, decision.Outcome == authpkg.OutcomeAllow, "%s %s: %s", subject.principal.Subject, action.Action, decision.Reason)
				}
			}
			decision, err := provider.Authorize(t.Context(), controller, "serviceAccount.token.issue", authpkg.ResourceContext{Kind: "serviceAccount", Name: "another-account"})
			require.NoError(t, err)
			assert.Equal(t, authpkg.OutcomeDeny, decision.Outcome)
			for _, visibility := range []any{nil, "public", "private", true} {
				decision, err := provider.Authorize(t.Context(), &authpkg.Principal{Subject: "anon", AuthMethod: "none", Groups: []string{"system:unauthenticated"}}, "repository.contents.read", authpkg.ResourceContext{Kind: "repository", Attrs: map[string]any{"namespace": "acme", "visibility": visibility}})
				require.NoError(t, err)
				assert.Equal(t, visibility == "public", decision.Outcome == authpkg.OutcomeAllow)
			}
			alice := &authpkg.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
			for _, test := range []struct {
				action, owner, tier string
				want                bool
			}{
				{"namespace.create", "", "USER", true}, {"namespace.create", "", "ORGANIZATION", false},
				{"namespace.read", "alice", "USER", true}, {"namespace.update", "bob", "USER", false},
				{"namespace.delete", "alice", "USER", true}, {"namespace.list", "alice", "USER", false},
			} {
				decision, err := provider.Authorize(t.Context(), alice, test.action, authpkg.ResourceContext{Kind: "namespace", Name: "alice", OwnerSub: test.owner, Attrs: map[string]any{"tier": test.tier}})
				require.NoError(t, err)
				assert.Equal(t, test.want, decision.Outcome == authpkg.OutcomeAllow, "%+v", test)
			}
			for _, name := range []string{"admin", "edit", "view"} {
				require.Contains(t, policy.ClusterRoles, name)
				isolated := *policy
				namespace := "acme"
				isolated.ClusterRoleBindings = nil
				isolated.RoleBindings = []Binding{{Namespace: &namespace, RoleRef: RoleRef{Kind: "ClusterRole", Name: name}, Subjects: []SubjectRef{{Kind: "User", Name: "alice"}}}}
				provider.snapshot.Store(compilePolicy(&isolated))
				for _, action := range authpkg.AuthorizationActions() {
					core := contains([]string{"repository", "categoryTaxonomy", "product", "productVariant", "collection", "file"}, action.Resource)
					want := core && contains([]string{"read", "list", "watch"}, action.Verb)
					want = want || (action.Resource == "product/management" || action.Resource == "productVariant/management")
					if name != "view" {
						want = want || core && contains([]string{"create", "update", "delete"}, action.Verb)
					}
					if name == "admin" {
						want = want || core && contains([]string{"transfer", "purge"}, action.Verb)
					}
					for _, ns := range []string{"acme", "other", ""} {
						decision, err := provider.Authorize(t.Context(), alice, action.Action, authpkg.ResourceContext{Kind: action.Kind, OwnerSub: "alice", Attrs: map[string]any{"namespace": ns, "targetOwnerRef": map[string]any{"kind": "User", "name": "alice"}}})
						require.NoError(t, err)
						assert.Equal(t, want && ns == "acme", decision.Outcome == authpkg.OutcomeAllow, "%s %s namespace=%q", name, action.Action, ns)
					}
				}
			}
		})
	}
}

func loadNeutralFixture(t *testing.T) neutralAuthorizationFixture {
	t.Helper()
	data, err := os.ReadFile("../../../../../tests/fixtures/authorization/decision-table.json")
	require.NoError(t, err)
	seed, err := os.ReadFile("../../../../../specs/064-authz-action-vocabulary/contracts/decision-table.json")
	require.NoError(t, err)
	require.Equal(t, seed, data, "neutral fixture expectations must remain unchanged")
	var fixture neutralAuthorizationFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	return fixture
}

func TestV1Beta1NeutralDecisions(t *testing.T) {
	fixture := loadNeutralFixture(t)
	require.Len(t, fixture.Cases, 55)
	providers := map[string]*RBACLocalProvider{}
	for key, policy := range fixture.Policies {
		encoded, err := yaml.Marshal(policy)
		require.NoError(t, err)
		providers[key] = newRBACProvider(t, string(encoded))
	}
	for _, c := range fixture.Cases {
		t.Run(c.ID, func(t *testing.T) {
			principal := &authpkg.Principal{Subject: c.Principal.Subject, AuthMethod: c.Principal.AuthMethod, Groups: c.Principal.Groups, ServiceAccountUID: c.Principal.ServiceAccountUID}
			resource := authpkg.ResourceContext{Kind: c.Resource.Kind, Name: c.Resource.Name, OwnerSub: c.Resource.OwnerSub, Attrs: c.Resource.Attrs}
			d, err := providers[c.Policy].Authorize(t.Context(), principal, c.Action, resource)
			require.NoError(t, err)
			assert.Equal(t, c.Expected.Outcome == "allow", d.Outcome == authpkg.OutcomeAllow)
			assert.NotEmpty(t, d.Reason)
			if c.Expected.Outcome == "allow" && c.ID != "masters-trusted-group" {
				for _, diagnostic := range []string{"binding=", "role=", "rule="} {
					assert.Contains(t, d.Reason, diagnostic)
				}
			}
		})
	}
}

func mutateNeutralPolicy(t *testing.T, root map[string]any, pointer, operation string, value any) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	var current any = root
	for _, part := range parts[:len(parts)-1] {
		part = strings.NewReplacer("~1", "/", "~0", "~").Replace(part)
		switch node := current.(type) {
		case map[string]any:
			current = node[part]
		case []any:
			index, err := strconv.Atoi(part)
			require.NoError(t, err)
			current = node[index]
		default:
			t.Fatalf("invalid fixture pointer %s", pointer)
		}
	}
	key := parts[len(parts)-1]
	object, ok := current.(map[string]any)
	require.True(t, ok)
	if operation == "remove" {
		delete(object, key)
	} else {
		object[key] = value
	}
}

func TestV1Beta1NeutralValidation(t *testing.T) {
	fixture := loadNeutralFixture(t)
	require.Equal(t, 28, len(fixture.ValidationCases)+len(fixture.RawValidationCases))
	base, err := json.Marshal(fixture.Policies["base"])
	require.NoError(t, err)
	for _, c := range fixture.ValidationCases {
		t.Run(c.ID, func(t *testing.T) {
			var policy map[string]any
			require.NoError(t, json.Unmarshal(base, &policy))
			mutateNeutralPolicy(t, policy, c.Path, c.Operation, c.Value)
			data, err := yaml.Marshal(policy)
			require.NoError(t, err)
			_, err = parsePolicy("fixture", data)
			if c.Expected == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, c := range fixture.RawValidationCases {
		t.Run(c.ID, func(t *testing.T) { _, err := parsePolicy("fixture", []byte(c.YAML)); require.Error(t, err) })
	}
}

func TestV1Beta1StrictGrammarAndFiniteWildcard(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", "", true},
		{"finite-all", "clusterRoles: {admin: {rules: [{resources: ['*'], verbs: ['*']}]}}", true},
		{"finite-read", "clusterRoles: {reader: {rules: [{resources: ['*'], verbs: [read]}]}}", true},
		{"invalid-cross-product", "clusterRoles: {reader: {rules: [{resources: [product, serviceAccount], verbs: [watch]}]}}", false},
		{"partial-verb", "clusterRoles: {reader: {rules: [{resources: [product], verbs: ['r*']}]}}", false},
		{"unused-verb", "clusterRoles: {reader: {rules: [{resources: ['*'], verbs: [teleport]}]}}", false},
		{"alias", "clusterRoles: &roles {}\nroles: *roles", false},
		{"unknown-root", "unknown: value", false},
		{"namespaced-definition", "roles: {reader: {namespace: acme, rules: []}}", false},
		{"empty-set", "clusterRoles: {reader: {rules: [{resources: [product], verbs: [read], when: [{attr: tier, op: in, values: []}]}]}}", false},
		{"nonstring-value", "clusterRoles: {reader: {rules: [{resources: [product], verbs: [read], when: [{attr: tier, op: eq, value: 3}]}]}}", false},
		{"unknown-condition-field", "clusterRoles: {reader: {rules: [{resources: [product], verbs: [read], when: [{attr: tier, op: eq, value: USER, secret: x}]}]}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePolicy("fixture", []byte("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\n"+tc.body))
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestV1Beta1PolicyBounds(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "RbacLocalConfiguration",
			"clusterRoles": map[string]any{"reader": map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}}}}},
		}
	}
	for _, tc := range []struct {
		name   string
		limit  int
		change func(map[string]any, int)
	}{
		{"roles", 200, func(p map[string]any, n int) {
			roles := map[string]any{}
			for i := 0; i < n; i++ {
				roles[fmt.Sprint("r", i)] = map[string]any{"rules": []any{}}
			}
			p["clusterRoles"] = roles
		}},
		{"rules", 2000, func(p map[string]any, n int) {
			rules := make([]any, n)
			for i := range rules {
				rules[i] = map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}}
			}
			p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"] = rules
		}},
		{"bindings", 1000, func(p map[string]any, n int) {
			bs := make([]any, n)
			for i := range bs {
				bs[i] = map[string]any{"roleRef": map[string]any{"kind": "ClusterRole", "name": "reader"}, "subjects": []any{map[string]any{"kind": "User", "name": "alice"}}}
			}
			p["clusterRoleBindings"] = bs
		}},
		{"subjects", 64, func(p map[string]any, n int) {
			ss := make([]any, n)
			for i := range ss {
				ss[i] = map[string]any{"kind": "User", "name": fmt.Sprint("u", i)}
			}
			p["clusterRoleBindings"] = []any{map[string]any{"roleRef": map[string]any{"kind": "ClusterRole", "name": "reader"}, "subjects": ss}}
		}},
		{"conditions", 16, func(p map[string]any, n int) {
			rule := p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"].([]any)[0].(map[string]any)
			rule["when"] = stringsRepeat("owner", n)
		}},
		{"operands", 64, func(p map[string]any, n int) {
			rule := p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"].([]any)[0].(map[string]any)
			rule["when"] = []any{map[string]any{"attr": "namespace", "op": "in", "values": stringsRepeat("acme", n)}}
		}},
		{"names", 256, func(p map[string]any, n int) {
			p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"].([]any)[0].(map[string]any)["resourceNames"] = stringsRepeat("p", n)
		}},
		{"resources", 128, func(p map[string]any, n int) {
			p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"].([]any)[0].(map[string]any)["resources"] = stringsRepeat("product", n)
		}},
		{"verbs", 32, func(p map[string]any, n int) {
			p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"].([]any)[0].(map[string]any)["verbs"] = stringsRepeat("read", n)
		}},
		{"string-bytes", 256, func(p map[string]any, n int) {
			p["clusterRoles"].(map[string]any)["reader"].(map[string]any)["rules"].([]any)[0].(map[string]any)["resourceNames"] = []string{strings.Repeat("a", n)}
		}},
	} {
		for _, n := range []int{tc.limit, tc.limit + 1} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, n), func(t *testing.T) {
				p := base()
				tc.change(p, n)
				data, err := yaml.Marshal(p)
				require.NoError(t, err)
				_, err = parsePolicy("bounds", data)
				if n == tc.limit {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
	data, err := yaml.Marshal(base())
	require.NoError(t, err)
	data = append(data, []byte("#"+strings.Repeat("x", 4*1024*1024-len(data)-1))...)
	_, err = parsePolicy("byte-limit", data)
	require.NoError(t, err)
	_, err = parsePolicy("byte-overflow", append(data, 'x'))
	require.Error(t, err)
}

func stringsRepeat(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}

func TestV1Beta1ReloadRetainsSnapshotAndConcurrentDecisions(t *testing.T) {
	fixture := loadNeutralFixture(t)
	allow, err := yaml.Marshal(fixture.Policies["base"])
	require.NoError(t, err)
	deny, err := yaml.Marshal(fixture.Policies["empty"])
	require.NoError(t, err)
	p := newRBACProvider(t, string(allow))
	principal := &authpkg.Principal{Subject: "reader", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
	resource := authpkg.ResourceContext{Kind: "product", Name: "p"}
	for _, invalid := range []string{"version: v1\nroles: {}", strings.Repeat("x", 4*1024*1024+1), "not: [valid"} {
		require.NoError(t, os.WriteFile(p.path, []byte(invalid), 0600))
		require.Error(t, p.Reload())
		d, err := p.Authorize(t.Context(), principal, "product.read", resource)
		require.NoError(t, err)
		require.Equal(t, authpkg.OutcomeAllow, d.Outcome)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				d, err := p.Authorize(t.Context(), principal, "product.read", resource)
				assert.NoError(t, err)
				assert.Contains(t, []authpkg.Outcome{authpkg.OutcomeAllow, authpkg.OutcomeDeny}, d.Outcome)
				assert.NotEmpty(t, d.Reason)
			}
		})
	}
	for i := range 20 {
		data := allow
		if i%2 == 0 {
			data = deny
		}
		require.NoError(t, os.WriteFile(p.path, data, 0600))
		require.NoError(t, p.Reload())
	}
	wg.Wait()
}

func TestV1Beta1InvalidRequestsFailClosedEvenMasters(t *testing.T) {
	fixture := loadNeutralFixture(t)
	data, err := yaml.Marshal(fixture.Policies["base"])
	require.NoError(t, err)
	p := newRBACProvider(t, string(data))
	for _, principal := range []*authpkg.Principal{nil, {Subject: "", AuthMethod: "static-users"}, {Subject: "admin", AuthMethod: "static-users", Groups: stringsRepeat("system:masters", 257)}, {Subject: "system:api", AuthMethod: "oidc-jwt", Groups: []string{"system:masters"}}, {Subject: "system:serviceaccount:controller", AuthMethod: "oidc-jwt", Groups: []string{"system:masters"}}} {
		d, err := p.Authorize(t.Context(), principal, "product.read", authpkg.ResourceContext{Kind: "product"})
		require.NoError(t, err)
		assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
	}
	d, err := p.Authorize(t.Context(), &authpkg.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}, "repository.transfer", authpkg.ResourceContext{Kind: "repository"})
	require.NoError(t, err)
	assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
}

func TestV1Beta1ReloadRetainsEffectiveUserPermission(t *testing.T) {
	fixture := loadNeutralFixture(t)
	data, err := yaml.Marshal(fixture.Policies["base"])
	require.NoError(t, err)
	p := newRBACProvider(t, string(data))
	require.NoError(t, p.SetActiveUsers([]*authpkg.UserProfile{{Subject: "reader", Active: true}, {Subject: "inactive", Active: false}}))
	data, err = yaml.Marshal(fixture.Policies["empty"])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p.path, data, 0600))
	require.Error(t, p.Reload())
	d, err := p.Authorize(t.Context(), &authpkg.Principal{Subject: "reader", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}, "product.read", authpkg.ResourceContext{Kind: "product"})
	require.NoError(t, err)
	require.Equal(t, authpkg.OutcomeAllow, d.Outcome)
}

func newRBACProvider(t *testing.T, policyContent string) *RBACLocalProvider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, []byte(policyContent), 0600))
	provider, err := New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
	require.NoError(t, err)
	return provider
}

func TestV1Beta1RejectsOldPolicyRatherThanTranslatingIt(t *testing.T) {
	for _, document := range []string{
		"version: v1\nroles: {admin: {allow: ['*']}}\nrole_bindings: {admin: [admin]}\n",
		"version: v1\ndefault_deny: false\nroles: {admin: {allow: ['*'], deny: ['product.read']}}\n",
	} {
		path := filepath.Join(t.TempDir(), "policy.yaml")
		require.NoError(t, os.WriteFile(path, []byte(document), 0600))
		_, err := New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
		require.ErrorContains(t, err, PolicyAPIVersion)
	}
}

func TestV1Beta1ConditionsDoNotCombineAcrossRules(t *testing.T) {
	p := newRBACProvider(t, `apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  transfer:
    rules:
      - resources: [repository]
        verbs: [transfer]
        when: [owner]
      - resources: [repository]
        verbs: [transfer]
        when: [targetOwnerMember]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: transfer}
    subjects: [{kind: User, name: alice}]
`)
	for _, target := range []map[string]any{{"kind": "User", "name": "bob"}, {"kind": "Group", "name": "system:group:team"}} {
		d, err := p.Authorize(t.Context(), &authpkg.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"}}, "repository.transfer", authpkg.ResourceContext{Kind: "repository", OwnerSub: "alice", Attrs: map[string]any{"targetOwnerRef": target}})
		require.NoError(t, err)
		assert.Equal(t, authpkg.OutcomeDeny, d.Outcome)
	}
}

// authorizationCapacityFixture exercises the maximum supported policy, including
// overlap: every binding is reachable through each of the principal's 50 groups.
// The last allow cannot be found until 1,999 rule conditions have failed.
func authorizationCapacityFixture(t *testing.T) (*RBACLocalProvider, []authorizationCapacityCase) {
	t.Helper()
	var document strings.Builder
	document.WriteString("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n")
	for role := range 200 {
		fmt.Fprintf(&document, "  role-%03d:\n    rules:\n", role)
		for rule := range 10 {
			value := "never"
			if role == 0 && rule == 0 {
				value = "early"
			} else if role == 199 && rule == 9 {
				value = "last"
			}
			fmt.Fprintf(&document, "      - resources: [product]\n        verbs: [read, transfer]\n        when: [owner, {attr: visibility, op: eq, value: %s}]\n", value)
		}
	}
	document.WriteString("clusterRoleBindings:\n")
	groups := make([]string, 50)
	for group := range groups {
		groups[group] = fmt.Sprintf("system:group:group-%02d", group)
	}
	for binding := range 1000 {
		fmt.Fprintf(&document, "  - roleRef: {kind: ClusterRole, name: role-%03d}\n    subjects:\n", binding%200)
		for _, group := range groups {
			fmt.Fprintf(&document, "      - {kind: Group, name: %s}\n", group)
		}
	}
	p := newRBACProvider(t, document.String())
	require.Len(t, p.snapshot.Load().bindings, 1000)
	require.Equal(t, 2000, p.snapshot.Load().ruleCount)
	principal := &authpkg.Principal{Subject: "alice", AuthMethod: "static-users", Groups: groups}
	unbound := &authpkg.Principal{Subject: "unbound", AuthMethod: "static-users", Groups: stringsRepeat("system:authenticated", 50)}
	resource := func(visibility string) authpkg.ResourceContext {
		return authpkg.ResourceContext{Kind: "product", Name: "widget", OwnerSub: "alice", Attrs: map[string]any{"namespace": "acme", "visibility": visibility, "targetOwnerRef": map[string]any{"kind": "User", "name": "alice"}}}
	}
	missing := resource("last")
	delete(missing.Attrs, "visibility")
	cases := []authorizationCapacityCase{
		{"early-allow", principal, "product.read", resource("early"), authpkg.OutcomeAllow},
		{"last-allow", principal, "product.read", resource("last"), authpkg.OutcomeAllow},
		{"no-binding", unbound, "product.read", resource("last"), authpkg.OutcomeDeny},
		{"overlapping-deny", principal, "product.read", resource("absent"), authpkg.OutcomeDeny},
		{"owner-transfer", principal, "product.transfer", resource("last"), authpkg.OutcomeAllow},
		{"missing-attribute", principal, "product.read", missing, authpkg.OutcomeDeny},
	}
	return p, cases
}

type authorizationCapacityCase struct {
	name      string
	principal *authpkg.Principal
	action    string
	resource  authpkg.ResourceContext
	want      authpkg.Outcome
}

// Fixed one-microsecond upper-bound buckets through 2ms, then overflow. Keeping
// independent per-case samples prevents cheap no-binding calls hiding slow hits.
type authorizationLatencyHistogram struct {
	Buckets [2001]uint64 `json:"bucketsUpperMicroseconds"`
	Samples uint64       `json:"samples"`
	Calls   uint64       `json:"calls"`
	MaxNS   int64        `json:"maxNanoseconds"`
}

func (h *authorizationLatencyHistogram) observe(elapsed time.Duration) {
	index := min(int(elapsed/time.Microsecond), len(h.Buckets)-1)
	h.Buckets[index]++
	h.Samples++
	h.MaxNS = max(h.MaxNS, int64(elapsed))
}

func (h authorizationLatencyHistogram) quantile(percent float64) time.Duration {
	if h.Samples == 0 {
		return time.Duration(math.MaxInt64)
	}
	rank := uint64(math.Ceil(float64(h.Samples) * percent))
	var cumulative uint64
	for index, count := range h.Buckets {
		cumulative += count
		if cumulative >= rank {
			if index == len(h.Buckets)-1 {
				return time.Duration(h.MaxNS)
			}
			return time.Duration(index+1) * time.Microsecond
		}
	}
	return time.Duration(h.MaxNS)
}

func sampleAuthorizationCapacity(t *testing.T, p *RBACLocalProvider, cases []authorizationCapacityCase, concurrency int, duration time.Duration) []authorizationLatencyHistogram {
	t.Helper()
	var stopped atomic.Bool
	var invalid atomic.Uint64
	workers := make([][]authorizationLatencyHistogram, concurrency)
	var ready, finished sync.WaitGroup
	ready.Add(concurrency)
	finished.Add(concurrency)
	start := make(chan struct{})
	for worker := range concurrency {
		workers[worker] = make([]authorizationLatencyHistogram, len(cases))
		go func() {
			defer finished.Done()
			state := uint64(worker+1) * 0x9e3779b97f4a7c15
			nextRandom := func() uint64 { state ^= state << 13; state ^= state >> 7; state ^= state << 17; return state }
			ready.Done()
			<-start
			for !stopped.Load() {
				index := int(nextRandom() % uint64(len(cases)))
				tc := cases[index]
				histogram := &workers[worker][index]
				// Separate PRNG draw for sampling, independent of case selection.
				sampled := nextRandom()%64 == 0
				var before time.Time
				if sampled {
					before = time.Now()
				}
				decision, err := p.Authorize(context.Background(), tc.principal, tc.action, tc.resource)
				if sampled {
					histogram.observe(time.Since(before))
				}
				histogram.Calls++
				if err != nil || decision.Outcome != tc.want {
					invalid.Add(1)
				}
			}
		}()
	}
	ready.Wait()
	close(start)
	timer := time.NewTimer(duration)
	select {
	case <-timer.C:
	case <-t.Context().Done():
		timer.Stop()
	}
	stopped.Store(true)
	finished.Wait()
	require.Zero(t, invalid.Load(), "every decision must match the semantic fixture")
	result := make([]authorizationLatencyHistogram, len(cases))
	for _, worker := range workers {
		for index, histogram := range worker {
			result[index].Samples += histogram.Samples
			result[index].Calls += histogram.Calls
			result[index].MaxNS = max(result[index].MaxNS, histogram.MaxNS)
			for bucket, count := range histogram.Buckets {
				result[index].Buckets[bucket] += count
			}
		}
	}
	return result
}

func TestAuthorizationCapacityHistogramAndFixture(t *testing.T) {
	var h authorizationLatencyHistogram
	require.Equal(t, time.Duration(math.MaxInt64), h.quantile(.99))
	for range 99 {
		h.observe(100 * time.Microsecond)
	}
	h.observe(3 * time.Millisecond)
	require.Equal(t, 101*time.Microsecond, h.quantile(.99))
	require.Equal(t, 3*time.Millisecond, h.quantile(1))
	p, cases := authorizationCapacityFixture(t)
	// Removing the source makes any accidental policy reload/read on the decision
	// path fail. No datastore or network client is a provider dependency.
	require.NoError(t, os.Remove(p.path))
	for _, tc := range cases {
		decision, err := p.Authorize(t.Context(), tc.principal, tc.action, tc.resource)
		require.NoError(t, err)
		require.Equal(t, tc.want, decision.Outcome, tc.name)
	}
	for _, histogram := range sampleAuthorizationCapacity(t, p, cases, 2, 50*time.Millisecond) {
		// A time-bounded random run need not visit every case on a busy race
		// runner. Semantic coverage is deterministic above; check accounting
		// here without turning scheduler throughput into a unit-test gate.
		var samples uint64
		for _, count := range histogram.Buckets {
			samples += count
		}
		require.Equal(t, histogram.Samples, samples)
		require.LessOrEqual(t, histogram.Samples, histogram.Calls)
	}
}

// Opt-in only: the ordinary unit suite verifies the fixture/histogram, not a
// long-running performance claim. Root capacity dispatch supplies evidence mode.
func TestAuthorizationDecisionCapacity(t *testing.T) {
	if os.Getenv("AUTHZ_CAPACITY_RUN") != "1" {
		t.Skip("authorization capacity requires explicit opt-in")
	}
	duration := func(key string, fallback time.Duration) time.Duration {
		if value := os.Getenv(key); value != "" {
			parsed, err := time.ParseDuration(value)
			require.NoError(t, err)
			require.Positive(t, parsed)
			if os.Getenv("MODE") != "diagnostic" {
				require.GreaterOrEqual(t, parsed, fallback, "acceptance durations cannot be shortened")
			}
			return parsed
		}
		return fallback
	}
	warmup := duration("AUTHZ_CAPACITY_WARMUP", time.Minute)
	measure := duration("AUTHZ_CAPACITY_DURATION", 10*time.Minute)
	p, cases := authorizationCapacityFixture(t)
	t.Logf("runtime=%s os=%s arch=%s cpus=%d gomaxprocs=%d policy=%s roles=200 bindings=1000 rules=2000 groups=50 sampleProbability=1/64", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0), p.snapshot.Load().digest)
	for _, concurrency := range []int{1, 8, 32} {
		t.Run(fmt.Sprintf("concurrency-%d", concurrency), func(t *testing.T) {
			sampleAuthorizationCapacity(t, p, cases, concurrency, warmup)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			started := time.Now()
			histograms := sampleAuthorizationCapacity(t, p, cases, concurrency, measure)
			elapsed := time.Since(started)
			runtime.ReadMemStats(&after)
			var calls uint64
			for index, histogram := range histograms {
				calls += histogram.Calls
				t.Logf("case=%s calls=%d samples=%d p50=%s p95=%s p99=%s max=%s", cases[index].name, histogram.Calls, histogram.Samples, histogram.quantile(.5), histogram.quantile(.95), histogram.quantile(.99), time.Duration(histogram.MaxNS))
				if os.Getenv("MODE") != "diagnostic" {
					require.GreaterOrEqual(t, histogram.Samples, uint64(1000))
					require.Less(t, histogram.quantile(.99), time.Millisecond)
				}
			}
			t.Logf("elapsed=%s decisionsPerSecond=%.1f allocatedBytesPerDecision=%.1f allocationsPerDecision=%.1f", elapsed, float64(calls)/elapsed.Seconds(), float64(after.TotalAlloc-before.TotalAlloc)/float64(calls), float64(after.Mallocs-before.Mallocs)/float64(calls))
			if directory := os.Getenv("CAPACITY_EVIDENCE_DIR"); directory != "" {
				data, err := json.MarshalIndent(map[string]any{"concurrency": concurrency, "policyDigest": p.snapshot.Load().digest, "warmupSeconds": warmup.Seconds(), "measurementSeconds": elapsed.Seconds(), "histograms": histograms}, "", "  ")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(directory, fmt.Sprintf("authorization-latency-%d.json", concurrency)), data, 0600))
			}
		})
	}
}
