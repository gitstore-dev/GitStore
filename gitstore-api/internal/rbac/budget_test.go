// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac_test

import (
	"math"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/stretchr/testify/require"
)

func TestMemoryBudgetReservesReplacementAndPinnedReaders(t *testing.T) {
	budget := rbac.NewMemoryBudget(100)
	old, err := budget.Reserve(60)
	require.NoError(t, err)
	_, err = budget.Reserve(60)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.EqualValues(t, 60, budget.Used())
	replacement, err := budget.Reserve(40)
	require.NoError(t, err)
	require.ErrorIs(t, replacement.Grow(1), rbac.ErrUnavailable)
	require.ErrorIs(t, replacement.Grow(math.MaxInt64), rbac.ErrUnavailable)
	old.Close()
	old.Close()
	require.EqualValues(t, 40, budget.Used())
	require.NoError(t, replacement.Grow(60))
	require.ErrorIs(t, old.Grow(1), rbac.ErrUnavailable)
	replacement.Close()
	require.Zero(t, budget.Used())
}

func TestMemoryBudgetConcurrentReservationsStayWithinBound(t *testing.T) {
	budget := rbac.NewMemoryBudget(8)
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			for range 100 {
				reservation, err := budget.Reserve(1)
				if err != nil {
					continue
				}
				if used := budget.Used(); used > 8 || used < 1 {
					t.Errorf("live reservation outside bound: %d", used)
				}
				reservation.Close()
			}
		})
	}
	workers.Wait()
	require.Zero(t, budget.Used())
	_, err := budget.Reserve(-1)
	require.ErrorIs(t, err, rbac.ErrUnavailable)
	require.Zero(t, budget.Used())
}
