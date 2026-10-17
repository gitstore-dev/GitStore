// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type boundedAdmissionEntryReader struct {
	started chan string
	release <-chan struct{}
	active  atomic.Int64
	peak    atomic.Int64
}

type batchOnlyAdmissionReader struct {
	GitReader
	readFiles func(context.Context, string, []string, string) ([][]byte, error)
	readPair  func(context.Context, string, []string, string, string) ([][]byte, [][]byte, error)
}

type currentProofAdmissionReader struct {
	GitReader
	proof func(context.Context, string, string, string, string) (bool, []byte, error)
}

func (r *currentProofAdmissionReader) ReadCurrentUnchangedFile(ctx context.Context, repositoryID, path, refName, ancestor string) (bool, []byte, error) {
	return r.proof(ctx, repositoryID, path, refName, ancestor)
}

func TestRepositoryCurrentCheckUsesOneProofWithoutRefOnlyFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		content    []byte
		err        error
		current    bool
		atAncestor bool
	}{
		{name: "proven", content: []byte("manifest"), current: true},
		{name: "exact ancestor receipt", atAncestor: true, current: true},
		{name: "different bytes", content: []byte("different")},
		{name: "path changed", err: grpcstatus.Error(codes.Aborted, "changed")},
		{name: "path absent", err: grpcstatus.Error(codes.NotFound, "absent")},
		{name: "repository absent", err: grpcstatus.Error(codes.FailedPrecondition, "repository unavailable")},
		{name: "storage corrupt", err: grpcstatus.Error(codes.Internal, "corrupt")},
		{name: "permission denied", err: grpcstatus.Error(codes.PermissionDenied, "denied")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			reader := &currentProofAdmissionReader{proof: func(_ context.Context, repo, path, refName, ancestor string) (bool, []byte, error) {
				calls++
				require.Equal(t, "repo", repo)
				require.Equal(t, "repositories/catalog.md", path)
				require.Equal(t, "refs/heads/main", refName)
				require.Equal(t, strings.Repeat("a", 40), ancestor)
				return tc.atAncestor, tc.content, tc.err
			}}
			server := &Server{git: reader}
			current, err := server.isCommittedManifestCurrent(t.Context(), admission.CommittedManifestRequest{
				RepositoryID: "repo", Path: "repositories/catalog.md", RefName: "refs/heads/main",
				CommitSHA: strings.Repeat("a", 40), Content: []byte("manifest"),
			})
			require.Equal(t, 1, calls)
			require.Equal(t, tc.current, current)
			if tc.err != nil && grpcstatus.Code(tc.err) != codes.Aborted && grpcstatus.Code(tc.err) != codes.NotFound {
				require.ErrorIs(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	server := &Server{git: &batchOnlyAdmissionReader{}}
	current, err := server.isCommittedManifestCurrent(t.Context(), admission.CommittedManifestRequest{})
	require.False(t, current)
	require.ErrorContains(t, err, "cannot prove current unchanged")
}

func (r *batchOnlyAdmissionReader) ReadFiles(ctx context.Context, repositoryID string, paths []string, commitSHA string) ([][]byte, error) {
	return r.readFiles(ctx, repositoryID, paths, commitSHA)
}

func (r *batchOnlyAdmissionReader) ReadFilePair(ctx context.Context, repositoryID string, paths []string, primaryCommit, secondaryCommit, refName string) ([][]byte, [][]byte, error) {
	if r.readPair != nil {
		return r.readPair(ctx, repositoryID, paths, primaryCommit, secondaryCommit)
	}
	first, err := r.ReadFiles(ctx, repositoryID, paths, primaryCommit)
	if err != nil {
		return nil, nil, err
	}
	second, err := r.ReadFiles(ctx, repositoryID, paths, secondaryCommit)
	return first, second, err
}

func TestAdmissionSnapshotPairIsOrderedBoundedAndFailsClosed(t *testing.T) {
	oldCommit, newCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	for count := 2; count <= 4; count++ {
		for _, scenario := range []string{"complete", "absence", "wire error", "old cardinality", "new cardinality", "cancelled"} {
			t.Run(fmt.Sprintf("%d/%s", count, scenario), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				paths := make([]string, count)
				for i := range paths {
					paths[i] = fmt.Sprintf("products/p%d.md", i)
				}
				input := append([]string{paths[count-1]}, paths...)
				before := append([]string(nil), input...)
				calls := 0
				reader := &batchOnlyAdmissionReader{readPair: func(_ context.Context, repo string, actual []string, old, current string) ([][]byte, [][]byte, error) {
					calls++
					require.Equal(t, "repository", repo)
					require.Equal(t, paths, actual)
					require.Equal(t, oldCommit, old)
					require.Equal(t, newCommit, current)
					if scenario == "wire error" {
						return nil, nil, grpcstatus.Error(codes.Unavailable, "pair failed")
					}
					first, second := make([][]byte, count), make([][]byte, count)
					for i := range first {
						first[i] = []byte(productDoc(fmt.Sprintf("p%d", i), "Product", ""))
						second[i] = append(append([]byte(nil), first[i]...), []byte("\nchanged\n")...)
					}
					switch scenario {
					case "absence":
						first[0], second[count-1] = nil, nil
					case "old cardinality":
						first = first[:count-1]
					case "new cardinality":
						second = second[:count-1]
					case "cancelled":
						cancel()
					}
					return first, second, nil
				}}
				server := &Server{git: reader, parser: validate.NewParser(), log: zap.NewNop()}
				first, second, refChecked, err := server.loadParsedEntryPair(ctx, "repository", oldCommit, newCommit, "gitstore", input, "")
				require.False(t, refChecked)
				require.Equal(t, 1, calls)
				require.Equal(t, before, input)
				switch scenario {
				case "wire error", "old cardinality", "new cardinality", "cancelled":
					code := codes.Internal
					if scenario == "wire error" {
						code = codes.Unavailable
					}
					if scenario == "cancelled" {
						code = codes.Canceled
					}
					require.Equal(t, code, grpcstatus.Code(err))
					require.Nil(t, first)
					require.Nil(t, second)
				default:
					require.NoError(t, err)
					expected := count
					if scenario == "absence" {
						expected--
					}
					require.Len(t, first, expected)
					require.Len(t, second, expected)
					if scenario == "complete" {
						for i := range first {
							require.Equal(t, paths[i], first[i].path)
							require.Equal(t, paths[i], second[i].path)
							require.NotEqual(t, first[i].sourceHash, second[i].sourceHash)
						}
					}
				}
			})
		}
	}
}

func TestAdmissionSmallDeltaUsesOneImmutableSnapshotAndFailsClosed(t *testing.T) {
	commit := strings.Repeat("a", 40)
	for _, scenario := range []string{"complete", "missing path", "wire error", "wrong cardinality", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int64
			reader := &batchOnlyAdmissionReader{readFiles: func(_ context.Context, repositoryID string, paths []string, ref string) ([][]byte, error) {
				calls.Add(1)
				assert.Equal(t, "repository", repositoryID)
				assert.Equal(t, commit, ref)
				assert.Equal(t, []string{"products/a.md", "products/b.md"}, paths)
				switch scenario {
				case "wire error":
					return nil, grpcstatus.Error(codes.Unavailable, "owned snapshot read failed")
				case "wrong cardinality":
					return [][]byte{[]byte(productDoc("a", "Product", ""))}, nil
				case "cancelled":
					cancel()
				}
				second := []byte(productDoc("b", "Product", ""))
				if scenario == "missing path" {
					second = nil
				}
				return [][]byte{[]byte(productDoc("a", "Product", "")), second}, nil
			}}
			server := &Server{git: reader, parser: validate.NewParser(), log: zap.NewNop()}
			paths := []string{"products/b.md", "products/a.md", "products/a.md"}
			before := append([]string(nil), paths...)
			entries, err := server.loadParsedEntries(ctx, "repository", commit, "gitstore", paths)
			require.Equal(t, int64(1), calls.Load())
			require.Equal(t, before, paths)
			switch scenario {
			case "wire error":
				require.Equal(t, codes.Unavailable, grpcstatus.Code(err))
				require.Nil(t, entries)
			case "wrong cardinality":
				require.Equal(t, codes.Internal, grpcstatus.Code(err))
				require.Nil(t, entries)
			case "cancelled":
				require.Equal(t, codes.Canceled, grpcstatus.Code(err))
				require.Nil(t, entries)
			default:
				require.NoError(t, err)
				expected := 2
				if scenario == "missing path" {
					expected = 1
				}
				require.Len(t, entries, expected)
				require.Equal(t, "products/a.md", entries[0].path)
			}
		})
	}
}

