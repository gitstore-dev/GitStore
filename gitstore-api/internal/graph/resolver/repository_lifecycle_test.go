// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
)

func TestRepositoryLifecycleSchemaContract(t *testing.T) {
	schema := namespaceContractSchema(t)

	for _, inputName := range []string{"CreateRepositoryInput", "UpdateRepositoryInput"} {
		for field, fieldType := range map[string]string{
			"apiVersion": "String!",
			"kind":       "String!",
			"metadata":   "MetadataInput!",
			"spec":       "RepositorySpecInput!",
		} {
			requireGraphQLField(t, schema, inputName, field, fieldType)
		}
		assert.Equal(t, "gitstore.dev/v1beta1", schema.Types[inputName].Fields.ForName("apiVersion").DefaultValue.Raw)
		assert.Equal(t, "Repository", schema.Types[inputName].Fields.ForName("kind").DefaultValue.Raw)
	}

	requireGraphQLField(t, schema, "MetadataInput", "name", "String!")
	requireGraphQLField(t, schema, "MetadataInput", "namespace", "String!")
	assert.Nil(t, schema.Types["RepositoryMetadataInput"], "Repository must use the shared MetadataInput envelope")
	requireGraphQLField(t, schema, "Mutation", "updateRepository", "UpdateRepositoryPayload!")
	requireGraphQLField(t, schema, "Mutation", "updateRepositoryStatus", "UpdateRepositoryStatusPayload!")
	requireGraphQLField(t, schema, "Mutation", "provisionRepositoryStorage", "ProvisionRepositoryStoragePayload!")
	requireGraphQLField(t, schema, "ProvisionRepositoryStorageInput", "namespace", "String!")
	requireGraphQLField(t, schema, "ProvisionRepositoryStorageInput", "name", "String!")
	requireGraphQLField(t, schema, "Mutation", "completeRepositoryDeletion", "CompleteRepositoryDeletionPayload!")
	requireGraphQLField(t, schema, "CompleteRepositoryDeletionInput", "namespace", "String!")
	requireGraphQLField(t, schema, "CompleteRepositoryDeletionInput", "name", "String!")
	requireGraphQLField(t, schema, "CompleteRepositoryDeletionInput", "resourceVersion", "String!")

	for _, fieldName := range []string{"renameRepository", "transferRepository"} {
		field := requireGraphQLField(t, schema, "Mutation", fieldName, schema.Types["Mutation"].Fields.ForName(fieldName).Type.String())
		require.NotNil(t, field.Directives.ForName("deprecated"), "%s must be deprecated", fieldName)
	}
}

func TestRepositoryLifecycleResultKeepsEncodedUIDIdentity(t *testing.T) {
	repository, namespace := repositoryContractFixture()
	result := datastoreRepositoryToModel(repository, namespace, "/data")
	require.NotNil(t, result)
	assert.Equal(t, result.ID, result.Metadata.UID)
	assert.NotEqual(t, repository.UID, result.Metadata.UID, "GraphQL metadata.uid is the encoded Relay node ID")
}

func newRepositoryStatusMutation(t *testing.T) (*mutationResolver, datastore.Datastore, *datastore.Repository) {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	ctx := context.Background()
	namespace := &datastore.Namespace{UID: "01960000-0000-7000-8000-000000000071", Name: "acme", CreationActor: "controller", UpdateActor: "controller"}
	require.NoError(t, store.CreateNamespace(ctx, namespace))
	repository := &datastore.Repository{
		UID: "01960000-0000-7000-8000-000000000072", RepositoryID: "01960000-0000-7000-8000-000000000072",
		APIVersion: repositoryAPIVersion, Kind: repositoryKind, Namespace: namespace.Name, NamespaceID: namespace.Name,
		Name: "catalog", Generation: 2, ResourceVersion: "7", DefaultBranch: "main", StorageClass: "standard",
		Status: []byte(`{"observedGeneration":1,"conditions":[]}`), CreationTimestamp: time.Now().UTC(),
	}
	require.NoError(t, store.CreateRepository(ctx, repository))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: namespace.Name, Name: repository.Name, RepositoryID: repository.UID}))
	service, err := NewService(ServiceDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	return &mutationResolver{Resolver: &Resolver{service: service, logger: zap.NewNop(), storageDataDir: "/data"}}, store, repository
}

