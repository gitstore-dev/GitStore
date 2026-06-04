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
	session                 gocqlx.Session
	log                     *zap.Logger
	productByNamespaceTable *table.Table
	productByNameTable      *table.Table
	productByUIDTable       *table.Table
	categoryTable           *table.Table
	collectionTable         *table.Table
	namespaceTable          *table.Table
	repositoryTable         *table.Table
	namespaceMappingTable   *table.Table
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

type categoryRow struct {
	Bucket       string     `db:"bucket"`
	CreatedAt    time.Time  `db:"created_at"`
	ID           gocql.UUID `db:"id"`
	Name         string     `db:"name"`
	Slug         string     `db:"slug"`
	ParentID     *string    `db:"parent_id"`
	DisplayOrder int        `db:"display_order"`
	UpdatedAt    time.Time  `db:"updated_at"`
	Body         string     `db:"body"`
}

type collectionRow struct {
	Bucket       string     `db:"bucket"`
	CreatedAt    time.Time  `db:"created_at"`
	ID           gocql.UUID `db:"id"`
	Name         string     `db:"name"`
	Slug         string     `db:"slug"`
	DisplayOrder int        `db:"display_order"`
	ProductIDs   []string   `db:"product_ids"`
	UpdatedAt    time.Time  `db:"updated_at"`
	Body         string     `db:"body"`
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
		session:                 gocqlx.NewSession(rawSession),
		log:                     log,
		productByNamespaceTable: ProductByNamespace,
		productByNameTable:      ProductByName,
		productByUIDTable:       ProductByUID,
		categoryTable:           Category,
		collectionTable:         Collection,
		namespaceTable:          Namespace,
		repositoryTable:         Repository,
		namespaceMappingTable:   NamespaceMapping,
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
	parsedUID := mustParseUUID(p.UID)

	insNS, _ := s.productByNamespaceTable.Insert()
	insName, _ := s.productByNameTable.Insert()
	insUID, _ := s.productByUIDTable.Insert()

	b := s.session.Batch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(insNS, row.Namespace, row.CreationTimestamp, row.UID, row.Name, row.APIVersion, row.Kind,
		row.Generation, row.ResourceVersion, row.Revision, row.Labels, row.Annotations,
		row.OwnerRefs, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status)
	b.Query(insName, row.Namespace, row.Name, row.UID, row.CreationTimestamp)
	b.Query(insUID, row.UID, row.Namespace, row.CreationTimestamp)
	_ = parsedUID
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
			return nil, fmt.Errorf("%w: product %s/%s", datastore.ErrNotFound, namespace, uid)
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

	updNS := fmt.Sprintf(
		"UPDATE products_by_namespace SET api_version=?, kind=?, generation=?, resource_version=?, " +
			"revision=?, labels=?, annotations=?, owner_refs=?, git_commit_sha=?, git_ref=?, spec=?, body=?, status=? " +
			"WHERE namespace=? AND creation_timestamp=? AND uid=?",
	)
	parsedUID := mustParseUUID(p.UID)

	b := s.session.NewBatch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(updNS,
		row.APIVersion, row.Kind, row.Generation, row.ResourceVersion,
		row.Revision, row.Labels, row.Annotations, row.OwnerRefs,
		row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.Namespace, row.CreationTimestamp, parsedUID,
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

	b := s.session.NewBatch(gocql.LoggedBatch).WithContext(ctx)
	b.Query(delNS, p.Namespace, p.CreationTimestamp, parsedUID)
	b.Query(delName, p.Namespace, p.Name)
	b.Query(delUID, parsedUID)
	if err := s.session.ExecuteBatch(b); err != nil {
		return fmt.Errorf("scylla: delete product: %w", err)
	}
	return nil
}

// ── Category ──────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateCategory(ctx context.Context, c *datastore.Category) error {
	if _, err := s.GetCategory(ctx, c.ID); err == nil {
		return fmt.Errorf("%w: category id %s", datastore.ErrAlreadyExists, c.ID)
	}
	if existing, err := s.GetCategoryBySlug(ctx, c.Slug); err == nil && existing.ID != c.ID {
		return fmt.Errorf("%w: category slug %s", datastore.ErrAlreadyExists, c.Slug)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	row := toCategoryRow(c)
	stmt, names := s.categoryTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create category: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetCategory(_ context.Context, id string) (*datastore.Category, error) {
	uid, err := gocql.ParseUUID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid category id %s", datastore.ErrNotFound, id)
	}
	stmt, names := qb.Select("categories").
		Columns(s.categoryTable.Metadata().Columns...).
		Where(qb.Eq("id")).
		Limit(1).
		ToCql()
	var row categoryRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": uid}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: category id %s", datastore.ErrNotFound, id)
		}
		return nil, fmt.Errorf("scylla: get category: %w", err)
	}
	return fromCategoryRow(&row), nil
}

func (s *scyllaDatastore) GetCategoryBySlug(_ context.Context, slug string) (*datastore.Category, error) {
	stmt, names := qb.Select("categories").
		Columns(s.categoryTable.Metadata().Columns...).
		Where(qb.Eq("slug")).
		ToCql()
	var row categoryRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"slug": slug}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: category slug %s", datastore.ErrNotFound, slug)
		}
		return nil, fmt.Errorf("scylla: get category by slug: %w", err)
	}
	return fromCategoryRow(&row), nil
}

