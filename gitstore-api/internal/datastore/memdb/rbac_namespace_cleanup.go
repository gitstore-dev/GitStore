// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

type rbacNamespaceCleanupRecord struct {
	ID  string
	Job rbac.NamespaceCleanup
}

func cleanupRecordID(job rbac.NamespaceCleanup) string {
	return fmt.Sprintf("%02d/%s", rbac.NamespaceCleanupBucket(job.NamespaceUID), job.NamespaceUID)
}

func (m *memdbDatastore) ScheduleRBACNamespaceCleanup(ctx context.Context, job rbac.NamespaceCleanup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rbac.ValidateNamespaceCleanup(job); err != nil {
		return datastore.RBACError(err)
	}
	tx := m.db.Txn(true)
	defer tx.Abort()
	record := &rbacNamespaceCleanupRecord{ID: cleanupRecordID(job), Job: job}
	existing, err := tx.First("rbac_namespace_cleanup", "id", record.ID)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.(*rbacNamespaceCleanupRecord).Job != job {
			return datastore.RBACError(rbac.ErrConflict)
		}
		return nil
	}
	if err := tx.Insert("rbac_namespace_cleanup", record); err != nil {
		return err
	}
	tx.Commit()
	return nil
}

func (m *memdbDatastore) ListRBACNamespaceCleanups(ctx context.Context, after string, limit int) (rbac.NamespaceCleanupPage, error) {
	bucket, uid, err := rbac.DecodeNamespaceCleanupCursor(after, limit)
	if err != nil {
		return rbac.NamespaceCleanupPage{}, datastore.RBACError(err)
	}
	if err := ctx.Err(); err != nil {
		return rbac.NamespaceCleanupPage{}, err
	}
	start := fmt.Sprintf("%02d/%s", bucket, uid)
	tx := m.db.Txn(false)
	defer tx.Abort()
	iter, err := tx.LowerBound("rbac_namespace_cleanup", "id", start)
	if err != nil {
		return rbac.NamespaceCleanupPage{}, err
	}
	page := rbac.NamespaceCleanupPage{}
	var last rbac.NamespaceCleanup
	for raw := iter.Next(); raw != nil; raw = iter.Next() {
		record := raw.(*rbacNamespaceCleanupRecord)
		if record.ID <= start {
			continue
		}
		if len(page.Jobs) == limit {
			page.Next = rbac.NamespaceCleanupCursor(rbac.NamespaceCleanupBucket(last.NamespaceUID), last.NamespaceUID)
			break
		}
		page.Jobs = append(page.Jobs, record.Job)
		last = record.Job
	}
	return page, nil
}

func (m *memdbDatastore) CompleteRBACNamespaceCleanup(ctx context.Context, job rbac.NamespaceCleanup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rbac.ValidateNamespaceCleanup(job); err != nil {
		return datastore.RBACError(err)
	}
	tx := m.db.Txn(true)
	defer tx.Abort()
	raw, err := tx.First("rbac_namespace_cleanup", "id", cleanupRecordID(job))
	if err != nil || raw == nil {
		return err
	}
	record := raw.(*rbacNamespaceCleanupRecord)
	if record.Job != job {
		return datastore.RBACError(rbac.ErrConflict)
	}
	if err := tx.Delete("rbac_namespace_cleanup", record); err != nil {
		return err
	}
	tx.Commit()
	return nil
}
