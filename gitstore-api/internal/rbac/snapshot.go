// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/google/uuid"
)

var ErrSnapshotProjectionMismatch = fmt.Errorf("%w: canonical projection mismatch", ErrUnavailable)

type SnapshotOrigin struct {
	NamespaceUID     string `json:"namespaceUID"`
	Namespace        string `json:"namespace"`
	NamespaceVersion string `json:"namespaceVersion"`
	Active           bool   `json:"active"`
	Root             bool   `json:"root"`
	RepositoryID     string `json:"repositoryID"`
	GitRef           string `json:"gitRef"`
	GitRevision      string `json:"gitRevision"`
	TreeDigest       string `json:"treeDigest"`
}

type Snapshot struct {
	ModelVersion  string         `json:"modelVersion"`
	State         TierState      `json:"state"`
	Origin        SnapshotOrigin `json:"origin"`
	CatalogDigest string         `json:"catalogDigest"`
	BuiltinDigest string         `json:"builtinDigest"`
	ContentDigest string         `json:"contentDigest"`
	Resources     []*Resource    `json:"resources"`
}

type SnapshotReader struct {
	Store Store
	// ReadOrigin must resolve the live Namespace UID/lifecycle and its canonical
	// system repository/Git identity, never an ordinary authorization cache.
	ReadOrigin func(context.Context, string) (SnapshotOrigin, error)
	// CheckOrigin may recheck live identity against an already verified
	// immutable tree; nil retains the full ReadOrigin recheck.
	CheckOrigin   func(context.Context, string, SnapshotOrigin) error
	CatalogDigest string
	BuiltinDigest string
	Retries       int
	Timeout       time.Duration
	MaxBytes      int64
}

