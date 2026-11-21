// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	namespacecontroller "github.com/gitstore-dev/gitstore/controller-manager/internal/namespace"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestOwnerAccessRecoversCommittedResponseLossWithoutAnotherCreate(t *testing.T) {
	namespace := namespacecontroller.Namespace{UID: uuid.NewString(), Name: "shop", Tier: "USER",
		OwnerKind: "USER", OwnerSubject: "staff-subject"}
	binding := ownerBinding(namespace)
	binding.Spec.RoleRef.Kind = "CLUSTER_ROLE"
	binding.Spec.Subjects[0].Kind = "USER"
	binding.ID = "binding-node"
	binding.Metadata.UID = ownerBindingUID(namespace.UID)
	binding.Metadata.ResourceVersion, binding.Metadata.Revision = "uid-bound-version", "main@sha1:"+strings.Repeat("a", 40)
	var mu sync.Mutex
	created, creates := false, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Query string }
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(request.Query, "createRoleBinding") {
			creates++
			created = true
			http.Error(w, "response lost after commit", http.StatusServiceUnavailable)
			return
		}
		var result any
		if created {
			result = binding
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": result}}))
	}))
	defer server.Close()
	first := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
	_, err := first.EnsureOwnerAccess(t.Context(), namespace)
	require.Error(t, err)
	second := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
	for _, replica := range []*GraphQLClient{second, first} {
		revision, err := replica.EnsureOwnerAccess(t.Context(), namespace)
		require.NoError(t, err)
		require.Equal(t, binding.Metadata.Revision, revision)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, creates)
}

func TestOwnerAccessUsesSubjectAndRefusesOrdinaryOrOldNamespaceCollisions(t *testing.T) {
	for _, scenario := range []string{"create", "existing", "relay create", "relay existing", "wrong relay kind", "ordinary collision", "old namespace", "wrong subject", "terminating"} {
		t.Run(scenario, func(t *testing.T) {
			namespace := namespacecontroller.Namespace{UID: uuid.NewString(), Name: "shop", Tier: "USER", OwnerKind: "USER", OwnerSubject: "staff-subject"}
			binding := ownerBinding(namespace)
			binding.Spec.RoleRef.Kind = "CLUSTER_ROLE"
			binding.Spec.Subjects[0].Kind = "USER"
			binding.ID = "binding-node"
			binding.Metadata.UID = ownerBindingUID(namespace.UID)
			binding.Metadata.ResourceVersion, binding.Metadata.Revision = "uid-bound-version", "main@sha1:"+strings.Repeat("a", 40)
			switch scenario {
			case "relay create", "relay existing":
				namespace.UID = base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/" + namespace.UID))
			case "wrong relay kind":
				namespace.UID = base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Repository/" + namespace.UID))
			case "ordinary collision":
				binding.Metadata.Annotations = nil
			case "old namespace":
				binding.Metadata.Annotations[provenancePrefix+"namespace-uid"] = uuid.NewString()
			case "wrong subject":
				binding.Spec.Subjects[0].Name = "shop"
			case "terminating":
				namespace.Finalizers = []string{namespacecontroller.ForegroundDeletionFinalizer}
			}
			creates := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Query     string
					Variables map[string]any
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(request.Query, "createRoleBinding") {
					creates++
					input := request.Variables["input"].(map[string]any)
					spec := input["spec"].(map[string]any)
					subject := spec["subjects"].([]any)[0].(map[string]any)
					require.Equal(t, "CLUSTER_ROLE", spec["roleRef"].(map[string]any)["kind"])
					require.Equal(t, "USER", subject["kind"])
					require.Equal(t, "staff-subject", subject["name"])
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": map[string]any{"roleBinding": binding}}}))
					return
				}
				var result any = binding
				if scenario == "create" || scenario == "relay create" {
					result = nil
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"result": result}}))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
			revision, err := client.EnsureOwnerAccess(t.Context(), namespace)
			switch scenario {
			case "create", "existing", "relay create", "relay existing":
				require.NoError(t, err)
				require.Equal(t, binding.Metadata.Revision, revision)
				require.Equal(t, map[string]int{"create": 1, "existing": 0, "relay create": 1, "relay existing": 0}[scenario], creates)
			default:
				require.Error(t, err)
				require.Empty(t, revision)
				require.Zero(t, creates)
			}
		})
	}
}
