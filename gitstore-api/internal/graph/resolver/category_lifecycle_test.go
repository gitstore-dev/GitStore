// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// memGit is an in-memory Git service: every commit snapshots the tree of
// its repository and advances refs/heads/main. It serves both the GraphQL
// writer and the admission reader, like the real Git service.
type memGit struct {
	mu       sync.Mutex
	seq      int
	heads    map[string]string
	trees    map[string]map[string][]byte // commit -> path -> content
	commits  []gitclient.CommitFileParams
	deletes  []gitclient.DeleteFileParams
	receipts map[string]string
	// afterWrite runs after each commit, outside the lock; tests use it to
	// race a concurrent commit in before admission.
	afterWrite func(repositoryID string)
}

func newMemGit() *memGit {
	return &memGit{heads: map[string]string{}, trees: map[string]map[string][]byte{}, receipts: map[string]string{}}
}

func (g *memGit) write(repositoryID, path string, content []byte) string {
	g.mu.Lock()
	g.seq++
	sha := fmt.Sprintf("%040x", g.seq)
	tree := map[string][]byte{}
	for p, c := range g.trees[g.heads[repositoryID]] {
		tree[p] = c
	}
	if content == nil {
		delete(tree, path)
	} else {
		tree[path] = append([]byte(nil), content...)
	}
	g.trees[sha] = tree
	g.heads[repositoryID] = sha
	hook := g.afterWrite
	g.mu.Unlock()
	if hook != nil {
		hook(repositoryID)
	}
	return sha
}

func (g *memGit) resolve(repositoryID, ref string) string {
	if strings.HasPrefix(ref, "refs/") || ref == "" || ref == "HEAD" {
		return g.heads[repositoryID]
	}
	return ref
}

func (g *memGit) CreateRepository(context.Context, string, string) (string, error) { return "", nil }
func (g *memGit) DeleteRepository(context.Context, string) error                   { return nil }
func (g *memGit) CommitFile(context.Context, gitclient.CommitFileParams) (string, error) {
	return "", errors.New("unscoped commit")
}
func (g *memGit) CommitFileForRepo(_ context.Context, repositoryID string, p gitclient.CommitFileParams) (string, error) {
	g.mu.Lock()
	g.commits = append(g.commits, p)
	g.mu.Unlock()
	return g.write(repositoryID, p.Path, p.Content), nil
}
func (g *memGit) ResolveRefForRepo(_ context.Context, repositoryID, ref string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.resolve(repositoryID, ref), nil
}
func (g *memGit) ReadFileForRepo(_ context.Context, repositoryID, path, ref string) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	content, ok := g.trees[g.resolve(repositoryID, ref)][path]
	if !ok {
		return nil, status.Error(codes.NotFound, "file not found")
	}
	return append([]byte(nil), content...), nil
}
func (g *memGit) DeleteFile(context.Context, gitclient.DeleteFileParams) (string, error) {
	return "", errors.New("unscoped delete")
}
func (g *memGit) DeleteFileForRepo(_ context.Context, repositoryID string, p gitclient.DeleteFileParams) (string, error) {
	g.mu.Lock()
	key := repositoryID + "\x00" + p.RefName + "\x00" + p.Path + "\x00" + p.ExpectedCommitSHA
	if p.ExpectedCommitSHA != "" && g.heads[repositoryID] != p.ExpectedCommitSHA {
		receipt := g.receipts[key]
		_, exists := g.trees[g.heads[repositoryID]][p.Path]
		g.mu.Unlock()
		if receipt != "" && !exists {
			return receipt, nil
		}
		if receipt != "" {
			return "", status.Error(codes.FailedPrecondition, "manifest restored after removal")
		}
		return "", status.Error(codes.Aborted, "deletion revision conflict")
	}
	g.deletes = append(g.deletes, p)
	g.seq++
	sha := fmt.Sprintf("%040x", g.seq)
	tree := map[string][]byte{}
	for path, content := range g.trees[g.heads[repositoryID]] {
		if path != p.Path {
			tree[path] = content
		}
	}
	g.trees[sha], g.heads[repositoryID] = tree, sha
	if p.ExpectedCommitSHA != "" {
		g.receipts[key] = sha
	}
	hook := g.afterWrite
	g.mu.Unlock()
	if hook != nil {
		hook(repositoryID)
	}
	return sha, nil
}
func (g *memGit) CreateTag(context.Context, gitclient.CreateTagParams) (string, error) {
	return "", nil
}

