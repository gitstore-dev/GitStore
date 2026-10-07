// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func NewNamespaceStructuralError(reason namespaceadmission.Reason, message string) error {
	return newNamespaceError(message, namespaceadmission.CodeStructuralValidationFailed, namespaceadmission.PhaseStructural, reason)
}

func NewNamespaceImmutableError(reason namespaceadmission.Reason, message string) error {
	return newNamespaceError(message, namespaceadmission.CodeImmutableField, namespaceadmission.PhaseStructural, reason)
}

func NewNamespacePolicyError(reason namespaceadmission.Reason, message string) error {
	return newNamespaceError(message, namespaceadmission.CodePolicyRejected, namespaceadmission.PhasePolicy, reason)
}

func NewNamespaceConflictError(reason namespaceadmission.Reason, message string) error {
	return newNamespaceError(message, namespaceadmission.CodeConflict, namespaceadmission.PhasePolicy, reason)
}

func NewNamespaceNotFoundError(message string) error {
	return newNamespaceError(message, "NOT_FOUND", namespaceadmission.PhasePolicy, namespaceadmission.ReasonNamespaceNotFound)
}

func NewNamespaceDeletionBlockedError(reasons []namespaceadmission.Reason, message string) error {
	ordered := namespaceadmission.OrderDeletionBlockers(reasons)
	values := make([]string, len(ordered))
	for i, reason := range ordered {
		values[i] = string(reason)
	}
	return &gqlerror.Error{
		Message: message,
		Extensions: map[string]any{
			"code":    namespaceadmission.CodeDeletionBlocked,
			"reasons": values,
		},
	}
}

func NewNamespaceRepositoryFenceDisabledError(operation string) error {
	return &gqlerror.Error{
		Message: "namespace deletion and repository create/transfer are disabled during staged rollout",
		Extensions: map[string]any{
			"code":      "NAMESPACE_REPOSITORY_FENCE_DISABLED",
			"reason":    "ROLLOUT_GATE_DISABLED",
			"operation": operation,
		},
	}
}

func newNamespaceError(message, code string, phase namespaceadmission.Phase, reason namespaceadmission.Reason) error {
	return &gqlerror.Error{
		Message: message,
		Extensions: map[string]any{
			"code":   code,
			"phase":  string(phase),
			"reason": string(reason),
		},
	}
}
