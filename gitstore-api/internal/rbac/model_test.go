// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestManifestTreeDigestTracksOnlyLiveAuthorContent(t *testing.T) {
	role := &Resource{APIVersion: APIVersion, Kind: Role, Metadata: Metadata{Name: "reader", Namespace: "shop"},
		Spec: json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["read"]}]}`)}
	binding := &Resource{APIVersion: APIVersion, Kind: RoleBinding, Metadata: Metadata{Name: "access", Namespace: "shop"},
		Spec: json.RawMessage(`{"roleRef":{"kind":"Role","name":"reader"},"subjects":[{"kind":"User","name":"alice"}]}`)}
	digest, err := ManifestTreeDigest([]*Resource{role, binding})
	require.NoError(t, err)
	role.Metadata.UID, role.Metadata.ResourceVersion, role.Metadata.Revision = "owned-uid", "7", "commit"
	role.Metadata.Finalizers = []string{LifecycleFinalizer}
	role.Status = json.RawMessage(`{"observedGeneration":1}`)
	reordered, err := ManifestTreeDigest([]*Resource{binding, role})
	require.NoError(t, err)
	require.Equal(t, digest, reordered)
	role.Spec = json.RawMessage(`{"rules":[{"resources":["product"],"verbs":["update"]}]}`)
	changed, err := ManifestTreeDigest([]*Resource{role, binding})
	require.NoError(t, err)
	require.NotEqual(t, digest, changed)
	now := time.Now().UTC()
	role.Metadata.DeletionTimestamp = &now
	terminating, err := ManifestTreeDigest([]*Resource{role, binding})
	require.NoError(t, err)
	remaining, err := ManifestTreeDigest([]*Resource{binding})
	require.NoError(t, err)
	require.Equal(t, remaining, terminating)
	_, err = ManifestTreeDigest([]*Resource{binding, binding})
	require.Error(t, err)
}

func modelFixture(t *testing.T) []map[string]any {
	t.Helper()
	data, err := os.ReadFile("testdata/model-v1.json")
	require.NoError(t, err)
	var fixture struct {
		Resources []map[string]any `json:"resources"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	for _, resource := range fixture.Resources {
		metadata := resource["metadata"].(map[string]any)
		for _, field := range []string{"uid", "generation", "resourceVersion"} {
			delete(metadata, field)
		}
	}
	return fixture.Resources
}

func fixtureScope(kind string) RepositoryScope {
	if strings.HasPrefix(kind, "Cluster") {
		return RepositoryScope{Root: true, Namespace: "gitstore-system", NamespaceUID: "root-uid"}
	}
	return RepositoryScope{Namespace: "acme", NamespaceUID: "11111111-1111-4111-8111-111111111111"}
}

func fixturePath(resource map[string]any) string {
	folder := map[string]string{
		"Role": "roles", "ClusterRole": "clusterroles",
		"RoleBinding": "rolebindings", "ClusterRoleBinding": "clusterrolebindings",
	}[resource["kind"].(string)]
	return "rbac/" + folder + "/" + resource["metadata"].(map[string]any)["name"].(string) + ".yaml"
}

func parseFixture(t *testing.T, resource map[string]any) *Resource {
	t.Helper()
	data, err := yaml.Marshal(resource)
	require.NoError(t, err)
	parsed, err := ParseManifest(data, fixturePath(resource), fixtureScope(resource["kind"].(string)))
	require.NoError(t, err)
	return parsed
}

func TestManifestEnvelopesAndCanonicalRoundTrip(t *testing.T) {
	for _, resource := range modelFixture(t) {
		t.Run(resource["metadata"].(map[string]any)["name"].(string), func(t *testing.T) {
			parsed := parseFixture(t, resource)
			if !strings.HasPrefix(resource["kind"].(string), "Cluster") {
				require.Equal(t, fixtureScope("Role").NamespaceUID, parsed.NamespaceUID)
			} else {
				require.Empty(t, parsed.NamespaceUID)
			}
			canonical, err := CanonicalManifest(parsed)
			require.NoError(t, err)
			roundTrip, err := ParseManifest(canonical, fixturePath(resource), fixtureScope(resource["kind"].(string)))
			require.NoError(t, err)
			require.JSONEq(t, string(parsed.Spec), string(roundTrip.Spec))
		})
	}
}

func TestManifestRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		index int
		edit  func(map[string]any)
	}{
		{"version", 0, func(r map[string]any) { r["apiVersion"] = "rbac.authorization.gitstore.dev/v1" }},
		{"kind", 0, func(r map[string]any) { r["kind"] = "Policy" }},
		{"unknown envelope field", 0, func(r map[string]any) { r["extra"] = "value" }},
		{"server status", 0, func(r map[string]any) { r["status"] = map[string]any{} }},
		{"server uid", 0, func(r map[string]any) { r["metadata"].(map[string]any)["uid"] = "forged" }},
		{"namespace mismatch", 0, func(r map[string]any) { r["metadata"].(map[string]any)["namespace"] = "other" }},
		{"missing namespace", 0, func(r map[string]any) { delete(r["metadata"].(map[string]any), "namespace") }},
		{"invalid name", 0, func(r map[string]any) { r["metadata"].(map[string]any)["name"] = "../escape" }},
		{"cluster namespace", 2, func(r map[string]any) { r["metadata"].(map[string]any)["namespace"] = "acme" }},
		{"deny", 0, func(r map[string]any) {
			r["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["effect"] = "deny"
		}},
		{"partial resource wildcard", 0, func(r map[string]any) {
			r["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["resources"] = []string{"prod*"}
		}},
		{"partial verb wildcard", 0, func(r map[string]any) {
			r["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["verbs"] = []string{"re*"}
		}},
		{"legacy action", 0, func(r map[string]any) {
			r["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["verbs"] = []string{"read.any"}
		}},
		{"cluster grant in Role", 0, func(r map[string]any) {
			r["spec"].(map[string]any)["rules"] = []any{map[string]any{"resources": []string{"namespace"}, "verbs": []string{"read"}}}
		}},
		{"rules on binding", 1, func(r map[string]any) { r["spec"].(map[string]any)["rules"] = []any{} }},
		{"unknown condition", 0, func(r map[string]any) {
			r["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["when"] = []string{"always"}
		}},
		{"reserved user", 1, func(r map[string]any) {
			r["spec"].(map[string]any)["subjects"] = []any{map[string]any{"kind": "User", "name": "system:serviceaccount:robot"}}
		}},
		{"legacy serviceaccount", 1, func(r map[string]any) {
			r["spec"].(map[string]any)["subjects"] = []any{map[string]any{"kind": "User", "name": "serviceaccount:robot"}}
		}},
		{"namespaced serviceaccount", 1, func(r map[string]any) {
			r["spec"].(map[string]any)["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "acme/robot"}}
		}},
		{"duplicate subjects", 1, func(r map[string]any) {
			subjects := r["spec"].(map[string]any)["subjects"].([]any)
			r["spec"].(map[string]any)["subjects"] = append(subjects, subjects[0])
		}},
		{"cluster binding to Role", 3, func(r map[string]any) { r["spec"].(map[string]any)["roleRef"].(map[string]any)["kind"] = "Role" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := modelFixture(t)[test.index]
			path, scope := fixturePath(resource), fixtureScope(resource["kind"].(string))
			test.edit(resource)
			data, err := yaml.Marshal(resource)
			require.NoError(t, err)
			_, err = ParseManifest(data, path, scope)
			require.Error(t, err)
		})
	}
}

func TestManifestScopePathAndPayloadBounds(t *testing.T) {
	resource := modelFixture(t)[0]
	data, err := yaml.Marshal(resource)
	require.NoError(t, err)
	for _, path := range []string{"roles/tenant-editor.yaml", "rbac/roles/other.yaml", "rbac/roles/../roles/tenant-editor.yaml", "rbac/roles/tenant-editor.json", "/rbac/roles/tenant-editor.yaml"} {
		_, err := ParseManifest(data, path, fixtureScope("Role"))
		require.Error(t, err, path)
	}
	cluster := modelFixture(t)[2]
	data, err = yaml.Marshal(cluster)
	require.NoError(t, err)
	_, err = ParseManifest(data, fixturePath(cluster), fixtureScope("Role"))
	require.Error(t, err)
	_, err = ParseManifest([]byte(strings.Repeat("x", MaxManifestBytes+1)), "rbac/roles/x.yaml", fixtureScope("Role"))
	require.Error(t, err)
	for _, count := range []int{100, 101} {
		role := modelFixture(t)[0]
		rule := role["spec"].(map[string]any)["rules"].([]any)[0]
		rules := make([]any, count)
		for i := range rules {
			rules[i] = rule
		}
		role["spec"].(map[string]any)["rules"] = rules
		data, err := yaml.Marshal(role)
		require.NoError(t, err)
		_, err = ParseManifest(data, fixturePath(role), fixtureScope("Role"))
		require.Equal(t, count == 101, err != nil)
	}
	for _, count := range []int{1000, 1001} {
		binding := modelFixture(t)[1]
		subjects := make([]any, count)
		for i := range subjects {
			subjects[i] = map[string]any{"kind": "User", "name": fmt.Sprintf("user-%d", i)}
		}
		binding["spec"].(map[string]any)["subjects"] = subjects
		data, err := json.Marshal(binding)
		require.NoError(t, err)
		_, err = ParseManifest(data, fixturePath(binding), fixtureScope("RoleBinding"))
		require.Equal(t, count == 1001, err != nil)
	}
}

func TestManifestRejectsAmbiguousYAML(t *testing.T) {
	prefix := "apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: Role\n"
	for _, input := range []string{
		prefix + "metadata: {name: x, name: y, namespace: acme}\nspec: {rules: []}\n",
		prefix + "metadata: &meta {name: x, namespace: acme}\nspec: {rules: [], extra: *meta}\n",
		prefix + "metadata: {name: x, namespace: 17}\nspec: {rules: []}\n",
		prefix + "metadata: {name: x, namespace: acme}\nspec: {rules: []}\n---\n{}\n",
	} {
		_, err := ParseManifest([]byte(input), "rbac/roles/x.yaml", fixtureScope("Role"))
		require.Error(t, err)
	}
}

func TestBindingRoleRefIsImmutableAndCandidatesHaveUniqueIdentities(t *testing.T) {
	binding := modelFixture(t)[1]
	old := parseFixture(t, binding)
	binding["spec"].(map[string]any)["roleRef"].(map[string]any)["name"] = "absent-role"
	updated := parseFixture(t, binding)
	require.Error(t, ValidateUpdate(old, updated))
	require.NoError(t, ValidateUpdate(old, old))
	require.Error(t, ValidateCandidate([]*Resource{old, old}))
	require.NoError(t, ValidateCandidate([]*Resource{old, parseFixture(t, modelFixture(t)[0])}))
}

func TestManifestCanonicalizesSetsWithoutChangingConditionMeaning(t *testing.T) {
	first := modelFixture(t)[0]
	second := modelFixture(t)[0]
	rules := second["spec"].(map[string]any)["rules"].([]any)
	rules[0].(map[string]any)["verbs"] = []string{"list", "read", "read"}
	conditions := rules[2].(map[string]any)["when"].([]any)
	conditions[0], conditions[1] = conditions[1], conditions[0]
	second["spec"].(map[string]any)["rules"] = []any{rules[2], rules[1], rules[0]}
	require.JSONEq(t, string(parseFixture(t, first).Spec), string(parseFixture(t, second).Spec))
}

func TestManifestLabelValueLimit(t *testing.T) {
	for _, count := range []int{63, 64} {
		resource := modelFixture(t)[0]
		resource["metadata"].(map[string]any)["labels"] = map[string]string{"team": strings.Repeat("x", count)}
		data, err := yaml.Marshal(resource)
		require.NoError(t, err)
		_, err = ParseManifest(data, fixturePath(resource), fixtureScope("Role"))
		require.Equal(t, count == 64, err != nil)
	}
}

func TestCandidateResourceAndAggregateByteLimits(t *testing.T) {
	source := parseFixture(t, modelFixture(t)[0])
	resources := make([]*Resource, 257)
	for i := range resources {
		copy := *source
		copy.Metadata.Name = fmt.Sprintf("role-%03d", i)
		resources[i] = &copy
	}
	require.NoError(t, ValidateCandidate(resources[:256]))
	require.ErrorContains(t, ValidateCandidate(resources), "256")

	document := modelFixture(t)[0]
	document["metadata"].(map[string]any)["name"] = "budget-000"
	padding := map[string]string{"pad": ""}
	document["metadata"].(map[string]any)["annotations"] = padding
	data, err := yaml.Marshal(document)
	require.NoError(t, err)
	padding["pad"] = strings.Repeat("x", MaxManifestBytes-len(data))
	data, err = yaml.Marshal(document)
	require.NoError(t, err)
	padding["pad"] += strings.Repeat("x", MaxManifestBytes-len(data))
	data, err = yaml.Marshal(document)
	require.NoError(t, err)
	require.Len(t, data, MaxManifestBytes)
	source, err = ParseManifest(data, fixturePath(document), fixtureScope("Role"))
	require.NoError(t, err)
	resources = make([]*Resource, 65)
	for i := range resources {
		copy := *source
		copy.Metadata.Name = fmt.Sprintf("budget-%03d", i)
		resources[i] = &copy
	}
	require.NoError(t, ValidateCandidate(resources[:64]))
	require.ErrorContains(t, ValidateCandidate(resources), "4 MiB")
}

func TestManifestSchemas(t *testing.T) {
	const base = "https://schemas.gitstore.dev/rbac.authorization.gitstore.dev/v1beta1/"
	files := map[string]string{
		"Role": "role.schema.json", "ClusterRole": "clusterrole.schema.json",
		"RoleBinding": "rolebinding.schema.json", "ClusterRoleBinding": "clusterrolebinding.schema.json",
	}
	compiler := jsonschema.NewCompiler()
	for _, file := range files {
		data, err := os.ReadFile("../../../schemas/rbac.authorization.gitstore.dev/v1beta1/" + file)
		require.NoError(t, err)
		require.NoError(t, compiler.AddResource(base+file, strings.NewReader(string(data))))
	}
	for _, resource := range modelFixture(t) {
		schema, err := compiler.Compile(base + files[resource["kind"].(string)])
		require.NoError(t, err)
		require.NoError(t, schema.Validate(resource))
		resource["unexpected"] = "value"
		require.Error(t, schema.Validate(resource))
	}
}

func TestManifestRBACActionScopes(t *testing.T) {
	for _, test := range []struct {
		index          int
		resource, verb string
		allowed        bool
	}{
		{0, "role", "escalate", true},
		{0, "roleBinding", "bind", true},
		{0, "role/access-review", "create", true},
		{2, "clusterRole", "escalate", true},
		{2, "clusterRoleBinding", "bind", true},
		{2, "clusterRole/access-review", "create", true},
		{0, "clusterRole", "escalate", false},
		{0, "clusterRoleBinding", "bind", false},
		{0, "clusterRole/access-review", "create", false},
		{0, "roleBinding", "escalate", false},
	} {
		t.Run(fmt.Sprintf("%d/%s/%s", test.index, test.resource, test.verb), func(t *testing.T) {
			document := modelFixture(t)[test.index]
			document["spec"].(map[string]any)["rules"] = []any{
				map[string]any{"resources": []string{test.resource}, "verbs": []string{test.verb}},
			}
			data, err := yaml.Marshal(document)
			require.NoError(t, err)
			_, err = ParseManifest(data, fixturePath(document), fixtureScope(document["kind"].(string)))
			if test.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
