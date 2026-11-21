// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
)

type managedContractDirectory struct{ auth.UserDirProvider }

func (managedContractDirectory) GetBySubject(_ context.Context, subject string) (*auth.UserProfile, error) {
	return &auth.UserProfile{Subject: subject, Username: "shop", Kind: auth.ProfileStaff, Active: true, Lifecycle: auth.ActorActive}, nil
}

func (managedContractDirectory) ListGroups(context.Context, string) ([]string, error) {
	return nil, nil
}

func managedContractRequest(t *testing.T, root *Resolver, principal *auth.Principal, query string, input any) (map[string]map[string]any, gqlerror.List) {
	t.Helper()
	response := managedContractResponse(t, root, principal, query, input, false)
	return response.Data, response.Errors
}

type managedGraphQLResponse struct {
	Data       map[string]map[string]any
	Errors     gqlerror.List
	Extensions map[string]any
}

func managedContractResponse(t *testing.T, root *Resolver, principal *auth.Principal, query string, input any, provenance bool) managedGraphQLResponse {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"input": input}})
	require.NoError(t, err)
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: root}))
	server.Use(root.ProvenanceExtension())
	guard := security.NewAuthorizeWithStore(root.registry, root.store, zap.NewNop())
	server.AroundOperations(guard.GraphQLAuthorizer)
	server.AroundFields(guard.GraphQLFieldAuthorizer)
	server.AroundResponses(guard.GraphQLResponseAuthorizer)
	request := httptest.NewRequest(http.MethodPost, "/admin/graphql", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if provenance {
		request.Header.Set("GitStore-Provenance-Include", "true")
	}
	request = request.WithContext(auth.ContextWithPrincipal(request.Context(), principal))
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	var response managedGraphQLResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

func TestManagedBindingGraphQLUsesCommonAdmissionAndProtectsControllerFields(t *testing.T) {
	root, git, _ := newRBACResourceFixture(t)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	parent.Tier, parent.CreationActor = datastore.NamespaceTierUser, "staff-subject"
	require.NoError(t, root.store.UpdateNamespace(t.Context(), parent, parent.ResourceVersion))
	parent, err = root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	git.mu.Lock()
	origin := git.origins[parent.UID]
	origin.NamespaceVersion = parent.ResourceVersion
	git.origins[parent.UID] = origin
	git.mu.Unlock()
	provider := root.registry.AuthZ().(*rbacResourceProvider)
	provider.allowLifecycle.Store(true)
	registry := auth.NewProviderRegistry(nil, provider, managedContractDirectory{})
	root.registry, root.rbacValidator.Capture.Registry = registry, registry
	root.rbacValidator.ManagedStore = root.store
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager", AuthMethod: "serviceaccount-jwt", ServiceAccountUID: uuid.NewString()}
	root.rbacValidator.ControllerSubject = controller.Subject
	require.NoError(t, root.store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager"}))
	resource, err := (rbac.ManagedBindingTemplate{Namespace: parent.Name, NamespaceUID: parent.UID, Subject: "staff-subject"}).Resource()
	require.NoError(t, err)
	input := map[string]any{"metadata": map[string]any{"name": resource.Metadata.Name, "namespace": parent.Name, "annotations": resource.Metadata.Annotations},
		"spec": map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "namespace-full-control"},
			"subjects": []any{map[string]any{"kind": "USER", "name": "staff-subject"}}}}
	create := `mutation($input: CreateRoleBindingInput!) { createRoleBinding(input: $input) { roleBinding {
		id metadata { uid name namespace resourceVersion revision annotations }
		spec { subjects { kind name } } status { resolved { roleUID } }
	} } }`
	administrator := &auth.Principal{Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:masters"}}
	_, rejected := managedContractRequest(t, root, administrator, create, input)
	require.Len(t, rejected, 1, "broad administrator authority cannot select the managed template")
	require.Zero(t, git.commits)
	data, rejected := managedContractRequest(t, root, controller, create, input)
	require.Empty(t, rejected)
	node := data["createRoleBinding"]["roleBinding"].(map[string]any)
	metadata := node["metadata"].(map[string]any)
	require.Equal(t, "staff-subject", node["spec"].(map[string]any)["subjects"].([]any)[0].(map[string]any)["name"])
	version := metadata["resourceVersion"].(string)
	role, err := root.rbacValidator.Builtins.Get(rbac.ClusterRole, "namespace-full-control")
	require.NoError(t, err)
	statusQuery := `mutation($input: UpdateResourceStatusInput!) { updateResourceStatus(input: $input) { object } }`
	statusInput := map[string]any{"kind": "RoleBinding", "namespace": "shop", "name": rbac.ManagedBindingName, "resourceVersion": version,
		"resolved": map[string]any{"roleUID": role.Metadata.UID, "roleKind": "ClusterRole", "roleName": "namespace-full-control"}}
	_, rejected = managedContractRequest(t, root, administrator, statusQuery, statusInput)
	require.Len(t, rejected, 1)
	require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	data, rejected = managedContractRequest(t, root, controller, statusQuery, statusInput)
	require.Empty(t, rejected)
	resolved := data["updateResourceStatus"]["object"].(map[string]any)
	require.Equal(t, role.Metadata.UID, resolved["status"].(map[string]any)["resolved"].(map[string]any)["roleUID"])
	version = resolved["metadata"].(map[string]any)["resourceVersion"].(string)
	require.Equal(t, 1, git.commits, "controller observations must not author another Git spec commit")
	git.mu.Lock()
	origin = git.origins[parent.UID]
	manifest := append([]byte(nil), git.files[origin.RepositoryID]["rbac/rolebindings/namespace-owner-access.yaml"]...)
	git.mu.Unlock()
	require.NoError(t, root.store.CreateRepository(t.Context(), &datastore.Repository{
		UID: origin.RepositoryID, Name: SystemRepositoryName, Namespace: parent.Name, DefaultBranch: "main",
	}))
	const callbackSecret = "owned-managed-binding-transport-parity"
	callback, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store: root.store, Logger: zap.NewNop(), Registry: registry,
		ServiceSecret: callbackSecret, RBACValidator: root.rbacValidator,
	})
	require.NoError(t, err)
	for _, actor := range []*auth.Principal{administrator, controller,
		{Subject: "staff-subject", AuthMethod: "static-users", Username: "shop", ProfileKind: auth.ProfileStaff}} {
		_, rejected := managedContractRequest(t, root, actor,
			`mutation($input:DeleteRoleBindingInput!){deleteRoleBinding(input:$input){roleBinding{id}}}`,
			map[string]any{"id": node["id"], "resourceVersion": version})
		require.Len(t, rejected, 1, "managed owner access cannot be removed through ordinary GraphQL deletion")
		require.Equal(t, 1, git.commits)
		assertManagedReceiveRejected(t, callback, callbackSecret, origin, manifest, actor, false)
		assertManagedReceiveRejected(t, callback, callbackSecret, origin, manifest, actor, true)
	}
}

