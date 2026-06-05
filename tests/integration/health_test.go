// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestHealthEndpoints covers contract C-001:
// gitstore-api must respond 200 with {"status":"healthy"}.
// Git HTTP is served by gitstore-api (spec#012); gitstore-git-service has no HTTP endpoint.
func TestHealthEndpoints(t *testing.T) {
	endpoints := []struct {
		name string
		url  string
	}{
		{"gitstore-api", fmt.Sprintf("%s/health", apiURL)},
	}

	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			resp, err := http.Get(ep.url)
			if err != nil {
				t.Fatalf("PREREQUISITE: service unreachable (%s): %v — is docker compose up?", ep.url, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				t.Errorf("expected 200 from %s, got %d", ep.url, resp.StatusCode)
			}

			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode health response from %s: %v", ep.url, err)
			}

			status, ok := body["status"].(string)
			if !ok || status != "healthy" {
				t.Errorf("expected {\"status\":\"healthy\"} from %s, got %v", ep.url, body)
			}
		})
	}
}