func validDigest(value string, bytes int) bool {
	if len(value) != bytes*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func (o SnapshotOrigin) valid(tier string) bool {
	branch, ok := strings.CutPrefix(o.GitRef, "refs/heads/")
	oid, revisionOK := strings.CutPrefix(o.GitRevision, branch+"@sha1:")
	return validUUID(o.NamespaceUID) && validUUID(o.RepositoryID) && resourceNamePattern.MatchString(o.Namespace) &&
		o.NamespaceVersion != "" && len(o.NamespaceVersion) <= 128 && o.Active &&
		o.Root == (tier == ClusterTierID) && (o.Root || o.NamespaceUID == tier) &&
		ok && branch != "" && len(o.GitRef) <= 256 && revisionOK && validDigest(oid, 20) && validDigest(o.TreeDigest, 32)
}

// Read captures a complete model, not watch readiness or a globally fenced
// transaction. Identity and row retries share one three-attempt budget.
func (r SnapshotReader) Read(ctx context.Context, tier string) (Snapshot, error) {
	if r.Timeout == 0 {
		r.Timeout = 30 * time.Second
	}
	if r.Retries == 0 {
		r.Retries = 3
	}
	if r.MaxBytes == 0 {
		r.MaxBytes = MaxSnapshotBytes
	}
	if r.Timeout <= 0 || r.Timeout > 30*time.Second || r.Retries < 1 || r.Retries > 3 || r.MaxBytes < 1 || r.MaxBytes > MaxSnapshotBytes {
		return Snapshot{}, snapshotUnavailable(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	if !ValidTier(tier) || r.Store == nil || r.ReadOrigin == nil ||
		!validDigest(r.CatalogDigest, 32) || !validDigest(r.BuiltinDigest, 32) {
		return Snapshot{}, datastore.RBACError(ErrUnavailable)
	}
	for range r.Retries {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		before, err := r.ReadOrigin(ctx, tier)
		if err != nil || !before.valid(tier) {
			return Snapshot{}, snapshotUnavailable(err)
		}
		stored, retry, err := datastore.ReadRBACSnapshotAttemptWithLimit(ctx, r.Store, tier, r.MaxBytes)
		if err != nil {
			return Snapshot{}, snapshotUnavailable(err)
		}
		if retry {
			continue
		}
		if stored.State.NamespaceRetired {
			return Snapshot{}, snapshotUnavailable(nil)
		}
		p := stored.State.Projection
		if p.RepositoryID != before.RepositoryID || p.GitRef != before.GitRef ||
			p.GitRevision != before.GitRevision || p.TreeDigest != before.TreeDigest {
			return Snapshot{}, datastore.RBACError(ErrSnapshotProjectionMismatch)
		}
		if r.CheckOrigin != nil {
			if err := r.CheckOrigin(ctx, tier, before); err != nil {
				if ctx.Err() != nil {
					return Snapshot{}, snapshotUnavailable(err)
				}
				continue
			}
		} else {
			after, err := r.ReadOrigin(ctx, tier)
			if err != nil {
				return Snapshot{}, snapshotUnavailable(err)
			}
			if before != after {
				continue
			}
		}
		result := Snapshot{
			ModelVersion: ModelVersion, State: stored.State, Origin: before,
			CatalogDigest: r.CatalogDigest, BuiltinDigest: r.BuiltinDigest,
		}
		header, _ := json.Marshal(result)
		total := len(header) + 64
		digest := sha256.New()
		_, _ = digest.Write(header)
		resources := stored.Resources
		slices.SortFunc(resources, func(a, b *Resource) int {
			return strings.Compare(string(a.Kind)+"/"+a.Metadata.Name, string(b.Kind)+"/"+b.Metadata.Name)
		})
		for index, resource := range resources {
			if !validUUID(resource.Metadata.UID) || resource.Metadata.Generation < 1 ||
				resource.Metadata.CreationTimestamp == nil || resource.Metadata.CreationTimestamp.IsZero() {
				return Snapshot{}, datastore.RBACError(ErrUnavailable)
			}
			if version, err := strconv.ParseUint(resource.Metadata.ResourceVersion, 10, 63); err != nil || version == 0 {
				return Snapshot{}, datastore.RBACError(ErrUnavailable)
			}
			namespace := before.Namespace
			if tier == ClusterTierID {
				namespace = ""
			}
			if resource.Metadata.Namespace != namespace {
				return Snapshot{}, datastore.RBACError(ErrUnavailable)
			}
			manifest, err := CanonicalManifest(resource)
			path, pathErr := CanonicalPath(resource.Kind, resource.Metadata.Name)
			if err != nil || pathErr != nil {
				return Snapshot{}, datastore.RBACError(ErrUnavailable)
			}
			if _, err := ParseManifest(manifest, path, RepositoryScope{
				Namespace: namespace, NamespaceUID: before.NamespaceUID, Root: before.Root,
			}); err != nil {
				return Snapshot{}, snapshotUnavailable(err)
			}
			copy, err := CloneResource(resource)
			if err != nil {
				return Snapshot{}, snapshotUnavailable(err)
			}
			data, err := json.Marshal(copy)
			if err != nil || int64(len(data)+1) > r.MaxBytes-int64(total) {
				return Snapshot{}, datastore.RBACError(ErrUnavailable)
			}
			total += len(data) + 1
			_, _ = digest.Write([]byte{0})
			_, _ = digest.Write(data)
			resources[index] = copy
		}
		if int64(total) > r.MaxBytes {
			return Snapshot{}, snapshotUnavailable(nil)
		}
		if err := ctx.Err(); err != nil {
			return Snapshot{}, snapshotUnavailable(err)
		}
		result.Resources, result.ContentDigest = resources, hex.EncodeToString(digest.Sum(nil))
		return result, nil
	}
	return Snapshot{}, datastore.RBACError(ErrUnavailable)
}

func snapshotUnavailable(err error) error {
	if err == nil {
		return datastore.RBACError(ErrUnavailable)
	}
	return fmt.Errorf("%w: %w", datastore.RBACError(ErrUnavailable), err)
}
