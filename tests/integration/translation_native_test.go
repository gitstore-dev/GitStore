// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

//go:embed testdata/translation-queries.json
var translationIntegrationQueries []byte

func translationIntegrationQuery(t *testing.T, name string) string {
	t.Helper()
	query, err := translationIntegrationQueryDocument(name)
	require.NoError(t, err)
	return query
}

func translationIntegrationQueryDocument(name string) (string, error) {
	var queries map[string]string
	if json.Unmarshal(translationIntegrationQueries, &queries) != nil || queries[name] == "" {
		return "", fmt.Errorf("translation fixture: missing integration query")
	}
	return queries[name], nil
}

func verifyTranslationIntegrationEmptyNamespace(t *testing.T, fixture rbacNativeFixture, api string) error {
	t.Helper()
	var after *string
	seen := make(map[string]bool)
	for range 64 {
		response := fixture.query(t, api, translationIntegrationQuery(t, "emptyNamespace"),
			map[string]any{"namespace": fixture.namespace, "after": after})
		if len(response.Errors) != 0 {
			return errors.New("translation fixture: empty namespace requires a capable authorized API")
		}
		var data struct {
			Translations *struct {
				Edges    []json.RawMessage
				PageInfo *struct {
					HasNextPage *bool
					EndCursor   *string
				}
			}
		}
		if json.Unmarshal(response.Data, &data) != nil || data.Translations == nil || data.Translations.Edges == nil {
			return errors.New("translation fixture: invalid namespace connection")
		}
		if len(data.Translations.Edges) != 0 {
			return errors.New("translation fixture: use a fresh namespace without authored Translations")
		}
		page := data.Translations.PageInfo
		if page == nil || page.HasNextPage == nil {
			return errors.New("translation fixture: namespace connection omitted page progress")
		}
		if !*page.HasNextPage {
			return nil
		}
		if page.EndCursor == nil || *page.EndCursor == "" || seen[*page.EndCursor] {
			return errors.New("translation fixture: namespace pagination did not advance")
		}
		seen[*page.EndCursor] = true
		after = page.EndCursor
	}
	return errors.New("translation fixture: namespace scan exceeded its page budget")
}

type translationIntegrationWatchEvent struct {
	Type            string
	Name            string
	ResourceVersion string
	Historical      bool
	Translation     *translationIntegrationResource
}

type translationIntegrationWatch struct {
	connection *websocket.Conn
	cursor     string
}

func verifyTranslationIntegrationWatchJoin(source []byte, revision string, event translationIntegrationWatchEvent, current translationIntegrationResource) error {
	if event.Translation == nil || event.ResourceVersion == "" || event.Name != current.Metadata.Name ||
		(event.Type != "ADDED" && event.Type != "MODIFIED") {
		return errors.New("translation fixture: incomplete watch source delivery")
	}
	if err := verifyTranslationIntegrationAuthored(source, revision, *event.Translation); err != nil {
		return err
	}
	if !event.Historical {
		if err := verifyTranslationIntegrationResource(source, revision, *event.Translation, true); err != nil {
			return err
		}
	}
	if event.Translation.Metadata.UID != current.Metadata.UID ||
		event.Translation.Metadata.ResourceVersion != current.Metadata.ResourceVersion {
		return errors.New("translation fixture: watch source does not join the current read")
	}
	// Historical replay proves delivery only; Ready always comes from a fresh read.
	return verifyTranslationIntegrationResource(source, revision, current, true)
}

var errTranslationIntegrationWatchRetryable = errors.New("translation fixture: watch temporarily unavailable")

func translationIntegrationWatchRecoverable(err error) bool {
	return errors.Is(err, errTranslationIntegrationWatchRetryable) ||
		websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure,
			websocket.CloseServiceRestart, websocket.CloseTryAgainLater)
}

func translationIntegrationWatchFailure(raw []json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("translation fixture: watch rejected source proof")
	}
	for _, item := range raw {
		var failure struct {
			Extensions struct {
				Code      string
				Retryable bool
			}
		}
		if json.Unmarshal(item, &failure) != nil {
			return errors.New("translation fixture: invalid watch error")
		}
		if failure.Extensions.Retryable || failure.Extensions.Code == "WATCH_UNAVAILABLE" {
			continue
		}
		switch failure.Extensions.Code {
		case "WATCH_EXPIRED", "FORBIDDEN", "UNAUTHENTICATED", "TRANSLATION_NOT_ACTIVATED":
			return fmt.Errorf("translation fixture: watch rejected source proof (%s)", failure.Extensions.Code)
		default:
			return errors.New("translation fixture: watch rejected source proof")
		}
	}
	return errTranslationIntegrationWatchRetryable
}

func readTranslationIntegrationEvent(conn *websocket.Conn) (translationIntegrationWatchEvent, error) {
	conn.SetReadLimit(8 << 20)
	for {
		var message struct {
			ID      string
			Type    string
			Payload json.RawMessage
		}
		if err := conn.ReadJSON(&message); err != nil {
			return translationIntegrationWatchEvent{}, err
		}
		switch message.Type {
		case "ping":
			if err := conn.WriteJSON(map[string]any{"type": "pong"}); err != nil {
				return translationIntegrationWatchEvent{}, err
			}
		case "error":
			var failures []json.RawMessage
			if json.Unmarshal(message.Payload, &failures) != nil {
				return translationIntegrationWatchEvent{}, errors.New("translation fixture: invalid watch error")
			}
			return translationIntegrationWatchEvent{}, translationIntegrationWatchFailure(failures)
		case "complete":
			return translationIntegrationWatchEvent{}, errTranslationIntegrationWatchRetryable
		case "next":
			if message.ID != "translation-integration" {
				return translationIntegrationWatchEvent{}, errors.New("translation fixture: unrelated watch operation")
			}
			var payload struct {
				Data struct {
					WatchTranslations translationIntegrationWatchEvent
				}
				Errors []json.RawMessage
			}
			if json.Unmarshal(message.Payload, &payload) != nil {
				return translationIntegrationWatchEvent{}, errors.New("translation fixture: invalid watch payload")
			}
			if len(payload.Errors) != 0 {
				return translationIntegrationWatchEvent{}, translationIntegrationWatchFailure(payload.Errors)
			}
			event := payload.Data.WatchTranslations
			if event.ResourceVersion == "" {
				return translationIntegrationWatchEvent{}, errors.New("translation fixture: watch omitted durable cursor")
			}
			switch event.Type {
			case "ADDED", "MODIFIED", "DELETED", "BOOKMARK":
				return event, nil
			default:
				return translationIntegrationWatchEvent{}, errors.New("translation fixture: invalid watch event type")
			}
		default:
			return translationIntegrationWatchEvent{}, errors.New("translation fixture: unexpected watch message")
		}
	}
}

