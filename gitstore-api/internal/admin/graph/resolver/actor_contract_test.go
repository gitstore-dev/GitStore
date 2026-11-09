// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/generated"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestUserInterfaceExcludesWorkloadsAndPreservesActorPayloads(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	user := schema.Types["User"]
	require.NotNil(t, user)
	require.ElementsMatch(t, []string{"Actor", "Node"}, user.Interfaces)
	for _, kind := range []string{"Staff", "Customer"} {
		require.Contains(t, schema.Types[kind].Interfaces, "User")
		for _, field := range schema.Types[kind].Fields {
			common := user.Fields.ForName(field.Name)
			require.NotNil(t, common, field.Name)
			require.Equal(t, field.Type.String(), common.Type.String())
		}
	}
	require.NotContains(t, schema.Types["ServiceAccount"].Interfaces, "User")
	require.Equal(t, "[String!]!", schema.Types["ServiceAccount"].Fields.ForName("groups").Type.String())
	for _, payload := range []string{"AssignUserGroupPayload", "RemoveUserGroupPayload"} {
		require.Equal(t, "Actor", schema.Types[payload].Fields.ForName("actor").Type.Name())
	}
}

func TestActorSchemaImplementsNodeWithConcreteHumanEnvelopes(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	actor := schema.Types["Actor"]
	require.Contains(t, actor.Interfaces, "Node")
	for _, kind := range []string{"Staff", "Customer", "ServiceAccount"} {
		object := schema.Types[kind]
		require.NotNil(t, object, kind)
		require.Contains(t, object.Interfaces, "Actor")
		require.Contains(t, object.Interfaces, "Node")
		for _, field := range []string{"id", "apiVersion", "kind", "metadata", "lifecycle"} {
			require.NotNil(t, object.Fields.ForName(field), kind+"."+field)
		}

		if kind != "ServiceAccount" {
			for _, field := range []string{"fullName", "identities", "groups", "primaryEmail", "emailVerified", "emailDeliverable", "contactConsent"} {
				require.NotNil(t, object.Fields.ForName(field), kind+"."+field)
			}
			for _, field := range []string{"subject", "username", "active", "createdAt"} {
				require.Nil(t, object.Fields.ForName(field), "human identity fields must remain in the reviewed envelope")
			}
		}
	}
}

func TestActorConnectionSelectorsUseReviewedRelayArguments(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	by := schema.Types["ActorBy"]
	require.NotNil(t, by)
	require.NotNil(t, by.Directives.ForName("oneOf"))
	require.Len(t, by.Fields, 3)
	for _, name := range []string{"id", "username", "serviceAccountName"} {
		require.NotNil(t, by.Fields.ForName(name))
	}
	for _, name := range []string{"actors", "serviceAccounts"} {
		field := schema.Query.Fields.ForName(name)
		require.NotNil(t, field)
		require.Len(t, field.Arguments, 4)
		for _, arg := range []string{"first", "after", "last", "before"} {
			require.NotNil(t, field.Arguments.ForName(arg))
		}
		connection := schema.Types[field.Type.Name()]
		require.NotNil(t, connection.Fields.ForName("edges"))
		require.NotNil(t, connection.Fields.ForName("pageInfo"))
		require.Nil(t, connection.Fields.ForName("totalCount"))
	}
}

type actorContractDirectory struct {
	auth.UserDirProvider
	profiles    []*auth.UserProfile
	page        *auth.ActorPage
	err         error
	searchCalls int
}

func TestHumanAdministrationSchemaUsesActionSpecificInputsAndPayloads(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{}).Schema()
	for _, action := range []string{"createStaff", "updateUser", "deactivateUser", "reactivateUser", "suspendUser",
		"resendStaffInvitation", "deleteUser", "assignUserGroup", "removeUserGroup", "setUserEmailDeliverability"} {
		field := schema.Mutation.Fields.ForName(action)
		require.NotNil(t, field, action)
		title := strings.ToUpper(action[:1]) + action[1:]
		require.Len(t, field.Arguments, 1)
		require.Equal(t, title+"Input", field.Arguments.ForName("input").Type.Name())
		require.Equal(t, title+"Payload", field.Type.Name())
		require.True(t, field.Type.NonNull)
	}
}

func (d *actorContractDirectory) SearchUsers(context.Context, string, int) ([]*auth.UserProfile, error) {
	d.searchCalls++
	return d.profiles, d.err
}

func (d *actorContractDirectory) GetBySubject(_ context.Context, uid string) (*auth.UserProfile, error) {
	for _, profile := range d.profiles {
		if profile.Subject == uid {
			return profile, nil
		}
	}
	return nil, auth.ErrProfileNotFound
}

