// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

var (
	ErrBootstrapNamespace = errors.New("bootstrap namespace is system-managed")
	ErrTierDemotion       = errors.New("namespace tier demotion is not allowed")
)

var bootstrapNames = map[string]struct{}{
	"gitstore-system": {},
	"default":         {},
}

type IDGenerator interface {
	NewID() string
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

	existing, err := store.GetNamespaceByName(ctx, name)
	if errors.Is(err, datastore.ErrNotFound) {
		namespace := &datastore.Namespace{
			ID:                ids.NewID(),
			Name:              name,
			Title:             resource.Spec.Title,
			Tier:              tier,
			Spec:              mustMarshalSpec(resource.Spec),
			CreationTimestamp: now,
			CreationActor:     actor,
			UpdateTimestamp:   now,
			UpdateActor:       actor,
		}
		datastore.NormalizeNamespaceContract(namespace)
		namespace.Status = AdmissionStatus(namespace.Generation, revision, now)
		if err := store.CreateNamespace(ctx, namespace); err != nil {
			return nil, false, fmt.Errorf("namespace admission: create: %w", err)
		}
		return namespace, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("namespace admission: lookup: %w", err)
	}
	if TierRank(tier) < TierRank(existing.Tier) {
		return nil, false, ErrTierDemotion
	}
	if existing.Title == resource.Spec.Title && existing.Tier == tier {
		return existing, false, nil
	}

	expectedResourceVersion := existing.ResourceVersion
	existing.Title = resource.Spec.Title
	existing.Tier = tier
	existing.Spec = mustMarshalSpec(resource.Spec)
	existing.UpdateTimestamp = now
	existing.UpdateActor = actor
	datastore.AdvanceNamespaceSpecVersion(existing)
	existing.Status = AdmissionStatus(existing.Generation, revision, now)
	if err := store.UpdateNamespace(ctx, existing, expectedResourceVersion); err != nil {
		return nil, false, fmt.Errorf("namespace admission: update: %w", err)
	}
	return existing, false, nil
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
