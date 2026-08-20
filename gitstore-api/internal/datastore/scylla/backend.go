// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"math/big"
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
	session                           gocqlx.Session
	log                               *zap.Logger
	productByNamespaceTable           *table.Table
	productByNameTable                *table.Table
	productByUIDTable                 *table.Table
	categoryTaxonomyTable             *table.Table
	categoryTaxonomyByNameTable       *table.Table
	categoryTaxonomyByUIDTable        *table.Table
	collectionTable                   *table.Table
	collectionByNameTable             *table.Table
	collectionByUIDTable              *table.Table
	productVariantByNamespaceTable    *table.Table
	productVariantByNameTable         *table.Table
	productVariantByUIDTable          *table.Table
	productVariantBySKUTable          *table.Table
	productVariantByProductRefTable   *table.Table
	namespaceByUIDTable               *table.Table
	namespaceByNameTable              *table.Table
	namespaceByBucketTable            *table.Table
	repositoryByUIDTable              *table.Table
	repositoryByNamespaceTable        *table.Table
	repositoryByBucketTable           *table.Table
	namespaceMappingTable             *table.Table
	namespaceMappingByRepositoryTable *table.Table
	mutations                         *mutationExecutor
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
	CreationActor     string            `db:"creation_actor"`
	UpdateTimestamp   time.Time         `db:"update_timestamp"`
	UpdateActor       string            `db:"update_actor"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerReferences   string            `db:"owner_references"`
	Finalizers        []string          `db:"finalizers"`
	DeletionTimestamp *time.Time        `db:"deletion_timestamp"`
	RepositoryID      *gocql.UUID       `db:"repository_id"`
	SourcePath        string            `db:"source_path"`
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
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	CreationActor     string            `db:"creation_actor"`
	UpdateTimestamp   time.Time         `db:"update_timestamp"`
	UpdateActor       string            `db:"update_actor"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerReferences   string            `db:"owner_references"`
	Finalizers        []string          `db:"finalizers"`
	DeletionTimestamp *time.Time        `db:"deletion_timestamp"`
	ParentName        string            `db:"parent_name"`
	AncestorPath      string            `db:"ancestor_path"`
	RepositoryID      *gocql.UUID       `db:"repository_id"`
	SourcePath        string            `db:"source_path"`
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
	CreationActor     string            `db:"creation_actor"`
	UpdateTimestamp   time.Time         `db:"update_timestamp"`
	UpdateActor       string            `db:"update_actor"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerReferences   string            `db:"owner_references"`
	Finalizers        []string          `db:"finalizers"`
	DeletionTimestamp *time.Time        `db:"deletion_timestamp"`
	RepositoryID      *gocql.UUID       `db:"repository_id"`
	SourcePath        string            `db:"source_path"`
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

type productVariantRow struct {
	Namespace         string            `db:"namespace"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	CreationActor     string            `db:"creation_actor"`
	UpdateTimestamp   time.Time         `db:"update_timestamp"`
	UpdateActor       string            `db:"update_actor"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerReferences   string            `db:"owner_references"`
	Finalizers        []string          `db:"finalizers"`
	DeletionTimestamp *time.Time        `db:"deletion_timestamp"`
	SKU               string            `db:"sku"`
	ProductRefName    string            `db:"product_ref_name"`
	RepositoryID      *gocql.UUID       `db:"repository_id"`
	SourcePath        string            `db:"source_path"`
	GitCommitSHA      string            `db:"git_commit_sha"`
	GitRef            string            `db:"git_ref"`
	Spec              string            `db:"spec"`
	Body              string            `db:"body"`
	Status            string            `db:"status"`
}

type productVariantNameRow struct {
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type productVariantUIDRow struct {
	UID               gocql.UUID `db:"uid"`
	Namespace         string     `db:"namespace"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type productVariantSKURow struct {
	Namespace         string     `db:"namespace"`
	SKU               string     `db:"sku"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type productVariantProductRefRow struct {
	Namespace         string     `db:"namespace"`
	ProductRefName    string     `db:"product_ref_name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type namespaceRow struct {
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	Title             string            `db:"title"`
	Tier              string            `db:"tier"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	CreationActor     string            `db:"creation_actor"`
	UpdateTimestamp   time.Time         `db:"update_timestamp"`
	UpdateActor       string            `db:"update_actor"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerReferences   string            `db:"owner_references"`
	Finalizers        []string          `db:"finalizers"`
	DeletionTimestamp *time.Time        `db:"deletion_timestamp"`
	SourcePath        string            `db:"source_path"`
	GitCommitSHA      string            `db:"git_commit_sha"`
	GitRef            string            `db:"git_ref"`
	Spec              string            `db:"spec"`
	Body              string            `db:"body"`
	Status            string            `db:"status"`
}

type namespaceNameRow struct {
	Name string     `db:"name"`
	UID  gocql.UUID `db:"uid"`
}

