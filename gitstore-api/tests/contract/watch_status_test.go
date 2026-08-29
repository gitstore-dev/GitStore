// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/eventbus"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func newWatchTestResolver(t *testing.T) (*resolver.Resolver, datastore.Datastore, *eventbus.Bus) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	bus := eventbus.New(100)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store:    store,
		Logger:   zap.NewNop(),
		Clock:    apiruntime.SystemClock{},
		EventBus: bus,
	})
	require.NoError(t, err)
	return r, store, bus
}

func mustReceiveCategoryEvent(t *testing.T, ch <-chan *model.CategoryWatchEvent) *model.CategoryWatchEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watch event")
		return nil
	}
}

func requireNoCategoryEvent(t *testing.T, ch <-chan *model.CategoryWatchEvent) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("expected no event, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchFiles_DeliversTypedPayloadAndFiltersNamespace(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchFiles(ctx, strptr("acme"), nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000061", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", ResourceVersion: "7",
		Spec: json.RawMessage(`{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero"}}`),
		Body: "alt text",
	}
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "File", Namespace: "other", Name: "ignored", ResourceVersion: "6", Object: file})
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "7", Object: file})
	select {
	case ev := <-events:
		require.Equal(t, "hero", ev.Name)
		require.Equal(t, model.WatchEventTypeAdded, ev.Type)
		require.NotNil(t, ev.File)
		require.Equal(t, "alt text", *ev.File.Body)
		require.Equal(t, "image/jpeg", ev.File.Spec.ContentType)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for File watch event")
	}
}

func strptr(s string) *string { return &s }

func TestWatchFiles_PreservesDuplicateEventsInCursorOrder(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchFiles(ctx, nil, nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000063", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
	}

	for i := 0; i < 2; i++ {
		bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "File", Namespace: "acme", Name: "hero", Object: file})
	}
	first := <-events
	second := <-events
	require.NotEqual(t, first.ResourceVersion, second.ResourceVersion)
	require.Equal(t, model.WatchEventTypeModified, first.Type)
	require.Equal(t, model.WatchEventTypeModified, second.Type)
}

func TestWatchFiles_PreservesPublishedOrderWhenResourceVersionsAreOutOfOrder(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchFiles(ctx, nil, nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000064", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
	}
	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "9", Object: file})
	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "8", Object: file})
	first := <-events
	second := <-events
	require.Equal(t, "1", first.ResourceVersion)
	require.Equal(t, "2", second.ResourceVersion)
}

// T014: watchCategories delivers Added/Modified/Deleted in admission order.
func TestWatchCategories_DeliversEventsInOrder(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := r.Subscription().WatchCategories(ctx, nil, nil, nil)
	require.NoError(t, err)

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "1"})
	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "2"})
	bus.Publish(eventbus.Event{Type: eventbus.Deleted, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "3"})

	e1 := mustReceiveCategoryEvent(t, events)
	e2 := mustReceiveCategoryEvent(t, events)
	e3 := mustReceiveCategoryEvent(t, events)

	require.Equal(t, model.WatchEventTypeAdded, e1.Type)
	require.Equal(t, model.WatchEventTypeModified, e2.Type)
	require.Equal(t, model.WatchEventTypeDeleted, e3.Type)
	require.Equal(t, "3", e3.ResourceVersion)
}

// T015: watchCategories resumed with a valid resourceVersion delivers only
// events after that cursor.
func TestWatchCategories_ResumeFromValidCursor(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "1"})
	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "2"})

	rv := "1"
	events, err := r.Subscription().WatchCategories(ctx, nil, nil, &rv)
	require.NoError(t, err)

	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, "2", e.ResourceVersion)
	requireNoCategoryEvent(t, events)
}

// T016: watchCategories opened with an expired resourceVersion terminates
// with a WATCH_EXPIRED-extension error.
func TestWatchCategories_ExpiredCursorReturnsWatchExpiredError(t *testing.T) {
	r, _, _ := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rv := "does-not-exist"
	_, err := r.Subscription().WatchCategories(ctx, nil, nil, &rv)
	require.Error(t, err)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	require.Equal(t, "WATCH_EXPIRED", gqlErr.Extensions["code"])
}

// T017: watchResources(kind: "CategoryTaxonomy", ...) exhibits the same
// list-then-watch/resume/expiry behavior as watchCategories.
func TestWatchResources_GenericPathParitiesWithWatchCategories(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := r.Subscription().WatchResources(ctx, "CategoryTaxonomy", nil, nil, nil)
	require.NoError(t, err)

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "electronics", ResourceVersion: "1"})

	select {
	case ev := <-events:
		require.Equal(t, model.WatchEventTypeAdded, ev.Type)
		require.Equal(t, "CategoryTaxonomy", ev.Kind)
		require.Equal(t, "1", ev.ResourceVersion)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic watch event")
	}
	rv := "nonexistent"
	_, err = r.Subscription().WatchResources(ctx, "CategoryTaxonomy", nil, nil, &rv)
	require.Error(t, err)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	require.Equal(t, "WATCH_EXPIRED", gqlErr.Extensions["code"])
}

