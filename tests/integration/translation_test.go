// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestTranslationAuthoringExamplesMatchIngestedCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	covered := map[string]bool{}
	for _, path := range []string{"../../docs/storage-groups/translations.md", "../../docs/storage-groups/git-backed.md"} {
		t.Run(path, func(t *testing.T) {
			guide, err := os.ReadFile(path)
			require.NoError(t, err)
			examples := regexp.MustCompile("(?ms)^```markdown\\n(.*?)^```[ \\t]*$").FindAllSubmatch(guide, -1)
			found := false
			for _, example := range examples {
				if !bytes.Contains(example[1], []byte("\nkind: Translation\n")) {
					continue
				}
				found = true
				matched := false
				for name, source := range corpus.Valid {
					if bytes.Equal(example[1], []byte(source)) {
						covered[name], matched = true, true
						break
					}
				}
				require.True(t, matched, "every published Translation must preserve the exact source/body bytes exercised by native ingestion")
			}
			require.True(t, found, "the authoring guide must include an executable Translation example")
		})
	}
	require.True(t, covered["product-field"], "retain a valid field-channel walkthrough")
	require.True(t, covered["file-body"], "retain a body-only walkthrough for File, which has no title field")
}

func translationQuickstartSources(t *testing.T, namespace, runID string) []translationCapacityDocument {
	t.Helper()
	guide, err := os.ReadFile("../../specs/071-translation-contract/quickstart.md")
	require.NoError(t, err)
	examples := regexp.MustCompile("(?ms)^```markdown\\n(.*?)^```[ \\t]*$").FindAllSubmatch(guide, -1)
	var documents []translationCapacityDocument
	for _, example := range examples {
		if !bytes.Contains(example[1], []byte("\nkind: Translation\n")) {
			continue
		}
		source, err := translationIntegrationSource(example[1], "acme-store", namespace)
		require.NoError(t, err)
		expected, err := translationIntegrationExpected(source)
		require.NoError(t, err)
		document, err := renderTranslationCapacityPush(translationCapacityDocument{
			Namespace: namespace, Name: expected.Metadata.Name, Source: source,
		}, runID, len(documents))
		require.NoError(t, err)
		documents = append(documents, document)
	}
	require.NotEmpty(t, documents, "the quickstart must publish executable Translation sources")
	return documents
}

func TestTranslationIntegrationEmptyNamespaceFollowsBoundedRelayProgress(t *testing.T) {
	for _, mode := range []string{"empty-progress", "authored-source", "missing-cursor", "repeated-cursor", "unending-progress"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				var request struct {
					Variables struct{ After *string }
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil ||
					(call == 1 && request.Variables.After != nil) ||
					(call > 1 && request.Variables.After == nil) {
					http.Error(w, "invalid pagination request", http.StatusBadRequest)
					return
				}
				var cursor any = "next"
				edges := []any{}
				more := true
				switch mode {
				case "empty-progress":
					if call == 2 {
						more, cursor = false, nil
					}
				case "authored-source":
					edges = []any{map[string]any{"node": map[string]any{"metadata": map[string]string{"uid": "existing"}}}}
				case "missing-cursor":
					cursor = nil
				case "unending-progress":
					cursor = strings.Repeat("x", int(call))
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
					"translations": map[string]any{"edges": edges, "pageInfo": map[string]any{
						"hasNextPage": more, "endCursor": cursor,
					}},
				}})
			}))
			defer server.Close()
			err := verifyTranslationIntegrationEmptyNamespace(t, rbacNativeFixture{namespace: "translation-owned", token: "fixture"}, server.URL)
			if mode == "empty-progress" {
				require.NoError(t, err)
				require.EqualValues(t, 2, calls.Load())
			} else {
				require.Error(t, err)
				require.LessOrEqual(t, calls.Load(), int64(64))
			}
		})
	}
}

