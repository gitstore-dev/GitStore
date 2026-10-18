// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const namespaceContractSystemRepository = "gitstore-system"

var namespaceContractServer struct {
	mu     sync.Mutex
	refs   int
	apiURL string
	cmd    *exec.Cmd
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
	Body       *string                          `json:"body"`
}

type namespaceContractNamespaceMeta struct {
	Name              string                            `json:"name"`
	Labels            map[string]any                    `json:"labels"`
	Annotations       map[string]any                    `json:"annotations"`
	UID               string                            `json:"uid"`
	ResourceVersion   string                            `json:"resourceVersion"`
	Generation        int                               `json:"generation"`
	CreationTimestamp string                            `json:"creationTimestamp"`
	Revision          *string                           `json:"revision"`
	OwnerReferences   []namespaceContractOwnerReference `json:"ownerReferences"`
	Finalizers        []string                          `json:"finalizers"`
}

type namespaceContractOwnerReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
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
		apiURL, cmd, _ := startNamespaceContractAPIServer(t)
		namespaceContractServer.apiURL = apiURL
		namespaceContractServer.cmd = cmd
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
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/app"
	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/anonymous"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountassertion"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountjwt"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type mockGitWriter struct {
	mu sync.Mutex
	seq int
	heads map[string]string
	trees map[string]map[string][]byte
	parents map[string]string
}

var _ cataloggrpc.GitReader = (*mockGitWriter)(nil)

// namespaceOwnerAuthZ mirrors namespace-scoped authorization: a non-admin
// subject may act on categories only in a namespace it owns.
type namespaceOwnerAuthZ struct {
	store datastore.Datastore
}

func (*namespaceOwnerAuthZ) Name() string { return "namespace-owner-test" }

func (n *namespaceOwnerAuthZ) Authorize(
	ctx context.Context,
	principal *authpkg.Principal,
	_ string,
	resource authpkg.ResourceContext,
) (authpkg.Decision, error) {
	if principal.Subject == "admin" {
		return authpkg.Allow("namespace-owner-test", "administrator"), nil
	}
	if namespace, _ := resource.Attrs["namespace"].(string); resource.Kind == "categoryTaxonomy" && namespace != "" {
		if ns, err := n.store.GetNamespaceByName(ctx, namespace); err == nil && ns.EffectiveOwnerSub() != "" && ns.EffectiveOwnerSub() != principal.Subject {
			return authpkg.Deny("namespace-owner-test", "namespace belongs to another user"), nil
		}
	}
	if resource.OwnerSub != "" && resource.OwnerSub != principal.Subject {
		return authpkg.Deny("namespace-owner-test", "resource belongs to another user"), nil
	}
	return authpkg.Allow("namespace-owner-test", "owner or unowned resource"), nil
}

func (m *mockGitWriter) CommitFile(ctx context.Context, p gitclient.CommitFileParams) (string, error) {
	return m.CommitFileForRepo(ctx, "", p)
}

func (m *mockGitWriter) write(repo, path string, content []byte) string {
	if m.heads == nil { m.heads = map[string]string{}; m.trees = map[string]map[string][]byte{}; m.parents = map[string]string{} }
	tree := map[string][]byte{}
	for path, content := range m.trees[m.heads[repo]] { tree[path] = content }
	if content == nil { delete(tree, path) } else { tree[path] = append([]byte(nil), content...) }
	m.seq++
	commit := fmt.Sprintf("%040x", m.seq)
	m.parents[commit] = m.heads[repo]
	m.trees[commit], m.heads[repo] = tree, commit
	return commit
}

func (m *mockGitWriter) CommitFileForRepo(_ context.Context, repo string, p gitclient.CommitFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.write(repo, p.Path, p.Content), nil
}

func (m *mockGitWriter) DeleteFileForRepo(_ context.Context, repo string, p gitclient.DeleteFileParams) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.ExpectedCommitSHA != "" && p.ExpectedCommitSHA != m.heads[repo] { return "", status.Error(codes.Aborted, "revision changed") }
	return m.write(repo, p.Path, nil), nil
}

func (m *mockGitWriter) ResolveRefForRepo(_ context.Context, repo string, _ string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.heads[repo], nil
}

func (m *mockGitWriter) ReadFileForRepo(_ context.Context, repo, path, ref string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(ref, "refs/") || ref == "HEAD" { ref = m.heads[repo] }
	content, ok := m.trees[ref][path]
	if !ok { return nil, status.Error(codes.NotFound, "file not found") }
	return append([]byte(nil), content...), nil
}

func (m *mockGitWriter) ListFiles(_ context.Context, repo, prefix, ref string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(ref, "refs/") || ref == "HEAD" { ref = m.heads[repo] }
	var paths []string
	for path := range m.trees[ref] { if strings.HasPrefix(path, prefix) { paths = append(paths, path) } }
	sort.Strings(paths)
	return paths, nil
}

func (m *mockGitWriter) ReadFile(ctx context.Context, repo, path, ref string) ([]byte, error) {
	return m.ReadFileForRepo(ctx, repo, path, ref)
}

func (m *mockGitWriter) readFilesLocked(repo string, paths []string, ref string) [][]byte {
	if strings.HasPrefix(ref, "refs/") || ref == "HEAD" { ref = m.heads[repo] }
	result := make([][]byte, len(paths))
	for i, path := range paths {
		if content, ok := m.trees[ref][path]; ok {
			result[i] = make([]byte, len(content))
			copy(result[i], content)
		}
	}
	return result
}

