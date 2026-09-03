// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

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

func (r *Resolver) publishCategoryTaxonomyDeletedEvent(c *datastore.CategoryTaxonomy) {
	if r.eventBus == nil || c == nil {
		return
	}
	r.eventBus.Publish(eventbus.Event{
		Type:            eventbus.Deleted,
		Kind:            "CategoryTaxonomy",
		Namespace:       c.Namespace,
		Name:            c.Name,
		ResourceVersion: c.ResourceVersion,
		Object:          c,
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
	case eventbus.Bookmark:
		return model.WatchEventTypeBookmark
	default:
		return model.WatchEventTypeBookmark
	}
}

// NamespaceJournalEventToGraphQL maps the durable event envelope without
// weakening the shipped Namespace resource contract.
func NamespaceJournalEventToGraphQL(event datastore.NamespaceWatchEvent, namespace *datastore.Namespace) (*model.NamespaceWatchEvent, error) {
	if namespace == nil && (event.Type == datastore.NamespaceWatchAdded || event.Type == datastore.NamespaceWatchModified) {
		var err error
		namespace, err = namespaceFromJournalEvent(event)
		if err != nil {
			return nil, err
		}
	}
	out := &model.NamespaceWatchEvent{
		Type:            model.WatchEventType(event.Type),
		Name:            event.Name,
		ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence),
	}
	if event.Type == datastore.NamespaceWatchAdded || event.Type == datastore.NamespaceWatchModified {
		out.Namespace = DatastoreNamespaceToGraphQL(namespace)
	}
	return out, nil
}

func namespaceFromJournalEvent(event datastore.NamespaceWatchEvent) (*datastore.Namespace, error) {
	if event.Type == datastore.NamespaceWatchDeleted || event.Type == datastore.NamespaceWatchBookmark {
		return nil, nil
	}
	if len(event.Payload) == 0 {
		return nil, gqlerror.Errorf("Namespace journal data event has no payload")
	}
	namespace := &datastore.Namespace{}
	if err := json.Unmarshal(event.Payload, namespace); err != nil {
		return nil, gqlerror.Errorf("decode Namespace journal payload: %v", err)
	}
	return namespace, nil
}

func namespaceJournalEventToGeneric(event datastore.NamespaceWatchEvent) (*model.WatchEvent, *datastore.Namespace, error) {
	typed, err := NamespaceJournalEventToGraphQL(event, nil)
	if err != nil {
		return nil, nil, err
	}
	namespace, err := namespaceFromJournalEvent(event)
	if err != nil {
		return nil, nil, err
	}
	out := &model.WatchEvent{
		Type: typed.Type, Kind: "Namespace", Name: typed.Name,
		ResourceVersion: typed.ResourceVersion,
	}
	if namespace != nil {
		out.Object = namespaceToJSONMap(namespace)
	}
	return out, namespace, nil
}

// projectNamespaceJournalEventForSelector applies Kubernetes-style selector
// transition semantics. A MODIFIED event becomes ADDED when the resource
// enters the selector and DELETED when it leaves, preventing filtered caches
// from retaining objects that no longer match.
func projectNamespaceJournalEventForSelector(event datastore.NamespaceWatchEvent, selector *model.LabelSelectorInput) (datastore.NamespaceWatchEvent, bool) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return event, true
	}
	if event.Type == datastore.NamespaceWatchBookmark {
		return event, true
	}
	currentLabels := event.SelectorLabels
	if currentLabels == nil && len(event.Payload) > 0 {
		if namespace, err := namespaceFromJournalEvent(event); err == nil && namespace != nil {
			currentLabels = namespace.Labels
		}
	}
	currentMatches := matchesWatchSelector(selector, currentLabels)
	if event.Type != datastore.NamespaceWatchModified {
		return event, currentMatches
	}

	previousMatches := matchesWatchSelector(selector, event.PreviousSelectorLabels)
	switch {
	case previousMatches && currentMatches:
		return event, true
	case !previousMatches && currentMatches:
		event.Type = datastore.NamespaceWatchAdded
		return event, true
	case previousMatches && !currentMatches:
		event.Type = datastore.NamespaceWatchDeleted
		event.Payload = nil
		event.SelectorLabels = event.PreviousSelectorLabels
		return event, true
	default:
		return event, false
	}
}

