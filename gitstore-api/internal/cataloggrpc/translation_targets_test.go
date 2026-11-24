// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type translationFailingAuthorizer struct{ auth.AuthZProvider }

func (translationFailingAuthorizer) Authorize(context.Context, *auth.Principal, string, auth.ResourceContext) (auth.Decision, error) {
	return auth.Decision{}, errors.New("private provider failure")
}

type translationCandidateStore struct {
	datastore.Datastore
	product        *datastore.Product
	err            error
	reads          int
	namespaceReads int
	onNamespace    func(*datastore.TranslationTargetNamespaceMetadata, int)
	onHead         func(*datastore.TranslationTargetFence)
	onContent      func(*datastore.Product)
	coverageErr    error
}

func (s *translationCandidateStore) GetProductByName(_ context.Context, namespace, name string) (*datastore.Product, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	if s.product == nil || s.product.Namespace != namespace || s.product.Name != name {
		return nil, datastore.ErrNotFound
	}
	return s.product, nil
}

func (s *translationCandidateStore) GetNamespaceByName(_ context.Context, name string) (*datastore.Namespace, error) {
	return &datastore.Namespace{Name: name, CreationActor: "owner"}, nil
}

func (s *translationCandidateStore) ReadTranslationTargetNamespaceMetadata(_ context.Context, name string) (*datastore.TranslationTargetNamespaceMetadata, error) {
	s.namespaceReads++
	result := &datastore.TranslationTargetNamespaceMetadata{
		UID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String(), Name: name, OwnerSub: "owner", ResourceVersion: "1", Active: true,
	}
	if s.onNamespace != nil {
		s.onNamespace(result, s.namespaceReads)
	}
	return result, nil
}

func (s *translationCandidateStore) GetTranslationTargetAuditProgress(_ context.Context, namespace string) (datastore.TranslationAuditProgress, error) {
	return datastore.TranslationAuditProgress{
		NamespaceUID: namespace, RunID: namespace, ResourceVersion: "1", Phase: "Verified",
		DescriptorDigest: translation.CapabilityDigest(), TargetMutationEpoch: namespace,
	}, nil
}

func (s *translationCandidateStore) ReadTranslationTargetCoverageEpoch(_ context.Context, namespace string) (string, error) {
	return namespace, errors.Join(s.err, s.coverageErr)
}

func (s *translationCandidateStore) GetTranslationTargetFence(ctx context.Context, key datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
	if s.err != nil {
		return nil, s.err
	}
	source := s.product
	if source == nil || key.Name != source.Name || key.NamespaceUID != uuid.NewSHA1(uuid.NameSpaceOID, []byte(source.Namespace)).String() {
		return nil, datastore.ErrNotFound
	}
	digest, err := translation.TargetContentDigest(ctx, source.APIVersion, source.Kind, source.Spec, source.Body)
	if err != nil {
		return nil, err
	}
	generation := max(source.Generation, 1)
	created := source.CreationTimestamp
	if created.IsZero() {
		created = time.Unix(1, 0)
	}
	result := &datastore.TranslationTargetFence{
		Target: key, Token: uuid.NewSHA1(uuid.NameSpaceOID, []byte(source.UID+"/"+source.ResourceVersion+"/"+digest)).String(),
		Phase: datastore.TranslationTargetStable, CreationTimestamp: created,
		Evidence: &catalog.TranslationTargetEvidence{
			APIVersion: source.APIVersion, Kind: source.Kind, Namespace: source.Namespace, Name: source.Name,
			UID: source.UID, ResourceVersion: source.ResourceVersion, Generation: generation, Revision: source.Revision, ContentDigest: digest,
		},
	}
	if s.onHead != nil {
		s.onHead(result)
	}
	return result, nil
}

