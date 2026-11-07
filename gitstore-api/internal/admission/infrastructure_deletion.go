// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

const DeletionPending = "DeletionPending"

// InfrastructureDeletionIntent survives a crash between Git commit and admission.
// It is system-owned, bound to an incarnation, and never copied to a manifest.
type InfrastructureDeletionIntent struct {
	UID            string `json:"uid"`
	RepositoryID   string `json:"repositoryID"`
	Path           string `json:"path"`
	Ref            string `json:"ref"`
	ExpectedCommit string `json:"expectedCommit"`
	RemovalCommit  string `json:"removalCommit,omitempty"`
	Actor          string `json:"actor"`
}

func ReadDeletionIntent(raw json.RawMessage) (*InfrastructureDeletionIntent, error) {
	var envelope struct {
		Intent *InfrastructureDeletionIntent `json:"deletionIntent"`
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode deletion intent: %w", err)
	}
	return envelope.Intent, nil
}

func WithDeletionIntent(raw json.RawMessage, intent *InfrastructureDeletionIntent, generation int64, now time.Time) (json.RawMessage, error) {
	fields := map[string]json.RawMessage{}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return nil, err
	}
	fields["deletionIntent"] = encoded
	var conditions []catalog.Condition
	if len(fields["conditions"]) != 0 {
		if err := json.Unmarshal(fields["conditions"], &conditions); err != nil {
			return nil, err
		}
	}
	filtered := conditions[:0]
	for _, condition := range conditions {
		if condition.Type != DeletionPending {
			filtered = append(filtered, condition)
		}
	}
	filtered = append(filtered, catalog.Condition{
		Type: DeletionPending, Status: catalog.ConditionTrue,
		Reason: "ManifestRemovalPending", Message: "An authorized manifest deletion is awaiting admission",
		ObservedGeneration: generation, LastTransitionTime: now,
	})
	fields["conditions"], err = json.Marshal(filtered)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func CheckInfrastructureDeletion(ctx context.Context, store datastore.Datastore, resource any) error {
	switch r := resource.(type) {
	case *datastore.Namespace:
		if r.Name == "gitstore-system" || r.Name == "default" {
			return NewError(CodeFailedPrecondition, "BOOTSTRAP_NAMESPACE", "bootstrap namespace cannot be deleted")
		}
		blocked, err := datastore.NamespaceDeletionBlocked(ctx, store, r)
		if err != nil {
			return err
		}
		if blocked {
			return NewError(CodeFailedPrecondition, "NAMESPACE_NOT_EMPTY", "namespace contains repositories or catalog resources")
		}
	case *datastore.Repository:
		if r.Name == "gitstore-system" {
			return NewError(CodeFailedPrecondition, "SYSTEM_REPOSITORY", "system repository is removed only during Namespace finalization")
		}
		blocked, err := store.HasCatalogResources(ctx, r.UID)
		if err != nil {
			return err
		}
		if blocked {
			return NewError(CodeFailedPrecondition, "CATALOG_RESOURCES_PRESENT", "repository contains catalog resources")
		}
	default:
		return fmt.Errorf("unsupported infrastructure deletion %T", resource)
	}
	return nil
}

// MarkInfrastructureDeletion is shared by pushed and API-authored removals.
func MarkInfrastructureDeletion(ctx context.Context, store datastore.Datastore, resource any, now time.Time, actor string) error {
	if err := CheckInfrastructureDeletion(ctx, store, resource); err != nil {
		return err
	}
	switch r := resource.(type) {
	case *datastore.Namespace:
		if r.DeletionTimestamp != nil {
			return nil
		}
		expected := r.ResourceVersion
		r.DeletionTimestamp, r.UpdateTimestamp, r.UpdateActor = &now, now, actor
		r.Finalizers = appendFinalizer(r.Finalizers, datastore.NamespaceForegroundDeletionFinalizer)
		datastore.AdvanceNamespaceSystemVersion(r)
		return store.MarkNamespaceDeletion(ctx, r, expected)
	case *datastore.Repository:
		if r.DeletionTimestamp != nil {
			return nil
		}
		expected := r.ResourceVersion
		r.DeletionTimestamp, r.UpdateTimestamp, r.UpdateActor = &now, now, actor
		r.Finalizers = appendFinalizer(r.Finalizers, datastore.RepositoryForegroundDeletionFinalizer)
		datastore.AdvanceRepositorySystemVersion(r)
		return store.UpdateRepository(ctx, r, expected)
	}
	return fmt.Errorf("unsupported infrastructure deletion %T", resource)
}

func appendFinalizer(values []string, finalizer string) []string {
	for _, value := range values {
		if value == finalizer {
			return values
		}
	}
	return append(values, finalizer)
}
