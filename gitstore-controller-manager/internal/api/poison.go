// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package api exposes the controller-manager HTTP management surface.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// Requeuer is the subset of Manager that the poison handlers need.
type Requeuer interface {
	QuarantineStore(kind string) *retry.QuarantineStore
	AllPoisonItems() []*retry.PoisonItem
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

		if kind == "_all" {
			items := mgr.AllPoisonItems()
			if items == nil {
				items = []*retry.PoisonItem{}
			}
			writeJSON(w, http.StatusOK, items)
			return
		}

		qs := mgr.QuarantineStore(kind)
		if qs == nil {
			writeJSON(w, http.StatusNotFound, errorBody{Error: "kind not registered"})
			return
		}

		items := qs.List(kind)
		if items == nil {
			items = []*retry.PoisonItem{}
		}
		writeJSON(w, http.StatusOK, items)
	}
}

// RequeuePoisonHandler removes a key from quarantine and re-enqueues it.
// Route: POST /controller/v1/poison/{kind}/{namespace}/{name}/requeue
func RequeuePoisonHandler(mgr Requeuer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := r.PathValue("kind")
		namespace := r.PathValue("namespace")
		name := r.PathValue("name")

		key := types.WorkItemKey{Kind: kind, Namespace: namespace, Name: name}
		if err := mgr.Requeue(key); err != nil {
			switch err {
			case types.ErrKindNotRegistered:
				writeJSON(w, http.StatusNotFound, errorBody{Error: "kind not registered"})
			case types.ErrNotFound:
				writeJSON(w, http.StatusNotFound, errorBody{Error: "item not in quarantine"})
			default:
				writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: err.Error()})
			}
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
