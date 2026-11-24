// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTranslationSource(t *testing.T, ds datastore.Datastore) (datastore.TranslationStore, *datastore.Translation) {
	t.Helper()
	capable, ok := ds.(datastore.TranslationCapable)
	require.True(t, ok, "backend must expose the same Translation source contract")
	store := capable.TranslationStore()
	namespace := "translation-" + newID()[:8]
	ns := newNamespace(datastore.NamespaceTierUser)
	ns.Name = namespace
	require.NoError(t, ds.CreateNamespace(t.Context(), ns))
	source := &datastore.Translation{
		APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		UID: newID(), Name: "laptop-de", Namespace: namespace, NamespaceID: ns.UID,
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Revision: "main@sha1:accepted-source", CreationActor: "owner",
		Spec: json.RawMessage(`{"locale":"de-DE","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titel"}]}`),
		Body: "\r\nexact body\n---\n", RawSource: []byte("complete accepted blob\r\n"),
		SourceDigest: "source-digest",
	}
	return store, source
}

func runTranslationSourceContract(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, ds)
	namespace := source.Namespace
	for _, field := range []string{"source", "body"} {
		invalid := *source
		if field == "source" {
			invalid.RawSource = []byte{0xff}
		} else {
			invalid.Body = string([]byte{0xff})
		}
		require.ErrorIs(t, store.CreateTranslation(t.Context(), &invalid), datastore.ErrInvalidArgument)
	}
	for _, owners := range []string{`{}`, `"owner"`, `[1]`, `[broken`, strings.Repeat(" ", 1<<20) + `[]`} {
		invalid := *source
		invalid.OwnerReferences = json.RawMessage(owners)
		require.ErrorIs(t, store.CreateTranslation(t.Context(), &invalid), datastore.ErrInvalidArgument)
	}
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	byID, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	byName, err := store.GetTranslationByName(t.Context(), namespace, source.Name)
	require.NoError(t, err)
	require.Equal(t, byID, byName)
	require.Equal(t, source.RawSource, byID.RawSource)
	require.Equal(t, source.Body, byID.Body)
	byID.RawSource[0] = 'X'
	again, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source.RawSource, again.RawSource, "reads must not alias stored source")
	oversizedVersion := *again
	oversizedVersion.ResourceVersion = strings.Repeat("0", 256) + "2"
	require.ErrorIs(t, store.UpdateTranslation(t.Context(), &oversizedVersion, "1"), datastore.ErrInvalidArgument)
	afterOversizedVersion, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, again, afterOversizedVersion, "oversized version tokens cannot change source or binding fencing")

	status := json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:accepted-source","conditions":[{"type":"TargetResolved","status":"False","reason":"Missing"}]}`)
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Go(func() {
			<-start
			_, err := store.UpdateTranslationStatus(t.Context(), source.UID, "1", status)
			results <- err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, datastore.ErrConflict) {
			conflicts++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	current, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.NotEqual(t, "1", current.ResourceVersion)
	require.Equal(t, int64(1), current.Generation)
	require.Equal(t, source.Revision, current.Revision, "accepted source does not depend on reconciliation")
	require.JSONEq(t, string(status), string(current.Status))

	bindingBefore, err := translationBindings(t, ds).GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	requests := make(chan bool, 8)
	requestErrors := make(chan error, 8)
	for range 8 {
		workers.Go(func() {
			queued, err := store.RequestTranslationRevalidation(t.Context(), current)
			requests <- queued
			requestErrors <- err
		})
	}
	workers.Wait()
	close(requests)
	close(requestErrors)
	queued := 0
	for requested := range requests {
		if requested {
			queued++
		}
	}
	for err := range requestErrors {
		require.NoError(t, err)
	}
	require.Equal(t, 1, queued, "read-triggered work coalesces per source version")
	afterRequest, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, current, afterRequest, "a hint cannot mutate source metadata, authored bytes or stored status")
	bindingAfter, err := translationBindings(t, ds).GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, bindingBefore, bindingAfter, "revalidation is not a binding or reference-epoch change")
	_, err = store.RequestTranslationRevalidation(t.Context(), source)
	require.ErrorIs(t, err, datastore.ErrConflict, "a stale reader cannot enqueue a different source version")
	wrongLifetime := *current
	wrongLifetime.NamespaceID = newID()
	_, err = store.RequestTranslationRevalidation(t.Context(), &wrongLifetime)
	require.ErrorIs(t, err, datastore.ErrConflict)
	wrongLifetime = *current
	wrongLifetime.CreationTimestamp = wrongLifetime.CreationTimestamp.Add(time.Millisecond)
	_, err = store.RequestTranslationRevalidation(t.Context(), &wrongLifetime)
	require.ErrorIs(t, err, datastore.ErrConflict)
	_, err = store.RequestTranslationRevalidation(t.Context(), nil)
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)

	stale := *current
	stale.ResourceVersion = "1"
	require.ErrorIs(t, store.UpdateTranslation(t.Context(), &stale, "1"), datastore.ErrConflict)
	require.ErrorIs(t, store.DeleteTranslation(t.Context(), source.UID, "1"), datastore.ErrConflict)
	page := firstTranslationSourcePage(t, store, namespace, datastore.PageParams{First: 1})
	require.Len(t, page.Items, 1)
	require.Equal(t, source.UID, page.Items[0].UID)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, current.ResourceVersion))
	_, err = store.RequestTranslationRevalidation(t.Context(), current)
	require.ErrorIs(t, err, datastore.ErrConflict, "read hints cannot resurrect retired source work")
	_, err = store.GetTranslation(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.ErrorIs(t, store.CreateTranslation(t.Context(), source), datastore.ErrAlreadyExists, "retired UIDs must not reset CAS tokens")
	replacement := *source
	replacement.UID = newID()
	require.NoError(t, store.CreateTranslation(t.Context(), &replacement))
	byName, err = store.GetTranslationByName(t.Context(), namespace, source.Name)
	require.NoError(t, err)
	require.Equal(t, replacement.UID, byName.UID)
	_, err = store.UpdateTranslationStatus(t.Context(), source.UID, "1", status)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func runTranslationPagesContract(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	store, prototype := newTranslationSource(t, ds)
	count := datastore.MaxTranslationPageSize + 3
	expected := make([]string, 0, count)
	created := make(map[string]time.Time, count)
	for i := range count {
		source := *prototype
		source.UID, source.Name = newID(), fmt.Sprintf("translation-%03d", i)
		source.CreationTimestamp = prototype.CreationTimestamp.Add(time.Duration(i) * time.Millisecond)
		require.NoError(t, store.CreateTranslation(t.Context(), &source))
		expected = append(expected, source.UID)
		created[source.UID] = source.CreationTimestamp
	}
	slices.SortFunc(expected, func(a, b string) int {
		left, err := datastore.TranslationIndexShard(a)
		require.NoError(t, err)
		right, err := datastore.TranslationIndexShard(b)
		require.NoError(t, err)
		if left != right {
			return int(left) - int(right)
		}
		return created[b].Compare(created[a])
	})
	var forward []string
	after := ""
	for range count {
		page, err := store.ListTranslations(t.Context(), prototype.Namespace, datastore.PageParams{First: 37, After: after})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Items), 37)
		for _, source := range page.Items {
			forward = append(forward, source.UID)
		}
		if !page.HasNext {
			break
		}
		require.NotEmpty(t, page.NextCursor)
		require.NotEqual(t, after, page.NextCursor)
		after = page.NextCursor
	}
	require.Equal(t, expected, forward)
	var backward []string
	before := ""
	for range count {
		page, err := store.ListTranslations(t.Context(), prototype.Namespace, datastore.PageParams{Last: 37, Before: before})
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Items), 37)
		ids := make([]string, 0, len(page.Items))
		for _, source := range page.Items {
			ids = append(ids, source.UID)
		}
		backward = append(ids, backward...)
		if !page.HasPrevious {
			break
		}
		require.NotEmpty(t, page.PreviousCursor)
		require.NotEqual(t, before, page.PreviousCursor)
		before = page.PreviousCursor
	}
	require.Equal(t, expected, backward)
	capped, err := store.ListTranslations(t.Context(), prototype.Namespace, datastore.PageParams{First: 1 << 30})
	require.NoError(t, err)
	require.LessOrEqual(t, len(capped.Items), datastore.MaxTranslationPageSize)
	require.True(t, capped.HasNext)
	first := firstTranslationSourcePage(t, store, prototype.Namespace, datastore.PageParams{First: 1})
	require.Len(t, first.Items, 1)
	require.NoError(t, store.DeleteTranslation(t.Context(), first.Items[0].UID, first.Items[0].ResourceVersion))
	next := firstTranslationSourcePage(t, store, prototype.Namespace, datastore.PageParams{First: 1, After: first.NextCursor})
	require.Len(t, next.Items, 1)
	require.Equal(t, expected[1], next.Items[0].UID, "deleting the cursor row must not lose the continuation")
	other := newNamespace(datastore.NamespaceTierUser)
	require.NoError(t, ds.CreateNamespace(t.Context(), other))
	_, err = store.ListTranslations(t.Context(), other.Name, datastore.PageParams{After: first.NextCursor})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	for _, invalid := range []datastore.PageParams{
		{First: -1}, {Last: -1}, {First: 1, Last: 1}, {Before: first.PreviousCursor},
		{After: "invalid"}, {Last: 1, After: first.NextCursor},
		{First: 1, After: first.NextCursor, Before: first.PreviousCursor},
	} {
		_, err := store.ListTranslations(t.Context(), prototype.Namespace, invalid)
		require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	}
}

