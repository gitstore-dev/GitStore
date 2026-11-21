// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/opa"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/watchjournal"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type inspectionProvenanceGit struct {
	GitWriter
	mu            sync.Mutex
	calls         int
	fail          error
	wrongSnapshot bool
	mutate        func(*gitv1.GetFileProvenanceResponse)
}

func (g *inspectionProvenanceGit) ReadFileProvenanceForRepo(_ context.Context, repository, path, sha string) (*gitv1.GetFileProvenanceResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.fail != nil {
		return nil, g.fail
	}
	if g.wrongSnapshot {
		sha = strings.Repeat("b", 40)
	}
	result := &gitv1.GetFileProvenanceResponse{SnapshotCommitSha: sha, ChangeCommitSha: strings.Repeat("a", 40),
		Path: path, AuthorName: "original Git author", AuthorTimeUnix: 1_700_000_000, CommitsExamined: 3}
	if g.mutate != nil {
		g.mutate(result)
	}
	return result, nil
}

type inspectionProvider struct {
	*opa.Engine
	cluster    *opa.Translation
	namespaces map[string]*opa.Translation
}

func (*inspectionProvider) Name() string { return "opa" }
func (p *inspectionProvider) Authorize(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	namespace, _ := resource.Attrs["namespace"].(string)
	return p.Engine.Evaluate(ctx, p.cluster, p.namespaces[namespace], principal, action, resource)
}

func refreshInspectionModel(t *testing.T, root *Resolver) {
	t.Helper()
	engine, err := opa.NewEngine(t.Context())
	require.NoError(t, err)
	provider := &inspectionProvider{Engine: engine, namespaces: map[string]*opa.Translation{}}
	snapshot, err := root.rbacValidator.Capture.Reader.Read(t.Context(), rbac.ClusterTierID)
	require.NoError(t, err)
	provider.cluster, err = opa.Translate(opa.Tier{ID: rbac.ClusterTierID, Active: true,
		Resources: snapshot.Resources, Builtins: root.rbacValidator.Builtins})
	require.NoError(t, err)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	snapshot, err = root.rbacValidator.Capture.Reader.Read(t.Context(), parent.UID)
	require.NoError(t, err)
	provider.namespaces["shop"], err = opa.Translate(opa.Tier{ID: parent.UID, Namespace: "shop", Active: true, Resources: snapshot.Resources})
	require.NoError(t, err)
	provider.namespaces["other"], err = opa.Translate(opa.Tier{ID: uuid.NewString(), Namespace: "other", Active: true})
	require.NoError(t, err)
	root.registry = auth.NewProviderRegistry(nil, provider, nil)
	root.rbacValidator.Capture.Registry = root.registry
}

func inspectionFixture(t *testing.T) (*Resolver, map[string]any, rbac.ResourceKey) {
	t.Helper()
	root, git, store := newRBACResourceFixture(t)
	_, rejected := rbacMutationRequest(t, root, "Create", "ClusterRole", map[string]any{
		"metadata": map[string]any{"name": "auditor"},
		"spec": map[string]any{"rules": []any{map[string]any{
			"resources": []string{"role", "roleBinding", "clusterRole", "clusterRoleBinding"}, "verbs": []string{"read", "list", "watch"},
		}}},
	})
	require.Empty(t, rejected)
	var unresolved map[string]any
	for _, binding := range []struct{ kind, name, subject, role string }{
		{"RoleBinding", "auditor", "auditor", "auditor"},
		{"ClusterRoleBinding", "cluster-auditor", "cluster-auditor", "auditor"},
		{"RoleBinding", "viewer", "viewer", "view"},
		{"RoleBinding", "unresolved", "future-subject", "not-created"},
	} {
		metadata := map[string]any{"name": binding.name}
		if binding.kind == "RoleBinding" {
			metadata["namespace"] = "shop"
		}
		node, rejected := rbacMutationRequest(t, root, "Create", binding.kind, map[string]any{
			"metadata": metadata, "spec": map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": binding.role},
				"subjects": []any{map[string]any{"kind": "USER", "name": binding.subject}}},
		})
		require.Empty(t, rejected)
		parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
		require.NoError(t, err)
		tier := rbac.ClusterTierID
		if binding.kind == "RoleBinding" {
			tier = parent.UID
		}
		key := rbac.ResourceKey{TierID: tier, Kind: rbac.Kind(binding.kind), Name: binding.name}
		row, err := store.GetRBAC(t.Context(), key)
		require.NoError(t, err)
		if binding.name != "unresolved" {
			_, err = (rbac.References{Store: store, Builtins: root.rbacValidator.Builtins}).Resolve(t.Context(), key, row.Metadata.UID, row.Metadata.ResourceVersion)
			require.NoError(t, err)
		} else {
			unresolved = node
		}
	}
	refreshInspectionModel(t, root)
	parent, err := root.store.GetNamespaceByName(t.Context(), "shop")
	require.NoError(t, err)
	repository := git.origins[parent.UID].RepositoryID
	require.NoError(t, root.store.CreateRepository(t.Context(), &datastore.Repository{
		UID: repository, ID: repository, Namespace: parent.Name, NamespaceID: parent.UID,
		Name: SystemRepositoryName, DefaultBranch: "main",
	}))
	return root, unresolved, rbac.ResourceKey{TierID: parent.UID, Kind: rbac.RoleBinding, Name: "unresolved"}
}

