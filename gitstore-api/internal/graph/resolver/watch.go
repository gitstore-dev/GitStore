// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"encoding/json"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

const namespaceWatchBootstrapCursor = "__namespace_watch_bootstrap__"

func (r *Resolver) publishNamespaceEvent(eventType eventbus.EventType, namespace *datastore.Namespace) {
	if r.eventBus == nil || namespace == nil {
		return
	}
	r.eventBus.Publish(eventbus.Event{
		Type:            eventType,
		Kind:            "Namespace",
		Name:            namespace.Name,
		ResourceVersion: namespace.ResourceVersion,
		Object:          namespace,
	})
}

// publishCategoryTaxonomyStatusEvent fans out a Modified event after a
// successful status write, so a watcher observing the resource also sees
// controller-driven status changes, not only spec-pipeline admissions
// (spec 040: status writes and admission share one event stream). No-op
// when eventBus is nil.
func (r *Resolver) publishCategoryTaxonomyStatusEvent(c *datastore.CategoryTaxonomy) {
	if r.eventBus == nil || c == nil {
		return
	}
	r.eventBus.Publish(eventbus.Event{
		Type:            eventbus.Modified,
		Kind:            "CategoryTaxonomy",
		Namespace:       c.Namespace,
		Name:            c.Name,
		ResourceVersion: c.ResourceVersion,
		Object:          c,
	})
}

func (r *Resolver) publishNamespaceStatusEvent(namespace *datastore.Namespace) {
	if r.eventBus == nil || namespace == nil {
		return
	}
	r.eventBus.Publish(eventbus.Event{
		Type:            eventbus.Modified,
		Kind:            "Namespace",
		Name:            namespace.Name,
		ResourceVersion: namespace.ResourceVersion,
		Object:          namespace,
	})
}

func (r *Resolver) publishNamespaceDeletedEvent(namespace *datastore.Namespace) {
	if r.eventBus == nil || namespace == nil {
		return
	}
	r.eventBus.Publish(eventbus.Event{
		Type:            eventbus.Deleted,
		Kind:            "Namespace",
		Name:            namespace.Name,
		ResourceVersion: namespace.ResourceVersion,
		Object:          namespace,
	})
}

func toWatchEventType(t eventbus.EventType) model.WatchEventType {
	switch t {
	case eventbus.Added:
		return model.WatchEventTypeAdded
	case eventbus.Modified:
		return model.WatchEventTypeModified
	case eventbus.Deleted:
		return model.WatchEventTypeDeleted
	default:
		return model.WatchEventTypeBookmark
	}
}

// categoryEventMatchesFilters reports whether ev satisfies the namespace
// filter for a watchCategories/watchResources subscription (spec 040
// FR-007). A nil/empty namespace means no filter — matches every namespace.
func categoryEventMatchesFilters(ev eventbus.Event, namespace *string) bool {
	if namespace == nil || *namespace == "" {
		return true
	}
	return ev.Namespace == *namespace
}

// categoryEventMatchesSelector reports whether ev's underlying
// CategoryTaxonomy's labels satisfy selector. Requires ev.Object to be a
// *datastore.CategoryTaxonomy — Deleted events (whose Object may be a
// stale snapshot taken at delete time) are still evaluated against the
// labels they carried, consistent with the delete notification being
// about the resource that was removed, not a hypothetical current state.
func categoryEventMatchesSelector(ev eventbus.Event, selector *model.LabelSelectorInput) bool {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return true
	}
	c, ok := ev.Object.(*datastore.CategoryTaxonomy)
	if !ok || c == nil {
		return false
	}
	return matchesWatchSelector(selector, c.Labels)
}

func toCategoryWatchEvent(ev eventbus.Event) *model.CategoryWatchEvent {
	out := &model.CategoryWatchEvent{
		Type:            toWatchEventType(ev.Type),
		Name:            ev.Name,
		ResourceVersion: ev.Cursor,
	}
	if ev.Namespace != "" {
		ns := ev.Namespace
		out.Namespace = &ns
	}
	if ev.Type != eventbus.Deleted {
		if c, ok := ev.Object.(*datastore.CategoryTaxonomy); ok {
			out.Category = DatastoreCategoryTaxonomyToGraphQL(c)
		}
	}
	return out
}