func TestTranslationIntegrationTargetSnapshotJoinsPublicIdentityAndCurrentSource(t *testing.T) {
	const uid = "11111111-1111-4111-8111-111111111111"
	const revision = "main@sha1:accepted-target"
	source := []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: CategoryTaxonomy\nmetadata:\n  name: computers\n  namespace: translation-owned\nspec:\n  title: Computers\n---\n\nOriginal category body.\n")
	for _, current := range []bool{false, true} {
		t.Run(map[bool]string{false: "stale-source", true: "current-source"}[current], func(t *testing.T) {
			observed := 0
			if current {
				observed = 1
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct{ Query string }
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					http.Error(w, "bad fixture request", http.StatusBadRequest)
					return
				}
				var data any
				if strings.Contains(request.Query, "translationTargetObservation") {
					data = map[string]any{"translationTargetObservation": map[string]any{
						"namespaceUID": "namespace", "evidence": map[string]any{
							"uid": uid, "generation": 1, "revision": revision, "contentDigest": "content-digest",
						},
					}}
				} else {
					data = map[string]any{"target": map[string]any{
						"metadata": map[string]any{
							"uid":        base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Category/" + uid)),
							"generation": 1, "revision": revision,
						},
						"body": "\nOriginal category body.\n",
						"status": map[string]any{
							"observedGeneration": observed, "lastAppliedRevision": revision,
							"conditions": []any{map[string]any{"type": "Ready", "status": "TRUE", "observedGeneration": 0}},
							"resolved":   map[string]any{"depth": 0},
						},
					}}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			}))
			defer server.Close()
			fixture := rbacNativeFixture{namespace: "translation-owned", token: "fixture"}
			sample, found := readTranslationIntegrationTarget(t, fixture, fixture, server.URL, source)
			require.Equal(t, current, found)
			if current {
				require.Equal(t, uid, sample.UID)
				require.Equal(t, revision, sample.Revision)
			}
		})
	}
}

func TestTranslationIntegrationFixturesPreserveAuthoredBodyBytes(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace string
		Valid     map[string]string
		Targets   map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for _, sources := range []map[string]string{corpus.Valid, corpus.Targets} {
		for name, source := range sources {
			t.Run(name, func(t *testing.T) {
				rendered, err := translationIntegrationSource([]byte(source), corpus.Namespace, "translation-owned-fixture")
				require.NoError(t, err)
				originalHeader, originalBody, err := translationIntegrationDocument([]byte(source))
				require.NoError(t, err)
				header, body, err := translationIntegrationDocument(rendered)
				require.NoError(t, err)
				require.Equal(t, originalBody, body, "namespace rendering must preserve exact body bytes")
				require.Equal(t, bytes.Count(originalHeader, []byte("\r\n")), bytes.Count(header, []byte("\r\n")))
				require.NotContains(t, string(header), "namespace: "+corpus.Namespace)
				require.Contains(t, string(header), "namespace: translation-owned-fixture")
			})
		}
	}
	for _, source := range [][]byte{
		nil, []byte("not frontmatter"), []byte("---\nspec: {}\n"),
		append([]byte("---\n---\n"), 0xff), bytes.Repeat([]byte("x"), (1<<20)+1),
	} {
		_, _, err := translationIntegrationDocument(source)
		require.Error(t, err)
	}
}

func TestTranslationIntegrationNamespaceRenderingDoesNotRewriteContent(t *testing.T) {
	source := []byte("---\r\napiVersion: i18n.gitstore.dev/v1beta1\r\nkind: Translation\r\nmetadata:\r\n  name: fixture\r\n  namespace: original\r\n  labels:\r\n    namespace: original\r\nspec:\r\n  locale: de-DE\r\n  targetRef:\r\n    namespace: original\r\n  patches:\r\n  - fieldPath: spec.title\r\n    translation: \"namespace: original\"\r\n---\r\n\r\nnamespace: original\r\n")
	rendered, err := translationIntegrationSource(source, "original", "translation-owned")
	require.NoError(t, err)
	require.Equal(t, []byte(strings.NewReplacer(
		"  namespace: original\r\n  labels:", "  namespace: translation-owned\r\n  labels:",
		"  targetRef:\r\n    namespace: original", "  targetRef:\r\n    namespace: translation-owned",
	).Replace(string(source))), rendered)
	_, err = translationIntegrationSource(source, "other", "translation-owned")
	require.Error(t, err)
	_, err = translationIntegrationSource(source, "original", "injected\nspec: {}")
	require.Error(t, err)
}