func (r *boundedAdmissionEntryReader) ListFiles(context.Context, string, string, string) ([]string, error) {
	return nil, fmt.Errorf("delta admission must not enumerate the tree")
}

func (r *boundedAdmissionEntryReader) ResolveRef(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("entry reads must use the supplied immutable commit")
}

func (r *boundedAdmissionEntryReader) ReadFiles(ctx context.Context, repositoryID string, paths []string, commitSHA string) ([][]byte, error) {
	results := make([][]byte, len(paths))
	for i, path := range paths {
		var err error
		results[i], err = r.ReadFile(ctx, repositoryID, path, commitSHA)
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (r *boundedAdmissionEntryReader) ReadFilePair(ctx context.Context, repositoryID string, paths []string, primaryCommit, secondaryCommit, refName string) ([][]byte, [][]byte, error) {
	first, err := r.ReadFiles(ctx, repositoryID, paths, primaryCommit)
	if err != nil {
		return nil, nil, err
	}
	second, err := r.ReadFiles(ctx, repositoryID, paths, secondaryCommit)
	return first, second, err
}

func (r *boundedAdmissionEntryReader) ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	if repositoryID != "immutable-repository" || ref != strings.Repeat("a", 40) {
		return nil, fmt.Errorf("entry read changed the repository or immutable commit")
	}
	active := r.active.Add(1)
	defer r.active.Add(-1)
	for previous := r.peak.Load(); active > previous; previous = r.peak.Load() {
		if r.peak.CompareAndSwap(previous, active) {
			break
		}
	}
	r.started <- path
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.release:
		return []byte(productDoc(strings.TrimSuffix(strings.TrimPrefix(path, "products/"), ".md"), "Product", "")), nil
	}
}

