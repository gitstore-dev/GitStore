// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestRBACNativeGraphQLOwnerAuthentication(t *testing.T) {
	testRBACNativeOwnerAuthentication(t, false, false, false)
}

func TestRBACNativeGitOwnerAuthentication(t *testing.T) {
	testRBACNativeOwnerAuthentication(t, true, false, false)
}

func TestRBACNativeOwnerAccessCatchupAfterControllerReplacement(t *testing.T) {
	if os.Getenv("RBAC_TEST_CONTROLLER_REPLACEMENT_RUN") != "1" {
		t.Skip("isolated owned controller processes must be explicitly selected")
	}
	testRBACNativeOwnerAuthentication(t, false, true, false)
}

func TestRBACNativeGitOwnerAccessCatchupAfterControllerReplacement(t *testing.T) {
	if os.Getenv("RBAC_TEST_CONTROLLER_REPLACEMENT_RUN") != "1" {
		t.Skip("isolated owned controller processes must be explicitly selected")
	}
	testRBACNativeOwnerAuthentication(t, true, true, false)
}

func TestRBACNativeOwnerAccessNamespaceRecreationAfterControllerReplacement(t *testing.T) {
	if os.Getenv("RBAC_TEST_CONTROLLER_REPLACEMENT_RUN") != "1" {
		t.Skip("isolated owned controller processes must be explicitly selected")
	}
	testRBACNativeOwnerAuthentication(t, false, true, true)
}

func TestRBACNativeOrdinaryOwnerCollisionCleanup(t *testing.T) {
	admin := ownedRBACNativeFixture(t)
	path := os.Getenv("RBAC_TEST_LEGACY_OWNER_FIXTURE")
	require.NotEmpty(t, path, "a matched test-owned historical Git/projection fixture is required")
	var seed struct {
		Namespace, NamespaceNodeUID, RunID, BindingUID, ManagedUID string
	}
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &seed))
	require.Equal(t, os.Getenv("RBAC_TEST_SUBJECT"), seed.Namespace)
	require.NotEmpty(t, seed.NamespaceNodeUID)
	require.NotEmpty(t, seed.RunID)
	require.NotEmpty(t, seed.BindingUID)
	require.NotEmpty(t, seed.ManagedUID)
	require.NotEqual(t, seed.BindingUID, seed.ManagedUID)
	observer := admin
	observer.namespace = seed.Namespace
	owner := observer
	owner.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	namespace := func(api string) (string, []struct{ Type, Status, Reason string }) {
		t.Helper()
		response := admin.query(t, api, `query($name:String!){namespace(by:{name:$name}){
			id metadata{uid labels} status{conditions{type status reason}}}}`,
			map[string]any{"name": seed.Namespace})
		require.Empty(t, response.Errors)
		var result struct {
			Namespace struct {
				ID       string
				Metadata struct {
					UID    string
					Labels map[string]string
				}
				Status struct {
					Conditions []struct{ Type, Status, Reason string }
				}
			}
		}
		require.NoError(t, json.Unmarshal(response.Data, &result))
		require.Equal(t, seed.NamespaceNodeUID, result.Namespace.Metadata.UID)
		require.Equal(t, seed.RunID, result.Namespace.Metadata.Labels["tests.gitstore.dev/owner-access-run"])
		return result.Namespace.ID, result.Namespace.Status.Conditions
	}
	matchesUID := func(actual, expected string) bool {
		decoded, _ := base64.StdEncoding.DecodeString(actual)
		return actual == expected || string(decoded) == "gid://GitStore/RoleBinding/"+expected
	}
	_, withControllers := rbacNativeOwnerControllerRecovery(t, admin, seed.Namespace, 2)
	withControllers(func() {
		var ordinaryID, managedID string
		defer func() {
			id, _ := namespace(admin.apis[0])
			deleted := admin.query(t, admin.apis[0],
				`mutation($input:DeleteNamespaceInput!){deleteNamespace(input:$input){outcome}}`,
				map[string]any{"input": map[string]any{"id": id}})
			require.Empty(t, deleted.Errors)
			for _, api := range admin.apis {
				rbacNativeEventually(t, func() bool {
					found := admin.query(t, api, `query($id:ID!){namespace(by:{id:$id}){id}}`, map[string]any{"id": id})
					return len(found.Errors) == 1 && strings.Contains(string(found.Errors[0]), "NOT_FOUND")
				}, "the exact historical fixture Namespace must finalize")
				for _, bindingID := range []string{ordinaryID, managedID} {
					if bindingID == "" {
						continue
					}
					rbacNativeEventually(t, func() bool {
						found := admin.query(t, api, `query($id:ID!){roleBinding(by:{id:$id}){id}}`,
							map[string]any{"id": bindingID})
						return graphqlErrorHasCode(found.Errors, "NOT_FOUND")
					}, "both collision and managed binding incarnations must disappear after cleanup")
				}
			}
		}()
		for _, api := range admin.apis {
			rbacNativeEventually(t, func() bool {
				_, conditions := namespace(api)
				for _, condition := range conditions {
					if condition.Type == "OwnerAccessReady" {
						return condition.Status == "FALSE" && condition.Reason == "Failed"
					}
				}
				return false
			}, "an ordinary collision must fail closed rather than be adopted")
			ordinary, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
			require.Empty(t, found.Errors)
			require.True(t, matchesUID(ordinary.Metadata.UID, seed.BindingUID))
			ordinaryID = ordinary.ID
			rbacNativeErrorCode(t, owner.query(t, api,
				`query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
				map[string]any{"ns": seed.Namespace}), "FORBIDDEN")
			rbacNativeErrorCode(t, owner.query(t, api,
				`mutation($input:DeleteRoleBindingInput!){deleteRoleBinding(input:$input){outcome}}`,
				map[string]any{"input": map[string]any{"id": ordinary.ID, "resourceVersion": ordinary.Metadata.ResourceVersion}}), "FORBIDDEN")
		}
		ordinary, found := observer.lookup(t, admin.apis[0], "RoleBinding", "namespace-owner-access")
		require.Empty(t, found.Errors)
		deleted := observer.query(t, admin.apis[0],
			`mutation($input:DeleteRoleBindingInput!){deleteRoleBinding(input:$input){outcome}}`,
			map[string]any{"input": map[string]any{"id": ordinary.ID, "resourceVersion": ordinary.Metadata.ResourceVersion}})
		require.Empty(t, deleted.Errors, "ordinary authorized cleanup must not require managed-controller authorship")
		for _, api := range admin.apis {
			var lastUID string
			var lastErrors []json.RawMessage
			defer func() {
				if t.Failed() {
					t.Logf("last collision recovery observation: uid=%q errors=%s", lastUID, lastErrors)
				}
			}()
			rbacNativeEventually(t, func() bool {
				managed, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
				lastUID, lastErrors = managed.Metadata.UID, found.Errors
				if len(found.Errors) != 0 || !matchesUID(managed.Metadata.UID, seed.ManagedUID) {
					return false
				}
				managedID = managed.ID
				var spec struct{ Subjects []struct{ Kind, Name string } }
				require.NoError(t, json.Unmarshal(managed.Spec, &spec))
				require.Len(t, spec.Subjects, 1)
				require.Equal(t, seed.Namespace, spec.Subjects[0].Name)
				allowed := owner.query(t, api, `query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
					map[string]any{"ns": seed.Namespace})
				lastErrors = allowed.Errors
				return len(allowed.Errors) == 0
			}, "cleanup must provision a new deterministic managed identity and restore owner access")
		}
	})
}

