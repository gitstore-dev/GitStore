// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
	"github.com/scylladb/gocqlx/v3"
	"github.com/scylladb/gocqlx/v3/qb"
	"github.com/scylladb/gocqlx/v3/table"
	"go.uber.org/zap"
)

// scyllaDatastore implements datastore.Datastore backed by ScyllaDB.
type scyllaDatastore struct {
	session                     gocqlx.Session
	log                         *zap.Logger
	productByNamespaceTable     *table.Table
	productByNameTable          *table.Table
	productByUIDTable           *table.Table
	categoryTaxonomyTable       *table.Table
	categoryTaxonomyByNameTable *table.Table
	categoryTaxonomyByUIDTable  *table.Table
	collectionTable             *table.Table
	collectionByNameTable       *table.Table
	collectionByUIDTable        *table.Table
	namespaceTable              *table.Table
	repositoryTable             *table.Table
	namespaceMappingTable       *table.Table
}

// row structs mirror the CQL columns.

// productRow mirrors the columns of products_by_namespace.
type productRow struct {
	Namespace         string            `db:"namespace"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerRefs         string            `db:"owner_refs"`
	GitCommitSHA      string            `db:"git_commit_sha"`
	GitRef            string            `db:"git_ref"`
	Spec              string            `db:"spec"`
	Body              string            `db:"body"`
	Status            string            `db:"status"`
}

// productNameRow mirrors products_by_name (index only).
type productNameRow struct {
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

// productUIDRow mirrors products_by_uid (index only).
type productUIDRow struct {
	UID               gocql.UUID `db:"uid"`
	Namespace         string     `db:"namespace"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type categoryTaxonomyRow struct {
	Namespace         string            `db:"namespace"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	UID               string            `db:"uid"`
	Name              string            `db:"name"`
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	ParentName        string            `db:"parent_name"`
	AncestorPath      string            `db:"ancestor_path"`
	GitCommitSHA      string            `db:"git_commit_sha"`
	GitRef            string            `db:"git_ref"`
	Spec              string            `db:"spec"`
	Body              string            `db:"body"`
	Status            string            `db:"status"`
}

// categoryTaxonomyNameRow mirrors category_taxonomy_by_name (index only).
type categoryTaxonomyNameRow struct {
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

// categoryTaxonomyUIDRow mirrors category_taxonomy_by_uid (index only).
type categoryTaxonomyUIDRow struct {
	UID               gocql.UUID `db:"uid"`
	Namespace         string     `db:"namespace"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type collectionRow struct {
	Namespace         string            `db:"namespace"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	GitCommitSHA      string            `db:"git_commit_sha"`
	GitRef            string            `db:"git_ref"`
	Spec              string            `db:"spec"`
	Body              string            `db:"body"`
	Status            string            `db:"status"`
}

type collectionNameRow struct {
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type collectionUIDRow struct {
	UID               gocql.UUID `db:"uid"`
	Namespace         string     `db:"namespace"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type namespaceRow struct {
	Bucket             string     `db:"bucket"`
	CreatedAt          time.Time  `db:"created_at"`
	ID                 gocql.UUID `db:"id"`
	Identifier         string     `db:"identifier"`
	DisplayName        string     `db:"display_name"`
	Tier               string     `db:"tier"`
	ParentEnterpriseID *string    `db:"parent_enterprise_id"`
	CreatedBy          string     `db:"created_by"`
	UpdatedAt          time.Time  `db:"updated_at"`
	UpdatedBy          string     `db:"updated_by"`
}

// New opens a ScyllaDB connection, runs pending migrations, and returns a Datastore.
// The keyspace must already exist; it is the operator's responsibility to provision it.
func New(cfg config.ScyllaConfig, log *zap.Logger) (datastore.Datastore, error) {
	parsedHosts, port := parseHosts(cfg.Hosts)
	cluster := gocql.NewCluster(parsedHosts...)
	cluster.Keyspace = cfg.Keyspace
	cluster.Consistency = gocql.Quorum
	cluster.DisableShardAwarePort = cfg.DisableShardAwarePort
	cluster.IgnorePeerAddr = cfg.IgnorePeerAddr
	if at, ok := cfg.AddressTranslator.(gocql.AddressTranslator); ok {
		cluster.AddressTranslator = at
	}
	if port > 0 {
		cluster.Port = port
	}
	if cfg.Username != "" {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: cfg.Username,
			Password: cfg.Password,
		}
	}

	rawSession, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("scylla: open session: %w", err)
	}

	instanceID := uuid.New().String()
	if err := RunMigrations(context.Background(), rawSession, cfg.Keyspace, instanceID, log); err != nil {
		rawSession.Close()
		return nil, fmt.Errorf("scylla: migrations: %w", err)
	}

	return &scyllaDatastore{
		session:                     gocqlx.NewSession(rawSession),
		log:                         log,
		productByNamespaceTable:     ProductByNamespace,
		productByNameTable:          ProductByName,
		productByUIDTable:           ProductByUID,
		categoryTaxonomyTable:       CategoryTaxonomy,
		categoryTaxonomyByNameTable: CategoryTaxonomyByName,
		categoryTaxonomyByUIDTable:  CategoryTaxonomyByUID,
		collectionTable:             Collection,
		collectionByNameTable:       CollectionByName,
		collectionByUIDTable:        CollectionByUID,
		namespaceTable:              Namespace,
		repositoryTable:             Repository,
		namespaceMappingTable:       NamespaceMapping,
	}, nil
}

// parseHosts splits "host:port" entries into plain hostnames and returns
// them alongside the port (0 meaning "use the default CQL port 9042").
// gocql.NewCluster only accepts hostnames; the port is set via cluster.Port.
func parseHosts(hosts []string) ([]string, int) {
	out := make([]string, 0, len(hosts))
	port := 0
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if host, portStr, err := net.SplitHostPort(h); err == nil {
			if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
				port = p
			}
			out = append(out, host)
		} else {
			out = append(out, h)
		}
	}
	return out, port
}

