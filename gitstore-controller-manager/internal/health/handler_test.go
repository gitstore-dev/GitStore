// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testManagerStats struct{}

func (testManagerStats) KindStats() map[string]KindStat { return map[string]KindStat{} }

type testCredentialReadiness bool

func (r testCredentialReadiness) Ready() bool { return bool(r) }

func TestNewHandlerDegradesWhenCredentialIsUnavailable(t *testing.T) {
	handler := NewHandler(testManagerStats{}, "test", testCredentialReadiness(false))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"credentialReady":false`) {
		t.Errorf("response = %q, want credential readiness state", body)
	}
}

func TestNewHandlerOmitsCredentialStateWhenNoCredentialChecker(t *testing.T) {
	handler := NewHandler(testManagerStats{}, "test")
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if strings.Contains(recorder.Body.String(), "credentialReady") {
		t.Errorf("response unexpectedly includes credential state: %q", recorder.Body.String())
	}
}
