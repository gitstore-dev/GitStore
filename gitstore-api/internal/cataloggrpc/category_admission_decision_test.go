// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// denyCategoryTitlePolicy denies any CategoryTaxonomy whose title is "Denied".
type denyCategoryTitlePolicy struct{}

func (denyCategoryTitlePolicy) Name() string { return "test-deny-category-title" }

func (denyCategoryTitlePolicy) Validate(_ context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if req.Kind != "CategoryTaxonomy" {
		return admission.DecisionAllow()
	}
	if res, ok := req.Object.(*catalog.CategoryTaxonomyResource); ok && res.Spec.Title == "Denied" {
		return admission.DecisionDeny("title Denied is not allowed", "spec.title")
	}
	return admission.DecisionAllow()
}

type failingCategoryWriteStore struct {
	datastore.Datastore
}

type productAdmissionInterleavingStore struct {
	datastore.Datastore
	beforeUpdate func(context.Context) error
	failure      error
	reloadError  error
	updates      int
	reloads      int
}

func (s *productAdmissionInterleavingStore) UpdateProduct(ctx context.Context, product *datastore.Product, expected string) error {
	s.updates++
	if s.beforeUpdate != nil {
		before := s.beforeUpdate
		s.beforeUpdate = nil
		if err := before(ctx); err != nil {
			return err
		}
	}
	if s.failure != nil {
		return s.failure
	}
	return s.Datastore.UpdateProduct(ctx, product, expected)
}

func (s *productAdmissionInterleavingStore) GetProduct(ctx context.Context, uid string) (*datastore.Product, error) {
	s.reloads++
	if s.reloadError != nil {
		return nil, s.reloadError
	}
	return s.Datastore.GetProduct(ctx, uid)
}

