// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

type PlannedChange struct {
	Key    ResourceKey
	Before *Resource
	After  *Resource
}

type TierPlan struct {
	State   TierState
	Changes []PlannedChange
	Replay  bool
}

func ValidTier(tier string) bool {
	if tier == ClusterTierID {
		return true
	}
	id, err := uuid.Parse(tier)
	return err == nil && id.String() == tier
}

func ValidateKey(key ResourceKey) error {
	if !ValidTier(key.TierID) || !resourceNamePattern.MatchString(key.Name) {
		return ErrInvalid
	}
	if _, err := CanonicalPath(key.Kind, key.Name); err != nil {
		return ErrInvalid
	}
	if key.Kind.Namespaced() == (key.TierID == ClusterTierID) {
		return ErrInvalid
	}
	return nil
}

func CloneResource(resource *Resource) (*Resource, error) {
	data, err := json.Marshal(resource)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid envelope", ErrInvalid)
	}
	var result Resource
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%w: invalid envelope", ErrInvalid)
	}
	result.NamespaceUID = resource.NamespaceUID
	result.ManifestBytes = resource.ManifestBytes
	return &result, nil
}

// PlanTierWrite is shared validation, not a concurrency boundary. Backends must
// conditionally publish every planned row and State as one transaction.
func PlanTierWrite(state TierState, current map[ResourceKey]*Resource, write TierWrite, now time.Time) (TierPlan, error) {
	return planTierWrite(state, current, write, now, write.Projection != nil)
}

// PlanAuthorCandidate checks the same row/count/size rules before Git commit,
// without inventing a commit receipt or publishing any lifecycle changes.
func PlanAuthorCandidate(state TierState, current map[ResourceKey]*Resource, write TierWrite, now time.Time) (TierPlan, error) {
	if write.Projection != nil || write.RetireNamespace {
		return TierPlan{}, ErrInvalid
	}
	return planTierWrite(state, current, write, now, true)
}

func ValidateTierWriteBounds(write TierWrite) error {
	limit := MaxCandidateResources
	if write.Projection != nil {
		// Repair can span several already-authorized receives. The complete
		// tier's quotas still apply; external candidates retain their own bound.
		limit = MaxRolesPerTier + MaxBindingsPerTier
	}
	if !ValidTier(write.TierID) || len(write.Changes) > limit ||
		len(write.Actor) > 256 || !utf8.ValidString(write.Actor) ||
		(len(write.Changes) == 0 && write.Projection == nil && !write.RetireNamespace) ||
		write.RetireNamespace && (write.TierID == ClusterTierID || write.Projection != nil || len(write.Changes) != 0) {
		return ErrInvalid
	}
	return nil
}

