// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package repository implements reconciliation for admitted Repository resources.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const (
	// SystemRepositoryName is the Namespace bootstrap repository. It is created
	// through the dedicated Namespace bootstrap path and is never reconciled as
	// an author-managed declarative Repository.
	SystemRepositoryName = "gitstore-system"

	// ForegroundDeletionFinalizer protects a terminating Repository until the
	// API has removed its backing storage and atomically completed deletion.
	ForegroundDeletionFinalizer = "gitstore.dev/foreground-deletion"

	conditionAdmissionAccepted  = "AdmissionAccepted"
	conditionStorageProvisioned = "StorageProvisioned"
	conditionReady              = "Ready"

	// ConditionStatus values are GraphQL enum wire values, rather than Go
	// bool strings. StatusPatch.IsNoOp compares these values exactly.
	statusTrue  = "TRUE"
	statusFalse = "FALSE"

	conflictRequeueDelay  = 100 * time.Millisecond
	rateLimitRequeueDelay = time.Second
)

// Repository is the cache entity populated by RepositoryListWatcher.
// UID is retained for cache identity and diagnostics; the controller invokes
// storage provisioning by stable namespace/name through the API.
type Repository struct {
	UID             string
	Namespace       string
	Name            string
	StorageClass    string
	Generation      int64
	ResourceVersion string
	Finalizers      []string
	Status          status.ResourceStatus
}

// ResolvedStorage is the system-computed storage location and class the API
// reports back once a Repository's bare Git storage has been provisioned.
type ResolvedStorage struct {
	StoragePath  string `json:"storagePath"`
	StorageClass string `json:"storageClass"`
}

// StorageClient provisions a Repository's bare Git storage. It must be
// idempotent: runners can replay events and reconciliation is at-least-once.
// The API-backed production implementation is intentionally kept outside the
// reconciler so Git-service credentials remain server-side.
type StorageClient interface {
	EnsureStorage(ctx context.Context, namespace, name string) (ResolvedStorage, error)
}

// CompletionClient performs the final deletion boundary after a Repository is
// terminating. Its API operation removes/validates backing storage, rechecks
// the catalog-resource drain condition, clears the finalizer, and only then
// permits hard deletion. The controller never cascades catalog resources.
type CompletionClient interface {
	CompleteDeletion(ctx context.Context, namespace, name, resourceVersion string) error
}

// Reconciler implements types.Reconciler for Repository resources.
type Reconciler struct {
	cache            cache.CacheAccessor[Repository]
	statusClient     status.StatusClient
	storageClient    StorageClient
	completionClient CompletionClient
}

// NewReconciler returns a Repository reconciler.
func NewReconciler(c cache.CacheAccessor[Repository], statusClient status.StatusClient, storageClient StorageClient, completionClient CompletionClient) *Reconciler {
	return &Reconciler{
		cache:            c,
		statusClient:     statusClient,
		storageClient:    storageClient,
		completionClient: completionClient,
	}
}

// Reconcile provisions admitted repositories and completes foreground deletion.
func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	current, ok := r.cache.Get(key)
	if !ok {
		// A queued key can outlive its object after watch replay, deletion, or a
		// checkpointed controller restart. Absence is the reconciled state.
		return types.ResultOK()
	}
	if current.Name == SystemRepositoryName {
		return types.ResultOK()
	}
	if slices.Contains(current.Finalizers, ForegroundDeletionFinalizer) {
		return r.reconcileDeletion(ctx, current)
	}
	return r.reconcileActive(ctx, key, current)
}

