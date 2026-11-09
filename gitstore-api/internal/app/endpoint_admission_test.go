// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTrustedCustomerDeniedOnAdminAndStorefrontRemainsUnavailable(t *testing.T) {
	for _, kind := range []auth.ProfileKind{auth.ProfileStaff, auth.ProfileCustomer} {
		t.Run(string(kind), func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			directory := &webSocketDirectory{lease: time.Minute, profile: &auth.UserProfile{
				Subject: "directory-id", Username: "alice", Kind: kind, Active: true, Lifecycle: auth.ActorActive,
				Groups: []string{"system:authenticated", "system:staff", "system:masters", "system:group:operators"},
			}}
			registry := auth.NewProviderRegistry(auth.NewChainedAuthN(&directoryWebSocketAuth{}),
				allowall.New(zap.NewNop()), directory)
			handler, err := NewGraphQLHandler(GraphQLHandlerDeps{
				Endpoint: config.DefaultEndpoint("admin"), Store: store, Registry: registry, Logger: zap.NewNop(),
				IDs:             apiruntime.NewSequenceIDGenerator(),
				RegisterCleanup: func(close func()) { t.Cleanup(close) },
			})
			require.NoError(t, err)
			request := httptest.NewRequest("POST", "/admin/graphql", strings.NewReader(`{"query":"{__typename}"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer trusted")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if kind == auth.ProfileCustomer {
				require.Contains(t, response.Body.String(), `"code":"FORBIDDEN"`)
			} else {
				require.Contains(t, response.Body.String(), `"__typename":"Query"`)
			}
			public, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{}, nil)
			require.NoError(t, err)
			response = httptest.NewRecorder()
			public.ServeHTTP(response, httptest.NewRequest("POST", "/storefront/graphql", nil))
			require.Equal(t, http.StatusServiceUnavailable, response.Code)
			require.Contains(t, response.Body.String(), "STOREFRONT_UNAVAILABLE")
		})
	}
	t.Run("source-backed workload remains admitted without a human directory", func(t *testing.T) {
		env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
		request, err := http.NewRequest(http.MethodPost, env.server.URL+"/admin/graphql",
			strings.NewReader(`{"query":"{__typename}"}`))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer valid")
		response, err := env.server.Client().Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
	})
}

func TestActorNodeAuthorizationMasksExistenceAndReusesCheckedProfile(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			path := filepath.Join(t.TempDir(), "policy.yaml")
			policy := "apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles: {}\n"
			if allowed {
				policy = "apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  reader:\n    rules: [{resources: [user], verbs: [read], resourceNames: [alice]}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: reader}\n    subjects: [{kind: User, name: remote-human}]\n"
			}

			require.NoError(t, os.WriteFile(path, []byte(policy), 0600))
			authorizer, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
			require.NoError(t, err)
			calls := 0
			directory := &webSocketDirectory{lease: time.Minute, profile: &auth.UserProfile{
				Subject: "directory-id", Username: "alice", Kind: auth.ProfileStaff, Active: true,
				Lifecycle: auth.ActorActive, DisplayName: "Checked profile",
				Metadata: &auth.DirectoryMetadata{UID: "directory-id", Name: "alice", Generation: 1, ResourceVersion: "1"},
			}}
			directory.onLookup = func() {
				calls++
				if calls > 2 {
					directory.profile.DisplayName = "Unchecked replacement"
				}
			}
			registry := auth.NewProviderRegistry(auth.NewChainedAuthN(&directoryWebSocketAuth{}), authorizer, directory)
			handler, err := NewGraphQLHandler(GraphQLHandlerDeps{
				Endpoint: config.DefaultEndpoint("admin"), Store: store, Registry: registry, Logger: zap.NewNop(),
				IDs: apiruntime.NewSequenceIDGenerator(), RegisterCleanup: func(close func()) { t.Cleanup(close) },
			})
			require.NoError(t, err)
			for _, uid := range []string{"directory-id", "missing-id"} {
				calls = 0
				id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + uid))
				request := httptest.NewRequest(http.MethodPost, "/admin/graphql",
					strings.NewReader(fmt.Sprintf(`{"query":"{node(id:\"%s\"){id ... on Staff{fullName metadata{name uid}}}}"}`, id)))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer trusted")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if allowed && uid == "directory-id" {
					require.Contains(t, response.Body.String(), `"fullName":"Checked profile"`)
					require.Equal(t, 2, calls, "resolution must reuse the exact profile checked by user.read")
				} else {
					require.Contains(t, response.Body.String(), `"code":"FORBIDDEN"`)
					require.NotContains(t, response.Body.String(), "alice")
					require.NotContains(t, response.Body.String(), "directory-id")
				}
			}
		})
	}
}

func TestAdmissionRejectsLargeChunkedBodyBeforeExecution(t *testing.T) {
	limits := config.DefaultEndpoint("admin").Limits
	limits.BodyBytes = 32
	gate := newEndpointAdmission("admin", limits)
	r := gin.New()
	r.POST("/admin/graphql", gate.middleware(), func(c *gin.Context) { t.Error("oversized body executed"); c.Status(200) })
	request := httptest.NewRequest(http.MethodPost, "/admin/graphql", strings.NewReader(strings.Repeat("x", 33)))
	request.ContentLength = -1
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestAdmissionDeadlineReleasesSlotAndPublicNeverReadsBody(t *testing.T) {
	l := config.DefaultEndpoint("admin").Limits
	l.Deadline = time.Millisecond
	gate := newEndpointAdmission("admin", l)
	r := gin.New()
	r.POST("/", gate.middleware(), func(c *gin.Context) { <-c.Request.Context().Done(); c.Status(408) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	require.Equal(t, 408, w.Code)
	require.Empty(t, gate.httpSlots)
	p, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	request := httptest.NewRequest("POST", "/storefront/graphql", nil)
	request.ContentLength = -1
	request.Body = panicReadBody{}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, request)
	require.Equal(t, 503, w.Code)
}

type panicReadBody struct{}

func (panicReadBody) Read([]byte) (int, error) { panic("public endpoint consumed body") }
func (panicReadBody) Close() error             { return nil }

type heldResponse struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
}

func (w heldResponse) Write(body []byte) (int, error) {
	close(w.started)
	<-w.release
	return w.ResponseRecorder.Write(body)
}

func TestBlockedPublicResponseDoesNotConsumeAdminCapacity(t *testing.T) {
	profile := config.DefaultEndpoint("storefront")
	profile.Limits.InFlight = 1
	public, err := NewStorefrontHandler(profile, config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		public.ServeHTTP(heldResponse{httptest.NewRecorder(), started, release}, httptest.NewRequest("POST", "/storefront/graphql", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	public.ServeHTTP(w, httptest.NewRequest("POST", "/storefront/graphql", nil))
	require.Equal(t, 429, w.Code)
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	r, err := http.NewRequest("POST", env.server.URL+"/admin/graphql", strings.NewReader(`{"query":"{__typename}"}`))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer valid")
	response, err := env.server.Client().Do(r)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	_ = response.Body.Close()
	close(release)
	<-done
}

func TestAdmissionHasNoWaitingQueueAndReleasesCapacity(t *testing.T) {
	limits := config.DefaultEndpoint("admin").Limits
	limits.InFlight = 1
	gate := newEndpointAdmission("admin", limits)
	started, release := make(chan struct{}), make(chan struct{})
	r := gin.New()
	r.POST("/admin/graphql", gate.middleware(), func(c *gin.Context) { close(started); <-release; c.Status(200) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/admin/graphql", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/graphql", nil))
	require.Equal(t, 429, w.Code)
	close(release)
	<-done
	require.Empty(t, gate.httpSlots)
}

type actorListingDirectory struct {
	*webSocketDirectory
	listCalls  int
	writeCalls int
}

func (d *actorListingDirectory) UpsertProfile(_ context.Context, profile *auth.UserProfile) error {
	d.writeCalls++
	d.profile = profile
	return nil
}

func (d *actorListingDirectory) ResolvePrincipal(ctx context.Context, principal *auth.Principal) (*auth.UserProfile, error) {
	return d.GetBySubject(ctx, principal.Subject)
}

func (d *actorListingDirectory) ListActors(_ context.Context, request auth.ActorPageRequest) (*auth.ActorPage, error) {
	if err := request.ValidateForward(); err != nil {
		return nil, err
	}
	d.listCalls++
	return &auth.ActorPage{Edges: []auth.ActorEdge{{Cursor: "opaque-edge", Profile: d.profile}}}, nil
}

func TestUnifiedActorsRequiresBothListGrantsBeforeEnumeration(t *testing.T) {
	for _, grants := range []string{"none", "user", "serviceAccount", "both"} {
		t.Run(grants, func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			resources := grants
			if grants == "both" {
				resources = "user, serviceAccount"
			}
			if grants == "none" {
				resources = "product"
			}
			path := filepath.Join(t.TempDir(), "policy.yaml")
			require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  reader:\n    rules: [{resources: [%s], verbs: [list]}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: reader}\n    subjects: [{kind: User, name: remote-human}]\n", resources)), 0600))
			authorizer, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
			require.NoError(t, err)
			directory := &actorListingDirectory{webSocketDirectory: &webSocketDirectory{lease: time.Minute, profile: &auth.UserProfile{
				Subject: "directory-id", Username: "alice", Kind: auth.ProfileStaff, Active: true, Lifecycle: auth.ActorActive,
				Metadata: &auth.DirectoryMetadata{UID: "directory-id", Name: "alice", Generation: 1, ResourceVersion: "1"},
			}}}
			registry := auth.NewProviderRegistry(auth.NewChainedAuthN(&directoryWebSocketAuth{}), authorizer, directory)
			handler, err := NewGraphQLHandler(GraphQLHandlerDeps{
				Endpoint: config.DefaultEndpoint("admin"), Store: store, Registry: registry, Logger: zap.NewNop(),
				IDs: apiruntime.NewSequenceIDGenerator(), RegisterCleanup: func(close func()) { t.Cleanup(close) },
			})
			require.NoError(t, err)
			for _, args := range []string{"first:1", "last:1"} {
				directory.listCalls = 0
				request := httptest.NewRequest(http.MethodPost, "/admin/graphql",
					strings.NewReader(fmt.Sprintf(`{"query":"{actors(%s){edges{cursor node{id}} pageInfo{hasNextPage endCursor}}}"}`, args)))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer trusted")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if grants != "both" {
					require.Contains(t, response.Body.String(), `"code":"FORBIDDEN"`)
					require.Zero(t, directory.listCalls, "denied lists must not enumerate or expose PageInfo")
				} else if args == "last:1" {
					require.Contains(t, response.Body.String(), `"reason":"UNSUPPORTED_PAGINATION"`)
					require.Zero(t, directory.listCalls, "unsupported direction must not enumerate")
				} else {
					require.Contains(t, response.Body.String(), `"cursor":"opaque-edge"`)
					require.Contains(t, response.Body.String(), `"endCursor":"opaque-edge"`)
					require.Equal(t, 1, directory.listCalls)
				}
			}
		})
	}
}

func TestHumanMutationsRequireTheirOwnActionAndConcealDeniedTargets(t *testing.T) {
	for _, allowUpdate := range []bool{false, true} {
		t.Run(fmt.Sprint(allowUpdate), func(t *testing.T) {
			store, err := memdb.New()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			verbs := "read"
			if allowUpdate {
				verbs = "update"
			}
			path := filepath.Join(t.TempDir(), "policy.yaml")
			require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("apiVersion: rbac.authorization.gitstore.dev/v1beta1\nkind: RbacLocalConfiguration\nclusterRoles:\n  reader:\n    rules: [{resources: [user], verbs: [%s]}]\nclusterRoleBindings:\n  - roleRef: {kind: ClusterRole, name: reader}\n    subjects: [{kind: User, name: remote-human}]\n", verbs)), 0600))
			authorizer, err := rbaclocal.New(config.RBACConfig{PolicyFile: path}, zap.NewNop())
			require.NoError(t, err)
			directory := &actorListingDirectory{webSocketDirectory: &webSocketDirectory{lease: time.Minute, profile: &auth.UserProfile{
				Subject: "directory-id", Username: "alice", Kind: auth.ProfileStaff, Active: true, Lifecycle: auth.ActorActive,
				Metadata: &auth.DirectoryMetadata{UID: "directory-id", Name: "alice", Generation: 1, ResourceVersion: "1"},
			}}}
			registry := auth.NewProviderRegistry(auth.NewChainedAuthN(&directoryWebSocketAuth{}), authorizer, directory)
			handler, err := NewGraphQLHandler(GraphQLHandlerDeps{Endpoint: config.DefaultEndpoint("admin"), Store: store,
				Registry: registry, Logger: zap.NewNop(), IDs: apiruntime.NewSequenceIDGenerator(),
				RegisterCleanup: func(close func()) { t.Cleanup(close) }})
			require.NoError(t, err)
			for _, uid := range []string{"directory-id", "missing-id"} {
				id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Staff/" + uid))
				request := httptest.NewRequest(http.MethodPost, "/admin/graphql",
					strings.NewReader(fmt.Sprintf(`{"query":"mutation{updateUser(input:{id:\"%s\",fullName:\"Changed\"}){actor{id ... on Staff{fullName}}}}"}`, id)))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Authorization", "Bearer trusted")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if !allowUpdate {
					require.Contains(t, response.Body.String(), `"code":"FORBIDDEN"`)
					require.NotContains(t, response.Body.String(), "alice")
					require.Zero(t, directory.writeCalls)
				} else if uid == "directory-id" {
					require.Contains(t, response.Body.String(), `"fullName":"Changed"`)
					require.Equal(t, 1, directory.writeCalls, "update permission does not require a separate read grant")
				} else {
					require.Contains(t, response.Body.String(), `"code":"NOT_FOUND"`)
					require.Equal(t, 1, directory.writeCalls)
				}
			}
		})
	}
}