type actorMutationDirectory struct {
	*actorContractDirectory
	writes int
}

func (d *actorMutationDirectory) UpsertProfile(_ context.Context, profile *auth.UserProfile) error {
	d.writes++
	d.profiles = []*auth.UserProfile{profile}
	return nil
}

func TestHumanUpdatePreservesProtectedFieldsAndRejectsAdministrativeSelfEdits(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	core, entries := observer.New(zap.InfoLevel)
	h.resolver.logger = zap.New(core)
	profile := &auth.UserProfile{Subject: "human-uid", Username: "alice", Kind: auth.ProfileStaff, Lifecycle: auth.ActorActive,
		Groups:   []string{"system:staff", "system:group:operators"},
		Metadata: &auth.DirectoryMetadata{UID: "human-uid", Name: "alice", Generation: 1, ResourceVersion: "1"}}
	directory := &actorMutationDirectory{actorContractDirectory: &actorContractDirectory{profiles: []*auth.UserProfile{profile}}}
	h.resolver.registry = auth.NewProviderRegistry(nil, h.authz, directory)
	mutation := &mutationResolver{h.resolver}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "external@example.test", Username: "alice", AuthMethod: "oidc-jwt"})
	id := mustEncodeNodeID(nodeKindStaff, "human-uid")
	result, err := mutation.UpdateUser(ctx, model.UpdateUserInput{ID: id, FullName: new("Changed"), PrimaryEmail: new("replacement@example.test")})
	require.NoError(t, err)
	require.Equal(t, "Changed", result.Actor.(*model.Staff).FullName)
	require.Equal(t, "alice", directory.profiles[0].Username)
	require.Equal(t, auth.ActorActive, directory.profiles[0].Lifecycle)
	require.Equal(t, profile.Groups, directory.profiles[0].Groups)
	for _, operation := range []string{"deactivateUser", "reactivateUser", "suspendUser", "deleteUser",
		"resendStaffInvitation", "setUserEmailDeliverability", "assignUserGroup", "removeUserGroup"} {
		_, err := mutation.changeActor(ctx, id, operation, nil, new(true), "operators")
		require.ErrorContains(t, err, "self-edits", operation)
	}
	_, err = mutation.UpdateUser(ctx, model.UpdateUserInput{ID: id, PrimaryEmail: new("Alias <replacement@example.test>")})
	require.ErrorContains(t, err, "invalid actor directory input")
	require.Equal(t, 1, directory.writes)
	created, err := h.resolver.CreateServiceAccount(serviceAccountAdminContext(), serviceAccountCreateInput(t, "", "alice", "key"))
	require.NoError(t, err)
	for _, operation := range []string{"updateUser", "deactivateUser", "reactivateUser", "suspendUser", "deleteUser", "setUserEmailDeliverability"} {
		_, err = mutation.changeActor(ctx, created.ServiceAccount.ID, operation, &model.UpdateUserInput{}, new(true), "")
		require.ErrorContains(t, err, "invalid actor directory input", "source-owned workloads cannot be changed through human mutations")
	}
	require.Equal(t, 1, directory.writes)
	for _, entry := range entries.All() {
		if entry.Message != "directory mutation" {
			continue
		}
		fields := entry.ContextMap()
		require.NotContains(t, fields["actor"], "external@example.test")
		require.Contains(t, fields["actor"], "sha256:")
		require.NotContains(t, fields, "email")
		require.Contains(t, fields, "target_id")
		require.Contains(t, fields, "change")
		require.Contains(t, fields, "outcome")
		require.Contains(t, fields, "time")
	}
}

func (d *actorContractDirectory) ResolvePrincipal(context.Context, *auth.Principal) (*auth.UserProfile, error) {
	return nil, auth.ErrProfileNotFound
}

func (d *actorContractDirectory) ListActors(context.Context, auth.ActorPageRequest) (*auth.ActorPage, error) {
	return d.page, d.err
}