func TestTranslationIntegrationOracleRejectsPartialOrStalePublicReads(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	source := []byte(corpus.Valid["product-combined"])
	expected, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	revision := "main@sha1:" + strings.Repeat("a", 40)
	fixture := func() translationIntegrationResource {
		var resource translationIntegrationResource
		resource.APIVersion, resource.Kind = expected.APIVersion, expected.Kind
		resource.Metadata.UID, resource.Metadata.ResourceVersion = "fixture-uid", "source-version"
		resource.Metadata.Name, resource.Metadata.Namespace = expected.Metadata.Name, expected.Metadata.Namespace
		resource.Metadata.Generation, resource.Metadata.Revision = 2, revision
		resource.Spec = expected.Spec
		resource.Spec.Patches = slices.Clone(expected.Spec.Patches)
		body := string(expected.body)
		resource.Body = &body
		resource.Status.ObservedGeneration, resource.Status.LastAppliedRevision = 2, revision
		resource.Status.Conditions = []translationIntegrationCondition{{Type: "Ready", Status: "TRUE", ObservedGeneration: 2}}
		return resource
	}

	require.NoError(t, verifyTranslationIntegrationResource(source, revision, fixture(), true))
	t.Run("case-folded-locale-with-authored-source", func(t *testing.T) {
		resource := fixture()
		resource.Status.Resolved = &struct {
			CanonicalLocale string `json:"canonicalLocale"`
		}{CanonicalLocale: strings.ToLower(resource.Spec.Locale)}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"translation": resource}})
		}))
		defer server.Close()
		observed := awaitTranslationIntegrationResources(t,
			rbacNativeFixture{apis: []string{server.URL, server.URL}, token: "fixture"},
			map[string][]byte{resource.Metadata.Name: source}, revision, true)
		require.Equal(t, expected.Spec.Locale, observed[resource.Metadata.Name].Spec.Locale)
	})
	unresolved := fixture()
	unresolved.Status.Conditions[0].Status = "FALSE"
	require.NoError(t, verifyTranslationIntegrationResource(source, revision, unresolved, false))
	require.Error(t, verifyTranslationIntegrationResource(source, revision, unresolved, true))
	t.Run("visibility-does-not-imply-ready", func(t *testing.T) {
		resource := fixture()
		resource.Status.ObservedGeneration--
		resource.Status.LastAppliedRevision = "old"
		resource.Status.Conditions = nil
		require.NoError(t, verifyTranslationIntegrationAuthored(source, revision, resource))
		require.Error(t, verifyTranslationIntegrationResource(source, revision, resource, true))
		body := "partial"
		resource.Body = &body
		require.Error(t, verifyTranslationIntegrationAuthored(source, revision, resource))
	})
	t.Run("historical-delivery-requires-independent-current-ready", func(t *testing.T) {
		delivered := fixture()
		delivered.Status.Conditions = nil
		delivered.Status.LastAppliedRevision = ""
		delivered.Status.ObservedGeneration = 0
		event := translationIntegrationWatchEvent{
			Type: "MODIFIED", Name: delivered.Metadata.Name, ResourceVersion: "journal-cursor",
			Historical: true, Translation: &delivered,
		}
		require.NoError(t, verifyTranslationIntegrationWatchJoin(source, revision, event, fixture()))
		stale := fixture()
		stale.Metadata.ResourceVersion = "another-version"
		require.Error(t, verifyTranslationIntegrationWatchJoin(source, revision, event, stale))
		stale = fixture()
		stale.Status.Conditions = nil
		require.Error(t, verifyTranslationIntegrationWatchJoin(source, revision, event, stale),
			"historical delivery cannot supply current Ready")
		partial := "partial"
		delivered.Body = &partial
		require.Error(t, verifyTranslationIntegrationWatchJoin(source, revision, event, fixture()))
	})
	for _, tc := range []struct {
		name   string
		change func(*translationIntegrationResource)
	}{
		{"wrong-kind", func(r *translationIntegrationResource) { r.Kind = "Product" }},
		{"wrong-name", func(r *translationIntegrationResource) { r.Metadata.Name = "other" }},
		{"wrong-namespace", func(r *translationIntegrationResource) { r.Metadata.Namespace = "other" }},
		{"missing-uid", func(r *translationIntegrationResource) { r.Metadata.UID = "" }},
		{"missing-version", func(r *translationIntegrationResource) { r.Metadata.ResourceVersion = "" }},
		{"old-revision", func(r *translationIntegrationResource) { r.Metadata.Revision = "old" }},
		{"normalized-raw-locale", func(r *translationIntegrationResource) { r.Spec.Locale = "de-de" }},
		{"missing-empty-patch", func(r *translationIntegrationResource) { r.Spec.Patches = r.Spec.Patches[:1] }},
		{"wrong-patch-text", func(r *translationIntegrationResource) { r.Spec.Patches[0].Translation = "incorrect" }},
		{"partial-body", func(r *translationIntegrationResource) { body := "partial"; r.Body = &body }},
		{"normalized-newlines", func(r *translationIntegrationResource) {
			body := strings.ReplaceAll(*r.Body, "\r\n", "\n")
			r.Body = &body
		}},
		{"stale-observation", func(r *translationIntegrationResource) { r.Status.ObservedGeneration-- }},
		{"stale-reconciled-revision", func(r *translationIntegrationResource) { r.Status.LastAppliedRevision = "old" }},
		{"stale-condition", func(r *translationIntegrationResource) { r.Status.Conditions[0].ObservedGeneration-- }},
		{"internal-enum", func(r *translationIntegrationResource) { r.Status.Conditions[0].Status = "True" }},
		{"duplicate-ready", func(r *translationIntegrationResource) {
			r.Status.Conditions = append(r.Status.Conditions, r.Status.Conditions[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := fixture()
			tc.change(&resource)
			require.Error(t, verifyTranslationIntegrationResource(source, revision, resource, true))
		})
	}
}

func TestTranslationIntegrationWatchDecoderPreservesProofAndRejectsBadAttribution(t *testing.T) {
	for _, tc := range []struct {
		name          string
		frame         map[string]any
		wantErr       bool
		wantRetryable bool
		wantBody      string
	}{
		{
			name: "historical-body-is-not-promoted",
			frame: map[string]any{"id": "translation-integration", "type": "next", "payload": map[string]any{
				"data": map[string]any{"watchTranslations": map[string]any{
					"type": "MODIFIED", "name": "example", "resourceVersion": "opaque-cursor", "historical": true,
					"translation": map[string]any{"body": "\r\nExact UTF-8: \u00e9.\r\n---\r\n"},
				}},
			}},
			wantBody: "\r\nExact UTF-8: \u00e9.\r\n---\r\n",
		},
		{name: "wrong-subscription", frame: map[string]any{"id": "other", "type": "next"}, wantErr: true},
		{name: "missing-cursor", frame: map[string]any{"id": "translation-integration", "type": "next", "payload": map[string]any{
			"data": map[string]any{"watchTranslations": map[string]any{"type": "MODIFIED"}},
		}}, wantErr: true},
		{name: "graphql-error", frame: map[string]any{"id": "translation-integration", "type": "next", "payload": map[string]any{
			"errors": []any{map[string]any{"message": "fixture-secret-token"}},
		}}, wantErr: true},
		{name: "retryable-error", frame: map[string]any{"id": "translation-integration", "type": "error",
			"payload": []any{map[string]any{"message": "fixture-secret-token",
				"extensions": map[string]any{"code": "TRANSLATION_UNAVAILABLE", "retryable": true}}},
		}, wantErr: true, wantRetryable: true},
		{name: "forbidden-error", frame: map[string]any{"id": "translation-integration", "type": "error",
			"payload": []any{map[string]any{"extensions": map[string]any{"code": "FORBIDDEN"}}},
		}, wantErr: true},
		{name: "journal-recovery", frame: map[string]any{"id": "translation-integration", "type": "error",
			"payload": []any{map[string]any{"extensions": map[string]any{"code": "WATCH_UNAVAILABLE"}}},
		}, wantErr: true, wantRetryable: true},
		{name: "expired-cursor", frame: map[string]any{"id": "translation-integration", "type": "error",
			"payload": []any{map[string]any{"extensions": map[string]any{"code": "WATCH_EXPIRED"}}},
		}, wantErr: true},
		{name: "terminated", frame: map[string]any{"type": "complete"}, wantErr: true, wantRetryable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					completed <- err
					return
				}
				defer conn.Close()
				if err = conn.WriteJSON(map[string]any{"type": "ping"}); err != nil {
					completed <- err
					return
				}
				var pong struct{ Type string }
				if err = conn.ReadJSON(&pong); err == nil && pong.Type != "pong" {
					err = errors.New("watch did not answer transport ping")
				}
				if err == nil {
					err = conn.WriteJSON(tc.frame)
				}
				completed <- err
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http", "ws", 1), nil)
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
			event, err := readTranslationIntegrationEvent(conn)
			if tc.wantErr {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "fixture-secret-token")
				require.Equal(t, tc.wantRetryable, errors.Is(err, errTranslationIntegrationWatchRetryable))
			} else {
				require.NoError(t, err)
				require.True(t, event.Historical)
				require.Equal(t, "opaque-cursor", event.ResourceVersion)
				require.NotNil(t, event.Translation)
				require.NotNil(t, event.Translation.Body)
				require.Equal(t, tc.wantBody, *event.Translation.Body)
			}
			require.NoError(t, <-completed)
		})
	}
}

