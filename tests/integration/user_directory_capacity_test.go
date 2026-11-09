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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type directoryCapacityCase struct {
	Kind    string   `json:"kind"`
	ActorID string   `json:"actorID"`
	Token   string   `json:"token"`
	Groups  []string `json:"groups"`
	Allowed bool     `json:"allowed"`
}

type directoryFaultWindow struct {
	Replicas    []int `json:"replicas"`
	StartMillis int64 `json:"startMillis"`
	EndMillis   int64 `json:"endMillis"`
}

func (r *directoryTestRuntime) seedCapacityHumans(t *testing.T, kind string, start, count int) {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}
	for offset := start; offset < count; offset += 500 {
		end := min(offset+500, count)
		patches := make([]map[string]any, 0, end-offset)
		for index := offset; index < end; index++ {
			name := fmt.Sprintf("owned-capacity-%s-%06d", kind, index)
			groups := []string{"merchandiser"}
			if kind == "staff" && index == 2 {
				groups = []string{}
			}
			patches = append(patches, map[string]any{"create": map[string]any{
				"schema_id": kind, "state": "active", "external_id": fmt.Sprintf("gitstore:capacity:%s:%d", kind, index),
				"traits": map[string]any{"username": name, "email": name + "@example.test", "full_name": name},
				"metadata_admin": map[string]any{
					"creation_actor": "operator", "update_actor": "operator", "setup_complete": true,
					"groups": groups, "gitstore": map[string]any{"generation": 1, "resource_version": "1"},
				},
				"verifiable_addresses": []map[string]any{{"value": name + "@example.test", "via": "email", "verified": true, "status": "completed"}},
			}})
		}
		payload, err := json.Marshal(map[string]any{"identities": patches})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		request, err := http.NewRequestWithContext(ctx, http.MethodPatch, r.admin+"/admin/identities", bytes.NewReader(payload))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		require.NoError(t, err, "bounded native identity batch failed")
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		cancel()
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, "native identity batch must succeed")
		if end%10000 == 0 || end == count {
			t.Logf("native %s identities seeded: %d", kind, end)
		}
	}
	require.Equal(t, strconv.Itoa(count), strings.TrimSpace(r.sql(t,
		fmt.Sprintf("SELECT count(*) FROM identities WHERE schema_id='%s';", kind))),
		"native storage, not an import response or manifest, proves the seeded count")
}

func (r *directoryTestRuntime) capacityHuman(t *testing.T, kind string, index int) directoryTestIdentity {
	t.Helper()
	status, body := r.request(t, http.MethodGet, "/admin/identities/by/external/"+
		url.PathEscape(fmt.Sprintf("gitstore:capacity:%s:%d", kind, index)), nil)
	require.Equal(t, http.StatusOK, status)
	var identity directoryTestIdentity
	require.NoError(t, json.Unmarshal(body, &identity))
	require.Equal(t, kind, identity.SchemaID)
	return identity
}

func capacityJSON(t *testing.T, path string, value any) {
	t.Helper()
	body, err := json.MarshalIndent(value, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(body, '\n'), 0600))
}

func TestOwnedUserDirectoryCapacitySeedContract(t *testing.T) {
	if os.Getenv("USER_DIRECTORY_TEST_CAPACITY") != "1" {
		t.Skip("requires explicit owned capacity fixture authorization")
	}
	r := startDirectoryTestRuntime(t)
	r.seedCapacityHumans(t, "staff", 0, 4)
	r.seedCapacityHumans(t, "customer", 0, 501)
	r.seedCapacityHumans(t, "customer", 501, 502)
	for _, sample := range []struct {
		kind  string
		index int
	}{{"staff", 1}, {"customer", 501}} {
		identity := r.capacityHuman(t, sample.kind, sample.index)
		require.Equal(t, "active", identity.State)
		require.Equal(t, true, identity.MetadataAdmin["setup_complete"])
		status, body := r.request(t, http.MethodGet, "/admin/identities/"+identity.ID, nil)
		require.Equal(t, http.StatusOK, status)
		var record struct {
			VerifiableAddresses []struct {
				Value    string `json:"value"`
				Verified bool   `json:"verified"`
			} `json:"verifiable_addresses"`
		}
		require.NoError(t, json.Unmarshal(body, &record))
		require.Len(t, record.VerifiableAddresses, 1)
		require.True(t, record.VerifiableAddresses[0].Verified)
		require.Equal(t, identity.Traits["email"], record.VerifiableAddresses[0].Value)
	}
}

