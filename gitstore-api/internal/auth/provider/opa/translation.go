// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"

	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/v1/ast"
)

// Tier is the engine input, not evidence of source readiness. Snapshot capture
// and the provider must verify Git, lifecycle and watch continuity separately.
type Tier struct {
	ID        string
	Namespace string
	Active    bool
	Resources []*rbac.Resource
	Builtins  *rbac.BuiltinSet
}

type Translation struct {
	tierID, namespace, digest string
	active                    bool
	data                      *ast.Term
	bytes                     int
}

type translatedTier struct {
	Version  string                       `json:"version"`
	Builtins string                       `json:"builtins,omitempty"`
	ID       string                       `json:"id"`
	Roles    map[string]translatedRole    `json:"roles"`
	Bindings map[string]translatedBinding `json:"bindings"`
	Subjects map[string]map[string]bool   `json:"subjects"`
}

type translatedRole struct {
	UID          string                      `json:"uid"`
	Name         string                      `json:"name"`
	Kind         rbac.Kind                   `json:"kind"`
	NamespaceUID string                      `json:"namespace_uid"`
	Actions      map[string][]translatedRule `json:"actions"`
}

type translatedRule struct {
	Index int                   `json:"index"`
	Names []string              `json:"names"`
	When  []translatedCondition `json:"when"`
}

type translatedCondition struct {
	Token  string   `json:"token"`
	Attr   string   `json:"attr"`
	Op     string   `json:"op"`
	Value  string   `json:"value"`
	Values []string `json:"values"`
}

type translatedBinding struct {
	UID          string    `json:"uid"`
	Cluster      bool      `json:"cluster"`
	Namespace    string    `json:"namespace"`
	NamespaceUID string    `json:"namespace_uid"`
	RoleUID      string    `json:"role_uid"`
	RoleKind     rbac.Kind `json:"role_kind"`
	RoleName     string    `json:"role_name"`
}

func subjectKey(kind, name string) string { return kind + "\x00" + name }

// Translate builds a private, deterministic subject/action-indexed model once
// per tier rebuild. Unresolved or terminating resources contribute no grant.
func Translate(tier Tier) (*Translation, error) {
	return translate(tier, nil)
}

