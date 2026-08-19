// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

const namespaceContractSystemRepository = "gitstore-system"

var namespaceContractServer struct {
	mu     sync.Mutex
	refs   int
	apiURL string
	cmd    *exec.Cmd
	logs   bytes.Buffer
}

type namespaceContractHarness struct {
	t      *testing.T
	apiURL string
	token  string
}

type namespaceContractNamespace struct {
	ID         string                           `json:"id"`
	APIVersion string                           `json:"apiVersion"`
	Kind       string                           `json:"kind"`
	Metadata   *namespaceContractNamespaceMeta  `json:"metadata"`
	Spec       *namespaceContractNamespaceSpec  `json:"spec"`
	Status     *namespaceContractNamespaceState `json:"status"`

	Identifier  string  `json:"identifier"`
	DisplayName *string `json:"displayName"`
	Tier        string  `json:"tier"`
	CreatedAt   string  `json:"createdAt"`
	CreatedBy   string  `json:"createdBy"`
	UpdatedAt   string  `json:"updatedAt"`
	UpdatedBy   string  `json:"updatedBy"`
}

type namespaceContractNamespaceMeta struct {
	Name              string   `json:"name"`
	UID               string   `json:"uid"`
	ResourceVersion   string   `json:"resourceVersion"`
	Generation        int      `json:"generation"`
	CreationTimestamp string   `json:"creationTimestamp"`
	Revision          *string  `json:"revision"`
	Finalizers        []string `json:"finalizers"`
}

type namespaceContractNamespaceSpec struct {
	Title              *string                              `json:"title"`
	Tier               string                               `json:"tier"`
	RepositoryDefaults *namespaceContractRepositoryDefaults `json:"repositoryDefaults"`
	PushPolicyDefaults *namespaceContractPushPolicyDefaults `json:"pushPolicyDefaults"`
}

type namespaceContractRepositoryDefaults struct {
	Visibility    *string `json:"visibility"`
	DefaultBranch *string `json:"defaultBranch"`
}

type namespaceContractPushPolicyDefaults struct {
	MaxPackSizeBytes *int64 `json:"maxPackSizeBytes"`
	MaxFileSizeBytes *int64 `json:"maxFileSizeBytes"`
}

type namespaceContractNamespaceState struct {
	ObservedGeneration  int                                `json:"observedGeneration"`
	LastAppliedRevision *string                            `json:"lastAppliedRevision"`
	Conditions          []namespaceContractStatusCondition `json:"conditions"`
}

type namespaceContractStatusCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

func newNamespaceContractHarness(t *testing.T) *namespaceContractHarness {
	t.Helper()
	apiURL := acquireNamespaceContractAPI(t)
	return &namespaceContractHarness{
		t:      t,
		apiURL: apiURL,
		token:  namespaceContractBootstrapToken(t, apiURL),
	}
}

func acquireNamespaceContractAPI(t *testing.T) string {
	t.Helper()
	namespaceContractServer.mu.Lock()
	defer namespaceContractServer.mu.Unlock()

	if namespaceContractServer.cmd == nil {
		apiURL, cmd, logs := startNamespaceContractAPIServer(t)
		namespaceContractServer.apiURL = apiURL
		namespaceContractServer.cmd = cmd
		namespaceContractServer.logs = *logs
	}
	namespaceContractServer.refs++
	t.Cleanup(func() {
		releaseNamespaceContractAPI(t)
	})
	return namespaceContractServer.apiURL
}

func releaseNamespaceContractAPI(t *testing.T) {
	t.Helper()
	namespaceContractServer.mu.Lock()
	defer namespaceContractServer.mu.Unlock()

	if namespaceContractServer.refs > 0 {
		namespaceContractServer.refs--
	}
	if namespaceContractServer.refs != 0 || namespaceContractServer.cmd == nil {
		return
	}

	cmd := namespaceContractServer.cmd
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	namespaceContractServer.apiURL = ""
	namespaceContractServer.cmd = nil
	namespaceContractServer.logs.Reset()
}

