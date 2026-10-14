// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"gopkg.in/yaml.v3"
)

const PolicyAPIVersion = "rbac.authorization.gitstore.dev/v1beta1"
const MaxPolicyBytes = 4 * 1024 * 1024
const MaxPrincipalGroups = 256
const maxPolicyRoles = 200
const maxPolicyBindings = 1000
const maxPolicyRules = 2000

// Policy is exclusively the strict, allow-only v1beta1 document.
type Policy struct {
	APIVersion          string                    `yaml:"apiVersion"`
	Kind                string                    `yaml:"kind"`
	ClusterRoles        map[string]RoleDefinition `yaml:"clusterRoles"`
	Roles               map[string]RoleDefinition `yaml:"roles"`
	ClusterRoleBindings []Binding                 `yaml:"clusterRoleBindings"`
	RoleBindings        []Binding                 `yaml:"roleBindings"`
	DefaultDeny         *bool                     `yaml:"defaultDeny"`
	digest              string
}

type RoleDefinition struct {
	Rules []Rule `yaml:"rules"`
}
type Rule struct {
	Resources     []string    `yaml:"resources"`
	Verbs         []string    `yaml:"verbs"`
	ResourceNames []string    `yaml:"resourceNames"`
	When          []Condition `yaml:"when"`
	actions       []auth.ActionDefinition
}
type RoleRef struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
}
type SubjectRef struct {
	Kind string `yaml:"kind"`
	Name string `yaml:"name"`
}
type Binding struct {
	Namespace *string      `yaml:"namespace"`
	RoleRef   RoleRef      `yaml:"roleRef"`
	Subjects  []SubjectRef `yaml:"subjects"`
}
type Condition struct {
	Token  string
	Attr   string
	Op     string
	Value  *string
	Values []string
}

// RolePolicy is the CLI action-list input, never a parsed policy role.
// The tooling slice replaces its legacy deny flag; v1 policy is not accepted.
type RolePolicy struct{ Allow, Deny []string }

func (c *Condition) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
		if node.Value != "owner" && node.Value != "targetOwnerMember" {
			return errors.New("unknown condition token")
		}
		c.Token = node.Value
		return nil
	}
	if node.Kind != yaml.MappingNode {
		return errors.New("when entries must be owner, targetOwnerMember or attribute comparisons")
	}
	fields := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if key != "attr" && key != "op" && key != "value" && key != "values" {
			return fmt.Errorf("unknown condition field %q", key)
		}
		fields[key] = node.Content[i+1]
	}
	for _, key := range []string{"attr", "op"} {
		n := fields[key]
		if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return fmt.Errorf("condition %s must be a string", key)
		}
	}
	c.Attr, c.Op = fields["attr"].Value, fields["op"].Value
	if c.Attr != "namespace" && c.Attr != "tier" && c.Attr != "visibility" {
		return errors.New("unknown condition attribute")
	}
	switch c.Op {
	case "eq", "neq":
		n := fields["value"]
		if n == nil || n.Kind != yaml.ScalarNode || n.Tag != "!!str" || fields["values"] != nil {
			return errors.New("eq/neq require only a string value")
		}
		value := n.Value
		c.Value = &value
	case "in", "notIn":
		n := fields["values"]
		if n == nil || n.Kind != yaml.SequenceNode || len(n.Content) == 0 || len(n.Content) > 64 || fields["value"] != nil {
			return errors.New("in/notIn require 1..64 string values and no value")
		}
		for _, v := range n.Content {
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				return errors.New("condition values must be strings")
			}
			c.Values = append(c.Values, v.Value)
		}
	default:
		return errors.New("unknown condition operator")
	}
	return nil
}

func loadPolicy(path string) (*Policy, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("rbaclocal: read policy: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxPolicyBytes+1))
	if err != nil {
		return nil, err
	}
	return parsePolicy(path, data)
}
func ValidatePolicyFile(path string) error { _, err := loadPolicy(path); return err }

func parsePolicy(path string, data []byte) (*Policy, error) {
	if len(data) > MaxPolicyBytes {
		return nil, errors.New("rbaclocal: policy exceeds 4 MiB")
	}
	// Inspect nodes before decoding to forbid aliases, duplicate keys and scalar
	// coercion. yaml.v3's string decoding alone accepts numeric scalar inputs.
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("rbaclocal: parse %q: %w", path, err)
	}
	if err := decoder.Decode(new(yaml.Node)); err != io.EOF {
		return nil, errors.New("rbaclocal: exactly one YAML document is required")
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("rbaclocal: policy must be a mapping")
	}
	version, err := policyMapping(document.Content[0], "apiVersion")
	if err != nil || version.Value != PolicyAPIVersion {
		return nil, fmt.Errorf("rbaclocal: supported apiVersion is %s; legacy versions are not supported", PolicyAPIVersion)
	}
	if err := validateYAMLNode(&document, 0); err != nil {
		return nil, fmt.Errorf("rbaclocal: %w", err)
	}
	var p Policy
	decoder = yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&p); err != nil {
		return nil, fmt.Errorf("rbaclocal: strict policy %q: %w", path, err)
	}
	if err := validatePolicy(&p); err != nil {
		return nil, fmt.Errorf("rbaclocal: invalid policy %q: %w", path, err)
	}
	if p.DefaultDeny == nil {
		secure := true
		p.DefaultDeny = &secure
	}
	digest := sha256.Sum256(data)
	p.digest = hex.EncodeToString(digest[:])
	return &p, nil
}

