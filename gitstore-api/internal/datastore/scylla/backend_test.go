// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/scylladb/gocqlx/v3/table"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

var (
	scyllaAddr     string
	scyllaKeyspace string
)

func TestMain(m *testing.M) {
	scyllaAddr = os.Getenv("GITSTORE_TEST_SCYLLA_ADDR")
	if scyllaAddr == "" {
		scyllaAddr = "127.0.0.1:9042"
	}
	scyllaKeyspace = fmt.Sprintf("gitstore_scylla_test_%d", os.Getpid())

	provisionKeyspace(scyllaAddr, scyllaKeyspace)
	code := m.Run()
	dropKeyspace(scyllaAddr, scyllaKeyspace)

	os.Exit(code)
}

// contactPointTranslator returns an AddressTranslator that redirects all peer
// addresses to the original contact point. This is needed when Scylla runs in
// a Docker container — its rpc_address is an internal Docker IP, but the host
// connects via a forwarded port on the contact-point address.
func contactPointTranslator(contactHost string, contactPort int) gocql.AddressTranslator {
	contactIP := net.ParseIP(contactHost)
	return gocql.AddressTranslatorFunc(func(_ net.IP, port int) (net.IP, int) {
		if contactPort > 0 {
			port = contactPort
		}
		return contactIP, port
	})
}

// provisionKeyspace creates the keyspace using a temporary no-keyspace session.
// This mirrors what the compose scylla-init service does for local/CI stacks.
// Retries for up to 30 s because ScyllaDB logs "Starting listening for CQL clients"
// slightly before it actually accepts connections.
func provisionKeyspace(addr, keyspace string) {
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cluster := gocql.NewCluster(host)
	if port > 0 {
		cluster.Port = port
	}
	cluster.Consistency = gocql.Quorum
	cluster.ConnectTimeout = 5 * time.Second
	cluster.Timeout = 5 * time.Second
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)

	var session *gocql.Session
	var err error
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		session, err = cluster.CreateSession()
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		panic("provisionKeyspace: open session: " + err.Error())
	}
	defer session.Close()

	stmt := fmt.Sprintf(
		`CREATE KEYSPACE IF NOT EXISTS %s `+
			`WITH replication = {'class': 'NetworkTopologyStrategy', 'replication_factor': '1'} `+
			`AND durable_writes = true`,
		keyspace,
	)
	if err := session.Query(stmt).Exec(); err != nil {
		panic("provisionKeyspace: create keyspace: " + err.Error())
	}
	if err := session.AwaitSchemaAgreement(context.Background()); err != nil {
		panic("provisionKeyspace: await schema agreement: " + err.Error())
	}
}

func dropKeyspace(addr, keyspace string) {
	session, err := openRootSession(addr)
	if err != nil {
		return
	}
	defer session.Close()
	_ = session.Query(fmt.Sprintf(`DROP KEYSPACE IF EXISTS %s`, keyspace)).Exec()
}

func openRootSession(addr string) (*gocql.Session, error) {
	host, portStr, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cluster := gocql.NewCluster(host)
	if port > 0 {
		cluster.Port = port
	}
	cluster.Consistency = gocql.Quorum
	cluster.ConnectTimeout = 5 * time.Second
	cluster.Timeout = 5 * time.Second
	cluster.DisableShardAwarePort = true
	cluster.IgnorePeerAddr = true
	cluster.AddressTranslator = contactPointTranslator(host, port)
	return cluster.CreateSession()
}

func newTestStore(t *testing.T) datastore.Datastore {
	return newTestStoreWithWatchBucket(t, 0)
}

