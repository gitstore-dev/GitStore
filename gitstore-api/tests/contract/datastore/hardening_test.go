// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package datastore_contract_test

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
)

const concurrentReservationTrials = 100

func TestScyllaConcurrentNameReservationHasOneWinner(t *testing.T) {
	requireScyllaHardeningEnvironment(t)
	first, second := newScyllaDatastores(t)

	for trial := 0; trial < concurrentReservationTrials; trial++ {
		namespace := hardeningNamespace(trial)
		name := "shared-name"
		left := hardeningProduct(namespace, name, newID(), trial)
		right := hardeningProduct(namespace, name, newID(), trial)

		assertOneReservationWinner(t,
			func() error { return first.CreateProduct(context.Background(), left) },
			func() error { return second.CreateProduct(context.Background(), right) },
		)
	}
}

func TestScyllaConcurrentUIDReservationHasOneWinner(t *testing.T) {
	requireScyllaHardeningEnvironment(t)
	first, second := newScyllaDatastores(t)

	for trial := 0; trial < concurrentReservationTrials; trial++ {
		namespace := hardeningNamespace(trial + concurrentReservationTrials)
		uid := newID()
		left := hardeningProduct(namespace, "left", uid, trial)
		right := hardeningProduct(namespace, "right", uid, trial)

		assertOneReservationWinner(t,
			func() error { return first.CreateProduct(context.Background(), left) },
			func() error { return second.CreateProduct(context.Background(), right) },
		)
	}
}

func TestScyllaConcurrentSKUReservationHasOneWinner(t *testing.T) {
	requireScyllaHardeningEnvironment(t)
	first, second := newScyllaDatastores(t)

	for trial := 0; trial < concurrentReservationTrials; trial++ {
		namespace := hardeningNamespace(trial + 2*concurrentReservationTrials)
		sku := "shared-sku"
		left := hardeningVariant(namespace, "left", sku, trial)
		right := hardeningVariant(namespace, "right", sku, trial)

		assertOneReservationWinner(t,
			func() error { return first.CreateProductVariant(context.Background(), left) },
			func() error { return second.CreateProductVariant(context.Background(), right) },
		)
	}
}

func requireScyllaHardeningEnvironment(t *testing.T) {
	t.Helper()
	if os.Getenv("GITSTORE_TEST_SCYLLA_ADDR") == "" {
		t.Skip("GITSTORE_TEST_SCYLLA_ADDR is required for live concurrent Scylla trials")
	}
}

func assertOneReservationWinner(t *testing.T, left, right func() error) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	for _, create := range []func() error{left, right} {
		create := create
		go func() {
			defer workers.Done()
			<-start
			results <- create()
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	var succeeded, conflicted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case assert.ErrorIs(t, err, datastore.ErrAlreadyExists):
			conflicted++
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Equal(t, 1, conflicted)
}

func hardeningProduct(namespace, name, uid string, trial int) *datastore.Product {
	return &datastore.Product{
		UID:               uid,
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "Product",
		ResourceVersion:   "1",
		Generation:        1,
		CreationTimestamp: hardeningTimestamp(0, trial),
	}
}

func hardeningVariant(namespace, name, sku string, trial int) *datastore.ProductVariant {
	return &datastore.ProductVariant{
		UID:               newID(),
		Namespace:         namespace,
		Name:              name,
		APIVersion:        "catalog.gitstore.dev/v1beta1",
		Kind:              "ProductVariant",
		ResourceVersion:   "1",
		Generation:        1,
		CreationTimestamp: hardeningTimestamp(0, trial),
		SKU:               sku,
	}
}
