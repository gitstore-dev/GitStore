// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestNativePushWatchResumesCursorAndRequiresBothReplicaIdentities(t *testing.T) {
	serve := func() *httptest.Server {
		var connections atomic.Int64
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			conn, err := (&websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}).Upgrade(w, r, nil)
			if err != nil {
				return
			}
			defer conn.Close()
			var init map[string]any
			if conn.ReadJSON(&init) != nil {
				return
			}
			_ = conn.WriteJSON(map[string]string{"type": "connection_ack"})
			var subscribe struct {
				Payload struct{ Variables struct{ Cursor string } }
			}
			if conn.ReadJSON(&subscribe) != nil {
				return
			}
			send := func(event map[string]any) {
				_ = conn.WriteJSON(map[string]any{"type": "next", "payload": map[string]any{"data": map[string]any{"watchCategories": event}}})
			}
			if connections.Add(1) == 1 {
				require.Equal(t, "__category_watch_bootstrap__", subscribe.Payload.Variables.Cursor)
				send(map[string]any{"type": "BOOKMARK", "resourceVersion": "durable-before", "name": ""})
				return
			}
			require.Equal(t, "durable-before", subscribe.Payload.Variables.Cursor)
			send(map[string]any{"type": "ADDED", "resourceVersion": "durable-after", "name": "cpd-fixture-000001", "category": map[string]any{"metadata": map[string]string{"uid": "stable", "revision": "main@sha1:" + strings.Repeat("a", 40)}}})
			_, _, _ = conn.ReadMessage()
		}))
	}
	a, b := serve(), serve()
	defer a.Close()
	defer b.Close()
	cfg := capacityPushConfig{verifyAPIA: a.URL, verifyAPIB: b.URL, namespace: "endpoint-split-fixture", runID: "fixture", kind: "category", duration: time.Minute, interval: time.Second}
	watch, err := newCapacityPushWatch(t.Context(), cfg, "fixture")
	require.NoError(t, err)
	defer watch.close()
	deadline := time.Now().Add(time.Second)
	require.Eventually(t, func() bool {
		seen, err := watch.verified("cpd-fixture-000001", "stable", strings.Repeat("a", 40), deadline)
		return err == nil && seen
	}, time.Second, 10*time.Millisecond)
	seen, err := watch.verified("cpd-fixture-000001", "wrong-uid", strings.Repeat("a", 40), deadline)
	require.NoError(t, err)
	require.False(t, seen)
	seen, err = watch.verified("cpd-fixture-000001", "stable", strings.Repeat("a", 40), time.Now().Add(-time.Minute))
	require.NoError(t, err)
	require.False(t, seen, "late watch delivery cannot satisfy the convergence deadline")
}

func TestCapacityPushOracleVerifiesCommitIdentityAndReadyOnBothReplicas(t *testing.T) {
	serve := func(uid string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/admin/graphql", r.URL.Path)
			require.Equal(t, "Bearer fixture", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"category": map[string]any{"metadata": map[string]any{"uid": uid, "revision": "main@sha1:" + strings.Repeat("a", 40)}, "status": map[string]any{"conditions": []map[string]string{{"type": "Ready", "status": "TRUE"}}}}}})
		}))
	}
	a, b := serve("stable-uid"), serve("stable-uid")
	defer a.Close()
	defer b.Close()
	cfg := capacityPushConfig{verifyAPIA: a.URL, verifyAPIB: b.URL, namespace: "endpoint-split-fixture", runID: "fixture", kind: "category"}
	result := capacityPushResult{acknowledged: true, sequence: 1, commit: strings.Repeat("a", 40), at: time.Now()}
	require.NoError(t, verifyCapacityPush(t.Context(), cfg, "fixture", result))
	other := serve("other-uid")
	defer other.Close()
	cfg.verifyAPIB = other.URL
	require.ErrorContains(t, verifyCapacityPush(t.Context(), cfg, "fixture", result), "identities differ")
	result.at = time.Now().Add(-2 * time.Minute)
	require.Error(t, verifyCapacityPush(t.Context(), cfg, "fixture", result))
}

// Verification is decoupled from offered Git pushes: a slow controller cannot
// silently reduce the offered schedule. Both the backlog and worker count are
// finite, and queue waiting is included in the 60-second convergence deadline.
type capacityPushOracle struct {
	jobs                      chan capacityPushResult
	wait                      sync.WaitGroup
	finishOnce                sync.Once
	verified, failed, dropped atomic.Int64
	watch                     *capacityPushWatch
}