func TestRBACInspectionScopesListsAndDoesNotGrantViewRolePolicyAccess(t *testing.T) {
	root, _, _ := inspectionFixture(t)
	query := `query($input:String!){roleBindings(namespace:$input,first:2){
	  edges{node{metadata{name} status{resolved{roleUID}}}} pageInfo{hasNextPage endCursor}
	}}`
	auditor := &auth.Principal{Subject: "auditor", AuthMethod: "static-users"}
	data, rejected := managedContractRequest(t, root, auditor, query, "shop")
	require.Empty(t, rejected)
	require.Len(t, data["roleBindings"]["edges"], 2)
	require.Equal(t, true, data["roleBindings"]["pageInfo"].(map[string]any)["hasNextPage"])
	_, rejected = managedContractRequest(t, root, auditor, query, "other")
	require.Len(t, rejected, 1)
	require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	_, rejected = managedContractRequest(t, root, &auth.Principal{Subject: "viewer", AuthMethod: "static-users"}, query, "shop")
	require.Len(t, rejected, 1)
	require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	data, rejected = managedContractRequest(t, root, &auth.Principal{Subject: "cluster-auditor", AuthMethod: "static-users"},
		`query{clusterRoles(first:100){edges{node{metadata{name} spec{rules{resources verbs}}}}}}`, nil)
	require.Empty(t, rejected)
	found := false
	for _, edge := range data["clusterRoles"]["edges"].([]any) {
		node := edge.(map[string]any)["node"].(map[string]any)
		if node["metadata"].(map[string]any)["name"] == "view" {
			found = true
		}
	}
	require.True(t, found, "authorized inspection includes immutable virtual built-ins")
}

