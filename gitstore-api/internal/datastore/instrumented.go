// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// InstrumentedDatastore wraps any Datastore with per-operation Prometheus
// metrics (latency histogram, error counter) and structured zap error logs.
type InstrumentedDatastore struct {
	next    Datastore
	backend string
	log     *zap.Logger
	metrics *datastoreMetrics
}

// NewInstrumentedDatastore returns a Datastore that records metrics and logs
// errors for every operation on next. Metrics are registered on the default
// Prometheus registry.
func NewInstrumentedDatastore(next Datastore, backend string, log *zap.Logger) Datastore {
	return NewInstrumentedDatastoreWithRegistry(next, backend, log, prometheus.DefaultRegisterer)
}

// NewInstrumentedDatastoreWithRegistry is like NewInstrumentedDatastore but
// registers metrics on reg, enabling isolated registries in tests.
func NewInstrumentedDatastoreWithRegistry(next Datastore, backend string, log *zap.Logger, reg prometheus.Registerer) Datastore {
	return &InstrumentedDatastore{next: next, backend: backend, log: log, metrics: newMetrics(reg)}
}

func (d *InstrumentedDatastore) observe(op string, start time.Time, err error) {
	elapsed := time.Since(start)
	d.metrics.duration.WithLabelValues(op, d.backend).Observe(elapsed.Seconds())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return
		}
		d.metrics.errors.WithLabelValues(op, d.backend).Inc()
		var repair *RepairRequiredError
		if errors.As(err, &repair) {
			kind := repair.Step.ResourceKind
			projection := repair.Step.Projection
			operation := repair.Step.Operation
			d.metrics.projectionFailures.WithLabelValues(operation, d.backend, kind, projection).Inc()
			d.metrics.compensationAttempts.WithLabelValues(operation, d.backend, kind, projection).Inc()
			if repair.Compensation != nil {
				d.metrics.compensationFailures.WithLabelValues(operation, d.backend, kind, projection).Inc()
			}
		}
		d.log.Error("datastore operation failed",
			zap.String("operation", op),
			zap.String("backend", d.backend),
			zap.Error(err),
			zap.Int64("duration_ms", elapsed.Milliseconds()),
		)
	}
}

func (d *InstrumentedDatastore) observeFinding(finding ProjectionFinding) {
	d.metrics.findings.WithLabelValues(
		finding.Operation,
		d.backend,
		finding.ResourceKind,
		finding.Projection,
		string(finding.Type),
	).Inc()
	d.log.Warn("datastore projection inconsistency",
		zap.String("operation", finding.Operation),
		zap.String("backend", d.backend),
		zap.String("resource_kind", finding.ResourceKind),
		zap.String("resource_uid", finding.ResourceUID),
		zap.String("projection", finding.Projection),
		zap.String("lookup_key", finding.LookupKey),
		zap.String("finding_type", string(finding.Type)),
	)
}

func (d *InstrumentedDatastore) withFindingObserver(ctx context.Context) context.Context {
	return WithProjectionFindingObserver(ctx, d.observeFinding)
}

// ── Product ────────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateProduct(ctx context.Context, p *Product) error {
	start := time.Now()
	err := d.next.CreateProduct(d.withFindingObserver(ctx), p)
	d.observe("CreateProduct", start, err)
	return err
}

