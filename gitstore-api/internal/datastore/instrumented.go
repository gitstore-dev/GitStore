// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// InstrumentedDatastore wraps any Datastore with per-operation Prometheus
// metrics (latency histogram, error counter) and structured zap error logs.
type InstrumentedDatastore struct {
	next    Datastore
	backend string
	log     *zap.Logger
	dur     *prometheus.HistogramVec
	errs    *prometheus.CounterVec
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
	dur, errs := newMetrics(reg)
	return &InstrumentedDatastore{next: next, backend: backend, log: log, dur: dur, errs: errs}
}

func (d *InstrumentedDatastore) observe(op string, start time.Time, err error) {
	elapsed := time.Since(start)
	d.dur.WithLabelValues(op, d.backend).Observe(elapsed.Seconds())
	if err != nil {
		d.errs.WithLabelValues(op, d.backend).Inc()
		d.log.Error("datastore operation failed",
			zap.String("operation", op),
			zap.String("backend", d.backend),
			zap.Error(err),
			zap.Int64("duration_ms", elapsed.Milliseconds()),
		)
	}
}

// ── Product ────────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateProduct(ctx context.Context, p *Product) error {
	start := time.Now()
	err := d.next.CreateProduct(ctx, p)
	d.observe("CreateProduct", start, err)
	return err
}

