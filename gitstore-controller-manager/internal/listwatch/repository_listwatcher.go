// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	repositorycontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/repository"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
)

const repositoryFields = `
  metadata { uid name namespace resourceVersion generation finalizers }
  spec { defaultBranch }
  status {
    observedGeneration
    lastAppliedRevision
    conditions { type status observedGeneration lastTransitionTime reason message }
    resolved { storageClass }
  }
`

const repositoriesControllerListQuery = `
query($namespace: String!, $after: String) {
  repositories(namespace: $namespace, first: 100, after: $after) {
    edges { node {
` + repositoryFields + `
    } }
    pageInfo { hasNextPage endCursor }
  }
}`

const watchRepositoriesSubscription = `
subscription($resourceVersion: String) {
  watchRepositories(resourceVersion: $resourceVersion) {
    type namespace name resourceVersion
    repository {
` + repositoryFields + `
    }
  }
}`

type repositoryMetadataJSON struct {
	UID             string   `json:"uid"`
	Name            string   `json:"name"`
	Namespace       string   `json:"namespace"`
	ResourceVersion string   `json:"resourceVersion"`
	Generation      int64    `json:"generation"`
	Finalizers      []string `json:"finalizers"`
}

type repositorySpecJSON struct {
	DefaultBranch string `json:"defaultBranch"`
}

type repositoryResolvedJSON struct {
	StorageClass string `json:"storageClass"`
}

type repositoryStatusJSON struct {
	ObservedGeneration  int64                   `json:"observedGeneration"`
	LastAppliedRevision string                  `json:"lastAppliedRevision"`
	Conditions          []conditionJSON         `json:"conditions"`
	Resolved            *repositoryResolvedJSON `json:"resolved"`
}

type repositoryNodeJSON struct {
	Metadata repositoryMetadataJSON `json:"metadata"`
	Spec     repositorySpecJSON     `json:"spec"`
	Status   *repositoryStatusJSON  `json:"status"`
}

func (n repositoryNodeJSON) toRepository() repositorycontroller.Repository {
	out := repositorycontroller.Repository{
		UID:             n.Metadata.UID,
		Namespace:       n.Metadata.Namespace,
		Name:            n.Metadata.Name,
		Generation:      n.Metadata.Generation,
		ResourceVersion: n.Metadata.ResourceVersion,
		Finalizers:      append([]string(nil), n.Metadata.Finalizers...),
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
			Type: condition.Type, Status: condition.Status,
			ObservedGeneration: condition.ObservedGeneration,
			LastTransitionTime: condition.LastTransitionTime,
			Reason:             condition.Reason, Message: condition.Message,
		})
	}
	if n.Status.Resolved != nil {
		out.StorageClass = n.Status.Resolved.StorageClass
	}
	return out
}

