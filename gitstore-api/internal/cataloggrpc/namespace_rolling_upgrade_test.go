// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSameVersionNamespaceCallbacksRetainShippedPolicy(t *testing.T) {
	store := newNamespacePolicyDatastore(t)
	policy, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "policy.yaml"))
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(path, policy, 0600))
	var replicas [2]*catalogTestServer
	var providers [2]*rbaclocal.RBACLocalProvider
	var baseline auth.AuthorizationSnapshot
	replace := func(i int) {
		t.Helper()
		providers[i], err = rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
		require.NoError(t, err)
		snapshot, ok := providers[i].AuthorizationSnapshot()
		require.True(t, ok)
		require.True(t, snapshot.Loaded)
		if baseline.Digest == "" {
			baseline = snapshot
		}
		require.Equal(t, baseline, snapshot)
		replicas[i] = newCatalogServer(t, store, nil, func(deps *cataloggrpc.ServerDeps) {
			deps.Registry = auth.NewProviderRegistry(nil, providers[i], nil)
		})
	}
	replace(0)
	replace(1)
	captured := namespaceValidationRequest(nil, []*catalogv1.ResourceBlob{namespaceBlob("namespaces/alice.md", "alice", "USER")})
	captured.Authorization = fixtureValidationEnvelope(captured.RepositoryId, captured.Trees)
	captured.Authorization.Actor.Subject = "alice"
	capturedContext := replicas[0].fixtureContext(t.Context(), captured)
	accepted, err := replicas[0].Server.ValidateResources(capturedContext, captured)
	require.NoError(t, err)
	require.True(t, accepted.Accepted)
	check := func(phase, owner string) {
		t.Helper()
		previousOwnerCode := codes.OK
		if owner != "test" {
			previousOwnerCode = codes.PermissionDenied
		}
		t.Run(phase, func(t *testing.T) {
			for i, replica := range replicas {
				snapshot, ok := providers[i].AuthorizationSnapshot()
				require.True(t, ok)
				require.Equal(t, baseline, snapshot)
				for _, tc := range []struct {
					name, actor, namespace, tier string
					existing                     bool
					code                         codes.Code
					constraint                   string
				}{
					{name: "admin-organization", actor: "admin", namespace: "new-org", tier: "ORGANIZATION"},
					{name: "unbound-organization", actor: "alice", namespace: "new-org", tier: "ORGANIZATION", code: codes.PermissionDenied},
					{name: "self-user", actor: "alice", namespace: "alice", tier: "USER"},
					{name: "other-user", actor: "alice", namespace: "bob", tier: "USER", constraint: "metadata.name:policy/user-namespace-identity"},
					{name: "admin-other-user", actor: "admin", namespace: "bob", tier: "USER", constraint: "metadata.name:policy/user-namespace-identity"},
					{name: "current-owner", actor: owner, namespace: "other", tier: "ORGANIZATION", existing: true},
					{name: "non-owner", actor: "mallory", namespace: "other", tier: "ORGANIZATION", existing: true, code: codes.PermissionDenied},
					{name: "immutable-tier", actor: "admin", namespace: "other", tier: "USER", existing: true, constraint: "spec.tier:policy/tier-demotion"},
					{name: "previous-owner", actor: "test", namespace: "other", tier: "ORGANIZATION", existing: true, code: previousOwnerCode},
				} {
					t.Run(fmt.Sprintf("api-%d/%s", i, tc.name), func(t *testing.T) {
						t.Parallel()
						proposed := namespaceBlob("namespaces/"+tc.namespace+".md", tc.namespace, tc.tier)
						var old []*catalogv1.ResourceBlob
						if tc.existing {
							old = []*catalogv1.ResourceBlob{namespaceBlob(proposed.Path, tc.namespace, "ORGANIZATION")}
							if owner != "test" {
								for _, blob := range []*catalogv1.ResourceBlob{old[0], proposed} {
									blob.Content = []byte(strings.Replace(string(blob.Content), "spec:\n",
										"  annotations:\n    "+datastore.OwnerAnnotationKey+": "+owner+"\nspec:\n", 1))
								}
							}
							proposed.Content = []byte(strings.Replace(string(proposed.Content), "  title: "+tc.namespace+"\n", "  title: Updated\n", 1))
						}
						request := namespaceValidationRequest(old, []*catalogv1.ResourceBlob{proposed})
						request.Authorization = fixtureValidationEnvelope(request.RepositoryId, request.Trees)
						request.Authorization.Actor.Subject = tc.actor
						response, err := replica.Server.ValidateResources(replica.fixtureContext(t.Context(), request), request)
						require.Equal(t, tc.code, grpcstatus.Code(err))
						if tc.code != codes.OK {
							require.Nil(t, response)
							return
						}
						require.NoError(t, err)
						require.NotNil(t, response)
						require.Equal(t, tc.constraint == "", response.Accepted)
						if tc.constraint == "" {
							require.Empty(t, response.Errors)
						} else {
							require.Equal(t, []string{tc.constraint}, validationConstraints(response))
						}
					})
				}
			}
		})
	}
	check("initial", "test")
	require.NoError(t, os.WriteFile(path, []byte("apiVersion: rbac.authorization.gitstore.dev/v1\n"), 0600))
	require.Error(t, providers[0].Reload())
	snapshot, ok := providers[0].AuthorizationSnapshot()
	require.True(t, ok)
	require.Equal(t, baseline, snapshot)
	check("rejected-reload", "test")
	require.NoError(t, os.WriteFile(path, policy, 0600))
	require.NoError(t, providers[0].Reload())
	check("same-policy-reload", "test")
	replace(0)
	_, err = replicas[0].Server.ValidateResources(capturedContext, captured)
	require.Equal(t, codes.Unauthenticated, grpcstatus.Code(err), "replacement must retain shared replay rejection")
	check("replacement-a", "test")
	replace(1)
	_, err = replicas[1].Server.ValidateResources(capturedContext, captured)
	require.Equal(t, codes.Unauthenticated, grpcstatus.Code(err), "peer replacement must retain shared replay rejection")
	check("replacement-b", "test")
	namespace, err := store.GetNamespaceByName(t.Context(), "other")
	require.NoError(t, err)
	expected := namespace.ResourceVersion
	namespace.Annotations = map[string]string{datastore.OwnerAnnotationKey: "bob"}
	datastore.AdvanceNamespaceSpecVersion(namespace)
	require.NoError(t, store.UpdateNamespace(t.Context(), namespace, expected))
	check("fresh-owner-after-replacement", "bob")
	stored, err := store.GetNamespaceByName(t.Context(), "other")
	require.NoError(t, err)
	require.Equal(t, "bob", stored.EffectiveOwnerSub())
	require.Equal(t, datastore.NamespaceTierOrganization, stored.Tier)
	require.Equal(t, namespace.ResourceVersion, stored.ResourceVersion)
	for _, name := range []string{"alice", "bob", "new-org"} {
		_, err := store.GetNamespaceByName(t.Context(), name)
		require.ErrorIs(t, err, datastore.ErrNotFound, "validation must never create the proposed Namespace")
	}
}

