// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

var errOriginAdvanced = errors.New("RBAC canonical revision advanced")

// ErrOriginRefBusy identifies native write-lock contention, never a missing ref.
var ErrOriginRefBusy = errors.New("RBAC canonical ref proof contended")

type OriginGitReader interface {
	ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
	ResolveRef(ctx context.Context, repositoryID, ref string) (string, error)
}

type OriginFileReader func(context.Context, string) ([]byte, error)

// OriginFileOpener validates the canonical mapping once for an immutable scan.
// ReadTree still checks live identity before and after using the scoped reader.
type OriginFileOpener interface {
	OpenFiles(context.Context, string, string) (OriginFileReader, error)
}

// OriginFileBatchOpener keeps bounded immutable reads under one canonical mapping.
type OriginFileBatchOpener interface {
	OpenFileBatches(context.Context, string, string) (func(context.Context, []string) ([][]byte, error), error)
}

// OriginRefResolver reuses the authoritative facts just read for this probe.
// Implementations must validate the supplied canonical mapping without caching it.
type OriginRefResolver interface {
	ResolveOriginRef(context.Context, *datastore.Namespace, *datastore.NamespaceMapping, *datastore.Repository) (string, error)
}

const MaxOriginChecks = datastore.MaxRBACOriginLookups

type OriginCheck struct {
	Tier     string
	Expected SnapshotOrigin
}

type OriginRefFacts = datastore.RBACOriginFacts

type OriginRefProof struct {
	Commit string
	Err    error
}

