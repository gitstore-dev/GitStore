// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package namespace implements reconciliation for admitted Namespace resources.
package namespace

import (
	"context"
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
	SystemRepositoryName        = "gitstore-system"
	ForegroundDeletionFinalizer = "gitstore.dev/foreground-deletion"

	conditionAdmissionAccepted = "AdmissionAccepted"
	conditionSystemRepoReady   = "SystemRepoReady"
	conditionReady             = "Ready"

	namespaceDrainPollInterval = 5 * time.Second
)

var bootstrapNamespaces = map[string]struct{}{
	"gitstore-system": {},
	"default":         {},
}

// Namespace is the cache entity populated by NamespaceListWatcher.
type Namespace struct {
	UID             string
	Name            string
	Generation      int64
	ResourceVersion string
	Finalizers      []string
	Status          status.ResourceStatus
}

// RepositoryClient provisions the namespace's system repository and checks
// whether any repository remains during foreground deletion.
type RepositoryClient interface {
	EnsureSystemRepository(ctx context.Context, namespace string) error
	HasRepositories(ctx context.Context, namespace string) (bool, error)
}

// DeletionClient removes the foreground-deletion finalizer and completes the
// Namespace deletion after the repository drain condition clears.
type DeletionClient interface {
	CompleteDeletion(ctx context.Context, namespace, resourceVersion string) error
}

// Reconciler implements types.Reconciler for Namespace resources.
type Reconciler struct {
	cache          cache.CacheAccessor[Namespace]
	statusClient   status.StatusClient
	repositories   RepositoryClient
	deletionClient DeletionClient
}

// NewReconciler returns a Namespace reconciler.
func NewReconciler(c cache.CacheAccessor[Namespace], statusClient status.StatusClient, repositories RepositoryClient, deletionClient DeletionClient) *Reconciler {
	return &Reconciler{
		cache:          c,
		statusClient:   statusClient,
		repositories:   repositories,
		deletionClient: deletionClient,
	}
}

// Reconcile provisions active admitted namespaces and drains terminating ones.
func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	current, ok := r.cache.Get(key)
	if !ok {
		return types.ResultTerminal(fmt.Errorf("namespace: %q not found in cache", key.Name))
	}
	if _, bootstrap := bootstrapNamespaces[current.Name]; bootstrap {
		return types.ResultOK()
	}
	if slices.Contains(current.Finalizers, ForegroundDeletionFinalizer) {
		return r.reconcileDeletion(ctx, current)
	}
	return r.reconcileActive(ctx, key, current)
}

func (r *Reconciler) reconcileActive(ctx context.Context, key types.WorkItemKey, current Namespace) types.ReconcileResult {
	admitted := conditionTrue(current.Status.Conditions, conditionAdmissionAccepted)
	var provisionErr error
	if admitted {
		provisionErr = r.repositories.EnsureSystemRepository(ctx, current.Name)
	}

	systemReady := admitted && provisionErr == nil
	conditions := mergeControllerConditions(current, admitted, systemReady, provisionErr)
	generation := current.Generation
	patch := &status.StatusPatch{
		ResourceVersion:    current.ResourceVersion,
		ObservedGeneration: &generation,
		Conditions:         conditions,
	}

	if !patch.IsNoOp(current.Status) {
		if err := r.statusClient.Apply(ctx, key, patch); err != nil {
			if provisionErr != nil {
				return types.ResultTransient(fmt.Errorf("namespace: provision and status update failed: %w", errors.Join(provisionErr, err)))
			}
			return types.ResultTransient(fmt.Errorf("namespace: update status: %w", err))
		}
	}
	if provisionErr != nil {
		return types.ResultTransient(fmt.Errorf("namespace: provision system repository: %w", provisionErr))
	}
	return types.ResultOK()
}

func (r *Reconciler) reconcileDeletion(ctx context.Context, current Namespace) types.ReconcileResult {
	hasRepositories, err := r.repositories.HasRepositories(ctx, current.Name)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("namespace: check repositories: %w", err))
	}
	if hasRepositories {
		return types.ResultAfter(namespaceDrainPollInterval)
	}
	if err := r.deletionClient.CompleteDeletion(ctx, current.Name, current.ResourceVersion); err != nil {
		return types.ResultTransient(fmt.Errorf("namespace: complete deletion: %w", err))
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

func mergeControllerConditions(current Namespace, admitted, systemReady bool, provisionErr error) []*status.Condition {
	conditions := make([]*status.Condition, 0, len(current.Status.Conditions)+2)
	for _, condition := range current.Status.Conditions {
		if condition == nil || condition.Type == conditionSystemRepoReady || condition.Type == conditionReady {
			continue
		}
		copy := *condition
		conditions = append(conditions, &copy)
	}

	systemCondition := &status.Condition{
		Type:               conditionSystemRepoReady,
		Status:             "False",
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
	}
	switch {
	case !admitted:
		systemCondition.Reason = "AdmissionPending"
		systemCondition.Message = "system repository provisioning waits for AdmissionAccepted=True"
	case provisionErr != nil:
		systemCondition.Reason = "ProvisioningFailed"
		systemCondition.Message = provisionErr.Error()
	default:
		systemCondition.Status = "True"
		systemCondition.Reason = "RepositoryReady"
		systemCondition.Message = "per-namespace gitstore-system repository exists"
	}

	readyCondition := &status.Condition{
		Type:               conditionReady,
		Status:             "False",
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
		Reason:             "ConditionsNotSatisfied",
		Message:            "AdmissionAccepted and SystemRepoReady must both be True",
	}
	if admitted && systemReady {
		readyCondition.Status = "True"
		readyCondition.Reason = "NamespaceReady"
		readyCondition.Message = "namespace admission and system repository provisioning are complete"
	}

	preserveTransitionTime(systemCondition, current.Status.Conditions)
	preserveTransitionTime(readyCondition, current.Status.Conditions)
	return append(conditions, systemCondition, readyCondition)
}

func preserveTransitionTime(fresh *status.Condition, prior []*status.Condition) {
	for _, condition := range prior {
		if condition != nil && condition.Type == fresh.Type && condition.Status == fresh.Status {
			fresh.LastTransitionTime = condition.LastTransitionTime
			return
		}
	}
}
