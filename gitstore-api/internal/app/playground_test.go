// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlaygroundExposesOnlyItsOwnInitialEndpoint(t *testing.T) {
	for _, kind := range []string{"admin", "storefront"} {
		w := httptest.NewRecorder()
		endpointPlayground(kind).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+kind+"/playground", nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.Contains(t, w.Body.String(), `id="endpoint-kind"`)
		require.Contains(t, w.Body.String(), `id="endpoint-url"`)
		require.Contains(t, w.Body.String(), `data-kind="`+kind+`"`)
		require.NotContains(t, w.Body.String(), "localhost:4000")
		require.NotContains(t, w.Body.String(), "localhost:4001")
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	}
}
