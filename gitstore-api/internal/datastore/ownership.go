// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import "strings"

// OwnerAnnotationKey is the reserved metadata.annotations key an author sets
// to assign or transfer a resource's owner (ADR-0010 §14). It lives under the
// same reserved domain ADR-0010 §10 reserves for the subject↔role↔permission
// plane, so it is never a candidate for a user-chosen annotation key.
const OwnerAnnotationKey = "rbac.authorization.gitstore.dev/owner"

// OwnerKind is the principal type that can own a resource (ADR-0010 §14/§7).
type OwnerKind string

const (
	OwnerKindUser           OwnerKind = "User"
	OwnerKindGroup          OwnerKind = "Group"
	OwnerKindServiceAccount OwnerKind = "ServiceAccount"
)

// EncodeOwnerSubject renders (kind, name) as the ADR-0010 §12 reserved-prefix
// subject string: a bare name for User, "system:group:<name>" for Group,
// "system:serviceaccount:<name>" for ServiceAccount. This is the same string
// shape principal.Subject/principal.Groups already use, so the encoded value
// can be compared against them directly without decoding.
func EncodeOwnerSubject(kind OwnerKind, name string) string {
	switch kind {
	case OwnerKindGroup:
		return "system:group:" + name
	case OwnerKindServiceAccount:
		return "system:serviceaccount:" + name
	default:
		return name
	}
}

// DecodeOwnerSubject reverses EncodeOwnerSubject, for the GraphQL read side.
func DecodeOwnerSubject(raw string) (OwnerKind, string) {
	switch {
	case strings.HasPrefix(raw, "system:group:"):
		return OwnerKindGroup, strings.TrimPrefix(raw, "system:group:")
	case strings.HasPrefix(raw, "system:serviceaccount:"):
		return OwnerKindServiceAccount, strings.TrimPrefix(raw, "system:serviceaccount:")
	default:
		return OwnerKindUser, raw
	}
}