// cataloggrpc.GitReader
func (g *memGit) ListFiles(_ context.Context, repositoryID, prefix, ref string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var paths []string
	for p := range g.trees[g.resolve(repositoryID, ref)] {
		if strings.HasPrefix(p, prefix) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}
func (g *memGit) ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	return g.ReadFileForRepo(ctx, repositoryID, path, ref)
}
func (g *memGit) ResolveRef(ctx context.Context, repositoryID, ref string) (string, error) {
	return g.ResolveRefForRepo(ctx, repositoryID, ref)
}

func (g *memGit) commitCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.commits)
}

const (
	lifecycleNamespace    = "acme"
	lifecycleSystemRepoID = "00000000-0000-0000-0000-00000000a001"
	lifecycleCatalogRepo  = "00000000-0000-0000-0000-00000000a002"
)

type categoryLifecycleEnv struct {
	store   datastore.Datastore
	git     *memGit
	catalog *cataloggrpc.Server
	service *Service
	mut     *mutationResolver
}

// denyTitledPolicy denies categories titled "Denied" after commit.
type denyTitledPolicy struct{}

func (denyTitledPolicy) Name() string { return "test-deny-titled" }
func (denyTitledPolicy) Validate(_ context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if res, ok := req.Object.(*catalog.CategoryTaxonomyResource); ok && res.Spec.Title == "Denied" {
		return admission.DecisionDeny("title Denied is not allowed", "spec.title")
	}
	return admission.DecisionAllow()
}

func newCategoryLifecycleEnv(t *testing.T) *categoryLifecycleEnv {
	t.Helper()
	ctx := context.Background()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "00000000-0000-0000-0000-00000000a000", Name: lifecycleNamespace, Tier: datastore.NamespaceTierUser}))
	for id, name := range map[string]string{lifecycleSystemRepoID: SystemRepositoryName, lifecycleCatalogRepo: "catalog"} {
		require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: id, ID: id, RepositoryID: id, Namespace: lifecycleNamespace, Name: name}))
		require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: lifecycleNamespace, Name: name, RepositoryID: id}))
	}
	git := newMemGit()
	catalogServer, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store: store, GitReader: git, Logger: zap.NewNop(),
		ExtraValidatingPolicies: []admission.ValidatingAdmissionPolicy{denyTitledPolicy{}},
	})
	require.NoError(t, err)
	service, err := NewService(ServiceDeps{Store: store, GitWriter: git, Logger: zap.NewNop(), CommittedManifestAdmitter: catalogServer})
	require.NoError(t, err)
	return &categoryLifecycleEnv{
		store: store, git: git, catalog: catalogServer, service: service,
		mut: &mutationResolver{Resolver: &Resolver{service: service, store: store, logger: zap.NewNop()}},
	}
}

func categoryInput(name, title string, parent string) CategoryManifestInput {
	spec := &model.CategorySpecInput{Title: title}
	if parent != "" {
		spec.ParentRef = &model.CatalogObjectReferenceInput{Name: parent}
	}
	return CategoryManifestInput{
		APIVersion: categoryAPIVersion, Kind: categoryKind,
		Metadata: &model.ObjectMetaInput{Name: name, Namespace: lifecycleNamespace},
		Spec:     spec,
	}
}

