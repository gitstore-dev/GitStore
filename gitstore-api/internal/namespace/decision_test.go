// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace_test

import (
	"testing"

	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/assert"
)

func TestNamespaceDecisionVocabulary(t *testing.T) {
	assert.Equal(t, namespaceadmission.Phase("STRUCTURAL"), namespaceadmission.PhaseStructural)
	assert.Equal(t, namespaceadmission.Phase("POLICY"), namespaceadmission.PhasePolicy)

	assert.Equal(t, namespaceadmission.Reason("INVALID_ENVELOPE"), namespaceadmission.ReasonInvalidEnvelope)
	assert.Equal(t, namespaceadmission.Reason("INVALID_IDENTIFIER"), namespaceadmission.ReasonInvalidIdentifier)
	assert.Equal(t, namespaceadmission.Reason("RESERVED_IDENTIFIER"), namespaceadmission.ReasonReservedIdentifier)
	assert.Equal(t, namespaceadmission.Reason("INVALID_TIER"), namespaceadmission.ReasonInvalidTier)
	assert.Equal(t, namespaceadmission.Reason("INVALID_AUTHORING_TARGET"), namespaceadmission.ReasonInvalidAuthoringTarget)
	assert.Equal(t, namespaceadmission.Reason("DUPLICATE_IDENTITY"), namespaceadmission.ReasonDuplicateIdentity)
	assert.Equal(t, namespaceadmission.Reason("IMMUTABLE_NAME"), namespaceadmission.ReasonImmutableName)
	assert.Equal(t, namespaceadmission.Reason("BOOTSTRAP_NAMESPACE"), namespaceadmission.ReasonBootstrapNamespace)
	assert.Equal(t, namespaceadmission.Reason("TIER_DEMOTION"), namespaceadmission.ReasonTierDemotion)
	assert.Equal(t, namespaceadmission.Reason("NAMESPACE_TERMINATING"), namespaceadmission.ReasonNamespaceTerminating)
	assert.Equal(t, namespaceadmission.Reason("NAMESPACE_ALREADY_EXISTS"), namespaceadmission.ReasonNamespaceAlreadyExists)
	assert.Equal(t, namespaceadmission.Reason("RESOURCE_VERSION_CONFLICT"), namespaceadmission.ReasonResourceVersionConflict)

	assert.Equal(t, "NAMESPACE_STRUCTURAL_VALIDATION_FAILED", namespaceadmission.CodeStructuralValidationFailed)
	assert.Equal(t, "NAMESPACE_IMMUTABLE_FIELD", namespaceadmission.CodeImmutableField)
	assert.Equal(t, "NAMESPACE_POLICY_REJECTED", namespaceadmission.CodePolicyRejected)
	assert.Equal(t, "NAMESPACE_DELETION_BLOCKED", namespaceadmission.CodeDeletionBlocked)
	assert.Equal(t, "NAMESPACE_CONFLICT", namespaceadmission.CodeConflict)
}

func TestNamespaceDeletionVocabularyAndOrdering(t *testing.T) {
	assert.Equal(t, namespaceadmission.DeletionOutcome("TERMINATION_STARTED"), namespaceadmission.DeletionOutcomeTerminationStarted)
	assert.Equal(t, namespaceadmission.DeletionOutcome("ALREADY_TERMINATING"), namespaceadmission.DeletionOutcomeAlreadyTerminating)

	got := namespaceadmission.OrderDeletionBlockers([]namespaceadmission.Reason{
		namespaceadmission.ReasonNamespaceNotEmpty,
		namespaceadmission.ReasonBootstrapNamespace,
		namespaceadmission.ReasonNamespaceNotEmpty,
	})
	assert.Equal(t, []namespaceadmission.Reason{
		namespaceadmission.ReasonBootstrapNamespace,
		namespaceadmission.ReasonNamespaceNotEmpty,
	}, got)
}