func firstTranslationSourcePage(t *testing.T, store datastore.TranslationStore, namespace string, params datastore.PageParams) *datastore.TranslationPage {
	t.Helper()
	for range 32 {
		page, err := store.ListTranslations(t.Context(), namespace, params)
		require.NoError(t, err)
		if len(page.Items) != 0 {
			return page
		}
		cursor, more := page.NextCursor, page.HasNext
		if params.Last > 0 {
			cursor, more = page.PreviousCursor, page.HasPrevious
		}
		if !more {
			return page
		}
		require.NotEmpty(t, cursor)
		if params.Last > 0 {
			require.NotEqual(t, params.Before, cursor)
			params.Before = cursor
		} else {
			require.NotEqual(t, params.After, cursor)
			params.After = cursor
		}
	}
	t.Fatal("namespace source paging did not make bounded progress")
	return nil
}

func startTranslationContractCDC(t *testing.T, backend datastore.Datastore, journal datastore.ResourceWatchJournal) {
	t.Helper()
	runner, ok := backend.(interface {
		RunCatalogCDC(context.Context, string, *watchjournal.Materializer, datastore.ResourceWatchLease, time.Duration, time.Duration, func()) error
	})
	if !ok {
		return
	}
	lease, acquired, err := journal.AcquireLease(t.Context(), "translation-contract-"+newID(), time.Now().UTC(), 5*time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() { require.NoError(t, journal.ReleaseLease(context.Background(), lease)) })
	ctx, cancel := context.WithCancel(t.Context())
	done, ready := make(chan struct{}), make(chan struct{})
	var runErr error
	go func() {
		runErr = runner.RunCatalogCDC(ctx, "Translation", watchjournal.NewMaterializer(journal, watchjournal.MaterializerConfig{EventTTL: time.Hour}),
			lease, 14*24*time.Hour, 500*time.Millisecond, func() { close(ready) })
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			require.True(t, runErr == nil || errors.Is(runErr, context.Canceled), "native Translation CDC: %v", runErr)
		case <-time.After(5 * time.Second):
			t.Error("owned Translation contract reader did not stop")
		}
	})
	select {
	case <-ready:
	case <-done:
		t.Fatalf("native Translation CDC failed: %v", runErr)
	case <-time.After(20 * time.Second):
		t.Fatal("native Translation CDC did not become ready")
	}
}