func TestAdmissionDeltaEntryReadsBoundFanoutAndDrainCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			release := make(chan struct{})
			reader := &boundedAdmissionEntryReader{started: make(chan string, 8), release: release}
			server := &Server{git: reader, parser: validate.NewParser(), log: zap.NewNop()}
			paths := []string{"products/h.md", "products/g.md", "products/f.md", "products/e.md",
				"products/d.md", "products/c.md", "products/b.md", "products/a.md", "products/a.md"}
			original := append([]string(nil), paths...)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			var entries []*parsedEntry
			go func() {
				var err error
				entries, err = server.loadParsedEntries(ctx, "immutable-repository", strings.Repeat("a", 40), "gitstore", paths)
				done <- err
			}()
			for range 4 {
				select {
				case <-reader.started:
				case <-time.After(time.Second):
					t.Fatal("immutable batch did not start four concurrent reads")
				}
			}
			require.EqualValues(t, 4, reader.active.Load())
			select {
			case <-reader.started:
				t.Fatal("immutable batch exceeded four reads")
			case <-time.After(20 * time.Millisecond):
			}
			if cancelled {
				cancel()
			} else {
				close(release)
			}
			select {
			case err := <-done:
				if cancelled {
					require.Error(t, err)
					require.Nil(t, entries, "cancelled snapshots cannot produce partial admission")
				} else {
					require.NoError(t, err)
					require.Len(t, entries, 8)
					for index, entry := range entries {
						require.Equal(t, fmt.Sprintf("products/%c.md", 'a'+index), entry.path)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("immutable batch retained reads after completion or cancellation")
			}
			require.Zero(t, reader.active.Load())
			require.EqualValues(t, 4, reader.peak.Load())
			require.Equal(t, original, paths, "caller-owned changed paths must remain untouched")
		})
	}
}

