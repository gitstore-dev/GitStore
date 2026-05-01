package middleware

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP_StripsPortFromRemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/graphql", nil)
	req.RemoteAddr = "198.51.100.10:45678"
	req.Header.Set("X-Forwarded-For", "203.0.113.1")

	got := clientIP(req)
	if got != "198.51.100.10" {
		t.Fatalf("clientIP() = %q, want %q", got, "198.51.100.10")
	}
}

func TestClientIP_IgnoresXForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/graphql", nil)
	req.RemoteAddr = "192.0.2.25:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.5")

	got := clientIP(req)
	if got != "192.0.2.25" {
		t.Fatalf("clientIP() = %q, want %q", got, "192.0.2.25")
	}
}