func (s *translationCandidateStore) ReadTranslationTargetMetadata(ctx context.Context, head *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
	current, err := s.GetTranslationTargetFence(ctx, head.Target)
	if err != nil {
		return nil, err
	}
	return &datastore.TranslationTargetMetadata{
		Evidence: *current.Evidence, CreationTimestamp: current.CreationTimestamp, OwnerSub: s.product.EffectiveOwnerSub(),
		Active: s.product.DeletionTimestamp == nil,
		SourceIdentity: datastore.TranslationTargetSourceIdentity{
			NamespaceUID: head.Target.NamespaceUID, WriteToken: current.Token, ContentDigest: current.Evidence.ContentDigest,
		},
	}, nil
}

func (s *translationCandidateStore) GetProduct(_ context.Context, uid string) (*datastore.Product, error) {
	s.reads++
	if s.err != nil {
		return nil, s.err
	}
	if s.product == nil || s.product.UID != uid {
		return nil, datastore.ErrNotFound
	}
	result := *s.product
	result.Generation = max(result.Generation, 1)
	if s.onContent != nil {
		s.onContent(&result)
	}
	return &result, nil
}

func translationCandidateSource() *catalog.TranslationResource {
	source := &catalog.TranslationResource{Metadata: catalog.ObjectMeta{Namespace: "gitstore"}}
	source.Spec.Locale = "de"
	source.Spec.TargetRef.APIVersion = "catalog.gitstore.dev/v1beta1"
	source.Spec.TargetRef.Kind = "Product"
	source.Spec.TargetRef.Name = "widget"
	source.Spec.Patches = []catalog.TranslationPatch{{FieldPath: "spec.title", Translation: "Titel"}}
	return source
}

func TestTranslationEffectiveCandidateOverridesStoredContent(t *testing.T) {
	for _, mode := range []string{"stored", "create", "update", "delete", "rename-away", "rename-to", "unrelated-namespace"} {
		t.Run(mode, func(t *testing.T) {
			stored := &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "widget",
				UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "7", CreationActor: "owner",
				Spec: []byte(`{"title":"Stored"}`),
			}
			store := &translationCandidateStore{product: stored}
			recorder := testutil.NewAllowAllAuthZ()
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, recorder, nil)}
			proposed := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Proposed", "unchanged target body"))
			other := mustParsedEntry(t, "products/other.md", productDoc("other", "Other", ""))
			operations := map[string]resourceAdmissionOperation{}
			key := proposed.identity.key()
			switch mode {
			case "create":
				store.product = nil
				operations[key] = resourceAdmissionOperation{operation: admission.OperationCreate, identity: proposed.identity, newEntry: proposed}
			case "update":
				operations[key] = resourceAdmissionOperation{operation: admission.OperationUpdate, identity: proposed.identity, newEntry: proposed}
			case "delete":
				operations[key] = resourceAdmissionOperation{operation: admission.OperationDelete, identity: proposed.identity}
			case "rename-away":
				operations[key] = resourceAdmissionOperation{operation: admission.OperationDelete, identity: proposed.identity}
				operations[other.identity.key()] = resourceAdmissionOperation{operation: admission.OperationCreate, identity: other.identity, newEntry: other}
			case "rename-to":
				store.product = nil
				operations[other.identity.key()] = resourceAdmissionOperation{operation: admission.OperationDelete, identity: other.identity}
				operations[key] = resourceAdmissionOperation{operation: admission.OperationCreate, identity: proposed.identity, newEntry: proposed}
			case "unrelated-namespace":
				foreign := proposed.identity
				foreign.Namespace = "other"
				operations[foreign.key()] = resourceAdmissionOperation{operation: admission.OperationDelete, identity: foreign}
			}
			before := slices.Clone(stored.Spec)
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			target, err := server.lookupTranslationManifestTarget(ctx, translationCandidateSource(), "gitstore", operations)
			require.NoError(t, err)
			require.Len(t, recorder.Calls, 1)
			require.Equal(t, "product.read", recorder.Calls[0].Action)
			require.Equal(t, "product", recorder.Calls[0].Resource.Kind)
			require.Equal(t, "gitstore", recorder.Calls[0].Resource.Attrs["namespace"])
			require.Equal(t, "owner", recorder.Calls[0].Resource.OwnerSub)
			switch mode {
			case "delete", "rename-away":
				require.Nil(t, target, "candidate deletion must not fall back to a live stored target")
			case "create", "update", "rename-to":
				require.Equal(t, "Proposed", target.Spec["title"])
				require.Empty(t, target.ResourceVersion, "future resourceVersions cannot be invented from the stored version")
				if mode == "update" {
					require.Equal(t, stored.UID, target.UID)
				} else {
					require.Empty(t, target.UID, "new incarnations have not allocated identity")
				}
				require.Zero(t, store.reads, "candidate content must not fetch stored target content")
			default:
				require.Equal(t, "Stored", target.Spec["title"])
				require.Equal(t, "7", target.ResourceVersion)
			}
			require.Equal(t, before, stored.Spec)
			require.Equal(t, "Proposed", proposed.parsed.Product.Spec.Title)
		})
	}
}