func TestActorSelectorsDoNotMergeHumanAndWorkloadNames(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	created, err := h.resolver.CreateServiceAccount(serviceAccountAdminContext(), serviceAccountCreateInput(t, "", "alice", "key"))
	require.NoError(t, err)
	directory := &actorContractDirectory{profiles: []*auth.UserProfile{{
		Subject: "human-uid", Username: "alice", Kind: auth.ProfileStaff, Lifecycle: auth.ActorActive,
		Metadata: &auth.DirectoryMetadata{UID: "human-uid", Name: "alice", Generation: 1, ResourceVersion: "1"},
	}}}
	h.resolver.registry = auth.NewProviderRegistry(nil, h.authz, directory)
	query := &queryResolver{h.resolver}
	human, err := query.Actor(t.Context(), model.ActorBy{Username: new("alice")})
	require.NoError(t, err)
	require.IsType(t, &model.Staff{}, human)
	account, err := query.Actor(t.Context(), model.ActorBy{ServiceAccountName: new("alice")})
	require.NoError(t, err)
	require.Equal(t, created.ServiceAccount.ID, account.GetID())
	require.NotEqual(t, human.GetID(), account.GetID())
	calls := directory.searchCalls
	_, err = query.Actor(t.Context(), model.ActorBy{Username: new("alice"), ServiceAccountName: new("alice")})
	require.ErrorContains(t, err, "exactly one")
	require.Equal(t, calls, directory.searchCalls)
	directory.profiles = append(directory.profiles, directory.profiles[0])
	_, err = query.Actor(t.Context(), model.ActorBy{Username: new("alice")})
	require.ErrorContains(t, err, "unavailable", "ambiguous provider results are not absence")
}

func TestSourceServiceAccountConnectionWorksWithoutDirectoryAndScopesCursors(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	for _, name := range []string{"first", "second"} {
		_, err := h.resolver.CreateServiceAccount(serviceAccountAdminContext(), serviceAccountCreateInput(t, "", name, "key"))
		require.NoError(t, err)
	}
	query := &queryResolver{h.resolver}
	first, err := query.ServiceAccounts(t.Context(), new(int32(1)), nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, first.Edges, 1)
	require.True(t, first.PageInfo.HasNextPage)
	next, err := query.ServiceAccounts(t.Context(), new(int32(1)), first.PageInfo.EndCursor, nil, nil)
	require.NoError(t, err)
	require.Len(t, next.Edges, 1)
	require.NotEqual(t, first.Edges[0].Node.ID, next.Edges[0].Node.ID)
	require.False(t, next.PageInfo.HasNextPage)
	back, err := query.ServiceAccounts(t.Context(), nil, nil, new(int32(1)), next.PageInfo.StartCursor)
	require.NoError(t, err)
	require.Equal(t, first.Edges[0].Node.ID, back.Edges[0].Node.ID)
	for _, cursor := range []string{"bad", strings.Repeat("x", 1025),
		base64.RawURLEncoding.EncodeToString([]byte("gitstore:actors:v1:foreign"))} {
		_, err = query.ServiceAccounts(t.Context(), new(int32(1)), &cursor, nil, nil)
		require.ErrorContains(t, err, "invalid ServiceAccount cursor")
	}
}

func TestUnifiedActorHydrationSkipsOrphansWithoutRebindingNames(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	created, err := h.resolver.CreateServiceAccount(serviceAccountAdminContext(), serviceAccountCreateInput(t, "", "reused", "key"))
	require.NoError(t, err)
	directory := &actorContractDirectory{page: &auth.ActorPage{Edges: []auth.ActorEdge{
		{Cursor: "stale", Profile: &auth.UserProfile{Username: "reused", Kind: auth.ProfileServiceAccount, ServiceAccountUID: "deleted-uid"}},
		{Cursor: "current", Profile: &auth.UserProfile{Username: "reused", Kind: auth.ProfileServiceAccount, ServiceAccountUID: created.ServiceAccount.Metadata.UID}},
	}}}
	h.resolver.registry = auth.NewProviderRegistry(nil, h.authz, directory)
	query := &queryResolver{h.resolver}
	page, err := query.Actors(t.Context(), new(int32(2)), nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, page.Edges, 1)
	require.Equal(t, created.ServiceAccount.ID, page.Edges[0].Node.GetID())
	require.Equal(t, "current", *page.PageInfo.StartCursor)
	require.Equal(t, "current", *page.PageInfo.EndCursor)
	directory.page.Edges = directory.page.Edges[:1]
	directory.page.HasNextPage = true
	_, err = query.Actors(t.Context(), new(int32(1)), nil, nil, nil)
	require.ErrorContains(t, err, "pagination cannot advance")
	directory.page.HasNextPage = false
	page, err = query.Actors(t.Context(), new(int32(1)), nil, nil, nil)
	require.NoError(t, err)
	require.Empty(t, page.Edges)
	require.Nil(t, page.PageInfo.EndCursor)
}

type actorGroupDirectory struct {
	*actorContractDirectory
	groupUID   string
	groupCalls int
	groups     []string
	groupErr   error
}

func (d *actorGroupDirectory) ListServiceAccountGroups(_ context.Context, uid string) ([]string, error) {
	d.groupUID = uid
	d.groupCalls++
	return d.groups, d.groupErr
}

