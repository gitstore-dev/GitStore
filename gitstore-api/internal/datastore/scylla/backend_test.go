// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package scylla_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
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
	store, err := scylla.New(cfg, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
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