func runTranslationJournalContract(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, ds)
	capable, ok := ds.(datastore.ResourceWatchCapable)
	require.True(t, ok)
	journal := capable.ResourceWatchJournal()
	startTranslationContractCDC(t, ds, journal)
	bounds, err := journal.Bounds(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	collision := *source
	collision.UID = newID()
	require.ErrorIs(t, store.CreateTranslation(t.Context(), &collision), datastore.ErrAlreadyExists)
	collision.UID, collision.Name = source.UID, "other-name"
	require.ErrorIs(t, store.CreateTranslation(t.Context(), &collision), datastore.ErrAlreadyExists)
	status := json.RawMessage(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:accepted-source","conditions":[]}`)
	observed, err := store.UpdateTranslationStatus(t.Context(), source.UID, "1", status)
	require.NoError(t, err)
	updated := *observed
	updated.ResourceVersion, updated.Generation = "3", 2
	updated.Revision = "main@sha1:new-source"
	updated.Body, updated.RawSource, updated.SourceDigest = "\nnew source body\r\n", []byte("new accepted blob\n"), "new-source-digest"
	updated.Status = nil
	updated.Labels = map[string]string{"locale": "de"}
	require.NoError(t, store.UpdateTranslation(t.Context(), &updated, observed.ResourceVersion))
	actual, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, updated.RawSource, actual.RawSource)
	require.Equal(t, updated.Revision, actual.Revision)
	require.JSONEq(t, string(status), string(actual.Status), "a source write must not erase the controller's last observation")
	_, err = store.UpdateTranslationStatus(t.Context(), source.UID, actual.ResourceVersion, status)
	require.ErrorIs(t, err, datastore.ErrConflict, "a status writer cannot attest to an older accepted source")
	_, err = store.UpdateTranslationStatus(t.Context(), source.UID, actual.ResourceVersion,
		json.RawMessage(`{"observedGeneration":2,"lastAppliedRevision":"main@sha1:new-source","freshness":"Current"}`))
	require.ErrorIs(t, err, datastore.ErrInvalidArgument, "read-time freshness must not be persisted")
	requested, err := store.RequestTranslationRevalidation(t.Context(), actual)
	require.NoError(t, err)
	require.True(t, requested)
	requested, err = store.RequestTranslationRevalidation(t.Context(), actual)
	require.NoError(t, err)
	require.False(t, requested)
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, actual.ResourceVersion))
	var translationEvents []datastore.ResourceWatchEvent
	cursor := datastore.ResourceWatchCursor{Epoch: bounds.Epoch, Sequence: bounds.HighWater}
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		events, err := journal.ReadAfter(t.Context(), cursor, 256)
		if !assert.NoError(collect, err) {
			return
		}
		for _, event := range events {
			cursor = datastore.ResourceWatchCursor{Epoch: event.Epoch, Sequence: event.Sequence}
			if event.Kind == "Translation" && event.Namespace == source.Namespace {
				translationEvents = append(translationEvents, event)
			}
		}
		assert.Len(collect, translationEvents, 5)
	}, 45*time.Second, 50*time.Millisecond)
	require.Len(t, translationEvents, 5, "failed reservations/CAS and coalesced hints must not publish")
	expectedTypes := []datastore.ResourceWatchEventType{datastore.ResourceWatchAdded, datastore.ResourceWatchModified,
		datastore.ResourceWatchModified, datastore.ResourceWatchModified, datastore.ResourceWatchDeleted}
	keys := map[string]bool{}
	for i, event := range translationEvents {
		require.Equal(t, expectedTypes[i], event.Type)
		require.NotEmpty(t, event.DeduplicationKey)
		require.False(t, keys[event.DeduplicationKey])
		keys[event.DeduplicationKey] = true
		var payload datastore.Translation
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		require.Equal(t, source.UID, payload.UID)
		if i >= 2 {
			require.Equal(t, updated.RawSource, payload.RawSource)
			require.Equal(t, updated.Body, payload.Body)
		}
		if i > 0 {
			require.Greater(t, event.Sequence, translationEvents[i-1].Sequence)
		}
	}
	require.Equal(t, updated.Labels, translationEvents[2].SelectorLabels)
	require.Equal(t, updated.Labels, translationEvents[3].SelectorLabels)
	require.Equal(t, updated.Labels, translationEvents[4].SelectorLabels)
	require.JSONEq(t, string(translationEvents[2].Payload), string(translationEvents[3].Payload),
		"revalidation advances the journal cursor, not the public source version or status")
}

