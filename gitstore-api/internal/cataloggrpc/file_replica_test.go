// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeFileWithBody builds a File fixture with a distinguishable Markdown
// body, used to prove which commit's content ultimately won an admission
// race or handoff.
func makeFileWithBody(name, body string) []byte {
	return []byte("---\napiVersion: storage.gitstore.dev/v1beta1\nkind: File\nmetadata:\n  name: " + name + "\nspec:\n  contentType: image/jpeg\n  source:\n    type: s3\n    uri: s3://bucket/" + name + "\n---\n" + body)
}

// TestAdmitResources_FileConcurrentDuplicateAdmissionIsIdempotent models two
// gitstore-api replicas that both receive the identical post-receive webhook
// for the same push (a realistic duplicate-delivery scenario) and both call
// AdmitResources with the same brand-new File identity concurrently. Neither
// replica shares in-memory state with the other — only the durable
// datastore — mirroring the existing multi-replica pattern already used for
// Namespace admission (TestAdmitResources_NamespaceOlderCommitCannotOverwriteNewerAdmission).
// Both calls must return without error, and the shared store must end up
// with exactly one File row for the identity (spec 051 T039).
func TestAdmitResources_FileConcurrentDuplicateAdmissionIsIdempotent(t *testing.T) {
	store := newTestDatastore(t)
	commit := strings.Repeat("d", 40)
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"files/hero.md"}, nil
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeFile("hero"), nil
		},
	}

	// Two independent Server instances sharing one datastore == two replicas
	// handling the same webhook delivery.
	replicas := []*cataloggrpc.Server{
		newCatalogServer(t, store, git),
		newCatalogServer(t, store, git),
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(replicas))
	for _, replica := range replicas {
		wg.Add(1)
		go func(srv *cataloggrpc.Server) {
			defer wg.Done()
			_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID, CommitSha: commit, RefName: "refs/heads/main",
			})
			errs <- err
		}(replica)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "duplicate concurrent admission must never surface as a gRPC error")
	}

	file, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, "Alt text", file.Body)

	page, err := store.ListFiles(context.Background(), "gitstore", datastore.PageParams{})
	require.NoError(t, err)
	assert.Len(t, page.Items, 1, "the duplicate-delivery race must never leave more than one durable File row")
}

// TestAdmitResources_FileOlderCommitCannotOverwriteNewerAdmission mirrors the
// existing Namespace multi-replica race guard
// (TestAdmitResources_NamespaceOlderCommitCannotOverwriteNewerAdmission) for
// File: an older commit's admission is still in flight (blocked mid git-tree
// read) on one replica when a newer commit is admitted to completion by a
// second replica. When the older admission finally resumes, its stale write
// must not clobber the newer, already-durable state (spec 051 T039).
func TestAdmitResources_FileOlderCommitCannotOverwriteNewerAdmission(t *testing.T) {
	store := newTestDatastore(t)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	c := strings.Repeat("c", 40)
	path := "files/hero.md"
	files := map[string]map[string][]byte{
		a: {path: makeFile("hero")},
		b: {path: makeFileWithBody("hero", "Older update")},
		c: {path: makeFileWithBody("hero", "Newest update")},
	}

	// Seed a real durable File first. Both raced admissions below are updates,
	// so the test cannot accidentally pass through create collision handling.
	seedCurrent := a
	seedServer := newCatalogServer(t, store, newTreeGitReader(&seedCurrent, files))
	_, err := seedServer.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, CommitSha: a, RefName: "refs/heads/main",
	})
	require.NoError(t, err)
	seeded, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	require.Equal(t, "1", seeded.ResourceVersion)

	var mu sync.Mutex
	current := b
	blockFirstBListing := true
	olderListingStarted := make(chan struct{})
	releaseOlder := make(chan struct{})
	git := &mockGitReader{
		listFilesFunc: func(_ context.Context, _, _, ref string) ([]string, error) {
			mu.Lock()
			block := ref == b && blockFirstBListing
			if block {
				blockFirstBListing = false
			}
			tree := files[ref]
			mu.Unlock()
			if block {
				close(olderListingStarted)
				<-releaseOlder
			}
			paths := make([]string, 0, len(tree))
			for filePath := range tree {
				paths = append(paths, filePath)
			}
			sort.Strings(paths)
			return paths, nil
		},
		readFileFunc: func(_ context.Context, _, path, ref string) ([]byte, error) {
			mu.Lock()
			content := files[ref][path]
			mu.Unlock()
			return content, nil
		},
		resolveRefFunc: func(_ context.Context, _, _ string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			return current, nil
		},
	}

	olderServer := newCatalogServer(t, store, git)
	newerServer := newCatalogServer(t, store, git)

	olderDone := make(chan error, 1)
	go func() {
		_, err := olderServer.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
			RepositoryId: testRepoID, OldCommitSha: a, NewCommitSha: b, CommitSha: b,
			RefName: "refs/heads/main", ChangedPaths: []string{path},
		})
		olderDone <- err
	}()

	select {
	case <-olderListingStarted:
	case <-time.After(time.Second):
		t.Fatal("older admission did not verify and begin loading its commit")
	}

	mu.Lock()
	current = c
	mu.Unlock()
	_, err = newerServer.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: b, NewCommitSha: c, CommitSha: c,
		RefName: "refs/heads/main", ChangedPaths: []string{path},
	})
	require.NoError(t, err)

	close(releaseOlder)
	select {
	case err := <-olderDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("older admission did not finish")
	}

	file, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, c, file.GitCommitSHA, "the stale update must not overwrite the already-durable newer update")
	assert.Equal(t, "Newest update", file.Body)
	assert.Equal(t, "2", file.ResourceVersion, "only the winning update may advance the durable version")
}