// pushCategory commits a manifest straight to a repository and admits it as a
// push would, so tests can set up push-authored provenance.
func (e *categoryLifecycleEnv) pushCategory(t *testing.T, repositoryID, path, name, title, parent, body string) *datastore.CategoryTaxonomy {
	t.Helper()
	manifest := "---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: CategoryTaxonomy\nmetadata:\n  name: " + name +
		"\n  namespace: " + lifecycleNamespace + "\nspec:\n  title: " + title + "\n"
	if parent != "" {
		manifest += "  parentRef:\n    name: " + parent + "\n"
	}
	manifest += "---\n" + body
	sha := e.git.write(repositoryID, path, []byte(manifest))
	_, err := e.catalog.AdmitCommittedManifest(context.Background(), admission.CommittedManifestRequest{
		RepositoryID: repositoryID, Namespace: lifecycleNamespace, ActorSubject: "pusher",
		CommitSHA: sha, RefName: "refs/heads/main", Path: path, Content: []byte(manifest), Operation: admission.OperationCreate,
	})
	require.NoError(t, err)
	record, err := e.store.GetCategoryTaxonomyByName(context.Background(), lifecycleNamespace, name)
	require.NoError(t, err)
	return record
}

func requireCategoryError(t *testing.T, err error, code admission.Code, phase admission.Phase, reason string) *admission.Error {
	t.Helper()
	var admissionErr *admission.Error
	require.True(t, errors.As(err, &admissionErr), "want *admission.Error, got %v", err)
	assert.Equal(t, code, admissionErr.Code)
	assert.Equal(t, phase, admissionErr.Phase)
	require.NotEmpty(t, admissionErr.Diagnostics)
	assert.Equal(t, reason, admissionErr.Diagnostics[0].Reason)
	return admissionErr
}

func categoryConditionStatus(t *testing.T, c *datastore.CategoryTaxonomy, conditionType string) string {
	t.Helper()
	var s catalog.CategoryTaxonomyStatus
	require.NoError(t, json.Unmarshal(c.Status, &s))
	for _, condition := range s.Conditions {
		if condition.Type == conditionType {
			return string(condition.Status)
		}
	}
	return ""
}

// ── createCategory ───────────────────────────────────────────────────────────

func TestCreateCategoryCommitsToSystemRepositoryAndAdmits(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	body := "Devices.\n"
	input := categoryInput("electronics", "Electronics", "")
	input.Body = &body

	category, err := env.service.CommitCategoryManifest(context.Background(), input, "alice", true)
	require.NoError(t, err)
	require.Len(t, env.git.commits, 1)
	commit := env.git.commits[0]
	assert.Equal(t, "categories/electronics.md", commit.Path)
	assert.Equal(t, "Create CategoryTaxonomy electronics", commit.CommitMessage)
	assert.Equal(t, "alice", commit.AuthorName)
	assert.True(t, strings.HasSuffix(string(commit.Content), "---\nDevices.\n"))
	assert.Equal(t, lifecycleSystemRepoID, category.RepositoryID)
	assert.Equal(t, "categories/electronics.md", category.SourcePath)
	assert.Equal(t, env.git.heads[lifecycleSystemRepoID], category.GitCommitSHA, "the record comes from the mutation's own commit")
	assert.Equal(t, "Devices.\n", category.Body)

	child, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("computers", "Computers", "electronics"), "alice", true)
	require.NoError(t, err)
	assert.Equal(t, "True", categoryConditionStatus(t, child, catalog.ConditionParentResolved))
}

func TestCreateCategoryExistingNameIsAlreadyExistsWithoutCommit(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("electronics", "Electronics", ""), "alice", true)
	require.NoError(t, err)
	_, err = env.service.CommitCategoryManifest(context.Background(), categoryInput("electronics", "Electronics", ""), "alice", true)
	requireCategoryError(t, err, admission.CodeAlreadyExists, "", "CATEGORY_ALREADY_EXISTS")
	assert.Equal(t, 1, env.git.commitCount())
}