type OriginRefBatchResolver interface {
	ResolveOriginRefs(context.Context, []OriginRefFacts) []OriginRefProof
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
	if err != nil || namespace == nil || namespace.DeletionTimestamp != nil || datastore.HasDeletionIntent(namespace.Status) ||
		tier != ClusterTierID && namespace.UID != tier {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	mapping, err := r.Store.LookupRepository(ctx, namespace.Name, "gitstore-system")
	if err != nil || mapping == nil {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	datastore.NormalizeNamespaceMappingContract(mapping)
	repository, err := r.Store.GetRepository(ctx, mapping.RepositoryID)
	if err != nil {
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	return r.resolveFacts(ctx, tier, namespace, mapping, repository)
}

func (r OriginReader) probeFacts(ctx context.Context, tier string, expected SnapshotOrigin) (SnapshotOrigin, error) {
	facts, err := r.probeInputs(ctx, tier, expected)
	if err != nil {
		return SnapshotOrigin{}, err
	}
	return r.resolveFacts(ctx, tier, facts.Namespace, facts.Mapping, facts.Repository)
}

func (r OriginReader) probeInputs(ctx context.Context, tier string, expected SnapshotOrigin) (OriginRefFacts, error) {
	if !ValidTier(tier) || r.Store == nil || r.Git == nil || !expected.valid(tier) ||
		tier == ClusterTierID && expected.Namespace != "gitstore-system" {
		return OriginRefFacts{}, snapshotUnavailable(nil)
	}
	// Prior identity supplies only lookup keys; all three live results must agree.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var namespace *datastore.Namespace
	var mapping *datastore.NamespaceMapping
	var repository *datastore.Repository
	var namespaceErr, mappingErr, repositoryErr error
	var reads sync.WaitGroup
	reads.Go(func() {
		if tier == ClusterTierID {
			namespace, namespaceErr = r.Store.GetNamespaceByName(ctx, "gitstore-system")
		} else {
			namespace, namespaceErr = r.Store.GetNamespace(ctx, tier)
		}
		if namespaceErr != nil {
			cancel()
		}
	})
	reads.Go(func() {
		mapping, mappingErr = r.Store.LookupRepository(ctx, expected.Namespace, "gitstore-system")
		if mappingErr != nil {
			cancel()
		}
	})
	reads.Go(func() {
		repository, repositoryErr = r.Store.GetRepository(ctx, expected.RepositoryID)
		if repositoryErr != nil {
			cancel()
		}
	})
	reads.Wait()
	if err := errors.Join(namespaceErr, mappingErr, repositoryErr, ctx.Err()); err != nil {
		return OriginRefFacts{}, snapshotUnavailable(err)
	}
	return OriginRefFacts{Namespace: namespace, Mapping: mapping, Repository: repository}, nil
}

func originIdentity(ctx context.Context, tier string, namespace *datastore.Namespace,
	mapping *datastore.NamespaceMapping, repository *datastore.Repository,
) (SnapshotOrigin, error) {
	if namespace == nil || mapping == nil || repository == nil || namespace.DeletionTimestamp != nil ||
		datastore.HasDeletionIntent(namespace.Status) || repository.DeletionTimestamp != nil ||
		tier == ClusterTierID && namespace.Name != "gitstore-system" ||
		tier != ClusterTierID && namespace.UID != tier {
		return SnapshotOrigin{}, snapshotUnavailable(nil)
	}
	datastore.NormalizeNamespaceMappingContract(mapping)
	datastore.NormalizeRepositoryContract(repository)
	if ctx.Err() != nil {
		return SnapshotOrigin{}, snapshotUnavailable(ctx.Err())
	}
	if mapping.Namespace != namespace.Name || mapping.Name != "gitstore-system" ||
		repository.Name != "gitstore-system" || repository.Namespace != namespace.Name ||
		repository.UID != mapping.RepositoryID || repository.DefaultBranch == "" || repository.SourcePath != "" {
		return SnapshotOrigin{}, snapshotUnavailable(nil)
	}
	origin := SnapshotOrigin{NamespaceUID: namespace.UID, Namespace: namespace.Name,
		NamespaceVersion: namespace.ResourceVersion, Active: true, Root: tier == ClusterTierID,
		RepositoryID: repository.UID, GitRef: "refs/heads/" + repository.DefaultBranch}
	return origin, nil
}

func (r OriginReader) resolveFacts(ctx context.Context, tier string, namespace *datastore.Namespace,
	mapping *datastore.NamespaceMapping, repository *datastore.Repository,
) (SnapshotOrigin, error) {
	origin, err := originIdentity(ctx, tier, namespace, mapping, repository)
	if err != nil {
		return SnapshotOrigin{}, err
	}
	var commit string
	if resolver, ok := r.Git.(OriginRefResolver); ok {
		commit, err = resolver.ResolveOriginRef(ctx, namespace, mapping, repository)
	} else {
		commit, err = r.Git.ResolveRef(ctx, origin.RepositoryID, origin.GitRef)
	}
	if err != nil || !validDigest(commit, 20) {
		if errors.Is(err, ErrOriginRefBusy) {
			return origin, snapshotUnavailable(err)
		}
		return SnapshotOrigin{}, snapshotUnavailable(err)
	}
	origin.GitRevision = repository.DefaultBranch + "@sha1:" + commit
	return origin, nil
}

// ReadProjectedOrigin routes uncached, unprojected revisions to recovery before
// scanning immutable files; matching projection metadata is not a tree proof.
func (r OriginReader) ReadProjectedOrigin(ctx context.Context, tier string) (SnapshotOrigin, error) {
	return r.readOrigin(ctx, tier, true)
}

func (r OriginReader) ReadOrigin(ctx context.Context, tier string) (SnapshotOrigin, error) {
	return r.readOrigin(ctx, tier, false)
}

func (r OriginReader) readOrigin(ctx context.Context, tier string, requireProjection bool) (SnapshotOrigin, error) {
	var initial *SnapshotOrigin
	if r.Digests != nil || requireProjection {
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
		if r.Digests != nil {
			if digest, ok := r.Digests.get(current, r.MaxBytes); ok {
				current.TreeDigest = digest
				return current, nil
			}
		}
		if requireProjection {
			capable, ok := r.Store.(datastore.RBACCapable)
			if !ok || capable.RBACStore() == nil {
				return SnapshotOrigin{}, snapshotUnavailable(nil)
			}
			state, err := capable.RBACStore().ReadRBACTier(ctx, tier)
			if err != nil || state.TierID != tier || ctx.Err() != nil {
				return SnapshotOrigin{}, snapshotUnavailable(err)
			}
			if state.Projection.RepositoryID != current.RepositoryID || state.Projection.GitRef != current.GitRef ||
				state.Projection.GitRevision != current.GitRevision {
				return SnapshotOrigin{}, datastore.RBACError(ErrSnapshotProjectionMismatch)
			}
		}
		initial = &current
	}
	origin, _, err := r.readTree(ctx, tier, initial)
	return origin, err
}

// CheckOrigin verifies identity without reading immutable manifests again.
func (r OriginReader) CheckOrigin(ctx context.Context, tier string, expected SnapshotOrigin) error {
	current, err := r.probeFacts(ctx, tier, expected)
	if errors.Is(err, ErrOriginRefBusy) {
		return contendedOrigin(ctx, current, expected)
	}
	if err != nil || ctx.Err() != nil {
		return snapshotUnavailable(err)
	}
	return compareOrigin(current, expected)
}

func contendedOrigin(ctx context.Context, current, expected SnapshotOrigin) error {
	current.GitRevision = expected.GitRevision
	if ctx.Err() == nil && compareOrigin(current, expected) == nil {
		return snapshotUnavailable(ErrOriginRefBusy)
	}
	return snapshotUnavailable(nil)
}

func compareOrigin(current, expected SnapshotOrigin) error {
	expected.TreeDigest = ""
	if current != expected {
		current.GitRevision = expected.GitRevision
		if current == expected {
			return errors.Join(ErrUnavailable, errOriginAdvanced)
		}
		return snapshotUnavailable(nil)
	}
	return nil
}

// CheckOrigins batches live fact and signed ref I/O, never identity validation or freshness.
func (r OriginReader) CheckOrigins(ctx context.Context, checks []OriginCheck) []error {
	results := make([]error, len(checks))
	for i := range results {
		results[i] = ErrUnavailable
	}
	if len(checks) < 1 || len(checks) > MaxOriginChecks {
		return results
	}
	resolver, batch := r.Git.(OriginRefBatchResolver)
	if !batch {
		for i, check := range checks {
			results[i] = ErrUnavailable
			if check.Tier != ClusterTierID {
				results[i] = r.CheckOrigin(ctx, check.Tier, check.Expected)
			}
		}
		return results
	}
	var facts []OriginRefFacts
	var origins []SnapshotOrigin
	var indices []int
	inputs := r.probeBatchInputs(ctx, checks)
	for i, input := range inputs {
		check := checks[i]
		origin, err := originIdentity(ctx, check.Tier, input.Namespace, input.Mapping, input.Repository)
		if err != nil {
			continue
		}
		facts, origins, indices = append(facts, input), append(origins, origin), append(indices, i)
	}
	if len(facts) == 0 || ctx.Err() != nil {
		return results
	}
	proofs := resolver.ResolveOriginRefs(ctx, facts)
	if len(proofs) != len(facts) || ctx.Err() != nil {
		return results
	}
	for offset, proof := range proofs {
		if errors.Is(proof.Err, ErrOriginRefBusy) && proof.Commit == "" {
			results[indices[offset]] = contendedOrigin(ctx, origins[offset], checks[indices[offset]].Expected)
			continue
		}
		if proof.Err != nil || !validDigest(proof.Commit, 20) {
			continue
		}
		origin := origins[offset]
		origin.GitRevision = strings.TrimPrefix(origin.GitRef, "refs/heads/") + "@sha1:" + proof.Commit
		results[indices[offset]] = compareOrigin(origin, checks[indices[offset]].Expected)
	}
	return results
}

func (r OriginReader) probeBatchInputs(ctx context.Context, checks []OriginCheck) []OriginRefFacts {
	inputs := make([]OriginRefFacts, len(checks))
	if capable, ok := r.Store.(datastore.RBACOriginFactsCapable); ok {
		if read := capable.RBACOriginFactsReader(); read != nil {
			var lookups []datastore.RBACOriginLookup
			var indices []int
			for i, check := range checks {
				if check.Tier == ClusterTierID || !ValidTier(check.Tier) || !check.Expected.valid(check.Tier) {
					continue
				}
				lookups = append(lookups, datastore.RBACOriginLookup{NamespaceUID: check.Tier,
					Namespace: check.Expected.Namespace, RepositoryID: check.Expected.RepositoryID})
				indices = append(indices, i)
			}
			if len(lookups) == 0 {
				return inputs
			}
			facts, err := read(ctx, lookups)
			if err != nil || len(facts) != len(lookups) || ctx.Err() != nil {
				return inputs
			}
			for offset, fact := range facts {
				lookup := lookups[offset]
				if fact.Namespace != nil && fact.Mapping != nil && fact.Repository != nil &&
					fact.Namespace.UID == lookup.NamespaceUID && fact.Namespace.Name == lookup.Namespace &&
					fact.Mapping.Namespace == lookup.Namespace && fact.Mapping.RepositoryID == lookup.RepositoryID &&
					fact.Repository.UID == lookup.RepositoryID {
					inputs[indices[offset]] = fact
				}
			}
			return inputs
		}
	}
	jobs := make(chan int, len(checks))
	for i, check := range checks {
		if check.Tier == ClusterTierID {
			continue
		}
		jobs <- i
	}
	close(jobs)
	var readers sync.WaitGroup
	// Four identities bound each batch to twelve concurrent live datastore reads.
	for range min(4, len(checks)) {
		readers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				input, err := r.probeInputs(ctx, checks[i].Tier, checks[i].Expected)
				if err == nil {
					inputs[i] = input
				}
			}
		})
	}
	readers.Wait()
	return inputs
}

