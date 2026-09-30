// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/assert"
)

func TestOwnerMatchesPrincipal_UserSubjectMatch(t *testing.T) {
	principal := &auth.Principal{Subject: "alice"}
	assert.True(t, ownerMatchesPrincipal("alice", principal))
}

func TestOwnerMatchesPrincipal_UserSubjectNoMatch(t *testing.T) {
	principal := &auth.Principal{Subject: "bob"}
	assert.False(t, ownerMatchesPrincipal("alice", principal))
}

func TestOwnerMatchesPrincipal_GroupMembershipMatch(t *testing.T) {
	principal := &auth.Principal{Subject: "bob", Groups: []string{"system:group:merchandiser"}}
	assert.True(t, ownerMatchesPrincipal("system:group:merchandiser", principal))
}

func TestOwnerMatchesPrincipal_GroupMembershipNoMatch(t *testing.T) {
	principal := &auth.Principal{Subject: "bob", Groups: []string{"system:group:other"}}
	assert.False(t, ownerMatchesPrincipal("system:group:merchandiser", principal))
}

func TestOwnerMatchesPrincipal_EmptyOwnerNeverMatches(t *testing.T) {
	principal := &auth.Principal{Subject: "alice"}
	assert.False(t, ownerMatchesPrincipal("", principal))
}

func TestOwnerMatchesPrincipal_NilPrincipalNeverMatches(t *testing.T) {
	assert.False(t, ownerMatchesPrincipal("alice", nil))
}

func TestCheckOwnerTransfer(t *testing.T) {
	admin := &auth.Principal{Subject: "root", Roles: []string{"admin"}}
	alice := &auth.Principal{Subject: "alice", Groups: []string{"system:group:merchandiser"}}
	tests := []struct {
		name      string
		owner     string
		kind      string
		target    string
		principal *auth.Principal
		wantSub   string
		wantErr   error
	}{
		{"owner to self user", "alice", "USER", "alice", alice, "alice", nil},
		{"owner to member group", "alice", "GROUP", "merchandiser", alice, "system:group:merchandiser", nil},
		{"group owner to member group", "system:group:merchandiser", "GROUP", "merchandiser", alice, "system:group:merchandiser", nil},
		{"owner to non-member group", "alice", "GROUP", "other", alice, "", errTransferNotMember},
		{"owner to other user", "alice", "USER", "bob", alice, "", errTransferNotMember},
		{"owner to service account", "alice", "SERVICE_ACCOUNT", "ci", alice, "", errTransferNotMember},
		{"non-owner", "bob", "USER", "alice", alice, "", errTransferNotOwner},
		{"admin to any group", "bob", "GROUP", "other", admin, "system:group:other", nil},
		{"admin to service account", "bob", "SERVICE_ACCOUNT", "ci", admin, "system:serviceaccount:ci", nil},
		{"missing kind", "alice", "", "alice", admin, "", errTransferInvalidKind},
		{"unknown kind", "alice", "ROBOT", "alice", admin, "", errTransferInvalidKind},
		{"missing name", "alice", "USER", "", admin, "", errTransferMissingTarget},
		{"nil principal", "alice", "USER", "alice", nil, "", errTransferNotOwner},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub, err := checkOwnerTransfer(tt.owner, tt.kind, tt.target, tt.principal)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Equal(t, tt.wantSub, sub)
		})
	}
}
