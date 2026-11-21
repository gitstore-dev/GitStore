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
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestOwnedUserDirectoryRollingUpgrade(t *testing.T) {
	r := startDirectoryTestRuntime(t, true)
	const baseline = "e9cbe2d7b22d9ca62be52bde64f20fa891a625a3"
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	source := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, args[0], args[1:]...)
		command.Dir = dir
		command.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
		output, err := command.CombinedOutput()
		require.NoError(t, err, "build exact rollout baseline: %s", output)
	}
	archive := filepath.Join(t.TempDir(), "baseline.tar")
	run(root, "git", "archive", "--format=tar", "--output="+archive, baseline)
	run(source, "tar", "-xf", archive)
	r.initialAPIBinary = filepath.Join(t.TempDir(), "baseline-api")
	run(filepath.Join(source, "gitstore-api"), "go", "build", "-o", r.initialAPIBinary, "./cmd/server")
	operator := r.createHuman(t, "staff", "operator", "operator@example.test")
	customer := r.createHuman(t, "customer", "customer", "customer@example.test")
	status, _ := r.request(t, http.MethodPatch, "/admin/identities/"+operator.ID, []map[string]any{
		{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
			{"value": operator.Traits["email"], "verified": true, "via": "email", "status": "completed"},
		}},
		{"op": "add", "path": "/metadata_admin/setup_complete", "value": true},
	})
	require.Equal(t, http.StatusOK, status)
	apis, token := r.startAPIs(t, operator.ID, true)
	operatorToken := token(operator.ID)
	started := func(name string) string {
		return strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", name))
	}
	gitName := "gitstore-userdir-git-" + r.owner[:12]
	gitStarted, secondStarted := started(gitName), started(apis[1].name)
	scylla := "gitstore-userdir-scylla-" + r.owner[:12]
	history := func() string {
		return r.command(t, nil, "exec", scylla, "cqlsh", "-e", "SELECT name FROM gitstore.gocqlx_migrate;")
	}
	require.NotContains(t, history(), "013_service_account_cdc.cql")
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	key := secretCapacityOwnedKey{ID: "owned-rollout",
		Public:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		Private: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))}
	create := fmt.Sprintf(`mutation{createServiceAccount(input:{metadata:{name:"owned-ci-bot"},
		publicKeys:[{kid:%q,algorithm:"Ed25519",publicKeyPEM:%q}]}){serviceAccount{metadata{uid}}}}`, key.ID, key.Public)
	result := r.graphql(t, apis[0], operatorToken, create)
	require.Empty(t, result.Errors)
	var created struct {
		ServiceAccount struct {
			Metadata struct{ UID string } `json:"metadata"`
		} `json:"serviceAccount"`
	}
	require.NoError(t, json.Unmarshal(result.Data["createServiceAccount"], &created))
	uid := created.ServiceAccount.Metadata.UID
	require.NotEmpty(t, uid)
	fixture := secretCapacityOwnedFixture{Name: "owned-ci-bot", UID: uid}
	issue := `mutation{issueServiceAccountToken(input:{metadata:{name:"owned-ci-bot"},
		spec:{expirationSeconds:300}}){tokenRequest{status{token}}}}`
	issueWithAssertion := func(api directoryAPI) directoryGraphQLResponse {
		t.Helper()
		assertion, err := fixture.assertion(key, fixture.Name)
		require.NoError(t, err)
		return r.graphql(t, api, assertion, issue)
	}
	for _, api := range apis {
		require.Empty(t, issueWithAssertion(api).Errors, "both old APIs must read the enrolled source")
	}
	issued := issueWithAssertion(apis[0])
	var credential struct {
		TokenRequest struct {
			Status struct{ Token string } `json:"status"`
		} `json:"tokenRequest"`
	}
	require.NoError(t, json.Unmarshal(issued.Data["issueServiceAccountToken"], &credential))
	workloadToken := credential.TokenRequest.Status.Token
	require.NotEmpty(t, workloadToken)
	restart := func(api directoryAPI) {
		t.Helper()
		require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
			`{{index .Config.Labels "dev.gitstore.test_owner"}}`, api.name)))
		r.command(t, nil, "restart", "--time", "5", api.name)
		require.Eventually(t, func() bool {
			response, err := r.client.Get(api.base + "/ready")
			if err != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, 30*time.Second, 100*time.Millisecond, "replacement must retain its endpoint and become ready")
	}
	trafficContext, cancelTraffic := context.WithCancel(t.Context())
	type trafficResult struct {
		requests int
		err      error
	}
	traffic := make(chan trafficResult, 1)
	trafficDone := make(chan struct{})
	go func() {
		defer close(trafficDone)
		result := trafficResult{}
		defer func() { traffic <- result }()
		for trafficContext.Err() == nil {
			assertion, err := fixture.assertion(key, fixture.Name)
			if err != nil {
				result.err = err
				return
			}
			body, _ := json.Marshal(map[string]string{"query": issue})
			request, err := http.NewRequestWithContext(trafficContext, http.MethodPost,
				apis[1].base+"/admin/graphql", bytes.NewReader(body))
			if err != nil {
				result.err = err
				return
			}
			request.Header.Set("Authorization", "Bearer "+assertion)
			request.Header.Set("Content-Type", "application/json")
			response, err := r.client.Do(request)
			if trafficContext.Err() != nil {
				if response != nil {
					response.Body.Close()
				}
				return
			}
			if err != nil {
				result.err = fmt.Errorf("old-replica request failed during replacement")
				return
			}
			var payload directoryGraphQLResponse
			err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || len(payload.Errors) != 0 {
				result.err = fmt.Errorf("old-replica admission failed during replacement")
				return
			}
			result.requests++
			select {
			case <-trafficContext.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(func() {
		cancelTraffic()
		select {
		case <-trafficDone:
		case <-time.After(3 * time.Second):
			t.Error("old-replica traffic did not stop")
		}
	})
	// Replacing the bind-mounted file leaves the other process on its old executable inode.
	require.NoError(t, os.Rename(r.currentAPIBinary, apis[0].binaryFile))
	restart(apis[0])
	cancelTraffic()
	continuous := <-traffic
	require.NoError(t, continuous.err)
	require.Positive(t, continuous.requests, "the old replica must actually serve requests during replacement")
	require.Contains(t, history(), "013_service_account_cdc.cql")
	require.Equal(t, secondStarted, started(apis[1].name), "the old replica must remain live through forward migration")
	for _, api := range apis {
		require.Empty(t, issueWithAssertion(api).Errors, "old/new replicas must retain source authority")
	}
	restart(apis[1])
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(apis[0].configFile), "policy.yaml"),
		[]byte(r.currentAPIPolicy), 0600))
	original, err := os.ReadFile(apis[0].configFile)
	require.NoError(t, err)
	switchProvider := func(api directoryAPI, provider string) {
		t.Helper()
		config := strings.Replace(string(original), `provider = "static-users"`, `provider = "`+provider+`"`, 1)
		require.NoError(t, os.WriteFile(api.configFile, []byte(config), 0600))
		restart(api)
	}
	switchProvider(apis[0], "kratos")
	id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + operator.ID))
	read := fmt.Sprintf(`{node(id:%q){id ... on User{metadata{name} groups}}}`, id)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, read).Errors)
	require.NotEmpty(t, r.graphql(t, apis[0], token(customer.ID), read).Errors)
	sourceID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/ServiceAccount/" + uid))
	sourceRead := fmt.Sprintf(`{node(id:%q){... on ServiceAccount{metadata{uid}}}}`, sourceID)
	sourceActor := r.graphql(t, apis[0], operatorToken, sourceRead)
	require.Empty(t, sourceActor.Errors)
	require.Contains(t, string(sourceActor.Data["node"]), uid)
	update := fmt.Sprintf(`mutation{updateUser(input:{id:%q,fullName:"Rolling operator"}){actor{id}}}`, id)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, update).Errors)
	setSource := func(disabled bool) {
		t.Helper()
		flag := "0"
		if disabled {
			flag = "1"
		}
		r.command(t, nil, "exec", "-e", "GITSTORE_TEST_OWNED_SCYLLA=1",
			"-e", "GITSTORE_TEST_OWNED_SOURCE_UID="+uid, "-e", "GITSTORE_TEST_OWNED_SOURCE_DISABLED="+flag,
			r.receiver, "/directory-receiver.test", "-test.run=^TestOwnedWorkloadSourceAuthority$", "-test.timeout=15s")
	}
	mirrorState := func() string {
		status, body := r.request(t, http.MethodGet, "/admin/identities/by/external/"+
			"gitstore:serviceaccount:"+uid, nil)
		var identity directoryTestIdentity
		if status != http.StatusOK || json.Unmarshal(body, &identity) != nil {
			return ""
		}
		return identity.State
	}
	setSource(true)
	require.Eventually(t, func() bool { return mirrorState() == "inactive" },
		15*time.Second, 100*time.Millisecond, "later source changes must reach the new live consumer")
	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(workloadToken, claims)
	require.NoError(t, err)
	expiration, err := claims.GetExpirationTime()
	require.NoError(t, err)
	require.NotNil(t, expiration)
	require.True(t, expiration.Time.After(time.Now().Add(time.Minute)),
		"source-authority denial must not be explained by credential expiry")
	for _, api := range apis {
		require.NotEmpty(t, r.graphql(t, api, workloadToken, issue).Errors,
			"unexpired credentials must not override source disablement with either provider")
	}
	switchProvider(apis[0], "static-users")
	setSource(false)
	time.Sleep(3 * time.Second)
	require.Equal(t, "inactive", mirrorState(), "switching away must cancel live directory delivery")
	switchProvider(apis[1], "kratos")
	setSource(true)
	setSource(false)
	require.Eventually(t, func() bool { return mirrorState() == "active" },
		15*time.Second, 100*time.Millisecond, "new consumer must deliver later events without replay/repair")
	switchProvider(apis[0], "none")
	require.NotEmpty(t, r.graphql(t, apis[0], operatorToken, read).Errors)
	require.Empty(t, r.graphql(t, apis[1], operatorToken, read).Errors,
		"the other replica must retain management admission through the provider switch")
	require.Equal(t, gitStarted, started(gitName), "the singleton Git process must remain unchanged")
	initial, err := os.ReadFile(r.initialAPIBinary)
	require.NoError(t, err)
	rollback := filepath.Join(t.TempDir(), "rollback-api")
	require.NoError(t, os.WriteFile(rollback, initial, 0755))
	require.NoError(t, os.Rename(rollback, apis[0].binaryFile))
	r.command(t, nil, "restart", "--time", "5", apis[0].name)
	require.Eventually(t, func() bool {
		return strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.Status}}", apis[0].name)) == "exited"
	}, 30*time.Second, 100*time.Millisecond, "unsupported old-binary rollback must fail startup")
	require.Contains(t, r.command(t, nil, "logs", "--tail", "20", apis[0].name), "database is ahead")
	require.Empty(t, r.graphql(t, apis[1], operatorToken, read).Errors)
	t.Logf("verified exact pre-directory baseline %s, forward migration, rolling replacement and provider reconstruction", baseline)
}
