// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func rbacBootstrapEnrollController(t *testing.T, fixture rbacNativeFixture) func(string) rbacNativeFixture {
	t.Helper()
	const name = "gitstore-controller-manager"
	recordPath := os.Getenv("RBAC_TEST_BOOTSTRAP_CONTROLLER_FIXTURE")
	require.True(t, filepath.IsAbs(recordPath), "retain the enrolled test controller in an explicitly private fixture file")
	_, err := os.Lstat(recordPath)
	require.True(t, os.IsNotExist(err), "never overwrite an existing controller credential fixture")
	key, err := newSecretCapacityOwnedKey()
	require.NoError(t, err)
	// Revocation intentionally removes the enrollment administrator. Retain the
	// owned credential so replacement checks and later fixture reuse can renew.
	record := secretCapacityOwnedFixture{SchemaVersion: 1, Name: name, Keys: []secretCapacityOwnedKey{key}}
	require.NoError(t, writeSecretCapacityPrivateJSON(recordPath, record))
	response := fixture.query(t, fixture.apis[0],
		`mutation($input:CreateServiceAccountInput!){createServiceAccount(input:$input){serviceAccount{metadata{uid name namespace}}}}`,
		map[string]any{"input": map[string]any{
			"metadata":   map[string]any{"name": name},
			"publicKeys": []map[string]string{{"kid": key.ID, "algorithm": "Ed25519", "publicKeyPEM": key.Public}},
		}})
	require.Empty(t, response.Errors)
	var enrolled struct {
		CreateServiceAccount struct {
			ServiceAccount struct {
				Metadata struct{ UID, Name, Namespace string }
			}
		}
	}
	require.NoError(t, json.Unmarshal(response.Data, &enrolled))
	metadata := enrolled.CreateServiceAccount.ServiceAccount.Metadata
	require.NotEmpty(t, metadata.UID)
	require.Equal(t, name, metadata.Name)
	require.Empty(t, metadata.Namespace)
	record.UID = metadata.UID
	require.NoError(t, writeSecretCapacityPrivateJSON(recordPath, record))
	info, err := os.Stat(recordPath)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0077)
	return func(api string) rbacNativeFixture {
		t.Helper()
		assertion, err := record.assertion(key, name)
		require.NoError(t, err)
		controller := fixture
		controller.token = assertion
		issued := controller.query(t, api,
			`mutation($input:IssueServiceAccountTokenInput!){issueServiceAccountToken(input:$input){tokenRequest{status{token expirationTimestamp}}}}`,
			map[string]any{"input": map[string]any{
				"metadata": map[string]any{"name": name},
				"spec":     map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 60},
			}})
		require.Empty(t, issued.Errors)
		var token struct {
			IssueServiceAccountToken struct {
				TokenRequest struct {
					Status struct {
						Token               string
						ExpirationTimestamp time.Time
					}
				}
			}
		}
		require.NoError(t, json.Unmarshal(issued.Data, &token))
		status := token.IssueServiceAccountToken.TokenRequest.Status
		require.NotEmpty(t, status.Token)
		require.True(t, status.ExpirationTimestamp.After(time.Now()))
		require.True(t, status.ExpirationTimestamp.Before(time.Now().Add(65*time.Second)))
		var claims jwt.MapClaims
		_, _, err = jwt.NewParser().ParseUnverified(status.Token, &claims)
		require.NoError(t, err)
		require.Equal(t, "system:serviceaccount:"+name, claims["sub"])
		require.Equal(t, record.UID, claims["sa_uid"])
		controller.token = status.Token
		return controller
	}
}

func TestRBACNativeBootstrapRevocationSurvivesProcessReplacement(t *testing.T) {
	testRBACNativeBootstrapProcessReplacement(t, false)
}

func TestRBACNativeBootstrapSameContractBuiltinUpgrade(t *testing.T) {
	if os.Getenv("RBAC_TEST_BOOTSTRAP_UPGRADE_RUN") != "1" {
		t.Skip("distinct old/new same-contract release binaries must be explicitly selected")
	}
	testRBACNativeBootstrapProcessReplacement(t, true)
}

