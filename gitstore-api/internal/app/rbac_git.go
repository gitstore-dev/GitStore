// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

// rbacRecoveryGit is only for internal canonical recovery, never Git HTTP or
// ordinary authoring. Its fixed capability cannot depend on the cache it builds.
type rbacRecoveryGit struct {
	store  datastore.Datastore
	client *gitclient.Client

	refsMu       sync.Mutex
	refsPending  []*recoveryRef
	refsInFlight int
	refsRunning  bool
	refsSlots    chan struct{}
}

type recoveryRef struct {
	ctx   context.Context
	query *gitv1.RepositoryRef
	done  chan recoveryRefResult
}

type recoveryRefResult struct {
	commit string
	err    error
}

func (g *rbacRecoveryGit) canonical(ctx context.Context, id string) (*datastore.Repository, *datastore.Namespace, error) {
	if g.store == nil || g.client == nil || g.client.Git == nil || g.client.Registry == nil ||
		g.client.Registry.AuthZ() == nil || g.client.Registry.AuthZ().Name() != "opa" {
		return nil, nil, rbac.ErrUnavailable
	}
	repository, err := g.store.GetRepository(ctx, id)
	if err != nil || repository == nil || repository.UID != id || repository.Name != bootstrapRepositoryName ||
		repository.DeletionTimestamp != nil || repository.SourcePath != "" || repository.DefaultBranch == "" {
		return nil, nil, rbac.ErrUnavailable
	}
	namespace, err := g.store.GetNamespaceByName(ctx, repository.Namespace)
	if err != nil || namespace == nil || namespace.DeletionTimestamp != nil || datastore.HasDeletionIntent(namespace.Status) {
		return nil, nil, rbac.ErrUnavailable
	}
	mapping, err := g.store.LookupRepository(ctx, namespace.Name, bootstrapRepositoryName)
	if err != nil || mapping == nil || mapping.RepositoryID != id {
		return nil, nil, rbac.ErrUnavailable
	}
	return repository, namespace, nil
}

func (g *rbacRecoveryGit) provisionNamespace(ctx context.Context, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if g.store == nil || g.client == nil || g.client.Registry == nil || g.client.Registry.AuthZ() == nil ||
		g.client.Registry.AuthZ().Name() != "opa" {
		return rbac.ErrUnavailable
	}
	decision, err := g.client.Registry.AuthZ().Authorize(ctx, auth.PrincipalFromContext(ctx),
		auth.ActionNamespaceSystemRepositoryProvision, auth.ResourceContext{Kind: "namespace", Name: name})
	if err != nil {
		return rbac.ErrUnavailable
	}
	if decision.Outcome != auth.OutcomeAllow {
		return admission.NewError(admission.CodeForbidden, "NAMESPACE_PROVISION_FORBIDDEN", "namespace provisioning is not authorized")
	}
	namespace, err := g.store.GetNamespaceByName(ctx, name)
	if err != nil || namespace == nil || namespace.DeletionTimestamp != nil || datastore.HasDeletionIntent(namespace.Status) {
		return rbac.ErrUnavailable
	}
	return ensureBootstrapRepository(ctx, g.store, g, apiruntime.SystemClock{}, namespace)
}