func (d *InstrumentedDatastore) GetProduct(ctx context.Context, id string) (*Product, error) {
	start := time.Now()
	v, err := d.next.GetProduct(ctx, id)
	d.observe("GetProduct", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetProductByName(ctx context.Context, namespace, name string) (*Product, error) {
	start := time.Now()
	v, err := d.next.GetProductByName(ctx, namespace, name)
	d.observe("GetProductByName", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListProducts(ctx context.Context, namespace string, params PageParams) (*PageResult[Product], error) {
	start := time.Now()
	v, err := d.next.ListProducts(ctx, namespace, params)
	d.observe("ListProducts", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateProduct(ctx context.Context, p *Product) error {
	start := time.Now()
	err := d.next.UpdateProduct(ctx, p)
	d.observe("UpdateProduct", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteProduct(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteProduct(ctx, id)
	d.observe("DeleteProduct", start, err)
	return err
}

// ── CategoryTaxonomy ───────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error {
	start := time.Now()
	err := d.next.CreateCategoryTaxonomy(ctx, c)
	d.observe("CreateCategoryTaxonomy", start, err)
	return err
}

func (d *InstrumentedDatastore) GetCategoryTaxonomy(ctx context.Context, uid string) (*CategoryTaxonomy, error) {
	start := time.Now()
	v, err := d.next.GetCategoryTaxonomy(ctx, uid)
	d.observe("GetCategoryTaxonomy", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetCategoryTaxonomyByName(ctx context.Context, namespace, name string) (*CategoryTaxonomy, error) {
	start := time.Now()
	v, err := d.next.GetCategoryTaxonomyByName(ctx, namespace, name)
	d.observe("GetCategoryTaxonomyByName", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListCategoryTaxonomies(ctx context.Context, namespace string, params PageParams) (*PageResult[CategoryTaxonomy], error) {
	start := time.Now()
	v, err := d.next.ListCategoryTaxonomies(ctx, namespace, params)
	d.observe("ListCategoryTaxonomies", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateCategoryTaxonomy(ctx context.Context, c *CategoryTaxonomy) error {
	start := time.Now()
	err := d.next.UpdateCategoryTaxonomy(ctx, c)
	d.observe("UpdateCategoryTaxonomy", start, err)
	return err
}

// ── Collection ─────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateCollection(ctx context.Context, c *Collection) error {
	start := time.Now()
	err := d.next.CreateCollection(ctx, c)
	d.observe("CreateCollection", start, err)
	return err
}

func (d *InstrumentedDatastore) GetCollection(ctx context.Context, id string) (*Collection, error) {
	start := time.Now()
	v, err := d.next.GetCollection(ctx, id)
	d.observe("GetCollection", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetCollectionBySlug(ctx context.Context, slug string) (*Collection, error) {
	start := time.Now()
	v, err := d.next.GetCollectionBySlug(ctx, slug)
	d.observe("GetCollectionBySlug", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListCollections(ctx context.Context, params PageParams) (*PageResult[Collection], error) {
	start := time.Now()
	v, err := d.next.ListCollections(ctx, params)
	d.observe("ListCollections", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateCollection(ctx context.Context, c *Collection) error {
	start := time.Now()
	err := d.next.UpdateCollection(ctx, c)
	d.observe("UpdateCollection", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteCollection(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteCollection(ctx, id)
	d.observe("DeleteCollection", start, err)
	return err
}

// ── Namespace ─────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateNamespace(ctx context.Context, ns *Namespace) error {
	start := time.Now()
	err := d.next.CreateNamespace(ctx, ns)
	d.observe("CreateNamespace", start, err)
	return err
}

func (d *InstrumentedDatastore) GetNamespace(ctx context.Context, id string) (*Namespace, error) {
	start := time.Now()
	v, err := d.next.GetNamespace(ctx, id)
	d.observe("GetNamespace", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetNamespaceByIdentifier(ctx context.Context, identifier string) (*Namespace, error) {
	start := time.Now()
	v, err := d.next.GetNamespaceByIdentifier(ctx, identifier)
	d.observe("GetNamespaceByIdentifier", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListNamespaces(ctx context.Context, params PageParams) (*PageResult[Namespace], error) {
	start := time.Now()
	v, err := d.next.ListNamespaces(ctx, params)
	d.observe("ListNamespaces", start, err)
	return v, err
}

func (d *InstrumentedDatastore) DeleteNamespace(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteNamespace(ctx, id)
	d.observe("DeleteNamespace", start, err)
	return err
}

// ── Repository ────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateRepository(ctx context.Context, r *Repository) error {
	start := time.Now()
	err := d.next.CreateRepository(ctx, r)
	d.observe("CreateRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) GetRepository(ctx context.Context, id string) (*Repository, error) {
	start := time.Now()
	v, err := d.next.GetRepository(ctx, id)
	d.observe("GetRepository", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListRepositoriesByNamespace(ctx context.Context, namespaceID string, params PageParams) (*PageResult[Repository], error) {
	start := time.Now()
	v, err := d.next.ListRepositoriesByNamespace(ctx, namespaceID, params)
	d.observe("ListRepositoriesByNamespace", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateRepository(ctx context.Context, r *Repository) error {
	start := time.Now()
	err := d.next.UpdateRepository(ctx, r)
	d.observe("UpdateRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteRepository(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteRepository(ctx, id)
	d.observe("DeleteRepository", start, err)
	return err
}

// ── NamespaceMapping ──────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateNamespaceMapping(ctx context.Context, m *NamespaceMapping) error {
	start := time.Now()
	err := d.next.CreateNamespaceMapping(ctx, m)
	d.observe("CreateNamespaceMapping", start, err)
	return err
}

func (d *InstrumentedDatastore) LookupRepository(ctx context.Context, namespaceID, name string) (*NamespaceMapping, error) {
	start := time.Now()
	v, err := d.next.LookupRepository(ctx, namespaceID, name)
	d.observe("LookupRepository", start, err)
	return v, err
}

func (d *InstrumentedDatastore) LookupNamespaceByRepoID(ctx context.Context, repoID string) (*NamespaceMapping, error) {
	start := time.Now()
	v, err := d.next.LookupNamespaceByRepoID(ctx, repoID)
	d.observe("LookupNamespaceByRepoID", start, err)
	return v, err
}

func (d *InstrumentedDatastore) RenameRepository(ctx context.Context, namespaceID, oldName, newName string) error {
	start := time.Now()
	err := d.next.RenameRepository(ctx, namespaceID, oldName, newName)
	d.observe("RenameRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) TransferRepository(ctx context.Context, repoID, fromNamespaceID, toNamespaceID string) error {
	start := time.Now()
	err := d.next.TransferRepository(ctx, repoID, fromNamespaceID, toNamespaceID)
	d.observe("TransferRepository", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteNamespaceMapping(ctx context.Context, namespaceID, name string) error {
	start := time.Now()
	err := d.next.DeleteNamespaceMapping(ctx, namespaceID, name)
	d.observe("DeleteNamespaceMapping", start, err)
	return err
}

// ── Lifecycle ──────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) Close() error {
	return d.next.Close()
}