func TestUpdateRepositoryStatusAppliesTypedPartialPatchAndReturnsGraphQLErrorOnConflict(t *testing.T) {
	mutation, store, repository := newRepositoryStatusMutation(t)
	observed := int32(2)
	revision := "main@sha1:abc"
	payload, err := mutation.UpdateRepositoryStatus(context.Background(), model.UpdateRepositoryStatusInput{
		Namespace: repository.Namespace, Name: repository.Name, ResourceVersion: "7", ObservedGeneration: &observed,
		LastAppliedRevision: &revision,
		Resolved:            &model.RepositoryResolvedStatusInput{StoragePath: "/data/acme/catalog.git", StorageClass: "premium"},
	})
	require.NoError(t, err)
	require.NotNil(t, payload.Repository.Status.Resolved)
	assert.Equal(t, "/data/acme/catalog.git", payload.Repository.Status.Resolved.StoragePath)
	assert.Equal(t, "premium", payload.Repository.Status.Resolved.StorageClass)
	persisted, err := store.GetRepository(context.Background(), repository.UID)
	require.NoError(t, err)
	var status catalog.RepositoryStatus
	require.NoError(t, json.Unmarshal(persisted.Status, &status))
	assert.Equal(t, int64(2), status.ObservedGeneration)
	assert.Equal(t, revision, status.LastAppliedRevision)
	assert.Equal(t, "8", persisted.ResourceVersion)

	_, err = mutation.UpdateRepositoryStatus(context.Background(), model.UpdateRepositoryStatusInput{
		Namespace: repository.Namespace, Name: repository.Name, ResourceVersion: "7",
	})
	var conflict *gqlerror.Error
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "RESOURCE_VERSION_CONFLICT", conflict.Extensions["code"])
	assert.Equal(t, "8", conflict.Extensions["resourceVersion"])
}

type repositoryLifecycleWriter struct {
	commits []gitclient.CommitFileParams
	sha     string
}

func (*repositoryLifecycleWriter) CreateRepository(context.Context, string, string) (string, error) {
	return "", nil
}
func (*repositoryLifecycleWriter) DeleteRepository(context.Context, string) error { return nil }
func (w *repositoryLifecycleWriter) CommitFile(_ context.Context, p gitclient.CommitFileParams) (string, error) {
	w.commits = append(w.commits, p)
	if w.sha == "" {
		return "commit-1", nil
	}
	return w.sha, nil
}
func (w *repositoryLifecycleWriter) CommitFileForRepo(ctx context.Context, _ string, p gitclient.CommitFileParams) (string, error) {
	return w.CommitFile(ctx, p)
}
func (*repositoryLifecycleWriter) ResolveRefForRepo(context.Context, string, string) (string, error) {
	return "", nil
}
func (*repositoryLifecycleWriter) ReadFileForRepo(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}
func (*repositoryLifecycleWriter) DeleteFile(context.Context, gitclient.DeleteFileParams) (string, error) {
	return "", nil
}
func (*repositoryLifecycleWriter) CreateTag(context.Context, gitclient.CreateTagParams) (string, error) {
	return "", nil
}

type repositoryLifecycleAdmitter struct {
	store datastore.Datastore
	calls []admission.CommittedManifestRequest
}

func (a *repositoryLifecycleAdmitter) AdmitCommittedManifest(ctx context.Context, request admission.CommittedManifestRequest) (*admission.CommittedManifestResult, error) {
	a.calls = append(a.calls, request)
	if request.Operation == admission.OperationCreate {
		repository := &datastore.Repository{UID: "01960000-0000-7000-8000-000000000073", RepositoryID: "01960000-0000-7000-8000-000000000073", APIVersion: repositoryAPIVersion, Kind: repositoryKind, Namespace: request.Namespace, NamespaceID: request.Namespace, Name: "catalog", DefaultBranch: "main", StorageClass: "standard", ResourceVersion: "1", Generation: 1, Status: []byte(`{"observedGeneration":0,"conditions":[]}`), CreationTimestamp: time.Now().UTC()}
		if err := a.store.CreateRepository(ctx, repository); err != nil {
			return nil, err
		}
		if err := a.store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: request.Namespace, Name: repository.Name, RepositoryID: repository.UID}); err != nil {
			return nil, err
		}
	}
	return &admission.CommittedManifestResult{Kind: "Repository", Name: "catalog", CommitSHA: request.CommitSHA}, nil
}

func TestRepositoryMutationsDelegateOneCommittedManifestToSharedAdmission(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "01960000-0000-7000-8000-000000000074", Name: "acme", CreationActor: "author", UpdateActor: "author"}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: SystemRepositoryName, RepositoryID: "01960000-0000-7000-8000-000000000075"}))
	writer := &repositoryLifecycleWriter{}
	admitter := &repositoryLifecycleAdmitter{store: store}
	service, err := NewService(ServiceDeps{Store: store, GitWriter: writer, Logger: zap.NewNop(), CommittedManifestAdmitter: admitter})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: &Resolver{service: service, logger: zap.NewNop(), storageDataDir: "/data"}}
	input := model.CreateRepositoryInput{APIVersion: repositoryAPIVersion, Kind: repositoryKind, Metadata: &model.MetadataInput{Name: "catalog", Namespace: "acme"}, Spec: &model.RepositorySpecInput{}}
	created, err := mutation.CreateRepository(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, created.Repository)
	assert.Equal(t, created.Repository.ID, created.Repository.Metadata.UID)
	require.Len(t, writer.commits, 1)
	assert.Equal(t, "repositories/catalog.md", writer.commits[0].Path)
	assert.Contains(t, string(writer.commits[0].Content), "apiVersion: gitstore.dev/v1beta1")
	assert.Contains(t, string(writer.commits[0].Content), "kind: Repository")
	require.Len(t, admitter.calls, 1)
	assert.Equal(t, admission.OperationCreate, admitter.calls[0].Operation)

	_, err = mutation.UpdateRepository(ctx, model.UpdateRepositoryInput{APIVersion: repositoryAPIVersion, Kind: repositoryKind, Metadata: input.Metadata, Spec: input.Spec})
	require.NoError(t, err)
	require.Len(t, writer.commits, 2)
	require.Len(t, admitter.calls, 2)
	assert.Equal(t, admission.OperationUpdate, admitter.calls[1].Operation)
}