func TestCreateCategoryPreReceiveDenialWritesNoCommit(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops", "laptops"), "alice", true)
	admissionErr := requireCategoryError(t, err, admission.CodeAdmissionRejected, admission.PhasePreReceive, "SELF_PARENT")
	assert.Empty(t, admissionErr.CommitSHA)
	assert.Equal(t, "categories/laptops.md", admissionErr.Diagnostics[0].File)
	assert.Zero(t, env.git.commitCount())

	gqlErr := admissionErr.ToGQLError()
	assert.Equal(t, "PRE_RECEIVE", gqlErr.Extensions["phase"])
	assert.NotContains(t, gqlErr.Extensions, "commit")
}

func TestCreateCategoryPostReceiveDenialReportsCommitAndLeavesNoRecord(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Denied", ""), "alice", true)
	admissionErr := requireCategoryError(t, err, admission.CodeAdmissionRejected, admission.PhasePostReceive, "POLICY_DENIED")
	assert.Equal(t, env.git.heads[lifecycleSystemRepoID], admissionErr.CommitSHA)
	assert.Equal(t, 1, env.git.commitCount())
	_, err = env.store.GetCategoryTaxonomyByName(context.Background(), lifecycleNamespace, "laptops")
	assert.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestCreateCategoryUnknownParentIsAdmittedUnresolved(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	category, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops", "missing"), "alice", true)
	require.NoError(t, err)
	assert.Equal(t, "False", categoryConditionStatus(t, category, catalog.ConditionParentResolved))
}

func TestIdenticalCategoryResubmitDoesNotCommit(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	created, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("electronics", "Electronics", ""), "alice", true)
	require.NoError(t, err)
	updated, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("electronics", "Electronics", ""), "alice", false)
	require.NoError(t, err)
	assert.Equal(t, 1, env.git.commitCount(), "a byte-identical manifest must not be committed again")
	assert.Equal(t, created.Generation, updated.Generation)
	assert.Equal(t, created.GitCommitSHA, updated.GitCommitSHA)
}

func TestCreateCategoryRetryAfterUnadmittedCommitAdmitsHead(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	input := categoryInput("electronics", "Electronics", "")
	labels, _ := stringMap(input.Metadata.Labels)
	annotations, _ := stringMap(input.Metadata.Annotations)
	content, err := renderManifest(categoryManifestEnvelope(input.Metadata, labels, annotations, input.Spec), nil)
	require.NoError(t, err)
	head := env.git.write(lifecycleSystemRepoID, "categories/electronics.md", content) // committed, never admitted

	category, err := env.service.CommitCategoryManifest(context.Background(), input, "alice", true)
	require.NoError(t, err)
	assert.Zero(t, env.git.commitCount(), "the retry must not commit again")
	assert.Equal(t, head, category.GitCommitSHA)
}

// ── updateCategory ───────────────────────────────────────────────────────────

func TestUpdateCategoryCommitsToStoredProvenanceAndPreservesBody(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	env.pushCategory(t, lifecycleSystemRepoID, "categories/electronics.md", "electronics", "Electronics", "", "")
	env.pushCategory(t, lifecycleSystemRepoID, "categories/office.md", "office", "Office", "", "")
	env.pushCategory(t, lifecycleCatalogRepo, "categories/laptops.md", "laptops", "Laptops", "electronics", "Portable computers.\n")

	updated, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops", "office"), "bob", false)
	require.NoError(t, err)
	require.Len(t, env.git.commits, 1)
	assert.Equal(t, "categories/laptops.md", env.git.commits[0].Path)
	assert.Equal(t, "Update CategoryTaxonomy laptops", env.git.commits[0].CommitMessage)
	assert.Equal(t, lifecycleCatalogRepo, updated.RepositoryID, "an update writes where the category was admitted from")
	assert.Equal(t, env.git.heads[lifecycleCatalogRepo], updated.GitCommitSHA)
	assert.Equal(t, "office", updated.ParentName)
	assert.Equal(t, "Portable computers.\n", updated.Body, "an omitted body is preserved")

	body := "Replaced.\n"
	input := categoryInput("laptops", "Laptops", "office")
	input.Body = &body
	replaced, err := env.service.CommitCategoryManifest(context.Background(), input, "bob", false)
	require.NoError(t, err)
	assert.Equal(t, "Replaced.\n", replaced.Body)
}