func TestOwnedUserDirectoryCapacity(t *testing.T) {
	if os.Getenv("USER_DIRECTORY_TEST_CAPACITY") != "1" {
		t.Skip("requires explicit USER_DIRECTORY_TEST_CAPACITY=1 and owned deployment authorization")
	}
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	r := startDirectoryTestRuntime(t)
	chaos := os.Getenv("USER_DIRECTORY_TEST_CHAOS") == "1"
	r.seedCapacityHumans(t, "staff", 0, 1000)
	r.seedCapacityHumans(t, "customer", 0, 100000)
	operator := r.capacityHuman(t, "staff", 0)
	apis, token := r.startAPIs(t, operator.ID, true)
	operatorToken := token(operator.ID)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	key := secretCapacityOwnedKey{ID: "owned-directory",
		Public:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		Private: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}))}
	var sourceUID string
	eventUID := ""
	createAccounts := func(start, count int) {
		for index := start; index < count; index++ {
			name := fmt.Sprintf("owned-capacity-bot-%04d", index)
			if index == 0 {
				name = "owned-ci-bot"
			}
			query := fmt.Sprintf(`mutation{createServiceAccount(input:{metadata:{name:%q},publicKeys:[
				{kid:%q,algorithm:"Ed25519",publicKeyPEM:%q}
			]}){serviceAccount{metadata{uid}}}}`, name, key.ID, key.Public)
			result := r.graphql(t, apis[index%len(apis)], operatorToken, query)
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
			if index == 0 {
				sourceUID = created.ServiceAccount.Metadata.UID
			}
			if index == 1 {
				eventUID = created.ServiceAccount.Metadata.UID
			}
		}
		require.Eventually(t, func() bool {
			return strings.TrimSpace(r.sql(t, "SELECT count(*) FROM identities WHERE schema_id='serviceAccount';")) == strconv.Itoa(count)
		}, 3*time.Minute, time.Second, "all actual source events must reach the native directory before capacity")
	}
	createAccounts(0, 100)
	serviceActorID := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/ServiceAccount/" + sourceUID))
	assign := fmt.Sprintf(`mutation{assignUserGroup(input:{id:%q,group:"ci"}){actor{id ... on ServiceAccount{groups}}}}`, serviceActorID)
	require.Empty(t, r.graphql(t, apis[0], operatorToken, assign).Errors)
	issueToken := func() string {
		fixture := secretCapacityOwnedFixture{Name: "owned-ci-bot", UID: sourceUID}
		assertion, err := fixture.assertion(key, fixture.Name)
		require.NoError(t, err)
		result := r.graphql(t, apis[1], assertion, `mutation{issueServiceAccountToken(input:{
			metadata:{name:"owned-ci-bot"},spec:{expirationSeconds:3600}}){tokenRequest{status{token}}}}`)
		require.Empty(t, result.Errors)
		var issued struct {
			TokenRequest struct {
				Status struct {
					Token string `json:"token"`
				} `json:"status"`
			} `json:"tokenRequest"`
		}
		require.NoError(t, json.Unmarshal(result.Data["issueServiceAccountToken"], &issued))
		require.NotEmpty(t, issued.TokenRequest.Status.Token)
		return issued.TokenRequest.Status.Token
	}
	privateDir := filepath.Join(root, ".gitstore", "identity-private", r.owner)
	require.NoError(t, os.MkdirAll(privateDir, 0700))
	t.Cleanup(func() {
		for _, name := range []string{"cases.json", "operator.token", "config.json", "environment.json", "load-done", "connected.json"} {
			err := os.Remove(filepath.Join(privateDir, name))
			require.True(t, err == nil || os.IsNotExist(err))
		}
		require.NoError(t, os.Remove(privateDir))
	})
	gitName := "gitstore-userdir-git-" + r.owner[:12]
	gitStarted := strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", gitName))
	setSource := func(uid string, disabled bool) {
		flag := "0"
		if disabled {
			flag = "1"
		}
		r.command(t, nil, "exec",
			"-e", "GITSTORE_TEST_OWNED_SCYLLA=1",
			"-e", "GITSTORE_TEST_OWNED_SOURCE_UID="+uid, "-e", "GITSTORE_TEST_OWNED_SOURCE_DISABLED="+flag,
			r.receiver, "/directory-receiver.test", "-test.run=^TestOwnedWorkloadSourceAuthority$", "-test.timeout=15s")
	}
	for _, envelope := range []struct {
		name            string
		customers, bots int
	}{
		{"alpha", 100000, 100},
		{"production", 1000000, 1000},
	} {
		if chaos && envelope.name != "alpha" {
			break
		}
		if envelope.name == "production" {
			r.seedCapacityHumans(t, "customer", 100000, envelope.customers)
			createAccounts(100, envelope.bots)
		}
		operatorToken = token(operator.ID)
		workloadToken := issueToken()
		cases := []directoryCapacityCase{}
		for _, sample := range []struct {
			kind    string
			index   int
			allowed bool
			groups  []string
		}{
			{"staff", 1, true, []string{"system:group:merchandiser", "system:staff"}},
			{"staff", 2, false, []string{"system:staff"}},
			{"staff", 3, true, []string{"system:group:merchandiser", "system:staff"}},
			{"customer", 0, false, []string{"system:group:customers", "system:customers"}},
			{"customer", envelope.customers - 1, false, []string{"system:group:customers", "system:customers"}},
		} {
			identity := r.capacityHuman(t, sample.kind, sample.index)
			kind := "Staff"
			if sample.kind == "customer" {
				kind = "Customer"
			}
			cases = append(cases, directoryCapacityCase{Kind: kind,
				ActorID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/" + kind + "/" + identity.ID)),
				Token:   token(identity.ID), Groups: sample.groups, Allowed: sample.allowed})
		}
		cases = append(cases, directoryCapacityCase{Kind: "ServiceAccount", ActorID: serviceActorID,
			Token: workloadToken, Groups: []string{"system:group:ci", "system:serviceaccounts"}, Allowed: true})
		windows := []directoryFaultWindow{}
		if chaos {
			start := time.Now().Add(2 * time.Minute).UnixMilli()
			windows = []directoryFaultWindow{
				{Replicas: []int{0, 1}, StartMillis: start, EndMillis: start + 30000},
				{Replicas: []int{0}, StartMillis: start + 120000, EndMillis: start + 150000},
			}
		}
		capacityJSON(t, filepath.Join(privateDir, "cases.json"), map[string]any{"cases": cases, "faultWindows": windows})
		require.NoError(t, os.WriteFile(filepath.Join(privateDir, "operator.token"), []byte(operatorToken), 0600))
		var dockerInfo struct {
			MemTotal     int64
			NCPU         int
			Architecture string
		}
		require.NoError(t, json.Unmarshal([]byte(r.command(t, nil, "info", "--format", "{{json .}}")), &dockerInfo))
		require.Positive(t, dockerInfo.MemTotal)
		require.Positive(t, dockerInfo.NCPU)
		capacityJSON(t, filepath.Join(privateDir, "config.json"), map[string]any{
			"schemaVersion": 1, "services": map[string]any{
				"api":        map[string]any{"build": "release", "replicas": 2},
				"gitService": map[string]any{"build": "release"},
				"scylla":     map[string]any{"nodes": 1, "smpPerNode": 1},
				"identity": map[string]any{"owner": r.owner, "staff": 1000, "customers": envelope.customers,
					"serviceAccounts": envelope.bots, "cacheEntries": 256, "postgresContainer": r.postgres,
					"scyllaContainer": "gitstore-userdir-scylla-" + r.owner[:12], "gitContainer": gitName,
					"kratosContainers": []string{"gitstore-userdir-kratos-" + r.owner[:12] + "-0", "gitstore-userdir-kratos-" + r.owner[:12] + "-1"}},
			},
		})
		capacityJSON(t, filepath.Join(privateDir, "environment.json"), map[string]any{
			"schemaVersion": 1, "runtime": "docker", "architecture": dockerInfo.Architecture,
			"host":      map[string]any{"logicalCPUs": dockerInfo.NCPU, "memoryBytes": dockerInfo.MemTotal},
			"datastore": map[string]any{"memoryBytesPerNode": int64(2 << 30), "requireNoUnexpectedRestarts": true, "requireNoOOMKills": true},
			"topology":  map[string]any{"apiReplicas": 2, "gitServiceReplicas": 1, "scyllaNodes": 1},
		})
		doneFile, connectedFile := filepath.Join(privateDir, "load-done"), filepath.Join(privateDir, "connected.json")
		for _, path := range []string{doneFile, connectedFile} {
			err := os.Remove(path)
			require.True(t, err == nil || os.IsNotExist(err))
		}
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Minute)
		logPath := filepath.Join(root, ".gitstore", "identity-"+envelope.name+"-"+r.owner[:12]+".log")
		_, err := os.Stat(logPath)
		require.True(t, os.IsNotExist(err), "capacity log must have a fresh owned path")
		command := exec.CommandContext(ctx, "python3", filepath.Join(root, "scripts/capacity-child.py"),
			"2700", logPath, "make", "capacity", "TARGET=api", "PROFILE=readiness", "MODE=alpha")
		command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
		command.WaitDelay = 10 * time.Second
		command.Dir = root
		command.Env = append(os.Environ(),
			"CAPACITY_IDENTITY_READINESS=1", "CAPACITY_IDENTITY_ENVELOPE="+envelope.name,
			"CAPACITY_TOKEN_FILE="+filepath.Join(privateDir, "operator.token"),
			"CAPACITY_IDENTITY_FIXTURE_FILE="+filepath.Join(privateDir, "cases.json"),
			"CAPACITY_IDENTITY_LOAD_DONE_FILE="+doneFile, "CAPACITY_IDENTITY_CONNECTED_FILE="+connectedFile,
			"CAPACITY_CONFIG_MANIFEST="+filepath.Join(privateDir, "config.json"),
			"CAPACITY_ENVIRONMENT_MANIFEST="+filepath.Join(privateDir, "environment.json"),
			"CAPACITY_API_ENDPOINTS="+apis[0].base+","+apis[1].base, "CAPACITY_BASE_URL="+apis[0].base,
			"CAPACITY_API_CONTAINERS="+apis[0].name+","+apis[1].name,
			"CAPACITY_API_REPLICAS=2", "CAPACITY_API_BUILD=release",
			"CAPACITY_RUNTIME_MEMORY_BYTES="+strconv.FormatInt(dockerInfo.MemTotal, 10),
			"CAPACITY_DATASTORE_CONTAINERS=gitstore-userdir-scylla-"+r.owner[:12],
			"CAPACITY_RUN_ID=identity-"+envelope.name+"-"+r.owner[:12],
			"CAPACITY_DOCKER_NETWORK=host", "CAPACITY_DURATION=30m", "CAPACITY_RATE=500")
		if chaos {
			command.Env = append(command.Env, "CAPACITY_IDENTITY_CHAOS=1")
		}
		require.NoError(t, command.Start())
		completed := make(chan error, 1)
		finished := make(chan struct{})
		go func() {
			completed <- command.Wait()
			close(finished)
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-finished:
			case <-time.After(15 * time.Second):
				t.Error("owned capacity process-group cleanup did not finish")
			}
		})
		events, disabled, connected := 0, false, false
		faults := []map[string]any{}
		restartMetrics := map[string]string{}
		nextEvent := time.Now().Add(20 * time.Second)
		ticker := time.NewTicker(time.Second)
		for {
			select {
			case err := <-completed:
				ticker.Stop()
				cancel()
				require.NoError(t, err, "capacity %s failed; retained private log: %s", envelope.name, logPath)
				t.Logf("capacity %s passed with %d observed live events", envelope.name, events)
				goto nextEnvelope
			case <-ctx.Done():
				ticker.Stop()
				cancel()
				t.Fatal("bounded identity capacity run exceeded its deadline")
			case <-ticker.C:
				if len(faults) < len(windows) && time.Now().UnixMilli() >= windows[len(faults)].StartMillis {
					metadataPath := filepath.Join(root, ".gitstore", "capacity", "api", "readiness", "alpha",
						"identity-"+envelope.name+"-"+r.owner[:12], "metadata.json")
					body, err := os.ReadFile(metadataPath)
					require.NoError(t, err, "faults require an active attributable capacity run")
					var load struct {
						Started string `json:"loadStartedAt"`
						Ended   string `json:"loadCompletedAt"`
					}
					require.NoError(t, json.Unmarshal(body, &load))
					require.NotEmpty(t, load.Started)
					require.Empty(t, load.Ended)
					index := len(faults)
					profile, target := "identity-restart", "gitstore-userdir-kratos-"+r.owner[:12]+"-1"
					if index == 1 {
						profile, target = "api-restart", apis[0].name
						restartMetrics["before"] = r.directoryMetrics(t, apis[0])
					}
					faults = append(faults, r.capacityFault(t, root, profile, target, windows[index], apis, operatorToken, cases[0].ActorID))
					if index == 1 {
						restartMetrics["after"] = r.directoryMetrics(t, apis[0])
					}
				}
				if _, err := os.Stat(doneFile); err == nil {
					if connected {
						continue
					}
					setSource(sourceUID, true)
					claims := jwt.MapClaims{}
					_, _, err := jwt.NewParser().ParseUnverified(workloadToken, claims)
					require.NoError(t, err)
					expires, err := claims.GetExpirationTime()
					require.NoError(t, err)
					require.NotNil(t, expires)
					require.True(t, expires.Time.After(time.Now().Add(time.Minute)), "authority denial must not be explained by expiry")
					read := fmt.Sprintf(`{node(id:%q){id ... on ServiceAccount{groups}}}`, serviceActorID)
					for _, api := range apis {
						require.NotEmpty(t, r.graphql(t, api, workloadToken, read).Errors)
					}
					setSource(sourceUID, false)
					require.Eventually(t, func() bool {
						return len(r.graphql(t, apis[0], workloadToken, read).Errors) == 0 &&
							len(r.graphql(t, apis[1], workloadToken, read).Errors) == 0
					}, 30*time.Second, 250*time.Millisecond)
					for _, api := range apis {
						var actor struct {
							Groups []string `json:"groups"`
						}
						require.NoError(t, json.Unmarshal(r.graphql(t, api, workloadToken, read).Data["node"], &actor))
						require.ElementsMatch(t, []string{"system:group:ci", "system:serviceaccounts"}, actor.Groups,
							"later source delivery must preserve administered workload membership")
					}
					preserved := strings.TrimSpace(r.command(t, nil, "volume", "inspect", "--format",
						`{{index .Labels "dev.gitstore.test_owner"}}`, r.postgresVolume)) == r.owner
					preserved = preserved && strings.TrimSpace(r.command(t, nil, "inspect", "--format",
						`{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}`, r.postgres)) == r.postgresVolume
					require.True(t, preserved)
					unchanged := strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", gitName)) == gitStarted
					require.True(t, unchanged)
					capacityJSON(t, connectedFile, map[string]any{"events": events, "sourceAuthorityPassed": true,
						"groupsPassed": true, "laterDeliveryPassed": true, "gitProcessUnchanged": unchanged,
						"sharedStoragePreserved": preserved, "faults": faults, "apiRestartMetrics": restartMetrics})
					connected = true
				} else {
					if time.Now().Before(nextEvent) {
						continue
					}

					nextEvent = time.Now().Add(20 * time.Second)
					disabled = !disabled
					setSource(eventUID, disabled)
					expected := "active"
					if disabled {
						expected = "inactive"
					}
					require.Eventually(t, func() bool {
						status, body := r.request(t, http.MethodGet, "/admin/identities/by/external/"+
							url.PathEscape("gitstore:serviceaccount:"+eventUID), nil)
						var mirror directoryTestIdentity
						return status == http.StatusOK && json.Unmarshal(body, &mirror) == nil && mirror.State == expected
					}, 15*time.Second, 250*time.Millisecond, "live source event must produce the expected native mirror during load")
					events++
				}
			}
		}
	nextEnvelope:
	}
}

