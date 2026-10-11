// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func reflectNil(value any) bool {
	return value == nil || (reflect.ValueOf(value).Kind() == reflect.Pointer && reflect.ValueOf(value).IsNil())
}

// Removing an explicit owner annotation restores the immutable creation actor;
// that is still an ownership transfer, not an ordinary metadata-only update.
func resourceCreationActor(resource any) string {
	switch r := resource.(type) {
	case *datastore.Namespace:
		return r.CreationActor
	case *datastore.Repository:
		return r.CreationActor
	case *datastore.Product:
		return r.CreationActor
	case *datastore.CategoryTaxonomy:
		return r.CreationActor
	case *datastore.File:
		return r.CreationActor
	case *datastore.Collection:
		return r.CreationActor
	case *datastore.ProductVariant:
		return r.CreationActor
	default:
		return ""
	}
}

func (s *Server) authorizeOperation(ctx context.Context, identity resourceIdentity, operation admission.Operation, proposed *parsedEntry, existing any) error {
	principal := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(principal) || principal.AuthMethod == "none" || principal.AuthMethod == "serviceaccount-assertion" {
		return status.Error(codes.Unauthenticated, "verified resource actor required")
	}
	if s.registry == nil || s.registry.AuthZ() == nil {
		return status.Error(codes.Unavailable, "resource authorizer unavailable")
	}
	kind := identity.Kind
	if kind != "" {
		kind = strings.ToLower(kind[:1]) + kind[1:]
	}
	verb := strings.ToLower(string(operation))
	if operation == namespaceConvergenceOperation {
		verb = "update"
	}
	action := kind + "." + verb
	definition, found := auth.LookupAction(action)
	if !found || definition.Reserved {
		return status.Error(codes.PermissionDenied, "unsupported resource operation")
	}
	resource := auth.ResourceContext{Kind: definition.Kind, Name: identity.Name, Attrs: map[string]any{}}
	if definition.Scope == "namespace" {
		resource.Attrs["namespace"] = identity.Namespace
	}
	if owner, ok := existing.(interface{ EffectiveOwnerSub() string }); ok && !reflectNil(existing) {
		resource.OwnerSub = owner.EffectiveOwnerSub()
	}
	if operation == admission.OperationCreate && identity.Namespace != "" {
		ns, err := s.store.GetNamespaceByName(ctx, identity.Namespace)
		if err != nil {
			return status.Error(codes.Unavailable, "namespace authorization facts unavailable")
		}
		resource.OwnerSub = ns.EffectiveOwnerSub()
	}
	if ns, ok := existing.(*datastore.Namespace); ok && ns != nil {
		switch ns.Tier {
		case datastore.NamespaceTierUser:
			resource.Attrs["tier"] = "USER"
		case datastore.NamespaceTierOrganization:
			resource.Attrs["tier"] = "ORGANIZATION"
		}
	}
	if proposed != nil && proposed.parsed.Namespace != nil {
		resource.Attrs["tier"] = string(proposed.parsed.Namespace.Spec.Tier)
	}
	check := func(action string) error {
		decision, err := s.registry.AuthZ().Authorize(ctx, principal, action, resource)
		if err != nil {
			return status.Error(codes.Unavailable, "resource authorization failed")
		}
		if decision.Outcome != auth.OutcomeAllow {
			return status.Error(codes.PermissionDenied, "resource operation forbidden")
		}
		return nil
	}
	if proposed != nil && operation != admission.OperationCreate && !reflectNil(existing) {
		cmp, _ := comparableForParsed(proposed.parsed, proposed.body, identity.Namespace)
		target := cmp.Annotations[datastore.OwnerAnnotationKey]
		if target == "" {
			target = resourceCreationActor(existing)
		}
		if target != resource.OwnerSub {
			ownerKind, name := datastore.DecodeOwnerSubject(target)
			resource.Attrs["targetOwnerRef"] = map[string]any{"kind": string(ownerKind), "name": name}
			if err := check(kind + ".transfer"); err != nil {
				return err
			}
			if ownerOnlyChange(proposed, existing, identity.Namespace) {
				return nil
			}
		}
	}
	return check(action)
}