func TestOwnerAnnotationRemovalRequiresTransferAuthorization(t *testing.T) {
	recorder := testutil.NewAllowAllAuthZ()
	server := &Server{registry: auth.NewProviderRegistry(nil, recorder, nil)}
	entry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", ""))
	existing := &datastore.Product{Namespace: "gitstore", Name: "widget", CreationActor: "alice", Annotations: map[string]string{datastore.OwnerAnnotationKey: "system:group:editors"}}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
	require.NoError(t, server.authorizeOperation(ctx, entry.identity, admission.OperationUpdate, entry, existing))
	require.Len(t, recorder.Calls, 2)
	require.Equal(t, "product.transfer", recorder.Calls[0].Action)
	require.Equal(t, "system:group:editors", recorder.Calls[0].Resource.OwnerSub)
	require.Equal(t, map[string]any{"kind": "User", "name": "alice"}, recorder.Calls[0].Resource.Attrs["targetOwnerRef"])
	require.Equal(t, "product.update", recorder.Calls[1].Action)
}

func TestSmallFileAuthorizationBatchFreshFactsAndFailures(t *testing.T) {
	for _, scenario := range []string{"complete", "absent", "storage error", "cardinality", "namespace", "name", "uid", "cancelled", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			recorder := testutil.NewAllowAllAuthZ()
			registry := auth.NewProviderRegistry(nil, recorder, nil)
			ctx, cancel := context.WithCancel(auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "actor", AuthMethod: "static-users"}))
			defer cancel()
			calls, owner := 0, "alice"
			store := &testutil.StubStore{
				GetFileByNameFunc: func(context.Context, string, string) (*datastore.File, error) {
					t.Fatal("small batch must not issue point reads or retry after failure")
					return nil, nil
				},
				GetFileSnapshotsByNameFunc: func(_ context.Context, namespace string, names []string) ([]*datastore.File, error) {
					calls++
					require.Equal(t, "catalog", namespace)
					require.Equal(t, []string{"first", "second", "third", "fourth"}, names)
					files := make([]*datastore.File, len(names))
					for i, name := range names {
						files[i] = &datastore.File{UID: fmt.Sprint(i), Namespace: namespace, Name: name, CreationActor: "alice", Annotations: map[string]string{datastore.OwnerAnnotationKey: owner}}
					}
					switch scenario {
					case "absent":
						files[1] = nil
					case "storage error":
						return nil, fmt.Errorf("storage unavailable")
					case "cardinality":
						files = files[:1]
					case "namespace":
						files[1].Namespace = "other"
					case "name":
						files[1].Name = "other"
					case "uid":
						files[1].UID = ""
					case "cancelled":
						cancel()
					case "revoked":
						registry.Swap(nil, testutil.NewDenyAllAuthZ(t), nil)
					}
					return files, nil
				},
			}
			server := &Server{store: store, registry: registry}
			operations := make([]resourceAdmissionOperation, datastore.FileNameSnapshotBatchLimit)
			for i, name := range []string{"first", "second", "third", "fourth"} {
				operations[i] = resourceAdmissionOperation{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "catalog", Name: name}}
			}
			err := server.authorizeOperations(ctx, operations)
			require.Equal(t, 1, calls)
			if scenario == "complete" || scenario == "absent" {
				require.NoError(t, err)
				require.Len(t, recorder.Calls, 4)
				for i, call := range recorder.Calls {
					require.Equal(t, "file.update", call.Action)
					require.Equal(t, operations[i].identity.Name, call.Resource.Name)
					require.Equal(t, "catalog", call.Resource.Attrs["namespace"])
					if scenario == "absent" && i == 1 {
						require.Empty(t, call.Resource.OwnerSub)
					} else {
						require.Equal(t, owner, call.Resource.OwnerSub)
					}
				}
				owner = "charlie"
				require.NoError(t, server.authorizeOperations(ctx, operations))
				require.Equal(t, 2, calls, "each authorization boundary reads fresh facts")
				require.Equal(t, "charlie", recorder.Calls[4].Resource.OwnerSub)
			} else if scenario == "revoked" {
				require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err))
			} else {
				require.Equal(t, codes.Unavailable, grpcstatus.Code(err))
				require.Empty(t, recorder.Calls)
			}
		})
	}
}

