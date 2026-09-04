// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
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

func TestNamespaceWatchBatchCountBoundsStatementsAndBytes(t *testing.T) {
	small := make([]datastore.NamespaceWatchEvent, namespaceWatchBatchMaxStatements+10)
	for index := range small {
		small[index] = datastore.NamespaceWatchEvent{Name: "namespace", Payload: []byte(`{"kind":"Namespace"}`)}
	}
	require.Equal(t, namespaceWatchBatchMaxStatements, namespaceWatchBatchCount(small, 1, 4096))

	large := []datastore.NamespaceWatchEvent{
		{Name: "one", Payload: make([]byte, namespaceWatchBatchMaxBytes/2)},
		{Name: "two", Payload: make([]byte, namespaceWatchBatchMaxBytes/2)},
	}
	require.Equal(t, 1, namespaceWatchBatchCount(large, 1, 4096))

	oversized := []datastore.NamespaceWatchEvent{{Payload: make([]byte, namespaceWatchBatchMaxBytes+1)}}
	require.Equal(t, 1, namespaceWatchBatchCount(oversized, 1, 4096))
}

func TestNamespaceWatchBatchCountStopsAtBucketBoundary(t *testing.T) {
	events := make([]datastore.NamespaceWatchEvent, 3)
	require.Equal(t, 1, namespaceWatchBatchCount(events, 3, 3))
}

func TestResolveAmbiguousNamespaceLeaseRenewal(t *testing.T) {
	previousExpiry := time.Now().UTC().Add(30 * time.Second)
	expires := previousExpiry.Add(30 * time.Second)
	lease := datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 7, ExpiresAt: previousExpiry}
	primary := errors.New("write timeout")
	tests := []struct {
		name    string
		state   namespaceWatchClockRow
		readErr error
		want    bool
		wantErr bool
	}{
		{name: "matching renewal is confirmed", state: namespaceWatchClockRow{Holder: "replica-a", FencingToken: 7, ExpiresAt: expires}, want: true},
		{name: "unchanged expiration was not renewed", state: namespaceWatchClockRow{Holder: "replica-a", FencingToken: 7, ExpiresAt: previousExpiry}},
		{name: "different holder did not renew", state: namespaceWatchClockRow{Holder: "replica-b", FencingToken: 7, ExpiresAt: expires}},
		{name: "different token did not renew", state: namespaceWatchClockRow{Holder: "replica-a", FencingToken: 8, ExpiresAt: expires}},
		{name: "unreadable outcome preserves error", readErr: errors.New("read timeout"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renewed, ok, err := runNamespaceLeaseRenewalResolution(context.Background(), lease, expires, primary, func(context.Context) (namespaceWatchClockRow, error) {
				return tt.state, tt.readErr
			})
			assert.Equal(t, tt.want, ok)
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, primary)
				return
			}
			require.NoError(t, err)
			if tt.want {
				assert.Equal(t, lease.Holder, renewed.Holder)
				assert.Equal(t, lease.FencingToken, renewed.FencingToken)
				assert.Equal(t, expires, renewed.ExpiresAt)
			}
		})
	}
}

func TestResolveAmbiguousNamespaceWatchPublication(t *testing.T) {
	epoch := gocql.TimeUUID()
	lease := datastore.NamespaceWatchLease{Holder: "replica-a", FencingToken: 7}
	clock := namespaceWatchClockRow{Epoch: epoch, HighWater: 10, Holder: lease.Holder, FencingToken: int64(lease.FencingToken)}
	candidates := []datastore.NamespaceWatchEvent{
		{Epoch: epoch.String(), Sequence: 11, Type: datastore.NamespaceWatchAdded, Name: "one", DeduplicationKey: "key-one", FencingToken: lease.FencingToken},
		{Epoch: epoch.String(), Sequence: 12, Type: datastore.NamespaceWatchModified, Name: "two", DeduplicationKey: "key-two", FencingToken: lease.FencingToken},
	}
	readMatching := func(_ context.Context, candidate datastore.NamespaceWatchEvent) (datastore.NamespaceWatchEvent, error) {
		return candidate, nil
	}

	resolved, err := runNamespaceWatchPublicationResolution(context.Background(), clock, lease, candidates, errors.New("write timeout"),
		func(context.Context) (namespaceWatchClockRow, error) {
			published := clock
			published.HighWater = 12
			return published, nil
		}, readMatching)
	require.NoError(t, err)
	require.True(t, resolved, "an ambiguously acknowledged published range must not be appended again")

	resolved, err = runNamespaceWatchPublicationResolution(context.Background(), clock, lease, candidates, nil,
		func(context.Context) (namespaceWatchClockRow, error) { return clock, nil }, readMatching)
	require.NoError(t, err)
	require.False(t, resolved, "an unpublished range may use the per-event recovery path")

	stale := clock
	stale.Holder = "replica-b"
	_, err = runNamespaceWatchPublicationResolution(context.Background(), clock, lease, candidates, nil,
		func(context.Context) (namespaceWatchClockRow, error) { return stale, nil }, readMatching)
	require.ErrorIs(t, err, datastore.ErrStaleWatchLease)

	published := clock
	published.HighWater = 12
	_, err = runNamespaceWatchPublicationResolution(context.Background(), clock, lease, candidates, nil,
		func(context.Context) (namespaceWatchClockRow, error) { return published, nil },
		func(_ context.Context, candidate datastore.NamespaceWatchEvent) (datastore.NamespaceWatchEvent, error) {
			candidate.DeduplicationKey = "different"
			return candidate, nil
		})
	require.ErrorContains(t, err, "different event")
}
