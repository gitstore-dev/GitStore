// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
)

type resourceListWatcher[T any] struct {
	client     *graphqlclient.Client
	kind       string
	listQuery  string
	watchQuery string
	identity   func(T) (string, string, error)
	tombstone  func(string, string) T
}

func (l *resourceListWatcher[T]) List(ctx context.Context) (ListResponse[T], error) {
	return collectList[T](ctx, l)
}

func (l *resourceListWatcher[T]) ListPages(ctx context.Context, visit func([]T) error) (string, error) {
	if visit == nil {
		return "", fmt.Errorf("listwatch: missing %s page visitor", l.kind)
	}
	cursor, err := bootstrapCursor[T](ctx, l, "__resource_watch_bootstrap__")
	if err != nil {
		return "", err
	}
	err = visitNamespaceIdentifiers(ctx, l.client, func(namespace string) error {
		if namespace == "" {
			return fmt.Errorf("listwatch: missing %s namespace", l.kind)
		}
		var after *string
		for {
			var response struct {
				Resources *struct {
					Edges []struct {
						Node T `json:"node"`
					} `json:"edges"`
					PageInfo struct {
						HasNextPage bool    `json:"hasNextPage"`
						EndCursor   *string `json:"endCursor"`
					} `json:"pageInfo"`
				} `json:"resources"`
			}
			variables := map[string]any{"namespace": namespace}
			if after != nil {
				variables["after"] = *after
			}
			if err := l.client.Query(ctx, l.listQuery, variables, &response); err != nil {
				return fmt.Errorf("listwatch: list %s: %w", l.kind, err)
			}
			if response.Resources == nil {
				return fmt.Errorf("listwatch: missing %s page", l.kind)
			}
			if len(response.Resources.Edges) > 100 {
				return fmt.Errorf("listwatch: oversized %s page", l.kind)
			}
			observeListPage(ctx, len(response.Resources.Edges))
			page := make([]T, 0, len(response.Resources.Edges))
			for _, edge := range response.Resources.Edges {
				actualNamespace, _, err := l.identity(edge.Node)
				if err != nil {
					return err
				}
				if actualNamespace != namespace {
					return fmt.Errorf("listwatch: %s page crossed namespace", l.kind)
				}
				page = append(page, edge.Node)
			}
			if err := visit(page); err != nil {
				return err
			}
			after, err = nextListCursor(response.Resources.PageInfo.HasNextPage, response.Resources.PageInfo.EndCursor, after)
			if err != nil || after == nil {
				return err
			}
		}
	})
	if err != nil {
		return "", err
	}
	return cursor, nil
}

func (l *resourceListWatcher[T]) Watch(ctx context.Context, cursor string) (Watcher[T], error) {
	variables := map[string]any{}
	if cursor != "" {
		variables["resourceVersion"] = cursor
	}
	sub, err := l.client.Subscribe(ctx, l.watchQuery, variables)
	if err != nil {
		if isWatchExpiredErr(err) {
			return nil, fmt.Errorf("listwatch: watch %s: %w", l.kind, ErrWatchExpired)
		}
		return nil, err
	}
	w := &resourceWatcher[T]{
		watchStop: newWatchStop(sub.Stop), sub: sub,
		events: make(chan WatchEvent[T], 16), source: l,
	}
	go w.run()
	return w, nil
}

type resourceWatcher[T any] struct {
	*watchStop
	sub    graphqlclient.Subscription
	events chan WatchEvent[T]
	source *resourceListWatcher[T]
	err    error
}

func (w *resourceWatcher[T]) Events() <-chan WatchEvent[T] { return w.events }
func (w *resourceWatcher[T]) Err() error                   { return w.err }

func (w *resourceWatcher[T]) run() {
	defer close(w.events)
	defer w.Stop()
	for raw := range w.sub.Next() {
		var payload struct {
			Event struct {
				Type            string `json:"type"`
				Namespace       string `json:"namespace"`
				Name            string `json:"name"`
				ResourceVersion string `json:"resourceVersion"`
				Resource        *T     `json:"resource"`
			} `json:"event"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			w.err = err
			return
		}
		event := payload.Event
		var eventType EventType
		switch event.Type {
		case "ADDED":
			eventType = Added
		case "MODIFIED":
			eventType = Modified
		case "DELETED":
			eventType = Deleted
		case "BOOKMARK":
			eventType = Bookmark
		default:
			w.err = fmt.Errorf("listwatch: unknown %s watch event", w.source.kind)
			return
		}
		if event.ResourceVersion == "" ||
			(eventType != Bookmark && (event.Namespace == "" || event.Name == "")) ||
			((eventType == Added || eventType == Modified) && event.Resource == nil) {
			w.err = fmt.Errorf("listwatch: incomplete %s watch event", w.source.kind)
			return
		}
		resource := w.source.tombstone(event.Namespace, event.Name)
		if event.Resource != nil {
			resource = *event.Resource
			namespace, name, err := w.source.identity(resource)
			if err != nil {
				w.err = err
				return
			}
			if namespace != event.Namespace || name != event.Name {
				w.err = fmt.Errorf("listwatch: %s watch identity mismatch", w.source.kind)
				return
			}
		}
		if !sendWatchEvent(w.watchStop, w.events, WatchEvent[T]{
			Type: eventType, Object: resource, ResourceVersion: event.ResourceVersion,
		}) {
			return
		}
	}
	if err := w.sub.Err(); isWatchExpiredErr(err) {
		w.err = ErrWatchExpired
	} else {
		w.err = err
	}
}
