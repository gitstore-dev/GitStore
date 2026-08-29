// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

var (
	ErrBootstrapNamespace     = errors.New("bootstrap namespace is system-managed")
	ErrTierDemotion           = errors.New("namespace tier demotion is not allowed")
	ErrNamespaceTerminating   = errors.New("namespace is terminating")
	ErrNamespaceAlreadyExists = errors.New("namespace already exists")
	ErrNamespaceNotFound      = errors.New("namespace not found")
	ErrAuthoringRefSuperseded = errors.New("namespace authoring ref was superseded")
	ErrAuthoringRefCheck      = errors.New("namespace authoring ref check failed")
)

const AdmissionWriteAttempts = 4

var bootstrapNames = map[string]struct{}{
	"gitstore-system": {},
	"default":         {},
}

type IDGenerator interface {
	NewID() string
}

type PolicyCheck struct {
	Operation        admission.Operation
	Name             string
	Tier             datastore.NamespaceTier
	CapturePreflight bool
}

type Preflight struct {
	Captured        bool
	Identity        string
	ResourceVersion string
	Existing        *datastore.Namespace
}

type PolicyEvaluator interface {
	Evaluate(ctx context.Context, check PolicyCheck) (*Decision, Preflight, error)
}

type datastorePolicyEvaluator struct {
	store datastore.Datastore
}

func NewPolicyEvaluator(store datastore.Datastore) PolicyEvaluator {
	return &datastorePolicyEvaluator{store: store}
}

func (e *datastorePolicyEvaluator) Evaluate(ctx context.Context, check PolicyCheck) (*Decision, Preflight, error) {
	preflight := Preflight{}
	if check.CapturePreflight {
		preflight.Captured = true
	}
	if IsBootstrap(check.Name) {
		return &Decision{
			Phase:   PhasePolicy,
			Reason:  ReasonBootstrapNamespace,
			Field:   "metadata.name",
			Message: "bootstrap namespace is system-managed",
		}, preflight, nil
	}

	existing, err := e.store.GetNamespaceByName(ctx, check.Name)
	if errors.Is(err, datastore.ErrNotFound) {
		if check.Operation == admission.OperationUpdate {
			return &Decision{
				Phase:   PhasePolicy,
				Reason:  ReasonNamespaceNotFound,
				Field:   "metadata.name",
				Message: "namespace not found",
			}, preflight, nil
		}
		return nil, preflight, nil
	}
	if err != nil {
		return nil, Preflight{}, fmt.Errorf("namespace policy lookup: %w", err)
	}
	if check.CapturePreflight {
		preflight = preflightForNamespace(existing)
	}
	if check.Operation == admission.OperationCreate {
		return &Decision{
			Phase:   PhasePolicy,
			Reason:  ReasonNamespaceAlreadyExists,
			Field:   "metadata.name",
			Message: "namespace already exists",
		}, preflight, nil
	}
	if existing.DeletionTimestamp != nil {
		return &Decision{
			Phase:   PhasePolicy,
			Reason:  ReasonNamespaceTerminating,
			Field:   "metadata.name",
			Message: "namespace is terminating",
		}, preflight, nil
	}
	if TierRank(check.Tier) < TierRank(existing.Tier) {
		return &Decision{
			Phase:   PhasePolicy,
			Reason:  ReasonTierDemotion,
			Field:   "spec.tier",
			Message: "namespace tier demotion is not allowed",
		}, preflight, nil
	}
	return nil, preflight, nil
}

func IsBootstrap(name string) bool {
	_, ok := bootstrapNames[name]
	return ok
}

// ApplyManifest creates or updates the Namespace represented by resource.
// The returned bool is true when a new row was created.
func ApplyManifest(
	ctx context.Context,
	store datastore.Datastore,
	ids IDGenerator,
	resource *catalog.NamespaceResource,
	now time.Time,
	revision string,
	actor string,
	operations ...admission.Operation,
) (*datastore.Namespace, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("namespace admission: resource is required")
	}
	name := resource.Metadata.Name
	if IsBootstrap(name) {
		return nil, false, ErrBootstrapNamespace
	}
	_, ok := TierFromManifest(resource.Spec.Tier)
	if !ok {
		return nil, false, fmt.Errorf("namespace admission: unsupported tier %q", resource.Spec.Tier)
	}
	var operation admission.Operation
	if len(operations) > 0 {
		operation = operations[0]
	}

	existing, err := store.GetNamespaceByName(ctx, name)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, false, fmt.Errorf("namespace admission: lookup: %w", err)
	}
	preflight := preflightForNamespace(nil)
	if err == nil {
		preflight = preflightForNamespace(existing)
	}

	return ApplyManifestOrdered(ctx, store, ids, resource, now, revision, actor, ApplyManifestOptions{
		Operation:     operation,
		Preflight:     preflight,
		WriteAttempts: 1,
	})
}

type AuthoringRefCheck func(context.Context) (bool, error)

