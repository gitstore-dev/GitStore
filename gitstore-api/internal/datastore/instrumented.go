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

func (d *InstrumentedDatastore) GetProductBySKU(ctx context.Context, sku string) (*Product, error) {
	start := time.Now()
	v, err := d.next.GetProductBySKU(ctx, sku)
	d.observe("GetProductBySKU", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListProducts(ctx context.Context, filter ProductFilter) ([]*Product, error) {
	start := time.Now()
	v, err := d.next.ListProducts(ctx, filter)
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

// ── Category ───────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) CreateCategory(ctx context.Context, c *Category) error {
	start := time.Now()
	err := d.next.CreateCategory(ctx, c)
	d.observe("CreateCategory", start, err)
	return err
}

func (d *InstrumentedDatastore) GetCategory(ctx context.Context, id string) (*Category, error) {
	start := time.Now()
	v, err := d.next.GetCategory(ctx, id)
	d.observe("GetCategory", start, err)
	return v, err
}

func (d *InstrumentedDatastore) GetCategoryBySlug(ctx context.Context, slug string) (*Category, error) {
	start := time.Now()
	v, err := d.next.GetCategoryBySlug(ctx, slug)
	d.observe("GetCategoryBySlug", start, err)
	return v, err
}

func (d *InstrumentedDatastore) ListCategories(ctx context.Context) ([]*Category, error) {
	start := time.Now()
	v, err := d.next.ListCategories(ctx)
	d.observe("ListCategories", start, err)
	return v, err
}

func (d *InstrumentedDatastore) UpdateCategory(ctx context.Context, c *Category) error {
	start := time.Now()
	err := d.next.UpdateCategory(ctx, c)
	d.observe("UpdateCategory", start, err)
	return err
}

func (d *InstrumentedDatastore) DeleteCategory(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteCategory(ctx, id)
	d.observe("DeleteCategory", start, err)
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

func (d *InstrumentedDatastore) ListCollections(ctx context.Context) ([]*Collection, error) {
	start := time.Now()
	v, err := d.next.ListCollections(ctx)
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

func (d *InstrumentedDatastore) ListNamespaces(ctx context.Context) ([]*Namespace, error) {
	start := time.Now()
	v, err := d.next.ListNamespaces(ctx)
	d.observe("ListNamespaces", start, err)
	return v, err
}

func (d *InstrumentedDatastore) DeleteNamespace(ctx context.Context, id string) error {
	start := time.Now()
	err := d.next.DeleteNamespace(ctx, id)
	d.observe("DeleteNamespace", start, err)
	return err
}

// ── Lifecycle ──────────────────────────────────────────────────────────────

func (d *InstrumentedDatastore) Close() error {
	return d.next.Close()
}
