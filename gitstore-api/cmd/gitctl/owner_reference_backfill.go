// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"go.uber.org/zap"
)

const (
	ownerReferenceBackfillLeaseTTL = 30 * time.Second
	ownerReferenceBackfillTimeout  = 5 * time.Minute
)

type ownerReferenceBackfillDatastore interface {
	datastore.NamespaceStore
	datastore.RepositoryStore
	datastore.ProductStore
	datastore.ProductVariantStore
	datastore.CategoryTaxonomyStore
	datastore.CollectionStore
	datastore.FileStore
	datastore.ServiceAccountStore
	datastore.OwnerReferenceBackfillStore
	datastore.Closer
}

var openOwnerReferenceBackfill = func(cfg config.ScyllaConfig) (ownerReferenceBackfillDatastore, error) {
	store, err := scylla.New(cfg, zap.NewNop())
	if err != nil {
		return nil, err
	}
	backfillStore, ok := store.(ownerReferenceBackfillDatastore)
	if !ok {
		_ = store.Close()
		return nil, errors.New("Scylla datastore does not support owner-reference backfill")
	}
	return backfillStore, nil
}

type ownerReferenceBackfillStatus struct {
	Initialized bool                                     `json:"initialized"`
	Progress    datastore.OwnerReferenceBackfillProgress `json:"progress"`
}

type ownerReferenceBackfillCursor struct {
	NamespaceAfter string `json:"namespaceAfter"`
	ResourceAfter  string `json:"resourceAfter"`
}

type ownerReferencePageResult struct {
	Cursor     string
	Scanned    int64
	Repaired   int64
	Unresolved int64
	Complete   bool
}

func runOwnerReferenceBackfill(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "run" && args[0] != "status") {
		fmt.Fprintln(stderr, "owner-reference-backfill requires run or status")
		return 2
	}
	command := args[0]
	flags := flag.NewFlagSet("owner-reference-backfill "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	hosts := flags.String("hosts", "", "explicit comma-separated Scylla deployment endpoints")
	keyspace := flags.String("keyspace", "", "explicit Scylla deployment keyspace")
	username := flags.String("username", "", "Scylla username")
	tls := flags.Bool("tls", false, "enable Scylla TLS")
	disableShardAware := flags.Bool("disable-shard-aware-port", false, "disable shard-aware port discovery")
	confirm := flags.Bool("confirm", false, "confirm owner-reference metadata mutations")
	pageSize := flags.Int("page-size", datastore.MaxOwnerDependentPageSize, "resources per bounded page (1-100)")
	maxPages := flags.Int("max-pages", 100, "maximum pages processed by this invocation")
	timeout := flags.Duration("timeout", ownerReferenceBackfillTimeout, "maximum command duration")
	holder := flags.String("holder", "", "lease holder identity (defaults to hostname and process ID)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "owner-reference-backfill does not accept positional arguments")
		return 2
	}
	if len(splitNonEmpty(*hosts)) == 0 || strings.TrimSpace(*keyspace) == "" {
		fmt.Fprintln(stderr, "owner-reference-backfill requires explicit --hosts and --keyspace deployment selection")
		return 2
	}
	if command == "run" && !*confirm {
		fmt.Fprintln(stderr, "owner-reference-backfill run requires --confirm")
		return 2
	}
	if *pageSize < 1 || *pageSize > datastore.MaxOwnerDependentPageSize || *maxPages < 1 || *timeout <= 0 {
		fmt.Fprintln(stderr, "owner-reference-backfill requires --page-size 1-100, positive --max-pages, and positive --timeout")
		return 2
	}
	if *holder == "" {
		host, err := os.Hostname()
		if err != nil {
			fmt.Fprintf(stderr, "owner-reference-backfill could not identify lease holder: %v\n", err)
			return 1
		}
		*holder = fmt.Sprintf("%s-%d", host, os.Getpid())
	}

	cfg := config.ScyllaConfig{
		Hosts:                 splitNonEmpty(*hosts),
		Keyspace:              strings.TrimSpace(*keyspace),
		Username:              *username,
		Password:              os.Getenv("GITSTORE_API__DATASTORE__SCYLLA__PASSWORD"),
		TLS:                   *tls,
		DisableShardAwarePort: *disableShardAware,
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	store, err := openOwnerReferenceBackfill(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "owner-reference-backfill could not open selected Scylla deployment: %v\n", err)
		return 1
	}

	var resultErr error
	if command == "status" {
		resultErr = writeOwnerReferenceBackfillStatus(ctx, store, stdout)
	} else {
		resultErr = executeOwnerReferenceBackfill(ctx, store, *holder, *pageSize, *maxPages, stdout)
	}
	closeErr := store.Close()
	if resultErr != nil {
		if closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("close Scylla deployment: %w", closeErr))
		}
		fmt.Fprintf(stderr, "owner-reference-backfill %s failed: %v\n", command, resultErr)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintf(stderr, "owner-reference-backfill could not close Scylla deployment: %v\n", closeErr)
		return 1
	}
	return 0
}

