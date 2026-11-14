// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"time"
)

func (d *InstrumentedDatastore) RBACStore() RBACStore {
	capable, ok := d.next.(RBACCapable)
	if !ok {
		return nil
	}
	next := capable.RBACStore()
	if next == nil {
		return nil
	}
	return &instrumentedRBACStore{next: next, parent: d}
}

type instrumentedRBACStore struct {
	next   RBACStore
	parent *InstrumentedDatastore
}

func (s *instrumentedRBACStore) ScheduleRBACNamespaceCleanup(ctx context.Context, job RBACNamespaceCleanup) error {
	start := time.Now()
	err := s.next.ScheduleRBACNamespaceCleanup(ctx, job)
	s.parent.observe("ScheduleRBACNamespaceCleanup", start, err)
	return err
}

func (s *instrumentedRBACStore) ListRBACNamespaceCleanups(ctx context.Context, after string, limit int) (RBACNamespaceCleanupPage, error) {
	start := time.Now()
	page, err := s.next.ListRBACNamespaceCleanups(ctx, after, limit)
	s.parent.observe("ListRBACNamespaceCleanups", start, err)
	return page, err
}

func (s *instrumentedRBACStore) CompleteRBACNamespaceCleanup(ctx context.Context, job RBACNamespaceCleanup) error {
	start := time.Now()
	err := s.next.CompleteRBACNamespaceCleanup(ctx, job)
	s.parent.observe("CompleteRBACNamespaceCleanup", start, err)
	return err
}

func (s *instrumentedRBACStore) GetRBAC(ctx context.Context, key RBACResourceKey) (*RBACResource, error) {
	start := time.Now()
	result, err := s.next.GetRBAC(ctx, key)
	s.parent.observe("GetRBAC", start, err)
	return result, err
}

func (s *instrumentedRBACStore) GetRBACByUID(ctx context.Context, uid string) (*RBACResource, error) {
	start := time.Now()
	result, err := s.next.GetRBACByUID(ctx, uid)
	s.parent.observe("GetRBACByUID", start, err)
	return result, err
}

func (s *instrumentedRBACStore) ReadRBACTier(ctx context.Context, tier string) (RBACTierState, error) {
	start := time.Now()
	result, err := s.next.ReadRBACTier(ctx, tier)
	s.parent.observe("ReadRBACTier", start, err)
	return result, err
}

func (s *instrumentedRBACStore) ListRBAC(ctx context.Context, request RBACListRequest) (RBACPage, error) {
	start := time.Now()
	result, err := s.next.ListRBAC(ctx, request)
	s.parent.observe("ListRBAC", start, err)
	return result, err
}

func (s *instrumentedRBACStore) ApplyRBAC(ctx context.Context, write RBACTierWrite) (RBACTierState, error) {
	start := time.Now()
	result, err := s.next.ApplyRBAC(ctx, write)
	s.parent.observe("ApplyRBAC", start, err)
	return result, err
}

func (s *instrumentedRBACStore) RepairRBACProjection(ctx context.Context, tier string, revision uint64) error {
	start := time.Now()
	err := s.next.RepairRBACProjection(ctx, tier, revision)
	s.parent.observe("RepairRBACProjection", start, err)
	return err
}

func (s *instrumentedRBACStore) ReadRBACRoleGuard(ctx context.Context, uid string) (RBACRoleGuard, error) {
	start := time.Now()
	result, err := s.next.ReadRBACRoleGuard(ctx, uid)
	s.parent.observe("ReadRBACRoleGuard", start, err)
	return result, err
}

func (s *instrumentedRBACStore) MutateRBACRoleGuard(ctx context.Context, mutation RBACGuardMutation) (RBACRoleGuard, error) {
	start := time.Now()
	result, err := s.next.MutateRBACRoleGuard(ctx, mutation)
	s.parent.observe("MutateRBACRoleGuard", start, err)
	return result, err
}

func (s *instrumentedRBACStore) ListRBACRoleClaims(ctx context.Context, uid, after string, limit int) (RBACClaimPage, error) {
	start := time.Now()
	result, err := s.next.ListRBACRoleClaims(ctx, uid, after, limit)
	s.parent.observe("ListRBACRoleClaims", start, err)
	return result, err
}

func (s *instrumentedRBACStore) ListRBACBindingsByRole(ctx context.Context, uid, after string, limit int) (RBACPage, error) {
	start := time.Now()
	result, err := s.next.ListRBACBindingsByRole(ctx, uid, after, limit)
	s.parent.observe("ListRBACBindingsByRole", start, err)
	return result, err
}
