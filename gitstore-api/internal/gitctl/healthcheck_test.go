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

func TestMetricsRetrievalFailsClosedAndBoundsOutput(t *testing.T) {
	const metrics = "process_start_time_seconds 100\ngitstore_api_process_instance_info{instance_id=\"api-a\"} 1\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unavailable":
			w.WriteHeader(http.StatusServiceUnavailable)
		case "/redirect":
			w.Header().Set("Location", "/metrics")
			w.WriteHeader(http.StatusFound)
		case "/oversized":
			_, _ = w.Write([]byte(strings.Repeat("x", (16<<20)+1)))
			return
		case "/slow":
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(metrics))
	}))
	defer server.Close()
	for _, path := range []string{"/metrics", "/unavailable", "/redirect", "/oversized", "/slow"} {
		t.Run(path, func(t *testing.T) {
			args := []string{"metrics", "--url", server.URL + path}
			if path == "/slow" {
				args = append(args, "--timeout", "10ms")
			}
			var stdout, stderr bytes.Buffer
			code := Run(args, strings.NewReader(""), &stdout, &stderr)
			if path == "/metrics" {
				if code != 0 || stdout.String() != metrics {
					t.Fatalf("metrics retrieval failed: code=%d stdout=%q stderr=%s", code, stdout.String(), stderr.String())
				}
			} else if code != 1 || stdout.Len() != 0 {
				t.Fatalf("invalid metrics response escaped: code=%d bytes=%d stderr=%s", code, stdout.Len(), stderr.String())
			}
		})
	}
}