func (s *scyllaDatastore) Close() error {
	s.session.Close()
	return nil
}

// ── Product ───────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateProduct(ctx context.Context, p *datastore.Product) error {
	if _, err := s.GetProduct(ctx, p.UID); err == nil {
		return fmt.Errorf("%w: product uid %s", datastore.ErrAlreadyExists, p.UID)
	}
	if _, err := s.GetProductByName(ctx, p.Namespace, p.Name); err == nil {
		return fmt.Errorf("%w: product %s/%s", datastore.ErrAlreadyExists, p.Namespace, p.Name)
	}
	if p.CreationTimestamp.IsZero() {
		p.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toProductRow(p)

	insNS, _ := s.productByNamespaceTable.Insert()
	insName, _ := s.productByNameTable.Insert()
	insUID, _ := s.productByUIDTable.Insert()

	// LOGGED BATCH spans three different partition keys (namespace, name, uid).
	// This crosses partition boundaries which is a ScyllaDB anti-pattern at scale
	// (incurs a coordinator-side batchlog write per operation).
	// TODO: switch to UNLOGGED BATCH + application-layer retry before production ramp.
	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(insNS, row.Namespace, row.CreationTimestamp, row.UID, row.Name, row.APIVersion, row.Kind,
		row.Generation, row.ResourceVersion, row.Revision, row.Labels, row.Annotations,
		row.OwnerRefs, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status)
	b.Query(insName, row.Namespace, row.Name, row.UID, row.CreationTimestamp)
	b.Query(insUID, row.UID, row.Namespace, row.CreationTimestamp)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: create product: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetProduct(_ context.Context, uid string) (*datastore.Product, error) {
	parsedUID, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid product uid %s", datastore.ErrNotFound, uid)
	}
	// Step 1: uid -> (namespace, creation_timestamp)
	getUID, names := s.productByUIDTable.Get()
	var uidRow productUIDRow
	if err := s.session.Query(getUID, names).BindMap(qb.M{"uid": parsedUID}).GetRelease(&uidRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product uid %s", datastore.ErrNotFound, uid)
		}
		return nil, fmt.Errorf("scylla: get product (uid lookup): %w", err)
	}
	// Step 2: (namespace, creation_timestamp, uid) -> full row
	return s.getProductByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
}

