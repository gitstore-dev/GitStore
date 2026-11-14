// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestRBACNativeColdStartRequiresCanonicalSource(t *testing.T) {
	fixture := ownedRBACNativeFixture(t)
	config := os.Getenv("RBAC_TEST_COLD_CONFIG")
	if config == "" {
		t.Skip("an isolated owned cold-process configuration is required")
	}
	binary := os.Getenv("RBAC_TEST_API_BINARY")
	endpoint := os.Getenv("RBAC_TEST_COLD_API")
	gitURI := os.Getenv("RBAC_TEST_GIT_GRPC_URI")
	require.NotEmpty(t, binary)
	require.NotEmpty(t, gitURI)
	parsed, err := url.Parse(endpoint)
	require.NoError(t, err)
	require.Equal(t, "http", parsed.Scheme)
	require.True(t, net.ParseIP(parsed.Hostname()).IsLoopback(), "the additional process must bind only a private loopback endpoint")
	require.NotContains(t, fixture.apis, endpoint)
	require.Nil(t, parsed.User)
	require.Empty(t, parsed.Path)
	require.Empty(t, parsed.RawQuery)
	require.Empty(t, parsed.Fragment)
	actor := fixture
	actor.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	list := `query($namespace:String!){products(namespace:$namespace,first:1){edges{node{id}}}}`
	variables := map[string]any{"namespace": fixture.namespace}
	assertStopped := func() {
		t.Helper()
		conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
		if conn != nil {
			_ = conn.Close()
		}
		require.Error(t, err, "never replace or probe an existing process as a cold fixture")
	}
	assertStopped()
	var sourceAttempts atomic.Uint64
	run := func(source string, ready, allowed bool, check func()) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		output, err := os.CreateTemp(t.TempDir(), "rbac-cold-*.log")
		require.NoError(t, err)
		defer output.Close()
		command := exec.CommandContext(ctx, binary, "-test.run", "^TestRBACNativeProcess$", "-test.timeout", "90s")
		command.Cancel = func() error { return command.Process.Signal(os.Interrupt) }
		command.WaitDelay = 15 * time.Second
		command.Env = append(os.Environ(),
			"GITSTORE_RBAC_NATIVE_TEST_OWNED=1",
			"GITSTORE_RBAC_NATIVE_TEST_CONFIG="+config,
			"GITSTORE_API__GIT_SERVICE__URI="+source)
		command.Stdout, command.Stderr = output, output
		require.NoError(t, command.Start())
		done := make(chan error, 1)
		go func() {
			done <- command.Wait()
			close(done)
		}()
		defer func() {
			cancel()
			<-done
			if ready {
				require.True(t, command.ProcessState.Success(), "healthy replacement must shut down gracefully and release its source leases")
			}
		}()
		client := &http.Client{Timeout: time.Second}
		started := time.Now()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case err := <-done:
				require.False(t, ready, "the healthy cold process must reach readiness before exiting")
				require.NoError(t, ctx.Err(), "a harness timeout is not proof of rejected startup")
				require.Error(t, err, "startup without canonical Git must fail rather than serve built-ins alone")
				require.Positive(t, sourceAttempts.Load(), "the cold process must actually attempt canonical-source access")
				return
			case <-ctx.Done():
				t.Fatal("cold-process readiness exceeded its bounded deadline")
			case <-ticker.C:
				if !ready && time.Since(started) >= 5*time.Second {
					require.Positive(t, sourceAttempts.Load(), "the cold process must actually attempt canonical-source access")
					return
				}
				response, err := client.Get(endpoint + "/ready")
				if err != nil {
					continue
				}
				status := response.StatusCode
				require.NoError(t, response.Body.Close())
				if !ready {
					require.Equal(t, http.StatusServiceUnavailable, status, "cold publication must never become ready without its source")
					continue
				}
				if status != http.StatusOK {
					continue
				}
				responseGraphQL := fixture.query(t, endpoint, `query{clusterRoles(first:1){edges{node{id}}}}`, nil)
				require.Empty(t, responseGraphQL.Errors)
				result := actor.query(t, endpoint, list, variables)
				if allowed {
					require.Empty(t, result.Errors, "a fresh process must reconstruct the admitted group grant")
					result = actor.query(t, endpoint,
						`mutation($input:CreateProductInput!){createProduct(input:$input){product{id}}}`,
						map[string]any{"input": map[string]any{
							"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
							"metadata": map[string]any{"namespace": fixture.namespace, "name": uniqueName("replacement-write")},
							"spec":     map[string]any{"title": "Replacement grant"},
						}})
					require.Empty(t, result.Errors)
					require.Empty(t, actor.query(t, endpoint, list, variables).Errors)
				} else {
					rbacNativeErrorCode(t, result, "FORBIDDEN")
				}
				if check != nil {
					check()
				}
				return
			}
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	unavailable := "dns:///" + listener.Addr().String()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			sourceAttempts.Add(1)
			_ = conn.Close()
		}
	}()
	run(unavailable, false, false, nil)
	assertStopped()
	run(gitURI, true, false, nil)
	assertStopped()

	group := os.Getenv("RBAC_TEST_GROUP")
	require.NotEmpty(t, group, "replacement requires the owned actor's trusted group")
	name := uniqueName("replacement-edit")
	binding := fixture.manifest("RoleBinding", name)
	binding["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "ClusterRole", "name": "edit"},
		"subjects": []any{map[string]any{"kind": "Group", "name": group}},
	}
	var uid string
	defer fixture.revokeBinding(t, "RoleBinding", name, binding["spec"], &uid)
	grant := fixture.create(t, "RoleBinding", binding)
	uid = grant.Metadata.UID
	for _, api := range fixture.apis {
		rbacNativeEventually(t, func() bool {
			response := actor.query(t, api, list, variables)
			if graphqlErrorHasCode(response.Errors, "FORBIDDEN") {
				return false
			}
			require.Empty(t, response.Errors)
			return true
		}, "both existing replicas must serve the grant before replacement")
	}
	require.True(t, strings.HasPrefix(gitURI, "dns:///"))
	backend, err := url.Parse("http://" + strings.TrimPrefix(gitURI, "dns:///"))
	require.NoError(t, err)
	require.True(t, net.ParseIP(backend.Hostname()).IsLoopback(), "only the owned loopback Git service may be faulted")
	require.Nil(t, backend.User)
	require.Empty(t, backend.Path)
	require.Empty(t, backend.RawQuery)
	require.Empty(t, backend.Fragment)
	protocols := &http.Protocols{}
	protocols.SetUnencryptedHTTP2(true)
	transport := &http.Transport{Protocols: protocols, ResponseHeaderTimeout: 35 * time.Second}
	defer transport.CloseIdleConnections()
	proxy := httputil.NewSingleHostReverseProxy(backend)
	proxy.Transport = transport
	var dropping atomic.Bool
	var dropped atomic.Uint64
	server := &http.Server{Protocols: protocols, ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if dropping.Load() {
				dropped.Add(1)
				writer.Header().Set("Content-Type", "application/grpc")
				writer.Header().Set("Grpc-Status", "14")
				writer.Header().Set("Grpc-Message", "owned canonical source interruption")
				writer.WriteHeader(http.StatusOK)
				return
			}
			proxy.ServeHTTP(writer, request)
		})}
	sourceListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- server.Serve(sourceListener) }()
	defer func() {
		require.NoError(t, server.Close())
		require.ErrorIs(t, <-served, http.ErrServerClosed)
	}()
	run("dns:///"+sourceListener.Addr().String(), true, true, func() {
		client := &http.Client{Timeout: time.Second}
		readyStatus := func(api string) int {
			response, err := client.Get(api + "/ready")
			if err != nil {
				return 0
			}
			require.NoError(t, response.Body.Close())
			return response.StatusCode
		}
		dropping.Store(true)
		rbacNativeEventually(t, func() bool {
			return dropped.Load() > 0 && readyStatus(endpoint) == http.StatusServiceUnavailable
		}, "the serving replacement must lose readiness after actual canonical RPC failures")
		result := secretTestGraphQLWithTimeout(t, endpoint, fixture.token,
			`query{clusterRoles(first:1){edges{node{id}}}}`, nil, 5*time.Second)
		require.True(t, result.Status == http.StatusServiceUnavailable ||
			graphqlErrorHasCode(result.Errors, "AUTHORIZATION_UNAVAILABLE"),
			"the retained administrator cannot bypass known source unavailability")
		for _, api := range fixture.apis {
			require.Equal(t, http.StatusOK, readyStatus(api), "a private source fault cannot invalidate unaffected replicas")
			require.Empty(t, actor.query(t, api, list, variables).Errors)
		}
		dropping.Store(false)
		rbacNativeEventually(t, func() bool { return readyStatus(endpoint) == http.StatusOK },
			"the same process must reconstruct complete canonical state before becoming ready again")
		require.Empty(t, actor.query(t, endpoint, list, variables).Errors)
	})
	assertStopped()
	fixture.revokeBinding(t, "RoleBinding", name, binding["spec"], &uid)
	for _, api := range fixture.apis {
		rbacNativeEventually(t, func() bool {
			response := actor.query(t, api, list, variables)
			return graphqlErrorHasCode(response.Errors, "FORBIDDEN")
		}, "both existing replicas must enforce revocation before the next replacement")
	}
	run(gitURI, true, false, nil)
	assertStopped()
}

