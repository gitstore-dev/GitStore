// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

type directoryAPI struct {
	name, base string
}

type directoryGraphQLResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Extensions struct {
			Code        string `json:"code"`
			Diagnostics []struct {
				Reason string `json:"reason"`
			} `json:"diagnostics"`
		} `json:"extensions"`
	} `json:"errors"`
}

func (r *directoryTestRuntime) startAPIs(t *testing.T, operator string, sharedSource ...bool) ([]directoryAPI, func(string) string) {
	t.Helper()
	privateDir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyFile := filepath.Join(privateDir, "issuer.pem")
	require.NoError(t, os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}), 0600))
	r.command(t, nil, "cp", keyFile, r.receiver+":/issuer.pem")
	issuerTimeout := "8m"
	tokenTTL := 5 * time.Minute
	if r.capacity {
		issuerTimeout = "2h"
		tokenTTL = time.Hour
	}
	r.command(t, nil, "exec", "-d", "-e", "GITSTORE_TEST_OWNED_OIDC_KEY_FILE=/issuer.pem", r.receiver,
		"/directory-receiver.test", "-test.run=^TestOwnedUserDirectoryIssuer$", "-test.timeout="+issuerTimeout)
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "docker", "exec", r.postgres, "wget", "-qO", "/dev/null",
			"http://directory-receiver:8086/health/ready").Run() == nil
	}, 20*time.Second, 100*time.Millisecond, "owned OIDC discovery/JWKS process must be responsive")

	apiDir, err := filepath.Abs("../../gitstore-api")
	require.NoError(t, err)
	binary := filepath.Join(privateDir, "api")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/server")
	build.Dir, build.Env = apiDir, append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build owned API: %s", output)
	policy := fmt.Sprintf(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
defaultDeny: true
clusterRoles:
  directory-admin:
    rules:
      - resources: [user, serviceAccount, namespace]
        verbs: [create, read, list, update, delete]
      - resources: [user/group]
        verbs: [update]
  catalog-viewer:
    rules:
      - resources: [user]
        verbs: [read]
      - resources: [user]
        verbs: [update]
        when: [subjectUsername]
  api-internal:
    rules:
      - resources: [repository]
        verbs: [create, delete]
      - resources: [repository/contents]
        verbs: [read, write]
  workload-token:
    rules:
      - resources: [serviceAccount/token]
        verbs: [issue]
        resourceNames: [owned-ci-bot]
clusterRoleBindings:
  - roleRef: {kind: ClusterRole, name: directory-admin}
    subjects: [{kind: User, name: %q}]
  - roleRef: {kind: ClusterRole, name: catalog-viewer}
    subjects: [{kind: Group, name: "system:group:merchandiser"}]
  - roleRef: {kind: ClusterRole, name: catalog-viewer}
    subjects: [{kind: Group, name: "system:group:ci"}]
  - roleRef: {kind: ClusterRole, name: api-internal}
    subjects: [{kind: User, name: "system:api"}]
  - roleRef: {kind: ClusterRole, name: workload-token}
    subjects: [{kind: ServiceAccount, name: owned-ci-bot}]
`, operator)
	if r.capacity {
		policy = strings.Replace(policy, "resources: [user]\n        verbs: [read]",
			"resources: [user, serviceAccount]\n        verbs: [read]", 1)
	}
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "policy.yaml"), []byte(policy), 0600))
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	apiImage, apiEntrypoint := "postgres:16-alpine", "/fixtures/api"
	if r.capacity {
		revisionCommand := exec.Command("git", "rev-parse", "HEAD")
		revisionCommand.Dir = root
		revision, err := revisionCommand.Output()
		require.NoError(t, err)
		tag := "gitstore-userdir-api:" + r.owner[:12]
		r.command(t, nil, "build", "-q", "--build-arg", "GIT_REVISION="+strings.TrimSpace(string(revision)),
			"--label", "dev.gitstore.test_owner="+r.owner,
			"-f", filepath.Join(root, "docker/api.Dockerfile"), "-t", tag, root)
		t.Cleanup(func() { r.command(t, nil, "image", "rm", tag) })
		apiImage = strings.TrimSpace(r.command(t, nil, "image", "inspect", "--format", "{{.Id}}", tag))
		apiEntrypoint = "/app/api"
	}
	source := len(sharedSource) != 0 && sharedSource[0]
	sourceSigningKey := ""
	if source {
		r.startScylla(t, root)
		_, signingKey, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		encoded, err := x509.MarshalPKCS8PrivateKey(signingKey)
		require.NoError(t, err)
		sourceSigningKey = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	}
	gitImage := "gitstore-userdir-git:" + r.owner[:12]
	r.command(t, nil, "build", "-q", "-f", filepath.Join(root, "docker/git-service.Dockerfile"), "-t", gitImage, root)
	t.Cleanup(func() { r.command(t, nil, "image", "rm", gitImage) })
	gitName := "gitstore-userdir-git-" + r.owner[:12]
	subnet := strings.TrimSpace(r.command(t, nil, "network", "inspect", "--format", "{{(index .IPAM.Config 0).Subnet}}", r.network))
	networkIP, networkRange, err := net.ParseCIDR(subnet)
	require.NoError(t, err)
	apiIP := networkIP.To4()
	require.NotNil(t, apiIP)
	apiIP[3] = 100
	require.True(t, networkRange.Contains(apiIP))
	gitConfig := fmt.Sprintf(`[git_service]
data_dir = "/var/lib/gitstore/repos"
[git_service.catalog]
uri = "dns:///%s:6000"
[grpc_auth]
hmac_secret = %q
[log]
level = "error"
`, apiIP.String(), r.secret)
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "git.toml"), []byte(gitConfig), 0600))
	repos := filepath.Join(privateDir, "repos")
	require.NoError(t, os.Mkdir(repos, 0700))
	r.command(t, nil, "run", "-d", "--name", gitName, "--label", "dev.gitstore.test_owner="+r.owner,
		"--network", r.network, "--memory", "384m", "--cpus", "1",
		"-v", privateDir+":/fixtures:ro", "-v", repos+":/var/lib/gitstore/repos",
		gitImage, "/app/git-service", "--config-file", "/fixtures/git.toml")
	r.cleanupContainer(t, gitName, true)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(strings.ReplaceAll(r.command(t, nil, "logs", "--tail", "20", gitName), r.secret, "[redacted]"))
		}
	})
	require.Eventually(t, func() bool {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "docker", "exec", gitName, "nc", "-z", "127.0.0.1", "50051").Run() == nil
	}, 15*time.Second, 100*time.Millisecond, "singleton Git process must be responsive")
	var apis []directoryAPI
	issuer := r.issuerURI
	if issuer == "" {
		issuer = "http://directory-receiver:8086"
	}
	for replica := range 2 {
		apiIP[3] = byte(100 + replica)
		name := fmt.Sprintf("gitstore-userdir-api-%s-%d", r.owner[:12], replica)
		config := fmt.Sprintf(`[api]
admin_port = 4000
storefront_port = 4001
git_port = 9000
grpc_port = 6000
[api.git_service]
uri = "dns:///%s:50051"
[api.auth.authn]
chain = ["oidc-jwt", "anonymous"]
[api.auth.oidc_jwt]
issuer_uri = %q
audience = "owned-directory"
clock_skew = "0s"
[api.auth.authz]
provider = "rbac-local"
[api.auth.rbac_local]
policy_file = "/fixtures/policy.yaml"
[api.auth.userdir]
provider = "kratos"
[api.auth.kratos]
admin_uri = "http://directory-kratos:4434"
cache_ttl = "500ms"
cache_entries = 256
customer_groups = ["customers"]
[[api.auth.kratos.mappings]]
provider = "oidc-jwt"
issuer = %q
mode = "uid"
[api.notification]
provider = "development"
api_key = %q
[api.notification.development]
smtp_uri = "smtps://mailslurper:1025/?skip_ssl_verify=true"
[grpc_auth]
hmac_secret = %q
[log]
level = "info"
format = "json"
`, gitName, issuer, issuer, r.secret, r.secret)
		if source {
			config = strings.Replace(config, `chain = ["oidc-jwt", "anonymous"]`,
				`chain = ["serviceaccount-assertion", "serviceaccount-jwt", "oidc-jwt", "anonymous"]`, 1)
			config = strings.Replace(config, `level = "info"`, `level = "warn"`, 1)
			config += fmt.Sprintf(`
[api.auth.serviceaccount]
signing_key = '''%s'''
clock_skew = "0s"
[[api.auth.kratos.mappings]]
provider = "serviceaccount-jwt"
issuer = "gitstore"
mode = "serviceaccount_uid"
[[api.auth.kratos.mappings]]
provider = "serviceaccount-assertion"
issuer = "system:serviceaccount:owned-ci-bot"
mode = "serviceaccount_uid"
[api.datastore]
backend = "scylla"
[api.datastore.scylla]
hosts = ["scylla:9042"]
keyspace = "gitstore"
[api.watch.journal.cdc]
confidence_window = "500ms"
`, sourceSigningKey)
		}
		configFile := filepath.Join(privateDir, fmt.Sprintf("api-%d.toml", replica))
		if r.capacity {
			config = strings.Replace(config, `cache_ttl = "500ms"`, `cache_ttl = "5m"`, 1)
			config += `
[api.rate_limit]
per_second = 1000
burst = 2000
[api.admin.limits]
principal_per_second = 500
principal_burst = 2000
`
		}
		require.NoError(t, os.WriteFile(configFile, []byte(config), 0600))
		r.command(t, nil, "run", "-d", "--name", name, "--label", "dev.gitstore.test_owner="+r.owner,
			"--network", r.network, "--ip", apiIP.String(), "--memory", "384m", "--cpus", "1",
			"-p", "127.0.0.1::4000", "-p", "127.0.0.1::4001",
			"-v", privateDir+":/fixtures:ro", "--entrypoint", apiEntrypoint,
			apiImage, "--config-file", fmt.Sprintf("/fixtures/api-%d.toml", replica))
		r.cleanupContainer(t, name, true)
		t.Cleanup(func() {
			if t.Failed() {
				t.Log(strings.ReplaceAll(r.command(t, nil, "logs", "--tail", "20", name), r.secret, "[redacted]"))
			}
		})
		port := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{(index (index .NetworkSettings.Ports "4000/tcp") 0).HostPort}}`, name))
		api := directoryAPI{name: name, base: "http://127.0.0.1:" + port}
		require.Eventually(t, func() bool {
			response, err := r.client.Get(api.base + "/ready")
			if err != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 90*time.Second, 250*time.Millisecond, "owned API process must be healthy")
		apis = append(apis, api)
	}
	return apis, func(subject string) string {
		t.Helper()
		now := time.Now()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": "http://directory-receiver:8086", "sub": subject, "aud": "owned-directory",
			"iat": now.Unix(), "exp": now.Add(tokenTTL).Unix(),
			"groups": []string{"system:staff", "system:group:forged"},
		})
		token.Header["kid"] = "owned-directory"
		signed, err := token.SignedString(key)
		require.NoError(t, err)
		return signed
	}
}

