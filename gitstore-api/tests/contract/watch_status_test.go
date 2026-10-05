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
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

// newWatchTestResolver serves CategoryTaxonomy watches from the memdb
// durable journal, so events come from real committed datastore writes.
func newWatchTestResolver(t *testing.T) (*resolver.Resolver, datastore.Datastore) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(t.Context(), t.Name(), time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(t.Context(), lease, datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark, At: time.Now()}, time.Hour)
	require.NoError(t, err)
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store: store, Logger: zap.NewNop(), Clock: apiruntime.SystemClock{},
		ResourceJournal: journal, NamespaceWatch: resourceContractWatchConfig(),
	})
	require.NoError(t, err)
	return r, store
}

func watchCategoryFixture(uid, namespace, name string, labels map[string]string) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID: uid, Namespace: namespace, Name: name, Labels: labels,
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now(),
	}
}

func newDurableWatchTestResolver(t *testing.T) (*resolver.Resolver, func(datastore.ResourceWatchEvent)) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	journal := store.(datastore.ResourceWatchCapable).ResourceWatchJournal()
	lease, acquired, err := journal.AcquireLease(t.Context(), t.Name(), time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	publish := func(event datastore.ResourceWatchEvent) {
		event.At = time.Now()
		_, err := journal.Append(t.Context(), lease, event, time.Hour)
		require.NoError(t, err)
	}
	publish(datastore.ResourceWatchEvent{Type: datastore.ResourceWatchBookmark})
	r, err := resolver.NewResolver(resolver.ResolverDeps{
		Store: store, Logger: zap.NewNop(), ResourceJournal: journal, NamespaceWatch: resourceContractWatchConfig(),
	})
	require.NoError(t, err)
	return r, publish
}

func mustReceiveCategoryEvent(t *testing.T, ch <-chan *model.CategoryWatchEvent) *model.CategoryWatchEvent {
	t.Helper()
	timeout := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == model.WatchEventTypeBookmark {
				continue
			}
			return ev
		case <-timeout:
			t.Fatal("timed out waiting for watch event")
			return nil
		}
	}
}

func requireNoCategoryEvent(t *testing.T, ch <-chan *model.CategoryWatchEvent) {
	t.Helper()
	timeout := time.After(50 * time.Millisecond)
	for {
		select {
		case ev := <-ch:
			if ev.Type != model.WatchEventTypeBookmark {
				t.Fatalf("expected no event, got %+v", ev)
			}
		case <-timeout:
			return
		}
	}
}

func TestWatchFiles_DeliversTypedPayloadAndFiltersNamespace(t *testing.T) {
	r, publish := newDurableWatchTestResolver(t)
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
	payload, err := json.Marshal(file)
	require.NoError(t, err)
	publish(datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "File", Namespace: "other", Name: "ignored", Payload: payload})
	publish(datastore.ResourceWatchEvent{Type: datastore.ResourceWatchAdded, Kind: "File", Namespace: "acme", Name: "hero", Payload: payload})
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
	r, publish := newDurableWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchFiles(ctx, nil, nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000063", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
	}

	payload, err := json.Marshal(file)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		publish(datastore.ResourceWatchEvent{Type: datastore.ResourceWatchModified, Kind: "File", Namespace: "acme", Name: "hero", Payload: payload})
	}
	first := <-events
	second := <-events
	require.NotEqual(t, first.ResourceVersion, second.ResourceVersion)
	require.Equal(t, model.WatchEventTypeModified, first.Type)
	require.Equal(t, model.WatchEventTypeModified, second.Type)
}

func TestWatchFiles_PreservesPublishedOrderWhenResourceVersionsAreOutOfOrder(t *testing.T) {
	r, publish := newDurableWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchFiles(ctx, nil, nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000064", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
	}
	for _, version := range []string{"9", "8"} {
		file.ResourceVersion = version
		payload, err := json.Marshal(file)
		require.NoError(t, err)
		publish(datastore.ResourceWatchEvent{Type: datastore.ResourceWatchModified, Kind: "File", Namespace: "acme", Name: "hero", Payload: payload})
	}
	first := <-events
	second := <-events
	require.NotEqual(t, first.ResourceVersion, second.ResourceVersion)
	require.Equal(t, "9", first.File.Metadata.ResourceVersion)
	require.Equal(t, "8", second.File.Metadata.ResourceVersion)
}