type repositoriesControllerListResponse struct {
	Repositories struct {
		Edges []struct {
			Node repositoryNodeJSON `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool    `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"repositories"`
}

// RepositoryListWatcher lists Repository resources from every Namespace and
// watches the corresponding global Repository stream. Repository list queries
// remain namespace scoped, so List enumerates Namespaces before collecting the
// per-Namespace pages.
type RepositoryListWatcher struct {
	client *graphqlclient.Client
}

func NewRepositoryListWatcher(client *graphqlclient.Client) *RepositoryListWatcher {
	return &RepositoryListWatcher{client: client}
}

func (lw *RepositoryListWatcher) List(ctx context.Context) (ListResponse[repositorycontroller.Repository], error) {
	watcher, err := lw.Watch(ctx, repositoryWatchBootstrapCursor)
	if err != nil {
		return ListResponse[repositorycontroller.Repository]{}, fmt.Errorf("listwatch: establish repository watch cursor: %w", err)
	}
	defer watcher.Stop()

	var cursorEvent WatchEvent[repositorycontroller.Repository]
	select {
	case event, ok := <-watcher.Events():
		if !ok {
			if err := watcher.Err(); err != nil {
				return ListResponse[repositorycontroller.Repository]{}, fmt.Errorf("listwatch: establish repository watch cursor: %w", err)
			}
			return ListResponse[repositorycontroller.Repository]{}, fmt.Errorf("listwatch: repository watch closed before bookmark")
		}
		cursorEvent = event
	case <-ctx.Done():
		return ListResponse[repositorycontroller.Repository]{}, ctx.Err()
	}
	if cursorEvent.Type != Bookmark || cursorEvent.ResourceVersion == "" {
		return ListResponse[repositorycontroller.Repository]{}, fmt.Errorf("listwatch: repository watch did not return a bootstrap bookmark")
	}
	watcher.Stop()

	namespaces, err := listNamespaceIdentifiers(ctx, lw.client)
	if err != nil {
		return ListResponse[repositorycontroller.Repository]{}, err
	}

	var items []repositorycontroller.Repository
	for _, namespace := range namespaces {
		var after *string
		for {
			var response repositoriesControllerListResponse
			vars := map[string]any{"namespace": namespace}
			if after != nil {
				vars["after"] = *after
			}
			if err := lw.client.Query(ctx, repositoriesControllerListQuery, vars, &response); err != nil {
				return ListResponse[repositorycontroller.Repository]{}, fmt.Errorf("listwatch: list repositories: %w", err)
			}
			for _, edge := range response.Repositories.Edges {
				items = append(items, edge.Node.toRepository())
			}
			if !response.Repositories.PageInfo.HasNextPage || response.Repositories.PageInfo.EndCursor == nil {
				break
			}
			after = response.Repositories.PageInfo.EndCursor
		}
	}
	return ListResponse[repositorycontroller.Repository]{Items: items, ResourceVersion: cursorEvent.ResourceVersion}, nil
}

func (lw *RepositoryListWatcher) Watch(ctx context.Context, resourceVersion string) (Watcher[repositorycontroller.Repository], error) {
	vars := map[string]any{}
	if resourceVersion != "" {
		vars["resourceVersion"] = resourceVersion
	}
	subscription, err := lw.client.Subscribe(ctx, watchRepositoriesSubscription, vars)
	if err != nil {
		if isWatchExpiredErr(err) {
			return nil, fmt.Errorf("listwatch: watch repositories: %w", ErrWatchExpired)
		}
		return nil, fmt.Errorf("listwatch: watch repositories: %w", err)
	}
	w := &repositoryWatcher{subscription: subscription, events: make(chan WatchEvent[repositorycontroller.Repository], 16)}
	go w.run()
	return w, nil
}

type repositoryWatchEventJSON struct {
	Type            string              `json:"type"`
	Namespace       *string             `json:"namespace"`
	Name            string              `json:"name"`
	ResourceVersion string              `json:"resourceVersion"`
	Repository      *repositoryNodeJSON `json:"repository"`
}

type repositoryWatcher struct {
	subscription graphqlclient.Subscription
	events       chan WatchEvent[repositorycontroller.Repository]
	err          error
}

func (w *repositoryWatcher) Events() <-chan WatchEvent[repositorycontroller.Repository] {
	return w.events
}
func (w *repositoryWatcher) Err() error { return w.err }
func (w *repositoryWatcher) Stop()      { w.subscription.Stop() }
func (w *repositoryWatcher) run() {
	defer close(w.events)
	for raw := range w.subscription.Next() {
		var payload struct {
			WatchRepositories repositoryWatchEventJSON `json:"watchRepositories"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			w.err = fmt.Errorf("listwatch: decode Repository watch payload: %w", err)
			return
		}
		event := payload.WatchRepositories
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
			w.err = fmt.Errorf("listwatch: unknown Repository event type %q", event.Type)
			return
		}
		item := repositorycontroller.Repository{Namespace: derefOr(event.Namespace, ""), Name: event.Name, ResourceVersion: event.ResourceVersion}
		if event.Repository != nil {
			item = event.Repository.toRepository()
		}
		w.events <- WatchEvent[repositorycontroller.Repository]{Type: eventType, Object: item, ResourceVersion: event.ResourceVersion}
	}
	if err := w.subscription.Err(); err != nil {
		if isWatchExpiredErr(err) {
			w.err = ErrWatchExpired
		} else {
			w.err = err
		}
	}
}

const repositoryWatchBootstrapCursor = "__repository_watch_bootstrap__"