type orderedFileUpdateStore struct {
	datastore.Datastore
	olderCommit, newerCommit                  string
	olderEntered, releaseOlder, olderFinished chan struct{}
	newerEntered, releaseNewer                chan struct{}
	olderOnce, newerOnce                      sync.Once
}

func (s *orderedFileUpdateStore) UpdateFile(ctx context.Context, file *datastore.File, expectedResourceVersion string) error {
	if file.GitCommitSHA == s.olderCommit {
		s.olderOnce.Do(func() {
			close(s.olderEntered)
			<-s.releaseOlder
		})
		err := s.Datastore.UpdateFile(ctx, file, expectedResourceVersion)
		close(s.olderFinished)
		return err
	}
	if file.GitCommitSHA == s.newerCommit {
		s.newerOnce.Do(func() {
			close(s.newerEntered)
			<-s.releaseNewer
		})
	}
	return s.Datastore.UpdateFile(ctx, file, expectedResourceVersion)
}

// TestAdmitResources_FileCurrentCommitRetriesAfterConcurrentConflict forces
// both replicas to load resourceVersion 1, lets stale commit B win the first
// CAS, then proves current commit C reloads resourceVersion 2 and retries to
// durable convergence instead of logging its conflict and returning success.
func TestAdmitResources_FileCurrentCommitRetriesAfterConcurrentConflict(t *testing.T) {
	base := newTestDatastore(t)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)
	c := strings.Repeat("c", 40)
	path := "files/hero.md"
	files := map[string]map[string][]byte{
		a: {path: makeFile("hero")},
		b: {path: makeFileWithBody("hero", "Older update")},
		c: {path: makeFileWithBody("hero", "Current update")},
	}
	current := a
	seed := newCatalogServer(t, base, newTreeGitReader(&current, files))
	_, err := seed.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, CommitSha: a, RefName: "refs/heads/main",
	})
	require.NoError(t, err)

	store := &orderedFileUpdateStore{
		Datastore: base, olderCommit: b, newerCommit: c,
		olderEntered: make(chan struct{}), releaseOlder: make(chan struct{}), olderFinished: make(chan struct{}),
		newerEntered: make(chan struct{}), releaseNewer: make(chan struct{}),
	}
	var mu sync.Mutex
	current = b
	git := newTreeGitReader(&current, files)
	git.resolveRefFunc = func(context.Context, string, string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	}
	olderServer := newCatalogServer(t, store, git)
	newerServer := newCatalogServer(t, store, git)

	olderDone := make(chan error, 1)
	go func() {
		_, err := olderServer.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
			RepositoryId: testRepoID, OldCommitSha: a, NewCommitSha: b, CommitSha: b,
			RefName: "refs/heads/main", ChangedPaths: []string{path},
		})
		olderDone <- err
	}()
	select {
	case <-store.olderEntered:
	case <-time.After(time.Second):
		t.Fatal("older update did not reach the datastore CAS")
	}

	mu.Lock()
	current = c
	mu.Unlock()
	newerDone := make(chan error, 1)
	go func() {
		_, err := newerServer.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
			RepositoryId: testRepoID, OldCommitSha: b, NewCommitSha: c, CommitSha: c,
			RefName: "refs/heads/main", ChangedPaths: []string{path},
		})
		newerDone <- err
	}()
	select {
	case <-store.newerEntered:
	case <-time.After(time.Second):
		t.Fatal("current update did not reach the datastore CAS")
	}

	close(store.releaseOlder)
	select {
	case <-store.olderFinished:
	case <-time.After(time.Second):
		t.Fatal("older update did not finish its CAS")
	}
	close(store.releaseNewer)
	require.NoError(t, <-olderDone)
	require.NoError(t, <-newerDone)

	file, err := base.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, c, file.GitCommitSHA)
	assert.Equal(t, "Current update", file.Body)
	assert.Equal(t, "3", file.ResourceVersion)
}

