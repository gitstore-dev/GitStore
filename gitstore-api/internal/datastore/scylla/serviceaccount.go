// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
)

// serviceAccountRow mirrors the columns of service_accounts_by_cluster_name.
// ServiceAccount is not a git-backed catalog resource (unlike File/Product),
// so it does not carry api_version/kind/labels/annotations/owner_references/
// finalizers/repository_id/spec/body/status — see models.go.
type serviceAccountRow struct {
	EnrollmentComplete bool       `db:"enrollment_complete"`
	CreationTimestamp  time.Time  `db:"creation_timestamp"`
	UID                gocql.UUID `db:"uid"`
	Name               string     `db:"name"`
	Generation         int64      `db:"generation"`
	ResourceVersion    string     `db:"resource_version"`
	CreationActor      string     `db:"creation_actor"`
	UpdateTimestamp    time.Time  `db:"update_timestamp"`
	UpdateActor        string     `db:"update_actor"`
	DeletionTimestamp  *time.Time `db:"deletion_timestamp"`
	Disabled           bool       `db:"disabled"`
	PublicKeys         string     `db:"public_keys"` // JSON-encoded []datastore.ServiceAccountPublicKey
}

type serviceAccountIndexRow struct {
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

type serviceAccountListingRow struct {
	Name              string     `db:"name"`
	Bucket            string     `db:"bucket"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

func toServiceAccountRow(sa *datastore.ServiceAccount) (*serviceAccountRow, error) {
	keys, err := json.Marshal(sa.PublicKeys)
	if err != nil {
		return nil, fmt.Errorf("scylla: marshal service account public keys: %w", err)
	}
	return &serviceAccountRow{
		CreationTimestamp: sa.CreationTimestamp,
		UID:               mustParseUUID(sa.UID),
		Name:              sa.Name,
		Generation:        sa.Generation,
		ResourceVersion:   sa.ResourceVersion,
		CreationActor:     sa.CreationActor,
		UpdateTimestamp:   sa.UpdateTimestamp,
		UpdateActor:       sa.UpdateActor,
		DeletionTimestamp: sa.DeletionTimestamp,
		Disabled:          sa.Disabled,
		PublicKeys:        string(keys),
	}, nil
}

func fromServiceAccountRow(r *serviceAccountRow) (*datastore.ServiceAccount, error) {
	var keys []datastore.ServiceAccountPublicKey
	if len(r.PublicKeys) > 0 {
		if err := json.Unmarshal([]byte(r.PublicKeys), &keys); err != nil {
			return nil, fmt.Errorf("scylla: unmarshal service account public keys: %w", err)
		}
	}
	return &datastore.ServiceAccount{
		UID:               r.UID.String(),
		Name:              r.Name,
		Disabled:          r.Disabled,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		PublicKeys:        keys,
		DeletionTimestamp: r.DeletionTimestamp,
	}, nil
}

func (s *scyllaDatastore) CreateServiceAccount(ctx context.Context, sa *datastore.ServiceAccount) error {
	if sa == nil || sa.UID == "" || sa.Name == "" {
		return fmt.Errorf("%w: service account uid and global name are required", datastore.ErrInvalidArgument)
	}
	if _, err := gocql.ParseUUID(sa.UID); err != nil {
		return fmt.Errorf("%w: invalid service account uid", datastore.ErrInvalidArgument)
	}
	if sa.CreationTimestamp.IsZero() {
		sa.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return err
	}
	// Name is authoritative. Incomplete enrollment is never usable by AuthN.
	applied, err := s.insertAuthoritative(ctx, s.serviceAccountByNameTable, row)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrAlreadyExists
	}
	const reserve = "INSERT INTO service_accounts_by_cluster_uid (uid,name,creation_timestamp) VALUES (?,?,?) IF NOT EXISTS"
	applied, err = s.session.Query(reserve, nil).WithContext(ctx).Bind(row.UID, row.Name, row.CreationTimestamp).ExecCASRelease()
	if err != nil {
		return err
	} // ambiguous reservation: bounded repair resolves it
	if !applied {
		_ = s.deleteServiceAccountAuthoritative(ctx, row, row.ResourceVersion)
		return datastore.ErrAlreadyExists
	}
	if err := s.insertServiceAccountListingProjection(ctx, row); err != nil {
		return err
	}
	return s.completeServiceAccountEnrollment(ctx, row)
}

func (s *scyllaDatastore) completeServiceAccountEnrollment(ctx context.Context, row *serviceAccountRow) error {
	const stmt = "UPDATE service_accounts_by_cluster_name SET enrollment_complete=true WHERE name=? IF uid=? AND resource_version=?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(row.Name, row.UID, row.ResourceVersion).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) serviceAccountUIDReservation(ctx context.Context, uid gocql.UUID) (*serviceAccountIndexRow, error) {
	stmt, names := s.serviceAccountByUIDTable.Get()
	var idx serviceAccountIndexRow
	if err := s.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"uid": uid}).GetRelease(&idx); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return &idx, nil
}

func (s *scyllaDatastore) serviceAccountAuthority(ctx context.Context, name string) (*serviceAccountRow, error) {
	stmt, names := s.serviceAccountByNameTable.Get()
	var row serviceAccountRow
	if err := s.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"name": name}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

func (s *scyllaDatastore) GetServiceAccountByUID(ctx context.Context, uid string) (*datastore.ServiceAccount, error) {
	parsed, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, datastore.ErrNotFound
	}
	idx, err := s.serviceAccountUIDReservation(ctx, parsed)
	if err != nil {
		return nil, err
	}
	row, err := s.serviceAccountAuthority(ctx, idx.Name)
	if err != nil {
		return nil, err
	}
	if !row.EnrollmentComplete || row.UID != parsed || !row.CreationTimestamp.Equal(idx.CreationTimestamp) {
		return nil, datastore.ErrNotFound
	}
	return fromServiceAccountRow(row)
}

func (s *scyllaDatastore) GetServiceAccountBySubject(ctx context.Context, name string) (*datastore.ServiceAccount, error) {
	row, err := s.serviceAccountAuthority(ctx, name)
	if err != nil {
		return nil, err
	}
	if !row.EnrollmentComplete {
		return nil, datastore.ErrNotFound
	}
	idx, err := s.serviceAccountUIDReservation(ctx, row.UID)
	if err != nil {
		return nil, err
	}
	if idx.Name != name || !row.CreationTimestamp.Equal(idx.CreationTimestamp) {
		return nil, datastore.ErrNotFound
	}
	return fromServiceAccountRow(row)
}

const maxServiceAccountAssertionReplayTTL = int64(2_147_483_647)

// TryConsumeServiceAccountAssertion conditionally writes the opaque JTI digest
// with a TTL ending at the assertion's replay-window expiry. Scylla LWT makes
// the first consume globally authoritative across API replicas.
func (s *scyllaDatastore) TryConsumeServiceAccountAssertion(ctx context.Context, jtiDigest string, expiresAt time.Time) (bool, error) {
	if jtiDigest == "" {
		return false, fmt.Errorf("%w: assertion JTI digest is required", datastore.ErrInvalidArgument)
	}
	ttl, err := serviceAccountAssertionReplayTTL(time.Now().UTC(), expiresAt)
	if err != nil {
		return false, err
	}
	const stmt = "INSERT INTO service_account_assertion_replays (jti_digest, consumed_at) VALUES (?, ?) IF NOT EXISTS USING TTL ?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(jtiDigest, time.Now().UTC(), ttl).ExecCASRelease()
	if err != nil {
		return false, err
	}
	return applied, nil
}

func serviceAccountAssertionReplayTTL(now, expiresAt time.Time) (int, error) {
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0, fmt.Errorf("%w: assertion replay expiry must be in the future", datastore.ErrInvalidArgument)
	}
	seconds := int64(remaining / time.Second)
	if remaining%time.Second != 0 {
		seconds++
	}
	if seconds > maxServiceAccountAssertionReplayTTL {
		return 0, fmt.Errorf("%w: assertion replay expiry exceeds maximum TTL", datastore.ErrInvalidArgument)
	}
	return int(seconds), nil
}

func (s *scyllaDatastore) ListServiceAccounts(ctx context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.ServiceAccount], error) {
	limit := page.Limit()
	backward := page.Last > 0
	items := make([]*datastore.ServiceAccount, 0, limit+1)
	lookups, queries := 0, 0
	for _, bucket := range serviceAccountBucketsForPage(page, time.Now().UTC()) {
		bucketPage := page
		if page.After != "" && !cursorInNamespaceBucket(page.After, bucket) {
			bucketPage.After = ""
		}
		if page.Before != "" && !cursorInNamespaceBucket(page.Before, bucket) {
			bucketPage.Before = ""
		}
		for {
			queries++
			if queries > projectionListQueryBudget {
				return nil, fmt.Errorf("ServiceAccount listing exceeds query budget; repair stale projections")
			}
			pq := buildPaginatedSelect(s.serviceAccountByBucketTable, bucketPage, "bucket", bucket, serviceAccountClusterKeys, nil, nil)
			var rows []serviceAccountListingRow
			if err := s.session.Query(pq.Stmt, nil).WithContext(ctx).Bind(pq.Args...).SelectRelease(&rows); err != nil {
				return nil, err
			}
			for _, idx := range rows {
				lookups++
				if lookups > projectionListLookupBudget {
					return nil, fmt.Errorf("ServiceAccount listing exceeds lookup budget; repair stale projections")
				}
				sa, err := s.GetServiceAccountByUID(ctx, idx.UID.String())
				if err != nil {
					if errors.Is(err, datastore.ErrNotFound) {
						continue
					}
					return nil, err
				}
				if sa.Name != idx.Name || !sa.CreationTimestamp.Equal(idx.CreationTimestamp) {
					continue
				}
				items = append(items, sa)
				if len(items) == limit+1 {
					break
				}
			}
			if len(items) == limit+1 || len(rows) < limit+1 {
				break
			}
			last := rows[len(rows)-1]
			cursor := encodeKeysetCursor(last.CreationTimestamp, last.UID.String())
			if backward {
				bucketPage.Before = cursor
			} else {
				bucketPage.After = cursor
			}
		}
		if len(items) == limit+1 {
			break
		}
	}
	if backward {
		reverseRows(items)
	}
	return buildPageResult(items, limit, page), nil
}

func (s *scyllaDatastore) UpdateServiceAccountKeys(ctx context.Context, uid string, add []datastore.ServiceAccountPublicKey, removeKeyIDs []string, expectedResourceVersion string) (*datastore.ServiceAccount, error) {
	sa, err := s.GetServiceAccountByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if err := datastore.ApplyServiceAccountKeyUpdate(sa, add, removeKeyIDs, expectedResourceVersion); err != nil {
		return nil, err
	}
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return nil, err
	}
	if err := s.updateServiceAccountKeysAuthoritative(ctx, row, expectedResourceVersion); err != nil {
		return nil, err
	}
	return sa, nil
}

func (s *scyllaDatastore) SetServiceAccountDisabled(ctx context.Context, uid string, disabled bool) error {
	sa, err := s.GetServiceAccountByUID(ctx, uid)
	if err != nil {
		return err
	}
	expected := sa.ResourceVersion
	sa.Disabled = disabled
	datastore.AdvanceServiceAccountSystemVersion(sa)
	sa.UpdateTimestamp = time.Now().UTC()
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return err
	}
	return s.updateServiceAccountDisabledAuthoritative(ctx, row, expected)
}

func (s *scyllaDatastore) DeleteServiceAccount(ctx context.Context, uid string) error {
	sa, err := s.GetServiceAccountByUID(ctx, uid)
	if err != nil {
		return err
	}
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return err
	}
	if err := s.deleteServiceAccountAuthoritative(ctx, row, sa.ResourceVersion); err != nil {
		return err
	}
	_ = s.deleteServiceAccountListingProjection(ctx, row)
	// Release only our own routing reservation, never a replacement account.
	_, err = s.session.Query("DELETE FROM service_accounts_by_cluster_uid WHERE uid=? IF name=? AND creation_timestamp=?", nil).WithContext(ctx).Bind(row.UID, row.Name, row.CreationTimestamp).ExecCASRelease()
	if err != nil {
		return err
	}
	return nil
}

func (s *scyllaDatastore) insertServiceAccountListingProjection(ctx context.Context, row *serviceAccountRow) error {
	if err := s.session.Query(
		"INSERT INTO service_accounts_by_cluster_bucket (bucket, creation_timestamp, uid, name) VALUES (?, ?, ?, ?)",
		nil,
	).WithContext(ctx).Bind(namespaceBucket(row.CreationTimestamp), row.CreationTimestamp, row.UID, row.Name).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: insert service account listing projection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) deleteServiceAccountListingProjection(ctx context.Context, row *serviceAccountRow) error {
	if err := s.session.Query(
		"DELETE FROM service_accounts_by_cluster_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(namespaceBucket(row.CreationTimestamp), row.CreationTimestamp, row.UID).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete service account listing projection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) updateServiceAccountKeysAuthoritative(ctx context.Context, row *serviceAccountRow, expected string) error {
	const stmt = "UPDATE service_accounts_by_cluster_name SET generation=?,resource_version=?,update_timestamp=?,update_actor=?,public_keys=? " +
		"WHERE name=? IF uid=? AND resource_version=?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		row.Generation, row.ResourceVersion, row.UpdateTimestamp, row.UpdateActor, row.PublicKeys,
		row.Name, row.UID, expected,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) updateServiceAccountDisabledAuthoritative(ctx context.Context, row *serviceAccountRow, expected string) error {
	const stmt = "UPDATE service_accounts_by_cluster_name SET disabled=?,resource_version=?,update_timestamp=? " +
		"WHERE name=? IF uid=? AND resource_version=?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		row.Disabled, row.ResourceVersion, row.UpdateTimestamp,
		row.Name, row.UID, expected,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) deleteServiceAccountAuthoritative(ctx context.Context, row *serviceAccountRow, expected string) error {
	return s.deleteAuthoritative(ctx,
		"DELETE FROM service_accounts_by_cluster_name WHERE name=? IF uid=? AND resource_version=?",
		row.Name, row.UID, expected,
	)
}
