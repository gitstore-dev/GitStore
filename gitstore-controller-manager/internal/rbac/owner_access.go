// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strings"

	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/google/uuid"
)

const (
	ManagedBindingName = "namespace-owner-access"
	provenancePrefix   = "rbac.authorization.gitstore.dev/"
)

func ownerBindingUID(namespaceUID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("gitstore/rbac/namespace-owner-access/"+namespaceUID)).String()
}

func ownerBinding(namespace namespacecontroller.Namespace) Resource {
	return Resource{Kind: "RoleBinding", Metadata: Metadata{Name: ManagedBindingName, Namespace: namespace.Name,
		Annotations: map[string]string{
			provenancePrefix + "managed-by":    "rbac-controller",
			provenancePrefix + "namespace-uid": namespace.UID,
			provenancePrefix + "initiator":     namespace.OwnerSubject,
		}}, Spec: Spec{RoleRef: &RoleRef{Kind: "ClusterRole", Name: "namespace-full-control"},
		Subjects: []Subject{{Kind: "User", Name: namespace.OwnerSubject}}}}
}

func verifyOwnerBinding(namespace namespacecontroller.Namespace, binding Resource) error {
	expected := ownerBinding(namespace)
	if binding.ID == "" || binding.Kind != expected.Kind || binding.Metadata.Name != expected.Metadata.Name ||
		binding.Metadata.Namespace != namespace.Name || binding.Metadata.UID != ownerBindingUID(namespace.UID) ||
		binding.Metadata.DeletionTimestamp != nil || binding.Metadata.Revision == "" ||
		!reflect.DeepEqual(binding.Spec, expected.Spec) || !reflect.DeepEqual(binding.Metadata.Annotations, expected.Metadata.Annotations) {
		return errors.New("rbac: namespace owner binding collides with an ordinary, terminating or different-incarnation resource")
	}
	return nil
}

func (c *GraphQLClient) EnsureOwnerAccess(ctx context.Context, namespace namespacecontroller.Namespace) (string, error) {
	// Namespace metadata uses a typed Relay ID; RBAC incarnation keys use UUIDs.
	if decoded, err := base64.StdEncoding.DecodeString(namespace.UID); err == nil {
		namespace.UID = strings.TrimPrefix(string(decoded), "gid://GitStore/Namespace/")
	}
	uid, err := uuid.Parse(namespace.UID)
	if err != nil || uid.String() != namespace.UID || namespace.Name == "" || namespace.Tier != "USER" ||
		namespace.OwnerKind != "USER" || namespace.OwnerSubject == "" || strings.HasPrefix(namespace.OwnerSubject, "system:") ||
		namespace.DeletionTimestamp != nil || slices.Contains(namespace.Finalizers, namespacecontroller.ForegroundDeletionFinalizer) {
		return "", errors.New("rbac: owner access requires a live USER namespace and human owner")
	}
	for _, condition := range namespace.Status.Conditions {
		if condition != nil && condition.Type == "DeletionPending" && strings.EqualFold(condition.Status, "true") {
			return "", errors.New("rbac: namespace deletion is pending")
		}
	}
	key := types.WorkItemKey{Kind: "RoleBinding", Namespace: namespace.Name, Name: ManagedBindingName}
	current, found, err := c.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if found {
		if err := verifyOwnerBinding(namespace, current); err != nil {
			return "", err
		}
		return current.Metadata.Revision, nil
	}
	desired := ownerBinding(namespace)
	spec := Spec{RoleRef: &RoleRef{Kind: "CLUSTER_ROLE", Name: desired.Spec.RoleRef.Name},
		Subjects: []Subject{{Kind: "USER", Name: namespace.OwnerSubject}}}
	input := map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": desired.Kind,
		"metadata": map[string]any{"name": desired.Metadata.Name, "namespace": namespace.Name, "annotations": desired.Metadata.Annotations},
		"spec":     spec,
	}
	var response struct {
		Result struct{ RoleBinding *Resource }
	}
	if err := c.client.Mutate(ctx, ownerBindingMutation, map[string]any{"input": input}, &response); err != nil {
		// The next durable attempt reads first, including after an unknown
		// commit outcome or another replica winning the create.
		return "", clientError(err)
	}
	if response.Result.RoleBinding == nil {
		return "", errors.New("rbac: owner binding admission returned no resource")
	}
	current = *response.Result.RoleBinding
	if err := verifyOwnerBinding(namespace, current); err != nil {
		return "", err
	}
	return current.Metadata.Revision, nil
}
