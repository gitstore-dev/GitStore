// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type directoryTestIdentity struct {
	ID            string         `json:"id"`
	SchemaID      string         `json:"schema_id"`
	State         string         `json:"state"`
	Traits        map[string]any `json:"traits"`
	MetadataAdmin map[string]any `json:"metadata_admin"`
}

type directoryTestRuntime struct {
	admin, public, postgres, network, owner, secret string
	receiver                                        string
	adminReplicas                                   []string
	client                                          *http.Client
	mailAPI                                         string
	capacity                                        bool
	postgresVolume                                  string
	issuerURI                                       string
}

func startDirectoryTestRuntime(t *testing.T, enableMail ...bool) *directoryTestRuntime {
	t.Helper()
	if os.Getenv("USER_DIRECTORY_TEST_OWNED_DEPLOYMENT") != "1" {
		t.Skip("requires USER_DIRECTORY_TEST_OWNED_DEPLOYMENT=1 and the selected Kratos/PostgreSQL images")
	}
	token := make([]byte, 16)
	_, err := rand.Read(token)
	require.NoError(t, err)
	owner := hex.EncodeToString(token)
	r := &directoryTestRuntime{
		owner: owner, network: "gitstore-userdir-test-" + owner[:12],
		postgres: "gitstore-userdir-postgres-" + owner[:12],
		secret:   owner, client: &http.Client{Timeout: 2 * time.Second},
		capacity: os.Getenv("USER_DIRECTORY_TEST_CAPACITY") == "1",
	}
	withMail := len(enableMail) > 0 && enableMail[0]
	if withMail {
		r.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	r.command(t, nil, "network", "create", "--label", "dev.gitstore.test_owner="+owner, r.network)
	t.Cleanup(func() {
		label := strings.TrimSpace(r.command(t, nil, "network", "inspect", "--format",
			`{{index .Labels "dev.gitstore.test_owner"}}`, r.network))
		if label != r.owner {
			t.Errorf("refusing cleanup of a network with unexpected ownership")
			return
		}
		r.command(t, nil, "network", "rm", r.network)
	})
	postgresArgs := []string{"run", "-d", "--rm", "--name", r.postgres,
		"--label", "dev.gitstore.test_owner=" + owner, "--network", r.network,
		"--network-alias", "postgres"}
	if r.capacity {
		r.postgresVolume = r.postgres + "-data"
		r.command(t, nil, "volume", "create", "--label", "dev.gitstore.test_owner="+owner, r.postgresVolume)
		t.Cleanup(func() {
			label := strings.TrimSpace(r.command(t, nil, "volume", "inspect", "--format",
				`{{index .Labels "dev.gitstore.test_owner"}}`, r.postgresVolume))
			if label != r.owner {
				t.Errorf("refusing cleanup of a capacity volume with unexpected ownership")
				return
			}
			r.command(t, nil, "volume", "rm", r.postgresVolume)
		})
		postgresArgs = append(postgresArgs, "--memory", "1g", "--cpus", "2",
			"-v", r.postgresVolume+":/var/lib/postgresql/data")
	} else {
		postgresArgs = append(postgresArgs, "--memory", "512m", "--cpus", "1",
			"--tmpfs", "/var/lib/postgresql/data:rw,size=256m")
	}
	postgresArgs = append(postgresArgs,
		"-e", "POSTGRES_USER=kratos", "-e", "POSTGRES_DB=kratos", "-e", "POSTGRES_PASSWORD",
		"postgres:16-alpine",
	)
	r.command(t, nil, postgresArgs...)
	r.cleanupContainer(t, r.postgres)
	require.Eventually(t, func() bool {
		return exec.Command("docker", "exec", r.postgres, "pg_isready", "-U", "kratos", "-d", "kratos").Run() == nil
	}, 45*time.Second, 250*time.Millisecond)
	r.command(t, nil, "run", "--rm", "--label", "dev.gitstore.test_owner="+owner,
		"--network", r.network, "-e", "DSN", "oryd/kratos:v26.2.0", "migrate", "sql", "up", "--yes", "-e")
	configDir, err := filepath.Abs("../../config/oidc/kratos")
	require.NoError(t, err)
	if withMail {
		catcher := "gitstore-userdir-mail-" + owner[:12]
		r.command(t, nil, "run", "-d", "--name", catcher,
			"--label", "dev.gitstore.test_owner="+owner, "--network", r.network,
			"--network-alias", "mailslurper", "--memory", "256m", "--cpus", "0.5",
			"-p", "127.0.0.1::4437", "oryd/mailslurper:latest-smtps")
		r.cleanupContainer(t, catcher, true)
		port := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{(index (index .NetworkSettings.Ports "4437/tcp") 0).HostPort}}`, catcher))
		r.mailAPI = "http://127.0.0.1:" + port
		require.Eventually(t, func() bool {
			response, err := r.client.Get(r.mailAPI + "/mail")
			if err != nil {
				return false
			}
			defer response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 30*time.Second, 250*time.Millisecond, "owned reference mail catcher must be responsive")
	}
	binary := filepath.Join(t.TempDir(), "directory-receiver.test")
	bridgeBinary := filepath.Join(t.TempDir(), "directory-bridge.test")
	apiDir, err := filepath.Abs("../../gitstore-api")
	require.NoError(t, err)
	buildCtx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(buildCtx, "go", "test", "-c", "-o", binary, "./internal/auth/provider/kratos")
	build.Dir = apiDir
	build.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build owned webhook receiver: %s", output)
	bridgeDir, err := filepath.Abs("../../gitstore-oidc-bridge")
	require.NoError(t, err)
	build = exec.CommandContext(buildCtx, "go", "test", "-c", "-o", bridgeBinary, "./internal/bridge")
	build.Dir = bridgeDir
	build.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	output, err = build.CombinedOutput()
	require.NoError(t, err, "build owned login bridge test: %s", output)
	r.receiver = "gitstore-userdir-receiver-" + owner[:12]
	receiverArgs := []string{"run", "-d", "--name", r.receiver,
		"--label", "dev.gitstore.test_owner=" + owner, "--network", r.network,
		"--network-alias", "directory-receiver", "--memory", "512m", "--cpus", "1",
		"-e", "GITSTORE_TEST_OWNED_RECEIVER=1",
		"-e", "GITSTORE_TEST_KRATOS_ADMIN_URI=http://gitstore-userdir-kratos-" + owner[:12] + "-0:4434",
	}
	if withMail {
		receiverArgs = append(receiverArgs, "-e", "GITSTORE_TEST_OWNED_MAIL_KEY")
	}
	receiverTimeout := "5m"
	if r.capacity {
		receiverTimeout = "2h"
	}
	receiverArgs = append(receiverArgs, "-v", binary+":/directory-receiver.test:ro",
		"-v", bridgeBinary+":/directory-bridge.test:ro", "--entrypoint", "/directory-receiver.test",
		"postgres:16-alpine", "-test.run=^TestOwnedKratosWebhookReceiver$", "-test.timeout="+receiverTimeout)
	r.command(t, nil, receiverArgs...)
	r.cleanupContainer(t, r.receiver, true)
	require.Eventually(t, func() bool {
		return exec.Command("docker", "exec", r.postgres, "wget", "-q", "-O", "/dev/null",
			"http://directory-receiver:8085/health/ready").Run() == nil
	}, 30*time.Second, 250*time.Millisecond, "owned Go webhook handler must be responsive")
	webhook := func(path string) map[string]any {
		return map[string]any{"hook": "web_hook", "config": map[string]any{
			"url":    "http://directory-receiver:8085/internal/userdir/kratos/" + path,
			"method": "POST", "body": "file:///etc/config/kratos/webhook.jsonnet",
			"response": map[string]any{"ignore": false, "parse": true},
		}}
	}
	hookSettings := map[string]any{"selfservice": map[string]any{"flows": map[string]any{
		"registration": map[string]any{"after": map[string]any{"password": map[string]any{
			"hooks": []any{webhook("registration"), map[string]any{"hook": "session"}},
		}}},
		"settings": map[string]any{"after": map[string]any{
			"profile":  map[string]any{"hooks": []any{webhook("settings")}},
			"password": map[string]any{"hooks": []any{webhook("settings-password")}},
		}},
	}}}
	if withMail {
		hookSettings["courier"] = map[string]any{"delivery_strategy": "http"}
	}
	hooks, err := json.Marshal(hookSettings)
	require.NoError(t, err)
	hookConfig := filepath.Join(t.TempDir(), "hooks.json")
	require.NoError(t, os.WriteFile(hookConfig, hooks, 0600))
	for replica := range 2 {
		kratos := fmt.Sprintf("gitstore-userdir-kratos-%s-%d", owner[:12], replica)
		r.cleanupContainer(t, kratos, true)
		kratosArgs := []string{"run", "-d", "--name", kratos,
			"--label", "dev.gitstore.test_owner=" + owner, "--network", r.network,
			"--network-alias", "directory-kratos",
			"--memory", "512m", "--cpus", "1",
			"-p", "127.0.0.1::4433", "-p", "127.0.0.1::4434",
			"-e", "DSN", "-e", "SECRETS_COOKIE", "-e", "SECRETS_DEFAULT",
			"-e", "COURIER_HTTP_REQUEST_CONFIG_URL=http://directory-receiver:8085/internal/notifications/mail",
			"-e", "COURIER_HTTP_REQUEST_CONFIG_AUTH_CONFIG_VALUE",
			"-v", hookConfig + ":/etc/gitstore-hooks.json:ro",
			"-v", configDir + ":/etc/config/kratos:ro", "oryd/kratos:v26.2.0",
			"serve", "--config", "/etc/config/kratos/kratos.yml,/etc/gitstore-hooks.json", "--dev"}
		if withMail {
			kratosArgs = append(kratosArgs, "--watch-courier")
		}
		r.command(t, nil, kratosArgs...)
		t.Cleanup(func() {
			if t.Failed() {
				for _, line := range strings.Split(r.command(t, nil, "logs", kratos), "\n") {
					var event map[string]json.RawMessage
					if json.Unmarshal([]byte(line), &event) != nil {
						continue
					}
					safeEvent := map[string]json.RawMessage{
						"level": event["level"], "msg": event["msg"], "service_version": event["service_version"],
					}
					var failure struct {
						Message string `json:"message"`
					}
					if json.Unmarshal(event["error"], &failure) == nil {
						if index := strings.Index(failure.Message, "status code "); index >= 0 {
							var status int
							if _, err := fmt.Sscanf(failure.Message[index:], "status code %d", &status); err == nil && status >= 100 && status <= 599 {
								safeEvent["courier_status"] = json.RawMessage(fmt.Sprintf("%d", status))
							}
						}
					}
					safe, err := json.Marshal(safeEvent)
					if err == nil {
						t.Log(string(safe))
					}
				}
			}
		})
		port := func(containerPort string) string {
			return strings.TrimSpace(r.command(t, nil, "inspect", "--format",
				fmt.Sprintf(`{{(index (index .NetworkSettings.Ports %q) 0).HostPort}}`, containerPort), kratos))
		}
		admin, public := "http://127.0.0.1:"+port("4434/tcp"), "http://127.0.0.1:"+port("4433/tcp")
		r.adminReplicas = append(r.adminReplicas, admin)
		if replica == 0 {
			r.admin, r.public = admin, public
		}
		require.Eventually(t, func() bool {
			response, requestErr := r.client.Get(public + "/health/ready")
			if requestErr != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 45*time.Second, 250*time.Millisecond, "selected Kratos configuration must start")
	}
	return r
}

func (r *directoryTestRuntime) command(t *testing.T, input io.Reader, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = append(os.Environ(),
		"POSTGRES_PASSWORD="+r.secret,
		"DSN=postgres://kratos:"+r.secret+"@postgres:5432/kratos?sslmode=disable",
		"SECRETS_COOKIE="+r.secret, "SECRETS_DEFAULT="+r.secret,
		"GITSTORE_TEST_OWNED_MAIL_KEY="+r.secret, "COURIER_HTTP_REQUEST_CONFIG_AUTH_CONFIG_VALUE="+r.secret)
	cmd.Env = append(cmd.Env, "GITSTORE_TEST_LOGIN_PASSWORD="+r.secret)
	cmd.Stdin = input
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "owned Docker command failed: %s",
		strings.ReplaceAll(string(output), r.secret, "[redacted]"))
	return string(output)
}

func (r *directoryTestRuntime) cleanupContainer(t *testing.T, name string, removeAfterStop ...bool) {
	t.Helper()
	t.Cleanup(func() {
		label := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{index .Config.Labels "dev.gitstore.test_owner"}}`, name))
		if label != r.owner {
			t.Errorf("refusing cleanup of a container with unexpected ownership")
			return
		}
		r.command(t, nil, "stop", "--time", "5", name)
		if len(removeAfterStop) != 0 && removeAfterStop[0] {
			r.command(t, nil, "rm", name)
		}
	})
}

