package middleware

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter tracks requests per IP using a fixed-window counter.
type rateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     int           // requests allowed per window
	window   time.Duration // rolling window
	cleanupT time.Duration // how often to purge stale buckets
	stopCh   chan struct{}
}

type bucket struct {
	count    int
	windowAt time.Time
}

// newRateLimiter creates a rate limiter with the given rate per window.
func newRateLimiter(rate int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		buckets:  make(map[string]*bucket),
		rate:     rate,
		window:   window,
		cleanupT: window * 5,
		stopCh:   make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// allow returns true if the request from key should be allowed.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok || now.After(b.windowAt.Add(rl.window)) {
		rl.buckets[key] = &bucket{count: 1, windowAt: now}
		return true
	}
	b.count++
	return b.count <= rl.rate
}

// cleanup periodically removes expired buckets.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(rl.cleanupT)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-rl.window)
			for key, b := range rl.buckets {
				if b.windowAt.Before(cutoff) {
					delete(rl.buckets, key)
				}
			}
			rl.mu.Unlock()
		case <-rl.stopCh:
			return
		}
	}
}

// RateLimitMiddleware returns an HTTP middleware that limits requests to
// `rate` per `window` per client IP. Requests that exceed the limit receive
// 429 Too Many Requests. The ctx controls the lifetime of the background
// cleanup goroutine — pass the server's base context.
func RateLimitMiddleware(ctx context.Context, rate int, window time.Duration) func(http.Handler) http.Handler {
	rl := newRateLimiter(rate, window)
	go func() {
		<-ctx.Done()
		close(rl.stopCh)
	}()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !rl.allow(ip) {
				http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns the host portion of r.RemoteAddr.
// X-Forwarded-For is intentionally ignored: it is attacker-controlled and
// cannot be trusted for rate-limiting decisions unless the service sits
// exclusively behind a verified trusted proxy. Reverse proxies that need
// per-client rate limiting should enforce it themselves.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
