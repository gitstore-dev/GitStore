// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestOwnedKratosServiceAccountMirrors(t *testing.T) {
	r := startDirectoryTestRuntime(t)
	second := fmt.Sprintf("GITSTORE_TEST_KRATOS_SECOND_ADMIN_URI=http://gitstore-userdir-kratos-%s-1:4434", r.owner[:12])
	r.command(t, nil, "exec", "-e", second, r.receiver, "/directory-receiver.test",
		"-test.run=^TestOwnedKratosServiceAccountMirrors$", "-test.timeout=30s")
}

func TestOwnedServiceAccountCDCWriteAcceptance(t *testing.T) {
	r := startDirectoryTestRuntime(t)
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	r.startScylla(t, root)
	binary := filepath.Join(t.TempDir(), "scylla.test")
	build := exec.CommandContext(t.Context(), "go", "test", "-c", "-tags", "scylla", "-o", binary,
		"./internal/datastore/scylla")
	build.Dir = filepath.Join(root, "gitstore-api")
	build.Env = append(os.Environ(), "GOOS=linux", "CGO_ENABLED=0")
	output, err := build.CombinedOutput()
	require.NoError(t, err, "build owned native CDC acceptance: %s", output)
	r.command(t, nil, "cp", binary, r.receiver+":/scylla.test")
	host := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		fmt.Sprintf(`{{with index .NetworkSettings.Networks %q}}{{.IPAddress}}{{end}}`, r.network),
		"gitstore-userdir-scylla-"+r.owner[:12]))
	require.NotEmpty(t, host)
	r.command(t, nil, "exec", "-e", "GITSTORE_TEST_SCYLLA_ADDR="+host+":9042", r.receiver,
		"/scylla.test", "-test.v",
		"-test.run=^TestServiceAccountCDC(ReaderEnrollmentDeletionAndNameReuse|UpgradePreservesDataAndMigrationHistory)$",
		"-test.timeout=3m")
}