func validateYAMLNode(n *yaml.Node, depth int) error {
	if depth > 24 {
		return errors.New("policy nesting exceeds limit")
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errors.New("YAML anchors and aliases are not allowed")
	}
	if n.Kind == yaml.ScalarNode {
		if len(n.Value) > 256 || !utf8.ValidString(n.Value) {
			return errors.New("policy strings must be valid UTF-8 and at most 256 bytes")
		}
		if n.Tag != "!!str" && n.Tag != "!!bool" {
			return errors.New("policy scalars must be strings (defaultDeny must be boolean)")
		}
	}
	if n.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || keys[key.Value] {
				return errors.New("duplicate or non-string mapping key")
			}
			keys[key.Value] = true
			if value.Kind == yaml.ScalarNode && value.Tag == "!!bool" && key.Value != "defaultDeny" {
				return errors.New("boolean only allowed for defaultDeny")
			}
			if key.Value == "defaultDeny" && value.Tag != "!!bool" {
				return errors.New("defaultDeny must be boolean true")
			}
		}
	}
	if n.Kind == yaml.SequenceNode {
		for _, child := range n.Content {
			if child.Kind == yaml.ScalarNode && child.Tag != "!!str" {
				return errors.New("policy list values must be strings")
			}
		}
	}
	for _, child := range n.Content {
		if err := validateYAMLNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validatePolicy(p *Policy) error {
	if p.APIVersion != PolicyAPIVersion || p.Kind != "RbacLocalConfiguration" {
		return fmt.Errorf("expected apiVersion %s and kind RbacLocalConfiguration", PolicyAPIVersion)
	}
	if p.DefaultDeny != nil && !*p.DefaultDeny {
		return errors.New("defaultDeny:false is not supported; policy is allow-only with default deny")
	}
	if len(p.ClusterRoles)+len(p.Roles) > maxPolicyRoles {
		return errors.New("policy exceeds 200 roles")
	}
	if len(p.ClusterRoleBindings)+len(p.RoleBindings) > maxPolicyBindings {
		return errors.New("policy exceeds 1000 bindings")
	}
	rules := 0
	for _, tier := range []struct {
		roles   map[string]RoleDefinition
		cluster bool
	}{{p.ClusterRoles, true}, {p.Roles, false}} {
		names := make([]string, 0, len(tier.roles))
		for name := range tier.roles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !validIdentifier(name) {
				return errors.New("role name must be nonempty and at most 256 bytes")
			}
			role := tier.roles[name]
			rules += len(role.Rules)
			if rules > maxPolicyRules {
				return errors.New("policy exceeds 2000 rules")
			}
			for i := range role.Rules {
				rule := &role.Rules[i]
				if len(rule.Resources) == 0 || len(rule.Resources) > 128 || len(rule.Verbs) == 0 || len(rule.Verbs) > 32 || len(rule.ResourceNames) > 256 || len(rule.When) > 16 {
					return fmt.Errorf("role %q rule %d exceeds resources/verbs/names/conditions bounds", name, i)
				}
				for _, n := range rule.ResourceNames {
					if !validIdentifier(n) {
						return errors.New("resourceNames must be nonempty bounded strings")
					}
				}
				actions, err := ExpandActions(rule.Resources, rule.Verbs)
				if err != nil {
					return fmt.Errorf("role %q rule %d: %w", name, i, err)
				}
				for _, a := range actions {
					if !tier.cluster && a.Scope == "cluster" {
						return fmt.Errorf("namespaced Role %q cannot grant cluster-scoped %s", name, a.Kind)
					}
				}
				rule.actions = actions
			}
			tier.roles[name] = role
		}
	}
	for _, tier := range []struct {
		bindings []Binding
		cluster  bool
	}{{p.ClusterRoleBindings, true}, {p.RoleBindings, false}} {
		for i, b := range tier.bindings {
			if tier.cluster && (b.Namespace != nil || b.RoleRef.Kind != "ClusterRole") {
				return fmt.Errorf("cluster binding %d requires ClusterRole and no namespace", i)
			}
			if !tier.cluster && (b.Namespace == nil || !validIdentifier(*b.Namespace)) {
				return fmt.Errorf("namespace binding %d requires namespace", i)
			}
			if !validIdentifier(b.RoleRef.Name) {
				return errors.New("roleRef name required")
			}
			roles := p.ClusterRoles
			switch b.RoleRef.Kind {
			case "ClusterRole":
			case "Role":
				roles = p.Roles
			default:
				return errors.New("roleRef kind must be Role or ClusterRole")
			}
			if _, ok := roles[b.RoleRef.Name]; !ok {
				return fmt.Errorf("undefined %s %q", b.RoleRef.Kind, b.RoleRef.Name)
			}
			if len(b.Subjects) == 0 || len(b.Subjects) > 64 {
				return errors.New("binding requires 1..64 subjects")
			}
			for _, s := range b.Subjects {
				if !validIdentifier(s.Name) {
					return errors.New("subject name required and bounded")
				}
				switch s.Kind {
				case "User":
					if strings.HasPrefix(s.Name, "system:") && s.Name != "system:api" {
						return errors.New("reserved User subject; only in-process system:api may be bound")
					}
					if strings.HasPrefix(s.Name, "serviceaccount:") {
						return errors.New("legacy ServiceAccount subject; use kind ServiceAccount with bare name")
					}
				case "ServiceAccount":
					if strings.ContainsAny(s.Name, ":/") {
						return errors.New("ServiceAccount subjects use bare cluster name, no namespace or prefix")
					}
				case "Group":
				default:
					return errors.New("subject kind must be User, Group or ServiceAccount")
				}
			}
		}
	}
	return nil
}
func validIdentifier(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 256 && utf8.ValidString(s)
}

