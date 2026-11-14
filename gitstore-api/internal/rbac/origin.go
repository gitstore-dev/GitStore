// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

type OriginGitReader interface {
	ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
	ResolveRef(ctx context.Context, repositoryID, ref string) (string, error)
}

// OriginReader reads canonical Git and live Namespace facts, not serving caches.
type OriginReader struct {
	Store    datastore.Datastore
	Git      OriginGitReader
	MaxBytes int64
	Digests  *OriginDigestCache
}

type originDigestKey struct {
	origin SnapshotOrigin
	limit  int64
}

// OriginDigestCache retains only verified immutable tree digests, not live
// Namespace facts, authorization decisions or freshness proofs.
type OriginDigestCache struct {
	mu      sync.Mutex
	entries map[originDigestKey]string
	order   [128]originDigestKey
	next    int
}

func digestKey(origin SnapshotOrigin, limit int64) originDigestKey {
	origin.NamespaceVersion, origin.TreeDigest = "", ""
	return originDigestKey{origin: origin, limit: limit}
}

func (c *OriginDigestCache) get(origin SnapshotOrigin, limit int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	digest, ok := c.entries[digestKey(origin, limit)]
	return digest, ok
}

func (c *OriginDigestCache) put(origin SnapshotOrigin, limit int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[originDigestKey]string)
	}
	key := digestKey(origin, limit)
	if _, ok := c.entries[key]; ok {
		return
	}
	if len(c.entries) == len(c.order) {
		delete(c.entries, c.order[c.next])
	}
	c.entries[key] = origin.TreeDigest
	c.order[c.next] = key
	c.next = (c.next + 1) % len(c.order)
}