func awaitTranslationIntegrationWatches(t *testing.T, fixture rbacNativeFixture, watches []*translationIntegrationWatch, sources map[string][]byte, revision string) {
	t.Helper()
	require.NotEmpty(t, watches)
	require.Len(t, watches, len(fixture.apis))
	if len(watches) > 1 {
		if !t.Run("replica-watches", func(t *testing.T) {
			for i, watch := range watches {
				t.Run(fmt.Sprintf("api-%d", i), func(t *testing.T) {
					t.Parallel()
					replica := fixture
					replica.apis = []string{fixture.apis[i]}
					awaitTranslationIntegrationWatches(t, replica, []*translationIntegrationWatch{watch}, sources, revision)
				})
			}
		}) {
			t.FailNow()
		}
		return
	}
	for i, watch := range watches {
		deadline := time.Now().Add(30 * time.Second)
		require.NoError(t, watch.connection.SetReadDeadline(deadline))
		seen := make(map[string]bool)
		reconnects := 0
		historical := 0
		defer func() {
			if t.Failed() {
				t.Logf("watch matched %d/%d current sources; reconnects=%d, historical=%d", len(seen), len(sources), reconnects, historical)
			}
		}()
		for count := 0; count < 512 && len(seen) < len(sources); count++ {
			event, err := readTranslationIntegrationEvent(watch.connection)
			if translationIntegrationWatchRecoverable(err) {
				reconnects++
				require.LessOrEqual(t, reconnects, 8, "watch recovery must remain bounded")
				require.NotEmpty(t, watch.cursor, "never replace a lost cursor with a fresh head")
				require.Greater(t, time.Until(deadline), 250*time.Millisecond)
				_ = watch.connection.Close()
				time.Sleep(250 * time.Millisecond)
				watch.connection, err = dialCapacitySubscription(fixture.apis[i], fixture.token, "translation-integration",
					translationIntegrationQuery(t, "watch"), map[string]any{"namespace": fixture.namespace, "cursor": watch.cursor},
					min(5*time.Second, time.Until(deadline)))
				require.NoError(t, err)
				require.NoError(t, watch.connection.SetReadDeadline(deadline))
				continue
			}
			require.NoError(t, err)
			watch.cursor = event.ResourceVersion
			if event.Historical {
				historical++
			}
			source, expected := sources[event.Name]
			if !expected || event.Type == "BOOKMARK" {
				continue
			}
			require.NotEqual(t, "DELETED", event.Type)
			require.NotNil(t, event.Translation)
			if event.Historical && verifyTranslationIntegrationAuthored(source, revision, *event.Translation) != nil ||
				!event.Historical && verifyTranslationIntegrationResource(source, revision, *event.Translation, true) != nil {
				continue
			}
			current, found := readTranslationIntegrationResource(t, fixture, fixture.apis[i], event.Name)
			if !found || verifyTranslationIntegrationWatchJoin(source, revision, event, current) != nil {
				continue
			}
			seen[event.Name] = true
		}
		require.Len(t, seen, len(sources), "each API watch must join every source to a current public read")
	}
}

func ownedTranslationNativeFixture(t *testing.T) (rbacNativeFixture, string) {
	t.Helper()
	if os.Getenv("TRANSLATION_TEST_RUN") != "1" {
		t.Skip("owned native Translation deployment not selected")
	}
	require.Equal(t, "1", os.Getenv("TRANSLATION_TEST_OWNED_DEPLOYMENT"))
	fixture := rbacNativeFixture{
		apis: []string{
			strings.TrimRight(os.Getenv("TRANSLATION_TEST_API_A"), "/"),
			strings.TrimRight(os.Getenv("TRANSLATION_TEST_API_B"), "/"),
		},
		gitURL:    strings.TrimRight(os.Getenv("TRANSLATION_TEST_GIT_URL_A"), "/"),
		namespace: os.Getenv("TRANSLATION_TEST_NAMESPACE"),
	}
	gitB := strings.TrimRight(os.Getenv("TRANSLATION_TEST_GIT_URL_B"), "/")
	require.True(t, translationIntegrationNamespace.MatchString(fixture.namespace))
	require.True(t, strings.HasPrefix(fixture.namespace, "translation-"), "use an explicitly owned Translation namespace")
	require.NotEqual(t, fixture.apis[0], fixture.apis[1])
	require.NotEqual(t, fixture.gitURL, gitB, "exercise both explicitly configured Smart HTTP frontends")
	for _, endpoint := range append(append([]string{}, fixture.apis...), fixture.gitURL, gitB) {
		parsed, err := url.Parse(endpoint)
		require.NoError(t, err)
		require.Contains(t, []string{"http", "https"}, parsed.Scheme)
		require.NotEmpty(t, parsed.Host)
		require.Nil(t, parsed.User)
		require.Empty(t, parsed.RawQuery)
		require.False(t, parsed.ForceQuery)
		require.Empty(t, parsed.Fragment)
		require.Empty(t, strings.TrimRight(parsed.Path, "/"), "native fixture endpoints must be base URLs")
	}
	fixture.token = rbacNativeToken(t, os.Getenv("TRANSLATION_TEST_TOKEN_FILE"))
	return fixture, gitB
}