func TestRBACInspectionShowsUnresolvedConditionsAndImmutableResolvedUID(t *testing.T) {
	root, node, key := inspectionFixture(t)
	_, rejected := rbacGraphQLRequest(t, root, `mutation($input:UpdateResourceStatusInput!){
	  updateResourceStatus(input:$input){object}
	}`, map[string]any{"kind": "RoleBinding", "namespace": "shop", "name": key.Name,
		"resourceVersion": node["metadata"].(map[string]any)["resourceVersion"], "observedGeneration": 1,
		"resolved": map[string]any{}, "conditions": []any{map[string]any{
			"type": "RoleResolved", "status": "FALSE", "reason": "RoleNotFound", "message": "the referenced role is absent",
			"observedGeneration": 1, "lastTransitionTime": time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		}}})
	require.Empty(t, rejected)
	query := `query($input:RoleBindingBy!){roleBinding(by:$input){
	  metadata{uid deletionTimestamp} status{conditions{type status reason} resolved{roleUID roleKind roleName}}
	}}`
	auditor := &auth.Principal{Subject: "auditor", AuthMethod: "static-users"}
	data, rejected := managedContractRequest(t, root, auditor, query, map[string]any{"id": node["id"]})
	require.Empty(t, rejected)
	status := data["roleBinding"]["status"].(map[string]any)
	require.Nil(t, status["resolved"])
	require.Equal(t, "RoleNotFound", status["conditions"].([]any)[0].(map[string]any)["reason"])
	_, rejected = rbacMutationRequest(t, root, "Create", "ClusterRole", map[string]any{
		"metadata": map[string]any{"name": "not-created"}, "spec": map[string]any{"rules": []any{}},
	})
	require.Empty(t, rejected)
	store, err := root.rbacStore()
	require.NoError(t, err)
	row, err := store.GetRBAC(t.Context(), key)
	require.NoError(t, err)
	resolved, err := (rbac.References{Store: store, Builtins: root.rbacValidator.Builtins}).Resolve(t.Context(), key, row.Metadata.UID, row.Metadata.ResourceVersion)
	require.NoError(t, err)
	roleUID, err := rbac.ResolvedRoleUID(resolved)
	require.NoError(t, err)
	data, rejected = managedContractRequest(t, root, auditor, query, map[string]any{"id": node["id"]})
	require.Empty(t, rejected)
	require.Equal(t, roleUID, data["roleBinding"]["status"].(map[string]any)["resolved"].(map[string]any)["roleUID"])
	resolved.Status = json.RawMessage(`{"resolved":{"roleRef":{"uid":"` + uuid.NewString() + `"}}}`)
	state, err := store.ReadRBACTier(t.Context(), key.TierID)
	require.NoError(t, err)
	_, err = store.ApplyRBAC(t.Context(), rbac.TierWrite{TierID: key.TierID, ExpectedRevision: state.Revision,
		Changes: []rbac.Change{{Resource: resolved, ExpectedUID: resolved.Metadata.UID, ExpectedResourceVersion: resolved.Metadata.ResourceVersion}}})
	require.Error(t, err, "published resolution cannot be replaced with another UID")
}

func TestRBACInspectionDoesNotRevealUnauthorizedTargetExistence(t *testing.T) {
	root, node, _ := inspectionFixture(t)
	missingID, err := EncodeNodeID("RoleBinding", uuid.NewString())
	require.NoError(t, err)
	viewer := &auth.Principal{Subject: "viewer", AuthMethod: "static-users"}
	for _, id := range []any{node["id"], missingID} {
		data, rejected := managedContractRequest(t, root, viewer,
			`query($input:RoleBindingBy!){roleBinding(by:$input){metadata{uid}}}`, map[string]any{"id": id})
		require.Nil(t, data["roleBinding"])
		require.Len(t, rejected, 1)
		require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	}

	auditor := &auth.Principal{Subject: "auditor", AuthMethod: "static-users"}
	for _, name := range []string{"auditor", "absent"} {
		_, rejected := managedContractRequest(t, root, auditor,
			`query($input:RoleBindingBy!){roleBinding(by:$input){metadata{uid}}}`,
			map[string]any{"namespacePath": map[string]any{"namespace": "other", "name": name}})
		require.Len(t, rejected, 1)
		require.Equal(t, "FORBIDDEN", rejected[0].Extensions["code"])
	}
}