func (r OriginReader) facts(ctx context.Context, tier string) (SnapshotOrigin, error) {
	if !ValidTier(tier) || r.Store == nil || r.Git == nil {
		return SnapshotOrigin{}, snapshotUnavailable(nil)
	}
	var namespace *datastore.Namespace
	var err error
	if tier == ClusterTierID {
		namespace, err = r.Store.GetNamespaceByName(ctx, "gitstore-system")
	} else {
		namespace, err = r.Store.GetNamespace(ctx, tier)
	}
	if err != nil || namespace == nil || namespace.DeletionTimestamp != nil ||
		tier != ClusterTierID && namespace.UID != tier {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	mapping, err := r.Store.LookupRepository(ctx, namespace.Name, "gitstore-system")
	if err != nil || mapping == nil {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	datastore.NormalizeNamespaceMappingContract(mapping)
	repository, err := r.Store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil || repository == nil || repository.DeletionTimestamp != nil {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	datastore.NormalizeRepositoryContract(repository)
	if mapping.Namespace != namespace.Name || mapping.Name != "gitstore-system" ||
		repository.Name != "gitstore-system" || repository.Namespace != namespace.Name ||
		repository.UID != mapping.RepositoryID || repository.DefaultBranch == "" {
		return SnapshotOrigin{}, snapshotUnavailable(nil)
	}
	origin := SnapshotOrigin{NamespaceUID: namespace.UID, Namespace: namespace.Name,
		NamespaceVersion: namespace.ResourceVersion, Active: true, Root: tier == ClusterTierID,
		RepositoryID: repository.UID, GitRef: "refs/heads/" + repository.DefaultBranch}
	commit, err := r.Git.ResolveRef(ctx, origin.RepositoryID, origin.GitRef)
	if err != nil || !validDigest(commit, 20) {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	origin.GitRevision = repository.DefaultBranch + "@sha1:" + commit
	return origin, nil
}

func (r OriginReader) ReadOrigin(ctx context.Context, tier string) (SnapshotOrigin, error) {
	if r.Digests != nil {
		if r.MaxBytes == 0 {
			r.MaxBytes = MaxSnapshotBytes
		}
		if r.MaxBytes < 1 || r.MaxBytes > MaxSnapshotBytes {
			return SnapshotOrigin{}, snapshotUnavailable(nil)
		}
		current, err := r.facts(ctx, tier)
		if err != nil || ctx.Err() != nil {
			return SnapshotOrigin{}, snapshotUnavailable(err)
		}
		if digest, ok := r.Digests.get(current, r.MaxBytes); ok {
			current.TreeDigest = digest
			return current, nil
		}
	}
	origin, _, err := r.ReadTree(ctx, tier)
	return origin, err
}

// CheckOrigin verifies identity without reading immutable manifests again.
func (r OriginReader) CheckOrigin(ctx context.Context, tier string, expected SnapshotOrigin) error {
	current, err := r.facts(ctx, tier)
	expected.TreeDigest = ""
	if err != nil || current != expected || ctx.Err() != nil {
		return snapshotUnavailable(err)
	}
	return nil
}

// ReadTree pins immutable blobs and checks identity again after the bounded scan.
// The native bounded listing rejects non-regular entries.
func (r OriginReader) ReadTree(ctx context.Context, tier string) (SnapshotOrigin, []*Resource, error) {
	if r.MaxBytes == 0 {
		r.MaxBytes = MaxSnapshotBytes
	}
	if r.MaxBytes < 1 || r.MaxBytes > MaxSnapshotBytes {
		return SnapshotOrigin{}, nil, snapshotUnavailable(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	before, err := r.facts(ctx, tier)
	if err != nil {
		return SnapshotOrigin{}, nil, err
	}
	_, commit, _ := strings.Cut(before.GitRevision, "@sha1:")
	paths, err := r.Git.ListFiles(ctx, before.RepositoryID, "rbac", commit)
	if err != nil || len(paths) > MaxRolesPerTier+MaxBindingsPerTier {
		return SnapshotOrigin{}, nil, snapshotUnavailable(err)
	}
	slices.Sort(paths)
	paths = slices.DeleteFunc(paths, func(path string) bool {
		return !strings.HasPrefix(path, "rbac/") && path != "rbac"
	})
	for index, path := range paths {
		if len(path) > 1024 || index > 0 && paths[index-1] == path {
			return SnapshotOrigin{}, nil, snapshotUnavailable(nil)
		}
	}
	resources := make([]*Resource, 0, len(paths))
	total, roles, bindings := 0, 0, 0
	var contents [2][]byte
	var readErrors [2]error
	for index, path := range paths {
		if index%len(contents) == 0 {
			contents, readErrors = [2][]byte{}, [2]error{}
			batchCtx, cancel := context.WithCancel(ctx)
			var reads sync.WaitGroup
			for offset, batchPath := range paths[index:min(index+len(contents), len(paths))] {
				reads.Go(func() {
					contents[offset], readErrors[offset] = r.Git.ReadFile(batchCtx, before.RepositoryID, batchPath, commit)
					if readErrors[offset] != nil {
						cancel()
					}
				})
			}
			reads.Wait()
			cancel()
		}
		content, err := contents[index%len(contents)], readErrors[index%len(contents)]
		if err != nil || len(content) > MaxManifestBytes || int64(len(content)) > r.MaxBytes-int64(total) {
			return SnapshotOrigin{}, nil, snapshotUnavailable(err)
		}
		total += len(content)
		resource, err := ParseManifest(content, path, RepositoryScope{
			Namespace: before.Namespace, NamespaceUID: before.NamespaceUID, Root: before.Root,
		})
		if err != nil {
			return SnapshotOrigin{}, nil, snapshotUnavailable(err)
		}
		if resource.Kind.IsRole() {
			roles++
		} else {
			bindings++
		}
		if roles > MaxRolesPerTier || bindings > MaxBindingsPerTier ||
			ValidateKey(ResourceKey{TierID: tier, Kind: resource.Kind, Name: resource.Metadata.Name}) != nil {
			return SnapshotOrigin{}, nil, snapshotUnavailable(nil)
		}
		resources = append(resources, resource)
	}
	after, err := r.facts(ctx, tier)
	if err != nil || before != after || ctx.Err() != nil {
		return SnapshotOrigin{}, nil, snapshotUnavailable(err)
	}
	before.TreeDigest, err = ManifestTreeDigest(resources)
	if err != nil || !before.valid(tier) || ctx.Err() != nil {
		return SnapshotOrigin{}, nil, snapshotUnavailable(err)
	}
	if r.Digests != nil {
		r.Digests.put(before, r.MaxBytes)
	}
	return before, resources, nil
}