func readTranslationIntegrationResource(t *testing.T, fixture rbacNativeFixture, api, name string) (translationIntegrationResource, bool) {
	t.Helper()
	response := fixture.queryBounded(t, api, translationIntegrationQuery(t, "translation"),
		map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": name}}}, 8<<20)
	if translationIntegrationReadPending(response) {
		return translationIntegrationResource{}, false
	}
	if len(response.Errors) != 0 {
		t.Fatalf("native Translation read for %s returned GraphQL errors: %s", name, response.Errors)
	}
	var data struct {
		Translation *translationIntegrationResource
	}
	require.NoError(t, json.Unmarshal(response.Data, &data))
	if data.Translation == nil {
		return translationIntegrationResource{}, false
	}
	return *data.Translation, true
}

func translationIntegrationReadPending(response gqlResponse) bool {
	if len(response.Errors) == 0 {
		return false
	}
	for _, raw := range response.Errors {
		var failure struct {
			Extensions struct {
				Code      string
				Retryable bool
			}
		}
		if json.Unmarshal(raw, &failure) != nil || (failure.Extensions.Code != "NOT_FOUND" && !failure.Extensions.Retryable) {
			return false
		}
	}
	return true
}

func awaitTranslationIntegrationResources(t *testing.T, fixture rbacNativeFixture, sources map[string][]byte, revision string, ready bool) map[string]translationIntegrationResource {
	t.Helper()
	result := make(map[string]translationIntegrationResource)
	var pending string
	defer func() {
		if t.Failed() && pending != "" {
			t.Log(pending)
		}
	}()
	rbacNativeEventually(t, func() bool {
		clear(result)
		for _, api := range fixture.apis {
			for name, source := range sources {
				resource, found := readTranslationIntegrationResource(t, fixture, api, name)
				if !found {
					pending = fmt.Sprintf("%s: source read pending", name)
					return false
				}
				if err := verifyTranslationIntegrationResource(source, revision, resource, ready); err != nil {
					pending = fmt.Sprintf("%s: %v (expected revision %s, actual %s)", name, err, revision, resource.Metadata.Revision)
					return false
				}
				expectedLocale := strings.ToLower(resource.Spec.Locale)
				if ready && (resource.Status.Resolved == nil || resource.Status.Resolved.CanonicalLocale != expectedLocale) {
					pending = fmt.Sprintf("%s: canonical locale is not current", name)
					return false
				}
				if previous, found := result[name]; found && (resource.Metadata.UID != previous.Metadata.UID ||
					resource.Metadata.ResourceVersion != previous.Metadata.ResourceVersion) {
					pending = fmt.Sprintf("%s: replica identity/version has not converged", name)
					return false
				}
				result[name] = resource
			}
		}
		return true
	}, "both API endpoints must expose the same exact, current Translation sources")
	return result
}

type translationIntegrationTargetSample struct {
	UID, Revision, ContentDigest string
	Generation                   int64
	Body                         *string
	Resolved                     json.RawMessage
}

func readTranslationIntegrationTarget(t *testing.T, fixture, controller rbacNativeFixture, api string, source []byte) (translationIntegrationTargetSample, bool) {
	t.Helper()
	header, expectedBody, err := translationIntegrationDocument(source)
	require.NoError(t, err)
	var authored translationIntegrationResource
	require.NoError(t, yaml.Unmarshal(header, &authored))
	response := fixture.query(t, api, translationIntegrationQuery(t, "target."+authored.Kind), map[string]any{
		"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": authored.Metadata.Name}},
	})
	if translationIntegrationReadPending(response) {
		return translationIntegrationTargetSample{}, false
	}
	require.Empty(t, response.Errors)
	var public struct {
		Target *struct {
			Metadata struct {
				UID        string
				Generation int64
				Revision   string
			}
			Body   *string
			Status *struct {
				ObservedGeneration  int64
				LastAppliedRevision string
				Conditions          []translationIntegrationCondition
				Resolved            json.RawMessage
			}
		}
	}
	require.NoError(t, json.Unmarshal(response.Data, &public))
	if public.Target == nil {
		return translationIntegrationTargetSample{}, false
	}
	if authored.Kind == "Product" || authored.Kind == "CategoryTaxonomy" {
		status := public.Target.Status
		if status == nil || status.ObservedGeneration != public.Target.Metadata.Generation ||
			status.LastAppliedRevision != public.Target.Metadata.Revision {
			return translationIntegrationTargetSample{}, false
		}
		ready := false
		for _, condition := range status.Conditions {
			if condition.Type == "Ready" && condition.Status == "TRUE" {
				ready = true
			}
		}
		if !ready {
			return translationIntegrationTargetSample{}, false
		}
	}
	var actualBody []byte
	if public.Target.Body != nil {
		actualBody = []byte(*public.Target.Body)
	}
	require.Equal(t, expectedBody, actualBody, "target body must remain authored, not translated")
	response = controller.query(t, api, translationIntegrationQuery(t, "targetObservation"),
		map[string]any{"namespace": fixture.namespace, "apiVersion": authored.APIVersion, "kind": authored.Kind, "name": authored.Metadata.Name})
	if translationIntegrationReadPending(response) {
		return translationIntegrationTargetSample{}, false
	}
	require.Empty(t, response.Errors, "owned target fences and controller authorization must be ready")
	var private struct {
		TranslationTargetObservation *struct {
			NamespaceUID string
			Evidence     struct {
				UID, Revision, ContentDigest string
				Generation                   int64
			}
		}
	}
	require.NoError(t, json.Unmarshal(response.Data, &private))
	require.NotNil(t, private.TranslationTargetObservation)
	observation := private.TranslationTargetObservation
	require.NotEmpty(t, observation.NamespaceUID)
	require.NotEmpty(t, observation.Evidence.ContentDigest)
	publicIdentity, err := base64.StdEncoding.DecodeString(public.Target.Metadata.UID)
	require.NoError(t, err)
	nodeKind := authored.Kind
	if nodeKind == "CategoryTaxonomy" {
		nodeKind = "Category"
	}
	// Separate public and controller reads can straddle a target update.
	if string(publicIdentity) != "gid://GitStore/"+nodeKind+"/"+observation.Evidence.UID ||
		public.Target.Metadata.Generation != observation.Evidence.Generation ||
		public.Target.Metadata.Revision != observation.Evidence.Revision {
		return translationIntegrationTargetSample{}, false
	}
	sample := translationIntegrationTargetSample{
		UID: observation.Evidence.UID, Revision: observation.Evidence.Revision,
		Generation: observation.Evidence.Generation, ContentDigest: observation.Evidence.ContentDigest,
		Body: public.Target.Body,
	}
	if public.Target.Status != nil {
		sample.Resolved = public.Target.Status.Resolved
	}
	return sample, true
}

