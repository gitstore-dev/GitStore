// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
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
	require.NoError(t, writeSecretCapacitySigningRecord(path, secretCapacityOwnedKey{ID: kid, Private: privateKey}))
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

func secretTestRotate(t *testing.T, api, token, namespace, name string, add []secretCapacityOwnedKey, remove []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	fixture := secretCapacityOwnedFixture{Namespace: namespace, Name: name}
	uid, err := fixture.rotate(ctx, client, api, token, add, remove)
	require.NoError(t, err, "test-key enrollment/retirement failed")
	return uid
}

func TestSecretBootstrapRotationDeployed(t *testing.T) {
	if os.Getenv("SECRET_TEST_RUN") != "1" {
		t.Skip("use make test TARGET=secret-integration against an isolated two-API test deployment")
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
	publicKey := func(kid, private string) secretCapacityOwnedKey {
		return secretCapacityOwnedKey{ID: kid, Public: extractPublicKeyPEM(t, private)}
	}
	uid := secretTestRotate(t, apiA, token, namespace, name,
		[]secretCapacityOwnedKey{publicKey(kidA, privateA), publicKey(kidB, privateB)}, []string{})
	t.Cleanup(func() {
		secretTestRotate(t, apiA, token, namespace, name, nil, []string{kidA, kidB, kidNext})
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
	secretTestRotate(t, apiB, token, namespace, name, []secretCapacityOwnedKey{publicKey(kidNext, privateNext)}, []string{})
	writeSecretTestRecord(t, a.record, privateNext, kidNext)
	writeSecretTestRecord(t, b.record, privateNext, kidNext)
	// Wait past the previously issued tokens before retiring their signing keys.
	secretTestHealthyWindow(t, a, b, 65*time.Second)
	secretTestRotate(t, apiA, token, namespace, name, nil, []string{kidA, kidB})
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

type secretCapacityOwnedKey struct {
	ID      string `json:"id"`
	Private string `json:"private"`
	Public  string `json:"public"`
}

var secretCapacityFixtureRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type secretCapacityOwnedFixture struct {
	SchemaVersion  int                      `json:"schemaVersion"`
	RunID          string                   `json:"runID"`
	Project        string                   `json:"project"`
	Namespace      string                   `json:"namespace"`
	Name           string                   `json:"name"`
	UID            string                   `json:"uid"`
	Keys           []secretCapacityOwnedKey `json:"keys"`
	RuntimeMarkers []string                 `json:"runtimeMarkers"`
}

func newSecretCapacityOwnedKey() (secretCapacityOwnedKey, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return secretCapacityOwnedKey{}, errors.New("secret capacity: cannot generate owned signing key")
	}
	defer clear(private)
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return secretCapacityOwnedKey{}, errors.New("secret capacity: cannot encode owned signing key")
	}
	defer clear(privateDER)
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return secretCapacityOwnedKey{}, errors.New("secret capacity: cannot encode owned public key")
	}
	return secretCapacityOwnedKey{
		ID:      "capacity-" + rand.Text(),
		Private: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
		Public:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
	}, nil
}

func writeSecretCapacityPrivateJSON(path string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".owned-record-*")
	if err != nil {
		return errors.New("secret capacity: cannot create owned record")
	}
	defer os.Remove(file.Name())
	encodeErr := json.NewEncoder(file).Encode(value)
	syncErr := file.Sync()
	closeErr := file.Close()
	if encodeErr != nil || syncErr != nil || closeErr != nil || os.Rename(file.Name(), path) != nil {
		return errors.New("secret capacity: cannot atomically persist owned record")
	}
	return nil
}

func writeSecretCapacitySigningRecord(path string, key secretCapacityOwnedKey) error {
	return writeSecretCapacityPrivateJSON(path, map[string]any{
		"format": "serviceaccount-signing-key/v1",
		"values": map[string]string{
			"privateKey": base64.StdEncoding.EncodeToString([]byte(key.Private)),
			"keyID":      base64.StdEncoding.EncodeToString([]byte(key.ID)),
		},
	})
}

func (fixture secretCapacityOwnedFixture) rotate(ctx context.Context, client *http.Client, api, token string,
	add []secretCapacityOwnedKey, remove []string,
) (string, error) {
	keys := make([]map[string]string, 0, len(add))
	for _, key := range add {
		keys = append(keys, map[string]string{"kid": key.ID, "algorithm": "Ed25519", "publicKeyPEM": key.Public})
	}
	if remove == nil {
		remove = []string{}
	}
	var data struct {
		RotateServiceAccountKey struct {
			ServiceAccount struct {
				Metadata struct {
					UID string `json:"uid"`
				} `json:"metadata"`
			} `json:"serviceAccount"`
		} `json:"rotateServiceAccountKey"`
	}
	err := secretCapacityGraphQL(ctx, client, api, token, `mutation($input:RotateServiceAccountKeyInput!) {
		rotateServiceAccountKey(input:$input) { serviceAccount { metadata { uid } } }
	}`, map[string]any{"input": map[string]any{
		"metadata": map[string]string{"namespace": fixture.Namespace, "name": fixture.Name},
		"add":      keys, "removeKids": remove,
	}}, &data)
	uid := data.RotateServiceAccountKey.ServiceAccount.Metadata.UID
	if err != nil || uid == "" || (fixture.UID != "" && uid != fixture.UID) {
		return "", errors.New("secret capacity: owned key enrollment or retirement failed")
	}
	return uid, nil
}

func (fixture secretCapacityOwnedFixture) assertion(key secretCapacityOwnedKey, subjectName string) (string, error) {
	block, _ := pem.Decode([]byte(key.Private))
	if block == nil {
		return "", errors.New("secret capacity: invalid owned signing material")
	}
	defer clear(block.Bytes)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", errors.New("secret capacity: invalid owned signing material")
	}
	private, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("secret capacity: owned signing material must be Ed25519")
	}
	defer clear(private)
	now := time.Now()
	subject := "serviceaccount:" + fixture.Namespace + ":" + subjectName
	assertion := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.MapClaims{
		"iss": subject, "sub": subject, "aud": []string{"gitstore-api/serviceaccount-token"},
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(45 * time.Second).Unix(),
		"jti": rand.Text(), "sa_uid": fixture.UID,
	})
	assertion.Header["typ"], assertion.Header["kid"] = "gitstore-sa-assertion+jwt", key.ID
	signed, err := assertion.SignedString(private)
	if err != nil {
		return "", errors.New("secret capacity: cannot sign owned assertion")
	}
	return signed, nil
}

