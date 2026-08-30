// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/graph/resolver"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type commitOrderGitWriter struct {
	*mockGitWriter

	mu      sync.Mutex
	commits []string
	next    int
	current string
	trees   map[string]map[string][]byte
}

func newCommitOrderGitWriter(current string, commits ...string) *commitOrderGitWriter {
	return &commitOrderGitWriter{
		mockGitWriter: &mockGitWriter{},
		commits:       commits,
		current:       current,
		trees:         map[string]map[string][]byte{current: {}},
	}
}

func (w *commitOrderGitWriter) CommitFileForRepo(_ context.Context, _ string, params gitclient.CommitFileParams) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sha := w.commits[w.next]
	w.next++
	tree := cloneCommitTree(w.trees[w.current])
	tree[params.Path] = append([]byte(nil), params.Content...)
	w.trees[sha] = tree
	w.current = sha
	return sha, nil
}

func (w *commitOrderGitWriter) ResolveRefForRepo(_ context.Context, _, _ string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current, nil
}

func (w *commitOrderGitWriter) ReadFileForRepo(_ context.Context, _, path, ref string) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ref == "refs/heads/main" {
		ref = w.current
	}
	content, ok := w.trees[ref][path]
	if !ok {
		return nil, errors.New("file not found")
	}
	return append([]byte(nil), content...), nil
}

func (w *commitOrderGitWriter) head() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current
}

func cloneCommitTree(tree map[string][]byte) map[string][]byte {
	cloned := make(map[string][]byte, len(tree))
	for path, content := range tree {
		cloned[path] = append([]byte(nil), content...)
	}
	return cloned
}

type descendantCommitGitWriter struct {
	*commitOrderGitWriter
	firstResolveStarted chan struct{}
	releaseFirstResolve chan struct{}
	resolveOnce         sync.Once
}

func newDescendantCommitGitWriter(current string, commits ...string) *descendantCommitGitWriter {
	return &descendantCommitGitWriter{
		commitOrderGitWriter: newCommitOrderGitWriter(current, commits...),
		firstResolveStarted:  make(chan struct{}),
		releaseFirstResolve:  make(chan struct{}),
	}
}