func TestProductAdmissionRebasesOnlyStatusConflicts(t *testing.T) {
	for _, scenario := range []string{
		"status", "competing-spec", "terminating", "deletion-intent", "malformed-deletion-intent",
		"labels", "annotations", "body", "provenance", "creation-metadata", "identity",
		"storage-error", "exhausted", "reload-error", "canceled",
	} {
		t.Run(scenario, func(t *testing.T) {
			base, err := memdb.New()
			require.NoError(t, err)
			server := newDecisionServer(t, base)
			parsed, body, err := validate.NewParser().ParseResource(strings.NewReader(
				"---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: widget\n  namespace: gitstore\nspec:\n  title: Original\n---\n"))
			require.NoError(t, err)
			admCtx := AdmissionContext{
				RepositoryID: "00000000-0000-0000-0000-000000000001",
				Namespace:    "gitstore", ActorSubject: "alice", CommitSHA: strings.Repeat("a", 40),
				RefName: "refs/heads/main", Revision: "main@sha1:" + strings.Repeat("a", 40), Now: time.Now().UTC(),
			}
			require.Equal(t, admission.EntryAccepted,
				server.admitProduct(t.Context(), parsed.Product, body, admCtx, "products/widget.md", admission.OperationCreate, nil).Outcome)
			existing, err := base.GetProductByName(t.Context(), "gitstore", "widget")
			require.NoError(t, err)
			initialVersion := existing.ResourceVersion
			wrapped := &productAdmissionInterleavingStore{Datastore: base}
			server.store = wrapped
			parsed.Product.Spec.Title = "Updated"
			admCtx.CommitSHA, admCtx.Revision = strings.Repeat("b", 40), "main@sha1:"+strings.Repeat("b", 40)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			storageError := errors.New("product storage unavailable")
			switch scenario {
			case "status":
				wrapped.beforeUpdate = func(ctx context.Context) error {
					_, err := base.UpdateProductStatus(ctx, existing.Namespace, existing.Name, datastore.ProductStatusPatch{
						ResourceVersion: initialVersion,
						Conditions:      []catalog.Condition{{Type: catalog.ConditionReady, Status: "True"}},
					})
					return err
				}
			case "competing-spec", "terminating", "deletion-intent", "malformed-deletion-intent",
				"labels", "annotations", "body", "provenance", "creation-metadata", "identity":
				wrapped.beforeUpdate = func(ctx context.Context) error {
					current, err := base.GetProduct(ctx, existing.UID)
					if err != nil {
						return err
					}
					expected := current.ResourceVersion
					datastore.AdvanceProductSystemVersion(current)
					switch scenario {
					case "competing-spec":
						current.Generation++
						current.GitCommitSHA = strings.Repeat("c", 40)
						current.Spec = []byte(`{"title":"Competing"}`)
					case "terminating":
						now := time.Now().UTC()
						current.DeletionTimestamp = &now
					case "deletion-intent":
						current.Status, err = admission.WithDeletionIntent(current.Status, &admission.InfrastructureDeletionIntent{
							UID: current.UID, RepositoryID: current.RepositoryID, Path: current.SourcePath,
							Ref: current.GitRef, ExpectedCommit: current.GitCommitSHA, Actor: "bob",
						}, current.Generation, time.Now().UTC())
						if err != nil {
							return err
						}
					case "malformed-deletion-intent":
						current.Status = json.RawMessage(`{"deletionIntent":42}`)
					case "labels":
						current.Labels = map[string]string{"team": "competing"}
					case "annotations":
						current.Annotations = map[string]string{"owner": "competing"}
					case "body":
						current.Body = "Competing body"
					case "provenance":
						current.SourcePath = "products/competing.md"
					case "creation-metadata":
						current.CreationActor = "bob"
						current.CreationTimestamp = current.CreationTimestamp.Add(time.Nanosecond)
					case "identity":
						current.Name = "replacement"
					}
					return base.UpdateProduct(ctx, current, expected)
				}
			case "storage-error":
				wrapped.failure = storageError
			case "exhausted":
				wrapped.failure = datastore.ErrConflict
			case "reload-error":
				wrapped.failure, wrapped.reloadError = datastore.ErrConflict, storageError
			case "canceled":
				cancel()
			}
			decision := server.admitProduct(ctx, parsed.Product, body, admCtx, "products/widget.md", admission.OperationUpdate, existing)
			current, err := base.GetProduct(t.Context(), existing.UID)
			require.NoError(t, err)
			switch scenario {
			case "status":
				require.Equal(t, admission.EntryAccepted, decision.Outcome)
				require.NoError(t, decision.Err)
				require.Equal(t, 2, wrapped.updates)
				require.EqualValues(t, 2, current.Generation)
				require.Equal(t, "3", current.ResourceVersion)
				require.Equal(t, admCtx.CommitSHA, current.GitCommitSHA)
				var spec catalog.ProductSpec
				require.NoError(t, json.Unmarshal(current.Spec, &spec))
				require.Equal(t, "Updated", spec.Title)
				require.Equal(t, "ACTIVE", spec.Lifecycle.State)
			case "canceled":
				require.ErrorIs(t, decision.Err, context.Canceled)
				require.Zero(t, wrapped.updates)
			case "storage-error", "reload-error":
				require.ErrorIs(t, decision.Err, storageError)
				require.Equal(t, 1, wrapped.updates)
			case "malformed-deletion-intent":
				require.Equal(t, admission.EntryFailed, decision.Outcome)
				require.ErrorContains(t, decision.Err, "read Product deletion intent after admission conflict")
				require.Equal(t, 1, wrapped.updates)
				require.JSONEq(t, `{"deletionIntent":42}`, string(current.Status))
			default:
				require.Equal(t, admission.EntryFailed, decision.Outcome)
				require.ErrorIs(t, decision.Err, datastore.ErrConflict)
				if scenario == "exhausted" {
					require.Equal(t, maxRepositoryAdmissionUpdateAttempts, wrapped.updates)
					require.Equal(t, maxRepositoryAdmissionUpdateAttempts-1, wrapped.reloads)
				} else {
					require.Equal(t, 1, wrapped.updates)
				}
				if scenario == "competing-spec" {
					require.Equal(t, strings.Repeat("c", 40), current.GitCommitSHA)
					require.JSONEq(t, `{"title":"Competing"}`, string(current.Spec))
				}
				if scenario == "terminating" {
					require.NotNil(t, current.DeletionTimestamp)
				}
				switch scenario {
				case "deletion-intent":
					intent, err := admission.ReadDeletionIntent(current.Status)
					require.NoError(t, err)
					require.NotNil(t, intent)
					require.Equal(t, current.UID, intent.UID)
					require.Equal(t, "bob", intent.Actor)
				case "labels":
					require.Equal(t, map[string]string{"team": "competing"}, current.Labels)
				case "annotations":
					require.Equal(t, map[string]string{"owner": "competing"}, current.Annotations)
				case "body":
					require.Equal(t, "Competing body", current.Body)
				case "provenance":
					require.Equal(t, "products/competing.md", current.SourcePath)
				case "creation-metadata":
					require.Equal(t, "bob", current.CreationActor)
				case "identity":
					require.Equal(t, "replacement", current.Name)
				}
			}
		})
	}
}