// ExpandActions projects only registered pairs. Exact resources×verbs must be
// valid across their full product; literal wildcards select finite pairs.
func ExpandActions(resources, verbs []string) ([]auth.ActionDefinition, error) {
	catalog := auth.AuthorizationActions()
	for _, r := range resources {
		if r != "*" && strings.Contains(r, "*") {
			return nil, errors.New("partial resource wildcards are not supported")
		}
		exists := r == "*"
		for _, a := range catalog {
			exists = exists || a.Resource == r
		}
		if !exists {
			return nil, fmt.Errorf("unknown resource %q%s", r, legacyHint(r))
		}
		for _, v := range verbs {
			if v != "*" && strings.Contains(v, "*") {
				return nil, errors.New("partial verb wildcards are not supported")
			}
			match := false
			for _, a := range catalog {
				if (r == "*" || r == a.Resource) && (v == "*" || v == a.Verb) {
					match = true
					break
				}
			}
			if !match {
				return nil, fmt.Errorf("unknown resource/verb %q/%q%s", r, v, legacyHint(strings.ReplaceAll(r, "/", ".")+"."+v))
			}
		}
	}
	matches := map[string]bool{}
	for _, r := range resources {
		for _, v := range verbs {
			for _, a := range catalog {
				if (r == "*" || r == a.Resource) && (v == "*" || v == a.Verb) {
					matches[a.Action] = true
				}
			}
		}
	}
	result := make([]auth.ActionDefinition, 0, len(matches))
	for _, a := range catalog {
		if matches[a.Action] {
			result = append(result, a)
		}
	}
	return result, nil
}
func legacyHint(s string) string {
	replacement := strings.TrimSuffix(strings.TrimSuffix(s, ".own"), ".any")
	if replacement == "category" {
		replacement = "categoryTaxonomy"
	}
	if strings.HasPrefix(replacement, "category.") {
		replacement = "categoryTaxonomy." + strings.TrimPrefix(replacement, "category.")
	}
	replacement = strings.ReplaceAll(replacement, "serviceaccount.", "serviceAccount.")
	replacement = strings.ReplaceAll(replacement, ".read.management", ".management.read")
	replacement = strings.ReplaceAll(replacement, ".list.management", ".management.list")
	replacement = strings.ReplaceAll(replacement, ".delete.complete", ".purge")
	if replacement == "namespace.create.user" || replacement == "namespace.create.organization" {
		replacement = "namespace.create"
	}
	if replacement != s {
		return "; use " + replacement
	}
	return ""
}