func (w *descendantCommitGitWriter) ResolveRefForRepo(ctx context.Context, repositoryID, ref string) (string, error) {
	blocked := false
	w.resolveOnce.Do(func() {
		blocked = true
		close(w.firstResolveStarted)
	})
	if blocked {
		select {
		case <-w.releaseFirstResolve:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return w.commitOrderGitWriter.ResolveRefForRepo(ctx, repositoryID, ref)
}

type blockingOlderNamespaceUpdateStore struct {
	datastore.Datastore
	name    string
	started chan struct{}
	release chan struct{}
	once    sync.Once

	mu       sync.Mutex
	getCount int
}

func (s *blockingOlderNamespaceUpdateStore) block() {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
}

func (s *blockingOlderNamespaceUpdateStore) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	if name == s.name {
		s.mu.Lock()
		s.getCount++
		count := s.getCount
		s.mu.Unlock()
		if count == 2 {
			s.block()
		}
	}
	return s.Datastore.GetNamespaceByName(ctx, name)
}

func (s *blockingOlderNamespaceUpdateStore) UpdateNamespace(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	if namespace.Name == s.name && namespace.Title == "Older update" {
		s.block()
	}
	return s.Datastore.UpdateNamespace(ctx, namespace, expectedResourceVersion)
}

type blockingNamespaceCreateStore struct {
	datastore.Datastore
	name    string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingTitleNamespaceUpdateStore struct {
	datastore.Datastore
	title   string
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTitleNamespaceUpdateStore) UpdateNamespace(
	ctx context.Context,
	namespace *datastore.Namespace,
	expectedResourceVersion string,
) error {
	if namespace.Title == s.title {
		s.once.Do(func() {
			close(s.started)
			<-s.release
		})
	}
	return s.Datastore.UpdateNamespace(ctx, namespace, expectedResourceVersion)
}

func (s *blockingNamespaceCreateStore) CreateNamespace(ctx context.Context, namespace *datastore.Namespace) error {
	if namespace.Name == s.name {
		s.once.Do(func() {
			close(s.started)
			<-s.release
		})
	}
	return s.Datastore.CreateNamespace(ctx, namespace)
}

func TestNamespaceGraphQLCommitOrderPreventsOlderStateWinning(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		seed := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		created, err := seed.CreateNamespace(ctx, createNamespaceInput("commit-order-update", model.NamespaceTierUser), "alice")
		require.NoError(t, err)

		olderSHA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		newerSHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		writer := newCommitOrderGitWriter("deadbeef", olderSHA, newerSHA)
		writer.trees["deadbeef"]["namespaces/"+created.Name+".md"] =
			namespaceManifestForGraphQL(created.Name, created.Title, model.NamespaceTierUser)
		olderStore := &blockingOlderNamespaceUpdateStore{
			Datastore: seed.Store(),
			name:      created.Name,
			started:   make(chan struct{}),
			release:   make(chan struct{}),
		}
		older := newCommitOrderService(t, olderStore, writer)
		newer := newCommitOrderService(t, seed.Store(), writer)

		olderInput := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
		olderTitle := "Older update"
		olderInput.Spec.Title = &olderTitle
		newerInput := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
		newerTitle := "Newer update"
		newerInput.Spec.Title = &newerTitle

		olderDone := make(chan error, 1)
		go func() {
			_, updateErr := older.UpdateNamespace(ctx, olderInput, "alice")
			olderDone <- updateErr
		}()

		waitForCommitOrderPoint(t, olderStore.started, "older update did not reach the post-ref-check admission window")
		_, err = newer.UpdateNamespace(ctx, newerInput, "alice")
		require.NoError(t, err)
		close(olderStore.release)

		olderErr := waitForCommitOrderResult(t, olderDone)
		require.NoError(t, olderErr)

		persisted, err := seed.GetNamespaceByName(ctx, created.Name)
		require.NoError(t, err)
		assert.Equal(t, newerTitle, persisted.Title)
		assert.Equal(t, newerSHA, writer.head())
		assertNamespaceAdmissionRevision(t, persisted, "main@sha1:"+newerSHA)
	})

	t.Run("create", func(t *testing.T) {
		seed := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		name := "commit-order-create"
		olderSHA := "cccccccccccccccccccccccccccccccccccccccc"
		newerSHA := "dddddddddddddddddddddddddddddddddddddddd"
		writer := newCommitOrderGitWriter("deadbeef", olderSHA, newerSHA)
		olderStore := &blockingNamespaceCreateStore{
			Datastore: seed.Store(),
			name:      name,
			started:   make(chan struct{}),
			release:   make(chan struct{}),
		}
		newerStore := &blockingNamespaceCreateStore{
			Datastore: seed.Store(),
			name:      name,
			started:   make(chan struct{}),
			release:   make(chan struct{}),
		}
		older := newCommitOrderService(t, olderStore, writer)
		newer := newCommitOrderService(t, newerStore, writer)

		olderInput := createNamespaceInput(name, model.NamespaceTierUser)
		olderTitle := "Older creation"
		olderInput.Spec.Title = &olderTitle
		newerInput := createNamespaceInput(name, model.NamespaceTierOrganization)
		newerTitle := "Newer creation"
		newerInput.Spec.Title = &newerTitle

		olderDone := make(chan error, 1)
		go func() {
			_, createErr := older.CreateNamespace(ctx, olderInput, "alice")
			olderDone <- createErr
		}()
		waitForCommitOrderPoint(t, olderStore.started, "older create did not reach its conditional write")

		newerDone := make(chan error, 1)
		go func() {
			_, createErr := newer.CreateNamespace(ctx, newerInput, "alice")
			newerDone <- createErr
		}()
		waitForCommitOrderPoint(t, newerStore.started, "newer create did not reach its conditional write")

		close(olderStore.release)
		require.Error(t, waitForCommitOrderResult(t, olderDone))
		close(newerStore.release)
		require.NoError(t, waitForCommitOrderResult(t, newerDone))

		persisted, err := seed.GetNamespaceByName(ctx, name)
		require.NoError(t, err)
		assert.Equal(t, newerTitle, persisted.Title)
		assert.Equal(t, newerSHA, writer.head())
		assertNamespaceAdmissionRevision(t, persisted, "main@sha1:"+newerSHA)
	})
}

