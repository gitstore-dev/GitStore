// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepositoryCDCAdditionWaitsForNamespaceProjection(t *testing.T) {
	createdAt := time.Now().UTC()
	repository := &datastore.Repository{
		UID:               "e42a4966-f4b8-4e8a-bb53-6b70a45c67f5",
		Namespace:         "shop",
		CreationTimestamp: createdAt,
		ResourceVersion:   "7",
	}
	current := func(context.Context) (*repositoryCDCVersion, error) {
		return &repositoryCDCVersion{Namespace: "shop", CreationTimestamp: createdAt, ResourceVersion: "7"}, nil
	}

	ready, err := repositoryCDCAdditionVisible(context.Background(), repository, current, func(context.Context) (bool, error) {
		return false, nil
	})
	require.ErrorContains(t, err, "namespace projection is not committed")
	assert.False(t, ready)

	ready, err = repositoryCDCAdditionVisible(context.Background(), repository, current, func(context.Context) (bool, error) {
		return true, nil
	})
	require.NoError(t, err)
	assert.True(t, ready)
}

func TestRepositoryCDCAdditionSkipsSupersededOrRolledBackCreate(t *testing.T) {
	createdAt := time.Now().UTC()
	repository := &datastore.Repository{Namespace: "shop", CreationTimestamp: createdAt, ResourceVersion: "7"}
	projectionRead := false
	projection := func(context.Context) (bool, error) {
		projectionRead = true
		return true, nil
	}

	ready, err := repositoryCDCAdditionVisible(context.Background(), repository, func(context.Context) (*repositoryCDCVersion, error) {
		return nil, nil
	}, projection)
	require.NoError(t, err)
	assert.False(t, ready)
	assert.False(t, projectionRead, "a rolled-back authoritative insert must not wait forever for its projection")

	ready, err = repositoryCDCAdditionVisible(context.Background(), repository, func(context.Context) (*repositoryCDCVersion, error) {
		return &repositoryCDCVersion{Namespace: "shop", CreationTimestamp: createdAt, ResourceVersion: "8"}, nil
	}, projection)
	require.NoError(t, err)
	assert.False(t, ready)
	assert.False(t, projectionRead, "a superseded create event must defer to its later CDC transition")
}

func TestRepositoryCDCAdditionPropagatesVisibilityReadErrors(t *testing.T) {
	repository := &datastore.Repository{Namespace: "shop", CreationTimestamp: time.Now().UTC(), ResourceVersion: "7"}
	readErr := errors.New("scylla unavailable")

	ready, err := repositoryCDCAdditionVisible(context.Background(), repository, func(context.Context) (*repositoryCDCVersion, error) {
		return nil, readErr
	}, func(context.Context) (bool, error) {
		t.Fatal("projection must not be read after an authoritative read failure")
		return false, nil
	})
	require.ErrorIs(t, err, readErr)
	assert.False(t, ready)

	ready, err = repositoryCDCAdditionVisible(context.Background(), repository, func(context.Context) (*repositoryCDCVersion, error) {
		return &repositoryCDCVersion{Namespace: repository.Namespace, CreationTimestamp: repository.CreationTimestamp, ResourceVersion: repository.ResourceVersion}, nil
	}, func(context.Context) (bool, error) {
		return false, readErr
	})
	require.ErrorIs(t, err, readErr)
	assert.False(t, ready)
}
