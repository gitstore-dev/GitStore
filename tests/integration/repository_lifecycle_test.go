// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repositoryAdmissionResource struct {
	ID       string `json:"id"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		UID       string `json:"uid"`
	} `json:"metadata"`
}

// TestRepositoryLifecycle_PushAdmissionReadIdentity verifies the production
// post-receive path: a pushed Repository manifest is admitted, then every
// public read envelope exposes the same Relay-encoded identity.  In
// particular, metadata.uid must never regress to the datastore UUID.
func TestRepositoryLifecycle_PushAdmissionReadIdentity(t *testing.T) {
	runRepositoryLifecyclePushAdmissionReadIdentity(t)
}

func runRepositoryLifecyclePushAdmissionReadIdentity(t *testing.T) {
	t.Helper()
	namespace := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("repository-push-admission")
	token := namespaceContractBootstrapToken(t, apiURL)
	// Repository manifests are valid only in the owning namespace's bootstrap
	// gitstore-system repository.  Using the general integration REPOSITORY
	// target here would exercise (and correctly fail) the authoring-target
	// guard instead of the production admission path this probe is intended to
	// verify.
	h := newPushHelperForRepoWithToken(t, namespace, "gitstore-system", token)
	h.commitRepository(name+".md", validRepositoryFixture(name, namespace))
	if out, err := h.push(); err != nil {
		t.Fatalf("push Repository manifest: %v\n%s", err, out)
	}

	byPath := waitForAdmittedRepository(t, token, namespace, name)
	require.NotEmpty(t, byPath.ID)
	assert.Equal(t, byPath.ID, byPath.Metadata.UID, "metadata.uid must use the Repository Relay encoding")
	assert.Equal(t, name, byPath.Metadata.Name)
	assert.Equal(t, namespace, byPath.Metadata.Namespace)

	byNode := repositoryAdmissionQueryNode(t, token, byPath.ID)
	assert.Equal(t, byPath, byNode, "node(id:) must return the admitted Repository envelope")

	listed := repositoryAdmissionQueryConnection(t, token, namespace)
	var fromConnection *repositoryAdmissionResource
	for _, repository := range listed {
		if repository.Metadata.Name == name {
			fromConnection = repository
			break
		}
	}
	require.NotNil(t, fromConnection, "admitted Repository must appear in its namespace connection")
	assert.Equal(t, byPath, fromConnection, "connection must preserve the Repository Relay identity")
}

// TestRepositoryLifecycle_DualControllerRegistrationAndReconcile is a
// deployment probe, not an in-process substitute. Each endpoint must identify
// a separately running controller-manager with independent queues, caches, and
// checkpoints. The counter deltas prove both registrations consumed work from
// the live Repository watch after the push.
func TestRepositoryLifecycle_DualControllerRegistrationAndReconcile(t *testing.T) {
	runRepositoryLifecycleDualControllerRegistrationAndReconcile(t)
}

func TestRepositoryWatchMetadataGenerationAndSelectorReplay(t *testing.T) {
	cfg := repositoryCapacityConfig{
		apiA:        strings.TrimSuffix(os.Getenv("REPOSITORY_API_A"), "/"),
		apiB:        strings.TrimSuffix(os.Getenv("REPOSITORY_API_B"), "/"),
		controllerA: strings.TrimSuffix(os.Getenv("REPOSITORY_CONTROLLER_A"), "/"),
		controllerB: strings.TrimSuffix(os.Getenv("REPOSITORY_CONTROLLER_B"), "/"),
		namespace:   uniqueName("repository-watch-metadata"),
	}
	if cfg.apiA == "" || cfg.apiB == "" || cfg.controllerA == "" || cfg.controllerB == "" {
		t.Skip("set two REPOSITORY_API endpoints, two REPOSITORY_CONTROLLER endpoints, and REPOSITORY_TOKEN or REPOSITORY_TOKEN_FILE for the deployed probe")
	}
	require.NotEqual(t, cfg.apiA, cfg.apiB)
	require.NotEqual(t, cfg.controllerA, cfg.controllerB)
	cfg.token = repositoryCapacityToken(t)
	runRepositoryWatchMetadataGenerationAndSelectorReplay(t, cfg)
}

func runRepositoryWatchMetadataGenerationAndSelectorReplay(t *testing.T, cfg repositoryCapacityConfig) repositoryCapacityConfig {
	t.Helper()
	require.NotEmpty(t, cfg.apiA)
	require.NotEmpty(t, cfg.apiB)
	require.NotEmpty(t, cfg.controllerA)
	require.NotEmpty(t, cfg.controllerB)
	require.NotEmpty(t, cfg.token)
	require.NotEqual(t, cfg.apiA, cfg.apiB)
	require.NotEqual(t, cfg.controllerA, cfg.controllerB)
	cfg.namespace = uniqueName("repository-watch-metadata")
	client := &http.Client{Timeout: 10 * time.Second}
	beforeA := requireRepositoryController(t, client, cfg.controllerA)
	beforeB := requireRepositoryController(t, client, cfg.controllerB)
	require.NotEqual(t, repositoryControllerIdentity(t, client, cfg.controllerA), repositoryControllerIdentity(t, client, cfg.controllerB))
	createWatchMetadataNamespace(t, cfg.apiA, cfg.apiB, cfg.token, cfg.namespace)
	require.NoError(t, waitRepositoryCapacityNamespaceReady(t.Context(), client, cfg))
	name := uniqueName("metadata-replay")
	selector := map[string]any{"matchLabels": map[string]any{"watch-metadata-run": name, "selected": "yes"}}
	const selection = `type name resourceVersion repository { metadata { generation resourceVersion revision annotations } }`
	open := func(endpoint, cursor string) *websocket.Conn {
		t.Helper()
		conn, err := dialRepositoryCapacityWatchSelection(endpoint, cfg, cursor, selection, selector)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	watchA := open(cfg.apiA, "__repository_watch_bootstrap__")
	require.NoError(t, watchA.SetReadDeadline(time.Now().Add(10*time.Second)))
	bookmark, err := readRepositoryCapacityEvent(watchA)
	require.NoError(t, err)
	require.Equal(t, "BOOKMARK", bookmark.Type)
	update := func(endpoint, phase string, create, selected bool) (string, string) {
		t.Helper()
		labels := map[string]any{"watch-metadata-run": name}
		if selected {
			labels["selected"] = "yes"
		}
		operation, inputType := "updateRepository", "UpdateRepositoryInput"
		if create {
			operation, inputType = "createRepository", "CreateRepositoryInput"
		}
		query := fmt.Sprintf(`mutation($input: %s!) { result: %s(input: $input) { repository { metadata { generation resourceVersion revision } } } }`, inputType, operation)
		response := watchMetadataGraphQL(t, endpoint, cfg.token, query, map[string]any{"input": map[string]any{
			"metadata": map[string]any{
				"name": name, "namespace": cfg.namespace, "labels": labels,
				"annotations": map[string]any{"watch-metadata-phase": phase},
			},
			"spec": map[string]any{"defaultBranch": "main", "visibility": "PRIVATE", "storageClass": "standard"},
		}})
		require.Empty(t, response.Errors, namespaceContractErrors(response.Errors))
		var result struct {
			Result repositoryCapacityEvent `json:"result"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &result))
		require.NotNil(t, result.Result.Repository)
		metadata := result.Result.Repository.Metadata
		require.EqualValues(t, 1, metadata.Generation, "metadata-only authoring must not advance generation")
		require.NotEmpty(t, metadata.ResourceVersion)
		require.NotEmpty(t, metadata.Revision)
		return metadata.ResourceVersion, metadata.Revision
	}
	read := func(conn *websocket.Conn, eventType, revision string) repositoryCapacityEvent {
		t.Helper()
		for range 100 {
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
			event, readErr := readRepositoryCapacityEvent(conn)
			require.NoError(t, readErr)
			if event.Name != name || event.Type != eventType {
				continue
			}
			require.NotEmpty(t, event.ResourceVersion)
			if eventType == "DELETED" {
				require.Nil(t, event.Repository, "typed selector exits have no resource payload")
				return event
			}
			require.NotNil(t, event.Repository)
			if event.Repository.Metadata.Revision != revision {
				continue
			}
			require.EqualValues(t, 1, event.Repository.Metadata.Generation)
			require.NotEmpty(t, event.Repository.Metadata.ResourceVersion)
			return event
		}
		t.Fatal("Repository watch did not expose the required transition")
		return repositoryCapacityEvent{}
	}
	update(cfg.apiB, "created", true, false)
	require.NoError(t, waitRepositoryCapacityResourceReady(t.Context(), client, cfg, "Repository", name))
	enteredVersion, enteredRevision := update(cfg.apiA, "entered", false, true)
	entered := read(watchA, "ADDED", enteredRevision)
	require.NoError(t, watchA.Close())
	modifiedVersion, modifiedRevision := update(cfg.apiB, "modified", false, true)
	exitedVersion, _ := update(cfg.apiA, "exited", false, false)
	require.NotEqual(t, enteredVersion, modifiedVersion)
	require.NotEqual(t, modifiedVersion, exitedVersion)
	watchB := open(cfg.apiB, entered.ResourceVersion)
	modified := read(watchB, "MODIFIED", modifiedRevision)
	exited := read(watchB, "DELETED", "")
	require.NotEqual(t, entered.ResourceVersion, modified.ResourceVersion)
	require.NotEqual(t, modified.ResourceVersion, exited.ResourceVersion)
	_, reenteredRevision := update(cfg.apiB, "reentered", false, true)
	read(watchB, "ADDED", reenteredRevision)

	h := newPushHelperForRepoWithToken(t, cfg.namespace, "gitstore-system", cfg.token)
	content, err := os.ReadFile(filepath.Join(h.workDir, "repositories", name+".md"))
	require.NoError(t, err)
	// A YAML comment changes Git provenance without changing parsed metadata,
	// specification or body.
	provenanceOnly := strings.Replace(string(content), "---\n", "---\n# provenance-only rewrite\n", 1)
	require.NotEqual(t, string(content), provenanceOnly)
	h.commitRepository(name+".md", provenanceOnly)
	revision := pushWatchMetadataProvenance(t, h)
	provenance := read(watchB, "MODIFIED", revision)
	require.Equal(t, "reentered", provenance.Repository.Metadata.Annotations["watch-metadata-phase"])
	require.NotEqual(t, reenteredRevision, provenance.Repository.Metadata.Revision)
	require.NoError(t, waitRepositoryCapacityResourceReady(t.Context(), client, cfg, "Repository", name))
	waitForRepositoryControllerAdvance(t, client, cfg.controllerA, beforeA)
	waitForRepositoryControllerAdvance(t, client, cfg.controllerB, beforeB)
	require.Empty(t, repositoryControllerPoison(t, client, cfg.controllerA))
	require.Empty(t, repositoryControllerPoison(t, client, cfg.controllerB))
	return cfg
}

