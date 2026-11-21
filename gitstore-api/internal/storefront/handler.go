// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package storefront owns the public endpoint boundary. It has no access to
// management rows or Git; publication reads require a separate reviewed contract.
package storefront

import "net/http"

// NewHandler returns the fixed unavailable boundary until publication and trusted
// target selection exist. It never reads a request body or executes a resolver.
func NewHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Storefront is unavailable","extensions":{"code":"STOREFRONT_UNAVAILABLE"}}]}`))
	})
}