func writeOwnerReferenceBackfillStatus(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	output io.Writer,
) error {
	progress, err := store.LoadOwnerReferenceBackfillProgress(ctx)
	if errors.Is(err, datastore.ErrNotFound) {
		progress = datastore.OwnerReferenceBackfillProgress{Kinds: map[string]datastore.OwnerReferenceBackfillKindProgress{}}
		return writeJSON(output, ownerReferenceBackfillStatus{Progress: progress})
	}
	if err != nil {
		return fmt.Errorf("load durable progress: %w", err)
	}
	return writeJSON(output, ownerReferenceBackfillStatus{Initialized: true, Progress: progress})
}

func executeOwnerReferenceBackfill(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	holder string,
	pageSize, maxPages int,
	output io.Writer,
) (resultErr error) {
	progress, err := store.LoadOwnerReferenceBackfillProgress(ctx)
	if errors.Is(err, datastore.ErrNotFound) {
		progress = datastore.OwnerReferenceBackfillProgress{Kinds: make(map[string]datastore.OwnerReferenceBackfillKindProgress)}
	} else if err != nil {
		return fmt.Errorf("load durable progress: %w", err)
	}
	if progress.Completed {
		return writeJSON(output, ownerReferenceBackfillStatus{Initialized: true, Progress: progress})
	}

	lease, acquired, err := store.AcquireOwnerReferenceBackfillLease(ctx, holder, time.Now().UTC(), ownerReferenceBackfillLeaseTTL)
	if err != nil {
		return fmt.Errorf("acquire durable lease: %w", err)
	}
	if !acquired {
		return errors.New("another owner-reference backfill operator holds the lease")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := store.ReleaseOwnerReferenceBackfillLease(releaseCtx, lease); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("release durable lease: %w", err)
		}
	}()

	if progress.Kinds == nil {
		progress.Kinds = make(map[string]datastore.OwnerReferenceBackfillKindProgress)
	}
	if allOwnerReferenceKindsComplete(progress) {
		retriedUnresolved := false
		for _, kind := range datastore.OwnerReferenceBackfillKinds() {
			state := progress.Kinds[kind]
			if state.Unresolved == 0 {
				continue
			}
			state.Cursor, state.Scanned, state.Repaired, state.Unresolved, state.Complete = "", 0, 0, 0, false
			progress.Kinds[kind] = state
			retriedUnresolved = true
		}
		if retriedUnresolved {
			progress.Completed = false
			progress.UpdatedAt = time.Now().UTC()
			if err := store.SaveOwnerReferenceBackfillProgress(ctx, lease, progress); err != nil {
				return fmt.Errorf("reset unresolved kinds for retry: %w", err)
			}
		}
	}

	pages := 0
	for _, kind := range datastore.OwnerReferenceBackfillKinds() {
		for !progress.Kinds[kind].Complete && pages < maxPages {
			lease, acquired, err = store.RenewOwnerReferenceBackfillLease(ctx, lease, time.Now().UTC(), ownerReferenceBackfillLeaseTTL)
			if err != nil {
				return fmt.Errorf("renew durable lease: %w", err)
			}
			if !acquired {
				return datastore.ErrStaleBackfillLease
			}
			state := progress.Kinds[kind]
			page, err := scanOwnerReferenceBackfillPage(ctx, store, kind, state.Cursor, pageSize)
			if err != nil {
				return fmt.Errorf("scan %s page: %w", kind, err)
			}
			state.Cursor = page.Cursor
			state.Scanned += page.Scanned
			state.Repaired += page.Repaired
			state.Unresolved += page.Unresolved
			state.Complete = page.Complete
			progress.Kinds[kind] = state
			progress.Completed = false
			progress.UpdatedAt = time.Now().UTC()
			if err := store.SaveOwnerReferenceBackfillProgress(ctx, lease, progress); err != nil {
				return fmt.Errorf("persist %s checkpoint: %w", kind, err)
			}
			pages++
		}
		if pages == maxPages {
			break
		}
	}

	if allOwnerReferenceKindsComplete(progress) {
		if unresolved := ownerReferenceBackfillUnresolved(progress); unresolved == 0 {
			progress.Completed = true
			progress.UpdatedAt = time.Now().UTC()
			if err := store.SaveOwnerReferenceBackfillProgress(ctx, lease, progress); err != nil {
				return fmt.Errorf("persist verified completion marker: %w", err)
			}
		}
	}
	if err := writeJSON(output, ownerReferenceBackfillStatus{Initialized: true, Progress: progress}); err != nil {
		return fmt.Errorf("write progress status: %w", err)
	}
	if allOwnerReferenceKindsComplete(progress) && ownerReferenceBackfillUnresolved(progress) > 0 {
		return fmt.Errorf("scan completed with %d unresolved relationships; resolve them and rerun", ownerReferenceBackfillUnresolved(progress))
	}
	return nil
}

