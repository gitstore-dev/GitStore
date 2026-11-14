// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
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
)

type rbacNativeFixture struct {
	apis      []string
	gitURL    string
	namespace string
	token     string
}

func ownedRBACNativeFixture(t *testing.T) rbacNativeFixture {
	t.Helper()
	if os.Getenv("RBAC_TEST_RUN") != "1" {
		t.Skip("owned native RBAC deployment not selected")
	}
	require.Equal(t, "1", os.Getenv("RBAC_TEST_OWNED_DEPLOYMENT"))
	fixture := rbacNativeFixture{
		apis:      []string{os.Getenv("RBAC_TEST_API_A"), os.Getenv("RBAC_TEST_API_B")},
		gitURL:    os.Getenv("RBAC_TEST_GIT_URL"),
		namespace: os.Getenv("RBAC_TEST_NAMESPACE"),
	}
	require.NotEmpty(t, fixture.namespace, "an explicitly owned namespace is required")
	require.NotEqual(t, fixture.apis[0], fixture.apis[1], "two independent API replicas are required")
	for _, endpoint := range append(append([]string{}, fixture.apis...), fixture.gitURL) {
		parsed, err := url.Parse(endpoint)
		require.NoError(t, err)
		require.Contains(t, []string{"http", "https"}, parsed.Scheme)
		require.NotEmpty(t, parsed.Host)
		require.Nil(t, parsed.User, "credentials must not appear in deployment URLs")
		require.Empty(t, parsed.RawQuery)
		require.Empty(t, parsed.Fragment)
		require.Empty(t, strings.TrimRight(parsed.Path, "/"), "native fixture endpoints must be base URLs")
	}
	fixture.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_TOKEN_FILE"))
	return fixture
}

func rbacNativeToken(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	info, err := file.Stat()
	require.NoError(t, err)
	require.True(t, info.Mode().IsRegular())
	require.Zero(t, info.Mode().Perm()&0077, "the administrator token must be private")
	token, err := io.ReadAll(io.LimitReader(file, 32769))
	require.NoError(t, err)
	require.LessOrEqual(t, len(token), 32768)
	value := strings.TrimSpace(string(token))
	require.NotEmpty(t, value)
	return value
}

