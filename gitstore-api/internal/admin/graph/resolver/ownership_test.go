// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
)

func TestGuardOwnerAnnotationUnchanged_AllowsWhenOwnerUnchanged(t *testing.T) {
	existing := map[string]string{datastore.OwnerAnnotationKey: "alice", "other": "x"}
	incoming := map[string]string{datastore.OwnerAnnotationKey: "alice", "other": "y"}
	assert.NoError(t, guardOwnerAnnotationUnchanged(existing, incoming))
}

func TestGuardOwnerAnnotationUnchanged_AllowsWhenBothAbsent(t *testing.T) {
	assert.NoError(t, guardOwnerAnnotationUnchanged(nil, map[string]string{"other": "y"}))
}

func TestGuardOwnerAnnotationUnchanged_RejectsSmuggledChange(t *testing.T) {
	existing := map[string]string{datastore.OwnerAnnotationKey: "alice"}
	incoming := map[string]string{datastore.OwnerAnnotationKey: "system:group:attacker-group"}
	assert.Error(t, guardOwnerAnnotationUnchanged(existing, incoming))
}

func TestGuardOwnerAnnotationUnchanged_RejectsSmuggledInitialAssignment(t *testing.T) {
	// No prior owner, but the update tries to introduce one via a plain
	// update path rather than the dedicated transfer mutation.
	incoming := map[string]string{datastore.OwnerAnnotationKey: "system:group:attacker-group"}
	assert.Error(t, guardOwnerAnnotationUnchanged(nil, incoming))
}