func startNamespaceContractAPIServer(t *testing.T) (string, *exec.Cmd, *bytes.Buffer) {
	t.Helper()

	port := namespaceContractFreePort(t)
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	apiDir := filepath.Join(repoRoot, "gitstore-api")
	apiURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	helperPath := filepath.Join(apiDir, "namespace_contract_server_helper.go")
	helperBinary := filepath.Join(apiDir, "namespace_contract_server_helper_bin")
	source := `package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/app"
	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticadmin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type mockGitWriter struct {
	mu sync.Mutex
}

func (m *mockGitWriter) CommitFile(_ context.Context, _ gitclient.CommitFileParams) (string, error) {
	return "deadbeef", nil
}

func (m *mockGitWriter) CommitFileForRepo(_ context.Context, _ string, _ gitclient.CommitFileParams) (string, error) {
	return "deadbeef", nil
}

func (m *mockGitWriter) DeleteFile(_ context.Context, _ gitclient.DeleteFileParams) (string, error) {
	return "cafe1234", nil
}

func (m *mockGitWriter) CreateTag(_ context.Context, _ gitclient.CreateTagParams) (string, error) {
	return "tag123", nil
}

func (m *mockGitWriter) CreateRepository(_ context.Context, repositoryID, _ string) (string, error) {
	return "/data/00/00/" + repositoryID + ".git", nil
}

func (m *mockGitWriter) DeleteRepository(_ context.Context, _ string) error {
	return nil
}

func main() {
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	cfg := config.AuthConfig{
		Admin: config.UserConfig{
			Username: "admin",
			Password: string(hash),
		},
		JWT: config.JWTConfig{
			Secret:   "namespace-contract-secret",
			Issuer:   "gitstore",
			Duration: "2h",
		},
	}
	staticAdmin, err := staticadmin.New(cfg, zap.NewNop())
	if err != nil {
		panic(err)
	}
	defer staticAdmin.Shutdown()

	registry := authpkg.NewProviderRegistry(
		authpkg.NewChainedAuthN(staticAdmin, anonymous.New()),
		allowall.New(zap.NewNop()),
		nil,
	)
	store, err := memdb.New()
	if err != nil {
		panic(err)
	}
	ids := apiruntime.NewSequenceIDGenerator()
	now := time.Now().UTC()
	systemNamespace := &datastore.Namespace{
		ID:                ids.NewID(),
		Name:              "gitstore-system",
		Title:             "GitStore System",
		Tier:              datastore.NamespaceTierOrganization,
		CreationTimestamp: now,
		CreationActor:     "system",
		UpdateTimestamp:   now,
		UpdateActor:       "system",
	}
	if err := store.CreateNamespace(context.Background(), systemNamespace); err != nil {
		panic(err)
	}
	systemRepository := &datastore.Repository{
		ID:                ids.NewID(),
		NamespaceID:       systemNamespace.ID,
		Name:              "gitstore-system",
		DefaultBranch:     "main",
		StorageClass:      "system",
		CreationTimestamp: now,
		CreationActor:     "system",
		UpdateTimestamp:   now,
		UpdateActor:       "system",
	}
	if err := store.CreateRepository(context.Background(), systemRepository); err != nil {
		panic(err)
	}
	if err := store.CreateNamespaceMapping(context.Background(), &datastore.NamespaceMapping{
		NamespaceID: systemNamespace.ID,
		Name:        systemRepository.Name,
		RepoID:      systemRepository.ID,
	}); err != nil {
		panic(err)
	}
	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{
		Store:     store,
		GitWriter: &mockGitWriter{},
		Logger:    zap.NewNop(),
		Registry:  registry,
		IDs:       ids,
	})
	if err != nil {
		panic(err)
	}
	server := &http.Server{
		Addr:    "127.0.0.1:" + os.Getenv("NAMESPACE_CONTRACT_TEST_PORT"),
		Handler: handler,
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(helperPath, []byte(source), 0o600); err != nil {
		t.Fatalf("write local GraphQL helper server: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Remove(helperPath)
		_ = os.Remove(helperBinary)
	})

	build := exec.Command("go", "build", "-o", helperBinary, "./namespace_contract_server_helper.go")
	build.Dir = apiDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local GraphQL helper server: %v\n%s", err, out)
	}

	cmd := exec.Command(helperBinary)
	cmd.Dir = apiDir
	cmd.Env = append(os.Environ(),
		"NAMESPACE_CONTRACT_TEST_PORT="+strconv.Itoa(port),
	)
	logs := &bytes.Buffer{}
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start current GraphQL API server: %v", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(apiURL + "/playground")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return apiURL, cmd, logs
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("current GraphQL API server did not become ready at %s\n%s", apiURL, logs.String())
	return "", nil, nil
}

func namespaceContractFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate local port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func namespaceContractBootstrapToken(t *testing.T, apiURL string) string {
	t.Helper()
	resp := gqlQueryWithURL(t, apiURL, "", `
		mutation($input: LoginInput!) {
			login(input: $input) {
				token {
					accessToken
					tokenType
				}
			}
		}
	`, map[string]any{"input": map[string]any{
		"username": "admin",
		"password": "admin123",
	}})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql login errors: %s", namespaceContractErrors(resp.Errors))
	}

	var data struct {
		Login *struct {
			Token struct {
				AccessToken string `json:"accessToken"`
				TokenType   string `json:"tokenType"`
			} `json:"token"`
		} `json:"login"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	if data.Login == nil || data.Login.Token.AccessToken == "" {
		t.Fatal("login returned no access token")
	}
	if data.Login.Token.TokenType != "Bearer" {
		t.Fatalf("login token type = %q, want %q", data.Login.Token.TokenType, "Bearer")
	}
	return data.Login.Token.AccessToken
}