func TestUpdateCategoryRejections(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("missing", "Missing", ""), "bob", false)
		requireCategoryError(t, err, admission.CodeNotFound, "", "CATEGORY_NOT_FOUND")
	})
	t.Run("terminating", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		record := env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")
		_, err := env.store.(datastore.CategoryTaxonomyDeletionStore).MarkCategoryTaxonomyDeletion(context.Background(), record.Namespace, record.Name, record.ResourceVersion, record.CreationTimestamp)
		require.NoError(t, err)
		_, err = env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops 2", ""), "bob", false)
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "CATEGORY_TERMINATING")
		assert.Zero(t, env.git.commitCount())
	})
	t.Run("missing provenance", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		record := env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")
		record.SourcePath = ""
		require.NoError(t, env.store.UpdateCategoryTaxonomy(context.Background(), record))
		_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops 2", ""), "bob", false)
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "PROVENANCE_UNAVAILABLE")
		assert.Zero(t, env.git.commitCount())
	})
	t.Run("admitted from another ref", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		record := env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")
		record.GitRef = "refs/heads/feature"
		require.NoError(t, env.store.UpdateCategoryTaxonomy(context.Background(), record))
		_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops 2", ""), "bob", false)
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "PROVENANCE_UNAVAILABLE")
		_, _, err = env.service.DeleteCategoryManifest(context.Background(), record.UID, "bob")
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "PROVENANCE_UNAVAILABLE")
		assert.Zero(t, env.git.commitCount())
		assert.Empty(t, env.git.deletes)
	})
	t.Run("owner annotation change", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")
		input := categoryInput("laptops", "Laptops", "")
		input.Metadata.Annotations = map[string]any{datastore.OwnerAnnotationKey: "mallory"}
		_, err := env.service.CommitCategoryManifest(context.Background(), input, "bob", false)
		requireCategoryError(t, err, admission.CodeAdmissionRejected, admission.PhasePreReceive, "IMMUTABLE_OWNER")
		assert.Zero(t, env.git.commitCount())
	})
	t.Run("superseded by a concurrent commit", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")
		raced := false
		env.git.afterWrite = func(repositoryID string) {
			if raced {
				return
			}
			raced = true
			env.git.write(repositoryID, "categories/laptops.md", []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: CategoryTaxonomy\nmetadata:\n  name: laptops\n  namespace: acme\nspec:\n  title: Someone Else\n---\n"))
		}
		_, err := env.service.CommitCategoryManifest(context.Background(), categoryInput("laptops", "Laptops 2", ""), "bob", false)
		requireCategoryError(t, err, admission.CodeConflict, "", "SUPERSEDED")
	})
}

func TestCategoryMutationResolversRenderSharedEnvelope(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	input := categoryInput("laptops", "Laptops", "laptops")
	_, err := env.mut.CreateCategory(context.Background(), model.CreateCategoryInput{
		APIVersion: input.APIVersion, Kind: input.Kind, Metadata: input.Metadata, Spec: input.Spec,
	})
	var gqlErr *gqlerror.Error
	require.ErrorAs(t, err, &gqlErr)
	assert.Equal(t, "ADMISSION_REJECTED", gqlErr.Extensions["code"])
	assert.Equal(t, "PRE_RECEIVE", gqlErr.Extensions["phase"])

	ok := categoryInput("electronics", "Electronics", "")
	payload, err := env.mut.CreateCategory(context.Background(), model.CreateCategoryInput{
		APIVersion: ok.APIVersion, Kind: ok.Kind, Metadata: ok.Metadata, Spec: ok.Spec,
	})
	require.NoError(t, err)
	assert.Equal(t, "electronics", payload.Category.Metadata.Name)
}

