// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitctl

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheckFailsClosedWithoutFollowingRedirects(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ready" {
					t.Error("healthcheck followed a redirect")
				}
				w.Header().Set("Location", "/login")
				w.WriteHeader(status)
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			code := Run([]string{"healthcheck", "--url", server.URL + "/ready"}, strings.NewReader(""), &stdout, &stderr)
			want := 1
			if status == http.StatusOK {
				want = 0
			}
			if code != want || stdout.Len() != 0 {
				t.Fatalf("healthcheck exit=%d, want=%d, stderr=%s", code, want, stderr.String())
			}
		})
	}
}

func TestHealthcheckDeadlineAndInvalidArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	var stderr bytes.Buffer
	if code := runHealthcheck([]string{"--url", server.URL, "--timeout", "10ms"}, &stderr); code != 1 {
		t.Fatalf("unresponsive readiness did not time out: code=%d", code)
	}
	for _, args := range [][]string{{"--timeout", "0s"}, {"--url", "file:///etc/passwd"}, {"unexpected"}} {
		if code := runHealthcheck(args, &stderr); code != 2 {
			t.Fatalf("invalid healthcheck arguments accepted: %v", args)
		}
	}
}