func newTestStoreWithWatchBucket(t *testing.T, watchBucketSize int) datastore.Datastore {
	t.Helper()
	host, portStr, splitErr := net.SplitHostPort(scyllaAddr)
	if splitErr != nil {
		host = scyllaAddr
		portStr = "9042"
	}
	port, _ := strconv.Atoi(portStr)
	cfg := config.ScyllaConfig{
		Hosts:                 []string{scyllaAddr},
		Keyspace:              scyllaKeyspace,
		DisableShardAwarePort: true,
		IgnorePeerAddr:        true,
		AddressTranslator:     contactPointTranslator(host, port),
	}
	store, err := scylla.New(cfg, zap.NewNop(), watchBucketSize)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestStores(t *testing.T) (datastore.Datastore, datastore.Datastore) {
	t.Helper()
	return newTestStore(t), newTestStore(t)
}

func newID() string { return uuid.New().String() }

func newProduct(namespace, name string) *datastore.Product {
	return &datastore.Product{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func newCollection(namespace, name string) *datastore.Collection {
	return &datastore.Collection{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Collection",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func newProductVariant(namespace, name, sku, productRefName string) *datastore.ProductVariant {
	return &datastore.ProductVariant{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "ProductVariant",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		SKU:               sku,
		ProductRefName:    productRefName,
	}
}

func newRepository() *datastore.Repository {
	now := time.Now().UTC().Truncate(time.Millisecond)
	uid := newID()
	namespace := "namespace-" + newID()[:8]
	return &datastore.Repository{
		UID:               uid,
		ID:                uid,
		Namespace:         namespace,
		NamespaceID:       namespace,
		Name:              "repo-" + newID()[:8],
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     "test",
		UpdateTimestamp:   now,
		UpdateActor:       "test",
	}
}

func setRepositoryContractFields(
	t *testing.T,
	repository *datastore.Repository,
	generation int64,
	resourceVersion string,
	status json.RawMessage,
) {
	t.Helper()
	value := reflect.ValueOf(repository).Elem()

	generationField := value.FieldByName("Generation")
	if !generationField.IsValid() {
		t.Fatal("datastore.Repository is missing Generation")
	}
	require.Equal(t, reflect.Int64, generationField.Kind())
	generationField.SetInt(generation)

	resourceVersionField := value.FieldByName("ResourceVersion")
	if !resourceVersionField.IsValid() {
		t.Fatal("datastore.Repository is missing ResourceVersion")
	}
	require.Equal(t, reflect.String, resourceVersionField.Kind())
	resourceVersionField.SetString(resourceVersion)

	statusField := value.FieldByName("Status")
	if !statusField.IsValid() {
		t.Fatal("datastore.Repository is missing Status")
	}
	require.Equal(t, reflect.TypeOf(json.RawMessage{}), statusField.Type())
	statusField.Set(reflect.ValueOf(status))
}

func repositoryContractFields(t *testing.T, repository *datastore.Repository) (int64, string, json.RawMessage) {
	t.Helper()
	value := reflect.ValueOf(repository).Elem()

	generationField := value.FieldByName("Generation")
	if !generationField.IsValid() {
		t.Fatal("datastore.Repository is missing Generation")
	}
	require.Equal(t, reflect.Int64, generationField.Kind())

	resourceVersionField := value.FieldByName("ResourceVersion")
	if !resourceVersionField.IsValid() {
		t.Fatal("datastore.Repository is missing ResourceVersion")
	}
	require.Equal(t, reflect.String, resourceVersionField.Kind())

	statusField := value.FieldByName("Status")
	if !statusField.IsValid() {
		t.Fatal("datastore.Repository is missing Status")
	}
	require.Equal(t, reflect.TypeOf(json.RawMessage{}), statusField.Type())

	return generationField.Int(), resourceVersionField.String(), statusField.Interface().(json.RawMessage)
}

// ── Repository ───────────────────────────────────────────────────────────────

func TestScylla_RepositoryResourceContractRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	initialStatus := json.RawMessage(`{
		"observedGeneration": 4,
		"lastAppliedRevision": "abc123",
		"conditions": [{
			"type": "Ready",
			"status": "True",
			"reason": "Reconciled",
			"message": "repository is ready",
			"lastTransitionTime": "2026-08-16T20:00:00Z",
			"observedGeneration": 4
		}]
	}`)
	setRepositoryContractFields(t, repository, 5, "12", initialStatus)

	require.NoError(t, store.CreateRepository(ctx, repository))

	created, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status := repositoryContractFields(t, created)
	assert.Equal(t, int64(5), generation)
	assert.Equal(t, "12", resourceVersion)
	assert.JSONEq(t, string(initialStatus), string(status))

	updatedStatus := json.RawMessage(`{
		"observedGeneration": 6,
		"lastAppliedRevision": "def456",
		"conditions": [{
			"type": "Ready",
			"status": "False",
			"reason": "Reconciling",
			"message": "repository update is pending",
			"lastTransitionTime": "2026-08-16T20:30:00Z",
			"observedGeneration": 6
		}]
	}`)
	setRepositoryContractFields(t, created, 6, "13", updatedStatus)
	require.NoError(t, store.UpdateRepository(ctx, created, "12"))

	updated, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status = repositoryContractFields(t, updated)
	assert.Equal(t, int64(6), generation)
	assert.Equal(t, "13", resourceVersion)
	assert.JSONEq(t, string(updatedStatus), string(status))
}

func TestScylla_RepositoryResourceContractLegacyNormalization(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	legacyRepository := newRepository()

	require.NoError(t, store.CreateRepository(ctx, legacyRepository))

	got, err := store.GetRepository(ctx, legacyRepository.ID)
	require.NoError(t, err)
	generation, resourceVersion, status := repositoryContractFields(t, got)
	assert.Equal(t, int64(1), generation)
	assert.Equal(t, "1", resourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(status))
}

func TestScylla_RepositoryVersionTransitionsPersist(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateRepository(ctx, repository))

	expectedResourceVersion := repository.ResourceVersion
	datastore.AdvanceRepositorySpecVersion(repository)
	require.NoError(t, store.UpdateRepository(ctx, repository, expectedResourceVersion))

	afterSpec, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSpec.Generation)
	assert.Equal(t, "2", afterSpec.ResourceVersion)
	assert.JSONEq(t, `{"observedGeneration":0,"conditions":[]}`, string(afterSpec.Status))

	afterSpec.Status = json.RawMessage(`{
		"observedGeneration": 2,
		"lastAppliedRevision": "main@sha1:abc",
		"conditions": [{
			"type": "Ready",
			"status": "True",
			"observedGeneration": 2,
			"lastTransitionTime": "2026-08-16T20:00:00Z"
		}]
	}`)
	expectedResourceVersion = afterSpec.ResourceVersion
	datastore.AdvanceRepositorySystemVersion(afterSpec)
	require.NoError(t, store.UpdateRepository(ctx, afterSpec, expectedResourceVersion))

	afterSystem, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), afterSystem.Generation)
	assert.Equal(t, "3", afterSystem.ResourceVersion)
	assert.JSONEq(t, string(afterSpec.Status), string(afterSystem.Status))
}

