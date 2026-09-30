// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// resourceOwnerFromSub decodes an ADR-0010 §14 owner subject (as returned by
// datastore's EffectiveOwnerSub) into the GraphQL ResourceOwner shape.
func resourceOwnerFromSub(ownerSub string) *model.ResourceOwner {
	kind, name := datastore.DecodeOwnerSubject(ownerSub)
	modelKind := model.OwnerKindUser
	switch kind {
	case datastore.OwnerKindGroup:
		modelKind = model.OwnerKindGroup
	case datastore.OwnerKindServiceAccount:
		modelKind = model.OwnerKindServiceAccount
	}
	return &model.ResourceOwner{Kind: modelKind, Name: name}
}

// guardOwnerAnnotationUnchanged rejects an update that attempts to change the
// ADR-0010 §14 reserved owner annotation outside the dedicated transfer
// mutation. Reassigning ownership is its own action with its own
// authorization rule (ADR-0010 §14: "Reassigning ownership after creation is
// its own action, not folded into update") — a caller with only generic
// update permission must not be able to smuggle an ownership change through
// metadata.annotations. This guard only applies to update paths reached via
// a GraphQL mutation; a raw git push is authorized by ordinary repository
// write access, same as any other manifest field (see the ADR-0010 §14
// spec's scope notes).
func guardOwnerAnnotationUnchanged(existing, incoming map[string]string) error {
	if existing[datastore.OwnerAnnotationKey] != incoming[datastore.OwnerAnnotationKey] {
		return gqlerror.Errorf("owner cannot be changed via update; use the dedicated transfer mutation")
	}
	return nil
}