func TestValidationProtobufFieldNumbersAndKindsRemainStable(t *testing.T) {
	assertProtoField(t, (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor(), "trees", 2, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor(), "repository_id", 15, protoreflect.StringKind, false)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "accepted", 1, protoreflect.BoolKind, false)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "errors", 2, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourcesResponse{}).ProtoReflect().Descriptor(), "resource_deletion_validation_complete", 3, protoreflect.BoolKind, false)
	assertProtoField(t, (&catalogv1.ResourceValidationTree{}).ProtoReflect().Descriptor(), "old_blobs", 1, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ResourceValidationTree{}).ProtoReflect().Descriptor(), "proposed_blobs", 2, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourceDeletionsRequest{}).ProtoReflect().Descriptor(), "trees", 1, protoreflect.MessageKind, true)
	assertProtoField(t, (&catalogv1.ValidateResourceDeletionsRequest{}).ProtoReflect().Descriptor(), "repository_id", 15, protoreflect.StringKind, false)

	errorDescriptor := (&catalogv1.ValidationError{}).ProtoReflect().Descriptor()
	assertProtoField(t, errorDescriptor, "file_path", 1, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "field", 2, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "constraint", 3, protoreflect.StringKind, false)
	assertProtoField(t, errorDescriptor, "message", 4, protoreflect.StringKind, false)

	file := (&catalogv1.ValidateResourcesRequest{}).ProtoReflect().Descriptor().ParentFile()
	service := file.Services().ByName("CatalogService")
	require.NotNil(t, service)
	method := service.Methods().ByName("ValidateResourceDeletions")
	require.NotNil(t, method)
}

func assertProtoField(
	t *testing.T,
	message protoreflect.MessageDescriptor,
	name protoreflect.Name,
	number protoreflect.FieldNumber,
	kind protoreflect.Kind,
	repeated bool,
) {
	t.Helper()
	field := message.Fields().ByName(name)
	require.NotNil(t, field, "%s.%s must remain present", message.FullName(), name)
	assert.Equal(t, number, field.Number())
	assert.Equal(t, kind, field.Kind())
	assert.Equal(t, repeated, field.Cardinality() == protoreflect.Repeated)
}
