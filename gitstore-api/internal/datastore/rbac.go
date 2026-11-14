// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrRBACNotFound    = errors.New("rbac: resource not found")
	ErrRBACConflict    = errors.New("rbac: revision conflict")
	ErrRBACInvalid     = errors.New("rbac: invalid resource")
	ErrRBACQuota       = errors.New("rbac: tier quota exceeded")
	ErrRBACUnavailable = errors.New("rbac: authoritative state unavailable")
)

type RBACKind string

const (
	RBACRole               RBACKind = "Role"
	RBACClusterRole        RBACKind = "ClusterRole"
	RBACRoleBinding        RBACKind = "RoleBinding"
	RBACClusterRoleBinding RBACKind = "ClusterRoleBinding"
	RBACClusterTierID               = "cluster"
)

func (k RBACKind) Namespaced() bool { return k == RBACRole || k == RBACRoleBinding }
func (k RBACKind) IsRole() bool     { return k == RBACRole || k == RBACClusterRole }

type RBACMetadata struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
	UID               string            `json:"uid,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Revision          string            `json:"revision,omitempty"`
	CreationTimestamp *time.Time        `json:"creationTimestamp,omitempty"`
	CreationActor     string            `json:"creationActor,omitempty"`
	UpdateTimestamp   *time.Time        `json:"updateTimestamp,omitempty"`
	UpdateActor       string            `json:"updateActor,omitempty"`
	DeletionTimestamp *time.Time        `json:"deletionTimestamp,omitempty"`
	Finalizers        []string          `json:"finalizers,omitempty"`
	OwnerReferences   json.RawMessage   `json:"ownerReferences,omitempty"`
}

type RBACResource struct {
	APIVersion    string          `json:"apiVersion"`
	Kind          RBACKind        `json:"kind"`
	Metadata      RBACMetadata    `json:"metadata"`
	Spec          json.RawMessage `json:"spec"`
	Status        json.RawMessage `json:"status,omitempty"`
	NamespaceUID  string          `json:"-"`
	ManifestBytes int             `json:"-"`
}

type RBACResourceKey struct {
	TierID string   `json:"tierID"`
	Kind   RBACKind `json:"kind"`
	Name   string   `json:"name"`
}

type RBACProjectionIdentity struct {
	RepositoryID string `json:"repositoryID"`
	GitRef       string `json:"gitRef"`
	GitRevision  string `json:"gitRevision"`
	TreeDigest   string `json:"treeDigest"`
	Actor        string `json:"actor"`
}

type RBACTierState struct {
	TierID            string                 `json:"tierID"`
	NamespaceRetired  bool                   `json:"namespaceRetired,omitempty"`
	Revision          uint64                 `json:"revision"`
	RoleCount         int                    `json:"roleCount"`
	BindingCount      int                    `json:"bindingCount"`
	Projection        RBACProjectionIdentity `json:"projection"`
	PayloadBytes      int64                  `json:"-"`
	ProjectionPending bool                   `json:"-"`
}

type RBACTierProgress struct {
	TierID   string `json:"tierID"`
	Revision uint64 `json:"revision"`
}

type RBACListRequest struct {
	TierID           string
	Kind             RBACKind
	After            string
	Limit            int
	ExpectedRevision *uint64
}

type RBACPage struct {
	Resources []*RBACResource
	Next      string
	Revision  uint64
}

type RBACChange struct {
	Resource                *RBACResource
	ExpectedUID             string
	ExpectedResourceVersion string
	Remove                  bool
	Terminate               bool
}

type RBACTierWrite struct {
	TierID              string
	RetireNamespace     bool
	ExpectedRevision    uint64
	ExpectedGitRevision string
	Projection          *RBACProjectionIdentity
	Changes             []RBACChange
	Actor               string
}

type rbacAuditActorContextKey struct{}

func ContextWithRBACAuditActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, rbacAuditActorContextKey{}, actor)
}

func RBACAuditActorFromContext(ctx context.Context) string {
	actor, _ := ctx.Value(rbacAuditActorContextKey{}).(string)
	return actor
}

type RBACGuardState string

const (
	RBACGuardActive      RBACGuardState = "active"
	RBACGuardTerminating RBACGuardState = "terminating"
	RBACGuardFinalized   RBACGuardState = "finalized"
)

type RBACRoleGuard struct {
	RoleUID string
	Version uint64
	State   RBACGuardState
	Claims  int64
}

type RBACRoleClaim struct {
	RoleUID    string          `json:"roleUID"`
	BindingUID string          `json:"bindingUID"`
	Binding    RBACResourceKey `json:"binding"`
}

type RBACGuardAction string

const (
	RBACGuardRegister RBACGuardAction = "register"
	RBACGuardAcquire  RBACGuardAction = "acquire"
	RBACGuardRelease  RBACGuardAction = "release"
	RBACGuardClose    RBACGuardAction = "close"
	RBACGuardFinalize RBACGuardAction = "finalize"
)

type RBACGuardMutation struct {
	RoleUID         string
	ExpectedVersion uint64
	Action          RBACGuardAction
	Claim           *RBACRoleClaim
}

type RBACClaimPage struct {
	Claims []RBACRoleClaim
	Next   string
}

type RBACNamespaceCleanup struct {
	NamespaceUID string
	Namespace    string
}

type RBACNamespaceCleanupPage struct {
	Jobs []RBACNamespaceCleanup
	Next string
}

type RBACStore interface {
	GetRBAC(context.Context, RBACResourceKey) (*RBACResource, error)
	GetRBACByUID(context.Context, string) (*RBACResource, error)
	ReadRBACTier(context.Context, string) (RBACTierState, error)
	ListRBAC(context.Context, RBACListRequest) (RBACPage, error)
	ApplyRBAC(context.Context, RBACTierWrite) (RBACTierState, error)
	RepairRBACProjection(context.Context, string, uint64) error
	ReadRBACRoleGuard(context.Context, string) (RBACRoleGuard, error)
	MutateRBACRoleGuard(context.Context, RBACGuardMutation) (RBACRoleGuard, error)
	ListRBACRoleClaims(context.Context, string, string, int) (RBACClaimPage, error)
	ListRBACBindingsByRole(context.Context, string, string, int) (RBACPage, error)
	ScheduleRBACNamespaceCleanup(context.Context, RBACNamespaceCleanup) error
	ListRBACNamespaceCleanups(context.Context, string, int) (RBACNamespaceCleanupPage, error)
	CompleteRBACNamespaceCleanup(context.Context, RBACNamespaceCleanup) error
}

type RBACSnapshot struct {
	State     RBACTierState   `json:"state"`
	Resources []*RBACResource `json:"resources"`
}

// ReadRBACSnapshot never installs a mixture of tier revisions. Git and parent
// lifecycle verification belong to the authoritative authorization capture.
func ReadRBACSnapshot(ctx context.Context, store RBACStore, tier string) (RBACSnapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for range 3 {
		snapshot, retry, err := ReadRBACSnapshotAttempt(ctx, store, tier)
		if err != nil || !retry {
			return snapshot, err
		}
	}
	return RBACSnapshot{}, RBACError(ErrRBACUnavailable)
}

// ReadRBACSnapshotAttempt performs one stable paged scan. The bool requests a
// retry; callers must share their outer identity-check retry/deadline budget.
func ReadRBACSnapshotAttempt(ctx context.Context, store RBACStore, tier string) (RBACSnapshot, bool, error) {
	return ReadRBACSnapshotAttemptWithLimit(ctx, store, tier, 64<<20)
}

func ReadRBACSnapshotAttemptWithLimit(ctx context.Context, store RBACStore, tier string, maxBytes int64) (RBACSnapshot, bool, error) {
	if maxBytes < 1 || maxBytes > 64<<20 {
		return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
	}
	before, err := store.ReadRBACTier(ctx, tier)
	if err != nil {
		if errors.Is(err, ErrRBACConflict) {
			return RBACSnapshot{}, true, nil
		}
		return RBACSnapshot{}, false, err
	}
	snapshot := RBACSnapshot{State: before}
	if before.TierID != tier || before.RoleCount < 0 || before.RoleCount > 500 || before.BindingCount < 0 || before.BindingCount > 5000 ||
		before.PayloadBytes < 0 || before.PayloadBytes > maxBytes {
		return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
	}
	seen := map[string]bool{}
	identities := map[string]bool{}
	var size int64
	roles, bindings := 0, 0
	after := ""
	for pageNumber := 0; pageNumber < 6; pageNumber++ {
		page, err := store.ListRBAC(ctx, RBACListRequest{TierID: tier, After: after, Limit: 1000, ExpectedRevision: &before.Revision})
		if err != nil {
			if errors.Is(err, ErrRBACConflict) {
				return RBACSnapshot{}, true, nil
			}
			return RBACSnapshot{}, false, err
		}
		if page.Revision != before.Revision {
			return RBACSnapshot{}, true, nil
		}
		if len(page.Resources) > 1000 {
			return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
		}
		for _, resource := range page.Resources {
			if resource == nil || resource.Metadata.UID == "" || resource.Metadata.Name == "" {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			if resource.Kind != RBACRole && resource.Kind != RBACClusterRole && resource.Kind != RBACRoleBinding && resource.Kind != RBACClusterRoleBinding {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			if resource.Kind.Namespaced() != (tier != RBACClusterTierID) ||
				(resource.Kind.Namespaced() && resource.NamespaceUID != tier) {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			key := string(resource.Kind) + "/" + resource.Metadata.Name
			if seen[resource.Metadata.UID] || identities[key] {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			seen[resource.Metadata.UID], identities[key] = true, true
			if resource.Kind.IsRole() {
				roles++
			} else {
				bindings++
			}
			data, err := json.Marshal(resource)
			if err != nil {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			size += int64(len(data))
			if size > maxBytes || roles > before.RoleCount || bindings > before.BindingCount {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			snapshot.Resources = append(snapshot.Resources, resource)
		}
		if page.Next == "" {
			final, err := store.ReadRBACTier(ctx, tier)
			if err != nil {
				return RBACSnapshot{}, false, err
			}
			if final.Revision != before.Revision || final.Projection != before.Projection {
				return RBACSnapshot{}, true, nil
			}
			if roles != before.RoleCount || bindings != before.BindingCount || size != before.PayloadBytes {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			header, err := json.Marshal(RBACSnapshot{State: before, Resources: []*RBACResource{}})
			separators := max(0, len(snapshot.Resources)-1)
			if err != nil || size+int64(len(header)+separators) > maxBytes {
				return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
			}
			return snapshot, false, nil
		}
		if page.Next == after || len(page.Next) > 1024 {
			return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
		}
		after = page.Next
	}
	return RBACSnapshot{}, false, RBACError(ErrRBACUnavailable)
}

type RBACCapable interface {
	RBACStore() RBACStore
}

// RBACError preserves the domain classification and existing datastore errors.
func RBACError(err error) error {
	switch {
	case errors.Is(err, ErrRBACNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.Is(err, ErrRBACConflict):
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case errors.Is(err, ErrRBACInvalid), errors.Is(err, ErrRBACQuota):
		return fmt.Errorf("%w: %w", ErrInvalidArgument, err)
	case errors.Is(err, ErrRBACUnavailable):
		return fmt.Errorf("%w: %w", ErrProjectionNotReady, err)
	default:
		return err
	}
}
