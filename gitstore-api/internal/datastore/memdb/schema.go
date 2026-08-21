// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import "github.com/hashicorp/go-memdb"

var schema = &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		"product": resourceTableSchema("product", map[string]*memdb.IndexSchema{
			"repository_id": optionalStringIndex("repository_id", "RepositoryID"),
		}),
		"file": resourceTableSchema("file", map[string]*memdb.IndexSchema{
			"repository_id": optionalStringIndex("repository_id", "RepositoryID"),
		}),
		"category_taxonomy": resourceTableSchema("category_taxonomy", map[string]*memdb.IndexSchema{
			"parent_name":   optionalStringIndex("parent_name", "ParentName"),
			"ancestor_path": optionalStringIndex("ancestor_path", "AncestorPath"),
			"repository_id": optionalStringIndex("repository_id", "RepositoryID"),
		}),
		"owner_reference": {
			Name: "owner_reference",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "ID"},
				},
				"owner_block": {
					Name:   "owner_block",
					Unique: false,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "Namespace"},
						&memdb.StringFieldIndex{Field: "RepositoryID"},
						&memdb.StringFieldIndex{Field: "OwnerUID"},
						&memdb.StringFieldIndex{Field: "BlockKey"},
					}},
				},
				"owner_product": {
					Name:   "owner_product",
					Unique: false,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "Namespace"},
						&memdb.StringFieldIndex{Field: "RepositoryID"},
						&memdb.StringFieldIndex{Field: "OwnerUID"},
						&memdb.StringFieldIndex{Field: "DependentKind"},
						&memdb.StringFieldIndex{Field: "BlockKey"},
					}},
				},
				"dependent": {
					Name:   "dependent",
					Unique: false,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "DependentKind"},
						&memdb.StringFieldIndex{Field: "DependentUID"},
					}},
				},
			},
		},
		"product_variant": resourceTableSchema("product_variant", map[string]*memdb.IndexSchema{
			"sku_namespace": {
				Name:   "sku_namespace",
				Unique: true,
				Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
					&memdb.StringFieldIndex{Field: "Namespace"},
					&memdb.StringFieldIndex{Field: "SKU"},
				}},
			},
			"product_ref": {
				Name:   "product_ref",
				Unique: false,
				Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
					&memdb.StringFieldIndex{Field: "Namespace"},
					&memdb.StringFieldIndex{Field: "ProductRefName"},
				}},
			},
			"repository_id": optionalStringIndex("repository_id", "RepositoryID"),
		}),
		"collection": resourceTableSchema("collection", map[string]*memdb.IndexSchema{
			"repository_id": optionalStringIndex("repository_id", "RepositoryID"),
		}),
		"namespaces": {
			Name: "namespaces",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
				},
				"name": {
					Name:    "name",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "Name"},
				},
				"tier": optionalStringIndex("tier", "Tier"),
			},
		},
		"repository": {
			Name: "repository",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
				},
				"namespace": {
					Name:    "namespace",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
				},
				"name_namespace": {
					Name:   "name_namespace",
					Unique: true,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "Namespace"},
						&memdb.StringFieldIndex{Field: "Name"},
					}},
				},
			},
		},
		"namespace_mapping": {
			Name: "namespace_mapping",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:   "id",
					Unique: true,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "Namespace"},
						&memdb.StringFieldIndex{Field: "Name"},
					}},
				},
				"repository_id": {
					Name:    "repository_id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "RepositoryID"},
				},
			},
		},
	},
}

func resourceTableSchema(name string, extra map[string]*memdb.IndexSchema) *memdb.TableSchema {
	indexes := map[string]*memdb.IndexSchema{
		"id": {
			Name:    "id",
			Unique:  true,
			Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
		},
		"name_namespace": {
			Name:   "name_namespace",
			Unique: true,
			Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
				&memdb.StringFieldIndex{Field: "Namespace"},
				&memdb.StringFieldIndex{Field: "Name"},
			}},
		},
		"namespace": {
			Name:    "namespace",
			Unique:  false,
			Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
		},
	}
	for indexName, index := range extra {
		indexes[indexName] = index
	}
	return &memdb.TableSchema{Name: name, Indexes: indexes}
}

func optionalStringIndex(name, field string) *memdb.IndexSchema {
	return &memdb.IndexSchema{
		Name:         name,
		Unique:       false,
		AllowMissing: true,
		Indexer:      &memdb.StringFieldIndex{Field: field},
	}
}