// ReadTree pins immutable blobs and checks identity again after the bounded scan.
// The native bounded listing rejects non-regular entries.
func (r OriginReader) ReadTree(ctx context.Context, tier string) (SnapshotOrigin, []*Resource, error) {
	return r.readTree(ctx, tier, nil)
}

func (r OriginReader) readTree(ctx context.Context, tier string, initial *SnapshotOrigin) (SnapshotOrigin, []*Resource, error) {
	if r.MaxBytes == 0 {
		r.MaxBytes = MaxSnapshotBytes
	}
	if r.MaxBytes < 1 || r.MaxBytes > MaxSnapshotBytes {
		return SnapshotOrigin{}, nil, snapshotUnavailable(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var before SnapshotOrigin
	if initial != nil {
		before = *initial
	} else {
		var err error
		before, err = r.facts(ctx, tier)
		if err != nil {
			return SnapshotOrigin{}, nil, err
		}
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
	readFile := OriginFileReader(func(ctx context.Context, path string) ([]byte, error) {
		return r.Git.ReadFile(ctx, before.RepositoryID, path, commit)
	})
	batchSize := 2
	readFiles := func(ctx context.Context, paths []string) ([][]byte, error) {
		batchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		contents := make([][]byte, len(paths))
		failures := make([]error, len(paths))
		var reads sync.WaitGroup
		for offset, path := range paths {
			reads.Go(func() {
				contents[offset], failures[offset] = readFile(batchCtx, path)
				if failures[offset] != nil {
					cancel()
				}
			})
		}
		reads.Wait()
		return contents, errors.Join(failures...)
	}
	if opener, ok := r.Git.(OriginFileBatchOpener); ok && len(paths) > 0 {
		readFiles, err = opener.OpenFileBatches(ctx, before.RepositoryID, commit)
		if err != nil || readFiles == nil {
			return SnapshotOrigin{}, nil, snapshotUnavailable(err)
		}
		batchSize = 32
	} else if opener, ok := r.Git.(OriginFileOpener); ok && len(paths) > 0 {
		readFile, err = opener.OpenFiles(ctx, before.RepositoryID, commit)
		if err != nil || readFile == nil {
			return SnapshotOrigin{}, nil, snapshotUnavailable(err)
		}
	}
	resources := make([]*Resource, 0, len(paths))
	total, roles, bindings := 0, 0, 0
	var contents [][]byte
	for index, path := range paths {
		if index%batchSize == 0 {
			batch := paths[index:min(index+batchSize, len(paths))]
			contents, err = readFiles(ctx, batch)
			if err != nil || len(contents) != len(batch) {
				return SnapshotOrigin{}, nil, snapshotUnavailable(err)
			}
		}
		content := contents[index%batchSize]
		if len(content) > MaxManifestBytes || int64(len(content)) > r.MaxBytes-int64(total) {
			return SnapshotOrigin{}, nil, snapshotUnavailable(nil)
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