type namespaceIndexRow struct {
	Bucket            string     `db:"bucket"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
	UID               gocql.UUID `db:"uid"`
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
		session:                           gocqlx.NewSession(rawSession),
		log:                               log,
		productByNamespaceTable:           ProductByNamespace,
		productByNameTable:                ProductByName,
		productByUIDTable:                 ProductByUID,
		categoryTaxonomyTable:             CategoryTaxonomy,
		categoryTaxonomyByNameTable:       CategoryTaxonomyByName,
		categoryTaxonomyByUIDTable:        CategoryTaxonomyByUID,
		collectionTable:                   Collection,
		collectionByNameTable:             CollectionByName,
		collectionByUIDTable:              CollectionByUID,
		productVariantByNamespaceTable:    ProductVariantByNamespace,
		productVariantByNameTable:         ProductVariantByName,
		productVariantByUIDTable:          ProductVariantByUID,
		productVariantBySKUTable:          ProductVariantBySKU,
		productVariantByProductRefTable:   ProductVariantByProductRef,
		namespaceByUIDTable:               NamespaceByUID,
		namespaceByNameTable:              NamespaceByName,
		namespaceByBucketTable:            NamespaceByBucket,
		repositoryByUIDTable:              RepositoryByUID,
		repositoryByNamespaceTable:        RepositoryByNamespace,
		repositoryByBucketTable:           RepositoryByBucket,
		namespaceMappingTable:             NamespaceMapping,
		namespaceMappingByRepositoryTable: NamespaceMappingByRepository,
		mutations:                         newMutationExecutor(nil),
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
	if p == nil || p.UID == "" || p.Namespace == "" || p.Name == "" {
		return fmt.Errorf("%w: product uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(p.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid product uid %s", datastore.ErrInvalidArgument, p.UID)
	}
	if p.CreationTimestamp.IsZero() {
		p.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toProductRow(p)
	var (
		existed      bool
		nameReserved bool
		uidReserved  bool
	)
	err = s.mutations.execute(ctx,
		mutationAction{
			Step: catalogueStep("create", "Product", p.UID, "products_by_name", p.Namespace+"/"+p.Name, "reserve-name"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				nameReserved, reserveErr = s.reserveNameOwned(ctx, "Product", "products_by_name", row.Namespace, row.Name, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !nameReserved {
					return nil
				}
				return s.releaseName(ctx, "products_by_name", row.Namespace, row.Name, uid)
			},
		},
		mutationAction{
			Step: catalogueStep("create", "Product", p.UID, "products_by_uid", p.UID, "reserve-uid"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				uidReserved, reserveErr = s.reserveUIDOwned(ctx, "Product", "products_by_uid", row.Namespace, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !uidReserved {
					return nil
				}
				return s.releaseUID(ctx, "products_by_uid", row.Namespace, uid, row.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("create", "Product", p.UID, "products_by_namespace", p.UID, "write-authoritative"),
			Apply: func(ctx context.Context) error {
				applied, applyErr := s.insertAuthoritative(ctx, s.productByNamespaceTable, row)
				if applyErr != nil {
					return applyErr
				}
				if !applied {
					current, getErr := s.getProductByKey(row.Namespace, row.CreationTimestamp, uid)
					if getErr != nil {
						return getErr
					}
					if current.UID != p.UID || current.Name != p.Name {
						return fmt.Errorf("%w: product uid %s already has a different authoritative row", datastore.ErrAlreadyExists, p.UID)
					}
					existed = true
				}
				return nil
			},
			Compensate: func(ctx context.Context) error {
				if existed {
					return nil
				}
				return s.deleteProductAuthoritative(ctx, row, row.ResourceVersion)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: create product: %w", err)
	}
	if existed {
		return fmt.Errorf("%w: product uid %s", datastore.ErrAlreadyExists, p.UID)
	}
	return nil
}

func (s *scyllaDatastore) GetProduct(ctx context.Context, uid string) (*datastore.Product, error) {
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
	product, err := s.getProductByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "Product", ResourceUID: uid, Projection: "products_by_uid",
			LookupKey: uid, Operation: "get", Type: datastore.FindingDangling,
		})
	}
	return product, err
}

func (s *scyllaDatastore) GetProductByName(ctx context.Context, namespace, name string) (*datastore.Product, error) {
	// Step 1: (namespace, name) -> (uid, creation_timestamp)
	getName, nameNames := s.productByNameTable.Get()
	var nameRow productNameRow
	if err := s.session.Query(getName, nameNames).BindMap(qb.M{"namespace": namespace, "name": name}).GetRelease(&nameRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product %s/%s", datastore.ErrNotFound, namespace, name)
		}
		return nil, fmt.Errorf("scylla: get product by name (name lookup): %w", err)
	}
	product, err := s.getProductByKey(nameRow.Namespace, nameRow.CreationTimestamp, nameRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "Product", ResourceUID: nameRow.UID.String(), Projection: "products_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingDangling,
		})
		return nil, err
	}
	if err == nil && (product.Name != name || product.Namespace != namespace) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "Product", ResourceUID: nameRow.UID.String(), Projection: "products_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingStale,
		})
		return nil, fmt.Errorf("%w: stale product name projection %s/%s", datastore.ErrNotFound, namespace, name)
	}
	return product, err
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
	if p == nil {
		return fmt.Errorf("%w: product is nil", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(p.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid product uid %s", datastore.ErrInvalidArgument, p.UID)
	}
	var existing *datastore.Product
	if p.CreationTimestamp.IsZero() {
		existing, err = s.GetProduct(ctx, p.UID)
	} else {
		existing, err = s.getProductByKey(p.Namespace, p.CreationTimestamp, uid)
	}
	if err != nil {
		return err
	}
	if existing.Namespace != p.Namespace || existing.Name != p.Name {
		return fmt.Errorf("%w: product identity is immutable", datastore.ErrConflict)
	}
	if err := validateResourceVersionTransition(existing.ResourceVersion, p.ResourceVersion); err != nil {
		return err
	}
	row := toProductRow(p)
	row.CreationTimestamp = existing.CreationTimestamp
	existingUID := mustParseUUID(existing.UID)

	err = s.mutations.executeUpdate(ctx, row.ResourceVersion,
		mutationAction{
			Step: catalogueStep("update", "Product", p.UID, "products_by_namespace", p.UID, "update-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.updateProductAuthoritative(ctx, row, existing.ResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("update", "Product", p.UID, "products_by_name", p.Namespace+"/"+p.Name, "converge-name"),
			Apply: func(ctx context.Context) error {
				return s.reserveName(ctx, "Product", "products_by_name", row.Namespace, row.Name, existingUID, row.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("update", "Product", p.UID, "products_by_uid", p.UID, "converge-uid"),
			Apply: func(ctx context.Context) error {
				return s.reserveUID(ctx, "Product", "products_by_uid", row.Namespace, existingUID, row.CreationTimestamp)
			},
		},
	)
	if err != nil {
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

	err = s.mutations.executeDelete(ctx,
		mutationAction{
			Step: catalogueStep("delete", "Product", uid, "products_by_namespace", uid, "delete-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.deleteProductAuthoritative(ctx, toProductRow(p), p.ResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "Product", uid, "products_by_name", p.Namespace+"/"+p.Name, "delete-name"),
			Apply: func(ctx context.Context) error {
				return s.releaseName(ctx, "products_by_name", p.Namespace, p.Name, parsedUID)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveName(ctx, "Product", "products_by_name", p.Namespace, p.Name, parsedUID, p.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "Product", uid, "products_by_uid", uid, "delete-uid"),
			Apply: func(ctx context.Context) error {
				return s.releaseUID(ctx, "products_by_uid", p.Namespace, parsedUID, p.CreationTimestamp)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveUID(ctx, "Product", "products_by_uid", p.Namespace, parsedUID, p.CreationTimestamp)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: delete product: %w", err)
	}
	return nil
}

// ── CategoryTaxonomy ──────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateCategoryTaxonomy(ctx context.Context, c *datastore.CategoryTaxonomy) error {
	if c == nil || c.UID == "" || c.Namespace == "" || c.Name == "" {
		return fmt.Errorf("%w: category taxonomy uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(c.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid category taxonomy uid %s", datastore.ErrInvalidArgument, c.UID)
	}
	if c.CreationTimestamp.IsZero() {
		c.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toCategoryTaxonomyRow(c)
	var (
		existed      bool
		nameReserved bool
		uidReserved  bool
	)
	err = s.mutations.execute(ctx,
		mutationAction{
			Step: catalogueStep("create", "CategoryTaxonomy", c.UID, "category_taxonomy_by_name", c.Namespace+"/"+c.Name, "reserve-name"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				nameReserved, reserveErr = s.reserveNameOwned(ctx, "CategoryTaxonomy", "category_taxonomy_by_name", row.Namespace, row.Name, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !nameReserved {
					return nil
				}
				return s.releaseName(ctx, "category_taxonomy_by_name", row.Namespace, row.Name, uid)
			},
		},
		mutationAction{
			Step: catalogueStep("create", "CategoryTaxonomy", c.UID, "category_taxonomy_by_uid", c.UID, "reserve-uid"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				uidReserved, reserveErr = s.reserveUIDOwned(ctx, "CategoryTaxonomy", "category_taxonomy_by_uid", row.Namespace, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !uidReserved {
					return nil
				}
				return s.releaseUID(ctx, "category_taxonomy_by_uid", row.Namespace, uid, row.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("create", "CategoryTaxonomy", c.UID, "category_taxonomy", c.UID, "write-authoritative"),
			Apply: func(ctx context.Context) error {
				applied, applyErr := s.insertAuthoritative(ctx, s.categoryTaxonomyTable, row)
				if applyErr != nil {
					return applyErr
				}
				if !applied {
					current, getErr := s.getCategoryTaxonomyByKey(row.Namespace, row.CreationTimestamp, uid)
					if getErr != nil {
						return getErr
					}
					if current.UID != c.UID || current.Name != c.Name {
						return fmt.Errorf("%w: category taxonomy uid %s already has a different authoritative row", datastore.ErrAlreadyExists, c.UID)
					}
					existed = true
				}
				return nil
			},
			Compensate: func(ctx context.Context) error {
				if existed {
					return nil
				}
				return s.deleteCategoryTaxonomyAuthoritative(ctx, row, row.ResourceVersion)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: create category_taxonomy: %w", err)
	}
	if existed {
		return fmt.Errorf("%w: category taxonomy uid %s", datastore.ErrAlreadyExists, c.UID)
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

func (s *scyllaDatastore) GetCategoryTaxonomy(ctx context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
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
	category, err := s.getCategoryTaxonomyByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "CategoryTaxonomy", ResourceUID: uid, Projection: "category_taxonomy_by_uid",
			LookupKey: uid, Operation: "get", Type: datastore.FindingDangling,
		})
	}
	return category, err
}

func (s *scyllaDatastore) GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*datastore.CategoryTaxonomy, error) {
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
	category, err := s.getCategoryTaxonomyByKey(namespace, nameRow.CreationTimestamp, nameRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "CategoryTaxonomy", ResourceUID: nameRow.UID.String(), Projection: "category_taxonomy_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingDangling,
		})
		return nil, err
	}
	if err == nil && (category.Name != name || category.Namespace != namespace) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "CategoryTaxonomy", ResourceUID: nameRow.UID.String(), Projection: "category_taxonomy_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingStale,
		})
		return nil, fmt.Errorf("%w: stale category taxonomy name projection %s/%s", datastore.ErrNotFound, namespace, name)
	}
	return category, err
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
	if c == nil {
		return fmt.Errorf("%w: category taxonomy is nil", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(c.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid category taxonomy uid %s", datastore.ErrInvalidArgument, c.UID)
	}
	var existing *datastore.CategoryTaxonomy
	if c.CreationTimestamp.IsZero() {
		existing, err = s.GetCategoryTaxonomy(ctx, c.UID)
	} else {
		existing, err = s.getCategoryTaxonomyByKey(c.Namespace, c.CreationTimestamp, uid)
	}
	if err != nil {
		return err
	}
	if existing.Namespace != c.Namespace || existing.Name != c.Name {
		return fmt.Errorf("%w: category taxonomy identity is immutable", datastore.ErrConflict)
	}
	if err := validateResourceVersionTransition(existing.ResourceVersion, c.ResourceVersion); err != nil {
		return err
	}
	row := toCategoryTaxonomyRow(c)
	row.CreationTimestamp = existing.CreationTimestamp
	existingUID := mustParseUUID(existing.UID)

	err = s.mutations.executeUpdate(ctx, row.ResourceVersion,
		mutationAction{
			Step: catalogueStep("update", "CategoryTaxonomy", c.UID, "category_taxonomy", c.UID, "update-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.updateCategoryTaxonomyAuthoritative(ctx, row, existing.ResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("update", "CategoryTaxonomy", c.UID, "category_taxonomy_by_name", c.Namespace+"/"+c.Name, "converge-name"),
			Apply: func(ctx context.Context) error {
				return s.reserveName(ctx, "CategoryTaxonomy", "category_taxonomy_by_name", row.Namespace, row.Name, existingUID, row.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("update", "CategoryTaxonomy", c.UID, "category_taxonomy_by_uid", c.UID, "converge-uid"),
			Apply: func(ctx context.Context) error {
				return s.reserveUID(ctx, "CategoryTaxonomy", "category_taxonomy_by_uid", row.Namespace, existingUID, row.CreationTimestamp)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: update category_taxonomy: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) UpdateCategoryTaxonomyStatus(ctx context.Context, namespace, name string, patch datastore.CategoryTaxonomyStatusPatch) (*datastore.CategoryTaxonomy, error) {
	existing, err := s.GetCategoryTaxonomyByName(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	observedResourceVersion := existing.ResourceVersion

	if applyErr := datastore.ApplyCategoryTaxonomyStatusPatch(existing, patch); applyErr != nil {
		return nil, applyErr
	}

	row := toCategoryTaxonomyRow(existing)
	existingUID := mustParseUUID(existing.UID)

	// Lightweight transaction (IF resource_version=?) closes the race
	// between the read above and this write: if another writer updated
	// the row concurrently, the condition fails and applied is false,
	// which we surface as ErrConflict rather than silently overwriting a
	// newer version (spec 040 FR-009).
	const updStatus = "UPDATE category_taxonomy SET resource_version=?, status=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(updStatus, nil).WithContext(ctx).Bind(
		row.ResourceVersion, row.Status,
		row.Namespace, row.CreationTimestamp, existingUID,
		observedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return nil, fmt.Errorf("scylla: update category_taxonomy status: %w", err)
	}
	if !applied {
		return nil, datastore.ErrConflict
	}
	return existing, nil
}

func (s *scyllaDatastore) DeleteCategoryTaxonomy(ctx context.Context, uid string) error {
	c, err := s.GetCategoryTaxonomy(ctx, uid)
	if err != nil {
		return err
	}
	parsedUID := mustParseUUID(uid)

	err = s.mutations.executeDelete(ctx,
		mutationAction{
			Step: catalogueStep("delete", "CategoryTaxonomy", uid, "category_taxonomy", uid, "delete-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.deleteCategoryTaxonomyAuthoritative(ctx, toCategoryTaxonomyRow(c), c.ResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "CategoryTaxonomy", uid, "category_taxonomy_by_name", c.Namespace+"/"+c.Name, "delete-name"),
			Apply: func(ctx context.Context) error {
				return s.releaseName(ctx, "category_taxonomy_by_name", c.Namespace, c.Name, parsedUID)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveName(ctx, "CategoryTaxonomy", "category_taxonomy_by_name", c.Namespace, c.Name, parsedUID, c.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "CategoryTaxonomy", uid, "category_taxonomy_by_uid", uid, "delete-uid"),
			Apply: func(ctx context.Context) error {
				return s.releaseUID(ctx, "category_taxonomy_by_uid", c.Namespace, parsedUID, c.CreationTimestamp)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveUID(ctx, "CategoryTaxonomy", "category_taxonomy_by_uid", c.Namespace, parsedUID, c.CreationTimestamp)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: delete category_taxonomy: %w", err)
	}
	return nil
}

// ── Collection ────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateCollection(ctx context.Context, c *datastore.Collection) error {
	if c == nil || c.UID == "" || c.Namespace == "" || c.Name == "" {
		return fmt.Errorf("%w: collection uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(c.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid collection uid %s", datastore.ErrInvalidArgument, c.UID)
	}
	if c.CreationTimestamp.IsZero() {
		c.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toCollectionRow(c)
	var (
		existed      bool
		nameReserved bool
		uidReserved  bool
	)
	err = s.mutations.execute(ctx,
		mutationAction{
			Step: catalogueStep("create", "Collection", c.UID, "collection_by_name", c.Namespace+"/"+c.Name, "reserve-name"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				nameReserved, reserveErr = s.reserveNameOwned(ctx, "Collection", "collection_by_name", row.Namespace, row.Name, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !nameReserved {
					return nil
				}
				return s.releaseName(ctx, "collection_by_name", row.Namespace, row.Name, uid)
			},
		},
		mutationAction{
			Step: catalogueStep("create", "Collection", c.UID, "collection_by_uid", c.UID, "reserve-uid"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				uidReserved, reserveErr = s.reserveUIDOwned(ctx, "Collection", "collection_by_uid", row.Namespace, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !uidReserved {
					return nil
				}
				return s.releaseUID(ctx, "collection_by_uid", row.Namespace, uid, row.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("create", "Collection", c.UID, "collection", c.UID, "write-authoritative"),
			Apply: func(ctx context.Context) error {
				applied, applyErr := s.insertAuthoritative(ctx, s.collectionTable, row)
				if applyErr != nil {
					return applyErr
				}
				if !applied {
					current, getErr := s.getCollectionByKey(row.Namespace, row.CreationTimestamp, uid)
					if getErr != nil {
						return getErr
					}
					if current.UID != c.UID || current.Name != c.Name {
						return fmt.Errorf("%w: collection uid %s already has a different authoritative row", datastore.ErrAlreadyExists, c.UID)
					}
					existed = true
				}
				return nil
			},
			Compensate: func(ctx context.Context) error {
				if existed {
					return nil
				}
				return s.deleteCollectionAuthoritative(ctx, row, row.ResourceVersion)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: create collection: %w", err)
	}
	if existed {
		return fmt.Errorf("%w: collection uid %s", datastore.ErrAlreadyExists, c.UID)
	}
	return nil
}

func (s *scyllaDatastore) GetCollection(ctx context.Context, uid string) (*datastore.Collection, error) {
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
	collection, err := s.getCollectionByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "Collection", ResourceUID: uid, Projection: "collection_by_uid",
			LookupKey: uid, Operation: "get", Type: datastore.FindingDangling,
		})
	}
	return collection, err
}

func (s *scyllaDatastore) GetCollectionByName(ctx context.Context, namespace, name string) (*datastore.Collection, error) {
	getName, nameNames := s.collectionByNameTable.Get()
	var nameRow collectionNameRow
	if err := s.session.Query(getName, nameNames).BindMap(qb.M{"namespace": namespace, "name": name}).GetRelease(&nameRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: collection %s/%s", datastore.ErrNotFound, namespace, name)
		}
		return nil, fmt.Errorf("scylla: get collection by name: %w", err)
	}
	collection, err := s.getCollectionByKey(nameRow.Namespace, nameRow.CreationTimestamp, nameRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "Collection", ResourceUID: nameRow.UID.String(), Projection: "collection_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingDangling,
		})
		return nil, err
	}
	if err == nil && (collection.Name != name || collection.Namespace != namespace) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "Collection", ResourceUID: nameRow.UID.String(), Projection: "collection_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingStale,
		})
		return nil, fmt.Errorf("%w: stale collection name projection %s/%s", datastore.ErrNotFound, namespace, name)
	}
	return collection, err
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
	if c == nil {
		return fmt.Errorf("%w: collection is nil", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(c.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid collection uid %s", datastore.ErrInvalidArgument, c.UID)
	}
	var existing *datastore.Collection
	if c.CreationTimestamp.IsZero() {
		existing, err = s.GetCollection(ctx, c.UID)
	} else {
		existing, err = s.getCollectionByKey(c.Namespace, c.CreationTimestamp, uid)
	}
	if err != nil {
		return err
	}
	if existing.Namespace != c.Namespace || existing.Name != c.Name {
		return fmt.Errorf("%w: collection identity is immutable", datastore.ErrConflict)
	}
	if err := validateResourceVersionTransition(existing.ResourceVersion, c.ResourceVersion); err != nil {
		return err
	}
	row := toCollectionRow(c)
	row.CreationTimestamp = existing.CreationTimestamp
	existingUID := mustParseUUID(existing.UID)

	err = s.mutations.executeUpdate(ctx, row.ResourceVersion,
		mutationAction{
			Step: catalogueStep("update", "Collection", c.UID, "collection", c.UID, "update-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.updateCollectionAuthoritative(ctx, row, existing.ResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("update", "Collection", c.UID, "collection_by_name", c.Namespace+"/"+c.Name, "converge-name"),
			Apply: func(ctx context.Context) error {
				return s.reserveName(ctx, "Collection", "collection_by_name", row.Namespace, row.Name, existingUID, row.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("update", "Collection", c.UID, "collection_by_uid", c.UID, "converge-uid"),
			Apply: func(ctx context.Context) error {
				return s.reserveUID(ctx, "Collection", "collection_by_uid", row.Namespace, existingUID, row.CreationTimestamp)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: update collection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteCollection(ctx context.Context, uid string) error {
	c, err := s.GetCollection(ctx, uid)
	if err != nil {
		return err
	}
	parsedUID := mustParseUUID(uid)

	err = s.mutations.executeDelete(ctx,
		mutationAction{
			Step: catalogueStep("delete", "Collection", uid, "collection", uid, "delete-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.deleteCollectionAuthoritative(ctx, toCollectionRow(c), c.ResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "Collection", uid, "collection_by_name", c.Namespace+"/"+c.Name, "delete-name"),
			Apply: func(ctx context.Context) error {
				return s.releaseName(ctx, "collection_by_name", c.Namespace, c.Name, parsedUID)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveName(ctx, "Collection", "collection_by_name", c.Namespace, c.Name, parsedUID, c.CreationTimestamp)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "Collection", uid, "collection_by_uid", uid, "delete-uid"),
			Apply: func(ctx context.Context) error {
				return s.releaseUID(ctx, "collection_by_uid", c.Namespace, parsedUID, c.CreationTimestamp)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveUID(ctx, "Collection", "collection_by_uid", c.Namespace, parsedUID, c.CreationTimestamp)
			},
		},
	)
	if err != nil {
		return fmt.Errorf("scylla: delete collection: %w", err)
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

// ── ProductVariant ────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateProductVariant(ctx context.Context, v *datastore.ProductVariant) error {
	if v == nil || v.UID == "" || v.Namespace == "" || v.Name == "" {
		return fmt.Errorf("%w: product variant uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(v.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid product variant uid %s", datastore.ErrInvalidArgument, v.UID)
	}
	if v.CreationTimestamp.IsZero() {
		v.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toProductVariantRow(v)
	var (
		existed      bool
		nameReserved bool
		uidReserved  bool
		skuReserved  bool
	)
	actions := []mutationAction{
		{
			Step: catalogueStep("create", "ProductVariant", v.UID, "product_variant_by_name", v.Namespace+"/"+v.Name, "reserve-name"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				nameReserved, reserveErr = s.reserveNameOwned(ctx, "ProductVariant", "product_variant_by_name", row.Namespace, row.Name, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !nameReserved {
					return nil
				}
				return s.releaseName(ctx, "product_variant_by_name", row.Namespace, row.Name, uid)
			},
		},
		{
			Step: catalogueStep("create", "ProductVariant", v.UID, "product_variant_by_uid", v.UID, "reserve-uid"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				uidReserved, reserveErr = s.reserveUIDOwned(ctx, "ProductVariant", "product_variant_by_uid", row.Namespace, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !uidReserved {
					return nil
				}
				return s.releaseUID(ctx, "product_variant_by_uid", row.Namespace, uid, row.CreationTimestamp)
			},
		},
	}
	if row.SKU != "" {
		actions = append(actions, mutationAction{
			Step: catalogueStep("create", "ProductVariant", v.UID, "product_variant_by_sku", v.Namespace+"/"+v.SKU, "reserve-sku"),
			Apply: func(ctx context.Context) error {
				var reserveErr error
				skuReserved, reserveErr = s.reserveSKUOwned(ctx, row.Namespace, row.SKU, uid, row.CreationTimestamp)
				return reserveErr
			},
			Compensate: func(ctx context.Context) error {
				if !skuReserved {
					return nil
				}
				return s.releaseSKU(ctx, row.Namespace, row.SKU, uid)
			},
		})
	}
	actions = append(actions, mutationAction{
		Step: catalogueStep("create", "ProductVariant", v.UID, "product_variant_by_namespace", v.UID, "write-authoritative"),
		Apply: func(ctx context.Context) error {
			applied, applyErr := s.insertAuthoritative(ctx, s.productVariantByNamespaceTable, row)
			if applyErr != nil {
				return applyErr
			}
			if !applied {
				current, getErr := s.getProductVariantByKey(row.Namespace, row.CreationTimestamp, uid)
				if getErr != nil {
					return getErr
				}
				if current.UID != v.UID || current.Name != v.Name {
					return fmt.Errorf("%w: product variant uid %s already has a different authoritative row", datastore.ErrAlreadyExists, v.UID)
				}
				existed = true
			}
			return nil
		},
		Compensate: func(ctx context.Context) error {
			if existed {
				return nil
			}
			return s.deleteProductVariantAuthoritative(ctx, row, row.ResourceVersion)
		},
	})
	if row.ProductRefName != "" {
		refRow := &productVariantProductRefRow{
			Namespace: row.Namespace, ProductRefName: row.ProductRefName,
			UID: uid, CreationTimestamp: row.CreationTimestamp,
		}
		actions = append(actions, mutationAction{
			Step: catalogueStep("create", "ProductVariant", v.UID, "product_variant_by_product_ref", v.Namespace+"/"+v.ProductRefName, "write-product-ref"),
			Apply: func(ctx context.Context) error {
				return s.insertProjection(ctx, s.productVariantByProductRefTable, refRow)
			},
			Compensate: func(ctx context.Context) error {
				if existed {
					return nil
				}
				return s.deleteProductRefProjection(ctx, refRow)
			},
		})
	}
	if err := s.mutations.execute(ctx, actions...); err != nil {
		return fmt.Errorf("scylla: create product_variant: %w", err)
	}
	if existed {
		return fmt.Errorf("%w: product variant uid %s", datastore.ErrAlreadyExists, v.UID)
	}
	return nil
}

func (s *scyllaDatastore) GetProductVariant(ctx context.Context, uid string) (*datastore.ProductVariant, error) {
	parsedUID, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid product_variant uid %s", datastore.ErrNotFound, uid)
	}
	stmt, names := s.productVariantByUIDTable.Get()
	var uidRow productVariantUIDRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"uid": parsedUID}).GetRelease(&uidRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product_variant uid %s", datastore.ErrNotFound, uid)
		}
		return nil, fmt.Errorf("scylla: get product_variant (uid lookup): %w", err)
	}
	variant, err := s.getProductVariantByKey(uidRow.Namespace, uidRow.CreationTimestamp, uidRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "ProductVariant", ResourceUID: uid, Projection: "product_variant_by_uid",
			LookupKey: uid, Operation: "get", Type: datastore.FindingDangling,
		})
	}
	return variant, err
}

func (s *scyllaDatastore) GetProductVariantByName(ctx context.Context, namespace, name string) (*datastore.ProductVariant, error) {
	stmt, names := s.productVariantByNameTable.Get()
	var nameRow productVariantNameRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace": namespace, "name": name}).GetRelease(&nameRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product_variant %s/%s", datastore.ErrNotFound, namespace, name)
		}
		return nil, fmt.Errorf("scylla: get product_variant by name: %w", err)
	}
	variant, err := s.getProductVariantByKey(nameRow.Namespace, nameRow.CreationTimestamp, nameRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "ProductVariant", ResourceUID: nameRow.UID.String(), Projection: "product_variant_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingDangling,
		})
		return nil, err
	}
	if err == nil && (variant.Name != name || variant.Namespace != namespace) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "ProductVariant", ResourceUID: nameRow.UID.String(), Projection: "product_variant_by_name",
			LookupKey: namespace + "/" + name, Operation: "get_by_name", Type: datastore.FindingStale,
		})
		return nil, fmt.Errorf("%w: stale product variant name projection %s/%s", datastore.ErrNotFound, namespace, name)
	}
	return variant, err
}

func (s *scyllaDatastore) GetProductVariantBySKU(ctx context.Context, namespace, sku string) (*datastore.ProductVariant, error) {
	stmt, names := s.productVariantBySKUTable.Get()
	var skuRow productVariantSKURow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace": namespace, "sku": sku}).GetRelease(&skuRow); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product_variant sku %s/%s", datastore.ErrNotFound, namespace, sku)
		}
		return nil, fmt.Errorf("scylla: get product_variant by sku: %w", err)
	}
	variant, err := s.getProductVariantByKey(skuRow.Namespace, skuRow.CreationTimestamp, skuRow.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "ProductVariant", ResourceUID: skuRow.UID.String(), Projection: "product_variant_by_sku",
			LookupKey: namespace + "/" + sku, Operation: "get_by_sku", Type: datastore.FindingDangling,
		})
		return nil, err
	}
	if err == nil && (variant.SKU != sku || variant.Namespace != namespace) {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "ProductVariant", ResourceUID: skuRow.UID.String(), Projection: "product_variant_by_sku",
			LookupKey: namespace + "/" + sku, Operation: "get_by_sku", Type: datastore.FindingStale,
		})
		return nil, fmt.Errorf("%w: stale product variant sku projection %s/%s", datastore.ErrNotFound, namespace, sku)
	}
	return variant, err
}

func (s *scyllaDatastore) getProductVariantByKey(namespace string, createdAt time.Time, uid gocql.UUID) (*datastore.ProductVariant, error) {
	cols := strings.Join(s.productVariantByNamespaceTable.Metadata().Columns, ", ")
	stmt := fmt.Sprintf(
		"SELECT %s FROM product_variant_by_namespace WHERE namespace = ? AND creation_timestamp = ? AND uid = ?",
		cols,
	)
	var row productVariantRow
	if err := s.session.Query(stmt, nil).Bind(namespace, createdAt, uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: product_variant namespace=%s uid=%s", datastore.ErrNotFound, namespace, uid)
		}
		return nil, fmt.Errorf("scylla: get product_variant by key: %w", err)
	}
	return fromProductVariantRow(&row), nil
}

func (s *scyllaDatastore) ListProductVariants(_ context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.ProductVariant], error) {
	limit := page.Limit()
	pq := buildPaginatedSelect(s.productVariantByNamespaceTable, page, "namespace", namespace, productClusterKeys, nil, nil)

	var rows []productVariantRow
	if err := s.session.Query(pq.Stmt, nil).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list product_variants: %w", err)
	}

	if page.Last > 0 {
		reverseRows(rows)
	}

	items := make([]*datastore.ProductVariant, len(rows))
	for i := range rows {
		items[i] = fromProductVariantRow(&rows[i])
	}
	return buildPageResult(items, limit, page), nil
}

func (s *scyllaDatastore) ListProductVariantsByProductRef(ctx context.Context, namespace, productRefName string) ([]*datastore.ProductVariant, error) {
	cols := strings.Join(s.productVariantByProductRefTable.Metadata().Columns, ", ")
	stmt := fmt.Sprintf(
		"SELECT %s FROM product_variant_by_product_ref WHERE namespace = ? AND product_ref_name = ?",
		cols,
	)
	var refRows []productVariantProductRefRow
	if err := s.session.Query(stmt, nil).Bind(namespace, productRefName).SelectRelease(&refRows); err != nil {
		return nil, fmt.Errorf("scylla: list product_variants by product_ref: %w", err)
	}
	result := make([]*datastore.ProductVariant, 0, len(refRows))
	for _, r := range refRows {
		v, err := s.getProductVariantByKey(r.Namespace, r.CreationTimestamp, r.UID)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				s.reportFinding(ctx, datastore.ProjectionFinding{
					ResourceKind: "ProductVariant", ResourceUID: r.UID.String(), Projection: "product_variant_by_product_ref",
					LookupKey: namespace + "/" + productRefName, Operation: "list_by_product_ref", Type: datastore.FindingDangling,
				})
			}
			continue
		}
		if v.ProductRefName != productRefName {
			s.reportFinding(ctx, datastore.ProjectionFinding{
				ResourceKind: "ProductVariant", ResourceUID: r.UID.String(), Projection: "product_variant_by_product_ref",
				LookupKey: namespace + "/" + productRefName, Operation: "list_by_product_ref", Type: datastore.FindingStale,
			})
			continue
		}
		result = append(result, v)
	}
	return result, nil
}

func (s *scyllaDatastore) UpdateProductVariant(ctx context.Context, v *datastore.ProductVariant) error {
	if v == nil {
		return fmt.Errorf("%w: product variant is nil", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(v.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid product variant uid %s", datastore.ErrInvalidArgument, v.UID)
	}
	var existing *datastore.ProductVariant
	if v.CreationTimestamp.IsZero() {
		existing, err = s.GetProductVariant(ctx, v.UID)
	} else {
		existing, err = s.getProductVariantByKey(v.Namespace, v.CreationTimestamp, uid)
	}
	if err != nil {
		return err
	}
	if existing.Namespace != v.Namespace || existing.Name != v.Name {
		return fmt.Errorf("%w: product variant identity is immutable", datastore.ErrConflict)
	}
	if err := validateResourceVersionTransition(existing.ResourceVersion, v.ResourceVersion); err != nil {
		return err
	}
	row := toProductVariantRow(v)
	row.CreationTimestamp = existing.CreationTimestamp
	existingUID := mustParseUUID(existing.UID)
	skuConverged := row.SKU == ""
	productRefConverged := row.ProductRefName == ""

	projections := []mutationAction{
		{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_name", v.Namespace+"/"+v.Name, "converge-name"),
			Apply: func(ctx context.Context) error {
				return s.reserveName(ctx, "ProductVariant", "product_variant_by_name", row.Namespace, row.Name, existingUID, row.CreationTimestamp)
			},
		},
		{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_uid", v.UID, "converge-uid"),
			Apply: func(ctx context.Context) error {
				return s.reserveUID(ctx, "ProductVariant", "product_variant_by_uid", row.Namespace, existingUID, row.CreationTimestamp)
			},
		},
	}
	if row.SKU != "" {
		projections = append(projections, mutationAction{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_sku", v.Namespace+"/"+v.SKU, "converge-sku"),
			Apply: func(ctx context.Context) error {
				if err := s.reserveSKU(ctx, row.Namespace, row.SKU, existingUID, row.CreationTimestamp); err != nil {
					return err
				}
				skuConverged = true
				return nil
			},
		})
	}
	if existing.SKU != "" && existing.SKU != row.SKU {
		projections = append(projections, mutationAction{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_sku", v.Namespace+"/"+existing.SKU, "delete-old-sku"),
			Apply: func(ctx context.Context) error {
				if !skuConverged {
					return errors.New("new sku reservation has not converged")
				}
				return s.releaseSKU(ctx, row.Namespace, existing.SKU, existingUID)
			},
		})
	}
	if row.ProductRefName != "" {
		refRow := &productVariantProductRefRow{
			Namespace: row.Namespace, ProductRefName: row.ProductRefName,
			UID: existingUID, CreationTimestamp: row.CreationTimestamp,
		}
		projections = append(projections, mutationAction{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_product_ref", v.Namespace+"/"+v.ProductRefName, "converge-product-ref"),
			Apply: func(ctx context.Context) error {
				if err := s.insertProjection(ctx, s.productVariantByProductRefTable, refRow); err != nil {
					return err
				}
				productRefConverged = true
				return nil
			},
		})
	}
	if existing.ProductRefName != "" && existing.ProductRefName != row.ProductRefName {
		oldRef := &productVariantProductRefRow{
			Namespace: row.Namespace, ProductRefName: existing.ProductRefName,
			UID: existingUID, CreationTimestamp: row.CreationTimestamp,
		}
		projections = append(projections, mutationAction{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_product_ref", v.Namespace+"/"+existing.ProductRefName, "delete-old-product-ref"),
			Apply: func(ctx context.Context) error {
				if !productRefConverged {
					return errors.New("new product reference projection has not converged")
				}
				return s.deleteProductRefProjection(ctx, oldRef)
			},
		})
	}
	err = s.mutations.executeUpdate(ctx, row.ResourceVersion,
		mutationAction{
			Step: catalogueStep("update", "ProductVariant", v.UID, "product_variant_by_namespace", v.UID, "update-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.updateProductVariantAuthoritative(ctx, row, existing.ResourceVersion)
			},
		},
		projections...,
	)
	if err != nil {
		return fmt.Errorf("scylla: update product_variant: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteProductVariant(ctx context.Context, uid string) error {
	v, err := s.GetProductVariant(ctx, uid)
	if err != nil {
		return err
	}
	parsedUID := mustParseUUID(uid)
	projections := []mutationAction{
		{
			Step: catalogueStep("delete", "ProductVariant", uid, "product_variant_by_name", v.Namespace+"/"+v.Name, "delete-name"),
			Apply: func(ctx context.Context) error {
				return s.releaseName(ctx, "product_variant_by_name", v.Namespace, v.Name, parsedUID)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveName(ctx, "ProductVariant", "product_variant_by_name", v.Namespace, v.Name, parsedUID, v.CreationTimestamp)
			},
		},
		{
			Step: catalogueStep("delete", "ProductVariant", uid, "product_variant_by_uid", uid, "delete-uid"),
			Apply: func(ctx context.Context) error {
				return s.releaseUID(ctx, "product_variant_by_uid", v.Namespace, parsedUID, v.CreationTimestamp)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveUID(ctx, "ProductVariant", "product_variant_by_uid", v.Namespace, parsedUID, v.CreationTimestamp)
			},
		},
	}
	if v.SKU != "" {
		projections = append(projections, mutationAction{
			Step: catalogueStep("delete", "ProductVariant", uid, "product_variant_by_sku", v.Namespace+"/"+v.SKU, "delete-sku"),
			Apply: func(ctx context.Context) error {
				return s.releaseSKU(ctx, v.Namespace, v.SKU, parsedUID)
			},
			Compensate: func(ctx context.Context) error {
				return s.reserveSKU(ctx, v.Namespace, v.SKU, parsedUID, v.CreationTimestamp)
			},
		})
	}
	if v.ProductRefName != "" {
		refRow := &productVariantProductRefRow{
			Namespace: v.Namespace, ProductRefName: v.ProductRefName,
			UID: parsedUID, CreationTimestamp: v.CreationTimestamp,
		}
		projections = append(projections, mutationAction{
			Step: catalogueStep("delete", "ProductVariant", uid, "product_variant_by_product_ref", v.Namespace+"/"+v.ProductRefName, "delete-product-ref"),
			Apply: func(ctx context.Context) error {
				return s.deleteProductRefProjection(ctx, refRow)
			},
			Compensate: func(ctx context.Context) error {
				return s.insertProjection(ctx, s.productVariantByProductRefTable, refRow)
			},
		})
	}
	err = s.mutations.executeDelete(ctx,
		mutationAction{
			Step: catalogueStep("delete", "ProductVariant", uid, "product_variant_by_namespace", uid, "delete-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.deleteProductVariantAuthoritative(ctx, toProductVariantRow(v), v.ResourceVersion)
			},
		},
		projections...,
	)
	if err != nil {
		return fmt.Errorf("scylla: delete product_variant: %w", err)
	}
	return nil
}

func catalogueStep(operation, resourceKind, uid, projection, lookupKey, action string) datastore.MutationStep {
	return datastore.MutationStep{
		Operation:    operation,
		ResourceKind: resourceKind,
		ResourceUID:  uid,
		Projection:   projection,
		LookupKey:    lookupKey,
		Action:       action,
	}
}

func validateResourceVersionTransition(current, desired string) error {
	if current == "" || desired == "" || current == desired {
		return nil
	}
	currentVersion, currentOK := new(big.Int).SetString(current, 10)
	desiredVersion, desiredOK := new(big.Int).SetString(desired, 10)
	if !currentOK || !desiredOK || currentVersion.Sign() < 0 || desiredVersion.Sign() < 1 {
		return fmt.Errorf("%w: invalid resource version transition %q -> %q", datastore.ErrConflict, current, desired)
	}
	if desiredVersion.Cmp(new(big.Int).Add(currentVersion, big.NewInt(1))) != 0 {
		return fmt.Errorf("%w: stale resource version transition %q -> %q", datastore.ErrConflict, current, desired)
	}
	return nil
}

func (s *scyllaDatastore) insertAuthoritative(ctx context.Context, target *table.Table, row any) (bool, error) {
	statement, names := target.Insert()
	applied, err := s.session.Query(statement+" IF NOT EXISTS", names).WithContext(ctx).
		BindStruct(row).ExecCASRelease()
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *scyllaDatastore) insertProjection(ctx context.Context, target *table.Table, row any) error {
	statement, names := target.Insert()
	return s.session.Query(statement, names).WithContext(ctx).BindStruct(row).ExecRelease()
}

func (s *scyllaDatastore) updateProductAuthoritative(ctx context.Context, row *productRow, expectedResourceVersion string) error {
	const statement = "UPDATE products_by_namespace SET name=?,api_version=?,kind=?,generation=?,resource_version=?,revision=?," +
		"creation_actor=?,update_timestamp=?,update_actor=?,labels=?,annotations=?,owner_references=?,finalizers=?,deletion_timestamp=?," +
		"repository_id=?,source_path=?,git_commit_sha=?,git_ref=?,spec=?,body=?,status=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision,
		row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations,
		row.OwnerReferences, row.Finalizers, row.DeletionTimestamp, row.RepositoryID,
		row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) updateCategoryTaxonomyAuthoritative(ctx context.Context, row *categoryTaxonomyRow, expectedResourceVersion string) error {
	const statement = "UPDATE category_taxonomy SET name=?,api_version=?,kind=?,generation=?,resource_version=?,revision=?," +
		"creation_actor=?,update_timestamp=?,update_actor=?,labels=?,annotations=?,owner_references=?,finalizers=?,deletion_timestamp=?," +
		"repository_id=?,source_path=?,git_commit_sha=?,git_ref=?,spec=?,body=?,status=?,parent_name=?,ancestor_path=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision,
		row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations,
		row.OwnerReferences, row.Finalizers, row.DeletionTimestamp, row.RepositoryID,
		row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.ParentName, row.AncestorPath, row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) updateCollectionAuthoritative(ctx context.Context, row *collectionRow, expectedResourceVersion string) error {
	const statement = "UPDATE collection SET name=?,api_version=?,kind=?,generation=?,resource_version=?,revision=?," +
		"creation_actor=?,update_timestamp=?,update_actor=?,labels=?,annotations=?,owner_references=?,finalizers=?,deletion_timestamp=?," +
		"repository_id=?,source_path=?,git_commit_sha=?,git_ref=?,spec=?,body=?,status=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision,
		row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations,
		row.OwnerReferences, row.Finalizers, row.DeletionTimestamp, row.RepositoryID,
		row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) updateProductVariantAuthoritative(ctx context.Context, row *productVariantRow, expectedResourceVersion string) error {
	const statement = "UPDATE product_variant_by_namespace SET name=?,api_version=?,kind=?,generation=?,resource_version=?,revision=?," +
		"creation_actor=?,update_timestamp=?,update_actor=?,labels=?,annotations=?,owner_references=?,finalizers=?,deletion_timestamp=?," +
		"repository_id=?,source_path=?,git_commit_sha=?,git_ref=?,spec=?,body=?,status=?,sku=?,product_ref_name=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision,
		row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations,
		row.OwnerReferences, row.Finalizers, row.DeletionTimestamp, row.RepositoryID,
		row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.SKU, row.ProductRefName, row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) deleteProductAuthoritative(ctx context.Context, row *productRow, expectedResourceVersion string) error {
	return s.deleteAuthoritative(ctx,
		"DELETE FROM products_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
		row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	)
}

func (s *scyllaDatastore) deleteCategoryTaxonomyAuthoritative(ctx context.Context, row *categoryTaxonomyRow, expectedResourceVersion string) error {
	return s.deleteAuthoritative(ctx,
		"DELETE FROM category_taxonomy WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
		row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	)
}

func (s *scyllaDatastore) deleteCollectionAuthoritative(ctx context.Context, row *collectionRow, expectedResourceVersion string) error {
	return s.deleteAuthoritative(ctx,
		"DELETE FROM collection WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
		row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	)
}

func (s *scyllaDatastore) deleteProductVariantAuthoritative(ctx context.Context, row *productVariantRow, expectedResourceVersion string) error {
	return s.deleteAuthoritative(ctx,
		"DELETE FROM product_variant_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
		row.Namespace, row.CreationTimestamp, row.UID, expectedResourceVersion,
	)
}

func (s *scyllaDatastore) deleteAuthoritative(ctx context.Context, statement string, args ...any) error {
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(args...).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) deleteProductRefProjection(ctx context.Context, row *productVariantProductRefRow) error {
	return s.session.Query(
		"DELETE FROM product_variant_by_product_ref WHERE namespace=? AND product_ref_name=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(row.Namespace, row.ProductRefName, row.CreationTimestamp, row.UID).ExecRelease()
}

// ── Namespace ─────────────────────────────────────────────────────────────────

func (s *scyllaDatastore) CreateNamespace(ctx context.Context, ns *datastore.Namespace) error {
	if ns == nil {
		return fmt.Errorf("%w: namespace is nil", datastore.ErrInvalidArgument)
	}
	if ns.UID == "" {
		return fmt.Errorf("%w: namespace uid is empty", datastore.ErrInvalidArgument)
	}
	if _, err := gocql.ParseUUID(ns.UID); err != nil {
		return fmt.Errorf("%w: invalid namespace uid %s", datastore.ErrInvalidArgument, ns.UID)
	}
	datastore.NormalizeNamespaceContract(ns)
	row := toNamespaceRow(ns)

	const reserveName = "INSERT INTO namespaces_by_name (name, uid) VALUES (?, ?) IF NOT EXISTS"
	applied, err := s.session.Query(reserveName, nil).WithContext(ctx).
		Bind(row.Name, row.UID).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: reserve namespace name: %w", err)
	}
	if !applied {
		return fmt.Errorf("%w: namespace name %s", datastore.ErrAlreadyExists, ns.Name)
	}

	insertByUID, names := s.namespaceByUIDTable.Insert()
	applied, err = s.session.Query(insertByUID+" IF NOT EXISTS", names).WithContext(ctx).
		BindStruct(row).ExecCASRelease()
	if err != nil || !applied {
		s.releaseNamespaceName(ctx, row.Name, row.UID)
		if err != nil {
			return fmt.Errorf("scylla: create namespace by uid: %w", err)
		}
		return fmt.Errorf("%w: namespace uid %s", datastore.ErrAlreadyExists, ns.UID)
	}

	const insertIndex = "INSERT INTO namespaces_by_bucket (bucket, creation_timestamp, uid) VALUES (?, ?, ?)"
	if err := s.session.Query(insertIndex, nil).WithContext(ctx).
		Bind(namespaceBucket(row.CreationTimestamp), row.CreationTimestamp, row.UID).ExecRelease(); err != nil {
		_ = s.session.Query("DELETE FROM namespaces_by_uid WHERE uid=?", nil).WithContext(ctx).Bind(row.UID).ExecRelease()
		s.releaseNamespaceName(ctx, row.Name, row.UID)
		return fmt.Errorf("scylla: create namespace listing index: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetNamespace(ctx context.Context, uidString string) (*datastore.Namespace, error) {
	uid, err := gocql.ParseUUID(uidString)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid namespace uid %s", datastore.ErrNotFound, uidString)
	}
	stmt, names := s.namespaceByUIDTable.Get()
	var row namespaceRow
	if err := s.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"uid": uid}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace uid %s", datastore.ErrNotFound, uidString)
		}
		return nil, fmt.Errorf("scylla: get namespace: %w", err)
	}
	return fromNamespaceRow(&row), nil
}

func (s *scyllaDatastore) GetNamespaceByName(ctx context.Context, name string) (*datastore.Namespace, error) {
	stmt, names := s.namespaceByNameTable.Get()
	var row namespaceNameRow
	if err := s.session.Query(stmt, names).WithContext(ctx).
		BindMap(qb.M{"name": name}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace name %s", datastore.ErrNotFound, name)
		}
		return nil, fmt.Errorf("scylla: get namespace by name: %w", err)
	}
	namespace, err := s.GetNamespace(ctx, row.UID.String())
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, fmt.Errorf("%w: namespace name %s", datastore.ErrNotFound, name)
	}
	return namespace, err
}

func (s *scyllaDatastore) ListNamespaces(ctx context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Namespace], error) {
	limit := page.Limit()
	backward := page.Last > 0
	rows := make([]namespaceIndexRow, 0, limit+1)
	for _, bucket := range namespaceBucketsForPage(page, time.Now().UTC()) {
		bucketPage := page
		if page.After != "" && !cursorInNamespaceBucket(page.After, bucket) {
			bucketPage.After = ""
		}
		if page.Before != "" && !cursorInNamespaceBucket(page.Before, bucket) {
			bucketPage.Before = ""
		}
		pq := buildPaginatedSelect(s.namespaceByBucketTable, bucketPage, "bucket", bucket, namespaceClusterKeys, nil, nil)
		var bucketRows []namespaceIndexRow
		if err := s.session.Query(pq.Stmt, nil).WithContext(ctx).Bind(pq.Args...).SelectRelease(&bucketRows); err != nil {
			return nil, fmt.Errorf("scylla: list namespaces bucket %s: %w", bucket, err)
		}
		rows = append(rows, bucketRows...)
		if len(rows) >= limit+1 {
			rows = rows[:limit+1]
			break
		}
	}
	if backward {
		reverseRows(rows)
	}

	namespaces := make([]*datastore.Namespace, 0, len(rows))
	for _, row := range rows {
		namespace, err := s.GetNamespace(ctx, row.UID.String())
		if errors.Is(err, datastore.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("scylla: hydrate listed namespace: %w", err)
		}
		namespaces = append(namespaces, namespace)
	}

	return buildPageResult(namespaces, limit, page), nil
}

func (s *scyllaDatastore) UpdateNamespace(ctx context.Context, ns *datastore.Namespace, expectedResourceVersion string) error {
	row := toNamespaceRow(ns)
	const statement = "UPDATE namespaces_by_uid SET api_version=?, kind=?, name=?, title=?, tier=?, generation=?, resource_version=?, revision=?, " +
		"creation_timestamp=?, creation_actor=?, update_timestamp=?, update_actor=?, labels=?, annotations=?, owner_references=?, " +
		"finalizers=?, deletion_timestamp=?, source_path=?, git_commit_sha=?, git_ref=?, spec=?, body=?, status=? " +
		"WHERE uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		row.APIVersion, row.Kind, row.Name, row.Title, row.Tier, row.Generation, row.ResourceVersion, row.Revision,
		row.CreationTimestamp, row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations, row.OwnerReferences,
		row.Finalizers, row.DeletionTimestamp, row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.UID, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: update namespace: %w", err)
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) DeleteNamespace(ctx context.Context, uidString string) error {
	ns, err := s.GetNamespace(ctx, uidString)
	if err != nil {
		return err
	}
	if err := s.deleteNamespaceIndexes(ctx, ns); err != nil {
		return err
	}
	uid := mustParseUUID(uidString)
	if err := s.session.Query("DELETE FROM namespaces_by_uid WHERE uid=?", nil).WithContext(ctx).Bind(uid).ExecRelease(); err != nil {
		if restoreErr := s.restoreNamespaceIndexes(ctx, ns); restoreErr != nil {
			return datastore.NewRepairRequiredError(
				datastore.MutationStep{
					Operation:    "delete_namespace",
					ResourceKind: "Namespace",
					ResourceUID:  uidString,
					Projection:   "namespaces_by_uid",
					Action:       "delete_authoritative",
				},
				fmt.Errorf("scylla: delete namespace: %w", err),
				restoreErr,
			)
		}
		return fmt.Errorf("scylla: delete namespace: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteNamespaceWithResourceVersion(ctx context.Context, uidString, expectedResourceVersion string) error {
	ns, err := s.GetNamespace(ctx, uidString)
	if err != nil {
		return err
	}
	if err := s.deleteNamespaceIndexes(ctx, ns); err != nil {
		return err
	}
	const statement = "DELETE FROM namespaces_by_uid WHERE uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		mustParseUUID(uidString), expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		_ = s.restoreNamespaceIndexes(ctx, ns)
		return fmt.Errorf("scylla: delete namespace with resource version: %w", err)
	}
	if !applied {
		_ = s.restoreNamespaceIndexes(ctx, ns)
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) deleteNamespaceIndexes(ctx context.Context, ns *datastore.Namespace) error {
	uid := mustParseUUID(ns.UID)
	var cleanupErr error
	if err := s.session.Query(
		"DELETE FROM namespaces_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(namespaceBucket(ns.CreationTimestamp), ns.CreationTimestamp, uid).ExecRelease(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete namespace listing index: %w", err))
	}
	const releaseName = "DELETE FROM namespaces_by_name WHERE name=? IF uid=?"
	if _, err := s.session.Query(releaseName, nil).WithContext(ctx).
		Bind(ns.Name, uid).ExecCASRelease(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete namespace name index: %w", err))
	}
	if cleanupErr != nil {
		return fmt.Errorf("scylla: delete namespace indexes: %w", cleanupErr)
	}
	return nil
}

func (s *scyllaDatastore) restoreNamespaceIndexes(ctx context.Context, ns *datastore.Namespace) error {
	uid := mustParseUUID(ns.UID)
	if err := s.session.Query(
		"INSERT INTO namespaces_by_name (name, uid) VALUES (?, ?)",
		nil,
	).WithContext(ctx).Bind(ns.Name, uid).ExecRelease(); err != nil {
		return fmt.Errorf("restore namespace name index: %w", err)
	}
	if err := s.session.Query(
		"INSERT INTO namespaces_by_bucket (bucket, creation_timestamp, uid) VALUES (?, ?, ?)",
		nil,
	).WithContext(ctx).Bind(namespaceBucket(ns.CreationTimestamp), ns.CreationTimestamp, uid).ExecRelease(); err != nil {
		return fmt.Errorf("restore namespace listing index: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) releaseNamespaceName(ctx context.Context, name string, uid gocql.UUID) {
	const statement = "DELETE FROM namespaces_by_name WHERE name=? IF uid=?"
	_, _ = s.session.Query(statement, nil).WithContext(ctx).Bind(name, uid).ExecCASRelease()
}

// catalogTablesByRepositoryID lists every namespace-partitioned catalog table,
// checked in order by HasCatalogResources with short-circuit on first match.
var catalogTablesByRepositoryID = []string{
	"products_by_namespace",
	"product_variant_by_namespace",
	"category_taxonomy",
	"collection",
}

// HasCatalogResources reports whether at least one Product, ProductVariant,
// CategoryTaxonomy, or Collection row currently has repository_id == repoID.
func (s *scyllaDatastore) HasCatalogResources(ctx context.Context, repoID string) (bool, error) {
	repo, err := s.GetRepository(ctx, repoID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("scylla: has catalog resources: %w", err)
	}
	for _, tableName := range catalogTablesByRepositoryID {
		stmt, names := qb.Select(tableName).
			Columns("repository_id").
			Where(qb.Eq("namespace"), qb.Eq("repository_id")).
			Limit(1).
			AllowFiltering().
			ToCql()
		var row struct {
			RepositoryID gocql.UUID `db:"repository_id"`
		}
		err := s.session.Query(stmt, names).BindMap(qb.M{
			"namespace":     repo.Namespace,
			"repository_id": mustParseUUID(repoID),
		}).GetRelease(&row)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, gocql.ErrNotFound) {
			return false, fmt.Errorf("scylla: has catalog resources (%s): %w", tableName, err)
		}
	}
	return false, nil
}

// ── row conversion helpers ────────────────────────────────────────────────────

func toProductRow(p *datastore.Product) *productRow {
	ownerReferences := ""
	if len(p.OwnerReferences) > 0 {
		ownerReferences = string(p.OwnerReferences)
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
		CreationActor:     p.CreationActor,
		UpdateTimestamp:   p.UpdateTimestamp,
		UpdateActor:       p.UpdateActor,
		Labels:            p.Labels,
		Annotations:       p.Annotations,
		OwnerReferences:   ownerReferences,
		Finalizers:        append([]string(nil), p.Finalizers...),
		DeletionTimestamp: p.DeletionTimestamp,
		RepositoryID:      optionalUUID(p.RepositoryID),
		SourcePath:        p.SourcePath,
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
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		OwnerReferences:   jsonOrNil(r.OwnerReferences),
		Finalizers:        append([]string(nil), r.Finalizers...),
		DeletionTimestamp: r.DeletionTimestamp,
		RepositoryID:      uuidString(r.RepositoryID),
		SourcePath:        r.SourcePath,
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
		UID:               mustParseUUID(c.UID),
		APIVersion:        c.APIVersion,
		Kind:              c.Kind,
		Generation:        c.Generation,
		ResourceVersion:   c.ResourceVersion,
		CreationTimestamp: c.CreationTimestamp,
		Revision:          c.Revision,
		CreationActor:     c.CreationActor,
		UpdateTimestamp:   c.UpdateTimestamp,
		UpdateActor:       c.UpdateActor,
		Labels:            c.Labels,
		Annotations:       c.Annotations,
		OwnerReferences:   string(c.OwnerReferences),
		Finalizers:        append([]string(nil), c.Finalizers...),
		DeletionTimestamp: c.DeletionTimestamp,
		ParentName:        c.ParentName,
		AncestorPath:      c.AncestorPath,
		RepositoryID:      optionalUUID(c.RepositoryID),
		SourcePath:        c.SourcePath,
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
		UID:               r.UID.String(),
		APIVersion:        r.APIVersion,
		Kind:              r.Kind,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		Revision:          r.Revision,
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		OwnerReferences:   jsonOrNil(r.OwnerReferences),
		Finalizers:        append([]string(nil), r.Finalizers...),
		DeletionTimestamp: r.DeletionTimestamp,
		ParentName:        r.ParentName,
		AncestorPath:      r.AncestorPath,
		RepositoryID:      uuidString(r.RepositoryID),
		SourcePath:        r.SourcePath,
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
		CreationActor:     c.CreationActor,
		UpdateTimestamp:   c.UpdateTimestamp,
		UpdateActor:       c.UpdateActor,
		Labels:            c.Labels,
		Annotations:       c.Annotations,
		OwnerReferences:   string(c.OwnerReferences),
		Finalizers:        append([]string(nil), c.Finalizers...),
		DeletionTimestamp: c.DeletionTimestamp,
		RepositoryID:      optionalUUID(c.RepositoryID),
		SourcePath:        c.SourcePath,
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
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		OwnerReferences:   jsonOrNil(r.OwnerReferences),
		Finalizers:        append([]string(nil), r.Finalizers...),
		DeletionTimestamp: r.DeletionTimestamp,
		RepositoryID:      uuidString(r.RepositoryID),
		SourcePath:        r.SourcePath,
		GitCommitSHA:      r.GitCommitSHA,
		GitRef:            r.GitRef,
		Spec:              jsonOrNil(r.Spec),
		Body:              r.Body,
		Status:            jsonOrNil(r.Status),
	}
}

func toProductVariantRow(v *datastore.ProductVariant) *productVariantRow {
	return &productVariantRow{
		Namespace:         v.Namespace,
		CreationTimestamp: v.CreationTimestamp,
		UID:               mustParseUUID(v.UID),
		Name:              v.Name,
		APIVersion:        v.APIVersion,
		Kind:              v.Kind,
		Generation:        v.Generation,
		ResourceVersion:   v.ResourceVersion,
		Revision:          v.Revision,
		CreationActor:     v.CreationActor,
		UpdateTimestamp:   v.UpdateTimestamp,
		UpdateActor:       v.UpdateActor,
		Labels:            v.Labels,
		Annotations:       v.Annotations,
		OwnerReferences:   string(v.OwnerReferences),
		Finalizers:        append([]string(nil), v.Finalizers...),
		DeletionTimestamp: v.DeletionTimestamp,
		SKU:               v.SKU,
		ProductRefName:    v.ProductRefName,
		RepositoryID:      optionalUUID(v.RepositoryID),
		SourcePath:        v.SourcePath,
		GitCommitSHA:      v.GitCommitSHA,
		GitRef:            v.GitRef,
		Spec:              string(v.Spec),
		Body:              v.Body,
		Status:            string(v.Status),
	}
}

func fromProductVariantRow(r *productVariantRow) *datastore.ProductVariant {
	return &datastore.ProductVariant{
		UID:               r.UID.String(),
		Namespace:         r.Namespace,
		Name:              r.Name,
		APIVersion:        r.APIVersion,
		Kind:              r.Kind,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		Revision:          r.Revision,
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		OwnerReferences:   jsonOrNil(r.OwnerReferences),
		Finalizers:        append([]string(nil), r.Finalizers...),
		DeletionTimestamp: r.DeletionTimestamp,
		SKU:               r.SKU,
		ProductRefName:    r.ProductRefName,
		RepositoryID:      uuidString(r.RepositoryID),
		SourcePath:        r.SourcePath,
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
	datastore.NormalizeNamespaceContract(ns)
	return &namespaceRow{
		APIVersion:        ns.APIVersion,
		Kind:              ns.Kind,
		UID:               mustParseUUID(ns.UID),
		Name:              ns.Name,
		Title:             ns.Title,
		Tier:              string(ns.Tier),
		Generation:        ns.Generation,
		ResourceVersion:   ns.ResourceVersion,
		Revision:          ns.Revision,
		CreationTimestamp: ns.CreationTimestamp,
		CreationActor:     ns.CreationActor,
		UpdateTimestamp:   ns.UpdateTimestamp,
		UpdateActor:       ns.UpdateActor,
		Labels:            ns.Labels,
		Annotations:       ns.Annotations,
		OwnerReferences:   string(ns.OwnerReferences),
		Finalizers:        append([]string(nil), ns.Finalizers...),
		DeletionTimestamp: ns.DeletionTimestamp,
		SourcePath:        ns.SourcePath,
		GitCommitSHA:      ns.GitCommitSHA,
		GitRef:            ns.GitRef,
		Spec:              string(ns.Spec),
		Body:              ns.Body,
		Status:            string(ns.Status),
	}
}

func fromNamespaceRow(r *namespaceRow) *datastore.Namespace {
	namespace := &datastore.Namespace{
		APIVersion:        r.APIVersion,
		Kind:              r.Kind,
		UID:               r.UID.String(),
		Name:              r.Name,
		Title:             r.Title,
		Tier:              datastore.NamespaceTier(r.Tier),
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		Revision:          r.Revision,
		CreationTimestamp: r.CreationTimestamp,
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		Labels:            r.Labels,
		Annotations:       r.Annotations,
		OwnerReferences:   jsonOrNil(r.OwnerReferences),
		Finalizers:        append([]string(nil), r.Finalizers...),
		DeletionTimestamp: r.DeletionTimestamp,
		SourcePath:        r.SourcePath,
		GitCommitSHA:      r.GitCommitSHA,
		GitRef:            r.GitRef,
		Spec:              jsonOrNil(r.Spec),
		Body:              r.Body,
		Status:            jsonOrNil(r.Status),
	}
	datastore.NormalizeNamespaceContract(namespace)
	return namespace
}
