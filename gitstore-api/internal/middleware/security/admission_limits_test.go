// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLimiterKeyBudgetNeverEvictsActiveQuota(t *testing.T) {
	r := NewBoundedRateLimit(1, 1, 2, time.Minute)
	first := func(ip string) int {
		engine := gin.New()
		engine.Use(r.RateLimiter)
		engine.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w.Code
	}
	require.Equal(t, 200, first("127.0.0.1"))
	require.Equal(t, 200, first("127.0.0.2"))
	require.Equal(t, 429, first("127.0.0.3"))
	require.Equal(t, 429, first("127.0.0.1"))
	require.Len(t, r.clients, 2)
	r.Close()
	require.Empty(t, r.clients)
	require.Equal(t, 429, first("127.0.0.4"))
}

func TestIdleExpiryNeverResetsAnUnrefilledQuota(t *testing.T) {
	r := NewBoundedRateLimit(0.000001, 1, 1, time.Minute)
	require.True(t, r.Allow("first", 1))
	// Force an idle sweep without waiting a minute. The entry remains quota
	// active even after idle TTL because its consumed token has not refilled.
	r.mu.Lock()
	r.clients["first"].lastSeen = time.Now().Add(-2 * time.Minute)
	r.nextSweep = time.Time{}
	r.mu.Unlock()
	require.False(t, r.Allow("second", 1))
	require.False(t, r.Allow("first", 1))
	require.Equal(t, 1, r.KeyCount())
}

func TestIdleRefilledEntriesReleaseFiniteKeyCapacity(t *testing.T) {
	r := NewBoundedRateLimit(1e9, 1, 1, time.Nanosecond)
	require.True(t, r.Allow("first", 1))
	require.Eventually(t, func() bool { return r.Allow("second", 1) }, time.Second, time.Millisecond)
	require.Equal(t, 1, r.KeyCount())
	r.Close()
	require.Zero(t, r.KeyCount())
}
