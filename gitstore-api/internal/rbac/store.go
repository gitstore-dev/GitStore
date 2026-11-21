// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import "github.com/gitstore-dev/gitstore/api/internal/datastore"

var (
	ErrNotFound    = datastore.ErrRBACNotFound
	ErrConflict    = datastore.ErrRBACConflict
	ErrInvalid     = datastore.ErrRBACInvalid
	ErrQuota       = datastore.ErrRBACQuota
	ErrUnavailable = datastore.ErrRBACUnavailable
)

const ClusterTierID = datastore.RBACClusterTierID

type ResourceKey = datastore.RBACResourceKey
type ProjectionIdentity = datastore.RBACProjectionIdentity
type TierState = datastore.RBACTierState
type ListRequest = datastore.RBACListRequest
type Page = datastore.RBACPage
type Change = datastore.RBACChange
type TierWrite = datastore.RBACTierWrite
type GuardState = datastore.RBACGuardState
type RoleGuard = datastore.RBACRoleGuard
type RoleClaim = datastore.RBACRoleClaim
type GuardAction = datastore.RBACGuardAction
type GuardMutation = datastore.RBACGuardMutation
type ClaimPage = datastore.RBACClaimPage
type NamespaceCleanup = datastore.RBACNamespaceCleanup
type NamespaceCleanupPage = datastore.RBACNamespaceCleanupPage
type Store = datastore.RBACStore

const (
	GuardActive      = datastore.RBACGuardActive
	GuardTerminating = datastore.RBACGuardTerminating
	GuardFinalized   = datastore.RBACGuardFinalized
	GuardRegister    = datastore.RBACGuardRegister
	GuardAcquire     = datastore.RBACGuardAcquire
	GuardRelease     = datastore.RBACGuardRelease
	GuardClose       = datastore.RBACGuardClose
	GuardFinalize    = datastore.RBACGuardFinalize
)
