// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
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

func (g *rbacRecoveryGit) ReadFile(ctx context.Context, id, path, commit string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, namespace, err := g.canonical(ctx, id)
	if err != nil || !recoveryPath(path, namespace.Name == "gitstore-system") || !recoveryCommit(commit) {
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
	authorization, err := recoveryAuthorization(ctx, id, "repository.contents.read")
	if err != nil {
		return "", err
	}
	response, err := g.client.Git.ListFiles(ctx, &gitv1.ListFilesRequest{
		RepositoryId: id, Ref: ref, Authorization: authorization,
	}, grpc.MaxCallRecvMsgSize(8<<20))
	if err != nil {
		return "", err
	}
	if response == nil || !recoveryCommit(response.RefCommitSha) {
		return "", rbac.ErrUnavailable
	}
	return response.RefCommitSha, nil
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
