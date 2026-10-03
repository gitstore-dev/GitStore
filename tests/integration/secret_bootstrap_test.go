// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type secretTestController struct {
	cmd      *exec.Cmd
	done     chan error
	stopped  bool
	endpoint string
	config   string
	record   string
	log      string
}

func startSecretTestController(t *testing.T, binary, api, namespace, name, uid, privateKey, kid string) *secretTestController {
	t.Helper()
	root := t.TempDir()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	providerRoot := filepath.Join(root, "provider")
	require.NoError(t, os.Mkdir(providerRoot, 0700))
	c := &secretTestController{
		endpoint: fmt.Sprintf("http://127.0.0.1:%d", port),
		config:   filepath.Join(root, "config.toml"), record: filepath.Join(providerRoot, "controller.json"),
		log: filepath.Join(root, "controller.log"),
	}
	writeSecretTestRecord(t, c.record, privateKey, kid)
	config := fmt.Sprintf(`[controller]
port = %d
api_uri = %q
[controller.serviceaccount]
namespace = %q
name = %q
uid = %q
key_ref = { kind = "SecretRef", name = "controller" }
[controller.secret_providers.bootstrap]
type = "file"
format = "json-record"
base_path = %q
[controller.checkpoint]
dir = %q
flush_interval_events = 1
[controller.watch]
resync_interval = "1s"
max_backoff = "2s"
`, port, api+"/graphql", namespace, name, uid, providerRoot, filepath.Join(root, "checkpoints"))
	require.NoError(t, os.WriteFile(c.config, []byte(config), 0600))
	c.start(t, binary)
	return c
}

func (c *secretTestController) start(t *testing.T, binary string) {
	t.Helper()
	log, err := os.OpenFile(c.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	c.cmd = exec.Command(binary, "--config-file", c.config)
	c.cmd.Dir = filepath.Dir(c.config)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GITSTORE_") {
			c.cmd.Env = append(c.cmd.Env, entry)
		}
	}
	c.cmd.Stdout, c.cmd.Stderr = log, log
	err = c.cmd.Start()
	if err != nil {
		_ = log.Close()
		t.Fatal("could not start test-owned controller")
	}
	c.done, c.stopped = make(chan error, 1), false
	go func() {
		err := c.cmd.Wait()
		_ = log.Close()
		c.done <- err
	}()
	t.Cleanup(func() { c.stop(t) })
	waitSecretTestReady(t, c, true, 60*time.Second)
}

func (c *secretTestController) stop(t *testing.T) {
	t.Helper()
	if c.stopped {
		return
	}
	c.stopped = true
	select {
	case err := <-c.done:
		if err != nil {
			t.Error("test-owned controller exited unexpectedly")
		}
		return
	default:
	}
	if err := c.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Error("could not signal test-owned controller")
	}
	select {
	case err := <-c.done:
		if err != nil {
			t.Error("test-owned controller shutdown failed")
		}
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
		t.Error("test-owned controller exceeded shutdown deadline")
	}
}

func writeSecretTestRecord(t *testing.T, path, privateKey, kid string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"format": "serviceaccount-signing-key/v1",
		"values": map[string]string{
			"privateKey": base64.StdEncoding.EncodeToString([]byte(privateKey)),
			"keyID":      base64.StdEncoding.EncodeToString([]byte(kid)),
		},
	})
	require.NoError(t, err)
	temp, err := os.CreateTemp(filepath.Dir(path), ".record-*")
	require.NoError(t, err)
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	require.NoError(t, err)
	require.NoError(t, temp.Sync())
	require.NoError(t, temp.Close())
	require.NoError(t, os.Rename(temp.Name(), path))
}

