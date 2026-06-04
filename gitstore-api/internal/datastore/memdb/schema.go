// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	memdb "github.com/hashicorp/go-memdb"
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
		"category": {
			Name: "category",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "ID"},
				},
				"slug": {
					Name:    "slug",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "Slug", Lowercase: true},
				},
			},
		},
		"collection": {
			Name: "collection",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.UUIDFieldIndex{Field: "ID"},
				},
				"slug": {
					Name:    "slug",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "Slug", Lowercase: true},
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
