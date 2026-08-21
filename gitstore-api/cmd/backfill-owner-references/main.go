// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Command backfill-owner-references reconstructs the additive owner-reference
// metadata and its datastore projection from existing catalog specifications.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"go.uber.org/zap"
)

type report struct {
	CategoriesUpdated int    `json:"categoriesUpdated"`
	ProductsUpdated   int    `json:"productsUpdated"`
	ResumeAfter       string `json:"resumeAfter,omitempty"`
	DryRun            bool   `json:"dryRun"`
}

func main() {
	var (
		hosts       string
		keyspace    string
		resumeAfter string
		dryRun      bool
	)
	flag.StringVar(&hosts, "hosts", envOr("GITSTORE_DATASTORE__SCYLLA__HOSTS", "localhost:9042"), "comma-separated Scylla endpoints")
	flag.StringVar(&keyspace, "keyspace", envOr("GITSTORE_DATASTORE__SCYLLA__KEYSPACE", "gitstore"), "Scylla keyspace")
	flag.StringVar(&resumeAfter, "resume-after", "", "opaque namespace cursor from a prior run")
	flag.BoolVar(&dryRun, "dry-run", false, "report mutations without writing them")
	flag.Parse()

	store, err := scylla.New(config.ScyllaConfig{
		Hosts:    splitHosts(hosts),
		Keyspace: keyspace,
		Username: os.Getenv("GITSTORE_DATASTORE__SCYLLA__USERNAME"),
		Password: os.Getenv("GITSTORE_DATASTORE__SCYLLA__PASSWORD"),
	}, zap.NewNop())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open datastore: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	result, err := run(context.Background(), store, resumeAfter, dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "backfill owner references: %v\n", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, store datastore.Datastore, after string, dryRun bool) (report, error) {
	result := report{DryRun: dryRun}
	for {
		namespaces, err := store.ListNamespaces(ctx, datastore.PageParams{First: datastore.DefaultPageSize, After: after})
		if err != nil {
			return result, fmt.Errorf("list namespaces: %w", err)
		}
		for _, namespace := range namespaces.Items {
			if err := backfillNamespace(ctx, store, namespace.Name, dryRun, &result); err != nil {
				return result, err
			}
			after = encodeCursor(namespace.CreationTimestamp, namespace.UID)
			result.ResumeAfter = after
		}
		if !namespaces.HasNext || len(namespaces.Items) == 0 {
			return result, nil
		}
	}
}

func backfillNamespace(ctx context.Context, store datastore.Datastore, namespace string, dryRun bool, result *report) error {
	categoryAfter := ""
	for {
		categories, err := store.ListCategoryTaxonomies(ctx, namespace, datastore.PageParams{First: datastore.DefaultPageSize, After: categoryAfter})
		if err != nil {
			return fmt.Errorf("list category taxonomies in %s: %w", namespace, err)
		}
		for _, category := range categories.Items {
			desired, err := categoryReferences(ctx, store, category)
			if err != nil {
				return err
			}
			if jsonEqual(category.OwnerReferences, desired) {
				continue
			}
			result.CategoriesUpdated++
			if !dryRun {
				category.OwnerReferences = desired
				datastore.AdvanceCategoryTaxonomySystemVersion(category)
				if err := store.UpdateCategoryTaxonomy(ctx, category); err != nil {
					return fmt.Errorf("update category taxonomy %s/%s: %w", namespace, category.Name, err)
				}
			}
		}
		if !categories.HasNext || len(categories.Items) == 0 {
			break
		}
		last := categories.Items[len(categories.Items)-1]
		categoryAfter = encodeCursor(last.CreationTimestamp, last.UID)
	}

	productAfter := ""
	for {
		products, err := store.ListProducts(ctx, namespace, datastore.PageParams{First: datastore.DefaultPageSize, After: productAfter})
		if err != nil {
			return fmt.Errorf("list products in %s: %w", namespace, err)
		}
		for _, product := range products.Items {
			desired, err := productReferences(ctx, store, product)
			if err != nil {
				return err
			}
			if jsonEqual(product.OwnerReferences, desired) {
				continue
			}
			result.ProductsUpdated++
			if !dryRun {
				product.OwnerReferences = desired
				datastore.AdvanceProductSystemVersion(product)
				if err := store.UpdateProduct(ctx, product); err != nil {
					return fmt.Errorf("update product %s/%s: %w", namespace, product.Name, err)
				}
			}
		}
		if !products.HasNext || len(products.Items) == 0 {
			return nil
		}
		last := products.Items[len(products.Items)-1]
		productAfter = encodeCursor(last.CreationTimestamp, last.UID)
	}
}

func categoryReferences(ctx context.Context, store datastore.Datastore, category *datastore.CategoryTaxonomy) ([]byte, error) {
	references := nonCategoryReferences(category.OwnerReferences)
	if category.ParentName != "" {
		parent, err := store.GetCategoryTaxonomyByName(ctx, category.Namespace, category.ParentName)
		if err == nil {
			references = append(references, catalog.OwnerReference{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: parent.Name,
				UID: parent.UID, BlockOwnerDeletion: true, RepositoryID: parent.RepositoryID,
			})
		} else if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return nil, fmt.Errorf("resolve category parent %s/%s: %w", category.Namespace, category.ParentName, err)
		}
	}
	return json.Marshal(references)
}

func productReferences(ctx context.Context, store datastore.Datastore, product *datastore.Product) ([]byte, error) {
	references := nonCategoryReferences(product.OwnerReferences)
	var spec struct {
		CategoryRef *struct {
			Name string `json:"name"`
		} `json:"categoryRef"`
	}
	if len(product.Spec) == 0 || json.Unmarshal(product.Spec, &spec) != nil || spec.CategoryRef == nil || spec.CategoryRef.Name == "" {
		return json.Marshal(references)
	}
	category, err := store.GetCategoryTaxonomyByName(ctx, product.Namespace, spec.CategoryRef.Name)
	if err == nil {
		references = append(references, catalog.OwnerReference{
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Name: category.Name,
			UID: category.UID, BlockOwnerDeletion: false, RepositoryID: category.RepositoryID,
		})
	} else if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, fmt.Errorf("resolve product category %s/%s: %w", product.Namespace, spec.CategoryRef.Name, err)
	}
	return json.Marshal(references)
}

func nonCategoryReferences(raw []byte) []catalog.OwnerReference {
	var references []catalog.OwnerReference
	_ = json.Unmarshal(raw, &references)
	filtered := make([]catalog.OwnerReference, 0, len(references))
	for _, reference := range references {
		if reference.Kind != "CategoryTaxonomy" {
			filtered = append(filtered, reference)
		}
	}
	return filtered
}

func jsonEqual(left, right []byte) bool {
	if len(strings.TrimSpace(string(left))) == 0 && string(right) == "[]" {
		return true
	}
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func encodeCursor(createdAt time.Time, uid string) string {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("keyset|%s|%s", createdAt.Format(time.RFC3339Nano), uid)))
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func splitHosts(value string) []string {
	var hosts []string
	for _, host := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(host); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	return hosts
}