func TestHumanUserFragmentAndServiceAccountGroupsOverGraphQL(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	account, err := h.resolver.CreateServiceAccount(serviceAccountAdminContext(), serviceAccountCreateInput(t, "", "alice", "key"))
	require.NoError(t, err)
	directory := &actorGroupDirectory{actorContractDirectory: &actorContractDirectory{profiles: []*auth.UserProfile{{
		Subject: "human-uid", Username: "alice", DisplayName: "Human Alice", Kind: auth.ProfileStaff, Lifecycle: auth.ActorActive,
		Metadata: &auth.DirectoryMetadata{UID: "human-uid", Name: "alice", Generation: 1, ResourceVersion: "1"},
	}}}, groups: []string{"system:group:ci", "system:serviceaccounts"}}
	h.resolver.registry = auth.NewProviderRegistry(nil, h.authz, directory)
	server := handler.NewDefaultServer(generated.NewExecutableSchema(generated.Config{Resolvers: h.resolver}))
	authorizer := security.NewAuthorizeWithStore(h.resolver.registry, h.store, zap.NewNop())
	server.AroundFields(authorizer.GraphQLFieldAuthorizer)
	execute := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(map[string]string{"query": query})
		require.NoError(t, err)
		request := httptest.NewRequest(http.MethodPost, "/admin/graphql", strings.NewReader(string(payload)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	response := execute(fmt.Sprintf(`{actor(by:{username:"alice"}){... on User{fullName metadata{name}}} node(id:%q){... on ServiceAccount{groups}}}`, account.ServiceAccount.ID))
	require.NotContains(t, response.Body.String(), `"errors"`)
	require.Contains(t, response.Body.String(), `"fullName":"Human Alice"`)
	require.Contains(t, response.Body.String(), `"groups":["system:group:ci","system:serviceaccounts"]`)
	require.Equal(t, account.ServiceAccount.Metadata.UID, directory.groupUID)
	directory.profiles = append(directory.profiles, &auth.UserProfile{
		Subject: "customer-uid", Username: "bob", DisplayName: "Customer Bob", Kind: auth.ProfileCustomer, Lifecycle: auth.ActorActive,
		Metadata: &auth.DirectoryMetadata{UID: "customer-uid", Name: "bob", Generation: -1, ResourceVersion: "-1"},
	})
	response = execute(fmt.Sprintf(`{node(id:%q){... on User{fullName metadata{name}}}}`, mustEncodeNodeID(nodeKindCustomer, "customer-uid")))
	require.NotContains(t, response.Body.String(), `"errors"`)
	require.Contains(t, response.Body.String(), `"fullName":"Customer Bob"`)
	h.authz.denyAction = auth.ActionServiceAccountRead
	response = execute(fmt.Sprintf(`{node(id:%q){... on ServiceAccount{groups}}}`, account.ServiceAccount.ID))
	require.Contains(t, response.Body.String(), `"code":"FORBIDDEN"`)
	require.Equal(t, 1, directory.groupCalls, "denied source reads must not look up directory memberships")
	h.authz.denyAction = ""
	directory.groupErr = auth.ErrDirectoryUnavailable
	response = execute(fmt.Sprintf(`{node(id:%q){id ... on ServiceAccount{keyIDs lifecycle{state}}}}`, account.ServiceAccount.ID))
	require.NotContains(t, response.Body.String(), `"errors"`)
	require.Equal(t, 1, directory.groupCalls, "unselected groups must not perform directory I/O")
	response = execute(fmt.Sprintf(`{node(id:%q){... on ServiceAccount{groups}}}`, account.ServiceAccount.ID))
	require.Contains(t, response.Body.String(), `"code":"SERVICE_UNAVAILABLE"`)
	directory.groupErr = auth.ErrProfileNotFound
	response = execute(fmt.Sprintf(`{node(id:%q){... on ServiceAccount{groups}}}`, account.ServiceAccount.ID))
	require.NotContains(t, response.Body.String(), `"errors"`)
	require.Contains(t, response.Body.String(), `"groups":[]`)
	h.resolver.registry = auth.NewProviderRegistry(nil, h.authz, directory.actorContractDirectory)
	response = execute(fmt.Sprintf(`{node(id:%q){... on ServiceAccount{groups}}}`, account.ServiceAccount.ID))
	require.NotContains(t, response.Body.String(), `"errors"`)
	require.Contains(t, response.Body.String(), `"groups":[]`)
	h.resolver.registry = auth.NewProviderRegistry(nil, h.authz, nil)
	response = execute(fmt.Sprintf(`{node(id:%q){... on ServiceAccount{groups}}}`, account.ServiceAccount.ID))
	require.NotContains(t, response.Body.String(), `"errors"`)
	require.Contains(t, response.Body.String(), `"groups":[]`)
}