func translationBindings(t *testing.T, ds datastore.Datastore) datastore.TranslationBindingStore {
	t.Helper()
	capable, ok := ds.(datastore.TranslationBindingCapable)
	require.True(t, ok, "binding authority must survive datastore decoration")
	store := capable.TranslationBindingStore()
	require.NotNil(t, store)
	return store
}

func nextTranslationContractVersion(t *testing.T, current string) string {
	t.Helper()
	value, err := strconv.Atoi(current)
	require.NoError(t, err)
	return strconv.Itoa(value + 1)
}

func changeTranslationReference(t *testing.T, store datastore.TranslationStore, uid string, change func(*translation.Spec)) (*datastore.Translation, error) {
	t.Helper()
	source, err := store.GetTranslation(t.Context(), uid)
	require.NoError(t, err)
	expected := source.ResourceVersion
	var spec translation.Spec
	require.NoError(t, json.Unmarshal(source.Spec, &spec))
	change(&spec)
	source.Spec, err = json.Marshal(spec)
	require.NoError(t, err)
	source.Generation++
	source.ResourceVersion = nextTranslationContractVersion(t, expected)
	source.Revision = "main@sha1:source-" + source.ResourceVersion
	source.RawSource = []byte("accepted source " + source.ResourceVersion)
	return source, store.UpdateTranslation(t.Context(), source, expected)
}