type productDeletionInterleavingStore struct {
	datastore.Datastore
	datastore.ProductLifecycleStore
	datastore.OwnerReferenceStore
	beforeMark             func(context.Context) error
	markError, reloadError error
	marks, reloads         int
	blocked                bool
}

func (s *productDeletionInterleavingStore) MarkProductTerminating(ctx context.Context, uid, version, finalizer string, at time.Time) (*datastore.Product, error) {
	s.marks++
	if s.beforeMark != nil {
		before := s.beforeMark
		s.beforeMark = nil
		if err := before(ctx); err != nil {
			return nil, err
		}
	}
	if s.markError != nil {
		return nil, s.markError
	}
	return s.ProductLifecycleStore.MarkProductTerminating(ctx, uid, version, finalizer, at)
}

func (s *productDeletionInterleavingStore) GetProduct(ctx context.Context, uid string) (*datastore.Product, error) {
	s.reloads++
	if s.reloadError != nil {
		return nil, s.reloadError
	}
	return s.Datastore.GetProduct(ctx, uid)
}

func (s *productDeletionInterleavingStore) HasBlockingOwnerDependents(context.Context, datastore.OwnerReferenceScope, string) (bool, error) {
	return s.blocked, nil
}

func TestProductDeletionRebasesOnlyControllerChanges(t *testing.T) {
	for _, scenario := range []string{"status", "peer-termination", "spec", "labels", "provenance", "replacement", "blocked", "blocked-after-conflict", "policy-reload", "deletion-intent", "malformed-deletion-intent", "storage-error", "reload-error", "exhausted", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			base, err := memdb.New()
			require.NoError(t, err)
			server := newDecisionServer(t, base)
			server.registry = auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
			product := &datastore.Product{
				UID: "00000000-0000-0000-0000-000000000003", Namespace: "gitstore", Name: "widget",
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Generation: 1, ResourceVersion: "1",
				RepositoryID: "00000000-0000-0000-0000-000000000001", SourcePath: "products/widget.md",
				GitRef: "refs/heads/main", GitCommitSHA: strings.Repeat("a", 40), Spec: json.RawMessage(`{"title":"Original"}`),
			}
			require.NoError(t, base.CreateProduct(t.Context(), product))
			existing, err := base.GetProduct(t.Context(), product.UID)
			require.NoError(t, err)
			wrapped := &productDeletionInterleavingStore{
				Datastore: base, ProductLifecycleStore: base.(datastore.ProductLifecycleStore), OwnerReferenceStore: base.(datastore.OwnerReferenceStore),
			}
			server.store = wrapped
			ctx, cancel := context.WithCancel(auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}))
			defer cancel()
			storageError := errors.New("product deletion storage unavailable")
			switch scenario {
			case "status", "blocked-after-conflict", "policy-reload":
				wrapped.beforeMark = func(ctx context.Context) error {
					_, err := base.UpdateProductStatus(ctx, product.Namespace, product.Name, datastore.ProductStatusPatch{
						ResourceVersion: existing.ResourceVersion,
						Conditions:      []catalog.Condition{{Type: catalog.ConditionReady, Status: "True"}},
					})
					wrapped.blocked = scenario == "blocked-after-conflict"
					if scenario == "policy-reload" {
						server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
					}
					return err
				}
			case "peer-termination":
				wrapped.beforeMark = func(ctx context.Context) error {
					_, err := wrapped.ProductLifecycleStore.MarkProductTerminating(ctx, existing.UID, existing.ResourceVersion, "gitstore.dev/foreground-deletion", time.Now().UTC())
					return err
				}
			case "spec", "labels", "provenance", "deletion-intent", "malformed-deletion-intent":
				wrapped.beforeMark = func(ctx context.Context) error {
					current, err := base.GetProduct(ctx, existing.UID)
					if err != nil {
						return err
					}
					expected := current.ResourceVersion
					datastore.AdvanceProductSystemVersion(current)
					switch scenario {
					case "spec":
						current.Spec = json.RawMessage(`{"title":"Competing"}`)
					case "labels":
						current.Labels = map[string]string{"owner": "competing"}
					case "provenance":
						current.SourcePath = "products/competing.md"
					case "deletion-intent":
						current.Status, err = admission.WithDeletionIntent(current.Status, &admission.InfrastructureDeletionIntent{
							UID: current.UID, RepositoryID: current.RepositoryID, Path: current.SourcePath,
							Ref: current.GitRef, ExpectedCommit: current.GitCommitSHA, Actor: "bob",
						}, current.Generation, time.Now().UTC())
						if err != nil {
							return err
						}
					case "malformed-deletion-intent":
						current.Status = json.RawMessage(`{"deletionIntent":42}`)
					}
					return base.UpdateProduct(ctx, current, expected)
				}
			case "replacement":
				wrapped.beforeMark = func(ctx context.Context) error {
					if err := base.DeleteProductWithResourceVersion(ctx, existing.UID, existing.ResourceVersion); err != nil {
						return err
					}
					replacement := *existing
					replacement.UID = "00000000-0000-0000-0000-000000000004"
					return base.CreateProduct(ctx, &replacement)
				}
			case "blocked":
				wrapped.blocked = true
			case "storage-error":
				wrapped.markError = storageError
			case "reload-error":
				wrapped.markError, wrapped.reloadError = datastore.ErrConflict, storageError
			case "exhausted":
				wrapped.markError = datastore.ErrConflict
			case "canceled":
				cancel()
			}
			err = server.markProductDeletion(ctx, existing)
			current, lookupErr := base.GetProductByName(t.Context(), product.Namespace, product.Name)
			require.NoError(t, lookupErr)
			switch scenario {
			case "status", "peer-termination":
				require.NoError(t, err)
				require.NotNil(t, current.DeletionTimestamp)
				require.Contains(t, current.Finalizers, "gitstore.dev/foreground-deletion")
				require.EqualValues(t, 1, current.Generation)
				require.Equal(t, existing.GitCommitSHA, current.GitCommitSHA)
				require.LessOrEqual(t, wrapped.marks, 2)
			case "spec", "labels", "provenance", "deletion-intent":
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.Nil(t, current.DeletionTimestamp)
				require.Equal(t, 1, wrapped.marks)
			case "replacement":
				require.ErrorIs(t, err, datastore.ErrNotFound)
				require.NotEqual(t, existing.UID, current.UID)
				require.Nil(t, current.DeletionTimestamp)
			case "policy-reload":
				require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err))
				require.Equal(t, 1, wrapped.marks)
				require.Nil(t, current.DeletionTimestamp)
			case "malformed-deletion-intent":
				require.ErrorContains(t, err, "read Product deletion intent")
				require.Equal(t, 1, wrapped.marks)
				require.Nil(t, current.DeletionTimestamp)
			case "blocked", "blocked-after-conflict":
				require.ErrorContains(t, err, "blocking ProductVariants")
				require.Nil(t, current.DeletionTimestamp)
				require.LessOrEqual(t, wrapped.marks, 1)
			case "storage-error", "reload-error":
				require.ErrorIs(t, err, storageError)
				require.Nil(t, current.DeletionTimestamp)
				require.Equal(t, 1, wrapped.marks)
			case "exhausted":
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.Equal(t, maxRepositoryAdmissionUpdateAttempts, wrapped.marks)
				require.Equal(t, maxRepositoryAdmissionUpdateAttempts-1, wrapped.reloads)
				require.Nil(t, current.DeletionTimestamp)
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, wrapped.marks)
				require.Nil(t, current.DeletionTimestamp)
			}
		})
	}
}