func TestRBACInspectionProvenanceIsOptInAuthorizedAndNotTheLatestTierActor(t *testing.T) {
	root, node, _ := inspectionFixture(t)
	reader := &inspectionProvenanceGit{GitWriter: root.service.gitWriter}
	root.service.gitWriter = reader
	auditor := &auth.Principal{Subject: "auditor", AuthMethod: "static-users"}
	query := `query($input:RoleBindingBy!){roleBinding(by:$input){
		  metadata{name revision}
		}}`
	input := map[string]any{"id": node["id"]}
	_, rejected := managedContractRequest(t, root, auditor,
		`query($input:RoleBindingBy!){roleBinding(by:$input){metadata{name}}}`, input)
	require.Empty(t, rejected)
	require.Zero(t, reader.calls, "ordinary resource reads must not walk Git history")
	response := managedContractResponse(t, root, auditor, query, input, true)
	require.Empty(t, response.Errors)
	entries := response.Extensions["provenance"].([]any)
	require.Len(t, entries, 1)
	source := entries[0].(map[string]any)
	require.Equal(t, "original Git author", source["author"])
	require.Equal(t, "rbac/rolebindings/unresolved.yaml", source["file"])
	require.Equal(t, []any{"roleBinding"}, source["path"])
	require.Equal(t, "shop/"+SystemRepositoryName, source["repositoryRef"])
	require.Equal(t, "refs/heads/main", source["ref"])
	revision := response.Data["roleBinding"]["metadata"].(map[string]any)["revision"].(string)
	require.Equal(t, strings.TrimPrefix(revision, "main@"), source["commit"])
	require.Equal(t, "sha1:"+strings.Repeat("a", 40), source["changeCommit"])
	require.Nil(t, source["builtinName"])
	require.Equal(t, 1, reader.calls)
	response = managedContractResponse(t, root, &auth.Principal{Subject: "viewer", AuthMethod: "static-users"}, query, input, true)
	require.Len(t, response.Errors, 1)
	require.NotContains(t, response.Extensions, "provenance")
	require.Equal(t, 1, reader.calls, "unauthorized inspection cannot trigger history lookup")
	response = managedContractResponse(t, root, &auth.Principal{Subject: "cluster-auditor", AuthMethod: "static-users"},
		`query{clusterRole(by:{name:"view"}){metadata{name}}}`, nil, true)
	require.Empty(t, response.Errors)
	builtin := response.Extensions["provenance"].([]any)[0].(map[string]any)
	require.Equal(t, "view", builtin["builtinName"])
	require.Equal(t, root.rbacBuiltinDigest, builtin["builtinDigest"])
	require.Nil(t, builtin["author"])
	require.Nil(t, builtin["changeCommit"])
	require.Equal(t, 1, reader.calls, "named release built-ins require no Git history")
}

func TestRBACInspectionProvenanceFailsClosedForHistoryGapsAndWrongSnapshot(t *testing.T) {
	for _, scenario := range []string{"history limit", "missing history", "wrong snapshot", "wrong path", "long author", "invalid time", "saturated", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			root, node, _ := inspectionFixture(t)
			reader := &inspectionProvenanceGit{GitWriter: root.service.gitWriter}
			switch scenario {
			case "history limit":
				reader.fail = grpcstatus.Error(codes.ResourceExhausted, "history limit")
			case "missing history":
				reader.fail = grpcstatus.Error(codes.NotFound, "history gap")
			case "wrong snapshot":
				reader.wrongSnapshot = true
			case "wrong path":
				reader.mutate = func(result *gitv1.GetFileProvenanceResponse) { result.Path = "rbac/roles/other.yaml" }
			case "long author":
				reader.mutate = func(result *gitv1.GetFileProvenanceResponse) { result.AuthorName = strings.Repeat("private", 1024) }
			case "invalid time":
				reader.mutate = func(result *gitv1.GetFileProvenanceResponse) { result.AuthorTimeUnix = 253402300800 }
			case "saturated":
				for range cap(root.rbacSourceReads) {
					root.rbacSourceReads <- struct{}{}
				}
			}
			root.service.gitWriter = reader
			if scenario == "unsupported" {
				root.service.gitWriter = reader.GitWriter
			}
			response := managedContractResponse(t, root, &auth.Principal{Subject: "auditor", AuthMethod: "static-users"},
				`query($input:RoleBindingBy!){roleBinding(by:$input){metadata{name}}}`, map[string]any{"id": node["id"]}, true)
			require.Empty(t, response.Errors)
			entry := response.Extensions["provenance"].([]any)[0].(map[string]any)
			require.Equal(t, "PROVENANCE_UNAVAILABLE", entry["error"])
			require.NotContains(t, entry, "author")
			require.Equal(t, "unresolved", response.Data["roleBinding"]["metadata"].(map[string]any)["name"])
			if scenario == "saturated" || scenario == "unsupported" {
				require.Zero(t, reader.calls)
			}
		})
	}
}