func TestScylla_RepositoryPushPolicyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	repository.MaxPackSizeBytes = 64 * 1024 * 1024
	repository.MaxFileSizeBytes = 8 * 1024 * 1024
	require.NoError(t, store.CreateRepository(ctx, repository))

	created, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, repository.MaxPackSizeBytes, created.MaxPackSizeBytes)
	assert.Equal(t, repository.MaxFileSizeBytes, created.MaxFileSizeBytes)

	expectedResourceVersion := created.ResourceVersion
	created.MaxPackSizeBytes *= 2
	created.MaxFileSizeBytes *= 2
	datastore.AdvanceRepositorySpecVersion(created)
	require.NoError(t, store.UpdateRepository(ctx, created, expectedResourceVersion))

	updated, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, created.MaxPackSizeBytes, updated.MaxPackSizeBytes)
	assert.Equal(t, created.MaxFileSizeBytes, updated.MaxFileSizeBytes)
}

func TestScylla_UpdateRepositoryRejectsStaleResourceVersion(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	repository := newRepository()
	require.NoError(t, store.CreateRepository(ctx, repository))

	first, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	stale, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)

	first.Name = "first-writer"
	datastore.AdvanceRepositorySpecVersion(first)
	require.NoError(t, store.UpdateRepository(ctx, first, "1"))

	stale.Name = "stale-writer"
	datastore.AdvanceRepositorySpecVersion(stale)
	require.ErrorIs(t, store.UpdateRepository(ctx, stale, "1"), datastore.ErrConflict)

	got, err := store.GetRepository(ctx, repository.ID)
	require.NoError(t, err)
	assert.Equal(t, "first-writer", got.Name)
	assert.Equal(t, "2", got.ResourceVersion)
}