func runRepositoryLifecycleDualControllerRegistrationAndReconcile(t *testing.T) {
	t.Helper()
	controllerA := strings.TrimSuffix(os.Getenv("REPOSITORY_CONTROLLER_A"), "/")
	controllerB := strings.TrimSuffix(os.Getenv("REPOSITORY_CONTROLLER_B"), "/")
	if controllerA == "" || controllerB == "" {
		t.Skip("set REPOSITORY_CONTROLLER_A and REPOSITORY_CONTROLLER_B to distinct deployed controller-manager endpoints")
	}
	require.NotEqual(t, controllerA, controllerB)

	client := &http.Client{Timeout: 5 * time.Second}
	beforeA := requireRepositoryController(t, client, controllerA)
	beforeB := requireRepositoryController(t, client, controllerB)
	require.NotEqual(t,
		repositoryControllerIdentity(t, client, controllerA),
		repositoryControllerIdentity(t, client, controllerB),
		"controller endpoints resolve to the same process instance",
	)

	namespace := getEnv("NAMESPACE", "gitstore-test")
	name := uniqueName("repository-dual-controller")
	token := namespaceContractBootstrapToken(t, apiURL)
	h := newPushHelperForRepoWithToken(t, namespace, "gitstore-system", token)
	h.commitRepository(name+".md", validRepositoryFixture(name, namespace))
	if out, err := h.push(); err != nil {
		t.Fatalf("push Repository manifest: %v\n%s", err, out)
	}

	waitForRepositoryReady(t, token, namespace, name)
	waitForRepositoryControllerAdvance(t, client, controllerA, beforeA)
	waitForRepositoryControllerAdvance(t, client, controllerB, beforeB)
	require.Empty(t, repositoryControllerPoison(t, client, controllerA))
	require.Empty(t, repositoryControllerPoison(t, client, controllerB))
}