func gqlQueryWithURL(t *testing.T, apiURL, token, query string, vars map[string]any) gqlResponse {
	t.Helper()
	body, err := json.Marshal(gqlRequest{Query: query, Variables: vars})
	if err != nil {
		t.Fatalf("marshal gql request: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, apiURL+"/graphql", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build graphql request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("graphql request failed: %v — is the stack up? (API_URL=%s)", err, apiURL)
	}
	defer resp.Body.Close()

	var gqlResp gqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&gqlResp); err != nil {
		t.Fatalf("decode gql response: %v", err)
	}
	return gqlResp
}

func (h *namespaceContractHarness) gql(query string, vars map[string]any) gqlResponse {
	h.t.Helper()
	return gqlQueryWithURL(h.t, h.apiURL, h.token, query, vars)
}

func (h *namespaceContractHarness) gqlAnonymous(query string, vars map[string]any) gqlResponse {
	h.t.Helper()
	return gqlQueryWithURL(h.t, h.apiURL, "", query, vars)
}

func namespaceContractErrors(errs []json.RawMessage) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([][]byte, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err)
	}
	return string(bytes.Join(parts, []byte("; ")))
}

func namespaceContractStringPtr(s string) *string {
	return &s
}

func assertNamespaceContractShape(t *testing.T, got *namespaceContractNamespace, identifier string, title *string, tier string) {
	t.Helper()
	if got == nil {
		t.Fatal("namespace payload is nil")
	}
	if got.APIVersion != "gitstore.dev/v1beta1" {
		t.Fatalf("apiVersion = %q, want %q", got.APIVersion, "gitstore.dev/v1beta1")
	}
	if got.Kind != "Namespace" {
		t.Fatalf("kind = %q, want %q", got.Kind, "Namespace")
	}
	if got.Metadata == nil {
		t.Fatal("metadata is nil")
	}
	if got.Metadata.Name != identifier {
		t.Fatalf("metadata.name = %q, want %q", got.Metadata.Name, identifier)
	}
	if got.Metadata.UID == "" {
		t.Fatal("metadata.uid is empty")
	}
	if got.Metadata.ResourceVersion != "1" {
		t.Fatalf("metadata.resourceVersion = %q, want %q", got.Metadata.ResourceVersion, "1")
	}
	if got.Metadata.Generation != 1 {
		t.Fatalf("metadata.generation = %d, want %d", got.Metadata.Generation, 1)
	}
	if got.Metadata.CreationTimestamp == "" {
		t.Fatal("metadata.creationTimestamp is empty")
	}
	if got.Spec == nil {
		t.Fatal("spec is nil")
	}
	if got.Spec.Tier != tier {
		t.Fatalf("spec.tier = %q, want %q", got.Spec.Tier, tier)
	}
	if title != nil {
		if got.Spec.Title == nil || *got.Spec.Title != *title {
			t.Fatalf("spec.title = %v, want %q", got.Spec.Title, *title)
		}
	}
	if got.Spec.RepositoryDefaults != nil {
		t.Fatalf("spec.repositoryDefaults = %+v, want nil", got.Spec.RepositoryDefaults)
	}
	if got.Spec.PushPolicyDefaults != nil {
		t.Fatalf("spec.pushPolicyDefaults = %+v, want nil", got.Spec.PushPolicyDefaults)
	}
	if got.Status == nil {
		t.Fatal("status is nil")
	}
	if got.Status.ObservedGeneration != 1 {
		t.Fatalf("status.observedGeneration = %d, want %d", got.Status.ObservedGeneration, 1)
	}
	if got.Status.LastAppliedRevision == nil || *got.Status.LastAppliedRevision != "main@sha1:deadbeef" {
		t.Fatalf("status.lastAppliedRevision = %v, want %q", got.Status.LastAppliedRevision, "main@sha1:deadbeef")
	}
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Type != "AdmissionAccepted" ||
		got.Status.Conditions[0].Status != "TRUE" {
		t.Fatalf("status.conditions = %+v, want AdmissionAccepted=TRUE", got.Status.Conditions)
	}
	if got.Identifier != identifier {
		t.Fatalf("identifier = %q, want %q", got.Identifier, identifier)
	}
	if title != nil {
		if got.DisplayName == nil || *got.DisplayName != *title {
			t.Fatalf("displayName = %v, want %q", got.DisplayName, *title)
		}
	}
	if got.Tier != tier {
		t.Fatalf("tier = %q, want %q", got.Tier, tier)
	}
	if got.CreatedAt == "" {
		t.Fatal("createdAt is empty")
	}
	if got.CreatedBy == "" {
		t.Fatal("createdBy is empty")
	}
	if got.UpdatedAt == "" {
		t.Fatal("updatedAt is empty")
	}
	if got.UpdatedBy == "" {
		t.Fatal("updatedBy is empty")
	}
}