func requireTranslationIntegrationTargetAbsent(t *testing.T, fixture rbacNativeFixture, source []byte) {
	t.Helper()
	header, _, err := translationIntegrationDocument(source)
	require.NoError(t, err)
	var authored translationIntegrationResource
	require.NoError(t, yaml.Unmarshal(header, &authored))
	for _, api := range fixture.apis {
		response := fixture.query(t, api, translationIntegrationQuery(t, "target."+authored.Kind),
			map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": authored.Metadata.Name}}})
		if len(response.Errors) != 0 {
			rbacNativeErrorCode(t, response, "NOT_FOUND")
		}
		var data struct{ Target json.RawMessage }
		require.NoError(t, json.Unmarshal(response.Data, &data))
		require.Equal(t, "null", string(data.Target), "never replace an existing target, even at another Git path")
	}
}

func TestTranslationIntegrationTargetReadsJoinCurrentEvidence(t *testing.T) {
	const uid = "target-lifetime"
	const body = "Authored target body.\n"
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Query string }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		turn := reads.Add(1)
		var data any
		if strings.Contains(request.Query, "translationTargetObservation") {
			data = map[string]any{"translationTargetObservation": map[string]any{
				"namespaceUID": "namespace-lifetime",
				"evidence":     map[string]any{"uid": uid, "generation": 2, "revision": "revision-2", "contentDigest": "digest-2"},
			}}
		} else {
			generation, revision := 2, "revision-2"
			if turn == 1 {
				generation, revision = 1, "revision-1"
			}
			data = map[string]any{"target": map[string]any{
				"metadata": map[string]any{
					"uid":        base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Product/" + uid)),
					"generation": generation, "revision": revision,
				},
				"body": body,
				"status": map[string]any{
					"observedGeneration": generation, "lastAppliedRevision": revision,
					"conditions": []map[string]string{{"type": "Ready", "status": "TRUE"}},
				},
			}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"data": data}); err != nil {
			t.Errorf("encode target read: %v", err)
		}
	}))
	defer server.Close()
	fixture := rbacNativeFixture{namespace: "translation-read-join", token: "test"}
	source := []byte("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: target\n---\n" + body)
	_, found := readTranslationIntegrationTarget(t, fixture, fixture, server.URL, source)
	require.False(t, found, "a public read before an update cannot join newer controller evidence")
	sample, found := readTranslationIntegrationTarget(t, fixture, fixture, server.URL, source)
	require.True(t, found)
	require.Equal(t, int64(2), sample.Generation)
}

func pushTranslationIntegrationSources(t *testing.T, git rbacNativeGit, worker *capacityPushWorker, format string, sources map[string][]byte) string {
	t.Helper()
	hash, err := commitTranslationIntegrationSources(t.Context(), worker, sources)
	require.NoError(t, err)
	git.push(t)
	return "main@" + format + ":" + hash
}

func testTranslationNativeDeferredAndOrdering(t *testing.T, fixture rbacNativeFixture, git rbacNativeGit, worker *capacityPushWorker,
	format string, product, translation []byte,
) {
	t.Helper()
	for _, name := range []string{"missing-product", "source-first-product", "target-first-product"} {
		target := bytes.Replace(product, []byte("  name: laptop\n"), []byte("  name: "+name+"\n"), 1)
		source := bytes.Replace(translation, []byte("  name: laptop-title-de-de\n"), []byte("  name: "+name+"-de\n"), 1)
		source = bytes.Replace(source, []byte("    name: laptop\n"), []byte("    name: "+name+"\n"), 1)
		requireTranslationIntegrationTargetAbsent(t, fixture, target)
		sourcePath, targetPath := "a-translations/"+name+".md", "z-targets/"+name+".md"
		if name == "target-first-product" {
			sourcePath, targetPath = "z-translations/"+name+".md", "a-targets/"+name+".md"
		}
		for _, path := range []string{sourcePath, targetPath} {
			_, err := secretCapacityGit(t.Context(), worker.directory, "", "cat-file", "-e", "HEAD:"+path)
			require.Error(t, err, "never overwrite an existing authored path")
		}
		sources := map[string][]byte{sourcePath: source, targetPath: target}
		if name == "missing-product" {
			delete(sources, targetPath)
		}
		revision := pushTranslationIntegrationSources(t, git, worker, format, sources)
		byName := map[string][]byte{name + "-de": source}
		initial := awaitTranslationIntegrationResources(t, fixture, byName, revision, name != "missing-product")
		if name == "missing-product" {
			pushTranslationIntegrationSources(t, git, worker, format, map[string][]byte{targetPath: target})
			current := awaitTranslationIntegrationResources(t, fixture, byName, revision, true)
			require.Equal(t, initial[name+"-de"].Metadata.UID, current[name+"-de"].Metadata.UID)
			require.Equal(t, initial[name+"-de"].Metadata.Generation, current[name+"-de"].Metadata.Generation,
				"target arrival changes status, not the Translation's authored generation or applied source revision")
		}
	}
}

type translationIntegrationNegative struct {
	Name, Base, Old, New, Field, Reason string
}

func testTranslationNativeRejections(t *testing.T, fixture rbacNativeFixture, sources map[string][]byte,
	cases []translationIntegrationNegative, messages map[string]string, runIDs ...string,
) []translationCapacityRejection {
	t.Helper()
	require.LessOrEqual(t, len(runIDs), 1)
	prefix := "negative"
	if len(runIDs) != 0 {
		require.NotEmpty(t, runIDs[0])
		prefix = fmt.Sprintf("negative-%x", sha256.Sum256([]byte(runIDs[0])))[:21]
	}
	receipts := make([]translationCapacityRejection, 0, len(cases))
	for i, negative := range cases {
		t.Run(negative.Name, func(t *testing.T) {
			invalidName := fmt.Sprintf("%s-invalid-%02d", prefix, i)
			base, exists := sources["translations/"+negative.Base+".md"]
			require.True(t, exists)
			parsed, err := translationIntegrationExpected(base)
			require.NoError(t, err)
			renamed := bytes.Replace(base, []byte("  name: "+parsed.Metadata.Name+"\n"),
				[]byte("  name: "+invalidName+"\n"), 1)
			require.Contains(t, string(renamed), negative.Old)
			invalid := bytes.ReplaceAll(renamed, []byte(negative.Old), []byte(negative.New))
			sentinelName := fmt.Sprintf("%s-sentinel-%02d", prefix, i)
			sentinel := bytes.Replace(sources["translations/product-field.md"], []byte("  name: laptop-title-de-de\n"),
				[]byte("  name: "+sentinelName+"\n"), 1)
			root := t.TempDir()
			ctx, cancel := fixtureCommandContext(t, 30*time.Second)
			worker, err := newCapacityPushWorker(ctx, capacityPushConfig{
				remote: fixture.gitURL + "/" + fixture.namespace + "/gitstore-system",
			}, root, fixture.token, 0)
			cancel()
			require.NoError(t, err)
			before, err := readTranslationCapacityReferences(t.Context(), fixture.gitURL+"/"+fixture.namespace+"/gitstore-system", fixture.token)
			require.NoError(t, err)
			parent, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "HEAD")
			require.NoError(t, err)
			commit, err := commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{
				"negative/invalid.md": invalid, "negative/sentinel.md": sentinel,
			})
			require.NoError(t, err)
			git := rbacNativeGit{fixture: fixture, dir: worker.directory}
			message, exists := messages[negative.Reason]
			require.True(t, exists, "missing common Git rejection message for %s", negative.Reason)
			require.NotEmpty(t, message)
			offered := time.Now().UTC()
			git.reject(t, "negative/invalid.md: "+message)
			completed := time.Now().UTC()
			after, err := readTranslationCapacityReferences(t.Context(), fixture.gitURL+"/"+fixture.namespace+"/gitstore-system", fixture.token)
			require.NoError(t, err)
			require.Equal(t, before, after, "rejected candidates cannot advance canonical refs")
			receipt := translationCapacityRejection{
				Case: negative.Name, Reason: negative.Reason, Namespace: fixture.namespace,
				InvalidName: invalidName, SentinelName: sentinelName,
				Commit: commit, Parent: strings.TrimSpace(string(parent)),
				BeforeReferences: before, AfterReferences: after,
				SourceDigest:   fmt.Sprintf("%x", sha256.Sum256(invalid)),
				SentinelDigest: fmt.Sprintf("%x", sha256.Sum256(sentinel)),
				OfferedAt:      offered, CompletedAt: completed,
			}
			for _, api := range fixture.apis {
				for _, name := range []string{sentinelName, invalidName} {
					response := fixture.query(t, api, translationIntegrationQuery(t, "translation"),
						map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": name}}})
					if len(response.Errors) != 0 {
						rbacNativeErrorCode(t, response, "NOT_FOUND")
					}
					var data struct{ Translation json.RawMessage }
					require.NoError(t, json.Unmarshal(response.Data, &data))
					require.Equal(t, "null", string(data.Translation), "a rejected multi-document push must publish nothing")
				}
				receipt.Observations = append(receipt.Observations, translationCapacityRejectionObservation{
					Replica: api, InvalidAbsent: true, SentinelAbsent: true, CheckedAt: time.Now().UTC(),
				})
			}
			receipts = append(receipts, receipt)
		})
	}
	return receipts
}

