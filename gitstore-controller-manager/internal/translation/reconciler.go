// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package translation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	contract "github.com/gitstore-dev/gitstore/translation"
)

// ValidationSnapshot is supplied by the authorized API boundary, not inferred
// from target watch projections. A nil Target means an authorized missing target;
// unavailable or unproved reads must return an error instead.
type ValidationSnapshot struct {
	SourceID, SourceVersion, SourceDigest, NamespaceUID string
	Binding                                             *BindingState
	Target                                              *ValidationTarget
	Observation                                         *status.ResourceStatus
}

type ValidationTarget struct {
	Namespace   string
	Observation TargetObservation
	// Fields is the normalized spec object, without an outer "spec" envelope.
	Fields map[string]any
}

type ValidationClient interface {
	ValidationSnapshot(context.Context, types.WorkItemKey, string, string) (*ValidationSnapshot, error)
	BindTarget(context.Context, string, string, *BindingState, string) (*BindingState, error)
}

type Reconciler struct {
	lookup       cache.LookupFunc[Resource]
	validation   ValidationClient
	statusClient status.StatusClient
	metrics      *metrics
}

func NewReconcilerWithLookup(lookup cache.LookupFunc[Resource], validation ValidationClient, statusClient status.StatusClient) *Reconciler {
	return &Reconciler{lookup: lookup, validation: validation, statusClient: statusClient, metrics: defaultMetrics}
}

type validationResolved struct {
	CanonicalLocale     string              `json:"canonicalLocale,omitempty"`
	DescriptorDigest    string              `json:"descriptorDigest"`
	SourceDigest        string              `json:"sourceDigest"`
	Target              *validationEvidence `json:"target,omitempty"`
	ValidatedFieldPaths []string            `json:"validatedFieldPaths"`
}

type validationEvidence struct {
	APIVersion      string `json:"apiVersion"`
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	Generation      int64  `json:"generation"`
	Revision        string `json:"revision"`
	ContentDigest   string `json:"contentDigest"`
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) (result types.ReconcileResult) {
	started := time.Now()
	defer func() { r.metrics.observeAttempt(ctx, started, result) }()
	if err := ctx.Err(); err != nil {
		return types.ResultTransient(err)
	}
	if key.Kind != "Translation" || r.lookup == nil || r.validation == nil || r.statusClient == nil {
		return types.ResultTerminal(errors.New("translation: incomplete reconciler configuration"))
	}
	current, found, err := r.lookup(ctx, key)
	r.metrics.observeLookup(ctx, "projection", found, err)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("translation: read projection: %w", err))
	}
	if !found {
		return types.ResultOK()
	}
	if err := current.Validate(); err != nil || current.Metadata.Namespace != key.Namespace || current.Metadata.Name != key.Name {
		return types.ResultTransient(errors.New("translation: invalid source projection"))
	}
	if current.Metadata.DeletionTimestamp != nil {
		return types.ResultOK()
	}
	if len(current.Spec) > 8*contract.MaxDocumentBytes || len(current.Body) > contract.MaxDocumentBytes {
		return types.ResultTerminal(errors.New("translation: source exceeds validation budget"))
	}
	var spec contract.Spec
	if err := json.Unmarshal(current.Spec, &spec); err != nil {
		return types.ResultTerminal(errors.New("translation: invalid source spec"))
	}
	snapshot, err := r.validation.ValidationSnapshot(ctx, key, current.Metadata.UID, current.Metadata.ResourceVersion)
	r.metrics.observeLookup(ctx, "snapshot", snapshot != nil, err)
	if err != nil {
		return reconciliationFailure(err)
	}
	if ctx.Err() != nil {
		return types.ResultTransient(ctx.Err())
	}
	if snapshot == nil || snapshot.SourceDigest == "" || snapshot.NamespaceUID == "" || snapshot.Binding.validate() != nil ||
		snapshot.Binding.NamespaceUID != snapshot.NamespaceUID {
		return types.ResultTransient(errors.New("translation: incomplete validation snapshot"))
	}
	if snapshot.SourceID != current.Metadata.UID || snapshot.SourceVersion != current.Metadata.ResourceVersion ||
		snapshot.Binding.TranslationID != current.Metadata.UID || snapshot.Binding.SourceVersion != current.Metadata.ResourceVersion {
		return types.ResultAfter(100 * time.Millisecond)
	}
	current.Status = status.ResourceStatus{}
	if snapshot.Observation != nil {
		current.Status = *snapshot.Observation
	}
	resolved := validationResolved{SourceDigest: snapshot.SourceDigest, ValidatedFieldPaths: []string{}}
	resolved.CanonicalLocale, _ = contract.CanonicalLocale(spec.Locale)
	descriptor, descriptorErr := contract.DescriptorFor(spec.TargetRef.APIVersion, spec.TargetRef.Kind)
	if descriptorErr == nil {
		resolved.DescriptorDigest = descriptor.Digest
	}
	reason := "Validated"
	if err := contract.ValidateSpec(spec, key.Namespace, []byte(current.Body)); err != nil {
		reason = validationReason(err)
	} else if target := snapshot.Target; target == nil {
		reason = "TargetNotFound"
	} else {
		observed := target.Observation
		identity := observed.Target
		switch {
		case target.Namespace != key.Namespace || identity.NamespaceUID != snapshot.NamespaceUID ||
			identity.APIVersion != spec.TargetRef.APIVersion || identity.Kind != spec.TargetRef.Kind ||
			identity.Name != spec.TargetRef.Name || identity.UID == "" || observed.ResourceVersion == "" ||
			observed.Generation < 1 || observed.ContentDigest == "":
			return types.ResultTransient(errors.New("translation: contradictory target snapshot"))
		case observed.Terminating:
			reason = "TargetTerminating"
		case snapshot.Binding.BoundUID != nil && *snapshot.Binding.BoundUID != identity.UID:
			reason = "TargetReplaced"
		case spec.TargetRef.UID != "" && spec.TargetRef.UID != identity.UID:
			reason = "TargetUIDMismatch"
		case spec.TargetRef.ResourceVersion != "" && spec.TargetRef.ResourceVersion != observed.ResourceVersion:
			reason = "TargetVersionMismatch"
		default:
			paths, err := contract.ValidateFieldsContext(ctx, identity.APIVersion, identity.Kind, target.Fields, spec.Patches, []byte(current.Body))
			if ctx.Err() != nil {
				return types.ResultTransient(ctx.Err())
			}
			if err != nil {
				var unavailable *contract.Error
				if errors.As(err, &unavailable) && unavailable.Reason == "TargetWorkUnavailable" {
					return types.ResultTransient(err)
				}
			}
			if snapshot.Binding.BoundUID == nil {
				observation := *snapshot.Binding
				request := observation
				bound, err := r.validation.BindTarget(ctx, key.Namespace, key.Name, &request, identity.UID)
				r.metrics.observeLookup(ctx, "binding", bound != nil, err)
				if err != nil {
					return reconciliationFailure(err)
				}
				if bound.validate() != nil || bound.TranslationID != current.Metadata.UID ||
					bound.SourceVersion != current.Metadata.ResourceVersion || bound.ReferenceEpoch != observation.ReferenceEpoch ||
					bound.NamespaceUID != observation.NamespaceUID ||
					bound.BoundUID == nil || *bound.BoundUID != identity.UID {
					return types.ResultTransient(errors.New("translation: binding acknowledgement changed identity"))
				}
			}
			if err != nil {
				reason = validationReason(err)
				break
			}
			resolved.ValidatedFieldPaths = paths
			resolved.Target = &validationEvidence{
				APIVersion: identity.APIVersion, Kind: identity.Kind, Namespace: key.Namespace, Name: identity.Name, UID: identity.UID,
				ResourceVersion: observed.ResourceVersion, Generation: observed.Generation, Revision: observed.Revision, ContentDigest: observed.ContentDigest,
			}
		}
	}
	if ctx.Err() != nil {
		return types.ResultTransient(ctx.Err())
	}
	ready := reason == "Validated"
	generation, revision := current.Metadata.Generation, current.Metadata.Revision
	raw, err := json.Marshal(resolved)
	if err != nil {
		return types.ResultTransient(fmt.Errorf("translation: encode observation: %w", err))
	}
	patch := &status.StatusPatch{
		UID: current.Metadata.UID, ResourceVersion: current.Metadata.ResourceVersion,
		ObservedGeneration: &generation, LastAppliedRevision: &revision,
		Conditions: validationConditions(current, ready, reason), Resolved: raw,
	}
	unchanged, err := validationNoOp(patch, current.Status)
	if err != nil {
		return types.ResultTransient(err)
	}
	if !unchanged {
		err := r.statusClient.Apply(ctx, key, patch)
		r.metrics.observeStatus(ctx, false, err)
		if err != nil {
			return reconciliationFailure(err)
		}
	} else {
		r.metrics.observeStatus(ctx, true, nil)
	}
	if !ready {
		return types.ResultAfter(time.Minute)
	}
	return types.ResultOK()
}