// TestRepositoryLifecycle_TwoReplicaCapacity is the evidence-gated deployment
// harness for T035/T037. It requires two real API processes, two real
// controller-manager processes, shared durable storage, and an external
// replacement trigger. Focused and in-process tests deliberately skip it.
func TestRepositoryLifecycle_TwoReplicaCapacity(t *testing.T) {
	runRepositoryLifecycleCapacity(t)
}

// TestRepositoryLifecycle_DeployedEvidenceGate runs the capacity workload
// first so its namespace and bootstrap repository exist, then proves the raw
// Git-push admission path and both independently running controller managers
// against that same deployed stack. The capacity dispatcher selects this
// aggregate test so all three probes are retained in one verifier log.
func TestRepositoryLifecycle_DeployedEvidenceGate(t *testing.T) {
	if os.Getenv("REPOSITORY_LIFECYCLE_CAPACITY_RUN") != "1" {
		t.Skip("run through make capacity TARGET=repository PROFILE=lifecycle MODE=alpha or MODE=production against a deployed two-API/two-controller stack")
	}
	runRepositoryLifecycleCapacity(t)
	t.Run("RawGitPushAdmission", runRepositoryLifecyclePushAdmissionReadIdentity)
	t.Run("DualControllerRegistrationAndReconcile", runRepositoryLifecycleDualControllerRegistrationAndReconcile)
}

