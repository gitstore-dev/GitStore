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
	"gopkg.in/inf.v0"
)

// scyllaDatastore implements datastore.Datastore backed by ScyllaDB.
type scyllaDatastore struct {
	session               gocqlx.Session
	log                   *zap.Logger
	productTable          *table.Table
	categoryTable         *table.Table
	collectionTable       *table.Table
	namespaceTable        *table.Table
	repositoryTable       *table.Table
	namespaceMappingTable *table.Table
}

// row structs mirror the CQL columns.

type productRow struct {
	ID                gocql.UUID        `db:"id"`
	SKU               string            `db:"sku"`
	Title             string            `db:"title"`
	Price             *inf.Dec          `db:"price"`
	Currency          string            `db:"currency"`
	InventoryStatus   string            `db:"inventory_status"`
	InventoryQuantity *int              `db:"inventory_quantity"`
	CategoryID        string            `db:"category_id"`
	CollectionIDs     []string          `db:"collection_ids"`
	Images            []string          `db:"images"`
	Metadata          map[string]string `db:"metadata"`
	CreatedAt         time.Time         `db:"created_at"`
	UpdatedAt         time.Time         `db:"updated_at"`
	Body              string            `db:"body"`
}

type categoryRow struct {
	ID           gocql.UUID `db:"id"`
	Name         string     `db:"name"`
	Slug         string     `db:"slug"`
	ParentID     *string    `db:"parent_id"`
	DisplayOrder int        `db:"display_order"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
	Body         string     `db:"body"`
}

type collectionRow struct {
	ID           gocql.UUID `db:"id"`
	Name         string     `db:"name"`
	Slug         string     `db:"slug"`
	DisplayOrder int        `db:"display_order"`
	ProductIDs   []string   `db:"product_ids"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
	Body         string     `db:"body"`
}

type namespaceRow struct {
	ID                 gocql.UUID `db:"id"`
	Identifier         string     `db:"identifier"`
	DisplayName        string     `db:"display_name"`
	Tier               string     `db:"tier"`
	ParentEnterpriseID *string    `db:"parent_enterprise_id"`
	CreatedAt          time.Time  `db:"created_at"`
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
		session:               gocqlx.NewSession(rawSession),
		log:                   log,
		productTable:          Product,
		categoryTable:         Category,
		collectionTable:       Collection,
		namespaceTable:        Namespace,
		repositoryTable:       Repository,
		namespaceMappingTable: NamespaceMapping,
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
	// Check for existing ID.
	if _, err := s.GetProduct(ctx, p.ID); err == nil {
		return fmt.Errorf("%w: product id %s", datastore.ErrAlreadyExists, p.ID)
	}
	// Check for duplicate SKU via secondary index.
	if existing, err := s.GetProductBySKU(ctx, p.SKU); err == nil && existing.ID != p.ID {
		return fmt.Errorf("%w: product sku %s", datastore.ErrAlreadyExists, p.SKU)
	}
	row := toProductRow(p)
	stmt, names := s.productTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create product: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetProduct(_ context.Context, id string) (*datastore.Product, error) {
	var row productRow
	stmt, names := s.productTable.Get()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product id %s", datastore.ErrNotFound, id)
		}
		return nil, fmt.Errorf("scylla: get product: %w", err)
	}
	return fromProductRow(&row), nil
}

func (s *scyllaDatastore) GetProductBySKU(_ context.Context, sku string) (*datastore.Product, error) {
	stmt, names := qb.Select("products").
		Columns(s.productTable.Metadata().Columns...).
		Where(qb.Eq("sku")).
		ToCql()
	var row productRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"sku": sku}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product sku %s", datastore.ErrNotFound, sku)
		}
		return nil, fmt.Errorf("scylla: get product by sku: %w", err)
	}
	return fromProductRow(&row), nil
}