type categoryDeletionInterleavingStore struct {
	datastore.Datastore
	datastore.CategoryTaxonomyDeletionStore
	datastore.OwnerReferenceStore
	beforeMark             func(context.Context) error
	markError, reloadError error
	marks, reloads         int
	blocked                bool
}

func (s *categoryDeletionInterleavingStore) MarkCategoryTaxonomyDeletion(ctx context.Context, namespace, name, version, uid string, at time.Time) (*datastore.CategoryTaxonomy, error) {
	s.marks++
	if s.beforeMark != nil {
		before := s.beforeMark
		s.beforeMark = nil
		if err := before(ctx); err != nil {
			return nil, err
		}
	}
	if s.markError != nil {
		return nil, s.markError
	}
	return s.CategoryTaxonomyDeletionStore.MarkCategoryTaxonomyDeletion(ctx, namespace, name, version, uid, at)
}

func (s *categoryDeletionInterleavingStore) GetCategoryTaxonomy(ctx context.Context, uid string) (*datastore.CategoryTaxonomy, error) {
	s.reloads++
	if s.reloadError != nil {
		return nil, s.reloadError
	}
	return s.Datastore.GetCategoryTaxonomy(ctx, uid)
}

func (s *categoryDeletionInterleavingStore) HasBlockingOwnerDependents(context.Context, datastore.OwnerReferenceScope, string) (bool, error) {
	return s.blocked, nil
}

