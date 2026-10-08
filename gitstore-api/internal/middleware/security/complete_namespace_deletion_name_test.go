// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestResolveCompleteNamespaceDeletionName(t *testing.T) {
	acme, other, empty := "acme", "other", ""
	tests := []struct {
		name       string
		nameArg    *string
		identifier *string
		want       string
		wantErr    bool
	}{
		{name: "name only", nameArg: &acme, want: "acme"},
		{name: "deprecated identifier only", identifier: &acme, want: "acme"},
		{name: "both equal", nameArg: &acme, identifier: &acme, want: "acme"},
		{name: "both differ", nameArg: &acme, identifier: &other, wantErr: true},
		{name: "neither", wantErr: true},
		{name: "empty", nameArg: &empty, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveCompleteNamespaceDeletionName(tc.nameArg, tc.identifier)
			if tc.wantErr {
				require.Error(t, err)
				gqlErr, ok := err.(*gqlerror.Error)
				require.True(t, ok)
				require.Equal(t, "BAD_USER_INPUT", gqlErr.Extensions["code"])
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