func TestScylla_FileRoundTripAndIndexedLookup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	file := &datastore.File{
		UID: newID(), Namespace: "test-ns", Name: "hero-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		Spec: json.RawMessage(`{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero"}}`),
		Body: "alt text", OwnerReferences: json.RawMessage(`[{"kind":"Repository","name":"repo","uid":"owner"}]`),
		Status: json.RawMessage(`{"conditions":[]}`),
	}
	require.NoError(t, store.CreateFile(ctx, file))
	got, err := store.GetFileByName(ctx, file.Namespace, file.Name)
	require.NoError(t, err)
	assert.Equal(t, file.UID, got.UID)
	assert.JSONEq(t, string(file.Spec), string(got.Spec))
	assert.JSONEq(t, string(file.OwnerReferences), string(got.OwnerReferences))
	assert.Equal(t, file.Body, got.Body)
}

func TestScylla_FileOwnerReferenceProjectionUsesOwnerRepositoryScope(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	file := &datastore.File{
		UID: newID(), Namespace: "test-ns", Name: "owned-" + newID()[:8],
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: time.Now().UTC(),
		RepositoryID:    "00000000-0000-0000-0000-000000000002",
		OwnerReferences: json.RawMessage(`[{"kind":"Repository","uid":"owner","repositoryID":"00000000-0000-0000-0000-000000000003","blockOwnerDeletion":true}]`),
	}
	require.NoError(t, store.CreateFile(ctx, file))
	owners := store.(datastore.OwnerReferenceStore)
	blocked, err := owners.HasBlockingOwnerDependents(ctx, datastore.OwnerReferenceScope{Namespace: file.Namespace, RepositoryID: "00000000-0000-0000-0000-000000000003"}, "owner")
	require.NoError(t, err)
	assert.True(t, blocked)
}

// ── Product ───────────────────────────────────────────────────────────────────

func TestScylla_CreateGetProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, p.Namespace, got.Namespace)
}

func TestScylla_CreateProduct_DuplicateUID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "dup-uid-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	err := store.CreateProduct(ctx, p)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateProduct_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-name-" + newID()[:8]
	p1 := newProduct("test-ns", name)
	require.NoError(t, store.CreateProduct(ctx, p1))
	p2 := newProduct("test-ns", name)
	err := store.CreateProduct(ctx, p2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetProductByName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "findable-" + newID()[:8]
	p := newProduct("test-ns", name)
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductByName(ctx, "test-ns", name)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
}

func TestScylla_GetProductByName_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetProductByName(context.Background(), "test-ns", "no-such-product-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListProducts(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "list-ns-" + newID()[:8]
	p1 := newProduct(ns, "p1-"+newID()[:8])
	p2 := newProduct(ns, "p2-"+newID()[:8])
	p3 := newProduct(ns, "p3-"+newID()[:8])

	require.NoError(t, store.CreateProduct(ctx, p1))
	require.NoError(t, store.CreateProduct(ctx, p2))
	require.NoError(t, store.CreateProduct(ctx, p3))

	result, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result.Items), 3)
}

func TestScylla_UpdateProductVariant_UpdatesSKUIndex(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "variant-ns-" + newID()[:8]
	name := "variant-" + newID()[:8]
	oldSKU := "sku-old-" + newID()[:8]
	newSKU := "sku-new-" + newID()[:8]

	v := newProductVariant(ns, name, oldSKU, "product-a")
	require.NoError(t, store.CreateProductVariant(ctx, v))

	v.SKU = newSKU
	v.Generation = 2
	v.ResourceVersion = "2"
	require.NoError(t, store.UpdateProductVariant(ctx, v))

	_, err := store.GetProductVariantBySKU(ctx, ns, oldSKU)
	require.ErrorIs(t, err, datastore.ErrNotFound)

	got, err := store.GetProductVariantBySKU(ctx, ns, newSKU)
	require.NoError(t, err)
	assert.Equal(t, name, got.Name)
	assert.Equal(t, newSKU, got.SKU)
}

func TestScylla_UpdateProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "upd-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	p.GitRef = "main"
	require.NoError(t, store.UpdateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", got.GitRef)
}

func TestScylla_UpdateProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	p := newProduct("test-ns", "ghost-"+newID()[:8])
	p.UID = newID() // does not exist
	err := store.UpdateProduct(context.Background(), p)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	p := newProduct("test-ns", "del-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.UID))

	_, err := store.GetProduct(ctx, p.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_DeleteProduct_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.DeleteProduct(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func newCategoryTaxonomy(ns, name string) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		UID:             newID(),
		Namespace:       ns,
		Name:            name,
		APIVersion:      "catalog.gitstore.dev/v1beta1",
		Kind:            "CategoryTaxonomy",
		Generation:      1,
		ResourceVersion: "1",
	}
}

func TestScylla_CreateGetCategoryTaxonomy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCategoryTaxonomy("test-ns", "cat-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))

	got, err := store.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)

	gotByUID, err := store.GetCategoryTaxonomy(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.Name, gotByUID.Name)
}

func TestScylla_CreateCategoryTaxonomy_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-cat-" + newID()[:8]
	c1 := newCategoryTaxonomy("test-ns", name)
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c1))
	c2 := newCategoryTaxonomy("test-ns", name)
	err := store.CreateCategoryTaxonomy(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCategoryTaxonomy_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCategoryTaxonomyByName(context.Background(), "test-ns", "no-such-cat-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)

	_, err = store.GetCategoryTaxonomy(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCategoryTaxonomies(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCategoryTaxonomies(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)

	c1 := newCategoryTaxonomy("test-ns", "catls1-"+newID()[:8])
	c2 := newCategoryTaxonomy("test-ns", "catls2-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c1))
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c2))

	after, err := store.ListCategoryTaxonomies(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Equal(t, len(before.Items)+2, len(after.Items))
}

func TestScylla_UpdateCategoryTaxonomy(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCategoryTaxonomy("test-ns", "upd-cat-"+newID()[:8])
	require.NoError(t, store.CreateCategoryTaxonomy(ctx, c))
	c.AncestorPath = "electronics"
	require.NoError(t, store.UpdateCategoryTaxonomy(ctx, c))

	got, err := store.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	require.NoError(t, err)
	assert.Equal(t, "electronics", got.AncestorPath)
}

func TestScylla_UpdateCategoryTaxonomy_NotFound(t *testing.T) {
	store := newTestStore(t)
	c := newCategoryTaxonomy("test-ns", "ghost-cat-"+newID()[:8])
	err := store.UpdateCategoryTaxonomy(context.Background(), c)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Collection ────────────────────────────────────────────────────────────────

func TestScylla_CreateGetCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "col-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
	assert.Equal(t, c.Name, got.Name)
	assert.Equal(t, c.Namespace, got.Namespace)
}

func TestScylla_CreateCollection_DuplicateUID(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "dup-uid-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c))
	err := store.CreateCollection(ctx, c)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_CreateCollection_DuplicateName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "dup-col-" + newID()[:8]
	c1 := newCollection("test-ns", name)
	require.NoError(t, store.CreateCollection(ctx, c1))
	c2 := newCollection("test-ns", name)
	err := store.CreateCollection(ctx, c2)
	require.ErrorIs(t, err, datastore.ErrAlreadyExists)
}

func TestScylla_GetCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollection(context.Background(), newID())
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_GetCollectionByName(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	name := "find-col-" + newID()[:8]
	c := newCollection("test-ns", name)
	require.NoError(t, store.CreateCollection(ctx, c))

	got, err := store.GetCollectionByName(ctx, "test-ns", name)
	require.NoError(t, err)
	assert.Equal(t, c.UID, got.UID)
}

func TestScylla_GetCollectionByName_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetCollectionByName(context.Background(), "test-ns", "no-col-"+newID()[:8])
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestScylla_ListCollections(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	before, err := store.ListCollections(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)

	c1 := newCollection("test-ns", "colls1-"+newID()[:8])
	c2 := newCollection("test-ns", "colls2-"+newID()[:8])
	require.NoError(t, store.CreateCollection(ctx, c1))
	require.NoError(t, store.CreateCollection(ctx, c2))

	after, err := store.ListCollections(ctx, "test-ns", datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Equal(t, len(before.Items)+2, len(after.Items))
}

func TestScylla_UpdateCollection(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	c := newCollection("test-ns", "upd-col-"+newID()[:8])
	c.Body = "Before"
	require.NoError(t, store.CreateCollection(ctx, c))
	c.Body = "After"
	require.NoError(t, store.UpdateCollection(ctx, c))

	got, err := store.GetCollection(ctx, c.UID)
	require.NoError(t, err)
	assert.Equal(t, "After", got.Body)
}

func TestScylla_UpdateCollection_NotFound(t *testing.T) {
	store := newTestStore(t)
	err := store.UpdateCollection(context.Background(), newCollection("test-ns", "ghost-col-"+newID()[:8]))
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

// ── Product: three-table schema tests (016-product-spec-hydration) ────────────

func productCursor(p *datastore.Product) string {
	raw := fmt.Sprintf("keyset|%s|%s",
		p.CreationTimestamp.UTC().Format(time.RFC3339Nano), p.UID)
	return base64Encode(raw)
}

func base64Encode(s string) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(s)
	dst := make([]byte, (len(src)+2)/3*4)
	n := 0
	for i := 0; i < len(src); i += 3 {
		var b [3]byte
		b[0] = src[i]
		if i+1 < len(src) {
			b[1] = src[i+1]
		}
		if i+2 < len(src) {
			b[2] = src[i+2]
		}
		remaining := len(src) - i
		dst[n] = enc[(b[0]>>2)&0x3f]
		dst[n+1] = enc[((b[0]&0x03)<<4)|((b[1]>>4)&0x0f)]
		if remaining > 1 {
			dst[n+2] = enc[((b[1]&0x0f)<<2)|((b[2]>>6)&0x03)]
		} else {
			dst[n+2] = '='
		}
		if remaining > 2 {
			dst[n+3] = enc[b[2]&0x3f]
		} else {
			dst[n+3] = '='
		}
		n += 4
	}
	return string(dst[:n])
}

func TestScylla_GetProductByName_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "rt-ns-" + newID()[:8]
	p := newProduct(ns, "findable-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProductByName(ctx, ns, p.Name)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
}

func TestScylla_GetProductByUID_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "uid-rt-ns-" + newID()[:8]
	p := newProduct(ns, "uid-rt-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, p.UID, got.UID)
	assert.Equal(t, p.Name, got.Name)
	assert.Equal(t, ns, got.Namespace)
}

func TestScylla_UpdateProduct_BatchFanOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "upd-fan-" + newID()[:8]
	p := newProduct(ns, "widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))

	p.GitRef = "main"
	p.GitCommitSHA = "abc123"
	require.NoError(t, store.UpdateProduct(ctx, p))

	byUID, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, "main", byUID.GitRef)
	assert.Equal(t, "abc123", byUID.GitCommitSHA)

	byName, err := store.GetProductByName(ctx, ns, p.Name)
	require.NoError(t, err)
	assert.Equal(t, "main", byName.GitRef)

	page, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.Equal(t, "main", page.Items[0].GitRef)
}

func TestScylla_DeleteProduct_BatchFanOut(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "del-fan-" + newID()[:8]
	p := newProduct(ns, "del-widget-"+newID()[:8])
	require.NoError(t, store.CreateProduct(ctx, p))
	require.NoError(t, store.DeleteProduct(ctx, p.UID))

	_, errUID := store.GetProduct(ctx, p.UID)
	assert.ErrorIs(t, errUID, datastore.ErrNotFound)

	_, errName := store.GetProductByName(ctx, ns, p.Name)
	assert.ErrorIs(t, errName, datastore.ErrNotFound)

	page, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 100})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
}

func TestScylla_Product_SpecStatus_RoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "spec-rt-" + newID()[:8]
	p := newProduct(ns, "spec-"+newID()[:8])
	p.Spec = []byte(`{"title":"Widget Pro","tags":["sale"]}`)
	p.Status = []byte(`{"observedGeneration":1,"conditions":[{"type":"READY","status":"TRUE","lastTransitionTime":"2026-01-01T00:00:00Z"}]}`)
	require.NoError(t, store.CreateProduct(ctx, p))

	got, err := store.GetProduct(ctx, p.UID)
	require.NoError(t, err)
	assert.Equal(t, string(p.Spec), string(got.Spec))
	assert.Equal(t, string(p.Status), string(got.Status))
}

func TestScylla_ListProducts_ThreePageForwardCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "paginate-" + newID()[:8]
	total := 25
	for i := 0; i < total; i++ {
		p := newProduct(ns, fmt.Sprintf("item-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	const pageSize = 10
	seen := make(map[string]bool)

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize})
	require.NoError(t, err)
	require.Len(t, r1.Items, 10)
	assert.True(t, r1.HasNext)
	for _, p := range r1.Items {
		require.False(t, seen[p.UID], "duplicate on page 1: %s", p.UID)
		seen[p.UID] = true
	}

	cursor1 := productCursor(r1.Items[len(r1.Items)-1])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize, After: cursor1})
	require.NoError(t, err)
	require.Len(t, r2.Items, 10)
	assert.True(t, r2.HasNext)
	for _, p := range r2.Items {
		require.False(t, seen[p.UID], "duplicate on page 2: %s", p.UID)
		seen[p.UID] = true
	}

	cursor2 := productCursor(r2.Items[len(r2.Items)-1])
	r3, err := store.ListProducts(ctx, ns, datastore.PageParams{First: pageSize, After: cursor2})
	require.NoError(t, err)
	require.Len(t, r3.Items, 5)
	assert.False(t, r3.HasNext)
	for _, p := range r3.Items {
		require.False(t, seen[p.UID], "duplicate on page 3: %s", p.UID)
		seen[p.UID] = true
	}

	assert.Len(t, seen, total)
}

func TestScylla_ListProducts_BackwardCursor(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "back-page-" + newID()[:8]
	for i := 0; i < 15; i++ {
		p := newProduct(ns, fmt.Sprintf("bitem-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{Last: 5})
	require.NoError(t, err)
	require.Len(t, r1.Items, 5)
	assert.True(t, r1.HasPrevious)

	seen := make(map[string]bool)
	for _, p := range r1.Items {
		seen[p.UID] = true
	}

	cursorBefore := productCursor(r1.Items[0])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{Last: 5, Before: cursorBefore})
	require.NoError(t, err)
	require.Len(t, r2.Items, 5)
	for _, p := range r2.Items {
		require.False(t, seen[p.UID], "duplicate on backward page 2: %s", p.UID)
		seen[p.UID] = true
	}
}

func TestScylla_ListProducts_AfterLastCursor_ReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	ns := "empty-page-" + newID()[:8]
	for i := 0; i < 3; i++ {
		p := newProduct(ns, fmt.Sprintf("ep-%03d-%s", i, newID()[:6]))
		p.CreationTimestamp = time.Now().UTC().Add(time.Duration(i) * time.Millisecond).Truncate(time.Millisecond)
		require.NoError(t, store.CreateProduct(ctx, p))
	}

	r1, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 10})
	require.NoError(t, err)
	require.Len(t, r1.Items, 3)
	assert.False(t, r1.HasNext)

	lastCursor := productCursor(r1.Items[len(r1.Items)-1])
	r2, err := store.ListProducts(ctx, ns, datastore.PageParams{First: 10, After: lastCursor})
	require.NoError(t, err)
	assert.Empty(t, r2.Items)
	assert.False(t, r2.HasNext)
}

func TestScylla_NamespaceDirectUID_FullEnvelopeAndBodyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Hour)
	uid := newID()
	namespace := &datastore.Namespace{
		APIVersion:        "gitstore.dev/v1beta1",
		Kind:              "Namespace",
		UID:               uid,
		ID:                uid,
		Name:              "namespace-envelope-" + newID()[:8],
		Generation:        7,
		ResourceVersion:   "11",
		Revision:          "main@sha1:namespace",
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now.Add(time.Minute),
		UpdateActor:       "bob",
		Labels:            map[string]string{"team": "catalog"},
		Annotations:       map[string]string{"gitstore.dev/note": "namespace"},
		OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		SourcePath:        "namespaces/example.md",
		GitCommitSHA:      "namespace-sha",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"title":"Canonical namespace","tier":"USER"}`),
		Body:              "# Namespace\n\nRaw **Markdown** body.\n",
		Status:            json.RawMessage(`{"observedGeneration":7,"conditions":[]}`),
		Title:             "Canonical namespace",
		Tier:              datastore.NamespaceTierUser,
	}

	require.NoError(t, store.CreateNamespace(ctx, namespace))

	got, err := store.GetNamespace(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, namespace, got)

	byName, err := store.GetNamespaceByName(ctx, namespace.Name)
	require.NoError(t, err)
	assert.Equal(t, namespace, byName)
	assert.Equal(t, uid, got.UID)
	assert.Equal(t, namespace.Body, got.Body)
	assert.JSONEq(t, string(namespace.OwnerReferences), string(got.OwnerReferences))
}