type ApplyManifestOptions struct {
	Operation         admission.Operation
	Preflight         Preflight
	CheckAuthoringRef AuthoringRefCheck
	WriteAttempts     int
	Body              []byte
	SourcePath        string
	GitCommitSHA      string
	GitRef            string
}

func ApplyManifestOrdered(
	ctx context.Context,
	store datastore.Datastore,
	ids IDGenerator,
	resource *catalog.NamespaceResource,
	now time.Time,
	revision string,
	actor string,
	options ApplyManifestOptions,
) (*datastore.Namespace, bool, error) {
	if resource == nil {
		return nil, false, fmt.Errorf("namespace admission: resource is required")
	}
	name := resource.Metadata.Name
	if IsBootstrap(name) {
		return nil, false, ErrBootstrapNamespace
	}
	tier, ok := TierFromManifest(resource.Spec.Tier)
	if !ok {
		return nil, false, fmt.Errorf("namespace admission: unsupported tier %q", resource.Spec.Tier)
	}
	specJSON := mustMarshalSpec(resource.Spec)
	body := string(options.Body)

	preflight := options.Preflight
	if !preflight.Captured {
		existing, err := store.GetNamespaceByName(ctx, name)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return nil, false, fmt.Errorf("namespace admission: lookup: %w", err)
		}
		preflight.Captured = true
		if err == nil {
			preflight = preflightForNamespace(existing)
		}
	}
	if preflight.Existing != nil {
		if preflight.Identity == "" {
			preflight.Identity = namespaceIdentity(preflight.Existing)
		}
		if preflight.ResourceVersion == "" {
			preflight.ResourceVersion = preflight.Existing.ResourceVersion
		}
	}
	initialIdentity := preflight.Identity
	existing := cloneNamespace(preflight.Existing)
	attempts := options.WriteAttempts
	if attempts < 1 {
		attempts = AdmissionWriteAttempts
	}
	createRetry := false

	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := RecheckAuthoringRef(ctx, options.CheckAuthoringRef); err != nil {
				return nil, false, err
			}
		}
		if initialIdentity != "" && namespaceIdentity(existing) != initialIdentity {
			return nil, false, fmt.Errorf("namespace admission: identity changed: %w", datastore.ErrConflict)
		}
		if options.Operation == admission.OperationUpdate && existing == nil {
			return nil, false, ErrNamespaceNotFound
		}
		if options.Operation == admission.OperationCreate && existing != nil && !createRetry {
			return nil, false, ErrNamespaceAlreadyExists
		}
		if existing != nil && existing.DeletionTimestamp != nil {
			return nil, false, ErrNamespaceTerminating
		}
		if existing != nil && !createRetry && TierRank(tier) < TierRank(existing.Tier) {
			return nil, false, ErrTierDemotion
		}

		created := existing == nil
		candidate := cloneNamespace(existing)
		expectedResourceVersion := ""
		if created {
			candidate = &datastore.Namespace{
				APIVersion:        resource.APIVersion,
				Kind:              resource.Kind,
				ID:                ids.NewID(),
				Name:              name,
				Title:             resource.Spec.Title,
				Tier:              tier,
				Labels:            cloneStringMap(resource.Metadata.Labels),
				Annotations:       cloneStringMap(resource.Metadata.Annotations),
				Spec:              specJSON,
				Body:              body,
				Revision:          revision,
				CreationTimestamp: now,
				CreationActor:     actor,
				UpdateTimestamp:   now,
				UpdateActor:       actor,
				SourcePath:        options.SourcePath,
				GitCommitSHA:      options.GitCommitSHA,
				GitRef:            options.GitRef,
			}
			datastore.NormalizeNamespaceContract(candidate)
			candidate.Status = AdmissionStatus(candidate.Generation, revision, now)
		} else {
			authorChanged := candidate.APIVersion != resource.APIVersion ||
				candidate.Kind != resource.Kind ||
				!stringMapsEqual(candidate.Labels, resource.Metadata.Labels) ||
				!stringMapsEqual(candidate.Annotations, resource.Metadata.Annotations) ||
				!bytes.Equal(candidate.Spec, specJSON) ||
				candidate.Body != body
			systemChanged := candidate.Revision != revision ||
				candidate.SourcePath != options.SourcePath ||
				candidate.GitCommitSHA != options.GitCommitSHA ||
				candidate.GitRef != options.GitRef
			if !authorChanged && !systemChanged {
				return candidate, false, nil
			}
			expectedResourceVersion = candidate.ResourceVersion
			if attempt == 0 && preflight.ResourceVersion != "" {
				expectedResourceVersion = preflight.ResourceVersion
			}
			candidate.APIVersion = resource.APIVersion
			candidate.Kind = resource.Kind
			candidate.Labels = cloneStringMap(resource.Metadata.Labels)
			candidate.Annotations = cloneStringMap(resource.Metadata.Annotations)
			candidate.Title = resource.Spec.Title
			candidate.Tier = tier
			candidate.Spec = append([]byte(nil), specJSON...)
			candidate.Body = body
			candidate.Revision = revision
			candidate.UpdateTimestamp = now
			candidate.UpdateActor = actor
			candidate.SourcePath = options.SourcePath
			candidate.GitCommitSHA = options.GitCommitSHA
			candidate.GitRef = options.GitRef
			if authorChanged {
				datastore.AdvanceNamespaceSpecVersion(candidate)
			} else {
				datastore.AdvanceNamespaceSystemVersion(candidate)
			}
			candidate.Status = AdmissionStatus(candidate.Generation, revision, now)
		}

		if err := RecheckAuthoringRef(ctx, options.CheckAuthoringRef); err != nil {
			return nil, false, err
		}
		var err error
		if created {
			err = store.CreateNamespace(ctx, candidate)
		} else {
			err = store.UpdateNamespace(ctx, candidate, expectedResourceVersion)
		}
		if err == nil {
			return candidate, created, nil
		}
		if !errors.Is(err, datastore.ErrConflict) && !errors.Is(err, datastore.ErrAlreadyExists) {
			if created {
				return nil, false, fmt.Errorf("namespace admission: create: %w", err)
			}
			return nil, false, fmt.Errorf("namespace admission: update: %w", err)
		}
		if attempt+1 >= attempts {
			if errors.Is(err, datastore.ErrAlreadyExists) {
				return nil, false, ErrNamespaceAlreadyExists
			}
			return nil, false, fmt.Errorf("namespace admission: update: %w", err)
		}

		refreshed, lookupErr := store.GetNamespaceByName(ctx, name)
		if errors.Is(lookupErr, datastore.ErrNotFound) {
			if options.Operation == admission.OperationUpdate {
				return nil, false, ErrNamespaceNotFound
			}
			existing = nil
			createRetry = true
			continue
		}
		if lookupErr != nil {
			return nil, false, fmt.Errorf("namespace admission: retry lookup: %w", lookupErr)
		}
		existing = cloneNamespace(refreshed)
		createRetry = options.Operation == admission.OperationCreate && preflight.Existing == nil
	}
	return nil, false, fmt.Errorf("namespace admission: repeated concurrent updates: %w", datastore.ErrConflict)
}