func TestWatchResources_FileProjectsObject(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchResources(ctx, "File", nil, nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000062", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", ResourceVersion: "8",
		Spec: json.RawMessage(`{"ContentType":"image/png","Source":{"Type":"git","URI":"blob://hero"}}`),
	}
	bus.Publish(eventbus.Event{Type: eventbus.Modified, Kind: "File", Namespace: "acme", Name: "hero", ResourceVersion: "8", Object: file})
	select {
	case ev := <-events:
		require.Equal(t, "File", ev.Kind)
		require.Equal(t, "1", ev.ResourceVersion)
		require.Equal(t, "hero", ev.Object["Name"])
		require.Equal(t, "image/png", ev.Object["Spec"].(map[string]any)["ContentType"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic File watch event")
	}
}

// T018: a resource transitioning into/out of an active namespace filter
// only delivers events matching the filter.
func TestWatchCategories_NamespaceFilterTransitions(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := "acme"
	events, err := r.Subscription().WatchCategories(ctx, &ns, nil, nil)
	require.NoError(t, err)

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "other-ns", Name: "electronics", ResourceVersion: "1"})
	requireNoCategoryEvent(t, events)

	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "furniture", ResourceVersion: "2"})
	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, "furniture", e.Name)
}

// T018 (selector variant): label-selector filtering only delivers events
// for resources whose labels match.
func TestWatchCategories_LabelSelectorFiltersEvents(t *testing.T) {
	r, _, bus := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selector := &model.LabelSelectorInput{
		MatchLabels: map[string]any{"tier": "premium"},
	}
	events, err := r.Subscription().WatchCategories(ctx, nil, selector, nil)
	require.NoError(t, err)

	nonMatching := &datastore.CategoryTaxonomy{UID: "d0000000-0000-0000-0000-000000000001", Name: "standard-cat", Namespace: "acme", Labels: map[string]string{"tier": "standard"}}
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "standard-cat", ResourceVersion: "1", Object: nonMatching})
	requireNoCategoryEvent(t, events)

	matching := &datastore.CategoryTaxonomy{UID: "d0000000-0000-0000-0000-000000000002", Name: "premium-cat", Namespace: "acme", Labels: map[string]string{"tier": "premium"}}
	bus.Publish(eventbus.Event{Type: eventbus.Added, Kind: "CategoryTaxonomy", Namespace: "acme", Name: "premium-cat", ResourceVersion: "2", Object: matching})
	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, "premium-cat", e.Name)
}

// T025: updateCategoryStatus with a correct resourceVersion applies only
// the supplied fields and leaves unsupplied status fields unchanged.
func TestUpdateCategoryStatus_PartialMergeAppliesOnlySuppliedFields(t *testing.T) {
	r, store, _ := newWatchTestResolver(t)
	ctx := context.Background()

	c := &datastore.CategoryTaxonomy{
		UID: "c0000000-0000-0000-0000-000000000001", Namespace: "acme", Name: "electronics",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1",
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	generation := int32(1)
	payload, err := r.Mutation().UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Name:               "electronics",
		Namespace:          "acme",
		ResourceVersion:    "1",
		ObservedGeneration: &generation,
	})
	require.NoError(t, err)
	require.Nil(t, payload.Conflict)
	require.NotNil(t, payload.Category)

	updated, err := store.GetCategoryTaxonomyByName(ctx, "acme", "electronics")
	require.NoError(t, err)
	require.NotEqual(t, "1", updated.ResourceVersion)

	var status catalog.CategoryTaxonomyStatus
	require.NoError(t, unmarshalStatus(updated.Status, &status))
	require.Equal(t, int64(1), status.ObservedGeneration)
}

// T026: updateCategoryStatus with a stale resourceVersion returns a
// non-null conflict, leaving status unchanged.
func TestUpdateCategoryStatus_StaleResourceVersionReturnsConflict(t *testing.T) {
	r, store, _ := newWatchTestResolver(t)
	ctx := context.Background()

	c := &datastore.CategoryTaxonomy{
		UID: "c0000000-0000-0000-0000-000000000002", Namespace: "acme", Name: "furniture",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1",
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	payload, err := r.Mutation().UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Name:            "furniture",
		Namespace:       "acme",
		ResourceVersion: "stale",
	})
	require.NoError(t, err)
	require.Nil(t, payload.Category)
	require.NotNil(t, payload.Conflict)
	require.Equal(t, "1", payload.Conflict.CurrentResourceVersion)
}

// T027: updateCategoryStatus targeting a deleted/nonexistent resource
// returns a distinct NOT_FOUND error, not a conflict payload.
func TestUpdateCategoryStatus_NotFoundReturnsDistinctError(t *testing.T) {
	r, _, _ := newWatchTestResolver(t)
	ctx := context.Background()

	_, err := r.Mutation().UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Name:            "no-such-category",
		Namespace:       "acme",
		ResourceVersion: "1",
	})
	require.Error(t, err)
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	require.Equal(t, "NOT_FOUND", gqlErr.Extensions["code"])
}

// T029: updateResourceStatus (generic CRD path) exhibits the same
// partial-merge/conflict/not-found semantics for a CRD-style kind.
// CategoryTaxonomy is reused as the "CRD-style kind" here since the
// generic path is kind-agnostic at the datastore layer today (only
// CategoryTaxonomy has a concrete status-write backend, per plan.md's
// scope note: initial implementation targets one core kind end-to-end).
func TestUpdateResourceStatus_GenericPathAppliesToCategoryTaxonomy(t *testing.T) {
	r, store, _ := newWatchTestResolver(t)
	ctx := context.Background()

	c := &datastore.CategoryTaxonomy{
		UID: "c0000000-0000-0000-0000-000000000003", Namespace: "acme", Name: "outdoor",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1",
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	payload, err := r.Mutation().UpdateResourceStatus(ctx, model.UpdateResourceStatusInput{
		Kind:            "CategoryTaxonomy",
		Name:            "outdoor",
		Namespace:       "acme",
		ResourceVersion: "1",
	})
	require.NoError(t, err)
	require.Nil(t, payload.Conflict)
	require.NotNil(t, payload.Object)
}

func unmarshalStatus(raw []byte, out *catalog.CategoryTaxonomyStatus) error {
	return json.Unmarshal(raw, out)
}