func assertManagedReceiveRejected(t *testing.T, server *cataloggrpc.Server, secret string, origin rbac.SnapshotOrigin, manifest []byte, actor *auth.Principal, revert bool) {
	t.Helper()
	digest := sha1.New()
	_, _ = fmt.Fprintf(digest, "blob %d\x00", len(manifest))
	_, _ = digest.Write(manifest)
	old := &catalogv1.ResourceBlob{Path: "rbac/rolebindings/namespace-owner-access.yaml",
		Content: manifest, BlobOid: fmt.Sprintf("%x", digest.Sum(nil))}
	_, oldCommit, _ := strings.Cut(origin.GitRevision, "@sha1:")
	ref := &catalogv1.ReceiveRef{RefName: origin.GitRef, OldCommitSha: oldCommit, NewCommitSha: strings.Repeat("b", 40)}
	// Reverting creation restores the pre-template tree. An ordinary deletion
	// can retain unrelated content; both must veto the complete receive.
	tree := &catalogv1.ResourceValidationTree{Ref: ref, OldBlobs: []*catalogv1.ResourceBlob{old}, RbacTree: &catalogv1.RBACValidationTree{}}
	if !revert {
		tree.ProposedBlobs = []*catalogv1.ResourceBlob{{Path: "README.md", Content: []byte("unrelated change\n")}}
	}
	request := &catalogv1.ValidateRBACReceiveRequest{RepositoryId: origin.RepositoryID, Trees: []*catalogv1.ResourceValidationTree{tree},
		Authorization: &catalogv1.CallbackAuthorization{RepositoryId: origin.RepositoryID, ReceiveId: uuid.NewString(),
			Refs: []*catalogv1.ReceiveRef{ref}, Actor: &catalogv1.CallbackActor{
				Subject: actor.Subject, AuthMethod: actor.AuthMethod, Groups: actor.Groups, ServiceAccountUid: actor.ServiceAccountUID,
			}}}
	signature, err := auth.SignRPC(secret, "gitstore-api", catalogv1.CatalogService_ValidateRBACReceive_FullMethodName, request)
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs(auth.RPCSignatureMetadata, signature))
	response, err := server.ValidateRBACReceive(ctx, request)
	require.NoError(t, err)
	require.False(t, response.Accepted)
	require.Len(t, response.Errors, 1)
	if actor.ServiceAccountUID == "" {
		require.Equal(t, "RBAC_CONTROLLER_REQUIRED", response.Errors[0].Constraint)
	} else {
		require.Equal(t, old.Path, response.Errors[0].FilePath)
		require.Equal(t, "RBAC_RESOURCE_PROTECTED", response.Errors[0].Constraint)
	}
}

func TestNamespaceStatusUIDPreconditionUsesIncarnationAcrossWireRepresentations(t *testing.T) {
	for _, scenario := range []string{"raw current", "relay current", "raw old", "relay old", "wrong relay kind"} {
		t.Run(scenario, func(t *testing.T) {
			root, _, _ := newRBACResourceFixture(t)
			namespace, err := root.store.GetNamespaceByName(t.Context(), "shop")
			require.NoError(t, err)
			uid := namespace.UID
			switch scenario {
			case "raw old", "relay old":
				uid = uuid.NewString()
			}
			switch scenario {
			case "relay current", "relay old":
				uid = mustEncodeNodeID(nodeKindNamespace, uid)
			case "wrong relay kind":
				uid = mustEncodeNodeID(nodeKindRepository, uid)
			}
			generation := int32(1)
			_, err = root.Mutation().UpdateNamespaceStatus(t.Context(), model.UpdateNamespaceStatusInput{
				Name: namespace.Name, UID: &uid, ResourceVersion: namespace.ResourceVersion, ObservedGeneration: &generation,
			})
			current, lookupErr := root.store.GetNamespaceByName(t.Context(), namespace.Name)
			require.NoError(t, lookupErr)
			if scenario == "raw current" || scenario == "relay current" {
				require.NoError(t, err)
				require.NotEqual(t, namespace.ResourceVersion, current.ResourceVersion)
			} else {
				require.Error(t, err)
				require.Equal(t, namespace.ResourceVersion, current.ResourceVersion)
				require.Equal(t, namespace.Status, current.Status)
			}
		})
	}
}
