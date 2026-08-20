// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutationAuditContextRoundTrip(t *testing.T) {
	t.Parallel()
	timestamp := time.Date(2026, time.August, 19, 22, 0, 0, 0, time.UTC)

	audit, ok := MutationAuditFromContext(WithMutationAudit(context.Background(), "alice", timestamp))

	require.True(t, ok)
	assert.Equal(t, "alice", audit.Actor)
	assert.Equal(t, timestamp, audit.Timestamp)
}

func TestMutationAuditContextAbsent(t *testing.T) {
	t.Parallel()

	_, ok := MutationAuditFromContext(context.Background())

	assert.False(t, ok)
}
