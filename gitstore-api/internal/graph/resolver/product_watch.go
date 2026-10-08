// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func productFromJournalEvent(event datastore.ResourceWatchEvent) (*datastore.Product, error) {
	if event.Type == datastore.ResourceWatchDeleted || event.Type == datastore.ResourceWatchBookmark {
		return nil, nil
	}
	if len(event.Payload) == 0 {
		return nil, gqlerror.Errorf("Product journal data event has no payload")
	}
	product := &datastore.Product{}
	if err := json.Unmarshal(event.Payload, product); err != nil {
		return nil, gqlerror.Errorf("decode Product journal payload: %v", err)
	}
	return product, nil
}

func productJournalEventMatches(event datastore.ResourceWatchEvent, namespace *string) bool {
	return event.Type == datastore.ResourceWatchBookmark || (event.Kind == "Product" && (namespace == nil || *namespace == "" || event.Namespace == *namespace))
}

func projectProductJournalEvent(event datastore.ResourceWatchEvent, selector *model.LabelSelectorInput) (datastore.ResourceWatchEvent, bool) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) || event.Type == datastore.ResourceWatchBookmark {
		return event, true
	}
	labels := event.SelectorLabels
	if labels == nil && len(event.Payload) > 0 {
		if product, err := productFromJournalEvent(event); err == nil && product != nil {
			labels = product.Labels
		}
	}
	current := matchesWatchSelector(selector, labels)
	if event.Type != datastore.ResourceWatchModified {
		return event, current
	}
	previous := matchesWatchSelector(selector, event.PreviousSelectorLabels)
	if previous && !current {
		event.Type, event.Payload = datastore.ResourceWatchDeleted, nil
		return event, true
	}
	if !previous && current {
		event.Type = datastore.ResourceWatchAdded
	}
	return event, previous || current
}

func productJournalEventToGraphQL(event datastore.ResourceWatchEvent) (*model.ProductWatchEvent, error) {
	product, err := productFromJournalEvent(event)
	if err != nil {
		return nil, err
	}
	out := &model.ProductWatchEvent{Type: model.WatchEventType(event.Type), Namespace: &event.Namespace, Name: event.Name, ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence)}
	if event.Type == datastore.ResourceWatchAdded || event.Type == datastore.ResourceWatchModified {
		out.Product = DatastoreProductToGraphQL(product)
	}
	return out, nil
}

func productToJSONMap(product *datastore.Product) map[string]any {
	converted := DatastoreProductToGraphQL(product)
	if converted == nil {
		return nil
	}
	encoded, err := json.Marshal(converted)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(encoded, &out) != nil {
		return nil
	}
	return out
}

func (r *Resolver) watchProductResources(ctx context.Context, namespace *string, selector *model.LabelSelectorInput, resourceVersion *string) (<-chan *model.ProductWatchEvent, error) {
	if err := r.repositoryWatchAvailable(); err != nil {
		return nil, err
	}
	cursor := ""
	if resourceVersion != nil {
		cursor = normalizeResourceWatchCursor(*resourceVersion)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := r.namespaceSubscriber.SubscribePath(streamCtx, cursor, "typed")
	if err != nil {
		cancel()
		return nil, repositoryWatchGraphQLError(err)
	}
	out := make(chan *model.ProductWatchEvent, r.namespaceWatch.Subscriber.Buffer)
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
					addRepositoryWatchSubscriptionError(ctx, repositoryWatchGraphQLError(err))
					return
				}
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if !productJournalEventMatches(event, namespace) {
					continue
				}
				projected, matches := projectProductJournalEvent(event, selector)
				if !matches {
					continue
				}
				converted, err := productJournalEventToGraphQL(projected)
				if err != nil {
					addRepositoryWatchSubscriptionError(ctx, err)
					return
				}
				if err := sendNamespaceWatchOutput(streamCtx, out, converted, r.namespaceWatch.Subscriber.Backpressure, r.namespaceMetrics); err != nil {
					if streamCtx.Err() == nil {
						addRepositoryWatchSubscriptionError(ctx, repositoryWatchGraphQLError(err))
					}
					return
				}
			}
		}
	}()
	return out, nil
}

func (r *Resolver) watchProductGenericResources(ctx context.Context, namespace *string, selector *model.LabelSelectorInput, resourceVersion *string) (<-chan *model.WatchEvent, error) {
	if err := r.repositoryWatchAvailable(); err != nil {
		return nil, err
	}
	cursor := ""
	if resourceVersion != nil {
		cursor = normalizeResourceWatchCursor(*resourceVersion)
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := r.namespaceSubscriber.SubscribePath(streamCtx, cursor, "generic")
	if err != nil {
		cancel()
		return nil, repositoryWatchGraphQLError(err)
	}
	out := make(chan *model.WatchEvent, r.namespaceWatch.Subscriber.Buffer)
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
					addRepositoryWatchSubscriptionError(ctx, repositoryWatchGraphQLError(err))
					return
				}
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if !productJournalEventMatches(event, namespace) {
					continue
				}
				projected, matches := projectProductJournalEvent(event, selector)
				if !matches {
					continue
				}
				typed, err := productJournalEventToGraphQL(projected)
				if err != nil {
					addRepositoryWatchSubscriptionError(ctx, err)
					return
				}
				generic := &model.WatchEvent{Type: typed.Type, Kind: "Product", Name: typed.Name, ResourceVersion: typed.ResourceVersion, Namespace: typed.Namespace}
				if product, err := productFromJournalEvent(projected); err == nil && product != nil {
					generic.Object = productToJSONMap(product)
				} else if err != nil {
					addRepositoryWatchSubscriptionError(ctx, err)
					return
				}
				if err := sendNamespaceWatchOutput(streamCtx, out, generic, r.namespaceWatch.Subscriber.Backpressure, r.namespaceMetrics); err != nil {
					if streamCtx.Err() == nil {
						addRepositoryWatchSubscriptionError(ctx, repositoryWatchGraphQLError(err))
					}
					return
				}
			}
		}
	}()
	return out, nil
}
