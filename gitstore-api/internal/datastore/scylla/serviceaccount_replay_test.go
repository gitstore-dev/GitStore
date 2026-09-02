// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountAssertionReplayTTL(t *testing.T) {
	now := time.Now().UTC()

	ttl, err := serviceAccountAssertionReplayTTL(now, now.Add(1500*time.Millisecond))
	require.NoError(t, err)
	assert.Equal(t, 2, ttl)

	_, err = serviceAccountAssertionReplayTTL(now, now)
	assert.ErrorIs(t, err, datastore.ErrInvalidArgument)

	_, err = serviceAccountAssertionReplayTTL(now, now.Add(time.Duration(maxServiceAccountAssertionReplayTTL+1)*time.Second))
	assert.ErrorIs(t, err, datastore.ErrInvalidArgument)
}