// TestAdmitResources_FileUpdateSurvivesReplicaProcessReplacement models a
// replica crashing (or being rolled) between two pushes: the Server instance
// that admitted the create is discarded entirely and a brand-new Server
// (sharing only the durable store, none of the discarded process's in-memory
// state) admits the following update. File admission must be fully
// stateless with respect to the Server instance handling it — every fact
// needed to admit correctly (identity, generation, contentType immutability)
// must come from the durable store alone (spec 051 T039).
func TestAdmitResources_FileUpdateSurvivesReplicaProcessReplacement(t *testing.T) {
	store := newTestDatastore(t)
	a := strings.Repeat("a", 40)
	b := strings.Repeat("b", 40)

	// "Process 1": creates the File, then is discarded (simulating a crash
	// or rolling replacement) without ever being reused again.
	gitA := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"files/hero.md"}, nil
		},
		readFileFunc: func(context.Context, string, string, string) ([]byte, error) {
			return makeFile("hero"), nil
		},
	}
	process1 := newCatalogServer(t, store, gitA)
	_, err := process1.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, CommitSha: a, RefName: "refs/heads/main",
	})
	require.NoError(t, err)
	// process1 is deliberately never referenced again below — it represents
	// a discarded/crashed process; only the durable store carries state
	// forward into process2.

	created, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Generation)

	// "Process 2": a freshly constructed replica, holding none of process
	// 1's in-memory state, picks up the very next push. Its git reader must
	// expose both the old and new commit trees so admission's diff engine
	// classifies this as an Update against the existing durable identity,
	// not a fresh Create (which would collide and no-op against the
	// already-existing row).
	current := a
	gitB := newTreeGitReader(&current, map[string]map[string][]byte{
		a: {"files/hero.md": makeFile("hero")},
		b: {"files/hero.md": makeFileWithBody("hero", "Replaced by process 2")},
	})
	current = b
	process2 := newCatalogServer(t, store, gitB)
	_, err = process2.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: a, NewCommitSha: b, CommitSha: b,
		RefName: "refs/heads/main", ChangedPaths: []string{"files/hero.md"},
	})
	require.NoError(t, err)

	updated, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, created.UID, updated.UID, "identity must be preserved across a replica replacement")
	assert.Equal(t, int64(2), updated.Generation, "the replacement replica must correctly compute the next generation from durable state alone")
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, "Replaced by process 2", updated.Body)
}