func (h *namespaceContractHarness) cleanupNamespace(identifier string) {
	h.t.Helper()

	repoID, ok := h.lookupRepositoryID(identifier, namespaceContractSystemRepository)
	if ok {
		resp := h.gql(`
			mutation($repositoryID: ID!) {
				deleteRepository(input: {repositoryId: $repositoryID}) {
					deletedRepositoryId
				}
			}
		`, map[string]any{"repositoryID": repoID})
		if len(resp.Errors) > 0 {
			h.t.Logf("cleanup deleteRepository(%s/%s) errors: %s", identifier, namespaceContractSystemRepository, namespaceContractErrors(resp.Errors))
		}
	}

	resp := h.gql(`
		mutation($identifier: String!) {
			deleteNamespace(input: {identifier: $identifier}) {
				deletedIdentifier
			}
		}
	`, map[string]any{"identifier": identifier})
	if len(resp.Errors) > 0 {
		h.t.Logf("cleanup deleteNamespace(%s) errors: %s", identifier, namespaceContractErrors(resp.Errors))
	}
}

func (h *namespaceContractHarness) lookupRepositoryID(namespace, name string) (string, bool) {
	h.t.Helper()
	resp := h.gql(`
		query($namespace: String!, $name: String!) {
			repository(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				id
			}
		}
	`, map[string]any{"namespace": namespace, "name": name})
	if len(resp.Errors) > 0 {
		h.t.Logf("repository lookup errors for %s/%s: %s", namespace, name, namespaceContractErrors(resp.Errors))
		return "", false
	}

	var data struct {
		Repository *struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		h.t.Logf("unmarshal repository lookup for %s/%s: %v", namespace, name, err)
		return "", false
	}
	if data.Repository == nil || data.Repository.ID == "" {
		return "", false
	}
	return data.Repository.ID, true
}