func TestOwnedScyllaWorkloadDirectoryAcrossAPIs(t *testing.T) {
	r := startDirectoryTestRuntime(t, true)
	operator := r.createHuman(t, "staff", "operator", "operator@example.test")
	status, _ := r.request(t, http.MethodPatch, "/admin/identities/"+operator.ID, []map[string]any{
		{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
			{"value": operator.Traits["email"], "verified": true, "via": "email", "status": "completed"},
		}},
		{"op": "add", "path": "/metadata_admin/setup_complete", "value": true},
	})
	require.Equal(t, http.StatusOK, status)
	apis, token := r.startAPIs(t, operator.ID, true)
	operatorToken := token(operator.ID)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	key := secretCapacityOwnedKey{ID: "owned-directory", Public: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		Private: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))}
	create := fmt.Sprintf(`mutation{createServiceAccount(input:{metadata:{name:"owned-ci-bot"},publicKeys:[
		{kid:%q,algorithm:"Ed25519",publicKeyPEM:%q}
	]}){serviceAccount{id metadata{uid}}}}`, key.ID, key.Public)
	sourceUID := func(result directoryGraphQLResponse) string {
		t.Helper()
		require.Empty(t, result.Errors)
		var created struct {
			ServiceAccount struct {
				Metadata struct {
					UID string `json:"uid"`
				} `json:"metadata"`
			} `json:"serviceAccount"`
		}
		require.NoError(t, json.Unmarshal(result.Data["createServiceAccount"], &created))
		require.NotEmpty(t, created.ServiceAccount.Metadata.UID)
		return created.ServiceAccount.Metadata.UID
	}
	uid := sourceUID(r.graphql(t, apis[0], operatorToken, create))
	linkedPath := func(uid string) string {
		return "/admin/identities/by/external/" + url.PathEscape("gitstore:serviceaccount:"+uid)
	}
	var mirror struct {
		ID                  string         `json:"id"`
		State               string         `json:"state"`
		Credentials         map[string]any `json:"credentials"`
		VerifiableAddresses []any          `json:"verifiable_addresses"`
		RecoveryAddresses   []any          `json:"recovery_addresses"`
		Traits              struct {
			SourceUID  string `json:"source_uid"`
			SourceName string `json:"source_name"`
		} `json:"traits"`
	}
	require.Eventually(t, func() bool {
		status, raw := r.request(t, http.MethodGet, linkedPath(uid)+"?include_credential=password&include_credential=oidc", nil)
		return status == http.StatusOK && json.Unmarshal(raw, &mirror) == nil && mirror.Traits.SourceUID == uid
	}, 45*time.Second, 250*time.Millisecond, "actual shared Scylla CDC must reach the native directory through deployed API consumers")
	require.NotEqual(t, uid, mirror.ID)
	require.Equal(t, "active", mirror.State)
	require.Equal(t, "owned-ci-bot", mirror.Traits.SourceName)
	require.Empty(t, mirror.Credentials)
	require.Empty(t, mirror.VerifiableAddresses)
	require.Empty(t, mirror.RecoveryAddresses)
	actorID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/ServiceAccount/" + uid))
	assign := fmt.Sprintf(`mutation{assignUserGroup(input:{id:%q,group:"ci"}){actor{id ... on ServiceAccount{groups}}}}`, actorID)
	for _, api := range apis {
		require.Empty(t, r.graphql(t, api, operatorToken, assign).Errors, "duplicate protected group writes across native API replicas are idempotent")
	}
	for _, operation := range []string{"updateUser", "suspendUser", "deactivateUser", "reactivateUser", "deleteUser", "resendStaffInvitation"} {
		query := fmt.Sprintf(`mutation{%s(input:{id:%q}){`, operation, actorID)
		if operation == "resendStaffInvitation" {
			query += "staff{id}}}"
		} else {
			query += "actor{id}}}"
		}
		require.NotEmpty(t, r.graphql(t, apis[0], operatorToken, query).Errors,
			"human operations must not mutate workload authority")
	}
	accessToken := func(uid string) string {
		t.Helper()
		fixture := secretCapacityOwnedFixture{Name: "owned-ci-bot", UID: uid}
		assertion, err := fixture.assertion(key, fixture.Name)
		require.NoError(t, err)
		query := `mutation{issueServiceAccountToken(input:{metadata:{name:"owned-ci-bot"},spec:{expirationSeconds:60}}){
			tokenRequest{status{token}}}}`
		response := r.graphql(t, apis[1], assertion, query)
		require.Empty(t, response.Errors)
		var issued struct {
			TokenRequest struct {
				Status struct {
					Token string `json:"token"`
				} `json:"status"`
			} `json:"tokenRequest"`
		}
		require.NoError(t, json.Unmarshal(response.Data["issueServiceAccountToken"], &issued))
		require.NotEmpty(t, issued.TokenRequest.Status.Token)
		return issued.TokenRequest.Status.Token
	}
	oldToken := accessToken(uid)
	requireUnexpired := func(token string) {
		t.Helper()
		claims := jwt.MapClaims{}
		_, _, err := jwt.NewParser().ParseUnverified(token, claims)
		require.NoError(t, err)
		expires, err := claims.GetExpirationTime()
		require.NoError(t, err)
		require.NotNil(t, expires)
		require.True(t, expires.Time.After(time.Now().Add(5*time.Second)),
			"source-authority rejection must not be explained by token expiry")
	}
	operatorID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + operator.ID))
	groupRead := fmt.Sprintf(`{node(id:%q){id ... on User{metadata{name}}}}`, operatorID)
	for _, api := range apis {
		require.Empty(t, r.graphql(t, api, oldToken, groupRead).Errors,
			"a real source-issued workload token must acquire the protected directory group authorization")
	}
	drops := make([]int, len(apis))
	for index, api := range apis {
		drops[index] = strings.Count(r.command(t, nil, "logs", api.name), `"outcome":"exhausted"`)
	}
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
	r.command(t, nil, "exec", "-e", "GITSTORE_TEST_OWNED_SCYLLA=1",
		"-e", "GITSTORE_TEST_OWNED_SOURCE_UID="+uid, "-e", "GITSTORE_TEST_OWNED_SOURCE_DISABLED=1",
		r.receiver, "/directory-receiver.test", "-test.run=^TestOwnedWorkloadSourceAuthority$", "-test.timeout=30s")
	require.Eventually(t, func() bool {
		for index, api := range apis {
			if strings.Count(r.command(t, nil, "logs", api.name), `"outcome":"exhausted"`) <= drops[index] {
				return false
			}
		}
		return true
	}, 30*time.Second, 500*time.Millisecond, "both live consumers must actually exhaust and discard the native disabled-source event")
	beforeRestart := strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", apis[1].name))
	require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{index .Config.Labels "dev.gitstore.test_owner"}}`, apis[1].name)))
	r.command(t, nil, "kill", "--signal", "KILL", apis[1].name)
	for replica := range 2 {
		r.command(t, nil, "unpause", fmt.Sprintf("gitstore-userdir-kratos-%s-%d", r.owner[:12], replica))
	}
	r.command(t, nil, "start", apis[1].name)
	port := strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{(index (index .NetworkSettings.Ports "4000/tcp") 0).HostPort}}`, apis[1].name))
	apis[1].base = "http://127.0.0.1:" + port
	require.Eventually(t, func() bool {
		health, err := r.client.Get(apis[1].base + "/ready")
		if err != nil {
			return false
		}
		health.Body.Close()
		return health.StatusCode == http.StatusOK
	}, 30*time.Second, 250*time.Millisecond)
	require.NotEqual(t, beforeRestart, strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", apis[1].name)))
	time.Sleep(time.Second)
	status, raw := r.request(t, http.MethodGet, linkedPath(uid), nil)
	require.Equal(t, http.StatusOK, status)
	require.NoError(t, json.Unmarshal(raw, &mirror))
	require.Equal(t, "active", mirror.State, "a discarded event must not be repaired or replayed on consumer restart")
	requireUnexpired(oldToken)
	for _, api := range apis {
		status, raw := r.requestAt(t, api.base, http.MethodPost, "/admin/graphql", map[string]string{"query": `{__typename}`},
			http.Header{"Authorization": {"Bearer " + oldToken}})
		if status == http.StatusOK {
			var denied directoryGraphQLResponse
			require.NoError(t, json.Unmarshal(raw, &denied))
			require.NotEmpty(t, denied.Errors)
		} else {
			require.Equal(t, http.StatusUnauthorized, status)
		}
	}
	r.command(t, nil, "exec", "-e", "GITSTORE_TEST_OWNED_SCYLLA=1",
		"-e", "GITSTORE_TEST_OWNED_SOURCE_UID="+uid, "-e", "GITSTORE_TEST_OWNED_SOURCE_DISABLED=0",
		r.receiver, "/directory-receiver.test", "-test.run=^TestOwnedWorkloadSourceAuthority$", "-test.timeout=30s")
	healthyToken := accessToken(uid)
	remove := strings.Replace(assign, "assignUserGroup", "removeUserGroup", 1)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, remove).Errors)
	require.Empty(t, r.graphql(t, apis[1], operatorToken, assign).Errors)
	require.Empty(t, r.graphql(t, apis[0], healthyToken, `{__typename}`).Errors,
		"later source events and fresh credentials remain usable after finite discard")
	deletion := `mutation{deleteServiceAccount(input:{metadata:{name:"owned-ci-bot"}}){serviceAccount{id}}}`
	require.Empty(t, r.graphql(t, apis[0], operatorToken, deletion).Errors)
	require.Eventually(t, func() bool {
		status, _ := r.request(t, http.MethodGet, linkedPath(uid), nil)
		return status == http.StatusNotFound
	}, 30*time.Second, 250*time.Millisecond)
	replacementUID := sourceUID(r.graphql(t, apis[1], operatorToken, create))
	require.NotEqual(t, uid, replacementUID)
	replacementID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/ServiceAccount/" + replacementUID))
	read := fmt.Sprintf(`{node(id:%q){id ... on ServiceAccount{groups}}}`, replacementID)
	require.Eventually(t, func() bool {
		status, _ := r.request(t, http.MethodGet, linkedPath(replacementUID), nil)
		return status == http.StatusOK
	}, 30*time.Second, 250*time.Millisecond)
	for _, api := range apis {
		actor := r.graphql(t, api, operatorToken, read)
		require.Empty(t, actor.Errors)
		var node struct {
			Groups []string `json:"groups"`
		}
		require.NoError(t, json.Unmarshal(actor.Data["node"], &node))
		require.Equal(t, []string{"system:serviceaccounts"}, node.Groups, "name reuse must not transfer the old UID's directory groups")
		requireUnexpired(healthyToken)
		status, raw := r.requestAt(t, api.base, http.MethodPost, "/admin/graphql", map[string]string{"query": groupRead},
			http.Header{"Authorization": {"Bearer " + healthyToken}})
		if status == http.StatusOK {
			var denied directoryGraphQLResponse
			require.NoError(t, json.Unmarshal(raw, &denied))
			require.NotEmpty(t, denied.Errors, "the old UID's credential must remain invalid after name reuse")
		} else {
			require.Equal(t, http.StatusUnauthorized, status)
		}
	}
}
