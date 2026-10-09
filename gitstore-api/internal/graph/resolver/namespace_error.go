// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
)

// NewNamespaceError builds a Namespace mutation error on the shared envelope.
// The code follows the reason; phase and commit only apply when the code is
// ADMISSION_REJECTED.
func NewNamespaceError(phase admission.Phase, commitSHA string, reason namespaceadmission.Reason, message string) error {
	code := namespaceadmission.CodeForReason(reason)
	err := &admission.Error{
		Code:        code,
		Diagnostics: []admission.Diagnostic{{Reason: string(reason), Message: message, Level: admission.LevelFailure}},
		Message:     message,
	}
	if code == admission.CodeAdmissionRejected {
		err.Phase, err.CommitSHA = phase, commitSHA
	}
	return err.ToGQLError()
}

// NewNamespaceStructuralError reports a manifest check that failed before commit.
func NewNamespaceStructuralError(reason namespaceadmission.Reason, message string) error {
	return NewNamespaceError(admission.PhasePreReceive, "", reason, message)
}

// NewNamespaceImmutableError reports an immutable-field change rejected before commit.
func NewNamespaceImmutableError(reason namespaceadmission.Reason, message string) error {
	return NewNamespaceError(admission.PhasePreReceive, "", reason, message)
}

// NewNamespacePolicyError reports a policy or lifecycle rejection. Preflight
// rejections are PRE_RECEIVE; rejections after the commit are POST_RECEIVE.
func NewNamespacePolicyError(phase admission.Phase, commitSHA string, reason namespaceadmission.Reason, message string) error {
	return NewNamespaceError(phase, commitSHA, reason, message)
}

// NewNamespaceConflictError reports a concurrent change (RESOURCE_VERSION_CONFLICT or SUPERSEDED).
func NewNamespaceConflictError(reason namespaceadmission.Reason, message string) error {
	return NewNamespaceError("", "", reason, message)
}

func NewNamespaceNotFoundError(message string) error {
	return NewNamespaceError("", "", namespaceadmission.ReasonNamespaceNotFound, message)
}

// NewNamespaceDeletionBlockedError reports one diagnostic per blocker, in the
// stable blocker order.
func NewNamespaceDeletionBlockedError(reasons []namespaceadmission.Reason, message string) error {
	ordered := namespaceadmission.OrderDeletionBlockers(reasons)
	diagnostics := make([]admission.Diagnostic, 0, len(ordered))
	for _, reason := range ordered {
		diagnostics = append(diagnostics, admission.Diagnostic{
			Reason: string(reason), Message: namespaceDeletionBlockerMessage(reason), Level: admission.LevelFailure,
		})
	}
	return (&admission.Error{Code: admission.CodeFailedPrecondition, Diagnostics: diagnostics, Message: message}).ToGQLError()
}

func namespaceDeletionBlockerMessage(reason namespaceadmission.Reason) string {
	switch reason {
	case namespaceadmission.ReasonBootstrapNamespace:
		return "bootstrap namespaces are system-managed and cannot be deleted"
	case namespaceadmission.ReasonNamespaceNotEmpty:
		return "namespace still contains repositories"
	default:
		return string(reason)
	}
}
