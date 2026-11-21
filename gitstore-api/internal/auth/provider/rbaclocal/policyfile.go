// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

var ErrRoleExists = errors.New("rbaclocal: role already exists")

const newPolicyFile = "apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles: {}\nroles: {}\nclusterRoleBindings: []\nroleBindings: []\n"

type RoleOptions struct{ Scope string }
type BindingOptions struct{ Scope, Namespace, SubjectKind, RoleKind string }

func policyScope(scope string) (bool, error) {
	switch scope {
	case "", "cluster":
		return true, nil
	case "namespace":
		return false, nil
	}
	return false, errors.New("rbaclocal: scope must be cluster or namespace")
}

// AddRole projects canonical capabilities exactly, without a resources × verbs
// cross-product overgrant. The complete candidate is validated before replacement.
func AddRole(path, name string, role RolePolicy, options ...RoleOptions) error {
	if !validIdentifier(name) {
		return errors.New("rbaclocal: role name is required and bounded")
	}
	if len(options) > 1 {
		return errors.New("rbaclocal: expected one role options value")
	}
	scope := ""
	if len(options) == 1 {
		scope = options[0].Scope
	}
	cluster, err := policyScope(scope)
	if err != nil {
		return err
	}
	if len(role.Deny) > 0 {
		return errors.New("rbaclocal: DENY/--deny is unsupported; v1beta1 is allow-only")
	}
	if len(role.Allow) == 0 {
		return errors.New("rbaclocal: role must define at least one canonical allow action")
	}
	projected, err := projectRoleActions(role.Allow)
	if err != nil {
		return err
	}
	original, mode, existed, err := readPolicyFile(path, true)
	if err != nil {
		return err
	}
	document, root, err := policyDocument(path, original)
	if err != nil {
		return err
	}
	key := "roles"
	if cluster {
		key = "clusterRoles"
	}
	roles, err := policyMapping(root, key)
	if err != nil {
		roles = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, scalarNode(key), roles)
	}
	if roles.Kind != yaml.MappingNode {
		return errors.New("rbaclocal: roles must be a mapping")
	}
	if _, err := policyMapping(roles, name); err == nil {
		return fmt.Errorf("%w: %q", ErrRoleExists, name)
	}
	roles.Style = 0
	roles.Content = append(roles.Content, scalarNode(name), projected)
	return encodeAndReplacePolicy(path, document, original, mode, existed)
}

func projectRoleActions(actions []string) (*yaml.Node, error) {
	pairs := map[string]map[string]bool{}
	for _, action := range actions {
		definition, ok := auth.LookupAction(action)
		if !ok {
			return nil, fmt.Errorf("rbaclocal: unknown or legacy action %q; use a canonical registered action", action)
		}
		if pairs[definition.Resource] == nil {
			pairs[definition.Resource] = map[string]bool{}
		}
		pairs[definition.Resource][definition.Verb] = true
	}
	resources := make([]string, 0, len(pairs))
	for resource := range pairs {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	rules := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, resource := range resources {
		verbs := make([]string, 0, len(pairs[resource]))
		for verb := range pairs[resource] {
			verbs = append(verbs, verb)
		}
		sort.Strings(verbs)
		rules.Content = append(rules.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalarNode("resources"), sequenceNode([]string{resource}), scalarNode("verbs"), sequenceNode(verbs)}})
	}
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{scalarNode("rules"), rules}}, nil
}