// ── deleteCategory ───────────────────────────────────────────────────────────

func TestDeleteCategoryRemovesManifestAndStartsTermination(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	record := env.pushCategory(t, lifecycleCatalogRepo, "categories/laptops.md", "laptops", "Laptops", "", "")

	category, outcome, err := env.service.DeleteCategoryManifest(context.Background(), record.UID, "bob")
	require.NoError(t, err)
	assert.Equal(t, model.ResourceDeletionOutcomeTerminationStarted, outcome)
	require.NotNil(t, category.DeletionTimestamp)
	require.Len(t, env.git.deletes, 1)
	assert.Equal(t, "categories/laptops.md", env.git.deletes[0].Path)
	assert.Equal(t, "Delete CategoryTaxonomy laptops", env.git.deletes[0].CommitMessage)
	assert.Equal(t, "bob", env.git.deletes[0].AuthorName)
	_, err = env.git.ReadFileForRepo(context.Background(), lifecycleCatalogRepo, "categories/laptops.md", "refs/heads/main")
	assert.Equal(t, codes.NotFound, status.Code(err), "the manifest is removed from Git")

	again, outcome, err := env.service.DeleteCategoryManifest(context.Background(), record.UID, "bob")
	require.NoError(t, err)
	assert.Equal(t, model.ResourceDeletionOutcomeAlreadyTerminating, outcome)
	assert.Equal(t, category.UID, again.UID)
	assert.Len(t, env.git.deletes, 1, "an already-terminating category writes no commit")

	payload, err := env.mut.DeleteCategory(context.Background(), model.DeleteCategoryInput{ID: mustEncodeNodeID(nodeKindCategory, record.UID)})
	require.NoError(t, err)
	assert.Equal(t, model.ResourceDeletionOutcomeAlreadyTerminating, payload.Outcome)
	assert.Equal(t, mustEncodeNodeID(nodeKindCategory, record.UID), payload.Category.ID)
}

func blockingChild(t *testing.T, env *categoryLifecycleEnv, parent *datastore.CategoryTaxonomy, repositoryID string) {
	t.Helper()
	child := env.pushCategory(t, repositoryID, "categories/child-"+repositoryID[len(repositoryID)-4:]+".md", "child-"+repositoryID[len(repositoryID)-4:], "Child", "", "")
	refs, err := json.Marshal([]catalog.OwnerReference{{APIVersion: categoryAPIVersion, Kind: categoryKind, Name: parent.Name, UID: parent.UID, BlockOwnerDeletion: true}})
	require.NoError(t, err)
	child.OwnerReferences = refs
	require.NoError(t, env.store.UpdateCategoryTaxonomy(context.Background(), child))
}

func TestDeleteCategoryChildInAnotherRepositoryDoesNotBlock(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	parent := env.pushCategory(t, lifecycleSystemRepoID, "categories/electronics.md", "electronics", "Electronics", "", "")
	blockingChild(t, env, parent, lifecycleCatalogRepo)
	_, outcome, err := env.service.DeleteCategoryManifest(context.Background(), parent.UID, "bob")
	require.NoError(t, err)
	assert.Equal(t, model.ResourceDeletionOutcomeTerminationStarted, outcome, "dependents are scoped to the owner's repository")
}

func TestDeleteCategoryWithChildrenIsBlockedWithoutCommit(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	parent := env.pushCategory(t, lifecycleSystemRepoID, "categories/electronics.md", "electronics", "Electronics", "", "")
	env.pushCategory(t, lifecycleSystemRepoID, "categories/computers.md", "computers", "Computers", "electronics", "")

	_, _, err := env.service.DeleteCategoryManifest(context.Background(), parent.UID, "bob")
	requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "CHILD_CATEGORIES_PRESENT")
	assert.Empty(t, env.git.deletes)
}

// ── completeCategoryDeletion ─────────────────────────────────────────────────

