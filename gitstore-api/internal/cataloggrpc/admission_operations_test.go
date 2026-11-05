// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type catalogBatchSelectionStore struct {
	datastore.Datastore
	entered bool
}

func (s *catalogBatchSelectionStore) WithCatalogMutationBatch(context.Context, string, func(context.Context) error) error {
	s.entered = true
	return errors.New("unexpected catalog batch")
}

func TestFileMutationBatchPreservesSequentialFallback(t *testing.T) {
	for _, shape := range []string{"single", "oversized", "mixed", "delete", "cross-namespace", "duplicate-physical-name", "unowned"} {
		t.Run(shape, func(t *testing.T) {
			count := 2
			if shape == "single" {
				count = 1
			} else if shape == "oversized" {
				count = datastore.FileNameSnapshotBatchLimit + 1
			}
			ops := make([]resourceAdmissionOperation, count)
			for i := range ops {
				identity := resourceIdentity{APIVersion: "v1", Kind: "File", Namespace: "gitstore", Name: strconv.Itoa(i)}
				ops[i] = resourceAdmissionOperation{
					operation: admission.OperationCreate, identity: identity,
					newEntry: &parsedEntry{identity: identity, parsed: &validate.ParsedResource{Kind: "File", File: &catalog.FileResource{}}},
				}
			}
			admCtx := AdmissionContext{Namespace: "gitstore", RepositoryID: "repository"}
			switch shape {
			case "mixed":
				ops[1].identity.Kind = "Product"
			case "delete":
				ops[1].operation = admission.OperationDelete
			case "cross-namespace":
				ops[1].identity.Namespace = "other"
			case "duplicate-physical-name":
				ops[1].identity.Name, ops[1].identity.APIVersion = ops[0].identity.Name, "v2"
			case "unowned":
				admCtx.RepositoryID = ""
			}
			store := &catalogBatchSelectionStore{Datastore: &testutil.StubStore{
				GetFileByNameFunc: func(context.Context, string, string) (*datastore.File, error) {
					return nil, datastore.ErrNotFound
				},
				GetNamespaceByNameFunc: func(_ context.Context, name string) (*datastore.Namespace, error) {
					return &datastore.Namespace{UID: "namespace", Name: name, CreationActor: "alice"}, nil
				},
			}}
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
			err := server.applyResourceOperations(ctx, ops, admCtx)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.False(t, store.entered, "ordering-sensitive and unsupported groups must not share a reservation")
		})
	}
}

func TestFileAuthorizationUsesFreshBoundedSnapshots(t *testing.T) {
	recorder := testutil.NewAllowAllAuthZ()
	reads, pointReads := 0, 0
	owner := "alice"
	store := &testutil.StubStore{
		GetFileSnapshotsByNameFunc: func(_ context.Context, namespace string, names []string) ([]*datastore.File, error) {
			reads++
			require.Equal(t, "gitstore", namespace)
			require.Equal(t, []string{"first", "second"}, names)
			return []*datastore.File{
				{UID: "first-uid", Namespace: namespace, Name: names[0], CreationActor: owner},
				{UID: "second-uid", Namespace: namespace, Name: names[1], CreationActor: "carol"},
			}, nil
		},
		GetFileByNameFunc: func(context.Context, string, string) (*datastore.File, error) {
			pointReads++
			return nil, datastore.ErrNotFound
		},
	}
	server := &Server{store: store, registry: auth.NewProviderRegistry(nil, recorder, nil)}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
	operations := []resourceAdmissionOperation{
		{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "gitstore", Name: "first"}},
		{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "gitstore", Name: "second"}},
	}
	require.NoError(t, server.authorizeOperations(ctx, operations))
	owner = "new-owner"
	require.NoError(t, server.authorizeOperations(ctx, operations))
	require.Equal(t, 2, reads)
	require.Zero(t, pointReads)
	require.Len(t, recorder.Calls, 4)
	require.Equal(t, "alice", recorder.Calls[0].Resource.OwnerSub)
	require.Equal(t, "new-owner", recorder.Calls[2].Resource.OwnerSub)
	require.Equal(t, "carol", recorder.Calls[1].Resource.OwnerSub)
	require.Equal(t, "file.update", recorder.Calls[0].Action)
}