func (m *mockGitWriter) ReadFiles(ctx context.Context, repo string, paths []string, ref string) ([][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil { return nil, err }
	return m.readFilesLocked(repo, paths, ref), nil
}

func (m *mockGitWriter) ReadFilePair(ctx context.Context, repo string, paths []string, primaryCommit, secondaryCommit, refName string) ([][]byte, [][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil { return nil, nil, err }
	if refName != "" && m.heads[repo] != secondaryCommit { return nil, nil, status.Error(codes.Aborted, "revision changed") }
	return m.readFilesLocked(repo, paths, primaryCommit), m.readFilesLocked(repo, paths, secondaryCommit), nil
}

func (m *mockGitWriter) ReadCurrentUnchangedFile(ctx context.Context, repo, path, refName, ancestor string) (bool, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil { return false, nil, err }
	current := m.heads[repo]
	if current == ancestor { return true, nil, nil }
	content, present := m.trees[current][path]
	if !present { return false, nil, status.Error(codes.NotFound, "file not found") }
	for commit := current; commit != ""; commit = m.parents[commit] {
		if err := ctx.Err(); err != nil { return false, nil, err }
		other, present := m.trees[commit][path]
		if !present || !bytes.Equal(content, other) { return false, nil, status.Error(codes.Aborted, "path changed in history") }
		if commit == ancestor { return false, append([]byte(nil), content...), nil }
	}
	return false, nil, status.Error(codes.Aborted, "ancestor is not reachable")
}

func (m *mockGitWriter) ResolveRef(ctx context.Context, repo, ref string) (string, error) {
	return m.ResolveRefForRepo(ctx, repo, ref)
}

func (m *mockGitWriter) DeleteFile(ctx context.Context, p gitclient.DeleteFileParams) (string, error) {
	return m.DeleteFileForRepo(ctx, "", p)
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
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	signingKeyDER, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		panic(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("admin123"), bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	usersFile := filepath.Join(os.TempDir(), fmt.Sprintf("gitstore-namespace-contract-users-%d.yaml", os.Getpid()))
	userHash := string(hash)
	usersYAML := "version: v1\nusers:\n" +
		"  - username: admin\n    password_hash: \"" + userHash + "\"\n" +
		"  - username: alice\n    password_hash: \"" + userHash + "\"\n" +
		"  - username: bob\n    password_hash: \"" + userHash + "\"\n"
	if err := os.WriteFile(usersFile, []byte(usersYAML), 0600); err != nil {
		panic(err)
	}
	cfg := config.AuthConfig{
		StaticUsers: config.StaticUsersConfig{UsersFile: usersFile},
		JWT: config.JWTConfig{
			Secret:   "namespace-contract-secret",
			Issuer:   "gitstore",
			TTL:    2 * time.Hour,
		},
		ServiceAccount: config.ServiceAccountConfig{
			Audience:   "gitstore-api",
			SigningKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signingKeyDER})),
		},
	}
	staticUsers, err := staticusers.New(cfg, zap.NewNop())
	if err != nil {
		panic(err)
	}
	defer staticUsers.Shutdown()

	store, err := memdb.New()
	if err != nil {
		panic(err)
	}
	assertionProvider, err := serviceaccountassertion.New(cfg.ServiceAccount, store, zap.NewNop())
	if err != nil {
		panic(err)
	}
	accessTokenProvider, err := serviceaccountjwt.New(cfg.ServiceAccount, store, zap.NewNop())
	if err != nil {
		panic(err)
	}
	defer accessTokenProvider.Shutdown()
	registry := authpkg.NewProviderRegistry(
		authpkg.NewChainedAuthN(staticUsers, assertionProvider, accessTokenProvider, anonymous.New()),
		&namespaceOwnerAuthZ{store: store},
		nil,
	)
	ids := apiruntime.NewSequenceIDGenerator()
	now := time.Now().UTC()
	systemNamespace := &datastore.Namespace{
		UID:               ids.NewID(),
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
		UID:               ids.NewID(),
		Namespace:         systemNamespace.Name,
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
		Namespace:    systemNamespace.Name,
		Name:         systemRepository.Name,
		RepositoryID: systemRepository.UID,
	}); err != nil {
		panic(err)
	}
	gitWriter := &mockGitWriter{}
	committedAdmitter, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Registry:    registry,
		Store:       store,
		GitReader:   gitWriter,
		Logger:      zap.NewNop(),
		IDGenerator: ids,
	})
	if err != nil {
		panic(err)
	}
	handler, err := app.NewGraphQLHandler(app.GraphQLHandlerDeps{
		Store:     store,
		GitWriter: gitWriter,
		Logger:    zap.NewNop(),
		Registry:  registry,
		IDs:       ids,
		CommittedManifestAdmitter: committedAdmitter,
		ServiceAccountAudience: cfg.ServiceAccount.Audience,
		ConnectionRegistry: wsregistry.New(),
	})
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", handler)
	mux.HandleFunc("/__test/resource-body", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		namespace := r.URL.Query().Get("namespace")
		name := r.URL.Query().Get("name")
		switch r.URL.Query().Get("kind") {
		case "Namespace":
			resource, err := store.GetNamespaceByName(r.Context(), name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			expected := resource.ResourceVersion
			resource.Body = string(body)
			datastore.AdvanceNamespaceSpecVersion(resource)
			err = store.UpdateNamespace(r.Context(), resource, expected)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		case "Repository":
			mapping, err := store.LookupRepository(r.Context(), namespace, name)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			resource, err := store.GetRepository(r.Context(), mapping.RepositoryID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			expected := resource.ResourceVersion
			resource.Body = string(body)
			datastore.AdvanceRepositorySpecVersion(resource)
			err = store.UpdateRepository(r.Context(), resource, expected)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		default:
			http.Error(w, "unknown kind", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{
		Addr:    "127.0.0.1:" + os.Getenv("NAMESPACE_CONTRACT_TEST_PORT"),
		Handler: mux,
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
	return namespaceContractLoginURL(t, apiURL, "admin", "admin123")
}

func namespaceContractLogin(t *testing.T, h *namespaceContractHarness, username, password string) string {
	t.Helper()
	return namespaceContractLoginURL(t, h.apiURL, username, password)
}

func namespaceContractLoginURL(t *testing.T, apiURL, username, password string) string {
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
		"username": username,
		"password": password,
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
	gqlResp.Status = resp.StatusCode
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

func (h *namespaceContractHarness) gqlWithToken(token, query string, vars map[string]any) gqlResponse {
	h.t.Helper()
	return gqlQueryWithURL(h.t, h.apiURL, token, query, vars)
}

func (h *namespaceContractHarness) setResourceBody(kind, namespace, name, body string) {
	h.t.Helper()
	endpoint := h.apiURL + "/__test/resource-body?kind=" + url.QueryEscape(kind) +
		"&namespace=" + url.QueryEscape(namespace) + "&name=" + url.QueryEscape(name)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
	if err != nil {
		h.t.Fatalf("build body fixture request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("set %s body: %v", kind, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		h.t.Fatalf("set %s body status = %d, want %d", kind, resp.StatusCode, http.StatusNoContent)
	}
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
	require.NotNil(t, got.Status.LastAppliedRevision)
	require.Regexp(t, `^main@sha1:[0-9a-f]{40}$`, *got.Status.LastAppliedRevision)
	if len(got.Status.Conditions) != 1 ||
		got.Status.Conditions[0].Type != "AdmissionAccepted" ||
		got.Status.Conditions[0].Status != "TRUE" {
		t.Fatalf("status.conditions = %+v, want AdmissionAccepted=TRUE", got.Status.Conditions)
	}
}

func (h *namespaceContractHarness) cleanupNamespace(identifier string) {
	h.t.Helper()

	namespaceID, ok := h.lookupNamespaceID(identifier)
	if !ok {
		return
	}
	resp := h.gql(`mutation($id: ID!) { deleteNamespace(input: {id: $id}) { namespace { id } outcome } }`, map[string]any{"id": namespaceID})
	if len(resp.Errors) > 0 {
		h.t.Logf("cleanup deleteNamespace(%s) errors: %s", identifier, namespaceContractErrors(resp.Errors))
	}
}

func (h *namespaceContractHarness) lookupNamespaceID(identifier string) (string, bool) {
	h.t.Helper()
	resp := h.gql(`query($name: String!) { namespace(by: {name: $name}) { id } }`, map[string]any{"name": identifier})
	if len(resp.Errors) > 0 {
		return "", false
	}
	var data struct {
		Namespace *struct {
			ID string `json:"id"`
		} `json:"namespace"`
	}
	if json.Unmarshal(resp.Data, &data) != nil || data.Namespace == nil || data.Namespace.ID == "" {
		return "", false
	}
	return data.Namespace.ID, true
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

func (h *namespaceContractHarness) requireSystemDeletionRejected(namespace string) {
	h.t.Helper()
	repoID, ok := h.lookupRepositoryID(namespace, namespaceContractSystemRepository)
	if !ok {
		h.t.Fatalf("system repository %q not found in namespace %q", namespaceContractSystemRepository, namespace)
	}

	if errors := h.deleteRepositoryAndComplete(repoID); len(errors) == 0 {
		h.t.Fatal("direct system repository deletion unexpectedly succeeded")
	}
}

func (h *namespaceContractHarness) deleteRepositoryAndComplete(repoID string) []json.RawMessage {
	h.t.Helper()
	resp := h.gql(`
		mutation($repositoryID: ID!) {
			deleteRepository(input: {id: $repositoryID}) {
				repository { id }
			}
		}
	`, map[string]any{"repositoryID": repoID})
	if len(resp.Errors) > 0 {
		return resp.Errors
	}

	var data struct {
		DeleteRepository *struct {
			Repository *struct {
				ID string `json:"id"`
			} `json:"repository"`
		} `json:"deleteRepository"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"message":%q}`, err.Error()))}
	}
	if data.DeleteRepository == nil || data.DeleteRepository.Repository == nil || data.DeleteRepository.Repository.ID != repoID {
		return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"message":%q}`, fmt.Sprintf("deleteRepository returned %+v, want repository.id %q", data.DeleteRepository, repoID)))}
	}

	resp = h.gql(`
		query($repositoryID: ID!) {
			repository(by: {id: $repositoryID}) {
				metadata { namespace name resourceVersion }
			}
		}
	`, map[string]any{"repositoryID": repoID})
	if len(resp.Errors) > 0 {
		return resp.Errors
	}
	var current struct {
		Repository struct {
			Metadata struct {
				Namespace       string `json:"namespace"`
				Name            string `json:"name"`
				ResourceVersion string `json:"resourceVersion"`
			} `json:"metadata"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(resp.Data, &current); err != nil {
		return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"message":%q}`, err.Error()))}
	}
	resp = h.gql(`
		mutation($namespace: String!, $name: String!, $resourceVersion: String!, $uid: ID!) {
			completeRepositoryDeletion(input: {
				namespace: $namespace
				name: $name
				resourceVersion: $resourceVersion
				id: $uid
			}) { id }
		}
	`, map[string]any{
		"namespace":       current.Repository.Metadata.Namespace,
		"name":            current.Repository.Metadata.Name,
		"resourceVersion": current.Repository.Metadata.ResourceVersion,
		"uid":             repoID,
	})
	return resp.Errors
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
		"spec":       map[string]any{"title": title, "tier": "ORGANIZATION"},
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
		mutation($namespace: String!) {
			provisionNamespaceSystemRepository(input: {namespace: $namespace}) {
				repository { id }
			}
		}
	`, map[string]any{
		"namespace": identifier,
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

	resp := h.gql(`
		query($identifier: String!) {
			namespace(by: {name: $identifier}) {
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
	assertNamespaceContractShape(t, data.Namespace, identifier, title, "ORGANIZATION")
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
					}
				}
				pageInfo {
					hasNextPage
					endCursor
				}
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
		} `json:"namespaces"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal namespaces response: %v", err)
	}
	if data.Namespaces == nil {
		t.Fatal("namespaces connection is nil")
	}
	if len(data.Namespaces.Edges) < 1 {
		t.Fatalf("namespaces.edges = %d, want at least 1", len(data.Namespaces.Edges))
	}

	for _, edge := range data.Namespaces.Edges {
		if edge.Node != nil && edge.Node.Metadata != nil && edge.Node.Metadata.Name == identifier {
			assertNamespaceContractShape(t, edge.Node, identifier, title, "ORGANIZATION")
			return
		}
	}
	t.Fatalf("namespace %q not found in namespaces connection", identifier)
}

func TestNamespaceContract_DirectAndConnectionEnvelopeBodyParity(t *testing.T) {
	h := newNamespaceContractHarness(t)
	identifier := uniqueName("namespace-envelope-parity")
	h.createNamespace(identifier, "Namespace Envelope Parity")
	t.Cleanup(func() {
		h.cleanupNamespace(identifier)
	})
	body := "# Namespace body\n\nRaw **Markdown** is preserved.\n"
	h.setResourceBody("Namespace", "", identifier, body)

	selection := `{
		id
		apiVersion
		kind
		metadata {
			name
			labels
			annotations
			uid
			resourceVersion
			generation
			creationTimestamp
			revision
			ownerReferences {
				apiVersion
				kind
				name
				uid
			}
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
		body
	}`

	directResponse := h.gql(
		`query($identifier: String!) {
			namespace(by: {name: $identifier}) `+selection+`
		}`,
		map[string]any{"identifier": identifier},
	)
	if len(directResponse.Errors) > 0 {
		t.Fatalf("graphql errors querying namespace parity: %s", namespaceContractErrors(directResponse.Errors))
	}
	var directData struct {
		Namespace *namespaceContractNamespace `json:"namespace"`
	}
	if err := json.Unmarshal(directResponse.Data, &directData); err != nil {
		t.Fatalf("unmarshal direct namespace parity response: %v", err)
	}
	if directData.Namespace == nil {
		t.Fatal("direct namespace is nil")
	}
	if directData.Namespace.Metadata == nil {
		t.Fatal("direct namespace metadata is nil")
	}

	connectionResponse := h.gql(
		`query {
			namespaces(first: 100) {
				edges {
					node `+selection+`
				}
			}
		}`,
		nil,
	)
	if len(connectionResponse.Errors) > 0 {
		t.Fatalf("graphql errors querying namespace connection parity: %s", namespaceContractErrors(connectionResponse.Errors))
	}
	var connectionData struct {
		Namespaces struct {
			Edges []struct {
				Node *namespaceContractNamespace `json:"node"`
			} `json:"edges"`
		} `json:"namespaces"`
	}
	if err := json.Unmarshal(connectionResponse.Data, &connectionData); err != nil {
		t.Fatalf("unmarshal namespace connection parity response: %v", err)
	}
	var connected *namespaceContractNamespace
	for _, edge := range connectionData.Namespaces.Edges {
		if edge.Node != nil && edge.Node.Metadata != nil && edge.Node.Metadata.Name == identifier {
			connected = edge.Node
			break
		}
	}
	if connected == nil {
		t.Fatalf("namespace %q not found in connection", identifier)
	}

	if !reflect.DeepEqual(directData.Namespace, connected) {
		t.Fatalf("direct namespace and connection edge differ:\ndirect: %+v\nedge: %+v", directData.Namespace, connected)
	}
	if directData.Namespace.Metadata.UID == "" {
		t.Fatal("metadata.uid is empty")
	}
	if directData.Namespace.ID != directData.Namespace.Metadata.UID {
		t.Fatalf("metadata.uid %q must use the Namespace Relay encoding, matching id %q", directData.Namespace.Metadata.UID, directData.Namespace.ID)
	}
	if directData.Namespace.Metadata.Labels == nil || directData.Namespace.Metadata.Annotations == nil {
		t.Fatalf("metadata maps must be present: labels=%v annotations=%v",
			directData.Namespace.Metadata.Labels, directData.Namespace.Metadata.Annotations)
	}
	if directData.Namespace.Metadata.OwnerReferences == nil || directData.Namespace.Metadata.Finalizers == nil {
		t.Fatalf("lifecycle fields must be present: ownerReferences=%v finalizers=%v",
			directData.Namespace.Metadata.OwnerReferences, directData.Namespace.Metadata.Finalizers)
	}
	if directData.Namespace.Body == nil || *directData.Namespace.Body != body {
		t.Fatalf("body = %v, want raw Markdown %q", directData.Namespace.Body, body)
	}
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
				}
			}
		}
	`, map[string]any{"input": map[string]any{
		"apiVersion": "gitstore.dev/v1beta1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": identifier},
		"spec":       map[string]any{"title": title, "tier": "ORGANIZATION"},
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
	assertNamespaceContractShape(t, data.CreateNamespace.Namespace, identifier, namespaceContractStringPtr(title), "ORGANIZATION")
}

func TestNamespaceContract_DeleteNamespaceFinalizesEmptySystemRepository(t *testing.T) {
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
	h.requireSystemDeletionRejected(identifier)

	namespaceID, ok := h.lookupNamespaceID(identifier)
	require.True(t, ok)
	resp := h.gql(`mutation($id: ID!) { deleteNamespace(input: {id: $id}) { namespace { id metadata { resourceVersion } } outcome } }`, map[string]any{"id": namespaceID})
	if len(resp.Errors) > 0 {
		t.Fatalf("graphql errors deleting namespace: %s", namespaceContractErrors(resp.Errors))
	}

	var data struct {
		DeleteNamespace *struct {
			Namespace *struct {
				ID       string `json:"id"`
				Metadata struct {
					ResourceVersion string `json:"resourceVersion"`
				} `json:"metadata"`
			} `json:"namespace"`
		} `json:"deleteNamespace"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal deleteNamespace response: %v", err)
	}
	if data.DeleteNamespace == nil {
		t.Fatal("deleteNamespace payload is nil")
	}
	if data.DeleteNamespace.Namespace == nil || data.DeleteNamespace.Namespace.ID != namespaceID {
		t.Fatalf("namespace ID = %+v, want %q", data.DeleteNamespace.Namespace, namespaceID)
	}
	resp = h.gql(`mutation($name: String!, $uid: ID!, $rv: String!) { completeNamespaceDeletion(input: {name: $name, id: $uid, resourceVersion: $rv}) { id } }`,
		map[string]any{"name": identifier, "uid": namespaceID, "rv": data.DeleteNamespace.Namespace.Metadata.ResourceVersion})
	require.Empty(t, resp.Errors)
	_, exists := h.lookupNamespaceID(identifier)
	require.False(t, exists)
	_, exists = h.lookupRepositoryID(identifier, namespaceContractSystemRepository)
	require.False(t, exists)
	created = false
}

type namespaceCapacityRaceResult struct {
	response gqlResponse
	err      error
}

func namespaceCapacityUnmaterializedRejection(response gqlResponse, namespace, repository string) bool {
	if response.Status != http.StatusOK || len(response.Errors) != 1 {
		return false
	}
	var failure struct {
		Message    string `json:"message"`
		Extensions struct {
			Code        string `json:"code"`
			Phase       string `json:"phase"`
			Commit      string `json:"commit"`
			Diagnostics []struct {
				Level   string `json:"level"`
				Message string `json:"message"`
				Reason  string `json:"reason"`
			} `json:"diagnostics"`
		} `json:"extensions"`
	}
	if json.Unmarshal(response.Errors[0], &failure) != nil {
		return false
	}
	extensions := failure.Extensions
	if extensions.Code != "ADMISSION_REJECTED" || extensions.Phase != "POST_RECEIVE" ||
		len(extensions.Commit) != 40 || strings.Trim(extensions.Commit, "0123456789abcdef") != "" ||
		len(extensions.Diagnostics) != 1 {
		return false
	}
	message := fmt.Sprintf("admission failed: committed admission did not materialize Repository %s/%s: datastore: not found: namespace mapping %s/%s",
		namespace, repository, namespace, repository)
	diagnostic := extensions.Diagnostics[0]
	return failure.Message == message && diagnostic.Message == message &&
		diagnostic.Level == "FAILURE" && diagnostic.Reason == "ADMISSION_FAILED"
}

func TestNamespaceCapacityUnmaterializedRejectionClassification(t *testing.T) {
	message := "admission failed: committed admission did not materialize Repository owned/child: datastore: not found: namespace mapping owned/child"
	for _, test := range []struct {
		name, code, phase, commit, reason, detail string
		want                                      bool
	}{
		{"exact missing projection", "ADMISSION_REJECTED", "POST_RECEIVE", strings.Repeat("a", 40), "ADMISSION_FAILED", message, true},
		{"storage unavailable", "ADMISSION_REJECTED", "POST_RECEIVE", strings.Repeat("a", 40), "ADMISSION_FAILED", "datastore unavailable", false},
		{"authorization", "FORBIDDEN", "POST_RECEIVE", strings.Repeat("a", 40), "ADMISSION_FAILED", message, false},
		{"pre receive", "ADMISSION_REJECTED", "PRE_RECEIVE", strings.Repeat("a", 40), "ADMISSION_FAILED", message, false},
		{"missing commit", "ADMISSION_REJECTED", "POST_RECEIVE", "", "ADMISSION_FAILED", message, false},
		{"invalid commit", "ADMISSION_REJECTED", "POST_RECEIVE", strings.Repeat("z", 40), "ADMISSION_FAILED", message, false},
		{"other reason", "ADMISSION_REJECTED", "POST_RECEIVE", strings.Repeat("a", 40), "NAMESPACE_NOT_READY", message, false},
		{"other identity", "ADMISSION_REJECTED", "POST_RECEIVE", strings.Repeat("a", 40), "ADMISSION_FAILED", strings.ReplaceAll(message, "owned/child", "other/child"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"errors": []any{map[string]any{
				"message": test.detail,
				"extensions": map[string]any{
					"code": test.code, "phase": test.phase, "commit": test.commit,
					"diagnostics": []any{map[string]any{"level": "FAILURE", "message": test.detail, "reason": test.reason}},
				},
			}}})
			require.NoError(t, err)
			var response gqlResponse
			require.NoError(t, json.Unmarshal(body, &response))
			response.Status = http.StatusOK
			require.Equal(t, test.want, namespaceCapacityUnmaterializedRejection(response, "owned", "child"))
			response.Status = http.StatusServiceUnavailable
			require.False(t, namespaceCapacityUnmaterializedRejection(response, "owned", "child"))
		})
	}
}