func secretTestReady(c *secretTestController) (bool, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(c.endpoint + "/health")
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	var body struct {
		CredentialReady *bool `json:"credentialReady"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body); err != nil {
		return false, err
	}
	if body.CredentialReady == nil {
		return false, fmt.Errorf("missing credential readiness")
	}
	return *body.CredentialReady, nil
}

func waitSecretTestReady(t *testing.T, c *secretTestController, want bool, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		got, err := secretTestReady(c)
		return err == nil && got == want
	}, timeout, 100*time.Millisecond, "controller did not reach the required credential state")
}

func secretTestGraphQL(t *testing.T, api, token, query string, variables map[string]any) gqlResponse {
	t.Helper()
	data, err := json.Marshal(gqlRequest{Query: query, Variables: variables})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/graphql", bytes.NewReader(data))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal("test GraphQL request failed")
	}
	defer response.Body.Close()
	var result gqlResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		t.Fatal("invalid test GraphQL response")
	}
	return result
}

func secretTestRotate(t *testing.T, api, token, namespace, name string, add []map[string]any, remove []string) string {
	t.Helper()
	result := secretTestGraphQL(t, api, token, `mutation($input: RotateServiceAccountKeyInput!) {
  rotateServiceAccountKey(input:$input) { serviceAccount { metadata { uid } } }
}`, map[string]any{"input": map[string]any{
		"metadata": map[string]string{"namespace": namespace, "name": name}, "add": add, "removeKids": remove,
	}})
	require.True(t, len(result.Errors) == 0, "test-key enrollment/retirement failed")
	var data struct {
		RotateServiceAccountKey struct {
			ServiceAccount struct {
				Metadata struct{ UID string } `json:"metadata"`
			} `json:"serviceAccount"`
		} `json:"rotateServiceAccountKey"`
	}
	require.NoError(t, json.Unmarshal(result.Data, &data))
	require.NotEmpty(t, data.RotateServiceAccountKey.ServiceAccount.Metadata.UID)
	return data.RotateServiceAccountKey.ServiceAccount.Metadata.UID
}

func TestSecretBootstrapRotationDeployed(t *testing.T) {
	if os.Getenv("SECRET_TEST_RUN") != "1" {
		t.Skip("use make test-secret-integration against an isolated two-API test deployment")
	}
	require.Equal(t, "1", os.Getenv("SECRET_TEST_OWNED_DEPLOYMENT"), "explicit test-deployment ownership is required")
	apiA, apiB := strings.TrimSuffix(os.Getenv("SECRET_TEST_API_A"), "/"), strings.TrimSuffix(os.Getenv("SECRET_TEST_API_B"), "/")
	binary := os.Getenv("SECRET_TEST_CONTROLLER_BINARY")
	for _, value := range []string{apiA, apiB, binary, os.Getenv("SECRET_TEST_TOKEN_FILE")} {
		require.NotEmpty(t, value, "two APIs, controller binary and administrator token file are required")
	}
	require.True(t, filepath.IsAbs(binary), "controller binary must be an absolute path")
	tokenBytes, err := os.ReadFile(os.Getenv("SECRET_TEST_TOKEN_FILE"))
	require.NoError(t, err)
	token := strings.TrimSpace(string(tokenBytes))
	clear(tokenBytes)
	client := &http.Client{Timeout: 5 * time.Second}
	require.NotEqual(t, repositoryCapacityAPIIdentity(t, client, apiA), repositoryCapacityAPIIdentity(t, client, apiB))
	namespace := getEnv("SECRET_TEST_NAMESPACE", "controllers")
	name := getEnv("SECRET_TEST_SERVICEACCOUNT", "gitstore-controller-manager")
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	kidA, kidB, kidNext := "spec063 A "+suffix, "spec063 B "+suffix, "spec063 rotated "+suffix
	privateA, privateB, privateNext := generateEd25519PEM(t), generateEd25519PEM(t), generateEd25519PEM(t)
	publicKey := func(kid, private string) map[string]any {
		return map[string]any{"kid": kid, "algorithm": "Ed25519", "publicKeyPEM": extractPublicKeyPEM(t, private)}
	}
	uid := secretTestRotate(t, apiA, token, namespace, name,
		[]map[string]any{publicKey(kidA, privateA), publicKey(kidB, privateB)}, []string{})
	t.Cleanup(func() {
		secretTestRotate(t, apiA, token, namespace, name, []map[string]any{}, []string{kidA, kidB, kidNext})
	})
	h := &serviceAccountAuthHarness{t: t, apiURL: apiA, token: token}
	const issue = `mutation($input: IssueServiceAccountTokenInput!) {
  issueServiceAccountToken(input:$input) { tokenRequest { status { token expirationTimestamp } } }
}`
	input := map[string]any{"input": map[string]any{
		"metadata": map[string]string{"namespace": namespace, "name": name},
		"spec":     map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 600},
	}}
	for _, api := range []string{apiA, apiB} {
		assertion := h.signClientAssertionWithKeyID(namespace, name, uid, kidA, privateA)
		result := secretTestGraphQL(t, api, assertion, issue, input)
		require.True(t, len(result.Errors) == 0, "authorized assertion was denied")
		var data struct {
			IssueServiceAccountToken struct {
				TokenRequest struct {
					Status struct{ Token string } `json:"status"`
				} `json:"tokenRequest"`
			} `json:"issueServiceAccountToken"`
		}
		require.NoError(t, json.Unmarshal(result.Data, &data))
		claims := jwt.RegisteredClaims{}
		_, _, err := jwt.NewParser().ParseUnverified(data.IssueServiceAccountToken.TokenRequest.Status.Token, &claims)
		require.True(t, err == nil, "invalid issued token")
		require.NotNil(t, claims.ExpiresAt)
		require.NotNil(t, claims.IssuedAt)
		require.Equal(t, time.Minute, claims.ExpiresAt.Sub(claims.IssuedAt.Time), "test APIs must clamp access-token TTL to 60s")
		bad := h.signClientAssertionWithKeyID(namespace, "wrong-subject", uid, kidA, privateA)
		denied := secretTestGraphQL(t, api, bad, issue, input)
		require.NotEmpty(t, denied.Errors, "wrong subject must be denied")
		require.NotContains(t, string(denied.Data), "eyJ", "denial must not deliver a token")
		mismatched := h.signClientAssertionWithKeyID(namespace, name, uid, kidA, privateB)
		require.NotEmpty(t, secretTestGraphQL(t, api, mismatched, issue, input).Errors, "mismatched signing key was accepted")
	}
	a := startSecretTestController(t, binary, apiA, namespace, name, uid, privateA, kidA)
	b := startSecretTestController(t, binary, apiB, namespace, name, uid, privateB, kidB)
	require.NotEqual(t, repositoryControllerIdentity(t, client, a.endpoint), repositoryControllerIdentity(t, client, b.endpoint))
	snapshot, err := os.ReadFile(a.config)
	require.NoError(t, err)
	require.NoError(t, os.Rename(a.record, a.record+".withdrawn"))
	outageUntil := time.Now().Add(90 * time.Second)
	for time.Now().Before(outageUntil) {
		ready, err := secretTestReady(b)
		require.NoError(t, err)
		require.True(t, ready, "peer credential failed during isolated provider outage")
		select {
		case <-t.Context().Done():
			t.Fatal("provider outage canceled")
		case <-time.After(time.Second):
		}
	}
	waitSecretTestReady(t, a, false, time.Second)
	restoredAt := time.Now()
	require.NoError(t, os.Rename(a.record+".withdrawn", a.record))
	waitSecretTestReady(t, a, true, 60*time.Second)
	recovery := time.Since(restoredAt)
	writeSecretTestRecord(t, a.record, privateB, kidA)
	secretTestHealthyWindow(t, b, b, 65*time.Second)
	waitSecretTestReady(t, a, false, time.Second)
	writeSecretTestRecord(t, a.record, privateA, kidA)
	waitSecretTestReady(t, a, true, 60*time.Second)
	secretTestRotate(t, apiB, token, namespace, name, []map[string]any{publicKey(kidNext, privateNext)}, []string{})
	writeSecretTestRecord(t, a.record, privateNext, kidNext)
	writeSecretTestRecord(t, b.record, privateNext, kidNext)
	// Wait past the previously issued tokens before retiring their signing keys.
	secretTestHealthyWindow(t, a, b, 65*time.Second)
	secretTestRotate(t, apiA, token, namespace, name, []map[string]any{}, []string{kidA, kidB})
	oldAssertion := h.signClientAssertionWithKeyID(namespace, name, uid, kidA, privateA)
	require.NotEmpty(t, secretTestGraphQL(t, apiB, oldAssertion, issue, input).Errors, "retired assertion key was accepted")
	secretTestHealthyWindow(t, a, b, 65*time.Second)
	before := repositoryControllerIdentity(t, client, a.endpoint)
	a.stop(t)
	a.start(t, binary)
	require.NotEqual(t, before, repositoryControllerIdentity(t, client, a.endpoint))
	after, err := os.ReadFile(a.config)
	require.NoError(t, err)
	require.True(t, bytes.Equal(snapshot, after), "rotation changed the reference/configuration")
	for _, endpoint := range []string{apiA, apiB, a.endpoint, b.endpoint} {
		response, err := client.Get(endpoint + "/metrics")
		require.NoError(t, err)
		metrics, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		_ = response.Body.Close()
		require.NoError(t, err)
		require.Less(t, len(metrics), 8<<20, "metrics exceeded bounded scan size")
		secretTestNoCredentials(t, metrics, []string{privateA, privateB, privateNext, token})
	}
	a.stop(t)
	b.stop(t)
	for _, c := range []*secretTestController{a, b} {
		log, err := os.ReadFile(c.log)
		require.NoError(t, err)
		secretTestNoCredentials(t, log, []string{privateA, privateB, privateNext, token})
	}
	t.Logf("two-API/two-controller bootstrap, outage, rotation and replacement completed; recovery=%s", recovery)
}

func secretTestNoCredentials(t *testing.T, data []byte, markers []string) {
	t.Helper()
	pattern := regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----|eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
	require.False(t, pattern.Match(data), "telemetry exposed PEM/JWT material")
	for _, marker := range markers {
		require.False(t, bytes.Contains(data, []byte(marker)), "telemetry exposed a credential marker")
	}
}

func secretTestHealthyWindow(t *testing.T, controllersA, controllersB *secretTestController, duration time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), duration)
	defer cancel()
	for {
		for _, c := range []*secretTestController{controllersA, controllersB} {
			ready, err := secretTestReady(c)
			require.NoError(t, err)
			require.True(t, ready, "controller failed to renew during key overlap/retirement")
		}
		select {
		case <-ctx.Done():
			require.NoError(t, t.Context().Err())
			return
		case <-time.After(time.Second):
		}
	}
}