func TestNamespaceGraphQLDescendantConvergence(t *testing.T) {
	t.Run("disjoint resources both materialize", func(t *testing.T) {
		seed := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		olderSHA := "1111111111111111111111111111111111111111"
		newerSHA := "2222222222222222222222222222222222222222"
		writer := newDescendantCommitGitWriter("deadbeef", olderSHA, newerSHA)
		older := newCommitOrderService(t, seed.Store(), writer)
		newer := newCommitOrderService(t, seed.Store(), writer)

		olderDone := make(chan error, 1)
		go func() {
			_, err := older.CreateNamespace(ctx, createNamespaceInput("descendant-x", model.NamespaceTierUser), "alice")
			olderDone <- err
		}()

		waitForCommitOrderPoint(t, writer.firstResolveStarted, "older mutation did not pause after its commit")
		_, err := newer.CreateNamespace(ctx, createNamespaceInput("descendant-y", model.NamespaceTierUser), "bob")
		require.NoError(t, err)
		close(writer.releaseFirstResolve)
		require.NoError(t, waitForCommitOrderResult(t, olderDone))

		x, err := seed.GetNamespaceByName(ctx, "descendant-x")
		require.NoError(t, err)
		y, err := seed.GetNamespaceByName(ctx, "descendant-y")
		require.NoError(t, err)
		assert.Equal(t, "main@sha1:"+newerSHA, x.Revision)
		assert.Equal(t, "main@sha1:"+newerSHA, y.Revision)
	})

	t.Run("same resource converges to newer content", func(t *testing.T) {
		seed := newTestSvc(t, &mockGitWriter{})
		ctx := context.Background()
		created, err := seed.CreateNamespace(ctx, createNamespaceInput("descendant-same", model.NamespaceTierUser), "seed")
		require.NoError(t, err)

		olderSHA := "3333333333333333333333333333333333333333"
		newerSHA := "4444444444444444444444444444444444444444"
		writer := newDescendantCommitGitWriter("deadbeef", olderSHA, newerSHA)
		older := newCommitOrderService(t, seed.Store(), writer)
		newer := newCommitOrderService(t, seed.Store(), writer)

		olderInput := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
		olderTitle := "Older descendant"
		olderInput.Spec.Title = &olderTitle
		newerInput := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
		newerTitle := "Newer descendant"
		newerInput.Spec.Title = &newerTitle
		writer.trees["deadbeef"]["namespaces/descendant-same.md"] =
			namespaceManifestForGraphQL(created.Name, created.Title, model.NamespaceTierUser)

		olderDone := make(chan error, 1)
		go func() {
			_, updateErr := older.UpdateNamespace(ctx, olderInput, "alice")
			olderDone <- updateErr
		}()

		waitForCommitOrderPoint(t, writer.firstResolveStarted, "older mutation did not pause after its commit")
		_, err = newer.UpdateNamespace(ctx, newerInput, "bob")
		require.NoError(t, err)
		close(writer.releaseFirstResolve)
		require.NoError(t, waitForCommitOrderResult(t, olderDone))

		persisted, err := seed.GetNamespaceByName(ctx, created.Name)
		require.NoError(t, err)
		assert.Equal(t, newerTitle, persisted.Title)
		assert.Equal(t, newerSHA, persisted.GitCommitSHA)
		assertNamespaceAdmissionRevision(t, persisted, "main@sha1:"+newerSHA)
	})
}

