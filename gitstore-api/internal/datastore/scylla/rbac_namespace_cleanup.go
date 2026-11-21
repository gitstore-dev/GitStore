// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gocql/gocql"
)

func (s *scyllaDatastore) ScheduleRBACNamespaceCleanup(ctx context.Context, job rbac.NamespaceCleanup) error {
	if err := rbac.ValidateNamespaceCleanup(job); err != nil {
		return datastore.RBACError(err)
	}
	existing := map[string]any{}
	applied, err := s.session.Session.Query(
		"INSERT INTO rbac_namespace_cleanups_by_bucket (bucket,namespace_uid,namespace_name) VALUES (?,?,?) IF NOT EXISTS",
		rbac.NamespaceCleanupBucket(job.NamespaceUID), job.NamespaceUID, job.Namespace,
	).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).MapScanCAS(existing)
	if err != nil || applied {
		return err
	}
	if existing["namespace_name"] != job.Namespace {
		return datastore.RBACError(rbac.ErrConflict)
	}
	return nil
}

func (s *scyllaDatastore) ListRBACNamespaceCleanups(ctx context.Context, after string, limit int) (rbac.NamespaceCleanupPage, error) {
	bucket, uid, err := rbac.DecodeNamespaceCleanupCursor(after, limit)
	if err != nil {
		return rbac.NamespaceCleanupPage{}, datastore.RBACError(err)
	}
	page := rbac.NamespaceCleanupPage{}
	var last rbac.NamespaceCleanup
	for ; bucket < rbac.NamespaceCleanupBuckets; bucket++ {
		iter := s.session.Session.Query(
			"SELECT namespace_uid,namespace_name FROM rbac_namespace_cleanups_by_bucket WHERE bucket=? AND namespace_uid>? LIMIT ?",
			bucket, uid, limit-len(page.Jobs)+1,
		).WithContext(ctx).Consistency(gocql.Quorum).Iter()
		var job rbac.NamespaceCleanup
		for iter.Scan(&job.NamespaceUID, &job.Namespace) {
			if len(page.Jobs) == limit {
				page.Next = rbac.NamespaceCleanupCursor(rbac.NamespaceCleanupBucket(last.NamespaceUID), last.NamespaceUID)
				if err := iter.Close(); err != nil {
					return rbac.NamespaceCleanupPage{}, err
				}
				return page, nil
			}
			if err := rbac.ValidateNamespaceCleanup(job); err != nil || rbac.NamespaceCleanupBucket(job.NamespaceUID) != bucket {
				_ = iter.Close()
				return rbac.NamespaceCleanupPage{}, datastore.RBACError(rbac.ErrUnavailable)
			}
			page.Jobs = append(page.Jobs, job)
			last = job
		}
		if err := iter.Close(); err != nil {
			return rbac.NamespaceCleanupPage{}, err
		}
		uid = ""
	}
	return page, nil
}

func (s *scyllaDatastore) CompleteRBACNamespaceCleanup(ctx context.Context, job rbac.NamespaceCleanup) error {
	if err := rbac.ValidateNamespaceCleanup(job); err != nil {
		return datastore.RBACError(err)
	}
	existing := map[string]any{}
	applied, err := s.session.Session.Query(
		"DELETE FROM rbac_namespace_cleanups_by_bucket WHERE bucket=? AND namespace_uid=? IF namespace_name=?",
		rbac.NamespaceCleanupBucket(job.NamespaceUID), job.NamespaceUID, job.Namespace,
	).WithContext(ctx).Consistency(gocql.Quorum).SerialConsistency(gocql.Serial).MapScanCAS(existing)
	if err != nil || applied {
		return err
	}
	if existing["namespace_name"] == nil || existing["namespace_name"] == "" {
		return nil
	}
	return datastore.RBACError(rbac.ErrConflict)
}
