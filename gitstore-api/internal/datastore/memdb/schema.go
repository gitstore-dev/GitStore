// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"github.com/hashicorp/go-memdb"
)

// schema defines all tables and indices for the in-memory datastore.
var schema = &memdb.DBSchema{
	Tables: map[string]*memdb.TableSchema{
		"product": {
			Name: "product",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
				},
				"name_namespace": {
					Name:   "name_namespace",
					Unique: true,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.StringFieldIndex{Field: "Namespace"},
							&memdb.StringFieldIndex{Field: "Name"},
						},
					},
				},
				"namespace": {
					Name:    "namespace",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
				},
			},
		},
		"category_taxonomy": {
			Name: "category_taxonomy",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
				},
				"name_namespace": {
					Name:   "name_namespace",
					Unique: true,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.StringFieldIndex{Field: "Namespace"},
							&memdb.StringFieldIndex{Field: "Name"},
						},
					},
				},
				"namespace": {
					Name:    "namespace",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
				},
				"parent_name": {
					Name:         "parent_name",
					Unique:       false,
					AllowMissing: true,
					Indexer:      &memdb.StringFieldIndex{Field: "ParentName"},
				},
				"ancestor_path": {
					Name:         "ancestor_path",
					Unique:       false,
					AllowMissing: true,
					Indexer:      &memdb.StringFieldIndex{Field: "AncestorPath"},
				},
			},
		},
		"product_variant": {
			Name: "product_variant",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
				},
				"name_namespace": {
					Name:   "name_namespace",
					Unique: true,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.StringFieldIndex{Field: "Namespace"},
							&memdb.StringFieldIndex{Field: "Name"},
						},
					},
				},
				"namespace": {
					Name:    "namespace",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
				},
				"sku_namespace": {
					Name:   "sku_namespace",
					Unique: true,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.StringFieldIndex{Field: "Namespace"},
							&memdb.StringFieldIndex{Field: "SKU"},
						},
					},
				},
				"product_ref": {
					Name:   "product_ref",
					Unique: false,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.StringFieldIndex{Field: "Namespace"},
							&memdb.StringFieldIndex{Field: "ProductRefName"},
						},
					},
				},
			},
		},
		"collection": {
			Name: "collection",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "UID"},
				},
				"name_namespace": {
					Name:   "name_namespace",
					Unique: true,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.StringFieldIndex{Field: "Namespace"},
							&memdb.StringFieldIndex{Field: "Name"},
						},
					},
				},
				"namespace": {
					Name:    "namespace",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "Namespace"},
				},
			},
		},
		"namespaces": {
			Name: "namespaces",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "ID"},
				},
				"identifier": {
					Name:    "identifier",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "Identifier"},
				},
				"tier": {
					Name:    "tier",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "Tier"},
				},
			},
		},
		"repository": {
			Name: "repository",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "ID"},
				},
				"namespace_id": {
					Name:    "namespace_id",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "NamespaceID"},
				},
			},
		},
		"namespace_mapping": {
			Name: "namespace_mapping",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:   "id",
					Unique: true,
					Indexer: &memdb.CompoundIndex{
						Indexes: []memdb.Indexer{
							&memdb.UUIDFieldIndex{Field: "NamespaceID"},
							&memdb.StringFieldIndex{Field: "Name"},
						},
					},
				},
				"repo_id": {
					Name:    "repo_id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "RepoID"},
				},
			},
		},
	},
}