func (r *directoryTestRuntime) sql(t *testing.T, statement string) string {
	t.Helper()
	return r.command(t, strings.NewReader(statement), "exec", "-i", r.postgres,
		"psql", "-U", "kratos", "-d", "kratos", "-v", "ON_ERROR_STOP=1", "-At")
}

func (r *directoryTestRuntime) request(t *testing.T, method, path string, body any) (int, []byte) {
	return r.requestAt(t, r.admin, method, path, body)
}

func (r *directoryTestRuntime) requestAt(t *testing.T, base, method, path string, body any, headers ...http.Header) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	request, err := http.NewRequest(method, base+path, bytes.NewReader(encoded))
	require.NoError(t, err)
	if len(headers) != 0 {
		request.Header = headers[0].Clone()
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	return response.StatusCode, data
}

func (r *directoryTestRuntime) createHuman(t *testing.T, kind, name, email string) directoryTestIdentity {
	t.Helper()
	status, body := r.request(t, http.MethodPost, "/admin/identities", map[string]any{
		"schema_id": kind, "state": "active",
		"traits": map[string]any{"username": name, "email": email, "full_name": name},
		"metadata_admin": map[string]any{"creation_actor": "operator", "update_actor": "operator",
			"gitstore": map[string]any{"generation": 1, "resource_version": "1"}},
		"credentials": map[string]any{"password": map[string]any{"config": map[string]any{"password": r.secret}}},
	})
	require.Equal(t, http.StatusCreated, status, "selected schema must support human creation")
	var identity directoryTestIdentity
	require.NoError(t, json.Unmarshal(body, &identity))
	return r.identity(t, identity.ID)
}