func recoveryCommit(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func recoveryPath(path string, root bool) bool {
	parts := strings.Split(path, "/")
	if len(parts) != 3 || parts[0] != "rbac" {
		return false
	}
	kind := map[string]rbac.Kind{"roles": rbac.Role, "clusterroles": rbac.ClusterRole,
		"rolebindings": rbac.RoleBinding, "clusterrolebindings": rbac.ClusterRoleBinding}[parts[1]]
	canonical, err := rbac.CanonicalPath(kind, strings.TrimSuffix(parts[2], ".yaml"))
	return err == nil && canonical == path && kind.Namespaced() != root
}

func recoveryAuthorization(ctx context.Context, repository, action string) (*gitv1.RequestAuthorization, error) {
	return gitclient.RequestAuthorization(gitclient.ContextForBackingWork(ctx), action, repository)
}

func (g *rbacRecoveryGit) ListFiles(ctx context.Context, id, prefix, commit string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, namespace, err := g.canonical(ctx, id)
	if err != nil || prefix != "rbac" || !recoveryCommit(commit) {
		return nil, rbac.ErrUnavailable
	}
	authorization, err := recoveryAuthorization(ctx, id, "repository.contents.read")
	if err != nil {
		return nil, err
	}
	response, err := g.client.Git.ListFiles(ctx, &gitv1.ListFilesRequest{
		RepositoryId: id, Ref: commit, PathPrefix: prefix, Recursive: true, Authorization: authorization,
	}, grpc.MaxCallRecvMsgSize(8<<20))
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.Files) > rbac.MaxRolesPerTier+rbac.MaxBindingsPerTier {
		return nil, rbac.ErrUnavailable
	}
	paths := make([]string, 0, len(response.Files))
	for _, file := range response.Files {
		if file == nil || !recoveryPath(file.Path, namespace.Name == "gitstore-system") || file.SizeBytes > rbac.MaxManifestBytes {
			return nil, rbac.ErrUnavailable
		}
		paths = append(paths, file.Path)
	}
	return paths, nil
}

func (g *rbacRecoveryGit) OpenFiles(ctx context.Context, id, commit string) (rbac.OriginFileReader, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, namespace, err := g.canonical(ctx, id)
	if err != nil || !recoveryCommit(commit) {
		return nil, rbac.ErrUnavailable
	}
	root := namespace.Name == "gitstore-system"
	return func(ctx context.Context, path string) ([]byte, error) {
		return g.readCanonicalFile(ctx, id, path, commit, root)
	}, nil
}

func (g *rbacRecoveryGit) OpenFileBatches(ctx context.Context, id, commit string) (func(context.Context, []string) ([][]byte, error), error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, namespace, err := g.canonical(ctx, id)
	if err != nil || !recoveryCommit(commit) {
		return nil, rbac.ErrUnavailable
	}
	root := namespace.Name == "gitstore-system"
	return func(ctx context.Context, paths []string) ([][]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if ctx.Err() != nil || len(paths) == 0 || len(paths) > 32 {
			return nil, rbac.ErrUnavailable
		}
		seen := make(map[string]bool, len(paths))
		for _, path := range paths {
			if !recoveryPath(path, root) || seen[path] {
				return nil, rbac.ErrUnavailable
			}
			seen[path] = true
		}
		authorization, err := recoveryAuthorization(ctx, id, "repository.contents.read")
		if err != nil {
			return nil, err
		}
		response, err := g.client.Git.GetFiles(ctx, &gitv1.GetFilesRequest{
			RepositoryId: id, CommitSha: commit, Paths: paths, Authorization: authorization,
		}, grpc.MaxCallRecvMsgSize(32*rbac.MaxManifestBytes+(64<<10)))
		if err != nil {
			return nil, err
		}
		if response == nil || response.CommitSha != commit || len(response.Files) != len(paths) || ctx.Err() != nil {
			return nil, rbac.ErrUnavailable
		}
		contents := make([][]byte, len(paths))
		for index, file := range response.Files {
			if file == nil || file.Path != paths[index] || !recoveryCommit(file.BlobSha) ||
				len(file.Content) > rbac.MaxManifestBytes || file.SizeBytes != uint64(len(file.Content)) {
				return nil, rbac.ErrUnavailable
			}
			contents[index] = file.Content
		}
		return contents, nil
	}, nil
}

func (g *rbacRecoveryGit) ReadFile(ctx context.Context, id, path, commit string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	read, err := g.OpenFiles(ctx, id, commit)
	if err != nil {
		return nil, err
	}
	return read(ctx, path)
}

func (g *rbacRecoveryGit) readCanonicalFile(ctx context.Context, id, path, commit string, root bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !recoveryPath(path, root) {
		return nil, rbac.ErrUnavailable
	}
	authorization, err := recoveryAuthorization(ctx, id, "repository.contents.read")
	if err != nil {
		return nil, err
	}
	response, err := g.client.Git.GetFile(ctx, &gitv1.GetFileRequest{
		RepositoryId: id, Path: path, Ref: commit, Authorization: authorization,
	}, grpc.MaxCallRecvMsgSize(rbac.MaxManifestBytes+4096))
	if err != nil {
		return nil, err
	}
	if response == nil || len(response.Content) > rbac.MaxManifestBytes {
		return nil, rbac.ErrUnavailable
	}
	return response.Content, nil
}