func namespaceWatchGraphQLError(err error) *gqlerror.Error {
	terminal, ok := watchjournal.AsTerminal(err)
	if !ok {
		return gqlerror.Errorf("namespace watch failed")
	}
	return &gqlerror.Error{
		Message:    terminal.Error(),
		Extensions: map[string]any{"code": terminal.Code, "reason": string(terminal.Reason)},
	}
}

func addNamespaceWatchSubscriptionError(ctx context.Context, err error) {
	var gqlErr *gqlerror.Error
	if !errors.As(err, &gqlErr) {
		gqlErr = gqlerror.Errorf("namespace watch failed")
	}
	transport.AddSubscriptionError(ctx, gqlErr)
}

func sendNamespaceWatchOutput[T any](ctx context.Context, out chan<- T, value T, timeout time.Duration, metrics *watchjournal.Metrics) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case out <- value:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if metrics != nil {
			metrics.IncOverflow()
			metrics.IncExpiry(watchjournal.ReasonSubscriberOverflow)
		}
		return &watchjournal.TerminalError{
			Code: watchjournal.CodeExpired, Reason: watchjournal.ReasonSubscriberOverflow,
		}
	}
}

func (r *Resolver) watchNamespaceResources(ctx context.Context, selector *model.LabelSelectorInput, resourceVersion *string) (<-chan *model.WatchEvent, error) {
	if r.namespaceSubscriber == nil || !r.namespaceWatch.ReadersEnabled {
		return nil, namespaceWatchGraphQLError(&watchjournal.TerminalError{Code: watchjournal.CodeUnavailable, Reason: "MATERIALIZER_NOT_READY"})
	}
	rawCursor := ""
	if resourceVersion != nil {
		rawCursor = *resourceVersion
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := r.namespaceSubscriber.SubscribePath(streamCtx, rawCursor, "generic")
	if err != nil {
		cancel()
		return nil, namespaceWatchGraphQLError(err)
	}
	out := make(chan *model.WatchEvent, r.namespaceWatch.SubscriberBuffer)
	go func() {
		defer cancel()
		defer close(out)
		events := stream.Events
		errorsOut := stream.Errors
		for events != nil || errorsOut != nil {
			select {
			case <-ctx.Done():
				return
			case streamErr, ok := <-errorsOut:
				if !ok {
					errorsOut = nil
					continue
				}
				if streamErr != nil {
					addNamespaceWatchSubscriptionError(ctx, namespaceWatchGraphQLError(streamErr))
					return
				}
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				projected, matches := projectNamespaceJournalEventForSelector(event, selector)
				if !matches {
					continue
				}
				converted, _, convertErr := namespaceJournalEventToGeneric(projected)
				if convertErr != nil {
					addNamespaceWatchSubscriptionError(ctx, convertErr)
					return
				}
				if sendErr := sendNamespaceWatchOutput(streamCtx, out, converted, time.Duration(r.namespaceWatch.SubscriberBackpressureMillis)*time.Millisecond, r.namespaceMetrics); sendErr != nil {
					if streamCtx.Err() == nil {
						addNamespaceWatchSubscriptionError(streamCtx, namespaceWatchGraphQLError(sendErr))
					}
					return
				}
			}
		}
	}()
	return out, nil
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

func fileEventMatchesSelector(ev eventbus.Event, selector *model.LabelSelectorInput) bool {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return true
	}
	file, ok := ev.Object.(*datastore.File)
	return ok && file != nil && matchesWatchSelector(selector, file.Labels)
}

func toFileWatchEvent(ev eventbus.Event) *model.FileWatchEvent {
	out := &model.FileWatchEvent{
		Type:            toWatchEventType(ev.Type),
		Name:            ev.Name,
		ResourceVersion: ev.Cursor,
	}
	if ev.Namespace != "" {
		ns := ev.Namespace
		out.Namespace = &ns
	}
	if ev.Type != eventbus.Deleted {
		if file, ok := ev.Object.(*datastore.File); ok {
			out.File = DatastoreFileToGraphQL(file)
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
		} else if file, ok := ev.Object.(*datastore.File); ok {
			out.Object = fileToJSONMap(file)
		}
	}

	return out
}

func fileResolvedFromJSONMap(m map[string]any) *catalog.ResolvedFileDefinition {
	if m == nil {
		return nil
	}
	raw, _ := json.Marshal(m)
	var out catalog.ResolvedFileDefinition
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return &out
}

func fileToJSONMap(file *datastore.File) map[string]any {
	raw, err := json.Marshal(file)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
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