type rbacNativeResource struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Metadata struct {
		UID               string  `json:"uid"`
		Name              string  `json:"name"`
		Namespace         string  `json:"namespace"`
		ResourceVersion   string  `json:"resourceVersion"`
		Generation        int64   `json:"generation"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	Spec   json.RawMessage `json:"spec"`
	Status struct {
		ObservedGeneration int64                     `json:"observedGeneration"`
		Resolved           *struct{ RoleUID string } `json:"resolved"`
	} `json:"status"`
}

func rbacNativeField(kind string) string {
	return strings.ToLower(kind[:1]) + kind[1:]
}

func rbacNativeSelection(kind string) string {
	spec := "rules { resources verbs }"
	if strings.HasSuffix(kind, "Binding") {
		spec = "roleRef { kind name } subjects { kind name }"
	}
	return "id kind metadata { uid name namespace resourceVersion generation deletionTimestamp } status { observedGeneration resolved { roleUID } } spec { " + spec + " }"
}

func rbacNativeSpec(kind string) map[string]any {
	if strings.HasSuffix(kind, "Binding") {
		return map[string]any{
			"roleRef":  map[string]any{"kind": "ClusterRole", "name": "view"},
			"subjects": []any{map[string]any{"kind": "User", "name": "native-rbac-nonexistent-subject"}},
		}
	}
	return map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}}}}
}

func (f rbacNativeFixture) manifest(kind, name string) map[string]any {
	metadata := map[string]any{"name": name}
	if !strings.HasPrefix(kind, "Cluster") {
		metadata["namespace"] = f.namespace
	}
	return map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": kind,
		"metadata": metadata, "spec": rbacNativeSpec(kind),
	}
}

func rbacNativeGraphQLSpec(t *testing.T, spec any) map[string]any {
	t.Helper()
	data, err := json.Marshal(spec)
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	kinds := map[string]string{
		"Role": "ROLE", "ClusterRole": "CLUSTER_ROLE",
		"User": "USER", "Group": "GROUP", "ServiceAccount": "SERVICE_ACCOUNT",
	}
	if ref, ok := result["roleRef"].(map[string]any); ok {
		if kind, ok := kinds[ref["kind"].(string)]; ok {
			ref["kind"] = kind
		}
	}
	if subjects, ok := result["subjects"].([]any); ok {
		for _, subject := range subjects {
			entry := subject.(map[string]any)
			if kind, ok := kinds[entry["kind"].(string)]; ok {
				entry["kind"] = kind
			}
		}
	}
	return result
}

func (f rbacNativeFixture) query(t *testing.T, api, query string, variables map[string]any) gqlResponse {
	t.Helper()
	if input, ok := variables["input"].(map[string]any); ok && input["spec"] != nil &&
		(input["kind"] == "RoleBinding" || input["kind"] == "ClusterRoleBinding") {
		input = maps.Clone(input)
		input["spec"] = rbacNativeGraphQLSpec(t, input["spec"])
		variables = maps.Clone(variables)
		variables["input"] = input
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		response := secretTestGraphQLWithTimeout(t, api, f.token, query, variables, 35*time.Second)
		retryable := response.Status == 503 && len(response.Data) == 0 && len(response.Errors) == 1 &&
			string(response.Errors[0]) == `{"message":"non-GraphQL HTTP failure"}`
		if len(response.Errors) == 1 {
			var failure struct {
				Extensions struct {
					Code      string
					Retryable bool
				}
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(response.Errors[0], &failure) == nil && failure.Extensions.Code == "AUTHORIZATION_UNAVAILABLE" &&
				failure.Extensions.Retryable && json.Unmarshal(response.Data, &fields) == nil {
				retryable = true
				for _, field := range fields {
					retryable = retryable && string(field) == "null"
				}
			}
		}
		if !retryable || time.Now().After(deadline) {
			return response
		}
		select {
		case <-t.Context().Done():
			t.Fatal("native GraphQL request canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (f rbacNativeFixture) lookup(t *testing.T, api, kind, name string) (rbacNativeResource, gqlResponse) {
	t.Helper()
	selector := map[string]any{"name": name}
	if !strings.HasPrefix(kind, "Cluster") {
		selector = map[string]any{"namespacePath": map[string]any{"namespace": f.namespace, "name": name}}
	}
	response := f.query(t, api,
		fmt.Sprintf("query($by:%sBy!){result:%s(by:$by){%s}}", kind, rbacNativeField(kind), rbacNativeSelection(kind)),
		map[string]any{"by": selector})
	if len(response.Data) == 0 {
		return rbacNativeResource{}, response
	}
	var result struct{ Result rbacNativeResource }
	require.NoError(t, json.Unmarshal(response.Data, &result))
	return result.Result, response
}

func (f rbacNativeFixture) revokeBinding(t *testing.T, kind, name string, spec any, uid *string) {
	t.Helper()
	require.Contains(t, []string{"RoleBinding", "ClusterRoleBinding"}, kind)
	for attempt := 0; attempt < 3; attempt++ {
		current, response := f.lookup(t, f.apis[0], kind, name)
		if len(response.Errors) == 1 && strings.Contains(string(response.Errors[0]), `"code":"NOT_FOUND"`) {
			return
		}
		require.Empty(t, response.Errors)
		require.Equal(t, kind, current.Kind)
		require.Equal(t, name, current.Metadata.Name)
		expected, err := json.Marshal(rbacNativeGraphQLSpec(t, spec))
		require.NoError(t, err)
		require.JSONEq(t, string(expected), string(current.Spec))
		require.NotEmpty(t, current.Metadata.UID)
		if *uid == "" {
			*uid = current.Metadata.UID
		}
		require.Equal(t, *uid, current.Metadata.UID)
		if current.Metadata.DeletionTimestamp != nil {
			return
		}
		response = f.query(t, f.apis[0],
			fmt.Sprintf("mutation($input:Delete%sInput!){delete%s(input:$input){%s{id}}}", kind, kind, rbacNativeField(kind)),
			map[string]any{"input": map[string]any{"id": current.ID, "resourceVersion": current.Metadata.ResourceVersion}})
		if len(response.Errors) == 1 && strings.Contains(string(response.Errors[0]), `"code":"CONFLICT"`) {
			continue
		}
		if len(response.Errors) == 1 {
			var partial struct {
				Extensions struct {
					Code, Phase, Commit string
					Diagnostics         []struct{ Reason string }
				}
			}
			if json.Unmarshal(response.Errors[0], &partial) == nil &&
				partial.Extensions.Code == "ADMISSION_REJECTED" && partial.Extensions.Phase == "POST_RECEIVE" &&
				len(partial.Extensions.Diagnostics) == 1 && partial.Extensions.Diagnostics[0].Reason == "RBAC_PROJECTION_PENDING" {
				commit := partial.Extensions.Commit
				require.Len(t, commit, 40)
				_, err := hex.DecodeString(commit)
				require.NoError(t, err)
				git := f.clone(t, kind)
				_, err = git.command(t, "merge-base", "--is-ancestor", commit, "HEAD")
				require.NoError(t, err, "a partial deletion must identify a real canonical commit")
				path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
				for _, revision := range []string{commit, "HEAD"} {
					paths, err := git.command(t, "ls-tree", "--name-only", revision, "--", path)
					require.NoError(t, err)
					require.Empty(t, strings.TrimSpace(string(paths)), "the exact binding must be absent from committed Git")
				}
				rbacNativeEventually(t, func() bool {
					got, readback := f.lookup(t, f.apis[0], kind, name)
					if graphqlErrorHasCode(readback.Errors, "NOT_FOUND") {
						return true
					}
					require.Empty(t, readback.Errors)
					require.Equal(t, *uid, got.Metadata.UID, "never mistake a same-name replacement for cleanup")
					require.JSONEq(t, string(expected), string(got.Spec))
					return got.Metadata.DeletionTimestamp != nil
				}, "the committed binding removal must converge without retrying its mutation")
				return
			}
		}
		require.Empty(t, response.Errors)
		return
	}
	t.Fatal("temporary actor grant cleanup exhausted known-conflict retries")
}

func (f rbacNativeFixture) await(t *testing.T, kind, name string, terminating bool) rbacNativeResource {
	t.Helper()
	var result rbacNativeResource
	rbacNativeEventually(t, func() bool {
		for _, api := range f.apis {
			got, response := f.lookup(t, api, kind, name)
			if len(response.Errors) != 0 || got.Metadata.UID == "" || got.Status.ObservedGeneration != got.Metadata.Generation ||
				(got.Metadata.DeletionTimestamp != nil) != terminating {
				return false
			}
			result = got
		}
		return true
	}, "both replicas must expose the admitted resource lifecycle")
	return result
}

func rbacNativeEventually(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(message)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("native RBAC convergence check canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

type rbacNativeGit struct {
	fixture rbacNativeFixture
	dir     string
}

func (f rbacNativeFixture) clone(t *testing.T, kind string) rbacNativeGit {
	t.Helper()
	namespace := f.namespace
	if strings.HasPrefix(kind, "Cluster") {
		namespace = "gitstore-system"
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		git := rbacNativeGit{fixture: f, dir: filepath.Join(t.TempDir(), "work")}
		output, err := git.command(t, "clone", "--branch", "main", strings.TrimRight(f.gitURL, "/")+"/"+namespace+"/gitstore-system", git.dir)
		if err == nil {
			return git
		}
		message := strings.ReplaceAll(string(output), f.token, "[redacted]")
		if time.Now().After(deadline) || (!strings.Contains(message, "returned error: 503") && !strings.Contains(message, "returned error: 403")) {
			require.NoError(t, err, "native clone of the owned canonical repository failed: %s", message)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("native clone canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (g rbacNativeGit) command(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	prefix := []string{"-c", "fetch.negotiationAlgorithm=noop", "-c", "http.lowSpeedLimit=1", "-c", "http.lowSpeedTime=10",
		"-c", "user.name=Native RBAC acceptance", "-c", "user.email=rbac-fixture@example.invalid"}
	if _, err := os.Stat(g.dir); err == nil {
		prefix = append(prefix, "-C", g.dir)
	}
	command := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	command.WaitDelay = time.Second
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer "+g.fixture.token)
	return command.CombinedOutput()
}

func (g rbacNativeGit) write(t *testing.T, path string, manifest map[string]any) {
	t.Helper()
	content, err := json.Marshal(manifest)
	require.NoError(t, err)
	if strings.HasSuffix(path, ".md") {
		content = append(append([]byte("---\n"), content...), []byte("\n---\n")...)
	}
	destination := filepath.Join(g.dir, filepath.FromSlash(path))
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0700))
	require.NoError(t, os.WriteFile(destination, content, 0600))
}

func (g rbacNativeGit) commit(t *testing.T) string {
	t.Helper()
	_, err := g.command(t, "add", "--all")
	require.NoError(t, err)
	_, err = g.command(t, "commit", "--no-gpg-sign", "-m", "Exercise native RBAC admission")
	require.NoError(t, err)
	sha, err := g.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	return strings.TrimSpace(string(sha))
}

func (g rbacNativeGit) push(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		output, err := g.command(t, "push", "origin", "HEAD:refs/heads/main")
		if err == nil {
			return
		}
		message := strings.ReplaceAll(string(output), g.fixture.token, "[redacted]")
		retryable := strings.Contains(message, "validation service unavailable") ||
			strings.Contains(message, "returned error: 503") || strings.Contains(message, "returned error: 403")
		if !retryable || time.Now().After(deadline) {
			require.NoError(t, err, "native Git push must pass blocking admission: %s", message)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("native Git push canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (g rbacNativeGit) reject(t *testing.T, reason string) string {
	t.Helper()
	before := g.remoteTip(t)
	deadline := time.Now().Add(30 * time.Second)
	var message string
	for {
		output, err := g.command(t, "push", "origin", "HEAD:refs/heads/main")
		require.Error(t, err, "the entire native receive must be rejected")
		message = strings.ReplaceAll(string(output), g.fixture.token, "[redacted]")
		retryable := strings.Contains(message, "validation service unavailable") ||
			strings.Contains(message, "returned error: 503") || strings.Contains(message, "returned error: 403")
		if !retryable || time.Now().After(deadline) {
			require.NotContains(t, message, "validation service unavailable", "unavailability is not an admission rejection")
			require.NotContains(t, message, "returned error:", "transport rejection is not an admission verdict")
			require.Contains(t, message, "[remote rejected]")
			require.Contains(t, message, reason)
			break
		}
		select {
		case <-t.Context().Done():
			t.Fatal("native Git rejection check canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
	after := g.remoteTip(t)
	require.Equal(t, before, after, "a rejected receive must not change the canonical ref")
	return message
}

func (g rbacNativeGit) remoteTip(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		output, err := g.command(t, "ls-remote", "origin", "refs/heads/main")
		if err == nil {
			return strings.TrimSpace(string(output))
		}
		message := strings.ReplaceAll(string(output), g.fixture.token, "[redacted]")
		if time.Now().After(deadline) || (!strings.Contains(message, "returned error: 503") && !strings.Contains(message, "returned error: 403")) {
			require.NoError(t, err, "read the actual canonical ref: %s", message)
		}
		select {
		case <-t.Context().Done():
			t.Fatal("canonical ref read canceled")
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func TestRBACNativeGitGraphQLParity(t *testing.T) {
	fixture := ownedRBACNativeFixture(t)
	for _, kind := range []string{"Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding"} {
		t.Run(kind, func(t *testing.T) {
			graphqlName, gitName := uniqueName("rbac-graphql"), uniqueName("rbac-git")
			field := rbacNativeField(kind)
			created := fixture.query(t, fixture.apis[0],
				fmt.Sprintf("mutation($input:Create%sInput!){create%s(input:$input){%s{%s}}}", kind, kind, field, rbacNativeSelection(kind)),
				map[string]any{"input": fixture.manifest(kind, graphqlName)})
			require.Empty(t, created.Errors, "GraphQL authoring must use common admission")
			graphqlResource := fixture.await(t, kind, graphqlName, false)

			git := fixture.clone(t, kind)
			path := "rbac/" + strings.ToLower(kind) + "s/" + gitName + ".yaml"
			git.write(t, path, fixture.manifest(kind, gitName))
			creation := git.commit(t)
			git.push(t)
			gitResource := fixture.await(t, kind, gitName, false)
			require.Equal(t, graphqlResource.Kind, gitResource.Kind)
			require.Equal(t, graphqlResource.Metadata.Namespace, gitResource.Metadata.Namespace)
			require.JSONEq(t, string(graphqlResource.Spec), string(gitResource.Spec))

			invalid := fixture.clone(t, kind)
			invalid.write(t, "rbac/wrong-location/"+gitName+".yaml", fixture.manifest(kind, gitName))
			neighbor := uniqueName("rbac-neighbor")
			invalid.write(t, "rbac/"+strings.ToLower(kind)+"s/"+neighbor+".yaml", fixture.manifest(kind, neighbor))
			invalid.commit(t)
			invalid.reject(t, "RBAC")

			if strings.HasSuffix(kind, "Binding") {
				input := fixture.manifest(kind, gitName)
				input["spec"].(map[string]any)["roleRef"] = map[string]any{"kind": "ClusterRole", "name": "edit"}
				input["metadata"].(map[string]any)["resourceVersion"] = gitResource.Metadata.ResourceVersion
				rejected := fixture.query(t, fixture.apis[0],
					fmt.Sprintf("mutation($input:Update%sInput!){update%s(input:$input){%s{id}}}", kind, kind, field),
					map[string]any{"input": input})
				require.NotEmpty(t, rejected.Errors, "GraphQL must reject roleRef replacement")
				require.Contains(t, string(rejected.Errors[0]), "RBAC_RESOURCE_PROTECTED")
				delete(input["metadata"].(map[string]any), "resourceVersion")
				invalid = fixture.clone(t, kind)
				invalid.write(t, path, input)
				invalid.commit(t)
				invalid.reject(t, "RBAC_RESOURCE_PROTECTED")
			}

			revert := fixture.clone(t, kind)
			_, err := revert.command(t, "revert", "--no-edit", creation)
			require.NoError(t, err)
			revert.push(t)
			rbacNativeEventually(t, func() bool {
				for _, api := range fixture.apis {
					resource, response := fixture.lookup(t, api, kind, gitName)
					if response.Status != 200 {
						return false
					}
					if len(response.Errors) != 0 {
						if !strings.Contains(string(response.Errors[0]), `"NOT_FOUND"`) {
							return false
						}
					} else if resource.ID != "" && resource.Metadata.DeletionTimestamp == nil {
						return false
					}
				}
				return true
			}, "revert must suppress the resource on both replicas")
		})
	}
}

func TestRBACNativeCommittedCallbackRecovery(t *testing.T) {
	fixture := ownedRBACNativeFixture(t)
	if os.Getenv("RBAC_TEST_CALLBACK_FAULT") != "1" {
		t.Skip("owned native callback fault not selected")
	}
	address := os.Getenv("RBAC_TEST_CALLBACK_PROXY_ADDR")
	host, _, err := net.SplitHostPort(address)
	require.NoError(t, err)
	require.True(t, net.ParseIP(host).IsLoopback(), "the fault listener must be explicitly loopback-bound")
	backend, err := url.Parse(os.Getenv("RBAC_TEST_CALLBACK_BACKEND"))
	require.NoError(t, err)
	require.Equal(t, "http", backend.Scheme)
	require.True(t, net.ParseIP(backend.Hostname()).IsLoopback(), "the backend must be an owned local catalog listener")
	require.Nil(t, backend.User)
	require.Empty(t, backend.RawQuery)
	require.Empty(t, backend.Fragment)
	require.Empty(t, backend.Path)
	require.NotEqual(t, address, backend.Host, "the proxy must not forward to itself")

	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols, ResponseHeaderTimeout: 35 * time.Second}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.Transport = transport
	var dropping atomic.Bool
	var validations, dropped, restored atomic.Int64
	dropping.Store(true)
	server := &http.Server{
		Protocols: protocols,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/gitstore.catalog.v1.CatalogService/ValidateRBACReceive":
				validations.Add(1)
			case "/gitstore.catalog.v1.CatalogService/AdmitResources":
				if dropping.Load() {
					dropped.Add(1)
					writer.Header().Set("Content-Type", "application/grpc")
					writer.Header().Set("Grpc-Status", "14")
					writer.Header().Set("Grpc-Message", "owned callback interruption")
					writer.WriteHeader(http.StatusOK)
					return
				}
				proxy.ServeHTTP(writer, request)
				if writer.Header().Get("Grpc-Status") == "0" || writer.Header().Get(http.TrailerPrefix+"Grpc-Status") == "0" {
					restored.Add(1)
				}
				return
			}
			proxy.ServeHTTP(writer, request)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		require.NoError(t, server.Close())
		transport.CloseIdleConnections()
		require.ErrorIs(t, <-served, http.ErrServerClosed)
	})

	git := fixture.clone(t, "Role")
	roleName, bindingName := uniqueName("callback-role"), uniqueName("callback-binding")
	role := fixture.manifest("Role", roleName)
	binding := fixture.manifest("RoleBinding", bindingName)
	binding["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": roleName},
		"subjects": []any{map[string]any{"kind": "User", "name": "native-rbac-callback-delegate"}},
	}
	git.write(t, "rbac/roles/"+roleName+".yaml", role)
	git.write(t, "rbac/rolebindings/"+bindingName+".yaml", binding)
	committed := git.commit(t)
	git.push(t)
	rbacNativeEventually(t, func() bool {
		return validations.Load() > 0 && dropped.Load() > 0
	}, "the real signed pre-receive must pass while its post-receive callback is actually lost")
	require.Equal(t, committed+"\trefs/heads/main", git.remoteTip(t), "callback failure cannot roll back committed Git")
	for _, kindAndName := range [][2]string{{"Role", roleName}, {"RoleBinding", bindingName}} {
		kind, name := kindAndName[0], kindAndName[1]
		expected := role["spec"]
		if kind == "RoleBinding" {
			expected = binding["spec"]
		}
		spec, err := json.Marshal(rbacNativeGraphQLSpec(t, expected))
		require.NoError(t, err)
		for _, api := range fixture.apis {
			var resource rbacNativeResource
			rbacNativeEventually(t, func() bool {
				var response gqlResponse
				resource, response = fixture.lookup(t, api, kind, name)
				return len(response.Errors) == 0 && resource.Metadata.UID != "" && resource.Metadata.DeletionTimestamp == nil
			}, "both APIs must recover the committed resource without its callback")
			require.JSONEq(t, string(spec), string(resource.Spec))
		}
	}
	for _, api := range fixture.apis {
		var response gqlResponse
		rbacNativeEventually(t, func() bool {
			response = secretTestGraphQLWithHeaders(t, api, fixture.token,
				`query($by:RoleBy!){role(by:$by){metadata{revision}}}`,
				map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": roleName}}},
				10*time.Second, map[string]string{"GitStore-Provenance-Include": "true"})
			var entries []struct{ Commit, ChangeCommit, Error string }
			return len(response.Errors) == 0 && json.Unmarshal(response.Extensions["provenance"], &entries) == nil &&
				len(entries) == 1 && entries[0].Error == "" && entries[0].Commit == "sha1:"+committed &&
				entries[0].ChangeCommit == "sha1:"+committed
		}, "provenance inspection must recover at the committed revision")
	}
	require.Equal(t, committed+"\trefs/heads/main", git.remoteTip(t), "repair must preserve the admitted canonical commit")

	dropping.Store(false)
	restoredName := uniqueName("callback-restored")
	git.write(t, "rbac/roles/"+restoredName+".yaml", fixture.manifest("Role", restoredName))
	git.commit(t)
	git.push(t)
	rbacNativeEventually(t, func() bool { return restored.Load() > 0 }, "the restored real callback must complete successfully")
	fixture.await(t, "Role", restoredName, false)
}

func TestRBACNativeEscalationParity(t *testing.T) {
	admin := ownedRBACNativeFixture(t)
	subject := os.Getenv("RBAC_TEST_SUBJECT")
	require.NotEmpty(t, subject)
	actor := admin
	actor.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	requireCleanupTime := func(t *testing.T) {
		t.Helper()
		if deadline, ok := t.Deadline(); ok && time.Until(deadline) < 3*time.Minute {
			t.Fatal("ending matrix before the process timeout to preserve temporary-grant cleanup")
		}
	}
	name := uniqueName("delegation-author")
	manifest := admin.manifest("Role", name)
	rules := []any{
		map[string]any{"resources": []string{"role", "roleBinding"}, "verbs": []string{"create", "update", "delete"}},
		map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}},
		map[string]any{"resources": []string{"repository"}, "verbs": []string{"read"}},
		map[string]any{"resources": []string{"repository/contents"}, "verbs": []string{"read", "write"}},
	}
	manifest["spec"] = map[string]any{"rules": rules}
	admin.create(t, "Role", manifest)
	binding := admin.manifest("RoleBinding", name)
	binding["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": name},
		"subjects": []any{map[string]any{"kind": "User", "name": subject}},
	}
	var authorUID string
	defer admin.revokeBinding(t, "RoleBinding", name, binding["spec"], &authorUID)
	authorBinding := admin.create(t, "RoleBinding", binding)
	authorUID = authorBinding.Metadata.UID
	clusterName := name + "-cluster"
	clusterManifest := admin.manifest("ClusterRole", clusterName)
	clusterRules := []any{
		map[string]any{"resources": []string{"clusterRole", "clusterRoleBinding"}, "verbs": []string{"create", "update", "delete"}},
		map[string]any{"resources": []string{"repository"}, "verbs": []string{"read"}},
		map[string]any{"resources": []string{"repository/contents"}, "verbs": []string{"read", "write"}},
	}
	clusterManifest["spec"] = map[string]any{"rules": clusterRules}
	admin.create(t, "ClusterRole", clusterManifest)
	clusterBinding := admin.manifest("ClusterRoleBinding", clusterName)
	clusterBinding["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "ClusterRole", "name": clusterName},
		"subjects": []any{map[string]any{"kind": "User", "name": subject}},
	}
	var clusterUID string
	defer admin.revokeBinding(t, "ClusterRoleBinding", clusterName, clusterBinding["spec"], &clusterUID)
	clusterGrant := admin.create(t, "ClusterRoleBinding", clusterBinding)
	clusterUID = clusterGrant.Metadata.UID
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			response := actor.query(t, api,
				`query($by:RepositoryBy!){repository(by:$by){id}}`,
				map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": admin.namespace, "name": "gitstore-system"}}})
			return len(response.Errors) == 0
		}, "the restricted author's repository grant must reach each replica")
	}

	type candidate struct {
		kind, name string
		spec       map[string]any
		allowed    bool
		operation  string
	}
	apply := func(t *testing.T, transport string, test candidate) {
		t.Helper()
		requireCleanupTime(t)
		operation := test.operation
		if operation == "" {
			operation = "create"
		}
		input := actor.manifest(test.kind, test.name)
		input["spec"] = test.spec
		var before rbacNativeResource
		if operation != "create" {
			var response gqlResponse
			before, response = admin.lookup(t, admin.apis[0], test.kind, test.name)
			require.Empty(t, response.Errors)
			require.NotEmpty(t, before.Metadata.UID)
			input["metadata"].(map[string]any)["resourceVersion"] = before.Metadata.ResourceVersion
		}
		if transport == "GraphQL" {
			if operation == "delete" {
				input = map[string]any{"id": before.ID, "resourceVersion": before.Metadata.ResourceVersion}
			}
			inputType := strings.ToUpper(operation[:1]) + operation[1:] + test.kind + "Input"
			var response gqlResponse
			for attempt := 0; attempt < 3; attempt++ {
				response = actor.query(t, actor.apis[0],
					fmt.Sprintf("mutation($input:%s!){%s%s(input:$input){%s{id}}}", inputType, operation, test.kind, rbacNativeField(test.kind)),
					map[string]any{"input": input})
				if operation == "create" || len(response.Errors) != 1 || attempt == 2 {
					break
				}
				var failure struct{ Extensions struct{ Code string } }
				if json.Unmarshal(response.Errors[0], &failure) != nil || failure.Extensions.Code != "CONFLICT" {
					break
				}
				current, lookup := admin.lookup(t, admin.apis[0], test.kind, test.name)
				require.Empty(t, lookup.Errors)
				require.Equal(t, before.Metadata.UID, current.Metadata.UID)
				if operation == "delete" {
					input["resourceVersion"] = current.Metadata.ResourceVersion
				} else {
					input["metadata"].(map[string]any)["resourceVersion"] = current.Metadata.ResourceVersion
				}
			}
			if !test.allowed {
				rbacNativeErrorCode(t, response, "FORBIDDEN")
				return
			}
			require.Empty(t, response.Errors)
		} else {
			git := actor.clone(t, test.kind)
			path := "rbac/" + strings.ToLower(test.kind) + "s/" + test.name + ".yaml"
			delete(input["metadata"].(map[string]any), "resourceVersion")
			if operation == "delete" {
				_, err := git.command(t, "rm", "--", path)
				require.NoError(t, err)
			} else {
				git.write(t, path, input)
			}
			git.commit(t)
			if !test.allowed {
				git.reject(t, "(forbidden)")
				return
			}
			git.push(t)
		}
		rbacNativeEventually(t, func() bool {
			for _, api := range admin.apis {
				resource, response := admin.lookup(t, api, test.kind, test.name)
				if operation == "delete" && len(response.Errors) == 1 &&
					strings.Contains(string(response.Errors[0]), `"code":"NOT_FOUND"`) {
					continue
				}
				if len(response.Errors) != 0 || resource.Metadata.UID == "" {
					return false
				}
				if operation == "delete" {
					if resource.Metadata.DeletionTimestamp == nil {
						return false
					}
				} else if resource.Metadata.DeletionTimestamp != nil ||
					operation == "update" && resource.Metadata.Generation <= before.Metadata.Generation {
					return false
				}
			}
			return true
		}, "both replicas must expose the admitted operation")
	}
	awaitAdmitted := func(t *testing.T, kind, name string, unresolved bool) {
		t.Helper()
		rbacNativeEventually(t, func() bool {
			for _, api := range admin.apis {
				resource, response := admin.lookup(t, api, kind, name)
				if len(response.Errors) != 0 || resource.Metadata.UID == "" || resource.Metadata.DeletionTimestamp != nil ||
					unresolved && resource.Status.Resolved != nil {
					return false
				}
			}
			return true
		}, "both APIs must expose the admitted resource without inventing an absent target's resolved UID")
	}
	pending := map[string][]candidate{}
	for _, transport := range []string{"GraphQL", "Git"} {
		covered, excessive, absent := uniqueName("covered"), uniqueName("excessive"), uniqueName("absent")
		deletable, coveredBinding := uniqueName("deletable"), uniqueName("covered-binding")
		roleSpec := func(verb string) map[string]any {
			return map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{verb}}}}
		}
		bindingSpec := func(role string) map[string]any {
			return map[string]any{
				"roleRef":  map[string]any{"kind": "Role", "name": role},
				"subjects": []any{map[string]any{"kind": "User", "name": "native-rbac-hypothetical-delegate"}},
			}
		}
		pending[transport] = []candidate{
			{"Role", excessive, roleSpec("delete"), true, ""},
			{"RoleBinding", absent, bindingSpec(absent), true, ""},
			{"Role", covered, roleSpec("delete"), true, "update"},
			{"ClusterRole", uniqueName("cluster-scope"), roleSpec("read"), true, ""},
			{"ClusterRoleBinding", uniqueName("cluster-bind"), map[string]any{
				"roleRef":  map[string]any{"kind": "ClusterRole", "name": "view"},
				"subjects": []any{map[string]any{"kind": "User", "name": "native-rbac-hypothetical-delegate"}},
			}, true, ""},
		}
		var condition any = map[string]any{"token": "owner"}
		if transport == "Git" {
			condition = "owner"
		}
		conditional := map[string]any{"rules": []any{map[string]any{
			"resources": []string{"product"}, "verbs": []string{"read"}, "when": []any{condition},
		}}}
		named := map[string]any{"rules": []any{map[string]any{
			"resources": []string{"product"}, "verbs": []string{"read"}, "resourceNames": []string{"delegated-product"},
		}}}
		cases := []candidate{
			{"Role", covered, roleSpec("read"), true, ""},
			{"Role", excessive, roleSpec("delete"), false, ""},
			{"Role", covered, roleSpec("delete"), false, "update"},
			{"Role", uniqueName("conditional"), conditional, true, ""},
			{"Role", uniqueName("named"), named, true, ""},
			{"RoleBinding", coveredBinding, bindingSpec(covered), true, ""},
			{"RoleBinding", absent, bindingSpec(absent), false, ""},
			{"RoleBinding", coveredBinding, map[string]any{
				"roleRef":  map[string]any{"kind": "Role", "name": covered},
				"subjects": []any{map[string]any{"kind": "Group", "name": "system:authenticated"}},
			}, true, "update"},
			{"RoleBinding", coveredBinding, nil, true, "delete"},
			{"Role", deletable, roleSpec("read"), true, ""},
			{"Role", deletable, nil, true, "delete"},
		}
		for _, cluster := range pending[transport][3:] {
			cluster.allowed = false
			cases = append(cases, cluster)
		}
		for _, test := range cases {
			if !t.Run(transport+"/"+test.operation+"/"+test.kind+"/"+test.name, func(t *testing.T) { apply(t, transport, test) }) {
				return
			}
		}
	}

	current, response := admin.lookup(t, admin.apis[0], "Role", name)
	require.Empty(t, response.Errors)
	rules = append(rules,
		map[string]any{"resources": []string{"role"}, "verbs": []string{"read", "escalate"}},
		map[string]any{"resources": []string{"roleBinding"}, "verbs": []string{"bind"}},
	)
	manifest["spec"] = map[string]any{"rules": rules}
	manifest["metadata"].(map[string]any)["resourceVersion"] = current.Metadata.ResourceVersion
	response = admin.query(t, admin.apis[0], `mutation($input:UpdateRoleInput!){updateRole(input:$input){role{id}}}`, map[string]any{"input": manifest})
	require.Empty(t, response.Errors)
	clusterCurrent, response := admin.lookup(t, admin.apis[0], "ClusterRole", clusterName)
	require.Empty(t, response.Errors)
	clusterRules = append(clusterRules,
		map[string]any{"resources": []string{"clusterRole"}, "verbs": []string{"read", "escalate"}},
		map[string]any{"resources": []string{"clusterRoleBinding"}, "verbs": []string{"bind"}},
	)
	clusterManifest["spec"] = map[string]any{"rules": clusterRules}
	clusterManifest["metadata"].(map[string]any)["resourceVersion"] = clusterCurrent.Metadata.ResourceVersion
	response = admin.query(t, admin.apis[0], `mutation($input:UpdateClusterRoleInput!){updateClusterRole(input:$input){clusterRole{id}}}`,
		map[string]any{"input": clusterManifest})
	require.Empty(t, response.Errors)
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			got, response := actor.lookup(t, api, "Role", name)
			cluster, clusterResponse := actor.lookup(t, api, "ClusterRole", clusterName)
			return len(response.Errors) == 0 && got.ID == current.ID && len(clusterResponse.Errors) == 0 && cluster.ID == clusterCurrent.ID
		}, "explicit delegation authority must reach each serving replica")
	}
	for _, transport := range []string{"GraphQL", "Git"} {
		for _, test := range pending[transport] {
			apply(t, transport, test)
			awaitAdmitted(t, test.kind, test.name, test.kind == "RoleBinding")
		}
	}
}
