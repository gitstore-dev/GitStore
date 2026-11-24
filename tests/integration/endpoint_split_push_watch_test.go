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
	uid, revision   string
	namespace       string
	generation      int64
	at              time.Time
	version, cursor string
	content         translationCapacityContent
	historical      bool
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

func newCapacityPushWatch(parent context.Context, cfg capacityPushConfig, token string, preparation ...capacityPushConfig) (*capacityPushWatch, error) {
	if len(preparation) > 1 {
		return nil, fmt.Errorf("native push watch permits one bounded preparation schedule")
	}
	prefixes := []string{"cpd-" + cfg.runID + "-"}
	maximum := int(cfg.duration/cfg.interval) + 1
	for _, extra := range preparation {
		if cfg.kind != "translation" || extra.kind != cfg.kind || extra.namespace != cfg.namespace ||
			extra.verifyAPIA != cfg.verifyAPIA || extra.verifyAPIB != cfg.verifyAPIB ||
			extra.runID == "" || extra.runID == cfg.runID || len(extra.runID) > 40 ||
			extra.duration <= 0 || extra.duration > 30*time.Second || extra.interval < 50*time.Millisecond {
			return nil, fmt.Errorf("native push watch preparation scope or schedule mismatch")
		}
		for _, character := range extra.runID {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return nil, fmt.Errorf("native push watch preparation identifier is invalid")
			}
		}
		prefixes = append(prefixes, "cpd-"+extra.runID+"-")
		maximum += int((extra.duration-1)/extra.interval) + 1
	}
	ctx, cancel := context.WithCancel(parent)
	w := &capacityPushWatch{cancel: cancel, maximum: maximum}
	for index, origin := range []string{cfg.verifyAPIA, cfg.verifyAPIB} {
		w.seen[index] = make(map[string]capacityPushWatchRecord)
		cursor := ""
		if cfg.kind == "translation" {
			var err error
			cursor, err = getCapacityBootstrapCursor(origin, token)
			if err != nil {
				w.close()
				return nil, err
			}
		}
		conn, err := dialCapacityPushWatch(origin, cfg, token, cursor)
		if err != nil {
			w.close()
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		if cfg.kind != "translation" {
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
			cursor = bookmark.ResourceVersion
		}
		_ = conn.SetReadDeadline(time.Time{})
		w.wait.Add(1)
		go func(index int, origin, cursor string, conn *websocket.Conn, stop func() bool) {
			defer w.wait.Done()
			defer func() {
				stop()
				if conn != nil {
					_ = conn.Close()
				}
			}()
			reconnects, connectedAt := 0, time.Now()
			for ctx.Err() == nil {
				event, err := readCapacityPushWatch(conn, cfg.kind)
				if err != nil {
					stop()
					_ = conn.Close()
					if ctx.Err() != nil {
						return
					}
					if cfg.kind == "translation" {
						if !translationIntegrationWatchRecoverable(err) {
							w.fail(err)
							return
						}
						if time.Since(connectedAt) > 30*time.Second {
							reconnects = 0
						}
						reconnects++
						if reconnects > 8 {
							w.fail(fmt.Errorf("translation capacity: watch reconnect budget exhausted"))
							return
						}
						select {
						case <-ctx.Done():
							return
						case <-time.After(250 * time.Millisecond):
						}
					}
					conn, err = dialCapacityPushWatch(origin, cfg, token, cursor)
					if err != nil {
						w.fail(err)
						return
					}
					current := conn
					stop = context.AfterFunc(ctx, func() { _ = current.Close() })
					connectedAt = time.Now()
					continue
				}
				if event.ResourceVersion == "" {
					w.fail(fmt.Errorf("native push watch omitted durable cursor"))
					return
				}
				if event.ResourceVersion != cursor {
					reconnects = 0
				}
				cursor = event.ResourceVersion
				matched := false
				for _, prefix := range prefixes {
					matched = matched || strings.HasPrefix(event.Name, prefix)
				}
				if event.Type == "BOOKMARK" || !matched {
					continue
				}
				w.mu.Lock()
				if _, exists := w.seen[index][event.Name]; !exists && len(w.seen[index]) >= w.maximum {
					w.err = fmt.Errorf("native push watch cardinality exceeded run bound")
					w.mu.Unlock()
					return
				}
				previous := w.seen[index][event.Name]
				if previous.uid != event.Metadata.UID || previous.revision != event.Metadata.Revision ||
					previous.version != event.Metadata.ResourceVersion || previous.content != event.Content ||
					previous.namespace != event.Metadata.Namespace || previous.generation != event.Metadata.Generation || previous.historical != event.Historical {
					w.seen[index][event.Name] = capacityPushWatchRecord{
						uid: event.Metadata.UID, revision: event.Metadata.Revision, at: time.Now(),
						version: event.Metadata.ResourceVersion, cursor: event.ResourceVersion,
						content: event.Content, historical: event.Historical,
						namespace: event.Metadata.Namespace, generation: event.Metadata.Generation,
					}
				}
				w.mu.Unlock()
			}
		}(index, origin, cursor, conn, stop)
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
	if cfg.kind == "translation" {
		if cursor == "" {
			return nil, fmt.Errorf("translation capacity: an observed initial or resume cursor is required")
		}
		query, err := translationIntegrationQueryDocument("watch")
		if err != nil {
			return nil, err
		}
		return dialCapacitySubscription(strings.TrimSuffix(origin, "/"), token, "translation-integration", query,
			map[string]any{"namespace": cfg.namespace, "cursor": cursor}, 30*time.Second)
	}
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
	Metadata                    capacityPushWatchMetadata
	Content                     translationCapacityContent
	Historical                  bool
}

type capacityPushWatchMetadata struct {
	UID, Revision, ResourceVersion, Namespace string
	Generation                                int64
}

func readCapacityPushWatch(conn *websocket.Conn, kind string) (capacityPushWatchEvent, error) {
	if kind == "translation" {
		event, err := readTranslationIntegrationEvent(conn)
		if err != nil {
			return capacityPushWatchEvent{}, err
		}
		result := capacityPushWatchEvent{
			Type: event.Type, Name: event.Name, ResourceVersion: event.ResourceVersion, Historical: event.Historical,
		}
		if event.Type == "BOOKMARK" {
			return result, nil
		}
		if event.Translation == nil || event.Translation.APIVersion != "i18n.gitstore.dev/v1beta1" ||
			event.Translation.Kind != "Translation" || event.Translation.Metadata.Name != event.Name {
			return result, fmt.Errorf("translation capacity: incomplete watch source")
		}
		source := event.Translation
		result.Metadata = capacityPushWatchMetadata{
			UID: source.Metadata.UID, Revision: source.Metadata.Revision,
			ResourceVersion: source.Metadata.ResourceVersion, Generation: source.Metadata.Generation, Namespace: source.Metadata.Namespace,
		}
		result.Content, err = translationCapacityObservedContent(*source, "")
		return result, err
	}
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
				Metadata capacityPushWatchMetadata
			}
			if err := json.Unmarshal(value, &object); err != nil {
				return event, err
			}
			event.Metadata = object.Metadata
		}
		return event, nil
	}
}
