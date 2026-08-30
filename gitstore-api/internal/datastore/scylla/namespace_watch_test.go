// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveAmbiguousNamespaceLeaseAcquisition(t *testing.T) {
	expires := time.Now().UTC().Add(time.Minute)
	primary := errors.New("write timeout")
	tests := []struct {
		name       string
		state      namespaceWatchClockRow
		readErr    error
		want       bool
		wantErr    bool
		wantExpiry time.Time
	}{
		{name: "matching holder and token acquired", state: namespaceWatchClockRow{Holder: "replica-a", FencingToken: 7, ExpiresAt: expires}, want: true, wantExpiry: expires},
		{name: "different holder did not acquire", state: namespaceWatchClockRow{Holder: "replica-b", FencingToken: 7, ExpiresAt: expires}},
		{name: "different token did not acquire", state: namespaceWatchClockRow{Holder: "replica-a", FencingToken: 8, ExpiresAt: expires}},
		{name: "unreadable outcome preserves error", readErr: errors.New("read timeout"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lease, acquired, err := runNamespaceLeaseAcquisitionResolution(context.Background(), "replica-a", 7, primary, func(context.Context) (namespaceWatchClockRow, error) {
				return tt.state, tt.readErr
			})
			assert.Equal(t, tt.want, acquired)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, primary)
				return
			}
			require.NoError(t, err)
			if tt.want {
				assert.Equal(t, "replica-a", lease.Holder)
				assert.Equal(t, uint64(7), lease.FencingToken)
				assert.Equal(t, tt.wantExpiry, lease.ExpiresAt)
			}
		})
	}
}
