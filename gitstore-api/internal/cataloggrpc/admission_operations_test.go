// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslationOperationIdentityRetainsExactAcceptedSource(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	content := []byte(corpus.Valid["product-combined"])
	parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader(content))
	require.NoError(t, err)
	require.Equal(t, content, parsed.RawSource)
	first, supported, err := newParsedEntry("translations/product.de.md", parsed, body, "default")
	require.NoError(t, err)
	require.True(t, supported)
	require.Equal(t, resourceIdentity{APIVersion: "i18n.gitstore.dev/v1beta1", Kind: "Translation",
		Namespace: "acme-store", Name: parsed.Translation.Metadata.Name}, first.identity)
	duplicate, _, err := newParsedEntry(first.path, parsed, body, "default")
	require.NoError(t, err)
	require.Equal(t, first.contentHash, duplicate.contentHash)
	rewritten := strings.Replace(string(content), "kind: Translation", "kind: Translation # retained source comment", 1)
	require.NotEqual(t, string(content), rewritten)
	updated, sameBody, err := validate.NewParser().ParseResource(strings.NewReader(rewritten))
	require.NoError(t, err)
	require.Equal(t, body, sameBody)
	next, supported, err := newParsedEntry(first.path, updated, sameBody, "default")
	require.NoError(t, err)
	require.True(t, supported)
	require.NotEqual(t, first.contentHash, next.contentHash, "byte-distinct accepted source must not disappear as a semantic no-op")
	operations := deriveResourceAdmissionOperations([]*parsedEntry{first}, []*parsedEntry{next}, nil)
	require.Len(t, operations, 1)
	require.Equal(t, admission.OperationUpdate, operations[0].operation)
	require.True(t, operations[0].contentChanged)
	require.Equal(t, content, parsed.RawSource)
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