func reconciliationFailure(err error) types.ReconcileResult {
	if errors.Is(err, types.ErrConflict) {
		return types.ResultAfter(100 * time.Millisecond)
	}
	return types.ResultTransient(err)
}

func validationReason(err error) string {
	var invalid *contract.Error
	if errors.As(err, &invalid) {
		return invalid.Reason
	}
	return "InvalidTranslation"
}

func validationConditions(current Resource, ready bool, reason string) []*status.Condition {
	value := "FALSE"
	if ready {
		value = "TRUE"
	}
	condition := &status.Condition{
		Type: "Ready", Status: value, ObservedGeneration: current.Metadata.Generation, LastTransitionTime: time.Now().UTC(),
		Reason: reason, Message: "Translation validation does not modify target content",
	}
	conditions := make([]*status.Condition, 0, len(current.Status.Conditions)+1)
	for _, prior := range current.Status.Conditions {
		if prior == nil {
			continue
		}
		if prior.Type == "Ready" {
			if prior.Status == value {
				condition.LastTransitionTime = prior.LastTransitionTime
			}
			continue
		}
		copy := *prior
		conditions = append(conditions, &copy)
	}
	return append(conditions, condition)
}

func validationNoOp(patch *status.StatusPatch, current status.ResourceStatus) (bool, error) {
	comparison := *patch
	canonical := func(raw json.RawMessage) (json.RawMessage, error) {
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
		return json.Marshal(fields)
	}
	var err error
	comparison.Resolved, err = canonical(comparison.Resolved)
	if err != nil {
		return false, err
	}
	if len(current.Resolved) != 0 {
		current.Resolved, err = canonical(current.Resolved)
		if err != nil {
			return false, err
		}
	}
	return comparison.IsNoOp(current), nil
}

var _ types.Reconciler = (*Reconciler)(nil)
