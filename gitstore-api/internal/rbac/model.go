// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion            = "rbac.authorization.gitstore.dev/v1beta1"
	ModelVersion          = "v1"
	MaxManifestBytes      = 64 << 10
	MaxCandidateBytes     = 4 << 20
	MaxCandidateResources = 256
	MaxRolesPerTier       = 500
	MaxBindingsPerTier    = 5000
	MaxRulesPerRole       = 100
	MaxSubjectsPerBinding = 1000
	MaxSnapshotBytes      = 64 << 20
	SnapshotPageSize      = 1000
)

type Kind = datastore.RBACKind

const (
	Role               = datastore.RBACRole
	ClusterRole        = datastore.RBACClusterRole
	RoleBinding        = datastore.RBACRoleBinding
	ClusterRoleBinding = datastore.RBACClusterRoleBinding
)

type RepositoryScope struct {
	Namespace    string
	NamespaceUID string
	Root         bool
}

type Metadata = datastore.RBACMetadata
type Resource = datastore.RBACResource

type RoleSpec struct {
	Rules []Rule `json:"rules" yaml:"rules"`
}

type Rule struct {
	Resources     []string    `json:"resources" yaml:"resources"`
	Verbs         []string    `json:"verbs" yaml:"verbs"`
	ResourceNames []string    `json:"resourceNames,omitempty" yaml:"resourceNames,omitempty"`
	When          []Condition `json:"when,omitempty" yaml:"when,omitempty"`
}

type Condition rbaclocal.Condition

func (c *Condition) UnmarshalYAML(node *yaml.Node) error {
	var condition rbaclocal.Condition
	if err := condition.UnmarshalYAML(node); err != nil {
		return err
	}
	*c = Condition(condition)
	return nil
}

func (c *Condition) UnmarshalJSON(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	if len(document.Content) != 1 {
		return errors.New("condition is required")
	}
	return c.UnmarshalYAML(document.Content[0])
}

func (c Condition) wireValue() any {
	if c.Token != "" {
		return c.Token
	}
	return struct {
		Attr   string   `json:"attr" yaml:"attr"`
		Op     string   `json:"op" yaml:"op"`
		Value  *string  `json:"value,omitempty" yaml:"value,omitempty"`
		Values []string `json:"values,omitempty" yaml:"values,omitempty"`
	}{c.Attr, c.Op, c.Value, c.Values}
}

func (c Condition) MarshalJSON() ([]byte, error) { return json.Marshal(c.wireValue()) }
func (c Condition) MarshalYAML() (any, error)    { return c.wireValue(), nil }

type RoleRef struct {
	Kind Kind   `json:"kind" yaml:"kind"`
	Name string `json:"name" yaml:"name"`
}

type Subject struct {
	Kind string `json:"kind" yaml:"kind"`
	Name string `json:"name" yaml:"name"`
}

type BindingSpec struct {
	RoleRef  RoleRef   `json:"roleRef" yaml:"roleRef"`
	Subjects []Subject `json:"subjects" yaml:"subjects"`
}

func DecodeRoleSpec(r *Resource) (RoleSpec, error) {
	var spec RoleSpec
	if !r.Kind.IsRole() {
		return spec, errors.New("resource is not a role")
	}
	err := json.Unmarshal(r.Spec, &spec)
	return spec, err
}

func DecodeBindingSpec(r *Resource) (BindingSpec, error) {
	var spec BindingSpec
	if r.Kind != RoleBinding && r.Kind != ClusterRoleBinding {
		return spec, errors.New("resource is not a binding")
	}
	err := json.Unmarshal(r.Spec, &spec)
	return spec, err
}

func ValidateUpdate(old, updated *Resource) error {
	if old == nil || updated == nil {
		return errors.New("both resource versions are required")
	}
	if old.Kind != updated.Kind || old.Metadata.Name != updated.Metadata.Name ||
		old.Metadata.Namespace != updated.Metadata.Namespace || old.NamespaceUID != updated.NamespaceUID ||
		old.Metadata.UID != updated.Metadata.UID {
		return errors.New("resource identity is immutable")
	}
	if !old.Kind.IsRole() {
		before, err := DecodeBindingSpec(old)
		if err != nil {
			return err
		}
		after, err := DecodeBindingSpec(updated)
		if err != nil {
			return err
		}
		if before.RoleRef != after.RoleRef {
			return errors.New("roleRef is immutable")
		}
	}
	return nil
}

func ValidateCandidate(resources []*Resource) error {
	if len(resources) > MaxCandidateResources {
		return errors.New("candidate exceeds 256 changed resources")
	}
	seen := make(map[string]bool, len(resources))
	total := 0
	for _, resource := range resources {
		if resource == nil {
			return errors.New("candidate contains a nil resource")
		}
		key := string(resource.Kind) + "\x00" + resource.NamespaceUID + "\x00" + resource.Metadata.Name
		if seen[key] {
			return errors.New("candidate contains duplicate resource identities")
		}
		seen[key] = true
		size := resource.ManifestBytes
		if size == 0 {
			data, err := CanonicalManifest(resource)
			if err != nil {
				return err
			}
			size = len(data)
		}
		if size > MaxManifestBytes || size > MaxCandidateBytes-total {
			return errors.New("candidate exceeds manifest or 4 MiB aggregate bound")
		}
		total += size
	}
	return nil
}

func canonicalRules(rules []rbaclocal.Rule) []Rule {
	result := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		normalized := Rule{
			Resources: sortedSet(rule.Resources), Verbs: sortedSet(rule.Verbs),
			ResourceNames: sortedSet(rule.ResourceNames),
		}
		for _, condition := range rule.When {
			condition.Values = sortedSet(condition.Values)
			normalized.When = append(normalized.When, Condition(condition))
		}
		sort.Slice(normalized.When, func(i, j int) bool {
			return canonicalKey(normalized.When[i]) < canonicalKey(normalized.When[j])
		})
		normalized.When = slices.CompactFunc(normalized.When, func(a, b Condition) bool {
			return canonicalKey(a) == canonicalKey(b)
		})
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool { return canonicalKey(result[i]) < canonicalKey(result[j]) })
	return slices.CompactFunc(result, func(a, b Rule) bool { return canonicalKey(a) == canonicalKey(b) })
}

func sortedSet(values []string) []string {
	slices.Sort(values)
	return slices.Compact(values)
}

func canonicalKey(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func strictYAML(data []byte, value any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("invalid RBAC manifest: %w", err)
	}
	return nil
}
