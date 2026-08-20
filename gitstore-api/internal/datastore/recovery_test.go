// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepairRequiredErrorRetainsPrimaryAndCompensationFailures(t *testing.T) {
	primary := errors.New("write projection")
	compensation := errors.New("release reservation")
	step := datastore.MutationStep{
		Operation:    "create",
		ResourceKind: "Product",
		Projection:   "products_by_name",
		Action:       "reserve",
	}

	err := datastore.NewRepairRequiredError(step, primary, compensation)

	require.Error(t, err)
	assert.ErrorIs(t, err, datastore.ErrRepairRequired)
	assert.ErrorIs(t, err, primary)
	var repair *datastore.RepairRequiredError
	require.ErrorAs(t, err, &repair)
	assert.Equal(t, step, repair.Step)
	assert.ErrorIs(t, repair.Compensation, compensation)
}

func TestProjectionFindingFields(t *testing.T) {
	finding := datastore.ProjectionFinding{
		ResourceKind: "Repository",
		ResourceUID:  "repo-1",
		Projection:   "namespace_mappings",
		LookupKey:    "tenant/catalog",
		Operation:    "lookup",
		Type:         datastore.FindingStale,
	}

	assert.Equal(t, "Repository", finding.ResourceKind)
	assert.Equal(t, datastore.FindingStale, finding.Type)
}

func TestProjectionFindingObserverReceivesFinding(t *testing.T) {
	var observed datastore.ProjectionFinding
	ctx := datastore.WithProjectionFindingObserver(context.Background(), func(finding datastore.ProjectionFinding) {
		observed = finding
	})
	expected := datastore.ProjectionFinding{
		ResourceKind: "ProductVariant",
		ResourceUID:  "variant-1",
		Projection:   "product_variant_by_sku",
		LookupKey:    "shop/sku-1",
		Operation:    "get_by_sku",
		Type:         datastore.FindingDangling,
	}

	datastore.ReportProjectionFinding(ctx, expected)

	assert.Equal(t, expected, observed)
}