func TestFileAuthorizationRejectsInvalidSnapshotFacts(t *testing.T) {
	for _, scenario := range []string{"cardinality", "namespace", "name", "uid", "error", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			recorder := testutil.NewAllowAllAuthZ()
			store := &testutil.StubStore{GetFileSnapshotsByNameFunc: func(context.Context, string, []string) ([]*datastore.File, error) {
				files := []*datastore.File{
					{UID: "first-uid", Namespace: "gitstore", Name: "first"},
					{UID: "second-uid", Namespace: "gitstore", Name: "second"},
				}
				switch scenario {
				case "cardinality":
					return files[:1], nil
				case "namespace":
					files[0].Namespace = "other"
				case "name":
					files[0].Name = "other"
				case "uid":
					files[0].UID = ""
				case "error":
					return nil, errors.New("unavailable")
				case "cancelled":
					cancel()
				}
				return files, nil
			}}
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, recorder, nil)}
			ctx = auth.ContextWithPrincipal(ctx, &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
			err := server.authorizeOperations(ctx, []resourceAdmissionOperation{
				{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "gitstore", Name: "first"}},
				{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "gitstore", Name: "second"}},
			})
			require.Equal(t, codes.Unavailable, status.Code(err))
			require.Empty(t, recorder.Calls)
		})
	}
}

func TestFileAuthorizationSnapshotStillRequiresProviderApproval(t *testing.T) {
	recorder := testutil.NewDenyAllAuthZ(t)
	store := &testutil.StubStore{GetFileSnapshotsByNameFunc: func(_ context.Context, namespace string, names []string) ([]*datastore.File, error) {
		return []*datastore.File{
			{UID: "first-uid", Namespace: namespace, Name: names[0]},
			{UID: "second-uid", Namespace: namespace, Name: names[1]},
		}, nil
	}}
	server := &Server{store: store, registry: auth.NewProviderRegistry(nil, recorder, nil)}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
	err := server.authorizeOperations(ctx, []resourceAdmissionOperation{
		{operation: admission.OperationUpdate, identity: resourceIdentity{Kind: "File", Namespace: "gitstore", Name: "first"}},
		{operation: admission.OperationDelete, identity: resourceIdentity{Kind: "File", Namespace: "gitstore", Name: "second"}},
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Len(t, recorder.Calls, 1)
	require.Equal(t, "file.update", recorder.Calls[0].Action)
}

func TestFileAuthorizationKeepsPointReadsOutsideSnapshotScope(t *testing.T) {
	for _, namespaces := range [][]string{{"one"}, {"one", "two"}, {"one", "one", "one", "one", "one"}} {
		t.Run(strings.Join(namespaces, "-"), func(t *testing.T) {
			reads := 0
			store := &testutil.StubStore{
				GetFileSnapshotsByNameFunc: func(context.Context, string, []string) ([]*datastore.File, error) {
					t.Fatal("snapshot read outside bounded same-namespace scope")
					return nil, nil
				},
				GetFileByNameFunc: func(_ context.Context, namespace, name string) (*datastore.File, error) {
					reads++
					return &datastore.File{UID: name + "-uid", Namespace: namespace, Name: name}, nil
				},
			}
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "bob", AuthMethod: "static-users"})
			operations := make([]resourceAdmissionOperation, len(namespaces))
			for i, namespace := range namespaces {
				operations[i] = resourceAdmissionOperation{
					operation: admission.OperationUpdate,
					identity:  resourceIdentity{Kind: "File", Namespace: namespace, Name: strconv.Itoa(i)},
				}
			}
			require.NoError(t, server.authorizeOperations(ctx, operations))
			require.Equal(t, len(namespaces), reads)
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
	entry, ok, err := newParsedEntry(path, parsed, body, "gitstore")
	require.NoError(t, err)
	require.True(t, ok)
	return entry
}

func productDoc(name, title, body string) string {
	return "---\n" +
		"apiVersion: catalog.gitstore.dev/v1beta1\n" +
		"kind: Product\n" +
		"metadata:\n  namespace: gitstore\n" +
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
		"metadata:\n  namespace: gitstore\n" +
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