func TestScylla_NamespaceRepositoryLifecycleCoordinationAcrossReplicas(t *testing.T) {
	storeA, storeB := newTestStores(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               newID(),
		Name:              "repository-lifecycle-" + newID()[:8],
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, storeA.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{
		UID:               newID(),
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}

	require.NoError(t, storeB.CreateRepositoryInActiveNamespace(ctx, repository))
	current, err := storeA.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	expectedResourceVersion := current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.ErrorIs(t, storeA.MarkNamespaceDeletion(ctx, current, expectedResourceVersion), datastore.ErrNamespaceNotEmpty)

	require.NoError(t, storeB.DeleteRepository(ctx, repository.UID))
	current, err = storeA.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion = current.ResourceVersion
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, storeA.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))

	late := *repository
	late.UID = newID()
	late.Name = "late"
	require.ErrorIs(t, storeB.CreateRepositoryInActiveNamespace(ctx, &late), datastore.ErrNamespaceNotActive)
}

func TestScylla_NamespaceRepositoryLifecycleLegacyNullFence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	namespace := &datastore.Namespace{
		UID:               newID(),
		Name:              "legacy-fence-" + newID()[:8],
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	clearNamespaceRepositoryFence(t, namespace.UID)

	repository := &datastore.Repository{
		UID:               newID(),
		Namespace:         namespace.Name,
		Name:              "catalog",
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, store.CreateRepositoryInActiveNamespace(ctx, repository))
	require.NoError(t, store.DeleteRepository(ctx, repository.UID))

	current, err := store.GetNamespace(ctx, namespace.UID)
	require.NoError(t, err)
	expectedResourceVersion := current.ResourceVersion
	deletedAt := time.Now().UTC().Truncate(time.Millisecond)
	current.DeletionTimestamp = &deletedAt
	datastore.AdvanceNamespaceSystemVersion(current)
	require.NoError(t, store.MarkNamespaceDeletion(ctx, current, expectedResourceVersion))
}

