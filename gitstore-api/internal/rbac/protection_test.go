// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProtectionRejectsBuiltinManagedAndCrossIncarnationWrites(t *testing.T) {
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	protection := rbac.Protection{Builtins: builtins}
	_, _, role, binding := referenceFixture(t)
	for _, invalid := range []string{"builtin", "managed-name", "managed-annotation", "uid", "role-ref", "status"} {
		t.Run(invalid, func(t *testing.T) {
			before, err := rbac.CloneResource(binding)
			require.NoError(t, err)
			after, err := rbac.CloneResource(binding)
			require.NoError(t, err)
			switch invalid {
			case "builtin":
				after, err = builtins.Get(rbac.ClusterRole, "admin")
				require.NoError(t, err)
				before = nil
			case "managed-name":
				after.Metadata.Name = "namespace-owner-access"
				before = nil
			case "managed-annotation":
				after.Metadata.Annotations = map[string]string{"rbac.authorization.gitstore.dev/managed-by": "controller"}
			case "uid":
				after.Metadata.UID = uuid.NewString()
			case "role-ref":
				after.Spec = json.RawMessage(`{"roleRef":{"kind":"Role","name":"other"},"subjects":[{"kind":"User","name":"alice"}]}`)
			case "status":
				after.Status = json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + role.Metadata.UID + `"}}}`)
			}
			require.Error(t, protection.AuthorChange(before, after))
		})
	}
	copy, err := rbac.CloneResource(role)
	require.NoError(t, err)
	copy.Metadata.Labels = map[string]string{"team": "catalog"}
	require.NoError(t, protection.AuthorChange(role, copy))
}

func TestStatusProtectionPreservesOthersAndRejectsCrossUIDOwners(t *testing.T) {
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	protection := rbac.Protection{Builtins: builtins}
	_, _, _, binding := referenceFixture(t)
	binding.Metadata.Finalizers = []string{"other.gitstore.dev/controller"}
	for _, invalid := range []string{"other-finalizer", "spec", "namespace-owner", "role-owner"} {
		t.Run(invalid, func(t *testing.T) {
			after, err := rbac.CloneResource(binding)
			require.NoError(t, err)
			switch invalid {
			case "other-finalizer":
				after.Metadata.Finalizers = nil
			case "spec":
				after.Spec = json.RawMessage(`{"roleRef":{"kind":"Role","name":"reader"},"subjects":[{"kind":"User","name":"mallory"}]}`)
			case "namespace-owner":
				after.Metadata.OwnerReferences = json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"shop","uid":"` + uuid.NewString() + `","blockOwnerDeletion":false}]`)
			case "role-owner":
				after.Metadata.OwnerReferences = json.RawMessage(`[{"apiVersion":"rbac.authorization.gitstore.dev/v1beta1","kind":"Role","name":"reader","uid":"` + uuid.NewString() + `","blockOwnerDeletion":true}]`)
			}
			require.Error(t, protection.StatusChange(binding, after))
		})
	}
	after, err := rbac.CloneResource(binding)
	require.NoError(t, err)
	after.Metadata.Finalizers = append(after.Metadata.Finalizers, "rbac.authorization.gitstore.dev/lifecycle")
	after.Status = json.RawMessage(`{"observedGeneration":1}`)
	require.NoError(t, protection.StatusChange(binding, after))
}

func TestOrdinaryOwnerCollisionCleanupDoesNotPermitAdoptionOrProvenanceSpoofing(t *testing.T) {
	builtins, err := rbac.ReleaseBuiltins()
	require.NoError(t, err)
	_, _, _, binding := referenceFixture(t)
	binding.Metadata.Name = rbac.ManagedBindingName
	template := &rbac.ManagedBindingTemplate{Namespace: binding.Metadata.Namespace, NamespaceUID: binding.NamespaceUID, Subject: "alice"}
	protection := rbac.Protection{Builtins: builtins, Managed: template}
	require.NoError(t, protection.AuthorChange(binding, nil))
	require.Error(t, protection.AuthorChange(nil, binding), "new ordinary reserved-name bindings remain forbidden")
	managed, err := template.Resource()
	require.NoError(t, err)
	require.Error(t, protection.AuthorChange(binding, managed), "a controller must not adopt the ordinary UID")
	for _, scenario := range []string{"managed UID", "unknown UID", "copied annotation", "copied label", "old namespace"} {
		t.Run(scenario, func(t *testing.T) {
			before, err := rbac.CloneResource(binding)
			require.NoError(t, err)
			switch scenario {
			case "managed UID":
				before.Metadata.UID = template.UID()
			case "unknown UID":
				before.Metadata.UID = ""
			case "copied annotation":
				before.Metadata.Annotations = map[string]string{"rbac.authorization.gitstore.dev/managed-by": "rbac-controller"}
			case "copied label":
				before.Metadata.Labels = map[string]string{"rbac.authorization.gitstore.dev/namespace-uid": template.NamespaceUID}
			case "old namespace":
				before.NamespaceUID = uuid.NewString()
			}
			require.Error(t, protection.AuthorChange(before, nil))
		})
	}
}