func (r *directoryTestRuntime) identity(t *testing.T, id string) directoryTestIdentity {
	t.Helper()
	status, body := r.request(t, http.MethodGet, "/admin/identities/"+id, nil)
	require.Equal(t, http.StatusOK, status)
	var identity directoryTestIdentity
	require.NoError(t, json.Unmarshal(body, &identity))
	return identity
}

func (r *directoryTestRuntime) startHydra(t *testing.T, bridge, callback string) {
	t.Helper()
	r.sql(t, "CREATE DATABASE hydra;")
	hydraEnv := filepath.Join(t.TempDir(), "hydra.env")
	require.NoError(t, os.WriteFile(hydraEnv, []byte(
		"DSN=postgres://kratos:"+r.secret+"@postgres:5432/hydra?sslmode=disable\n"+
			"SECRETS_SYSTEM="+r.secret+"\nSECRETS_COOKIE="+r.secret+"\n"+
			"URLS_SELF_ISSUER=http://hydra:4444/\nURLS_LOGIN="+bridge+"/login\nURLS_CONSENT="+bridge+"/consent\n"), 0600))
	r.command(t, nil, "run", "--rm", "--label", "dev.gitstore.test_owner="+r.owner,
		"--network", r.network, "--env-file", hydraEnv, "oryd/hydra:v26.2.0",
		"migrate", "sql", "up", "--yes", "-e")
	hydraConfig, err := filepath.Abs("../../config/oidc/hydra/config.yaml")
	require.NoError(t, err)
	hydra := "gitstore-userdir-hydra-" + r.owner[:12]
	r.command(t, nil, "run", "-d", "--rm", "--name", hydra,
		"--label", "dev.gitstore.test_owner="+r.owner, "--network", r.network,
		"--network-alias", "hydra", "--memory", "512m", "--cpus", "1",
		"--env-file", hydraEnv, "-p", "127.0.0.1::4444",
		"-v", hydraConfig+":/etc/config/hydra/config.yaml:ro", "oryd/hydra:v26.2.0",
		"serve", "all", "--config", "/etc/config/hydra/config.yaml", "--dev")
	r.cleanupContainer(t, hydra)
	hydraPort := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{(index (index .NetworkSettings.Ports "4444/tcp") 0).HostPort}}`, hydra))
	require.Eventually(t, func() bool {
		response, err := r.client.Get("http://127.0.0.1:" + hydraPort + "/.well-known/openid-configuration")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}, 30*time.Second, 250*time.Millisecond)
	r.command(t, nil, "run", "--rm", "--label", "dev.gitstore.test_owner="+r.owner,
		"--network", r.network, "oryd/hydra:v26.2.0", "create", "oauth2-client",
		"--endpoint", "http://hydra:4445", "--id", "directory-test-client",
		"--secret", r.secret, "--grant-type", "authorization_code,refresh_token",
		"--response-type", "code", "--scope", "openid,profile,email", "--audience", "owned-directory",
		"--redirect-uri", callback,
		"--token-endpoint-auth-method", "client_secret_basic", "--format", "json")
}

func TestOwnedNativeHydraDirectoryAcrossAPIs(t *testing.T) {
	r := startDirectoryTestRuntime(t, true)
	operator := r.createHuman(t, "staff", "operator", "operator@example.test")
	viewer := r.createHuman(t, "staff", "viewer", "viewer@example.test")
	customer := r.createHuman(t, "customer", "customer", "customer@example.test")
	kratos := "gitstore-userdir-kratos-" + r.owner[:12] + "-0"
	bridge := "http://" + kratos + ":8087"
	r.startHydra(t, bridge, bridge+"/callback")
	bridgeBinary := filepath.Join(t.TempDir(), "bridge.test")
	r.command(t, nil, "cp", r.receiver+":/directory-bridge.test", bridgeBinary)
	// Keep Kratos and the bridge on one hostname so the browser jar sends real session cookies.
	r.command(t, nil, "cp", bridgeBinary, kratos+":/tmp/directory-bridge.test")
	privateTokens := make(map[string][]string)
	for _, identity := range []directoryTestIdentity{operator, viewer, customer} {
		status, _ := r.request(t, http.MethodPatch, "/admin/identities/"+identity.ID, []map[string]any{
			{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
				{"value": identity.Traits["email"], "verified": true, "via": "email", "status": "completed"},
			}},
			{"op": "add", "path": "/metadata_admin/setup_complete", "value": true},
		})
		require.Equal(t, http.StatusOK, status)
		r.command(t, nil, "exec", "-e", "GITSTORE_TEST_OWNED_RECEIVER=1",
			"-e", "GITSTORE_TEST_KRATOS_ADMIN_URI=http://"+kratos+":4434",
			"-e", "GITSTORE_TEST_LOGIN_IDENTITY_ID="+identity.ID,
			"-e", "GITSTORE_TEST_LOGIN_USERNAME="+identity.Traits["username"].(string),
			"-e", "GITSTORE_TEST_LOGIN_EMAIL="+identity.Traits["email"].(string),
			"-e", "GITSTORE_TEST_LOGIN_PASSWORD", "-e", "GITSTORE_TEST_LOGIN_TOKEN_FILE=/tmp/native-tokens.json",
			kratos, "/tmp/directory-bridge.test",
			"-test.run=^TestOwnedNativeHydraLoginPreservesDirectorySubject$", "-test.timeout=45s")
		path := filepath.Join(t.TempDir(), "tokens.json")
		r.command(t, nil, "cp", kratos+":/tmp/native-tokens.json", path)
		tokens, err := os.ReadFile(path)
		require.NoError(t, err)
		var decoded []string
		require.NoError(t, json.Unmarshal(tokens, &decoded))
		require.Len(t, decoded, 2)
		privateTokens[identity.ID] = decoded
	}
	r.issuerURI = "http://hydra:4444/"
	apis, _ := r.startAPIs(t, operator.ID)
	operatorID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + operator.ID))
	read := fmt.Sprintf(`{node(id:%q){id ... on User{groups metadata{name}}}}`, operatorID)
	for _, api := range apis {
		for _, token := range privateTokens[operator.ID] {
			result := r.graphql(t, api, token, read)
			require.Empty(t, result.Errors, "real Hydra credentials must enrich and authorize on both replicas")
			require.Contains(t, string(result.Data["node"]), operatorID)
		}
		for _, token := range privateTokens[customer.ID] {
			result := r.graphql(t, api, token, read)
			require.NotEmpty(t, result.Errors)
			require.Equal(t, "FORBIDDEN", result.Errors[0].Extensions.Code,
				"native Customer login must not grant Admin admission")
		}
		for _, token := range privateTokens[viewer.ID] {
			require.NotEmpty(t, r.graphql(t, api, token, read).Errors,
				"native Staff credentials alone must not grant directory management")
		}
	}
	viewerID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + viewer.ID))
	assign := fmt.Sprintf(`mutation{assignUserGroup(input:{id:%q,group:"merchandiser"}){actor{id}}}`, viewerID)
	require.Empty(t, r.graphql(t, apis[0], privateTokens[operator.ID][0], assign).Errors)
	for _, api := range apis {
		for _, token := range privateTokens[viewer.ID] {
			require.Eventually(t, func() bool {
				return len(r.graphql(t, api, token, read).Errors) == 0
			}, 3*time.Second, 100*time.Millisecond,
				"directory membership must authorize already-issued native credentials on either API")
		}
	}
}

func TestKratosSchemaContracts(t *testing.T) {
	r := startDirectoryTestRuntime(t)
	staff := r.createHuman(t, "staff", "operator", "operator@example.test")
	customer := r.createHuman(t, "customer", "customer", "customer@example.test")
	t.Run("superseded schema is not registered", func(t *testing.T) {
		status, _ := r.request(t, http.MethodPost, "/admin/identities", map[string]any{
			"schema_id": "gitstore-user", "state": "active",
			"traits": map[string]any{"username": "superseded", "email": "superseded@example.test"},
		})
		require.Equal(t, http.StatusBadRequest, status)
	})
	t.Run("bounded exact identifier and external ID lookup endpoints", func(t *testing.T) {
		for _, identifier := range []string{"customer", "customer@example.test"} {
			status, body := r.request(t, http.MethodGet,
				"/admin/identities?credentials_identifier="+url.QueryEscape(identifier)+"&page_size=2", nil)
			require.Equal(t, http.StatusOK, status)
			var identities []directoryTestIdentity
			require.NoError(t, json.Unmarshal(body, &identities))
			require.Len(t, identities, 1, "the selected release must perform exact lookup without an inventory scan")
			require.Equal(t, customer.ID, identities[0].ID)
		}
		externalID := "gitstore:serviceaccount:8f90a766-d347-4bd1-897b-217224c919c3"
		status, body := r.request(t, http.MethodPost, "/admin/identities", map[string]any{
			"schema_id": "serviceAccount", "state": "active", "external_id": externalID,
			"traits": map[string]any{
				"source_uid": "8f90a766-d347-4bd1-897b-217224c919c3", "source_name": "mapped-workload",
			},
		})
		require.Equal(t, http.StatusCreated, status)
		var created directoryTestIdentity
		require.NoError(t, json.Unmarshal(body, &created))
		status, body = r.request(t, http.MethodGet,
			"/admin/identities/by/external/"+url.PathEscape(externalID), nil)
		require.Equal(t, http.StatusOK, status)
		var resolved directoryTestIdentity
		require.NoError(t, json.Unmarshal(body, &resolved))
		require.Equal(t, created.ID, resolved.ID)
	})
	t.Run("human identifiers are unique across schemas", func(t *testing.T) {
		for _, traits := range []map[string]any{
			{"username": "operator", "email": "another@example.test"},
			{"username": "another", "email": "operator@example.test"},
		} {
			status, _ := r.request(t, http.MethodPost, "/admin/identities", map[string]any{
				"schema_id": "customer", "state": "active", "traits": traits,
				"credentials": map[string]any{"password": map[string]any{"config": map[string]any{"password": r.secret}}},
			})
			require.NotEqual(t, http.StatusCreated, status)
		}
	})
	t.Run("public cross-schema conflicts do not disclose the existing actor", func(t *testing.T) {
		for _, traits := range []map[string]any{
			{"username": "operator", "email": "public-conflict@example.test"},
			{"username": "public-conflict", "email": "operator@example.test"},
		} {
			status, data := r.requestAt(t, r.public, http.MethodGet, "/self-service/registration/api", nil)
			require.Equal(t, http.StatusOK, status)
			var flow struct {
				ID string `json:"id"`
			}
			require.NoError(t, json.Unmarshal(data, &flow))
			status, data = r.requestAt(t, r.public, http.MethodPost,
				"/self-service/registration?flow="+flow.ID, map[string]any{
					"method": "password", "password": r.secret, "traits": traits,
				})
			require.Equal(t, http.StatusBadRequest, status)
			require.False(t, strings.Contains(string(data), staff.ID), "conflicts must not disclose an existing UID")
			require.False(t, strings.Contains(string(data), `"schema_id":"staff"`), "conflicts must not disclose an existing actor kind")
			require.False(t, strings.Contains(string(data), `"schema_id": "staff"`), "conflicts must not disclose an existing actor kind")
		}
	})
	t.Run("native Customers cannot inherit forged operator groups through static or OIDC authentication", func(t *testing.T) {
		r.command(t, nil, "exec", r.receiver, "/directory-receiver.test",
			"-test.run=^TestOwnedCustomerIdentityAuthorization$", "-test.timeout=30s")
	})
	t.Run("direct Admin writes bypass hooks and counter maintenance", func(t *testing.T) {
		metadata, ok := staff.MetadataAdmin["gitstore"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "operator", staff.MetadataAdmin["creation_actor"])
		require.EqualValues(t, 1, metadata["generation"])
		require.Equal(t, "1", metadata["resource_version"])
		metadata["generation"] = 999
		staff.Traits["full_name"] = "Updated name"
		status, _ := r.request(t, http.MethodPut, "/admin/identities/"+staff.ID, staff)
		require.Equal(t, http.StatusOK, status)
		updated := r.identity(t, staff.ID).MetadataAdmin["gitstore"].(map[string]any)
		require.EqualValues(t, 999, updated["generation"], "Admin callers can overwrite protected metadata")
		require.Equal(t, "1", updated["resource_version"], "stock Kratos does not advance custom counters")
	})
	t.Run("workload schema has no human credentials", func(t *testing.T) {
		status, _ := r.request(t, http.MethodPost, "/admin/identities", map[string]any{
			"schema_id": "serviceAccount", "state": "active",
			"external_id": "gitstore-serviceaccount:00000000-0000-4000-8000-000000000001",
			"traits": map[string]any{
				"source_uid": "00000000-0000-4000-8000-000000000001", "source_name": "worker",
			},
		})
		require.Equal(t, http.StatusCreated, status)
		status, _ = r.request(t, http.MethodPost, "/admin/identities", map[string]any{
			"schema_id": "serviceAccount", "state": "active",
			"traits": map[string]any{
				"source_uid": "00000000-0000-4000-8000-000000000002", "source_name": "credential-attempt",
			},
			"credentials": map[string]any{"password": map[string]any{"config": map[string]any{"password": r.secret}}},
		})
		require.Equal(t, http.StatusCreated, status, "the schema is not an Admin credential constraint")
	})
	t.Run("privileged Admin callers can bypass username and kind guards", func(t *testing.T) {
		customer.Traits["username"] = "replacement"
		status, _ := r.request(t, http.MethodPut, "/admin/identities/"+customer.ID, customer)
		require.Equal(t, http.StatusOK, status)
		require.Equal(t, "replacement", r.identity(t, customer.ID).Traits["username"])
		current := r.identity(t, customer.ID)
		current.SchemaID = "staff"
		status, _ = r.request(t, http.MethodPut, "/admin/identities/"+customer.ID, current)
		require.Equal(t, http.StatusOK, status, "supported public hooks do not constrain the Admin API")
	})
	t.Run("concurrent Admin writes accept the same observed revision", func(t *testing.T) {
		current := r.identity(t, staff.ID)
		before := current.MetadataAdmin["gitstore"].(map[string]any)
		statuses := make([]int, 8)
		var writers sync.WaitGroup
		for i := range statuses {
			writers.Add(1)
			go func(index int) {
				defer writers.Done()
				payload := map[string]any{
					"schema_id": current.SchemaID, "state": current.State,
					"metadata_admin": current.MetadataAdmin,
					"traits": map[string]any{
						"username": "operator", "email": "operator@example.test",
						"full_name": fmt.Sprintf("Concurrent writer %d", index),
					},
				}
				statuses[index], _ = r.requestAt(t, r.adminReplicas[index%len(r.adminReplicas)],
					http.MethodPut, "/admin/identities/"+staff.ID, payload)
			}(i)
		}
		writers.Wait()
		successes := 0
		for _, status := range statuses {
			if status == http.StatusOK {
				successes++
			}
		}
		require.Greater(t, successes, 1, "stock Kratos has no database revision CAS")
		after := r.identity(t, staff.ID).MetadataAdmin["gitstore"].(map[string]any)
		require.Equal(t, before["generation"], after["generation"])
		require.Equal(t, before["resource_version"], after["resource_version"])
	})
	t.Run("public registration is customer only with protected metadata", func(t *testing.T) {
		for _, schema := range []string{"staff", "serviceAccount", "gitstore-user"} {
			status, _ := r.requestAt(t, r.public, http.MethodGet,
				"/self-service/registration/api?identity_schema="+schema, nil)
			require.NotEqual(t, http.StatusOK, status)
		}
		status, data := r.requestAt(t, r.public, http.MethodGet, "/self-service/registration/api", nil)
		require.Equal(t, http.StatusOK, status)
		var flow struct {
			ID string `json:"id"`
		}
		require.NoError(t, json.Unmarshal(data, &flow))
		status, data = r.requestAt(t, r.public, http.MethodPost,
			"/self-service/registration?flow="+flow.ID, map[string]any{
				"method": "password", "password": r.secret,
				"traits": map[string]any{
					"username": "registered", "email": "registered@example.test", "full_name": "Registered",
				},
				"metadata_admin": map[string]any{"creation_actor": "forged", "update_actor": "forged",
					"groups": []string{"system:staff", "system:masters"}, "lifecycle_state": "ACTIVE",
					"email_deliverable": true, "contact_consent": map[string]any{"marketing": true},
					"gitstore": map[string]any{"generation": 999}},
				"schema_id": "staff",
			})
		require.Equal(t, http.StatusOK, status)
		var result struct {
			Identity     directoryTestIdentity `json:"identity"`
			SessionToken string                `json:"session_token"`
		}
		require.NoError(t, json.Unmarshal(data, &result))
		actual := r.identity(t, result.Identity.ID)
		require.Equal(t, "customer", actual.SchemaID)
		metadata := actual.MetadataAdmin["gitstore"].(map[string]any)
		require.Equal(t, "registered", actual.MetadataAdmin["creation_actor"])
		require.Equal(t, "registered", actual.MetadataAdmin["update_actor"])
		require.Nil(t, actual.MetadataAdmin["groups"])
		require.NotEqual(t, "ACTIVE", actual.MetadataAdmin["lifecycle_state"])
		require.NotEqual(t, true, actual.MetadataAdmin["email_deliverable"])
		require.Nil(t, actual.MetadataAdmin["contact_consent"])
		require.EqualValues(t, -1, metadata["generation"])
		require.Equal(t, "-1", metadata["resource_version"])
		for _, identifier := range []string{"registered", "registered@example.test"} {
			status, data = r.requestAt(t, r.public, http.MethodGet, "/self-service/login/api", nil)
			require.Equal(t, http.StatusOK, status)
			require.NoError(t, json.Unmarshal(data, &flow))
			status, data = r.requestAt(t, r.public, http.MethodPost, "/self-service/login?flow="+flow.ID,
				map[string]any{"method": "password", "password": r.secret, "identifier": identifier})
			require.Equal(t, http.StatusOK, status, "both password identifiers must authenticate")
			var login struct {
				Session struct {
					Identity directoryTestIdentity `json:"identity"`
				} `json:"session"`
			}
			require.NoError(t, json.Unmarshal(data, &login))
			require.Equal(t, actual.ID, login.Session.Identity.ID, "login identifiers must never merge or select a different subject")
		}
		r.command(t, nil, "exec",
			"-e", "GITSTORE_TEST_LOGIN_PASSWORD", "-e", "GITSTORE_TEST_LOGIN_IDENTITY_ID="+actual.ID,
			r.receiver, "/directory-bridge.test", "-test.run=^TestOwnedNativeDualIdentifierLoginPreservesOIDCSubject$", "-test.timeout=30s")
		authenticated := http.Header{"X-Session-Token": {result.SessionToken}}
		status, data = r.requestAt(t, r.public, http.MethodGet, "/self-service/settings/api", nil, authenticated)
		require.Equal(t, http.StatusOK, status)
		require.NoError(t, json.Unmarshal(data, &flow))
		status, _ = r.requestAt(t, r.public, http.MethodPost, "/self-service/settings?flow="+flow.ID,
			map[string]any{"method": "profile", "traits": map[string]any{
				"username": "registered", "email": "registered@example.test", "full_name": "Publicly updated",
			}}, authenticated)
		require.Equal(t, http.StatusOK, status)
		changed := r.identity(t, actual.ID).MetadataAdmin["gitstore"].(map[string]any)
		require.EqualValues(t, -1, changed["generation"])
		require.Equal(t, "-1", changed["resource_version"])
		require.Equal(t, "registered", r.identity(t, actual.ID).MetadataAdmin["creation_actor"])
		status, data = r.requestAt(t, r.public, http.MethodGet, "/self-service/settings/api", nil, authenticated)
		require.Equal(t, http.StatusOK, status)
		require.NoError(t, json.Unmarshal(data, &flow))
		status, _ = r.requestAt(t, r.public, http.MethodPost, "/self-service/settings?flow="+flow.ID,
			map[string]any{"method": "profile", "traits": map[string]any{
				"username": "changed-username", "email": "registered@example.test", "full_name": "Publicly updated",
			}}, authenticated)
		require.GreaterOrEqual(t, status, http.StatusBadRequest)
		require.Equal(t, "registered", r.identity(t, actual.ID).Traits["username"])
		previousTimeout := r.client.Timeout
		r.client.Timeout = 45 * time.Second
		defer func() { r.client.Timeout = previousTimeout }()
		r.command(t, nil, "stop", "--time", "5", r.receiver)
		defer func() {
			r.command(t, nil, "start", r.receiver)
			require.Eventually(t, func() bool {
				return exec.Command("docker", "exec", r.postgres, "wget", "-q", "-O", "/dev/null",
					"http://directory-receiver:8085/health/ready").Run() == nil
			}, 30*time.Second, 250*time.Millisecond)
		}()
		status, data = r.requestAt(t, r.public, http.MethodGet, "/self-service/settings/api", nil, authenticated)
		require.Equal(t, http.StatusOK, status)
		require.NoError(t, json.Unmarshal(data, &flow))
		status, _ = r.requestAt(t, r.public, http.MethodPost, "/self-service/settings?flow="+flow.ID,
			map[string]any{"method": "profile", "traits": map[string]any{
				"username": "registered", "email": "registered@example.test", "full_name": "Blocked update",
			}}, authenticated)
		require.GreaterOrEqual(t, status, http.StatusBadRequest, "blocking hook failure must abort persistence")
		require.Equal(t, "Publicly updated", r.identity(t, actual.ID).Traits["full_name"])
	})
	t.Run("GitStore Admin PATCH persists revisions and actors", func(t *testing.T) {
		r.command(t, nil, "exec", r.receiver, "/directory-receiver.test",
			"-test.run=^TestOwnedKratosAdminPatch$", "-test.timeout=20s")
	})
	t.Run("selected Hydra and UI images remain compatible", func(t *testing.T) {
		r.startHydra(t, "http://localhost:4445", "http://localhost:3000/callback")
		privateDir := t.TempDir()
		uiEnv := filepath.Join(privateDir, "ui.env")
		require.NoError(t, os.WriteFile(uiEnv, []byte(
			"KRATOS_PUBLIC_URL=http://gitstore-userdir-kratos-"+r.owner[:12]+"-0:4433/\n"+
				"KRATOS_BROWSER_URL="+r.public+"/\nCOOKIE_SECRET="+r.secret+
				"\nCSRF_COOKIE_NAME=gitstore_oidc_csrf\nCSRF_COOKIE_SECRET="+r.secret+"\nCOOKIE_SECURE=false\n"), 0600))
		ui := "gitstore-userdir-ui-" + r.owner[:12]
		r.command(t, nil, "run", "-d", "--name", ui,
			"--label", "dev.gitstore.test_owner="+r.owner, "--network", r.network,
			"--memory", "512m", "--cpus", "1", "--env-file", uiEnv,
			"-p", "127.0.0.1::3000", "oryd/kratos-selfservice-ui-node:v26.2.0")
		r.cleanupContainer(t, ui, true)
		t.Cleanup(func() {
			if t.Failed() {
				t.Log(strings.ReplaceAll(r.command(t, nil, "logs", ui), r.secret, "[redacted]"))
			}
		})
		uiPort := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{(index (index .NetworkSettings.Ports "3000/tcp") 0).HostPort}}`, ui))
		uiClient := &http.Client{Timeout: 2 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		require.Eventually(t, func() bool {
			response, err := uiClient.Get("http://127.0.0.1:" + uiPort + "/registration")
			if err != nil {
				return false
			}
			defer response.Body.Close()
			return response.StatusCode == http.StatusOK || response.StatusCode == http.StatusSeeOther ||
				response.StatusCode == http.StatusFound
		}, 30*time.Second, 250*time.Millisecond)
	})
}
