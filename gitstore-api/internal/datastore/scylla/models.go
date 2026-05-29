// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import "github.com/scylladb/gocqlx/v3/table"

// BucketAll is the sentinel partition key for global listing tables.
const BucketAll = "all"

// Table models
var (
	Product = table.New(table.Metadata{
		Name: "products",
		Columns: []string{
			"bucket",
			"created_at",
			"id",
			"sku",
			"title",
			"price",
			"currency",
			"inventory_status",
			"inventory_quantity",
			"category_id",
			"collection_ids",
			"images",
			"metadata",
			"updated_at",
			"body",
		},
		PartKey: []string{
			"bucket",
		},
		SortKey: []string{
			"created_at",
			"id",
		},
	})

	Category = table.New(table.Metadata{
		Name: "categories",
		Columns: []string{
			"bucket",
			"created_at",
			"id",
			"name",
			"slug",
			"parent_id",
			"display_order",
			"updated_at",
			"body",
		},
		PartKey: []string{
			"bucket",
		},
		SortKey: []string{
			"created_at",
			"id",
		},
	})

	Collection = table.New(table.Metadata{
		Name: "collections",
		Columns: []string{
			"bucket",
			"created_at",
			"id",
			"name",
			"slug",
			"display_order",
			"product_ids",
			"updated_at",
			"body",
		},
		PartKey: []string{
			"bucket",
		},
		SortKey: []string{
			"created_at",
			"id",
		},
	})

	Namespace = table.New(table.Metadata{
		Name: "namespaces",
		Columns: []string{
			"bucket",
			"created_at",
			"id",
			"identifier",
			"display_name",
			"tier",
			"parent_enterprise_id",
			"created_by",
			"updated_at",
			"updated_by",
		},
		PartKey: []string{
			"bucket",
		},
		SortKey: []string{
			"created_at",
			"id",
		},
	})

	Repository = table.New(table.Metadata{
		Name: "repositories",
		Columns: []string{
			"bucket",
			"created_at",
			"id",
			"namespace_id",
			"name",
			"default_branch",
			"storage_class",
			"created_by",
			"updated_at",
			"updated_by",
		},
		PartKey: []string{
			"bucket",
		},
		SortKey: []string{
			"created_at",
			"id",
		},
	})

	NamespaceMapping = table.New(table.Metadata{
		Name: "namespace_mappings",
		Columns: []string{
			"namespace_id",
			"name",
			"repo_id",
		},
		PartKey: []string{
			"namespace_id",
		},
		SortKey: []string{
			"name",
		},
	})
)