func TestNamespaceGraphQLDescendantSameResourceRejectsStaleRowBeforeExactHeadAdmission(t *testing.T) {
	ctx := context.Background()
	seed := newTestSvc(t, &mockGitWriter{})
	created, err := seed.CreateNamespace(ctx, createNamespaceInput("descendant-audit-same", model.NamespaceTierUser), "seed")
	require.NoError(t, err)

	olderSHA := "5555555555555555555555555555555555555555"
	newerSHA := "6666666666666666666666666666666666666666"
	writer := newDescendantCommitGitWriter("deadbeef", olderSHA, newerSHA)
	path := "namespaces/descendant-audit-same.md"
	writer.trees["deadbeef"][path] = namespaceManifestForGraphQL(created.Name, "", model.NamespaceTierUser)
	olderTime := time.Date(2026, 8, 29, 14, 0, 0, 0, time.UTC)
	newerTime := olderTime.Add(time.Hour)
	newerStore := &blockingTitleNamespaceUpdateStore{
		Datastore: seed.Store(),
		title:     "Newer audit",
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	older := newCommitOrderServiceAt(t, seed.Store(), writer, olderTime)
	newer := newCommitOrderServiceAt(t, newerStore, writer, newerTime)

	olderInput := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
	olderTitle := "Older audit"
	olderInput.Spec.Title = &olderTitle
	newerInput := updateNamespaceInput(created.Name, model.NamespaceTierOrganization)
	newerTitle := "Newer audit"
	newerInput.Spec.Title = &newerTitle

	olderDone := make(chan error, 1)
	go func() {
		_, updateErr := older.UpdateNamespace(ctx, olderInput, "alice")
		olderDone <- updateErr
	}()
	waitForCommitOrderPoint(t, writer.firstResolveStarted, "older mutation did not pause after its commit")

	newerDone := make(chan error, 1)
	go func() {
		_, updateErr := newer.UpdateNamespace(ctx, newerInput, "bob")
		newerDone <- updateErr
	}()
	waitForCommitOrderPoint(t, newerStore.started, "newer exact-head mutation did not reach its datastore write")

	close(writer.releaseFirstResolve)
	require.Error(t, waitForCommitOrderResult(t, olderDone))

	stale, err := seed.GetNamespaceByName(ctx, created.Name)
	require.NoError(t, err)
	assert.NotEqual(t, newerSHA, stale.GitCommitSHA)

	close(newerStore.release)
	require.NoError(t, waitForCommitOrderResult(t, newerDone))

	persisted, err := seed.GetNamespaceByName(ctx, created.Name)
	require.NoError(t, err)
	assert.Equal(t, newerTitle, persisted.Title)
	assert.Equal(t, "bob", persisted.UpdateActor)
	assert.Equal(t, newerTime, persisted.UpdateTimestamp)
	assert.Equal(t, newerSHA, persisted.GitCommitSHA)
}

func TestNamespaceGraphQLDescendantDisjointCommitKeepsRequestAudit(t *testing.T) {
	ctx := context.Background()
	seed := newTestSvc(t, &mockGitWriter{})
	olderSHA := "7777777777777777777777777777777777777777"
	newerSHA := "8888888888888888888888888888888888888888"
	writer := newDescendantCommitGitWriter("deadbeef", olderSHA, newerSHA)
	olderTime := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	newerTime := olderTime.Add(time.Hour)
	newerStore := &blockingNamespaceCreateStore{
		Datastore: seed.Store(),
		name:      "descendant-audit-y",
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	older := newCommitOrderServiceAt(t, seed.Store(), writer, olderTime)
	newer := newCommitOrderServiceAt(t, newerStore, writer, newerTime)

	olderDone := make(chan error, 1)
	go func() {
		_, createErr := older.CreateNamespace(ctx, createNamespaceInput("descendant-audit-x", model.NamespaceTierUser), "alice")
		olderDone <- createErr
	}()
	waitForCommitOrderPoint(t, writer.firstResolveStarted, "older mutation did not pause after its commit")

	newerDone := make(chan error, 1)
	go func() {
		_, createErr := newer.CreateNamespace(ctx, createNamespaceInput("descendant-audit-y", model.NamespaceTierUser), "bob")
		newerDone <- createErr
	}()
	waitForCommitOrderPoint(t, newerStore.started, "newer disjoint mutation did not reach its datastore write")

	close(writer.releaseFirstResolve)
	require.NoError(t, waitForCommitOrderResult(t, olderDone))
	close(newerStore.release)
	require.NoError(t, waitForCommitOrderResult(t, newerDone))

	x, err := seed.GetNamespaceByName(ctx, "descendant-audit-x")
	require.NoError(t, err)
	assert.Equal(t, "alice", x.CreationActor)
	assert.Equal(t, "alice", x.UpdateActor)
	assert.Equal(t, olderTime, x.CreationTimestamp)
	assert.Equal(t, olderTime, x.UpdateTimestamp)
	assert.Equal(t, newerSHA, x.GitCommitSHA)

	y, err := seed.GetNamespaceByName(ctx, "descendant-audit-y")
	require.NoError(t, err)
	assert.Equal(t, "bob", y.CreationActor)
	assert.Equal(t, newerTime, y.CreationTimestamp)
}

func newCommitOrderService(t *testing.T, store datastore.Datastore, writer resolver.GitWriter) *resolver.Service {
	return newCommitOrderServiceAt(t, store, writer, time.Time{})
}

func newCommitOrderServiceAt(
	t *testing.T,
	store datastore.Datastore,
	writer resolver.GitWriter,
	now time.Time,
) *resolver.Service {
	t.Helper()
	deps := resolver.ServiceDeps{
		Store:     store,
		GitWriter: writer,
		Logger:    zap.NewNop(),
	}
	if !now.IsZero() {
		deps.Clock = apiruntime.NewFixedClock(now)
	}
	svc, err := resolver.NewService(deps)
	require.NoError(t, err)
	return svc
}

func namespaceManifestForGraphQL(name, title string, tier model.NamespaceTier) []byte {
	return []byte("---\napiVersion: gitstore.dev/v1beta1\nkind: Namespace\nmetadata:\n  name: " +
		name + "\nspec:\n  title: " + title + "\n  tier: " + string(tier) + "\n---\n")
}

func waitForCommitOrderPoint(t *testing.T, ready <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}

func waitForCommitOrderResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Namespace commit-order mutation did not finish")
		return nil
	}
}

func assertNamespaceAdmissionRevision(t *testing.T, namespace *datastore.Namespace, revision string) {
	t.Helper()
	var status catalog.NamespaceStatus
	require.NoError(t, json.Unmarshal(namespace.Status, &status))
	assert.Equal(t, revision, status.LastAppliedRevision)
}