func testRBACNativeOwnerAuthentication(t *testing.T, nativeGit, replacement, recreation bool) {
	t.Helper()
	admin := ownedRBACNativeFixture(t)
	subject := os.Getenv("RBAC_TEST_SUBJECT")
	require.NotEmpty(t, subject, "the explicitly owned authenticated Staff subject is required")
	require.NotEqual(t, admin.namespace, subject)
	var recoverControllers func(string)
	var withControllers func(func())
	if replacement {
		recoverControllers, withControllers = rbacNativeOwnerControllerRecovery(t, admin, subject)
	}
	owner := admin
	owner.namespace = subject
	owner.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	observer := owner
	observer.token = admin.token
	const selection = `id metadata { uid name resourceVersion annotations labels deletionTimestamp } status { conditions { type status reason } }`
	const runLabel = "tests.gitstore.dev/owner-access-run"
	runID := rand.Text()
	type namespace struct {
		ID       string
		Metadata struct {
			UID, Name, ResourceVersion string
			Annotations                map[string]string
			Labels                     map[string]string
			DeletionTimestamp          *string
		}
		Status struct {
			Conditions []struct{ Type, Status, Reason string }
		}
	}
	lookup := func() (namespace, gqlResponse) {
		response := admin.query(t, admin.apis[0],
			`query($name:String!){result:namespace(by:{name:$name}){`+selection+`}}`,
			map[string]any{"name": subject})
		var data struct{ Result namespace }
		if len(response.Data) > 0 {
			require.NoError(t, json.Unmarshal(response.Data, &data))
		}
		return data.Result, response
	}
	errorCode := func(response gqlResponse) string {
		if len(response.Errors) != 1 {
			return ""
		}
		var failure struct{ Extensions struct{ Code string } }
		require.NoError(t, json.Unmarshal(response.Errors[0], &failure))
		return failure.Extensions.Code
	}
	existing, response := lookup()
	require.Empty(t, existing.ID, "never adopt an existing fixed-name namespace")
	require.Equal(t, "NOT_FOUND", errorCode(response))
	var revokeSourceRole, revokeSourceBinding func()
	defer func() {
		cleanup := func() {
			if revokeSourceBinding != nil {
				revokeSourceBinding()
			}
			if revokeSourceRole != nil {
				revokeSourceRole()
			}
		}
		if withControllers != nil && (revokeSourceRole != nil || revokeSourceBinding != nil) {
			withControllers(cleanup)
		} else {
			cleanup()
		}
	}()
	if nativeGit {
		grantSourceWrite := func() {
			name := uniqueName("owner-source-writer")
			manifest := admin.manifest("ClusterRole", name)
			manifest["metadata"].(map[string]any)["labels"] = map[string]string{runLabel: runID}
			manifest["spec"] = map[string]any{"rules": []any{
				map[string]any{"resources": []string{"repository", "repository/contents"},
					"verbs": []string{"read"}, "resourceNames": []string{"gitstore-system"}},
				map[string]any{"resources": []string{"repository/contents"},
					"verbs": []string{"write"}, "resourceNames": []string{"gitstore-system"}},
			}}
			var roleUID string
			revokeSourceRole = func() {
				current, found := admin.lookup(t, admin.apis[0], "ClusterRole", name)
				if errorCode(found) == "NOT_FOUND" {
					return
				}
				require.Empty(t, found.Errors)
				if roleUID != "" {
					require.Equal(t, roleUID, current.Metadata.UID)
				} else {
					receipt := admin.query(t, admin.apis[0],
						`query($name:String!){clusterRole(by:{name:$name}){metadata{uid labels}}}`,
						map[string]any{"name": name})
					require.Empty(t, receipt.Errors)
					var data struct {
						ClusterRole struct {
							Metadata struct {
								UID    string
								Labels map[string]string
							}
						}
					}
					require.NoError(t, json.Unmarshal(receipt.Data, &data))
					require.Equal(t, current.Metadata.UID, data.ClusterRole.Metadata.UID)
					require.Equal(t, runID, data.ClusterRole.Metadata.Labels[runLabel], "never adopt another partial source grant")
				}
				deleted := admin.query(t, admin.apis[0],
					`mutation($input:DeleteClusterRoleInput!){deleteClusterRole(input:$input){clusterRole{id}}}`,
					map[string]any{"input": map[string]any{"id": current.ID, "resourceVersion": current.Metadata.ResourceVersion}})
				require.Empty(t, deleted.Errors)
			}
			role := admin.create(t, "ClusterRole", manifest)
			roleUID = role.Metadata.UID
			binding := admin.manifest("ClusterRoleBinding", name)
			binding["metadata"].(map[string]any)["labels"] = map[string]string{runLabel: runID}
			binding["spec"] = map[string]any{
				"roleRef":  map[string]any{"kind": "ClusterRole", "name": name},
				"subjects": []any{map[string]any{"kind": "User", "name": subject}},
			}
			var uid string
			revokeSourceBinding = func() { admin.revokeBinding(t, "ClusterRoleBinding", name, binding["spec"], &uid) }
			granted := admin.create(t, "ClusterRoleBinding", binding)
			uid = granted.Metadata.UID
		}
		if withControllers != nil {
			withControllers(grantSourceWrite)
		} else {
			grantSourceWrite()
		}
	}
	var parent namespace
	removeNamespace := func() {
		rbacNativeEventually(t, func() bool {
			current, found := lookup()
			if errorCode(found) == "NOT_FOUND" {
				if parent.ID != "" {
					for _, api := range admin.apis {
						response := admin.query(t, api,
							`query($id:ID!){result:namespace(by:{id:$id}){`+selection+`}}`,
							map[string]any{"id": parent.ID})
						if errorCode(response) != "NOT_FOUND" {
							require.Empty(t, response.Errors)
							var data struct{ Result namespace }
							require.NoError(t, json.Unmarshal(response.Data, &data))
							require.Equal(t, parent.Metadata.UID, data.Result.Metadata.UID)
							require.Equal(t, runID, data.Result.Metadata.Labels[runLabel])
							return false
						}
					}
				}
				return true
			}
			require.Empty(t, found.Errors)
			require.Equal(t, runID, current.Metadata.Labels[runLabel], "cleanup must not adopt another run")
			if parent.Metadata.UID != "" {
				require.Equal(t, parent.Metadata.UID, current.Metadata.UID, "cleanup must not affect a replacement")
			}
			parent = current
			if current.Metadata.DeletionTimestamp == nil {
				deleted := admin.query(t, admin.apis[0],
					`mutation($input:DeleteNamespaceInput!){deleteNamespace(input:$input){outcome}}`,
					map[string]any{"input": map[string]any{"id": parent.ID}})
				if len(deleted.Errors) != 0 && errorCode(deleted) != "CONFLICT" {
					require.Len(t, deleted.Errors, 1)
					var failure struct {
						Extensions struct {
							Code, Phase, Commit string
							Diagnostics         []struct{ Reason, Message string }
						}
					}
					require.NoError(t, json.Unmarshal(deleted.Errors[0], &failure))
					require.Equal(t, "ADMISSION_REJECTED", failure.Extensions.Code)
					require.Equal(t, "POST_RECEIVE", failure.Extensions.Phase)
					require.NotEmpty(t, failure.Extensions.Commit)
					require.Len(t, failure.Extensions.Diagnostics, 1)
					require.Equal(t, "ADMISSION_FAILED", failure.Extensions.Diagnostics[0].Reason)
					require.Equal(t, "admission failed: datastore: resourceVersion conflict", failure.Extensions.Diagnostics[0].Message)
				}
			}
			return false
		}, "the exact owned namespace must finalize without leaving test resources")
	}
	defer removeNamespace()
	input := map[string]any{
		"metadata": map[string]any{
			"name": subject, "labels": map[string]string{runLabel: runID},
			"annotations": map[string]string{"rbac.authorization.gitstore.dev/owner": subject},
		},
		"spec": map[string]any{"tier": "USER", "title": "Owned RBAC identity acceptance"},
	}
	if nativeGit {
		git := owner.clone(t, "ClusterRole")
		input["apiVersion"] = "gitstore.dev/v1beta1"
		input["kind"] = "Namespace"
		git.write(t, "namespaces/"+subject+".md", input)
		git.commit(t)
		git.push(t)
		rbacNativeEventually(t, func() bool {
			parent, response = lookup()
			if errorCode(response) == "NOT_FOUND" {
				return false
			}
			require.Empty(t, response.Errors)
			return parent.Metadata.Labels[runLabel] == runID
		}, "the asynchronous signed callback must project the owned native Namespace")
	} else {
		response = owner.query(t, owner.apis[0],
			`mutation($input:CreateNamespaceInput!){result:createNamespace(input:$input){namespace{`+selection+`}}}`,
			map[string]any{"input": input})
		var created struct{ Result struct{ Namespace namespace } }
		require.NoError(t, json.Unmarshal(response.Data, &created))
		parent = created.Result.Namespace
		if parent.ID == "" {
			current, found := lookup()
			if len(found.Errors) == 0 && current.Metadata.Labels[runLabel] == runID {
				parent = current
			}
		}
	}
	require.Empty(t, response.Errors)
	require.NotEmpty(t, parent.ID)
	require.NotEmpty(t, parent.Metadata.UID)
	require.Equal(t, runID, parent.Metadata.Labels[runLabel])
	require.Equal(t, subject, parent.Metadata.Name)
	require.Equal(t, subject, parent.Metadata.Annotations["rbac.authorization.gitstore.dev/owner"])
	require.Equal(t, "static-users", parent.Metadata.Annotations["rbac.authorization.gitstore.dev/owner-auth-method"])
	require.Equal(t, "gitstore/static-users", parent.Metadata.Annotations["rbac.authorization.gitstore.dev/owner-issuer"])
	if recoverControllers != nil {
		recoverControllers(parent.Metadata.UID)
	}

	var managedUID string
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			binding, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
			if len(found.Errors) != 0 || binding.Metadata.UID == "" {
				return false
			}
			var spec struct {
				RoleRef  struct{ Kind, Name string }
				Subjects []struct{ Kind, Name string }
			}
			require.NoError(t, json.Unmarshal(binding.Spec, &spec))
			require.Equal(t, "CLUSTER_ROLE", spec.RoleRef.Kind)
			require.Equal(t, "namespace-full-control", spec.RoleRef.Name)
			require.Len(t, spec.Subjects, 1)
			require.Equal(t, "USER", spec.Subjects[0].Kind)
			require.Equal(t, subject, spec.Subjects[0].Name)
			if managedUID == "" {
				managedUID = binding.Metadata.UID
			}
			require.Equal(t, managedUID, binding.Metadata.UID, "both APIs must observe one binding incarnation")
			return true
		}, "both controllers must converge on the authenticated owner binding")
		rbacNativeEventually(t, func() bool {
			allowed := owner.query(t, api, `query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
				map[string]any{"ns": subject})
			return len(allowed.Errors) == 0
		}, "owner access must become usable on each API")
		protected, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
		require.Empty(t, found.Errors)
		replacement := observer.manifest("RoleBinding", "namespace-owner-access")
		replacement["metadata"].(map[string]any)["resourceVersion"] = protected.Metadata.ResourceVersion
		replacement["spec"] = map[string]any{
			"roleRef":  map[string]any{"kind": "CLUSTER_ROLE", "name": "namespace-full-control"},
			"subjects": []any{map[string]any{"kind": "USER", "name": uniqueName("owner-replacement")}},
		}
		for _, actor := range []rbacNativeFixture{owner, observer} {
			rbacNativeErrorCode(t, actor.query(t, api,
				`mutation($input:UpdateRoleBindingInput!){updateRoleBinding(input:$input){roleBinding{id}}}`,
				map[string]any{"input": replacement}), "FORBIDDEN")
			rbacNativeErrorCode(t, actor.query(t, api,
				`mutation($input:DeleteRoleBindingInput!){deleteRoleBinding(input:$input){outcome}}`,
				map[string]any{"input": map[string]any{"id": protected.ID, "resourceVersion": protected.Metadata.ResourceVersion}}), "FORBIDDEN")
		}
		unchanged, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
		require.Empty(t, found.Errors)
		require.Equal(t, protected.Metadata.UID, unchanged.Metadata.UID)
		require.Equal(t, protected.Metadata.Generation, unchanged.Metadata.Generation)
		require.Nil(t, unchanged.Metadata.DeletionTimestamp)
		require.JSONEq(t, string(protected.Spec), string(unchanged.Spec))
		denied := owner.query(t, api, `query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
			map[string]any{"ns": admin.namespace})
		require.Equal(t, "FORBIDDEN", errorCode(denied), "the owner wildcard must remain namespace-scoped")
	}
	rbacNativeEventually(t, func() bool {
		current, found := lookup()
		require.Empty(t, found.Errors)
		require.Equal(t, parent.Metadata.UID, current.Metadata.UID)
		for _, condition := range current.Status.Conditions {
			if condition.Type == "OwnerAccessReady" {
				return condition.Status == "TRUE" && condition.Reason == "BindingAdmitted"
			}
		}
		return false
	}, "owner status must identify admitted access, independently of per-API readiness")
	for _, actor := range []rbacNativeFixture{owner, observer} {
		for _, remove := range []bool{false, true} {
			git := actor.clone(t, "RoleBinding")
			const path = "rbac/rolebindings/namespace-owner-access.yaml"
			if remove {
				require.NoError(t, os.Remove(filepath.Join(git.dir, filepath.FromSlash(path))))
			} else {
				content, err := os.ReadFile(filepath.Join(git.dir, filepath.FromSlash(path)))
				require.NoError(t, err)
				var manifest map[string]any
				require.NoError(t, yaml.Unmarshal(content, &manifest))
				manifest["spec"].(map[string]any)["subjects"] = []any{
					map[string]any{"kind": "User", "name": uniqueName("git-owner-replacement")},
				}
				git.write(t, path, manifest)
			}
			git.commit(t)
			git.reject(t, "forbidden")
		}
	}
	for _, api := range admin.apis {
		current, found := lookup()
		require.Empty(t, found.Errors)
		annotations := make(map[string]string, len(current.Metadata.Annotations))
		for key, value := range current.Metadata.Annotations {
			annotations[key] = value
		}
		annotations["rbac.authorization.gitstore.dev/owner"] = uniqueName("other-owner")
		rejected := admin.query(t, api,
			`mutation($input:UpdateNamespaceInput!){updateNamespace(input:$input){namespace{id}}}`,
			map[string]any{"input": map[string]any{
				"metadata": map[string]any{"name": subject, "annotations": annotations},
				"spec":     input["spec"],
			}})
		require.Len(t, rejected.Errors, 1)
		require.Contains(t, string(rejected.Errors[0]), "owner cannot be changed via update")
		transfer := admin.query(t, api,
			`mutation($input:TransferNamespaceOwnerInput!){transferNamespaceOwner(input:$input){namespace{id}}}`,
			map[string]any{"input": map[string]any{
				"namespaceId": current.ID,
				"targetOwnerRef": map[string]any{"kind": "USER",
					"name": annotations["rbac.authorization.gitstore.dev/owner"]},
			}})
		rbacNativeErrorCode(t, transfer, "ADMISSION_REJECTED")
		require.Contains(t, string(transfer.Errors[0]), "USER_NAMESPACE_IDENTITY",
			"the dedicated transfer mutation must also preserve authenticated USER ownership")
		unchanged, found := lookup()
		require.Empty(t, found.Errors)
		require.Equal(t, current.Metadata.UID, unchanged.Metadata.UID)
		require.Equal(t, current.Metadata.Name, unchanged.Metadata.Name)
		require.Equal(t, current.Metadata.Annotations, unchanged.Metadata.Annotations)
		protected, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
		require.Empty(t, found.Errors)
		require.Equal(t, managedUID, protected.Metadata.UID)
		require.Nil(t, protected.Metadata.DeletionTimestamp)
		var spec struct{ Subjects []struct{ Kind, Name string } }
		require.NoError(t, json.Unmarshal(protected.Spec, &spec))
		require.Len(t, spec.Subjects, 1)
		require.Equal(t, subject, spec.Subjects[0].Name)
	}
	if !recreation {
		return
	}

	name := uniqueName("owner-incarnation")
	roleManifest := observer.manifest("Role", name)
	roleManifest["spec"] = map[string]any{"rules": []any{
		map[string]any{"resources": []string{"role"}, "verbs": []string{"read"}},
	}}
	role := observer.create(t, "Role", roleManifest)
	bindingManifest := observer.manifest("RoleBinding", name)
	reviewSubject := uniqueName("old-namespace-subject")
	bindingManifest["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": name},
		"subjects": []any{map[string]any{"kind": "User", "name": reviewSubject}},
	}
	binding := observer.create(t, "RoleBinding", bindingManifest)
	review := func(api, target string) rbacNativeReview {
		t.Helper()
		return observer.review(t, api, map[string]any{"spec": map[string]any{
			"principal": map[string]any{"subject": reviewSubject, "groups": []string{}},
			"action":    "role.read", "resourceRef": map[string]any{"id": target},
		}})
	}
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			current, found := observer.lookup(t, api, "RoleBinding", name)
			require.Empty(t, found.Errors)
			require.Equal(t, binding.Metadata.UID, current.Metadata.UID)
			return current.Status.Resolved != nil && current.Status.Resolved.RoleUID == role.Metadata.UID &&
				review(api, role.ID).Status.Allowed
		}, "the old namespace must have a resolved, effective ordinary role claim before deletion")
	}

	oldNamespaceUID := parent.Metadata.UID
	removeNamespace()
	parent = namespace{}
	response = owner.query(t, owner.apis[0],
		`mutation($input:CreateNamespaceInput!){result:createNamespace(input:$input){namespace{`+selection+`}}}`,
		map[string]any{"input": input})
	var recreated struct{ Result struct{ Namespace namespace } }
	require.NoError(t, json.Unmarshal(response.Data, &recreated))
	parent = recreated.Result.Namespace
	if parent.ID == "" {
		current, found := lookup()
		if len(found.Errors) == 0 && current.Metadata.Labels[runLabel] == runID {
			parent = current
		}
	}
	require.Empty(t, response.Errors)
	require.NotEmpty(t, parent.Metadata.UID)
	require.NotEqual(t, oldNamespaceUID, parent.Metadata.UID)
	require.Equal(t, subject, parent.Metadata.Name)
	require.Equal(t, runID, parent.Metadata.Labels[runLabel])
	var replacementManagedUID string
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			current, found := observer.lookup(t, api, "RoleBinding", "namespace-owner-access")
			if len(found.Errors) != 0 || current.Metadata.UID == "" || current.Metadata.UID == managedUID {
				return false
			}
			if replacementManagedUID == "" {
				replacementManagedUID = current.Metadata.UID
			}
			require.Equal(t, replacementManagedUID, current.Metadata.UID)
			return len(owner.query(t, api, `query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
				map[string]any{"ns": subject}).Errors) == 0
		}, "same-name Namespace recreation must receive a new managed binding, not adopt the old UID")
		for _, kind := range []string{"Role", "RoleBinding"} {
			_, found := observer.lookup(t, api, kind, name)
			rbacNativeErrorCode(t, found, "NOT_FOUND")
		}
	}
	replacementRole := observer.create(t, "Role", roleManifest)
	require.NotEqual(t, role.Metadata.UID, replacementRole.Metadata.UID)
	for _, api := range admin.apis {
		hypothesis := review(api, replacementRole.ID)
		require.True(t, hypothesis.Status.Hypothetical)
		require.False(t, hypothesis.Status.Allowed, "old binding claims must not grant access in a same-name replacement Namespace")
		denied := owner.query(t, api, `query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
			map[string]any{"ns": admin.namespace})
		rbacNativeErrorCode(t, denied, "FORBIDDEN")
	}
}