// AssignRole adds a typed binding. An identical subject/ref/scope assignment is
// a byte-for-byte no-op, including when the subject shares a multi-subject binding.
func AssignRole(path, subject, role string, options ...BindingOptions) (bool, error) {
	if !validIdentifier(subject) || !validIdentifier(role) {
		return false, errors.New("rbaclocal: subject and role are required and bounded")
	}
	if len(options) > 1 {
		return false, errors.New("rbaclocal: expected one binding options value")
	}
	opt := BindingOptions{}
	if len(options) == 1 {
		opt = options[0]
	}
	cluster, err := policyScope(opt.Scope)
	if err != nil {
		return false, err
	}
	if opt.SubjectKind == "" {
		opt.SubjectKind = "User"
	}
	if opt.RoleKind == "" {
		opt.RoleKind = "ClusterRole"
	}
	if cluster && (opt.Namespace != "" || opt.RoleKind != "ClusterRole") {
		return false, errors.New("rbaclocal: cluster binding requires ClusterRole and no namespace")
	}
	if !cluster && !validIdentifier(opt.Namespace) {
		return false, errors.New("rbaclocal: namespace binding requires an explicit namespace")
	}
	original, mode, existed, err := readPolicyFile(path, false)
	if err != nil {
		return false, err
	}
	document, root, err := policyDocument(path, original)
	if err != nil {
		return false, err
	}
	policy, err := parsePolicy(path, original)
	if err != nil {
		return false, err
	}
	roles := policy.ClusterRoles
	switch opt.RoleKind {
	case "ClusterRole":
	case "Role":
		roles = policy.Roles
	default:
		return false, errors.New("rbaclocal: role kind must be Role or ClusterRole")
	}
	if _, ok := roles[role]; !ok {
		return false, fmt.Errorf("rbaclocal: %s %q is not defined", opt.RoleKind, role)
	}
	key := "roleBindings"
	existing := policy.RoleBindings
	if cluster {
		key = "clusterRoleBindings"
		existing = policy.ClusterRoleBindings
	}
	for _, binding := range existing {
		namespace := ""
		if binding.Namespace != nil {
			namespace = *binding.Namespace
		}
		if namespace != opt.Namespace || binding.RoleRef.Kind != opt.RoleKind || binding.RoleRef.Name != role {
			continue
		}
		for _, bound := range binding.Subjects {
			if bound.Kind == opt.SubjectKind && bound.Name == subject {
				return false, nil
			}
		}
	}
	bindings, err := policyMapping(root, key)
	if err != nil {
		bindings = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, scalarNode(key), bindings)
	}
	candidate := Binding{RoleRef: RoleRef{Kind: opt.RoleKind, Name: role}, Subjects: []SubjectRef{{Kind: opt.SubjectKind, Name: subject}}}
	if !cluster {
		candidate.Namespace = &opt.Namespace
	}
	// Encode only explicitly applicable fields: namespace:null is invalid for a
	// cluster binding, and the strict parser deliberately rejects that ambiguity.
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if !cluster {
		node.Content = append(node.Content, scalarNode("namespace"), scalarNode(opt.Namespace))
	}
	var ref, subjects yaml.Node
	if err = ref.Encode(candidate.RoleRef); err != nil {
		return false, err
	}
	if err = subjects.Encode(candidate.Subjects); err != nil {
		return false, err
	}
	node.Content = append(node.Content, scalarNode("roleRef"), &ref, scalarNode("subjects"), &subjects)
	bindings.Style = 0
	bindings.Content = append(bindings.Content, node)
	if err = encodeAndReplacePolicy(path, document, original, mode, existed); err != nil {
		return false, err
	}
	return true, nil
}

func readPolicyFile(path string, create bool) ([]byte, fs.FileMode, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && create {
		return []byte(newPolicyFile), 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("rbaclocal: read %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("rbaclocal: stat %q: %w", path, err)
	}
	return b, info.Mode().Perm(), true, nil
}

func policyDocument(path string, data []byte) (*yaml.Node, *yaml.Node, error) {
	if _, err := parsePolicy(path, data); err != nil {
		return nil, nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, nil, fmt.Errorf("rbaclocal: parse %q: %w", path, err)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("rbaclocal: parse %q: document root must be a mapping", path)
	}
	return &document, document.Content[0], nil
}

func policyMapping(mapping *yaml.Node, key string) (*yaml.Node, error) {
	if mapping.Kind != yaml.MappingNode {
		return nil, errors.New("expected a mapping")
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], nil
		}
	}
	return nil, fmt.Errorf("missing %q", key)
}

func sequenceNode(values []string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		node.Content = append(node.Content, scalarNode(value))
	}
	return node
}

func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func encodeAndReplacePolicy(path string, document *yaml.Node, original []byte, mode fs.FileMode, existed bool) error {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return fmt.Errorf("rbaclocal: encode %q: %w", path, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("rbaclocal: encode %q: %w", path, err)
	}
	if _, err := parsePolicy(path, output.Bytes()); err != nil {
		return err
	}
	return replacePolicyFile(path, original, output.Bytes(), mode, existed)
}

func replacePolicyFile(path string, original, replacement []byte, mode fs.FileMode, existed bool) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy.yaml.tmp-*")
	if err != nil {
		return fmt.Errorf("rbaclocal: create temporary file beside %q: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("rbaclocal: set temporary file mode: %w", err)
	}
	if _, err := tmp.Write(replacement); err != nil {
		tmp.Close()
		return fmt.Errorf("rbaclocal: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("rbaclocal: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("rbaclocal: close temporary file: %w", err)
	}
	current, err := os.ReadFile(path)
	if existed {
		if err != nil || !bytes.Equal(current, original) {
			return fmt.Errorf("rbaclocal: %q changed while updating policy; retry", path)
		}
	} else if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("rbaclocal: %q was created while updating policy; retry", path)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rbaclocal: replace %q: %w", path, err)
	}
	return nil
}