func TestTranslationEffectiveCandidateIsIndependentOfFileOrder(t *testing.T) {
	old := mustParsedEntry(t, "widget.md", productDoc("widget", "Stored", ""))
	proposed := mustParsedEntry(t, "widget.md", productDoc("widget", "Proposed", ""))
	renamed := mustParsedEntry(t, "widget.md", productDoc("renamed", "Renamed", ""))
	other := mustParsedEntry(t, "other.md", productDoc("other", "Other", ""))
	for _, mode := range []string{"create", "update", "delete", "rename"} {
		t.Run(mode, func(t *testing.T) {
			oldEntries, newEntries := []*parsedEntry{old, other}, []*parsedEntry{proposed, other}
			store := &translationCandidateStore{product: &datastore.Product{
				APIVersion: old.identity.APIVersion, Kind: "Product", Namespace: "gitstore", Name: "widget",
				UID: uuid.NewString(), ResourceVersion: "1", Spec: []byte(`{"Title":"Stored"}`),
			}}
			switch mode {
			case "create":
				oldEntries, store.product = []*parsedEntry{other}, nil
			case "delete":
				newEntries = []*parsedEntry{other}
			case "rename":
				newEntries = []*parsedEntry{renamed, other}
			}
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			for range 2 {
				operations := map[string]resourceAdmissionOperation{}
				for _, operation := range deriveResourceAdmissionOperations(oldEntries, newEntries, nil) {
					operations[operation.identity.key()] = operation
				}
				target, err := server.lookupTranslationManifestTarget(ctx, translationCandidateSource(), "gitstore", operations)
				require.NoError(t, err)
				if mode == "delete" || mode == "rename" {
					require.Nil(t, target)
				} else {
					require.Equal(t, "Proposed", target.Spec["title"])
				}
				slices.Reverse(oldEntries)
				slices.Reverse(newEntries)
			}
		})
	}
}

func TestTranslationEffectiveCandidateLookupFailsClosed(t *testing.T) {
	for _, mode := range []string{"missing", "storage-error", "denied", "provider-error", "provider-missing", "anonymous", "cross-namespace", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			store := &translationCandidateStore{}
			provider := testutil.NewAllowAllAuthZ()
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, provider, nil)}
			source := translationCandidateSource()
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			switch mode {
			case "storage-error":
				store.err = errors.New("private storage failure")
			case "denied":
				server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
			case "provider-error":
				server.registry = auth.NewProviderRegistry(nil, translationFailingAuthorizer{AuthZProvider: provider}, nil)
			case "provider-missing":
				server.registry = nil
			case "anonymous":
				ctx = t.Context()
			case "cross-namespace":
				source.Spec.TargetRef.Namespace = "other"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			target, err := server.lookupTranslationManifestTarget(ctx, source, "gitstore", nil)
			require.Nil(t, target)
			if mode == "missing" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			switch mode {
			case "storage-error", "provider-missing", "provider-error":
				require.Equal(t, codes.Unavailable, status.Code(err))
				require.NotContains(t, err.Error(), "private storage")
				require.NotContains(t, err.Error(), "private provider")
			case "denied":
				require.Equal(t, codes.PermissionDenied, status.Code(err))
				require.Zero(t, store.reads, "target content cannot be fetched before its read permission is granted")
			case "anonymous":
				require.Equal(t, codes.Unauthenticated, status.Code(err))
				require.Zero(t, store.reads)
			case "cross-namespace":
				require.Zero(t, store.reads)
			case "canceled":
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, store.reads)
			}
		})
	}
}