func testRBACNativeBootstrapProcessReplacement(t *testing.T, upgrade bool) {
	t.Helper()
	if os.Getenv("RBAC_TEST_BOOTSTRAP_RUN") != "1" {
		t.Skip("an explicitly fresh owned bootstrap deployment is required")
	}
	fixture := ownedRBACNativeFixture(t)
	binary := os.Getenv("RBAC_TEST_API_BINARY")
	require.NotEmpty(t, binary)
	replacement := binary
	if upgrade {
		replacement = os.Getenv("RBAC_TEST_UPGRADE_API_BINARY")
		require.True(t, filepath.IsAbs(binary))
		require.True(t, filepath.IsAbs(replacement))
		require.NotEqual(t, binary, replacement)
	}
	require.NotEmpty(t, os.Getenv("RBAC_TEST_BOOTSTRAP_SUBJECT"))
	logDirectory := os.Getenv("RBAC_TEST_PROCESS_LOG_DIR")
	if logDirectory == "" {
		logDirectory = t.TempDir()
	}
	configs := []string{os.Getenv("RBAC_TEST_BOOTSTRAP_CONFIG_A"), os.Getenv("RBAC_TEST_BOOTSTRAP_CONFIG_B")}
	addresses := make([]string, len(fixture.apis))
	for i, endpoint := range fixture.apis {
		require.NotEmpty(t, configs[i])
		parsed, err := url.Parse(endpoint)
		require.NoError(t, err)
		require.Equal(t, "http", parsed.Scheme)
		require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback(), "only isolated loopback processes may be replaced")
		addresses[i] = parsed.Host
		conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, "never adopt or replace an existing process")
	}
	stops := make([]func(), len(fixture.apis))
	defer func() {
		for i := len(stops) - 1; i >= 0; i-- {
			if stops[i] != nil {
				stops[i]()
			}
		}
	}()
	start := func(i int, executable string) func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
		log, err := os.CreateTemp(logDirectory, "rbac-bootstrap-api-*.log")
		require.NoError(t, err)
		command := exec.CommandContext(ctx, executable, "-test.run", "^TestRBACNativeProcess$", "-test.timeout", "4m")
		command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
		command.WaitDelay = 15 * time.Second
		command.Env = append(os.Environ(),
			"GITSTORE_RBAC_NATIVE_TEST_OWNED=1",
			"GITSTORE_RBAC_NATIVE_TEST_CONFIG="+configs[i])
		command.Stdout, command.Stderr = log, log
		if err := command.Start(); err != nil {
			cancel()
			_ = log.Close()
			t.Fatalf("start owned bootstrap API: %v", err)
		}
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		return func() {
			cancel()
			<-done
			if err := log.Close(); err != nil {
				t.Errorf("close owned API log: %v", err)
			}
			if !command.ProcessState.Success() {
				t.Errorf("owned API must shut down cleanly; log: %s", log.Name())
			}
		}
	}
	client := &http.Client{Timeout: time.Second}
	waitReady := func(i int) {
		t.Helper()
		require.Eventually(t, func() bool {
			response, err := client.Get(fixture.apis[i] + "/ready")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 90*time.Second, 200*time.Millisecond, "fresh API %d must initialize durable bootstrap before serving", i)
	}
	for i := range stops {
		stops[i] = start(i, binary)
		waitReady(i)
	}
	var seed rbacNativeResource
	builtins := make(map[string]rbacNativeResource)
	for _, api := range fixture.apis {
		binding, response := fixture.lookup(t, api, "ClusterRoleBinding", "initial-admin")
		require.Empty(t, response.Errors)
		require.NotEmpty(t, binding.Metadata.UID)
		var spec struct {
			RoleRef  struct{ Kind, Name string }
			Subjects []struct{ Kind, Name string }
		}
		require.NoError(t, json.Unmarshal(binding.Spec, &spec))
		require.Equal(t, "CLUSTER_ROLE", spec.RoleRef.Kind)
		require.Equal(t, "cluster-admin", spec.RoleRef.Name)
		require.Len(t, spec.Subjects, 1)
		require.Equal(t, "USER", spec.Subjects[0].Kind)
		require.Equal(t, os.Getenv("RBAC_TEST_BOOTSTRAP_SUBJECT"), spec.Subjects[0].Name)
		if seed.ID != "" {
			require.Equal(t, seed.Metadata.UID, binding.Metadata.UID, "API replicas must share one ordinary seed")
		}
		seed = binding
		for _, kind := range []string{"ClusterRole", "ClusterRoleBinding"} {
			builtin, found := fixture.lookup(t, api, kind, "controller")
			require.Empty(t, found.Errors)
			require.NotEmpty(t, builtin.Metadata.UID)
			if existing, ok := builtins[kind]; ok {
				require.Equal(t, existing, builtin, "initial replicas must expose one release definition")
			}
			builtins[kind] = builtin
			recreated := fixture.query(t, api,
				fmt.Sprintf("mutation($input:Create%sInput!){create%s(input:$input){%s{id}}}", kind, kind, rbacNativeField(kind)),
				map[string]any{"input": fixture.manifest(kind, "controller")})
			rbacNativeErrorCode(t, recreated, "ADMISSION_REJECTED")
			var rejectedCreation struct {
				Extensions struct {
					Diagnostics []struct{ Reason string }
				}
			}
			require.NoError(t, json.Unmarshal(recreated.Errors[0], &rejectedCreation))
			require.NotEmpty(t, rejectedCreation.Extensions.Diagnostics)
			require.Equal(t, "RBAC_RESOURCE_PROTECTED", rejectedCreation.Extensions.Diagnostics[0].Reason)
			rejected := fixture.query(t, api,
				fmt.Sprintf("mutation($input:Delete%sInput!){delete%s(input:$input){%s{id}}}", kind, kind, rbacNativeField(kind)),
				map[string]any{"input": map[string]any{"id": builtin.ID, "resourceVersion": builtin.Metadata.ResourceVersion}})
			rbacNativeErrorCode(t, rejected, "NOT_FOUND")
			unchanged, found := fixture.lookup(t, api, kind, "controller")
			require.Empty(t, found.Errors)
			require.Equal(t, builtin, unchanged)
		}
	}
	view, found := fixture.lookup(t, fixture.apis[0], "ClusterRole", "view")
	require.Empty(t, found.Errors)
	controllerCredential := rbacBootstrapEnrollController(t, fixture)
	upgradedBuiltins := make(map[string]rbacNativeResource)
	assertRelease := func(api string, upgraded bool) {
		t.Helper()
		controller := controllerCredential(api)
		for kind, original := range builtins {
			builtin, response := controller.lookup(t, api, kind, "controller")
			require.Empty(t, response.Errors)
			require.Equal(t, original.Metadata.UID, builtin.Metadata.UID, "release changes must preserve built-in incarnation")
			require.JSONEq(t, string(original.Spec), string(builtin.Spec), "controller access must remain compatible")
			if upgraded {
				require.NotEqual(t, original.Metadata.ResourceVersion, builtin.Metadata.ResourceVersion, "a different executable alone does not prove a changed built-in release")
				if existing, ok := upgradedBuiltins[kind]; ok {
					require.Equal(t, existing, builtin, "upgraded replicas must expose the same UID-bound release version")
				}
				upgradedBuiltins[kind] = builtin
			} else {
				require.Equal(t, original, builtin)
			}
		}
		current, response := controller.lookup(t, api, "ClusterRole", "view")
		require.Empty(t, response.Errors)
		require.Equal(t, view.Metadata.UID, current.Metadata.UID)
		if upgraded {
			require.NotEqual(t, view.Spec, current.Spec, "the selected releases must exercise a real rule-content change")
			require.NotEqual(t, view.Metadata.ResourceVersion, current.Metadata.ResourceVersion)
			if existing, ok := upgradedBuiltins["view"]; ok {
				require.Equal(t, existing, current)
			}
			upgradedBuiltins["view"] = current
		} else {
			require.Equal(t, view, current)
		}
	}
	assertControllerAccess := func(api string) {
		t.Helper()
		controller := controllerCredential(api)
		response := controller.query(t, api,
			`query{namespaces(first:1){edges{node{id}}} clusterRoles(first:1){edges{node{id}}} clusterRoleBindings(first:1){edges{node{id}}}}`, nil)
		require.Empty(t, response.Errors, "the enrolled controller must retain built-in system access")
		var data struct {
			Namespaces, ClusterRoles, ClusterRoleBindings struct {
				Edges []struct{ Node struct{ ID string } }
			}
		}
		require.NoError(t, json.Unmarshal(response.Data, &data))
		require.NotEmpty(t, data.Namespaces.Edges)
		require.NotEmpty(t, data.ClusterRoles.Edges)
		require.NotEmpty(t, data.ClusterRoleBindings.Edges)
		denied := controller.query(t, api,
			`mutation($input:CreateClusterRoleInput!){createClusterRole(input:$input){clusterRole{id}}}`,
			map[string]any{"input": fixture.manifest("ClusterRole", "bootstrap-controller-must-not-create")})
		rbacNativeErrorCode(t, denied, "FORBIDDEN")
	}
	for _, api := range fixture.apis {
		assertControllerAccess(api)
		if upgrade {
			assertRelease(api, false)
		}
	}
	revoke := fixture.query(t, fixture.apis[0],
		`mutation($input:DeleteClusterRoleBindingInput!){deleteClusterRoleBinding(input:$input){clusterRoleBinding{id}}}`,
		map[string]any{"input": map[string]any{"id": seed.ID, "resourceVersion": seed.Metadata.ResourceVersion}})
	require.Empty(t, revoke.Errors)
	assertRevoked := func(api string) {
		t.Helper()
		rbacNativeEventually(t, func() bool {
			response := fixture.query(t, api, `query{clusterRoles(first:1){edges{node{id}}}}`, nil)
			return graphqlErrorHasCode(response.Errors, "FORBIDDEN")
		}, "the removed administrator must be denied, not merely unavailable")
	}
	for _, api := range fixture.apis {
		assertRevoked(api)
		assertControllerAccess(api)
	}
	for i := range stops {
		stop := stops[i]
		stops[i] = nil
		stop()
		conn, err := net.DialTimeout("tcp", addresses[i], time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, "the previous API must release its listener before replacement")
		stops[i] = start(i, replacement)
		waitReady(i)
		for j, api := range fixture.apis {
			assertRevoked(api)
			assertControllerAccess(api)
			if upgrade {
				assertRelease(api, j <= i)
			}
		}
	}
}
