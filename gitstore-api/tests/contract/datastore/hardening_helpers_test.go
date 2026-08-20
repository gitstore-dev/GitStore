//go:build scylla

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var hardeningEpoch = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

func hardeningTimestamp(monthOffset, sequence int) time.Time {
	return hardeningEpoch.AddDate(0, monthOffset, 0).Add(time.Duration(sequence) * time.Millisecond)
}

func hardeningNamespace(sequence int) string {
	return fmt.Sprintf("hardening-%03d", sequence)
}

func assertStableIDs(t *testing.T, ids []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			t.Errorf("duplicate stable id %q", id)
		}
		seen[id] = struct{}{}
	}
	assert.Len(t, seen, len(ids))
}