func (s *scyllaDatastore) GetProductByName(_ context.Context, namespace, name string) (*datastore.Product, error) {
	// Step 1: (namespace, name) -> (uid, creation_timestamp)
	getName, nameNames := s.productByNameTable.Get()
	var nameRow productNameRow
	if err := s.session.Query(getName, nameNames).BindMap(qb.M{"namespace": namespace, "name": name}).GetRelease(&nameRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product %s/%s", datastore.ErrNotFound, namespace, name)
		}
		return nil, fmt.Errorf("scylla: get product by name (name lookup): %w", err)
	}
	// Step 2: full row from products_by_namespace
	return s.getProductByKey(nameRow.Namespace, nameRow.CreationTimestamp, nameRow.UID)
}

// getProductByKey fetches a full product row from products_by_namespace by its complete primary key.
func (s *scyllaDatastore) getProductByKey(namespace string, createdAt time.Time, uid gocql.UUID) (*datastore.Product, error) {
	cols := strings.Join(s.productByNamespaceTable.Metadata().Columns, ", ")
	stmt := fmt.Sprintf(
		"SELECT %s FROM products_by_namespace WHERE namespace = ? AND creation_timestamp = ? AND uid = ?",
		cols,
	)
	var row productRow
	if err := s.session.Query(stmt, nil).Bind(namespace, createdAt, uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product namespace=%s uid=%s", datastore.ErrNotFound, namespace, uid)
		}
		return nil, fmt.Errorf("scylla: get product by key: %w", err)
	}
	return fromProductRow(&row), nil
}

func (s *scyllaDatastore) ListProducts(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Product], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.productByNamespaceTable, page, "namespace", namespace, productClusterKeys, nil, nil)

	var rows []productRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list products: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	products := make([]*datastore.Product, len(rows))
	for i := range rows {
		products[i] = fromProductRow(&rows[i])
	}

	return buildPageResult(products, limit, page), nil
}