func runNamespaceCapacityAdmissionDeletionRace(ctx context.Context, cfg repositoryCapacityConfig, namespaceID, repository string) [2]namespaceCapacityRaceResult {
	operations := [2]struct {
		endpoint, query string
		variables       map[string]any
	}{
		{cfg.apiA, `mutation($input: CreateRepositoryInput!) {
			createRepository(input: $input) { repository { id } }
		}`, map[string]any{"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1", "kind": "Repository",
			"metadata": map[string]any{"namespace": cfg.namespace, "name": repository},
			"spec":     map[string]any{"defaultBranch": "main", "visibility": "PRIVATE", "storageClass": "standard"},
		}}},
		{cfg.apiB, `mutation($id: ID!) {
			deleteNamespace(input: {id: $id}) { outcome namespace { id } }
		}`, map[string]any{"id": namespaceID}},
	}
	var results [2]namespaceCapacityRaceResult
	var workers sync.WaitGroup
	start := make(chan struct{})
	for index, operation := range operations {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results[index].response, results[index].err = secretTestGraphQLRequest(ctx, operation.endpoint, cfg.token, operation.query, operation.variables)
		}()
	}
	close(start)
	workers.Wait()
	return results
}

func TestNamespaceCapacityAdmissionDeletionRaceConcurrentRequests(t *testing.T) {
	var arrived atomic.Int32
	ready := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owned-test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if arrived.Add(1) == 2 {
			close(ready)
		}
		select {
		case <-ready:
			_, _ = w.Write([]byte(`{"data":{"acknowledged":true}}`))
		case <-r.Context().Done():
		}
	})
	apiA, apiB := httptest.NewServer(handler), httptest.NewServer(handler)
	defer apiA.Close()
	defer apiB.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	results := runNamespaceCapacityAdmissionDeletionRace(ctx, repositoryCapacityConfig{
		apiA: apiA.URL, apiB: apiB.URL, token: "owned-test-token", namespace: "owned-race",
	}, "namespace-id", "owned-child")
	for _, result := range results {
		require.NoError(t, result.err)
		require.Equal(t, http.StatusOK, result.response.Status)
		require.Empty(t, result.response.Errors)
		require.JSONEq(t, `{"acknowledged":true}`, string(result.response.Data))
	}
	require.EqualValues(t, 2, arrived.Load(), "each actor must issue exactly one request, without retries")
}