// T014: watchCategories delivers Added/Modified/Deleted in commit order.
func TestWatchCategories_DeliversEventsInOrder(t *testing.T) {
	r, store := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := r.Subscription().WatchCategories(ctx, nil, nil, nil)
	require.NoError(t, err)

	category := watchCategoryFixture("c1000000-0000-0000-0000-000000000001", "acme", "electronics", nil)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, category))
	category.ResourceVersion = "2"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, category))
	require.NoError(t, store.DeleteCategoryTaxonomy(ctx, category.UID))

	e1 := mustReceiveCategoryEvent(t, events)
	e2 := mustReceiveCategoryEvent(t, events)
	e3 := mustReceiveCategoryEvent(t, events)

	require.Equal(t, model.WatchEventTypeAdded, e1.Type)
	require.Equal(t, "electronics", e1.Category.Metadata.Name)
	require.Equal(t, model.WatchEventTypeModified, e2.Type)
	require.Equal(t, "2", e2.Category.Metadata.ResourceVersion)
	require.Equal(t, model.WatchEventTypeDeleted, e3.Type)
	require.Nil(t, e3.Category)
	require.NotEqual(t, e1.ResourceVersion, e2.ResourceVersion)
	require.NotEqual(t, e2.ResourceVersion, e3.ResourceVersion)
}

// T015: watchCategories resumed with a valid resourceVersion delivers only
// events after that cursor.
func TestWatchCategories_ResumeFromValidCursor(t *testing.T) {
	r, store := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first, err := r.Subscription().WatchCategories(ctx, nil, nil, nil)
	require.NoError(t, err)
	category := watchCategoryFixture("c1000000-0000-0000-0000-000000000002", "acme", "electronics", nil)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, category))
	added := mustReceiveCategoryEvent(t, first)
	category.ResourceVersion = "2"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, category))

	rv := added.ResourceVersion
	events, err := r.Subscription().WatchCategories(ctx, nil, nil, &rv)
	require.NoError(t, err)

	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, model.WatchEventTypeModified, e.Type)
	require.Equal(t, "2", e.Category.Metadata.ResourceVersion)
	requireNoCategoryEvent(t, events)
}

// T016: watchCategories opened with an unknown or legacy event-bus
// resourceVersion terminates with a WATCH_EXPIRED-extension error so the
// controller re-lists.
func TestWatchCategories_ExpiredCursorReturnsWatchExpiredError(t *testing.T) {
	r, _ := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, rv := range []string{"does-not-exist", "0", "17"} {
		_, err := r.Subscription().WatchCategories(ctx, nil, nil, &rv)
		require.Error(t, err)
		var gqlErr *gqlerror.Error
		require.True(t, errors.As(err, &gqlErr))
		require.Equal(t, "WATCH_EXPIRED", gqlErr.Extensions["code"], rv)
	}
}

// T017: watchResources(kind: "CategoryTaxonomy", ...) is a second projection
// of the same durable record as watchCategories.
func TestWatchResources_GenericPathParitiesWithWatchCategories(t *testing.T) {
	r, store := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	typed, err := r.Subscription().WatchCategories(ctx, nil, nil, nil)
	require.NoError(t, err)
	events, err := r.Subscription().WatchResources(ctx, "CategoryTaxonomy", nil, nil, nil)
	require.NoError(t, err)

	require.NoError(t, store.CreateCategoryTaxonomy(ctx, watchCategoryFixture("c1000000-0000-0000-0000-000000000003", "acme", "electronics", nil)))

	typedEvent := mustReceiveCategoryEvent(t, typed)
	timeout := time.After(time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == model.WatchEventTypeBookmark {
				continue
			}
			require.Equal(t, model.WatchEventTypeAdded, ev.Type)
			require.Equal(t, "CategoryTaxonomy", ev.Kind)
			require.Equal(t, typedEvent.ResourceVersion, ev.ResourceVersion)
			require.Equal(t, "electronics", ev.Object["metadata"].(map[string]any)["name"])
			rv := "nonexistent"
			_, err = r.Subscription().WatchResources(ctx, "CategoryTaxonomy", nil, nil, &rv)
			require.Error(t, err)
			var gqlErr *gqlerror.Error
			require.True(t, errors.As(err, &gqlErr))
			require.Equal(t, "WATCH_EXPIRED", gqlErr.Extensions["code"])
			return
		case <-timeout:
			t.Fatal("timed out waiting for generic watch event")
		}
	}
}

// Kinds without a durable journal source fail loudly instead of accepting a
// subscription that can never deliver an event.
func TestWatchResources_UnsupportedKindIsRejected(t *testing.T) {
	r, _ := newWatchTestResolver(t)
	for _, kind := range []string{"Collection", "ProductVariant", "Bogus"} {
		_, err := r.Subscription().WatchResources(t.Context(), kind, nil, nil, nil)
		require.Error(t, err)
		var gqlErr *gqlerror.Error
		require.True(t, errors.As(err, &gqlErr))
		require.Equal(t, "UNSUPPORTED_KIND", gqlErr.Extensions["code"], kind)
	}
}