func (s *scyllaDatastore) UpdateProduct(ctx context.Context, p *datastore.Product) error {
	existing, err := s.GetProductByName(ctx, p.Namespace, p.Name)
	if err != nil {
		return err
	}
	row := toProductRow(p)
	// Preserve the original creation_timestamp so the primary key is unchanged.
	row.CreationTimestamp = existing.CreationTimestamp
	// Use the UID from the stored row, not the caller, so the WHERE clause targets
	// the row that was actually found rather than a potentially stale caller value.
	existingUID := mustParseUUID(existing.UID)

	const updNS = "UPDATE products_by_namespace SET api_version=?, kind=?, generation=?, resource_version=?, " +
		"revision=?, labels=?, annotations=?, owner_refs=?, git_commit_sha=?, git_ref=?, spec=?, body=?, status=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=?"

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(updNS,
		row.APIVersion, row.Kind, row.Generation, row.ResourceVersion,
		row.Revision, row.Labels, row.Annotations, row.OwnerRefs,
		row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.Namespace, row.CreationTimestamp, existingUID,
	)
	// products_by_name and products_by_uid are index-only; their non-key columns
	// (uid, creation_timestamp) do not change on update, so no update needed there.
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: update product: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteProduct(ctx context.Context, uid string) error {
	p, err := s.GetProduct(ctx, uid)
	if err != nil {
		return err
	}
	parsedUID := mustParseUUID(uid)

	delNS := "DELETE FROM products_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?"
	delName := "DELETE FROM products_by_name WHERE namespace=? AND name=?"
	delUID := "DELETE FROM products_by_uid WHERE uid=?"

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(delNS, p.Namespace, p.CreationTimestamp, parsedUID)
	b.Query(delName, p.Namespace, p.Name)
	b.Query(delUID, parsedUID)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: delete product: %w", err)
	}
	return nil
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateCategoryTaxonomy(ctx context.Context, c *datastore.CategoryTaxonomy) error {
	if _, err := s.GetCategoryTaxonomy(ctx, c.UID); err == nil {
		return fmt.Errorf("%w: category_taxonomy uid %s", datastore.ErrAlreadyExists, c.UID)
	}
	if _, err := s.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name); err == nil {
		return fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if c.CreationTimestamp.IsZero() {
		c.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toCategoryTaxonomyRow(c)

	insMain, _ := s.categoryTaxonomyTable.Insert()
	insName, _ := s.categoryTaxonomyByNameTable.Insert()
	insUID, _ := s.categoryTaxonomyByUIDTable.Insert()

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(insMain, row.Namespace, row.CreationTimestamp, mustParseUUID(row.UID), row.Name,
		row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision,
		row.Labels, row.Annotations, row.ParentName, row.AncestorPath,
		row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status)
	b.Query(insName, row.Namespace, row.Name, mustParseUUID(row.UID), row.CreationTimestamp)
	b.Query(insUID, mustParseUUID(row.UID), row.Namespace, row.CreationTimestamp)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: create category_taxonomy: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) getCategoryTaxonomyByKey(namespace string, creationTimestamp time.Time, uid gocql.UUID) (*datastore.CategoryTaxonomy, error) {
	const stmt = "SELECT %s FROM category_taxonomy WHERE namespace = ? AND creation_timestamp = ? AND uid = ?"
	cols := strings.Join(s.categoryTaxonomyTable.Metadata().Columns, ", ")
	var row categoryTaxonomyRow
	if err := s.session.Query(fmt.Sprintf(stmt, cols), nil).
		Bind(namespace, creationTimestamp, uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrNotFound, namespace, uid)
		}
		return nil, fmt.Errorf("scylla: get category_taxonomy by key: %w", err)
	}
	return fromCategoryTaxonomyRow(&row), nil
}

func (s *scyllaDatastore) GetCategoryTaxonomy(_ context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
	parsedUID, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid category_taxonomy uid %s", datastore.ErrNotFound, uid)
	}
	// Step 1: uid -> (namespace, creation_timestamp)
	stmt, names := s.categoryTaxonomyByUIDTable.Get()
	var uidRow categoryTaxonomyUIDRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{
		"uid": parsedUID,
	}).GetRelease(&uidRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: category_taxonomy uid %s", datastore.ErrNotFound, uid)
		}
		return nil, fmt.Errorf("scylla: get category_taxonomy by uid: %w", err)
	}
	// Step 2: (namespace, creation_timestamp, uid) -> full row
	return s.getCategoryTaxonomyByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
}

func (s *scyllaDatastore) GetCategoryTaxonomyByName(_ context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
	// Step 1: (namespace, name) -> (uid, creation_timestamp)
	stmt, names := s.categoryTaxonomyByNameTable.Get()
	var nameRow categoryTaxonomyNameRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{
		"namespace": namespace,
		"name":      name,
	}).GetRelease(&nameRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrNotFound, namespace, name)
		}
		return nil, fmt.Errorf("scylla: get category_taxonomy by name: %w", err)
	}
	// Step 2: (namespace, creation_timestamp, uid) -> full row
	return s.getCategoryTaxonomyByKey(namespace, nameRow.CreationTimestamp, nameRow.UID)
}

func (s *scyllaDatastore) ListCategoryTaxonomies(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.CategoryTaxonomy], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.categoryTaxonomyTable, page, "namespace", namespace, clusterKeys{TimestampCol: "creation_timestamp", IDCol: "uid"}, nil, nil)

	var rows []categoryTaxonomyRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list category_taxonomies: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	cats := make([]*datastore.CategoryTaxonomy, len(rows))
	for i := range rows {
		cats[i] = fromCategoryTaxonomyRow(&rows[i])
	}

	return buildPageResult(cats, limit, page), nil
}