func validRepositoryFixture(name, namespace string) string {
	return fmt.Sprintf(`---
apiVersion: gitstore.dev/v1beta1
kind: Repository
metadata:
  name: %s
  namespace: %s
spec:
  defaultBranch: main
  visibility: PRIVATE
  storageClass: standard
---
`, name, namespace)
}

func waitForAdmittedRepository(t *testing.T, token, namespace, name string) *repositoryAdmissionResource {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response := gqlQueryWithURL(t, apiURL, token, `query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				id
				metadata { name namespace uid }
			}
		}`, map[string]any{"namespace": namespace, "name": name})
		if len(response.Errors) > 0 {
			if time.Now().After(deadline) || !repositoryNotFoundResponse(response.Errors) {
				require.Empty(t, response.Errors, "repository direct lookup GraphQL errors: %s", response.Errors)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		var data struct {
			Repository *repositoryAdmissionResource `json:"repository"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &data))
		if data.Repository != nil || time.Now().After(deadline) {
			require.NotNil(t, data.Repository, "Repository manifest was pushed but was not admitted")
			return data.Repository
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func repositoryNotFoundResponse(errors []json.RawMessage) bool {
	if len(errors) == 0 {
		return false
	}
	for _, raw := range errors {
		var item struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		}
		if json.Unmarshal(raw, &item) != nil || item.Extensions.Code != "NOT_FOUND" {
			return false
		}
	}
	return true
}

func waitForRepositoryReady(t *testing.T, token, namespace, name string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		response := gqlQueryWithURL(t, apiURL, token, `query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				metadata { generation }
				status { observedGeneration conditions { type status observedGeneration } }
			}
		}`, map[string]any{"namespace": namespace, "name": name})
		if len(response.Errors) > 0 && repositoryNotFoundResponse(response.Errors) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		require.Empty(t, response.Errors, "repository readiness GraphQL errors: %s", response.Errors)
		var data struct {
			Repository *struct {
				Metadata struct {
					Generation int `json:"generation"`
				} `json:"metadata"`
				Status struct {
					ObservedGeneration int `json:"observedGeneration"`
					Conditions         []struct {
						Type               string `json:"type"`
						Status             string `json:"status"`
						ObservedGeneration int    `json:"observedGeneration"`
					} `json:"conditions"`
				} `json:"status"`
			} `json:"repository"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &data))
		if data.Repository != nil && data.Repository.Metadata.Generation > 0 && data.Repository.Status.ObservedGeneration == data.Repository.Metadata.Generation {
			ready := false
			storage := false
			accepted := false
			for _, condition := range data.Repository.Status.Conditions {
				if condition.ObservedGeneration != data.Repository.Metadata.Generation {
					continue
				}
				ready = ready || condition.Type == "Ready" && condition.Status == "TRUE"
				storage = storage || condition.Type == "StorageProvisioned" && condition.Status == "TRUE"
				accepted = accepted || condition.Type == "AdmissionAccepted" && condition.Status == "TRUE"
			}
			if ready && storage && accepted {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Repository %s/%s did not become StorageProvisioned=True and Ready=True", namespace, name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func requireRepositoryController(t *testing.T, client *http.Client, endpoint string) float64 {
	t.Helper()
	response, err := client.Get(endpoint + "/health")
	require.NoError(t, err)
	defer response.Body.Close()
	contents, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode, string(contents))
	var health struct {
		Kinds map[string]struct {
			Registered bool `json:"registered"`
		} `json:"kinds"`
		CredentialReady bool `json:"credentialReady"`
		Ready           bool `json:"ready"`
	}
	require.NoError(t, json.Unmarshal(contents, &health))
	require.True(t, health.Ready, "%s controller is not ready", endpoint)
	require.True(t, health.CredentialReady, "%s credential source is not ready", endpoint)
	require.True(t, health.Kinds["Repository"].Registered, "%s has no Repository registration", endpoint)
	return repositoryControllerSuccessMetric(t, client, endpoint)
}