func TestTranslationIntegrationReplicaWatchesConsumeConcurrently(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	source := []byte(corpus.Valid["product-field"])
	resource, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	const revision = "main@sha1:accepted"
	resource.Metadata.UID, resource.Metadata.ResourceVersion = "fixture-uid", "2"
	resource.Metadata.Generation, resource.Metadata.Revision = 1, revision
	resource.Status.ObservedGeneration, resource.Status.LastAppliedRevision = 1, revision
	resource.Status.Conditions = []translationIntegrationCondition{{Type: "Ready", Status: "TRUE", ObservedGeneration: 1}}
	var readers atomic.Int64
	both := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			if readers.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
				_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"translation": resource}})
			case <-time.After(time.Second):
				http.Error(w, "replica watch reader was starved", http.StatusRequestTimeout)
			}
			return
		}
		connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.WriteJSON(map[string]any{"id": "translation-integration", "type": "next",
			"payload": map[string]any{"data": map[string]any{"watchTranslations": map[string]any{
				"type": "MODIFIED", "name": resource.Metadata.Name, "resourceVersion": "journal-2",
				"translation": resource,
			}}}})
	}))
	defer server.Close()
	var watches []*translationIntegrationWatch
	for range 2 {
		connection, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http", "ws", 1), nil)
		require.NoError(t, err)
		defer connection.Close()
		watches = append(watches, &translationIntegrationWatch{connection: connection, cursor: "journal-1"})
	}
	awaitTranslationIntegrationWatches(t, rbacNativeFixture{apis: []string{server.URL, server.URL}, token: "fixture"},
		watches, map[string][]byte{resource.Metadata.Name: source}, revision)
	require.EqualValues(t, 2, readers.Load())
}

