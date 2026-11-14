// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/google/uuid"
)

const (
	ManagedBindingName = "namespace-owner-access"
	LifecycleFinalizer = "rbac.authorization.gitstore.dev/lifecycle"
	provenancePrefix   = "rbac.authorization.gitstore.dev/"
)

// Protection validates immutable identity and field ownership, not caller
// permissions. Transport/common admission must authorize the operation first.
type Protection struct {
	Builtins *BuiltinSet
	Managed  *ManagedBindingTemplate
}

type ManagedBindingTemplate struct {
	Namespace, NamespaceUID, Subject string
}

func (m ManagedBindingTemplate) UID() string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("gitstore/rbac/namespace-owner-access/"+m.NamespaceUID)).String()
}

func (m ManagedBindingTemplate) Resource() (*Resource, error) {
	if !validUUID(m.NamespaceUID) || ValidateKey(ResourceKey{TierID: m.NamespaceUID, Kind: RoleBinding, Name: ManagedBindingName}) != nil ||
		m.Namespace == "" || m.Subject == "" || strings.HasPrefix(m.Subject, "system:") {
		return nil, ErrInvalid
	}
	spec, err := json.Marshal(BindingSpec{RoleRef: RoleRef{Kind: ClusterRole, Name: "namespace-full-control"},
		Subjects: []Subject{{Kind: "User", Name: m.Subject}}})
	if err != nil {
		return nil, err
	}
	return &Resource{APIVersion: APIVersion, Kind: RoleBinding, NamespaceUID: m.NamespaceUID,
		Metadata: Metadata{Name: ManagedBindingName, Namespace: m.Namespace, Annotations: map[string]string{
			provenancePrefix + "managed-by":    "rbac-controller",
			provenancePrefix + "namespace-uid": m.NamespaceUID,
			provenancePrefix + "initiator":     m.Subject,
		}}, Spec: spec}, nil
}

func (m ManagedBindingTemplate) Check(resource *Resource) error {
	expected, err := m.Resource()
	if err != nil || resource == nil || resource.NamespaceUID != m.NamespaceUID ||
		resource.Metadata.UID != "" && resource.Metadata.UID != m.UID() {
		return ErrInvalid
	}
	actualBody, err := CanonicalManifest(resource)
	if err != nil {
		return ErrInvalid
	}
	expectedBody, err := CanonicalManifest(expected)
	if err != nil || !bytes.Equal(actualBody, expectedBody) {
		return ErrInvalid
	}
	return nil
}

func (m ManagedBindingTemplate) OrdinaryCollision(resource *Resource) bool {
	if !validUUID(m.NamespaceUID) || m.Namespace == "" || m.Subject == "" || strings.HasPrefix(m.Subject, "system:") ||
		resource == nil || resource.APIVersion != APIVersion || resource.Kind != RoleBinding ||
		resource.Metadata.Name != ManagedBindingName || resource.NamespaceUID != m.NamespaceUID ||
		resource.Metadata.Namespace != m.Namespace || !validUUID(resource.Metadata.UID) || resource.Metadata.UID == m.UID() {
		return false
	}
	for _, values := range []map[string]string{resource.Metadata.Labels, resource.Metadata.Annotations} {
		for key := range values {
			if strings.HasPrefix(key, provenancePrefix) {
				return false
			}
		}
	}
	return true
}