func translate(tier Tier, reserveAllocation func(int) bool) (*Translation, error) {
	cluster := tier.ID == rbac.ClusterTierID
	if !rbac.ValidTier(tier.ID) || cluster != (tier.Namespace == "") || cluster && !tier.Active ||
		len(tier.Resources) > rbac.MaxRolesPerTier+rbac.MaxBindingsPerTier {
		return nil, ErrUnavailable
	}
	if reserveAllocation != nil && !reserveAllocation(4096) {
		return nil, ErrUnavailable
	}
	model := translatedTier{Version: rbac.ModelVersion, ID: tier.ID,
		Roles: map[string]translatedRole{}, Bindings: map[string]translatedBinding{},
		Subjects: map[string]map[string]bool{}}
	resources := append([]*rbac.Resource(nil), tier.Resources...)
	if tier.Builtins != nil {
		if !cluster {
			return nil, ErrUnavailable
		}
		builtins, err := tier.Builtins.Resources()
		if err != nil {
			return nil, ErrUnavailable
		}
		resources = append(resources, builtins...)
		model.Builtins = tier.Builtins.Digest()
	}
	seenUIDs, seenNames := map[string]bool{}, map[string]bool{}
	roles, bindings, total := 0, 0, 0
	// Conservative serialized-size reservation precedes expanded action/index
	// allocation. In particular, a small wildcard manifest may expand greatly.
	remaining := rbac.MaxSnapshotBytes - 4096
	reserve := func(size int) bool {
		if size > remaining || reserveAllocation != nil && !reserveAllocation(size) {
			return false
		}
		remaining -= size
		return true
	}
	for index, resource := range resources {
		builtin := index >= len(tier.Resources)
		if resource == nil || resource.Metadata.UID == "" || resource.APIVersion != rbac.APIVersion ||
			resource.Kind.Namespaced() == cluster || resource.Metadata.Namespace != tier.Namespace ||
			!cluster && resource.NamespaceUID != tier.ID || cluster && resource.NamespaceUID != "" ||
			len(resource.Status) > 0 && !json.Valid(resource.Status) {
			return nil, ErrUnavailable
		}
		if uid, err := uuid.Parse(resource.Metadata.UID); err != nil || uid.String() != resource.Metadata.UID {
			return nil, ErrUnavailable
		}
		key := string(resource.Kind) + "/" + resource.Metadata.Name
		if seenUIDs[resource.Metadata.UID] || seenNames[key] {
			return nil, ErrUnavailable
		}
		seenUIDs[resource.Metadata.UID], seenNames[key] = true, true
		if len(resource.Spec) > rbac.MaxManifestBytes || len(resource.Status) > rbac.MaxSnapshotBytes-total {
			return nil, ErrUnavailable
		}
		var spec any = new(rbac.BindingSpec)
		if resource.Kind.IsRole() {
			spec = new(rbac.RoleSpec)
		}
		decoder := json.NewDecoder(bytes.NewReader(resource.Spec))
		decoder.DisallowUnknownFields()
		if decoder.Decode(spec) != nil || decoder.Decode(new(any)) != io.EOF {
			return nil, ErrUnavailable
		}
		encoded, err := rbac.CanonicalManifest(resource)
		if err != nil {
			return nil, ErrUnavailable
		}
		total += len(encoded) + len(resource.Status)
		if total > rbac.MaxSnapshotBytes {
			return nil, ErrUnavailable
		}
		parsed := resource
		if !builtin {
			path, err := rbac.CanonicalPath(resource.Kind, resource.Metadata.Name)
			if err != nil {
				return nil, ErrUnavailable
			}
			parsed, err = rbac.ParseManifest(encoded, path, rbac.RepositoryScope{Root: cluster, Namespace: tier.Namespace, NamespaceUID: tier.ID})
			if err != nil {
				return nil, ErrUnavailable
			}
		}
		if resource.Kind.IsRole() {
			if !builtin {
				roles++
			}
			if roles > rbac.MaxRolesPerTier {
				return nil, ErrUnavailable
			}
		} else {
			if !builtin {
				bindings++
			}
			if bindings > rbac.MaxBindingsPerTier {
				return nil, ErrUnavailable
			}
		}
		if !tier.Active || resource.Metadata.DeletionTimestamp != nil {
			continue
		}
		if !reserve(512) {
			return nil, ErrUnavailable
		}
		if resource.Kind.IsRole() {
			spec, err := rbac.DecodeRoleSpec(parsed)
			if err != nil {
				return nil, ErrUnavailable
			}
			role := translatedRole{UID: resource.Metadata.UID, Name: resource.Metadata.Name, Kind: resource.Kind,
				NamespaceUID: resource.NamespaceUID, Actions: map[string][]translatedRule{}}
			for index, rule := range spec.Rules {
				value := translatedRule{Index: index, Names: append([]string{}, rule.ResourceNames...), When: []translatedCondition{}}
				for _, condition := range rule.When {
					compiled := translatedCondition{Token: condition.Token, Attr: condition.Attr, Op: condition.Op, Values: append([]string{}, condition.Values...)}
					if condition.Value != nil {
						compiled.Value = *condition.Value
					}
					value.When = append(value.When, compiled)
				}
				actions, err := rbaclocal.ExpandActions(rule.Resources, rule.Verbs)
				if err != nil {
					return nil, ErrUnavailable
				}
				ruleBytes, err := json.Marshal(value)
				if err != nil {
					return nil, ErrUnavailable
				}
				for _, action := range actions {
					if !reserve(len(ruleBytes) + len(action.Action)*6 + 32) {
						return nil, ErrUnavailable
					}
					role.Actions[action.Action] = append(role.Actions[action.Action], value)
				}
			}
			model.Roles[role.UID] = role
		} else {
			spec, err := rbac.DecodeBindingSpec(parsed)
			if err != nil {
				return nil, ErrUnavailable
			}
			roleUID, err := rbac.ResolvedRoleUID(resource)
			if err != nil {
				return nil, ErrUnavailable
			}
			if roleUID == "" {
				continue
			}
			model.Bindings[resource.Metadata.UID] = translatedBinding{
				UID: resource.Metadata.UID, Cluster: cluster, Namespace: tier.Namespace, NamespaceUID: resource.NamespaceUID,
				RoleUID: roleUID, RoleKind: spec.RoleRef.Kind, RoleName: spec.RoleRef.Name}
			for _, subject := range spec.Subjects {
				key := subjectKey(subject.Kind, subject.Name)
				if !reserve(len(key)*6 + len(resource.Metadata.UID)*6 + 32) {
					return nil, ErrUnavailable
				}
				if model.Subjects[key] == nil {
					model.Subjects[key] = map[string]bool{}
				}
				model.Subjects[key][resource.Metadata.UID] = true
			}
		}
	}
	data, err := json.Marshal(model)
	if err != nil || len(data) > rbac.MaxSnapshotBytes {
		return nil, ErrUnavailable
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, ErrUnavailable
	}
	parsed, err := ast.InterfaceToValue(value)
	if err != nil {
		return nil, ErrUnavailable
	}
	sum := sha256.Sum256(data)
	return &Translation{tierID: tier.ID, namespace: tier.Namespace, active: tier.Active,
		digest: hex.EncodeToString(sum[:]), data: ast.NewTerm(parsed), bytes: len(data)}, nil
}
