// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"errors"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

var (
	errTransferNotOwner      = errors.New("permission denied: caller does not own this namespace")
	errTransferNotMember     = errors.New("permission denied: caller is not a member of the target owner")
	errTransferInvalidKind   = errors.New("targetOwnerRef.kind must be USER, GROUP, or SERVICE_ACCOUNT")
	errTransferMissingTarget = errors.New("targetOwnerRef.name is required")
)

// checkOwnerTransfer applies the ADR-0010 §14 two-condition transfer rule and
// returns the §12-encoded target owner subject: (1) the caller currently owns
// the resource, or is admin; (2) the caller is a member of the target owner,
// or is admin. principal.IsAdmin() stands in for the ADR's "unconditional
// cluster-tier grant," which has no distinct primitive yet. targetKind is the
// GraphQL OwnerKind enum value.
func checkOwnerTransfer(currentOwnerSub, targetKind, targetName string, principal *auth.Principal) (string, error) {
	if targetName == "" {
		return "", errTransferMissingTarget
	}
	var ownerKind datastore.OwnerKind
	switch targetKind {
	case "USER":
		ownerKind = datastore.OwnerKindUser
	case "GROUP":
		ownerKind = datastore.OwnerKindGroup
	case "SERVICE_ACCOUNT":
		ownerKind = datastore.OwnerKindServiceAccount
	default:
		return "", errTransferInvalidKind
	}
	targetOwnerSub := datastore.EncodeOwnerSubject(ownerKind, targetName)
	isAdmin := principal != nil && principal.IsAdmin()
	if !isAdmin && !ownerMatchesPrincipal(currentOwnerSub, principal) {
		return "", errTransferNotOwner
	}
	if !isAdmin && !ownerMatchesPrincipal(targetOwnerSub, principal) {
		return "", errTransferNotMember
	}
	return targetOwnerSub, nil
}

// ownerMatchesPrincipal reports whether principal currently satisfies
// ownerSub — an ADR-0010 §12-encoded subject string (bare name = User or
// ServiceAccount subject, "system:group:<name>" = Group).
//
// TODO(ADR-0010 §14, follow-up vocabulary spec): this pre-computes .own/.any
// in Go; AuthZProvider.Authorize does not yet consume ResourceContext.OwnerSub
// (rbaclocal discards it — internal/auth/provider/rbaclocal/provider.go).
// This is a known, temporary deviation from the ADR's "callers stop
// pre-computing ownership" goal — see docs/ADRs/0010-authorization-model.md
// §14 addendum.
func ownerMatchesPrincipal(ownerSub string, principal *auth.Principal) bool {
	if ownerSub == "" || principal == nil {
		return false
	}
	if ownerSub == principal.Subject {
		return true
	}
	for _, g := range principal.Groups {
		if g == ownerSub {
			return true
		}
	}
	return false
}
