// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
)

func validateMetadataScope(kind string, metadata *model.ObjectMetaInput) *admission.Error {
	var namespace *string
	if metadata != nil {
		namespace = metadata.Namespace
	}
	if err := admission.ValidateNamespaceScope(kind, namespace); err != nil {
		return err
	}
	if metadata != nil && metadata.ResourceVersion != nil {
		return admission.NewError(admission.CodeBadUserInput, "UNSUPPORTED_RESOURCE_VERSION", "metadata.resourceVersion is supported only by RBAC updates")
	}
	return nil
}

func namespaceValue(namespace *string) string {
	if namespace == nil {
		return ""
	}
	return *namespace
}