func (s *scyllaDatastore) UpdateCategoryTaxonomy(ctx context.Context, c *datastore.CategoryTaxonomy) error {
	existing, err := s.GetCategoryTaxonomyByName(ctx, c.Namespace, c.Name)
	if err != nil {
		return err
	}
	row := toCategoryTaxonomyRow(c)
	// Preserve the original creation_timestamp so the primary key is unchanged.
	row.CreationTimestamp = existing.CreationTimestamp
	existingUID := mustParseUUID(existing.UID)

	const updMain = "UPDATE category_taxonomy SET name=?, api_version=?, kind=?, generation=?, resource_version=?, " +
		"revision=?, labels=?, annotations=?, parent_name=?, ancestor_path=?, " +
		"git_commit_sha=?, git_ref=?, spec=?, body=?, status=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=?"

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(updMain,
		row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion,
		row.Revision, row.Labels, row.Annotations, row.ParentName, row.AncestorPath,
		row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.Namespace, row.CreationTimestamp, existingUID,
	)
	insUID, _ := s.categoryTaxonomyByUIDTable.Insert()
	b.Query(insUID, existingUID, row.Namespace, row.CreationTimestamp)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: update category_taxonomy: %w", err)
	}
	return nil
}

// ── Collection ────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateCollection(ctx context.Context, c *datastore.Collection) error {
	if _, err := s.GetCollection(ctx, c.UID); err == nil {
		return fmt.Errorf("%w: collection uid %s", datastore.ErrAlreadyExists, c.UID)
	}
	if _, err := s.GetCollectionByName(ctx, c.Namespace, c.Name); err == nil {
		return fmt.Errorf("%w: collection %s/%s", datastore.ErrAlreadyExists, c.Namespace, c.Name)
	}
	if c.CreationTimestamp.IsZero() {
		c.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toCollectionRow(c)

	insNS, _ := s.collectionTable.Insert()
	insName, _ := s.collectionByNameTable.Insert()
	insUID, _ := s.collectionByUIDTable.Insert()

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(insNS, row.Namespace, row.CreationTimestamp, row.UID, row.Name, row.APIVersion, row.Kind,
		row.Generation, row.ResourceVersion, row.Revision, row.Labels, row.Annotations,
		row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status)
	b.Query(insName, row.Namespace, row.Name, row.UID, row.CreationTimestamp)
	b.Query(insUID, row.UID, row.Namespace, row.CreationTimestamp)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: create collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetCollection(_ context.Context, uid string) (*datastore.Collection, error) {
	parsedUID, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid collection uid %s", datastore.ErrNotFound, uid)
	}
	getUID, names := s.collectionByUIDTable.Get()
	var uidRow collectionUIDRow
	if err := s.session.Query(getUID, names).BindMap(qb.M{"uid": parsedUID}).GetRelease(&uidRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: collection uid %s", datastore.ErrNotFound, uid)
		}
		return nil, fmt.Errorf("scylla: get collection (uid lookup): %w", err)
	}
	return s.getCollectionByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
}

func (s *scyllaDatastore) GetCollectionByName(_ context.Context, namespace, name string) (*datastore.Collection, error) {
	getName, nameNames := s.collectionByNameTable.Get()
	var nameRow collectionNameRow
	if err := s.session.Query(getName, nameNames).BindMap(qb.M{"namespace": namespace, "name": name}).GetRelease(&nameRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: collection %s/%s", datastore.ErrNotFound, namespace, name)
		}
		return nil, fmt.Errorf("scylla: get collection by name: %w", err)
	}
	return s.getCollectionByKey(nameRow.Namespace, nameRow.CreationTimestamp, nameRow.UID)
}

