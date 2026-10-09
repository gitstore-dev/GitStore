// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package api exposes the controller-manager HTTP management surface.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// Requeuer is the subset of Manager that the poison handlers need.
type Requeuer interface {
	ListPoisonPage(context.Context, string, string, int) ([]*retry.PoisonItem, string, error)
	Requeue(key types.WorkItemKey) error
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ListPoisonHandler returns JSON list of quarantined items.
// Route: GET /controller/v1/poison/{kind}
// kind="_all" returns items for all kinds.
func ListPoisonHandler(mgr Requeuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		limit := checkpoint.DiskPageItems
		if value := r.URL.Query().Get("limit"); value != "" {
			var err error
			limit, err = strconv.Atoi(value)
			if err != nil || limit < 1 || limit > checkpoint.DiskPageItems {
				writeJSON(w, http.StatusBadRequest, errorBody{Error: "limit must be between 1 and 256"})
				return
			}
		}
		after := ""
		if value := r.URL.Query().Get("after"); value != "" {
			if len(value) > 8192 {
				writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid poison cursor"})
				return
			}
			decoded, err := base64.RawURLEncoding.DecodeString(value)
			parts := strings.Split(string(decoded), "\x00")
			if err != nil || len(decoded) > 4096 || len(parts) != 3 || kind != "_all" && parts[0] != kind {
				writeJSON(w, http.StatusBadRequest, errorBody{Error: "invalid poison cursor"})
				return
			}
			after = string(decoded)
		}
		items, next, err := mgr.ListPoisonPage(r.Context(), kind, after, limit)
		if err != nil {
			code := http.StatusServiceUnavailable
			if errors.Is(err, types.ErrKindNotRegistered) {
				code = http.StatusNotFound
			}
			writeJSON(w, code, errorBody{Error: err.Error()})
			return
		}
		if items == nil {
			items = []*retry.PoisonItem{}
		}
		if next != "" {
			w.Header().Set("X-Next-Cursor", base64.RawURLEncoding.EncodeToString([]byte(next)))
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// RequeuePoisonHandler removes a key from quarantine and re-enqueues it.
// Route: POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue
func RequeuePoisonHandler(mgr Requeuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		namespace := r.PathValue("namespace")
		name := r.PathValue("name")

		key := types.WorkItemKey{Kind: kind, Namespace: namespace, Name: name}
		if err := mgr.Requeue(key); err != nil {
			switch {
			case errors.Is(err, types.ErrKindNotRegistered):
				writeJSON(w, http.StatusNotFound, errorBody{Error: "kind not registered"})
			case errors.Is(err, types.ErrNotFound):
				writeJSON(w, http.StatusNotFound, errorBody{Error: "item not in quarantine"})
			default:
				writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: err.Error()})
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