func TestCategoryDeletionRebasesOnlyControllerChanges(t *testing.T) {
	for _, scenario := range []string{"status", "peer-termination", "spec", "labels", "annotations", "body", "parent", "provenance", "commit", "revision", "generation", "creation-metadata", "replacement", "blocked", "blocked-after-conflict", "policy-reload", "deletion-intent", "malformed-deletion-intent", "storage-error", "reload-error", "exhausted", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			base, err := memdb.New()
			require.NoError(t, err)
			server := newDecisionServer(t, base)
			server.registry = auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
			category := &datastore.CategoryTaxonomy{
				UID: "00000000-0000-0000-0000-000000000003", Namespace: "gitstore", Name: "electronics",
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "CategoryTaxonomy", Generation: 1, ResourceVersion: "1",
				RepositoryID: "00000000-0000-0000-0000-000000000001", SourcePath: "categories/electronics.md",
				GitRef: "refs/heads/main", GitCommitSHA: strings.Repeat("a", 40), Spec: json.RawMessage(`{"title":"Original"}`),
			}
			require.NoError(t, base.CreateCategoryTaxonomy(t.Context(), category))
			existing, err := base.GetCategoryTaxonomy(t.Context(), category.UID)
			require.NoError(t, err)
			wrapped := &categoryDeletionInterleavingStore{
				Datastore: base, CategoryTaxonomyDeletionStore: base.(datastore.CategoryTaxonomyDeletionStore),
				OwnerReferenceStore: base.(datastore.OwnerReferenceStore),
			}
			server.store = wrapped
			ctx, cancel := context.WithCancel(auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"}))
			defer cancel()
			storageError := errors.New("category deletion storage unavailable")
			switch scenario {
			case "status", "blocked-after-conflict", "policy-reload":
				wrapped.beforeMark = func(ctx context.Context) error {
					_, err := base.UpdateCategoryTaxonomyStatus(ctx, category.Namespace, category.Name, datastore.CategoryTaxonomyStatusPatch{
						ResourceVersion: existing.ResourceVersion,
						Conditions:      []catalog.Condition{{Type: catalog.ConditionReady, Status: "True"}},
					})
					wrapped.blocked = scenario == "blocked-after-conflict"
					if scenario == "policy-reload" {
						server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
					}
					return err
				}
			case "peer-termination":
				wrapped.beforeMark = func(ctx context.Context) error {
					_, err := wrapped.CategoryTaxonomyDeletionStore.MarkCategoryTaxonomyDeletion(ctx, existing.Namespace, existing.Name, existing.ResourceVersion, existing.UID, time.Now().UTC())
					return err
				}
			case "spec", "labels", "annotations", "body", "parent", "provenance", "commit", "revision", "generation", "creation-metadata", "deletion-intent", "malformed-deletion-intent":
				wrapped.beforeMark = func(ctx context.Context) error {
					current, err := base.GetCategoryTaxonomy(ctx, existing.UID)
					if err != nil {
						return err
					}
					expected := current.ResourceVersion
					datastore.AdvanceCategoryTaxonomySystemVersion(current)
					switch scenario {
					case "spec":
						current.Spec = json.RawMessage(`{"title":"Competing"}`)
					case "labels":
						current.Labels = map[string]string{"owner": "competing"}
					case "annotations":
						current.Annotations = map[string]string{"owner": "competing"}
					case "body":
						current.Body = "Competing body"
					case "parent":
						current.ParentName = "competing"
					case "provenance":
						current.SourcePath = "categories/competing.md"
					case "commit":
						current.GitCommitSHA = strings.Repeat("b", 40)
					case "revision":
						current.Revision = "competing"
					case "generation":
						current.Generation++
					case "creation-metadata":
						current.CreationActor = "bob"
					case "deletion-intent":
						current.Status, err = admission.WithDeletionIntent(current.Status, &admission.InfrastructureDeletionIntent{
							UID: current.UID, RepositoryID: current.RepositoryID, Path: current.SourcePath,
							Ref: current.GitRef, ExpectedCommit: current.GitCommitSHA, Actor: "bob",
						}, current.Generation, time.Now().UTC())
						if err != nil {
							return err
						}
					case "malformed-deletion-intent":
						current.Status = json.RawMessage(`{"deletionIntent":42}`)
					}
					return base.UpdateCategoryTaxonomy(ctx, current, expected)
				}
			case "replacement":
				wrapped.beforeMark = func(ctx context.Context) error {
					if err := base.DeleteCategoryTaxonomy(ctx, existing.UID); err != nil {
						return err
					}
					replacement := *existing
					replacement.UID = "00000000-0000-0000-0000-000000000004"
					return base.CreateCategoryTaxonomy(ctx, &replacement)
				}
			case "blocked":
				wrapped.blocked = true
			case "storage-error":
				wrapped.markError = storageError
			case "reload-error":
				wrapped.markError, wrapped.reloadError = datastore.ErrConflict, storageError
			case "exhausted":
				wrapped.markError = datastore.ErrConflict
			case "canceled":
				cancel()
			}
			err = server.deleteResource(ctx, resourceIdentity{Kind: "CategoryTaxonomy", Namespace: existing.Namespace, Name: existing.Name},
				existing.RepositoryID, existing.GitRef, existing.SourcePath, "alice")
			current, lookupErr := base.GetCategoryTaxonomyByName(t.Context(), category.Namespace, category.Name)
			require.NoError(t, lookupErr)
			switch scenario {
			case "status", "peer-termination":
				require.NoError(t, err)
				require.NotNil(t, current.DeletionTimestamp)
				require.Contains(t, current.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer)
				require.EqualValues(t, 1, current.Generation)
				require.Equal(t, existing.GitCommitSHA, current.GitCommitSHA)
				require.LessOrEqual(t, wrapped.marks, 2)
			case "spec", "labels", "annotations", "body", "parent", "provenance", "commit", "revision", "generation", "creation-metadata", "deletion-intent":
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.Nil(t, current.DeletionTimestamp)
				require.Equal(t, 1, wrapped.marks)
			case "replacement":
				require.ErrorIs(t, err, datastore.ErrNotFound)
				require.NotEqual(t, existing.UID, current.UID)
				require.Nil(t, current.DeletionTimestamp)
			case "policy-reload":
				require.Equal(t, codes.PermissionDenied, grpcstatus.Code(err))
				require.Equal(t, 1, wrapped.marks)
				require.Nil(t, current.DeletionTimestamp)
			case "malformed-deletion-intent":
				require.ErrorContains(t, err, "read CategoryTaxonomy deletion intent")
				require.Equal(t, 1, wrapped.marks)
				require.Nil(t, current.DeletionTimestamp)
			case "blocked", "blocked-after-conflict":
				require.ErrorIs(t, err, errCategoryDeletionBlocked)
				require.Nil(t, current.DeletionTimestamp)
				require.LessOrEqual(t, wrapped.marks, 1)
			case "storage-error", "reload-error":
				require.ErrorIs(t, err, storageError)
				require.Nil(t, current.DeletionTimestamp)
				require.Equal(t, 1, wrapped.marks)
			case "exhausted":
				require.ErrorIs(t, err, datastore.ErrConflict)
				require.Equal(t, maxRepositoryAdmissionUpdateAttempts, wrapped.marks)
				require.Equal(t, maxRepositoryAdmissionUpdateAttempts-1, wrapped.reloads)
				require.Nil(t, current.DeletionTimestamp)
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, wrapped.marks)
				require.Nil(t, current.DeletionTimestamp)
			}
		})
	}
}

