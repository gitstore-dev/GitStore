// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

func TestTranslationCapacityCorpusPreservesSourcesAndConcentratedFanout(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var namespaces [10]string
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("translation-capacity-%02d", i)
	}
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	require.Len(t, dataset.Documents, 1009)
	identities, locales, kinds, dependents := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]int{}
	nearLimit, fieldOnly, bodyOnly, combined := 0, 0, 0, 0
	for _, document := range dataset.Documents {
		expected, err := translationIntegrationExpected(document.Source)
		require.NoError(t, err)
		require.Equal(t, document.Namespace, expected.Metadata.Namespace)
		require.Equal(t, document.Name, expected.Metadata.Name)
		identity := document.Namespace + "/" + document.Name
		require.False(t, identities[identity], "independently named documents must not collapse")
		identities[identity] = true
		locales[expected.Spec.Locale], kinds[expected.Spec.TargetRef.Kind] = true, true
		dependents[document.Namespace+"/"+expected.Spec.TargetRef.Kind+"/"+expected.Spec.TargetRef.Name]++
		hash := sha256.Sum256(document.Source)
		require.Equal(t, hex.EncodeToString(hash[:]), document.SourceDigest)
		hash = sha256.Sum256(expected.body)
		require.Equal(t, hex.EncodeToString(hash[:]), document.BodyDigest)
		require.LessOrEqual(t, len(document.Source), 1<<20)
		if len(document.Source) == 1<<20 {
			nearLimit++
		}
		switch {
		case len(expected.Spec.Patches) > 0 && len(bytes.TrimSpace(expected.body)) > 0:
			combined++
		case len(expected.Spec.Patches) > 0:
			fieldOnly++
		default:
			require.NotEmpty(t, bytes.TrimSpace(expected.body))
			bodyOnly++
		}
	}
	require.Equal(t, 1000, dependents[namespaces[0]+"/Product/laptop"])
	require.Len(t, locales, 2)
	require.Len(t, kinds, 5)
	require.Positive(t, fieldOnly)
	require.Positive(t, bodyOnly)
	require.Positive(t, combined)
	require.Equal(t, 1, nearLimit)
	for _, namespace := range namespaces {
		require.Len(t, dataset.Targets[namespace], 5)
		require.NotZero(t, dependents[namespace+"/Product/laptop"]+
			dependents[namespace+"/ProductVariant/laptop-silver"]+
			dependents[namespace+"/CategoryTaxonomy/computers"]+
			dependents[namespace+"/Collection/featured"]+dependents[namespace+"/File/laptop-hero"])
	}
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for i, template := range []string{"product-field", "product-body", "product-combined"} {
		original, err := translationIntegrationExpected([]byte(corpus.Valid[template]))
		require.NoError(t, err)
		rendered, err := translationIntegrationExpected(dataset.Documents[i].Source)
		require.NoError(t, err)
		require.Equal(t, original.body, rendered.body, "namespace/name/locale replacement must preserve exact body bytes")
		push, err := renderTranslationCapacityPush(dataset.Documents[i], "fixture", i)
		require.NoError(t, err)
		actual, err := translationIntegrationExpected(push.Source)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("cpd-fixture-%06d", i), actual.Metadata.Name)
		require.Equal(t, original.body, actual.body)
		require.Equal(t, dataset.Documents[i].BodyDigest, push.BodyDigest)
		require.Equal(t, dataset.Documents[i].PatchDigest, push.PatchDigest)
		require.NotEqual(t, dataset.Documents[i].SourceDigest, push.SourceDigest)
	}
	require.Contains(t, string(dataset.Documents[2].Source), "\r\n")
	nearLimitTemplate := dataset.Documents[999]
	original, err := translationIntegrationExpected(nearLimitTemplate.Source)
	require.NoError(t, err)
	for _, runID := range []string{"x", strings.Repeat("r", 40)} {
		push, err := renderTranslationCapacityPush(nearLimitTemplate, runID, 0)
		require.NoError(t, err, "renaming a near-limit push must resize only its synthetic padding")
		require.Equal(t, 1<<20, len(push.Source))
		actual, err := translationIntegrationExpected(push.Source)
		require.NoError(t, err)
		require.Equal(t, "cpd-"+runID+"-000000", actual.Metadata.Name)
		require.Equal(t, bytes.TrimRight(original.body, "x"), bytes.TrimRight(actual.body, "x"))
		require.Equal(t, nearLimitTemplate.PatchDigest, push.PatchDigest)
		hash := sha256.Sum256(push.Source)
		require.Equal(t, hex.EncodeToString(hash[:]), push.SourceDigest)
		hash = sha256.Sum256(actual.body)
		require.Equal(t, hex.EncodeToString(hash[:]), push.BodyDigest)
	}
	hash := sha256.Sum256(nearLimitTemplate.Source)
	require.Equal(t, hex.EncodeToString(hash[:]), nearLimitTemplate.SourceDigest, "retained near-limit source must not change")
	unmarked := nearLimitTemplate
	unmarked.PaddingBytes = 0
	push, err := renderTranslationCapacityPush(unmarked, "x", 0)
	require.NoError(t, err)
	require.Equal(t, nearLimitTemplate.BodyDigest, push.BodyDigest, "authored body bytes without a synthetic padding marker must not be resized")
	broken := nearLimitTemplate
	broken.Source = bytes.Clone(nearLimitTemplate.Source)
	broken.Source[len(broken.Source)-1] = 'y'
	_, err = renderTranslationCapacityPush(broken, "x", 0)
	require.Error(t, err, "a padding marker cannot authorize replacing non-filler body bytes")
}

func TestTranslationCapacityCorpusRejectsUnownedOrIncompleteInputs(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var namespaces [10]string
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("translation-capacity-%02d", i)
	}
	for _, value := range []string{"", "../escape", "operator-namespace", namespaces[0]} {
		invalid := namespaces
		invalid[9] = value
		_, err := buildTranslationCapacityDataset(raw, invalid)
		require.Error(t, err)
	}
	var corpus map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &corpus))
	delete(corpus, "valid")
	incomplete, err := json.Marshal(corpus)
	require.NoError(t, err)
	_, err = buildTranslationCapacityDataset(incomplete, namespaces)
	require.Error(t, err)
}

func TestTranslationCapacityMeasuredScheduleIncludesNearLimitWithoutChangingRetainedSource(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var namespaces [10]string
	for index := range namespaces {
		namespaces[index] = fmt.Sprintf("translation-schedule-%d", index)
	}
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	before := bytes.Clone(dataset.Documents[999].Source)
	documents, err := translationCapacityScheduledDocuments(dataset, "measured", 30)
	require.NoError(t, err)
	require.Len(t, documents, 30)
	require.Len(t, documents[0].Source, 1<<20)
	require.Equal(t, before, dataset.Documents[999].Source)
	field, body, combined, nearLimit := 0, 0, 0, 0
	for sequence, document := range documents {
		require.Equal(t, namespaces[sequence%10], document.Namespace)
		require.Equal(t, fmt.Sprintf("cpd-measured-%06d", sequence), document.Name)
		parsed, err := translationIntegrationExpected(document.Source)
		require.NoError(t, err)
		if len(document.Source) == 1<<20 {
			nearLimit++
		}
		switch {
		case len(parsed.Spec.Patches) > 0 && len(bytes.TrimSpace(parsed.body)) > 0:
			combined++
		case len(parsed.Spec.Patches) > 0:
			field++
		default:
			body++
		}
	}
	require.Equal(t, 1, nearLimit)
	require.Positive(t, field)
	require.Positive(t, body)
	require.Positive(t, combined)
	preparation, err := translationCapacityScheduledDocuments(dataset, strings.Repeat("w", 39), 20)
	require.NoError(t, err)
	require.Len(t, preparation[0].Source, 1<<20, "preparation must exercise the measured near-limit channel, not only smaller sources")
	require.NotEqual(t, documents[0].Name, preparation[0].Name)
	require.Equal(t, before, dataset.Documents[999].Source)
}