func (s *scyllaDatastore) getCollectionByKey(namespace string, createdAt time.Time, uid gocql.UUID) (*datastore.Collection, error) {
	cols := strings.Join(s.collectionTable.Metadata().Columns, ", ")
	stmt := fmt.Sprintf(
		"SELECT %s FROM collection WHERE namespace = ? AND creation_timestamp = ? AND uid = ?",
		cols,
	)
	var row collectionRow
	if err := s.session.Query(stmt, nil).Bind(namespace, createdAt, uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: collection namespace=%s uid=%s", datastore.ErrNotFound, namespace, uid)
		}
		return nil, fmt.Errorf("scylla: get collection by key: %w", err)
	}
	return fromCollectionRow(&row), nil
}

func (s *scyllaDatastore) ListCollections(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.collectionTable, page, "namespace", namespace, collectionClusterKeys, nil, nil)

	var rows []collectionRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list collections: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	items := make([]*datastore.Collection, len(rows))
	for i := range rows {
		items[i] = fromCollectionRow(&rows[i])
	}

	return buildPageResult(items, limit, page), nil
}

func (s *scyllaDatastore) UpdateCollection(ctx context.Context, c *datastore.Collection) error {
	existing, err := s.GetCollectionByName(ctx, c.Namespace, c.Name)
	if err != nil {
		return err
	}
	row := toCollectionRow(c)
	row.CreationTimestamp = existing.CreationTimestamp
	existingUID := mustParseUUID(existing.UID)

	const updNS = "UPDATE collection SET api_version=?, kind=?, generation=?, resource_version=?, " +
		"revision=?, labels=?, annotations=?, git_commit_sha=?, git_ref=?, spec=?, body=?, status=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=?"

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(updNS,
		row.APIVersion, row.Kind, row.Generation, row.ResourceVersion,
		row.Revision, row.Labels, row.Annotations,
		row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.Namespace, row.CreationTimestamp, existingUID,
	)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: update collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) ListProductsByLabelSelector(ctx context.Context, namespace string, selector catalog.LabelSelector) ([]*datastore.Product, error) {
	const batchSize = 500
	var (
		matched []*datastore.Product
		page    = datastore.PageParams{First: batchSize}
	)
	for {
		result, err := s.ListProducts(ctx, namespace, page)
		if err != nil {
			return nil, err
		}
		for _, p := range result.Items {
			if catalog.MatchesLabels(&selector, p.Labels) {
				matched = append(matched, p)
			}
		}
		if !result.HasNext || len(result.Items) == 0 {
			break
		}
		last := result.Items[len(result.Items)-1]
		page.After = encodeKeysetCursor(last.CreationTimestamp, last.UID)
	}
	return matched, nil
}

// ── Namespace ─────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateNamespace(ctx context.Context, ns *datastore.Namespace) error {
	if ns == nil {
		return fmt.Errorf("%w: namespace is nil", datastore.ErrInvalidArgument)
	}
	if ns.ID == "" {
		return fmt.Errorf("%w: namespace id is empty", datastore.ErrInvalidArgument)
	}
	if _, err := s.GetNamespace(ctx, ns.ID); err == nil {
		return fmt.Errorf("%w: namespace id %s", datastore.ErrAlreadyExists, ns.ID)
	}
	if existing, err := s.GetNamespaceByIdentifier(ctx, ns.Identifier); err == nil && existing.ID != ns.ID {
		return fmt.Errorf("%w: namespace identifier %s", datastore.ErrAlreadyExists, ns.Identifier)
	}
	row := toNamespaceRow(ns)
	stmt, names := s.namespaceTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create namespace: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetNamespace(_ context.Context, id string) (*datastore.Namespace, error) {
	uid, err := gocql.ParseUUID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid namespace id %s", datastore.ErrNotFound, id)
	}
	stmt, names := qb.Select("namespaces").
		Columns(s.namespaceTable.Metadata().Columns...).
		Where(qb.Eq("id")).
		Limit(1).
		ToCql()
	var row namespaceRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": uid}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace id %s", datastore.ErrNotFound, id)
		}
		return nil, fmt.Errorf("scylla: get namespace: %w", err)
	}
	return fromNamespaceRow(&row), nil
}