func allOwnerReferenceKindsComplete(progress datastore.OwnerReferenceBackfillProgress) bool {
	for _, kind := range datastore.OwnerReferenceBackfillKinds() {
		if !progress.Kinds[kind].Complete {
			return false
		}
	}
	return true
}

func ownerReferenceBackfillUnresolved(progress datastore.OwnerReferenceBackfillProgress) int64 {
	var unresolved int64
	for _, kind := range datastore.OwnerReferenceBackfillKinds() {
		unresolved += progress.Kinds[kind].Unresolved
	}
	return unresolved
}

func scanOwnerReferenceBackfillPage(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	kind, cursor string,
	pageSize int,
) (ownerReferencePageResult, error) {
	switch kind {
	case "Namespace":
		return scanGlobalOwnerReferencePage(ctx, cursor, pageSize, store.ListNamespaces,
			func(item *datastore.Namespace) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.Namespace) (bool, int64, error) {
				return repairNamespaceOwnerReferences(ctx, store, item)
			})
	case "Repository":
		return scanNamespacedOwnerReferencePage(ctx, store, cursor, pageSize, store.ListRepositoriesByNamespace,
			func(item *datastore.Repository) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.Repository) (bool, int64, error) {
				return repairRepositoryOwnerReferences(ctx, store, item)
			})
	case "Product":
		return scanNamespacedOwnerReferencePage(ctx, store, cursor, pageSize, store.ListProducts,
			func(item *datastore.Product) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.Product) (bool, int64, error) {
				return repairProductOwnerReferences(ctx, store, item)
			})
	case "ProductVariant":
		return scanNamespacedOwnerReferencePage(ctx, store, cursor, pageSize, store.ListProductVariants,
			func(item *datastore.ProductVariant) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.ProductVariant) (bool, int64, error) {
				return repairProductVariantOwnerReferences(ctx, store, item)
			})
	case "CategoryTaxonomy":
		return scanNamespacedOwnerReferencePage(ctx, store, cursor, pageSize, store.ListCategoryTaxonomies,
			func(item *datastore.CategoryTaxonomy) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.CategoryTaxonomy) (bool, int64, error) {
				return repairCategoryTaxonomyOwnerReferences(ctx, store, item)
			})
	case "Collection":
		return scanNamespacedOwnerReferencePage(ctx, store, cursor, pageSize, store.ListCollections,
			func(item *datastore.Collection) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.Collection) (bool, int64, error) {
				return repairCollectionOwnerReferences(ctx, store, item)
			})
	case "File":
		return scanNamespacedOwnerReferencePage(ctx, store, cursor, pageSize, store.ListFiles,
			func(item *datastore.File) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(item *datastore.File) (bool, int64, error) { return repairFileOwnerReferences(ctx, store, item) })
	case "ServiceAccount":
		return scanGlobalOwnerReferencePage(ctx, cursor, pageSize, store.ListServiceAccounts,
			func(item *datastore.ServiceAccount) (time.Time, string) { return item.CreationTimestamp, item.UID },
			func(*datastore.ServiceAccount) (bool, int64, error) { return false, 0, nil })
	default:
		return ownerReferencePageResult{}, fmt.Errorf("%w: unsupported resource kind %q", datastore.ErrInvalidArgument, kind)
	}
}

