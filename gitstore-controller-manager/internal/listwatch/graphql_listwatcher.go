// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package listwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
)

const categoryFields = `
  metadata { uid name namespace resourceVersion generation }
  spec {
    parentRef { name }
    media { fileRef { name optional } }
  }
  status {
    observedGeneration
    lastAppliedRevision
    conditions { type status observedGeneration lastTransitionTime reason message }
    resolved { depth path childCount productCount }
  }
`

const categoriesListQuery = `
query($after: String) {
  categories(first: 100, after: $after) {
    edges {
      cursor
      node {
` + categoryFields + `
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
` + categoryFields + `
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

type mediaJSON struct {
	FileRef struct {
		Name     string `json:"name"`
		Optional bool   `json:"optional"`
	} `json:"fileRef"`
}

type categorySpecJSON struct {
	ParentRef *struct {
		Name string `json:"name"`
	} `json:"parentRef"`
	Media []mediaJSON `json:"media"`
}

type conditionJSON struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	ObservedGeneration int64     `json:"observedGeneration"`
	LastTransitionTime time.Time `json:"lastTransitionTime"`
	Reason             string    `json:"reason"`
	Message            string    `json:"message"`
}

type resolvedJSON struct {
	Depth        int8     `json:"depth"`
	Path         []string `json:"path"`
	ChildCount   int64    `json:"childCount"`
	ProductCount int64    `json:"productCount"`
}

type categoryStatusJSON struct {
	ObservedGeneration  int64           `json:"observedGeneration"`
	LastAppliedRevision string          `json:"lastAppliedRevision"`
	Conditions          []conditionJSON `json:"conditions"`
	Resolved            *resolvedJSON   `json:"resolved"`
}

type categoryNodeJSON struct {
	Metadata categoryMetadataJSON `json:"metadata"`
	Spec     categorySpecJSON     `json:"spec"`
	Status   *categoryStatusJSON  `json:"status"`
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
	for _, m := range n.Spec.Media {
		c.Media = append(c.Media, categorytaxonomy.MediaRef{Name: m.FileRef.Name, Optional: m.FileRef.Optional})
	}
	if n.Status != nil {
		c.Status = status.ResourceStatus{
			ResourceVersion:     n.Metadata.ResourceVersion,
			ObservedGeneration:  n.Status.ObservedGeneration,
			LastAppliedRevision: n.Status.LastAppliedRevision,
		}
		for _, cond := range n.Status.Conditions {
			c.Status.Conditions = append(c.Status.Conditions, &status.Condition{
				Type:               cond.Type,
				Status:             cond.Status,
				ObservedGeneration: cond.ObservedGeneration,
				LastTransitionTime: cond.LastTransitionTime,
				Reason:             cond.Reason,
				Message:            cond.Message,
			})
		}
		if n.Status.Resolved != nil {
			resolvedJSON, err := json.Marshal(categorytaxonomy.ResolvedCategoryTaxonomy{
				Depth:        n.Status.Resolved.Depth,
				Path:         n.Status.Resolved.Path,
				ChildCount:   n.Status.Resolved.ChildCount,
				ProductCount: n.Status.Resolved.ProductCount,
			})
			if err == nil {
				c.Status.Resolved = resolvedJSON
			}
		}
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

// noResourceVersionSentinel is returned by List when the namespace has zero
// CategoryTaxonomy resources, so there is no real resourceVersion to report
// yet. checkpoint.FilesystemStore/spec 036's ListWatcher contract require a
// non-empty cursor from List (an empty string is reserved for "no checkpoint
// exists"), but gitstore-api's real resourceVersions are always >= "1"
// (nextResourceVersion), so "0" can never collide with one. Watch treats it
// identically to "" (subscribe from the beginning).
const noResourceVersionSentinel = "0"

// List paginates the categories query to completion, returning every
// CategoryTaxonomy and the highest observed resourceVersion as the list-time
// cursor. When the namespace has zero categories, ResourceVersion is
// noResourceVersionSentinel rather than "" (see its doc comment).
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

	if highestRV == "" {
		highestRV = noResourceVersionSentinel
	}
	return ListResponse[categorytaxonomy.CategoryTaxonomy]{Items: items, ResourceVersion: highestRV}, nil
}

// Watch opens a watchCategories subscription starting after resourceVersion.
func (lw *CategoryTaxonomyListWatcher) Watch(ctx context.Context, resourceVersion string) (Watcher[categorytaxonomy.CategoryTaxonomy], error) {
	if resourceVersion == noResourceVersionSentinel {
		resourceVersion = ""
	}
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

func (w *categoryWatcher) Events() <-chan WatchEvent[categorytaxonomy.CategoryTaxonomy] {
	return w.events
}
func (w *categoryWatcher) Err() error { return w.err }
func (w *categoryWatcher) Stop()      { w.sub.Stop() }

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
