// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"sort"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
)

type Reason string

const (
	ReasonInvalidEnvelope          Reason = "INVALID_ENVELOPE"
	ReasonInvalidIdentifier        Reason = "INVALID_IDENTIFIER"
	ReasonReservedIdentifier       Reason = "RESERVED_IDENTIFIER"
	ReasonInvalidTier              Reason = "INVALID_TIER"
	ReasonInvalidAuthoringTarget   Reason = "INVALID_AUTHORING_TARGET"
	ReasonDuplicateIdentity        Reason = "DUPLICATE_IDENTITY"
	ReasonImmutableName            Reason = "IMMUTABLE_NAME"
	ReasonBootstrapNamespace       Reason = "BOOTSTRAP_NAMESPACE"
	ReasonPushPolicyExceedsCeiling Reason = "PUSH_POLICY_EXCEEDS_CEILING"
	ReasonTierDemotion             Reason = "TIER_DEMOTION"
	ReasonNamespaceTerminating     Reason = "NAMESPACE_TERMINATING"
	ReasonNamespaceAlreadyExists   Reason = "NAMESPACE_ALREADY_EXISTS"
	ReasonNamespaceNotFound        Reason = "NAMESPACE_NOT_FOUND"
	ReasonNamespaceNotEmpty        Reason = "NAMESPACE_NOT_EMPTY"
	ReasonResourceVersionConflict  Reason = "RESOURCE_VERSION_CONFLICT"
	ReasonSuperseded               Reason = "SUPERSEDED"
)

// CodeForReason selects the shared mutation error code for a Namespace reason.
func CodeForReason(reason Reason) admission.Code {
	switch reason {
	case ReasonBootstrapNamespace, ReasonNamespaceTerminating, ReasonNamespaceNotEmpty:
		return admission.CodeFailedPrecondition
	case ReasonNamespaceAlreadyExists:
		return admission.CodeAlreadyExists
	case ReasonNamespaceNotFound:
		return admission.CodeNotFound
	case ReasonResourceVersionConflict, ReasonSuperseded:
		return admission.CodeConflict
	default:
		return admission.CodeAdmissionRejected
	}
}

// ValidationStage labels Namespace validation latency.
type ValidationStage string

const (
	StageStructural ValidationStage = "STRUCTURAL"
	StagePolicy     ValidationStage = "POLICY"
)

// Decision is a Namespace policy rejection.
type Decision struct {
	Reason   Reason
	Field    string
	Message  string
	FilePath string
}

func (d Decision) Constraint() string {
	if d.Reason == ReasonImmutableName {
		return "immutable"
	}
	return "policy/" + strings.ToLower(strings.ReplaceAll(string(d.Reason), "_", "-"))
}

type DeletionOutcome string

const (
	DeletionOutcomeTerminationStarted DeletionOutcome = "TERMINATION_STARTED"
	DeletionOutcomeAlreadyTerminating DeletionOutcome = "ALREADY_TERMINATING"
)

func OrderDeletionBlockers(blockers []Reason) []Reason {
	seen := make(map[Reason]struct{}, len(blockers))
	for _, blocker := range blockers {
		if blocker == ReasonBootstrapNamespace || blocker == ReasonNamespaceNotEmpty {
			seen[blocker] = struct{}{}
		}
	}
	ordered := make([]Reason, 0, len(seen))
	for blocker := range seen {
		ordered = append(ordered, blocker)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return deletionBlockerRank(ordered[i]) < deletionBlockerRank(ordered[j])
	})
	return ordered
}

func deletionBlockerRank(reason Reason) int {
	switch reason {
	case ReasonBootstrapNamespace:
		return 0
	case ReasonNamespaceNotEmpty:
		return 1
	default:
		return 2
	}
}