func (g *rbacRecoveryGit) ResolveRef(ctx context.Context, id, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	repository, _, err := g.canonical(ctx, id)
	if err != nil || ref != "refs/heads/"+repository.DefaultBranch {
		return "", rbac.ErrUnavailable
	}
	return g.resolveRef(ctx, id, ref, repository.Namespace == "gitstore-system")
}

func (g *rbacRecoveryGit) ResolveOriginRef(ctx context.Context, namespace *datastore.Namespace,
	mapping *datastore.NamespaceMapping, repository *datastore.Repository,
) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !g.validOriginFacts(namespace, mapping, repository) {
		return "", rbac.ErrUnavailable
	}
	return g.resolveRef(ctx, repository.UID, "refs/heads/"+repository.DefaultBranch, namespace.Name == "gitstore-system")
}

func (g *rbacRecoveryGit) validOriginFacts(namespace *datastore.Namespace,
	mapping *datastore.NamespaceMapping, repository *datastore.Repository,
) bool {
	if g.store == nil || g.client == nil || g.client.Git == nil || g.client.Registry == nil ||
		g.client.Registry.AuthZ() == nil || g.client.Registry.AuthZ().Name() != "opa" ||
		namespace == nil || mapping == nil || repository == nil ||
		namespace.UID == "" || namespace.Name == "" || namespace.DeletionTimestamp != nil || datastore.HasDeletionIntent(namespace.Status) ||
		repository.DeletionTimestamp != nil || repository.UID == "" ||
		repository.Name != bootstrapRepositoryName || repository.Namespace != namespace.Name ||
		repository.SourcePath != "" || repository.DefaultBranch == "" ||
		mapping.Namespace != namespace.Name || mapping.Name != bootstrapRepositoryName || mapping.RepositoryID != repository.UID {
		return false
	}
	return true
}

func (g *rbacRecoveryGit) ResolveOriginRefs(ctx context.Context, facts []rbac.OriginRefFacts) []rbac.OriginRefProof {
	proofs := make([]rbac.OriginRefProof, len(facts))
	for i := range proofs {
		proofs[i].Err = rbac.ErrUnavailable
	}
	if len(facts) < 1 || len(facts) > rbac.MaxOriginChecks || ctx.Err() != nil {
		return proofs
	}
	queries := make([]*gitv1.RepositoryRef, len(facts))
	for i, input := range facts {
		if !g.validOriginFacts(input.Namespace, input.Mapping, input.Repository) || input.Namespace.Name == "gitstore-system" {
			return proofs
		}
		authorization, err := recoveryAuthorization(ctx, input.Repository.UID, "repository.contents.read")
		if err != nil {
			return proofs
		}
		queries[i] = &gitv1.RepositoryRef{RepositoryId: input.Repository.UID,
			Ref: "refs/heads/" + input.Repository.DefaultBranch, Authorization: authorization}
	}
	for i, result := range g.queueRefProofs(ctx, queries) {
		proofs[i] = rbac.OriginRefProof{Commit: result.commit, Err: result.err}
	}
	return proofs
}

func (g *rbacRecoveryGit) resolveRef(ctx context.Context, id, ref string, root bool) (string, error) {
	authorization, err := recoveryAuthorization(ctx, id, "repository.contents.read")
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	query := &gitv1.RepositoryRef{
		RepositoryId: id, Ref: ref, Authorization: authorization,
	}
	if root {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		result := g.readRefProofs(ctx, []*gitv1.RepositoryRef{query})[0]
		return result.commit, result.err
	}
	result := g.queueRefProofs(ctx, []*gitv1.RepositoryRef{query})[0]
	return result.commit, result.err
}