func TestFileAuthorizationBatchRetainsOtherReadShapes(t *testing.T) {
	for _, scenario := range []string{"single", "larger", "mixed", "cross namespace"} {
		t.Run(scenario, func(t *testing.T) {
			pointCalls := 0
			point := func(_ context.Context, _, _ string) (*datastore.File, error) {
				pointCalls++
				return nil, datastore.ErrNotFound
			}
			store := &testutil.StubStore{GetFileByNameFunc: point,
				GetFileSnapshotsByNameFunc: func(context.Context, string, []string) ([]*datastore.File, error) {
					t.Fatal("this shape must retain individual reads")
					return nil, nil
				}}
			count := 2
			if scenario == "single" {
				count = 1
			} else if scenario == "larger" {
				count = datastore.FileNameSnapshotBatchLimit + 1
			}
			operations := make([]resourceAdmissionOperation, count)
			for i := range operations {
				operations[i] = resourceAdmissionOperation{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "catalog", Name: fmt.Sprint(i)}}
			}
			if scenario == "mixed" {
				operations[1].identity.Kind = "Product"
			} else if scenario == "cross namespace" {
				operations[1].identity.Namespace = "other"
			}
			recorder := testutil.NewAllowAllAuthZ()
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, recorder, nil)}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "actor", AuthMethod: "static-users"})
			require.NoError(t, server.authorizeOperations(ctx, operations))
			expectedPoints := count
			if scenario == "mixed" {
				expectedPoints--
			}
			require.Equal(t, expectedPoints, pointCalls)
			require.Len(t, recorder.Calls, count)
		})
	}
}

func TestOwnerOnlyChangeNeedsTransferWithoutUpdate(t *testing.T) {
	recorder := testutil.NewAllowAllAuthZ()
	server := &Server{registry: auth.NewProviderRegistry(nil, recorder, nil)}
	entry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", ""))
	spec, err := json.Marshal(entry.parsed.Product.Spec)
	require.NoError(t, err)
	existing := &datastore.Product{APIVersion: entry.identity.APIVersion, Kind: "Product", Namespace: "gitstore", Name: "widget", SourcePath: entry.path, CreationActor: "alice", Spec: spec, Annotations: map[string]string{datastore.OwnerAnnotationKey: "system:group:editors"}}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
	require.NoError(t, server.authorizeOperation(ctx, entry.identity, admission.OperationUpdate, entry, existing))
	require.Len(t, recorder.Calls, 1)
	require.Equal(t, "product.transfer", recorder.Calls[0].Action)
}

func mustParsedEntry(t *testing.T, path, content string) *parsedEntry {
	t.Helper()
	parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	require.NotNil(t, parsed)
	entry, ok, err := newParsedEntry(path, parsed, body, "gitstore", []byte(content))
	require.NoError(t, err)
	require.True(t, ok)
	return entry
}

func productDoc(name, title, body string) string {
	return "---\n" +
		"apiVersion: catalog.gitstore.dev/v1beta1\n" +
		"kind: Product\n" +
		"metadata:\n" +
		"  name: " + name + "\n" +
		"spec:\n" +
		"  title: " + title + "\n" +
		"---\n" +
		body
}

func collectionDoc(name string) string {
	return "---\n" +
		"apiVersion: catalog.gitstore.dev/v1beta1\n" +
		"kind: Collection\n" +
		"metadata:\n" +
		"  name: " + name + "\n" +
		"spec:\n" +
		"  title: " + name + "\n" +
		"---\n"
}

func TestDeriveResourceAdmissionOperations_Create(t *testing.T) {
	newEntry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", "body"))

	ops := deriveResourceAdmissionOperations(nil, []*parsedEntry{newEntry}, nil)

	require.Len(t, ops, 1)
	assert.Equal(t, admission.OperationCreate, ops[0].operation)
	assert.Equal(t, "Product", ops[0].identity.Kind)
	assert.Equal(t, "widget", ops[0].identity.Name)
}

func TestDeriveResourceAdmissionOperations_Update(t *testing.T) {
	oldEntry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", "old"))
	newEntry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", "new"))

	ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, []*parsedEntry{newEntry}, nil)

	require.Len(t, ops, 1)
	assert.Equal(t, admission.OperationUpdate, ops[0].operation)
	assert.True(t, ops[0].contentChanged)
	assert.False(t, ops[0].pathChanged)
}