// Owner transfer is an independently grantable operation. Compare only
// author-writable fields; controller-owned status/provenance are not permissions.
// Any other desired-state change still needs the ordinary update grant.
func ownerOnlyChange(proposed *parsedEntry, existing any, namespace string) bool {
	desired, ok := comparableForParsed(proposed.parsed, proposed.body, namespace)
	if !ok {
		return false
	}
	stored := comparableResource{}
	var path string
	fill := func(apiVersion, kind, ns, name string, labels, annotations map[string]string, spec json.RawMessage, body, sourcePath string) {
		stored = comparableResource{APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name, Labels: labels, Annotations: annotations, Spec: spec, Body: body}
		path = sourcePath
	}
	switch r := existing.(type) {
	case *datastore.Namespace:
		fill(r.APIVersion, r.Kind, "", r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	case *datastore.Repository:
		fill(r.APIVersion, r.Kind, r.Namespace, r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	case *datastore.Product:
		fill(r.APIVersion, r.Kind, r.Namespace, r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	case *datastore.CategoryTaxonomy:
		fill(r.APIVersion, r.Kind, r.Namespace, r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	case *datastore.File:
		fill(r.APIVersion, r.Kind, r.Namespace, r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	case *datastore.Collection:
		fill(r.APIVersion, r.Kind, r.Namespace, r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	case *datastore.ProductVariant:
		fill(r.APIVersion, r.Kind, r.Namespace, r.Name, r.Labels, r.Annotations, r.Spec, r.Body, r.SourcePath)
	default:
		return false
	}
	if path != proposed.path {
		return false
	}
	desired.Annotations, stored.Annotations = maps.Clone(desired.Annotations), maps.Clone(stored.Annotations)
	delete(desired.Annotations, datastore.OwnerAnnotationKey)
	delete(stored.Annotations, datastore.OwnerAnnotationKey)
	// Decode both JSON representations so object key order is insignificant.
	left, err := json.Marshal(desired)
	if err != nil {
		return false
	}
	right, err := json.Marshal(stored)
	if err != nil {
		return false
	}
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func (s *Server) authorizeOperations(ctx context.Context, operations []resourceAdmissionOperation) error {
	// A personal namespace identity cannot be moved by spelling a rename as
	// deletion plus a new declaration (possibly at another path or tier).
	// Separate deletion and unrelated creation into distinct pushes.
	removedUser, createdNamespace := false, false
	for _, op := range operations {
		if op.identity.Kind != "Namespace" {
			continue
		}
		createdNamespace = createdNamespace || op.operation == admission.OperationCreate
		if op.operation == admission.OperationDelete && op.oldEntry != nil && op.oldEntry.parsed.Namespace != nil && op.oldEntry.parsed.Namespace.Spec.Tier == "USER" {
			removedUser = true
		}
	}
	if removedUser && createdNamespace {
		return status.Error(codes.PermissionDenied, "USER namespace rename is forbidden; delete and unrelated creation require separate pushes")
	}
	for _, op := range operations {
		existing, err := s.lookupResourceByIdentity(ctx, op.identity)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return status.Error(codes.Unavailable, "resource authorization facts unavailable")
		}
		if err := s.authorizeOperation(ctx, op.identity, op.operation, op.newEntry, existing); err != nil {
			return err
		}
	}
	return nil
}

type resourceIdentity struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

func (i resourceIdentity) key() string {
	return strings.Join([]string{i.APIVersion, i.Kind, i.Namespace, i.Name}, "\x00")
}

type parsedEntry struct {
	path        string
	parsed      *validate.ParsedResource
	body        []byte
	identity    resourceIdentity
	contentHash string
}

type resourceAdmissionOperation struct {
	operation      admission.Operation
	identity       resourceIdentity
	oldEntry       *parsedEntry
	newEntry       *parsedEntry
	pathChanged    bool
	contentChanged bool
}

const namespaceConvergenceOperation admission.Operation = "CONVERGE"

func namespaceConvergenceOperations(
	operations []resourceAdmissionOperation,
	requestedEntries []*parsedEntry,
) []resourceAdmissionOperation {
	requestedByPath := make(map[string]*parsedEntry, len(requestedEntries))
	for _, entry := range requestedEntries {
		if entry != nil {
			requestedByPath[entry.path] = entry
		}
	}
	converged := make([]resourceAdmissionOperation, 0, len(operations))
	for _, operation := range operations {
		if operation.newEntry == nil ||
			operation.newEntry.parsed == nil ||
			operation.newEntry.parsed.Namespace == nil {
			continue
		}
		requested := requestedByPath[operation.newEntry.path]
		if requested == nil ||
			requested.identity.key() != operation.newEntry.identity.key() ||
			requested.contentHash != operation.newEntry.contentHash {
			continue
		}
		operation.operation = namespaceConvergenceOperation
		converged = append(converged, operation)
	}
	return converged
}

func namespaceConvergencePaths(
	currentEntries []*parsedEntry,
	requestedEntries []*parsedEntry,
) []string {
	currentByPath := make(map[string]*parsedEntry, len(currentEntries))
	for _, entry := range currentEntries {
		if entry != nil {
			currentByPath[entry.path] = entry
		}
	}
	paths := make([]string, 0, len(requestedEntries))
	for _, requested := range requestedEntries {
		if requested == nil ||
			requested.parsed == nil ||
			requested.parsed.Namespace == nil {
			continue
		}
		current := currentByPath[requested.path]
		if current == nil ||
			current.identity.key() != requested.identity.key() ||
			current.contentHash != requested.contentHash {
			continue
		}
		paths = append(paths, requested.path)
	}
	sort.Strings(paths)
	return paths
}

type comparableResource struct {
	APIVersion  string            `json:"apiVersion"`
	Kind        string            `json:"kind"`
	Namespace   string            `json:"namespace"`
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Spec        any               `json:"spec,omitempty"`
	Body        string            `json:"body,omitempty"`
}

func newParsedEntry(path string, parsed *validate.ParsedResource, body []byte, defaultNamespace string) (*parsedEntry, bool, error) {
	cmp, ok := comparableForParsed(parsed, body, defaultNamespace)
	if !ok {
		return nil, false, nil
	}
	identity := resourceIdentity{
		APIVersion: cmp.APIVersion,
		Kind:       cmp.Kind,
		Namespace:  cmp.Namespace,
		Name:       cmp.Name,
	}
	contentHash, err := hashJSON(cmp)
	if err != nil {
		return nil, false, err
	}
	return &parsedEntry{
		path:        path,
		parsed:      parsed,
		body:        body,
		identity:    identity,
		contentHash: contentHash,
	}, true, nil
}

func comparableForParsed(parsed *validate.ParsedResource, body []byte, defaultNamespace string) (comparableResource, bool) {
	if parsed == nil {
		return comparableResource{}, false
	}
	switch parsed.Kind {
	case "Product":
		r := parsed.Product
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "CategoryTaxonomy":
		r := parsed.CategoryTaxonomy
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "Collection":
		r := parsed.Collection
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "ProductVariant":
		r := parsed.ProductVariant
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "Namespace":
		r := parsed.Namespace
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, ""), true
	case "File":
		r := parsed.File
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	case "Repository":
		r := parsed.Repository
		if r == nil {
			return comparableResource{}, false
		}
		return comparableFromMeta(r.APIVersion, r.Kind, r.Metadata, r.Spec, body, defaultNamespace), true
	default:
		return comparableResource{}, false
	}
}

func comparableFromMeta(apiVersion, kind string, meta catalog.ObjectMeta, spec any, body []byte, defaultNamespace string) comparableResource {
	namespace := meta.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}
	return comparableResource{
		APIVersion:  apiVersion,
		Kind:        kind,
		Namespace:   namespace,
		Name:        meta.Name,
		Labels:      meta.Labels,
		Annotations: meta.Annotations,
		Spec:        spec,
		Body:        string(body),
	}
}

func hashJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func deriveResourceAdmissionOperations(oldEntries, newEntries []*parsedEntry, changedPaths []string) []resourceAdmissionOperation {
	oldByID := make(map[string]*parsedEntry, len(oldEntries))
	newByID := make(map[string]*parsedEntry, len(newEntries))
	for _, e := range oldEntries {
		oldByID[e.identity.key()] = e
	}
	for _, e := range newEntries {
		newByID[e.identity.key()] = e
	}

	changedPathSet := make(map[string]struct{}, len(changedPaths))
	for _, path := range changedPaths {
		if path != "" {
			changedPathSet[path] = struct{}{}
		}
	}
	pathFilterEnabled := len(changedPathSet) > 0
	pathSelected := func(oldEntry, newEntry *parsedEntry) bool {
		if !pathFilterEnabled {
			return true
		}
		if oldEntry != nil {
			if _, ok := changedPathSet[oldEntry.path]; ok {
				return true
			}
		}
		if newEntry != nil {
			if _, ok := changedPathSet[newEntry.path]; ok {
				return true
			}
		}
		return false
	}

	var ops []resourceAdmissionOperation
	for key, oldEntry := range oldByID {
		newEntry := newByID[key]
		if newEntry == nil {
			if pathSelected(oldEntry, nil) {
				ops = append(ops, resourceAdmissionOperation{
					operation: admission.OperationDelete,
					identity:  oldEntry.identity,
					oldEntry:  oldEntry,
				})
			}
			continue
		}
		pathChanged := oldEntry.path != newEntry.path
		contentChanged := oldEntry.contentHash != newEntry.contentHash
		if (pathChanged || contentChanged) && pathSelected(oldEntry, newEntry) {
			ops = append(ops, resourceAdmissionOperation{
				operation:      admission.OperationUpdate,
				identity:       newEntry.identity,
				oldEntry:       oldEntry,
				newEntry:       newEntry,
				pathChanged:    pathChanged,
				contentChanged: contentChanged,
			})
		}
	}
	for key, newEntry := range newByID {
		if _, ok := oldByID[key]; ok {
			continue
		}
		if pathSelected(nil, newEntry) {
			ops = append(ops, resourceAdmissionOperation{
				operation: admission.OperationCreate,
				identity:  newEntry.identity,
				newEntry:  newEntry,
			})
		}
	}

	sort.SliceStable(ops, func(i, j int) bool {
		pi := operationSortPriority(ops[i].operation)
		pj := operationSortPriority(ops[j].operation)
		if pi != pj {
			return pi < pj
		}
		return fmt.Sprintf("%s/%s/%s", ops[i].identity.Kind, ops[i].identity.Namespace, ops[i].identity.Name) <
			fmt.Sprintf("%s/%s/%s", ops[j].identity.Kind, ops[j].identity.Namespace, ops[j].identity.Name)
	})
	return ops
}

func operationSortPriority(op admission.Operation) int {
	switch op {
	case admission.OperationDelete:
		return 0
	case admission.OperationCreate:
		return 1
	case admission.OperationUpdate:
		return 2
	default:
		return 3
	}
}

// repositoryImmutablePathChanges detects a Repository identity change in the
// same manifest path. Pre-receive rejects this via
// validateImmutableResourceChanges; this second guard makes post-receive safe
// when a caller invokes AdmitResources directly or a rolling deployment has a
// stale pre-receive hook. Without it, identity-based operation derivation
// would incorrectly turn a rename/transfer into a create.
func repositoryImmutablePathChanges(oldEntries, newEntries []*parsedEntry) []string {
	oldByPath := make(map[string]*parsedEntry, len(oldEntries))
	for _, entry := range oldEntries {
		if entry != nil && entry.identity.Kind == "Repository" {
			oldByPath[entry.path] = entry
		}
	}
	var changed []string
	for _, entry := range newEntries {
		if entry == nil || entry.identity.Kind != "Repository" {
			continue
		}
		old, ok := oldByPath[entry.path]
		if ok && (old.identity.Name != entry.identity.Name || old.identity.Namespace != entry.identity.Namespace) {
			changed = append(changed, entry.path)
		}
	}
	sort.Strings(changed)
	return changed
}
