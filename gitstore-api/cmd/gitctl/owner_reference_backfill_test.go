// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
)

type ownerReferenceBackfillTestStore struct {
	datastore.Datastore
	datastore.OwnerReferenceBackfillStore
}

func (ownerReferenceBackfillTestStore) Close() error { return nil }

func TestOwnerReferenceBackfillRequiresExplicitDeployment(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"owner-reference-backfill", "status"}, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("status without deployment selection returned %d, want usage error: %s", code, stderr.String())
	}
	for _, flag := range []string{"--hosts", "--keyspace"} {
		if !strings.Contains(stderr.String(), flag) {
			t.Errorf("missing required deployment selection %s: %s", flag, stderr.String())
		}
	}
}

func TestOwnerReferenceBackfillRunRequiresConfirmation(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{
		"owner-reference-backfill", "run",
		"--hosts", "scylla.test:9042",
		"--keyspace", "gitstore_test",
	}
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 2 {
		t.Fatalf("unconfirmed run returned %d, want usage error: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "owner-reference-backfill run requires --confirm") {
		t.Fatalf("run did not require explicit confirmation: %s", stderr.String())
	}
}

func TestOwnerReferenceBackfillResumesBoundedPagesAndRepairsMetadata(t *testing.T) {
	rawStore, err := memdb.New()
	if err != nil {
		t.Fatal(err)
	}
	store := ownerReferenceBackfillTestStore{
		Datastore:                   rawStore,
		OwnerReferenceBackfillStore: rawStore.(datastore.OwnerReferenceBackfillStore),
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	namespace := &datastore.Namespace{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace",
		UID: "00000000-0000-0000-0000-000000000101", Name: "backfill-test",
		ResourceVersion: "1", CreationTimestamp: now,
	}
	if err := store.CreateNamespace(ctx, namespace); err != nil {
		t.Fatal(err)
	}
	repository := &datastore.Repository{
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
		UID: "00000000-0000-0000-0000-000000000102", Namespace: namespace.Name, Name: "catalog",
		ResourceVersion: "1", CreationTimestamp: now,
	}
	if err := store.CreateRepositoryInActiveNamespace(ctx, repository); err != nil {
		t.Fatal(err)
	}
	parentCategory := &datastore.CategoryTaxonomy{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		UID: "00000000-0000-0000-0000-000000000104", Namespace: namespace.Name, Name: "parent",
		RepositoryID: repository.UID, ResourceVersion: "1", CreationTimestamp: now,
	}
	if err := store.CreateCategoryTaxonomy(ctx, parentCategory); err != nil {
		t.Fatal(err)
	}
	childCategory := &datastore.CategoryTaxonomy{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy",
		UID: "00000000-0000-0000-0000-000000000105", Namespace: namespace.Name, Name: "child",
		ParentName: parentCategory.Name, RepositoryID: repository.UID, ResourceVersion: "1",
		CreationTimestamp: now.Add(time.Second),
	}
	if err := store.CreateCategoryTaxonomy(ctx, childCategory); err != nil {
		t.Fatal(err)
	}
	product := &datastore.Product{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
		UID: "00000000-0000-0000-0000-000000000103", Namespace: namespace.Name, Name: "item",
		RepositoryID: repository.UID, ResourceVersion: "1", CreationTimestamp: now,
		Spec: json.RawMessage(`{"title":"Item","categoryRef":{"name":"parent"}}`),
	}
	if err := store.CreateProduct(ctx, product); err != nil {
		t.Fatal(err)
	}
	variant := &datastore.ProductVariant{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "ProductVariant",
		UID: "00000000-0000-0000-0000-000000000106", Namespace: namespace.Name, Name: "item-variant",
		SKU: "ITEM-1", ProductRefName: product.Name, RepositoryID: repository.UID,
		ResourceVersion: "1", CreationTimestamp: now,
	}
	if err := store.CreateProductVariant(ctx, variant); err != nil {
		t.Fatal(err)
	}
	collection := &datastore.Collection{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Collection",
		UID: "00000000-0000-0000-0000-000000000107", Namespace: namespace.Name, Name: "collection",
		RepositoryID: repository.UID, ResourceVersion: "1", CreationTimestamp: now,
	}
	if err := store.CreateCollection(ctx, collection); err != nil {
		t.Fatal(err)
	}
	file := &datastore.File{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "File",
		UID: "00000000-0000-0000-0000-000000000108", Namespace: namespace.Name, Name: "file",
		RepositoryID: repository.UID, ResourceVersion: "1", CreationTimestamp: now,
	}
	if err := store.CreateFile(ctx, file); err != nil {
		t.Fatal(err)
	}

	originalOpen := openOwnerReferenceBackfill
	t.Cleanup(func() {
		openOwnerReferenceBackfill = originalOpen
		_ = rawStore.Close()
	})
	openOwnerReferenceBackfill = func(cfg config.ScyllaConfig) (ownerReferenceBackfillDatastore, error) {
		if len(cfg.Hosts) != 1 || cfg.Hosts[0] != "scylla.test:9042" || cfg.Keyspace != "gitstore_test" {
			t.Fatalf("unexpected explicit deployment selection: %+v", cfg)
		}
		return store, nil
	}

	runBackfill := func() {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := []string{
			"owner-reference-backfill", "run",
			"--hosts", "scylla.test:9042", "--keyspace", "gitstore_test",
			"--confirm", "--holder", "test-operator", "--page-size", "1", "--max-pages", "1",
		}
		if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("backfill run failed: %s", stderr.String())
		}
	}

	testUnresolved := func(t *testing.T) {
		rawStore, err := memdb.New()
		if err != nil {
			t.Fatal(err)
		}
		store := ownerReferenceBackfillTestStore{
			Datastore:                   rawStore,
			OwnerReferenceBackfillStore: rawStore.(datastore.OwnerReferenceBackfillStore),
		}
		t.Cleanup(func() { _ = rawStore.Close() })

		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		namespace := &datastore.Namespace{
			APIVersion: "gitstore.dev/v1beta1", Kind: "Namespace",
			UID: "00000000-0000-0000-0000-000000000201", Name: "unresolved-test",
			ResourceVersion: "1", CreationTimestamp: now,
		}
		if err := store.CreateNamespace(ctx, namespace); err != nil {
			t.Fatal(err)
		}
		repository := &datastore.Repository{
			APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
			UID: "00000000-0000-0000-0000-000000000202", Namespace: namespace.Name, Name: "catalog",
			ResourceVersion: "1", CreationTimestamp: now,
		}
		if err := store.CreateRepositoryInActiveNamespace(ctx, repository); err != nil {
			t.Fatal(err)
		}
		product := &datastore.Product{
			APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
			UID: "00000000-0000-0000-0000-000000000203", Namespace: namespace.Name, Name: "item",
			RepositoryID: repository.UID, ResourceVersion: "1", CreationTimestamp: now,
			Spec: json.RawMessage(`{"title":"Item","categoryRef":{"name":"missing"}}`),
		}
		if err := store.CreateProduct(ctx, product); err != nil {
			t.Fatal(err)
		}

		originalOpen := openOwnerReferenceBackfill
		t.Cleanup(func() { openOwnerReferenceBackfill = originalOpen })
		openOwnerReferenceBackfill = func(config.ScyllaConfig) (ownerReferenceBackfillDatastore, error) {
			return store, nil
		}

		args := []string{
			"owner-reference-backfill", "run",
			"--hosts", "scylla.test:9042", "--keyspace", "gitstore_test",
			"--confirm", "--holder", "test-operator", "--page-size", "10",
		}
		var runOutput, runError bytes.Buffer
		if code := run(args, strings.NewReader(""), &runOutput, &runError); code != 1 {
			t.Fatalf("run with unresolved relationship returned %d, want failure: %s", code, runError.String())
		}
		if !strings.Contains(runError.String(), "unresolved relationships") {
			t.Fatalf("run did not explain unresolved relationship failure: %s", runError.String())
		}

		var runStatus ownerReferenceBackfillStatus
		if err := json.Unmarshal(runOutput.Bytes(), &runStatus); err != nil {
			t.Fatalf("decode run status: %v", err)
		}
		if runStatus.Progress.Completed || runStatus.Progress.Kinds["Product"].Unresolved != 1 {
			t.Fatalf("run status hid unresolved relationship: %+v", runStatus.Progress)
		}

		statusArgs := []string{"owner-reference-backfill", "status", "--hosts", "scylla.test:9042", "--keyspace", "gitstore_test"}
		var statusOutput, statusError bytes.Buffer
		if code := run(statusArgs, strings.NewReader(""), &statusOutput, &statusError); code != 0 {
			t.Fatalf("status failed: %s", statusError.String())
		}
		var persistedStatus ownerReferenceBackfillStatus
		if err := json.Unmarshal(statusOutput.Bytes(), &persistedStatus); err != nil {
			t.Fatalf("decode persisted status: %v", err)
		}
		if !persistedStatus.Initialized || persistedStatus.Progress.Completed || persistedStatus.Progress.Kinds["Product"].Unresolved != 1 {
			t.Fatalf("status did not report durable unresolved relationship: %+v", persistedStatus)
		}
	}
	runBackfill()
	progress, err := store.LoadOwnerReferenceBackfillProgress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Kinds["Namespace"].Complete || progress.Kinds["Repository"].Complete {
		t.Fatalf("max-pages was not enforced: %+v", progress.Kinds)
	}

	for i := 0; i < 2*len(datastore.OwnerReferenceBackfillKinds())+5; i++ {
		if progress.Completed {
			break
		}
		runBackfill()
		progress, err = store.LoadOwnerReferenceBackfillProgress(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !progress.Completed {
		t.Fatalf("resumed scans did not complete: %+v", progress)
	}

	repairedRepository, err := store.GetRepository(ctx, repository.UID)
	if err != nil {
		t.Fatal(err)
	}
	var repositoryRefs []catalog.OwnerReference
	if err := json.Unmarshal(repairedRepository.OwnerReferences, &repositoryRefs); err != nil {
		t.Fatal(err)
	}
	if len(repositoryRefs) != 1 || repositoryRefs[0].UID != namespace.UID || repositoryRefs[0].Kind != "Namespace" {
		t.Fatalf("Repository Namespace owner was not repaired: %+v", repositoryRefs)
	}
	repairedProduct, err := store.GetProduct(ctx, product.UID)
	if err != nil {
		t.Fatal(err)
	}
	var productRefs []catalog.OwnerReference
	if err := json.Unmarshal(repairedProduct.OwnerReferences, &productRefs); err != nil {
		t.Fatal(err)
	}
	if len(productRefs) != 2 || productRefs[0].UID != parentCategory.UID || productRefs[0].Kind != "CategoryTaxonomy" ||
		productRefs[0].BlockOwnerDeletion || productRefs[1].UID != repository.UID || productRefs[1].Kind != "Repository" {
		t.Fatalf("Product Repository owner was not repaired: %+v", productRefs)
	}
	repairedVariant, err := store.GetProductVariant(ctx, variant.UID)
	if err != nil {
		t.Fatal(err)
	}
	var variantRefs []catalog.OwnerReference
	if err := json.Unmarshal(repairedVariant.OwnerReferences, &variantRefs); err != nil {
		t.Fatal(err)
	}
	if len(variantRefs) != 2 || variantRefs[0].UID != product.UID || variantRefs[0].Kind != "Product" ||
		!variantRefs[0].BlockOwnerDeletion || variantRefs[1].UID != repository.UID {
		t.Fatalf("ProductVariant owner references were not repaired: %+v", variantRefs)
	}
	repairedChild, err := store.GetCategoryTaxonomy(ctx, childCategory.UID)
	if err != nil {
		t.Fatal(err)
	}
	var childRefs []catalog.OwnerReference
	if err := json.Unmarshal(repairedChild.OwnerReferences, &childRefs); err != nil {
		t.Fatal(err)
	}
	if len(childRefs) != 2 || childRefs[0].UID != parentCategory.UID || childRefs[0].Kind != "CategoryTaxonomy" ||
		!childRefs[0].BlockOwnerDeletion || childRefs[1].UID != repository.UID {
		t.Fatalf("CategoryTaxonomy owner references were not repaired: %+v", childRefs)
	}
	repairedCollection, err := store.GetCollection(ctx, collection.UID)
	if err != nil {
		t.Fatal(err)
	}
	repairedFile, err := store.GetFile(ctx, file.UID)
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "Collection", raw: repairedCollection.OwnerReferences},
		{name: "File", raw: repairedFile.OwnerReferences},
	} {
		var refs []catalog.OwnerReference
		if err := json.Unmarshal(resource.raw, &refs); err != nil {
			t.Fatal(err)
		}
		if len(refs) != 1 || refs[0].Kind != "Repository" || refs[0].UID != repository.UID || !refs[0].BlockOwnerDeletion {
			t.Errorf("%s owner reference was not repaired: %+v", resource.name, refs)
		}
	}
	t.Run("unresolved status", testUnresolved)
}