func terminatingCategory(t *testing.T, env *categoryLifecycleEnv, name string) *datastore.CategoryTaxonomy {
	t.Helper()
	record := env.pushCategory(t, lifecycleSystemRepoID, "categories/"+name+".md", name, name, "", "")
	_, _, err := env.service.DeleteCategoryManifest(context.Background(), record.UID, "bob")
	require.NoError(t, err)
	terminating, err := env.store.GetCategoryTaxonomyByName(context.Background(), lifecycleNamespace, name)
	require.NoError(t, err)
	return terminating
}

type categoryCompletionRaceStore struct {
	datastore.Datastore
	datastore.OwnerReferenceStore
	datastore.CategoryTaxonomyDeletionStore
	beforeComplete func()
}

func (s *categoryCompletionRaceStore) CompleteCategoryTaxonomyDeletion(ctx context.Context, namespace, name, resourceVersion, expectedUID string) (*datastore.CategoryTaxonomy, error) {
	s.beforeComplete()
	return s.CategoryTaxonomyDeletionStore.CompleteCategoryTaxonomyDeletion(ctx, namespace, name, resourceVersion, expectedUID)
}

func TestCompleteCategoryDeletion(t *testing.T) {
	t.Run("replacement between service check and atomic completion is fenced", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		ctx := t.Context()
		old := terminatingCategory(t, env, "laptops")
		replacement := *old
		replacement.UID = "00000000-0000-0000-0000-00000000b003"
		lifecycle := env.store.(datastore.CategoryTaxonomyDeletionStore)
		env.service.store = &categoryCompletionRaceStore{
			Datastore: env.store, OwnerReferenceStore: env.store.(datastore.OwnerReferenceStore), CategoryTaxonomyDeletionStore: lifecycle,
			beforeComplete: func() {
				_, err := lifecycle.CompleteCategoryTaxonomyDeletion(ctx, old.Namespace, old.Name, old.ResourceVersion, old.UID)
				require.NoError(t, err)
				require.NoError(t, env.store.CreateCategoryTaxonomy(ctx, &replacement))
			},
		}
		_, err := env.mut.CompleteCategoryDeletion(ctx, model.CompleteCategoryDeletionInput{
			ID: mustEncodeNodeID(nodeKindCategory, old.UID), Namespace: old.Namespace, Name: old.Name, ResourceVersion: old.ResourceVersion,
		})
		var gqlErr *gqlerror.Error
		require.ErrorAs(t, err, &gqlErr)
		require.Equal(t, "CONFLICT", gqlErr.Extensions["code"])
		current, err := env.store.GetCategoryTaxonomyByName(ctx, old.Namespace, old.Name)
		require.NoError(t, err)
		require.Equal(t, replacement.UID, current.UID)
		require.Equal(t, replacement.ResourceVersion, current.ResourceVersion)
		require.Equal(t, replacement.Finalizers, current.Finalizers)
	})
	t.Run("same name and version replacement rejects stale identity", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		ctx := context.Background()
		old := terminatingCategory(t, env, "laptops")
		_, err := env.service.CompleteCategoryDeletion(ctx, old.Namespace, old.Name, old.ResourceVersion, old.UID)
		require.NoError(t, err)
		replacement := *old
		replacement.UID = "00000000-0000-0000-0000-00000000b002"
		require.NoError(t, env.store.CreateCategoryTaxonomy(ctx, &replacement))
		_, err = env.mut.CompleteCategoryDeletion(ctx, model.CompleteCategoryDeletionInput{
			ID: mustEncodeNodeID(nodeKindCategory, old.UID), Namespace: old.Namespace, Name: old.Name, ResourceVersion: old.ResourceVersion,
		})
		var gqlErr *gqlerror.Error
		require.ErrorAs(t, err, &gqlErr)
		require.Equal(t, "CONFLICT", gqlErr.Extensions["code"])
		current, err := env.store.GetCategoryTaxonomyByName(ctx, replacement.Namespace, replacement.Name)
		require.NoError(t, err)
		require.Equal(t, replacement.UID, current.UID)
		require.Equal(t, replacement.ResourceVersion, current.ResourceVersion)
		require.Equal(t, replacement.Finalizers, current.Finalizers)
		_, err = env.service.CompleteCategoryDeletion(ctx, replacement.Namespace, replacement.Name, replacement.ResourceVersion, old.UID)
		require.ErrorIs(t, err, datastore.ErrConflict)
		payload, err := env.mut.CompleteCategoryDeletion(ctx, model.CompleteCategoryDeletionInput{
			ID: mustEncodeNodeID(nodeKindCategory, replacement.UID), Namespace: replacement.Namespace, Name: replacement.Name, ResourceVersion: replacement.ResourceVersion,
		})
		require.NoError(t, err)
		require.Equal(t, mustEncodeNodeID(nodeKindCategory, replacement.UID), *payload.ID)
	})
	t.Run("success removes the record", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		category := terminatingCategory(t, env, "laptops")
		payload, err := env.mut.CompleteCategoryDeletion(context.Background(), model.CompleteCategoryDeletionInput{
			ID:        mustEncodeNodeID(nodeKindCategory, category.UID),
			Namespace: lifecycleNamespace, Name: "laptops", ResourceVersion: category.ResourceVersion,
		})
		require.NoError(t, err)
		require.NotNil(t, payload.ID)
		assert.Equal(t, mustEncodeNodeID(nodeKindCategory, category.UID), *payload.ID)
		_, err = env.store.GetCategoryTaxonomyByName(context.Background(), lifecycleNamespace, "laptops")
		assert.ErrorIs(t, err, datastore.ErrNotFound)
	})
	t.Run("resource version mismatch", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		category := terminatingCategory(t, env, "laptops")
		_, err := env.mut.CompleteCategoryDeletion(context.Background(), model.CompleteCategoryDeletionInput{
			ID:        mustEncodeNodeID(nodeKindCategory, category.UID),
			Namespace: lifecycleNamespace, Name: "laptops", ResourceVersion: "stale",
		})
		var gqlErr *gqlerror.Error
		require.ErrorAs(t, err, &gqlErr)
		assert.Equal(t, "CONFLICT", gqlErr.Extensions["code"])
	})
	t.Run("not terminating", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		record := env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")
		_, err := env.service.CompleteCategoryDeletion(context.Background(), lifecycleNamespace, "laptops", record.ResourceVersion, record.UID)
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "CATEGORY_NOT_TERMINATING")
	})
	t.Run("children present", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		category := terminatingCategory(t, env, "electronics")
		// A blocking child that appeared after termination started.
		blockingChild(t, env, category, lifecycleSystemRepoID)
		_, err := env.service.CompleteCategoryDeletion(context.Background(), lifecycleNamespace, "electronics", category.ResourceVersion, category.UID)
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "CHILD_CATEGORIES_PRESENT")
	})
	t.Run("products still to decouple", func(t *testing.T) {
		env := newCategoryLifecycleEnv(t)
		category := terminatingCategory(t, env, "laptops")
		refs, err := json.Marshal([]catalog.OwnerReference{{APIVersion: categoryAPIVersion, Kind: categoryKind, Name: category.Name, UID: category.UID}})
		require.NoError(t, err)
		require.NoError(t, env.store.CreateProduct(context.Background(), &datastore.Product{
			UID: "00000000-0000-0000-0000-00000000b001", Namespace: lifecycleNamespace, RepositoryID: category.RepositoryID, Name: "widget",
			ResourceVersion: "1", OwnerReferences: refs, Spec: []byte(`{"categoryRef":{"name":"laptops"}}`),
		}))
		_, err = env.service.CompleteCategoryDeletion(context.Background(), lifecycleNamespace, "laptops", category.ResourceVersion, category.UID)
		requireCategoryError(t, err, admission.CodeFailedPrecondition, "", "PRODUCT_DECOUPLING_INCOMPLETE")
	})
}