func assertNamespaceCapacityDeletionLifecycle(t *testing.T, client *http.Client, cfg repositoryCapacityConfig) {
	t.Helper()
	assertCapacityCatalogBlockers(t, client, cfg, "Namespace")
	cfg.namespace = uniqueName("under-load-namespace")
	createWatchMetadataNamespace(t, cfg.apiA, cfg.apiB, cfg.token, cfg.namespace)
	require.NoError(t, waitRepositoryCapacityNamespaceReady(t.Context(), client, cfg))
	type lifecycleResource struct {
		ID       string `json:"id"`
		Metadata struct {
			Name              string    `json:"name"`
			ResourceVersion   string    `json:"resourceVersion"`
			Generation        int64     `json:"generation"`
			CreationTimestamp time.Time `json:"creationTimestamp"`
			DeletionTimestamp *string   `json:"deletionTimestamp"`
			Finalizers        []string  `json:"finalizers"`
		} `json:"metadata"`
		Status struct {
			Conditions []namespaceContractStatusCondition `json:"conditions"`
		} `json:"status"`
	}
	type snapshot struct {
		Namespace  *lifecycleResource `json:"namespace"`
		Repository *lifecycleResource `json:"repository"`
	}
	read := func(endpoint, repository string) snapshot {
		t.Helper()
		response := secretTestGraphQL(t, endpoint, cfg.token, `query($namespace: String!, $repository: String!) {
			namespace(by: {name: $namespace}) {
				id metadata { name resourceVersion generation creationTimestamp finalizers }
				status { conditions { type status } }
			}
			repository(by: {namespacePath: {namespace: $namespace, name: $repository}}) {
				id metadata { name resourceVersion generation creationTimestamp deletionTimestamp finalizers }
			}
		}`, map[string]any{"namespace": cfg.namespace, "repository": repository})
		require.Equal(t, http.StatusOK, response.Status)
		require.Empty(t, response.Errors)
		var result snapshot
		require.NoError(t, json.Unmarshal(response.Data, &result))
		require.NotNil(t, result.Namespace)
		require.NotNil(t, result.Repository)
		require.NotEmpty(t, result.Namespace.ID)
		require.NotEmpty(t, result.Namespace.Metadata.ResourceVersion)
		require.NotEmpty(t, result.Repository.ID)
		return result
	}
	fixtureResource := func(current snapshot, kind string) *lifecycleResource {
		if kind == "Namespace" {
			return current.Namespace
		}
		require.Equal(t, "Repository", kind)
		return current.Repository
	}
	changeHeldFinalizer := func(kind string, baseline snapshot, repository string, release bool) {
		t.Helper()
		expected := fixtureResource(baseline, kind)
		for range 8 {
			current := read(cfg.apiA, repository)
			require.Equal(t, baseline.Namespace.ID, current.Namespace.ID, "fixture cannot cross a Namespace incarnation")
			target := fixtureResource(current, kind)
			require.Equal(t, expected.ID, target.ID)
			require.Equal(t, expected.Metadata.Generation, target.Metadata.Generation)
			require.True(t, expected.Metadata.CreationTimestamp.Equal(target.Metadata.CreationTimestamp))
			statement, err := capacityInfrastructureFixtureUpdate(cfg.keyspace, capacityInfrastructureFixture{
				kind: kind, namespace: cfg.namespace, name: target.Metadata.Name, id: target.ID,
				version: target.Metadata.ResourceVersion, created: target.Metadata.CreationTimestamp,
				finalizers: target.Metadata.Finalizers,
			}, release)
			require.NoError(t, err)
			t.Logf("infrastructure_finalizer_fixture kind=%s namespace=%s name=%s id=%s resource_version=%s creation_timestamp=%s release=%t",
				kind, cfg.namespace, target.Metadata.Name, target.ID, target.Metadata.ResourceVersion,
				target.Metadata.CreationTimestamp.UTC().Format(time.RFC3339Nano), release)
			if applyCapacityOwnedFinalizerFixture(t, cfg, statement) {
				return
			}
		}
		t.Fatal("infrastructure finalizer fixture exhausted its eight resource-version CAS attempts")
	}
	assertHeldCompletion := func(kind string, baseline snapshot, repository string) {
		t.Helper()
		for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
			rejected := false
			for range 8 {
				current := read(endpoint, repository)
				require.Equal(t, baseline.Namespace.ID, current.Namespace.ID)
				target := fixtureResource(current, kind)
				require.Equal(t, fixtureResource(baseline, kind).ID, target.ID)
				query := `mutation($id: ID!, $name: String!, $rv: String!) {
					completeNamespaceDeletion(input: {id: $id, name: $name, resourceVersion: $rv}) { id }
				}`
				variables := map[string]any{"id": target.ID, "name": target.Metadata.Name, "rv": target.Metadata.ResourceVersion}
				if kind == "Repository" {
					query = `mutation($id: ID!, $name: String!, $namespace: String!, $rv: String!) {
						completeRepositoryDeletion(input: {id: $id, name: $name, namespace: $namespace, resourceVersion: $rv}) { id }
					}`
					variables["namespace"] = cfg.namespace
				}
				response := secretTestGraphQL(t, endpoint, cfg.token, query, variables)
				require.Equal(t, http.StatusOK, response.Status)
				require.Len(t, response.Errors, 1)
				var failure struct {
					Message    string `json:"message"`
					Extensions struct {
						Code string `json:"code"`
					} `json:"extensions"`
				}
				require.NoError(t, json.Unmarshal(response.Errors[0], &failure))
				if failure.Extensions.Code == "CONFLICT" {
					continue
				}
				require.Contains(t, failure.Message, "other finalizers", "completion must be rejected by the held finalizer, not another failure")
				rejected = true
				break
			}
			require.True(t, rejected, "held completion did not reach the finalizer gate within eight version attempts")
		}
	}
	initial := read(cfg.apiA, namespaceContractSystemRepository)
	systemStorage := captureCapacityRepositoryStorage(t, client, cfg, namespaceContractSystemRepository)
	if cfg.verifyFinalizers {
		for direction := range 2 {
			baseline := read(cfg.apiA, namespaceContractSystemRepository)
			changeHeldFinalizer("Namespace", baseline, namespaceContractSystemRepository, false)
			raceConfig := cfg
			if direction == 1 {
				raceConfig.apiA, raceConfig.apiB = cfg.apiB, cfg.apiA
			}
			childName := uniqueName("namespace-racer")
			started := time.Now()
			var before int64
			if cfg.acknowledgements != nil {
				before = cfg.acknowledgements.Load()
				require.Positive(t, before)
			}
			results := runNamespaceCapacityAdmissionDeletionRace(t.Context(), raceConfig, baseline.Namespace.ID, childName)
			codes := [2]string{}
			for index, result := range results {
				require.NoError(t, result.err, "concurrent actor %d failed at transport/protocol level", index)
				require.Equal(t, http.StatusOK, result.response.Status)
				if len(result.response.Errors) == 0 {
					continue
				}
				require.Len(t, result.response.Errors, 1)
				var failure struct {
					Extensions struct {
						Code string `json:"code"`
					} `json:"extensions"`
				}
				require.NoError(t, json.Unmarshal(result.response.Errors[0], &failure))
				codes[index] = failure.Extensions.Code
				allowed := []string{"CONFLICT", "FAILED_PRECONDITION"}
				if index == 0 {
					allowed = append(allowed, "NOT_FOUND")
					if codes[index] == "ADMISSION_REJECTED" {
						require.True(t, namespaceCapacityUnmaterializedRejection(result.response, cfg.namespace, childName),
							"only the exact committed missing-projection denial is eligible for a terminating parent")
						allowed = append(allowed, "ADMISSION_REJECTED")
					}
				}
				require.Contains(t, allowed, codes[index], "unrelated failures are not evidence of a lifecycle fence")
			}
			childAcknowledged, deletionAcknowledged := codes[0] == "", codes[1] == ""
			require.False(t, childAcknowledged && deletionAcknowledged, "Namespace deletion and child creation must not both win")
			if !childAcknowledged && !deletionAcknowledged {
				require.Equal(t, [2]string{"CONFLICT", "CONFLICT"}, codes, "an active, childless Namespace can only reject both actors for contention")
			}
			for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
				current := read(endpoint, namespaceContractSystemRepository)
				require.Equal(t, baseline.Namespace.ID, current.Namespace.ID)
				require.Equal(t, baseline.Repository.ID, current.Repository.ID)
				for _, kind := range []string{"Namespace", "Repository"} {
					expected, actual := fixtureResource(baseline, kind), fixtureResource(current, kind)
					require.Equal(t, expected.Metadata.Generation, actual.Metadata.Generation)
					require.True(t, expected.Metadata.CreationTimestamp.Equal(actual.Metadata.CreationTimestamp))
				}
				require.Contains(t, current.Namespace.Metadata.Finalizers, capacityCatalogHeldFinalizer)
				require.Nil(t, current.Repository.Metadata.DeletionTimestamp)
				terminating := namespaceContractStatusCondition{Type: "Terminating", Status: "TRUE"}
				if deletionAcknowledged {
					require.Contains(t, current.Namespace.Status.Conditions, terminating)
				} else {
					require.NotContains(t, current.Namespace.Status.Conditions, terminating)
				}
			}
			assertCapacityRepositoryStorage(t, systemStorage, false)
			var childID string
			if childAcknowledged {
				var created struct {
					Create struct {
						Repository *lifecycleResource `json:"repository"`
					} `json:"createRepository"`
				}
				require.NoError(t, json.Unmarshal(results[0].response.Data, &created))
				require.NotNil(t, created.Create.Repository)
				childID = created.Create.Repository.ID
				require.NotEmpty(t, childID)
				for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
					current := read(endpoint, childName)
					require.Equal(t, childID, current.Repository.ID)
					require.Nil(t, current.Repository.Metadata.DeletionTimestamp)
					require.Equal(t, baseline.Namespace.ID, current.Namespace.ID)
				}
			} else {
				for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
					response := secretTestGraphQL(t, endpoint, cfg.token, `query($namespace: String!, $name: String!) {
						repository(by: {namespacePath: {namespace: $namespace, name: $name}}) { id }
					}`, map[string]any{"namespace": cfg.namespace, "name": childName})
					require.Equal(t, http.StatusOK, response.Status)
					require.Len(t, response.Errors, 1)
					var missing struct {
						Extensions struct {
							Code string `json:"code"`
						} `json:"extensions"`
					}
					require.NoError(t, json.Unmarshal(response.Errors[0], &missing))
					require.Equal(t, "NOT_FOUND", missing.Extensions.Code, "rejected admission must not leave a visible orphan")
				}
			}
			if deletionAcknowledged {
				require.JSONEq(t, fmt.Sprintf(`{"deleteNamespace":{"outcome":"TERMINATION_STARTED","namespace":{"id":%q}}}`, baseline.Namespace.ID),
					string(results[1].response.Data))
			}
			directory := filepath.Join(t.TempDir(), "race-manifest")
			_, err := newSecretCapacityGitWorker(t.Context(), directory,
				strings.TrimRight(os.Getenv("GIT_URL"), "/")+"/"+cfg.namespace+"/gitstore-system.git",
				cfg.namespace, childName, cfg.token, 0)
			require.NoError(t, err)
			path := "repositories/" + childName + ".md"
			manifest, err := secretCapacityGit(t.Context(), directory, "", "ls-tree", "--name-only", "HEAD", "--", path)
			require.NoError(t, err)
			if childAcknowledged {
				require.Equal(t, path, strings.TrimSpace(string(manifest)), "acknowledged child must retain its committed manifest")
				childStorage := captureCapacityRepositoryStorage(t, client, cfg, childName)
				assertCapacityRepositoryStorage(t, childStorage, false)
				rejectNamespaceDeletion := secretTestGraphQL(t, raceConfig.apiB, cfg.token,
					`mutation($id: ID!) { deleteNamespace(input: {id: $id}) { outcome } }`,
					map[string]any{"id": baseline.Namespace.ID})
				require.Equal(t, http.StatusOK, rejectNamespaceDeletion.Status)
				require.Len(t, rejectNamespaceDeletion.Errors, 1)
				var blocked struct {
					Extensions struct {
						Code        string `json:"code"`
						Diagnostics []struct {
							Reason string `json:"reason"`
						} `json:"diagnostics"`
					} `json:"extensions"`
				}
				require.NoError(t, json.Unmarshal(rejectNamespaceDeletion.Errors[0], &blocked))
				require.Equal(t, "FAILED_PRECONDITION", blocked.Extensions.Code)
				require.Len(t, blocked.Extensions.Diagnostics, 1)
				require.Equal(t, "NAMESPACE_NOT_EMPTY", blocked.Extensions.Diagnostics[0].Reason)
				cleanup := secretTestGraphQL(t, raceConfig.apiA, cfg.token,
					`mutation($id: ID!) { deleteRepository(input: {id: $id}) { outcome } }`,
					map[string]any{"id": childID})
				require.Equal(t, http.StatusOK, cleanup.Status)
				require.Empty(t, cleanup.Errors)
				require.JSONEq(t, `{"deleteRepository":{"outcome":"TERMINATION_STARTED"}}`, string(cleanup.Data))
				waitCapacityNodesDeleted(t, cfg, childID)
				assertCapacityRepositoryStorage(t, childStorage, true)
			}
			if !deletionAcknowledged {
				cleanup := secretTestGraphQL(t, raceConfig.apiB, cfg.token,
					`mutation($id: ID!) { deleteNamespace(input: {id: $id}) { outcome } }`,
					map[string]any{"id": baseline.Namespace.ID})
				require.Equal(t, http.StatusOK, cleanup.Status)
				require.Empty(t, cleanup.Errors)
				require.JSONEq(t, `{"deleteNamespace":{"outcome":"TERMINATION_STARTED"}}`, string(cleanup.Data))
			}
			changeHeldFinalizer("Namespace", baseline, namespaceContractSystemRepository, true)
			waitCapacityNodesDeleted(t, cfg, baseline.Namespace.ID, baseline.Repository.ID)
			assertCapacityRepositoryStorage(t, systemStorage, true)
			var after int64
			if cfg.acknowledgements != nil {
				after = cfg.acknowledgements.Load()
				require.Greater(t, after, before, "admission/deletion contention requires continuing workload acknowledgements")
			}
			t.Logf("namespace_admission_deletion_race direction=%d namespace_id=%s child_id=%s child_acknowledged=%t child_code=%s deletion_acknowledged=%t deletion_code=%s committed_child_manifest=%t started_at=%s completed_at=%s acknowledged_before=%d acknowledged_after=%d",
				direction, baseline.Namespace.ID, childID, childAcknowledged, codes[0], deletionAcknowledged, codes[1],
				strings.TrimSpace(string(manifest)) == path, started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), before, after)
			createWatchMetadataNamespace(t, cfg.apiA, cfg.apiB, cfg.token, cfg.namespace)
			require.NoError(t, waitRepositoryCapacityNamespaceReady(t.Context(), client, cfg))
			initial = read(cfg.apiA, namespaceContractSystemRepository)
			require.NotEqual(t, baseline.Namespace.ID, initial.Namespace.ID)
			require.NotEqual(t, baseline.Repository.ID, initial.Repository.ID)
			systemStorage = captureCapacityRepositoryStorage(t, client, cfg, namespaceContractSystemRepository)
		}
	}
	name := uniqueName("namespace-blocker")
	require.NoError(t, repositoryCapacityMutation(t.Context(), client, cfg.apiA, cfg.token, true, cfg.namespace, name, "namespace-blocker", 0, ""))
	require.NoError(t, waitRepositoryCapacityResourceReady(t.Context(), client, cfg, "Repository", name))
	ordinary := read(cfg.apiA, name)
	ordinaryStorage := captureCapacityRepositoryStorage(t, client, cfg, name)
	rejectNamespaceDeletion := func(endpoint string) {
		t.Helper()
		blocked := secretTestGraphQL(t, endpoint, cfg.token, `mutation($id: ID!) {
			deleteNamespace(input: {id: $id}) { outcome }
		}`, map[string]any{"id": initial.Namespace.ID})
		require.Equal(t, http.StatusOK, blocked.Status)
		require.Len(t, blocked.Errors, 1)
		var diagnostic struct {
			Extensions struct {
				Code        string `json:"code"`
				Diagnostics []struct {
					Reason string `json:"reason"`
				} `json:"diagnostics"`
			} `json:"extensions"`
		}
		require.NoError(t, json.Unmarshal(blocked.Errors[0], &diagnostic))
		require.Equal(t, "FAILED_PRECONDITION", diagnostic.Extensions.Code)
		require.Len(t, diagnostic.Extensions.Diagnostics, 1)
		require.Equal(t, "NAMESPACE_NOT_EMPTY", diagnostic.Extensions.Diagnostics[0].Reason)
	}
	verifyHeldFinalizer := func(kind string, baseline snapshot, repository string, storage *capacityRepositoryStorage) {
		t.Helper()
		started := time.Now()
		var before int64
		if cfg.acknowledgements != nil {
			before = cfg.acknowledgements.Load()
			require.Positive(t, before)
		}
		for repetition := range 2 {
			for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
				current := read(endpoint, repository)
				if kind == "Repository" && repository == namespaceContractSystemRepository {
					require.Eventually(t, func() bool {
						current = read(endpoint, repository)
						return current.Repository.Metadata.DeletionTimestamp != nil
					}, time.Minute, 100*time.Millisecond, "Namespace controller must initiate real system Repository termination")
				}
				require.Equal(t, baseline.Namespace.ID, current.Namespace.ID)
				require.Equal(t, baseline.Repository.ID, current.Repository.ID)
				for _, targetKind := range []string{"Namespace", "Repository"} {
					expected, target := fixtureResource(baseline, targetKind), fixtureResource(current, targetKind)
					require.Equal(t, expected.Metadata.Generation, target.Metadata.Generation)
					require.True(t, expected.Metadata.CreationTimestamp.Equal(target.Metadata.CreationTimestamp))
				}
				target := fixtureResource(current, kind)
				require.Contains(t, target.Metadata.Finalizers, capacityCatalogHeldFinalizer)
				if kind == "Namespace" {
					require.Nil(t, current.Repository.Metadata.DeletionTimestamp, "Namespace finalizer must block even system Repository termination")
				} else {
					require.NotNil(t, current.Repository.Metadata.DeletionTimestamp, "only the real API/controller path may initiate termination")
				}
				if repository == namespaceContractSystemRepository {
					require.Contains(t, current.Namespace.Status.Conditions, namespaceContractStatusCondition{Type: "Terminating", Status: "TRUE"})
				} else {
					rejectNamespaceDeletion(endpoint)
					require.ElementsMatch(t, initial.Namespace.Metadata.Finalizers, current.Namespace.Metadata.Finalizers, "terminating ordinary Repository cannot cascade Namespace deletion")
				}
			}
			assertHeldCompletion("Repository", baseline, repository)
			if repository == namespaceContractSystemRepository {
				assertHeldCompletion("Namespace", baseline, repository)
			}
			assertCapacityRepositoryStorage(t, storage, false)
			if repetition == 0 && cfg.acknowledgements != nil {
				require.Eventually(t, func() bool { return cfg.acknowledgements.Load() > before },
					5*time.Second, 100*time.Millisecond, "held infrastructure completion requires continuing acknowledgements")
			}
		}
		var after int64
		if cfg.acknowledgements != nil {
			after = cfg.acknowledgements.Load()
			require.Greater(t, after, before)
		}
		t.Logf("namespace_infrastructure_finalizer_hold kind=%s repository=%s real_termination=true storage_preserved=true started_at=%s completed_at=%s acknowledged_before=%d acknowledged_after=%d",
			kind, repository, started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), before, after)
	}
	for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
		rejectNamespaceDeletion(endpoint)
		current := read(endpoint, name)
		require.Equal(t, initial.Namespace.ID, current.Namespace.ID)
		require.Equal(t, ordinary.Repository.ID, current.Repository.ID)
		require.ElementsMatch(t, initial.Namespace.Metadata.Finalizers, current.Namespace.Metadata.Finalizers, "blocked deletion must not add Namespace termination finalizers")
		require.Nil(t, current.Repository.Metadata.DeletionTimestamp, "Namespace deletion must not cascade to ordinary Repositories")
	}
	if cfg.verifyFinalizers {
		changeHeldFinalizer("Repository", ordinary, name, false)
	}
	deleted := secretTestGraphQL(t, cfg.apiB, cfg.token, `mutation($id: ID!) {
		deleteRepository(input: {id: $id}) { outcome }
	}`, map[string]any{"id": ordinary.Repository.ID})
	require.Equal(t, http.StatusOK, deleted.Status)
	require.Empty(t, deleted.Errors)
	require.JSONEq(t, `{"deleteRepository":{"outcome":"TERMINATION_STARTED"}}`, string(deleted.Data))
	if cfg.verifyFinalizers {
		verifyHeldFinalizer("Repository", ordinary, name, ordinaryStorage)
		assertCapacityRepositoryStorage(t, systemStorage, false)
		changeHeldFinalizer("Repository", ordinary, name, true)
	}
	waitCapacityNodesDeleted(t, cfg, ordinary.Repository.ID)
	assertCapacityRepositoryStorage(t, ordinaryStorage, true)
	assertCapacityRepositoryStorage(t, systemStorage, false)
	for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
		current := read(endpoint, namespaceContractSystemRepository)
		require.Equal(t, initial.Namespace.ID, current.Namespace.ID)
		require.Equal(t, initial.Repository.ID, current.Repository.ID)
		require.ElementsMatch(t, initial.Namespace.Metadata.Finalizers, current.Namespace.Metadata.Finalizers)
		require.Nil(t, current.Repository.Metadata.DeletionTimestamp)
	}
	type catalogSnapshot struct {
		ID       string `json:"id"`
		Metadata *struct {
			Name              string  `json:"name"`
			Namespace         string  `json:"namespace"`
			ResourceVersion   string  `json:"resourceVersion"`
			DeletionTimestamp *string `json:"deletionTimestamp"`
		} `json:"metadata"`
	}
	gitURL := strings.TrimRight(os.Getenv("GIT_URL"), "/")
	for _, catalog := range []struct{ kind, field, directory string }{
		{"Product", "product", "products"},
		{"CategoryTaxonomy", "category", "categories"},
	} {
		catalogName := uniqueName("namespace-catalog-blocker")
		suffix := strings.ToUpper(catalog.field[:1]) + catalog.field[1:]
		readCatalog := func(endpoint string) catalogSnapshot {
			t.Helper()
			response := secretTestGraphQL(t, endpoint, cfg.token, fmt.Sprintf(`query($namespace: String!, $name: String!) {
			resource: %s(by: {namespacePath: {namespace: $namespace, name: $name}}) {
				id metadata { name namespace resourceVersion deletionTimestamp }
			}
		}`, catalog.field), map[string]any{"namespace": cfg.namespace, "name": catalogName})
			require.Equal(t, http.StatusOK, response.Status)
			require.Empty(t, response.Errors)
			var data struct {
				Resource *catalogSnapshot `json:"resource"`
			}
			require.NoError(t, json.Unmarshal(response.Data, &data))
			require.NotNil(t, data.Resource)
			require.NotEmpty(t, data.Resource.ID)
			require.NotNil(t, data.Resource.Metadata)
			require.Equal(t, catalogName, data.Resource.Metadata.Name)
			require.Equal(t, cfg.namespace, data.Resource.Metadata.Namespace)
			require.NotEmpty(t, data.Resource.Metadata.ResourceVersion)
			require.Nil(t, data.Resource.Metadata.DeletionTimestamp, "blocked Namespace deletion must not cascade to its catalog child")
			return *data.Resource
		}
		var previousID string
		directory := filepath.Join(t.TempDir(), "catalog-authoring")
		path := catalog.directory + "/" + catalogName + ".md"
		for incarnation := range 2 {
			created := secretTestGraphQL(t, cfg.apiA, cfg.token, fmt.Sprintf(`mutation($input: Create%sInput!) {
			resource: create%s(input: $input) { object: %s { id } }
		}`, suffix, suffix, catalog.field), map[string]any{"input": map[string]any{
				"metadata": map[string]any{"namespace": cfg.namespace, "name": catalogName},
				"spec":     map[string]any{"title": "Namespace catalog deletion blocker"},
			}})
			require.Equal(t, http.StatusOK, created.Status)
			require.Empty(t, created.Errors)
			resource := readCatalog(cfg.apiA)
			require.JSONEq(t, fmt.Sprintf(`{"resource":{"object":{"id":%q}}}`, resource.ID), string(created.Data))
			if incarnation == 0 {
				_, err := newSecretCapacityGitWorker(t.Context(), directory, gitURL+"/"+cfg.namespace+"/gitstore-system.git", cfg.namespace, catalogName, cfg.token, 0)
				require.NoError(t, err)
			}
			for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
				if previousID != "" {
					require.NotEqual(t, previousID, resource.ID)
					stale := secretTestGraphQL(t, endpoint, cfg.token, fmt.Sprintf(`mutation($id: ID!, $namespace: String!, $name: String!, $rv: String!) {
					complete%sDeletion(input: {id: $id, namespace: $namespace, name: $name, resourceVersion: $rv}) { id }
				}`, suffix), map[string]any{"id": previousID, "namespace": cfg.namespace, "name": catalogName, "rv": resource.Metadata.ResourceVersion})
					require.Equal(t, http.StatusOK, stale.Status)
					require.Len(t, stale.Errors, 1)
					var conflict struct {
						Extensions struct {
							Code string `json:"code"`
						} `json:"extensions"`
					}
					require.NoError(t, json.Unmarshal(stale.Errors[0], &conflict))
					require.Equal(t, "CONFLICT", conflict.Extensions.Code)
					missingID := secretTestGraphQL(t, endpoint, cfg.token, fmt.Sprintf(`mutation($namespace: String!, $name: String!, $rv: String!) {
						complete%sDeletion(input: {namespace: $namespace, name: $name, resourceVersion: $rv}) { id }
					}`, suffix), map[string]any{"namespace": cfg.namespace, "name": catalogName, "rv": resource.Metadata.ResourceVersion})
					require.Equal(t, http.StatusUnprocessableEntity, missingID.Status)
					require.Len(t, missingID.Errors, 1)
					require.NoError(t, json.Unmarshal(missingID.Errors[0], &conflict))
					require.Equal(t, "GRAPHQL_VALIDATION_FAILED", conflict.Extensions.Code)
				}
				rejectNamespaceDeletion(endpoint)
				require.Equal(t, resource.ID, readCatalog(endpoint).ID)
				current := read(endpoint, namespaceContractSystemRepository)
				require.Equal(t, initial.Namespace.ID, current.Namespace.ID)
				require.Equal(t, initial.Repository.ID, current.Repository.ID)
				require.ElementsMatch(t, initial.Namespace.Metadata.Finalizers, current.Namespace.Metadata.Finalizers)
				require.Nil(t, current.Repository.Metadata.DeletionTimestamp)
			}
			_, err := secretCapacityGit(t.Context(), directory, cfg.token, "fetch", "--quiet", "--depth=1", "origin", "refs/heads/main")
			require.NoError(t, err)
			manifest, err := secretCapacityGit(t.Context(), directory, "", "ls-tree", "--name-only", "FETCH_HEAD", "--", path)
			require.NoError(t, err)
			require.Equal(t, path, strings.TrimSpace(string(manifest)), "blocked Namespace deletion must preserve the committed catalog manifest")
			assertCapacityRepositoryStorage(t, systemStorage, false)
			started := time.Now()
			var before int64
			if cfg.acknowledgements != nil {
				before = cfg.acknowledgements.Load()
			}
			deleted := secretTestGraphQL(t, cfg.apiB, cfg.token, fmt.Sprintf(`mutation($id: ID!) {
			resource: delete%s(input: {id: $id}) { outcome }
		}`, suffix), map[string]any{"id": resource.ID})
			require.Equal(t, http.StatusOK, deleted.Status)
			require.Empty(t, deleted.Errors)
			require.JSONEq(t, `{"resource":{"outcome":"TERMINATION_STARTED"}}`, string(deleted.Data))
			waitCapacityNodesDeleted(t, cfg, resource.ID)
			_, err = secretCapacityGit(t.Context(), directory, cfg.token, "fetch", "--quiet", "--depth=1", "origin", "refs/heads/main")
			require.NoError(t, err)
			manifest, err = secretCapacityGit(t.Context(), directory, "", "ls-tree", "--name-only", "FETCH_HEAD", "--", path)
			require.NoError(t, err)
			require.Empty(t, strings.TrimSpace(string(manifest)), "explicit catalog deletion must remove its manifest")
			if cfg.acknowledgements != nil {
				after := cfg.acknowledgements.Load()
				require.Positive(t, before)
				require.Greater(t, after, before, "catalog cleanup requires acknowledged sustained mutations")
				t.Logf("namespace_catalog_cleanup_under_load kind=%s started_at=%s completed_at=%s acknowledged_before=%d acknowledged_after=%d",
					catalog.kind, started.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), before, after)
			}
			previousID = resource.ID
		}
	}
	terminate := func(namespaceID, systemID string, storage *capacityRepositoryStorage, heldKind string) {
		t.Helper()
		baseline := read(cfg.apiA, namespaceContractSystemRepository)
		require.Equal(t, namespaceID, baseline.Namespace.ID)
		require.Equal(t, systemID, baseline.Repository.ID)
		if cfg.verifyFinalizers {
			changeHeldFinalizer(heldKind, baseline, namespaceContractSystemRepository, false)
		}
		started := time.Now()
		var before int64
		if cfg.acknowledgements != nil {
			before = cfg.acknowledgements.Load()
		}
		response := secretTestGraphQL(t, cfg.apiA, cfg.token, `mutation($id: ID!) {
			deleteNamespace(input: {id: $id}) { outcome namespace { id } }
		}`, map[string]any{"id": namespaceID})
		require.Equal(t, http.StatusOK, response.Status)
		require.Empty(t, response.Errors)
		var result struct {
			Delete struct {
				Outcome   string `json:"outcome"`
				Namespace struct {
					ID string `json:"id"`
				} `json:"namespace"`
			} `json:"deleteNamespace"`
		}
		require.NoError(t, json.Unmarshal(response.Data, &result))
		require.Equal(t, "TERMINATION_STARTED", result.Delete.Outcome)
		require.Equal(t, namespaceID, result.Delete.Namespace.ID)
		if cfg.verifyFinalizers {
			verifyHeldFinalizer(heldKind, baseline, namespaceContractSystemRepository, storage)
			if heldKind == "Namespace" {
				for direction := range 2 {
					late := cfg
					if direction == 1 {
						late.apiA, late.apiB = cfg.apiB, cfg.apiA
					}
					name := uniqueName("namespace-late")
					results := runNamespaceCapacityAdmissionDeletionRace(t.Context(), late, namespaceID, name)
					for _, result := range results {
						require.NoError(t, result.err)
						require.Equal(t, http.StatusOK, result.response.Status)
					}
					require.Len(t, results[0].response.Errors, 1, "a child must not be acknowledged after Namespace termination has won")
					var rejection struct {
						Extensions struct {
							Code string `json:"code"`
						} `json:"extensions"`
					}
					require.NoError(t, json.Unmarshal(results[0].response.Errors[0], &rejection))
					if rejection.Extensions.Code == "ADMISSION_REJECTED" {
						require.True(t, namespaceCapacityUnmaterializedRejection(results[0].response, cfg.namespace, name),
							"unrelated admission failures must not pass the termination-first probe")
					} else {
						require.Contains(t, []string{"FAILED_PRECONDITION", "NOT_FOUND"}, rejection.Extensions.Code)
					}
					require.Empty(t, results[1].response.Errors)
					require.JSONEq(t, fmt.Sprintf(`{"deleteNamespace":{"outcome":"ALREADY_TERMINATING","namespace":{"id":%q}}}`, namespaceID),
						string(results[1].response.Data))
					for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
						missing := secretTestGraphQL(t, endpoint, cfg.token, `query($namespace: String!, $name: String!) {
							repository(by: {namespacePath: {namespace: $namespace, name: $name}}) { id }
						}`, map[string]any{"namespace": cfg.namespace, "name": name})
						require.Equal(t, http.StatusOK, missing.Status)
						require.Len(t, missing.Errors, 1)
						require.NoError(t, json.Unmarshal(missing.Errors[0], &rejection))
						require.Equal(t, "NOT_FOUND", rejection.Extensions.Code)
						current := read(endpoint, namespaceContractSystemRepository)
						require.Equal(t, namespaceID, current.Namespace.ID)
						require.Equal(t, systemID, current.Repository.ID)
						require.Nil(t, current.Repository.Metadata.DeletionTimestamp)
					}
					assertCapacityRepositoryStorage(t, storage, false)
					t.Logf("namespace_termination_first_admission direction=%d namespace_id=%s child_name=%s rejected=true system_storage_preserved=true",
						direction, namespaceID, name)
				}
			}
			changeHeldFinalizer(heldKind, baseline, namespaceContractSystemRepository, true)
		}
		waitCapacityNodesDeleted(t, cfg, namespaceID, systemID)
		assertCapacityRepositoryStorage(t, storage, true)
		if cfg.acknowledgements != nil {
			completed, after := time.Now(), cfg.acknowledgements.Load()
			t.Logf("namespace_deletion_under_load started_at=%s completed_at=%s acknowledged_before=%d acknowledged_after=%d",
				started.UTC().Format(time.RFC3339Nano), completed.UTC().Format(time.RFC3339Nano), before, after)
			require.Positive(t, before)
			require.Greater(t, after, before, "Namespace initiation/system-repository completion requires acknowledged sustained mutations")
		}
	}
	terminate(initial.Namespace.ID, initial.Repository.ID, systemStorage, "Namespace")
	createWatchMetadataNamespace(t, cfg.apiB, cfg.apiA, cfg.token, cfg.namespace)
	require.NoError(t, waitRepositoryCapacityNamespaceReady(t.Context(), client, cfg))
	replacement := read(cfg.apiB, namespaceContractSystemRepository)
	replacementStorage := captureCapacityRepositoryStorage(t, client, cfg, namespaceContractSystemRepository)
	require.NotEqual(t, initial.Namespace.ID, replacement.Namespace.ID)
	require.NotEqual(t, initial.Repository.ID, replacement.Repository.ID)
	for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
		stale := secretTestGraphQL(t, endpoint, cfg.token, `mutation($name: String!, $id: ID!, $rv: String!) {
			completeNamespaceDeletion(input: {name: $name, id: $id, resourceVersion: $rv}) { id }
		}`, map[string]any{"name": cfg.namespace, "id": initial.Namespace.ID, "rv": replacement.Namespace.Metadata.ResourceVersion})
		require.Equal(t, http.StatusOK, stale.Status)
		require.Len(t, stale.Errors, 1)
		var conflict struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		}
		require.NoError(t, json.Unmarshal(stale.Errors[0], &conflict))
		require.Equal(t, "CONFLICT", conflict.Extensions.Code)
		missingID := secretTestGraphQL(t, endpoint, cfg.token, `mutation($name: String!, $rv: String!) {
			completeNamespaceDeletion(input: {name: $name, resourceVersion: $rv}) { id }
		}`, map[string]any{"name": cfg.namespace, "rv": replacement.Namespace.Metadata.ResourceVersion})
		require.Equal(t, http.StatusUnprocessableEntity, missingID.Status)
		require.Len(t, missingID.Errors, 1)
		require.NoError(t, json.Unmarshal(missingID.Errors[0], &conflict))
		require.Equal(t, "GRAPHQL_VALIDATION_FAILED", conflict.Extensions.Code)
		current := read(endpoint, namespaceContractSystemRepository)
		require.Equal(t, replacement.Namespace.ID, current.Namespace.ID)
		require.Equal(t, replacement.Repository.ID, current.Repository.ID)
		require.ElementsMatch(t, replacement.Namespace.Metadata.Finalizers, current.Namespace.Metadata.Finalizers)
		require.Nil(t, current.Repository.Metadata.DeletionTimestamp)
		assertCapacityRepositoryStorage(t, systemStorage, true)
		assertCapacityRepositoryStorage(t, replacementStorage, false)
	}
	terminate(replacement.Namespace.ID, replacement.Repository.ID, replacementStorage, "Repository")
	t.Log("Namespace deletion: ordinary Repository, Product and CategoryTaxonomy rejection without cascade, catalog/Namespace reincarnation and stale-UID preservation, and empty system-repository completion verified on both APIs")
}