func RecheckAuthoringRef(ctx context.Context, check AuthoringRefCheck) error {
	if check == nil {
		return nil
	}
	current, err := check(ctx)
	if err != nil {
		return err
	}
	if !current {
		return ErrAuthoringRefSuperseded
	}
	return nil
}

func cloneNamespace(namespace *datastore.Namespace) *datastore.Namespace {
	if namespace == nil {
		return nil
	}
	clone := *namespace
	clone.Spec = append([]byte(nil), namespace.Spec...)
	clone.Status = append([]byte(nil), namespace.Status...)
	clone.Labels = cloneStringMap(namespace.Labels)
	clone.Annotations = cloneStringMap(namespace.Annotations)
	clone.OwnerReferences = append([]byte(nil), namespace.OwnerReferences...)
	clone.Finalizers = append([]string(nil), namespace.Finalizers...)
	return &clone
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	clone := make(map[string]string, len(input))
	for key, value := range input {
		clone[key] = value
	}
	return clone
}

func stringMapsEqual(left, right map[string]string) bool {
	return maps.Equal(left, right)
}

func namespaceIdentity(namespace *datastore.Namespace) string {
	if namespace == nil {
		return ""
	}
	datastore.NormalizeNamespaceContract(namespace)
	return namespace.UID
}

func preflightForNamespace(namespace *datastore.Namespace) Preflight {
	existing := cloneNamespace(namespace)
	if existing == nil {
		return Preflight{Captured: true}
	}
	return Preflight{
		Captured:        true,
		Identity:        namespaceIdentity(existing),
		ResourceVersion: existing.ResourceVersion,
		Existing:        existing,
	}
}

func mustMarshalSpec(spec catalog.NamespaceSpec) []byte {
	data, err := json.Marshal(spec)
	if err != nil {
		return nil
	}
	return data
}

func TierFromManifest(tier string) (datastore.NamespaceTier, bool) {
	switch tier {
	case "USER":
		return datastore.NamespaceTierUser, true
	case "ORGANIZATION":
		return datastore.NamespaceTierOrganization, true
	default:
		return "", false
	}
}

func TierRank(tier datastore.NamespaceTier) int {
	switch tier {
	case datastore.NamespaceTierOrganization:
		return 2
	case datastore.NamespaceTierUser:
		return 1
	default:
		return 0
	}
}

func AdmissionStatus(generation int64, revision string, now time.Time) []byte {
	status := catalog.NamespaceStatus{
		ObservedGeneration:  generation,
		LastAppliedRevision: revision,
		Conditions: []catalog.Condition{{
			Type:               catalog.ConditionAdmissionAccepted,
			Status:             catalog.ConditionTrue,
			ObservedGeneration: generation,
			LastTransitionTime: now,
			Reason:             "AdmittedByHookPipeline",
			Message:            "Namespace manifest admitted successfully.",
		}},
	}
	data, err := json.Marshal(status)
	if err != nil {
		return []byte(`{"observedGeneration":0,"conditions":[]}`)
	}
	return data
}
