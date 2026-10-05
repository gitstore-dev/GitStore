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
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

const (
	namespaceWatchBootstrapCursor  = "__namespace_watch_bootstrap__"
	repositoryWatchBootstrapCursor = "__repository_watch_bootstrap__"
	productWatchBootstrapCursor    = "__product_watch_bootstrap__"
	fileWatchBootstrapCursor       = "__file_watch_bootstrap__"
	categoryWatchBootstrapCursor   = "__category_watch_bootstrap__"
)

// normalizeResourceWatchCursor preserves the private typed-watch bootstrap
// sentinels while resource projections share one generic
// durable journal. Ordinary opaque cursors are returned unchanged.
func normalizeResourceWatchCursor(raw string) string {
	switch raw {
	case namespaceWatchBootstrapCursor, repositoryWatchBootstrapCursor, productWatchBootstrapCursor, fileWatchBootstrapCursor, categoryWatchBootstrapCursor:
		return watchjournal.BootstrapCursor
	default:
		return raw
	}
}

func fileFromJournalEvent(event datastore.ResourceWatchEvent) (*datastore.File, error) {
	if event.Type == datastore.ResourceWatchDeleted || event.Type == datastore.ResourceWatchBookmark {
		return nil, nil
	}
	if len(event.Payload) == 0 {
		return nil, gqlerror.Errorf("File journal data event has no payload")
	}
	var file datastore.File
	if err := json.Unmarshal(event.Payload, &file); err != nil {
		return nil, gqlerror.Errorf("decode File journal payload")
	}
	return &file, nil
}

func fileJournalEventToGraphQL(event datastore.ResourceWatchEvent) (*model.FileWatchEvent, error) {
	file, err := fileFromJournalEvent(event)
	if err != nil {
		return nil, err
	}
	return &model.FileWatchEvent{
		Type: model.WatchEventType(event.Type), Namespace: &event.Namespace, Name: event.Name,
		ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence),
		File:            DatastoreFileToGraphQL(file),
	}, nil
}

func fileJournalEventToGeneric(event datastore.ResourceWatchEvent) (*model.WatchEvent, error) {
	file, err := fileFromJournalEvent(event)
	if err != nil {
		return nil, err
	}
	out := &model.WatchEvent{
		Type: model.WatchEventType(event.Type), Kind: "File", Namespace: &event.Namespace,
		Name: event.Name, ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence),
	}
	if file != nil {
		raw, err := json.Marshal(DatastoreFileToGraphQL(file))
		if err != nil {
			return nil, gqlerror.Errorf("encode File watch projection")
		}
		if err := json.Unmarshal(raw, &out.Object); err != nil {
			return nil, gqlerror.Errorf("decode File watch projection")
		}
	}
	return out, nil
}

func categoryFromJournalEvent(event datastore.ResourceWatchEvent) (*datastore.CategoryTaxonomy, error) {
	if event.Type == datastore.ResourceWatchDeleted || event.Type == datastore.ResourceWatchBookmark {
		return nil, nil
	}
	if len(event.Payload) == 0 {
		return nil, gqlerror.Errorf("CategoryTaxonomy journal data event has no payload")
	}
	var category datastore.CategoryTaxonomy
	if err := json.Unmarshal(event.Payload, &category); err != nil {
		return nil, gqlerror.Errorf("decode CategoryTaxonomy journal payload")
	}
	return &category, nil
}

func categoryJournalEventToGraphQL(event datastore.ResourceWatchEvent) (*model.CategoryWatchEvent, error) {
	category, err := categoryFromJournalEvent(event)
	if err != nil {
		return nil, err
	}
	return &model.CategoryWatchEvent{
		Type: model.WatchEventType(event.Type), Namespace: &event.Namespace, Name: event.Name,
		ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence),
		Category:        DatastoreCategoryTaxonomyToGraphQL(category),
	}, nil
}

func categoryJournalEventToGeneric(event datastore.ResourceWatchEvent) (*model.WatchEvent, error) {
	category, err := categoryFromJournalEvent(event)
	if err != nil {
		return nil, err
	}
	out := &model.WatchEvent{
		Type: model.WatchEventType(event.Type), Kind: "CategoryTaxonomy", Namespace: &event.Namespace,
		Name: event.Name, ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence),
	}
	if category != nil {
		out.Object = categoryTaxonomyToJSONMap(category)
		if out.Object == nil {
			return nil, gqlerror.Errorf("encode CategoryTaxonomy watch projection")
		}
	}
	return out, nil
}