// toGenericWatchEvent maps an eventbus.Event to the JSON-boxed WatchEvent
// used by the generic watchResources subscription (spec 040 FR-006). The
// object is marshaled through model conversion where a typed converter is
// known (CategoryTaxonomy today); other kinds pass through as a raw map
// once additional kinds register their own converter (research.md R7).
func toGenericWatchEvent(kind string, ev eventbus.Event) *model.WatchEvent {
	out := &model.WatchEvent{
		Type:            toWatchEventType(ev.Type),
		Kind:            kind,
		Name:            ev.Name,
		ResourceVersion: ev.Cursor,
	}
	if ev.Namespace != "" {
		ns := ev.Namespace
		out.Namespace = &ns
	}
	if ev.Type != eventbus.Deleted {
		if c, ok := ev.Object.(*datastore.CategoryTaxonomy); ok {
			out.Object = categoryTaxonomyToJSONMap(c)
		} else if namespace, ok := ev.Object.(*datastore.Namespace); ok {
			out.Object = namespaceToJSONMap(namespace)
		}
	}
	return out
}

func namespaceToJSONMap(namespace *datastore.Namespace) map[string]any {
	data, err := json.Marshal(DatastoreNamespaceToGraphQL(namespace))
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return out
}

// resolvedFromJSONMap decodes the generic updateResourceStatus mutation's
// JSON-boxed `resolved` argument into the typed ResolvedCategoryTaxonomy
// shape, for the "CategoryTaxonomy" kind case. Round-trips through JSON
// since a map[string]any and a typed struct don't share a Go type.
func resolvedFromJSONMap(m map[string]any) *catalog.ResolvedCategoryTaxonomy {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var out catalog.ResolvedCategoryTaxonomy
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return &out
}

// categoryTaxonomyToJSONMap renders a CategoryTaxonomy as the JSON-boxed
// map[string]any WatchEvent.object needs. Reuses the same GraphQL model
// conversion as the strongly-typed path (toCategoryWatchEvent), then
// round-trips it through JSON so the generic path never hand-maintains a
// second, divergent field list.
func categoryTaxonomyToJSONMap(c *datastore.CategoryTaxonomy) map[string]any {
	cat := DatastoreCategoryTaxonomyToGraphQL(c)
	if cat == nil {
		return nil
	}
	b, err := json.Marshal(cat)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

// productEventMatchesFilters reports whether ev satisfies the namespace
// filter for a watchProducts subscription (spec 042, mirroring
// categoryEventMatchesFilters). A nil/empty namespace means no filter.
func productEventMatchesFilters(ev eventbus.Event, namespace *string) bool {
	if namespace == nil || *namespace == "" {
		return true
	}
	return ev.Namespace == *namespace
}

// productEventMatchesSelector reports whether ev's underlying Product's
// labels satisfy selector, mirroring categoryEventMatchesSelector.
func productEventMatchesSelector(ev eventbus.Event, selector *model.LabelSelectorInput) bool {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return true
	}
	p, ok := ev.Object.(*datastore.Product)
	if !ok || p == nil {
		return false
	}
	return matchesWatchSelector(selector, p.Labels)
}

// toProductWatchEvent maps an eventbus.Event to the strongly-typed
// ProductWatchEvent, mirroring toCategoryWatchEvent.
func toProductWatchEvent(ev eventbus.Event) *model.ProductWatchEvent {
	out := &model.ProductWatchEvent{
		Type:            toWatchEventType(ev.Type),
		Name:            ev.Name,
		ResourceVersion: ev.Cursor,
	}
	if ev.Namespace != "" {
		ns := ev.Namespace
		out.Namespace = &ns
	}
	if ev.Type != eventbus.Deleted {
		if p, ok := ev.Object.(*datastore.Product); ok {
			out.Product = DatastoreProductToGraphQL(p)
		}
	}
	return out
}