func (r *Reconciler) reconcileActive(ctx context.Context, key types.WorkItemKey, current Repository) types.ReconcileResult {
	admitted := conditionTrue(current.Status.Conditions, conditionAdmissionAccepted)
	var resolved ResolvedStorage
	var provisionErr error
	if admitted {
		resolved, provisionErr = r.storageClient.EnsureStorage(ctx, current.Namespace, current.Name)
	}

	storageReady := admitted && provisionErr == nil
	conditions := mergeControllerConditions(current, admitted, storageReady, provisionErr)
	generation := current.Generation
	patch := &status.StatusPatch{
		ResourceVersion:    current.ResourceVersion,
		ObservedGeneration: &generation,
		Conditions:         conditions,
	}
	if storageReady {
		resolvedJSON, err := json.Marshal(resolved)
		if err != nil {
			return types.ResultTransient(fmt.Errorf("repository: marshal resolved status: %w", err))
		}
		patch.Resolved = resolvedJSON
	}
	if !patch.IsNoOp(current.Status) {
		if err := r.statusClient.Apply(ctx, key, patch); err != nil {
			if errors.Is(err, types.ErrRateLimited) || errors.Is(provisionErr, types.ErrRateLimited) {
				return types.ResultAfter(rateLimitRequeueDelay)
			}
			if errors.Is(err, types.ErrConflict) {
				// Another replica (or a newer watch event) won the status write.
				// Re-enter through the queue so the next attempt observes fresh
				// cache state instead of exhausting one stale retry budget.
				return types.ResultAfter(conflictRequeueDelay)
			}
			if provisionErr != nil {
				return types.ResultTransient(fmt.Errorf("repository: provision and status update failed: %w", errors.Join(provisionErr, err)))
			}
			return types.ResultTransient(fmt.Errorf("repository: update status: %w", err))
		}
	}
	if provisionErr != nil {
		if errors.Is(provisionErr, types.ErrRateLimited) {
			return types.ResultAfter(rateLimitRequeueDelay)
		}
		return types.ResultTransient(fmt.Errorf("repository: provision storage: %w", provisionErr))
	}
	return types.ResultOK()
}

func (r *Reconciler) reconcileDeletion(ctx context.Context, current Repository) types.ReconcileResult {
	if err := r.completionClient.CompleteDeletion(ctx, current.Namespace, current.Name, current.ResourceVersion); err != nil {
		if errors.Is(err, types.ErrConflict) {
			return types.ResultAfter(conflictRequeueDelay)
		}
		return types.ResultTransient(fmt.Errorf("repository: complete deletion: %w", err))
	}
	return types.ResultOK()
}

func conditionTrue(conditions []*status.Condition, conditionType string) bool {
	for _, condition := range conditions {
		if condition != nil && condition.Type == conditionType {
			return strings.EqualFold(condition.Status, "true")
		}
	}
	return false
}

func mergeControllerConditions(current Repository, admitted, storageReady bool, provisionErr error) []*status.Condition {
	conditions := make([]*status.Condition, 0, len(current.Status.Conditions)+2)
	for _, prior := range current.Status.Conditions {
		if prior == nil || prior.Type == conditionStorageProvisioned || prior.Type == conditionReady {
			continue
		}
		copied := *prior
		conditions = append(conditions, &copied)
	}

	storageCondition := &status.Condition{
		Type:               conditionStorageProvisioned,
		Status:             statusFalse,
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
	}
	switch {
	case !admitted:
		storageCondition.Reason = "AdmissionPending"
		storageCondition.Message = "storage provisioning waits for AdmissionAccepted=True"
	case provisionErr != nil:
		storageCondition.Reason = "ProvisioningFailed"
		storageCondition.Message = provisionErr.Error()
	default:
		storageCondition.Status = statusTrue
		storageCondition.Reason = "StorageReady"
		storageCondition.Message = "bare Git repository exists"
	}

	readyCondition := &status.Condition{
		Type:               conditionReady,
		Status:             statusFalse,
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
		Reason:             "ConditionsNotSatisfied",
		Message:            "AdmissionAccepted and StorageProvisioned must both be True",
	}
	if admitted && storageReady {
		readyCondition.Status = statusTrue
		readyCondition.Reason = "RepositoryReady"
		readyCondition.Message = "repository admission and storage provisioning are complete"
	}

	preserveTransitionTime(storageCondition, current.Status.Conditions)
	preserveTransitionTime(readyCondition, current.Status.Conditions)
	return append(conditions, storageCondition, readyCondition)
}

func preserveTransitionTime(fresh *status.Condition, prior []*status.Condition) {
	for _, condition := range prior {
		if condition != nil && condition.Type == fresh.Type && condition.Status == fresh.Status {
			fresh.LastTransitionTime = condition.LastTransitionTime
			return
		}
	}
}