func scanGlobalOwnerReferencePage[T any](
	ctx context.Context,
	cursor string,
	pageSize int,
	list func(context.Context, datastore.PageParams) (*datastore.PageResult[T], error),
	position func(*T) (time.Time, string),
	repair func(*T) (bool, int64, error),
) (ownerReferencePageResult, error) {
	page, err := list(ctx, datastore.PageParams{First: pageSize, After: cursor})
	if err != nil {
		return ownerReferencePageResult{}, err
	}
	result := ownerReferencePageResult{Complete: !page.HasNext}
	for _, item := range page.Items {
		if item == nil {
			return ownerReferencePageResult{}, errors.New("datastore returned a nil resource in backfill page")
		}
		result.Scanned++
		repaired, unresolved, err := repair(item)
		if err != nil {
			return ownerReferencePageResult{}, err
		}
		if repaired {
			result.Repaired++
		}
		result.Unresolved += unresolved
	}
	if len(page.Items) == 0 {
		if page.HasNext {
			return ownerReferencePageResult{}, errors.New("datastore returned an empty page with more results")
		}
		result.Complete = true
		return result, nil
	}
	if page.HasNext {
		createdAt, uid := position(page.Items[len(page.Items)-1])
		result.Cursor = encodeOwnerReferenceKeysetCursor(createdAt, uid)
	}
	return result, nil
}

func scanNamespacedOwnerReferencePage[T any](
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	cursor string,
	pageSize int,
	list func(context.Context, string, datastore.PageParams) (*datastore.PageResult[T], error),
	position func(*T) (time.Time, string),
	repair func(*T) (bool, int64, error),
) (ownerReferencePageResult, error) {
	state, err := decodeOwnerReferenceBackfillCursor(cursor)
	if err != nil {
		return ownerReferencePageResult{}, err
	}
	namespaces, err := store.ListNamespaces(ctx, datastore.PageParams{First: 1, After: state.NamespaceAfter})
	if err != nil {
		return ownerReferencePageResult{}, fmt.Errorf("list Namespace page: %w", err)
	}
	if len(namespaces.Items) == 0 {
		return ownerReferencePageResult{Complete: true}, nil
	}
	namespace := namespaces.Items[0]
	if namespace == nil {
		return ownerReferencePageResult{}, errors.New("datastore returned a nil Namespace in backfill page")
	}
	resources, err := list(ctx, namespace.Name, datastore.PageParams{First: pageSize, After: state.ResourceAfter})
	if err != nil {
		return ownerReferencePageResult{}, err
	}
	result := ownerReferencePageResult{Complete: !namespaces.HasNext && !resources.HasNext}
	for _, item := range resources.Items {
		if item == nil {
			return ownerReferencePageResult{}, errors.New("datastore returned a nil resource in backfill page")
		}
		result.Scanned++
		repaired, unresolved, err := repair(item)
		if err != nil {
			return ownerReferencePageResult{}, err
		}
		if repaired {
			result.Repaired++
		}
		result.Unresolved += unresolved
	}
	if len(resources.Items) == 0 && resources.HasNext {
		return ownerReferencePageResult{}, errors.New("datastore returned an empty page with more results")
	}
	switch {
	case resources.HasNext:
		createdAt, uid := position(resources.Items[len(resources.Items)-1])
		result.Cursor = encodeOwnerReferenceBackfillCursor(state.NamespaceAfter, encodeOwnerReferenceKeysetCursor(createdAt, uid))
	case namespaces.HasNext:
		createdAt, uid := namespace.CreationTimestamp, namespace.UID
		result.Cursor = encodeOwnerReferenceBackfillCursor(encodeOwnerReferenceKeysetCursor(createdAt, uid), "")
	}
	return result, nil
}

func encodeOwnerReferenceBackfillCursor(namespaceAfter, resourceAfter string) string {
	data, _ := json.Marshal(ownerReferenceBackfillCursor{NamespaceAfter: namespaceAfter, ResourceAfter: resourceAfter})
	return base64.StdEncoding.EncodeToString(data)
}

func decodeOwnerReferenceBackfillCursor(cursor string) (ownerReferenceBackfillCursor, error) {
	if cursor == "" {
		return ownerReferenceBackfillCursor{}, nil
	}
	data, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return ownerReferenceBackfillCursor{}, fmt.Errorf("%w: invalid owner-reference backfill cursor: %v", datastore.ErrInvalidArgument, err)
	}
	var decoded ownerReferenceBackfillCursor
	if err := json.Unmarshal(data, &decoded); err != nil {
		return ownerReferenceBackfillCursor{}, fmt.Errorf("%w: invalid owner-reference backfill cursor: %v", datastore.ErrInvalidArgument, err)
	}
	return decoded, nil
}