func TestRBACInspectionProvenanceIdentifiesAliasesAndConnectionIndices(t *testing.T) {
	root, node, _ := inspectionFixture(t)
	reader := &inspectionProvenanceGit{GitWriter: root.service.gitWriter}
	root.service.gitWriter = reader
	response := managedContractResponse(t, root, &auth.Principal{Subject: "auditor", AuthMethod: "static-users"},
		`query($input:RoleBindingBy!){
			first:roleBinding(by:$input){id}
			second:roleBinding(by:$input){id}
			connections:roleBindings(namespace:"shop",first:10){edges{node{id}}}
		}`, map[string]any{"id": node["id"]}, true)
	require.Empty(t, response.Errors)
	edges := response.Data["connections"]["edges"].([]any)
	entries := response.Extensions["provenance"].([]any)
	require.Len(t, entries, len(edges)+2)
	seen := map[string]bool{}
	for _, value := range entries {
		entry := value.(map[string]any)
		require.NotContains(t, entry, "error")
		path := entry["path"].([]any)
		rootField := path[0].(string)
		switch rootField {
		case "first", "second":
			require.Equal(t, []any{rootField}, path)
			require.Equal(t, node["id"], entry["id"])
		case "connections":
			require.Len(t, path, 4)
			require.Equal(t, "edges", path[1])
			require.Equal(t, "node", path[3])
			index := int(path[2].(float64))
			require.Equal(t, edges[index].(map[string]any)["node"].(map[string]any)["id"], entry["id"])
		default:
			t.Fatalf("unexpected provenance response path: %v", path)
		}
		key := fmt.Sprint(path)
		require.False(t, seen[key])
		seen[key] = true
	}
}

func TestRBACInspectionProvenanceBoundsTotalEntries(t *testing.T) {
	root, _, _ := inspectionFixture(t)
	reader := &inspectionProvenanceGit{GitWriter: root.service.gitWriter}
	root.service.gitWriter = reader
	var query strings.Builder
	query.WriteString("query{")
	for index := range 65 {
		fmt.Fprintf(&query, `r%d:clusterRole(by:{name:"view"}){id}`, index)
	}
	query.WriteString("}")
	response := managedContractResponse(t, root, &auth.Principal{Subject: "cluster-auditor", AuthMethod: "static-users"},
		query.String(), nil, true)
	require.Empty(t, response.Errors)
	require.Len(t, response.Data, 65)
	entries := response.Extensions["provenance"].([]any)
	require.Len(t, entries, 65, "at most 64 diagnostics plus one truncation notice")
	require.Equal(t, map[string]any{"error": "PROVENANCE_LIMIT_EXCEEDED"}, entries[64])
	require.Zero(t, reader.calls)
}

