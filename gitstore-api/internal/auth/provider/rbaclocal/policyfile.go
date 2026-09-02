// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

var ErrRoleExists = errors.New("rbaclocal: role already exists")

const newPolicyFile = "version: v1\nroles: {}\ndefault_deny: true\nrole_bindings: {}\n"

// AddRole appends a role definition through a validated atomic YAML update.
func AddRole(path, name string, role RolePolicy) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("rbaclocal: role name is required")
	}
	if len(role.Allow) == 0 && len(role.Deny) == 0 {
		return errors.New("rbaclocal: role must define at least one allow or deny action")
	}
	original, mode, existed, err := readPolicyFile(path, true)
	if err != nil {
		return err
	}
	document, root, err := policyDocument(path, original)
	if err != nil {
		return err
	}
	roles, err := policyMapping(root, "roles")
	if err != nil || roles.Kind != yaml.MappingNode {
		return fmt.Errorf("rbaclocal: parse %q: roles must be a mapping", path)
	}
	if _, err := policyMapping(roles, name); err == nil {
		return fmt.Errorf("%w: %q", ErrRoleExists, name)
	}
	roles.Style = 0
	roles.Content = append(roles.Content, scalarNode(name), roleNode(role))
	return encodeAndReplacePolicy(path, document, original, mode, existed)
}

// AssignRole adds an existing role to a subject. It returns false without
// changing the file when that exact binding is already present.
func AssignRole(path, subject, role string) (bool, error) {
	if strings.TrimSpace(subject) == "" || strings.TrimSpace(role) == "" {
		return false, errors.New("rbaclocal: subject and role are required")
	}
	original, mode, existed, err := readPolicyFile(path, false)
	if err != nil {
		return false, err
	}
	document, root, err := policyDocument(path, original)
	if err != nil {
		return false, err
	}
	roles, err := policyMapping(root, "roles")
	if err != nil {
		return false, fmt.Errorf("rbaclocal: parse %q: %w", path, err)
	}
	if _, err := policyMapping(roles, role); err != nil {
		return false, fmt.Errorf("rbaclocal: role %q is not defined", role)
	}
	bindings, err := policyMapping(root, "role_bindings")
	if err != nil {
		bindings = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, scalarNode("role_bindings"), bindings)
	}
	if bindings.Kind != yaml.MappingNode {
		return false, fmt.Errorf("rbaclocal: parse %q: role_bindings must be a mapping", path)
	}
	boundRoles, err := policyMapping(bindings, subject)
	if err != nil {
		boundRoles = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		bindings.Content = append(bindings.Content, scalarNode(subject), boundRoles)
	}
	if boundRoles.Kind != yaml.SequenceNode {
		return false, fmt.Errorf("rbaclocal: parse %q: binding for %q must be a sequence", path, subject)
	}
	for _, boundRole := range boundRoles.Content {
		if boundRole.Value == role {
			return false, nil
		}
	}
	bindings.Style = 0
	boundRoles.Style = 0
	boundRoles.Content = append(boundRoles.Content, scalarNode(role))
	if err := encodeAndReplacePolicy(path, document, original, mode, existed); err != nil {
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
	if _, err := parsePolicy(path, data); err != nil && string(data) != newPolicyFile {
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

func roleNode(role RolePolicy) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	node.Content = append(node.Content, scalarNode("allow"), sequenceNode(role.Allow))
	node.Content = append(node.Content, scalarNode("deny"), sequenceNode(role.Deny))
	return node
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