func (r *directoryTestRuntime) startScylla(t *testing.T, root string) {
	t.Helper()
	name := "gitstore-userdir-scylla-" + r.owner[:12]
	volume := name + "-data"
	override := fmt.Sprintf(`services:
  scylla:
    container_name: %s
    labels: {dev.gitstore.test_owner: %s}
    ports: !override []
    command: --developer-mode=1 --overprovisioned=1 --smp=1 --memory=1536M --reserve-memory=0M
    mem_limit: 2g
    cpus: "1"
  scylla-init:
    container_name: %s-init
    labels: {dev.gitstore.test_owner: %s}
    mem_limit: 256m
    cpus: "0.5"
networks:
  gitstore-network:
    external: true
    name: %s
volumes:
  scylla-data:
    name: %s
    labels: {dev.gitstore.test_owner: %s}
`, name, r.owner, name, r.owner, r.network, volume, r.owner)
	file := filepath.Join(t.TempDir(), "scylla.yml")
	require.NoError(t, os.WriteFile(file, []byte(override), 0600))
	args := []string{"compose", "--project-name", r.network,
		"-f", filepath.Join(root, "compose.yml"), "-f", filepath.Join(root, "compose.scylla.yml"), "-f", file}
	t.Cleanup(func() {
		label := strings.TrimSpace(r.command(t, nil, "volume", "inspect", "--format",
			`{{index .Labels "dev.gitstore.test_owner"}}`, volume))
		if label != r.owner {
			t.Error("refusing cleanup of a Scylla volume with unexpected ownership")
			return
		}
		for _, container := range []string{name, name + "-init"} {
			if strings.TrimSpace(r.command(t, nil, "inspect", "--format",
				`{{index .Config.Labels "dev.gitstore.test_owner"}}`, container)) != r.owner {
				t.Error("refusing cleanup of a Scylla container with unexpected ownership")
				return
			}
		}
		r.command(t, nil, append(append([]string{}, args...), "down")...)
		r.command(t, nil, "volume", "rm", volume)
	})
	r.command(t, nil, append(append([]string{}, args...), "up", "-d", "--wait", "--wait-timeout", "90", "scylla", "scylla-init")...)
	for _, container := range []string{name, name + "-init"} {
		require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{index .Config.Labels "dev.gitstore.test_owner"}}`, container)))
	}
	require.Equal(t, "0", strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.ExitCode}}", name+"-init")))
}

func (r *directoryTestRuntime) graphql(t *testing.T, api directoryAPI, token, query string) directoryGraphQLResponse {
	t.Helper()
	status, data := r.requestAt(t, api.base, http.MethodPost, "/admin/graphql", map[string]string{"query": query},
		http.Header{"Authorization": {"Bearer " + token}})
	require.Equal(t, http.StatusOK, status)
	var response directoryGraphQLResponse
	require.NoError(t, json.Unmarshal(data, &response))
	return response
}

func directoryWebSocket(t *testing.T, api directoryAPI, token string) *websocket.Conn {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{"graphql-transport-ws"}, HandshakeTimeout: 3 * time.Second}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(api.base, "http")+"/admin/graphql", nil)
	if response != nil {
		response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	require.NoError(t, conn.WriteJSON(map[string]any{
		"type": "connection_init", "payload": map[string]any{"Authorization": "Bearer " + token},
	}))
	var ack struct {
		Type string `json:"type"`
	}
	require.NoError(t, conn.ReadJSON(&ack))
	require.Equal(t, "connection_ack", ack.Type)
	return conn
}

func TestOwnedUserDirectoryAdministrationAcrossAPIs(t *testing.T) {
	r := startDirectoryTestRuntime(t, true)
	operator := r.createHuman(t, "staff", "operator", "operator@example.test")
	viewer := r.createHuman(t, "staff", "viewer", "viewer@example.test")
	customer := r.createHuman(t, "customer", "customer", "customer@example.test")
	for _, identity := range []directoryTestIdentity{operator, viewer, customer} {
		status, _ := r.request(t, http.MethodPatch, "/admin/identities/"+identity.ID, []map[string]any{
			{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
				{"value": identity.Traits["email"], "verified": true, "via": "email", "status": "completed"},
			}},
			{"op": "add", "path": "/metadata_admin/setup_complete", "value": true},
		})
		require.Equal(t, http.StatusOK, status)
	}
	apis, token := r.startAPIs(t, operator.ID)
	operatorToken, viewerToken, customerToken := token(operator.ID), token(viewer.ID), token(customer.ID)
	operatorID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + operator.ID))
	viewerID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + viewer.ID))
	customerID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Customer/" + customer.ID))
	read := fmt.Sprintf(`{node(id:%q){id ... on User{metadata{name} groups lifecycle{state}}}}`, operatorID)
	for _, api := range apis {
		require.Empty(t, r.graphql(t, api, operatorToken, read).Errors)
		denied := r.graphql(t, api, viewerToken, read)
		require.NotEmpty(t, denied.Errors)
		require.Equal(t, "FORBIDDEN", denied.Errors[0].Extensions.Code)
		customerDenied := r.graphql(t, api, customerToken, read)
		require.NotEmpty(t, customerDenied.Errors)
		require.Equal(t, "FORBIDDEN", customerDenied.Errors[0].Extensions.Code,
			"Customer credentials cannot enter Admin even with forged JWT groups")
		for _, operation := range []string{"updateUser", "suspendUser", "deactivateUser", "reactivateUser",
			"deleteUser", "resendStaffInvitation", "assignUserGroup", "removeUserGroup", "setUserEmailDeliverability"} {
			input := fmt.Sprintf("id:%q", operatorID)
			switch operation {
			case "assignUserGroup", "removeUserGroup":
				input += `,group:"merchandiser"`
			case "setUserEmailDeliverability":
				input += ",deliverable:false"
			case "updateUser":
				input += `,fullName:"Denied edit"`
			}
			payload := "actor{id}"
			if operation == "resendStaffInvitation" {
				payload = "staff{id}"
			}
			denied := r.graphql(t, api, viewerToken, fmt.Sprintf(`mutation{%s(input:{%s}){%s}}`, operation, input, payload))
			require.NotEmpty(t, denied.Errors)
			require.Equal(t, "FORBIDDEN", denied.Errors[0].Extensions.Code, operation)
		}
	}
	customerAssign := fmt.Sprintf(`mutation{assignUserGroup(input:{id:%q,group:"customers"}){actor{id ... on User{groups metadata{generation resourceVersion}}}}}`, customerID)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, customerAssign).Errors)
	require.NotEmpty(t, r.graphql(t, apis[1], operatorToken, strings.Replace(customerAssign, `"customers"`, `"vip"`, 1)).Errors)
	require.NotEmpty(t, r.graphql(t, apis[1], operatorToken, strings.Replace(customerAssign, "assignUserGroup", "removeUserGroup", 1)).Errors)
	assign := fmt.Sprintf(`mutation{assignUserGroup(input:{id:%q,group:"merchandiser"}){actor{id ... on User{groups}}}}`, viewerID)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, assign).Errors)
	require.Eventually(t, func() bool {
		return len(r.graphql(t, apis[1], viewerToken, read).Errors) == 0
	}, 3*time.Second, 100*time.Millisecond, "other API must refresh directory membership within its configured TTL")
	ws := directoryWebSocket(t, apis[1], viewerToken)
	_, _, wsErr := ws.ReadMessage()
	var closed *websocket.CloseError
	require.ErrorAs(t, wsErr, &closed, "an idle authenticated transport must not outlive directory freshness")
	require.Equal(t, websocket.CloseNormalClosure, closed.Code)
	reserved := strings.Replace(assign, `"merchandiser"`, `"system:staff"`, 1)
	require.NotEmpty(t, r.graphql(t, apis[0], operatorToken, reserved).Errors)
	selfSuspend := fmt.Sprintf(`mutation{suspendUser(input:{id:%q}){actor{id}}}`, operatorID)
	selfDenied := r.graphql(t, apis[0], operatorToken, selfSuspend)
	require.NotEmpty(t, selfDenied.Errors)
	require.Equal(t, "BAD_USER_INPUT", selfDenied.Errors[0].Extensions.Code)
	require.NotEmpty(t, selfDenied.Errors[0].Extensions.Diagnostics)
	require.Equal(t, "FORBIDDEN_SELF_EDIT", selfDenied.Errors[0].Extensions.Diagnostics[0].Reason)
	update := fmt.Sprintf(`mutation{updateUser(input:{id:%q,fullName:"Native API viewer"}){actor{id ... on User{fullName metadata{generation resourceVersion}}}}}`, viewerID)
	require.Empty(t, r.graphql(t, apis[1], viewerToken, update).Errors)
	for _, operation := range []string{"suspendUser", "deactivateUser", "reactivateUser"} {
		query := fmt.Sprintf(`mutation{%s(input:{id:%q}){actor{id ... on User{lifecycle{state}}}}}`, operation, viewerID)
		response := r.graphql(t, apis[0], operatorToken, query)
		require.Empty(t, response.Errors)
		var payload struct {
			Actor struct {
				Lifecycle struct {
					State string `json:"state"`
				} `json:"lifecycle"`
			} `json:"actor"`
		}
		require.NoError(t, json.Unmarshal(response.Data[operation], &payload))
		require.Equal(t, map[string]string{"suspendUser": "SUSPENDED", "deactivateUser": "INACTIVE", "reactivateUser": "ACTIVE"}[operation],
			payload.Actor.Lifecycle.State)
		if operation != "reactivateUser" {
			require.Eventually(t, func() bool {
				return len(r.graphql(t, apis[1], viewerToken, read).Errors) != 0
			}, 3*time.Second, 100*time.Millisecond, "inactive/suspended profiles must be rejected on both processes")
		}
	}
	remove := strings.Replace(assign, "assignUserGroup", "removeUserGroup", 1)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, remove).Errors)
	require.Eventually(t, func() bool {
		return len(r.graphql(t, apis[1], viewerToken, read).Errors) != 0
	}, 3*time.Second, 100*time.Millisecond, "removed memberships must not remain usable after the freshness bound")
	last := r.graphql(t, apis[0], operatorToken, `{actors(last:1){edges{cursor}}}`)
	require.NotEmpty(t, last.Errors)
	require.Equal(t, "BAD_USER_INPUT", last.Errors[0].Extensions.Code)
	require.NotEmpty(t, last.Errors[0].Extensions.Diagnostics)
	require.Equal(t, "UNSUPPORTED_PAGINATION", last.Errors[0].Extensions.Diagnostics[0].Reason)
	page := r.graphql(t, apis[0], operatorToken, `{actors(first:2){edges{cursor node{id}} pageInfo{hasNextPage endCursor}}}`)
	require.Empty(t, page.Errors)
	var pageData struct {
		Edges []struct {
			Cursor string `json:"cursor"`
			Node   struct {
				ID string `json:"id"`
			} `json:"node"`
		} `json:"edges"`
	}
	require.NoError(t, json.Unmarshal(page.Data["actors"], &pageData))
	require.Len(t, pageData.Edges, 2)
	continued := r.graphql(t, apis[1], operatorToken,
		fmt.Sprintf(`{actors(first:2,after:%q){edges{node{id}}}}`, pageData.Edges[0].Cursor))
	require.Empty(t, continued.Errors)
	var nextData struct {
		Edges []struct {
			Node struct {
				ID string `json:"id"`
			} `json:"node"`
		} `json:"edges"`
	}
	require.NoError(t, json.Unmarshal(continued.Data["actors"], &nextData))
	require.NotEmpty(t, nextData.Edges)
	require.Equal(t, pageData.Edges[1].Node.ID, nextData.Edges[0].Node.ID)

	var writes sync.WaitGroup
	for index := range 8 {
		writes.Go(func() {
			query := fmt.Sprintf(`mutation{updateUser(input:{id:%q,fullName:"concurrent-%d"}){actor{id}}}`, viewerID, index)
			response := r.graphql(t, apis[index%2], operatorToken, query)
			if len(response.Errors) != 0 {
				require.NotEmpty(t, response.Errors[0].Extensions.Diagnostics)
				require.Equal(t, "DIRECTORY_CONFLICT", response.Errors[0].Extensions.Diagnostics[0].Reason)
			}
		})
	}
	writes.Wait()
	actual := r.identity(t, viewer.ID)
	require.True(t, strings.HasPrefix(actual.Traits["full_name"].(string), "concurrent-"),
		"native concurrent PATCH is last-writer/best-effort, not an atomic merge")
	require.Equal(t, viewer.ID, actual.ID)
	require.Equal(t, "viewer", actual.Traits["username"])
	emailConflict := fmt.Sprintf(`mutation{updateUser(input:{id:%q,primaryEmail:"operator@example.test"}){actor{id}}}`, viewerID)
	conflictingEmail := r.graphql(t, apis[1], operatorToken, emailConflict)
	require.NotEmpty(t, conflictingEmail.Errors)
	require.NotEmpty(t, conflictingEmail.Errors[0].Extensions.Diagnostics)
	require.Equal(t, "DIRECTORY_CONFLICT", conflictingEmail.Errors[0].Extensions.Diagnostics[0].Reason)

	created := r.graphql(t, apis[0], operatorToken, `mutation{createStaff(input:{
		metadata:{name:"carol"},fullName:"Carol",primaryEmail:"carol@example.test",groups:["catalog-viewer"]
	}){staff{id lifecycle{state} metadata{generation resourceVersion}}}}`)
	require.Empty(t, created.Errors)
	conflicting := r.graphql(t, apis[1], operatorToken, `mutation{createStaff(input:{
		metadata:{name:"carol"},fullName:"Conflict",primaryEmail:"conflict@example.test"
	}){staff{id}}}`)
	require.NotEmpty(t, conflicting.Errors)
	require.Equal(t, "DIRECTORY_CONFLICT", conflicting.Errors[0].Extensions.Diagnostics[0].Reason)
	var staff struct {
		Staff struct {
			ID        string `json:"id"`
			Lifecycle struct {
				State string `json:"state"`
			} `json:"lifecycle"`
		} `json:"staff"`
	}
	require.NoError(t, json.Unmarshal(created.Data["createStaff"], &staff))
	require.Equal(t, "INVITED", staff.Staff.Lifecycle.State)
	invitedID := staff.Staff.ID
	rawID, err := base64.StdEncoding.DecodeString(invitedID)
	require.NoError(t, err)
	uid := strings.TrimPrefix(string(rawID), "gid://GitStore/Staff/")
	require.Equal(t, "operator", r.identity(t, uid).MetadataAdmin["creation_actor"])
	resend := fmt.Sprintf(`mutation{resendStaffInvitation(input:{id:%q}){staff{id lifecycle{state}}}}`, invitedID)
	require.Empty(t, r.graphql(t, apis[1], operatorToken, resend).Errors)
	r.command(t, nil, "exec", "-e", "GITSTORE_TEST_OWNED_STAFF_UID="+uid, r.receiver,
		"/directory-receiver.test", "-test.run=^TestOwnedAPIStaffSetup$", "-test.timeout=30s")
	changedEmail := fmt.Sprintf(`mutation{updateUser(input:{id:%q,primaryEmail:"carol-pending@example.test"}){actor{
		id ... on User{primaryEmail emailVerified emailDeliverable lifecycle{state}}
	}}}`, invitedID)
	pending := r.graphql(t, apis[0], operatorToken, changedEmail)
	require.Empty(t, pending.Errors)
	var pendingPayload struct {
		Actor struct {
			EmailVerified bool `json:"emailVerified"`
			Lifecycle     struct {
				State string `json:"state"`
			} `json:"lifecycle"`
		} `json:"actor"`
	}
	require.NoError(t, json.Unmarshal(pending.Data["updateUser"], &pendingPayload))
	require.False(t, pendingPayload.Actor.EmailVerified)
	require.Equal(t, "PENDING_VERIFICATION", pendingPayload.Actor.Lifecycle.State)
	require.Equal(t, "carol@example.test", r.identity(t, uid).MetadataAdmin["deliverable_email"],
		"an unverified replacement must not replace the previous trusted deliverable address")
	deleted := fmt.Sprintf(`mutation{deleteUser(input:{id:%q}){actor{id}}}`, invitedID)
	require.Empty(t, r.graphql(t, apis[1], operatorToken, deleted).Errors)
	status, _ := r.request(t, http.MethodGet, "/admin/identities/"+uid, nil)
	require.Equal(t, http.StatusNotFound, status)
	auditCount := 0
	for _, api := range apis {
		logs := r.command(t, nil, "logs", "--tail", "1000", api.name)
		for _, line := range strings.Split(logs, "\n") {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) != nil || event["msg"] != "directory mutation" {
				continue
			}
			auditCount++
			for _, field := range []string{"actor", "target_id", "change", "outcome", "time"} {
				require.Contains(t, event, field)
			}
			require.NotContains(t, line, "example.test", "mutation receipts must not include contact addresses")
			require.NotContains(t, line, r.secret)
		}
	}
	require.Greater(t, auditCount, 10, "the deployed resolver must actually emit redacted actor mutation receipts")

	revived := r.graphql(t, apis[0], operatorToken, assign)
	require.Empty(t, revived.Errors)
	started := make([]string, len(apis))
	for index, api := range apis {
		started[index] = strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", api.name))
	}
	require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{index .Config.Labels "dev.gitstore.test_owner"}}`, apis[1].name)))
	r.command(t, nil, "restart", "--time", "0", apis[1].name)
	restartedPort := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{(index (index .NetworkSettings.Ports "4000/tcp") 0).HostPort}}`, apis[1].name))
	apis[1].base = "http://127.0.0.1:" + restartedPort
	require.Empty(t, r.graphql(t, apis[0], operatorToken, read).Errors,
		"the other actual API process must keep admitting trusted identities during replacement")
	require.Eventually(t, func() bool {
		health, err := r.client.Get(apis[1].base + "/health")
		if err != nil {
			return false
		}
		health.Body.Close()
		if health.StatusCode != http.StatusOK {
			return false
		}
		status, data := r.requestAt(t, apis[1].base, http.MethodPost, "/admin/graphql",
			map[string]string{"query": read}, http.Header{"Authorization": {"Bearer " + viewerToken}})
		var recovered directoryGraphQLResponse
		return status == http.StatusOK && json.Unmarshal(data, &recovered) == nil && len(recovered.Errors) == 0
	}, 30*time.Second, 250*time.Millisecond, "existing directory identity must recover after an actual API process restart")
	require.Equal(t, started[0], strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", apis[0].name)))
	require.NotEqual(t, started[1], strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", apis[1].name)))

	for replica := range 2 {
		name := fmt.Sprintf("gitstore-userdir-kratos-%s-%d", r.owner[:12], replica)
		require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{index .Config.Labels "dev.gitstore.test_owner"}}`, name)))
		t.Cleanup(func() {
			if strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.Paused}}", name)) == "true" {
				r.command(t, nil, "unpause", name)
			}
		})
		r.command(t, nil, "pause", name)
	}
	time.Sleep(600 * time.Millisecond)
	requestTimeout := r.client.Timeout
	r.client.Timeout = 6 * time.Second
	defer func() { r.client.Timeout = requestTimeout }()
	for _, api := range apis {
		status, data := r.requestAt(t, api.base, http.MethodPost, "/admin/graphql",
			map[string]string{"query": read}, http.Header{"Authorization": {"Bearer " + operatorToken}})
		if status == http.StatusOK {
			var denied directoryGraphQLResponse
			require.NoError(t, json.Unmarshal(data, &denied))
			require.NotEmpty(t, denied.Errors, "an expired cache must not convert a real directory outage into an authenticated profile")
		} else {
			require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable}, status)
		}
	}
	for replica := range 2 {
		r.command(t, nil, "unpause", fmt.Sprintf("gitstore-userdir-kratos-%s-%d", r.owner[:12], replica))
	}
	require.Eventually(t, func() bool {
		for _, api := range apis {
			if len(r.graphql(t, api, viewerToken, read).Errors) != 0 {
				return false
			}
		}
		return true
	}, 30*time.Second, 250*time.Millisecond, "existing-profile authentication must recover on both processes after directory restoration")
}