func TestTranslationNativeQuickstart(t *testing.T) {
	fixture, _ := ownedTranslationNativeFixture(t)
	evidence := os.Getenv("TRANSLATION_TEST_EVIDENCE_FILE")
	if evidence != "" {
		_, err := os.Lstat(evidence)
		require.True(t, os.IsNotExist(err), "preserve existing quickstart receipts")
	}
	reader, credential, _ := newTranslationCapacityReader(t, fixture)
	controller := fixture
	var err error
	controller.token, err = credential.current(t.Context())
	require.NoError(t, err)
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace string
		Targets   map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	targets := make(map[string][]byte, len(corpus.Targets))
	before := make(map[string][2]translationIntegrationTargetSample, len(corpus.Targets))
	for kind, source := range corpus.Targets {
		targets[kind], err = translationIntegrationSource([]byte(source), corpus.Namespace, fixture.namespace)
		require.NoError(t, err)
		for index, api := range fixture.apis {
			controller.token, err = credential.current(t.Context())
			require.NoError(t, err)
			sample, found := readTranslationIntegrationTarget(t, fixture, controller, api, targets[kind])
			require.True(t, found, "quickstart prerequisites must already exist in the owned fixture")
			pair := before[kind]
			pair[index] = sample
			before[kind] = pair
		}
	}
	runID := uniqueName("quickstart")
	documents := translationQuickstartSources(t, fixture.namespace, runID)
	require.Len(t, documents, 2, "exercise both the Product and descriptive File walkthrough")
	worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{
		remote: fixture.gitURL + "/" + fixture.namespace + "/gitstore-system",
	}, t.TempDir(), fixture.token, 0)
	require.NoError(t, err)
	parent, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "HEAD")
	require.NoError(t, err)
	sources, authored := make(map[string][]byte), make(map[string][]byte)
	for _, document := range documents {
		_, found := readTranslationIntegrationResource(t, fixture, fixture.apis[0], document.Name)
		require.False(t, found, "never overwrite an existing owned source")
		sources[document.Path], authored[document.Name] = document.Source, document.Source
	}
	commit, err := commitTranslationIntegrationSources(t.Context(), worker, sources)
	require.NoError(t, err)
	_, err = secretCapacityGit(t.Context(), worker.directory, fixture.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	require.NoError(t, err)
	revision := "main@sha1:" + commit
	awaitTranslationIntegrationResources(t, fixture, authored, revision, true)
	_, err = secretCapacityGit(t.Context(), worker.directory, fixture.token, "fetch", "--quiet", "origin", "main")
	require.NoError(t, err)
	observations := make(map[string][]translationCapacityObservation, len(documents))
	for _, document := range documents {
		actual, err := secretCapacityGit(t.Context(), worker.directory, "", "cat-file", "blob", "FETCH_HEAD:"+document.Path)
		require.NoError(t, err)
		require.Equal(t, document.Source, actual, "quickstart source/body bytes must survive actual Git ingestion")
		for _, api := range fixture.apis {
			observation, err := reader.observe(t.Context(), api, document, revision)
			require.NoError(t, err)
			observations[document.Name] = append(observations[document.Name], observation)
		}
	}
	for kind, source := range targets {
		for index, api := range fixture.apis {
			controller.token, err = credential.current(t.Context())
			require.NoError(t, err)
			sample, found := readTranslationIntegrationTarget(t, fixture, controller, api, source)
			require.True(t, found)
			require.Equal(t, before[kind][index], sample, "quickstart validation cannot apply localized target content")
		}
	}
	if evidence != "" {
		require.NoError(t, writeSecretCapacityPrivateJSON(evidence, map[string]any{
			"schemaVersion": 1, "evidenceClass": "diagnostic", "scope": "native-quickstart-ingestion",
			"runId": runID, "namespace": fixture.namespace, "commit": commit,
			"parent": strings.TrimSpace(string(parent)), "documents": documents, "observations": observations,
			"targetsUnchanged": true,
			"measurementScope": "exact published Product/File sources with only owned namespace/name substitution, actual Git byte retention and both-API current validation; no localized application or capacity acceptance",
		}))
	}
}