func (s *scyllaDatastore) ListCategories(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Category], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.categoryTable, page, "bucket", BucketAll, defaultClusterKeys, nil, nil)

	var rows []categoryRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list categories: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	cats := make([]*datastore.Category, len(rows))
	for i := range rows {
		cats[i] = fromCategoryRow(&rows[i])
	}

	return buildPageResult(cats, limit, page), nil
}

func (s *scyllaDatastore) UpdateCategory(ctx context.Context, c *datastore.Category) error {
	if _, err := s.GetCategory(ctx, c.ID); err != nil {
		return err
	}
	row := toCategoryRow(c)
	stmt, names := s.categoryTable.Update(
		"name", "slug", "parent_id", "display_order",
		"updated_at", "body",
	)
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update category: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteCategory(ctx context.Context, id string) error {
	cat, err := s.GetCategory(ctx, id)
	if err != nil {
		return err
	}
	stmt, names := s.categoryTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{
		"bucket":     BucketAll,
		"created_at": cat.CreatedAt,
		"id":         mustParseUUID(id),
	}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete category: %w", err)
	}
	return nil
}

// ── Collection ────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateCollection(ctx context.Context, c *datastore.Collection) error {
	if _, err := s.GetCollection(ctx, c.ID); err == nil {
		return fmt.Errorf("%w: collection id %s", datastore.ErrAlreadyExists, c.ID)
	}
	if existing, err := s.GetCollectionBySlug(ctx, c.Slug); err == nil && existing.ID != c.ID {
		return fmt.Errorf("%w: collection slug %s", datastore.ErrAlreadyExists, c.Slug)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	row := toCollectionRow(c)
	stmt, names := s.collectionTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetCollection(_ context.Context, id string) (*datastore.Collection, error) {
	uid, err := gocql.ParseUUID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid collection id %s", datastore.ErrNotFound, id)
	}
	stmt, names := qb.Select("collections").
		Columns(s.collectionTable.Metadata().Columns...).
		Where(qb.Eq("id")).
		Limit(1).
		ToCql()
	var row collectionRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": uid}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: collection id %s", datastore.ErrNotFound, id)
		}
		return nil, fmt.Errorf("scylla: get collection: %w", err)
	}
	return fromCollectionRow(&row), nil
}

func (s *scyllaDatastore) GetCollectionBySlug(_ context.Context, slug string) (*datastore.Collection, error) {
	stmt, names := qb.Select("collections").
		Columns(s.collectionTable.Metadata().Columns...).
		Where(qb.Eq("slug")).
		ToCql()
	var row collectionRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"slug": slug}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: collection slug %s", datastore.ErrNotFound, slug)
		}
		return nil, fmt.Errorf("scylla: get collection by slug: %w", err)
	}
	return fromCollectionRow(&row), nil
}

func (s *scyllaDatastore) ListCollections(_ context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Collection], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.collectionTable, page, "bucket", BucketAll, defaultClusterKeys, nil, nil)

	var rows []collectionRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list collections: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	cols := make([]*datastore.Collection, len(rows))
	for i := range rows {
		cols[i] = fromCollectionRow(&rows[i])
	}

	return buildPageResult(cols, limit, page), nil
}

func (s *scyllaDatastore) UpdateCollection(ctx context.Context, c *datastore.Collection) error {
	if _, err := s.GetCollection(ctx, c.ID); err != nil {
		return err
	}
	row := toCollectionRow(c)
	stmt, names := s.collectionTable.Update(
		"name", "slug", "display_order", "product_ids",
		"updated_at", "body",
	)
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteCollection(ctx context.Context, id string) error {
	col, err := s.GetCollection(ctx, id)
	if err != nil {
		return err
	}
	stmt, names := s.collectionTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{
		"bucket":     BucketAll,
		"created_at": col.CreatedAt,
		"id":         mustParseUUID(id),
	}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete collection: %w", err)
	}
	return nil
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

func toCategoryRow(c *datastore.Category) *categoryRow {
	return &categoryRow{
		Bucket:       BucketAll,
		CreatedAt:    c.CreatedAt,
		ID:           mustParseUUID(c.ID),
		Name:         c.Name,
		Slug:         c.Slug,
		ParentID:     c.ParentID,
		DisplayOrder: c.DisplayOrder,
		UpdatedAt:    c.UpdatedAt,
		Body:         c.Body,
	}
}

func fromCategoryRow(r *categoryRow) *datastore.Category {
	return &datastore.Category{
		ID:           r.ID.String(),
		Name:         r.Name,
		Slug:         r.Slug,
		ParentID:     r.ParentID,
		DisplayOrder: r.DisplayOrder,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		Body:         r.Body,
	}
}

func toCollectionRow(c *datastore.Collection) *collectionRow {
	return &collectionRow{
		Bucket:       BucketAll,
		CreatedAt:    c.CreatedAt,
		ID:           mustParseUUID(c.ID),
		Name:         c.Name,
		Slug:         c.Slug,
		DisplayOrder: c.DisplayOrder,
		ProductIDs:   c.ProductIDs,
		UpdatedAt:    c.UpdatedAt,
		Body:         c.Body,
	}
}

func fromCollectionRow(r *collectionRow) *datastore.Collection {
	return &datastore.Collection{
		ID:           r.ID.String(),
		Name:         r.Name,
		Slug:         r.Slug,
		DisplayOrder: r.DisplayOrder,
		ProductIDs:   r.ProductIDs,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		Body:         r.Body,
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