func clearNamespaceRepositoryFence(t *testing.T, namespaceUID string) {
	t.Helper()
	session, err := openRootSession(scyllaAddr)
	require.NoError(t, err)
	t.Cleanup(session.Close)
	uid, err := gocql.ParseUUID(namespaceUID)
	require.NoError(t, err)
	require.NoError(t, session.Query(
		fmt.Sprintf(
			"UPDATE %s.namespaces_by_uid SET repository_creation_epoch=null, pending_repository_creations=null WHERE uid=?",
			scyllaKeyspace,
		),
		uid,
	).Exec())
}

func TestScylla_RepositoryDirectUIDPathReversePath_FullEnvelopeAndBodyRoundTrip(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Hour)
	uid := newID()
	namespace := "repository-envelope-" + newID()[:8]
	repository := &datastore.Repository{
		APIVersion:        "gitstore.dev/v1beta1",
		Kind:              "Repository",
		UID:               uid,
		ID:                uid,
		Namespace:         namespace,
		NamespaceID:       namespace,
		Name:              "canonical-" + newID()[:8],
		Generation:        9,
		ResourceVersion:   "13",
		Revision:          "main@sha1:repository",
		CreationTimestamp: now,
		CreationActor:     "alice",
		UpdateTimestamp:   now.Add(time.Minute),
		UpdateActor:       "bob",
		Labels:            map[string]string{"team": "catalog"},
		Annotations:       map[string]string{"gitstore.dev/note": "repository"},
		OwnerReferences:   json.RawMessage(`[{"apiVersion":"gitstore.dev/v1beta1","kind":"Namespace","name":"owner","uid":"00000000-0000-0000-0000-000000000001"}]`),
		Finalizers:        []string{"gitstore.dev/test"},
		DeletionTimestamp: &deletedAt,
		RepositoryID:      uid,
		SourcePath:        "repositories/canonical.md",
		GitCommitSHA:      "repository-sha",
		GitRef:            "refs/heads/main",
		Spec:              json.RawMessage(`{"defaultBranch":"main","visibility":"PRIVATE"}`),
		Body:              "# Repository\n\nRaw **Markdown** body.\n",
		Status:            json.RawMessage(`{"observedGeneration":9,"conditions":[]}`),
		DefaultBranch:     "main",
		StorageClass:      "default",
		MaxPackSizeBytes:  64 * 1024 * 1024,
		MaxFileSizeBytes:  8 * 1024 * 1024,
	}
	mapping := &datastore.NamespaceMapping{
		Namespace:    namespace,
		NamespaceID:  namespace,
		Name:         repository.Name,
		RepositoryID: uid,
		RepoID:       uid,
	}

	require.NoError(t, store.CreateRepository(ctx, repository))
	require.NoError(t, store.CreateNamespaceMapping(ctx, mapping))

	got, err := store.GetRepository(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, repository, got)
	assert.Equal(t, uid, got.UID)
	assert.Equal(t, namespace, got.Namespace)
	assert.Equal(t, uid, got.RepositoryID)
	assert.Equal(t, repository.Body, got.Body)
	assert.JSONEq(t, string(repository.OwnerReferences), string(got.OwnerReferences))

	byPath, err := store.LookupRepository(ctx, namespace, repository.Name)
	require.NoError(t, err)
	assert.Equal(t, mapping, byPath)

	reverse, err := store.LookupNamespaceByRepoID(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, mapping, reverse)
}