func TestRBACInspectionAuthorizedWatchResumesConditions(t *testing.T) {
	original, _, _ := inspectionFixture(t)
	journal := original.store.(datastore.NamespaceWatchCapable).NamespaceWatchJournal()
	lease, acquired, err := journal.AcquireLease(t.Context(), "inspection-test", time.Now(), time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	_, err = journal.Append(t.Context(), lease, datastore.ResourceWatchEvent{
		Type: datastore.ResourceWatchBookmark, At: time.Now(),
	}, time.Hour)
	require.NoError(t, err)
	root, err := NewResolver(ResolverDeps{Store: original.store, GitWriter: original.service.gitWriter,
		Registry: original.registry, RBACValidator: original.rbacValidator, ResourceJournal: journal, Logger: zap.NewNop(),
		NamespaceWatch: config.WatchJournalConfig{
			Read:         config.ReadWatchConfig{BatchSize: 64, MaxReplayEvents: 1000},
			Subscriber:   config.SubscriberWatchConfig{Buffer: 8, Backpressure: time.Second},
			Poll:         config.PollWatchConfig{Min: time.Millisecond, Max: 10 * time.Millisecond},
			Materializer: config.MaterializerWatchConfig{MaxLag: time.Minute},
		}})
	require.NoError(t, err)
	namespace, cursor := "shop", watchjournal.BootstrapCursor
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "auditor", AuthMethod: "static-users"})
	generic, err := root.Subscription().WatchResources(ctx, "RoleBinding", &namespace, nil, &cursor)
	require.NoError(t, err)
	typed, err := root.Subscription().WatchRoleBindings(ctx, &namespace, nil, &cursor)
	require.NoError(t, err)
	require.Equal(t, model.WatchEventTypeBookmark, receiveRBACWatchEvent(t, generic).Type)
	require.Equal(t, model.WatchEventTypeBookmark, receiveRBACWatchEvent(t, typed).Type)
	other := "other"
	_, err = root.Subscription().WatchRoleBindings(ctx, &other, nil, &cursor)
	require.Error(t, err)
	_, err = root.Subscription().WatchClusterRoles(ctx, nil, &cursor)
	require.Error(t, err)
	viewer := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "viewer", AuthMethod: "static-users"})
	_, err = root.Subscription().WatchRoleBindings(viewer, &namespace, nil, &cursor)
	require.Error(t, err)
	node, rejected := rbacMutationRequest(t, root, "Create", "RoleBinding", map[string]any{
		"metadata": map[string]any{"name": "watch-unresolved", "namespace": namespace},
		"spec": map[string]any{"roleRef": map[string]any{"kind": "CLUSTER_ROLE", "name": "absent"},
			"subjects": []any{map[string]any{"kind": "USER", "name": "future"}}},
	})
	require.Empty(t, rejected)
	next := func() *model.WatchEvent {
		t.Helper()
		for range 8 {
			event := receiveRBACWatchEvent(t, generic)
			if event.Type != model.WatchEventTypeBookmark {
				return event
			}
		}
		t.Fatal("no data event after bounded progress")
		return nil
	}
	added := next()
	require.Equal(t, model.WatchEventTypeAdded, added.Type)
	typedAdded := false
	for range 8 {
		event := receiveRBACWatchEvent(t, typed)
		if event.Type != model.WatchEventTypeBookmark {
			require.Equal(t, added.ResourceVersion, event.ResourceVersion)
			require.Equal(t, "watch-unresolved", event.RoleBinding.Metadata.Name)
			typedAdded = true
			break
		}
	}
	require.True(t, typedAdded)
	_, rejected = rbacGraphQLRequest(t, root,
		`mutation($input:UpdateResourceStatusInput!){updateResourceStatus(input:$input){object}}`,
		map[string]any{"kind": "RoleBinding", "namespace": namespace, "name": "watch-unresolved",
			"resourceVersion": node["metadata"].(map[string]any)["resourceVersion"],
			"resolved":        map[string]any{}, "conditions": []any{map[string]any{
				"type": "RoleResolved", "status": "FALSE", "reason": "RoleNotFound",
				"observedGeneration": 1, "lastTransitionTime": time.Now().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
			}}})
	require.Empty(t, rejected)
	modified := next()
	require.Equal(t, model.WatchEventTypeModified, modified.Type)
	status := modified.Object["status"].(map[string]any)
	require.Equal(t, "FALSE", status["conditions"].([]any)[0].(map[string]any)["status"])
	resumed, err := root.Subscription().WatchRoleBindings(ctx, &namespace, nil, &added.ResourceVersion)
	require.NoError(t, err)
	for range 8 {
		replay := receiveRBACWatchEvent(t, resumed)
		if replay.Type == model.WatchEventTypeBookmark {
			continue
		}
		require.Equal(t, modified.ResourceVersion, replay.ResourceVersion)
		require.Equal(t, model.WatchEventTypeModified, replay.Type)
		require.Equal(t, model.ConditionStatusFalse, replay.RoleBinding.Status.Conditions[0].Status)
		require.Nil(t, replay.RoleBinding.Status.Resolved)
		return
	}
	t.Fatal("resumed watch did not replay the condition update")
}