func (h *namespaceContractHarness) requireDeleteSystemRepository(namespace string) {
	h.t.Helper()
	repoID, ok := h.lookupRepositoryID(namespace, namespaceContractSystemRepository)
	if !ok {
		h.t.Fatalf("system repository %q not found in namespace %q", namespaceContractSystemRepository, namespace)
	}

	resp := h.gql(`
		mutation($repositoryID: ID!) {
			deleteRepository(input: {repositoryId: $repositoryID}) {
				deletedRepositoryId
			}
		}
	`, map[string]any{"repositoryID": repoID})
	if len(resp.Errors) > 0 {
		h.t.Fatalf("graphql errors deleting system repository %s/%s: %s", namespace, namespaceContractSystemRepository, namespaceContractErrors(resp.Errors))
	}

	var data struct {
		DeleteRepository *struct {
			DeletedRepositoryID string `json:"deletedRepositoryId"`
		} `json:"deleteRepository"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		h.t.Fatalf("unmarshal deleteRepository response: %v", err)
	}
	if data.DeleteRepository == nil || data.DeleteRepository.DeletedRepositoryID != repoID {
		h.t.Fatalf("deleteRepository returned %+v, want deletedRepositoryId %q", data.DeleteRepository, repoID)
	}
}

func (h *namespaceContractHarness) createNamespace(identifier, title string) {
	h.t.Helper()
	resp := h.gql(`
		mutation($input: CreateNamespaceInput!) {
			createNamespace(input: $input) {
				namespace {
					metadata {
						name
					}
				}
			}
		}
	`, map[string]any{"input": map[string]any{
		"apiVersion": "gitstore.dev/v1beta1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": identifier},
		"spec":       map[string]any{"title": title, "tier": "USER"},
	}})
	if len(resp.Errors) > 0 {
		h.t.Fatalf("graphql errors creating namespace %q: %s", identifier, namespaceContractErrors(resp.Errors))
	}

	var data struct {
		CreateNamespace *struct {
			Namespace *struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"namespace"`
		} `json:"createNamespace"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		h.t.Fatalf("unmarshal createNamespace response: %v", err)
	}
	if data.CreateNamespace == nil || data.CreateNamespace.Namespace == nil || data.CreateNamespace.Namespace.Metadata.Name != identifier {
		h.t.Fatalf("createNamespace returned %+v, want metadata.name %q", data.CreateNamespace, identifier)
	}

	resp = h.gql(`
		mutation($namespace: String!, $name: String!, $defaultBranch: String!) {
			createRepository(input: {namespace: $namespace, name: $name, defaultBranch: $defaultBranch}) {
				repository { id }
			}
		}
	`, map[string]any{
		"namespace":     identifier,
		"name":          namespaceContractSystemRepository,
		"defaultBranch": "main",
	})
	if len(resp.Errors) > 0 {
		h.t.Fatalf("graphql errors provisioning system repository for %q: %s", identifier, namespaceContractErrors(resp.Errors))
	}
}

func TestNamespaceContract_QueryNamespaceProjectsDeclarativeFields(t *testing.T) {
	h := newNamespaceContractHarness(t)
	identifier := uniqueName("namespace-contract-query")
	title := namespaceContractStringPtr("Namespace Contract Query")
	h.createNamespace(identifier, *title)

	resp := h.gqlAnonymous(`
		query($identifier: String!) {
			namespace(by: {identifier: $identifier}) {
				id
				apiVersion
				kind
				metadata {
					name
					uid
					resourceVersion
					generation
					creationTimestamp
					revision
					finalizers
				}
				spec {
					title
					tier
					repositoryDefaults {
						visibility
						defaultBranch
					}
					pushPolicyDefaults {
						maxPackSizeBytes
						maxFileSizeBytes
					}
				}
				status {
					observedGeneration
					lastAppliedRevision
					conditions {
						type
						status
					}
				}
				identifier
				displayName
				tier
				createdAt
				createdBy
				updatedAt
				updatedBy
			}
		}
	`, map[string]any{"identifier": identifier})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors querying namespace contract: %s", namespaceContractErrors(resp.Errors))
	}

	var data struct {
		Namespace *namespaceContractNamespace `json:"namespace"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal namespace response: %v", err)
	}
	assertNamespaceContractShape(t, data.Namespace, identifier, title, "USER")
}