func TestScylla_NamespaceRepositoryQueryShapeMetadata(t *testing.T) {
	tests := []struct {
		name  string
		table interface {
			Metadata() table.Metadata
			Get(...string) (string, []string)
		}
		partKey  []string
		sortKey  []string
		whereKey string
	}{
		{"namespace UID", scylla.NamespaceByUID, []string{"uid"}, nil, "uid"},
		{"namespace name", scylla.NamespaceByName, []string{"name"}, nil, "name"},
		{"repository UID", scylla.RepositoryByUID, []string{"uid"}, nil, "uid"},
		{"repository path", scylla.NamespaceMapping, []string{"namespace"}, []string{"name"}, "namespace"},
		{"repository reverse path", scylla.NamespaceMappingByRepository, []string{"repository_id"}, nil, "repository_id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := tt.table.Metadata()
			assert.Equal(t, tt.partKey, metadata.PartKey)
			assert.Equal(t, tt.sortKey, metadata.SortKey)

			statement, _ := tt.table.Get()
			lower := strings.ToLower(statement)
			assert.Contains(t, lower, "where "+tt.whereKey+"=")
			assert.NotContains(t, lower, "allow filtering")
			assert.NotContains(t, lower, " index ")
		})
	}

	store := newTestStore(t)
	require.NotNil(t, store)
	session := newRawSession(t)
	iter := session.Query(
		`SELECT index_name FROM system_schema.indexes WHERE keyspace_name = ?`,
		scyllaKeyspace,
	).Iter()
	var indexName string
	for iter.Scan(&indexName) {
		lower := strings.ToLower(indexName)
		assert.NotContains(t, lower, "repository")
		assert.NotContains(t, lower, "namespace_mapping")
	}
	require.NoError(t, iter.Close())
}