func encodeOwnerReferenceKeysetCursor(createdAt time.Time, uid string) string {
	payload := fmt.Sprintf("keyset|%s|%s", createdAt.Format(time.RFC3339Nano), uid)
	return base64.StdEncoding.EncodeToString([]byte(payload))
}

func repairNamespaceOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	namespace *datastore.Namespace,
) (bool, int64, error) {
	raw, changed, err := mergeOwnerReferenceMetadata(namespace.OwnerReferences, nil, nil)
	if err != nil || !changed {
		return false, 0, err
	}
	expected := namespace.ResourceVersion
	namespace.OwnerReferences = raw
	datastore.AdvanceNamespaceSystemVersion(namespace)
	if err := store.UpdateNamespace(ctx, namespace, expected); err != nil {
		return false, 0, fmt.Errorf("update Namespace %s with resourceVersion precondition: %w", namespace.UID, err)
	}
	return true, 0, nil
}

func repairRepositoryOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	repository *datastore.Repository,
) (bool, int64, error) {
	var expected []catalog.OwnerReference
	var unresolved int64
	namespace, err := store.GetNamespaceByName(ctx, repository.Namespace)
	if errors.Is(err, datastore.ErrNotFound) || (err == nil && (namespace == nil || namespace.DeletionTimestamp != nil)) {
		unresolved++
	} else if err != nil {
		return false, 0, err
	} else {
		expected = append(expected, catalog.OwnerReference{
			APIVersion: ownerReferenceAPIVersion(namespace.APIVersion, "gitstore.dev/v1beta1"),
			Kind:       "Namespace", Name: namespace.Name, UID: namespace.UID, BlockOwnerDeletion: true,
		})
	}
	raw, changed, err := mergeOwnerReferenceMetadata(repository.OwnerReferences, []string{"Namespace"}, expected)
	if err != nil || !changed {
		return false, unresolved, err
	}
	expectedVersion := repository.ResourceVersion
	repository.OwnerReferences = raw
	datastore.AdvanceRepositorySystemVersion(repository)
	if err := store.UpdateRepository(ctx, repository, expectedVersion); err != nil {
		return false, unresolved, fmt.Errorf("update Repository %s with resourceVersion precondition: %w", repository.UID, err)
	}
	return true, unresolved, nil
}

func repairProductOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	product *datastore.Product,
) (bool, int64, error) {
	var spec catalog.ProductSpec
	if len(product.Spec) > 0 {
		if err := json.Unmarshal(product.Spec, &spec); err != nil {
			return false, 0, fmt.Errorf("decode Product %s spec: %w", product.UID, err)
		}
	}
	expected, unresolved, err := domainOwnerReferences(ctx, store, product.Namespace,
		product.UID, "CategoryTaxonomy", spec.CategoryRef, false)
	if err != nil {
		return false, unresolved, err
	}
	return updateProductOwnerReferences(ctx, store, product, expected, unresolved)
}

func repairProductVariantOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	variant *datastore.ProductVariant,
) (bool, int64, error) {
	var expected []catalog.OwnerReference
	var unresolved int64
	if variant.ProductRefName != "" {
		owner, err := store.GetProductByName(ctx, variant.Namespace, variant.ProductRefName)
		if errors.Is(err, datastore.ErrNotFound) || (err == nil && (owner == nil || owner.DeletionTimestamp != nil)) {
			unresolved++
		} else if err != nil {
			return false, 0, err
		} else if owner.UID == variant.UID {
			unresolved++
		} else {
			expected = append(expected, catalog.OwnerReference{
				APIVersion: ownerReferenceAPIVersion(owner.APIVersion, "catalog.gitstore.dev/v1beta1"),
				Kind:       "Product", Name: owner.Name, UID: owner.UID, BlockOwnerDeletion: true,
				RepositoryID: owner.RepositoryID,
			})
		}
	}
	repository, count, err := resolveRepositoryOwner(ctx, store, variant.RepositoryID)
	unresolved += count
	if err != nil {
		return false, unresolved, err
	}
	if repository != nil {
		expected = append(expected, repositoryOwnerReference(repository))
	}
	raw, changed, err := mergeOwnerReferenceMetadata(variant.OwnerReferences, []string{"Product", "Repository"}, expected)
	if err != nil || !changed {
		return false, unresolved, err
	}
	expectedVersion := variant.ResourceVersion
	variant.OwnerReferences = raw
	datastore.AdvanceProductVariantSystemVersion(variant)
	if err := store.UpdateProductVariant(ctx, variant, expectedVersion); err != nil {
		return false, unresolved, fmt.Errorf("update ProductVariant %s with resourceVersion precondition: %w", variant.UID, err)
	}
	return true, unresolved, nil
}

func repairCategoryTaxonomyOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	category *datastore.CategoryTaxonomy,
) (bool, int64, error) {
	var expected []catalog.OwnerReference
	var unresolved int64
	if category.ParentName != "" {
		owner, err := store.GetCategoryTaxonomyByName(ctx, category.Namespace, category.ParentName)
		if errors.Is(err, datastore.ErrNotFound) || (err == nil && (owner == nil || owner.DeletionTimestamp != nil)) {
			unresolved++
		} else if err != nil {
			return false, 0, err
		} else if owner.UID == category.UID {
			unresolved++
		} else {
			expected = append(expected, catalog.OwnerReference{
				APIVersion: ownerReferenceAPIVersion(owner.APIVersion, "catalog.gitstore.dev/v1beta1"),
				Kind:       "CategoryTaxonomy", Name: owner.Name, UID: owner.UID, BlockOwnerDeletion: true,
				RepositoryID: owner.RepositoryID,
			})
		}
	}
	repository, count, err := resolveRepositoryOwner(ctx, store, category.RepositoryID)
	unresolved += count
	if err != nil {
		return false, unresolved, err
	}
	if repository != nil {
		expected = append(expected, repositoryOwnerReference(repository))
	}
	raw, changed, err := mergeOwnerReferenceMetadata(category.OwnerReferences, []string{"CategoryTaxonomy", "Repository"}, expected)
	if err != nil || !changed {
		return false, unresolved, err
	}
	expectedVersion := category.ResourceVersion
	category.OwnerReferences = raw
	datastore.AdvanceCategoryTaxonomySystemVersion(category)
	if err := store.UpdateCategoryTaxonomy(ctx, category, expectedVersion); err != nil {
		return false, unresolved, fmt.Errorf("update CategoryTaxonomy %s with resourceVersion precondition: %w", category.UID, err)
	}
	return true, unresolved, nil
}

func repairCollectionOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	collection *datastore.Collection,
) (bool, int64, error) {
	repository, unresolved, err := resolveRepositoryOwner(ctx, store, collection.RepositoryID)
	if err != nil {
		return false, unresolved, err
	}
	var expected []catalog.OwnerReference
	if repository != nil {
		expected = append(expected, repositoryOwnerReference(repository))
	}
	raw, changed, err := mergeOwnerReferenceMetadata(collection.OwnerReferences, []string{"Repository"}, expected)
	if err != nil || !changed {
		return false, unresolved, err
	}
	expectedVersion := collection.ResourceVersion
	collection.OwnerReferences = raw
	datastore.AdvanceCollectionSystemVersion(collection)
	if err := store.UpdateCollection(ctx, collection, expectedVersion); err != nil {
		return false, unresolved, fmt.Errorf("update Collection %s with resourceVersion precondition: %w", collection.UID, err)
	}
	return true, unresolved, nil
}

func repairFileOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	file *datastore.File,
) (bool, int64, error) {
	repository, unresolved, err := resolveRepositoryOwner(ctx, store, file.RepositoryID)
	if err != nil {
		return false, unresolved, err
	}
	var expected []catalog.OwnerReference
	if repository != nil {
		expected = append(expected, repositoryOwnerReference(repository))
	}
	raw, changed, err := mergeOwnerReferenceMetadata(file.OwnerReferences, []string{"Repository"}, expected)
	if err != nil || !changed {
		return false, unresolved, err
	}
	expectedVersion := file.ResourceVersion
	file.OwnerReferences = raw
	datastore.AdvanceFileSystemVersion(file)
	if err := store.UpdateFile(ctx, file, expectedVersion); err != nil {
		return false, unresolved, fmt.Errorf("update File %s with resourceVersion precondition: %w", file.UID, err)
	}
	return true, unresolved, nil
}

func updateProductOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	product *datastore.Product,
	expected []catalog.OwnerReference,
	unresolved int64,
) (bool, int64, error) {
	repository, count, err := resolveRepositoryOwner(ctx, store, product.RepositoryID)
	unresolved += count
	if err != nil {
		return false, unresolved, err
	}
	if repository != nil {
		expected = append(expected, repositoryOwnerReference(repository))
	}
	raw, changed, err := mergeOwnerReferenceMetadata(product.OwnerReferences, []string{"CategoryTaxonomy", "Repository"}, expected)
	if err != nil || !changed {
		return false, unresolved, err
	}
	expectedVersion := product.ResourceVersion
	product.OwnerReferences = raw
	datastore.AdvanceProductSystemVersion(product)
	if err := store.UpdateProduct(ctx, product, expectedVersion); err != nil {
		return false, unresolved, fmt.Errorf("update Product %s with resourceVersion precondition: %w", product.UID, err)
	}
	return true, unresolved, nil
}

func domainOwnerReferences(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	namespace, dependentUID, kind string,
	reference *catalog.ObjectReference,
	blockOwnerDeletion bool,
) ([]catalog.OwnerReference, int64, error) {
	if reference == nil || reference.Name == "" {
		return nil, 0, nil
	}
	owner, err := store.GetCategoryTaxonomyByName(ctx, namespace, reference.Name)
	if errors.Is(err, datastore.ErrNotFound) || (err == nil && (owner == nil || owner.DeletionTimestamp != nil)) {
		return nil, 1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if owner.UID == dependentUID {
		return nil, 1, nil
	}
	return []catalog.OwnerReference{{
		APIVersion: ownerReferenceAPIVersion(owner.APIVersion, "catalog.gitstore.dev/v1beta1"),
		Kind:       kind, Name: owner.Name, UID: owner.UID, BlockOwnerDeletion: blockOwnerDeletion,
		RepositoryID: owner.RepositoryID,
	}}, 0, nil
}

func resolveRepositoryOwner(
	ctx context.Context,
	store ownerReferenceBackfillDatastore,
	repositoryID string,
) (*datastore.Repository, int64, error) {
	if repositoryID == "" {
		return nil, 1, nil
	}
	repository, err := store.GetRepository(ctx, repositoryID)
	if errors.Is(err, datastore.ErrNotFound) || (err == nil && (repository == nil || repository.DeletionTimestamp != nil)) {
		return nil, 1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	return repository, 0, nil
}

func repositoryOwnerReference(repository *datastore.Repository) catalog.OwnerReference {
	return catalog.OwnerReference{
		APIVersion: ownerReferenceAPIVersion(repository.APIVersion, "gitstore.dev/v1beta1"),
		Kind:       "Repository", Name: repository.Name, UID: repository.UID,
		BlockOwnerDeletion: true, RepositoryID: repository.UID,
	}
}

func ownerReferenceAPIVersion(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func mergeOwnerReferenceMetadata(
	current json.RawMessage,
	managedKinds []string,
	expected []catalog.OwnerReference,
) (json.RawMessage, bool, error) {
	var existing []catalog.OwnerReference
	if len(current) > 0 && string(current) != "null" {
		if err := json.Unmarshal(current, &existing); err != nil {
			return nil, false, fmt.Errorf("decode ownerReferences: %w", err)
		}
	}
	managed := make(map[string]struct{}, len(managedKinds))
	for _, kind := range managedKinds {
		managed[kind] = struct{}{}
	}
	expectedByKind := make(map[string]catalog.OwnerReference, len(expected))
	for _, reference := range expected {
		expectedByKind[reference.Kind] = reference
	}
	merged := make([]catalog.OwnerReference, 0, len(existing)+len(expected))
	added := make(map[string]struct{}, len(expected))
	for _, reference := range existing {
		if _, isManaged := managed[reference.Kind]; !isManaged {
			merged = append(merged, reference)
			continue
		}
		if replacement, exists := expectedByKind[reference.Kind]; exists {
			if _, alreadyAdded := added[reference.Kind]; !alreadyAdded {
				merged = append(merged, replacement)
				added[reference.Kind] = struct{}{}
			}
		}
	}
	for _, reference := range expected {
		if _, exists := added[reference.Kind]; !exists {
			merged = append(merged, reference)
			added[reference.Kind] = struct{}{}
		}
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, false, fmt.Errorf("encode ownerReferences: %w", err)
	}
	return raw, !bytes.Equal(current, raw), nil
}