func bindingCandidate(t *testing.T, current *datastore.TranslationBinding, uid string) *datastore.TranslationBinding {
	t.Helper()
	candidate := *current
	candidate.Epochs = slices.Clone(current.Epochs)
	candidate.ResourceVersion = nextTranslationContractVersion(t, current.ResourceVersion)
	candidate.Epochs[len(candidate.Epochs)-1].BoundUID = uid
	return &candidate
}

func runTranslationBindingContract(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, ds)
	bindings := translationBindings(t, ds)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	initial, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, source.ResourceVersion, initial.SourceVersion)
	require.Len(t, initial.Epochs, 1)
	require.NotEmpty(t, initial.CurrentEpoch)
	require.Equal(t, initial.CurrentEpoch, initial.Epochs[0].Epoch)
	require.Equal(t, source.NamespaceID, initial.Epochs[0].Target.NamespaceUID)

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		candidate := bindingCandidate(t, initial, newID())
		workers.Go(func() {
			<-start
			results <- bindings.CompareAndSwapTranslationBinding(t.Context(), candidate, initial.ResourceVersion)
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, datastore.ErrConflict)
			conflicts++
		}
	}
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	current, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	firstUID, firstEpoch := current.Epochs[0].BoundUID, current.CurrentEpoch
	current.Epochs[0].BoundUID = "caller mutation"
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, firstUID, current.Epochs[0].BoundUID)

	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.Locale = "fr-FR" })
	require.NoError(t, err)
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, firstEpoch, current.CurrentEpoch)
	require.Len(t, current.Epochs, 1, "locale changes cannot retarget or consume reference epochs")
	require.ErrorIs(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, initial, newID()), initial.ResourceVersion), datastore.ErrConflict)
	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.TargetRef.Namespace = source.Namespace })
	require.NoError(t, err)
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, firstEpoch, current.CurrentEpoch, "default and explicit effective namespace are one reference")
	require.Equal(t, firstUID, current.Epochs[0].BoundUID)

	replacement := newID()
	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.TargetRef.UID = replacement })
	require.NoError(t, err)
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Len(t, current.Epochs, 2)
	require.NotEqual(t, firstEpoch, current.CurrentEpoch)
	require.Empty(t, current.Epochs[1].BoundUID)
	require.ErrorIs(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, current, firstUID), current.ResourceVersion), datastore.ErrConflict)
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, current, replacement), current.ResourceVersion))

	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.TargetRef.UID = "" })
	require.NoError(t, err)
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Len(t, current.Epochs, 3)
	require.Equal(t, replacement, current.Epochs[2].BoundUID, "removing a pin retains the last explicitly bound incarnation")
	require.ErrorIs(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, current, firstUID), current.ResourceVersion), datastore.ErrConflict)
	truncated := bindingCandidate(t, current, replacement)
	truncated.Epochs = truncated.Epochs[1:]
	require.ErrorIs(t, bindings.CompareAndSwapTranslationBinding(t.Context(), truncated, current.ResourceVersion), datastore.ErrConflict)

	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.TargetRef.Name = "other-target" })
	require.NoError(t, err)
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, current, newID()), current.ResourceVersion))
	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.TargetRef.Name = "laptop" })
	require.NoError(t, err)
	current, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, replacement, current.Epochs[len(current.Epochs)-1].BoundUID, "leaving and returning to an identity cannot erase its incarnation history")

	accepted, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	status, err := json.Marshal(map[string]any{"observedGeneration": accepted.Generation, "lastAppliedRevision": accepted.Revision, "conditions": []any{}})
	require.NoError(t, err)
	observed, err := store.UpdateTranslationStatus(t.Context(), source.UID, accepted.ResourceVersion, status)
	require.NoError(t, err)
	require.ErrorIs(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, current, replacement), current.ResourceVersion), datastore.ErrConflict)
	afterStatus, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, observed.ResourceVersion, afterStatus.SourceVersion)
	require.Equal(t, current.CurrentEpoch, afterStatus.CurrentEpoch)
	require.Equal(t, current.Epochs, afterStatus.Epochs, "a status write cannot erase sticky history")
	require.NoError(t, store.DeleteTranslation(t.Context(), source.UID, observed.ResourceVersion))
	_, err = bindings.GetTranslationBinding(t.Context(), source.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
	require.ErrorIs(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, afterStatus, replacement), afterStatus.ResourceVersion), datastore.ErrNotFound)
}