func rbacRuntimePublicationMatches(t *testing.T, path string, offset int64, subject string, revisions map[string]bool) bool {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	_, err = file.Seek(offset, io.SeekStart)
	require.NoError(t, err)
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	for scanner.Scan() {
		var decision struct {
			Subject, Action, Outcome string
			Tiers                    []struct{ GitRevision string } `json:"authorization_tiers"`
		}
		if json.Unmarshal(scanner.Bytes(), &decision) != nil || decision.Subject != subject ||
			decision.Action != "product.list" || decision.Outcome != "allow" || len(decision.Tiers) != len(revisions) {
			continue
		}
		matches := make(map[string]bool, len(revisions))
		for _, tier := range decision.Tiers {
			if revisions[tier.GitRevision] {
				matches[tier.GitRevision] = true
			}
		}
		if len(matches) == len(revisions) {
			return true
		}
	}
	require.NoError(t, scanner.Err())
	return false
}

type rbacRuntimeWatchError struct {
	errors []json.RawMessage
}

func (e *rbacRuntimeWatchError) Error() string {
	return fmt.Sprintf("product watch errors: %s", e.errors)
}

func readRBACRuntimeProductEvent(conn *websocket.Conn) (productWatchWireEvent, bool, error) {
	for {
		var message struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			return productWatchWireEvent{}, false, err
		}
		switch message.Type {
		case "ping":
			if err := conn.WriteJSON(map[string]any{"type": "pong"}); err != nil {
				return productWatchWireEvent{}, false, err
			}
		case "complete":
			return productWatchWireEvent{}, true, nil
		case "error":
			var failures []json.RawMessage
			if err := json.Unmarshal(message.Payload, &failures); err != nil {
				return productWatchWireEvent{}, false, err
			}
			return productWatchWireEvent{}, false, &rbacRuntimeWatchError{errors: failures}
		case "next":
			var payload struct {
				Data struct {
					Watch productWatchWireEvent `json:"watchProducts"`
				} `json:"data"`
				Errors []json.RawMessage `json:"errors"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				return productWatchWireEvent{}, false, err
			}
			if len(payload.Errors) != 0 {
				return productWatchWireEvent{}, false, &rbacRuntimeWatchError{errors: payload.Errors}
			}
			if payload.Data.Watch.Type == "" || payload.Data.Watch.ResourceVersion == "" {
				return productWatchWireEvent{}, false, errors.New("product watch omitted event identity")
			}
			return payload.Data.Watch, false, nil
		default:
			return productWatchWireEvent{}, false, fmt.Errorf("unexpected product watch message %q", message.Type)
		}
	}
}

func TestRBACNativeRuntimeGrantRevoke(t *testing.T) {
	admin := ownedRBACNativeFixture(t)
	actor := admin
	actor.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	group := os.Getenv("RBAC_TEST_GROUP")
	require.NotEmpty(t, group, "an owned actor's real trusted UserDir group is required")
	subject := os.Getenv("RBAC_TEST_SUBJECT")
	require.NotEmpty(t, subject, "the real owned actor subject is required for serving-revision joins")
	logs := []string{os.Getenv("RBAC_TEST_API_A_LOG"), os.Getenv("RBAC_TEST_API_B_LOG")}
	for _, path := range logs {
		require.NotEmpty(t, path, "owned decision logs are required to establish the initialized watch fixture")
	}
	list := `query($namespace:String!){products(namespace:$namespace,first:1){edges{node{id}}}}`
	create := `mutation($input:CreateProductInput!){createProduct(input:$input){product{id metadata{uid name namespace}}}}`
	productInput := func(namespace, name string) map[string]any {
		return map[string]any{"input": map[string]any{
			"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
			"metadata": map[string]any{"namespace": namespace, "name": name},
			"spec":     map[string]any{"title": name},
		}}
	}
	for _, api := range admin.apis {
		rbacNativeErrorCode(t, actor.query(t, api, list, map[string]any{"namespace": admin.namespace}), "FORBIDDEN")
		rbacNativeErrorCode(t, actor.query(t, api, create, productInput(admin.namespace, uniqueName("before-grant"))), "FORBIDDEN")
	}

	otherNamespace := uniqueName("rbac-isolation")
	response := admin.query(t, admin.apis[0],
		`mutation($input:CreateNamespaceInput!){createNamespace(input:$input){namespace{metadata{uid}}}}`,
		map[string]any{"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace",
			"metadata": map[string]any{"name": otherNamespace},
			"spec":     map[string]any{"title": otherNamespace, "tier": "ORGANIZATION"},
		}})
	require.Empty(t, response.Errors)
	for _, api := range admin.apis {
		response = admin.query(t, api, list, map[string]any{"namespace": otherNamespace})
		require.Empty(t, response.Errors, "isolation requires an initialized, available second namespace")
	}
	repository := uniqueName("rbac-watch-products")
	response = admin.query(t, admin.apis[0],
		`mutation($input:CreateRepositoryInput!){createRepository(input:$input){repository{id}}}`,
		map[string]any{"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1", "kind": "Repository",
			"metadata": map[string]any{"namespace": admin.namespace, "name": repository},
			"spec":     map[string]any{"defaultBranch": "main", "visibility": "PRIVATE", "storageClass": "standard"},
		}})
	require.Empty(t, response.Errors)
	git := rbacNativeGit{fixture: admin, dir: t.TempDir()}
	_, err := git.command(t, "init", "--initial-branch=main")
	require.NoError(t, err)
	_, err = git.command(t, "remote", "add", "origin", admin.gitURL+"/"+admin.namespace+"/"+repository)
	require.NoError(t, err)

	name := uniqueName("runtime-edit")
	binding := admin.manifest("RoleBinding", name)
	binding["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "ClusterRole", "name": "edit"},
		"subjects": []any{map[string]any{"kind": "Group", "name": group}},
	}
	var uid string
	defer admin.revokeBinding(t, "RoleBinding", name, binding["spec"], &uid)
	grant := admin.create(t, "RoleBinding", binding)
	uid = grant.Metadata.UID
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			result := actor.query(t, api, list, map[string]any{"namespace": admin.namespace})
			if len(result.Errors) == 1 && graphqlErrorHasCode(result.Errors, "FORBIDDEN") {
				return false
			}
			require.Empty(t, result.Errors)
			return true
		}, "the granted group must reach each API's serving publication")
		response = actor.query(t, api, create, productInput(admin.namespace, uniqueName("granted-write")))
		require.Empty(t, response.Errors, "each API must enforce the real group grant")
		rbacNativeErrorCode(t, actor.query(t, api, list, map[string]any{"namespace": otherNamespace}), "FORBIDDEN")
		rbacNativeErrorCode(t, actor.query(t, api, create, productInput(otherNamespace, uniqueName("cross-namespace"))), "FORBIDDEN")
	}

	revisions := make(map[string]bool, 2)
	for _, namespace := range []string{"gitstore-system", admin.namespace} {
		var output []byte
		rbacNativeEventually(t, func() bool {
			var err error
			output, err = git.command(t, "ls-remote", admin.gitURL+"/"+namespace+"/gitstore-system", "refs/heads/main")
			message := strings.ReplaceAll(string(output), admin.token, "[redacted]")
			if err != nil && (strings.Contains(message, "returned error: 503") || strings.Contains(message, "returned error: 403")) {
				return false
			}
			require.NoError(t, err, "canonical tip read failed: %s", message)
			return true
		}, "the canonical tip read must recover from bounded pre-execution unavailability")
		fields := strings.Fields(string(output))
		require.Len(t, fields, 2)
		revisions["main@sha1:"+fields[0]] = true
	}
	for index, api := range admin.apis {
		info, err := os.Stat(logs[index])
		require.NoError(t, err)
		offset := info.Size()
		rbacNativeEventually(t, func() bool {
			result := actor.query(t, api, list, map[string]any{"namespace": admin.namespace})
			require.Empty(t, result.Errors)
			return rbacRuntimePublicationMatches(t, logs[index], offset, subject, revisions)
		}, "stable watches require both APIs to serve the actual post-setup canonical tips")
	}

	connections := make([]*websocket.Conn, 0, len(admin.apis))
	for _, api := range admin.apis {
		var conn *websocket.Conn
		rbacNativeEventually(t, func() bool {
			var err error
			conn, err = dialCapacitySubscription(api, actor.token, "rbac-runtime",
				`subscription($namespace:String!){watchProducts(namespace:$namespace,resourceVersion:"__product_watch_bootstrap__"){type name resourceVersion}}`,
				map[string]any{"namespace": admin.namespace}, 30*time.Second)
			require.NoError(t, err)
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
			observed := false
			for {
				event, terminal, err := readRBACRuntimeProductEvent(conn)
				var failure *rbacRuntimeWatchError
				if !observed && errors.As(err, &failure) && graphqlErrorHasCode(failure.errors, "AUTHORIZATION_UNAVAILABLE") {
					require.NoError(t, conn.Close())
					return false
				}
				require.NoError(t, err)
				require.False(t, terminal, "the granted subscription must bootstrap before revocation")
				if event.Type == "BOOKMARK" {
					return true
				}
				require.Contains(t, []string{"ADDED", "MODIFIED", "DELETED"}, event.Type)
				observed = true
			}
		}, "the granted subscription must bootstrap from a complete serving publication")
		defer conn.Close()
		connections = append(connections, conn)
	}
	for iteration := 0; iteration < 2; iteration++ {
		if iteration > 0 {
			time.Sleep(2 * time.Second)
		}
		product := uniqueName("runtime-event")
		require.NoError(t, os.WriteFile(filepath.Join(git.dir, product+".md"), []byte(validProductFixture(product, admin.namespace)), 0600))
		git.commit(t)
		git.push(t)
		for _, conn := range connections {
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
			for {
				event, terminal, err := readRBACRuntimeProductEvent(conn)
				require.NoError(t, err)
				require.False(t, terminal, "the same subscription must remain live across authorization checks")
				if event.Type == "ADDED" && event.Name == product {
					break
				}
			}
		}
	}

	admin.revokeBinding(t, "RoleBinding", name, binding["spec"], &uid)
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			result := actor.query(t, api, list, map[string]any{"namespace": admin.namespace})
			if len(result.Errors) == 1 && graphqlErrorHasCode(result.Errors, "FORBIDDEN") {
				return true
			}
			require.Empty(t, result.Errors)
			return false
		}, "revocation must become a policy denial on each API")
		rbacNativeErrorCode(t, actor.query(t, api, create, productInput(admin.namespace, uniqueName("revoked-write"))), "FORBIDDEN")
	}
	canary := uniqueName("after-revocation")
	require.NoError(t, os.WriteFile(filepath.Join(git.dir, canary+".md"), []byte(validProductFixture(canary, admin.namespace)), 0600))
	git.commit(t)
	git.push(t)
	for _, conn := range connections {
		require.NoError(t, conn.SetReadDeadline(time.Now().Add(30*time.Second)))
		for {
			event, terminal, err := readRBACRuntimeProductEvent(conn)
			var failure *rbacRuntimeWatchError
			if errors.As(err, &failure) {
				require.Len(t, failure.errors, 1)
				require.True(t, graphqlErrorHasCode(failure.errors, "FORBIDDEN") ||
					graphqlErrorHasCode(failure.errors, "AUTHORIZATION_UNAVAILABLE"),
					"only an explicit authorization termination can establish fail-closed delivery: %v", err)
				break
			}
			require.NoError(t, err, "transport errors and timeouts are not evidence of subscription revocation")
			if terminal {
				break
			}
			require.Contains(t, []string{"BOOKMARK", "ADDED", "MODIFIED", "DELETED"}, event.Type)
			require.NotEqual(t, canary, event.Name, "revoked subscriptions must not deliver a post-denial write; earlier frames may already be buffered")
		}
	}
}