func (s *scyllaDatastore) GetNamespaceByIdentifier(_ context.Context, identifier string) (*datastore.Namespace, error) {
	stmt, names := qb.Select("namespaces").
		Columns(s.namespaceTable.Metadata().Columns...).
		Where(qb.Eq("identifier")).
		ToCql()
	var row namespaceRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"identifier": identifier}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace identifier %s", datastore.ErrNotFound, identifier)
		}
		return nil, fmt.Errorf("scylla: get namespace by identifier: %w", err)
	}
	return fromNamespaceRow(&row), nil
}

func (s *scyllaDatastore) ListNamespaces(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.namespaceTable, page, "bucket", BucketAll, defaultClusterKeys, nil, nil)

	var rows []namespaceRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list namespaces: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	nss := make([]*datastore.Namespace, len(rows))
	for i := range rows {
		nss[i] = fromNamespaceRow(&rows[i])
	}

	return buildPageResult(nss, limit, page), nil
}

func (s *scyllaDatastore) DeleteNamespace(ctx context.Context, id string) error {
	ns, err := s.GetNamespace(ctx, id)
	if err != nil {
		return err
	}
	stmt, names := s.namespaceTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{
		"bucket":     BucketAll,
		"created_at": ns.CreatedAt,
		"id":         mustParseUUID(id),
	}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete namespace: %w", err)
	}
	return nil
}

// ── row conversion helpers ────────────────────────────────────────────────────

func toProductRow(p *datastore.Product) *productRow {
	ownerRefs := ""
	if len(p.OwnerRefs) > 0 {
		ownerRefs = string(p.OwnerRefs)
	}
	spec := ""
	if len(p.Spec) > 0 {
		spec = string(p.Spec)
	}
	status := ""
	if len(p.Status) > 0 {
		status = string(p.Status)
	}
	return &productRow{
		Namespace:         p.Namespace,
		CreationTimestamp: p.CreationTimestamp,
		UID:               mustParseUUID(p.UID),
		Name:              p.Name,
		APIVersion:        p.APIVersion,
		Kind:              p.Kind,
		Generation:        p.Generation,
		ResourceVersion:   p.ResourceVersion,
		Revision:          p.Revision,
		Labels:            p.Labels,
		Annotations:       p.Annotations,
		OwnerRefs:         ownerRefs,
		GitCommitSHA:      p.GitCommitSHA,
		GitRef:            p.GitRef,
		Spec:              spec,
		Body:              p.Body,
		Status:            status,
	}
}

func fromProductRow(r *productRow) *datastore.Product {
	return &datastore.Product{
		Namespace:         r.Namespace,
		Name:              r.Name,
		UID:               r.UID.String(),
		APIVersion:        r.APIVersion,
		Kind:              r.Kind,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		Revision:          r.Revision,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		OwnerRefs:         jsonOrNil(r.OwnerRefs),
		GitCommitSHA:      r.GitCommitSHA,
		GitRef:            r.GitRef,
		Spec:              jsonOrNil(r.Spec),
		Body:              r.Body,
		Status:            jsonOrNil(r.Status),
	}
}

func jsonOrNil(s string) []byte {
	if s == "" {
		return nil
	}
	return []byte(s)
}

