// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import "github.com/scylladb/gocqlx/v3/table"

func resourceColumns(includeNamespace, includeRepository bool) []string {
	columns := []string{"api_version", "kind"}
	if includeNamespace {
		columns = append(columns, "namespace")
	}
	columns = append(columns,
		"uid",
		"name",
		"generation",
		"resource_version",
		"revision",
		"creation_timestamp",
		"creation_actor",
		"update_timestamp",
		"update_actor",
		"labels",
		"annotations",
		"owner_references",
		"finalizers",
		"deletion_timestamp",
	)
	if includeRepository {
		columns = append(columns, "repository_id")
	}
	return append(columns,
		"source_path",
		"git_commit_sha",
		"git_ref",
		"spec",
		"body",
		"status",
	)
}

func authoritativeColumns(includeNamespace, includeRepository bool, specific ...string) []string {
	columns := resourceColumns(includeNamespace, includeRepository)
	return append(columns, specific...)
}

var (
	ProductByNamespace = table.New(table.Metadata{
		Name:    "products_by_namespace",
		Columns: authoritativeColumns(true, true),
		PartKey: []string{"namespace"},
		SortKey: []string{"creation_timestamp", "uid"},
	})
	ProductByName = table.New(table.Metadata{
		Name:    "products_by_name",
		Columns: []string{"namespace", "name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"},
		SortKey: []string{"name"},
	})
	ProductByUID = table.New(table.Metadata{
		Name:    "products_by_uid",
		Columns: []string{"uid", "namespace", "creation_timestamp"},
		PartKey: []string{"uid"},
	})

	CategoryTaxonomy = table.New(table.Metadata{
		Name:    "category_taxonomy",
		Columns: authoritativeColumns(true, true, "parent_name", "ancestor_path"),
		PartKey: []string{"namespace"},
		SortKey: []string{"creation_timestamp", "uid"},
	})
	CategoryTaxonomyByName = table.New(table.Metadata{
		Name:    "category_taxonomy_by_name",
		Columns: []string{"namespace", "name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"},
		SortKey: []string{"name"},
	})
	CategoryTaxonomyByUID = table.New(table.Metadata{
		Name:    "category_taxonomy_by_uid",
		Columns: []string{"uid", "namespace", "creation_timestamp"},
		PartKey: []string{"uid"},
	})

	Collection = table.New(table.Metadata{
		Name:    "collection",
		Columns: authoritativeColumns(true, true),
		PartKey: []string{"namespace"},
		SortKey: []string{"creation_timestamp", "uid"},
	})
	CollectionByName = table.New(table.Metadata{
		Name:    "collection_by_name",
		Columns: []string{"namespace", "name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"},
		SortKey: []string{"name"},
	})
	CollectionByUID = table.New(table.Metadata{
		Name:    "collection_by_uid",
		Columns: []string{"uid", "namespace", "creation_timestamp"},
		PartKey: []string{"uid"},
	})
	FileByNamespace = table.New(table.Metadata{
		Name: "files_by_namespace", Columns: authoritativeColumns(true, true),
		PartKey: []string{"namespace"}, SortKey: []string{"creation_timestamp", "uid"},
	})
	FileByName = table.New(table.Metadata{
		Name: "files_by_name", Columns: []string{"namespace", "name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"}, SortKey: []string{"name"},
	})
	FileByUID = table.New(table.Metadata{
		Name: "files_by_uid", Columns: []string{"uid", "namespace", "creation_timestamp"}, PartKey: []string{"uid"},
	})

	NamespaceByUID = table.New(table.Metadata{
		Name:    "namespaces_by_uid",
		Columns: authoritativeColumns(false, false, "title", "tier", "repository_creation_epoch", "pending_repository_creations"),
		PartKey: []string{"uid"},
	})
	NamespaceByName = table.New(table.Metadata{
		Name:    "namespaces_by_name",
		Columns: []string{"name", "uid"},
		PartKey: []string{"name"},
	})
	NamespaceByBucket = table.New(table.Metadata{
		Name:    "namespaces_by_bucket",
		Columns: []string{"bucket", "creation_timestamp", "uid"},
		PartKey: []string{"bucket"},
		SortKey: []string{"creation_timestamp", "uid"},
	})

	RepositoryByUID = table.New(table.Metadata{
		Name: "repositories_by_uid",
		Columns: authoritativeColumns(true, true,
			"default_branch",
			"storage_class",
			"max_pack_size_bytes",
			"max_file_size_bytes",
		),
		PartKey: []string{"uid"},
	})
	RepositoryByNamespace = table.New(table.Metadata{
		Name:    "repositories_by_namespace",
		Columns: []string{"namespace", "bucket", "creation_timestamp", "uid"},
		PartKey: []string{"namespace", "bucket"},
		SortKey: []string{"creation_timestamp", "uid"},
	})
	RepositoryByBucket = table.New(table.Metadata{
		Name:    "repositories_by_bucket",
		Columns: []string{"bucket", "creation_timestamp", "uid"},
		PartKey: []string{"bucket"},
		SortKey: []string{"creation_timestamp", "uid"},
	})

	ProductVariantByNamespace = table.New(table.Metadata{
		Name:    "product_variant_by_namespace",
		Columns: authoritativeColumns(true, true, "sku", "product_ref_name"),
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
	})
	ProductVariantBySKU = table.New(table.Metadata{
		Name:    "product_variant_by_sku",
		Columns: []string{"namespace", "sku", "uid", "creation_timestamp"},
		PartKey: []string{"namespace"},
		SortKey: []string{"sku"},
	})
	ProductVariantByProductRef = table.New(table.Metadata{
		Name:    "product_variant_by_product_ref",
		Columns: []string{"namespace", "product_ref_name", "uid", "creation_timestamp"},
		PartKey: []string{"namespace", "product_ref_name"},
		SortKey: []string{"creation_timestamp", "uid"},
	})

	NamespaceMapping = table.New(table.Metadata{
		Name:    "namespace_mappings",
		Columns: []string{"namespace", "name", "repository_id"},
		PartKey: []string{"namespace"},
		SortKey: []string{"name"},
	})
	NamespaceMappingByRepository = table.New(table.Metadata{
		Name:    "namespace_mappings_by_repository",
		Columns: []string{"repository_id", "namespace", "name"},
		PartKey: []string{"repository_id"},
	})
)