func TestTranslationEffectiveCandidateDeliveredKindsUseAuthoredNames(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace string
		Targets   map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for kind, document := range corpus.Targets {
		t.Run(kind, func(t *testing.T) {
			backend, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, backend.Close()) })
			now := time.Now().UTC().Truncate(time.Millisecond)
			require.NoError(t, backend.CreateNamespace(t.Context(), &datastore.Namespace{
				ID: uuid.NewString(), Name: corpus.Namespace, Tier: datastore.NamespaceTierOrganization,
				CreationTimestamp: now, CreationActor: "owner", UpdateTimestamp: now, UpdateActor: "owner",
			}))
			parsed, body, err := validate.NewParser().ParseResource(bytes.NewBufferString(document))
			require.NoError(t, err)
			entry, supported, err := newParsedEntry("target.md", parsed, body, corpus.Namespace)
			require.NoError(t, err)
			require.True(t, supported)
			comparable, ok := comparableForParsed(parsed, body, corpus.Namespace)
			require.True(t, ok)
			spec, err := json.Marshal(comparable.Spec)
			require.NoError(t, err)
			uid, namespace, name := uuid.NewString(), entry.identity.Namespace, entry.identity.Name
			switch kind {
			case "Product":
				err = backend.CreateProduct(t.Context(), &datastore.Product{APIVersion: comparable.APIVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid, Generation: 1, ResourceVersion: "1", CreationTimestamp: now, Spec: spec})
			case "ProductVariant":
				err = backend.CreateProductVariant(t.Context(), &datastore.ProductVariant{APIVersion: comparable.APIVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid, SKU: parsed.ProductVariant.Spec.SKU, ProductRefName: parsed.ProductVariant.Spec.ProductRef.Name, Generation: 1, ResourceVersion: "1", CreationTimestamp: now, Spec: spec})
			case "CategoryTaxonomy":
				err = backend.CreateCategoryTaxonomy(t.Context(), &datastore.CategoryTaxonomy{APIVersion: comparable.APIVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid, Generation: 1, ResourceVersion: "1", CreationTimestamp: now, Spec: spec})
			case "Collection":
				err = backend.CreateCollection(t.Context(), &datastore.Collection{APIVersion: comparable.APIVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid, Generation: 1, ResourceVersion: "1", CreationTimestamp: now, Spec: spec})
			case "File":
				err = backend.CreateFile(t.Context(), &datastore.File{APIVersion: comparable.APIVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid, Generation: 1, ResourceVersion: "1", CreationTimestamp: now, Spec: spec})
			}
			require.NoError(t, err)
			recorder := testutil.NewAllowAllAuthZ()
			server := &Server{store: backend, registry: auth.NewProviderRegistry(nil, recorder, nil)}
			source := translationCandidateSource()
			source.Metadata.Namespace = namespace
			source.Spec.TargetRef.APIVersion, source.Spec.TargetRef.Kind, source.Spec.TargetRef.Name = comparable.APIVersion, kind, name
			if kind == "File" {
				source.Spec.Patches = nil
			}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			for _, proposed := range []bool{false, true} {
				var operations map[string]resourceAdmissionOperation
				if proposed {
					operations = map[string]resourceAdmissionOperation{entry.identity.key(): {
						operation: admission.OperationUpdate, identity: entry.identity, newEntry: entry,
					}}
				}
				target, err := server.lookupTranslationManifestTarget(ctx, source, namespace, operations)
				require.NoError(t, err)
				result, err := validate.ValidateTranslationSnapshot(ctx, source, []byte("Translated body"), namespace, target)
				require.NoError(t, err)
				require.True(t, result.TargetResolved)
				require.Equal(t, uid, target.UID)
				require.Contains(t, result.ValidatedPaths, "{body}")
				if kind != "File" {
					require.Contains(t, target.Spec, "title")
					require.NotContains(t, target.Spec, "Title")
					require.Contains(t, result.ValidatedPaths, "spec.title")
				}
			}
		})
	}
}