func TestRepositoryControllerReadiness(t *testing.T) {
	if endpoint := os.Getenv("TEST_REPOSITORY_CONTROLLER_ENDPOINT"); endpoint != "" {
		client := &http.Client{Timeout: time.Second}
		defer client.CloseIdleConnections()
		if os.Getenv("TEST_REPOSITORY_CONTROLLER_PHASE") == "progress" {
			waitForRepositoryControllerAdvance(t, client, endpoint, 0)
		} else {
			requireRepositoryController(t, client, endpoint)
		}
		return
	}
	for _, phase := range []string{"initial", "progress"} {
		for _, tc := range []struct {
			name, body, wantError string
			status                int
		}{
			{"recovering", `{"ready":false,"credentialReady":true,"kinds":{"Repository":{"registered":true}}}`, "controller is not ready", http.StatusOK},
			{"missing", `{"credentialReady":true,"kinds":{"Repository":{"registered":true}}}`, "controller is not ready", http.StatusOK},
			{"non-boolean", `{"ready":"true","credentialReady":true,"kinds":{"Repository":{"registered":true}}}`, "cannot unmarshal string", http.StatusOK},
			{"credentials", `{"ready":true,"credentialReady":false,"kinds":{"Repository":{"registered":true}}}`, "credential source is not ready", http.StatusOK},
			{"registration", `{"ready":true,"credentialReady":true,"kinds":{"Repository":{"registered":false}}}`, "has no Repository registration", http.StatusOK},
			{"http-status", `{"ready":true,"credentialReady":true,"kinds":{"Repository":{"registered":true}}}`, "Not equal", http.StatusServiceUnavailable},
			{"malformed", `not json`, "invalid character", http.StatusOK},
			{"healthy", `{"ready":true,"credentialReady":true,"kinds":{"Repository":{"registered":true}}}`, "", http.StatusOK},
		} {
			t.Run(phase+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/health":
						w.WriteHeader(tc.status)
						_, _ = io.WriteString(w, tc.body)
					case "/metrics":
						_, _ = io.WriteString(w, "gitstore_controller_reconcile_total{kind=\"Repository\",result=\"success\"} 2\n")
					default:
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRepositoryControllerReadiness$", "-test.timeout=5s")
				command.Env = append(os.Environ(), "TEST_REPOSITORY_CONTROLLER_ENDPOINT="+server.URL, "TEST_REPOSITORY_CONTROLLER_PHASE="+phase)
				output, err := command.CombinedOutput()
				if tc.wantError == "" {
					require.NoError(t, err, "%s", output)
				} else {
					require.Error(t, err, "invalid controller passed the %s gate: %s", phase, output)
					require.Contains(t, string(output), tc.wantError)
					require.NotContains(t, string(output), "test timed out")
				}
			})
		}
	}
}

