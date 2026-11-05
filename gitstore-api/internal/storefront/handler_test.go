// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package storefront

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type unreadableBody struct{}

func (unreadableBody) Read([]byte) (int, error) { panic("unavailable handler read request body") }
func (unreadableBody) Close() error             { return nil }

func TestUnavailableNeverExecutesData(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, credential := range []string{"", "Bearer invalid", "Bearer admin"} {
			t.Run(method+credential, func(t *testing.T) {
				r := httptest.NewRequest(method, "/storefront/graphql", nil)
				r.Body = io.ReadCloser(unreadableBody{})
				r.Header.Set("Authorization", credential)
				r.Header.Set("Upgrade", "websocket")
				w := httptest.NewRecorder()
				NewHandler().ServeHTTP(w, r)
				require.Equal(t, http.StatusServiceUnavailable, w.Code)
				require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				require.NotContains(t, body, "data")
				require.True(t, strings.Contains(string(body["errors"]), "STOREFRONT_UNAVAILABLE"))
			})
		}
	}
}