func (p Protection) mutable(resource *Resource) error {
	if resource == nil {
		return nil
	}
	if p.Builtins == nil || p.Builtins.Digest() == "" {
		return ErrUnavailable
	}
	if _, err := p.Builtins.Get(resource.Kind, resource.Metadata.Name); err == nil {
		return ErrInvalid
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
	if resource.Kind == RoleBinding && resource.Metadata.Name == ManagedBindingName {
		if p.Managed == nil {
			return ErrUnavailable
		}
		if !p.Managed.OrdinaryCollision(resource) {
			return p.Managed.Check(resource)
		}
	}
	tier := ClusterTierID
	if resource.Kind.Namespaced() {
		tier = resource.NamespaceUID
		if resource.Metadata.Namespace == "" {
			return ErrInvalid
		}
	}
	if resource.APIVersion != APIVersion ||
		ValidateKey(ResourceKey{TierID: tier, Kind: resource.Kind, Name: resource.Metadata.Name}) != nil {
		return ErrInvalid
	}
	for _, values := range []map[string]string{resource.Metadata.Labels, resource.Metadata.Annotations} {
		for key := range values {
			if strings.HasPrefix(key, provenancePrefix) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func (p Protection) AuthorChange(before, after *Resource) error {
	if before == nil && after == nil {
		return ErrInvalid
	}
	if p.Managed != nil {
		if after != nil && after.Kind == RoleBinding && after.Metadata.Name == ManagedBindingName &&
			(p.Managed.OrdinaryCollision(after) || p.Managed.OrdinaryCollision(before)) {
			return ErrInvalid
		}
		if before != nil && after == nil && before.Kind == RoleBinding &&
			before.Metadata.Name == ManagedBindingName && !p.Managed.OrdinaryCollision(before) {
			return ErrInvalid
		}
	}
	for _, resource := range []*Resource{before, after} {
		if err := p.mutable(resource); err != nil {
			return err
		}
	}
	if after == nil {
		return nil
	}
	previous := &Resource{}
	if before != nil {
		if before.Metadata.DeletionTimestamp != nil {
			return ErrConflict
		}
		normalized := *after
		if normalized.Metadata.UID == "" {
			normalized.Metadata.UID = before.Metadata.UID
		}
		if err := ValidateUpdate(before, &normalized); err != nil {
			return err
		}
		previous = before
	}
	a, b := after.Metadata, previous.Metadata
	if len(after.Status) > 0 && !bytes.Equal(after.Status, previous.Status) ||
		len(a.OwnerReferences) > 0 && !bytes.Equal(a.OwnerReferences, b.OwnerReferences) ||
		len(a.Finalizers) > 0 && !reflect.DeepEqual(a.Finalizers, b.Finalizers) ||
		a.DeletionTimestamp != nil && !reflect.DeepEqual(a.DeletionTimestamp, b.DeletionTimestamp) ||
		a.CreationTimestamp != nil && !reflect.DeepEqual(a.CreationTimestamp, b.CreationTimestamp) ||
		a.Generation != 0 && a.Generation != b.Generation ||
		a.ResourceVersion != "" && a.ResourceVersion != b.ResourceVersion ||
		a.Revision != "" && a.Revision != b.Revision {
		return ErrInvalid
	}
	return nil
}

func (p Protection) StatusChange(before, after *Resource) error {
	if before == nil || after == nil {
		return ErrInvalid
	}
	if err := p.mutable(before); err != nil {
		return err
	}
	if err := p.mutable(after); err != nil {
		return err
	}
	if err := ValidateUpdate(before, after); err != nil {
		return err
	}
	a, b := after.Metadata, before.Metadata
	if !bytes.Equal(before.Spec, after.Spec) || !reflect.DeepEqual(a.Labels, b.Labels) ||
		!reflect.DeepEqual(a.Annotations, b.Annotations) ||
		!reflect.DeepEqual(a.DeletionTimestamp, b.DeletionTimestamp) ||
		!reflect.DeepEqual(a.CreationTimestamp, b.CreationTimestamp) ||
		a.Generation != b.Generation || a.ResourceVersion != b.ResourceVersion || a.Revision != b.Revision {
		return ErrInvalid
	}
	otherFinalizers := func(values []string) ([]string, bool) {
		result, seen := []string{}, map[string]bool{}
		for _, value := range values {
			if value == "" || seen[value] || len(values) > 256 {
				return nil, false
			}
			seen[value] = true
			if value != LifecycleFinalizer {
				result = append(result, value)
			}
		}
		slices.Sort(result)
		return result, true
	}
	previous, previousOK := otherFinalizers(b.Finalizers)
	next, nextOK := otherFinalizers(a.Finalizers)
	if !previousOK || !nextOK || !slices.Equal(previous, next) {
		return ErrInvalid
	}
	var status map[string]json.RawMessage
	if len(after.Status) > MaxManifestBytes || len(after.Status) > 0 && json.Unmarshal(after.Status, &status) != nil {
		return ErrInvalid
	}
	oldUID, oldErr := ResolvedRoleUID(before)
	newUID, newErr := ResolvedRoleUID(after)
	if oldErr != nil || newErr != nil || oldUID != "" && oldUID != newUID || newUID != "" && !validUUID(newUID) {
		return ErrInvalid
	}
	var owners []struct {
		APIVersion         string `json:"apiVersion"`
		Kind               Kind   `json:"kind"`
		Name               string `json:"name"`
		UID                string `json:"uid"`
		BlockOwnerDeletion bool   `json:"blockOwnerDeletion"`
	}
	if len(a.OwnerReferences) > MaxManifestBytes ||
		len(a.OwnerReferences) > 0 && json.Unmarshal(a.OwnerReferences, &owners) != nil || len(owners) > 2 {
		return ErrInvalid
	}
	found, seen := false, map[string]bool{}
	for _, owner := range owners {
		if seen[owner.UID] {
			return ErrInvalid
		}
		seen[owner.UID] = true
		if owner.Kind == "Namespace" {
			if !after.Kind.Namespaced() || owner.APIVersion != "gitstore.dev/v1beta1" ||
				owner.UID != after.NamespaceUID || owner.Name != after.Metadata.Namespace || owner.BlockOwnerDeletion {
				return ErrInvalid
			}
			continue
		}
		spec, err := DecodeBindingSpec(after)
		if err != nil || owner.APIVersion != APIVersion || owner.UID != newUID || newUID == "" ||
			owner.Kind != spec.RoleRef.Kind || owner.Name != spec.RoleRef.Name || !owner.BlockOwnerDeletion {
			return ErrInvalid
		}
		found = true
	}
	if newUID != "" && !found && after.Metadata.DeletionTimestamp == nil {
		return ErrInvalid
	}
	return nil
}