func (g *rbacRecoveryGit) queueRefProofs(ctx context.Context, queries []*gitv1.RepositoryRef) []recoveryRefResult {
	results := make([]recoveryRefResult, len(queries))
	for i := range results {
		results[i].err = rbac.ErrUnavailable
	}
	if len(queries) < 1 || len(queries) > rbac.MaxOriginChecks || ctx.Err() != nil {
		return results
	}
	g.refsMu.Lock()
	if g.refsInFlight+len(queries) > rbac.MaxOriginChecks {
		g.refsMu.Unlock()
		return results
	}
	requests := make([]*recoveryRef, len(queries))
	for i, query := range queries {
		requests[i] = &recoveryRef{ctx: ctx, query: query, done: make(chan recoveryRefResult, 1)}
	}
	g.refsInFlight += len(requests)
	g.refsPending = append(g.refsPending, requests...)
	if !g.refsRunning {
		if g.refsSlots == nil {
			g.refsSlots = make(chan struct{}, 2)
		}
		g.refsRunning = true
		go g.proveRefs()
	}
	g.refsMu.Unlock()
	for i, request := range requests {
		select {
		case <-ctx.Done():
			results[i].err = ctx.Err()
		case result := <-request.done:
			if ctx.Err() != nil {
				result = recoveryRefResult{err: ctx.Err()}
			}
			results[i] = result
		}
	}
	return results
}

func (g *rbacRecoveryGit) proveRefs() {
	for {
		g.refsSlots <- struct{}{}
		time.Sleep(time.Millisecond)
		g.refsMu.Lock()
		batch := g.refsPending
		g.refsPending = nil
		if len(batch) == 0 {
			g.refsRunning = false
			g.refsMu.Unlock()
			<-g.refsSlots
			return
		}
		g.refsMu.Unlock()
		go g.proveRefBatch(batch)
	}
}

func (g *rbacRecoveryGit) proveRefBatch(batch []*recoveryRef) {
	defer func() { <-g.refsSlots }()
	active := make([]*recoveryRef, 0, len(batch))
	var queries []*gitv1.RepositoryRef
	for _, waiter := range batch {
		if waiter.ctx.Err() == nil {
			active = append(active, waiter)
			queries = append(queries, waiter.query)
		}
	}
	if len(active) > 0 {
		// Cancellation of one waiter must not cancel another tier's proof.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(active[0].ctx), 5*time.Second)
		results := g.readRefProofs(ctx, queries)
		cancel()
		for index, waiter := range active {
			waiter.done <- results[index]
		}
	}
	g.refsMu.Lock()
	g.refsInFlight -= len(batch)
	g.refsMu.Unlock()
}

func (g *rbacRecoveryGit) readRefProofs(ctx context.Context, queries []*gitv1.RepositoryRef) []recoveryRefResult {
	response, err := g.client.Git.ResolveRefs(ctx, &gitv1.ResolveRefsRequest{Refs: queries}, grpc.MaxCallRecvMsgSize(64<<10))
	if err == nil {
		if response == nil || len(response.Proofs) != len(queries) {
			err = rbac.ErrUnavailable
		} else {
			for index, proof := range response.Proofs {
				query := queries[index]
				if proof == nil || proof.RepositoryId != query.RepositoryId || proof.Ref != query.Ref ||
					proof.CommitSha != "" && (!recoveryCommit(proof.CommitSha) || proof.Busy) {
					err = rbac.ErrUnavailable
					break
				}
			}
		}
	}
	results := make([]recoveryRefResult, len(queries))
	for index := range queries {
		results[index].err = err
		if err == nil {
			results[index].commit = response.Proofs[index].CommitSha
			if results[index].commit == "" {
				results[index].err = grpcstatus.Error(codes.NotFound, "canonical ref unavailable")
				if response.Proofs[index].Busy {
					results[index].err = rbac.ErrOriginRefBusy
				}
			}
		}
	}
	return results
}

