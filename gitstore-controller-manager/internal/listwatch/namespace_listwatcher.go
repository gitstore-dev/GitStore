// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
)

const namespaceFields = `
  metadata { uid name resourceVersion generation finalizers }
  status {
    observedGeneration
    lastAppliedRevision
    conditions { type status observedGeneration lastTransitionTime reason message }
  }
`

const namespacesControllerListQuery = `
query($after: String) {
  namespaces(first: 100, after: $after) {
    edges {
      node {
` + namespaceFields + `
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}`

const watchNamespacesSubscription = `
subscription($resourceVersion: String) {
  watchResources(kind: "Namespace", resourceVersion: $resourceVersion) {
    type
    kind
    name
    resourceVersion
    object
  }
}`

type namespaceMetadataJSON struct {
	UID             string   `json:"uid"`
	Name            string   `json:"name"`
	ResourceVersion string   `json:"resourceVersion"`
	Generation      int64    `json:"generation"`
	Finalizers      []string `json:"finalizers"`
}

type namespaceStatusJSON struct {
	ObservedGeneration  int64           `json:"observedGeneration"`
	LastAppliedRevision string          `json:"lastAppliedRevision"`
	Conditions          []conditionJSON `json:"conditions"`
}

type namespaceNodeJSON struct {
	Metadata namespaceMetadataJSON `json:"metadata"`
	Status   *namespaceStatusJSON  `json:"status"`
}

func (n namespaceNodeJSON) toNamespace() namespacecontroller.Namespace {
	out := namespacecontroller.Namespace{
		UID:             n.Metadata.UID,
		Name:            n.Metadata.Name,
		Generation:      n.Metadata.Generation,
		ResourceVersion: n.Metadata.ResourceVersion,
		Finalizers:      n.Metadata.Finalizers,
	}
	if n.Status == nil {
		return out
	}
	out.Status = status.ResourceStatus{
		ResourceVersion:     n.Metadata.ResourceVersion,
		ObservedGeneration:  n.Status.ObservedGeneration,
		LastAppliedRevision: n.Status.LastAppliedRevision,
	}
	for _, condition := range n.Status.Conditions {
		out.Status.Conditions = append(out.Status.Conditions, &status.Condition{
			Type:               condition.Type,
			Status:             condition.Status,
			ObservedGeneration: condition.ObservedGeneration,
			LastTransitionTime: condition.LastTransitionTime,
			Reason:             condition.Reason,
			Message:            condition.Message,
		})
	}
	return out
}

type namespacesControllerListResponse struct {
	Namespaces struct {
		Edges []struct {
			Node namespaceNodeJSON `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool    `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"namespaces"`
}

// NamespaceListWatcher lists Namespaces and watches the generic Namespace
// resource stream.
type NamespaceListWatcher struct {
	client *graphqlclient.Client
}

// NewNamespaceListWatcher returns a Namespace ListWatcher.
func NewNamespaceListWatcher(client *graphqlclient.Client) *NamespaceListWatcher {
	return &NamespaceListWatcher{client: client}
}

// List paginates all Namespaces and returns the highest observed version.
func (lw *NamespaceListWatcher) List(ctx context.Context) (ListResponse[namespacecontroller.Namespace], error) {
	var items []namespacecontroller.Namespace
	highestVersion := ""
	var after *string
	for {
		var response namespacesControllerListResponse
		vars := map[string]any{}
		if after != nil {
			vars["after"] = *after
		}
		if err := lw.client.Query(ctx, namespacesControllerListQuery, vars, &response); err != nil {
			return ListResponse[namespacecontroller.Namespace]{}, fmt.Errorf("listwatch: list namespaces: %w", err)
		}
		for _, edge := range response.Namespaces.Edges {
			item := edge.Node.toNamespace()
			items = append(items, item)
			if item.ResourceVersion > highestVersion {
				highestVersion = item.ResourceVersion
			}
		}
		if !response.Namespaces.PageInfo.HasNextPage || response.Namespaces.PageInfo.EndCursor == nil {
			break
		}
		after = response.Namespaces.PageInfo.EndCursor
	}
	if highestVersion == "" {
		highestVersion = noResourceVersionSentinel
	}
	return ListResponse[namespacecontroller.Namespace]{Items: items, ResourceVersion: highestVersion}, nil
}

// Watch opens the generic watchResources stream for Namespace events.
func (lw *NamespaceListWatcher) Watch(ctx context.Context, resourceVersion string) (Watcher[namespacecontroller.Namespace], error) {
	if resourceVersion == noResourceVersionSentinel {
		resourceVersion = ""
	}
	vars := map[string]any{}
	if resourceVersion != "" {
		vars["resourceVersion"] = resourceVersion
	}
	subscription, err := lw.client.Subscribe(ctx, watchNamespacesSubscription, vars)
	if err != nil {
		if isWatchExpiredErr(err) {
			return nil, fmt.Errorf("listwatch: watch namespaces: %w", ErrWatchExpired)
		}
		return nil, fmt.Errorf("listwatch: watch namespaces: %w", err)
	}

	watcher := &namespaceWatcher{
		subscription: subscription,
		events:       make(chan WatchEvent[namespacecontroller.Namespace], 16),
	}
	go watcher.run()
	return watcher, nil
}

type namespaceWatchEventJSON struct {
	Type            string          `json:"type"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	ResourceVersion string          `json:"resourceVersion"`
	Object          json.RawMessage `json:"object"`
}

type namespaceWatcher struct {
	subscription graphqlclient.Subscription
	events       chan WatchEvent[namespacecontroller.Namespace]
	err          error
}

func (w *namespaceWatcher) Events() <-chan WatchEvent[namespacecontroller.Namespace] {
	return w.events
}

func (w *namespaceWatcher) Err() error { return w.err }
func (w *namespaceWatcher) Stop()      { w.subscription.Stop() }

func (w *namespaceWatcher) run() {
	defer close(w.events)
	for raw := range w.subscription.Next() {
		var payload struct {
			WatchResources namespaceWatchEventJSON `json:"watchResources"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			w.err = fmt.Errorf("listwatch: decode Namespace watch payload: %w", err)
			return
		}
		event := payload.WatchResources
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
			w.err = fmt.Errorf("listwatch: unknown Namespace event type %q", event.Type)
			return
		}

		item := namespacecontroller.Namespace{Name: event.Name, ResourceVersion: event.ResourceVersion}
		if len(event.Object) > 0 && string(event.Object) != "null" {
			var node namespaceNodeJSON
			if err := json.Unmarshal(event.Object, &node); err != nil {
				w.err = fmt.Errorf("listwatch: decode Namespace object: %w", err)
				return
			}
			item = node.toNamespace()
		}
		w.events <- WatchEvent[namespacecontroller.Namespace]{
			Type:            eventType,
			Object:          item,
			ResourceVersion: event.ResourceVersion,
		}
	}
	if err := w.subscription.Err(); err != nil {
		if isWatchExpiredErr(err) {
			w.err = ErrWatchExpired
		} else {
			w.err = err
		}
	}
}