func TestTranslationEffectiveCandidateKeepsPinsTruthful(t *testing.T) {
	proposed := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Proposed", ""))
	stored := &datastore.Product{
		APIVersion: proposed.identity.APIVersion, Kind: "Product", Namespace: "gitstore", Name: "widget",
		UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "7", Spec: []byte(`{"title":"Stored"}`),
	}
	server := &Server{store: &translationCandidateStore{product: stored},
		registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
	source := translationCandidateSource()
	source.Spec.TargetRef.UID, source.Spec.TargetRef.ResourceVersion = stored.UID, "7"
	target, err := server.lookupTranslationManifestTarget(ctx, source, "gitstore", nil)
	require.NoError(t, err)
	result, err := validate.ValidateTranslationSnapshot(ctx, source, nil, "gitstore", target)
	require.NoError(t, err)
	require.True(t, result.TargetResolved)
	source.Spec.TargetRef.ResourceVersion = "8"
	result, err = validate.ValidateTranslationSnapshot(ctx, source, nil, "gitstore", target)
	require.Error(t, err)
	require.Nil(t, result)
	source.Spec.TargetRef.ResourceVersion = "7"
	target, err = server.lookupTranslationManifestTarget(ctx, source, "gitstore", map[string]resourceAdmissionOperation{
		proposed.identity.key(): {operation: admission.OperationUpdate, identity: proposed.identity, newEntry: proposed},
	})
	require.NoError(t, err)
	result, err = validate.ValidateTranslationSnapshot(ctx, source, nil, "gitstore", target)
	require.NoError(t, err)
	require.False(t, result.TargetResolved)
	require.Equal(t, "PinUnavailable", result.Reason)
	source.Spec.TargetRef.UID = "00000000-0000-4000-8000-000000000002"
	result, err = validate.ValidateTranslationSnapshot(ctx, source, nil, "gitstore", target)
	require.Error(t, err)
	require.Nil(t, result)
	source.Spec.TargetRef.UID, source.Spec.TargetRef.ResourceVersion = stored.UID, ""
	target, err = server.lookupTranslationManifestTarget(ctx, source, "gitstore", map[string]resourceAdmissionOperation{
		proposed.identity.key(): {operation: admission.OperationCreate, identity: proposed.identity, newEntry: proposed},
	})
	require.NoError(t, err)
	result, err = validate.ValidateTranslationSnapshot(ctx, source, nil, "gitstore", target)
	require.NoError(t, err)
	require.False(t, result.TargetResolved, "a candidate create cannot borrow a stored incarnation's UID")
	require.Equal(t, "PinUnavailable", result.Reason)
}

func TestTranslationEffectiveCandidateUnchangedVersionRequiresExactMaterial(t *testing.T) {
	entry := mustParsedEntry(t, "products/widget.md", productDoc("widget", "Proposed", "Target body"))
	comparable, ok := comparableForParsed(entry.parsed, entry.body, "gitstore")
	require.True(t, ok)
	spec, err := json.Marshal(comparable.Spec)
	require.NoError(t, err)
	for _, drift := range []bool{false, true} {
		t.Run(map[bool]string{false: "proved-current", true: "stale-projection"}[drift], func(t *testing.T) {
			stored := &datastore.Product{
				APIVersion: entry.identity.APIVersion, Kind: "Product", Namespace: "gitstore", Name: "widget",
				UID: uuid.NewString(), ResourceVersion: "7", Spec: spec, Body: comparable.Body,
			}
			if drift {
				stored.Body = "Previous body"
			}
			store := &translationCandidateStore{product: stored}
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
			source := translationCandidateSource()
			source.Spec.TargetRef.UID, source.Spec.TargetRef.ResourceVersion = stored.UID, stored.ResourceVersion
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			target, err := server.lookupTranslationManifestTarget(ctx, source, "gitstore", map[string]resourceAdmissionOperation{
				entry.identity.key(): {operation: admission.Operation("READ"), identity: entry.identity, newEntry: entry},
			})
			require.NoError(t, err)
			result, err := validate.ValidateTranslationSnapshot(ctx, source, nil, "gitstore", target)
			require.NoError(t, err)
			require.Equal(t, !drift, result.TargetResolved)
			if drift {
				require.Equal(t, "PinUnavailable", result.Reason)
			} else {
				require.Equal(t, stored.ResourceVersion, target.ResourceVersion)
			}
			require.Zero(t, store.reads)
		})
	}
}

func TestTranslationEffectiveCandidateRejectsUnprovedAndRacingObservations(t *testing.T) {
	for _, mode := range []string{"missing-coverage", "pending-head", "stale-content", "late-target", "status-only", "status-pin", "namespace-replacement", "denied-unproved"} {
		t.Run(mode, func(t *testing.T) {
			store := &translationCandidateStore{product: &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "widget",
				UID: uuid.NewString(), ResourceVersion: "7", Spec: []byte(`{"title":"Stored"}`),
			}}
			server := &Server{store: store, registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
			switch mode {
			case "missing-coverage", "denied-unproved":
				store.product, store.coverageErr = nil, datastore.ErrRepairRequired
				if mode == "denied-unproved" {
					server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
				}
			case "pending-head":
				store.onHead = func(head *datastore.TranslationTargetFence) {
					next, err := datastore.PlanTranslationTargetWrite(head, head.Target, datastore.TranslationTargetWriteIntent{
						ID: uuid.NewString(), PreviousUID: head.Evidence.UID, NextUID: head.Evidence.UID,
						ExpectedResourceVersion: "7", SourceDigest: "next", CreationTimestamp: head.CreationTimestamp,
					}, head.Token)
					require.NoError(t, err)
					*head = *next
				}
			case "stale-content":
				store.onContent = func(result *datastore.Product) { result.Body = "stale full-body read" }
			case "late-target":
				store.onContent = func(*datastore.Product) {
					store.product.Body, store.product.ResourceVersion = "later material", "8"
				}
			case "status-only", "status-pin":
				store.onContent = func(result *datastore.Product) {
					store.product.ResourceVersion, result.ResourceVersion = "8", "8"
				}
			case "namespace-replacement":
				store.onNamespace = func(namespace *datastore.TranslationTargetNamespaceMetadata, read int) {
					if read == 2 {
						namespace.UID = uuid.NewString()
					}
				}
			}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			source := translationCandidateSource()
			if mode == "status-pin" {
				source.Spec.TargetRef.ResourceVersion = "7"
			}
			target, err := server.lookupTranslationManifestTarget(ctx, source, "gitstore", nil)
			if mode == "status-only" {
				require.NoError(t, err)
				require.NotNil(t, target, "status-only churn must not invalidate unpinned material")
				require.Equal(t, "8", target.ResourceVersion)
				return
			}
			code := codes.Unavailable
			if mode == "denied-unproved" {
				code = codes.PermissionDenied
			}
			require.Equal(t, code, status.Code(err))
			require.Nil(t, target)
			if mode == "missing-coverage" || mode == "pending-head" || mode == "denied-unproved" {
				require.Zero(t, store.reads)
			}
		})
	}
}

func TestTranslationEffectiveCandidateRejectsAmbiguousOrUnboundedSpecs(t *testing.T) {
	for _, mode := range []string{"ambiguous-fields", "too-deep", "too-many-nodes"} {
		t.Run(mode, func(t *testing.T) {
			spec := []byte(`{"Title":"Original","title":"Different"}`)
			code := codes.Unavailable
			switch mode {
			case "too-deep":
				spec = []byte(`{"Title":"Original","ignored":` + strings.Repeat("[", translation.MaxTargetDepth+2) + "null" + strings.Repeat("]", translation.MaxTargetDepth+2) + "}")
				code = codes.ResourceExhausted
			case "too-many-nodes":
				spec = []byte(`{"Title":"Original","ignored":[` + strings.Repeat("null,", translation.MaxTargetNodes) + "null]}")
				code = codes.ResourceExhausted
			}
			server := &Server{store: &translationCandidateStore{product: &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "widget",
				UID: uuid.NewString(), ResourceVersion: "1", Spec: spec,
			}}, registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			target, err := server.lookupTranslationManifestTarget(ctx, translationCandidateSource(), "gitstore", nil)
			require.Equal(t, code, status.Code(err))
			require.Nil(t, target)
		})
	}
}

func TestTranslationEffectiveCandidateConnectedValidation(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Targets map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for _, mode := range []string{"source-first", "target-first", "invalid-candidate-field", "unchanged-candidate-target", "unchanged-translation", "single-manifest", "denied-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newTranslationCallbackFixture(t)
			f.server.store = f.store.Datastore
			require.NoError(t, f.store.CreateProduct(f.ctx, &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "laptop",
				UID: uuid.NewString(), ResourceVersion: "1", Generation: 1, CreationTimestamp: f.admCtx.Now,
				Spec: []byte(`{"Title":"Old stored projection","Options":[]}`),
			}))
			before, err := f.store.GetProductByName(f.ctx, "gitstore", "laptop")
			require.NoError(t, err)
			target := strings.ReplaceAll(corpus.Targets["Product"], "acme-store", "gitstore")
			oldTarget := strings.Replace(target, "title:", "title: Previous #", 1)
			if mode == "unchanged-candidate-target" {
				oldTarget = target
			}
			if mode == "invalid-candidate-field" || mode == "unchanged-translation" {
				target = strings.Replace(target, "name: color", "name: shade", 1)
			}
			sourceBlob := &catalogv1.ResourceBlob{Path: f.entry.path, Content: slices.Clone(f.entry.parsed.RawSource)}
			targetBlob := &catalogv1.ResourceBlob{Path: "products/laptop.md", Content: []byte(target)}
			tree := &catalogv1.ResourceValidationTree{
				OldBlobs:      []*catalogv1.ResourceBlob{{Path: targetBlob.Path, Content: []byte(oldTarget)}},
				ProposedBlobs: []*catalogv1.ResourceBlob{sourceBlob, targetBlob},
			}
			if mode == "target-first" {
				slices.Reverse(tree.ProposedBlobs)
			}
			if mode == "unchanged-translation" {
				tree.OldBlobs = append(tree.OldBlobs, sourceBlob)
			}
			if mode == "denied-source" {
				f.server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
			}
			if mode == "single-manifest" {
				diagnostics, err := f.server.ValidateManifest(f.ctx, admission.ManifestValidationRequest{
					RepositoryID: f.admCtx.RepositoryID, Path: sourceBlob.Path, NewContent: sourceBlob.Content,
				})
				require.NoError(t, err)
				require.NotEmpty(t, diagnostics, "single-manifest preflight must check the stored target")
				require.Equal(t, "spec.patches[1].fieldPath", diagnostics[0].Field)
			} else {
				response, err := f.server.validateResources(f.ctx, &catalogv1.ValidateResourcesRequest{
					RepositoryId: f.admCtx.RepositoryID, Trees: []*catalogv1.ResourceValidationTree{tree},
				})
				if mode == "denied-source" {
					require.Equal(t, codes.PermissionDenied, status.Code(err))
					require.Nil(t, response)
				} else if mode == "invalid-candidate-field" {
					require.NoError(t, err)
					require.False(t, response.Accepted)
					require.NotEmpty(t, response.Errors)
					require.Equal(t, sourceBlob.Path, response.Errors[0].FilePath)
					require.Equal(t, "spec.patches[1].fieldPath", response.Errors[0].Field)
				} else {
					require.NoError(t, err)
					require.True(t, response.Accepted, "%v", response.Errors)
				}
			}
			after, err := f.store.GetProductByName(f.ctx, "gitstore", "laptop")
			require.NoError(t, err)
			require.Equal(t, before, after, "preflight must not publish target or source content")
			_, err = f.store.GetTranslationByName(f.ctx, "gitstore", f.entry.identity.Name)
			require.ErrorIs(t, err, datastore.ErrNotFound)
		})
	}
}