func (g *rbacRecoveryGit) CreateRepository(ctx context.Context, id, storageClass string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	repository, namespace, err := g.canonical(ctx, id)
	if err != nil || storageClass != repository.StorageClass {
		return "", rbac.ErrUnavailable
	}
	authorization, err := recoveryAuthorization(ctx, id, "repository.create")
	if err != nil {
		return "", err
	}
	response, err := g.client.Git.CreateRepository(ctx, &gitv1.CreateRepositoryRequest{
		RepositoryId: id, StorageClass: storageClass, Authorization: authorization,
	})
	if err != nil && grpcstatus.Code(err) != codes.AlreadyExists {
		return "", err
	}
	if err == nil && response == nil {
		return "", rbac.ErrUnavailable
	}
	if _, err := g.ResolveRef(ctx, id, "refs/heads/"+repository.DefaultBranch); err != nil {
		if grpcstatus.Code(err) != codes.NotFound {
			return "", err
		}
		capable, ok := g.store.(datastore.RBACCapable)
		if !ok || capable.RBACStore() == nil {
			return "", rbac.ErrUnavailable
		}
		tier := namespace.UID
		if namespace.Name == "gitstore-system" {
			tier = rbac.ClusterTierID
		}
		if state, err := capable.RBACStore().ReadRBACTier(ctx, tier); err != nil || state.Revision != 0 {
			return "", rbac.ErrUnavailable
		}
		authorization, err := recoveryAuthorization(ctx, id, "repository.contents.write")
		if err != nil {
			return "", err
		}
		_, err = g.client.Git.CommitFileAtRevision(ctx, &gitv1.CommitFileAtRevisionRequest{
			File: &gitv1.CommitFileRequest{RepositoryId: id, Path: ".gitkeep",
				AuthorName: bootstrapActor, CommitMessage: "Initialize system repository", Authorization: authorization},
			RefName: "refs/heads/" + repository.DefaultBranch, ExpectedCommitSha: strings.Repeat("0", 40),
		})
		if grpcstatus.Code(err) == codes.Aborted {
			_, err = g.ResolveRef(ctx, id, "refs/heads/"+repository.DefaultBranch)
		}
		if err != nil {
			return "", err
		}
	}
	if response != nil {
		return response.StoragePath, nil
	}
	return "", nil
}

func (g *rbacRecoveryGit) CommitFileAtRevisionForRepo(ctx context.Context, id string, params gitclient.CommitFileAtRevisionParams) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	repository, namespace, err := g.canonical(ctx, id)
	if err != nil || namespace.Name != "gitstore-system" {
		return "", rbac.ErrUnavailable
	}
	receipt, err := datastore.ReadNamespaceRBACBootstrap(namespace)
	if err != nil || receipt == nil || receipt.Commit != "" || receipt.RepositoryID != id ||
		receipt.GitRef != "refs/heads/"+repository.DefaultBranch ||
		params.Path != "rbac/clusterrolebindings/initial-admin.yaml" ||
		params.RefName != receipt.GitRef || params.ExpectedCommitSHA != receipt.ExpectedCommit || !params.RetainReceipt ||
		params.AuthorName != bootstrapActor || params.AuthorEmail != "" || params.CommitMessage != "Initialize RBAC for "+namespace.UID {
		return "", rbac.ErrUnavailable
	}
	content, err := initialAdministratorManifest(receipt.Subject)
	if err != nil || !bytes.Equal(params.Content, content) {
		return "", rbac.ErrUnavailable
	}
	authorization, err := recoveryAuthorization(ctx, id, "repository.contents.write")
	if err != nil {
		return "", err
	}
	response, err := g.client.Git.CommitFileAtRevision(ctx, &gitv1.CommitFileAtRevisionRequest{
		File: &gitv1.CommitFileRequest{RepositoryId: id, Path: params.Path, Content: content,
			CommitMessage: params.CommitMessage, AuthorName: bootstrapActor, Authorization: authorization},
		RefName: params.RefName, ExpectedCommitSha: params.ExpectedCommitSHA, RetainReceipt: true, ReceiptOnly: params.ReceiptOnly,
	})
	if err != nil {
		return "", err
	}
	if response == nil || !recoveryCommit(response.CommitSha) {
		return "", rbac.ErrUnavailable
	}
	return response.CommitSha, nil
}