func (s *scyllaDatastore) ListProducts(_ context.Context, filter datastore.ProductFilter) ([]*datastore.Product, error) {
	var stmt string
	var names []string
	var bindMap qb.M

	if filter.CategoryID != "" {
		stmt, names = qb.Select("products").
			Columns(s.productTable.Metadata().Columns...).
			Where(qb.Eq("category_id")).
			ToCql()
		bindMap = qb.M{"category_id": filter.CategoryID}
	} else {
		stmt, names = qb.Select("products").
			Columns(s.productTable.Metadata().Columns...).
			ToCql()
		bindMap = qb.M{}
	}

	var rows []productRow
	if err := s.session.Query(stmt, names).BindMap(bindMap).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list products: %w", err)
	}

	products := make([]*datastore.Product, len(rows))
	for i := range rows {
		products[i] = fromProductRow(&rows[i])
	}
	return products, nil
}

func (s *scyllaDatastore) UpdateProduct(ctx context.Context, p *datastore.Product) error {
	if _, err := s.GetProduct(ctx, p.ID); err != nil {
		return err
	}
	row := toProductRow(p)
	stmt, names := s.productTable.Update(
		"sku", "title", "price", "currency",
		"inventory_status", "inventory_quantity",
		"category_id", "collection_ids", "images",
		"metadata", "created_at", "updated_at", "body",
	)
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update product: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteProduct(ctx context.Context, id string) error {
	if _, err := s.GetProduct(ctx, id); err != nil {
		return err
	}
	stmt, names := s.productTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).ExecRelease(); err != nil {
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
	row := toCategoryRow(c)
	stmt, names := s.categoryTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create category: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetCategory(_ context.Context, id string) (*datastore.Category, error) {
	var row categoryRow
	stmt, names := s.categoryTable.Get()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).GetRelease(&row); err != nil {
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

func (s *scyllaDatastore) ListCategories(_ context.Context) ([]*datastore.Category, error) {
	stmt, names := qb.Select("categories").
		Columns(s.categoryTable.Metadata().Columns...).
		ToCql()
	// Use page_size to limit fetching in ScyllaDB for better performance
	var rows []categoryRow
	if err := s.session.Query(stmt, names).PageSize(100).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list categories: %w", err)
	}
	cats := make([]*datastore.Category, len(rows))
	for i := range rows {
		cats[i] = fromCategoryRow(&rows[i])
	}
	return cats, nil
}

func (s *scyllaDatastore) UpdateCategory(ctx context.Context, c *datastore.Category) error {
	if _, err := s.GetCategory(ctx, c.ID); err != nil {
		return err
	}
	row := toCategoryRow(c)
	stmt, names := s.categoryTable.Update(
		"name", "slug", "parent_id", "display_order",
		"created_at", "updated_at", "body",
	)
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update category: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteCategory(ctx context.Context, id string) error {
	if _, err := s.GetCategory(ctx, id); err != nil {
		return err
	}
	stmt, names := s.categoryTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).ExecRelease(); err != nil {
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
	row := toCollectionRow(c)
	stmt, names := s.collectionTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetCollection(_ context.Context, id string) (*datastore.Collection, error) {
	var row collectionRow
	stmt, names := s.collectionTable.Get()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).GetRelease(&row); err != nil {
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

func (s *scyllaDatastore) ListCollections(_ context.Context) ([]*datastore.Collection, error) {
	stmt, names := qb.Select("collections").
		Columns(s.collectionTable.Metadata().Columns...).
		ToCql()
	// Use page_size to limit fetching in ScyllaDB for better performance
	var rows []collectionRow
	if err := s.session.Query(stmt, names).PageSize(100).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list collections: %w", err)
	}
	cols := make([]*datastore.Collection, len(rows))
	for i := range rows {
		cols[i] = fromCollectionRow(&rows[i])
	}
	return cols, nil
}

func (s *scyllaDatastore) UpdateCollection(ctx context.Context, c *datastore.Collection) error {
	if _, err := s.GetCollection(ctx, c.ID); err != nil {
		return err
	}
	row := toCollectionRow(c)
	stmt, names := s.collectionTable.Update(
		"name", "slug", "display_order", "product_ids",
		"created_at", "updated_at", "body",
	)
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteCollection(ctx context.Context, id string) error {
	if _, err := s.GetCollection(ctx, id); err != nil {
		return err
	}
	stmt, names := s.collectionTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).ExecRelease(); err != nil {
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
	var row namespaceRow
	stmt, names := s.namespaceTable.Get()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).GetRelease(&row); err != nil {
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

func (s *scyllaDatastore) ListNamespaces(_ context.Context) ([]*datastore.Namespace, error) {
	stmt, names := qb.Select("namespaces").
		Columns(s.namespaceTable.Metadata().Columns...).
		ToCql()
	var rows []namespaceRow
	if err := s.session.Query(stmt, names).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list namespaces: %w", err)
	}
	nss := make([]*datastore.Namespace, len(rows))
	for i := range rows {
		nss[i] = fromNamespaceRow(&rows[i])
	}
	return nss, nil
}

func (s *scyllaDatastore) DeleteNamespace(ctx context.Context, id string) error {
	if _, err := s.GetNamespace(ctx, id); err != nil {
		return err
	}
	stmt, names := s.namespaceTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete namespace: %w", err)
	}
	return nil
}

// ── row conversion helpers ────────────────────────────────────────────────────

func toProductRow(p *datastore.Product) *productRow {
	meta := make(map[string]string, len(p.Metadata))
	for k, v := range p.Metadata {
		meta[k] = fmt.Sprintf("%v", v)
	}
	return &productRow{
		ID:                mustParseUUID(p.ID),
		SKU:               p.SKU,
		Title:             p.Title,
		Price:             inf.NewDec(int64(p.Price*1e8), 8),
		Currency:          p.Currency,
		InventoryStatus:   p.InventoryStatus,
		InventoryQuantity: p.InventoryQuantity,
		CategoryID:        p.CategoryID,
		CollectionIDs:     p.CollectionIDs,
		Images:            p.Images,
		Metadata:          meta,
		CreatedAt:         p.CreatedAt,
		UpdatedAt:         p.UpdatedAt,
		Body:              p.Body,
	}
}

func fromProductRow(r *productRow) *datastore.Product {
	meta := make(map[string]any, len(r.Metadata))
	for k, v := range r.Metadata {
		meta[k] = v
	}
	var price float64
	if r.Price != nil {
		f, _ := strconv.ParseFloat(r.Price.String(), 64)
		price = f
	}
	return &datastore.Product{
		ID:                r.ID.String(),
		SKU:               r.SKU,
		Title:             r.Title,
		Price:             price,
		Currency:          r.Currency,
		InventoryStatus:   r.InventoryStatus,
		InventoryQuantity: r.InventoryQuantity,
		CategoryID:        r.CategoryID,
		CollectionIDs:     r.CollectionIDs,
		Images:            r.Images,
		Metadata:          meta,
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
		Body:              r.Body,
	}
}

func toCategoryRow(c *datastore.Category) *categoryRow {
	return &categoryRow{
		ID:           mustParseUUID(c.ID),
		Name:         c.Name,
		Slug:         c.Slug,
		ParentID:     c.ParentID,
		DisplayOrder: c.DisplayOrder,
		CreatedAt:    c.CreatedAt,
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
		ID:           mustParseUUID(c.ID),
		Name:         c.Name,
		Slug:         c.Slug,
		DisplayOrder: c.DisplayOrder,
		ProductIDs:   c.ProductIDs,
		CreatedAt:    c.CreatedAt,
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
	displayName := ns.DisplayName
	return &namespaceRow{
		ID:                 mustParseUUID(ns.ID),
		Identifier:         ns.Identifier,
		DisplayName:        displayName,
		Tier:               string(ns.Tier),
		ParentEnterpriseID: ns.ParentEnterpriseID,
		CreatedAt:          ns.CreatedAt,
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