func TestTranslationCapacityRejectionReceiptJoinsRealRejectedCommitAndBothAPIs(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace           string
		Valid               map[string]string
		Negative            []translationIntegrationNegative
		NegativeGitMessages map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	namespace := "translation-negative-contract"
	sources := make(map[string][]byte)
	for name, source := range corpus.Valid {
		sources["translations/"+name+".md"], err = translationIntegrationSource([]byte(source), corpus.Namespace, namespace)
		require.NoError(t, err)
	}
	root := t.TempDir()
	remote := filepath.Join(root, namespace, "gitstore-system")
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o700))
	_, err = secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", remote)
	require.NoError(t, err)
	seed, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, t.TempDir(), "", 0)
	require.NoError(t, err)
	parent, err := commitTranslationIntegrationSources(t.Context(), seed, map[string][]byte{
		"translations/original.md": sources["translations/product-field.md"],
	})
	require.NoError(t, err)
	_, err = secretCapacityGit(t.Context(), seed.directory, "", "push", "--quiet", "origin", "HEAD:refs/heads/main")
	require.NoError(t, err)
	before, err := readTranslationCapacityReferences(t.Context(), remote, "")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(
		"#!/bin/sh\nprintf '%s\\n' 'negative/invalid.md: expected a well-formed BCP 47 tag' >&2\nexit 1\n"), 0o700))
	var reads atomic.Int64
	serve := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"translation":null}}`))
		}))
	}
	a, b := serve(), serve()
	defer a.Close()
	defer b.Close()
	var negatives []translationIntegrationNegative
	for _, negative := range corpus.Negative {
		if negative.Name == "locale-underscore" {
			negatives = append(negatives, negative)
		}
	}
	fixture := rbacNativeFixture{namespace: namespace, gitURL: root, apis: []string{a.URL, b.URL}}
	receipts := testTranslationNativeRejections(t, fixture, sources, negatives, corpus.NegativeGitMessages, "receipt")
	require.Len(t, receipts, 1)
	receipt := receipts[0]
	require.Equal(t, parent, receipt.Parent)
	require.NotEqual(t, parent, receipt.Commit)
	require.Equal(t, before, receipt.BeforeReferences)
	require.Equal(t, before, receipt.AfterReferences)
	require.Equal(t, int64(4), reads.Load())
	require.Len(t, receipt.Observations, 2)
	require.NotEqual(t, receipt.SourceDigest, receipt.SentinelDigest)
	for _, observation := range receipt.Observations {
		require.True(t, observation.InvalidAbsent && observation.SentinelAbsent)
		require.False(t, observation.CheckedAt.Before(receipt.CompletedAt))
	}
}

func TestTranslationCapacityRetainedSourceUsesLastAuthoredCommitAndExactGitBytes(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var namespaces [10]string
	for index := range namespaces {
		namespaces[index] = fmt.Sprintf("translation-retained-%d", index)
	}
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	document := dataset.Documents[999]
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			remote := root + "/retained.git"
			_, err := secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", "--object-format="+format, remote)
			require.NoError(t, err)
			worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote, namespace: document.Namespace}, root, "", 0)
			require.NoError(t, err)
			authored, err := commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{document.Path: document.Source})
			require.NoError(t, err)
			head, err := commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{"unrelated.txt": []byte("later retained history\n")})
			require.NoError(t, err)
			require.NotEqual(t, authored, head)
			revision, err := readTranslationCapacityRetainedDocument(t.Context(), worker, document)
			require.NoError(t, err)
			require.Equal(t, "main@"+format+":"+authored, revision, "unrelated later commits cannot become the source revision")
			wrong := document
			wrong.Namespace = namespaces[1]
			_, err = readTranslationCapacityRetainedDocument(t.Context(), worker, wrong)
			require.Error(t, err, "retained sources cannot cross clone scopes")
			changed := append([]byte(nil), document.Source...)
			changed[len(changed)-1] = 'y'
			_, err = commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{document.Path: changed})
			require.NoError(t, err)
			_, err = readTranslationCapacityRetainedDocument(t.Context(), worker, document)
			require.Error(t, err, "a partial or changed near-limit body cannot reuse retained source evidence")
		})
	}
}

func TestTranslationCapacityReaderJoinsNearLimitSourceAndAuthorizedSnapshot(t *testing.T) {
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var namespaces [10]string
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("translation-capacity-%02d", i)
	}
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	document := dataset.Documents[999]
	const revision = "main@sha1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, change := range []string{"", "partial-body", "not-ready", "stale-snapshot", "wrong-digest", "negative-snapshot"} {
		t.Run(change, func(t *testing.T) {
			resource, err := translationIntegrationExpected(document.Source)
			require.NoError(t, err)
			resource.Metadata.UID, resource.Metadata.ResourceVersion = "source-uid", "version-2"
			resource.Metadata.Generation, resource.Metadata.Revision = 2, revision
			body := string(resource.body)
			resource.Body = &body
			resource.Status.ObservedGeneration, resource.Status.LastAppliedRevision = 2, revision
			resource.Status.Conditions = []translationIntegrationCondition{{Type: "Ready", Status: "TRUE", ObservedGeneration: 2}}
			resource.Status.Resolved = &struct {
				CanonicalLocale string `json:"canonicalLocale"`
			}{CanonicalLocale: strings.ToLower(resource.Spec.Locale)}
			snapshot := map[string]any{
				"sourceVersion": resource.Metadata.ResourceVersion, "sourceDigest": document.SourceDigest,
				"observation": map[string]any{
					"observedGeneration": 2, "lastAppliedRevision": revision,
					"conditions": resource.Status.Conditions, "resolved": map[string]any{"proof": "current"},
				},
			}
			switch change {
			case "partial-body":
				body = "partial"
			case "not-ready":
				resource.Status.Conditions = nil
			case "stale-snapshot":
				snapshot["sourceVersion"] = "previous-version"
			case "wrong-digest":
				snapshot["sourceDigest"] = strings.Repeat("b", 64)
			case "negative-snapshot":
				snapshot["observation"] = nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request gqlRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					http.Error(w, "invalid fixture request", http.StatusBadRequest)
					return
				}
				result := map[string]any{"translation": resource}
				if strings.Contains(request.Query, "translationValidationSnapshot") {
					result = map[string]any{"translationValidationSnapshot": snapshot}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
			}))
			defer server.Close()
			reader := translationCapacityReader{
				client: server.Client(), token: "fixture", controllerToken: "fixture-controller",
				query: translationIntegrationQuery(t, "translation"), snapshot: translationIntegrationQuery(t, "snapshot"),
			}
			before := time.Now()
			observation, err := reader.observe(t.Context(), server.URL, document, revision)
			if change != "" {
				require.Error(t, err)
				if change == "not-ready" {
					require.False(t, observation.VisibleAt.Before(before), "visible authored source is not current Ready evidence")
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, document.translationCapacityContent, observation.translationCapacityContent)
			require.Equal(t, resource.Metadata.UID, observation.UID)
			require.Equal(t, resource.Metadata.ResourceVersion, observation.Version)
			require.True(t, observation.Ready)
			require.False(t, observation.VisibleAt.Before(before))
			require.False(t, observation.ReadyAt.Before(observation.VisibleAt))
			require.Equal(t, observation.VisibleAt, observation.ReadVisibleAt)
			require.Equal(t, observation.ReadyAt, observation.ReadReadyAt)
			require.Empty(t, observation.WatchCursor, "a read cannot fabricate durable watch proof")
			actual, found := readTranslationIntegrationResource(t,
				rbacNativeFixture{token: "fixture", namespace: document.Namespace}, server.URL, document.Name)
			require.True(t, found)
			require.NoError(t, verifyTranslationIntegrationResource(document.Source, revision, actual, true))
			for _, identity := range []string{"matching", "recreated", "advanced-version", "missing-uid", "missing-version"} {
				t.Run("recovery-read/"+identity, func(t *testing.T) {
					resource.Metadata.UID, resource.Metadata.ResourceVersion = "source-uid", "version-2"
					target := translationCapacityReadTarget{Document: document, Revision: revision,
						SourceID: "source-uid", SourceVersion: "version-2"}
					switch identity {
					case "recreated":
						resource.Metadata.UID = "recreated-source"
					case "advanced-version":
						resource.Metadata.ResourceVersion = "version-3"
					case "missing-uid":
						target.SourceID = ""
					case "missing-version":
						target.SourceVersion = ""
					}
					started := time.Now()
					reads, readErr := reader.recoveryReads(t.Context(), [2]string{server.URL, server.URL + "/replica"},
						document, strings.TrimPrefix(revision, "main@sha1:"), target.SourceID, target.SourceVersion)
					if identity == "matching" {
						require.NoError(t, readErr)
						require.Len(t, reads, 2)
						for _, read := range reads {
							require.False(t, read.VisibleAt.Before(started), "recovery receipts require fresh reads, not earlier observations")
							require.Equal(t, document.translationCapacityContent, read.translationCapacityContent)
						}
					} else {
						require.Error(t, readErr)
						require.Empty(t, reads, "failed source identity joins cannot publish partial recovery evidence")
					}
					result, err := reader.load(t.Context(), [2]string{server.URL, server.URL + "/replica"},
						time.Now(), 10*time.Millisecond, 20, 1, []translationCapacityReadTarget{target})
					if strings.HasPrefix(identity, "missing-") {
						require.Error(t, err, "recovery reads require the complete identity/version pair")
						return
					}
					require.NoError(t, err)
					require.Equal(t, 1, result.Completed)
					expectedErrors := 0
					if identity != "matching" {
						expectedErrors = 1
					}
					require.Equal(t, expectedErrors, result.UnexpectedErrors,
						"matching authored bytes cannot hide a recreated or advanced source")
				})
			}
		})
	}
}

func TestTranslationCapacityControllerCredentialRefreshesThroughNormalIssuance(t *testing.T) {
	key, err := newSecretCapacityOwnedKey()
	require.NoError(t, err)
	fixture := secretCapacityOwnedFixture{Name: "gitstore-controller-manager", UID: "enrolled-controller"}
	var calls, mode atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if mode.Load() == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		var assertion jwt.MapClaims
		_, _, err := jwt.NewParser().ParseUnverified(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), &assertion)
		if err != nil || assertion["sa_uid"] != fixture.UID {
			http.Error(w, "assertion rejected", http.StatusUnauthorized)
			return
		}
		expires := time.Now().Add(time.Minute).Truncate(time.Second)
		claims := jwt.MapClaims{
			"sub": "system:serviceaccount:" + fixture.Name, "sa_uid": fixture.UID,
			"aud": []string{"gitstore-api"}, "exp": expires.Unix(), "jti": fmt.Sprint(calls.Load()),
		}
		if mode.Load() == 2 {
			claims["sa_uid"] = "other-controller"
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("fixture"))
		if err != nil {
			http.Error(w, "issuance failed", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"issueServiceAccountToken": map[string]any{
			"tokenRequest": map[string]any{"status": map[string]any{"token": token, "expirationTimestamp": expires}},
		}}})
	}))
	defer server.Close()
	credential := translationCapacityControllerCredential{client: server.Client(), api: server.URL, fixture: fixture, key: key}
	first, err := credential.current(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, first)
	_, err = credential.current(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, calls.Load(), "an unexpired issued credential is reused")
	credential.expires = time.Now().Add(10 * time.Second)
	next, err := credential.current(t.Context())
	require.NoError(t, err)
	require.True(t, next != first, "renewal must use a newly issued credential")
	require.EqualValues(t, 2, calls.Load())
	for _, failure := range []int64{1, 2} {
		credential.expires = time.Now().Add(-time.Second)
		mode.Store(failure)
		token, err := credential.current(t.Context())
		require.Error(t, err)
		require.Empty(t, token, "issuance failure or wrong identity cannot fall back to an expired token")
	}
}

func TestTranslationCapacityReusesFiniteOfferedSchedule(t *testing.T) {
	var active, maximum atomic.Int64
	const workers = 2
	offered, dropped, results := runCapacityPushSchedule(t.Context(), 80*time.Millisecond, time.Millisecond, workers,
		func(context.Context, int) capacityPushResult {
			n := active.Add(1)
			for previous := maximum.Load(); n > previous; previous = maximum.Load() {
				if maximum.CompareAndSwap(previous, n) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			active.Add(-1)
			return capacityPushResult{acknowledged: true, at: time.Now()}
		})
	require.Equal(t, 80, offered, "slow completion must not reduce the offered schedule")
	require.Positive(t, dropped, "a finite pending budget must expose overload")
	require.Equal(t, offered, dropped+len(results))
	require.LessOrEqual(t, maximum.Load(), int64(workers))
	for _, result := range results {
		require.GreaterOrEqual(t, result.latency, result.dispatchDelay)
	}
}

func TestTranslationCapacityReadLoadChargesFailuresAndOverload(t *testing.T) {
	var requests atomic.Int64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		time.Sleep(30 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"translation": nil}})
	})
	a, b := httptest.NewServer(handler), httptest.NewServer(handler)
	defer a.Close()
	defer b.Close()
	reader := translationCapacityReader{client: a.Client(), query: translationIntegrationQuery(t, "translation")}
	targets := []translationCapacityReadTarget{{Document: translationCapacityDocument{Namespace: "translation-owned", Name: "missing"}}}
	result, err := reader.load(t.Context(), [2]string{a.URL, b.URL}, time.Time{}, 100*time.Millisecond, 100, 1, targets)
	require.NoError(t, err)
	require.Equal(t, 10, result.Offered, "slow reads cannot lower the offered denominator")
	require.Positive(t, result.Dropped)
	require.Equal(t, result.Offered, result.Completed+result.Dropped)
	require.Equal(t, result.Offered, result.UnexpectedErrors, "missing sources and scheduler drops remain failures")
	require.Equal(t, int64(result.Completed), requests.Load(), "load counts public domain requests, not private oracle calls")
	require.Len(t, result.Latencies, result.Completed)
	missing := make(map[int]bool, result.Offered)
	for sequence := range result.Offered {
		missing[sequence] = true
	}
	for index, sequence := range result.CompletedSequences {
		require.True(t, missing[sequence], "completion diagnostics must identify unique actual scheduled offers")
		delete(missing, sequence)
		require.LessOrEqual(t, result.DispatchDelays[index], result.Latencies[index])
	}
	require.Len(t, missing, result.Dropped, "diagnostics must retain the identities of every dropped offer")
}

func TestTranslationCapacitySchedulesShareAnExactOfferedWindow(t *testing.T) {
	started := time.Now().Add(20 * time.Millisecond)
	offered, dropped, results := runCapacityPushScheduleAt(t.Context(), started, 100*time.Millisecond, 50*time.Millisecond, 1,
		func(_ context.Context, sequence int) capacityPushResult {
			return capacityPushResult{sequence: sequence, acknowledged: true, at: time.Now()}
		})
	require.Equal(t, 2, offered)
	require.Zero(t, dropped)
	require.Len(t, results, offered)
	for _, result := range results {
		require.True(t, result.at.Add(-result.latency).Equal(started.Add(time.Duration(result.sequence)*50*time.Millisecond)),
			"receipts must use the exact common anchor, not an approximate caller timestamp")
	}
}

func TestTranslationCapacityConnectedModeSchedule(t *testing.T) {
	for _, test := range []struct {
		name, mode, duration, interval, workers string
		wantDuration, wantInterval              time.Duration
		wantWorkers, wantOffers                 int
	}{
		{"diagnostic-default", "diagnostic", "", "", "", 10 * time.Second, time.Second, 2, 10},
		{"diagnostic-bound", "diagnostic", "90m", "1s", "2", 90 * time.Minute, time.Second, 2, 5400},
		{"alpha-minimum", "alpha", "30m", "", "", 30 * time.Minute, 6 * time.Second, 2, 300},
		{"alpha-single-worker", "alpha", "30m", "6s", "1", 30 * time.Minute, 6 * time.Second, 1, 300},
		{"alpha-bound", "alpha", "90m", "6s", "2", 90 * time.Minute, 6 * time.Second, 2, 900},
		{"missing-mode", "", "30m", "", "", 0, 0, 0, 0},
		{"production-closed", "production", "60m", "", "", 0, 0, 0, 0},
		{"unknown-mode", "Alpha", "30m", "", "", 0, 0, 0, 0},
		{"diagnostic-short", "diagnostic", "9.999s", "", "", 0, 0, 0, 0},
		{"alpha-missing-window", "alpha", "", "", "", 0, 0, 0, 0},
		{"alpha-short", "alpha", "29m59.999s", "", "", 0, 0, 0, 0},
		{"unbounded", "alpha", "90m1ns", "", "", 0, 0, 0, 0},
		{"invalid-duration", "alpha", "invalid", "", "", 0, 0, 0, 0},
		{"alpha-rate-override", "alpha", "30m", "1s", "", 0, 0, 0, 0},
		{"diagnostic-rate-override", "diagnostic", "10s", "6s", "", 0, 0, 0, 0},
		{"invalid-interval", "alpha", "30m", "invalid", "", 0, 0, 0, 0},
		{"worker-overflow", "alpha", "30m", "", "3", 0, 0, 0, 0},
		{"zero-workers", "diagnostic", "10s", "", "0", 0, 0, 0, 0},
		{"invalid-workers", "alpha", "30m", "", "invalid", 0, 0, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := parseTranslationCapacityConnectedConfig(test.mode, test.duration, test.interval, test.workers)
			if test.wantOffers == 0 {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.mode, config.Mode)
			require.Equal(t, test.wantDuration, config.Duration)
			require.Equal(t, test.wantInterval, config.Interval)
			require.Equal(t, test.wantWorkers, config.Workers)
			require.Equal(t, test.wantOffers, config.scheduledOffers())
		})
	}
}

func TestTranslationCapacityPreparationBarrier(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(32 * time.Second)
	for _, change := range []string{"", "missing-warmup", "missed-warmup", "wrong-offset", "reused-run"} {
		t.Run(change, func(t *testing.T) {
			run, warmup := "fixture", start.Add(-30*time.Second)
			switch change {
			case "missing-warmup":
				warmup = time.Time{}
			case "missed-warmup":
				warmup = now.Add(-time.Second)
			case "wrong-offset":
				warmup = warmup.Add(time.Millisecond)
			case "reused-run":
				run = "other"
			}
			raw, err := json.Marshal(map[string]any{"runId": run, "startAt": start, "warmupStartAt": warmup})
			require.NoError(t, err)
			measured, prepared, err := readTranslationCapacityPreparationBarrier(raw, "fixture", now)
			if change != "" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.True(t, measured.Equal(start))
			require.True(t, prepared.Equal(warmup))
		})
	}
}

func TestTranslationCapacityPreparationRetainsOwnedWatch(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(fmt.Sprintf("retain-%t", retain), func(t *testing.T) {
			watchContext, cancel := context.WithCancel(t.Context())
			defer cancel()
			watch := &capacityPushWatch{cancel: cancel}
			oracle := startCapacityPushOracle(t.Context(), watch, func(context.Context, capacityPushResult) error {
				return nil
			})
			oracle.offer(capacityPushResult{})
			verified, failed, dropped := oracle.finish(retain)
			require.Equal(t, 1, verified)
			require.Zero(t, failed)
			require.Zero(t, dropped)
			if retain {
				require.NoError(t, watchContext.Err(), "measured traffic must reuse the still-live owned watch")
				watch.close()
			}
			require.ErrorIs(t, watchContext.Err(), context.Canceled)
		})
	}
}

func TestTranslationCapacityPreparationSharesScopedBoundedWatch(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		t.Run(fmt.Sprintf("prepared-%t", prepared), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := (&websocket.Upgrader{Subprotocols: []string{"graphql-transport-ws"}}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer connection.Close()
				var message struct {
					ID      string                 `json:"id"`
					Payload struct{ Query string } `json:"payload"`
				}
				if connection.ReadJSON(&message) != nil {
					return
				}
				_ = connection.WriteJSON(map[string]any{"type": "connection_ack"})
				if connection.ReadJSON(&message) != nil {
					return
				}
				if strings.Contains(message.Payload.Query, "watchNamespaces") {
					_ = connection.WriteJSON(map[string]any{"id": message.ID, "type": "next",
						"payload": map[string]any{"data": map[string]any{"watchNamespaces": map[string]any{
							"type": "BOOKMARK", "resourceVersion": "bootstrap-cursor",
						}}}})
					return
				}
				for _, name := range []string{"cpd-preparation-000000", "cpd-preparation-000001", "cpd-unrelated-000000", "cpd-fixture-000000", "cpd-fixture-000001"} {
					_ = connection.WriteJSON(map[string]any{"id": message.ID, "type": "next",
						"payload": map[string]any{"data": map[string]any{"watchTranslations": map[string]any{
							"type": "ADDED", "name": name, "resourceVersion": "cursor-" + name,
							"translation": map[string]any{
								"apiVersion": "i18n.gitstore.dev/v1beta1", "kind": "Translation",
								"metadata": map[string]any{"uid": name, "name": name, "namespace": "translation-owned",
									"generation": 1, "revision": "main@sha1:" + strings.Repeat("a", 40), "resourceVersion": "source-" + name},
								"spec": map[string]any{"locale": "de-DE"}, "body": "Exact source body",
							},
						}}}})
				}
				_, _, _ = connection.ReadMessage()
			}))
			defer server.Close()
			cfg := capacityPushConfig{kind: "translation", runID: "fixture", namespace: "translation-owned",
				verifyAPIA: server.URL, verifyAPIB: server.URL, duration: time.Second, interval: time.Second}
			var preparation []capacityPushConfig
			if prepared {
				extra := cfg
				extra.runID, extra.duration = "preparation", 20*time.Second
				preparation = append(preparation, extra)
			}
			watch, err := newCapacityPushWatch(t.Context(), cfg, "fixture", preparation...)
			require.NoError(t, err)
			defer watch.close()
			count := 2
			if prepared {
				count += 2
			}
			require.Eventually(t, func() bool {
				watch.mu.Lock()
				defer watch.mu.Unlock()
				return watch.err == nil && len(watch.seen[0]) == count && len(watch.seen[1]) == count
			}, time.Second, time.Millisecond, "both phases must join the same watch without accepting unrelated runs or exhausting the measured-only bound")
			watch.mu.Lock()
			defer watch.mu.Unlock()
			for _, seen := range watch.seen {
				require.NotContains(t, seen, "cpd-unrelated-000000")
				if prepared {
					require.Contains(t, seen, "cpd-preparation-000000")
				} else {
					require.NotContains(t, seen, "cpd-preparation-000000")
				}
			}
		})
	}
}

func TestTranslationCapacityConnectedScheduleReceipt(t *testing.T) {
	for _, mode := range []string{"diagnostic", "alpha"} {
		t.Run(mode, func(t *testing.T) {
			duration := "10s"
			if mode == "alpha" {
				duration = "30m"
			}
			config, err := parseTranslationCapacityConnectedConfig(mode, duration, "", "")
			require.NoError(t, err)
			start := time.Now().UTC()
			planned := config.scheduledOffers()
			for _, change := range []string{"", "missing-start", "partial-window", "missed-offer", "dropped", "incomplete", "unverified"} {
				t.Run(change, func(t *testing.T) {
					at, offered, dropped, completed, verified := start, planned, 0, planned, planned
					finished := start.Add(config.Duration)
					switch change {
					case "missing-start":
						at = time.Time{}
					case "partial-window":
						finished = finished.Add(-time.Nanosecond)
					case "missed-offer":
						offered--
					case "dropped":
						dropped++
					case "incomplete":
						completed--
					case "unverified":
						verified--
					}
					receipt, err := config.receipt(at, finished, offered, dropped, completed, verified)
					if change != "" {
						require.Error(t, err, "partial scheduling cannot produce joined evidence")
						return
					}
					require.NoError(t, err)
					raw, err := json.Marshal(receipt)
					require.NoError(t, err)
					var decoded map[string]any
					require.NoError(t, json.Unmarshal(raw, &decoded))
					require.Equal(t, mode, decoded["mode"])
					require.Equal(t, mode, decoded["evidenceClass"])
					require.Equal(t, float64(config.Interval.Nanoseconds()), decoded["intervalNanos"])
					require.Equal(t, float64(config.Workers), decoded["workers"])
					for _, field := range []string{"scheduledOffers", "offered", "completed", "verified"} {
						require.Equal(t, float64(planned), decoded[field], field)
					}
					require.Equal(t, float64(0), decoded["dropped"])
					require.Equal(t, start.Format(time.RFC3339Nano), decoded["start"])
					require.Equal(t, start.Add(config.Duration).Format(time.RFC3339Nano), decoded["end"])
					require.Equal(t, finished.Format(time.RFC3339Nano), decoded["completedAt"])
				})
			}
		})
	}
}

func TestTranslationCapacityConnectedBarrierRejectsStaleOrMissedWindows(t *testing.T) {
	now := time.Now().UTC()
	for _, change := range []string{"", "run", "missing", "missed", "unbounded"} {
		t.Run(change, func(t *testing.T) {
			barrier := translationCapacityLoadBarrier{RunID: "current", StartAt: now.Add(time.Second)}
			switch change {
			case "run":
				barrier.RunID = "previous"
			case "missing":
				barrier.StartAt = time.Time{}
			case "missed":
				barrier.StartAt = now.Add(-time.Millisecond)
			case "unbounded":
				barrier.StartAt = now.Add(11 * time.Minute)
			}
			raw, err := json.Marshal(barrier)
			require.NoError(t, err)
			at, err := readTranslationCapacityLoadBarrier(raw, "current", now)
			if change == "" {
				require.NoError(t, err)
				require.True(t, barrier.StartAt.Equal(at))
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestTranslationCapacityCanceledReadWindowCannotPassWithoutSamples(t *testing.T) {
	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("canceled window must not issue a domain read")
	})
	a, b := httptest.NewServer(handler), httptest.NewServer(handler)
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := translationCapacityReader{client: a.Client(), query: translationIntegrationQuery(t, "translation")}
	targets := []translationCapacityReadTarget{{Document: translationCapacityDocument{Namespace: "translation-owned", Name: "source"}}}
	result, err := reader.load(ctx, [2]string{a.URL, b.URL}, time.Now().Add(time.Second), time.Second, 20, 1, targets)
	require.ErrorContains(t, err, "offered read window was interrupted")
	require.Zero(t, result.Completed)
}

func TestTranslationCapacitySharedWatchPreservesVersionAndHistoricalDelivery(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprint(historical), func(t *testing.T) {
			body := "\r\nExact UTF-8: \u00e9.\r\n---\r\n"
			patches := []map[string]string{{"fieldPath": "spec.title", "translation": "Translated"}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connection, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer connection.Close()
				_ = connection.WriteJSON(map[string]any{"id": "translation-integration", "type": "next",
					"payload": map[string]any{"data": map[string]any{"watchTranslations": map[string]any{
						"type": "MODIFIED", "name": "cpd-fixture-000001", "resourceVersion": "durable-cursor", "historical": historical,
						"translation": map[string]any{
							"apiVersion": "i18n.gitstore.dev/v1beta1", "kind": "Translation",
							"metadata": map[string]any{"uid": "stable", "name": "cpd-fixture-000001", "namespace": "translation-owned", "generation": 2, "revision": "main@sha1:" + strings.Repeat("a", 40), "resourceVersion": "source-version"},
							"spec":     map[string]any{"locale": "de-DE", "patches": patches}, "body": body,
						},
					}}}})
			}))
			defer server.Close()
			connection, _, err := websocket.DefaultDialer.Dial(strings.Replace(server.URL, "http", "ws", 1), nil)
			require.NoError(t, err)
			defer connection.Close()
			event, err := readCapacityPushWatch(connection, "translation")
			require.NoError(t, err)
			require.Equal(t, "source-version", event.Metadata.ResourceVersion)
			require.Equal(t, "durable-cursor", event.ResourceVersion)
			require.Equal(t, historical, event.Historical)
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(body))), event.Content.BodyDigest)
			patchBytes, err := json.Marshal(patches)
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(patchBytes)), event.Content.PatchDigest)
		})
	}
}

func TestTranslationCapacityWatchJoinsOnlyExactCurrentReadEvidence(t *testing.T) {
	now := time.Now()
	content := translationCapacityContent{SourceDigest: "authored", BodyDigest: "body", PatchDigest: "patch"}
	document := translationCapacityDocument{Namespace: "translation-owned", Name: "cpd-fixture-000001", translationCapacityContent: content}
	current := translationCapacityObservation{
		translationCapacityContent: content, UID: "source", Revision: "revision", Version: "source-version",
		Generation: 2, ObservedGeneration: 2, Ready: true, VisibleAt: now, ReadyAt: now,
		ReadVisibleAt: now, ReadReadyAt: now,
	}
	for _, change := range []string{"", "historical-delivery", "missing", "uid", "namespace", "version", "generation", "revision", "body", "patch", "cursor", "late", "historical-ready", "not-ready", "wrong-authored-source"} {
		t.Run(change, func(t *testing.T) {
			record := capacityPushWatchRecord{
				uid: current.UID, revision: current.Revision, version: current.Version, generation: 2,
				namespace: document.Namespace, cursor: "durable-cursor", content: content, at: now.Add(time.Millisecond),
			}
			observation := current
			switch change {
			case "historical-delivery":
				record.historical = true
			case "uid":
				record.uid = "other"
			case "namespace":
				record.namespace = "translation-other"
			case "version":
				record.version = "other"
			case "generation":
				record.generation++
			case "revision":
				record.revision = "other"
			case "body":
				record.content.BodyDigest = "other"
			case "patch":
				record.content.PatchDigest = "other"
			case "cursor":
				record.cursor = ""
			case "late":
				record.at = now.Add(time.Minute)
			case "historical-ready":
				observation.Historical = true
			case "not-ready":
				observation.Ready = false
			case "wrong-authored-source":
				observation.SourceDigest = "other"
			}
			watch := &capacityPushWatch{}
			watch.seen[0] = map[string]capacityPushWatchRecord{}
			if change != "missing" {
				watch.seen[0][document.Name] = record
			}
			joined, matched, err := watch.joinTranslation(0, document, observation, now.Add(time.Second))
			require.NoError(t, err)
			if change != "" && change != "historical-delivery" {
				require.False(t, matched)
				require.Empty(t, joined.WatchCursor)
				return
			}
			require.True(t, matched)
			require.Equal(t, "durable-cursor", joined.WatchCursor)
			require.Equal(t, current.Version, joined.WatchVersion)
			require.Equal(t, record.historical, joined.WatchHistorical)
			require.False(t, joined.Historical, "historical source delivery cannot become Ready authority")
			require.Equal(t, record.at, joined.ReadyAt, "convergence includes actual watch delivery")
			require.Equal(t, record.at, joined.VisibleAt, "combined visibility still includes watch delivery")
			require.Equal(t, now, joined.ReadVisibleAt, "a late watch cannot rewrite the independent source read clock")
			require.Equal(t, now, joined.ReadReadyAt, "a late watch cannot rewrite the independent guarded-read clock")
			require.Equal(t, record.at, joined.WatchAt)
		})
	}
}

func TestTranslationCapacityBaselinePagesHashIdentityAndMaterial(t *testing.T) {
	collect := func(change string) (translationCapacityBaseline, error) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			cursor, _ := request.Variables["after"].(string)
			name, uid, body := "p-0001", "uid-1", "exact\r\nbody"
			title, inventory := "Original", 3
			next, end := true, "one"
			if cursor != "" {
				name, uid, next, end = "p-0002", "uid-2", false, "two"
				switch change {
				case "identity":
					uid = "replacement"
				case "body":
					body = "partial"
				case "spec":
					title = "Changed"
				case "resolved":
					inventory++
				case "duplicate-uid":
					uid = "uid-1"
				case "descending-names":
					name = "p-0000"
				case "reused-cursor":
					next, end = true, "one"
				}
			}
			node := map[string]any{
				"apiVersion": "catalog.gitstore.dev/v1beta1", "kind": "Product",
				"metadata": map[string]any{"uid": uid, "name": name, "namespace": "retained", "generation": 1, "revision": "main@sha1:original", "resourceVersion": "version"},
				"spec":     map[string]any{"title": title}, "body": body,
				"status": map[string]any{"resolved": map[string]any{"totalInventory": inventory}},
			}
			edges := []any{map[string]any{"node": node}}
			if change == "empty-progress" && cursor == "one" {
				edges, end = []any{}, "empty"
				next = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"products": map[string]any{
				"edges": edges, "pageInfo": map[string]any{"hasNextPage": next, "endCursor": end},
			}}})
		}))
		defer server.Close()
		reader := translationCapacityReader{client: server.Client(), token: "fixture", baselineQuery: "baseline"}
		return reader.baseline(t.Context(), server.URL, "retained", 1, 4)
	}
	before, err := collect("")
	require.NoError(t, err)
	require.EqualValues(t, 2, before.Products)
	for _, change := range []string{"identity", "body", "spec", "resolved"} {
		after, err := collect(change)
		require.NoError(t, err)
		require.NotEqual(t, before.Digest, after.Digest, change)
	}
	after, err := collect("empty-progress")
	require.NoError(t, err)
	require.Equal(t, before, after, "empty Relay progress must not alter material evidence")
	_, err = collect("descending-names")
	require.NoError(t, err, "opaque Relay pagination does not promise ascending resource names")
	for _, change := range []string{"duplicate-uid", "reused-cursor"} {
		_, err := collect(change)
		require.Error(t, err, change)
	}
}

func TestTranslationCapacityOracleJoinsBothReplicasToAuthoredBytes(t *testing.T) {
	plan, receipt := translationCapacityContractFixture()
	require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt))
	for _, tc := range []struct {
		name   string
		change func(*translationCapacityReceipt)
	}{
		{"missing-replica", func(r *translationCapacityReceipt) { r.Pushes[0].Observations = r.Pushes[0].Observations[:1] }},
		{"repeated-replica", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].Replica = "api-a" }},
		{"different-uid", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].UID = "other" }},
		{"aliased-independent-source", func(r *translationCapacityReceipt) {
			for index := range r.Pushes[1].Observations {
				r.Pushes[1].Observations[index].UID = r.Pushes[0].Observations[0].UID
			}
		}},
		{"old-revision", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].Revision = "old" }},
		{"partial-source", func(r *translationCapacityReceipt) {
			r.Pushes[0].Observations[1].SourceDigest = strings.Repeat("b", 64)
		}},
		{"partial-body", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].BodyDigest = strings.Repeat("b", 64) }},
		{"different-patch", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].PatchDigest = strings.Repeat("b", 64) }},
		{"stale-generation", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].ObservedGeneration-- }},
		{"false-ready", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].Ready = false }},
		{"historical-ready", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].Historical = true }},
		{"missing-watch", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].WatchCursor = "" }},
		{"watch-other-version", func(r *translationCapacityReceipt) { r.Pushes[0].Observations[1].WatchVersion = "other" }},
		{"mutated-target", func(r *translationCapacityReceipt) { r.TargetsAfter = strings.Repeat("b", 64) }},
		{"mutated-baseline", func(r *translationCapacityReceipt) { r.BaselineAfter = strings.Repeat("b", 64) }},
		{"unjoined-completion", func(r *translationCapacityReceipt) { r.Pushes[1].Sequence = r.Pushes[0].Sequence }},
		{"missing-completion", func(r *translationCapacityReceipt) { r.Pushes = r.Pushes[1:] }},
		{"batch-with-one-proof", func(r *translationCapacityReceipt) { r.Pushes[0].Documents = 20 }},
		{"reused-acknowledgement", func(r *translationCapacityReceipt) {
			r.Pushes[1].Commit = r.Pushes[0].Commit
			for i := range r.Pushes[1].Observations {
				r.Pushes[1].Observations[i].Revision = r.Pushes[0].Observations[0].Revision
				r.Pushes[1].Observations[i].LastAppliedRevision = r.Pushes[0].Observations[0].LastAppliedRevision
			}
		}},
		{"early-visibility", func(r *translationCapacityReceipt) {
			r.Pushes[0].Observations[0].VisibleAt = plan.Start.Add(-time.Second)
		}},
		{"late-push", func(r *translationCapacityReceipt) {
			r.Pushes[0].AcknowledgedAt = r.Pushes[0].OfferedAt.Add(31 * time.Second)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, changed := translationCapacityContractFixture()
			tc.change(&changed)
			require.Error(t, verifyTranslationCapacityReceipt(plan, changed))
		})
	}
}

func TestTranslationCapacityEvidenceJoinsIndependentClocks(t *testing.T) {
	fixture := func() (translationCapacityPlan, translationCapacityReceipt) {
		plan, receipt := translationCapacityContractFixture()
		for index := range receipt.Pushes {
			for replica := range receipt.Pushes[index].Observations {
				observed := &receipt.Pushes[index].Observations[replica]
				observed.ReadVisibleAt, observed.ReadReadyAt, observed.WatchAt = observed.VisibleAt, observed.ReadyAt, observed.VisibleAt
			}
		}
		return plan, receipt
	}
	plan, receipt := fixture()
	require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt))
	receipt.Pushes[0].Observations[0].WatchAt = receipt.Pushes[0].OfferedAt
	require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt), "watch delivery may precede client acknowledgement")
	for _, tc := range []struct {
		name   string
		change func(*translationCapacityPush)
	}{
		{"missing-read", func(p *translationCapacityPush) { p.Observations[0].ReadVisibleAt = time.Time{} }},
		{"missing-ready", func(p *translationCapacityPush) { p.Observations[0].ReadReadyAt = time.Time{} }},
		{"missing-watch", func(p *translationCapacityPush) { p.Observations[0].WatchAt = time.Time{} }},
		{"mixed-clock-coverage", func(p *translationCapacityPush) {
			p.Observations[0].ReadVisibleAt, p.Observations[0].ReadReadyAt, p.Observations[0].WatchAt = time.Time{}, time.Time{}, time.Time{}
		}},
		{"read-before-acknowledgement", func(p *translationCapacityPush) {
			p.Observations[0].ReadVisibleAt = p.AcknowledgedAt.Add(-time.Nanosecond)
		}},
		{"ready-before-source", func(p *translationCapacityPush) {
			p.Observations[0].ReadReadyAt = p.Observations[0].ReadVisibleAt.Add(-time.Nanosecond)
		}},
		{"ready-after-combined-boundary", func(p *translationCapacityPush) {
			p.Observations[0].ReadReadyAt = p.Observations[0].ReadyAt.Add(time.Nanosecond)
		}},
		{"watch-before-offer", func(p *translationCapacityPush) {
			p.Observations[0].WatchAt = p.OfferedAt.Add(-time.Nanosecond)
		}},
		{"hidden-watch-delay", func(p *translationCapacityPush) {
			p.Observations[0].WatchAt = p.Observations[0].VisibleAt.Add(time.Nanosecond)
		}},
		{"rewritten-combined-visibility", func(p *translationCapacityPush) {
			p.Observations[0].VisibleAt = p.Observations[0].VisibleAt.Add(time.Nanosecond)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, receipt := fixture()
			tc.change(&receipt.Pushes[0])
			require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
		})
	}
}

func TestTranslationCapacityCallbackFaultCancelsWithoutLeavingInjectionActive(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	t.Setenv("TRANSLATION_TEST_CALLBACK_EVIDENCE_DIR", root)
	t.Setenv("TRANSLATION_TEST_CALLBACK_UPSTREAM", "127.0.0.1:58221")
	cfg := capacityPushConfig{runID: "fault-cancellation", startAt: time.Now().Add(time.Second), duration: 20 * time.Second}
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "ready.json"), map[string]string{
		"runId": cfg.runID, "scope": "owned-callback-fault-proxy", "evidenceClass": "diagnostic", "upstream": "127.0.0.1:58221",
	}))
	ctx, cancel := context.WithCancel(t.Context())
	fault, err := newTranslationCapacityCallbackFault(ctx, cfg, translationCapacityReader{}, translationCapacityDocument{}, "callback-loss", "")
	require.NoError(t, err)
	cancel()
	completed, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	_, err = fault.finish(completed)
	require.ErrorIs(t, err, context.Canceled)
	var selector struct{ RunID, Stage string }
	require.NoError(t, readTranslationRecoveryPrivateJSON(filepath.Join(root, "fault.json"), &selector))
	require.Equal(t, cfg.runID, selector.RunID)
	require.Empty(t, selector.Stage)
}

func TestTranslationCapacityCallbackFaultRejectsAnotherProxyBeforeActivation(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	t.Setenv("TRANSLATION_TEST_CALLBACK_EVIDENCE_DIR", root)
	t.Setenv("TRANSLATION_TEST_CALLBACK_UPSTREAM", "127.0.0.1:58221")
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "ready.json"), map[string]string{
		"runId": "another-run", "scope": "owned-callback-fault-proxy", "evidenceClass": "diagnostic", "upstream": "127.0.0.1:58221",
	}))
	_, err := newTranslationCapacityCallbackFault(t.Context(),
		capacityPushConfig{runID: "current-run", startAt: time.Now().Add(time.Second), duration: 20 * time.Second},
		translationCapacityReader{}, translationCapacityDocument{}, "callback-loss", "")
	require.ErrorContains(t, err, "another run")
	_, err = os.Lstat(filepath.Join(root, "fault.json"))
	require.True(t, os.IsNotExist(err), "another proxy must never be activated or adopted")
}

func TestTranslationCapacityEvidenceRejectsUnattributedOrIncompleteWindows(t *testing.T) {
	plan, _ := translationCapacityContractFixture()
	for _, tc := range []struct {
		name   string
		change func(*translationCapacityReceipt)
	}{
		{"health-only", func(r *translationCapacityReceipt) { r.Scope = "health" }},
		{"helper-only", func(r *translationCapacityReceipt) { r.Scope = "validation-helper" }},
		{"stale-run", func(r *translationCapacityReceipt) { r.RunID = "previous" }},
		{"different-source", func(r *translationCapacityReceipt) { r.SourceDigest = strings.Repeat("b", 64) }},
		{"different-config", func(r *translationCapacityReceipt) { r.ConfigDigest = strings.Repeat("b", 64) }},
		{"different-topology", func(r *translationCapacityReceipt) { r.TopologyDigest = strings.Repeat("b", 64) }},
		{"late-start", func(r *translationCapacityReceipt) { r.Start = r.Start.Add(time.Second) }},
		{"early-stop", func(r *translationCapacityReceipt) { r.End = r.End.Add(-time.Second) }},
		{"dropped-offers", func(r *translationCapacityReceipt) { r.Dropped++ }},
		{"offered-not-completed", func(r *translationCapacityReceipt) { r.Completed-- }},
		{"too-few-reads", func(r *translationCapacityReceipt) { r.Reads-- }},
		{"unmeasured-reads", func(r *translationCapacityReceipt) { r.ReadSamples-- }},
		{"unexpected-errors", func(r *translationCapacityReceipt) { r.UnexpectedErrors = r.Reads / 1000 }},
		{"bad-read-tail", func(r *translationCapacityReceipt) { r.ReadP99 = 3 * time.Second }},
		{"bad-convergence-tail", func(r *translationCapacityReceipt) { r.ConvergenceP99 = 3*time.Second + time.Nanosecond }},
		{"missing-near-limit", func(r *translationCapacityReceipt) { r.NearLimit = 0 }},
		{"missing-negative", func(r *translationCapacityReceipt) { r.ExpectedRejections = 0 }},
		{"too-few-documents", func(r *translationCapacityReceipt) { r.Documents = 999 }},
		{"missing-kind", func(r *translationCapacityReceipt) { r.TargetKinds = r.TargetKinds[:4] }},
		{"insufficient-fanout", func(r *translationCapacityReceipt) { r.MaximumDependents = 999 }},
		{"missing-body-channel", func(r *translationCapacityReceipt) { r.BodyCases = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, receipt := translationCapacityContractFixture()
			tc.change(&receipt)
			require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
		})
	}
	t.Run("production-does-not-inherit-alpha", func(t *testing.T) {
		plan, receipt := translationCapacityContractFixture()
		plan.Mode = "production"
		require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
	})
	t.Run("duplicate-api-identities", func(t *testing.T) {
		plan, receipt := translationCapacityContractFixture()
		plan.APIs[1] = plan.APIs[0]
		require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
	})
}

func TestTranslationCapacityFaultEvidenceDoesNotPromoteSimulations(t *testing.T) {
	plan, receipt := translationCapacityContractFixture()
	plan.Faults = map[string]string{"api-restart": "physical"}
	fault := translationCapacityFault{
		Name: "api-restart", Kind: "physical", At: plan.Start.Add(time.Minute),
		ProcessBefore: "old-api", ProcessAfter: "replacement-api", Restored: true,
		TrafficFrom: plan.Start.Add(time.Minute + time.Second), TrafficUntil: plan.Start.Add(time.Minute + 6*time.Second),
		CorrectReads: 100, ReplayedWithoutResubmission: true,
	}
	fault = translationCapacityFaultSourceFixture(receipt, fault)
	receipt.Faults = []translationCapacityFault{fault}
	receipt.Resources[0] = translationCapacityResourceFixture(plan, plan.APIs[0], fault.ProcessBefore, plan.Start, fault.At)
	receipt.Resources = append(receipt.Resources, translationCapacityResourceFixture(plan, plan.APIs[0], fault.ProcessAfter, fault.At, plan.End))
	require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt))
	for _, change := range []func(*translationCapacityFault){
		func(f *translationCapacityFault) { f.Kind = "simulated" },
		func(f *translationCapacityFault) { f.ProcessAfter = f.ProcessBefore },
		func(f *translationCapacityFault) { f.Restored = false },
		func(f *translationCapacityFault) { f.TrafficUntil = f.TrafficFrom.Add(time.Second) },
		func(f *translationCapacityFault) { f.TrafficUntil = f.At.Add(31 * time.Second) },
		func(f *translationCapacityFault) { f.CorrectReads = 1 },
		func(f *translationCapacityFault) { f.ReplayedWithoutResubmission = false },
	} {
		changed := fault
		change(&changed)
		receipt.Faults = []translationCapacityFault{changed}
		require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
	}
	receipt.Faults = nil
	require.Error(t, verifyTranslationCapacityReceipt(plan, receipt), "unexercised faults cannot disappear")
	plan.Faults["api-restart"] = "simulated"
	fault.Kind, fault.ProcessBefore, fault.ProcessAfter = "simulated", "", ""
	receipt.Faults = []translationCapacityFault{fault}
	_, unchanged := translationCapacityContractFixture()
	receipt.Resources = unchanged.Resources
	require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt), "explicitly simulated evidence stays simulated")
}

func translationCapacityContractFixture() (translationCapacityPlan, translationCapacityReceipt) {
	start := time.Unix(1_800_000_000, 0).UTC()
	digest := strings.Repeat("a", 64)
	plan := translationCapacityPlan{
		RunID: "contract-fixture", Mode: "alpha", SourceDigest: digest, ConfigDigest: digest,
		TopologyDigest: digest, Start: start, End: start.Add(30 * time.Minute),
		APIs: [2]string{"api-a", "api-b"}, Controllers: [2]string{"controller-a", "controller-b"},
		Git: "singleton-git", Workers: 2, PushInterval: 6 * time.Second, ReadRate: 20,
		Expected:         make(map[int]translationCapacityContent),
		ResourceInterval: 5 * time.Second,
		ResourceLimits:   make(map[string]translationCapacityResourceLimit),
	}
	receipt := translationCapacityReceipt{
		RunID: plan.RunID, Scope: "connected-translation", SourceDigest: digest,
		ConfigDigest: digest, TopologyDigest: digest, Start: plan.Start, End: plan.End,
		Offered: 300, Completed: 300, Reads: 36000, Documents: 1000, Namespaces: 10,
		ReadSamples: 36000, ReadP95: time.Millisecond, ReadP99: 2 * time.Millisecond,
		ConvergenceP95: 2 * time.Millisecond, ConvergenceP99: 2 * time.Millisecond,
		Locales: []string{"fr", "de"}, TargetKinds: []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"},
		MaximumDependents: 1000, FieldCases: 1, BodyCases: 1, CombinedCases: 1,
		NearLimit: 1, ExpectedRejections: 1, TargetsBefore: digest, TargetsAfter: digest,
		BaselineBefore: digest, BaselineAfter: digest,
	}
	for _, replica := range []string{plan.APIs[0], plan.APIs[1], plan.Controllers[0], plan.Controllers[1]} {
		plan.ResourceLimits[replica] = translationCapacityResourceLimit{CPUCores: 2, MemoryBytes: 1 << 30}
		receipt.Resources = append(receipt.Resources, translationCapacityResourceFixture(plan, replica, replica+"-process", plan.Start, plan.End))
	}
	for sequence := range 300 {
		at := start.Add(time.Duration(sequence) * plan.PushInterval)
		push := translationCapacityPush{
			Sequence: sequence, OfferedAt: at, AcknowledgedAt: at.Add(time.Millisecond),
			Commit: fmt.Sprintf("%040x", sequence+1), Documents: 1,
		}
		plan.Expected[sequence] = translationCapacityContent{SourceDigest: digest, BodyDigest: digest, PatchDigest: digest}
		for _, api := range plan.APIs {
			push.Observations = append(push.Observations, translationCapacityObservation{
				Replica: api, UID: fmt.Sprintf("translation-%d", sequence), Generation: 1, ObservedGeneration: 1,
				Revision: "main@sha1:" + push.Commit, LastAppliedRevision: "main@sha1:" + push.Commit,
				Version: "1", WatchVersion: "1", WatchCursor: "durable-cursor", Ready: true,
				VisibleAt: at.Add(2 * time.Millisecond), ReadyAt: at.Add(3 * time.Millisecond),
				translationCapacityContent: plan.Expected[sequence],
			})
		}
		receipt.Pushes = append(receipt.Pushes, push)
	}
	return plan, receipt
}

func translationCapacityResourceFixture(plan translationCapacityPlan, replica, process string, start, end time.Time) translationCapacityResourceEpoch {
	epoch := translationCapacityResourceEpoch{Replica: replica, Process: process, Start: start, End: end}
	for at := start; ; at = at.Add(plan.ResourceInterval) {
		if at.After(end) {
			at = end
		}
		epoch.Samples = append(epoch.Samples, translationCapacityResourceSample{
			At: at, CPUCores: .5, ResidentBytes: 64 << 20, RetainedBytes: 32 << 20, Goroutines: 100,
		})
		if at.Equal(end) {
			return epoch
		}
	}
}

func translationCapacityFaultSourceFixture(receipt translationCapacityReceipt, fault translationCapacityFault) translationCapacityFault {
	fault.RestoredAt = fault.TrafficFrom.Add(-time.Second)
	for _, push := range receipt.Pushes {
		if push.AcknowledgedAt.After(fault.At) {
			continue
		}
		fault.Commit = push.Commit
		fault.Observations = append([]translationCapacityObservation(nil), push.Observations...)
	}
	for i := range fault.Observations {
		fault.Observations[i].VisibleAt = fault.RestoredAt
		fault.Observations[i].ReadyAt = fault.RestoredAt
	}
	return fault
}

func TestTranslationCapacityFaultSourcesRequireIndependentRestorationReads(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*translationCapacityFault)
	}{
		{"missing-restoration-time", func(f *translationCapacityFault) { f.RestoredAt = time.Time{} }},
		{"restoration-after-recovery-reads", func(f *translationCapacityFault) { f.RestoredAt = f.TrafficFrom }},
		{"unacknowledged-source", func(f *translationCapacityFault) { f.Commit = fmt.Sprintf("%040x", 999999) }},
		{"missing-api-read", func(f *translationCapacityFault) { f.Observations = f.Observations[:1] }},
		{"reused-api-read", func(f *translationCapacityFault) { f.Observations[1] = f.Observations[0] }},
		{"different-source-lifetime", func(f *translationCapacityFault) { f.Observations[0].UID = "replacement-source" }},
		{"changed-source-content", func(f *translationCapacityFault) { f.Observations[0].BodyDigest = strings.Repeat("b", 64) }},
		{"historical-ready", func(f *translationCapacityFault) { f.Observations[0].Historical = true }},
		{"unreconciled-generation", func(f *translationCapacityFault) { f.Observations[0].ObservedGeneration++ }},
		{"read-after-traffic-start", func(f *translationCapacityFault) { f.Observations[0].ReadyAt = f.TrafficFrom.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, receipt := translationCapacityContractFixture()
			fault := translationCapacityFaultSourceFixture(receipt, translationCapacityFault{
				Name: "callback-loss", Kind: "simulated", At: plan.Start.Add(time.Minute),
				TrafficFrom:  plan.Start.Add(time.Minute + 2*time.Second),
				TrafficUntil: plan.Start.Add(time.Minute + 7*time.Second),
				CorrectReads: 100, Restored: true, ReplayedWithoutResubmission: true,
			})
			plan.Faults = map[string]string{fault.Name: fault.Kind}
			tc.change(&fault)
			receipt.Faults = []translationCapacityFault{fault}
			require.Error(t, verifyTranslationCapacityReceipt(plan, receipt),
				"recovery counts alone cannot prove the acknowledged source survived restoration")
		})
	}
}

func TestTranslationCapacityResourcesRejectIncompleteOrUnboundedEvidence(t *testing.T) {
	t.Run("inclusive-ceilings-and-drift-exclusive-growth-and-cpu", func(t *testing.T) {
		plan, receipt := translationCapacityContractFixture()
		sample := &receipt.Resources[0].Samples[10]
		sample.CPUCores = math.Nextafter(1.6, 0)
		sample.ResidentBytes = plan.ResourceLimits[plan.APIs[0]].MemoryBytes
		sample.RetainedBytes *= 1.099
		sample.Goroutines = 105
		require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt))
		sample.Goroutines = 95
		require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt))
	})
	for _, tc := range []struct {
		name   string
		change func(*translationCapacityPlan, *translationCapacityReceipt)
	}{
		{"missing-resources", func(_ *translationCapacityPlan, r *translationCapacityReceipt) { r.Resources = nil }},
		{"missing-controller", func(_ *translationCapacityPlan, r *translationCapacityReceipt) { r.Resources = r.Resources[:3] }},
		{"repeated-replica", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[1].Replica = r.Resources[0].Replica
		}},
		{"shared-process", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[1].Process = r.Resources[0].Process
		}},
		{"missing-process", func(_ *translationCapacityPlan, r *translationCapacityReceipt) { r.Resources[0].Process = "" }},
		{"undeclared-replica", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Replica = "operator-api"
		}},
		{"missing-limit", func(p *translationCapacityPlan, _ *translationCapacityReceipt) { delete(p.ResourceLimits, p.APIs[0]) }},
		{"unknown-limit", func(p *translationCapacityPlan, _ *translationCapacityReceipt) {
			p.ResourceLimits["other"] = p.ResourceLimits[p.APIs[0]]
		}},
		{"zero-cpu-limit", func(p *translationCapacityPlan, _ *translationCapacityReceipt) {
			p.ResourceLimits[p.APIs[0]] = translationCapacityResourceLimit{MemoryBytes: 1 << 30}
		}},
		{"nonfinite-limit", func(p *translationCapacityPlan, _ *translationCapacityReceipt) {
			p.ResourceLimits[p.APIs[0]] = translationCapacityResourceLimit{CPUCores: 2, MemoryBytes: math.Inf(1)}
		}},
		{"unbounded-cadence", func(p *translationCapacityPlan, _ *translationCapacityReceipt) { p.ResourceInterval = time.Hour }},
		{"missing-cadence", func(p *translationCapacityPlan, _ *translationCapacityReceipt) { p.ResourceInterval = 0 }},
		{"empty-epoch", func(_ *translationCapacityPlan, r *translationCapacityReceipt) { r.Resources[0].Samples = nil }},
		{"late-epoch", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Start = r.Resources[0].Start.Add(time.Second)
		}},
		{"short-epoch", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].End = r.Resources[0].End.Add(-time.Second)
		}},
		{"missing-first-scrape", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples = r.Resources[0].Samples[1:]
		}},
		{"missing-last-scrape", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples = r.Resources[0].Samples[:360]
		}},
		{"interior-scrape-gap", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples = append(r.Resources[0].Samples[:10], r.Resources[0].Samples[11:]...)
		}},
		{"reordered-scrape", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[1], r.Resources[0].Samples[2] = r.Resources[0].Samples[2], r.Resources[0].Samples[1]
		}},
		{"duplicate-scrape", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[1].At = r.Resources[0].Samples[0].At
		}},
		{"cpu-at-80-percent", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].CPUCores = 1.6
		}},
		{"cpu-over-ceiling", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].CPUCores = 3
		}},
		{"memory-over-ceiling", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].ResidentBytes = (1 << 30) + 1
		}},
		{"retained-at-ten-percent", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].RetainedBytes *= 1.1
		}},
		{"transient-goroutine-drift", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].Goroutines = 106
		}},
		{"goroutine-downward-drift", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].Goroutines = 94
		}},
		{"zero-retained-baseline", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[0].RetainedBytes = 0
		}},
		{"zero-goroutine-baseline", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[0].Goroutines = 0
		}},
		{"retained-exceeds-resident", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[0].Samples[10].ResidentBytes = 1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, receipt := translationCapacityContractFixture()
			tc.change(&plan, &receipt)
			require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
		})
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"cpu", "resident", "retained", "goroutines"} {
			t.Run(fmt.Sprintf("%s/%v", field, value), func(t *testing.T) {
				plan, receipt := translationCapacityContractFixture()
				sample := &receipt.Resources[0].Samples[10]
				switch field {
				case "cpu":
					sample.CPUCores = value
				case "resident":
					sample.ResidentBytes = value
				case "retained":
					sample.RetainedBytes = value
				case "goroutines":
					sample.Goroutines = value
				}
				require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
			})
		}
	}
}

func TestTranslationCapacityResourcesCoverReplacementEpochsAndDeclaredGaps(t *testing.T) {
	fixture := func(gap time.Duration) (translationCapacityPlan, translationCapacityReceipt) {
		plan, receipt := translationCapacityContractFixture()
		at := plan.Start.Add(time.Minute)
		receipt.Resources[0] = translationCapacityResourceFixture(plan, plan.APIs[0], "old-api", plan.Start, at)
		receipt.Resources = append(receipt.Resources, translationCapacityResourceFixture(plan, plan.APIs[0], "new-api", at.Add(gap), plan.End))
		plan.Faults = map[string]string{"api-restart": "physical"}
		receipt.Faults = []translationCapacityFault{{
			Name: "api-restart", Kind: "physical", ProcessBefore: "old-api", ProcessAfter: "new-api",
			At: at, TrafficFrom: at.Add(gap + time.Second), TrafficUntil: at.Add(gap + 6*time.Second),
			CorrectReads: 100, Restored: true, ReplayedWithoutResubmission: true,
		}}
		receipt.Faults[0] = translationCapacityFaultSourceFixture(receipt, receipt.Faults[0])
		if gap > 0 {
			plan.ResourceGaps = []translationCapacityResourceGap{{Replica: plan.APIs[0], Fault: "api-restart", Start: at, End: at.Add(gap)}}
		}
		return plan, receipt
	}
	for _, gap := range []time.Duration{0, 5 * time.Second} {
		plan, receipt := fixture(gap)
		last := len(receipt.Resources) - 1
		receipt.Resources[last].Samples[0].RetainedBytes = 16 << 20
		for i := range receipt.Resources[last].Samples {
			receipt.Resources[last].Samples[i].RetainedBytes = 16 << 20
			receipt.Resources[last].Samples[i].Goroutines = 200
		}
		require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt), "replacement baselines are per process, not across restarts")
	}
	t.Run("controller-replacement-with-unchanged-peers", func(t *testing.T) {
		plan, receipt := translationCapacityContractFixture()
		at := plan.Start.Add(time.Minute)
		plan.Faults = map[string]string{"controller-restart": "physical"}
		receipt.Resources[2] = translationCapacityResourceFixture(plan, plan.Controllers[0], "old-controller", plan.Start, at)
		receipt.Resources = append(receipt.Resources, translationCapacityResourceFixture(plan, plan.Controllers[0], "new-controller", at, plan.End))
		receipt.Faults = []translationCapacityFault{{
			Name: "controller-restart", Kind: "physical", ProcessBefore: "old-controller", ProcessAfter: "new-controller",
			At: at, TrafficFrom: at.Add(time.Second), TrafficUntil: at.Add(6 * time.Second),
			CorrectReads: 100, Restored: true, ReplayedWithoutResubmission: true,
		}}
		receipt.Faults[0] = translationCapacityFaultSourceFixture(receipt, receipt.Faults[0])
		require.NoError(t, verifyTranslationCapacityReceipt(plan, receipt))
		receipt.Faults[0].Name = "api-restart"
		plan.Faults = map[string]string{"api-restart": "physical"}
		require.Error(t, verifyTranslationCapacityReceipt(plan, receipt), "fault role must match the replaced replica")
	})
	for _, tc := range []struct {
		name   string
		change func(*translationCapacityPlan, *translationCapacityReceipt)
	}{
		{"undeclared-gap", func(p *translationCapacityPlan, _ *translationCapacityReceipt) { p.ResourceGaps = nil }},
		{"wrong-gap-replica", func(p *translationCapacityPlan, _ *translationCapacityReceipt) { p.ResourceGaps[0].Replica = p.APIs[1] }},
		{"wrong-gap-boundary", func(p *translationCapacityPlan, _ *translationCapacityReceipt) {
			p.ResourceGaps[0].End = p.ResourceGaps[0].End.Add(time.Second)
		}},
		{"duplicate-gap", func(p *translationCapacityPlan, _ *translationCapacityReceipt) {
			p.ResourceGaps = append(p.ResourceGaps, p.ResourceGaps[0])
		}},
		{"unknown-gap-fault", func(p *translationCapacityPlan, _ *translationCapacityReceipt) { p.ResourceGaps[0].Fault = "unknown" }},
		{"simulated-gap", func(p *translationCapacityPlan, r *translationCapacityReceipt) {
			p.Faults["api-restart"] = "simulated"
			r.Faults[0].Kind = "simulated"
		}},
		{"wrong-fault-process", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Faults[0].ProcessAfter = "unobserved-api"
		}},
		{"replacement-starts-after-restoration", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Faults[0].RestoredAt = r.Faults[0].At
		}},
		{"overlapping-epochs", func(p *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[4] = translationCapacityResourceFixture(*p, p.APIs[0], "new-api", p.Start.Add(59*time.Second), p.End)
		}},
		{"reused-process-epoch", func(_ *translationCapacityPlan, r *translationCapacityReceipt) { r.Resources[4].Process = "old-api" }},
		{"replacement-missing-endpoint", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[4].Samples = r.Resources[4].Samples[1:]
		}},
		{"replacement-memory-growth", func(_ *translationCapacityPlan, r *translationCapacityReceipt) {
			r.Resources[4].Samples[1].RetainedBytes *= 1.2
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, receipt := fixture(5 * time.Second)
			tc.change(&plan, &receipt)
			require.Error(t, verifyTranslationCapacityReceipt(plan, receipt))
		})
	}
}
