// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
)

const categoriesListQuery = `
query($after: String) {
  categories(first: 100, after: $after) {
    edges {
      cursor
      node {
        metadata { uid name namespace resourceVersion generation }
        spec { parentRef { name } }
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}`

const watchCategoriesSubscription = `
subscription($resourceVersion: String) {
  watchCategories(resourceVersion: $resourceVersion) {
    type
    namespace
    name
    resourceVersion
    category {
      metadata { uid name namespace resourceVersion generation }
      spec { parentRef { name } }
    }
  }
}`

type categoryMetadataJSON struct {
	UID             string `json:"uid"`
	Name            string `json:"name"`
	Namespace       string `json:"namespace"`
	ResourceVersion string `json:"resourceVersion"`
	Generation      int64  `json:"generation"`
}

type categorySpecJSON struct {
	ParentRef *struct {
		Name string `json:"name"`
	} `json:"parentRef"`
}

type categoryNodeJSON struct {
	Metadata categoryMetadataJSON `json:"metadata"`
	Spec     categorySpecJSON     `json:"spec"`
}

func (n categoryNodeJSON) toCategoryTaxonomy() categorytaxonomy.CategoryTaxonomy {
	c := categorytaxonomy.CategoryTaxonomy{
		UID:             n.Metadata.UID,
		Namespace:       n.Metadata.Namespace,
		Name:            n.Metadata.Name,
		Generation:      n.Metadata.Generation,
		ResourceVersion: n.Metadata.ResourceVersion,
	}
	if n.Spec.ParentRef != nil {
		c.ParentRefName = n.Spec.ParentRef.Name
	}
	return c
}

type categoriesListResponse struct {
	Categories struct {
		Edges []struct {
			Node categoryNodeJSON `json:"node"`
		} `json:"edges"`
		PageInfo struct {
			HasNextPage bool    `json:"hasNextPage"`
			EndCursor   *string `json:"endCursor"`
		} `json:"pageInfo"`
	} `json:"categories"`
}

// CategoryTaxonomyListWatcher satisfies ListWatcher[categorytaxonomy.CategoryTaxonomy]/
// Watcher[categorytaxonomy.CategoryTaxonomy] against a real gitstore-api instance,
// via the categories query and watchCategories subscription (spec 040).
type CategoryTaxonomyListWatcher struct {
	client *graphqlclient.Client
}

// NewCategoryTaxonomyListWatcher returns a CategoryTaxonomyListWatcher issuing
// requests through client.
func NewCategoryTaxonomyListWatcher(client *graphqlclient.Client) *CategoryTaxonomyListWatcher {
	return &CategoryTaxonomyListWatcher{client: client}
}

// List paginates the categories query to completion, returning every
// CategoryTaxonomy and the highest observed resourceVersion as the list-time
// cursor.
func (lw *CategoryTaxonomyListWatcher) List(ctx context.Context) (ListResponse[categorytaxonomy.CategoryTaxonomy], error) {
	var items []categorytaxonomy.CategoryTaxonomy
	highestRV := ""
	var after *string

	for {
		var resp categoriesListResponse
		vars := map[string]any{}
		if after != nil {
			vars["after"] = *after
		}
		if err := lw.client.Query(ctx, categoriesListQuery, vars, &resp); err != nil {
			return ListResponse[categorytaxonomy.CategoryTaxonomy]{}, fmt.Errorf("listwatch: list categories: %w", err)
		}
		for _, edge := range resp.Categories.Edges {
			c := edge.Node.toCategoryTaxonomy()
			items = append(items, c)
			if c.ResourceVersion > highestRV {
				highestRV = c.ResourceVersion
			}
		}
		if !resp.Categories.PageInfo.HasNextPage || resp.Categories.PageInfo.EndCursor == nil {
			break
		}
		after = resp.Categories.PageInfo.EndCursor
	}

	return ListResponse[categorytaxonomy.CategoryTaxonomy]{Items: items, ResourceVersion: highestRV}, nil
}

// Watch opens a watchCategories subscription starting after resourceVersion.
func (lw *CategoryTaxonomyListWatcher) Watch(ctx context.Context, resourceVersion string) (Watcher[categorytaxonomy.CategoryTaxonomy], error) {
	vars := map[string]any{}
	if resourceVersion != "" {
		vars["resourceVersion"] = resourceVersion
	}
	sub, err := lw.client.Subscribe(ctx, watchCategoriesSubscription, vars)
	if err != nil {
		if isWatchExpiredErr(err) {
			return nil, fmt.Errorf("listwatch: watch categories: %w", ErrWatchExpired)
		}
		return nil, fmt.Errorf("listwatch: watch categories: %w", err)
	}

	w := &categoryWatcher{sub: sub, events: make(chan WatchEvent[categorytaxonomy.CategoryTaxonomy], 16)}
	go w.run()
	return w, nil
}

func isWatchExpiredErr(err error) bool {
	var gqlErr *graphqlclient.Error
	if errors.As(err, &gqlErr) {
		return gqlErr.Extensions["code"] == "WATCH_EXPIRED"
	}
	return false
}

type watchCategoriesEventJSON struct {
	Type            string            `json:"type"`
	Namespace       *string           `json:"namespace"`
	Name            string            `json:"name"`
	ResourceVersion string            `json:"resourceVersion"`
	Category        *categoryNodeJSON `json:"category"`
}

type categoryWatcher struct {
	sub    graphqlclient.Subscription
	events chan WatchEvent[categorytaxonomy.CategoryTaxonomy]
	err    error
}

func (w *categoryWatcher) Events() <-chan WatchEvent[categorytaxonomy.CategoryTaxonomy] { return w.events }
func (w *categoryWatcher) Err() error                                                   { return w.err }
func (w *categoryWatcher) Stop()                                                        { w.sub.Stop() }

func (w *categoryWatcher) run() {
	defer close(w.events)
	for raw := range w.sub.Next() {
		var payload struct {
			WatchCategories watchCategoriesEventJSON `json:"watchCategories"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			w.err = fmt.Errorf("listwatch: decode watchCategories payload: %w", err)
			return
		}
		ev := payload.WatchCategories

		var evType EventType
		switch ev.Type {
		case "ADDED":
			evType = Added
		case "MODIFIED":
			evType = Modified
		case "DELETED":
			evType = Deleted
		case "BOOKMARK":
			evType = Bookmark
		default:
			w.err = fmt.Errorf("listwatch: unknown watchCategories event type %q", ev.Type)
			return
		}

		var obj categorytaxonomy.CategoryTaxonomy
		if ev.Category != nil {
			obj = ev.Category.toCategoryTaxonomy()
		} else {
			obj.Namespace = derefOr(ev.Namespace, "")
			obj.Name = ev.Name
			obj.ResourceVersion = ev.ResourceVersion
		}

		w.events <- WatchEvent[categorytaxonomy.CategoryTaxonomy]{
			Type:            evType,
			Object:          obj,
			ResourceVersion: ev.ResourceVersion,
		}
	}
	if err := w.sub.Err(); err != nil {
		if isWatchExpiredErr(err) {
			w.err = ErrWatchExpired
		} else {
			w.err = err
		}
	}
}

func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}
