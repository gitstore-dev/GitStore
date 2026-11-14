// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/graphqlclient"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/listwatch"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/schemavalidate"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/stretchr/testify/require"
)

type watchFixture struct {
	data    chan json.RawMessage
	err     error
	stopped chan struct{}
	once    sync.Once
}

func (s *watchFixture) Next() <-chan json.RawMessage { return s.data }
func (s *watchFixture) Err() error                   { return s.err }
func (s *watchFixture) Stop()                        { s.once.Do(func() { close(s.stopped) }) }

func TestRBACWatchPreservesResourceVersionsAndRejectsCrossScopeImages(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "cross-scope"}[invalid], func(t *testing.T) {
			sub := &watchFixture{data: make(chan json.RawMessage, 2), stopped: make(chan struct{})}
			sub.data <- json.RawMessage(`{"watchResources":{"type":"BOOKMARK","kind":"RoleBinding","resourceVersion":"journal-10"}}`)
			namespace := "shop"
			if invalid {
				namespace = "other"
			}
			image := `{"watchResources":{"type":"ADDED","kind":"RoleBinding","namespace":"shop","name":"access","resourceVersion":"journal-11",
				"object":{"id":"node","kind":"RoleBinding","metadata":{"uid":"binding-uid","namespace":"` + namespace + `","name":"access","resourceVersion":"uid-bound-version"},
				"spec":{"roleRef":{"kind":"ROLE","name":"reader"},"subjects":[{"kind":"SERVICE_ACCOUNT","name":"ci"}]},
				"status":{"resolved":{"roleUID":"role-uid","roleKind":"ROLE","roleName":"reader"}}}}}`
			sub.data <- json.RawMessage(image)
			close(sub.data)
			watch := newResourceWatcher(t.Context(), "RoleBinding", sub)
			bookmark := <-watch.Events()
			require.Equal(t, listwatch.Bookmark, bookmark.Type)
			require.Equal(t, "journal-10", bookmark.ResourceVersion)
			if invalid {
				_, open := <-watch.Events()
				require.False(t, open)
				require.Error(t, watch.Err())
			} else {
				event := <-watch.Events()
				require.Equal(t, "journal-11", event.ResourceVersion)
				require.Equal(t, "uid-bound-version", event.Object.Metadata.ResourceVersion)
				require.Equal(t, "Role", event.Object.Spec.RoleRef.Kind)
				require.Equal(t, "ServiceAccount", event.Object.Spec.Subjects[0].Kind)
				require.Equal(t, "Role", event.Object.Status.Resolved.RoleKind)
				_, open := <-watch.Events()
				require.False(t, open)
				require.NoError(t, watch.Err())
			}
			select {
			case <-sub.stopped:
			case <-time.After(time.Second):
				t.Fatal("closed watches must release the subscription")
			}
		})
	}
}