// watchCatalogJournal serves the typed and generic projections of one
// namespaced catalog kind from the shared durable envelope and bounded
// subscriber; no projection can fall back to a process-local cursor.
func watchCatalogJournal[T any](ctx context.Context, r *Resolver, kind string, namespace *string, selector *model.LabelSelectorInput, resourceVersion *string, path string, convert func(datastore.ResourceWatchEvent) (T, error)) (<-chan T, error) {
	if err := r.repositoryWatchAvailable(); err != nil {
		return nil, err
	}
	cursor := ""
	if resourceVersion != nil {
		cursor = normalizeResourceWatchCursor(*resourceVersion)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := r.namespaceSubscriber.SubscribePath(streamCtx, cursor, path)
	if err != nil {
		cancel()
		return nil, namespaceWatchGraphQLError(err)
	}
	out := make(chan T, r.namespaceWatch.SubscriberBuffer)
	go func() {
		defer cancel()
		defer close(out)
		for events, errs := stream.Events, stream.Errors; events != nil || errs != nil; {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errs:
				if !ok {
					errs = nil
					continue
				}
				if err != nil {
					addNamespaceWatchSubscriptionError(ctx, namespaceWatchGraphQLError(err))
					return
				}
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if event.Type != datastore.ResourceWatchBookmark &&
					(event.Kind != kind || !repositoryJournalEventMatchesNamespace(event, namespace)) {
					continue
				}
				// Selector transitions depend only on envelope labels, independent
				// of resource kind; the shared projector also preserves tombstones.
				projected, matches := projectResourceJournalSelector(event, selector)
				if !matches {
					continue
				}
				value, err := convert(projected)
				if err != nil {
					addNamespaceWatchSubscriptionError(ctx, err)
					return
				}
				if err := sendNamespaceWatchOutput(streamCtx, out, value, time.Duration(r.namespaceWatch.SubscriberBackpressureMillis)*time.Millisecond, r.namespaceMetrics); err != nil {
					if streamCtx.Err() == nil {
						addNamespaceWatchSubscriptionError(ctx, namespaceWatchGraphQLError(err))
					}
					return
				}
			}
		}
	}()
	return out, nil
}

// NamespaceJournalEventToGraphQL maps the durable event envelope without
// weakening the shipped Namespace resource contract.
func NamespaceJournalEventToGraphQL(event datastore.ResourceWatchEvent, namespace *datastore.Namespace) (*model.NamespaceWatchEvent, error) {
	if namespace == nil && (event.Type == datastore.ResourceWatchAdded || event.Type == datastore.ResourceWatchModified) {
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

func namespaceFromJournalEvent(event datastore.ResourceWatchEvent) (*datastore.Namespace, error) {
	if event.Type == datastore.ResourceWatchDeleted || event.Type == datastore.ResourceWatchBookmark {
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

func namespaceJournalEventToGeneric(event datastore.ResourceWatchEvent) (*model.WatchEvent, *datastore.Namespace, error) {
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
func projectNamespaceJournalEventForSelector(event datastore.ResourceWatchEvent, selector *model.LabelSelectorInput) (datastore.ResourceWatchEvent, bool) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return event, true
	}
	if event.Type == datastore.ResourceWatchBookmark {
		return event, true
	}
	currentLabels := event.SelectorLabels
	if currentLabels == nil && len(event.Payload) > 0 {
		if namespace, err := namespaceFromJournalEvent(event); err == nil && namespace != nil {
			currentLabels = namespace.Labels
		}
	}
	currentMatches := matchesWatchSelector(selector, currentLabels)
	if event.Type != datastore.ResourceWatchModified {
		return event, currentMatches
	}

	previousMatches := matchesWatchSelector(selector, event.PreviousSelectorLabels)
	switch {
	case previousMatches && currentMatches:
		return event, true
	case !previousMatches && currentMatches:
		event.Type = datastore.ResourceWatchAdded
		return event, true
	case previousMatches:
		event.Type = datastore.ResourceWatchDeleted
		event.Payload = nil
		event.SelectorLabels = event.PreviousSelectorLabels
		return event, true
	default:
		return event, false
	}
}

// namespaceJournalEventMatchesKind retains cluster-wide bookmarks while
// projecting the shared durable journal onto the Namespace stream. Empty Kind
// remains accepted only for pre-generic-journal retained Namespace entries.
func namespaceJournalEventMatchesKind(event datastore.ResourceWatchEvent) bool {
	if event.Type == datastore.ResourceWatchBookmark {
		return true
	}
	return event.Kind == "" || event.Kind == "Namespace"
}

func namespaceWatchGraphQLError(err error) *gqlerror.Error {
	terminal, ok := watchjournal.AsTerminal(err)
	if !ok {
		return gqlerror.Errorf("durable resource watch failed")
	}
	return &gqlerror.Error{
		Message:    terminal.Error(),
		Extensions: map[string]any{"code": terminal.Code, "reason": string(terminal.Reason)},
	}
}

func addNamespaceWatchSubscriptionError(ctx context.Context, err error) {
	var gqlErr *gqlerror.Error
	if !errors.As(err, &gqlErr) {
		gqlErr = gqlerror.Errorf("durable resource watch failed")
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
		rawCursor = normalizeResourceWatchCursor(*resourceVersion)
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
				if !namespaceJournalEventMatchesKind(event) {
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

// repositoryToJSONMap deliberately passes through the Repository GraphQL
// converter. This keeps the generic watch payload on the same public identity
// boundary as typed Repository results: Relay id and metadata.uid are never
// replaced by the datastore's raw UID.
func repositoryToJSONMap(repository *datastore.Repository) map[string]any {
	data, err := json.Marshal(DatastoreRepositoryToGraphQL(repository))
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
// conversion as the strongly-typed path (categoryJournalEventToGraphQL), then
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