func TestWatchResources_FileProjectsObject(t *testing.T) {
	r, publish := newDurableWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := r.Subscription().WatchResources(ctx, "File", nil, nil, nil)
	require.NoError(t, err)
	file := &datastore.File{
		UID: "00000000-0000-0000-0000-000000000062", Namespace: "acme", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File", ResourceVersion: "8",
		Spec: json.RawMessage(`{"ContentType":"image/png","Source":{"Type":"git","URI":"blob://hero"}}`),
	}
	payload, err := json.Marshal(file)
	require.NoError(t, err)
	publish(datastore.ResourceWatchEvent{Type: datastore.ResourceWatchModified, Kind: "File", Namespace: "acme", Name: "hero", Payload: payload})
	select {
	case ev := <-events:
		require.Equal(t, "File", ev.Kind)
		require.NotEmpty(t, ev.ResourceVersion)
		require.Equal(t, "hero", ev.Object["metadata"].(map[string]any)["name"])
		require.Equal(t, "image/png", ev.Object["spec"].(map[string]any)["contentType"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for generic File watch event")
	}
}

// T018: a resource transitioning into/out of an active namespace filter
// only delivers events matching the filter.
func TestWatchCategories_NamespaceFilterTransitions(t *testing.T) {
	r, store := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ns := "acme"
	events, err := r.Subscription().WatchCategories(ctx, &ns, nil, nil)
	require.NoError(t, err)

	require.NoError(t, store.CreateCategoryTaxonomy(ctx, watchCategoryFixture("c1000000-0000-0000-0000-000000000004", "other-ns", "electronics", nil)))
	requireNoCategoryEvent(t, events)

	require.NoError(t, store.CreateCategoryTaxonomy(ctx, watchCategoryFixture("c1000000-0000-0000-0000-000000000005", "acme", "furniture", nil)))
	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, "furniture", e.Name)
}

// T018 (selector variant): label-selector filtering only delivers events
// for resources whose labels match, and a label change out of the selector
// is delivered as a Deleted transition.
func TestWatchCategories_LabelSelectorFiltersEvents(t *testing.T) {
	r, store := newWatchTestResolver(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	selector := &model.LabelSelectorInput{
		MatchLabels: map[string]any{"tier": "premium"},
	}
	events, err := r.Subscription().WatchCategories(ctx, nil, selector, nil)
	require.NoError(t, err)

	require.NoError(t, store.CreateCategoryTaxonomy(ctx, watchCategoryFixture("d0000000-0000-0000-0000-000000000001", "acme", "standard-cat", map[string]string{"tier": "standard"})))
	requireNoCategoryEvent(t, events)

	matching := watchCategoryFixture("d0000000-0000-0000-0000-000000000002", "acme", "premium-cat", map[string]string{"tier": "premium"})
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, matching))
	e := mustReceiveCategoryEvent(t, events)
	require.Equal(t, "premium-cat", e.Name)

	matching.Labels = map[string]string{"tier": "standard"}
	matching.ResourceVersion = "2"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, matching))
	exit := mustReceiveCategoryEvent(t, events)
	require.Equal(t, model.WatchEventTypeDeleted, exit.Type)
	require.Equal(t, "premium-cat", exit.Name)
}

// T025: updateCategoryStatus with a correct resourceVersion applies only
// the supplied fields and leaves unsupplied status fields unchanged.
func TestUpdateCategoryStatus_PartialMergeAppliesOnlySuppliedFields(t *testing.T) {
	r, store := newWatchTestResolver(t)
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
	require.NotNil(t, payload.Category)

	updated, err := store.GetCategoryTaxonomyByName(ctx, "acme", "electronics")
	require.NoError(t, err)
	require.NotEqual(t, "1", updated.ResourceVersion)

	var status catalog.CategoryTaxonomyStatus
	require.NoError(t, unmarshalStatus(updated.Status, &status))
	require.Equal(t, int64(1), status.ObservedGeneration)
}

// T026: updateCategoryStatus with a stale resourceVersion returns a
// RESOURCE_VERSION_CONFLICT GraphQL error, leaving status unchanged.
func TestUpdateCategoryStatus_StaleResourceVersionReturnsGraphQLError(t *testing.T) {
	r, store := newWatchTestResolver(t)
	ctx := context.Background()

	c := &datastore.CategoryTaxonomy{
		UID: "c0000000-0000-0000-0000-000000000002", Namespace: "acme", Name: "furniture",
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		Generation: 1, ResourceVersion: "1",
	}
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	_, err := r.Mutation().UpdateCategoryStatus(ctx, model.UpdateCategoryStatusInput{
		Name:            "furniture",
		Namespace:       "acme",
		ResourceVersion: "stale",
	})
	var gqlErr *gqlerror.Error
	require.True(t, errors.As(err, &gqlErr))
	require.Equal(t, "RESOURCE_VERSION_CONFLICT", gqlErr.Extensions["code"])
	require.Equal(t, "1", gqlErr.Extensions["resourceVersion"])
}

// T027: updateCategoryStatus targeting a deleted/nonexistent resource
// returns a distinct NOT_FOUND error, not a conflict payload.
func TestUpdateCategoryStatus_NotFoundReturnsDistinctError(t *testing.T) {
	r, _ := newWatchTestResolver(t)
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
	r, store := newWatchTestResolver(t)
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
	require.NotNil(t, payload.Object)
}

func unmarshalStatus(raw []byte, out *catalog.CategoryTaxonomyStatus) error {
	return json.Unmarshal(raw, out)
}
