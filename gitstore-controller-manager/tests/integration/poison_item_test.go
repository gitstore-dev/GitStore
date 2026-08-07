// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/api"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/manager"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/retry"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestIntegration_PoisonedItem_SurfacedAsTerminalFailure covers FR-007: an
// item that exhausts its retry budget (transient failures past MaxAttempts)
// or fails with TerminalFailure immediately must be surfaced as a poisoned
// item, not retried forever.
func TestIntegration_PoisonedItem_SurfacedAsTerminalFailure(t *testing.T) {
	t.Run("TransientPastMaxAttempts", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		kind := "Widget-US4-TransientPoison"
		r := newScriptedReconciler(types.ResultTransient(errors.New("permanent failure")))
		mgr := manager.New()
		c := cache.New[string]()
		c.MarkSynced()
		if err := mgr.Register(manager.ReconcilerRegistration{
			Kind:            kind,
			Reconciler:      r,
			Cache:           c,
			MaxAttempts:     3,
			InitialInterval: 5 * time.Millisecond,
			MaxInterval:     20 * time.Millisecond,
			Multiplier:      2.0,
			StallThreshold:  1 * time.Minute,
			WorkerCount:     1,
		}); err != nil {
			t.Fatalf("Register failed: %v", err)
		}

		before := testutil.ToFloat64(health.PoisonItemsTotal.WithLabelValues(kind))

		go func() { _ = mgr.Start(ctx) }()

		key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "poison-widget"}
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}

		deadline := time.Now().Add(3 * time.Second)
		var quarantined bool
		for time.Now().Before(deadline) {
			if mgr.IsQuarantined(key) {
				quarantined = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !quarantined {
			t.Fatal("expected item to be quarantined after exhausting MaxAttempts")
		}

		found := false
		for _, item := range mgr.AllPoisonItems() {
			if item.Key == key {
				found = true
				break
			}
		}
		if !found {
			t.Error("expected quarantined key to appear in Manager.AllPoisonItems()")
		}

		after := testutil.ToFloat64(health.PoisonItemsTotal.WithLabelValues(kind))
		if after != before+1 {
			// KindStats() must be called to refresh the gauge (it does not
			// self-update); trigger it directly, matching how health.NewMetricsHandler does.
			mgr.KindStats()
			after = testutil.ToFloat64(health.PoisonItemsTotal.WithLabelValues(kind))
		}
		if after != before+1 {
			t.Errorf("PoisonItemsTotal{%s} = %v, want %v", kind, after, before+1)
		}
	})

	t.Run("TerminalImmediately", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		kind := "Widget-US4-TerminalPoison"
		r := newScriptedReconciler(types.ResultTerminal(errors.New("unrecoverable")))
		mgr := manager.New()
		c := cache.New[string]()
		c.MarkSynced()
		if err := mgr.Register(manager.ReconcilerRegistration{
			Kind:            kind,
			Reconciler:      r,
			Cache:           c,
			MaxAttempts:     3,
			InitialInterval: 5 * time.Millisecond,
			MaxInterval:     20 * time.Millisecond,
			Multiplier:      2.0,
			StallThreshold:  1 * time.Minute,
			WorkerCount:     1,
		}); err != nil {
			t.Fatalf("Register failed: %v", err)
		}

		go func() { _ = mgr.Start(ctx) }()

		key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "terminal-widget"}
		if err := mgr.Enqueue(key); err != nil {
			t.Fatalf("Enqueue failed: %v", err)
		}

		deadline := time.Now().Add(2 * time.Second)
		var quarantined bool
		for time.Now().Before(deadline) {
			if mgr.IsQuarantined(key) {
				quarantined = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !quarantined {
			t.Fatal("expected terminal failure to be quarantined immediately")
		}
		// Terminal failures must not consume retry budget: exactly one call.
		if got := r.callCount(); got != 1 {
			t.Errorf("reconciler call count = %d, want 1 (terminal failure consumes no retry budget)", got)
		}
	})
}