func TestTranslationEffectiveCandidateManifestIsAllOrNothing(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	base := corpus.Valid["product-combined"]
	for _, mode := range []string{"valid", "default-namespace", "mixed-fields", "null-field", "missing-field", "identity-case", "denied", "candidate-deleted", "candidate-over-stale-content", "stale-identity"} {
		t.Run(mode, func(t *testing.T) {
			stored := &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
				UID: uuid.NewString(), ResourceVersion: "1",
				Spec: []byte(`{"Title":"Original","Options":[{"Name":"color","Title":"Color"}]}`),
			}

			store := &translationCandidateStore{product: stored}
			server := &Server{store: store, parser: validate.NewParser(),
				registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)}
			content := []byte(base)
			var operations map[string]resourceAdmissionOperation
			switch mode {
			case "default-namespace":
				content = []byte(strings.Replace(base, "  namespace: acme-store", "", 1))
			case "mixed-fields":
				stored.Spec = []byte(`{"Title":"Original","Options":[]}`)
			case "null-field":
				stored.Spec = []byte(`{"Title":"Original","Options":[{"Name":"color","Title":null}]}`)
			case "missing-field":
				stored.Spec = []byte(`{"Title":"Original","Options":[{"Name":"color"}]}`)
			case "identity-case":
				stored.Spec = []byte(`{"Title":"Original","Options":[{"Name":"Color","Title":"Color"}]}`)
			case "denied":
				stored.Spec = []byte("private malformed target")
				server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
			case "candidate-deleted":
				identity := resourceIdentity{APIVersion: stored.APIVersion, Kind: stored.Kind, Namespace: stored.Namespace, Name: stored.Name}
				operations = map[string]resourceAdmissionOperation{identity.key(): {operation: admission.OperationDelete, identity: identity}}
			case "candidate-over-stale-content":
				targetDocument := strings.Replace(productDoc("laptop", "Proposed", ""), "namespace: gitstore", "namespace: acme-store", 1)
				targetDocument = strings.Replace(targetDocument, "  title: Proposed", "  title: Proposed\n  options:\n    - name: color\n      title: Color\n      values: [red]", 1)
				proposed := mustParsedEntry(t, "laptop.md", targetDocument)
				stored.Spec = []byte("private malformed target")
				operations = map[string]resourceAdmissionOperation{proposed.identity.key(): {
					operation: admission.OperationUpdate, identity: proposed.identity, newEntry: proposed,
				}}
			case "stale-identity":
				stored.APIVersion = "unrelated/v1"
			}
			sourceBefore, targetBefore := slices.Clone(content), slices.Clone(stored.Spec)
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			parsed, body, result, err := server.validateTranslationManifestCandidate(ctx, content, "acme-store", operations)
			switch mode {
			case "valid", "default-namespace", "candidate-over-stale-content":
				require.NoError(t, err)
				require.True(t, result.TargetResolved)
				require.Equal(t, []string{"spec.title", "spec.options{color}.title", "{body}"}, result.ValidatedPaths)
				_, expected, ok := strings.Cut(base, "\r\n---\r\n")
				require.True(t, ok)
				require.Equal(t, []byte(expected), body)
				require.NotNil(t, parsed)
			case "candidate-deleted":
				require.NoError(t, err)
				require.False(t, result.TargetResolved)
				require.Equal(t, "Missing", result.Reason)
			default:
				require.Error(t, err)
				require.Nil(t, parsed)
				require.Nil(t, body)
				require.Nil(t, result, "no partial field success may escape a failed authorized manifest")
				require.NotContains(t, err.Error(), "private")
				require.NotContains(t, err.Error(), "Tragbarer Computer")
			}
			require.Equal(t, sourceBefore, content)
			require.Equal(t, targetBefore, stored.Spec)
		})
	}
}
