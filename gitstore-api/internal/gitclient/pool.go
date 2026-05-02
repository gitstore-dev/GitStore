package gitclient

import (
	"net/http"
	"time"
)

// newPooledHTTPClient creates an http.Client with a connection pool tuned for
// frequent short-lived requests to the git-server (clone, push, health checks).
//
// Key settings:
//   - MaxIdleConns / MaxIdleConnsPerHost: keep connections warm between operations
//   - IdleConnTimeout: release idle connections after 90 s to avoid holding
//     file descriptors when there is no activity
//   - DisableCompression: git payloads are already compressed (packfiles)
func newPooledHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}
