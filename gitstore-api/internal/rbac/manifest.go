// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"gopkg.in/yaml.v3"
)

var resourceNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type authorMetadata struct {
	Name        string            `yaml:"name"`
	Namespace   *string           `yaml:"namespace,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

type manifest struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       Kind           `yaml:"kind"`
	Metadata   authorMetadata `yaml:"metadata"`
	Spec       yaml.Node      `yaml:"spec"`
}

func CanonicalPath(kind Kind, name string) (string, error) {
	folder := map[Kind]string{
		Role: "roles", ClusterRole: "clusterroles",
		RoleBinding: "rolebindings", ClusterRoleBinding: "clusterrolebindings",
	}[kind]
	if folder == "" || !resourceNamePattern.MatchString(name) {
		return "", errors.New("invalid RBAC kind or resource name")
	}
	return "rbac/" + folder + "/" + name + ".yaml", nil
}

func ParseManifest(data []byte, path string, scope RepositoryScope) (*Resource, error) {
	if len(data) == 0 || len(data) > MaxManifestBytes {
		return nil, errors.New("RBAC manifest must contain 1..65536 bytes")
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid RBAC YAML: %w", err)
	}
	if err := decoder.Decode(new(yaml.Node)); err != io.EOF {
		return nil, errors.New("RBAC manifest requires exactly one YAML document")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("RBAC manifest must be an object")
	}
	if err := validateManifestNode(&document, 0); err != nil {
		return nil, err
	}
	var wire manifest
	if err := strictYAML(data, &wire); err != nil {
		return nil, err
	}
	for _, value := range wire.Metadata.Labels {
		if utf8.RuneCountInString(value) > 63 {
			return nil, errors.New("RBAC label values must be at most 63 characters")
		}
	}
	if wire.APIVersion != APIVersion {
		return nil, errors.New("unsupported RBAC apiVersion")
	}
	canonicalPath, err := CanonicalPath(wire.Kind, wire.Metadata.Name)
	if err != nil {
		return nil, err
	}
	if path != canonicalPath {
		return nil, fmt.Errorf("RBAC manifest must reside at %s", canonicalPath)
	}
	if wire.Kind.Namespaced() {
		if scope.Namespace == "" || scope.NamespaceUID == "" || wire.Metadata.Namespace == nil || *wire.Metadata.Namespace != scope.Namespace {
			return nil, errors.New("RBAC namespace must match the repository namespace and live UID")
		}
	} else if !scope.Root || wire.Metadata.Namespace != nil {
		return nil, errors.New("cluster RBAC requires the root system repository and no namespace")
	}
	if wire.Spec.Kind != yaml.MappingNode {
		return nil, errors.New("RBAC spec must be an object")
	}
	specData, err := yaml.Marshal(&wire.Spec)
	if err != nil {
		return nil, err
	}
	resource := &Resource{
		APIVersion: APIVersion, Kind: wire.Kind, ManifestBytes: len(data),
		Metadata: Metadata{Name: wire.Metadata.Name, Labels: wire.Metadata.Labels, Annotations: wire.Metadata.Annotations},
	}
	if wire.Kind.Namespaced() {
		resource.Metadata.Namespace, resource.NamespaceUID = scope.Namespace, scope.NamespaceUID
	}
	if wire.Kind.IsRole() {
		var spec RoleSpec
		if err := strictYAML(specData, &spec); err != nil {
			return nil, err
		}
		if spec.Rules == nil || len(spec.Rules) > MaxRulesPerRole {
			return nil, errors.New("role requires rules array with at most 100 entries")
		}
		role, err := rbaclocal.ParseRoleDefinition(specData, wire.Kind == ClusterRole)
		if err != nil {
			return nil, err
		}
		resource.Spec, err = json.Marshal(RoleSpec{Rules: canonicalRules(role.Rules)})
		if err != nil {
			return nil, err
		}
	} else {
		var spec BindingSpec
		if err := strictYAML(specData, &spec); err != nil {
			return nil, err
		}
		if (spec.RoleRef.Kind != Role && spec.RoleRef.Kind != ClusterRole) ||
			(wire.Kind == ClusterRoleBinding && spec.RoleRef.Kind != ClusterRole) ||
			!resourceNamePattern.MatchString(spec.RoleRef.Name) {
			return nil, errors.New("invalid binding roleRef")
		}
		if len(spec.Subjects) < 1 || len(spec.Subjects) > MaxSubjectsPerBinding {
			return nil, errors.New("binding requires 1..1000 subjects")
		}
		seen := make(map[Subject]bool, len(spec.Subjects))
		for _, subject := range spec.Subjects {
			if err := rbaclocal.ValidateSubject(rbaclocal.SubjectRef{Kind: subject.Kind, Name: subject.Name}); err != nil {
				return nil, err
			}
			if seen[subject] {
				return nil, errors.New("duplicate binding subject")
			}
			seen[subject] = true
		}
		sort.Slice(spec.Subjects, func(i, j int) bool {
			a, b := spec.Subjects[i], spec.Subjects[j]
			return a.Kind < b.Kind || a.Kind == b.Kind && a.Name < b.Name
		})
		resource.Spec, err = json.Marshal(spec)
		if err != nil {
			return nil, err
		}
	}
	return resource, nil
}

func CanonicalManifest(r *Resource) ([]byte, error) {
	var spec any
	if r.Kind.IsRole() {
		value, err := DecodeRoleSpec(r)
		if err != nil {
			return nil, err
		}
		spec = value
	} else {
		value, err := DecodeBindingSpec(r)
		if err != nil {
			return nil, err
		}
		spec = value
	}
	metadata := authorMetadata{Name: r.Metadata.Name, Labels: r.Metadata.Labels, Annotations: r.Metadata.Annotations}
	if r.Kind.Namespaced() {
		metadata.Namespace = &r.Metadata.Namespace
	}
	return yaml.Marshal(struct {
		APIVersion string         `yaml:"apiVersion"`
		Kind       Kind           `yaml:"kind"`
		Metadata   authorMetadata `yaml:"metadata"`
		Spec       any            `yaml:"spec"`
	}{r.APIVersion, r.Kind, metadata, spec})
}

// ManifestTreeDigest identifies author-controlled RBAC content, independently
// of YAML formatting, Git's unrelated paths and controller-owned observations.
func ManifestTreeDigest(resources []*Resource) (string, error) {
	if len(resources) > MaxRolesPerTier+MaxBindingsPerTier {
		return "", ErrInvalid
	}
	entries := make(map[string][32]byte, len(resources))
	paths := make([]string, 0, len(resources))
	for _, resource := range resources {
		if resource == nil {
			return "", ErrInvalid
		}
		if resource.Metadata.DeletionTimestamp != nil {
			continue
		}
		path, err := CanonicalPath(resource.Kind, resource.Metadata.Name)
		if err != nil {
			return "", err
		}
		if _, exists := entries[path]; exists {
			return "", ErrInvalid
		}
		content, err := CanonicalManifest(resource)
		if err != nil {
			return "", err
		}
		entries[path] = sha256.Sum256(content)
		paths = append(paths, path)
	}
	sort.Strings(paths)
	digest := sha256.New()
	for _, path := range paths {
		_, _ = fmt.Fprintf(digest, "%d:%s:%x\n", len(path), path, entries[path])
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func validateManifestNode(node *yaml.Node, depth int) error {
	if depth > 24 {
		return errors.New("RBAC manifest nesting exceeds limit")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("YAML anchors and aliases are not allowed")
	}
	if node.Kind == yaml.ScalarNode && (node.Tag != "!!str" || !utf8.ValidString(node.Value)) {
		return errors.New("RBAC manifest values must be valid UTF-8 strings")
	}
	if node.Kind == yaml.MappingNode {
		keys := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || strings.TrimSpace(key.Value) == "" || keys[key.Value] {
				return errors.New("duplicate or invalid RBAC mapping key")
			}
			keys[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateManifestNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