func repositoryControllerSuccessMetric(t *testing.T, client *http.Client, endpoint string) float64 {
	t.Helper()
	response, err := client.Get(endpoint + "/metrics")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	scanner := bufio.NewScanner(response.Body)
	const prefix = `gitstore_controller_reconcile_total{kind="Repository",result="success"} `
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), prefix) {
			value, parseErr := strconv.ParseFloat(strings.TrimPrefix(scanner.Text(), prefix), 64)
			require.NoError(t, parseErr)
			return value
		}
	}
	require.NoError(t, scanner.Err())
	return 0
}

func repositoryControllerIdentity(t *testing.T, client *http.Client, endpoint string) string {
	t.Helper()
	response, err := client.Get(endpoint + "/metrics")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	scanner := bufio.NewScanner(response.Body)
	const prefix = `gitstore_controller_process_instance_info{instance_id="`
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, prefix) {
			identity, _, found := strings.Cut(strings.TrimPrefix(line, prefix), `"}`)
			require.True(t, found, "malformed controller process identity metric from %s", endpoint)
			require.NotEmpty(t, identity)
			return identity
		}
	}
	require.NoError(t, scanner.Err())
	t.Fatalf("%s does not expose gitstore_controller_process_instance_info", endpoint)
	return ""
}

func TestRepositoryControllerIdentityReadsCollisionSafeMetric(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "# HELP gitstore_controller_process_instance_info Collision-safe identity.\n"+
			"gitstore_controller_process_instance_info{instance_id=\"controller-process-a\"} 1\n")
	}))
	defer server.Close()

	require.Equal(t, "controller-process-a", repositoryControllerIdentity(t, server.Client(), server.URL))
}

func waitForRepositoryControllerAdvance(t *testing.T, client *http.Client, endpoint string, before float64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if repositoryControllerSuccessMetric(t, client, endpoint) > before {
			requireRepositoryController(t, client, endpoint)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not record a successful Repository reconciliation after the push", endpoint)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func repositoryControllerPoison(t *testing.T, client *http.Client, endpoint string) []json.RawMessage {
	t.Helper()
	response, err := client.Get(endpoint + "/controller/v1/poison/Repository")
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var items []json.RawMessage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&items))
	return items
}

func repositoryAdmissionQueryNode(t *testing.T, token, id string) *repositoryAdmissionResource {
	t.Helper()
	response := gqlQueryWithURL(t, apiURL, token, `query($id: ID!) {
		node(id: $id) {
			... on Repository {
				id
				metadata { name namespace uid }
			}
		}
	}`, map[string]any{"id": id})
	require.Empty(t, response.Errors, "Repository node(id:) GraphQL errors: %s", response.Errors)
	var data struct {
		Node *repositoryAdmissionResource `json:"node"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.NotNil(t, data.Node)
	return data.Node
}

func repositoryAdmissionQueryConnection(t *testing.T, token, namespace string) []*repositoryAdmissionResource {
	t.Helper()
	response := gqlQueryWithURL(t, apiURL, token, `query($namespace: String!) {
		repositories(namespace: $namespace, first: 100) {
			edges { node { id metadata { name namespace uid } } }
		}
	}`, map[string]any{"namespace": namespace})
	require.Empty(t, response.Errors, "Repository connection GraphQL errors: %s", response.Errors)
	var data struct {
		Repositories struct {
			Edges []struct {
				Node *repositoryAdmissionResource `json:"node"`
			} `json:"edges"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	repositories := make([]*repositoryAdmissionResource, 0, len(data.Repositories.Edges))
	for _, edge := range data.Repositories.Edges {
		if edge.Node != nil {
			repositories = append(repositories, edge.Node)
		}
	}
	return repositories
}
