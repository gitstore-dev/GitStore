// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
)

func TestEncodeDecodeOwnerSubject_RoundTrip(t *testing.T) {
	cases := []struct {
		kind datastore.OwnerKind
		name string
	}{
		{datastore.OwnerKindUser, "alice"},
		{datastore.OwnerKindGroup, "merchandiser"},
		{datastore.OwnerKindServiceAccount, "gitstore-controller-manager"},
	}
	for _, tc := range cases {
		encoded := datastore.EncodeOwnerSubject(tc.kind, tc.name)
		kind, name := datastore.DecodeOwnerSubject(encoded)
		assert.Equal(t, tc.kind, kind, "kind round-trip for %s", tc.name)
		assert.Equal(t, tc.name, name, "name round-trip for %s", tc.name)
	}
}

func TestEncodeOwnerSubject_UserHasNoPrefix(t *testing.T) {
	assert.Equal(t, "alice", datastore.EncodeOwnerSubject(datastore.OwnerKindUser, "alice"))
}

func TestEncodeOwnerSubject_GroupUsesReservedPrefix(t *testing.T) {
	assert.Equal(t, "system:group:merchandiser", datastore.EncodeOwnerSubject(datastore.OwnerKindGroup, "merchandiser"))
}

func TestEncodeOwnerSubject_ServiceAccountUsesReservedPrefix(t *testing.T) {
	assert.Equal(t, "system:serviceaccount:ci-bot", datastore.EncodeOwnerSubject(datastore.OwnerKindServiceAccount, "ci-bot"))
}

func TestDecodeOwnerSubject_BareNameIsUser(t *testing.T) {
	kind, name := datastore.DecodeOwnerSubject("alice")
	assert.Equal(t, datastore.OwnerKindUser, kind)
	assert.Equal(t, "alice", name)
}

func TestNamespace_EffectiveOwnerSub_FallsBackToCreationActor(t *testing.T) {
	ns := &datastore.Namespace{CreationActor: "alice"}
	assert.Equal(t, "alice", ns.EffectiveOwnerSub())
}

func TestNamespace_EffectiveOwnerSub_PrefersOwnerAnnotation(t *testing.T) {
	ns := &datastore.Namespace{
		CreationActor: "alice",
		Annotations:   map[string]string{datastore.OwnerAnnotationKey: "system:group:merchandiser"},
	}
	assert.Equal(t, "system:group:merchandiser", ns.EffectiveOwnerSub())
}

func TestNamespace_EffectiveOwnerSub_IgnoresEmptyAnnotationValue(t *testing.T) {
	ns := &datastore.Namespace{
		CreationActor: "alice",
		Annotations:   map[string]string{datastore.OwnerAnnotationKey: ""},
	}
	assert.Equal(t, "alice", ns.EffectiveOwnerSub())
}

func TestRepository_EffectiveOwnerSub_PrefersOwnerAnnotation(t *testing.T) {
	repo := &datastore.Repository{
		CreationActor: "alice",
		Annotations:   map[string]string{datastore.OwnerAnnotationKey: "bob"},
	}
	assert.Equal(t, "bob", repo.EffectiveOwnerSub())
}

func TestProduct_EffectiveOwnerSub_FallsBackToCreationActor(t *testing.T) {
	p := &datastore.Product{CreationActor: "alice"}
	assert.Equal(t, "alice", p.EffectiveOwnerSub())
}
