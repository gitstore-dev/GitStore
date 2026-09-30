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
