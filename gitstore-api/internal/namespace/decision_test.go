// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/assert"
)

func TestNamespaceDecisionVocabulary(t *testing.T) {
	assert.Equal(t, namespaceadmission.ValidationStage("STRUCTURAL"), namespaceadmission.StageStructural)
	assert.Equal(t, namespaceadmission.ValidationStage("POLICY"), namespaceadmission.StagePolicy)

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

	assert.Equal(t, namespaceadmission.Reason("SUPERSEDED"), namespaceadmission.ReasonSuperseded)

	for reason, code := range map[namespaceadmission.Reason]admission.Code{
		namespaceadmission.ReasonInvalidEnvelope:         admission.CodeAdmissionRejected,
		namespaceadmission.ReasonImmutableName:           admission.CodeAdmissionRejected,
		namespaceadmission.ReasonTierDemotion:            admission.CodeAdmissionRejected,
		namespaceadmission.ReasonBootstrapNamespace:      admission.CodeFailedPrecondition,
		namespaceadmission.ReasonNamespaceTerminating:    admission.CodeFailedPrecondition,
		namespaceadmission.ReasonNamespaceNotEmpty:       admission.CodeFailedPrecondition,
		namespaceadmission.ReasonNamespaceAlreadyExists:  admission.CodeAlreadyExists,
		namespaceadmission.ReasonNamespaceNotFound:       admission.CodeNotFound,
		namespaceadmission.ReasonResourceVersionConflict: admission.CodeConflict,
		namespaceadmission.ReasonSuperseded:              admission.CodeConflict,
	} {
		assert.Equal(t, code, namespaceadmission.CodeForReason(reason), string(reason))
	}
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
