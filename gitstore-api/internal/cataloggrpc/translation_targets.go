// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type translationManifestOwner string

func (owner translationManifestOwner) EffectiveOwnerSub() string { return string(owner) }

func (s *Server) lookupTranslationManifestTarget(ctx context.Context, source *catalog.TranslationResource, namespace string, operations map[string]resourceAdmissionOperation) (*validate.TranslationTargetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || namespace == "" || (source.Metadata.Namespace != "" && source.Metadata.Namespace != namespace) ||
		source.Spec.TargetRef.Name == "" || (source.Spec.TargetRef.Namespace != "" && source.Spec.TargetRef.Namespace != namespace) {
		return nil, status.Error(codes.InvalidArgument, "Translation target must belong to its effective namespace")
	}
	ref := source.Spec.TargetRef
	if _, err := translation.DescriptorFor(ref.APIVersion, ref.Kind); err != nil {
		return nil, err
	}
	principal := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(principal) || principal.AuthMethod == "none" || principal.AuthMethod == "serviceaccount-assertion" {
		return nil, status.Error(codes.Unauthenticated, "verified resource actor required")
	}
	if s.registry == nil || s.registry.AuthZ() == nil {
		return nil, status.Error(codes.Unavailable, "resource authorizer unavailable")
	}
	identity := resourceIdentity{APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: namespace, Name: ref.Name}
	namespaces, ok := s.store.(datastore.TranslationTargetNamespaceMetadataStore)
	if !ok {
		return nil, status.Error(codes.Unavailable, "namespace authorization facts unavailable")
	}
	scope, proofErr := namespaces.ReadTranslationTargetNamespaceMetadata(ctx, namespace)
	owner := ""
	var facts *datastore.TranslationTargetMetadata
	var key datastore.TranslationTargetKey
	if proofErr == nil {
		if scope == nil || scope.Name != namespace || !scope.Active {
			proofErr = datastore.ErrRepairRequired
		} else if id, err := uuid.Parse(scope.UID); err != nil || id.String() != scope.UID {
			proofErr = datastore.ErrRepairRequired
		} else {
			owner = scope.OwnerSub
			key = datastore.TranslationTargetKey{NamespaceUID: scope.UID, APIVersion: ref.APIVersion, Kind: ref.Kind, Name: ref.Name}
			facts, proofErr = s.translationManifestTargetFacts(ctx, key)
			if facts != nil && facts.Evidence.Namespace != namespace {
				proofErr = datastore.ErrRepairRequired
			}
			if facts != nil && proofErr == nil {
				owner = facts.OwnerSub
			}
		}
	}
	if err := s.authorizeOperation(ctx, identity, admission.Operation("READ"), nil, translationManifestOwner(owner)); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if proofErr != nil {
		return nil, status.Error(codes.Unavailable, "target authorization facts unavailable")
	}
	sameEvidence := func(left, right catalog.TranslationTargetEvidence) bool {
		if ref.ResourceVersion == "" {
			left.ResourceVersion, right.ResourceVersion = "", ""
		}
		return left == right
	}
	finish := func() error {
		after, err := namespaces.ReadTranslationTargetNamespaceMetadata(ctx, namespace)
		if err != nil || after == nil || *scope != *after {
			return status.Error(codes.Unavailable, "target namespace changed")
		}
		if facts != nil {
			current, err := s.translationManifestTargetFacts(ctx, key)
			if err != nil || current == nil || !sameEvidence(current.Evidence, facts.Evidence) ||
				current.OwnerSub != facts.OwnerSub || !current.CreationTimestamp.Equal(facts.CreationTimestamp) {
				return status.Error(codes.Unavailable, "target observation changed")
			}
		}
		return ctx.Err()
	}
	operation, proposed := operations[identity.key()]
	if proposed && (operation.identity != identity || (operation.newEntry != nil && operation.newEntry.identity != identity)) {
		return nil, status.Error(codes.Unavailable, "effective target identity unavailable")
	}
	if proposed && operation.newEntry == nil {
		if operation.operation != admission.OperationDelete {
			return nil, status.Error(codes.Unavailable, "effective target content unavailable")
		}
		return nil, finish()
	}
	var snapshot *validate.TranslationTargetSnapshot
	var spec json.RawMessage
	var schema reflect.Type
	if proposed {
		comparable, ok := comparableForParsed(operation.newEntry.parsed, operation.newEntry.body, namespace)
		if !ok || comparable.APIVersion != identity.APIVersion || comparable.Kind != identity.Kind ||
			comparable.Namespace != namespace || comparable.Name != identity.Name {
			return nil, status.Error(codes.Unavailable, "effective target content unavailable")
		}
		snapshot = &validate.TranslationTargetSnapshot{APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: namespace, Name: ref.Name}
		if facts != nil && (operation.operation == admission.OperationUpdate || operation.operation == admission.Operation("READ")) {
			snapshot.UID = facts.Evidence.UID
		}
		var err error
		spec, err = json.Marshal(comparable.Spec)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "effective target content unavailable")
		}
		if facts != nil && operation.operation == admission.Operation("READ") {
			digest, err := translation.TargetContentDigest(ctx, ref.APIVersion, ref.Kind, spec, comparable.Body)
			if err != nil {
				return nil, err
			}
			if digest == facts.Evidence.ContentDigest {
				snapshot.ResourceVersion = facts.Evidence.ResourceVersion
			}
		}
		schema = reflect.TypeOf(comparable.Spec)
	} else {
		if facts == nil {
			return nil, finish()
		}
		existing, err := s.translationManifestTargetResource(ctx, ref.Kind, facts.Evidence.UID)
		if err != nil || reflectNil(existing) {
			return nil, status.Error(codes.Unavailable, "target content unavailable")
		}
		var evidence *catalog.TranslationTargetEvidence
		var terminating bool
		snapshot, spec, evidence, terminating, err = translationManifestStoredTarget(ctx, existing)
		if err != nil {
			return nil, err
		}
		if snapshot == nil || evidence == nil || !sameEvidence(*evidence, facts.Evidence) || terminating {
			return nil, status.Error(codes.Unavailable, "target content changed")
		}
		switch ref.Kind {
		case "Product":
			schema = reflect.TypeFor[catalog.ProductSpec]()
		case "ProductVariant":
			schema = reflect.TypeFor[catalog.ProductVariantSpec]()
		case "CategoryTaxonomy":
			schema = reflect.TypeFor[catalog.CategoryTaxonomySpec]()
		case "Collection":
			schema = reflect.TypeFor[catalog.CollectionSpec]()
		case "File":
			schema = reflect.TypeFor[catalog.FileSpec]()
		}
	}
	fields, err := validate.TranslationTargetFields(ctx, spec, schema)
	if err != nil {
		return nil, err
	}
	snapshot.Spec = fields
	if err := finish(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *Server) translationManifestTargetFacts(ctx context.Context, key datastore.TranslationTargetKey) (*datastore.TranslationTargetMetadata, error) {
	fences, ok := s.store.(interface {
		GetTranslationTargetFence(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error)
	})
	metadata, hasMetadata := s.store.(datastore.TranslationTargetMetadataStore)
	if !ok || !hasMetadata {
		return nil, datastore.ErrProjectionNotReady
	}
	head, err := fences.GetTranslationTargetFence(ctx, key)
	if errors.Is(err, datastore.ErrNotFound) || (err == nil && head != nil &&
		head.Phase == datastore.TranslationTargetStable && head.Evidence == nil) {
		coverage, ok := s.store.(datastore.TranslationTargetCoverageReader)
		if !ok {
			return nil, datastore.ErrProjectionNotReady
		}
		return nil, datastore.ReadTranslationTargetAbsence(ctx, key, coverage, fences.GetTranslationTargetFence)
	}
	if err != nil {
		return nil, err
	}
	first := true
	var observed *datastore.TranslationTargetMetadata
	evidence, err := datastore.ReadTranslationTargetEvidence(ctx, key,
		func(ctx context.Context, key datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
			if first {
				first = false
				return head, nil
			}
			return fences.GetTranslationTargetFence(ctx, key)
		},
		func(ctx context.Context, head *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
			var err error
			observed, err = metadata.ReadTranslationTargetMetadata(ctx, head)
			return observed, err
		})
	if err != nil || observed == nil || evidence == nil {
		if err == nil {
			err = datastore.ErrRepairRequired
		}
		return nil, err
	}
	result := *observed
	result.Evidence = *evidence
	return &result, nil
}