func planTierWrite(state TierState, current map[ResourceKey]*Resource, write TierWrite, now time.Time, authoring bool) (TierPlan, error) {
	plan := TierPlan{State: state}
	if err := ValidateTierWriteBounds(write); err != nil {
		return plan, err
	}
	if state.NamespaceRetired && authoring {
		return plan, ErrConflict
	}
	if write.Projection != nil {
		p := *write.Projection
		if p.RepositoryID == "" || p.GitRef == "" || p.GitRevision == "" || p.TreeDigest == "" || p.Actor == "" ||
			len(p.RepositoryID) > 64 || len(p.GitRef) > 256 || len(p.GitRevision) > 512 || len(p.TreeDigest) > 128 || len(p.Actor) > 256 {
			return plan, ErrInvalid
		}
		if state.Revision > 0 && state.Projection == p {
			plan.Replay = true
			return plan, nil
		}
		if state.Projection.GitRevision != write.ExpectedGitRevision || state.Projection.GitRevision == p.GitRevision {
			return plan, ErrConflict
		}
	}
	if state.Revision != write.ExpectedRevision {
		return plan, ErrConflict
	}
	if state.ProjectionPending {
		return plan, ErrUnavailable
	}
	if state.Revision >= math.MaxInt64 {
		return plan, ErrInvalid
	}
	plan.State.TierID = write.TierID
	plan.State.Revision++
	plan.State.NamespaceRetired = state.NamespaceRetired || write.RetireNamespace
	if write.Projection != nil {
		plan.State.Projection = *write.Projection
	}
	seen := map[ResourceKey]bool{}
	candidate := make([]*Resource, 0, len(write.Changes))
	for _, change := range write.Changes {
		input := change.Resource
		if input == nil || change.Remove && change.Terminate || change.Terminate && !authoring {
			return plan, ErrInvalid
		}
		if len(input.Spec) > MaxManifestBytes || len(input.Status) > MaxManifestBytes ||
			len(input.Metadata.OwnerReferences) > MaxManifestBytes || len(input.Metadata.Finalizers) > 256 {
			return plan, ErrInvalid
		}
		key := ResourceKey{TierID: write.TierID, Kind: input.Kind, Name: input.Metadata.Name}
		if err := ValidateKey(key); err != nil || seen[key] {
			return plan, ErrInvalid
		}
		seen[key] = true
		if input.APIVersion != APIVersion || input.Metadata.UID == "" ||
			(input.Kind.Namespaced() && input.NamespaceUID != write.TierID) ||
			(!input.Kind.Namespaced() && (input.NamespaceUID != "" || input.Metadata.Namespace != "")) {
			return plan, ErrInvalid
		}
		before := current[key]
		if before == nil {
			if change.ExpectedUID != "" || change.ExpectedResourceVersion != "" || change.Remove || change.Terminate || !authoring {
				return plan, ErrConflict
			}
		} else if before.Metadata.UID != change.ExpectedUID || before.Metadata.ResourceVersion != change.ExpectedResourceVersion {
			return plan, ErrConflict
		}
		if before != nil {
			if err := ValidateUpdate(before, input); err != nil {
				return plan, fmt.Errorf("%w: %s", ErrInvalid, err)
			}
			if (!authoring || change.Terminate) && (!bytes.Equal(before.Spec, input.Spec) ||
				!reflect.DeepEqual(before.Metadata.Labels, input.Metadata.Labels) ||
				!reflect.DeepEqual(before.Metadata.Annotations, input.Metadata.Annotations)) {
				return plan, ErrInvalid
			}
			if before.Metadata.DeletionTimestamp != nil && !reflect.DeepEqual(before.Metadata.DeletionTimestamp, input.Metadata.DeletionTimestamp) {
				return plan, ErrInvalid
			}
		}
		candidate = append(candidate, input)
		if change.Remove {
			if before.Metadata.DeletionTimestamp == nil || len(before.Metadata.Finalizers) != 0 {
				return plan, ErrConflict
			}
			plan.Changes = append(plan.Changes, PlannedChange{Key: key, Before: before})
		} else {
			after, err := CloneResource(input)
			if err != nil {
				return plan, err
			}
			if before == nil {
				after.Metadata.Generation, after.Metadata.ResourceVersion = 1, "1"
				created := now.UTC()
				after.Metadata.CreationTimestamp = &created
				after.Metadata.CreationActor = write.Actor
			} else {
				version, err := strconv.ParseUint(before.Metadata.ResourceVersion, 10, 63)
				if err != nil || version >= math.MaxInt64 {
					return plan, ErrUnavailable
				}
				after.Metadata.ResourceVersion = strconv.FormatUint(version+1, 10)
				after.Metadata.Generation = before.Metadata.Generation
				after.Metadata.CreationTimestamp = before.Metadata.CreationTimestamp
				after.Metadata.CreationActor = before.Metadata.CreationActor
				after.Metadata.Revision = before.Metadata.Revision
				if !bytes.Equal(before.Spec, after.Spec) {
					if after.Metadata.Generation == math.MaxInt64 {
						return plan, ErrInvalid
					}
					after.Metadata.Generation++
				}
				if authoring {
					after.Status = bytes.Clone(before.Status)
					after.Metadata.DeletionTimestamp = before.Metadata.DeletionTimestamp
					after.Metadata.Finalizers = append([]string(nil), before.Metadata.Finalizers...)
					after.Metadata.OwnerReferences = bytes.Clone(before.Metadata.OwnerReferences)
				}
			}
			if change.Terminate && after.Metadata.DeletionTimestamp == nil {
				deleted := now.UTC()
				after.Metadata.DeletionTimestamp = &deleted
			}
			updated := now.UTC()
			after.Metadata.UpdateTimestamp = &updated
			after.Metadata.UpdateActor = write.Actor
			if write.Projection != nil {
				after.Metadata.Revision = write.Projection.GitRevision
			}
			canonical, err := CanonicalManifest(after)
			if err != nil {
				return plan, ErrInvalid
			}
			path, _ := CanonicalPath(after.Kind, after.Metadata.Name)
			if _, err := ParseManifest(canonical, path, RepositoryScope{
				Namespace: after.Metadata.Namespace, NamespaceUID: after.NamespaceUID, Root: write.TierID == ClusterTierID,
			}); err != nil {
				return plan, fmt.Errorf("%w: %s", ErrInvalid, err)
			}
			plan.Changes = append(plan.Changes, PlannedChange{Key: key, Before: before, After: after})
		}
	}
	if write.Projection == nil {
		if err := ValidateCandidate(candidate); err != nil {
			return plan, fmt.Errorf("%w: %s", ErrInvalid, err)
		}
	}
	for _, change := range plan.Changes {
		for i, resource := range []*Resource{change.Before, change.After} {
			if resource == nil {
				continue
			}
			sign := 1
			if i == 0 {
				sign = -1
			}
			if resource.Kind.IsRole() {
				plan.State.RoleCount += sign
			} else {
				plan.State.BindingCount += sign
			}
			data, err := json.Marshal(resource)
			if err != nil {
				return plan, ErrInvalid
			}
			plan.State.PayloadBytes += int64(sign * len(data))
		}
	}
	header, _ := json.Marshal(plan.State)
	if plan.State.RoleCount < 0 || plan.State.BindingCount < 0 || plan.State.PayloadBytes < 0 {
		return plan, ErrUnavailable
	}
	if plan.State.RoleCount > MaxRolesPerTier || plan.State.BindingCount > MaxBindingsPerTier ||
		plan.State.PayloadBytes+int64(len(header)+plan.State.RoleCount+plan.State.BindingCount+64) > MaxSnapshotBytes {
		return plan, ErrQuota
	}
	return plan, nil
}

type pageCursor struct {
	Scope string `json:"scope"`
	Key   string `json:"key"`
}

func EncodePageCursor(scope, key string) string {
	data, _ := json.Marshal(pageCursor{Scope: scope, Key: key})
	return base64.RawURLEncoding.EncodeToString(data)
}

func DecodePageCursor(scope, encoded string, limit int) (string, error) {
	if scope == "" || limit < 1 || limit > SnapshotPageSize || len(encoded) > 1024 {
		return "", ErrInvalid
	}
	if encoded == "" {
		return "", nil
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	var cursor pageCursor
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Scope != scope || cursor.Key == "" {
		return "", ErrInvalid
	}
	return cursor.Key, nil
}
