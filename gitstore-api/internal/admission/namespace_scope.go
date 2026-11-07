// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"fmt"
	"strings"
)

const (
	ReasonNamespaceRequired  = "NAMESPACE_REQUIRED"
	ReasonNamespaceForbidden = "NAMESPACE_FORBIDDEN"
)

// ValidateNamespaceScope validates author-controlled metadata before looking up
// namespace facts. A pointer preserves the distinction between null and a
// supplied empty string for cluster-scoped resources. Unknown kinds are left
// to the caller's envelope validation.
func ValidateNamespaceScope(kind string, namespace *string) *Error {
	var reason, message string
	switch kind {
	case "Namespace", "ServiceAccount":
		if namespace == nil {
			return nil
		}
		reason = ReasonNamespaceForbidden
		message = fmt.Sprintf("%s is cluster-scoped. Omit metadata.namespace or set it to null", kind)
	case "Product", "CategoryTaxonomy", "Collection", "ProductVariant", "Repository", "File":
		if namespace != nil && strings.TrimSpace(*namespace) != "" {
			return nil
		}
		reason = ReasonNamespaceRequired
		message = fmt.Sprintf("%s requires a nonblank metadata.namespace. Supply the owning namespace", kind)
	default:
		return nil
	}
	return &Error{Code: CodeBadUserInput, Diagnostics: []Diagnostic{{
		Reason: reason, Message: message, Field: "metadata.namespace", Level: LevelFailure,
	}}}
}