func runTranslationEpochBudgetContract(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, ds)
	bindings := translationBindings(t, ds)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	for i := 1; i < datastore.MaxTranslationEpochs; i++ {
		_, err := changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) {
			spec.TargetRef.Name = fmt.Sprintf("target-%03d", i)
		})
		require.NoError(t, err)
	}
	before, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	current, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Len(t, current.Epochs, datastore.MaxTranslationEpochs)
	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.TargetRef.Name = "one-too-many" })
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	after, err := store.GetTranslation(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "epoch exhaustion must not partially accept source")
	unchanged, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, current, unchanged)
	_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.Locale = "fr-FR" })
	require.NoError(t, err, "non-reference source edits remain possible after epoch exhaustion")
	localeUpdate, err := bindings.GetTranslationBinding(t.Context(), source.UID)
	require.NoError(t, err)
	require.Equal(t, current.CurrentEpoch, localeUpdate.CurrentEpoch)
	require.Equal(t, current.Epochs, localeUpdate.Epochs)
}

func runTranslationBindingByteBudgetContract(t *testing.T, ds datastore.Datastore) {
	t.Helper()
	store, source := newTranslationSource(t, ds)
	bindings := translationBindings(t, ds)
	require.NoError(t, store.CreateTranslation(t.Context(), source))
	exhausted := false
	for i := 1; i < datastore.MaxTranslationEpochs; i++ {
		before, err := store.GetTranslation(t.Context(), source.UID)
		require.NoError(t, err)
		current, err := bindings.GetTranslationBinding(t.Context(), source.UID)
		require.NoError(t, err)
		_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) {
			spec.TargetRef.ResourceVersion = strings.Repeat("r", 8192) + strconv.Itoa(i)
		})
		if !errors.Is(err, datastore.ErrInvalidArgument) {
			require.NoError(t, err)
			continue
		}
		exhausted = true
		after, err := store.GetTranslation(t.Context(), source.UID)
		require.NoError(t, err)
		require.Equal(t, before, after, "byte exhaustion must not partially accept source")
		unchanged, err := bindings.GetTranslationBinding(t.Context(), source.UID)
		require.NoError(t, err)
		require.Equal(t, current, unchanged)
		require.NoError(t, bindings.CompareAndSwapTranslationBinding(t.Context(), bindingCandidate(t, current, newID()), current.ResourceVersion), "admitted epochs reserve first-binding space")
		_, err = changeTranslationReference(t, store, source.UID, func(spec *translation.Spec) { spec.Locale = "fr-FR" })
		require.NoError(t, err, "non-reference edits remain possible after byte exhaustion")
		break
	}
	require.True(t, exhausted, "resource bytes must be bounded independently of epoch count")
}
