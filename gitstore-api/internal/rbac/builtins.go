// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/google/uuid"
)

// BuiltinSet can only contain release definitions. Readback returns copies;
// author labels, names and status cannot establish this provenance.
type BuiltinSet struct {
	resources []*Resource
	digest    string
}

func (b *BuiltinSet) Digest() string {
	if b == nil {
		return ""
	}
	return b.digest
}

func (b *BuiltinSet) Resources() ([]*Resource, error) {
	if b == nil || b.digest == "" {
		return nil, ErrUnavailable
	}
	result := make([]*Resource, 0, len(b.resources))
	for _, resource := range b.resources {
		copy, err := CloneResource(resource)
		if err != nil {
			return nil, err
		}
		result = append(result, copy)
	}
	return result, nil
}

func (b *BuiltinSet) Get(kind Kind, name string) (*Resource, error) {
	if b == nil || b.digest == "" {
		return nil, ErrUnavailable
	}
	for _, resource := range b.resources {
		if resource.Kind == kind && resource.Metadata.Name == name {
			return CloneResource(resource)
		}
	}
	return nil, ErrNotFound
}

func builtinUID(kind Kind, name string) string {
	// Incarnation stays stable across release content changes so existing
	// resolved UID links do not silently become links to a recreated role.
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(APIVersion+"/"+string(kind)+"/"+name)).String()
}

func ReleaseBuiltins() (*BuiltinSet, error) {
	rule := func(resources, verbs []string) Rule { return Rule{Resources: resources, Verbs: verbs} }
	core := []string{"repository", "categoryTaxonomy", "product", "productVariant", "collection", "file"}
	read := []string{"read", "list", "watch"}
	write := []string{"create", "read", "list", "watch", "update", "delete"}
	management := rule([]string{"product/management", "productVariant/management"}, []string{"read", "list"})
	userTier := "USER"
	roles := map[string][]Rule{
		"cluster-admin": {rule([]string{"*"}, []string{"*"})},
		"admin": {
			rule(core, []string{"create", "read", "list", "watch", "update", "delete", "transfer"}),
			rule([]string{"repository", "categoryTaxonomy", "product"}, []string{"purge"}), management,
			rule([]string{"role", "roleBinding"}, write),
			rule([]string{"role/access-review"}, []string{"create"}),
		},
		"edit": {rule(core, write), management},
		"view": {rule(core, read), management},
		"namespace-owner": {{
			Resources: []string{"namespace"}, Verbs: []string{"read", "update", "delete", "transfer"},
			When: []Condition{{Token: "owner"}},
		}},
		"namespace-creator": {{
			Resources: []string{"namespace"}, Verbs: []string{"create"},
			When: []Condition{{Token: "subjectUsername"}, {Attr: "tier", Op: "eq", Value: &userTier}},
		}},
		"controller": {
			rule([]string{"namespace", "repository", "categoryTaxonomy", "product", "file"}, read),
			rule([]string{"namespace/status", "repository/status", "categoryTaxonomy/status", "product/status", "file/status"}, []string{"write"}),
			rule([]string{"namespace", "repository", "categoryTaxonomy", "product"}, []string{"purge"}),
			rule([]string{"namespace/system-repository"}, []string{"provision"}),
			rule([]string{"repository"}, []string{"create"}),
			rule([]string{"product/management"}, []string{"read", "list"}),
			rule([]string{"role", "clusterRole", "roleBinding", "clusterRoleBinding"}, read),
			rule([]string{"role/status", "clusterRole/status", "roleBinding/status", "clusterRoleBinding/status"}, []string{"write"}),
			rule([]string{"role", "clusterRole", "roleBinding", "clusterRoleBinding"}, []string{"purge"}),
			{Resources: []string{"roleBinding"}, Verbs: []string{"create", "update", "delete", "bind"}, ResourceNames: []string{"namespace-owner-access"}},
		},
		"controller-token": {{
			Resources: []string{"serviceAccount/token"}, Verbs: []string{"issue"}, ResourceNames: []string{"gitstore-controller-manager"},
		}},
		"system:api-internal": {
			rule([]string{"repository"}, []string{"create", "delete"}),
			rule([]string{"repository/contents"}, []string{"read", "write"}),
		},
		"public-reader": {{
			Resources: []string{"repository/contents"}, Verbs: []string{"read"},
			When: []Condition{{Attr: "visibility", Op: "eq", Value: new("public")}},
		}},
	}
	actions, err := rbaclocal.ExpandActions([]string{"*"}, []string{"*"})
	if err != nil {
		return nil, err
	}
	namespaced := map[string][]string{}
	for _, action := range actions {
		if action.Scope == "namespace" {
			namespaced[action.Resource] = append(namespaced[action.Resource], action.Verb)
		}
	}
	resources := make([]string, 0, len(namespaced))
	for resource := range namespaced {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	for _, resource := range resources {
		roles["namespace-full-control"] = append(roles["namespace-full-control"], rule([]string{resource}, namespaced[resource]))
	}
	b := &BuiltinSet{}
	roleNames := make([]string, 0, len(roles))
	for name := range roles {
		roleNames = append(roleNames, name)
	}
	sort.Strings(roleNames)
	for _, name := range roleNames {
		for _, rule := range roles[name] {
			if _, err := rbaclocal.ExpandActions(rule.Resources, rule.Verbs); err != nil {
				return nil, err
			}
		}
		spec, err := json.Marshal(RoleSpec{Rules: roles[name]})
		if err != nil {
			return nil, err
		}
		b.resources = append(b.resources, &Resource{APIVersion: APIVersion, Kind: ClusterRole,
			Metadata: Metadata{Name: name, UID: builtinUID(ClusterRole, name), Generation: 1}, Spec: spec})
	}
	for _, binding := range []struct{ role, kind, subject string }{
		{"namespace-owner", "Group", "system:authenticated"},
		{"namespace-creator", "Group", "system:staff"},
		{"controller", "ServiceAccount", "gitstore-controller-manager"},
		{"controller-token", "ServiceAccount", "gitstore-controller-manager"},
		{"system:api-internal", "User", "system:api"},
		{"public-reader", "Group", "system:unauthenticated"},
	} {
		spec, _ := json.Marshal(BindingSpec{RoleRef: RoleRef{Kind: ClusterRole, Name: binding.role},
			Subjects: []Subject{{Kind: binding.kind, Name: binding.subject}}})
		status, _ := json.Marshal(map[string]any{"resolved": map[string]any{"roleRef": map[string]string{"uid": builtinUID(ClusterRole, binding.role)}}})
		b.resources = append(b.resources, &Resource{APIVersion: APIVersion, Kind: ClusterRoleBinding,
			Metadata: Metadata{Name: binding.role, UID: builtinUID(ClusterRoleBinding, binding.role), Generation: 1},
			Spec:     spec, Status: status})
	}
	data, err := json.Marshal(b.resources)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	b.digest = hex.EncodeToString(sum[:])
	for _, resource := range b.resources {
		resource.Metadata.Revision, resource.Metadata.ResourceVersion = b.digest, b.digest
	}
	return b, nil
}