func TestTranslationIntegrationWatchResumesLastDurableCursor(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	source := []byte(corpus.Valid["product-field"])
	resource, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	const revision = "main@sha1:accepted"
	resource.Metadata.UID, resource.Metadata.ResourceVersion = "fixture-uid", "2"
	resource.Metadata.Generation, resource.Metadata.Revision = 1, revision
	body := string(resource.body)
	resource.Body = &body
	resource.Status.ObservedGeneration, resource.Status.LastAppliedRevision = 1, revision
	resource.Status.Conditions = []translationIntegrationCondition{{Type: "Ready", Status: "TRUE", ObservedGeneration: 1}}
	var connections atomic.Int64
	failures := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !websocket.IsWebSocketUpgrade(r) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"translation": resource}})
			return
		}
		conn, err := (&websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}).Upgrade(w, r, nil)
		if err != nil {
			failures <- err
			return
		}
		defer conn.Close()
		if err := conn.ReadJSON(&map[string]any{}); err != nil {
			failures <- err
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "connection_ack"})
		var request struct {
			Payload struct {
				Variables struct{ Cursor string }
			}
		}
		if err := conn.ReadJSON(&request); err != nil {
			failures <- err
			return
		}
		first := connections.Add(1) == 1
		expected := "checkpoint"
		delivered := resource
		delivered.Status.Conditions = nil
		delivered.Status.ObservedGeneration = 0
		delivered.Status.LastAppliedRevision = ""
		event := map[string]any{"type": "MODIFIED", "name": resource.Metadata.Name,
			"resourceVersion": "final", "historical": true, "translation": delivered}
		if first {
			expected = "bootstrap"
			event = map[string]any{"type": "BOOKMARK", "resourceVersion": "checkpoint"}
		}
		if request.Payload.Variables.Cursor != expected {
			failures <- errors.New("watch recovery did not retain the last durable cursor")
			return
		}
		_ = conn.WriteJSON(map[string]any{"id": "translation-integration", "type": "next",
			"payload": map[string]any{"data": map[string]any{"watchTranslations": event}}})
		if first {
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "terminated"),
				time.Now().Add(time.Second))
		}
		failures <- nil
	}))
	defer server.Close()
	connection, err := dialCapacitySubscription(server.URL, "fixture", "translation-integration",
		translationIntegrationQuery(t, "watch"), map[string]any{"namespace": resource.Metadata.Namespace, "cursor": "bootstrap"}, time.Second)
	require.NoError(t, err)
	watch := &translationIntegrationWatch{connection: connection, cursor: "bootstrap"}
	defer func() { _ = watch.connection.Close() }()
	awaitTranslationIntegrationWatches(t, rbacNativeFixture{apis: []string{server.URL}, token: "fixture"},
		[]*translationIntegrationWatch{watch}, map[string][]byte{resource.Metadata.Name: source}, revision)
	require.Equal(t, "final", watch.cursor)
	require.EqualValues(t, 2, connections.Load())
	require.NoError(t, <-failures)
	require.NoError(t, <-failures)
}
