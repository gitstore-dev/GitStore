// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeTransferTargetProvidesFactsNotPermission(t *testing.T) {
	for _, tc := range [][2]string{{"USER", "system:serviceaccount:ci"}, {"USER", "anon"}, {"SERVICE_ACCOUNT", "ns:ci"}, {"GROUP", "system:group:system:group:team"}} {
		_, err := normalizeTransferTarget(tc[0], tc[1])
		assert.Error(t, err, "%v must not change identity kinds through encoding", tc)
	}
	for _, tc := range []struct {
		kind, name, want string
		err              error
	}{
		{"USER", "bob", "bob", nil},
		{"GROUP", "team", "system:group:team", nil},
		{"GROUP", "system:group:team", "system:group:team", nil},
		{"SERVICE_ACCOUNT", "ci", "system:serviceaccount:ci", nil},
		{"SERVICE_ACCOUNT", "system:serviceaccount:ci", "system:serviceaccount:ci", nil},
		{"USER", "", "", errTransferMissingTarget},
		{"", "alice", "", errTransferInvalidKind},
		{"ROBOT", "alice", "", errTransferInvalidKind},
	} {
		got, err := normalizeTransferTarget(tc.kind, tc.name)
		assert.Equal(t, tc.want, got)
		assert.ErrorIs(t, err, tc.err)
	}
}