func TestNamespaceContract_NamespacesConnectionProjectsDeclarativeFields(t *testing.T) {
	h := newNamespaceContractHarness(t)
	identifier := uniqueName("namespace-contract-list")
	title := namespaceContractStringPtr("Namespace Contract List")
	h.createNamespace(identifier, *title)

	resp := h.gqlAnonymous(`
		query {
			namespaces(first: 20) {
				edges {
					node {
						id
						apiVersion
						kind
						metadata {
							name
							uid
							resourceVersion
							generation
							creationTimestamp
							revision
							finalizers
						}
						spec {
							title
							tier
							repositoryDefaults {
								visibility
								defaultBranch
							}
							pushPolicyDefaults {
								maxPackSizeBytes
								maxFileSizeBytes
							}
						}
						status {
							observedGeneration
							lastAppliedRevision
							conditions {
								type
								status
							}
						}
						identifier
						displayName
						tier
						createdAt
						createdBy
						updatedAt
						updatedBy
					}
				}
				pageInfo {
					hasNextPage
					endCursor
				}
				totalCount
			}
		}
	`, nil)
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors listing namespaces contract: %s", namespaceContractErrors(resp.Errors))
	}

	var data struct {
		Namespaces *struct {
			Edges []struct {
				Node *namespaceContractNamespace `json:"node"`
			} `json:"edges"`
			TotalCount int `json:"totalCount"`
		} `json:"namespaces"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal namespaces response: %v", err)
	}
	if data.Namespaces == nil {
		t.Fatal("namespaces connection is nil")
	}
	if data.Namespaces.TotalCount < 1 {
		t.Fatalf("namespaces.totalCount = %d, want at least 1", data.Namespaces.TotalCount)
	}

	for _, edge := range data.Namespaces.Edges {
		if edge.Node != nil && edge.Node.Metadata != nil && edge.Node.Metadata.Name == identifier {
			assertNamespaceContractShape(t, edge.Node, identifier, title, "USER")
			return
		}
	}
	t.Fatalf("namespace %q not found in namespaces connection", identifier)
}

func TestNamespaceContract_CreateNamespaceReturnsAdditiveContract(t *testing.T) {
	h := newNamespaceContractHarness(t)
	identifier := uniqueName("namespace-contract-create")
	title := "Namespace Contract Create"
	created := false
	t.Cleanup(func() {
		if created {
			h.cleanupNamespace(identifier)
		}
	})

	resp := h.gql(`
		mutation($input: CreateNamespaceInput!) {
			createNamespace(input: $input) {
				namespace {
					id
					apiVersion
					kind
					metadata {
						name
						uid
						resourceVersion
						generation
						creationTimestamp
						revision
						finalizers
					}
					spec {
						title
						tier
						repositoryDefaults {
							visibility
							defaultBranch
						}
						pushPolicyDefaults {
							maxPackSizeBytes
							maxFileSizeBytes
						}
					}
					status {
						observedGeneration
						lastAppliedRevision
						conditions {
							type
							status
						}
					}
					identifier
					displayName
					tier
					createdAt
					createdBy
					updatedAt
					updatedBy
				}
			}
		}
	`, map[string]any{"input": map[string]any{
		"apiVersion": "gitstore.dev/v1beta1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": identifier},
		"spec":       map[string]any{"title": title, "tier": "USER"},
	}})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors creating namespace additive contract: %s", namespaceContractErrors(resp.Errors))
	}

	var data struct {
		CreateNamespace *struct {
			Namespace *namespaceContractNamespace `json:"namespace"`
		} `json:"createNamespace"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal createNamespace response: %v", err)
	}
	if data.CreateNamespace == nil {
		t.Fatal("createNamespace payload is nil")
	}
	created = true
	assertNamespaceContractShape(t, data.CreateNamespace.Namespace, identifier, namespaceContractStringPtr(title), "USER")
}

func TestNamespaceContract_DeleteNamespaceBehaviorUnchanged(t *testing.T) {
	h := newNamespaceContractHarness(t)
	identifier := uniqueName("namespace-contract-delete")
	displayName := fmt.Sprintf("Delete %s", identifier)
	created := false
	t.Cleanup(func() {
		if created {
			h.cleanupNamespace(identifier)
		}
	})

	h.createNamespace(identifier, displayName)
	created = true
	h.requireDeleteSystemRepository(identifier)

	resp := h.gql(`
		mutation($identifier: String!) {
			deleteNamespace(input: {identifier: $identifier}) {
				deletedIdentifier
			}
		}
	`, map[string]any{"identifier": identifier})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors deleting namespace: %s", namespaceContractErrors(resp.Errors))
	}

	var data struct {
		DeleteNamespace *struct {
			DeletedIdentifier string `json:"deletedIdentifier"`
		} `json:"deleteNamespace"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal deleteNamespace response: %v", err)
	}
	if data.DeleteNamespace == nil {
		t.Fatal("deleteNamespace payload is nil")
	}
	if data.DeleteNamespace.DeletedIdentifier != identifier {
		t.Fatalf("deletedIdentifier = %q, want %q", data.DeleteNamespace.DeletedIdentifier, identifier)
	}
	created = false
}