func (r *directoryTestRuntime) directoryMetrics(t *testing.T, api directoryAPI) string {
	t.Helper()
	response, err := r.client.Get(api.base + "/metrics")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	return string(body)
}

func (r *directoryTestRuntime) capacityFault(t *testing.T, root, profile, target string, window directoryFaultWindow,
	apis []directoryAPI, operatorToken, actorID string,
) map[string]any {
	t.Helper()
	require.Greater(t, window.EndMillis-time.Now().UnixMilli(), int64(20000), "fault must start within its declared bounded window")
	require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{index .Config.Labels "dev.gitstore.test_owner"}}`, target)))
	before := strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", target))
	runID := r.owner + "-" + profile
	injected := time.Now().UnixMilli()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "make", "chaos", "CHAOS_PROFILE="+profile, "CHAOS_TARGET="+target,
		"CHAOS_CONFIRM=1", "CHAOS_OWNER="+r.owner, "CHAOS_OWNERSHIP_LABEL=dev.gitstore.test_owner",
		"CHAOS_RUN_ID="+runID)
	command.Dir = root
	output, err := command.CombinedOutput()
	require.NoError(t, err, "confirmed owned chaos failed: %s", output)
	receipt, err := os.ReadFile(filepath.Join(root, ".gitstore", "chaos", profile, runID, "metadata.json"))
	require.NoError(t, err)
	var metadata struct {
		Injected bool `json:"injected"`
		ExitCode int  `json:"exitCode"`
	}
	require.NoError(t, json.Unmarshal(receipt, &metadata))
	require.True(t, metadata.Injected)
	require.Zero(t, metadata.ExitCode)
	after := strings.TrimSpace(r.command(t, nil, "inspect", "--format", "{{.State.StartedAt}}", target))
	require.NotEqual(t, before, after, "the native process must actually restart")
	require.Equal(t, r.owner, strings.TrimSpace(r.command(t, nil, "inspect", "--format",
		`{{index .Config.Labels "dev.gitstore.test_owner"}}`, target)))
	remaining := func() time.Duration { return time.Until(time.UnixMilli(window.EndMillis)) }
	require.Positive(t, remaining())
	if profile == "identity-restart" {
		require.Eventually(t, func() bool {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			return exec.CommandContext(ctx, "docker", "exec", r.postgres, "wget", "-qO", "/dev/null",
				"http://"+target+":4433/health/ready").Run() == nil
		}, remaining(), 100*time.Millisecond)
	} else {
		require.Eventually(t, func() bool {
			response, err := r.client.Get(apis[0].base + "/ready")
			if err != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == http.StatusOK
		}, remaining(), 100*time.Millisecond)
	}
	dependencyReady := time.Now().UnixMilli()
	read := fmt.Sprintf(`{node(id:%q){id}}`, actorID)
	for _, api := range apis {
		require.Positive(t, remaining())
		require.Eventually(t, func() bool {
			result := r.graphql(t, api, operatorToken, read)
			var actor struct{ ID string }
			return len(result.Errors) == 0 && json.Unmarshal(result.Data["node"], &actor) == nil && actor.ID == actorID
		}, remaining(), 100*time.Millisecond, "existing-profile admission must recover on both APIs within 30 seconds")
	}
	recovered := time.Now().UnixMilli()
	require.LessOrEqual(t, recovered, window.EndMillis)
	t.Logf("confirmed %s during sustained identity load; both API admissions recovered within the bounded window", profile)
	return map[string]any{"profile": profile, "injected": true, "processChanged": true, "recoveredOnBoth": true,
		"injectedMillis": injected, "dependencyReadyMillis": dependencyReady, "recoveredMillis": recovered}
}