func newCapacityPushOracle(ctx context.Context, cfg capacityPushConfig, token string) (*capacityPushOracle, error) {
	if cfg.verifyAPIA == "" || cfg.verifyAPIB == "" || cfg.verifyAPIA == cfg.verifyAPIB || cfg.namespace == "" {
		return nil, fmt.Errorf("two distinct Admin origins and an owned namespace are required")
	}
	oracle := &capacityPushOracle{jobs: make(chan capacityPushResult, 128)}
	watch, err := newCapacityPushWatch(ctx, cfg, token)
	if err != nil {
		return nil, err
	}
	oracle.watch = watch
	for range 4 {
		oracle.wait.Add(1)
		go func() {
			defer oracle.wait.Done()
			for result := range oracle.jobs {
				if err := verifyCapacityPush(ctx, cfg, token, result, watch); err != nil {
					oracle.failed.Add(1)
				} else {
					oracle.verified.Add(1)
				}
			}
		}()
	}
	return oracle, nil
}
func (o *capacityPushOracle) offer(result capacityPushResult) {
	select {
	case o.jobs <- result:
	default:
		o.dropped.Add(1)
	}
}
func (o *capacityPushOracle) finish() (int, int, int) {
	o.finishOnce.Do(func() { close(o.jobs) })
	o.wait.Wait()
	o.watch.close()
	return int(o.verified.Load()), int(o.failed.Load()), int(o.dropped.Load())
}

func verifyCapacityPush(parent context.Context, cfg capacityPushConfig, token string, result capacityPushResult, watches ...*capacityPushWatch) error {
	if result.commit == "" || result.at.IsZero() {
		return fmt.Errorf("acknowledged push has no commit/time proof")
	}
	ctx, cancel := context.WithDeadline(parent, result.at.Add(time.Minute))
	defer cancel()
	field := "category"
	if cfg.kind == "product" {
		field = "product"
	}
	name := fmt.Sprintf("cpd-%s-%06d", cfg.runID, result.sequence)
	query := `query($namespace:String!,$name:String!){` + field + `(by:{namespacePath:{namespace:$namespace,name:$name}}){metadata{uid revision}status{conditions{type status}}}}`
	payload, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"namespace": cfg.namespace, "name": name}})
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		ready, uid := true, ""
		for _, origin := range []string{cfg.verifyAPIA, cfg.verifyAPIB} {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(origin, "/")+"/admin/graphql", bytes.NewReader(payload))
			if err != nil {
				return err
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token)
			response, err := client.Do(request)
			if err != nil {
				ready = false
				continue
			}
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			_ = response.Body.Close()
			if response.StatusCode == 401 || response.StatusCode == 403 {
				return fmt.Errorf("push oracle authorization rejected")
			}
			var envelope struct {
				Data map[string]*struct {
					Metadata struct{ UID, Revision string }
					Status   struct {
						Conditions []struct{ Type, Status string }
					}
				}
				Errors []json.RawMessage
			}
			if readErr != nil || response.StatusCode != 200 || json.Unmarshal(body, &envelope) != nil || len(envelope.Errors) > 0 || envelope.Data[field] == nil {
				ready = false
				continue
			}
			resource := envelope.Data[field]
			if resource.Metadata.UID == "" || !capacityPushRevisionMatches(resource.Metadata.Revision, result.commit) {
				ready = false
				continue
			}
			if uid != "" && uid != resource.Metadata.UID {
				return fmt.Errorf("replica resource identities differ")
			}
			uid = resource.Metadata.UID
			resourceReady := false
			for _, condition := range resource.Status.Conditions {
				if condition.Type == "Ready" && strings.EqualFold(condition.Status, "true") {
					resourceReady = true
				}
			}
			ready = ready && resourceReady
		}
		if ready {
			if len(watches) == 0 {
				return nil
			}
			seen, err := watches[0].verified(name, uid, result.commit, result.at.Add(time.Minute))
			if err != nil {
				return err
			}
			if seen {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func capacityPushRevisionMatches(revision, commit string) bool {
	return len(commit) == 40 && secretCapacityCommitRevision.MatchString(commit) &&
		(revision == commit || revision == "main@sha1:"+commit)
}

func TestCapacityPushOracleAcceptsAdmissionRevisionWithoutAcceptingAnotherCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, revision := range []string{commit, "main@sha1:" + commit} {
		require.True(t, capacityPushRevisionMatches(revision, commit))
	}
	for _, revision := range []string{"other@sha1:" + commit, "main@sha1:" + strings.Repeat("b", 40), "main@sha1:abc"} {
		require.False(t, capacityPushRevisionMatches(revision, commit))
	}
}