func TestTranslationNativeQuickstartLifecycle(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_QUICKSTART_LIFECYCLE") != "1" {
		t.Skip("disposable quickstart lifecycle checks not selected")
	}
	fixture, gitB := ownedTranslationNativeFixture(t)
	for _, api := range fixture.apis {
		require.NoError(t, verifyTranslationIntegrationEmptyNamespace(t, fixture, api),
			"use a fresh disposable namespace, not the retained baseline")
	}
	evidence := os.Getenv("TRANSLATION_TEST_EVIDENCE_FILE")
	if evidence != "" {
		_, err := os.Lstat(evidence)
		require.True(t, os.IsNotExist(err), "preserve existing lifecycle receipts")
	}
	_, credential, _ := newTranslationCapacityReader(t, fixture)
	controller := fixture
	refreshController := func() {
		var err error
		controller.token, err = credential.current(t.Context())
		require.NoError(t, err)
	}
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace           string
		Targets             map[string]string
		Valid               map[string]string
		Negative            []translationIntegrationNegative
		NegativeGitMessages map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	runID := uniqueName("quickstart-lifecycle")
	product, err := translationIntegrationSource([]byte(corpus.Targets["Product"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	product = bytes.Replace(product, []byte("  name: laptop\n"), []byte("  name: "+runID+"\n"), 1)
	color := "  - name: color\n    title: Color\n    values: [silver, black]\n"
	size := "  - name: size\n    title: Size\n    values: [small, large]\n"
	require.Contains(t, string(product), color)
	product = bytes.Replace(product, []byte(color), []byte(color+size), 1)
	requireTranslationIntegrationTargetAbsent(t, fixture, product)
	var source []byte
	for _, document := range translationQuickstartSources(t, fixture.namespace, runID) {
		expected, err := translationIntegrationExpected(document.Source)
		require.NoError(t, err)
		if expected.Spec.TargetRef.Kind == "Product" {
			source = bytes.Replace(document.Source, []byte("    name: laptop\n"), []byte("    name: "+runID+"\n"), 1)
		}
	}
	require.NotEmpty(t, source)
	expected, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	require.Equal(t, runID, expected.Spec.TargetRef.Name)
	_, found := readTranslationIntegrationResource(t, fixture, fixture.apis[0], expected.Metadata.Name)
	require.False(t, found, "never overwrite an existing owned Translation")
	worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{
		remote: fixture.gitURL + "/" + fixture.namespace + "/gitstore-system",
	}, t.TempDir(), fixture.token, 0)
	require.NoError(t, err)
	git := rbacNativeGit{fixture: fixture, dir: worker.directory}
	format, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "--show-object-format")
	require.NoError(t, err)
	references, err := secretCapacityGit(t.Context(), worker.directory, fixture.token, "ls-remote", "--refs", "origin")
	require.NoError(t, err)
	require.Empty(t, references, "the disposable namespace requires an empty repository")
	productPath, sourcePath := "quickstart/"+runID+"-product.md", "quickstart/"+runID+"-translation.md"
	for _, name := range []string{productPath, sourcePath} {
		_, err := secretCapacityGit(t.Context(), worker.directory, "", "cat-file", "-e", "HEAD:"+name)
		require.Error(t, err, "never overwrite a retained Git path")
	}
	var commits []string
	push := func(sources map[string][]byte) string {
		revision := pushTranslationIntegrationSources(t, git, worker, strings.TrimSpace(string(format)), sources)
		commits = append(commits, revision)
		return revision
	}
	readTarget := func(authored []byte, targetRevision string) translationIntegrationTargetSample {
		var result translationIntegrationTargetSample
		rbacNativeEventually(t, func() bool {
			for i, api := range fixture.apis {
				refreshController()
				sample, found := readTranslationIntegrationTarget(t, fixture, controller, api, authored)
				if !found || sample.Revision != targetRevision || (i != 0 && !reflect.DeepEqual(result, sample)) {
					return false
				}
				result = sample
			}
			return true
		}, "both APIs must converge on the disposable target's current source")
		return result
	}
	readBinding := func() string {
		var uid string
		rbacNativeEventually(t, func() bool {
			var current string
			for i, api := range fixture.apis {
				refreshController()
				response := controller.query(t, api, translationIntegrationQuery(t, "binding"),
					map[string]any{"namespace": fixture.namespace, "name": expected.Metadata.Name})
				if translationIntegrationReadPending(response) {
					return false
				}
				require.Empty(t, response.Errors)
				var data struct {
					TranslationBinding *struct{ BoundUID string }
				}
				require.NoError(t, json.Unmarshal(response.Data, &data))
				require.NotNil(t, data.TranslationBinding)
				require.NotEmpty(t, data.TranslationBinding.BoundUID)
				if i != 0 && current != data.TranslationBinding.BoundUID {
					return false
				}
				current = data.TranslationBinding.BoundUID
			}
			uid = current
			return true
		}, "both APIs must expose the same current binding after transient unavailability")
		return uid
	}
	revision := push(map[string][]byte{productPath: product, sourcePath: source})
	initialTarget := readTarget(product, revision)
	byName := map[string][]byte{expected.Metadata.Name: source}
	initial := awaitTranslationIntegrationResources(t, fixture, byName, revision, true)[expected.Metadata.Name]
	require.Equal(t, initialTarget.UID, readBinding())
	_, err = git.command(t, "remote", "set-url", "origin", gitB+"/"+fixture.namespace+"/gitstore-system")
	require.NoError(t, err)

	reordered := bytes.Replace(product, []byte(color+size), []byte(size+color), 1)
	targetRevision := push(map[string][]byte{productPath: reordered})
	reorderedTarget := readTarget(reordered, targetRevision)
	require.Equal(t, initialTarget.UID, reorderedTarget.UID)
	require.Equal(t, initialTarget.Generation+1, reorderedTarget.Generation)
	current := awaitTranslationIntegrationResources(t, fixture, byName, revision, true)[expected.Metadata.Name]
	require.Equal(t, initial.Metadata.UID, current.Metadata.UID)
	require.Equal(t, initial.Metadata.Generation, current.Metadata.Generation)
	require.Equal(t, initialTarget.UID, readBinding())

	missingSelector := bytes.Replace(reordered, []byte(color), nil, 1)
	targetRevision = push(map[string][]byte{productPath: missingSelector})
	readTarget(missingSelector, targetRevision)
	current = awaitTranslationIntegrationResources(t, fixture, byName, revision, false)[expected.Metadata.Name]
	require.Equal(t, initial.Metadata.UID, current.Metadata.UID)
	require.Equal(t, initial.Metadata.Generation, current.Metadata.Generation)
	require.Equal(t, initialTarget.UID, readBinding())
	targetRevision = push(map[string][]byte{productPath: reordered})
	readTarget(reordered, targetRevision)
	awaitTranslationIntegrationResources(t, fixture, byName, revision, true)

	_, err = git.command(t, "rm", "--", productPath)
	require.NoError(t, err)
	_, err = git.command(t, "commit", "--quiet", "-m", "Exercise disposable Translation target removal")
	require.NoError(t, err)
	hash, err := git.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	git.push(t)
	commits = append(commits, "main@"+strings.TrimSpace(string(format))+":"+strings.TrimSpace(string(hash)))
	rbacNativeEventually(t, func() bool {
		for _, api := range fixture.apis {
			response := fixture.query(t, api, translationIntegrationQuery(t, "target.Product"),
				map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": runID}}})
			if translationIntegrationReadPending(response) && len(response.Data) == 0 {
				return false
			}
			if len(response.Errors) != 0 {
				for _, raw := range response.Errors {
					var failure struct {
						Extensions struct{ Code string }
					}
					require.NoError(t, json.Unmarshal(raw, &failure))
					if failure.Extensions.Code != "NOT_FOUND" {
						return false
					}
				}
			}
			var data struct{ Target json.RawMessage }
			require.NoError(t, json.Unmarshal(response.Data, &data))
			if string(data.Target) != "null" {
				return false
			}
		}
		return true
	}, "only the disposable target may finish removal")
	awaitTranslationIntegrationResources(t, fixture, byName, revision, false)
	require.Equal(t, initialTarget.UID, readBinding())
	targetRevision = push(map[string][]byte{productPath: product})
	replacement := readTarget(product, targetRevision)
	require.NotEqual(t, initialTarget.UID, replacement.UID)
	awaitTranslationIntegrationResources(t, fixture, byName, revision, false)
	require.Equal(t, initialTarget.UID, readBinding(), "same-name recreation cannot reset a binding")

	source = append(bytes.Clone(source), []byte("\nAn unrelated source edit.\n")...)
	byName[expected.Metadata.Name] = source
	revision = push(map[string][]byte{sourcePath: source})
	edited := awaitTranslationIntegrationResources(t, fixture, byName, revision, false)[expected.Metadata.Name]
	require.Equal(t, initial.Metadata.UID, edited.Metadata.UID)
	require.Equal(t, initial.Metadata.Generation+1, edited.Metadata.Generation)
	require.Equal(t, initialTarget.UID, readBinding(), "an unrelated source edit cannot authorize replacement")
	source = bytes.Replace(source, []byte("    name: "+runID+"\n"),
		[]byte("    name: "+runID+"\n    uid: "+replacement.UID+"\n"), 1)
	byName[expected.Metadata.Name] = source
	revision = push(map[string][]byte{sourcePath: source})
	final := awaitTranslationIntegrationResources(t, fixture, byName, revision, true)[expected.Metadata.Name]
	require.Equal(t, initial.Metadata.UID, final.Metadata.UID)
	require.Equal(t, edited.Metadata.Generation+1, final.Metadata.Generation)
	require.Equal(t, replacement.UID, readBinding())
	require.Equal(t, replacement, readTarget(product, targetRevision), "retargeting cannot mutate target material")
	fieldSource, err := translationIntegrationSource([]byte(corpus.Valid["product-field"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	rejectionSource := bytes.Replace(fieldSource, []byte("    name: laptop\n"), []byte("    name: "+runID+"\n"), 1)
	var guideCases []translationIntegrationNegative
	for _, negative := range corpus.Negative {
		switch negative.Name {
		case "locale-underscore", "duplicate-path", "object-terminal", "unknown-spec-member", "authored-status":
			guideCases = append(guideCases, negative)
		}
	}
	require.Len(t, guideCases, 5)
	guideCases = append(guideCases, translationIntegrationNegative{
		Name: "quickstart-reference-identity", Base: "product-field",
		Old: "fieldPath: spec.title", New: "fieldPath: spec.media{hero-image}.fileRef.name",
		Field: "spec.patches[0].fieldPath", Reason: "ReferenceBoundary",
	})
	rejections := testTranslationNativeRejections(t, fixture,
		map[string][]byte{"translations/product-field.md": rejectionSource},
		guideCases, corpus.NegativeGitMessages, runID)
	require.Len(t, rejections, 6)
	deferredProduct, err := translationIntegrationSource([]byte(corpus.Targets["Product"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	testTranslationNativeDeferredAndOrdering(t, fixture, git, worker,
		strings.TrimSpace(string(format)), deferredProduct, fieldSource)
	require.Equal(t, replacement, readTarget(product, targetRevision), "guide checks cannot modify the lifecycle target")
	finalHead, err := git.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	if evidence != "" {
		require.NoError(t, writeSecretCapacityPrivateJSON(evidence, map[string]any{
			"schemaVersion": 1, "evidenceClass": "diagnostic", "scope": "native-quickstart-lifecycle",
			"runId": runID, "namespace": fixture.namespace, "initialReferencesEmpty": true,
			"commits": commits, "source": source, "sourceRevision": revision,
			"finalHead": strings.TrimSpace(string(finalHead)), "guideRejections": rejections, "deferredCases": 3,
			"originalTargetUID": initialTarget.UID, "replacementTargetUID": replacement.UID,
			"translationUID": final.Metadata.UID, "translationGeneration": final.Metadata.Generation,
			"measurementScope": "disposable target selector reorder, missing-selector invalidation, removal/recreation, sticky binding and explicit retarget, six quickstart whole-receive rejections and deferred/candidate ordering through ordinary Git ingestion; not capacity or rollout acceptance",
		}))
	}
}

func TestTranslationNativeGitIngestion(t *testing.T) {
	fixture, gitB := ownedTranslationNativeFixture(t)
	controller := fixture
	controller.token = rbacNativeToken(t, os.Getenv("TRANSLATION_TEST_CONTROLLER_TOKEN_FILE"))
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace           string
		Targets             map[string]string
		Valid               map[string]string
		Negative            []translationIntegrationNegative
		NegativeGitMessages map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for _, api := range fixture.apis {
		require.NoError(t, verifyTranslationIntegrationEmptyNamespace(t, fixture, api))
	}
	root := t.TempDir()
	ctx, cancel := fixtureCommandContext(t, 30*time.Second)
	worker, err := newCapacityPushWorker(ctx, capacityPushConfig{
		remote: strings.TrimRight(fixture.gitURL, "/") + "/" + fixture.namespace + "/gitstore-system",
	}, root, fixture.token, 0)
	cancel()
	require.NoError(t, err)
	git := rbacNativeGit{fixture: fixture, dir: worker.directory}
	targets := make(map[string][]byte)
	for kind, source := range corpus.Targets {
		rendered, err := translationIntegrationSource([]byte(source), corpus.Namespace, fixture.namespace)
		require.NoError(t, err)
		targets["targets/"+kind+".md"] = rendered
		requireTranslationIntegrationTargetAbsent(t, fixture, rendered)
	}
	imageBytes, err := translationIntegrationImage()
	require.NoError(t, err)
	targets["images/laptop.png"] = imageBytes
	for name := range targets {
		_, err := secretCapacityGit(t.Context(), worker.directory, "", "cat-file", "-e", "HEAD:"+name)
		require.Error(t, err, "never overwrite an existing owned repository file")
	}
	_, err = commitTranslationIntegrationSources(t.Context(), worker, targets)
	require.NoError(t, err)
	git.push(t)

	before := make(map[string]translationIntegrationTargetSample)
	rbacNativeEventually(t, func() bool {
		clear(before)
		for name, source := range targets {
			if !strings.HasSuffix(name, ".md") {
				continue
			}
			for i, api := range fixture.apis {
				sample, found := readTranslationIntegrationTarget(t, fixture, controller, api, source)
				if !found || (i != 0 && !reflect.DeepEqual(before[name], sample)) {
					return false
				}
				before[name] = sample
			}
		}
		return true
	}, "both endpoints must converge on original targets before authoring Translations")
	var watches []*translationIntegrationWatch
	for _, api := range fixture.apis {
		cursor := capacityBootstrapCursor(t, api, fixture.token)
		watch, err := dialCapacitySubscription(api, fixture.token, "translation-integration",
			translationIntegrationQuery(t, "watch"), map[string]any{"namespace": fixture.namespace, "cursor": cursor}, 10*time.Second)
		require.NoError(t, err)
		stream := &translationIntegrationWatch{connection: watch, cursor: cursor}
		t.Cleanup(func() { _ = stream.connection.Close() })
		watches = append(watches, stream)
	}
	sources, authored := make(map[string][]byte), make(map[string][]byte)
	for name, source := range corpus.Valid {
		rendered, err := translationIntegrationSource([]byte(source), corpus.Namespace, fixture.namespace)
		require.NoError(t, err)
		expected, err := translationIntegrationExpected(rendered)
		require.NoError(t, err)
		sources["translations/"+name+".md"] = rendered
		authored[expected.Metadata.Name] = rendered
	}
	equivalent := strings.NewReplacer(
		"  name: laptop-title-de-de\n", "  name: laptop-title-equivalent\n",
		"  locale: de-DE\n", "  locale: DE-de\n",
	).Replace(string(sources["translations/product-field.md"]))
	sources["translations/product-equivalent.md"] = []byte(equivalent)
	authored["laptop-title-equivalent"] = []byte(equivalent)
	hash, err := commitTranslationIntegrationSources(t.Context(), worker, sources)
	require.NoError(t, err)
	format, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "--show-object-format")
	require.NoError(t, err)
	_, err = git.command(t, "remote", "set-url", "origin", strings.TrimRight(gitB, "/")+"/"+fixture.namespace+"/gitstore-system")
	require.NoError(t, err)
	git.push(t)
	revision := "main@" + strings.TrimSpace(string(format)) + ":" + hash
	awaitTranslationIntegrationWatches(t, fixture, watches, authored, revision)
	resources := awaitTranslationIntegrationResources(t, fixture, authored, revision, true)
	require.NotEqual(t, resources["laptop-title-de-de"].Metadata.UID, resources["laptop-title-equivalent"].Metadata.UID,
		"equivalent locales must not collapse independently named documents")
	updated := append(bytes.Clone(sources["translations/product-combined.md"]), []byte("\r\nUpdated through the other ingress.\r\n")...)
	updatedSource, err := translationIntegrationExpected(updated)
	require.NoError(t, err)
	hash, err = commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{"translations/product-combined.md": updated})
	require.NoError(t, err)
	_, err = git.command(t, "remote", "set-url", "origin", fixture.gitURL+"/"+fixture.namespace+"/gitstore-system")
	require.NoError(t, err)
	git.push(t)
	updatedRevision := "main@" + strings.TrimSpace(string(format)) + ":" + hash
	updatedByName := map[string][]byte{updatedSource.Metadata.Name: updated}
	awaitTranslationIntegrationWatches(t, fixture, watches, updatedByName, updatedRevision)
	updatedResources := awaitTranslationIntegrationResources(t, fixture, updatedByName, updatedRevision, true)
	require.Equal(t, resources[updatedSource.Metadata.Name].Metadata.UID, updatedResources[updatedSource.Metadata.Name].Metadata.UID)
	require.Equal(t, resources[updatedSource.Metadata.Name].Metadata.Generation+1, updatedResources[updatedSource.Metadata.Name].Metadata.Generation)
	sources["translations/product-combined.md"] = updated
	delete(authored, updatedSource.Metadata.Name)
	unchanged := awaitTranslationIntegrationResources(t, fixture, authored, revision, true)
	for name, resource := range unchanged {
		require.Equal(t, resources[name].Metadata.UID, resource.Metadata.UID)
		require.Equal(t, resources[name].Metadata.Generation, resource.Metadata.Generation)
	}
	for name, source := range targets {
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		for _, api := range fixture.apis {
			sample, found := readTranslationIntegrationTarget(t, fixture, controller, api, source)
			require.True(t, found)
			require.Equal(t, before[name], sample,
				"Translation admission and reconciliation may not mutate target authored or resolved material")
		}
	}
	_, err = secretCapacityGit(t.Context(), worker.directory, fixture.token, "fetch", "--quiet", "origin", "main")
	require.NoError(t, err)
	for name, expected := range sources {
		actual, err := secretCapacityGit(t.Context(), worker.directory, "", "cat-file", "blob", "FETCH_HEAD:"+name)
		require.NoError(t, err)
		require.Equal(t, expected, actual, "accepted Git source must retain exact bytes")
	}
	t.Run("deferred-and-candidate-ordering", func(t *testing.T) {
		testTranslationNativeDeferredAndOrdering(t, fixture, git, worker, strings.TrimSpace(string(format)),
			targets["targets/Product.md"], sources["translations/product-field.md"])
	})
	t.Run("whole-receive-rejections", func(t *testing.T) {
		testTranslationNativeRejections(t, fixture, sources, corpus.Negative, corpus.NegativeGitMessages)
	})
}