func toCategoryTaxonomyRow(c *datastore.CategoryTaxonomy) *categoryTaxonomyRow {
	spec := ""
	if len(c.Spec) > 0 {
		spec = string(c.Spec)
	}
	status := ""
	if len(c.Status) > 0 {
		status = string(c.Status)
	}
	return &categoryTaxonomyRow{
		Namespace:         c.Namespace,
		Name:              c.Name,
		UID:               c.UID,
		APIVersion:        c.APIVersion,
		Kind:              c.Kind,
		Generation:        c.Generation,
		ResourceVersion:   c.ResourceVersion,
		CreationTimestamp: c.CreationTimestamp,
		Revision:          c.Revision,
		Labels:            c.Labels,
		Annotations:       c.Annotations,
		ParentName:        c.ParentName,
		AncestorPath:      c.AncestorPath,
		GitCommitSHA:      c.GitCommitSHA,
		GitRef:            c.GitRef,
		Spec:              spec,
		Body:              c.Body,
		Status:            status,
	}
}

func fromCategoryTaxonomyRow(r *categoryTaxonomyRow) *datastore.CategoryTaxonomy {
	return &datastore.CategoryTaxonomy{
		Namespace:         r.Namespace,
		Name:              r.Name,
		UID:               r.UID,
		APIVersion:        r.APIVersion,
		Kind:              r.Kind,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		Revision:          r.Revision,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		ParentName:        r.ParentName,
		AncestorPath:      r.AncestorPath,
		GitCommitSHA:      r.GitCommitSHA,
		GitRef:            r.GitRef,
		Spec:              jsonOrNil(r.Spec),
		Body:              r.Body,
		Status:            jsonOrNil(r.Status),
	}
}

func toCollectionRow(c *datastore.Collection) *collectionRow {
	return &collectionRow{
		Namespace:         c.Namespace,
		CreationTimestamp: c.CreationTimestamp,
		UID:               mustParseUUID(c.UID),
		Name:              c.Name,
		APIVersion:        c.APIVersion,
		Kind:              c.Kind,
		Generation:        c.Generation,
		ResourceVersion:   c.ResourceVersion,
		Revision:          c.Revision,
		Labels:            c.Labels,
		Annotations:       c.Annotations,
		GitCommitSHA:      c.GitCommitSHA,
		GitRef:            c.GitRef,
		Spec:              string(c.Spec),
		Body:              c.Body,
		Status:            string(c.Status),
	}
}

func fromCollectionRow(r *collectionRow) *datastore.Collection {
	return &datastore.Collection{
		UID:               r.UID.String(),
		Namespace:         r.Namespace,
		Name:              r.Name,
		APIVersion:        r.APIVersion,
		Kind:              r.Kind,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		Revision:          r.Revision,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		GitCommitSHA:      r.GitCommitSHA,
		GitRef:            r.GitRef,
		Spec:              jsonOrNil(r.Spec),
		Body:              r.Body,
		Status:            jsonOrNil(r.Status),
	}
}

func mustParseUUID(s string) gocql.UUID {
	u, err := gocql.ParseUUID(s)
	if err != nil {
		panic(err)
	}
	return u
}

func toNamespaceRow(ns *datastore.Namespace) *namespaceRow {
	return &namespaceRow{
		Bucket:             BucketAll,
		CreatedAt:          ns.CreatedAt,
		ID:                 mustParseUUID(ns.ID),
		Identifier:         ns.Identifier,
		DisplayName:        ns.DisplayName,
		Tier:               string(ns.Tier),
		ParentEnterpriseID: ns.ParentEnterpriseID,
		CreatedBy:          ns.CreatedBy,
		UpdatedAt:          ns.UpdatedAt,
		UpdatedBy:          ns.UpdatedBy,
	}
}

func fromNamespaceRow(r *namespaceRow) *datastore.Namespace {
	return &datastore.Namespace{
		ID:                 r.ID.String(),
		Identifier:         r.Identifier,
		DisplayName:        r.DisplayName,
		Tier:               datastore.NamespaceTier(r.Tier),
		ParentEnterpriseID: r.ParentEnterpriseID,
		CreatedAt:          r.CreatedAt,
		CreatedBy:          r.CreatedBy,
		UpdatedAt:          r.UpdatedAt,
		UpdatedBy:          r.UpdatedBy,
	}
}
