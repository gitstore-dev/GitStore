// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type capacityPushWatchRecord struct {
	uid, revision string
	at            time.Time
}

// Two additional measurement streams verify the native pushes. Each owns a
// finite run-sized map, resumes its durable cursor after disconnect, and never
// treats an HTTP read or a connected socket as a journal acknowledgement.
type capacityPushWatch struct {
	mu      sync.Mutex
	seen    [2]map[string]capacityPushWatchRecord
	err     error
	cancel  context.CancelFunc
	wait    sync.WaitGroup
	maximum int
}

func newCapacityPushWatch(parent context.Context, cfg capacityPushConfig, token string) (*capacityPushWatch, error) {
	ctx, cancel := context.WithCancel(parent)
	w := &capacityPushWatch{cancel: cancel, maximum: int(cfg.duration/cfg.interval) + 1}
	for index, origin := range []string{cfg.verifyAPIA, cfg.verifyAPIB} {
		w.seen[index] = make(map[string]capacityPushWatchRecord)
		conn, err := dialCapacityPushWatch(origin, cfg, token, "")
		if err != nil {
			w.close()
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		bookmark, err := readCapacityPushWatch(conn, cfg.kind)
		if err != nil || bookmark.ResourceVersion == "" || bookmark.Type != "BOOKMARK" {
			stop()
			_ = conn.Close()
			w.close()
			if err != nil {
				return nil, fmt.Errorf("native push watch bootstrap failed: %w", err)
			}
			return nil, fmt.Errorf("native push watch bootstrap omitted its bookmark/cursor")
		}
		_ = conn.SetReadDeadline(time.Time{})
		w.wait.Add(1)
		go func(index int, origin, cursor string, conn *websocket.Conn, stop func() bool) {
			defer w.wait.Done()
			defer func() { stop(); _ = conn.Close() }()
			for ctx.Err() == nil {
				event, err := readCapacityPushWatch(conn, cfg.kind)
				if err != nil {
					stop()
					_ = conn.Close()
					if ctx.Err() != nil {
						return
					}
					conn, err = dialCapacityPushWatch(origin, cfg, token, cursor)
					if err != nil {
						w.fail(err)
						return
					}
					current := conn
					stop = context.AfterFunc(ctx, func() { _ = current.Close() })
					continue
				}
				if event.ResourceVersion == "" {
					w.fail(fmt.Errorf("native push watch omitted durable cursor"))
					return
				}
				cursor = event.ResourceVersion
				if event.Type == "BOOKMARK" || !strings.HasPrefix(event.Name, "cpd-"+cfg.runID+"-") {
					continue
				}
				w.mu.Lock()
				if _, exists := w.seen[index][event.Name]; !exists && len(w.seen[index]) >= w.maximum {
					w.err = fmt.Errorf("native push watch cardinality exceeded run bound")
					w.mu.Unlock()
					return
				}
				previous := w.seen[index][event.Name]
				if previous.uid != event.Metadata.UID || previous.revision != event.Metadata.Revision {
					w.seen[index][event.Name] = capacityPushWatchRecord{event.Metadata.UID, event.Metadata.Revision, time.Now()}
				}
				w.mu.Unlock()
			}
		}(index, origin, bookmark.ResourceVersion, conn, stop)
	}
	return w, nil
}

func (w *capacityPushWatch) fail(err error) { w.mu.Lock(); defer w.mu.Unlock(); w.err = err }
func (w *capacityPushWatch) close()         { w.cancel(); w.wait.Wait() }
func (w *capacityPushWatch) verified(name, uid, revision string, deadline time.Time) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return false, w.err
	}
	for _, seen := range w.seen {
		record, ok := seen[name]
		if !ok || record.uid != uid || !capacityPushRevisionMatches(record.revision, revision) || record.at.After(deadline) {
			return false, nil
		}
	}
	return true, nil
}

func dialCapacityPushWatch(origin string, cfg capacityPushConfig, token, cursor string) (*websocket.Conn, error) {
	field, resource := "watchCategories", "category"
	if cfg.kind == "product" {
		field, resource = "watchProducts", "product"
	}
	if cursor == "" {
		cursor = "__category_watch_bootstrap__"
		if cfg.kind == "product" {
			cursor = "__product_watch_bootstrap__"
		}
	}
	query := `subscription($namespace:String!,$cursor:String){` + field + `(namespace:$namespace,resourceVersion:$cursor){type name resourceVersion ` + resource + `{metadata{uid revision}}}}`
	return dialCapacitySubscription(strings.TrimSuffix(origin, "/"), token, "native-push", query,
		map[string]any{"namespace": cfg.namespace, "cursor": cursor}, 30*time.Second)
}

type capacityPushWatchEvent struct {
	Type, Name, ResourceVersion string
	Metadata                    struct{ UID, Revision string }
}

func readCapacityPushWatch(conn *websocket.Conn, kind string) (capacityPushWatchEvent, error) {
	field, resource := "watchCategories", "category"
	if kind == "product" {
		field, resource = "watchProducts", "product"
	}
	for {
		var message struct {
			Type    string
			Payload struct {
				Data   map[string]json.RawMessage
				Errors []json.RawMessage
			}
		}
		if err := conn.ReadJSON(&message); err != nil {
			return capacityPushWatchEvent{}, err
		}
		if message.Type == "ping" {
			if err := conn.WriteJSON(map[string]string{"type": "pong"}); err != nil {
				return capacityPushWatchEvent{}, err
			}
			continue
		}
		if message.Type != "next" || len(message.Payload.Errors) > 0 {
			return capacityPushWatchEvent{}, fmt.Errorf("native push watch protocol/error response")
		}
		var raw map[string]json.RawMessage
		var event capacityPushWatchEvent
		if err := json.Unmarshal(message.Payload.Data[field], &event); err != nil {
			return event, err
		}
		if err := json.Unmarshal(message.Payload.Data[field], &raw); err != nil {
			return event, err
		}
		if value := raw[resource]; len(value) > 0 && string(value) != "null" {
			var object struct {
				Metadata struct{ UID, Revision string }
			}
			if err := json.Unmarshal(value, &object); err != nil {
				return event, err
			}
			event.Metadata = object.Metadata
		}
		return event, nil
	}
}
