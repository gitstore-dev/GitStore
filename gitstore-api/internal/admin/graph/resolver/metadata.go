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
	return admission.ValidateNamespaceScope(kind, namespace)
}

func namespaceValue(namespace *string) string {
	if namespace == nil {
		return ""
	}
	return *namespace
}