func (d *InstrumentedDatastore) GetProduct(ctx context.Context, id string) (*Product, error) {
	start := time.Now()
	v, err := d.next.GetProduct(d.withFindingObserver(ctx), id)
	d.observe("GetProduct", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetProductByName(ctx context.Context, namespace, name string) (*Product, error) {
	start := time.Now()
	v, err := d.next.GetProductByName(d.withFindingObserver(ctx), namespace, name)
	d.observe("GetProductByName", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListProducts(ctx context.Context, namespace string, params PageParams) (*PageResult[Product], error) {
	start := time.Now()
	v, err := d.next.ListProducts(d.withFindingObserver(ctx), namespace, params)
	d.observe("ListProducts", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateProduct(ctx context.Context, p *Product) error {
	start := time.Now()
	err := d.next.UpdateProduct(d.withFindingObserver(ctx), p)
	d.observe("UpdateProduct", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteProduct(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteProduct(d.withFindingObserver(ctx), id)
	d.observe("DeleteProduct", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteProductWithResourceVersion(ctx context.Context, id, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.DeleteProductWithResourceVersion(d.withFindingObserver(ctx), id, expectedResourceVersion)
	d.observe("DeleteProductWithResourceVersion", start, err)
	return err
}

func (d *InstrumentedDatastore) CreateFile(ctx context.Context, f *File) error {
	start := time.Now()
	err := d.next.CreateFile(d.withFindingObserver(ctx), f)
	d.observe("CreateFile", start, err)
	return err
}
func (d *InstrumentedDatastore) GetFile(ctx context.Context, id string) (*File, error) {
	start := time.Now()
	v, err := d.next.GetFile(d.withFindingObserver(ctx), id)
	d.observe("GetFile", start, err)
	return v, err
}
func (d *InstrumentedDatastore) GetFileByName(ctx context.Context, namespace, name string) (*File, error) {
	start := time.Now()
	v, err := d.next.GetFileByName(d.withFindingObserver(ctx), namespace, name)
	d.observe("GetFileByName", start, err)
	return v, err
}
func (d *InstrumentedDatastore) ListFiles(ctx context.Context, namespace string, p PageParams) (*PageResult[File], error) {
	start := time.Now()
	v, err := d.next.ListFiles(d.withFindingObserver(ctx), namespace, p)
	d.observe("ListFiles", start, err)
	return v, err
}
func (d *InstrumentedDatastore) UpdateFile(ctx context.Context, f *File, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.UpdateFile(d.withFindingObserver(ctx), f, expectedResourceVersion)
	d.observe("UpdateFile", start, err)
	return err
}
func (d *InstrumentedDatastore) DeleteFile(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteFile(d.withFindingObserver(ctx), id)
	d.observe("DeleteFile", start, err)
	return err
}
func (d *InstrumentedDatastore) DeleteFileWithResourceVersion(ctx context.Context, id, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.DeleteFileWithResourceVersion(d.withFindingObserver(ctx), id, expectedResourceVersion)
	d.observe("DeleteFileWithResourceVersion", start, err)
	return err
}
func (d *InstrumentedDatastore) UpdateFileStatus(ctx context.Context, namespace, name string, p FileStatusPatch) (*File, error) {
	start := time.Now()
	v, err := d.next.UpdateFileStatus(d.withFindingObserver(ctx), namespace, name, p)
	d.observe("UpdateFileStatus", start, err)
	return v, err
}

// ── CategoryTaxonomy ───────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error {
	start := time.Now()
	err := d.next.CreateCategoryTaxonomy(d.withFindingObserver(ctx), c)
	d.observe("CreateCategoryTaxonomy", start, err)
	return err
}

func (d *InstrumentedDatastore) GetCategoryTaxonomy(ctx context.Context, uid string) (*CategoryTaxonomy, error) {
	start := time.Now()
	v, err := d.next.GetCategoryTaxonomy(d.withFindingObserver(ctx), uid)
	d.observe("GetCategoryTaxonomy", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*CategoryTaxonomy, error) {
	start := time.Now()
	v, err := d.next.GetCategoryTaxonomyByName(d.withFindingObserver(ctx), namespace, name)
	d.observe("GetCategoryTaxonomyByName", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListCategoryTaxonomies(ctx context.Context, namespace string, params PageParams) (*PageResult[CategoryTaxonomy], error) {
	start := time.Now()
	v, err := d.next.ListCategoryTaxonomies(d.withFindingObserver(ctx), namespace, params)
	d.observe("ListCategoryTaxonomies", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error {
	start := time.Now()
	err := d.next.UpdateCategoryTaxonomy(d.withFindingObserver(ctx), c)
	d.observe("UpdateCategoryTaxonomy", start, err)
	return err
}

func (d *InstrumentedDatastore) UpdateCategoryTaxonomyStatus(ctx context.Context, namespace, name string, patch CategoryTaxonomyStatusPatch) (*CategoryTaxonomy, error) {
	start := time.Now()
	c, err := d.next.UpdateCategoryTaxonomyStatus(d.withFindingObserver(ctx), namespace, name, patch)
	d.observe("UpdateCategoryTaxonomyStatus", start, err)
	return c, err
}

func (d *InstrumentedDatastore) DeleteCategoryTaxonomy(ctx context.Context, uid string) error {
	start := time.Now()
	err := d.next.DeleteCategoryTaxonomy(d.withFindingObserver(ctx), uid)
	d.observe("DeleteCategoryTaxonomy", start, err)
	return err
}

// ── ProductVariant ────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateProductVariant(ctx context.Context, v *ProductVariant) error {
	start := time.Now()
	err := d.next.CreateProductVariant(d.withFindingObserver(ctx), v)
	d.observe("CreateProductVariant", start, err)
	return err
}

func (d *InstrumentedDatastore) GetProductVariant(ctx context.Context, uid string) (*ProductVariant, error) {
	start := time.Now()
	result, err := d.next.GetProductVariant(d.withFindingObserver(ctx), uid)
	d.observe("GetProductVariant", start, err)
	return result, err
}

func (d *InstrumentedDatastore) GetProductVariantByName(ctx context.Context, namespace, name string) (*ProductVariant, error) {
	start := time.Now()
	result, err := d.next.GetProductVariantByName(d.withFindingObserver(ctx), namespace, name)
	d.observe("GetProductVariantByName", start, err)
	return result, err
}

func (d *InstrumentedDatastore) GetProductVariantBySKU(ctx context.Context, namespace, sku string) (*ProductVariant, error) {
	start := time.Now()
	result, err := d.next.GetProductVariantBySKU(d.withFindingObserver(ctx), namespace, sku)
	d.observe("GetProductVariantBySKU", start, err)
	return result, err
}

func (d *InstrumentedDatastore) ListProductVariants(ctx context.Context, namespace string, params PageParams) (*PageResult[ProductVariant], error) {
	start := time.Now()
	result, err := d.next.ListProductVariants(d.withFindingObserver(ctx), namespace, params)
	d.observe("ListProductVariants", start, err)
	return result, err
}

func (d *InstrumentedDatastore) ListProductVariantsByProductRef(ctx context.Context, namespace, productRefName string) ([]*ProductVariant, error) {
	start := time.Now()
	result, err := d.next.ListProductVariantsByProductRef(d.withFindingObserver(ctx), namespace, productRefName)
	d.observe("ListProductVariantsByProductRef", start, err)
	return result, err
}

func (d *InstrumentedDatastore) UpdateProductVariant(ctx context.Context, v *ProductVariant) error {
	start := time.Now()
	err := d.next.UpdateProductVariant(d.withFindingObserver(ctx), v)
	d.observe("UpdateProductVariant", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteProductVariant(ctx context.Context, uid string) error {
	start := time.Now()
	err := d.next.DeleteProductVariant(d.withFindingObserver(ctx), uid)
	d.observe("DeleteProductVariant", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteProductVariantWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.DeleteProductVariantWithResourceVersion(d.withFindingObserver(ctx), uid, expectedResourceVersion)
	d.observe("DeleteProductVariantWithResourceVersion", start, err)
	return err
}

// ── Collection ─────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateCollection(ctx context.Context, c *Collection) error {
	start := time.Now()
	err := d.next.CreateCollection(d.withFindingObserver(ctx), c)
	d.observe("CreateCollection", start, err)
	return err
}

func (d *InstrumentedDatastore) GetCollection(ctx context.Context, uid string) (*Collection, error) {
	start := time.Now()
	v, err := d.next.GetCollection(d.withFindingObserver(ctx), uid)
	d.observe("GetCollection", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetCollectionByName(ctx context.Context, namespace, name string) (*Collection, error) {
	start := time.Now()
	v, err := d.next.GetCollectionByName(d.withFindingObserver(ctx), namespace, name)
	d.observe("GetCollectionByName", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListCollections(ctx context.Context, namespace string, params PageParams) (*PageResult[Collection], error) {
	start := time.Now()
	v, err := d.next.ListCollections(d.withFindingObserver(ctx), namespace, params)
	d.observe("ListCollections", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateCollection(ctx context.Context, c *Collection) error {
	start := time.Now()
	err := d.next.UpdateCollection(d.withFindingObserver(ctx), c)
	d.observe("UpdateCollection", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteCollection(ctx context.Context, uid string) error {
	start := time.Now()
	err := d.next.DeleteCollection(d.withFindingObserver(ctx), uid)
	d.observe("DeleteCollection", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteCollectionWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.DeleteCollectionWithResourceVersion(d.withFindingObserver(ctx), uid, expectedResourceVersion)
	d.observe("DeleteCollectionWithResourceVersion", start, err)
	return err
}

func (d *InstrumentedDatastore) ListProductsByLabelSelector(ctx context.Context, namespace string, selector catalog.LabelSelector) ([]*Product, error) {
	start := time.Now()
	v, err := d.next.ListProductsByLabelSelector(d.withFindingObserver(ctx), namespace, selector)
	d.observe("ListProductsByLabelSelector", start, err)
	return v, err
}

// ── Namespace ─────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateNamespace(ctx context.Context, ns *Namespace) error {
	start := time.Now()
	err := d.next.CreateNamespace(d.withFindingObserver(ctx), ns)
	d.observe("CreateNamespace", start, err)
	return err
}

func (d *InstrumentedDatastore) GetNamespace(ctx context.Context, id string) (*Namespace, error) {
	start := time.Now()
	v, err := d.next.GetNamespace(d.withFindingObserver(ctx), id)
	d.observe("GetNamespace", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetNamespaceByName(ctx context.Context, name string) (*Namespace, error) {
	start := time.Now()
	v, err := d.next.GetNamespaceByName(d.withFindingObserver(ctx), name)
	d.observe("GetNamespaceByName", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListNamespaces(ctx context.Context, params PageParams) (*PageResult[Namespace], error) {
	start := time.Now()
	v, err := d.next.ListNamespaces(d.withFindingObserver(ctx), params)
	d.observe("ListNamespaces", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateNamespace(ctx context.Context, ns *Namespace, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.UpdateNamespace(d.withFindingObserver(ctx), ns, expectedResourceVersion)
	d.observe("UpdateNamespace", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteNamespace(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteNamespace(d.withFindingObserver(ctx), id)
	d.observe("DeleteNamespace", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteNamespaceWithResourceVersion(ctx context.Context, id, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.DeleteNamespaceWithResourceVersion(d.withFindingObserver(ctx), id, expectedResourceVersion)
	d.observe("DeleteNamespaceWithResourceVersion", start, err)
	return err
}

func (d *InstrumentedDatastore) HasRepositories(ctx context.Context, namespaceID string) (bool, error) {
	start := time.Now()
	v, err := d.next.HasRepositories(d.withFindingObserver(ctx), namespaceID)
	d.observe("HasRepositories", start, err)
	return v, err
}

// ── Repository ────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateRepository(ctx context.Context, r *Repository) error {
	start := time.Now()
	err := d.next.CreateRepository(d.withFindingObserver(ctx), r)
	d.observe("CreateRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) GetRepository(ctx context.Context, id string) (*Repository, error) {
	start := time.Now()
	v, err := d.next.GetRepository(d.withFindingObserver(ctx), id)
	d.observe("GetRepository", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListRepositoriesByNamespace(ctx context.Context, namespaceID string, params PageParams) (*PageResult[Repository], error) {
	start := time.Now()
	v, err := d.next.ListRepositoriesByNamespace(d.withFindingObserver(ctx), namespaceID, params)
	d.observe("ListRepositoriesByNamespace", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListRepositories(ctx context.Context, params PageParams) (*PageResult[Repository], error) {
	start := time.Now()
	lister, ok := d.next.(GlobalRepositoryLister)
	if !ok {
		err := fmt.Errorf("%w: backend does not support global repository listing", ErrInvalidArgument)
		d.observe("ListRepositories", start, err)
		return nil, err
	}
	v, err := lister.ListRepositories(d.withFindingObserver(ctx), params)
	d.observe("ListRepositories", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateRepository(ctx context.Context, r *Repository, expectedResourceVersion string) error {
	start := time.Now()
	err := d.next.UpdateRepository(d.withFindingObserver(ctx), r, expectedResourceVersion)
	d.observe("UpdateRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteRepository(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteRepository(d.withFindingObserver(ctx), id)
	d.observe("DeleteRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) HasCatalogResources(ctx context.Context, repoID string) (bool, error) {
	start := time.Now()
	v, err := d.next.HasCatalogResources(d.withFindingObserver(ctx), repoID)
	d.observe("HasCatalogResources", start, err)
	return v, err
}

// ── NamespaceMapping ──────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateNamespaceMapping(ctx context.Context, m *NamespaceMapping) error {
	start := time.Now()
	err := d.next.CreateNamespaceMapping(d.withFindingObserver(ctx), m)
	d.observe("CreateNamespaceMapping", start, err)
	return err
}

func (d *InstrumentedDatastore) LookupRepository(ctx context.Context, namespaceID, name string) (*NamespaceMapping, error) {
	start := time.Now()
	v, err := d.next.LookupRepository(d.withFindingObserver(ctx), namespaceID, name)
	d.observe("LookupRepository", start, err)
	return v, err
}

func (d *InstrumentedDatastore) LookupNamespaceByRepoID(ctx context.Context, repoID string) (*NamespaceMapping, error) {
	start := time.Now()
	v, err := d.next.LookupNamespaceByRepoID(d.withFindingObserver(ctx), repoID)
	d.observe("LookupNamespaceByRepoID", start, err)
	return v, err
}

func (d *InstrumentedDatastore) LookupNamespaceByRepositoryID(ctx context.Context, repositoryID string) (*NamespaceMapping, error) {
	start := time.Now()
	lookup, ok := d.next.(RepositoryNamespaceLookup)
	if !ok {
		err := fmt.Errorf("%w: backend does not support canonical repository namespace lookup", ErrInvalidArgument)
		d.observe("LookupNamespaceByRepositoryID", start, err)
		return nil, err
	}
	v, err := lookup.LookupNamespaceByRepositoryID(d.withFindingObserver(ctx), repositoryID)
	d.observe("LookupNamespaceByRepositoryID", start, err)
	return v, err
}

func (d *InstrumentedDatastore) RenameRepository(ctx context.Context, namespaceID, oldName, newName string) error {
	start := time.Now()
	err := d.next.RenameRepository(d.withFindingObserver(ctx), namespaceID, oldName, newName)
	d.observe("RenameRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) TransferRepository(ctx context.Context, repoID, fromNamespaceID, toNamespaceID string) error {
	start := time.Now()
	err := d.next.TransferRepository(d.withFindingObserver(ctx), repoID, fromNamespaceID, toNamespaceID)
	d.observe("TransferRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteNamespaceMapping(ctx context.Context, namespaceID, name string) error {
	start := time.Now()
	err := d.next.DeleteNamespaceMapping(d.withFindingObserver(ctx), namespaceID, name)
	d.observe("DeleteNamespaceMapping", start, err)
	return err
}

// ── Lifecycle ──────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) Close() error {
	return d.next.Close()
}