func TestDeriveResourceAdmissionOperations_ChangedPathPreservesProvenance(t *testing.T) {
	for _, kind := range []string{"Namespace", "Repository", "Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
		t.Run(kind, func(t *testing.T) {
			identity := resourceIdentity{APIVersion: "gitstore.dev/v1beta1", Kind: kind, Namespace: "gitstore", Name: "widget"}
			path := strings.ToLower(kind) + "/widget.md"
			oldEntry := &parsedEntry{path: path, identity: identity, contentHash: "same-parsed-content"}
			newEntry := &parsedEntry{path: path, identity: identity, contentHash: oldEntry.contentHash}
			for _, changedPaths := range [][]string{nil, {"unrelated.md"}, {path, path}} {
				ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, []*parsedEntry{newEntry}, changedPaths)
				if len(changedPaths) == 0 || changedPaths[0] != path {
					require.Empty(t, ops, "full-tree or unrelated-path admission must not rewrite unchanged resources")
					continue
				}
				require.Len(t, ops, 1, "an explicitly changed Git path must advance provenance even if parsed author state is unchanged")
				require.Equal(t, admission.OperationUpdate, ops[0].operation)
				require.Same(t, newEntry, ops[0].newEntry)
				require.False(t, ops[0].contentChanged)
				require.False(t, ops[0].pathChanged)
			}
		})
	}
}

func TestDeriveResourceAdmissionOperations_SourceOnlyChangeRequiresUpdate(t *testing.T) {
	oldContent := productDoc("widget", "Widget", "body")
	newContent := strings.Replace(oldContent, "---\n", "---\n# formatting-only rewrite\n", 1)
	oldEntry := mustParsedEntry(t, "products/widget.md", oldContent)
	newEntry := mustParsedEntry(t, oldEntry.path, newContent)
	require.Equal(t, oldEntry.contentHash, newEntry.contentHash)
	require.NotEqual(t, oldEntry.sourceHash, newEntry.sourceHash)
	ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, []*parsedEntry{newEntry}, nil)
	require.Len(t, ops, 1, "pre-receive full-tree validation must authorize the same provenance-only update as admission")
	require.Equal(t, admission.OperationUpdate, ops[0].operation)
	require.False(t, ops[0].contentChanged)
	require.False(t, ops[0].pathChanged)
}

func TestDeriveResourceAdmissionOperations_Delete(t *testing.T) {
	oldEntry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", "body"))

	ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, nil, nil)

	require.Len(t, ops, 1)
	assert.Equal(t, admission.OperationDelete, ops[0].operation)
	assert.Equal(t, "widget", ops[0].identity.Name)
}

func TestDeriveResourceAdmissionOperations_MovePreservesIdentityAsUpdate(t *testing.T) {
	oldEntry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", "body"))
	newEntry := mustParsedEntry(t, "catalog/products/widget.md", productDoc("widget", "Widget", "body"))

	ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, []*parsedEntry{newEntry}, nil)

	require.Len(t, ops, 1)
	assert.Equal(t, admission.OperationUpdate, ops[0].operation)
	assert.True(t, ops[0].pathChanged)
	assert.False(t, ops[0].contentChanged)
}

func TestDeriveResourceAdmissionOperations_MetadataNameChangeDeleteCreate(t *testing.T) {
	oldEntry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Widget", "body"))
	newEntry := mustParsedEntry(t, "products/widget.md", productDoc("gadget", "Gadget", "body"))

	ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, []*parsedEntry{newEntry}, nil)

	require.Len(t, ops, 2)
	assert.Equal(t, admission.OperationDelete, ops[0].operation)
	assert.Equal(t, "widget", ops[0].identity.Name)
	assert.Equal(t, admission.OperationCreate, ops[1].operation)
	assert.Equal(t, "gadget", ops[1].identity.Name)
}

func TestDeriveResourceAdmissionOperations_KindChangeDeleteCreate(t *testing.T) {
	oldEntry := mustParsedEntry(t, "catalog/item.md", productDoc("item", "Item", "body"))
	newEntry := mustParsedEntry(t, "catalog/item.md", collectionDoc("item"))

	ops := deriveResourceAdmissionOperations([]*parsedEntry{oldEntry}, []*parsedEntry{newEntry}, nil)

	require.Len(t, ops, 2)
	assert.Equal(t, admission.OperationDelete, ops[0].operation)
	assert.Equal(t, "Product", ops[0].identity.Kind)
	assert.Equal(t, admission.OperationCreate, ops[1].operation)
	assert.Equal(t, "Collection", ops[1].identity.Kind)
}
