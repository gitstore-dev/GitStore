// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"encoding/binary"
	"fmt"
	"maps"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/hashicorp/go-memdb"
)

// repositoryNamespaceOrderIndex supports bounded first-page lifecycle probes
// without materializing and sorting all repositories in the namespace.
type repositoryNamespaceOrderIndex struct{}

func (repositoryNamespaceOrderIndex) FromObject(raw interface{}) (bool, []byte, error) {
	repository := raw.(*datastore.Repository)
	key := append([]byte(repository.Namespace), 0)
	key = binary.BigEndian.AppendUint64(key, ^(uint64(repository.CreationTimestamp.Unix()) ^ (1 << 63)))
	key = binary.BigEndian.AppendUint32(key, ^uint32(repository.CreationTimestamp.Nanosecond()))
	for _, value := range []byte(repository.UID) {
		key = append(key, ^value)
	}
	return true, key, nil
}

func (repositoryNamespaceOrderIndex) FromArgs(args ...interface{}) ([]byte, error) {
	if len(args) != 1 {
		return nil, fmt.Errorf("repository namespace index requires one namespace")
	}
	namespace, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("repository namespace index requires a string")
	}
	return append([]byte(namespace), 0), nil
}

func (index repositoryNamespaceOrderIndex) PrefixFromArgs(args ...interface{}) ([]byte, error) {
	return index.FromArgs(args...)
}

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
		"category_ancestor_index": {
			Name: "category_ancestor_index",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "ID"},
				},
				"ancestor": {
					Name:   "ancestor",
					Unique: false,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "Namespace"},
						&memdb.StringFieldIndex{Field: "Ancestor"},
					}},
				},
				"descendant": {
					Name:   "descendant",
					Unique: false,
					Indexer: &memdb.CompoundIndex{Indexes: []memdb.Indexer{
						&memdb.StringFieldIndex{Field: "Namespace"},
						&memdb.StringFieldIndex{Field: "Descendant"},
					}},
				},
			},
		},
		"service_account": resourceTableSchema("service_account", map[string]*memdb.IndexSchema{}),
		"service_account_assertion_replay": {
			Name: "service_account_assertion_replay",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {
					Name:    "id",
					Unique:  true,
					Indexer: &memdb.StringFieldIndex{Field: "JTIDigest"},
				},
				"expires_at": {
					Name:    "expires_at",
					Unique:  false,
					Indexer: &memdb.StringFieldIndex{Field: "ExpiresAtIndex"},
				},
			},
		},
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
		"deleted_repository": {
			Name: "deleted_repository",
			Indexes: map[string]*memdb.IndexSchema{
				"id": {Name: "id", Unique: true, Indexer: &memdb.UUIDFieldIndex{Field: "UID"}},
			},
		},
		"repository": {
			Name: "repository",
			Indexes: map[string]*memdb.IndexSchema{
				"namespace_created": {
					Name: "namespace_created", Unique: true, Indexer: repositoryNamespaceOrderIndex{},
				},
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
	maps.Copy(indexes, extra)
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