func (s *Server) translationManifestTargetResource(ctx context.Context, kind, uid string) (any, error) {
	switch kind {
	case "Product":
		return s.store.GetProduct(ctx, uid)
	case "ProductVariant":
		return s.store.GetProductVariant(ctx, uid)
	case "CategoryTaxonomy":
		return s.store.GetCategoryTaxonomy(ctx, uid)
	case "Collection":
		return s.store.GetCollection(ctx, uid)
	case "File":
		return s.store.GetFile(ctx, uid)
	default:
		return nil, datastore.ErrInvalidArgument
	}
}

func translationManifestStoredTarget(ctx context.Context, raw any) (*validate.TranslationTargetSnapshot, json.RawMessage, *catalog.TranslationTargetEvidence, bool, error) {
	if reflectNil(raw) {
		return nil, nil, nil, false, nil
	}
	snapshot := &validate.TranslationTargetSnapshot{}
	var spec json.RawMessage
	var deletion *time.Time
	var body string
	evidence := &catalog.TranslationTargetEvidence{}
	set := func(apiVersion, kind, namespace, name, uid, version string, generation int64, revision string, content json.RawMessage, text string, terminating *time.Time) {
		snapshot.APIVersion, snapshot.Kind, snapshot.Namespace, snapshot.Name = apiVersion, kind, namespace, name
		snapshot.UID, snapshot.ResourceVersion, spec, deletion = uid, version, content, terminating
		body = text
		*evidence = catalog.TranslationTargetEvidence{
			APIVersion: apiVersion, Kind: kind, Namespace: namespace, Name: name, UID: uid,
			ResourceVersion: version, Generation: generation, Revision: revision,
		}
	}
	switch source := raw.(type) {
	case *datastore.Product:
		set(source.APIVersion, source.Kind, source.Namespace, source.Name, source.UID, source.ResourceVersion, source.Generation, source.Revision, source.Spec, source.Body, source.DeletionTimestamp)
	case *datastore.ProductVariant:
		set(source.APIVersion, source.Kind, source.Namespace, source.Name, source.UID, source.ResourceVersion, source.Generation, source.Revision, source.Spec, source.Body, source.DeletionTimestamp)
	case *datastore.CategoryTaxonomy:
		set(source.APIVersion, source.Kind, source.Namespace, source.Name, source.UID, source.ResourceVersion, source.Generation, source.Revision, source.Spec, source.Body, source.DeletionTimestamp)
	case *datastore.Collection:
		set(source.APIVersion, source.Kind, source.Namespace, source.Name, source.UID, source.ResourceVersion, source.Generation, source.Revision, source.Spec, source.Body, source.DeletionTimestamp)
	case *datastore.File:
		set(source.APIVersion, source.Kind, source.Namespace, source.Name, source.UID, source.ResourceVersion, source.Generation, source.Revision, source.Spec, source.Body, source.DeletionTimestamp)
	default:
		return nil, nil, nil, false, status.Error(codes.Unavailable, "target content unavailable")
	}
	digest, err := translation.TargetContentDigest(ctx, evidence.APIVersion, evidence.Kind, spec, body)
	if err != nil {
		return nil, nil, nil, false, err
	}
	evidence.ContentDigest = digest
	return snapshot, spec, evidence, deletion != nil, nil
}