type failingCategoryLookupStore struct {
	datastore.Datastore
	failure error
}

func (s failingCategoryLookupStore) GetCategoryTaxonomyByName(context.Context, string, string) (*datastore.CategoryTaxonomy, error) {
	return nil, s.failure
}

func TestCategorySiblingOperationLookupPreservesFailure(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, errors.New("category lookup IO failure")} {
		t.Run(failure.Error(), func(t *testing.T) {
			base, err := memdb.New()
			require.NoError(t, err)
			srv := newDecisionServer(t, failingCategoryLookupStore{Datastore: base, failure: failure})
			content := []byte(categoryManifest("electronics", "Electronics"))
			parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader(content))
			require.NoError(t, err)
			entry, accepted, err := newParsedEntry("categories/electronics.md", parsed, body, "gitstore", content)
			require.NoError(t, err)
			require.True(t, accepted)
			decisions, err := srv.admitParsedEntries(t.Context(), []*parsedEntry{entry},
				AdmissionContext{Namespace: "gitstore"}, nil)
			require.ErrorIs(t, err, failure)
			require.Nil(t, decisions, "an unreadable sibling cannot enter admission as a create")
			_, err = base.GetCategoryTaxonomyByName(t.Context(), "gitstore", "electronics")
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func (failingCategoryWriteStore) CreateCategoryTaxonomy(context.Context, *datastore.CategoryTaxonomy) error {
	return errors.New("store unavailable")
}

func categoryManifest(name, title string) string {
	return "---\n" +
		"apiVersion: catalog.gitstore.dev/v1beta1\n" +
		"kind: CategoryTaxonomy\n" +
		"metadata:\n" +
		"  name: " + name + "\n" +
		"  namespace: gitstore\n" +
		"spec:\n" +
		"  title: " + title + "\n" +
		"---\n"
}

func newDecisionServer(t *testing.T, store datastore.Datastore) *Server {
	t.Helper()
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{UID: "00000000-0000-0000-0000-000000000002", Name: "gitstore"}))
	require.NoError(t, store.CreateRepository(t.Context(), &datastore.Repository{
		UID: "00000000-0000-0000-0000-000000000001", Namespace: "gitstore", Name: "catalog",
	}))
	srv, err := NewServer(ServerDeps{
		Store:                   store,
		Logger:                  zap.NewNop(),
		ExtraValidatingPolicies: []admission.ValidatingAdmissionPolicy{denyCategoryTitlePolicy{}},
	})
	require.NoError(t, err)
	return srv
}