func TestRBACWatchRejectsInvalidScopesAndStopsBlockedDelivery(t *testing.T) {
	for _, kind := range []string{"Role", "ClusterRole"} {
		t.Run(kind, func(t *testing.T) {
			namespace := ""
			if kind == "ClusterRole" {
				namespace = "shop"
			}
			sub := &watchFixture{data: make(chan json.RawMessage, 1), stopped: make(chan struct{})}
			body, err := json.Marshal(map[string]any{"watchResources": map[string]any{
				"type": "DELETED", "kind": kind, "namespace": namespace, "name": "removed", "resourceVersion": "cursor",
			}})
			require.NoError(t, err)
			sub.data <- body
			close(sub.data)
			watch := newResourceWatcher(t.Context(), kind, sub)
			_, open := <-watch.Events()
			require.False(t, open)
			require.Error(t, watch.Err())
		})
	}
	sub := &watchFixture{data: make(chan json.RawMessage, 32), stopped: make(chan struct{})}
	for range 32 {
		sub.data <- json.RawMessage(`{"watchResources":{"type":"BOOKMARK","kind":"Role","resourceVersion":"cursor"}}`)
	}
	watch := newResourceWatcher(t.Context(), "Role", sub)
	watch.Stop()
	for range watch.Events() {
	}
	require.NoError(t, watch.Err())
	require.Eventually(t, func() bool {
		select {
		case <-sub.stopped:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	expired := &watchFixture{data: make(chan json.RawMessage), stopped: make(chan struct{}),
		err: &graphqlclient.Error{Message: "expired", Extensions: map[string]any{"code": "WATCH_EXPIRED"}}}
	close(expired.data)
	watch = newResourceWatcher(t.Context(), "Role", expired)
	for range watch.Events() {
	}
	require.ErrorIs(t, watch.Err(), listwatch.ErrWatchExpired)
}

func TestRBACListPagesAppliesBackpressureAndRejectsStalledCursors(t *testing.T) {
	for _, mode := range []string{"success", "visitor-failure", "stalled-cursor"} {
		t.Run(mode, func(t *testing.T) {
			calls, visits := 0, 0
			visitorError := errors.New("checkpoint unavailable")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Variables map[string]any }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				calls++
				require.Equal(t, calls-1, visits, "checkpoint page before fetching the next one")
				require.Equal(t, "shop", request.Variables["namespace"])
				if calls == 1 {
					require.Nil(t, request.Variables["after"])
				} else {
					require.Equal(t, "next-page", request.Variables["after"])
				}
				more := calls == 1 || mode == "stalled-cursor"
				_, _ = w.Write([]byte(`{"data":{"result":{"edges":[{"node":{
					"id":"node","kind":"Role","metadata":{"uid":"uid","name":"read","namespace":"shop","resourceVersion":"version"}
				}}],"pageInfo":{"hasNextPage":` + map[bool]string{true: "true", false: "false"}[more] + `,"endCursor":"next-page"}}}}`))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
			err := client.ListPages(t.Context(), "Role", "shop", func(page []Resource) error {
				visits++
				require.Len(t, page, 1)
				if mode == "visitor-failure" {
					return visitorError
				}
				return nil
			})
			if mode == "success" {
				require.NoError(t, err)
				require.Equal(t, 2, calls)
			} else {
				require.Error(t, err)
				if mode == "visitor-failure" {
					require.ErrorIs(t, err, visitorError)
					require.Equal(t, 1, calls)
				} else {
					require.Equal(t, 2, calls)
				}
			}
		})
	}
}

func TestGraphQLOperationsMatchSchema(t *testing.T) {
	var operations []schemavalidate.Operation
	for _, kind := range Kinds {
		operations = append(operations,
			schemavalidate.Operation{Name: kind + " list", Query: listQuery(kind)},
			schemavalidate.Operation{Name: kind + " get", Query: getQuery(kind)},
			schemavalidate.Operation{Name: kind + " completion", Query: completionMutation(kind)})
	}
	operations = append(operations, schemavalidate.Operation{Name: "status", Query: statusMutation},
		schemavalidate.Operation{Name: "namespace cleanup", Query: namespaceCleanupMutation},
		schemavalidate.Operation{Name: "watch", Query: watchSubscription},
		schemavalidate.Operation{Name: "owner template", Query: ownerBindingMutation})
	schemavalidate.Validate(t, operations)
}

func TestNamespaceCleanupClientPreservesCursorAndRejectsMissingResults(t *testing.T) {
	for _, response := range []string{
		`{"data":{"result":{"nextCursor":"next","pending":true}}}`,
		`{"data":{"result":null}}`,
		`{"errors":[{"message":"denied","extensions":{"code":"FORBIDDEN"}}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Query     string
					Variables map[string]map[string]any
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Equal(t, namespaceCleanupMutation, request.Query)
				require.Equal(t, "previous", request.Variables["input"]["after"])
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
			next, pending, err := client.ReconcileNamespaceCleanup(t.Context(), "previous")
			if response == `{"data":{"result":{"nextCursor":"next","pending":true}}}` {
				require.NoError(t, err)
				require.Equal(t, "next", next)
				require.True(t, pending)
			} else {
				require.Error(t, err)
				require.Empty(t, next)
			}
		})
	}
}

func TestRBACStatusUsesDateTimeContractAndDoesNotReattachResolvedUIDs(t *testing.T) {
	for _, state := range []string{"unresolved", "resolved", "terminating"} {
		t.Run(state, func(t *testing.T) {
			observed := time.Date(2026, 11, 10, 12, 0, 0, 123456789, time.UTC)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Variables map[string]map[string]any }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				input := request.Variables["input"]
				require.Equal(t, "uid-bound-version", input["resourceVersion"])
				condition := input["conditions"].([]any)[0].(map[string]any)
				require.Equal(t, "2026-11-10T12:00:00.123Z", condition["lastTransitionTime"],
					"the DateTime scalar requires exactly three fractional digits")
				if state == "unresolved" {
					require.Equal(t, "role-uid", input["resolved"].(map[string]any)["roleUID"])
				} else {
					require.NotContains(t, input, "resolved", "conditions must not attempt UID re-resolution")
				}
				_, _ = w.Write([]byte(`{"data":{"updateResourceStatus":{"object":{}}}}`))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
			item := Resource{Kind: "RoleBinding", Metadata: Metadata{Name: "access", Namespace: "shop",
				ResourceVersion: "uid-bound-version", Generation: 1}}
			if state == "resolved" {
				item.Status.Resolved = &ResolvedRole{RoleUID: "original-role"}
			}
			if state == "terminating" {
				item.Metadata.DeletionTimestamp = &observed
			}
			require.NoError(t, client.UpdateStatus(t.Context(), item, StatusUpdate{
				Resolved: &ResolvedRole{RoleUID: "role-uid", RoleKind: "Role", RoleName: "reader"},
				Conditions: []*status.Condition{{Type: "RoleResolved", Status: "TRUE", ObservedGeneration: 1,
					LastTransitionTime: observed, Reason: "RoleReady", Message: "ready"}},
			}))
		})
	}
}

func TestGraphQLStatusAndCompletionPreserveUIDCASAndErrors(t *testing.T) {
	for _, code := range []string{"", "NOT_FOUND", "RESOURCE_VERSION_CONFLICT", "FORBIDDEN", "AUTHORIZATION_UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			var input map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Variables map[string]any `json:"variables"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				input = request.Variables["input"].(map[string]any)
				w.Header().Set("Content-Type", "application/json")
				if code != "" {
					require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"errors": []any{map[string]any{
						"message": "failure", "extensions": map[string]any{"code": code}}}}))
					return
				}
				_, _ = w.Write([]byte(`{"data":{"result":{"id":"binding-node"}}}`))
			}))
			defer server.Close()
			client := NewGraphQLClient(graphqlclient.New(server.URL, graphqlclient.NewStaticToken("enrolled")))
			item := Resource{ID: "binding-node", Kind: "RoleBinding", Metadata: Metadata{
				UID: "binding-uid", Name: "access", Namespace: "shop", ResourceVersion: "uid-bound-version",
			}}
			err := client.CompleteDeletion(t.Context(), item)
			switch code {
			case "", "NOT_FOUND":
				require.NoError(t, err)
			case "RESOURCE_VERSION_CONFLICT":
				require.True(t, errors.Is(err, types.ErrConflict))
			default:
				require.Error(t, err, "permission and infrastructure failures are not absence")
			}
			require.Equal(t, "binding-node", input["id"])
			require.Equal(t, "uid-bound-version", input["resourceVersion"])
			require.Equal(t, "shop", input["namespace"])
		})
	}
}
