// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// RepositoryJournalEventToGraphQL projects one generic durable event onto the
// typed Repository contract. Repository payloads cross the public boundary
// through the ordinary converter, preserving encoded Relay node IDs.
func RepositoryJournalEventToGraphQL(event datastore.ResourceWatchEvent, repository *datastore.Repository) (*model.RepositoryWatchEvent, error) {
	if repository == nil && (event.Type == datastore.ResourceWatchAdded || event.Type == datastore.ResourceWatchModified) {
		var err error
		repository, err = repositoryFromJournalEvent(event)
		if err != nil {
			return nil, err
		}
	}
	out := &model.RepositoryWatchEvent{
		Type:            model.WatchEventType(event.Type),
		Namespace:       event.Namespace,
		Name:            event.Name,
		ResourceVersion: watchjournal.EncodeCursor(event.Epoch, event.Sequence),
	}
	if event.Type == datastore.ResourceWatchAdded || event.Type == datastore.ResourceWatchModified {
		out.Repository = DatastoreRepositoryToGraphQL(repository)
	}
	return out, nil
}

func repositoryFromJournalEvent(event datastore.ResourceWatchEvent) (*datastore.Repository, error) {
	if event.Type == datastore.ResourceWatchDeleted || event.Type == datastore.ResourceWatchBookmark {
		return nil, nil
	}
	if len(event.Payload) == 0 {
		return nil, gqlerror.Errorf("Repository journal data event has no payload")
	}
	repository := &datastore.Repository{}
	if err := json.Unmarshal(event.Payload, repository); err != nil {
		return nil, gqlerror.Errorf("decode Repository journal payload: %v", err)
	}
	return repository, nil
}

func repositoryJournalEventToGeneric(event datastore.ResourceWatchEvent) (*model.WatchEvent, *datastore.Repository, error) {
	typed, err := RepositoryJournalEventToGraphQL(event, nil)
	if err != nil {
		return nil, nil, err
	}
	repository, err := repositoryFromJournalEvent(event)
	if err != nil {
		return nil, nil, err
	}
	out := &model.WatchEvent{Type: typed.Type, Kind: "Repository", Name: typed.Name, ResourceVersion: typed.ResourceVersion}
	if event.Namespace != "" {
		namespace := event.Namespace
		out.Namespace = &namespace
	}
	if repository != nil {
		out.Object = repositoryToJSONMap(repository)
	}
	return out, repository, nil
}

// projectRepositoryJournalEvent applies selector-transition semantics before
// typed or generic output is built.
func projectRepositoryJournalEvent(event datastore.ResourceWatchEvent, selector *model.LabelSelectorInput) (datastore.ResourceWatchEvent, bool) {
	if selector == nil || (len(selector.MatchLabels) == 0 && len(selector.MatchExpressions) == 0) {
		return event, true
	}
	if event.Type == datastore.ResourceWatchBookmark {
		return event, true
	}
	currentLabels := event.SelectorLabels
	if currentLabels == nil && len(event.Payload) > 0 {
		if repository, err := repositoryFromJournalEvent(event); err == nil && repository != nil {
			currentLabels = repository.Labels
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
	case previousMatches && !currentMatches:
		event.Type = datastore.ResourceWatchDeleted
		event.Payload = nil
		event.SelectorLabels = event.PreviousSelectorLabels
		return event, true
	default:
		return event, false
	}
}

func repositoryJournalEventMatchesKind(event datastore.ResourceWatchEvent) bool {
	return event.Type == datastore.ResourceWatchBookmark || event.Kind == "Repository"
}

func repositoryJournalEventMatchesNamespace(event datastore.ResourceWatchEvent, namespace *string) bool {
	return event.Type == datastore.ResourceWatchBookmark || namespace == nil || *namespace == "" || event.Namespace == *namespace
}

func repositoryWatchGraphQLError(err error) *gqlerror.Error {
	terminal, ok := watchjournal.AsTerminal(err)
	if !ok {
		return gqlerror.Errorf("repository watch failed")
	}
	return &gqlerror.Error{Message: terminal.Error(), Extensions: map[string]any{"code": terminal.Code, "reason": string(terminal.Reason)}}
}

func addRepositoryWatchSubscriptionError(ctx context.Context, err error) {
	var gqlErr *gqlerror.Error
	if !errors.As(err, &gqlErr) {
		gqlErr = gqlerror.Errorf("repository watch failed")
	}
	transport.AddSubscriptionError(ctx, gqlErr)
}

func (r *Resolver) repositoryWatchAvailable() error {
	if r.namespaceSubscriber == nil || !r.namespaceWatch.ReadersEnabled {
		return repositoryWatchGraphQLError(&watchjournal.TerminalError{Code: watchjournal.CodeUnavailable, Reason: "MATERIALIZER_NOT_READY"})
	}
	return nil
}

// watchRepositoryResources is the generic Repository projection of the shared
// durable journal. It deliberately does not subscribe to the event bus: an
// eventbus cursor cannot provide replay or multi-replica continuity.
func (r *Resolver) watchRepositoryResources(ctx context.Context, namespace *string, selector *model.LabelSelectorInput, resourceVersion *string) (<-chan *model.WatchEvent, error) {
	if err := r.repositoryWatchAvailable(); err != nil {
		return nil, err
	}
	rawCursor := ""
	if resourceVersion != nil {
		rawCursor = *resourceVersion
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := r.namespaceSubscriber.SubscribePath(streamCtx, rawCursor, "generic")
	if err != nil {
		cancel()
		return nil, repositoryWatchGraphQLError(err)
	}
	out := make(chan *model.WatchEvent, r.namespaceWatch.SubscriberBuffer)
	go func() {
		defer cancel()
		defer close(out)
		for events, errorsOut := stream.Events, stream.Errors; events != nil || errorsOut != nil; {
			select {
			case <-ctx.Done():
				return
			case streamErr, ok := <-errorsOut:
				if !ok {
					errorsOut = nil
					continue
				}
				if streamErr != nil {
					addRepositoryWatchSubscriptionError(ctx, repositoryWatchGraphQLError(streamErr))
					return
				}
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if !repositoryJournalEventMatchesKind(event) || !repositoryJournalEventMatchesNamespace(event, namespace) {
					continue
				}
				projected, matches := projectRepositoryJournalEvent(event, selector)
				if !matches {
					continue
				}
				converted, _, convertErr := repositoryJournalEventToGeneric(projected)
				if convertErr != nil {
					addRepositoryWatchSubscriptionError(ctx, convertErr)
					return
				}
				if sendErr := sendNamespaceWatchOutput(streamCtx, out, converted, time.Duration(r.namespaceWatch.SubscriberBackpressureMillis)*time.Millisecond, r.namespaceMetrics); sendErr != nil {
					if streamCtx.Err() == nil {
						addRepositoryWatchSubscriptionError(streamCtx, repositoryWatchGraphQLError(sendErr))
					}
					return
				}
			}
		}
	}()
	return out, nil
}
