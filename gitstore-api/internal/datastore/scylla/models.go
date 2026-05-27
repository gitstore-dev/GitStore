// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import "github.com/scylladb/gocqlx/v3/table"

// Table models
var (
	Product = table.New(table.Metadata{
		Name: "products",
		Columns: []string{
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
			"created_at",
			"updated_at",
			"body",
		},
		PartKey: []string{
			"id",
		},
	})

	Category = table.New(table.Metadata{
		Name: "categories",
		Columns: []string{
			"id",
			"name",
			"slug",
			"parent_id",
			"display_order",
			"created_at",
			"updated_at",
			"body",
		},
		PartKey: []string{
			"id",
		},
	})

	Collection = table.New(table.Metadata{
		Name: "collections",
		Columns: []string{
			"id",
			"name",
			"slug",
			"display_order",
			"product_ids",
			"created_at",
			"updated_at",
			"body",
		},
		PartKey: []string{
			"id",
		},
	})

	Namespace = table.New(table.Metadata{
		Name: "namespaces",
		Columns: []string{
			"id",
			"identifier",
			"display_name",
			"tier",
			"parent_enterprise_id",
			"created_at",
			"created_by",
			"updated_at",
			"updated_by",
		},
		PartKey: []string{
			"id",
		},
	})

	Repository = table.New(table.Metadata{
		Name: "repositories",
		Columns: []string{
			"id",
			"namespace_id",
			"name",
			"default_branch",
			"storage_class",
			"created_at",
			"created_by",
			"updated_at",
			"updated_by",
		},
		PartKey: []string{
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
