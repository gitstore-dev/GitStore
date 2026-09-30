// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import "github.com/gitstore-dev/gitstore/api/internal/auth"

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