func admitCategoryForTest(t *testing.T, srv *Server, content string) admission.EntryDecision {
	t.Helper()
	ctx := context.Background()
	parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	category := parsed.CategoryTaxonomy
	existing, lookupErr := srv.store.GetCategoryTaxonomyByName(ctx, "gitstore", category.Metadata.Name)
	var raw any
	op := admission.OperationCreate
	if lookupErr == nil {
		raw, op = existing, admission.OperationUpdate
	}
	admCtx := AdmissionContext{
		RepositoryID: "00000000-0000-0000-0000-000000000001",
		Namespace:    "gitstore",
		ActorSubject: "alice",
		CommitSHA:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
		Revision:     "main@sha1:" + strings.Repeat("a", 40),
		Now:          time.Now().UTC(),
	}
	return srv.admitCategoryTaxonomyWithContext(ctx, category, body, admCtx, "categories/"+category.Metadata.Name+".md", op, raw, map[string]string{}, nil)
}

func TestAdmitCategoryTaxonomyReturnsEntryDecision(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		store, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, store)
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics"))
		assert.Equal(t, admission.EntryAccepted, decision.Outcome)
		assert.Equal(t, "CategoryTaxonomy", decision.Kind)
		assert.Equal(t, "gitstore", decision.Namespace)
		assert.Equal(t, "electronics", decision.Name)
		assert.Equal(t, "categories/electronics.md", decision.Path)
	})

	t.Run("no-op on identical re-admission", func(t *testing.T) {
		store, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, store)
		require.Equal(t, admission.EntryAccepted, admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics")).Outcome)
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics"))
		assert.Equal(t, admission.EntryNoOp, decision.Outcome)
	})

	t.Run("denied with diagnostics", func(t *testing.T) {
		store, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, store)
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Denied"))
		assert.Equal(t, admission.EntryDenied, decision.Outcome)
		require.Len(t, decision.Diagnostics, 1)
		assert.Equal(t, "POLICY_DENIED", decision.Diagnostics[0].Reason)
		assert.Equal(t, "title Denied is not allowed", decision.Diagnostics[0].Message)
		assert.Equal(t, "categories/electronics.md", decision.Diagnostics[0].File)
		assert.Equal(t, "spec.title", decision.Diagnostics[0].Field)
		assert.Equal(t, admission.LevelFailure, decision.Diagnostics[0].Level)
		_, err = store.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
		assert.ErrorIs(t, err, datastore.ErrNotFound, "a denied create leaves no record")
	})

	t.Run("failed when the store write fails", func(t *testing.T) {
		base, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, failingCategoryWriteStore{Datastore: base})
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics"))
		assert.Equal(t, admission.EntryFailed, decision.Outcome)
		assert.ErrorContains(t, decision.Err, "store unavailable")
	})
}
