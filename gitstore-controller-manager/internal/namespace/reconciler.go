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
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	SystemRepositoryName        = "gitstore-system"
	ForegroundDeletionFinalizer = "gitstore.dev/foreground-deletion"

	conditionAdmissionAccepted = "AdmissionAccepted"
	conditionSystemRepoReady   = "SystemRepoReady"
	conditionReady             = "Ready"

	// statusTrue/statusFalse match the GraphQL wire enum ConditionStatus
	// (shared/schemas/schema.graphqls: TRUE/FALSE/UNKNOWN), not Go's
	// zero-value bool-ish casing. This matters because
	// internal/status.StatusPatch.IsNoOp compares Condition.Status
	// case-sensitively against values read back from the real GraphQL
	// list/watch path, which always carries the enum's upper-case wire
	// casing.
	statusTrue  = "TRUE"
	statusFalse = "FALSE"
)

var bootstrapNamespaces = map[string]struct{}{
	"gitstore-system": {},
	"default":         {},
}

// Namespace is the cache entity populated by NamespaceListWatcher.
type Namespace struct {
	Tier              string
	OwnerKind         string
	OwnerSubject      string
	DeletionTimestamp *time.Time
	UID               string
	Name              string
	Generation        int64
	ResourceVersion   string
	Revision          string
	Finalizers        []string
	Status            status.ResourceStatus
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
	CompleteDeletion(ctx context.Context, namespace, resourceVersion, uid string) error
}

type OwnerAccessClient interface {
	EnsureOwnerAccess(context.Context, Namespace) (string, error)
}

// Reconciler implements types.Reconciler for Namespace resources.
type Reconciler struct {
	lookup         cache.LookupFunc[Namespace]
	statusClient   status.StatusClient
	repositories   RepositoryClient
	deletionClient DeletionClient
	ownerAccess    OwnerAccessClient
}

// NewReconciler returns a Namespace reconciler.
func NewReconciler(c cache.CacheAccessor[Namespace], statusClient status.StatusClient, repositories RepositoryClient, deletionClient DeletionClient) *Reconciler {
	return NewReconcilerWithLookup(cache.LookupFrom(c), statusClient, repositories, deletionClient)
}

func NewReconcilerWithLookup(lookup cache.LookupFunc[Namespace], statusClient status.StatusClient, repositories RepositoryClient, deletionClient DeletionClient) *Reconciler {
	return &Reconciler{
		lookup:         lookup,
		statusClient:   statusClient,
		repositories:   repositories,
		deletionClient: deletionClient,
	}
}

// Reconcile provisions active admitted namespaces and drains terminating ones.
func (r *Reconciler) WithOwnerAccess(client OwnerAccessClient) *Reconciler {
	r.ownerAccess = client
	return r
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	current, ok, err := r.lookup(ctx, key)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("namespace: read projection: %w", err))
	}
	if !ok {
		// A queued key can outlive its object after watch replay, deletion, or a
		// checkpointed controller restart. Absence is the reconciled state.
		return types.ResultOK()
	}
	if span := trace.SpanFromContext(ctx); span.IsRecording() {
		uid, rv, revision := current.UID, current.ResourceVersion, current.Revision
		exact := uid != "" && rv != "" && revision != "" && current.Generation > 0
		// Stay within the telemetry SDK's 256-character cap; never truncate evidence.
		if len(uid) > 64 || len(rv) > 128 || len(revision) > 256 ||
			!utf8.ValidString(uid) || !utf8.ValidString(rv) || !utf8.ValidString(revision) {
			uid, rv, revision = "[invalid]", "[invalid]", "[invalid]"
			exact = false
		}
		span.AddEvent("controller.reconcile.snapshot", trace.WithAttributes(
			attribute.String("resource.uid", uid),
			attribute.String("resource.resource_version", rv),
			attribute.Int64("resource.generation", current.Generation),
			attribute.String("resource.revision", revision),
			attribute.Bool("resource.snapshot.exact", exact),
		))
	}
	if _, bootstrap := bootstrapNamespaces[current.Name]; bootstrap {
		return types.ResultOK()
	}
	if current.DeletionTimestamp != nil || slices.Contains(current.Finalizers, ForegroundDeletionFinalizer) || conditionTrue(current.Status.Conditions, "DeletionPending") {
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
	var ownerErr error
	if r.ownerAccess != nil && current.Tier == "USER" {
		ownerCondition := &status.Condition{Type: "OwnerAccessReady", Status: statusFalse, ObservedGeneration: current.Generation,
			LastTransitionTime: time.Now().UTC(), Reason: "Pending", Message: "owner access waits for namespace admission and system storage"}
		if systemReady {
			var revision string
			revision, ownerErr = r.ownerAccess.EnsureOwnerAccess(ctx, current)
			if ownerErr != nil {
				ownerCondition.Reason, ownerCondition.Message = "Failed", "owner binding admission is pending; inspect controller diagnostics"
			} else {
				ownerCondition.Status, ownerCondition.Reason = statusTrue, "BindingAdmitted"
				ownerCondition.Message = "binding admitted at " + revision + "; per-API authorization readiness is independent"
			}
		}
		preserveTransitionTime(ownerCondition, current.Status.Conditions)
		conditions = slices.DeleteFunc(conditions, func(condition *status.Condition) bool {
			return condition != nil && condition.Type == ownerCondition.Type
		})
		conditions = append(conditions, ownerCondition)
	}
	generation := current.Generation
	patch := &status.StatusPatch{
		UID:                current.UID,
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
	if ownerErr != nil {
		return types.ResultTransient(fmt.Errorf("namespace: provision owner access: %w", ownerErr))
	}
	return types.ResultOK()
}

func (r *Reconciler) reconcileDeletion(ctx context.Context, current Namespace) types.ReconcileResult {
	if err := r.deletionClient.CompleteDeletion(ctx, current.Name, current.ResourceVersion, current.UID); err != nil {
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
		copied := *condition
		conditions = append(conditions, &copied)
	}

	systemCondition := &status.Condition{
		Type:               conditionSystemRepoReady,
		Status:             statusFalse,
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
		systemCondition.Status = statusTrue
		systemCondition.Reason = "RepositoryReady"
		systemCondition.Message = "per-namespace gitstore-system repository exists"
	}

	readyCondition := &status.Condition{
		Type:               conditionReady,
		Status:             statusFalse,
		ObservedGeneration: current.Generation,
		LastTransitionTime: time.Now(),
		Reason:             "ConditionsNotSatisfied",
		Message:            "AdmissionAccepted and SystemRepoReady must both be True",
	}
	if admitted && systemReady {
		readyCondition.Status = statusTrue
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
