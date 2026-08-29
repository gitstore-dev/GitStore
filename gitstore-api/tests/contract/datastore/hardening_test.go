// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build scylla

package datastore_contract_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestScyllaRepositoryCreateAndNamespaceTerminationHaveOneWinner(t *testing.T) {
	requireScyllaHardeningEnvironment(t)
	createReplica, deleteReplica := newScyllaDatastores(t)

	for trial := 0; trial < concurrentReservationTrials; trial++ {
		namespace := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, createReplica.CreateNamespace(context.Background(), namespace))
		current, err := deleteReplica.GetNamespace(context.Background(), namespace.UID)
		require.NoError(t, err)
		expectedResourceVersion := current.ResourceVersion
		deletedAt := time.Now().UTC().Truncate(time.Millisecond)
		current.DeletionTimestamp = &deletedAt
		datastore.AdvanceNamespaceSystemVersion(current)
		repository := newRepository(namespace.Name)

		start := make(chan struct{})
		createResult := make(chan error, 1)
		deleteResult := make(chan error, 1)
		go func() {
			<-start
			createResult <- createReplica.CreateRepositoryInActiveNamespace(context.Background(), repository)
		}()
		go func() {
			<-start
			deleteResult <- deleteReplica.MarkNamespaceDeletion(context.Background(), current, expectedResourceVersion)
		}()
		close(start)
		createErr := <-createResult
		deleteErr := <-deleteResult

		assert.False(t, createErr == nil && deleteErr == nil, "trial %d allowed both repository create and termination", trial)
		switch {
		case createErr == nil:
			assert.True(t,
				errors.Is(deleteErr, datastore.ErrNamespaceNotEmpty) || errors.Is(deleteErr, datastore.ErrConflict),
				"trial %d delete error = %v", trial, deleteErr,
			)
			persisted, getErr := deleteReplica.GetNamespace(context.Background(), namespace.UID)
			require.NoError(t, getErr)
			assert.Nil(t, persisted.DeletionTimestamp)
		case deleteErr == nil:
			assert.True(t,
				errors.Is(createErr, datastore.ErrNamespaceNotActive) || errors.Is(createErr, datastore.ErrConflict),
				"trial %d create error = %v", trial, createErr,
			)
			persisted, getErr := createReplica.GetNamespace(context.Background(), namespace.UID)
			require.NoError(t, getErr)
			assert.NotNil(t, persisted.DeletionTimestamp)
		default:
			t.Fatalf("trial %d had no winner: create=%v delete=%v", trial, createErr, deleteErr)
		}
	}
}

func TestScyllaRepositoryTransferAndTargetTerminationHaveOneWinner(t *testing.T) {
	requireScyllaHardeningEnvironment(t)
	transferReplica, deleteReplica := newScyllaDatastores(t)

	for trial := 0; trial < concurrentReservationTrials; trial++ {
		source := newNamespace(datastore.NamespaceTierUser)
		target := newNamespace(datastore.NamespaceTierUser)
		require.NoError(t, transferReplica.CreateNamespace(context.Background(), source))
		require.NoError(t, transferReplica.CreateNamespace(context.Background(), target))
		repository := newRepository(source.Name)
		require.NoError(t, transferReplica.CreateRepositoryInActiveNamespace(context.Background(), repository))

		currentTarget, err := deleteReplica.GetNamespace(context.Background(), target.UID)
		require.NoError(t, err)
		expectedResourceVersion := currentTarget.ResourceVersion
		deletedAt := time.Now().UTC().Truncate(time.Millisecond)
		currentTarget.DeletionTimestamp = &deletedAt
		datastore.AdvanceNamespaceSystemVersion(currentTarget)

		start := make(chan struct{})
		transferResult := make(chan error, 1)
		deleteResult := make(chan error, 1)
		go func() {
			<-start
			transferResult <- transferReplica.TransferRepository(context.Background(), repository.UID, source.Name, target.Name)
		}()
		go func() {
			<-start
			deleteResult <- deleteReplica.MarkNamespaceDeletion(context.Background(), currentTarget, expectedResourceVersion)
		}()
		close(start)
		transferErr := <-transferResult
		deleteErr := <-deleteResult

		assert.False(t, transferErr == nil && deleteErr == nil, "trial %d allowed both transfer and termination", trial)
		switch {
		case transferErr == nil:
			assert.True(t,
				errors.Is(deleteErr, datastore.ErrNamespaceNotEmpty) || errors.Is(deleteErr, datastore.ErrConflict),
				"trial %d delete error = %v", trial, deleteErr,
			)
			persistedRepository, getErr := deleteReplica.GetRepository(context.Background(), repository.UID)
			require.NoError(t, getErr)
			assert.Equal(t, target.Name, persistedRepository.Namespace)
			persistedTarget, getErr := deleteReplica.GetNamespace(context.Background(), target.UID)
			require.NoError(t, getErr)
			assert.Nil(t, persistedTarget.DeletionTimestamp)
		case deleteErr == nil:
			assert.True(t,
				errors.Is(transferErr, datastore.ErrNamespaceNotActive) || errors.Is(transferErr, datastore.ErrConflict),
				"trial %d transfer error = %v", trial, transferErr,
			)
			persistedRepository, getErr := transferReplica.GetRepository(context.Background(), repository.UID)
			require.NoError(t, getErr)
			assert.Equal(t, source.Name, persistedRepository.Namespace)
			persistedTarget, getErr := transferReplica.GetNamespace(context.Background(), target.UID)
			require.NoError(t, getErr)
			assert.NotNil(t, persistedTarget.DeletionTimestamp)
		default:
			t.Fatalf("trial %d had no winner: transfer=%v delete=%v", trial, transferErr, deleteErr)
		}
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