// TestIntegration_PoisonedItem_VisibleViaHTTPPoisonAPI covers FR-007/FR-011:
// a quarantined item must be visible through the same poison-item HTTP
// handlers cmd/controller/main.go registers in production, and requeuing
// through that API must clear quarantine and re-enqueue the item.
func TestIntegration_PoisonedItem_VisibleViaHTTPPoisonAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	kind := "Widget-US4-HTTPPoisonAPI"
	// First call fails terminally (quarantining immediately); the call
	// triggered by the HTTP requeue succeeds, proving requeue both clears
	// quarantine and genuinely re-dispatches the item for reconciliation
	// (rather than leaving it in limbo).
	r := newScriptedReconciler(types.ResultTerminal(errors.New("bad data")), types.ResultOK())
	mgr := manager.New()
	c := cache.New[string]()
	c.MarkSynced()
	if err := mgr.Register(manager.ReconcilerRegistration{
		Kind:            kind,
		Reconciler:      r,
		Cache:           c,
		MaxAttempts:     3,
		InitialInterval: 5 * time.Millisecond,
		MaxInterval:     20 * time.Millisecond,
		Multiplier:      2.0,
		StallThreshold:  1 * time.Minute,
		WorkerCount:     1,
	}); err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	go func() { _ = mgr.Start(ctx) }()

	key := types.WorkItemKey{Kind: kind, Namespace: "ns", Name: "http-poison-widget"}
	if err := mgr.Enqueue(key); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.IsQuarantined(key) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mgr.IsQuarantined(key) {
		t.Fatal("expected item to be quarantined before exercising the HTTP API")
	}

	// Mirrors the exact route registration in cmd/controller/main.go's
	// buildMux: a single GET route handles both a specific kind and "_all".
	mux := http.NewServeMux()
	mux.HandleFunc("GET /controller/v1/poison/{kind}", api.ListPoisonHandler(mgr))
	mux.HandleFunc("POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue", api.RequeuePoisonHandler(mgr))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// GET /controller/v1/poison/{kind}
	resp, err := http.Get(srv.URL + "/controller/v1/poison/" + kind)
	if err != nil {
		t.Fatalf("GET poison/{kind} failed: %v", err)
	}
	var items []*retry.PoisonItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		t.Fatalf("decode GET poison/{kind} response: %v", err)
	}
	resp.Body.Close()
	if len(items) != 1 || items[0].Key != key || items[0].LastError == "" {
		t.Fatalf("GET poison/{kind} items = %+v, want one item with key %+v and a non-empty LastError", items, key)
	}

	// GET /controller/v1/poison/_all
	respAll, err := http.Get(srv.URL + "/controller/v1/poison/_all")
	if err != nil {
		t.Fatalf("GET poison/_all failed: %v", err)
	}
	var allItems []*retry.PoisonItem
	if err := json.NewDecoder(respAll.Body).Decode(&allItems); err != nil {
		t.Fatalf("decode GET poison/_all response: %v", err)
	}
	respAll.Body.Close()
	found := false
	for _, item := range allItems {
		if item.Key == key {
			found = true
		}
	}
	if !found {
		t.Errorf("GET poison/_all = %+v, want to include key %+v", allItems, key)
	}

	// POST /controller/v1/poison/{namespace}/{kind}/{name}/requeue
	requeueURL := srv.URL + "/controller/v1/poison/" + key.Namespace + "/" + kind + "/" + key.Name + "/requeue"
	requeueResp, err := http.Post(requeueURL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST requeue failed: %v", err)
	}
	requeueResp.Body.Close()
	if requeueResp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST requeue status = %d, want %d", requeueResp.StatusCode, http.StatusNoContent)
	}

	// The requeue handler removes the key from quarantine synchronously
	// (Manager.Requeue), but the follow-up reconcile dispatch is async.
	// Poll for the reconciler's second call (the scripted success) to
	// confirm requeue genuinely re-dispatched the item.
	deadline2 := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline2) {
		if r.callCount() >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.callCount(); got < 2 {
		t.Fatalf("reconciler call count after requeue = %d, want at least 2 (original terminal call + requeued call)", got)
	}
	if mgr.IsQuarantined(key) {
		t.Error("expected requeue to clear quarantine and the requeued reconcile to succeed, but item is quarantined again")
	}
}
