// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import "github.com/scylladb/gocqlx/v3/table"

// BucketAll is the sentinel partition key for global listing tables.
const BucketAll = "all"

// Table models
var (
	// ProductByNamespace is the primary paginated read table (newest-first per namespace).
	ProductByNamespace = table.New(table.Metadata{
		Name: "products_by_namespace",
		Columns: []string{
			"namespace",
			"creation_timestamp",
			"uid",
			"name",
			"api_version",
			"kind",
			"generation",
			"resource_version",
			"revision",
			"labels",
			"annotations",
			"owner_refs",
			"repository_id",
			"source_path",
			"git_commit_sha",
			"git_ref",
			"spec",
			"body",
			"status",
		},
		PartKey: []string{
			"namespace",
		},
		SortKey: []string{
			"creation_timestamp",
			"uid",
		},
	})

	// ProductByName is the lookup table for GetProductByName(namespace, name).
	ProductByName = table.New(table.Metadata{
		Name: "products_by_name",
		Columns: []string{
			"namespace",
			"name",
			"uid",
			"creation_timestamp",
		},
		PartKey: []string{
			"namespace",
		},
		SortKey: []string{
			"name",
		},
	})

	// ProductByUID is the lookup table for GetProduct(uid).
	ProductByUID = table.New(table.Metadata{
		Name: "products_by_uid",
		Columns: []string{
			"uid",
			"namespace",
			"creation_timestamp",
		},
		PartKey: []string{
			"uid",
		},
		SortKey: []string{},
	})

	CategoryTaxonomy = table.New(table.Metadata{
		Name: "category_taxonomy",
		Columns: []string{
			"namespace",
			"creation_timestamp",
			"uid",
			"name",
			"api_version",
			"kind",
			"generation",
			"resource_version",
			"revision",
			"labels",
			"annotations",
			"parent_name",
			"ancestor_path",
			"repository_id",
			"source_path",
			"git_commit_sha",
			"git_ref",
			"spec",
			"body",
			"status",
		},
		PartKey: []string{
			"namespace",
		},
		SortKey: []string{
			"creation_timestamp",
			"uid",
		},
	})

	// CategoryTaxonomyByName is the lookup table for GetCategoryTaxonomyByName(namespace, name).
	CategoryTaxonomyByName = table.New(table.Metadata{
		Name: "category_taxonomy_by_name",
		Columns: []string{
			"namespace",
			"name",
			"uid",
			"creation_timestamp",
		},
		PartKey: []string{
			"namespace",
		},
		SortKey: []string{
			"name",
		},
	})

	// CategoryTaxonomyByUID is the lookup table for GetCategoryTaxonomy(uid).
	CategoryTaxonomyByUID = table.New(table.Metadata{
		Name: "category_taxonomy_by_uid",
		Columns: []string{
			"uid",
			"namespace",
			"creation_timestamp",
		},
		PartKey: []string{
			"uid",
		},
		SortKey: []string{},
	})

	// Collection is the primary paginated read table (newest-first per namespace).
	Collection = table.New(table.Metadata{
		Name: "collection",
		Columns: []string{
			"namespace",
			"creation_timestamp",
			"uid",
			"name",
			"api_version",
			"kind",
			"generation",
			"resource_version",
			"revision",
			"labels",
			"annotations",
			"repository_id",
			"source_path",
			"git_commit_sha",
			"git_ref",
			"spec",
			"body",
			"status",
		},
		PartKey: []string{
			"namespace",
		},
		SortKey: []string{
			"creation_timestamp",
			"uid",
		},
	})

	// CollectionByName is the lookup table for GetCollectionByName(namespace, name).
	CollectionByName = table.New(table.Metadata{
		Name: "collection_by_name",
		Columns: []string{
			"namespace",
			"name",
			"uid",
			"creation_timestamp",
		},
		PartKey: []string{
			"namespace",
		},
		SortKey: []string{
			"name",
		},
	})

	// CollectionByUID is the lookup table for GetCollection(uid).
	CollectionByUID = table.New(table.Metadata{
		Name: "collection_by_uid",
		Columns: []string{
			"uid",
			"namespace",
			"creation_timestamp",
		},
		PartKey: []string{
			"uid",
		},
		SortKey: []string{},
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

	ProductVariantByNamespace = table.New(table.Metadata{
		Name: "product_variant_by_namespace",
		Columns: []string{
			"namespace",
			"creation_timestamp",
			"uid",
			"name",
			"api_version",
			"kind",
			"generation",
			"resource_version",
			"revision",
			"labels",
			"annotations",
			"owner_refs",
			"sku",
			"product_ref_name",
			"repository_id",
			"source_path",
			"git_commit_sha",
			"git_ref",
			"spec",
			"body",
			"status",
		},
		PartKey: []string{"namespace"},
		SortKey: []string{"creation_timestamp", "uid"},
	})

	ProductVariantByName = table.New(table.Metadata{
		Name:    "product_variant_by_name",
		Columns: []string{"namespace", "name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"},
		SortKey: []string{"name"},
	})

	ProductVariantByUID = table.New(table.Metadata{
		Name:    "product_variant_by_uid",
		Columns: []string{"uid", "namespace", "creation_timestamp"},
		PartKey: []string{"uid"},
		SortKey: []string{},
	})

	ProductVariantBySKU = table.New(table.Metadata{
		Name:    "product_variant_by_sku",
		Columns: []string{"namespace", "sku", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"},
		SortKey: []string{"sku"},
	})

	// ProductVariantByProductRef is the lookup table for ListProductVariantsByProductRef(namespace, productRefName).
	ProductVariantByProductRef = table.New(table.Metadata{
		Name:    "product_variant_by_product_ref",
		Columns: []string{"namespace", "product_ref_name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace", "product_ref_name"},
		SortKey: []string{"creation_timestamp", "uid"},
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