func secretCapacityFixturePath() (string, string, error) {
	root, runID := os.Getenv("REPOSITORY_CAPACITY_SECRET_FIXTURE_DIR"), os.Getenv("CAPACITY_RUN_ID")
	if os.Getenv("REPOSITORY_CAPACITY_SECRET_OWNED_DEPLOYMENT") != "1" || !filepath.IsAbs(root) ||
		runID == "" || filepath.Base(root) != runID || strings.ContainsAny(runID, `/\`) ||
		!secretCapacityFixtureRunID.MatchString(runID) {
		return "", "", errors.New("secret capacity: explicit owned deployment and run-scoped fixture directory required")
	}
	return root, runID, nil
}

func readSecretCapacityOwnedFixture(root, runID string) (secretCapacityOwnedFixture, error) {
	var fixture secretCapacityOwnedFixture
	invalid := errors.New("secret capacity: invalid owned fixture identity")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return fixture, invalid
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return fixture, invalid
	}
	defer dir.Close()
	file, err := openSecretCapacityArtifact(dir, "owned-fixture.json")
	if err != nil {
		return fixture, invalid
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || info.Size() > 64*1024 || info.Mode().Perm()&0077 != 0 {
		return fixture, invalid
	}
	body := make([]byte, info.Size())
	if _, err := io.ReadFull(file, body); err != nil || decodeSecretCapacityJSON(body, &fixture) != nil {
		clear(body)
		return secretCapacityOwnedFixture{}, invalid
	}
	clear(body)
	if fixture.SchemaVersion != 1 || fixture.RunID != runID || fixture.Project == "" ||
		fixture.Namespace != "controllers" || fixture.Name != "gitstore-controller-manager" || len(fixture.Keys) != 3 {
		return secretCapacityOwnedFixture{}, invalid
	}
	seen := make(map[string]bool)
	for _, key := range fixture.Keys {
		if !strings.HasPrefix(key.ID, "capacity-") || seen[key.ID] || key.Private == "" || key.Public == "" {
			return secretCapacityOwnedFixture{}, invalid
		}
		seen[key.ID] = true
	}
	return fixture, nil
}

func TestSecretCapacityFixtureProvision(t *testing.T) {
	if os.Getenv("REPOSITORY_CAPACITY_SECRET_FIXTURE_ACTION") == "" {
		t.Skip("owned fixture lifecycle is invoked by the root capacity stack")
	}
	root, runID, err := secretCapacityFixturePath()
	require.NoError(t, err)
	tokenBytes, err := os.ReadFile(os.Getenv("REPOSITORY_TOKEN_FILE"))
	require.NoError(t, err)
	token := strings.TrimSpace(string(tokenBytes))
	clear(tokenBytes)
	require.NotEmpty(t, token)
	client := &http.Client{Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	api := getEnv("REPOSITORY_API_A", "http://127.0.0.1:4000")
	action := os.Getenv("REPOSITORY_CAPACITY_SECRET_FIXTURE_ACTION")
	if action == "cleanup" {
		fixture, err := readSecretCapacityOwnedFixture(root, runID)
		require.NoError(t, err)
		var ids []string
		for _, key := range fixture.Keys {
			ids = append(ids, key.ID)
		}
		_, err = fixture.rotate(t.Context(), client, api, token, nil, ids)
		if err != nil {
			t.Error("owned key retirement failed; still removing private fixture files")
		}
		// Remove only enumerated files created by this fixture owner.
		for _, role := range []string{"controller-a", "controller-b"} {
			for _, name := range []string{"provider/controller.json", "provider/controller.json.withdrawn", "config.toml"} {
				err := os.Remove(filepath.Join(root, role, name))
				require.True(t, err == nil || errors.Is(err, os.ErrNotExist), "owned fixture file cleanup failed")
			}
			for _, directory := range []string{filepath.Join(root, role, "provider"), filepath.Join(root, role)} {
				err := os.Remove(directory)
				require.True(t, err == nil || errors.Is(err, os.ErrNotExist), "owned fixture directory cleanup failed")
			}
		}
		require.NoError(t, os.Remove(filepath.Join(root, "owned-fixture.json")))
		require.NoError(t, os.Remove(root))
		return
	}
	require.Equal(t, "provision", action)
	require.NoError(t, os.Mkdir(root, 0700), "refuse to overwrite any existing fixture directory")
	fixture := secretCapacityOwnedFixture{SchemaVersion: 1, RunID: runID,
		Project:   getEnv("REPOSITORY_CAPACITY_PROJECT", "gitstore-repository-capacity"),
		Namespace: "controllers", Name: "gitstore-controller-manager"}
	fixture.RuntimeMarkers = []string{}
	for range 3 {
		key, err := newSecretCapacityOwnedKey()
		require.NoError(t, err)
		fixture.Keys = append(fixture.Keys, key)
	}
	// Persist owned IDs before enrollment, allowing cleanup after partial failure.
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "owned-fixture.json"), fixture))
	fixture.UID, err = fixture.rotate(t.Context(), client, api, token, fixture.Keys[:2], nil)
	require.NoError(t, err)
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "owned-fixture.json"), fixture))
	for i, role := range []string{"controller-a", "controller-b"} {
		provider := filepath.Join(root, role, "provider")
		require.NoError(t, os.MkdirAll(provider, 0700))
		require.NoError(t, writeSecretCapacitySigningRecord(filepath.Join(provider, "controller.json"), fixture.Keys[i]))
		config := fmt.Sprintf(`[controller]
port = 5001
api_uri = "http://api-%s:4000/graphql"
[controller.serviceaccount]
namespace = "controllers"
name = "gitstore-controller-manager"
uid = %q
key_ref = {kind = "SecretRef", name = "controller"}
[controller.secret_providers.bootstrap]
type = "file"
format = "json-record"
base_path = "/run/secrets"
[controller.checkpoint]
dir = "/var/lib/gitstore/checkpoints"
flush_interval_events = 1
`, []string{"a", "b"}[i], fixture.UID)
		require.NoError(t, os.WriteFile(filepath.Join(root, role, "config.toml"), []byte(config), 0600))
	}
}

type secretCapacityFaultObservations struct {
	SchemaVersion       int                            `json:"schemaVersion"`
	Component           string                         `json:"component"`
	RunID               string                         `json:"runID"`
	Completed           bool                           `json:"completed"`
	OutageAt            time.Duration                  `json:"outageAt"`
	OutageElapsed       time.Duration                  `json:"outageElapsed"`
	ExpiredUnready      bool                           `json:"expiredUnready"`
	ClassifiedFailures  int64                          `json:"classifiedFailures"`
	PeerFailures        int64                          `json:"peerFailures"`
	Recovery            []time.Duration                `json:"recovery"`
	RotationAt          time.Duration                  `json:"rotationAt"`
	OverlapRenewals     []int64                        `json:"overlapRenewals"`
	RetiredKeyDenied    int64                          `json:"retiredKeyDenied"`
	WrongSubjectDenied  int64                          `json:"wrongSubjectDenied"`
	AuthorizedIssuance  int64                          `json:"authorizedIssuance"`
	RestartAt           time.Duration                  `json:"restartAt"`
	RestartTargetID     string                         `json:"restartTargetID"`
	RestartBefore       secretCapacityControllerSample `json:"restartBefore"`
	Replacement         secretCapacityControllerSample `json:"replacement"`
	ReplacementRecovery time.Duration                  `json:"replacementRecovery"`
	RestartConfirmed    bool                           `json:"restartConfirmed"`
	RestartSamplingGaps int64                          `json:"restartSamplingGaps"`
}

type secretCapacityFaultDriver struct {
	fixture             secretCapacityOwnedFixture
	root, evidence      string
	client              *http.Client
	cfg                 repositoryCapacityConfig
	restartSamplingGaps int64
	initialAuthorized   int64
}

func secretCapacityOwnedCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	var output secretCapacityGitOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil || output.overflow {
		clear(output.buffer.Bytes())
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("secret capacity: owned deployment command failed")
	}
	return output.buffer.Bytes(), nil
}

func validateSecretCapacityOwnedMounts(ctx context.Context, root, project string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return errors.New("secret capacity: cannot resolve owned fixture root")
	}
	topology, err := secretCapacityOwnedCommand(ctx, "docker", "ps", "--filter", "label=com.docker.compose.project="+project,
		"--format", `{{.Names}} {{.Label "com.docker.compose.service"}}`)
	if err != nil {
		return err
	}
	if err := validateSecretCapacityGitTopology(string(topology)); err != nil {
		return err
	}
	names := []string{"gitstore-capacity-controller-manager-a", "gitstore-capacity-controller-manager-b",
		"gitstore-capacity-api-a", "gitstore-capacity-api-b", "gitstore-capacity-git-service"}
	for i, name := range names {
		body, err := secretCapacityOwnedCommand(ctx, "docker", "inspect", "--format",
			`{"project":{{json (index .Config.Labels "com.docker.compose.project")}},"running":{{json .State.Running}},"mounts":{{json .Mounts}}}`, name)
		if err != nil {
			return err
		}
		var inspected struct {
			Project string `json:"project"`
			Running bool   `json:"running"`
			Mounts  []struct {
				Source      string `json:"Source"`
				Destination string `json:"Destination"`
				RW          bool   `json:"RW"`
			} `json:"mounts"`
		}
		if json.Unmarshal(body, &inspected) != nil || inspected.Project != project || !inspected.Running {
			return errors.New("secret capacity: container is not in the owned running project")
		}
		ownedProvider, ownedConfig := false, false
		for _, mount := range inspected.Mounts {
			source := filepath.Clean(mount.Source)
			overlap := source == root || strings.HasPrefix(source, root+string(os.PathSeparator)) ||
				strings.HasPrefix(root, source+string(os.PathSeparator))
			if overlap {
				if i >= 2 || mount.RW {
					return errors.New("secret capacity: provider mounts are writable or visible to another service")
				}
				role := []string{"controller-a", "controller-b"}[i]
				switch {
				case source == filepath.Join(root, role, "provider") && mount.Destination == "/run/secrets":
					ownedProvider = true
				case source == filepath.Join(root, role, "config.toml") && mount.Destination == "/etc/gitstore/gitstore.toml":
					ownedConfig = true
				default:
					return errors.New("secret capacity: unexpected access to owned private fixture files")
				}
			} else if i < 2 && mount.Destination != "/var/lib/gitstore/checkpoints" &&
				!(mount.Destination == "/run/controller-bootstrap" && !mount.RW) {
				return errors.New("secret capacity: unexpected controller mount outside owned provider scope")
			}
		}
		if i < 2 && (!ownedProvider || !ownedConfig || len(inspected.Mounts) != 4) {
			return errors.New("secret capacity: controllers require owned read-only config/provider, UID and checkpoint mounts")
		}
	}
	return nil
}

func validateSecretCapacityGitTopology(topology string) error {
	gitProcesses := 0
	for _, line := range strings.Split(strings.TrimSpace(topology), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return errors.New("secret capacity: malformed owned project topology")
		}
		if strings.HasSuffix(fields[1], "git-service") {
			if fields[0] != "gitstore-capacity-git-service" || fields[1] != "capacity-git-service" {
				return errors.New("secret capacity: an additional Git service is active in the owned deployment")
			}
			gitProcesses++
		}
	}
	if gitProcesses != 1 {
		return errors.New("secret capacity requires exactly one active singleton Git service")
	}
	return nil
}

func TestSecretCapacityOwnedGitTopology(t *testing.T) {
	valid := "gitstore-capacity-git-service capacity-git-service\napi api-a\n"
	require.NoError(t, validateSecretCapacityGitTopology(valid))
	for _, invalid := range []string{"", "api api-a", valid + valid, valid + "other git-service\n", valid + "other capacity-git-service\n"} {
		require.Error(t, validateSecretCapacityGitTopology(invalid))
	}
}

func (driver *secretCapacityFaultDriver) samples(ctx context.Context) ([2]secretCapacityControllerSample, error) {
	var result [2]secretCapacityControllerSample
	for i, endpoint := range []string{driver.cfg.controllerA, driver.cfg.controllerB} {
		sample, err := sampleSecretCapacityController(ctx, driver.client, endpoint)
		if err != nil {
			return result, err
		}
		result[i] = sample
	}
	if result[0].ID == result[1].ID {
		return result, errors.New("secret capacity: controller endpoints identify the same process")
	}
	return result, nil
}

func waitSecretCapacityUntil(ctx context.Context, deadline time.Time) error {
	timer := time.NewTimer(max(time.Until(deadline), 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (driver *secretCapacityFaultDriver) recover(ctx context.Context, baseline [2]secretCapacityControllerSample,
	started time.Time, replacement bool,
) ([2]secretCapacityControllerSample, []time.Duration, error) {
	ctx, cancel := context.WithDeadline(ctx, started.Add(time.Minute))
	defer cancel()
	times := make([]time.Duration, 2)
	var latest [2]secretCapacityControllerSample
	for {
		samples, err := driver.samples(ctx)
		if err != nil {
			// A restarted process can be unreachable before its HTTP listener opens.
			// No observation is credited until a complete identity-bracketed sample.
			if !replacement || !errors.Is(err, errSecretCapacityControllerUnavailable) || ctx.Err() != nil {
				return latest, times, err
			}
			driver.restartSamplingGaps++
		} else {
			latest = samples
			for i := range samples {
				progress, err := secretCapacityControllerProgress(baseline[i], samples[i], replacement && i == 0)
				if err != nil {
					return latest, times, err
				}
				if progress && times[i] == 0 {
					times[i] = time.Since(started)
					if times[i] > time.Minute {
						return latest, times, errors.New("secret capacity: authenticated recovery exceeded 60 seconds")
					}
				}
			}
			if times[0] > 0 && times[1] > 0 {
				return latest, times, nil
			}
		}
		if err := waitSecretCapacityUntil(ctx, time.Now().Add(time.Second)); err != nil {
			return latest, times, err
		}
	}
}

func (driver *secretCapacityFaultDriver) issue(ctx context.Context, api string, key secretCapacityOwnedKey, wrongSubject, wantDenied bool) error {
	name := driver.fixture.Name
	if wrongSubject {
		name += "-wrong-subject"
	}
	assertion, err := driver.fixture.assertion(key, name)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"query": `mutation($input:IssueServiceAccountTokenInput!) {
			issueServiceAccountToken(input:$input) { tokenRequest { status { token expirationTimestamp } } }
		}`,
		"variables": map[string]any{"input": map[string]any{
			"metadata": map[string]string{"namespace": driver.fixture.Namespace, "name": driver.fixture.Name},
			"spec":     map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 600},
		}},
	})
	if err != nil {
		return errors.New("secret capacity: cannot encode issuance probe")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(api, "/")+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return errors.New("secret capacity: invalid issuance endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+assertion)
	client := *driver.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return errors.New("secret capacity: issuance probe transport failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		return errors.New("secret capacity: invalid issuance probe response")
	}
	defer clear(body)
	if wantDenied && secretCapacitySensitivePattern.Match(body) {
		return errors.New("secret capacity: denied issuance response exposed credential material")
	}
	if wantDenied && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		return nil
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("secret capacity: unexpected issuance status")
	}
	var data struct {
		Data struct {
			IssueServiceAccountToken struct {
				TokenRequest struct {
					Status struct {
						Token               string    `json:"token"`
						ExpirationTimestamp time.Time `json:"expirationTimestamp"`
					} `json:"status"`
				} `json:"tokenRequest"`
			} `json:"issueServiceAccountToken"`
		} `json:"data"`
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &data) != nil {
		return errors.New("secret capacity: malformed issuance response")
	}
	status := data.Data.IssueServiceAccountToken.TokenRequest.Status
	if wantDenied {
		if status.Token == "" && len(data.Errors) > 0 {
			for _, denied := range data.Errors {
				if denied.Extensions.Code != "FORBIDDEN" && denied.Extensions.Code != "UNAUTHENTICATED" {
					return errors.New("secret capacity: issuance failed without an authorization denial")
				}
			}
			return nil
		}
		return errors.New("secret capacity: unauthorized issuance was not denied")
	}
	var claims jwt.RegisteredClaims
	_, _, parseErr := jwt.NewParser().ParseUnverified(status.Token, &claims)
	if len(data.Errors) != 0 || parseErr != nil || claims.ExpiresAt == nil || claims.IssuedAt == nil ||
		claims.ExpiresAt.Sub(claims.IssuedAt.Time) != time.Minute || !status.ExpirationTimestamp.Truncate(time.Second).Equal(claims.ExpiresAt.Time) ||
		claims.Subject != "serviceaccount:"+driver.fixture.Namespace+":"+driver.fixture.Name {
		return errors.New("secret capacity: authorized issuance must deliver the expected subject and a 60-second token")
	}
	return nil
}

func (driver *secretCapacityFaultDriver) run(ctx context.Context, started time.Time) (observation secretCapacityFaultObservations, resultErr error) {
	observation = secretCapacityFaultObservations{SchemaVersion: 1, Component: "secret-faults/v1",
		RunID: driver.fixture.RunID, Recovery: make([]time.Duration, 2), OverlapRenewals: make([]int64, 2),
		AuthorizedIssuance: driver.initialAuthorized}
	defer func() {
		observation.Completed = resultErr == nil
		observation.RestartSamplingGaps = driver.restartSamplingGaps
		resultErr = errors.Join(resultErr, writeSecretCapacityComponent(driver.evidence, "faults.json", observation))
	}()
	if err := waitSecretCapacityUntil(ctx, started.Add(15*time.Minute)); err != nil {
		return observation, err
	}
	before, err := driver.samples(ctx)
	if err != nil {
		return observation, err
	}
	record := filepath.Join(driver.root, "controller-a", "provider", "controller.json")
	if _, err := os.Lstat(record + ".withdrawn"); !errors.Is(err, os.ErrNotExist) {
		return observation, errors.New("secret capacity: refusing to replace an existing withdrawal backup")
	}
	outageStarted := time.Now()
	if err := os.Rename(record, record+".withdrawn"); err != nil {
		return observation, errors.New("secret capacity: cannot withdraw owned record")
	}
	withdrawn := true
	defer func() {
		if withdrawn && os.Rename(record+".withdrawn", record) != nil {
			resultErr = errors.Join(resultErr, errors.New("secret capacity: owned record restoration failed"))
		}
	}()
	observation.OutageAt = outageStarted.Sub(started)
	for time.Since(outageStarted) < 90*time.Second {
		current, err := driver.samples(ctx)
		if err != nil {
			return observation, err
		}
		if current[0].ID != before[0].ID || current[1].ID != before[1].ID || !current[1].Healthy {
			observation.PeerFailures++
			return observation, errors.New("secret capacity: peer failed or controller changed during isolated outage")
		}
		if time.Since(outageStarted) >= time.Minute && !current[0].CredentialReady {
			observation.ExpiredUnready = true
		}
		observation.ClassifiedFailures = current[0].RecordNotFound - before[0].RecordNotFound
		if err := waitSecretCapacityUntil(ctx, minTime(outageStarted.Add(90*time.Second), time.Now().Add(time.Second))); err != nil {
			return observation, err
		}
	}
	if err := os.Rename(record+".withdrawn", record); err != nil {
		return observation, errors.New("secret capacity: cannot restore owned record")
	}
	withdrawn = false
	restored := time.Now()
	observation.OutageElapsed = restored.Sub(outageStarted)
	if !observation.ExpiredUnready || observation.ClassifiedFailures <= 0 || observation.OutageElapsed > 91*time.Second {
		return observation, errors.New("secret capacity: expiry-spanning classified outage was not observed within its timing bound")
	}
	before, err = driver.samples(ctx)
	if err != nil {
		return observation, err
	}
	_, observation.Recovery, err = driver.recover(ctx, before, restored, false)
	if err != nil {
		return observation, err
	}
	if err := waitSecretCapacityUntil(ctx, started.Add(30*time.Minute)); err != nil {
		return observation, err
	}
	before, err = driver.samples(ctx)
	if err != nil {
		return observation, err
	}
	observation.RotationAt = time.Since(started)
	next := driver.fixture.Keys[2]
	if _, err := driver.fixture.rotate(ctx, driver.client, driver.cfg.apiA, driver.cfg.token, []secretCapacityOwnedKey{next}, nil); err != nil {
		return observation, err
	}
	for _, role := range []string{"controller-a", "controller-b"} {
		if err := writeSecretCapacitySigningRecord(filepath.Join(driver.root, role, "provider", "controller.json"), next); err != nil {
			return observation, err
		}
	}
	overlapStarted := time.Now()
	// Let an exchange that acquired the old record before replacement exhaust
	// its ten-second total budget before attributing fresh renewals to this key.
	for time.Since(overlapStarted) < 11*time.Second {
		current, err := driver.samples(ctx)
		if err != nil {
			return observation, err
		}
		for i := range current {
			if !current[i].Healthy || current[i].ID != before[i].ID {
				return observation, errors.New("secret capacity: controller failed during record replacement")
			}
		}
		if err := waitSecretCapacityUntil(ctx, minTime(overlapStarted.Add(11*time.Second), time.Now().Add(time.Second))); err != nil {
			return observation, err
		}
	}
	before, err = driver.samples(ctx)
	if err != nil {
		return observation, err
	}
	after, _, err := driver.recover(ctx, before, time.Now(), false)
	if err != nil {
		return observation, err
	}
	for i := range after {
		observation.OverlapRenewals[i] = after[i].FreshTokens - before[i].FreshTokens
	}
	for time.Since(overlapStarted) < 90*time.Second {
		current, err := driver.samples(ctx)
		if err != nil {
			return observation, err
		}
		for i := range current {
			if !current[i].Healthy || current[i].ID != before[i].ID {
				return observation, errors.New("secret capacity: controller failed during key overlap")
			}
		}
		if err := waitSecretCapacityUntil(ctx, minTime(overlapStarted.Add(90*time.Second), time.Now().Add(time.Second))); err != nil {
			return observation, err
		}
	}
	oldIDs := []string{driver.fixture.Keys[0].ID, driver.fixture.Keys[1].ID}
	if _, err := driver.fixture.rotate(ctx, driver.client, driver.cfg.apiB, driver.cfg.token, nil, oldIDs); err != nil {
		return observation, err
	}
	for i, api := range []string{driver.cfg.apiA, driver.cfg.apiB} {
		if err := driver.issue(ctx, api, next, false, false); err != nil {
			return observation, err
		}
		observation.AuthorizedIssuance++
		if err := driver.issue(ctx, api, next, true, true); err != nil {
			return observation, err
		}
		observation.WrongSubjectDenied++
		if err := driver.issue(ctx, api, driver.fixture.Keys[i], false, true); err != nil {
			return observation, err
		}
		observation.RetiredKeyDenied++
	}
	before, err = driver.samples(ctx)
	if err != nil {
		return observation, err
	}
	if _, _, err := driver.recover(ctx, before, time.Now(), false); err != nil {
		return observation, err
	}
	if err := waitSecretCapacityUntil(ctx, started.Add(45*time.Minute)); err != nil {
		return observation, err
	}
	before, err = driver.samples(ctx)
	if err != nil {
		return observation, err
	}
	restarted := time.Now()
	observation.RestartAt, observation.RestartTargetID = restarted.Sub(started), before[0].ID
	observation.RestartBefore = before[0]
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		return observation, errors.New("secret capacity: cannot locate root chaos command")
	}
	restartCtx, cancel := context.WithDeadline(ctx, restarted.Add(time.Minute))
	defer cancel()
	_, err = secretCapacityOwnedCommand(restartCtx, "make", "-C", repoRoot, "--no-print-directory", "chaos",
		"CHAOS_PROFILE=controller-restart", "CHAOS_TARGET=gitstore-capacity-controller-manager-a", "CHAOS_CONFIRM=1",
		"CHAOS_RUN_ID="+driver.fixture.RunID, "CHAOS_EVIDENCE_DIR="+filepath.Join(driver.evidence, "secret", "chaos"))
	if err != nil {
		return observation, err
	}
	observation.RestartConfirmed = true
	after, times, err := driver.recover(ctx, before, restarted, true)
	if err != nil {
		return observation, err
	}
	observation.Replacement, observation.ReplacementRecovery = after[0], times[0]
	if observation.OutageAt/time.Minute != 15 || observation.RotationAt/time.Minute != 30 ||
		observation.RestartAt/time.Minute != 45 {
		return observation, errors.New("secret capacity: scheduled fault missed its required minute")
	}
	return observation, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func prepareSecretCapacityFaultDriver(t *testing.T, cfg repositoryCapacityConfig) *secretCapacityFaultDriver {
	t.Helper()
	require.Equal(t, "1", os.Getenv("CHAOS_CONFIRM"), "secret scenario requires explicit CHAOS_CONFIRM=1")
	require.Equal(t, time.Hour, cfg.duration, "the complete secret fault schedule requires the one-hour offered load")
	require.False(t, cfg.skipReplacement, "secret capacity requires real API replacement")
	root, runID, err := secretCapacityFixturePath()
	require.NoError(t, err)
	fixture, err := readSecretCapacityOwnedFixture(root, runID)
	require.NoError(t, err)
	require.NotEmpty(t, fixture.UID, "fixture enrollment must be complete")
	evidence := os.Getenv("CAPACITY_EVIDENCE_DIR")
	require.True(t, filepath.IsAbs(evidence))
	relative, err := filepath.Rel(evidence, root)
	require.NoError(t, err)
	require.True(t, relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)),
		"private fixture records must be outside evidence")
	lockPath := filepath.Join(root, "active-run")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err, "refuse concurrent fault drivers against the same owned fixture")
	require.NoError(t, lock.Close())
	t.Cleanup(func() { require.NoError(t, os.Remove(lockPath)) })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	require.NoError(t, validateSecretCapacityOwnedMounts(ctx, root, fixture.Project))
	client := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	driver := &secretCapacityFaultDriver{fixture: fixture, root: root, evidence: evidence, client: client, cfg: cfg}
	samples, err := driver.samples(ctx)
	require.NoError(t, err)
	for i, name := range []string{"gitstore-capacity-controller-manager-a", "gitstore-capacity-controller-manager-b"} {
		body, err := secretCapacityOwnedCommand(ctx, "docker", "exec", name, "wget", "-qO-", "http://127.0.0.1:5001/metrics")
		require.NoError(t, err)
		internal, err := parseSecretCapacityControllerMetrics(body)
		require.NoError(t, err)
		require.Equal(t, internal.ID, samples[i].ID, "HTTP endpoint must match the explicitly owned controller")
	}
	for i, api := range []string{cfg.apiA, cfg.apiB} {
		require.NoError(t, driver.issue(ctx, api, fixture.Keys[i], false, false), "verify server-clamped 60-second access TTL")
		driver.initialAuthorized++
	}
	return driver
}

type secretCapacityFaultResult struct {
	observations secretCapacityFaultObservations
	err          error
}

func startSecretCapacityFaults(t *testing.T, driver *secretCapacityFaultDriver, started time.Time) <-chan secretCapacityFaultResult {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	result := make(chan secretCapacityFaultResult, 1)
	go func() {
		defer close(done)
		observation, err := driver.run(ctx, started)
		result <- secretCapacityFaultResult{observation, err}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return result
}

const secretCapacityControllerBodyLimit = 2 * 1024 * 1024

type secretCapacityControllerSample struct {
	ID                string        `json:"id"`
	ObservedAt        time.Time     `json:"observedAt"`
	CredentialReady   bool          `json:"credentialReady"`
	Healthy           bool          `json:"healthy"`
	FreshTokens       int64         `json:"freshTokens"`
	FailedExchanges   int64         `json:"failedExchanges"`
	CanceledExchanges int64         `json:"canceledExchanges"`
	DeadlineExchanges int64         `json:"deadlineExchanges"`
	ExchangeInflight  int64         `json:"exchangeInflight"`
	ExchangePeak      int64         `json:"exchangePeak"`
	MaxRetry          time.Duration `json:"maxRetry"`
	Reconciliations   int64         `json:"reconciliations"`
	RecordNotFound    int64         `json:"recordNotFound"`
	CPUSeconds        float64       `json:"cpuSeconds"`
	RSS               int64         `json:"rss"`
	Goroutines        int64         `json:"goroutines"`
	GOMAXPROCS        int64         `json:"gomaxprocs"`
}

var secretCapacityControllerID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var errSecretCapacityControllerUnavailable = errors.New("secret capacity: controller observation endpoint unavailable")

func parseSecretCapacityControllerMetrics(body []byte) (secretCapacityControllerSample, error) {
	var sample secretCapacityControllerSample
	invalid := errors.New("secret capacity: missing, malformed or ambiguous controller metrics")
	if len(body) == 0 || len(body) > secretCapacityControllerBodyLimit {
		return sample, invalid
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	if err != nil {
		return sample, invalid
	}
	types := map[string]dto.MetricType{
		"gitstore_controller_process_instance_info":             dto.MetricType_GAUGE,
		"gitstore_controller_credential_exchange_total":         dto.MetricType_COUNTER,
		"gitstore_controller_credential_exchange_inflight":      dto.MetricType_GAUGE,
		"gitstore_controller_credential_exchange_peak_inflight": dto.MetricType_GAUGE,
		"gitstore_controller_credential_retry_max_seconds":      dto.MetricType_GAUGE,
		"gitstore_controller_reconcile_total":                   dto.MetricType_COUNTER,
		"gitstore_secret_resolution_total":                      dto.MetricType_COUNTER,
		"process_cpu_seconds_total":                             dto.MetricType_COUNTER,
		"process_resident_memory_bytes":                         dto.MetricType_GAUGE,
		"go_goroutines":                                         dto.MetricType_GAUGE,
		"go_sched_gomaxprocs_threads":                           dto.MetricType_GAUGE,
	}
	for name, want := range types {
		family := families[name]
		if family == nil {
			if name == "gitstore_controller_reconcile_total" {
				continue // CounterVec children do not exist until the first reconciliation.
			}
			return sample, invalid
		}
		if family.Type == nil || family.GetType() != want {
			return sample, invalid
		}
		if name != "gitstore_controller_process_instance_info" && name != "gitstore_controller_credential_exchange_total" &&
			name != "gitstore_controller_reconcile_total" && name != "gitstore_secret_resolution_total" &&
			(len(family.Metric) != 1 || len(family.Metric[0].Label) != 0) {
			return sample, invalid
		}
		seen := make(map[string]bool)
		for _, metric := range family.Metric {
			if metric.TimestampMs != nil {
				return sample, invalid
			}
			labels := make(map[string]bool, len(metric.Label))
			signature := make([]string, 0, len(metric.Label))
			for _, label := range metric.Label {
				if label.Name == nil || label.Value == nil || labels[label.GetName()] {
					return sample, invalid
				}
				labels[label.GetName()] = true
				signature = append(signature, strconv.Quote(label.GetName())+"="+strconv.Quote(label.GetValue()))
			}
			sort.Strings(signature)
			key := strings.Join(signature, ",")
			if seen[key] {
				return sample, invalid
			}
			seen[key] = true
			value := metric.GetGauge().GetValue()
			if want == dto.MetricType_COUNTER {
				value = metric.GetCounter().GetValue()
			}
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return sample, invalid
			}
		}
	}
	var fieldErr error
	value := func(name string, labels map[string]string, optional bool) float64 {
		for _, metric := range families[name].GetMetric() {
			if len(metric.Label) != len(labels) {
				continue
			}
			match := true
			for _, label := range metric.Label {
				expected, exists := labels[label.GetName()]
				match = match && exists && expected == label.GetValue()
			}
			if match {
				if types[name] == dto.MetricType_COUNTER {
					return metric.GetCounter().GetValue()
				}
				return metric.GetGauge().GetValue()
			}
		}
		if !optional {
			fieldErr = invalid
		}
		return 0
	}
	integer := func(name string, labels map[string]string, optional bool) int64 {
		v := value(name, labels, optional)
		if v > 1<<53-1 || math.Trunc(v) != v {
			fieldErr = invalid
			return 0
		}
		return int64(v)
	}
	identity := families["gitstore_controller_process_instance_info"].Metric
	if len(identity) != 1 || len(identity[0].Label) != 1 ||
		identity[0].Label[0].GetName() != "instance_id" || identity[0].GetGauge().GetValue() != 1 {
		return sample, invalid
	}
	sample.ID = identity[0].Label[0].GetValue()
	if !secretCapacityControllerID.MatchString(sample.ID) {
		return secretCapacityControllerSample{}, invalid
	}
	if len(families["gitstore_controller_credential_exchange_total"].Metric) != 4 {
		return secretCapacityControllerSample{}, invalid
	}
	for outcome, target := range map[string]*int64{
		"success": &sample.FreshTokens, "failed": &sample.FailedExchanges,
		"canceled": &sample.CanceledExchanges, "deadline_exceeded": &sample.DeadlineExchanges,
	} {
		*target = integer("gitstore_controller_credential_exchange_total", map[string]string{"result": outcome}, false)
	}
	sample.ExchangeInflight = integer("gitstore_controller_credential_exchange_inflight", nil, false)
	sample.ExchangePeak = integer("gitstore_controller_credential_exchange_peak_inflight", nil, false)
	retry := value("gitstore_controller_credential_retry_max_seconds", nil, false)
	if retry > 30 || sample.ExchangePeak > 1 || sample.ExchangeInflight > sample.ExchangePeak {
		return secretCapacityControllerSample{}, errors.New("secret capacity: controller exchange bounds exceeded")
	}
	sample.MaxRetry = time.Duration(math.Ceil(retry * float64(time.Second)))
	sample.Reconciliations = integer("gitstore_controller_reconcile_total",
		map[string]string{"kind": "Repository", "result": "success"}, true)
	providerLabels := map[string]string{
		"consumer": "controller-manager", "purpose": "identity", "tier": "bootstrap", "provider": "file", "reason": "success",
	}
	integer("gitstore_secret_resolution_total", providerLabels, false)
	providerLabels["reason"] = "NotFound"
	sample.RecordNotFound = integer("gitstore_secret_resolution_total", providerLabels, true)
	sample.CPUSeconds = value("process_cpu_seconds_total", nil, false)
	sample.RSS = integer("process_resident_memory_bytes", nil, false)
	sample.Goroutines = integer("go_goroutines", nil, false)
	sample.GOMAXPROCS = integer("go_sched_gomaxprocs_threads", nil, false)
	if fieldErr != nil || sample.RSS == 0 || sample.Goroutines == 0 || sample.GOMAXPROCS == 0 {
		return secretCapacityControllerSample{}, invalid
	}
	return sample, nil
}

// Bracket health with process-identified scrapes so a restart cannot attach
// the old process's credential readiness to the replacement's counters.
func sampleSecretCapacityController(ctx context.Context, client *http.Client, endpoint string) (secretCapacityControllerSample, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var empty secretCapacityControllerSample
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || client == nil {
		return empty, errors.New("secret capacity: invalid controller endpoint")
	}
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	read := func(path string, health bool) ([]byte, int, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(endpoint, "/")+path, nil)
		if err != nil {
			return nil, 0, errors.New("secret capacity: cannot create controller observation request")
		}
		response, err := boundedClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			return nil, 0, errSecretCapacityControllerUnavailable
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK && !(health && response.StatusCode == http.StatusServiceUnavailable) {
			if response.StatusCode == http.StatusServiceUnavailable {
				return nil, 0, errSecretCapacityControllerUnavailable
			}
			return nil, 0, errors.New("secret capacity: unexpected controller observation status")
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, secretCapacityControllerBodyLimit+1))
		if err != nil || len(body) > secretCapacityControllerBodyLimit {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			return nil, 0, errors.New("secret capacity: invalid controller observation body")
		}
		return body, response.StatusCode, nil
	}
	scrape := func() (secretCapacityControllerSample, error) {
		body, _, err := read("/metrics", false)
		if err != nil {
			return empty, err
		}
		return parseSecretCapacityControllerMetrics(body)
	}
	before, err := scrape()
	if err != nil {
		return empty, err
	}
	body, status, err := read("/health", true)
	if err != nil {
		return empty, err
	}
	observedAt := time.Now().UTC()
	var health struct {
		Status          string `json:"status"`
		Version         string `json:"version"`
		CredentialReady bool   `json:"credentialReady"`
		Kinds           map[string]struct {
			ActiveWorkers int64 `json:"activeWorkers"`
			QueueDepth    int64 `json:"queueDepth"`
			PoisonItems   int64 `json:"poisonItems"`
			Stalled       bool  `json:"stalled"`
			Registered    bool  `json:"registered"`
		} `json:"kinds"`
	}
	if decodeSecretCapacityJSON(body, &health) != nil || !health.Kinds["Repository"].Registered ||
		(status == http.StatusOK && (health.Status != "ok" || !health.CredentialReady)) ||
		(status == http.StatusServiceUnavailable && health.Status != "degraded") {
		return empty, errors.New("secret capacity: incomplete or inconsistent controller readiness")
	}
	for _, kind := range health.Kinds {
		if kind.ActiveWorkers < 0 || kind.QueueDepth < 0 || kind.PoisonItems < 0 ||
			(status == http.StatusOK && (kind.Stalled || kind.PoisonItems > 0)) {
			return empty, errors.New("secret capacity: inconsistent controller kind health")
		}
	}
	after, err := scrape()
	if err != nil {
		return empty, err
	}
	if before.ID != after.ID || !secretCapacityControllerCountersAdvance(before, after) {
		return empty, errors.New("secret capacity: controller changed during readiness observation")
	}
	// Use the first scrape's counters: all attributed progress precedes the
	// readiness observation, rather than occurring after that health response.
	before.ObservedAt = observedAt
	before.CredentialReady = health.CredentialReady
	before.Healthy = status == http.StatusOK
	return before, nil
}

func secretCapacityControllerCountersAdvance(before, after secretCapacityControllerSample) bool {
	return after.FreshTokens >= before.FreshTokens && after.FailedExchanges >= before.FailedExchanges &&
		after.CanceledExchanges >= before.CanceledExchanges && after.DeadlineExchanges >= before.DeadlineExchanges &&
		after.Reconciliations >= before.Reconciliations && after.RecordNotFound >= before.RecordNotFound &&
		after.ExchangePeak >= before.ExchangePeak && after.MaxRetry >= before.MaxRetry && after.CPUSeconds >= before.CPUSeconds
}

func secretCapacityControllerProgress(before, after secretCapacityControllerSample, replacement bool) (bool, error) {
	if before.ID == "" || after.ID == "" || !after.ObservedAt.After(before.ObservedAt) ||
		after.MaxRetry < 0 || after.MaxRetry > 30*time.Second || after.ExchangePeak < 0 || after.ExchangePeak > 1 {
		return false, errors.New("secret capacity: invalid controller progress observation")
	}
	if before.ID != after.ID {
		if !replacement {
			return false, errors.New("secret capacity: unplanned controller replacement")
		}
		return after.CredentialReady && after.Healthy && after.FreshTokens > 0 && after.Reconciliations > 0, nil
	}
	if !secretCapacityControllerCountersAdvance(before, after) {
		return false, errors.New("secret capacity: controller counters regressed without replacement")
	}
	return !replacement && after.CredentialReady && after.Healthy &&
		after.FreshTokens > before.FreshTokens && after.Reconciliations > before.Reconciliations, nil
}

const secretCapacityControllerTestID = "controller-instance-000001"

func secretCapacityControllerMetricsFixture(id string, tokens, reconciles int) string {
	return fmt.Sprintf(`# TYPE gitstore_controller_process_instance_info gauge
gitstore_controller_process_instance_info{instance_id="%s"} 1
# TYPE gitstore_controller_credential_exchange_total counter
gitstore_controller_credential_exchange_total{result="success"} %d
gitstore_controller_credential_exchange_total{result="failed"} 0
gitstore_controller_credential_exchange_total{result="canceled"} 0
gitstore_controller_credential_exchange_total{result="deadline_exceeded"} 0
# TYPE gitstore_controller_credential_exchange_inflight gauge
gitstore_controller_credential_exchange_inflight 0
# TYPE gitstore_controller_credential_exchange_peak_inflight gauge
gitstore_controller_credential_exchange_peak_inflight 1
# TYPE gitstore_controller_credential_retry_max_seconds gauge
gitstore_controller_credential_retry_max_seconds 0
# TYPE gitstore_controller_reconcile_total counter
gitstore_controller_reconcile_total{kind="Repository",result="success"} %d
gitstore_controller_reconcile_total{kind="Product",result="success"} 1000
# TYPE gitstore_secret_resolution_total counter
gitstore_secret_resolution_total{consumer="controller-manager",purpose="identity",tier="bootstrap",provider="file",reason="success"} 1
# TYPE process_cpu_seconds_total counter
process_cpu_seconds_total 10.25
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 10485760
# TYPE go_goroutines gauge
go_goroutines 12
# TYPE go_sched_gomaxprocs_threads gauge
go_sched_gomaxprocs_threads 2
`, id, tokens, reconciles)
}

func TestSecretCapacityControllerMetricsAreStrict(t *testing.T) {
	valid := secretCapacityControllerMetricsFixture(secretCapacityControllerTestID, 2, 3)
	sample, err := parseSecretCapacityControllerMetrics([]byte(valid))
	require.NoError(t, err)
	require.Equal(t, secretCapacityControllerTestID, sample.ID)
	require.EqualValues(t, 2, sample.FreshTokens)
	require.EqualValues(t, 3, sample.Reconciliations, "other resource kinds cannot prove Repository progress")
	require.Zero(t, sample.RecordNotFound, "unobserved lazy failure series starts at zero")
	require.Equal(t, 10.25, sample.CPUSeconds)
	require.EqualValues(t, 10485760, sample.RSS)
	require.EqualValues(t, 12, sample.Goroutines)
	for name, change := range map[string]func(string) string{
		"missing exchange outcomes": func(s string) string {
			return strings.ReplaceAll(s, `gitstore_controller_credential_exchange_total{result="failed"} 0`+"\n", "")
		},
		"missing memory": func(s string) string {
			return strings.ReplaceAll(s, "process_resident_memory_bytes 10485760\n", "")
		},
		"duplicate sample": func(s string) string {
			return s + "gitstore_controller_credential_exchange_peak_inflight 1\n"
		},
		"duplicate identity": func(s string) string {
			return s + `gitstore_controller_process_instance_info{instance_id="controller-instance-000002"} 1` + "\n"
		},
		"duplicate label": func(s string) string {
			return strings.ReplaceAll(s, `result="failed"`, `result="failed",result="failed"`)
		},
		"counter reset corruption": func(s string) string {
			return strings.ReplaceAll(s, `result="success"} 2`, `result="success"} -1`)
		},
		"fractional counter": func(s string) string {
			return strings.ReplaceAll(s, `result="success"} 2`, `result="success"} 2.5`)
		},
		"imprecise counter": func(s string) string {
			return strings.ReplaceAll(s, `result="success"} 2`, `result="success"} 9007199254740992`)
		},
		"nan":      func(s string) string { return strings.ReplaceAll(s, "10485760", "NaN") },
		"infinity": func(s string) string { return strings.ReplaceAll(s, "10.25", "+Inf") },
		"wrong type": func(s string) string {
			return strings.ReplaceAll(s, "TYPE gitstore_controller_credential_exchange_total counter",
				"TYPE gitstore_controller_credential_exchange_total gauge")
		},
		"unclassified result": func(s string) string {
			return s + `gitstore_controller_credential_exchange_total{result="unexpected"} 1` + "\n"
		},
		"extra labels": func(s string) string {
			return strings.ReplaceAll(s, `result="failed"`, `result="failed",key="private-marker"`)
		},
		"unsafe identity": func(s string) string { return strings.ReplaceAll(s, secretCapacityControllerTestID, "private marker") },
		"stale timestamp": func(s string) string {
			return strings.ReplaceAll(s, "process_cpu_seconds_total 10.25", "process_cpu_seconds_total 10.25 1000")
		},
		"oversized": func(s string) string { return s + strings.Repeat("# padding\n", 250_000) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseSecretCapacityControllerMetrics([]byte(change(valid)))
			require.Error(t, err)
			require.NotContains(t, err.Error(), "private-marker")
		})
	}
}

func TestSecretCapacityControllerSamplingBracketsReadiness(t *testing.T) {
	var mode atomic.Int32
	var scrapes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			id := secretCapacityControllerTestID
			if mode.Load() == 2 && scrapes.Add(1) > 1 {
				id = "controller-instance-000002"
			}
			_, _ = fmt.Fprint(w, secretCapacityControllerMetricsFixture(id, 2, 3))
			return
		}
		if mode.Load() == 3 {
			_, _ = fmt.Fprint(w, `{"status":"ok","credentialReady":true}`)
			return
		}
		ready, status := true, "ok"
		if mode.Load() == 1 {
			ready, status = false, "degraded"
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_, _ = fmt.Fprintf(w, `{"status":%q,"version":"test","credentialReady":%t,
			"kinds":{"Repository":{"registered":true,"stalled":false,"activeWorkers":0,"queueDepth":0,"poisonItems":0}}}`, status, ready)
	}))
	defer server.Close()
	sample, err := sampleSecretCapacityController(t.Context(), server.Client(), server.URL)
	require.NoError(t, err)
	require.True(t, sample.CredentialReady)
	require.True(t, sample.Healthy)
	require.False(t, sample.ObservedAt.IsZero())
	mode.Store(1)
	sample, err = sampleSecretCapacityController(t.Context(), server.Client(), server.URL)
	require.NoError(t, err, "503 with explicit exhausted credentials is observable, not a transport failure")
	require.False(t, sample.CredentialReady)
	require.False(t, sample.Healthy)
	mode.Store(2)
	_, err = sampleSecretCapacityController(t.Context(), server.Client(), server.URL)
	require.Error(t, err, "readiness cannot be attributed across a process replacement")
	mode.Store(3)
	_, err = sampleSecretCapacityController(t.Context(), server.Client(), server.URL)
	require.Error(t, err, "missing health fields cannot silently become healthy defaults")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = sampleSecretCapacityController(ctx, server.Client(), server.URL)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSecretCapacityControllerProgressRequiresFreshAuthentication(t *testing.T) {
	before, err := parseSecretCapacityControllerMetrics([]byte(secretCapacityControllerMetricsFixture(secretCapacityControllerTestID, 2, 3)))
	require.NoError(t, err)
	before.ObservedAt = time.Now()
	after := before
	after.ObservedAt = before.ObservedAt.Add(time.Second)
	after.CredentialReady, after.Healthy = true, true
	after.Reconciliations++
	progress, err := secretCapacityControllerProgress(before, after, false)
	require.NoError(t, err)
	require.False(t, progress, "reconciliation alone cannot prove token issuance")
	after.FreshTokens++
	progress, err = secretCapacityControllerProgress(before, after, false)
	require.NoError(t, err)
	require.True(t, progress)
	for name, change := range map[string]func(*secretCapacityControllerSample){
		"unexpected replacement": func(s *secretCapacityControllerSample) { s.ID = "controller-instance-000002" },
		"counter reset":          func(s *secretCapacityControllerSample) { s.FreshTokens = 1 },
		"retry overflow":         func(s *secretCapacityControllerSample) { s.MaxRetry = 30*time.Second + time.Nanosecond },
		"concurrent exchanges":   func(s *secretCapacityControllerSample) { s.ExchangePeak = 2 },
		"backward time":          func(s *secretCapacityControllerSample) { s.ObservedAt = before.ObservedAt },
	} {
		t.Run(name, func(t *testing.T) {
			bad := after
			change(&bad)
			_, err := secretCapacityControllerProgress(before, bad, false)
			require.Error(t, err)
		})
	}

	progress, err = secretCapacityControllerProgress(before, after, true)
	require.NoError(t, err)
	require.False(t, progress, "a restart command without a changed process cannot prove recovery")
	after.ID = "controller-instance-000002"
	after.FreshTokens, after.Reconciliations = 1, 1
	progress, err = secretCapacityControllerProgress(before, after, true)
	require.NoError(t, err)
	require.True(t, progress, "replacement counters are process-local, not deltas from the old process")
}

func TestSecretCapacityControllerLifecycleSnapshots(t *testing.T) {
	server := func(id string) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/metrics" {
				_, _ = fmt.Fprint(w, secretCapacityControllerMetricsFixture(id, 2, 3))
				return
			}
			_, _ = fmt.Fprint(w, `{"status":"ok","version":"test","credentialReady":true,
					"kinds":{"Repository":{"registered":true,"stalled":false,"activeWorkers":0,"queueDepth":0,"poisonItems":0}}}`)
		}))
		t.Cleanup(s.Close)
		return s
	}
	a, b := server(secretCapacityControllerTestID), server("controller-instance-000002")
	root := t.TempDir()
	t.Setenv("CAPACITY_EVIDENCE_DIR", root)
	t.Setenv("CAPACITY_RUN_ID", "controller-observer-test")
	samples := recordSecretCapacityControllers(t, a.Client(), repositoryCapacityConfig{
		controllerA: a.URL, controllerB: b.URL, mode: capacityModeDiagnostic,
	}, "controllers-before-load.json")
	require.NotEqual(t, samples[0].ID, samples[1].ID)
	body, err := os.ReadFile(filepath.Join(root, "secret", "controllers-before-load.json"))
	require.NoError(t, err)
	var observation map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &observation))
	require.NotContains(t, observation, "passed")
	require.JSONEq(t, `"controller-observer-test"`, string(observation["runID"]))
	require.NoError(t, scanSecretCapacityArtifacts(root, nil))
	require.NotContains(t, string(body), "/metrics")
	require.NotContains(t, string(body), "gitstore_controller_credential_exchange_total")
}

func TestSecretCapacityControllerHealthRejectsMalformedResponses(t *testing.T) {
	valid := `{"status":"ok","version":"test","credentialReady":true,
			"kinds":{"Repository":{"registered":true,"stalled":false,"activeWorkers":0,"queueDepth":0,"poisonItems":0}}}`
	for name, body := range map[string]string{
		"duplicate kind":    strings.Replace(valid, `"kinds":{`, `"kinds":{"Repository":{},`, 1),
		"missing readiness": strings.Replace(valid, `"credentialReady":true,`, "", 1),
		"null readiness":    strings.Replace(valid, `"credentialReady":true`, `"credentialReady":null`, 1),
		"unregistered":      strings.Replace(valid, `"registered":true`, `"registered":false`, 1),
		"missing queue":     strings.Replace(valid, `"queueDepth":0,`, "", 1),
		"negative queue":    strings.Replace(valid, `"queueDepth":0`, `"queueDepth":-1`, 1),
		"poisoned but ok":   strings.Replace(valid, `"poisonItems":0`, `"poisonItems":1`, 1),
		"stalled but ok":    strings.Replace(valid, `"stalled":false`, `"stalled":true`, 1),
		"trailing body":     valid + "{}",
	} {
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/metrics" {
					_, _ = fmt.Fprint(w, secretCapacityControllerMetricsFixture(secretCapacityControllerTestID, 2, 3))
					return
				}
				_, _ = fmt.Fprint(w, body)
			}))
			defer s.Close()
			_, err := sampleSecretCapacityController(t.Context(), s.Client(), s.URL)
			require.Error(t, err)
			require.NotContains(t, err.Error(), body)
		})
	}
}

func TestSecretCapacityOwnedFixtureRecordsAndAssertions(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	fixture := secretCapacityOwnedFixture{SchemaVersion: 1, RunID: "fixture-test",
		Project: "gitstore-owned-test", Namespace: "controllers", Name: "gitstore-controller-manager", UID: "owned-uid"}
	fixture.RuntimeMarkers = []string{}
	for range 3 {
		key, err := newSecretCapacityOwnedKey()
		require.NoError(t, err)
		fixture.Keys = append(fixture.Keys, key)
	}
	path := filepath.Join(root, "owned-fixture.json")
	require.NoError(t, writeSecretCapacityPrivateJSON(path, fixture))
	loaded, err := readSecretCapacityOwnedFixture(root, fixture.RunID)
	require.NoError(t, err)
	require.Equal(t, fixture.UID, loaded.UID)
	_, err = readSecretCapacityOwnedFixture(root, "another-run")
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = readSecretCapacityOwnedFixture(root, fixture.RunID)
	require.Error(t, err, "private fixture records must not be world-readable")
	require.NoError(t, os.Chmod(path, 0600))
	record := filepath.Join(root, "controller.json")
	require.NoError(t, writeSecretCapacitySigningRecord(record, fixture.Keys[0]))
	before, err := os.ReadFile(record)
	require.NoError(t, err)
	require.NoError(t, writeSecretCapacitySigningRecord(record, fixture.Keys[2]))
	after, err := os.ReadFile(record)
	require.NoError(t, err)
	require.NotEqual(t, string(before), string(after))
	var wire struct {
		Format string            `json:"format"`
		Values map[string][]byte `json:"values"`
	}
	require.NoError(t, json.Unmarshal(after, &wire))
	require.Equal(t, "serviceaccount-signing-key/v1", wire.Format)
	require.True(t, string(wire.Values["privateKey"]) == fixture.Keys[2].Private)
	require.Equal(t, fixture.Keys[2].ID, string(wire.Values["keyID"]))
	assertion, err := fixture.assertion(fixture.Keys[2], fixture.Name)
	require.NoError(t, err)
	block, _ := pem.Decode([]byte(fixture.Keys[2].Public))
	public, err := x509.ParsePKIXPublicKey(block.Bytes)
	require.NoError(t, err)
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(assertion, claims, func(*jwt.Token) (any, error) { return public, nil },
		jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithAudience("gitstore-api/serviceaccount-token"))
	require.True(t, err == nil && parsed.Valid, "owned assertion must verify against its enrolled public key")
	require.Equal(t, "gitstore-sa-assertion+jwt", parsed.Header["typ"])
	require.Equal(t, fixture.Keys[2].ID, parsed.Header["kid"])
	require.Equal(t, float64(45), claims["exp"].(float64)-claims["iat"].(float64))
	second, err := fixture.assertion(fixture.Keys[2], fixture.Name)
	require.NoError(t, err)
	require.True(t, assertion != second, "every assertion must have a fresh replay identifier")
	_, err = fixture.assertion(secretCapacityOwnedKey{Private: "private-marker"}, fixture.Name)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-marker")
}

func secretCapacityFaultTestController(t *testing.T, id string, tokens, reconciles int, onRequest func(*http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onRequest != nil {
			onRequest(r)
		}
		if r.URL.Path == "/metrics" {
			_, _ = fmt.Fprint(w, secretCapacityControllerMetricsFixture(id, tokens, reconciles))
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"ok","version":"test","credentialReady":true,
			"kinds":{"Repository":{"registered":true,"stalled":false,"activeWorkers":0,"queueDepth":0,"poisonItems":0}}}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestSecretCapacityFaultCancellationRestoresOnlyOwnedRecord(t *testing.T) {
	root, evidence := t.TempDir(), t.TempDir()
	provider := filepath.Join(root, "controller-a", "provider")
	require.NoError(t, os.MkdirAll(provider, 0700))
	record := filepath.Join(provider, "controller.json")
	require.NoError(t, os.WriteFile(record, []byte("test-owned-record"), 0600))
	peerRecord := filepath.Join(root, "peer-record")
	require.NoError(t, os.WriteFile(peerRecord, []byte("unchanged-peer-record"), 0600))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	a := secretCapacityFaultTestController(t, secretCapacityControllerTestID, 2, 3, func(*http.Request) {
		if _, err := os.Stat(record); os.IsNotExist(err) {
			cancel()
		}
	})
	b := secretCapacityFaultTestController(t, "controller-instance-000002", 2, 3, nil)
	driver := &secretCapacityFaultDriver{
		root: root, evidence: evidence, client: a.Client(), fixture: secretCapacityOwnedFixture{RunID: "cancel-test"},
		cfg: repositoryCapacityConfig{controllerA: a.URL, controllerB: b.URL},
	}
	result, err := driver.run(ctx, time.Now().Add(-15*time.Minute))
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, result.Completed)
	body, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, "test-owned-record", string(body))
	peer, err := os.ReadFile(peerRecord)
	require.NoError(t, err)
	require.Equal(t, "unchanged-peer-record", string(peer))
	_, err = os.Stat(record + ".withdrawn")
	require.True(t, os.IsNotExist(err))
	body, err = os.ReadFile(filepath.Join(evidence, "secret", "faults.json"))
	require.NoError(t, err)
	require.NotContains(t, string(body), "test-owned-record")
	require.Contains(t, string(body), `"completed":false`)
	require.NoError(t, scanSecretCapacityArtifacts(evidence, nil))
}

func TestSecretCapacityFaultRecoveryRequiresBothProcesses(t *testing.T) {
	before := [2]secretCapacityControllerSample{
		{ID: secretCapacityControllerTestID, ObservedAt: time.Now().Add(-time.Second), FreshTokens: 2, Reconciliations: 3},
		{ID: "controller-instance-000002", ObservedAt: time.Now().Add(-time.Second), FreshTokens: 2, Reconciliations: 3},
	}
	a := secretCapacityFaultTestController(t, "controller-instance-000003", 1, 1, nil)
	b := secretCapacityFaultTestController(t, before[1].ID, 3, 4, nil)
	driver := &secretCapacityFaultDriver{client: a.Client(), cfg: repositoryCapacityConfig{controllerA: a.URL, controllerB: b.URL}}
	after, elapsed, err := driver.recover(t.Context(), before, time.Now(), true)
	require.NoError(t, err)
	require.NotEqual(t, before[0].ID, after[0].ID)
	require.Positive(t, elapsed[0])
	require.Positive(t, elapsed[1])
	stale := secretCapacityFaultTestController(t, before[1].ID, 2, 4, nil)
	driver.cfg.controllerB = stale.URL
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, _, err = driver.recover(ctx, before, time.Now(), true)
	require.Error(t, err, "peer reconciliation without a new token cannot prove recovery")
}

func TestSecretCapacityIssuanceRequiresClampedTTL(t *testing.T) {
	key, err := newSecretCapacityOwnedKey()
	require.NoError(t, err)
	block, _ := pem.Decode([]byte(key.Private))
	private, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	require.NoError(t, err)
	fixture := secretCapacityOwnedFixture{Namespace: "controllers", Name: "gitstore-controller-manager", UID: "owned-uid"}
	for _, ttl := range []time.Duration{time.Minute, 2 * time.Minute} {
		t.Run(ttl.String(), func(t *testing.T) {
			now := time.Now().Truncate(time.Second).Add(123 * time.Millisecond)
			expiry := now.Add(ttl)
			token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, jwt.RegisteredClaims{
				Subject:  "serviceaccount:controllers:gitstore-controller-manager",
				IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(expiry),
			})
			signed, err := token.SignedString(private)
			require.NoError(t, err)
			body, err := json.Marshal(map[string]any{"data": map[string]any{"issueServiceAccountToken": map[string]any{
				"tokenRequest": map[string]any{"status": map[string]any{"token": signed, "expirationTimestamp": expiry}},
			}}})
			require.NoError(t, err)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(body)
			}))
			defer server.Close()
			driver := &secretCapacityFaultDriver{fixture: fixture, client: server.Client()}
			err = driver.issue(t.Context(), server.URL, key, false, false)
			if ttl == time.Minute {
				require.NoError(t, err, "API timestamps retain fractions while JWT NumericDate uses whole seconds")
			} else {
				require.Error(t, err, "unclamped tokens cannot prove the expiry-spanning fault window")
			}
		})
	}
}

func TestSecretCapacityIssuanceDoesNotCountTransportErrorsAsDenials(t *testing.T) {
	key, err := newSecretCapacityOwnedKey()
	require.NoError(t, err)
	fixture := secretCapacityOwnedFixture{Namespace: "controllers", Name: "gitstore-controller-manager", UID: "owned-uid"}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError, http.StatusTemporaryRedirect} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))
				w.WriteHeader(status)
			}))
			defer server.Close()
			driver := &secretCapacityFaultDriver{fixture: fixture, client: server.Client()}
			err := driver.issue(t.Context(), server.URL, key, true, true)
			if status == 401 || status == 403 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
