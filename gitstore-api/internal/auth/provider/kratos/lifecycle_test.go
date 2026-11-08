// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestTrackedStaffSetupAndVerificationCannotBeBypassedByLifecycleWrites(t *testing.T) {
	for _, test := range []struct {
		name            string
		stored, want    auth.ActorLifecycleState
		setup, verified bool
	}{
		{"activation before password setup", auth.ActorActive, auth.ActorInvited, false, true},
		{"setup awaits verification", auth.ActorInvited, auth.ActorPendingVerification, true, false},
		{"completed invitation", auth.ActorInvited, auth.ActorActive, true, true},
		{"completed verification", auth.ActorPendingVerification, auth.ActorActive, true, true},
		{"deactivation retains unfinished setup", auth.ActorInactive, auth.ActorInactive, false, true},
		{"suspension retains unfinished setup", auth.ActorSuspended, auth.ActorSuspended, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := identityFixture()
			metadata := fixture["metadata_admin"].(map[string]any)
			metadata["lifecycle_state"], metadata["setup_complete"] = test.stored, test.setup
			email := fixture["traits"].(map[string]any)["email"]
			fixture["verifiable_addresses"] = []map[string]any{{"value": email, "verified": test.verified}}
			body, err := json.Marshal(fixture)
			require.NoError(t, err)
			var record identity
			require.NoError(t, json.Unmarshal(body, &record))
			provider, err := New(directoryConfig("http://directory.invalid"))
			require.NoError(t, err)
			profile, err := provider.profile(record)
			require.NoError(t, err)
			require.Equal(t, test.want, profile.Lifecycle)
			require.Equal(t, test.want == auth.ActorActive, profile.Active)
		})
	}
}