func rbacNativeOwnerControllerRecovery(t *testing.T, admin rbacNativeFixture, subject string, initialPhase ...int32) (func(string), func(func())) {
	t.Helper()
	binary := os.Getenv("RBAC_TEST_CONTROLLER_BINARY")
	require.NotEmpty(t, binary)
	configs := []string{os.Getenv("RBAC_TEST_CONTROLLER_CONFIG_A"), os.Getenv("RBAC_TEST_CONTROLLER_CONFIG_B")}
	endpoints := []string{os.Getenv("RBAC_TEST_CONTROLLER_A"), os.Getenv("RBAC_TEST_CONTROLLER_B")}
	directories := []string{t.TempDir(), t.TempDir()}
	for i, endpoint := range endpoints {
		require.NotEmpty(t, configs[i])
		parsed, err := url.Parse(endpoint)
		require.NoError(t, err)
		require.Equal(t, "http", parsed.Scheme)
		require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback())
		require.Nil(t, parsed.User)
		require.Empty(t, parsed.Path)
		require.Empty(t, parsed.RawQuery)
		require.Empty(t, parsed.Fragment)
	}
	require.NotEqual(t, endpoints[0], endpoints[1])
	assertStopped := func(i int) {
		t.Helper()
		parsed, err := url.Parse(endpoints[i])
		require.NoError(t, err)
		connection, err := net.DialTimeout("tcp", parsed.Host, time.Second)
		if connection != nil {
			_ = connection.Close()
		}
		require.Error(t, err, "never adopt an existing controller or overlap checkpoint writers")
	}
	for i := range endpoints {
		assertStopped(i)
	}

	var phase atomic.Int32
	if len(initialPhase) != 0 {
		require.Len(t, initialPhase, 1)
		phase.Store(initialPhase[0])
	}
	var provisions, admissions [2]atomic.Int64
	proxies := make([]*httptest.Server, len(endpoints))
	for i := range proxies {
		target, err := url.Parse(admin.apis[i])
		require.NoError(t, err)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxies[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPost && request.URL.Path == "/admin/graphql" {
				body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20+1))
				_ = request.Body.Close()
				if err != nil || len(body) > 1<<20 {
					http.Error(w, "bounded fixture request exceeded", http.StatusBadRequest)
					return
				}
				request.Body = io.NopCloser(bytes.NewReader(body))
				var operation struct {
					Query     string
					Variables struct {
						Input struct {
							Namespace string
							Metadata  struct{ Namespace string }
						}
					}
				}
				if json.Unmarshal(body, &operation) != nil {
					http.Error(w, "invalid fixture request", http.StatusBadRequest)
					return
				}
				if phase.Load() == 0 && strings.Contains(operation.Query, "provisionNamespaceSystemRepository(") &&
					operation.Variables.Input.Namespace == subject {
					provisions[i].Add(1)
					http.Error(w, "owned provisioning fault", http.StatusServiceUnavailable)
					return
				}
				if phase.Load() == 1 && strings.Contains(operation.Query, "createRoleBinding(") &&
					operation.Variables.Input.Metadata.Namespace == subject {
					admissions[i].Add(1)
					http.Error(w, "owned admission response fault", http.StatusServiceUnavailable)
					return
				}
			}
			proxy.ServeHTTP(w, request)
		}))
		t.Cleanup(proxies[i].Close)
	}
	stops := make([]func(), len(endpoints))
	logDirectory := os.Getenv("RBAC_TEST_PROCESS_LOG_DIR")
	if logDirectory == "" {
		logDirectory = t.TempDir()
	}
	t.Cleanup(func() {
		for i := len(stops) - 1; i >= 0; i-- {
			if stops[i] != nil {
				stops[i]()
			}
		}
	})
	client := &http.Client{Timeout: time.Second}
	start := func(i int) {
		t.Helper()
		require.Nil(t, stops[i])
		assertStopped(i)
		ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
		log, err := os.CreateTemp(logDirectory, "rbac-owner-controller-*.log")
		require.NoError(t, err)
		command := exec.CommandContext(ctx, binary, "-test.run", "^TestRBACNativeProcess$", "-test.timeout", "8m")
		command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
		command.WaitDelay = 15 * time.Second
		command.Env = append(os.Environ(),
			"GITSTORE_RBAC_NATIVE_TEST_OWNED=1",
			"GITSTORE_RBAC_NATIVE_TEST_CONFIG="+configs[i],
			"GITSTORE_CONTROLLER__API_URI="+proxies[i].URL+"/admin/graphql",
			"GITSTORE_CONTROLLER__CHECKPOINT__DIR="+directories[i],
			"GITSTORE_RBAC_NATIVE_TEST_CHECKPOINT_RECEIPT="+filepath.Join(directories[i], "namespace-checkpoint.json"))
		command.Stdout, command.Stderr = log, log
		if err := command.Start(); err != nil {
			cancel()
			_ = log.Close()
			t.Fatalf("start owned controller: %v", err)
		}
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		stops[i] = func() {
			cancel()
			<-done
			if err := log.Close(); err != nil {
				t.Errorf("close controller log: %v", err)
			}
			if !command.ProcessState.Success() {
				t.Errorf("owned controller must stop cleanly; private log: %s", log.Name())
			}
			assertStopped(i)
			stops[i] = nil
		}
		require.Eventually(t, func() bool {
			select {
			case <-done:
				t.Fatalf("owned controller exited before responding; private log: %s", log.Name())
			default:
			}
			response, err := client.Get(endpoints[i] + "/health")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusServiceUnavailable
		}, 30*time.Second, 100*time.Millisecond, "the owned controller must be running and responsive")
	}
	recover := func(uid string) {
		t.Helper()
		observe := func(reason, status string) {
			t.Helper()
			rbacNativeEventually(t, func() bool {
				response := admin.query(t, admin.apis[0],
					`query($name:String!){namespace(by:{name:$name}){metadata{uid} status{conditions{type status reason}}}}`,
					map[string]any{"name": subject})
				require.Empty(t, response.Errors)
				var data struct {
					Namespace struct {
						Metadata struct{ UID string }
						Status   struct {
							Conditions []struct{ Type, Status, Reason string }
						}
					}
				}
				require.NoError(t, json.Unmarshal(response.Data, &data))
				require.Equal(t, uid, data.Namespace.Metadata.UID)
				for _, condition := range data.Namespace.Status.Conditions {
					if condition.Type == "OwnerAccessReady" {
						return condition.Reason == reason && condition.Status == status
					}
				}
				return false
			}, "the existing USER namespace must reach owner condition "+reason)
		}
		for i := range endpoints {
			start(i)
		}
		observe("Pending", "FALSE")
		require.Eventually(t, func() bool { return provisions[0].Load() > 0 && provisions[1].Load() > 0 },
			30*time.Second, 100*time.Millisecond, "both replicas must encounter the provisioning fault")
		phase.Store(1)
		observe("Failed", "FALSE")
		require.Eventually(t, func() bool { return admissions[0].Load() > 0 && admissions[1].Load() > 0 },
			30*time.Second, 100*time.Millisecond, "both replicas must retain failed owner admission work")
		collisionFixture := admin
		collisionFixture.namespace = subject
		collision := collisionFixture.manifest("RoleBinding", "namespace-owner-access")
		collision["spec"] = map[string]any{
			"roleRef":  map[string]any{"kind": "CLUSTER_ROLE", "name": "namespace-full-control"},
			"subjects": []any{map[string]any{"kind": "USER", "name": uniqueName("owner-collision")}},
		}
		owner := collisionFixture
		owner.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
		for _, api := range admin.apis {
			denied := admin.query(t, api,
				`mutation($input:CreateRoleBindingInput!){createRoleBinding(input:$input){roleBinding{id}}}`,
				map[string]any{"input": collision})
			rbacNativeErrorCode(t, denied, "FORBIDDEN")
			_, missing := collisionFixture.lookup(t, api, "RoleBinding", "namespace-owner-access")
			rbacNativeErrorCode(t, missing, "NOT_FOUND")
			rbacNativeErrorCode(t, owner.query(t, api,
				`query($ns:String!){products(namespace:$ns,first:1){edges{node{id}}}}`,
				map[string]any{"ns": subject}), "FORBIDDEN")
		}
		type snapshotState struct {
			Active, Generation, Staging uint64
			HasCursor                   bool
		}
		readSnapshot := func(i int) snapshotState {
			t.Helper()
			data, err := os.ReadFile(filepath.Join(directories[i], "namespace-checkpoint.json"))
			require.NoError(t, err)
			var state snapshotState
			require.NoError(t, json.Unmarshal(data, &state))
			require.Positive(t, state.Active)
			require.Equal(t, state.Active, state.Generation, "the retained snapshot must be complete")
			require.Zero(t, state.Staging)
			require.True(t, state.HasCursor)
			return state
		}
		checkpoints := make([]os.FileInfo, len(endpoints))
		snapshots := make([]snapshotState, len(endpoints))
		for i := range stops {
			stops[i]()
			snapshots[i] = readSnapshot(i)
			info, err := os.Stat(filepath.Join(directories[i], "Namespace.disk-v2"))
			require.NoError(t, err)
			require.True(t, info.IsDir())
			checkpoints[i] = info
		}
		phase.Store(2)
		start(0)
		observe("BindingAdmitted", "TRUE")
		stops[0]()
		require.Equal(t, snapshots[0], readSnapshot(0), "replacement must resume retained pending work without starting a fresh Namespace snapshot")
		for i := range endpoints {
			start(i)
		}
		for i := range endpoints {
			info, err := os.Stat(filepath.Join(directories[i], "Namespace.disk-v2"))
			require.NoError(t, err)
			require.True(t, os.SameFile(checkpoints[i], info), "replacement must retain its original private checkpoint directory")
		}
	}
	return recover, func(action func()) {
		t.Helper()
		started := make([]bool, len(stops))
		defer func() {
			for i := len(stops) - 1; i >= 0; i-- {
				if started[i] && stops[i] != nil {
					stops[i]()
				}
			}
		}()
		for i := range endpoints {
			if stops[i] == nil {
				start(i)
				started[i] = true
			}
		}
		action()
	}
}