func TestRepositoryMutationRejectsEnvelopeMismatchAndBootstrapBeforeGitCommit(t *testing.T) {
	mutation := &mutationResolver{Resolver: &Resolver{logger: zap.NewNop()}}
	_, err := mutation.CreateRepository(context.Background(), model.CreateRepositoryInput{APIVersion: "wrong", Kind: repositoryKind, Metadata: &model.MetadataInput{Name: "catalog", Namespace: "acme"}, Spec: &model.RepositorySpecInput{}})
	require.ErrorContains(t, err, "apiVersion")
	_, err = mutation.CreateRepository(context.Background(), model.CreateRepositoryInput{APIVersion: repositoryAPIVersion, Kind: repositoryKind, Metadata: &model.MetadataInput{Name: SystemRepositoryName, Namespace: "acme"}, Spec: &model.RepositorySpecInput{}})
	require.ErrorContains(t, err, "system-managed")
}

type repositoryLifecycleGitReader struct{ current *string }

func (*repositoryLifecycleGitReader) ListFiles(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}
func (*repositoryLifecycleGitReader) ReadFile(context.Context, string, string, string) ([]byte, error) {
	return nil, nil
}
func (r *repositoryLifecycleGitReader) ResolveRef(context.Context, string, string) (string, error) {
	return *r.current, nil
}

func TestRepositoryLifecycleResolverUsesRealCatalogAdmissionForStatusAndVersions(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	ctx := context.Background()
	const systemRepositoryID = "01960000-0000-7000-8000-000000000076"
	require.NoError(t, store.CreateNamespace(ctx, &datastore.Namespace{UID: "01960000-0000-7000-8000-000000000077", Name: "acme", CreationActor: "author", UpdateActor: "author"}))
	require.NoError(t, store.CreateRepository(ctx, &datastore.Repository{UID: systemRepositoryID, RepositoryID: systemRepositoryID, Namespace: "acme", NamespaceID: "acme", Name: SystemRepositoryName, CreationTimestamp: time.Now().UTC()}))
	require.NoError(t, store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{Namespace: "acme", Name: SystemRepositoryName, RepositoryID: systemRepositoryID}))
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	current := first
	admitter, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{Store: store, GitReader: &repositoryLifecycleGitReader{current: &current}, Logger: zap.NewNop()})
	require.NoError(t, err)
	writer := &repositoryLifecycleWriter{sha: first}
	service, err := NewService(ServiceDeps{Store: store, GitWriter: writer, Logger: zap.NewNop(), CommittedManifestAdmitter: admitter})
	require.NoError(t, err)
	mutation := &mutationResolver{Resolver: &Resolver{service: service, logger: zap.NewNop(), storageDataDir: "/data"}}
	input := model.CreateRepositoryInput{APIVersion: repositoryAPIVersion, Kind: repositoryKind, Metadata: &model.MetadataInput{Name: "catalog", Namespace: "acme"}, Spec: &model.RepositorySpecInput{DefaultBranch: stringPointer("main"), StorageClass: stringPointer("standard")}}
	created, err := mutation.CreateRepository(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, int32(1), created.Repository.Metadata.Generation)
	assert.Equal(t, "1", created.Repository.Metadata.ResourceVersion)
	assert.Equal(t, created.Repository.ID, created.Repository.Metadata.UID)
	admission := repositoryModelConditionByType(t, created.Repository.Status.Conditions, catalog.ConditionAdmissionAccepted)
	assert.Equal(t, model.ConditionStatusTrue, admission.Status)

	current, writer.sha = second, second
	updated, err := mutation.UpdateRepository(ctx, model.UpdateRepositoryInput{APIVersion: repositoryAPIVersion, Kind: repositoryKind, Metadata: input.Metadata, Spec: &model.RepositorySpecInput{DefaultBranch: stringPointer("trunk"), StorageClass: stringPointer("premium")}})
	require.NoError(t, err)
	assert.Equal(t, int32(2), updated.Repository.Metadata.Generation)
	assert.Equal(t, "2", updated.Repository.Metadata.ResourceVersion)
	assert.Equal(t, "trunk", updated.Repository.Spec.DefaultBranch)
	assert.Equal(t, "premium", updated.Repository.Status.Resolved.StorageClass)
}

func repositoryModelConditionByType(t *testing.T, conditions []*model.Condition, conditionType string) *model.Condition {
	t.Helper()
	for _, condition := range conditions {
		if condition != nil && condition.Type == conditionType {
			return condition
		}
	}
	t.Fatalf("condition %q not found", conditionType)
	return nil
}
