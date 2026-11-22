// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRBACCapacityPropagationUsesTheOriginalAcknowledgementAndBothReplicas(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			var calls [2]atomic.Int64
			acknowledgedAt := time.Now().Add(-time.Second)
			latencies, err := observeRBACCapacityPropagation(t.Context(), acknowledgedAt, allowed,
				func(_ context.Context, replica, attempt int) string {
					calls[replica].Add(1)
					if (attempt >= replica) == allowed {
						return "allow"
					}
					return "deny"
				})
			require.NoError(t, err)
			require.EqualValues(t, 1, calls[0].Load())
			require.EqualValues(t, 2, calls[1].Load(), "one converged replica must not hide another replica's stale verdict")
			for _, latency := range latencies {
				require.GreaterOrEqual(t, latency, time.Second, "readback must not restart the successful-push clock")
				require.LessOrEqual(t, latency, 5*time.Second)
			}
		})
	}
}

func TestRBACCapacityRootReadbacksBatchMixedKindsAndKeepErrorsScoped(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			fixture := rbacNativeFixture{token: "owned-readback-test", cleanupContext: t.Context()}
			resources := make([]rbacCapacityAuthoredRootResource, 4)
			for index := range resources {
				kind := "ClusterRole"
				if index%2 != 0 {
					kind = "ClusterRoleBinding"
				}
				resources[index].Manifest = fixture.manifest(kind, fmt.Sprintf("root-%d", index))
			}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var request struct {
					Query     string
					Variables map[string]struct{ Name string }
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.Len(t, request.Variables, 4)
				require.Contains(t, request.Query, "$by0:ClusterRoleBy!")
				require.Contains(t, request.Query, "$by1:ClusterRoleBindingBy!")
				require.Contains(t, request.Query, "resourceNames when { token attribute { attr op value values } }")
				data := make(map[string]any, 4)
				var failures []map[string]any
				for index := range resources {
					alias := fmt.Sprintf("r%d", index)
					name := fmt.Sprintf("root-%d", index)
					require.Equal(t, name, request.Variables[fmt.Sprintf("by%d", index)].Name)
					if removed && index%2 == 0 {
						data[alias] = nil
						failures = append(failures, map[string]any{
							"message": "resource is absent", "path": []string{alias},
							"extensions": map[string]any{"code": "NOT_FOUND"},
						})
						continue
					}
					data[alias] = map[string]any{
						"kind":     resources[index].Manifest["kind"],
						"metadata": map[string]any{"uid": name + "-uid", "name": name, "generation": index + 1},
						"spec":     resources[index].Manifest["spec"],
					}
				}
				slices.Reverse(failures)
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"data": data, "errors": failures}))
			}))
			defer server.Close()
			readbacks := lookupRBACCapacityRootBatch(t, fixture, server.URL, resources)
			require.EqualValues(t, 1, requests.Load(), "all four complete readbacks share one bounded request")
			require.Len(t, readbacks, len(resources))
			for index, readback := range readbacks {
				if removed && index%2 == 0 {
					require.Empty(t, readback.Resource.Metadata.UID)
					require.Len(t, readback.Errors, 1)
					require.Contains(t, string(readback.Errors[0]), fmt.Sprintf(`"r%d"`, index))
					continue
				}
				require.Empty(t, readback.Errors, "another alias's absence must not hide this resource")
				require.Equal(t, fmt.Sprintf("root-%d-uid", index), readback.Resource.Metadata.UID)
				require.EqualValues(t, index+1, readback.Resource.Metadata.Generation)
				spec, err := json.Marshal(resources[index].Manifest["spec"])
				require.NoError(t, err)
				require.JSONEq(t, string(spec), string(readback.Resource.Spec))
			}
		})
	}
}

func TestRBACCapacityAdvancedBaselinePreservesOriginalSeedAndMutationClocks(t *testing.T) {
	for _, kind := range []string{"", "conditionalRole", "generalRole"} {
		t.Run("kind="+kind, func(t *testing.T) {
			plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t)
			if kind != "" {
				plan.MutationSubject = record.Subject
				plan.GeneralRoleMutations = kind == "generalRole"
			}
			config.Plan, config.ResumeSeedDirectory = plan, "/private/original-seed"
			name := plan.namespaceName(1)
			original := seeds[name]
			baseline := rbacCapacitySeedBaseline{
				SeedGitRevision: original.GitRevision, GitRevision: "main@sha1:" + strings.Repeat("b", 40),
				Generation: 3, AcknowledgedAt: original.Admissions[0].AcknowledgedAt.Add(500 * time.Millisecond),
			}
			config.ResumeBaselines = map[string]rbacCapacitySeedBaseline{name: baseline}
			require.NoError(t, applyRBACCapacityResumeBaselines(config, seeds))
			require.Equal(t, original.Admissions, seeds[name].Admissions, "an advanced baseline must not fabricate another original import")
			require.Equal(t, original.NamespaceNodeUID, seeds[name].NamespaceNodeUID)
			require.Equal(t, original.Roles, seeds[name].Roles)
			require.Equal(t, original.Bindings, seeds[name].Bindings)
			for _, admissions := range [][]rbacCapacityMutationAdmission{record.Admissions, record.Restorations} {
				for index := range admissions {
					if admissions[index].Namespace == name {
						admissions[index].Generation += 2
					}
				}
			}
			require.NoError(t, checkRBACCapacityMutationReceipt(plan, config.Count, config.K6Workload, seeds, record, window))
			history := rbacCapacityMutationHistory(t, plan, config.Count, config.K6Workload, seeds, record, window)
			require.Equal(t, baseline.GitRevision, history[0].Receipt.GitRevision)
			require.Equal(t, baseline.AcknowledgedAt, history[0].AcknowledgedAt)
			require.Equal(t, plan.mutationGenerations(3), history[0].Generations)
			require.Equal(t, record.Admissions[0].AcknowledgedAt, history[1].AcknowledgedAt,
				"continuation must retain each new mutation's actual original acknowledgement")
			namespace := plan.namespace(1)
			var resources []json.RawMessage
			for _, manifest := range namespace.Resources {
				encoded, err := json.Marshal(manifest)
				require.NoError(t, err)
				var source map[string]any
				require.NoError(t, json.Unmarshal(encoded, &source))
				metadata := source["metadata"].(map[string]any)
				name := metadata["name"].(string)
				uid, generation := original.Roles[name], int64(1)
				if pinned := plan.mutationGenerations(3)[source["kind"].(string)+"/"+name]; pinned != 0 {
					generation = pinned
				}
				if source["kind"] == "RoleBinding" {
					uid = original.Bindings[name]
					ref := source["spec"].(map[string]any)["roleRef"].(map[string]any)
					status := map[string]any{"observedGeneration": generation}
					if ref["name"] != "capacity-unresolved" {
						roleUID := original.Roles[ref["name"].(string)]
						if ref["kind"] == "ClusterRole" {
							roleUID = "builtin-view"
						}
						status["resolved"] = map[string]any{"roleRef": map[string]any{
							"kind": ref["kind"], "name": ref["name"], "uid": roleUID, "observedGeneration": 1,
						}}
					}
					source["status"] = status
				}
				metadata["uid"], metadata["generation"] = uid, generation
				encoded, err = json.Marshal(source)
				require.NoError(t, err)
				resources = append(resources, encoded)
			}
			origin, err := json.Marshal(map[string]any{
				"namespace": name, "namespaceUID": "tier-1", "gitRevision": baseline.GitRevision,
			})
			require.NoError(t, err)
			state, err := json.Marshal(map[string]any{
				"tierID": "tier-1", "roleCount": 10, "bindingCount": 50,
				"projection": map[string]any{"gitRevision": baseline.GitRevision},
			})
			require.NoError(t, err)
			header := rbacCapacitySourceHeader{Origin: origin, State: state}
			require.True(t, checkRBACCapacityNamespaceSource(t, header, resources, namespace, seeds[name], nil, nil))
			_, _, target := plan.mutationTarget()
			var stale map[string]any
			require.NoError(t, json.Unmarshal(resources[target], &stale))
			stale["metadata"].(map[string]any)["generation"] = 1
			if status, ok := stale["status"].(map[string]any); ok {
				status["observedGeneration"] = 1
			}
			resources[target], err = json.Marshal(stale)
			require.NoError(t, err)
			require.False(t, checkRBACCapacityNamespaceSource(t, header, resources, namespace, seeds[name], nil, nil),
				"even a static retained source must match the independently declared advanced generation")
			record.Admissions[0].Generation = 2
			require.Error(t, checkRBACCapacityMutationReceipt(plan, config.Count, config.K6Workload, seeds, record, window))
		})
	}
}

func TestRBACCapacityAdvancedBaselineRejectsUnownedOrUnprovenChangesAtomically(t *testing.T) {
	plan, config, seeds, record, _ := makeRBACCapacityMutationJournal(t)
	plan.MutationSubject = record.Subject
	config.Plan, config.ResumeSeedDirectory = plan, "/private/original-seed"
	name := plan.namespaceName(1)
	baseline := rbacCapacitySeedBaseline{
		SeedGitRevision: seeds[name].GitRevision, GitRevision: "main@sha1:" + strings.Repeat("b", 40),
		Generation: 3, AcknowledgedAt: seeds[name].Admissions[0].AcknowledgedAt.Add(500 * time.Millisecond),
	}
	for _, mutation := range []string{"personal", "parent", "same-ref", "invalid-ref", "generation", "overflow", "old-clock", "future-clock", "alpha", "fresh"} {
		t.Run(mutation, func(t *testing.T) {
			candidate, declared, key := config, baseline, name
			switch mutation {
			case "personal":
				key = plan.namespaceName(0)
			case "parent":
				declared.SeedGitRevision = "main@sha1:" + strings.Repeat("c", 40)
			case "same-ref":
				declared.GitRevision = declared.SeedGitRevision
			case "invalid-ref":
				declared.GitRevision = "main"
			case "generation":
				declared.Generation = 1
			case "overflow":
				declared.Generation = math.MaxInt64
			case "old-clock":
				declared.AcknowledgedAt = seeds[name].Admissions[0].AcknowledgedAt
			case "future-clock":
				declared.AcknowledgedAt = time.Now().Add(time.Hour)
			case "alpha":
				candidate.Mode = "alpha"
			case "fresh":
				candidate.ResumeSeedDirectory = ""
			}
			candidate.ResumeBaselines = map[string]rbacCapacitySeedBaseline{
				plan.namespaceName(2): {
					SeedGitRevision: seeds[plan.namespaceName(2)].GitRevision,
					GitRevision:     "main@sha1:" + strings.Repeat("d", 40),
					Generation:      3, AcknowledgedAt: baseline.AcknowledgedAt,
				},
				key: declared,
			}
			receipts := maps.Clone(seeds)
			require.Error(t, applyRBACCapacityResumeBaselines(candidate, receipts))
			require.Equal(t, seeds, receipts, "a rejected partial declaration must not advance any other Namespace")
		})
	}
}

func TestRBACCapacityMutationRejectsNonDisjointOrUnboundedCompanions(t *testing.T) {
	plan := rbacCapacitySeedPlan{Owner: "owned-fixture", Prefix: "rbac-mutation", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"}
	config := func() *rbacCapacityK6DiagnosticConfig {
		return &rbacCapacityK6DiagnosticConfig{Duration: 24 * time.Second, Rate: 30,
			Mutation: &rbacCapacityMutationConfig{Subject: plan.Prefix + "-filler-mutation", TokenFile: "private.jwt",
				Schedule: rbacCapacityScheduleConfig{Duration: 12 * time.Second, Drain: 5 * time.Second,
					Period: time.Minute, Rate: 50, Workers: 1, Pending: 1}}}
	}
	require.NoError(t, validateRBACCapacityMutation(plan, 5, config(), "administrator"))
	for _, change := range []string{"ordinary", "owner", "group", "admin", "membership", "credential", "writers", "queue", "period", "rate", "partial-cycle", "no-overlap"} {
		t.Run(change, func(t *testing.T) {
			candidate := config()
			admin := "administrator"
			switch change {
			case "ordinary":
				candidate.Mutation.Subject = plan.Subjects[0]
			case "owner":
				candidate.Mutation.Subject = plan.PersonalSubject
			case "group":
				candidate.Mutation.Subject = plan.Group
			case "admin":
				admin = candidate.Mutation.Subject
			case "membership":
				candidate.GroupMembers = []string{candidate.Mutation.Subject}
			case "credential":
				candidate.Mutation.TokenFile = ""
			case "writers":
				candidate.Mutation.Schedule.Workers = 4
			case "queue":
				candidate.Mutation.Schedule.Pending = 2
			case "period":
				candidate.Mutation.Schedule.Period = time.Second
			case "rate":
				candidate.Mutation.Schedule.Rate = 49
			case "partial-cycle":
				candidate.Mutation.Schedule.Duration = 13 * time.Second
			case "no-overlap":
				candidate.Duration = candidate.Mutation.Schedule.Duration
			}
			require.Error(t, validateRBACCapacityMutation(plan, 5, candidate, admin))
		})
	}
	require.Error(t, validateRBACCapacityMutation(plan, 4, config(), "administrator"))
}

func TestRBACCapacityMutationRefusesUnmodeledTransportAndMissingMixedAdmissions(t *testing.T) {
	plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t)
	require.NoError(t, json.Unmarshal([]byte(`{"Transport":"unsupported"}`), config.K6Workload.Mutation))
	require.Error(t, validateRBACCapacityMutation(plan, 5, config.K6Workload, window.Root.AdminSubject))
	require.NoError(t, json.Unmarshal([]byte(`{"Transport":"mixed"}`), config.K6Workload.Mutation))
	require.NoError(t, validateRBACCapacityMutation(plan, 5, config.K6Workload, window.Root.AdminSubject))
	require.Error(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window),
		"Git-only receipts must not silently certify the selected GraphQL/native-Git mix")
	transports := map[string]map[string]bool{}
	for index := range record.Admissions {
		admission := &record.Admissions[index]
		admission.Transport = config.K6Workload.Mutation.transport(admission.Offer)
		if transports[admission.Namespace] == nil {
			transports[admission.Namespace] = map[string]bool{}
		}
		transports[admission.Namespace][admission.Transport] = true
	}
	for _, observed := range transports {
		require.Equal(t, map[string]bool{"git": true, "graphql": true}, observed,
			"neither transport may be permanently pinned to only part of the four-tier working set")
	}
	require.NoError(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
	record.Admissions[1].Transport = "git"
	require.Error(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
}

func TestRBACCapacityGraphQLMutationIsSingleAttemptAndRequiresExactAcknowledgement(t *testing.T) {
	for _, kind := range []string{"Role", "RoleBinding"} {
		t.Run(kind, func(t *testing.T) {
			for _, failure := range []string{"", "unavailable", "errors", "malformed", "missing", "uid", "generation", "oversized"} {
				t.Run(failure, func(t *testing.T) {
					var calls atomic.Int64
					server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						calls.Add(1)
						require.Equal(t, "/admin/graphql", request.URL.Path)
						require.Equal(t, http.MethodPost, request.Method)
						require.Equal(t, "Bearer ordinary-test-token", request.Header.Get("Authorization"))
						require.Equal(t, "owned-admission-1", request.Header.Get("X-Request-ID"))
						var payload struct {
							Query     string
							Variables map[string]map[string]any
						}
						require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
						require.Contains(t, payload.Query, "Update"+kind+"Input!")
						require.Equal(t, "original-cas", payload.Variables["input"]["metadata"].(map[string]any)["resourceVersion"])
						uid, generation := "owned-node-uid", 7
						switch failure {
						case "unavailable":
							writer.WriteHeader(http.StatusServiceUnavailable)
							return
						case "errors":
							_, _ = io.WriteString(writer, `{"data":{},"errors":[{"message":"refused"}]}`)
							return
						case "malformed":
							_, _ = io.WriteString(writer, `{"data":`)
							return
						case "missing":
							_, _ = io.WriteString(writer, `{"data":{}}`)
							return
						case "uid":
							uid = "foreign-node-uid"
						case "generation":
							generation++
						case "oversized":
							_, _ = io.WriteString(writer, strings.Repeat(" ", (1<<20)+1))
							return
						}
						require.NoError(t, json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{
							"update" + kind: map[string]any{rbacNativeField(kind): map[string]any{
								"metadata": map[string]any{"uid": uid, "generation": generation},
							}},
						}}))
					}))
					defer server.Close()
					client := server.Client()
					client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
					acknowledged, err := submitRBACCapacityGraphQLMutation(t.Context(), client, server.URL,
						"ordinary-test-token", "owned-admission-1", kind, map[string]any{
							"metadata": map[string]any{"resourceVersion": "original-cas"},
						}, "owned-node-uid", 7)
					if failure == "" {
						require.NoError(t, err)
						require.False(t, acknowledged.IsZero())
					} else {
						require.Error(t, err)
					}
					require.EqualValues(t, 1, calls.Load(), "uncertain admissions must never be resubmitted")
				})
			}
		})
	}
}

func TestRBACCapacityReadProbeRetainsOnlyClassifiedFailureDetails(t *testing.T) {
	const private = "private-response-content"
	for _, test := range []struct {
		name, body, outcome, code string
		status                    int
	}{
		{"http", private, "unavailable", "HTTP_STATUS", http.StatusServiceUnavailable},
		{"authorization", `{"errors":[{"message":"` + private + `","extensions":{"code":"AUTHORIZATION_UNAVAILABLE"}}]}`,
			"unavailable", "AUTHORIZATION_UNAVAILABLE", http.StatusOK},
		{"directory", `{"errors":[{"message":"` + private + `","extensions":{"code":"DIRECTORY_UNAVAILABLE"}}]}`,
			"unavailable", "DIRECTORY_UNAVAILABLE", http.StatusOK},
		{"denial", `{"data":{"products":null},"errors":[{"message":"` + private + `","extensions":{"code":"FORBIDDEN"}}]}`,
			"deny", "FORBIDDEN", http.StatusOK},
		{"unknown", `{"errors":[{"message":"` + private + `","extensions":{"code":"` + private + `"}}]}`,
			"wrong", "UNCLASSIFIED", http.StatusOK},
		{"mixed", `{"errors":[{"extensions":{"code":"FORBIDDEN"}},{"extensions":{"code":"` + private + `"}}]}`,
			"wrong", "MULTIPLE", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			observation := inspectRBACCapacityRequest(t.Context(), server.Client(), server.URL,
				"ordinary-test-token", rbacCapacityRequest{
					ID: "owned-baseline", Namespace: "owned", Action: "product.list", Expected: "deny",
				})
			require.Equal(t, test.outcome, observation.Outcome)
			require.Equal(t, test.status, observation.HTTPStatus)
			require.Equal(t, test.code, observation.Code)
			encoded, err := json.Marshal(observation)
			require.NoError(t, err)
			require.NotContains(t, string(encoded), private)
			require.EqualValues(t, 1, calls.Load(), "probe diagnostics must not hide a failed request behind retries")
		})
	}
}

func TestRBACCapacityMutationRetainsOnlyClassifiedGraphQLFailures(t *testing.T) {
	const private = "private-response-content"
	for _, test := range []struct {
		name, body, code string
		status           int
	}{
		{"unavailable", private, "HTTP_STATUS", http.StatusServiceUnavailable},
		{"malformed", `{"data":` + private, "INVALID_JSON", http.StatusOK},
		{"conflict", `{"errors":[{"message":"` + private + `","extensions":{"code":"CONFLICT","detail":"` + private + `"}}]}`, "CONFLICT", http.StatusOK},
		{"authz", `{"errors":[{"message":"` + private + `","extensions":{"code":"AUTHORIZATION_UNAVAILABLE"}}]}`, "AUTHORIZATION_UNAVAILABLE", http.StatusOK},
		{"rejected", `{"errors":[{"message":"` + private + `","extensions":{"code":"ADMISSION_REJECTED","diagnostics":[{"message":"` + private + `"}],"commit":"` + private + `"}}]}`, "ADMISSION_REJECTED", http.StatusOK},
		{"already-exists", `{"errors":[{"message":"` + private + `","extensions":{"code":"ALREADY_EXISTS"}}]}`, "ALREADY_EXISTS", http.StatusOK},
		{"precondition", `{"errors":[{"message":"` + private + `","extensions":{"code":"FAILED_PRECONDITION","diagnostics":[{"reason":"` + private + `"}]}}]}`, "FAILED_PRECONDITION", http.StatusOK},
		{"unclassified", `{"errors":[{"message":"` + private + `","extensions":{"code":"` + private + `"}}]}`, "UNCLASSIFIED", http.StatusOK},
		{"non-string", `{"errors":[{"message":"` + private + `","extensions":{"code":{"value":"` + private + `"}}}]}`, "UNCLASSIFIED", http.StatusOK},
		{"multiple", `{"errors":[{"extensions":{"code":"CONFLICT"}},{"message":"` + private + `","extensions":{"code":"FORBIDDEN"}}]}`, "MULTIPLE", http.StatusOK},
		{"matching", `{"errors":[{"extensions":{"code":"CONFLICT"}},{"message":"` + private + `","extensions":{"code":"CONFLICT"}}]}`, "CONFLICT", http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			config := rbacCapacityScheduleConfig{Duration: time.Second, Drain: time.Second, Rate: 2, Workers: 1, Pending: 1}
			_, failures, err := runRBACCapacityMutationSchedule(t.Context(), config,
				func(ctx context.Context, _ int64) (string, func(context.Context) error, error) {
					acknowledged, err := submitRBACCapacityGraphQLMutation(ctx, server.Client(), server.URL,
						"private-request-token", "owned-admission-1", "Role", map[string]any{}, "owned-node-uid", 7)
					require.Zero(t, acknowledged, "a refusal must not invent an admission clock")
					return "", nil, fmt.Errorf("admission: %w", err)
				})
			require.Error(t, err)
			require.EqualValues(t, 1, calls.Load(), "recording a refusal must never replay it")
			require.Len(t, failures, 1)
			require.Equal(t, "single-attempt-admission-failed", failures[0].Kind)
			require.Equal(t, &rbacCapacityGraphQLAdmissionError{HTTPStatus: test.status, Code: test.code}, failures[0].GraphQL)
			content, err := json.Marshal(failures)
			require.NoError(t, err)
			require.NotContains(t, string(content), private)
			require.NotContains(t, string(content), "private-request-token")
			require.NotContains(t, failures[0].GraphQL.Error(), private)
		})
	}
}

func TestRBACCapacityMutationCanonicalReadDoesNotSynchronizeWorkingClone(t *testing.T) {
	sha := strings.Repeat("a", 40)
	revision, err := readRBACCapacityMutationRevision(func(args ...string) ([]byte, error) {
		require.Equal(t, []string{"ls-remote", "--exit-code", "origin", "refs/heads/main"}, args,
			"an independent acknowledged-ref read must not fetch objects or update the working clone")
		return []byte(sha + "\trefs/heads/main\n"), nil
	})
	require.NoError(t, err)
	require.Equal(t, "main@sha1:"+sha, revision)
}

func TestRBACCapacityMutationCanonicalReadRejectsUnverifiableRefs(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, response := range []string{"", sha + "\tHEAD\n", sha + "\trefs/heads/main\n" + sha + "\tHEAD\n",
		"not-a-commit\trefs/heads/main\n", strings.ToUpper(sha) + "\trefs/heads/main\n"} {
		t.Run(response, func(t *testing.T) {
			revision, err := readRBACCapacityMutationRevision(func(...string) ([]byte, error) {
				return []byte(response), nil
			})
			require.Error(t, err)
			require.Empty(t, revision)
		})
	}
	unavailable := errors.New("canonical reference unavailable")
	revision, err := readRBACCapacityMutationRevision(func(...string) ([]byte, error) {
		return nil, unavailable
	})
	require.ErrorIs(t, err, unavailable)
	require.Empty(t, revision)
}

func TestRBACCapacityMutationFetchRefusesUnknownParentsWithoutAuthoring(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, failure := range []string{"invalid-revision", "fetch", "rev-parse", "changed-ref"} {
		t.Run(failure, func(t *testing.T) {
			revision := "main@sha1:" + sha
			if failure == "invalid-revision" {
				revision = "HEAD"
			}
			var calls []string
			err := fetchRBACCapacityMutationRevision(func(args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == failure {
					return nil, errors.New("Git read unavailable")
				}
				return []byte(strings.Repeat("b", 40)), nil
			}, revision)
			require.Error(t, err)
			switch failure {
			case "invalid-revision":
				require.Empty(t, calls)
			case "fetch":
				require.Equal(t, []string{"fetch"}, calls)
			default:
				require.Equal(t, []string{"fetch", "rev-parse"}, calls)
			}
		})
	}
}

func TestRBACCapacityAcknowledgedRevisionRetriesOnlyReadsWithinOriginalClock(t *testing.T) {
	acknowledgedAt := time.Now()
	transient := errors.New("new origin is not installed yet")
	var calls int
	expected := "main@sha1:" + strings.Repeat("a", 40)
	revision, attempts, err := readRBACCapacityAcknowledgedRevision(t.Context(), acknowledgedAt,
		func(ctx context.Context) (string, error) {
			calls++
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, acknowledgedAt.Add(5*time.Second), deadline,
				"read retries must not reset either the admission or propagation clock")
			if calls == 1 {
				return "", transient
			}
			return expected, nil
		})
	require.NoError(t, err)
	require.Equal(t, expected, revision)
	require.Equal(t, 2, attempts)
	require.Equal(t, 2, calls)
}

func TestRBACCapacityAcknowledgedRevisionRejectsExpiredOrUnverifiableReads(t *testing.T) {
	for _, failure := range []string{"expired", "missing-ack", "cancelled", "cancelled-read", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			acknowledgedAt := time.Now()
			if failure == "expired" {
				acknowledgedAt = acknowledgedAt.Add(-6 * time.Second)
			} else if failure == "missing-ack" {
				acknowledgedAt = time.Time{}
			} else if failure == "cancelled" {
				cancel()
			}
			var calls int
			revision, attempts, err := readRBACCapacityAcknowledgedRevision(ctx, acknowledgedAt,
				func(context.Context) (string, error) {
					calls++
					if failure == "cancelled-read" {
						cancel()
					}
					if failure == "malformed" {
						return "HEAD", nil
					}
					return "main@sha1:" + strings.Repeat("a", 40), nil
				})
			require.Error(t, err)
			require.Empty(t, revision)
			require.Equal(t, calls, attempts)
			if failure == "cancelled-read" || failure == "malformed" {
				require.Equal(t, 1, calls, "invalid or late proof must not become an affirmative revision")
			} else {
				require.Zero(t, calls, "expired or unacknowledged work cannot start another canonical read")
			}
		})
	}
}

func TestRBACCapacityConcurrentObservationsDoNotRequireSupersededIntent(t *testing.T) {
	var calls [2]atomic.Int64
	acknowledgedAt := time.Now()
	var last [2]time.Time
	require.NoError(t, observeRBACCapacityMutationPhase(t.Context(), acknowledgedAt, false, false,
		func(_ context.Context, replica, _ int) string {
			calls[replica].Add(1)
			last[replica] = time.Now()
			return "allow"
		}))
	for replica := range calls {
		require.EqualValues(t, 10, calls[replica].Load())
		require.GreaterOrEqual(t, last[replica].Sub(acknowledgedAt), 4*time.Second,
			"ten probes must cover the original five-second propagation window, not only its first second")
	}
	require.Error(t, observeRBACCapacityMutationPhase(t.Context(), time.Now().Add(-6*time.Second), true, false,
		func(context.Context, int, int) string { return "allow" }),
		"final restoration still requires actual denial within the original five-second bound")
}

func TestRBACCapacityConcurrentObservationsReleaseSlotsForMatchingIntent(t *testing.T) {
	var calls [2]atomic.Int64
	started := time.Now()
	require.NoError(t, observeRBACCapacityMutationPhase(t.Context(), started, false, true,
		func(_ context.Context, replica, _ int) string {
			calls[replica].Add(1)
			return "allow"
		}))
	require.Less(t, time.Since(started), 2*time.Second,
		"matching intent must not occupy all four observers until the next 50/min admission")
	for replica := range calls {
		require.EqualValues(t, 10, calls[replica].Load())
	}
}

func TestRBACCapacityUncapturedAdmissionsStillSupersedeOriginalSources(t *testing.T) {
	at := time.Unix(1000, 0)
	oracle := newRBACCapacityOracle(5 * time.Second)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "root", GitRevision: "root-baseline", Digest: "verified-root"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "namespace", GitRevision: "baseline", Digest: "verified-baseline"},
	}
	require.NoError(t, oracle.admitNamespace("namespace", "baseline", at))
	require.NoError(t, oracle.admitNamespace("namespace", "superseded-before-capture", at.Add(time.Second)))
	require.NoError(t, oracle.admitNamespace("namespace", "restored", at.Add(2*time.Second)))
	require.NoError(t, oracle.commit(vector, at, at, map[string]bool{"holdout/product.list": false}))
	require.NoError(t, oracle.check(vector, "holdout/product.list", false, at.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "holdout/product.list", false, at.Add(6*time.Second+time.Nanosecond)),
		"an unseen intermediate source cannot refresh or erase the baseline supersession clock")
	require.Error(t, oracle.admitNamespace("namespace", "superseded-before-capture", at.Add(3*time.Second)))
	unseen := vector
	unseen.Namespace.GitRevision = "superseded-before-capture"
	require.Error(t, oracle.check(unseen, "holdout/product.list", true, at.Add(time.Second)),
		"admitting a clock must never invent a verified source vector")
}

func TestRBACCapacityOracleAdmitsCompleteBoundedMutationHistory(t *testing.T) {
	at := time.Unix(1000, 0)
	oracle := newRBACCapacityOracle(5 * time.Second)
	for index := range 1508 {
		require.NoError(t, oracle.admitNamespace("namespace", fmt.Sprintf("revision-%d", index),
			at.Add(time.Duration(index)*time.Second)),
			"the oracle must retain up to 1,500 mutations plus baseline and restoration admissions")
	}
	require.NoError(t, oracle.admitNamespace("namespace", "revision-0", at),
		"a source alias at the bound must retain its original admission clock")
	require.Error(t, oracle.admitNamespace("namespace", "revision-0", at.Add(time.Second)))
	require.Error(t, oracle.admitNamespace("namespace", "beyond-bound", at.Add(1508*time.Second)),
		"the complete workload history must remain bounded")
}

func TestRBACCapacityMutationObserversDrainBeyondTheSchedulerBoundary(t *testing.T) {
	var observations atomic.Int64
	config := rbacCapacityScheduleConfig{Duration: 20 * time.Millisecond, Drain: time.Second, Rate: 50, Workers: 1, Pending: 1}
	accounting, failures, err := runRBACCapacityMutationSchedule(t.Context(), config,
		func(workCtx context.Context, offer int64) (string, func(context.Context) error, error) {
			return "allow", func(observerCtx context.Context) error {
				select {
				case <-workCtx.Done():
				case <-observerCtx.Done():
					return errors.New("observer cancelled before the scheduler boundary")
				}
				if err := observerCtx.Err(); err != nil {
					return err
				}
				observations.Add(1)
				return nil
			}, nil
		})
	require.NoError(t, err)
	require.Empty(t, failures)
	require.NoError(t, accounting.validate(1, false))
	require.EqualValues(t, 1, observations.Load(), "joined observers must retain their independent drain context")
}

func TestRBACCapacityMutationCancellationJoinsObserversWithoutAnotherAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var admissions, joined atomic.Int64
	config := rbacCapacityScheduleConfig{Duration: time.Second, Drain: time.Second, Rate: 2, Workers: 1, Pending: 1}
	accounting, failures, err := runRBACCapacityMutationSchedule(ctx, config,
		func(context.Context, int64) (string, func(context.Context) error, error) {
			admissions.Add(1)
			return "allow", func(ctx context.Context) error {
				cancel()
				<-ctx.Done()
				joined.Add(1)
				return ctx.Err()
			}, nil
		})
	require.Error(t, err)
	require.Len(t, failures, 1)
	require.Equal(t, "bounded-http-observation-failed", failures[0].Kind)
	require.EqualValues(t, 1, accounting.Completed)
	require.EqualValues(t, 1, admissions.Load())
	require.EqualValues(t, 1, joined.Load())
}

func TestRBACCapacityMutationObserverSaturationUsesExistingBoundedQueue(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var admissions, active, peak, joined atomic.Int64
			started := make(chan struct{}, 6)
			release := make(chan struct{})
			type result struct {
				accounting rbacCapacityAccounting
				failures   []rbacCapacityMutationFailure
				err        error
			}
			done := make(chan result, 1)
			config := rbacCapacityScheduleConfig{Duration: 1200 * time.Millisecond, Drain: time.Second, Rate: 5, Workers: 1, Pending: 1}
			go func() {
				accounting, failures, err := runRBACCapacityMutationSchedule(ctx, config,
					func(context.Context, int64) (string, func(context.Context) error, error) {
						offer := admissions.Add(1) - 1
						return "allow", func(ctx context.Context) error {
							current := active.Add(1)
							for previous := peak.Load(); current > previous && !peak.CompareAndSwap(previous, current); previous = peak.Load() {
							}
							defer active.Add(-1)
							defer joined.Add(1)
							started <- struct{}{}
							if offer >= 4 {
								return nil
							}
							select {
							case <-release:
								return nil
							case <-ctx.Done():
								return ctx.Err()
							}
						}, nil
					})
				done <- result{accounting, failures, err}
			}()
			for range 4 {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("four bounded observers did not start")
				}
			}
			select {
			case result := <-done:
				t.Fatalf("observer saturation prematurely ended the original schedule: %v", result.err)
			case <-time.After(250 * time.Millisecond):
			}
			require.EqualValues(t, 4, admissions.Load(), "the fifth admission must wait without adding an observer")
			if canceled {
				cancel()
			} else {
				close(release)
			}
			select {
			case result := <-done:
				if canceled {
					require.Error(t, result.err)
					require.EqualValues(t, 4, admissions.Load(), "cancellation must not admit queued writes")
				} else {
					require.NoError(t, result.err)
					require.Empty(t, result.failures)
					require.NoError(t, result.accounting.validate(6, false))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("bounded scheduler did not join its observers")
			}
			require.EqualValues(t, admissions.Load(), joined.Load())
			require.EqualValues(t, 4, peak.Load())
		})
	}
}

func TestRBACCapacityMutationCommandsNeverRetryAnAdmissionFailure(t *testing.T) {
	for _, failing := range []string{"add", "commit", "rev-parse", "push"} {
		t.Run(failing, func(t *testing.T) {
			var calls []string
			revision, err := commitRBACCapacityMutation(func(args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == failing {
					return nil, errors.New("native effect unavailable")
				}
				return []byte(strings.Repeat("a", 40)), nil
			})
			require.Error(t, err)
			require.Empty(t, revision)
			expected := []string{"add", "commit", "rev-parse", "push"}
			for index, command := range expected {
				if command == failing {
					require.Equal(t, expected[:index+1], calls)
				}
			}
		})
	}
	var calls atomic.Int64
	config := rbacCapacityScheduleConfig{Duration: time.Second, Drain: time.Second, Rate: 2, Workers: 1, Pending: 1}
	_, failures, err := runRBACCapacityMutationSchedule(t.Context(), config,
		func(context.Context, int64) (string, func(context.Context) error, error) {
			calls.Add(1)
			return "", nil, errors.New("single native push failed")
		})
	require.Error(t, err)
	require.EqualValues(t, 1, calls.Load())
	require.Len(t, failures, 1)
	require.Equal(t, "single-attempt-admission-failed", failures[0].Kind)
}

func TestRBACCapacityFailedK6StillRestoresWithoutRelabelingTheFailure(t *testing.T) {
	processErr := errors.New("k6 threshold exit 99")
	restoreErr := errors.New("owned restoration failed")
	var calls int
	err := finishRBACCapacityMutation(processErr, func() error {
		calls++
		return nil
	})
	require.Equal(t, 1, calls)
	require.ErrorIs(t, err, processErr)
	err = finishRBACCapacityMutation(processErr, func() error {
		calls++
		return restoreErr
	})
	require.Equal(t, 2, calls)
	require.ErrorIs(t, err, processErr)
	require.ErrorIs(t, err, restoreErr)
}

func TestRBACCapacityMutationRefusesKnownOrdinaryFailuresBeforeProcessExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k6.log")
	done := make(chan struct{})
	require.NoError(t, os.WriteFile(path, []byte("execution: local\n"), 0o600))
	require.NoError(t, checkRBACCapacityOrdinaryWorkload(path, done))
	for _, failure := range []string{
		`level=error msg="{\"type\":\"rbac_failed_response\",\"reason\":\"http_throttled\"}"`,
		`level=warning msg="Insufficient VUs, reached 30 active VUs"`,
		`level=error msg="ordinary executor failed"`,
	} {
		require.NoError(t, os.WriteFile(path, []byte("execution: local\n"+failure+"\n"), 0o600))
		require.Error(t, checkRBACCapacityOrdinaryWorkload(path, done),
			"a live process must not hide an already failed ordinary workload")
	}
	require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{'x'}, (1<<20)+1), 0o600))
	require.Error(t, checkRBACCapacityOrdinaryWorkload(path, done))
	require.NoError(t, os.WriteFile(path, []byte("execution: local\n"), 0o600))
	link := filepath.Join(filepath.Dir(path), "aliased.log")
	require.NoError(t, os.Symlink(path, link))
	require.Error(t, checkRBACCapacityOrdinaryWorkload(link, done))
	close(done)
	require.Error(t, checkRBACCapacityOrdinaryWorkload(path, done))
}

func TestRBACCapacityCleanupWaitsForPinnedProjectionWithoutRetryingWrites(t *testing.T) {
	resource := rbacNativeResource{}
	resource.Metadata.UID, resource.Metadata.Generation = "original-binding", 2
	resource.Status.ObservedGeneration = 2
	resource.Status.Resolved = &struct{ RoleUID string }{RoleUID: "original-role"}
	var reads int
	current, err := waitRBACCapacityMutationProjection(t.Context(), "original-binding", "original-role", 3,
		func(context.Context) (rbacNativeResource, error) {
			reads++
			if reads == 2 {
				resource.Metadata.Generation, resource.Status.ObservedGeneration = 3, 3
			}
			return resource, nil
		})
	require.NoError(t, err)
	require.Equal(t, 2, reads)
	require.EqualValues(t, 3, current.Metadata.Generation)
	for _, changed := range []string{"binding", "role", "later-generation"} {
		t.Run(changed, func(t *testing.T) {
			stale := resource
			switch changed {
			case "binding":
				stale.Metadata.UID = "replacement-binding"
			case "role":
				stale.Status.Resolved = &struct{ RoleUID string }{RoleUID: "replacement-role"}
			case "later-generation":
				stale.Metadata.Generation = 4
			}
			var calls int
			_, err := waitRBACCapacityMutationProjection(t.Context(), "original-binding", "original-role", 3,
				func(context.Context) (rbacNativeResource, error) { calls++; return stale, nil })
			require.Error(t, err)
			require.Equal(t, 1, calls, "unsafe identity or generation changes must refuse immediately")
		})
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	resource.Metadata.Generation, resource.Status.ObservedGeneration = 2, 2
	_, err = waitRBACCapacityMutationProjection(ctx, "original-binding", "original-role", 3,
		func(context.Context) (rbacNativeResource, error) { return resource, nil })
	require.ErrorIs(t, err, context.DeadlineExceeded, "an independently bounded cleanup cannot wait forever for stale projections")
}

func TestRBACCapacityCleanupGuardsEveryEvidenceFailureAndJoinsBeforeRestoring(t *testing.T) {
	for _, stage := range []string{"process-exit", "log-sync", "profile-read", "window-open", "window-encode", "window-sync", "window-close"} {
		t.Run(stage, func(t *testing.T) {
			original := errors.New(stage)
			var calls []string
			cleanup := &rbacCapacityMutationCleanup{timeout: time.Second,
				stop: func(context.Context) error { calls = append(calls, "stopped-and-joined"); return nil },
				restore: func(ctx context.Context) error {
					require.NoError(t, ctx.Err())
					require.Equal(t, []string{"stopped-and-joined"}, calls)
					calls = append(calls, "restored")
					return nil
				},
			}
			run := func() error {
				var failure error
				runRBACCapacityGuardedK6(t.Context(), cleanup, func(err error) { t.Error(err) }, func() { failure = original })
				return failure
			}
			require.ErrorIs(t, run(), original)
			require.NoError(t, cleanup.run(t.Context()), "explicit success must not repeat deferred restoration")
			require.Equal(t, []string{"stopped-and-joined", "restored"}, calls)
		})
	}
}

func TestRBACCapacityWindowPersistenceFailureStillRestoresOnce(t *testing.T) {
	dir := fmt.Sprintf(".rbac-cleanup-evidence-%d", time.Now().UnixNano())
	require.NoError(t, os.Mkdir(dir, 0700))
	defer func() { require.NoError(t, os.RemoveAll(dir)) }()
	window, err := os.OpenFile(filepath.Join(dir, "k6-process.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	require.NoError(t, err)
	require.NoError(t, window.Close())
	var restored, stopped int
	cleanup := &rbacCapacityMutationCleanup{timeout: time.Second,
		stop:    func(context.Context) error { stopped++; return nil },
		restore: func(context.Context) error { require.Equal(t, 1, stopped); restored++; return nil },
	}
	var persistenceErr error
	runRBACCapacityGuardedK6(t.Context(), cleanup, func(err error) { t.Error(err) }, func() {
		persistenceErr = window.Sync()
	})
	require.Error(t, persistenceErr)
	require.NoError(t, cleanup.run(t.Context()))
	require.Equal(t, 1, restored)
}

func TestRBACCapacityCleanupCancellationIsBoundedAndNeverRestoresBeforeJoin(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	var restored bool
	cleanup := &rbacCapacityMutationCleanup{timeout: time.Second,
		stop: func(ctx context.Context) error {
			require.NoError(t, ctx.Err(), "cleanup is separately bounded, not revived load work")
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.LessOrEqual(t, time.Until(deadline), time.Second)
			return nil
		},
		restore: func(ctx context.Context) error { require.NoError(t, ctx.Err()); restored = true; return nil },
	}
	require.NoError(t, cleanup.run(parent))
	require.True(t, restored)
	joinErr := errors.New("k6 still alive")
	cleanup = &rbacCapacityMutationCleanup{timeout: time.Second,
		stop:    func(context.Context) error { return joinErr },
		restore: func(context.Context) error { t.Fatal("restoration overlapped live k6"); return nil },
	}
	require.ErrorIs(t, cleanup.run(parent), joinErr)
}

func TestRBACCapacityCleanupCapturesUnexpectedAssertionExitAndRetainsPins(t *testing.T) {
	require.Error(t, runRBACCapacityCleanupAssertions(func() { runtime.Goexit() }))
	require.NoError(t, runRBACCapacityCleanupAssertions(func() {}))
	var joined, restored bool
	cleanup := &rbacCapacityMutationCleanup{timeout: time.Second,
		stop:    func(context.Context) error { joined = true; return nil },
		restore: func(context.Context) error { restored = joined; return nil },
	}
	require.Error(t, runRBACCapacityCleanupAssertions(func() {
		runRBACCapacityGuardedK6(t.Context(), cleanup, func(err error) { t.Error(err) }, func() { runtime.Goexit() })
	}))
	require.True(t, restored, "an unexpected FailNow-style exit must still stop/join and restore")
	plan, _, seeds, record, _ := makeRBACCapacityMutationJournal(t)
	record.Restorations = nil
	pending := record.Admissions[9]
	pending.AcknowledgedAt = time.Time{}
	record.Admissions = record.Admissions[:9]
	record.Pending = &pending
	pins := rbacCapacityMutationRecoveryPins(record, seeds)
	require.Len(t, pins, 2)
	require.Equal(t, "grant-last-acknowledged", pins[0].State)
	require.Equal(t, "single-attempt-admission-unacknowledged", pins[1].State)
	require.Equal(t, plan.namespaceName(2), pins[1].Admission.Namespace)
	require.Equal(t, seeds[plan.namespaceName(2)].NamespaceNodeUID, pins[1].NamespaceNodeUID)
	require.EqualValues(t, 4, pins[1].Admission.Generation)
	require.EqualValues(t, 1, pins[1].Admission.ReferenceGeneration)
	require.True(t, pins[1].Admission.AcknowledgedAt.IsZero(), "cleanup must not invent a missing acknowledgement")
	pending.AcknowledgedAt = time.Now()
	pending.GitRevision = ""
	pins = rbacCapacityMutationRecoveryPins(record, seeds)
	require.Equal(t, "admission-acknowledged-reference-unverified", pins[1].State,
		"a failed post-commit read must not relabel an acknowledged write as an uncertain admission")
	require.Equal(t, pending.AcknowledgedAt, pins[1].Admission.AcknowledgedAt)
}

func makeRBACCapacityMutationJournal(t *testing.T, durations ...time.Duration) (rbacCapacitySeedPlan, rbacCapacityNativeSeedConfig,
	map[string]rbacCapacitySeedReceipt, rbacCapacityMutationReceipt, rbacCapacityK6ProcessWindow,
) {
	t.Helper()
	plan := rbacCapacitySeedPlan{Owner: "owned-fixture", Prefix: "rbac-rotating", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"}
	duration := 12 * time.Second
	if len(durations) != 0 {
		duration = durations[0]
	}
	offered := rbacCapacityScheduledOffers(duration, 50, time.Minute)
	config := rbacCapacityNativeSeedConfig{Plan: plan, Count: 5, Mode: "diagnostic", InitialAdminSubject: "administrator",
		K6Workload: &rbacCapacityK6DiagnosticConfig{Duration: duration + 12*time.Second, Rate: 30, PreallocatedVUs: 10, MaxVUs: 20,
			Mutation: &rbacCapacityMutationConfig{Subject: plan.Prefix + "-filler-mutation", TokenFile: "private.jwt",
				Schedule: rbacCapacityScheduleConfig{Duration: duration, Drain: 5 * time.Second,
					Period: time.Minute, Rate: 50, Workers: 1, Pending: 1}}}}
	at := time.Unix(1000, 0)
	window := rbacCapacityK6ProcessWindow{Owner: plan.Owner, StartedAt: at.Add(time.Second), CompletedAt: at.Add(duration + 13*time.Second),
		Root: rbacCapacityRootPolicy{NamespaceUID: "root-uid", BindingUID: "root-binding", AdminSubject: "administrator"}}
	var err error
	window.Root.TreeDigest, err = rbacCapacityRootTreeDigest(window.Root.AdminSubject)
	require.NoError(t, err)
	record := rbacCapacityMutationReceipt{Owner: plan.Owner, Subject: config.K6Workload.Mutation.Subject,
		ScheduledAt: at.Add(2 * time.Second), CompletedAt: at.Add(duration + 2*time.Second + time.Millisecond),
		RestoredAt: window.CompletedAt.Add(time.Second), Complete: true,
		Baseline:   [2]map[string]rbacCapacityProbeExpectation{{}, {}},
		Accounting: rbacCapacityAccounting{Offered: offered, Completed: offered}}
	seeds := make(map[string]rbacCapacitySeedReceipt)
	for index := 1; index <= 4; index++ {
		namespace := plan.namespace(index)
		revision := fmt.Sprintf("main@sha1:%040d", index)
		seed := rbacCapacitySeedReceipt{Owner: plan.Owner, Namespace: namespace.Name, Tier: namespace.Tier,
			NamespaceNodeUID: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("gid://GitStore/Namespace/tier-%d", index))),
			GitRevision:      revision, Complete: true, Roles: make(map[string]string), Bindings: make(map[string]string),
			Admissions: []rbacCapacitySeedAdmission{{GitRevision: revision, AcknowledgedAt: at, Resources: 60}}}
		for _, resource := range namespace.Resources {
			name := resource["metadata"].(map[string]any)["name"].(string)
			uids := seed.Roles
			if resource["kind"] == "RoleBinding" {
				uids = seed.Bindings
			}
			uids[name] = fmt.Sprintf("tier-%d-%s", index, name)
		}
		seeds[namespace.Name] = seed
		for replica := range 2 {
			record.Baseline[replica][fmt.Sprintf("%s-mutation-baseline-%d-%d", plan.Owner, index, replica)] =
				rbacCapacityProbeExpectation{Subject: record.Subject, Namespace: namespace.Name, Action: "product.list",
					ObservedAt: at.Add(1500 * time.Millisecond)}
		}
	}
	makeAdmission := func(offer int64, restoring bool) rbacCapacityMutationAdmission {
		index, generation := 1+int(offer)%4, offer/4+2
		dispatched := record.ScheduledAt.Add(time.Duration(offer)*1200*time.Millisecond + time.Millisecond)
		family, allowed, attempts := "mutation", offer/4%2 == 0, 10
		if restoring {
			index = int(offer)
			generation = (offered-int64(index))/4 + 3
			dispatched = window.CompletedAt.Add(time.Duration(offer) * 200 * time.Millisecond)
			family, allowed, attempts = "mutation-restore", false, 1
		}
		name := plan.namespaceName(index)
		seed := seeds[name]
		admission := rbacCapacityMutationAdmission{Offer: offer, Namespace: name, BindingUID: seed.Bindings["capacity-binding-04"],
			RoleUID: seed.Roles["capacity-role-04"], ReferenceGeneration: 1, Generation: generation,
			DispatchedAt: dispatched, AcknowledgedAt: dispatched.Add(50 * time.Millisecond),
			GitRevision: fmt.Sprintf("main@sha1:%040d", 100+offer+int64(len(family))),
			Probes:      [2]map[string]rbacCapacityProbeExpectation{{}, {}}}
		for replica := range 2 {
			for attempt := range attempts {
				admission.Probes[replica][fmt.Sprintf("%s-%s-%d-%d-%02d", plan.Owner, family, offer, replica, attempt)] =
					rbacCapacityProbeExpectation{Subject: record.Subject, Namespace: name, Action: "product.list",
						Allowed: allowed, ObservedAt: admission.AcknowledgedAt.Add(100*time.Millisecond + time.Duration(attempt)*90*time.Millisecond)}
			}
		}
		return admission
	}
	lastAllowed := make(map[int]bool)
	for offer := range offered {
		record.Admissions = append(record.Admissions, makeAdmission(offer, false))
		allowed := offer/4%2 == 0
		lastAllowed[1+int(offer)%4] = allowed
		if allowed {
			record.Accounting.Allowed++
		} else {
			record.Accounting.Denied++
		}
	}
	for index := 1; index <= 4; index++ {
		if lastAllowed[index] {
			record.Restorations = append(record.Restorations, makeAdmission(int64(index), true))
		}
	}
	return plan, config, seeds, record, window
}

func TestRBACCapacityLongMutationJournalRemainsBoundedAndReplayable(t *testing.T) {
	plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t, 30*time.Minute)
	config.K6Workload.Rate = 500
	require.NoError(t, validateRBACCapacityMutationSelection(config))
	require.NoError(t, validateRBACCapacityK6Diagnostic(config.K6Workload))
	require.NoError(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
	directory := fmt.Sprintf(".rbac-long-mutation-%d", time.Now().UnixNano())
	require.NoError(t, os.Mkdir(directory, 0700))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	content := mustRBACCapacityJSON(t, record)
	require.Greater(t, len(content), 1<<20)
	require.Less(t, len(content), 16<<20)
	path := filepath.Join(directory, "mutation-companion.json")
	require.NoError(t, os.WriteFile(path, content, 0600))
	_, err := readRBACCapacityPrivateFixture(path)
	require.Error(t, err, "ordinary credential/config read bounds must remain unchanged")
	workingSetBytes, err := readRBACCapacityPrivateFixtureWithLimit(path, 16<<20)
	require.NoError(t, err)
	require.Equal(t, content, workingSetBytes)
	sealed, history := readRBACCapacityMutation(t, plan, 5, config.K6Workload, directory, seeds, window)
	require.Len(t, sealed.Admissions, 1500)
	index, err := newRBACCapacityMutationTraceIndex(history)
	require.NoError(t, err)
	for _, admission := range record.Admissions {
		changed, err := index.check(admission.Namespace, admission.GitRevision, admission.AcknowledgedAt)
		require.NoError(t, err)
		require.True(t, changed)
	}
	config.K6Workload.Mutation.Schedule.Duration += 12 * time.Second
	require.Error(t, validateRBACCapacityMutationSelection(config), "the declared journal cannot grow beyond 1500 admitted writes")
	config.K6Workload.Mutation.Schedule.Duration -= 12 * time.Second
	config.K6Workload.Rate++
	require.Error(t, validateRBACCapacityK6Diagnostic(config.K6Workload))
	require.NoError(t, os.Truncate(path, 16<<20+1))
	_, err = readRBACCapacityPrivateFixtureWithLimit(path, 16<<20)
	require.Error(t, err, "extended fixture readers must still refuse oversized files before decoding")
}

func TestRBACCapacityK6SummaryUsesTheDeclaredBoundedWindow(t *testing.T) {
	for _, sample := range []struct {
		duration time.Duration
		rate     int
		mutation bool
	}{{30 * time.Second, 100, false}, {36 * time.Second, 500, true}, {1812 * time.Second, 500, true}} {
		t.Run(fmt.Sprintf("%s/%d", sample.duration, sample.rate), func(t *testing.T) {
			config := &rbacCapacityK6DiagnosticConfig{Duration: sample.duration, Rate: sample.rate,
				PreallocatedVUs: 10, MaxVUs: 20}
			if sample.mutation {
				config.Mutation = &rbacCapacityMutationConfig{}
			}
			offered := int64(sample.duration/time.Second) * int64(sample.rate)
			metrics := map[string]map[string]int64{
				"rbac_requests": {"count": offered * 2}, "rbac_completed": {"count": offered * 2},
				"rbac_allowed": {"count": offered * 12 / 10}, "rbac_denied": {"count": offered * 6 / 10},
				"rbac_isolation": {"count": offered * 2 / 10}, "rbac_wrong": {"count": 0}, "rbac_unavailable": {"count": 0},
			}
			for replica := range 2 {
				for _, name := range []string{"rbac_requests", "rbac_completed"} {
					metrics[fmt.Sprintf("%s{replica:%d}", name, replica)] = map[string]int64{"count": offered}
				}
			}
			check := func() error {
				t.Helper()
				actual, err := checkRBACCapacityK6Summary(mustRBACCapacityJSON(t, map[string]any{"metrics": metrics}), config)
				if err == nil {
					require.Equal(t, offered, actual)
				}
				return err
			}
			require.NoError(t, check(), "validated mutation windows must not inherit the short static diagnostic's 3,000-offer ceiling")
			for _, name := range []string{"rbac_requests", "rbac_completed{replica:1}", "rbac_wrong", "rbac_unavailable", "rbac_denied"} {
				metrics[name]["count"]++
				require.Error(t, check(), "larger windows must retain exact per-API counts, outcome mix and zero-error gates")
				metrics[name]["count"]--
			}
			delete(metrics["rbac_requests{replica:0}"], "count")
			require.Error(t, check(), "aggregate counts cannot replace missing direct-API evidence")
			config.Duration = 32 * time.Minute
			require.Error(t, check(), "the extended declaration must still be bounded")
		})
	}
}

func TestRBACCapacityMutationPropagationRequiresEveryOriginalRevisionAndVerdict(t *testing.T) {
	_, _, _, original, _ := makeRBACCapacityMutationJournal(t)
	makeRevisions := func() map[string]string {
		revisions := make(map[string]string)
		for _, admission := range original.Admissions {
			for id := range admission.Probes[0] {
				revisions[id] = admission.GitRevision
			}
		}
		return revisions
	}
	revisions := makeRevisions()
	latencies, err := rbacCapacityMutationPropagation(original, 0, revisions)
	require.NoError(t, err)
	require.Len(t, latencies, len(original.Admissions))
	for _, latency := range latencies {
		require.Equal(t, 100*time.Millisecond, latency)
	}
	for id, probe := range original.Admissions[0].Probes[0] {
		if probe.ObservedAt.Equal(original.Admissions[0].AcknowledgedAt.Add(100 * time.Millisecond)) {
			revisions[id] = "older-revision-with-the-same-verdict"
		}
	}
	latencies, err = rbacCapacityMutationPropagation(original, 0, revisions)
	require.NoError(t, err)
	require.Equal(t, 190*time.Millisecond, latencies[0], "an old same-verdict publication cannot start the propagation clock")

	for _, tamper := range []string{"missing", "same-verdict-other-revision", "wrong-verdict", "before-ack", "after-deadline"} {
		t.Run(tamper, func(t *testing.T) {
			record := original
			record.Admissions = slices.Clone(original.Admissions)
			admission := &record.Admissions[len(record.Admissions)-1]
			admission.Probes[0] = maps.Clone(admission.Probes[0])
			revisions := makeRevisions()
			for id, probe := range admission.Probes[0] {
				switch tamper {
				case "missing":
					delete(revisions, id)
				case "same-verdict-other-revision":
					revisions[id] = original.Admissions[0].GitRevision
				case "wrong-verdict":
					probe.Allowed = !probe.Allowed
				case "before-ack":
					probe.ObservedAt = admission.AcknowledgedAt.Add(-time.Nanosecond)
				case "after-deadline":
					probe.ObservedAt = admission.AcknowledgedAt.Add(5*time.Second + time.Nanosecond)
				}
				admission.Probes[0][id] = probe
			}
			latencies, err := rbacCapacityMutationPropagation(record, 0, revisions)
			require.Error(t, err)
			require.Nil(t, latencies, "never report percentiles over only the fast or observable subset")
		})
	}
}

func TestRBACCapacityMutationProbeReplaySharesOneBoundAcrossTheFullHistory(t *testing.T) {
	_, _, _, record, _ := makeRBACCapacityMutationJournal(t, 30*time.Minute)
	for replica := range 2 {
		probes, err := rbacCapacityMutationProbes(record, replica)
		require.NoError(t, err)
		require.Len(t, probes, 15008, "all 1,500 admissions, both baseline/restoration phases and their original clocks must share one pass")
		for id, expected := range record.Admissions[len(record.Admissions)-1].Probes[replica] {
			require.Equal(t, expected, probes[id], "the last admission must not fall outside an old short-diagnostic bound")
		}
	}
	duplicate := record.Admissions[0]
	duplicate.Probes = [2]map[string]rbacCapacityProbeExpectation{maps.Clone(duplicate.Probes[0]), maps.Clone(duplicate.Probes[1])}
	record.Restorations = append(record.Restorations, duplicate)
	_, err := rbacCapacityMutationProbes(record, 0)
	require.Error(t, err, "different phases cannot overwrite the same request ID during the stream join")
	record.Restorations = record.Restorations[:len(record.Restorations)-1]
	extra := rbacCapacityMutationAdmission{Probes: [2]map[string]rbacCapacityProbeExpectation{{}, {}}}
	for index := range 2000 {
		extra.Probes[0][fmt.Sprintf("extra-probe-%04d", index)] = rbacCapacityProbeExpectation{}
	}
	record.Restorations = append(record.Restorations, extra)
	_, err = rbacCapacityMutationProbes(record, 0)
	require.Error(t, err, "separately bounded phase maps must not multiply the shared per-API probe bound")
}

func TestRBACCapacitySealedMutationJournalPinsEveryTierAndRestoration(t *testing.T) {
	plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t)
	require.NoError(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
	history := rbacCapacityMutationHistory(t, plan, 5, config.K6Workload, seeds, record, window)
	require.Len(t, history, 16)
	var serialized bytes.Buffer
	require.NoError(t, json.NewEncoder(&serialized).Encode(record))
	for _, tamper := range []string{"missing-offer", "namespace", "binding", "role", "reference-generation", "generation",
		"refreshed-ack", "missing-probe", "fabricated-probe", "missing-restoration", "pre-load-restoration",
		"pending-admission", "cleanup-error", "remaining-recovery-pins"} {
		t.Run(tamper, func(t *testing.T) {
			var changed rbacCapacityMutationReceipt
			require.NoError(t, json.Unmarshal(serialized.Bytes(), &changed))
			switch tamper {
			case "missing-offer":
				changed.Admissions = changed.Admissions[1:]
			case "namespace":
				changed.Admissions[4].Namespace = plan.namespaceName(2)
			case "binding":
				changed.Admissions[4].BindingUID = seeds[plan.namespaceName(2)].Bindings["capacity-binding-04"]
			case "role":
				changed.Admissions[4].RoleUID = "fabricated-role"
			case "reference-generation":
				changed.Admissions[4].ReferenceGeneration = changed.Admissions[4].Generation
			case "generation":
				changed.Admissions[8].Generation = 1
			case "refreshed-ack":
				changed.Admissions[0].AcknowledgedAt = changed.Admissions[0].AcknowledgedAt.Add(2 * time.Second)
			case "missing-probe":
				delete(changed.Admissions[4].Probes[1], plan.Owner+"-mutation-4-1-00")
			case "fabricated-probe":
				changed.Admissions[4].Probes[1]["fabricated"] = changed.Admissions[4].Probes[1][plan.Owner+"-mutation-4-1-00"]
			case "missing-restoration":
				changed.Restorations = changed.Restorations[:1]
			case "pre-load-restoration":
				changed.Restorations[0].DispatchedAt = record.CompletedAt
			case "pending-admission":
				changed.Pending = &changed.Admissions[0]
			case "cleanup-error":
				changed.CleanupError = "bounded cleanup failed"
			case "remaining-recovery-pins":
				changed.RecoveryPins = []rbacCapacityMutationRecoveryPin{{Admission: changed.Admissions[0]}}
			}
			require.Error(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, changed, window))
		})
	}
	config.DecisionWorkload = &rbacCapacityDecisionWorkloadConfig{PropagationDiagnostic: true}
	require.Error(t, validateRBACCapacityMutationSelection(config), "selected sequential phases cannot be silently skipped")
}

func TestRBACCapacityMutationScheduleUsesRetainedMonotonicDispatchOffsets(t *testing.T) {
	plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t, 24*time.Second)
	for index := range record.Admissions {
		admission := &record.Admissions[index]
		offset := admission.DispatchedAt.Sub(record.ScheduledAt)
		admission.DispatchedAfter = &offset
	}
	admission := &record.Admissions[17]
	due := time.Duration(17) * 1200 * time.Millisecond
	admission.DispatchedAt = record.ScheduledAt.Add(due - 200*time.Microsecond)
	require.NoError(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window),
		"a small wall-clock correction cannot turn an on-time monotonic offer into an early admission")
	original := *admission.DispatchedAfter
	early := due - time.Nanosecond
	admission.DispatchedAfter = &early
	require.Error(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
	late := due + 1210*time.Millisecond + time.Nanosecond
	admission.DispatchedAfter = &late
	require.Error(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
	admission.DispatchedAfter = nil
	require.Error(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window),
		"older wall-clock-only receipts retain their original stricter verification; do not relabel failed evidence")
	admission.DispatchedAfter = &original
	require.NoError(t, checkRBACCapacityMutationReceipt(plan, 5, config.K6Workload, seeds, record, window))
}

func TestRBACCapacityIndexedMutationClocksPreserveOriginalTierBoundaries(t *testing.T) {
	at := time.Unix(1000, 0)
	history := []rbacCapacityAuthoredAdmission{
		{Namespace: rbacCapacitySeedNamespace{Name: "tier-a"}, Receipt: rbacCapacitySeedReceipt{GitRevision: "seed"}, AcknowledgedAt: at},
		{Namespace: rbacCapacitySeedNamespace{Name: "tier-a"}, Receipt: rbacCapacitySeedReceipt{GitRevision: "grant"}, AcknowledgedAt: at.Add(time.Second)},
		{Namespace: rbacCapacitySeedNamespace{Name: "tier-a"}, Receipt: rbacCapacitySeedReceipt{GitRevision: "revoke"}, AcknowledgedAt: at.Add(6 * time.Second)},
		{Namespace: rbacCapacitySeedNamespace{Name: "tier-b"}, Receipt: rbacCapacitySeedReceipt{GitRevision: "seed"}, AcknowledgedAt: at},
		{Namespace: rbacCapacitySeedNamespace{Name: "tier-b"}, Receipt: rbacCapacitySeedReceipt{GitRevision: "grant"}, AcknowledgedAt: at.Add(3 * time.Second)},
	}
	index, err := newRBACCapacityMutationTraceIndex(history)
	require.NoError(t, err)
	for _, test := range []struct {
		namespace, revision string
		at                  time.Time
		mutation, fails     bool
	}{
		{"tier-a", "grant", at.Add(time.Second - time.Nanosecond), true, true},
		{"tier-a", "grant", at.Add(time.Second), true, false},
		{"tier-b", "grant", at.Add(time.Second), true, true},
		{"tier-a", "seed", at.Add(6 * time.Second), false, false},
		{"tier-a", "seed", at.Add(6*time.Second + time.Nanosecond), false, true},
		{"tier-a", "grant", at.Add(11 * time.Second), true, false},
		{"tier-a", "grant", at.Add(11*time.Second + time.Nanosecond), true, true},
		{"tier-b", "grant", at.Add(20 * time.Second), true, false},
		{"unmodified-tier", "seed", at.Add(20 * time.Second), false, false},
	} {
		mutation, err := index.check(test.namespace, test.revision, test.at)
		require.Equal(t, test.mutation, mutation)
		require.Equal(t, test.fails, err != nil, "%s/%s at %s", test.namespace, test.revision, test.at)
	}
	for _, next := range []rbacCapacityAuthoredAdmission{
		history[1],
		{Namespace: history[0].Namespace, Receipt: rbacCapacitySeedReceipt{GitRevision: "later"}, AcknowledgedAt: at},
		{Namespace: history[0].Namespace, Receipt: rbacCapacitySeedReceipt{GitRevision: "later"}},
	} {
		_, err := newRBACCapacityMutationTraceIndex(append(slices.Clone(history), next))
		require.Error(t, err, "an index must not reinterpret duplicate or nonmonotonic original clocks")
	}
}

func TestRBACCapacityMutationHistoryKeepsAuthoredGrantsIndependent(t *testing.T) {
	plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t)
	history := rbacCapacityMutationHistory(t, plan, 5, config.K6Workload, seeds, record, window)
	var grants []int
	for index, admission := range history {
		if admission.Namespace.Name == plan.namespaceName(1) && admission.Cases[record.Subject+"/product.list"] {
			grants = append(grants, index)
		}
	}
	require.Len(t, grants, 2)
	before := mustRBACCapacityJSON(t, history)
	first := history[grants[0]].Namespace.Resources[14]["spec"].(map[string]any)
	first["subjects"] = []any{map[string]any{"kind": "User", "name": "changed-private-copy"}}
	require.Contains(t, string(mustRBACCapacityJSON(t, history[grants[0]])), "changed-private-copy")
	require.NotContains(t, string(mustRBACCapacityJSON(t, history[grants[1]])), "changed-private-copy")
	first["subjects"] = []any{map[string]any{"kind": "User", "name": record.Subject}}
	require.JSONEq(t, string(before), string(mustRBACCapacityJSON(t, history)),
		"changing one authored grant must not change seed, revoked, other-tier or later-grant truth")
}

func TestRBACCapacityConditionalRoleRecipeIsDisjointAndKeepsOriginalConditions(t *testing.T) {
	plan, config, _, _, _ := makeRBACCapacityMutationJournal(t)
	config.K6Workload.GroupMembers = []string{plan.Subjects[0]}
	ordinary := make([]rbacCapacityRequest, 100)
	for offer := range ordinary {
		var err error
		ordinary[offer], err = plan.request(int64(offer), 5, config.K6Workload.GroupMembers)
		require.NoError(t, err)
	}
	plan.MutationSubject = config.K6Workload.Mutation.Subject
	config.Plan = plan
	require.NoError(t, validateRBACCapacityMutationSelection(config))
	baseline := plan.namespace(1)
	before := mustRBACCapacityJSON(t, baseline)
	granted := plan.mutationNamespace(baseline, plan.MutationSubject, true)
	repeated := plan.mutationNamespace(baseline, plan.MutationSubject, true)
	rule := granted.Resources[4]["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	rule["when"] = []any{map[string]any{"attr": "namespace", "op": "eq", "value": "changed-private-condition"}}
	require.NotContains(t, string(mustRBACCapacityJSON(t, repeated)), "changed-private-condition")
	require.JSONEq(t, string(before), string(mustRBACCapacityJSON(t, baseline)))
	require.JSONEq(t, string(before), string(mustRBACCapacityJSON(t, plan.mutationNamespace(baseline, plan.MutationSubject, false))))
	for offer, expected := range ordinary {
		actual, err := plan.request(int64(offer), 5, config.K6Workload.GroupMembers)
		require.NoError(t, err)
		require.Equal(t, expected, actual, "conditional holdout Role mutations cannot change the ordinary 60/30/10 cohort")
	}
	config.K6Workload.Mutation = nil
	require.Error(t, validateRBACCapacityMutationSelection(config), "a conditional seed cannot silently omit its selected mutation workload")
	plan.MutationSubject = "not-the-independent-holdout"
	require.Error(t, plan.validate())
}

func TestRBACCapacitySealedMutationReplayRequiresEveryActualSourceAndDecision(t *testing.T) {
	for _, duration := range []time.Duration{12 * time.Second, 24 * time.Second} {
		for _, kind := range []string{"binding", "conditionalRole", "generalRole"} {
			t.Run(fmt.Sprintf("%s/%s", duration, kind),
				func(t *testing.T) { testRBACCapacitySealedMutationReplay(t, duration, kind) })
		}
	}
}

func TestRBACCapacityGeneralRoleRecipeChangesRulesWithoutChangingSeedOrOrdinaryIntent(t *testing.T) {
	plan, config, _, _, _ := makeRBACCapacityMutationJournal(t)
	plan.GeneralRoleMutations = true
	require.Error(t, plan.validate(), "general Role edits cannot acquire the binding mutation cohort")
	plan.MutationSubject = config.K6Workload.Mutation.Subject
	require.NoError(t, plan.validate())
	baseline := plan.namespace(1)
	before := mustRBACCapacityJSON(t, baseline)
	granted := plan.mutationNamespace(baseline, plan.MutationSubject, true)
	rule := granted.Resources[4]["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	require.Equal(t, []string{"product"}, rule["resources"])
	require.Equal(t, []string{"list"}, rule["verbs"])
	require.NotContains(t, rule, "when", "general edits must not merely change the original namespace condition")
	require.JSONEq(t, string(before), string(mustRBACCapacityJSON(t, baseline)))
	require.JSONEq(t, string(before), string(mustRBACCapacityJSON(t, plan.mutationNamespace(baseline, plan.MutationSubject, false))))
	original := plan
	original.GeneralRoleMutations = false
	require.JSONEq(t, string(mustRBACCapacityJSON(t, original.namespace(1))), string(before),
		"selecting general edits must leave original native seed policy and retained identities unchanged")
	for offer := int64(0); offer < 100; offer++ {
		expected, err := original.request(offer, 5, []string{plan.Subjects[0]})
		require.NoError(t, err)
		actual, err := plan.request(offer, 5, []string{plan.Subjects[0]})
		require.NoError(t, err)
		require.Equal(t, expected, actual, "holdout rule edits cannot change ordinary 60/30/10 intent")
	}
}

func testRBACCapacitySealedMutationReplay(t *testing.T, duration time.Duration, kind string) {
	plan, config, seeds, record, window := makeRBACCapacityMutationJournal(t, duration)
	if kind != "binding" {
		plan.MutationSubject = config.K6Workload.Mutation.Subject
		plan.GeneralRoleMutations = kind == "generalRole"
		config.Plan = plan
	}
	directory := fmt.Sprintf(".rbac-mutation-%d", time.Now().UnixNano())
	require.NoError(t, os.Mkdir(directory, 0700))
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	require.NoError(t, os.WriteFile(filepath.Join(directory, "mutation-companion.json"), mustRBACCapacityJSON(t, record), 0600))
	_, history := readRBACCapacityMutation(t, plan, 5, config.K6Workload, directory, seeds, window)
	sourcePath := filepath.Join(directory, "source-models.jsonl")
	var sources bytes.Buffer
	writeSource := func(origin, state map[string]any, resources []json.RawMessage, at time.Time) rbacCapacityPolicyRevision {
		header := rbacCapacitySourceHeader{ModelVersion: "v1", Origin: mustRBACCapacityJSON(t, origin), State: mustRBACCapacityJSON(t, state)}
		hash := sha256.New()
		_, _ = hash.Write(mustRBACCapacityJSON(t, header))
		for _, resource := range resources {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write(resource)
		}
		header.ContentDigest = hex.EncodeToString(hash.Sum(nil))
		encoder := json.NewEncoder(&sources)
		require.NoError(t, encoder.Encode(map[string]any{"type": "begin", "snapshot": header, "observedAt": at, "resourceCount": len(resources)}))
		for _, resource := range resources {
			require.NoError(t, encoder.Encode(map[string]any{"type": "resource", "resource": resource}))
		}
		require.NoError(t, encoder.Encode(map[string]any{"type": "end", "digest": header.ContentDigest}))
		return rbacCapacityPolicyRevision{TierUID: state["tierID"].(string), GitRevision: origin["gitRevision"].(string), Digest: header.ContentDigest}
	}
	root := rbacCapacityInitialRootManifest(window.Root.AdminSubject)
	root["metadata"].(map[string]any)["uid"] = window.Root.BindingUID
	root["metadata"].(map[string]any)["generation"] = 1
	root["status"] = map[string]any{"observedGeneration": 1, "resolved": map[string]any{
		"roleRef": map[string]any{"kind": "ClusterRole", "name": "cluster-admin", "uid": "builtin-admin", "observedGeneration": 1}}}
	cluster := writeSource(map[string]any{"namespace": "gitstore-system", "namespaceUID": window.Root.NamespaceUID,
		"gitRevision": "root-commit", "root": true, "active": true, "treeDigest": window.Root.TreeDigest},
		map[string]any{"tierID": "cluster", "roleCount": 0, "bindingCount": 1},
		[]json.RawMessage{mustRBACCapacityJSON(t, root)}, window.StartedAt.Add(-time.Second))
	revisions := make(map[string]rbacCapacityPolicyRevision)
	for _, admission := range history {
		var resources []json.RawMessage
		for _, resource := range admission.Namespace.Resources {
			var authored map[string]any
			require.NoError(t, json.Unmarshal(mustRBACCapacityJSON(t, resource), &authored))
			metadata := authored["metadata"].(map[string]any)
			kind, name := authored["kind"].(string), metadata["name"].(string)
			generation := int64(1)
			if pinned, found := admission.Generations[kind+"/"+name]; found {
				generation = pinned
			}
			uids := admission.Receipt.Roles
			if kind == "RoleBinding" {
				uids = admission.Receipt.Bindings
				ref := authored["spec"].(map[string]any)["roleRef"].(map[string]any)
				status := map[string]any{"observedGeneration": generation}
				if ref["name"] != "capacity-unresolved" {
					roleUID := admission.Receipt.Roles[ref["name"].(string)]
					if ref["kind"] == "ClusterRole" {
						roleUID = "builtin-view"
					}
					status["resolved"] = map[string]any{"roleRef": map[string]any{
						"kind": ref["kind"], "name": ref["name"], "uid": roleUID, "observedGeneration": 1}}
				}
				authored["status"] = status
			}
			metadata["uid"], metadata["generation"] = uids[name], generation
			resources = append(resources, mustRBACCapacityJSON(t, authored))
		}
		rawUID, err := base64.StdEncoding.DecodeString(admission.Receipt.NamespaceNodeUID)
		require.NoError(t, err)
		uid := strings.TrimPrefix(string(rawUID), "gid://GitStore/Namespace/")
		revision := writeSource(map[string]any{"namespace": admission.Namespace.Name, "namespaceUID": uid, "gitRevision": admission.Receipt.GitRevision},
			map[string]any{"tierID": uid, "roleCount": 10, "bindingCount": 50,
				"projection": map[string]any{"gitRevision": admission.Receipt.GitRevision}},
			resources, admission.AcknowledgedAt.Add(20*time.Millisecond))
		revisions[admission.Namespace.Name+"/"+admission.Receipt.GitRevision] = revision
	}
	require.NoError(t, os.WriteFile(sourcePath, sources.Bytes(), 0600))
	models := readRBACCapacityProbeSourcesWithHistory(t, sourcePath, plan, 5, seeds, window.Root, history, record.RestoredAt)
	var decisions [2]bytes.Buffer
	writeProbe := func(replica int, id string, probe rbacCapacityProbeExpectation) {
		var selected rbacCapacityAuthoredAdmission
		for _, admission := range history {
			if admission.Namespace.Name == probe.Namespace && admission.AcknowledgedAt.Before(probe.ObservedAt) {
				selected = admission
			}
		}
		revision := revisions[selected.Namespace.Name+"/"+selected.Receipt.GitRevision]
		at := probe.ObservedAt.Add(-time.Millisecond)
		outcome := "deny"
		if probe.Allowed {
			outcome = "allow"
		}
		require.NoError(t, json.NewEncoder(&decisions[replica]).Encode(map[string]any{
			"request_id": id, "ts": float64(at.UnixNano()) / float64(time.Second), "subject": probe.Subject,
			"action": probe.Action, "resource_namespace": probe.Namespace, "resource_kind": "product",
			"provider": "opa", "outcome": outcome, "authorization_tiers": []map[string]any{
				{"tierID": cluster.TierUID, "gitRevision": cluster.GitRevision, "contentDigest": cluster.Digest},
				{"tierID": revision.TierUID, "gitRevision": revision.GitRevision, "contentDigest": revision.Digest}}}))
	}
	for replica := range 2 {
		for id, probe := range record.Baseline[replica] {
			writeProbe(replica, id, probe)
		}
		for _, admission := range append(slices.Clone(record.Admissions), record.Restorations...) {
			for id, probe := range admission.Probes[replica] {
				writeProbe(replica, id, probe)
			}
		}
		path := filepath.Join(directory, fmt.Sprintf("decisions-%d.jsonl", replica))
		require.NoError(t, os.WriteFile(path, decisions[replica].Bytes(), 0600))
		probes, err := rbacCapacityMutationProbes(record, replica)
		require.NoError(t, err)
		observed := make(map[string]string)
		require.NoError(t, verifyRBACCapacityConcurrentTraces(t, path, history, models, probes, observed))
		latencies, err := rbacCapacityMutationPropagation(record, replica, observed)
		require.NoError(t, err)
		require.Len(t, latencies, len(record.Admissions))
	}
	path := filepath.Join(directory, "decisions-0.jsonl")
	expected := record.Admissions[4].Probes[0]
	omitted := maps.Clone(models)
	revision := revisions[record.Admissions[4].Namespace+"/"+record.Admissions[4].GitRevision]
	delete(omitted, revision)
	require.Error(t, verifyRBACCapacityConcurrentTraces(t, path, history, omitted, expected, nil))
	late := maps.Clone(models)
	alias := late[revision]
	alias.ObservedAt = record.RestoredAt
	late[revision] = alias
	require.Error(t, verifyRBACCapacityConcurrentTraces(t, path, history, late, expected, nil))
	for _, tamper := range []string{"missing", "duplicate", "fabricated-source", "fabricated-clock"} {
		t.Run(tamper, func(t *testing.T) {
			var lines []string
			for _, line := range strings.Split(strings.TrimSpace(decisions[0].String()), "\n") {
				if strings.Contains(line, `"request_id":"`+plan.Owner+`-mutation-4-0-00"`) {
					switch tamper {
					case "missing":
						continue
					case "duplicate":
						lines = append(lines, line)
					case "fabricated-source":
						line = strings.Replace(line, revision.Digest, "fabricated-source", 1)
					case "fabricated-clock":
						var receipt map[string]any
						require.NoError(t, json.Unmarshal([]byte(line), &receipt))
						receipt["ts"] = 0
						line = string(mustRBACCapacityJSON(t, receipt))
					}
				}
				lines = append(lines, line)
			}
			require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600))
			require.Error(t, verifyRBACCapacityConcurrentTraces(t, path, history, models, expected, nil))
		})
	}
}

func mustRBACCapacityJSON(t *testing.T, value any) []byte {
	t.Helper()
	content, err := json.Marshal(value)
	require.NoError(t, err)
	return content
}

func TestRBACCapacityRestorationDoesNotReuseUnadmittedLocalCandidate(t *testing.T) {
	plan, _, _, _, _ := makeRBACCapacityMutationJournal(t)
	plan.MutationSubject = plan.Prefix + "-filler-mutation"
	namespace := plan.namespace(1)
	directory := t.TempDir()
	remote := filepath.Join(directory, "remote.git")
	git := rbacNativeGit{dir: filepath.Join(directory, "work")}
	require.NoError(t, os.Mkdir(git.dir, 0700))
	_, err := git.command(t, "init", "--bare", remote)
	require.NoError(t, err)
	_, err = git.command(t, "init", "--initial-branch=main")
	require.NoError(t, err)
	_, err = git.command(t, "remote", "add", "origin", remote)
	require.NoError(t, err)
	path := "rbac/roles/capacity-role-04.yaml"
	granted := plan.mutationNamespace(namespace, plan.MutationSubject, true)
	git.write(t, path, granted.Resources[4])
	original, err := commitRBACCapacityMutation(func(args ...string) ([]byte, error) { return git.command(t, args...) }, path)
	require.NoError(t, err)
	git.write(t, path, namespace.Resources[4])
	candidate := git.commit(t)
	require.NotEqual(t, original, "main@sha1:"+candidate)
	require.Equal(t, strings.TrimPrefix(original, "main@sha1:"), strings.Fields(git.remoteTip(t))[0])

	restoration := prepareRBACCapacityRestoration(t, git, original)
	restoration.write(t, path, namespace.Resources[4])
	restored, err := commitRBACCapacityMutation(func(args ...string) ([]byte, error) {
		return restoration.command(t, args...)
	}, path)
	require.NoError(t, err, "a new restoration must commit even when the rejected local candidate already has the baseline")
	parent, err := restoration.command(t, "rev-parse", "HEAD^")
	require.NoError(t, err)
	require.Equal(t, strings.TrimPrefix(original, "main@sha1:"), strings.TrimSpace(string(parent)))
	require.Equal(t, strings.TrimPrefix(restored, "main@sha1:"), strings.Fields(git.remoteTip(t))[0])
	local, err := git.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, candidate, strings.TrimSpace(string(local)), "the unadmitted candidate must remain untouched")
}

func TestRBACCapacityMutationReadDefersObjectsUntilGitAdmissionOrRestoration(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprint("restoration=", restore), func(t *testing.T) {
			plan, _, _, _, _ := makeRBACCapacityMutationJournal(t)
			plan.MutationSubject = plan.Prefix + "-filler-mutation"
			namespace := plan.namespace(1)
			directory := t.TempDir()
			remote := filepath.Join(directory, "remote.git")
			git := rbacNativeGit{dir: filepath.Join(directory, "work")}
			require.NoError(t, os.Mkdir(git.dir, 0700))
			command := func(args ...string) ([]byte, error) { return git.command(t, args...) }
			_, err := command("init", "--bare", remote)
			require.NoError(t, err)
			_, err = command("init", "--initial-branch=main")
			require.NoError(t, err)
			_, err = command("remote", "add", "origin", remote)
			require.NoError(t, err)
			path := "rbac/roles/capacity-role-04.yaml"
			git.write(t, path, namespace.Resources[4])
			initial, err := commitRBACCapacityMutation(command, path)
			require.NoError(t, err)
			writer := rbacNativeGit{dir: filepath.Join(directory, "other-writer")}
			_, err = command("clone", "--branch", "main", remote, writer.dir)
			require.NoError(t, err)
			granted := plan.mutationNamespace(namespace, plan.MutationSubject, true)
			writer.write(t, path, granted.Resources[4])
			acknowledged, err := commitRBACCapacityMutation(func(args ...string) ([]byte, error) {
				return writer.command(t, args...)
			}, path)
			require.NoError(t, err)
			revision, err := readRBACCapacityMutationRevision(command)
			require.NoError(t, err)
			require.Equal(t, acknowledged, revision)
			commit := strings.TrimPrefix(revision, "main@sha1:")
			_, err = command("cat-file", "-e", commit)
			require.Error(t, err, "canonical readback must not transfer the remotely authored objects")
			if restore {
				git = prepareRBACCapacityRestoration(t, git, revision)
			} else {
				tracePath := filepath.Join(directory, "fetch-packets.log")
				_, err = command("config", "remote.origin.uploadpack",
					"env "+strconv.Quote("GIT_TRACE_PACKET="+tracePath)+" git-upload-pack")
				require.NoError(t, err)
				require.NoError(t, fetchRBACCapacityMutationRevision(command, revision))
				trace, err := os.ReadFile(tracePath)
				require.NoError(t, err)
				require.Contains(t, string(trace), "have "+strings.TrimPrefix(initial, "main@sha1:"),
					"working-clone fetch must negotiate its known parent instead of requesting the complete retained history")
				_, err = command("merge", "--ff-only", commit)
				require.NoError(t, err)
			}
			git.write(t, path, namespace.Resources[4])
			final, err := commitRBACCapacityMutation(command, path)
			require.NoError(t, err)
			parent, err := command("rev-parse", "HEAD^")
			require.NoError(t, err)
			require.Equal(t, commit, strings.TrimSpace(string(parent)),
				"the next Git write must be a single forward admission from the independently acknowledged remote commit")
			require.Equal(t, strings.TrimPrefix(final, "main@sha1:"), strings.Fields(git.remoteTip(t))[0])
			if restore {
				original := rbacNativeGit{dir: filepath.Join(directory, "work")}
				head, err := original.command(t, "rev-parse", "HEAD")
				require.NoError(t, err)
				require.Equal(t, strings.TrimPrefix(initial, "main@sha1:"), strings.TrimSpace(string(head)))
			}
		})
	}
}

func TestRBACCapacityConditionalRootCanonicalizationPinsNamespacePredicate(t *testing.T) {
	manifest := func(value string) map[string]any {
		return map[string]any{
			"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRole",
			"metadata": map[string]any{"name": "conditional-root"},
			"spec": map[string]any{"rules": []any{map[string]any{
				"resources": []string{"product"}, "verbs": []string{"list"},
				"when": []any{map[string]any{"attr": "namespace", "op": "eq", "value": value}},
			}}},
		}
	}
	digest := func(role map[string]any) (string, error) {
		return rbacCapacityAuthoredRootTreeDigest("admin", []rbacCapacityAuthoredRootResource{{Manifest: role, Generation: 1}})
	}
	allowed, err := digest(manifest("owned-namespace"))
	require.NoError(t, err)
	denied, err := digest(manifest("filler-namespace"))
	require.NoError(t, err)
	require.NotEqual(t, allowed, denied, "a root condition must be part of the independently authored tree digest")
	unconditional := manifest("owned-namespace")
	delete(unconditional["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any), "when")
	withoutCondition, err := digest(unconditional)
	require.NoError(t, err)
	require.NotEqual(t, allowed, withoutCondition, "dropping a condition cannot become a verified source alias")
	for _, field := range []string{"attr", "op", "token"} {
		t.Run(field, func(t *testing.T) {
			changed := manifest("owned-namespace")
			condition := changed["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["when"].([]any)[0].(map[string]any)
			condition[field] = "unsupported"
			_, err := digest(changed)
			require.Error(t, err, "the bounded recipe must refuse unmodeled expression semantics")
		})
	}
	plan, config, _, _, _ := makeRBACCapacityMutationJournal(t)
	plan.ConditionalRoot = true
	config.Plan = plan
	require.Error(t, validateRBACCapacityMutationSelection(config), "conditional root intent must be rejected before seeding without its sequential observer")
}

func TestRBACCapacitySealedPropagationRequiresEveryReplicaAndPhase(t *testing.T) {
	at := time.Now().Add(-time.Second)
	build := func() [2]map[string]rbacCapacityProbeExpectation {
		probes := [2]map[string]rbacCapacityProbeExpectation{{}, {}}
		for replica := range probes {
			for step := range 4 {
				probes[replica][fmt.Sprintf("owner-propagation-%d-%d-00", step, replica)] = rbacCapacityProbeExpectation{
					Subject: "subject", Namespace: "namespace", Action: "product.list",
					Allowed: step%2 == 1, ObservedAt: at.Add(time.Duration(step)*100*time.Millisecond + time.Millisecond),
				}
			}
			probes[replica][fmt.Sprintf("owner-propagation-baseline-%d", replica)] = rbacCapacityProbeExpectation{
				Subject: "subject", Namespace: "namespace", Action: "product.list",
				Allowed: true, ObservedAt: at.Add(-time.Millisecond),
			}
		}
		return probes
	}
	clocks := []time.Time{at, at.Add(100 * time.Millisecond), at.Add(200 * time.Millisecond), at.Add(300 * time.Millisecond)}
	check := func(probes [2]map[string]rbacCapacityProbeExpectation) error {
		return validateRBACCapacitySealedProbes("owner", "subject", "namespace", false, 0, clocks, probes)
	}
	require.NoError(t, check(build()))
	for _, mutation := range []string{"missing-phase", "wrong-replica", "wrong-subject", "wrong-verdict", "late", "missing-attempt", "missing-baseline"} {
		t.Run(mutation, func(t *testing.T) {
			probes := build()
			id := "owner-propagation-2-1-00"
			probe := probes[1][id]
			switch mutation {
			case "missing-phase":
				delete(probes[1], id)
			case "wrong-replica":
				delete(probes[1], id)
				probes[1]["owner-propagation-2-0-00"] = probe
			case "wrong-subject":
				probe.Subject = "other"
				probes[1][id] = probe
			case "wrong-verdict":
				probe.Allowed = true
				probes[1][id] = probe
			case "late":
				probe.ObservedAt = clocks[2].Add(6 * time.Second)
				probes[1][id] = probe
			case "missing-attempt":
				delete(probes[1], id)
				probes[1]["owner-propagation-2-1-01"] = probe
			case "missing-baseline":
				delete(probes[1], "owner-propagation-baseline-1")
			}
			require.Error(t, check(probes), "Complete cannot replace missing or tampered HTTP evidence")
		})
	}
}

func TestRBACCapacitySealedRootProbesRetainIndependentStages(t *testing.T) {
	at := time.Now().Add(-time.Second)
	clocks := []time.Time{at, at.Add(100 * time.Millisecond), at.Add(200 * time.Millisecond), at.Add(300 * time.Millisecond)}
	probes := [2]map[string]rbacCapacityProbeExpectation{{}, {}}
	for replica := range probes {
		for local := range 4 {
			probes[replica][fmt.Sprintf("owner-root-propagation-%d-%d-00", local+4, replica)] = rbacCapacityProbeExpectation{
				Subject: "subject", Namespace: "namespace", Action: "product.list",
				Allowed: local%2 == 0, ObservedAt: clocks[local].Add(time.Millisecond),
			}
		}
	}
	check := func() error {
		return validateRBACCapacitySealedProbes("owner", "subject", "namespace", true, 4, clocks, probes)
	}
	require.NoError(t, check(), "custom Role mutations are independently bounded after the binding probe stage")
	id := "owner-root-propagation-4-0-00"
	probe := probes[0][id]
	delete(probes[0], id)
	probes[0]["owner-root-propagation-0-0-00"] = probe
	require.Error(t, check(), "an earlier binding-stage request cannot stand in for a custom Role mutation")
}

func TestRBACCapacityRootUnderLoadRequiresIndependentSubjectTokenAndUntouchedTier(t *testing.T) {
	plan, config, _, _, _ := makeRBACCapacityMutationJournal(t)
	workload := config.K6Workload
	workload.RootUnderLoad, workload.Duration = true, 72*time.Second
	tokens := map[string]string{plan.PersonalSubject: "owner-token"}
	for _, subject := range plan.Subjects {
		tokens[subject] = "token-" + subject
	}
	workload.GroupMembers = []string{"reader-a"}
	ordinary, err := makeRBACCapacityK6WorkingSet(plan, 6, workload.GroupMembers, tokens)
	require.NoError(t, err)
	check := func(count int, token string) (rbacCapacityRequest, error) {
		return rbacCapacityRootUnderLoadTarget(plan, count, workload, config.InitialAdminSubject,
			token, "administrator-token", ordinary)
	}
	target, err := check(6, "holdout-token")
	require.NoError(t, err)
	require.Equal(t, plan.namespaceName(5), target.Namespace)
	require.Equal(t, workload.Mutation.Subject, target.Subject)
	require.Equal(t, "product.list", target.Action)
	_, err = check(5, "holdout-token")
	require.Error(t, err, "root probes cannot share one of the four mutation tiers")
	for _, token := range []string{"", "administrator-token", ordinary.Cases[0].Token} {
		_, err = check(6, token)
		require.Error(t, err, "token equality is independently rejected even when the declared subject differs")
	}
	ordinary.Cases[0].Subject = workload.Mutation.Subject
	_, err = check(6, "holdout-token")
	require.Error(t, err, "subject equality is independently rejected even when tokens differ")
	workload.Duration--
	require.Error(t, validateRBACCapacityK6Diagnostic(workload), "do not shorten the 48-second preamble or 12-second ordinary tail")
	workload.Duration = 31 * time.Minute
	workload.Mutation.Schedule.Duration = 30 * time.Minute
	require.NoError(t, validateRBACCapacityK6Diagnostic(workload), "the full 1800-second writer fits without reducing its rate or duration")
	workload.Mutation = nil
	require.Error(t, validateRBACCapacityK6Diagnostic(workload))
}

func TestRBACCapacityRootUnderLoadRequiresActualOrdinaryDecisionOnEachAPI(t *testing.T) {
	started := time.Unix(1000, 0)
	sample := rbacCapacityK6Case{Subject: "ordinary", Namespace: "owned-namespace", Action: "product.list", Expected: "allow"}
	root := rbacCapacityPolicyRevision{TierUID: "root-uid", GitRevision: "root-commit", Digest: "root-digest"}
	namespace := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "namespace-commit", Digest: "namespace-digest"}
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{
		root:      {Root: true, ObservedAt: started.Add(-time.Second)},
		namespace: {Namespace: sample.Namespace, ObservedAt: started.Add(-time.Second)},
	}
	check := func(replica int, record map[string]any) (bool, error) {
		content, err := json.Marshal(record)
		require.NoError(t, err)
		return checkRBACCapacityOrdinaryStart(bytes.NewReader(content), "owned", replica, started,
			func() time.Time { return started.Add(time.Second) }, sample, models)
	}
	record := map[string]any{
		"ts": 1000.5, "request_id": "owned-ordinary-0-0", "action": sample.Action,
		"subject": sample.Subject, "resource_namespace": sample.Namespace, "resource_kind": "product",
		"outcome": "allow", "provider": "opa", "authorization_tiers": []map[string]string{
			{"tierID": root.TierUID, "gitRevision": root.GitRevision, "contentDigest": root.Digest},
			{"tierID": namespace.TierUID, "gitRevision": namespace.GitRevision, "contentDigest": namespace.Digest},
		},
	}
	confirmed, err := check(0, record)
	require.NoError(t, err)
	require.True(t, confirmed)
	confirmed, err = check(1, record)
	require.NoError(t, err)
	require.False(t, confirmed, "a live process and one API's actual decisions cannot establish the other API's traffic")
	for _, tc := range []struct {
		field string
		value any
	}{
		{"ts", 999.0}, {"ts", 1002.0}, {"subject", "administrator"}, {"outcome", "deny"},
		{"provider", "allow-all"}, {"authorization_tiers", []any{}},
	} {
		t.Run(tc.field+fmt.Sprint(tc.value), func(t *testing.T) {
			altered := maps.Clone(record)
			altered[tc.field] = tc.value
			confirmed, err := check(0, altered)
			require.Error(t, err)
			require.False(t, confirmed)
		})
	}
	t.Run("decision-appended-during-poll", func(t *testing.T) {
		altered := maps.Clone(record)
		altered["ts"] = 1001.001
		content, err := json.Marshal(altered)
		require.NoError(t, err)
		reader := bytes.NewReader(content)
		confirmed, err := checkRBACCapacityOrdinaryStart(reader, "owned", 0, started, func() time.Time {
			require.Zero(t, reader.Len(), "sample the upper clock bound after reading the concurrent append")
			return started.Add(2 * time.Second)
		}, sample, models)
		require.NoError(t, err)
		require.True(t, confirmed)
	})
	models[root] = rbacCapacityProbeSource{Root: true, ObservedAt: started.Add(time.Second)}
	confirmed, err = check(0, record)
	require.Error(t, err, "later source capture cannot establish pre-write ordinary readiness")
	require.False(t, confirmed)
}

func TestRBACCapacityRootUnderLoadEvidenceEnvelopeCoversTheFullScheduledStream(t *testing.T) {
	_, config, _, _, _ := makeRBACCapacityMutationJournal(t)
	workload := config.K6Workload
	workload.RootUnderLoad, workload.Duration, workload.Rate = true, 31*time.Minute, 500
	workload.Mutation.Schedule.Duration = 30 * time.Minute
	bound, err := rbacCapacityK6DecisionByteBound(workload)
	require.NoError(t, err)
	ordinary := int64(workload.Duration/time.Second) * int64(workload.Rate)
	require.Greater(t, bound, ordinary*1141, "full-sized ordinary receipts alone exceed the former 1 GiB root replay limit")
	require.Greater(t, bound, int64(1<<30))
	require.LessOrEqual(t, bound, int64(8<<30), "the derived envelope cannot weaken the existing k6 ceiling")
	workload.Duration, workload.Mutation.Schedule.Duration = 72*time.Second, 12*time.Second
	short, err := rbacCapacityK6DecisionByteBound(workload)
	require.NoError(t, err)
	require.Less(t, short, bound, "short diagnostics cannot consume the full sustained evidence allowance")
	workload.Duration = 32 * time.Minute
	_, err = rbacCapacityK6DecisionByteBound(workload)
	require.Error(t, err, "invalid declared windows cannot obtain a larger evidence envelope")
}

func makeRBACCapacityRootUnderLoadJournal(t *testing.T) (rbacCapacitySeedPlan, *rbacCapacityK6DiagnosticConfig,
	rbacCapacityRootPropagationReceipt, rbacCapacityMutationReceipt, rbacCapacityK6ProcessWindow,
) {
	t.Helper()
	plan, config, _, mutation, window := makeRBACCapacityMutationJournal(t)
	plan.MaximumRootBatch, plan.ConditionalRoot = true, true
	workload := config.K6Workload
	workload.RootUnderLoad, workload.Duration = true, 72*time.Second
	window.CompletedAt = window.StartedAt.Add(workload.Duration)
	mutation.ScheduledAt = window.StartedAt.Add(48 * time.Second)
	mutation.CompletedAt = mutation.ScheduledAt.Add(workload.Mutation.Schedule.Duration + time.Millisecond)
	record := rbacCapacityRootPropagationReceipt{Owner: plan.Owner, Subject: workload.Mutation.Subject,
		Namespace: plan.namespaceName(5), VerifiedSources: 8, Complete: true,
		CompletedAt: window.StartedAt.Add(34 * time.Second),
		ProbeStages: [][2]map[string]rbacCapacityProbeExpectation{{{}, {}}, {{}, {}}},
	}
	baseline := rbacCapacityAuthoredRootAdmission{Policy: window.Root, GitRevision: fmt.Sprintf("main@sha1:%040d", 0),
		BaselineKnownAt: window.StartedAt.Add(time.Second), Cases: map[string]bool{record.Subject + "/product.list": false}}
	record.History = append(record.History, baseline)
	manifest := func(kind, name string, spec map[string]any) map[string]any {
		return map[string]any{"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": kind,
			"metadata": map[string]any{"name": name, "labels": map[string]any{rbacCapacityOwnerLabel: plan.Owner}}, "spec": spec}
	}
	binding := func(name, role, subject string) map[string]any {
		return manifest("ClusterRoleBinding", name, map[string]any{
			"roleRef":  map[string]any{"kind": "ClusterRole", "name": role},
			"subjects": []any{map[string]any{"kind": "User", "name": subject}},
		})
	}
	for step := range 8 {
		allowed := step%2 == 0
		admission := rbacCapacityAuthoredRootAdmission{Policy: window.Root,
			GitRevision:    fmt.Sprintf("main@sha1:%040d", step+1),
			AcknowledgedAt: window.StartedAt.Add(time.Duration(2+step*4) * time.Second),
			Cases:          map[string]bool{record.Subject + "/product.list": allowed},
			Resources:      slices.Clone(record.History[step].Resources)}
		if step == 0 || step == 4 {
			admission.PendingCases = map[string]bool{record.Subject + "/product.list": false}
		}
		switch {
		case step < 3:
			subject := record.Subject
			if !allowed {
				subject = plan.Prefix + "-filler-root-propagation"
			}
			admission.Resources = []rbacCapacityAuthoredRootResource{{
				Manifest: binding(plan.Prefix+"-root-view", "view", subject),
				UID:      "view-binding", RoleUID: "builtin-view", Generation: int64(step + 1), ReferenceGeneration: 1}}
		case step == 3:
			admission.Resources[0].Removed = true
		case step == 7:
			for index := 1; index < len(admission.Resources); index++ {
				admission.Resources[index].Removed = true
			}
		default:
			value := record.Namespace
			if !allowed {
				value = plan.Prefix + "-filler-root-condition"
			}
			role := manifest("ClusterRole", plan.Prefix+"-root-rule", map[string]any{
				"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"list"},
					"when": []any{map[string]any{"attr": "namespace", "op": "eq", "value": value}}}},
			})
			if step == 4 {
				batch, err := rbacCapacityMaximumRootBatch(plan, []rbacCapacityAuthoredRootResource{
					{Manifest: role, UID: "custom-role", Generation: 1},
					{Manifest: binding(plan.Prefix+"-root-rule-binding", plan.Prefix+"-root-rule", record.Subject),
						UID: "custom-binding", RoleUID: "custom-role", Generation: 1, ReferenceGeneration: 1},
				})
				require.NoError(t, err)
				for index := 2; index < len(batch); index++ {
					batch[index].UID = fmt.Sprintf("filler-%d", index)
				}
				admission.Resources = append(admission.Resources, batch...)
			} else {
				admission.Resources[1].Manifest, admission.Resources[1].Generation = role, int64(step-3)
			}
		}
		var err error
		admission.Policy.TreeDigest, err = rbacCapacityAuthoredRootTreeDigest(window.Root.AdminSubject, admission.Resources)
		require.NoError(t, err)
		record.History = append(record.History, admission)
		record.Latencies = append(record.Latencies, [2]time.Duration{time.Millisecond, 2 * time.Millisecond})
		for replica := range 2 {
			record.ProbeStages[step/4][replica][fmt.Sprintf("%s-root-propagation-%d-%d-00", plan.Owner, step, replica)] =
				rbacCapacityProbeExpectation{Subject: record.Subject, Namespace: record.Namespace, Action: "product.list",
					Allowed: allowed, ObservedAt: admission.AcknowledgedAt.Add(record.Latencies[step][replica])}
		}
	}
	for replica := range 2 {
		record.ProbeStages[0][replica][fmt.Sprintf("%s-root-propagation--1-%d-00", plan.Owner, replica)] =
			rbacCapacityProbeExpectation{Subject: record.Subject, Namespace: record.Namespace, Action: "product.list",
				ObservedAt: baseline.BaselineKnownAt.Add(time.Millisecond)}
	}
	return plan, workload, record, mutation, window
}

func TestRBACCapacityMutationStartRechecksSealedWallClockAfterTimerWake(t *testing.T) {
	deadline := time.Now().Add(time.Second)
	readings := []time.Time{deadline.Add(-time.Millisecond), deadline.Add(-time.Millisecond), deadline}
	now := func() time.Time {
		require.NotEmpty(t, readings)
		current := readings[0]
		readings = readings[1:]
		return current
	}
	require.NoError(t, waitRBACCapacityMutationStart(t.Context(), make(chan struct{}), deadline, now))
	require.Empty(t, readings, "a timer wake must not authorize writes before the serialized wall-clock preamble ends")
}

func TestRBACCapacityMutationStartStopsWithOrdinaryWorkloadOrContext(t *testing.T) {
	for _, stop := range []string{"ordinary", "context"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			processDone := make(chan struct{})
			deadline := time.Now().Add(time.Hour)
			now := func() time.Time {
				if stop == "ordinary" {
					close(processDone)
				} else {
					cancel()
				}
				return deadline.Add(-time.Hour)
			}
			err := waitRBACCapacityMutationStart(ctx, processDone, deadline, now)
			if stop == "context" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.ErrorContains(t, err, "ordinary k6 exited")
			}
		})
	}
}

func TestRBACCapacityRootUnderLoadSealedMaximumHistoryRejectsTampering(t *testing.T) {
	plan, config, record, mutation, window := makeRBACCapacityRootUnderLoadJournal(t)
	require.NoError(t, checkRBACCapacityRootUnderLoadReceipt(plan, 6, config, record, mutation, window))
	for _, change := range []string{"late-root", "short-writes", "overlap", "early-writes", "missing-phase", "pending", "cleanup-error",
		"filler-uid", "builtin-uid", "filler-removal", "filler-owner", "filler-generation", "missing-api", "moved-ack", "reused-revision"} {
		t.Run(change, func(t *testing.T) {
			altered := record
			altered.History = slices.Clone(record.History)
			for index := range altered.History {
				altered.History[index].Resources = slices.Clone(record.History[index].Resources)
			}
			altered.ProbeStages = slices.Clone(record.ProbeStages)
			writes := mutation
			switch change {
			case "late-root":
				altered.CompletedAt = window.StartedAt.Add(49 * time.Second)
			case "short-writes":
				writes.CompletedAt = writes.ScheduledAt.Add(11 * time.Second)
			case "overlap":
				writes.ScheduledAt = altered.History[7].AcknowledgedAt
			case "early-writes":
				writes.ScheduledAt = window.StartedAt.Add(48*time.Second - time.Nanosecond)
			case "missing-phase":
				altered.History = altered.History[:8]
			case "pending":
				altered.Pending = &altered.History[8]
			case "cleanup-error":
				altered.CleanupError = "unrestored"
			case "filler-uid":
				altered.History[5].Resources[3].UID = altered.History[5].Resources[4].UID
			case "builtin-uid":
				altered.History[5].Resources[3].UID = altered.History[1].Resources[0].RoleUID
			case "filler-removal":
				altered.History[8].Resources[3].Removed = false
				var err error
				altered.History[8].Policy.TreeDigest, err = rbacCapacityAuthoredRootTreeDigest(window.Root.AdminSubject, altered.History[8].Resources)
				require.NoError(t, err, "a rehashed source cannot hide a retained filler")
			case "filler-owner":
				resource := &altered.History[5].Resources[3]
				resource.Manifest = maps.Clone(resource.Manifest)
				metadata := maps.Clone(resource.Manifest["metadata"].(map[string]any))
				metadata["labels"] = map[string]any{rbacCapacityOwnerLabel: "someone-else"}
				resource.Manifest["metadata"] = metadata
			case "filler-generation":
				altered.History[6].Resources[3].Generation++
			case "missing-api":
				altered.ProbeStages[1][1] = nil
			case "moved-ack":
				altered.History[6].AcknowledgedAt = altered.History[5].AcknowledgedAt.Add(6 * time.Second)
			case "reused-revision":
				altered.History[6].GitRevision = altered.History[5].GitRevision
			}
			require.Error(t, checkRBACCapacityRootUnderLoadReceipt(plan, 6, config, altered, writes, window))
		})
	}
}

func TestRBACCapacityRootRestorationRejectsRefAndOwnershipDrift(t *testing.T) {
	_, _, record, _, _ := makeRBACCapacityRootUnderLoadJournal(t)
	admission, err := rbacCapacityRootRestorationAdmission(&record, record.History[5].GitRevision)
	require.NoError(t, err)
	require.Same(t, &record.History[5], admission)
	_, err = rbacCapacityRootRestorationAdmission(&record, "unrelated-canonical-tip")
	require.Error(t, err)
	record.Pending = &record.History[5]
	record.History = record.History[:5]
	_, err = rbacCapacityRootRestorationAdmission(&record, record.Pending.GitRevision)
	require.NoError(t, err, "an uncertain dispatched creation retains its exact recovery candidate")
	record.Pending.Resources[3].Manifest["metadata"].(map[string]any)["labels"] = map[string]any{rbacCapacityOwnerLabel: "other"}
	_, err = rbacCapacityRootRestorationAdmission(&record, record.Pending.GitRevision)
	require.Error(t, err, "even an exact authored ref cannot authorize removal of an unowned filler")
}

func TestRBACCapacityRootCleanupJoinsBeforeRestorationAndPreservesFailedReceipts(t *testing.T) {
	for _, fault := range []string{"none", "join", "restore", "complete"} {
		t.Run(fault, func(t *testing.T) {
			pending := &rbacCapacityAuthoredRootAdmission{GitRevision: "uncertain-root-candidate"}
			record := rbacCapacityRootPropagationReceipt{Owner: "owned", Pending: pending, Complete: fault == "complete"}
			var events []string
			err := runRBACCapacityRootCleanup(&record, func() error {
				events = append(events, "stop-and-join")
				if fault == "join" {
					return errors.New("live k6 did not join")
				}
				return nil
			}, func() error {
				events = append(events, "restore")
				require.Equal(t, []string{"stop-and-join", "restore"}, events)
				record.Restorations = append(record.Restorations, rbacCapacityAuthoredRootAdmission{GitRevision: "forward-removal"})
				if fault == "restore" {
					return errors.New("root projection incarnation drift")
				}
				record.RestoredAt = time.Now()
				return nil
			})
			require.Same(t, pending, record.Pending, "cleanup cannot relabel or discard an uncertain admission")
			switch fault {
			case "join":
				require.Error(t, err)
				require.Equal(t, []string{"stop-and-join"}, events, "never restore root authority while ordinary traffic can still run")
				require.Empty(t, record.Restorations)
				require.Contains(t, record.CleanupError, "did not join")
			case "restore":
				require.Error(t, err)
				require.Contains(t, record.CleanupError, "incarnation drift")
				require.Len(t, record.Restorations, 1, "failed forward-removal verification must retain its authored restoration receipt")
				require.True(t, record.RestoredAt.IsZero())
			case "complete":
				require.NoError(t, err)
				require.Empty(t, events, "completed removal must not trigger another root write")
			default:
				require.NoError(t, err)
				require.False(t, record.RestoredAt.IsZero())
				require.False(t, record.Complete, "successful failure cleanup is not a successful load run")
			}
		})
	}
}

func TestRBACCapacityScheduleSupportsFiftyWritesPerMinuteWithoutBurstRounding(t *testing.T) {
	started := time.Now()
	observations := make(chan time.Time, 2)
	accounting, err := runRBACCapacitySchedule(t.Context(), rbacCapacityScheduleConfig{
		Duration: 2400 * time.Millisecond, Drain: time.Second,
		Rate: 50, Period: time.Minute, Workers: 4, Pending: 4,
	}, func(context.Context, int64) string {
		observations <- time.Now()
		return "allow"
	})
	require.NoError(t, err)
	require.NoError(t, accounting.validate(2, false))
	first, second := <-observations, <-observations
	require.Less(t, first.Sub(started), time.Second)
	require.GreaterOrEqual(t, second.Sub(started), 1200*time.Millisecond,
		"50/min must use original 1.2-second due times, not a rounded 1/s rate or batch of writes")
	require.EqualValues(t, 1500, rbacCapacityScheduledOffers(30*time.Minute, 50, time.Minute))
}

func TestRBACCapacityPropagationRejectsLateSuccessAndUnavailableDenials(t *testing.T) {
	for _, outcome := range []string{"unavailable", "wrong", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			_, err := observeRBACCapacityPropagation(t.Context(), time.Now(), false,
				func(context.Context, int, int) string { return outcome })
			require.Error(t, err, "transport, malformed and fail-closed responses are not successful revocations")
		})
	}
	t.Run("expired-original-clock", func(t *testing.T) {
		_, err := observeRBACCapacityPropagation(t.Context(), time.Now().Add(-6*time.Second), true,
			func(context.Context, int, int) string {
				t.Error("expired propagation work must not start another observation")
				return "allow"
			})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("late-success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, err := observeRBACCapacityPropagation(ctx, time.Now(), true,
			func(context.Context, int, int) string {
				cancel()
				return "allow"
			})
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("one-replica-never-converges", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		var calls [2]atomic.Int64
		_, err := observeRBACCapacityPropagation(ctx, time.Now(), true,
			func(_ context.Context, replica, attempt int) string {
				calls[replica].Add(1)
				if replica == 0 {
					return "allow"
				}
				return "deny"
			})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.EqualValues(t, 1, calls[0].Load())
		require.EqualValues(t, 1, calls[1].Load())
	})
	for _, acknowledgedAt := range []time.Time{{}, time.Now().Add(time.Hour)} {
		_, err := observeRBACCapacityPropagation(t.Context(), acknowledgedAt, true,
			func(context.Context, int, int) string {
				t.Error("unattributable admission clocks must fail before observation")
				return "allow"
			})
		require.Error(t, err)
	}
}

func TestRBACCapacityPropagationSelectsAnIndependentOrganizationGrant(t *testing.T) {
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-propagation", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	index, err := rbacCapacityPropagationNamespace(plan, 4, []string{"reader-a", "reader-b"})
	require.NoError(t, err)
	require.Equal(t, 2, index, "skip the protected personal owner and every overlapping Group grantee")
	for _, tc := range []struct {
		count  int
		groups []string
	}{
		{1, nil}, {1001, nil}, {2, []string{"reader-b"}},
		{4, plan.Subjects}, {4, []string{"unknown"}}, {4, []string{"reader-a", "reader-a"}},
	} {
		_, err := rbacCapacityPropagationNamespace(plan, tc.count, tc.groups)
		require.Error(t, err, "a revocation must not assume removal of an independent or protected grant")
	}
}

type rbacCapacitySourceReadCounter struct {
	io.Reader
	calls int
}

func TestRBACCapacityCapturedSourceUsesTheOriginalObservationDeadline(t *testing.T) {
	resource := json.RawMessage(`{"kind":"Role","metadata":{"name":"owned-role"},"spec":{"rules":[]}}`)
	header := rbacCapacitySourceHeader{ModelVersion: "v1", State: json.RawMessage(`{"tierID":"owned"}`),
		Origin: json.RawMessage(`{"namespace":"owned"}`)}
	canonical, err := json.Marshal(header)
	require.NoError(t, err)
	digest := sha256.Sum256(append(append(canonical, 0), resource...))
	header.ContentDigest = hex.EncodeToString(digest[:])
	stream := func(at time.Time) string {
		var output bytes.Buffer
		encoder := json.NewEncoder(&output)
		require.NoError(t, encoder.Encode(map[string]any{"type": "begin", "snapshot": header, "resourceCount": 1, "observedAt": at}))
		require.NoError(t, encoder.Encode(map[string]any{"type": "resource", "resource": resource}))
		require.NoError(t, encoder.Encode(map[string]any{"type": "end", "digest": header.ContentDigest}))
		return output.String()
	}
	acknowledgedAt := time.Now().Add(-10 * time.Second)
	deadline := acknowledgedAt.Add(5 * time.Second)
	for _, tc := range []struct {
		name            string
		at              time.Time
		eligible        bool
		want, wantError bool
	}{
		{"before-deadline", deadline.Add(-time.Nanosecond), true, true, false},
		{"at-deadline", deadline, true, true, false},
		{"late-capture", deadline.Add(time.Nanosecond), true, false, false},
		{"pending-source", deadline, false, false, false},
		{"missing-clock", time.Time{}, true, false, true},
		{"future-clock", time.Now().Add(time.Hour), true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verified, err := checkRBACCapacityCapturedSource(strings.NewReader(stream(tc.at)), deadline,
				func(rbacCapacitySourceHeader, []json.RawMessage) bool { return tc.eligible })
			require.Equal(t, tc.want, verified)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	directory := t.TempDir()
	var paths []string
	for index := range 2 {
		path := filepath.Join(directory, fmt.Sprintf("source-%d.jsonl", index))
		require.NoError(t, os.WriteFile(path, []byte(stream(deadline.Add(-time.Second))), 0600))
		paths = append(paths, path)
	}
	waitRBACCapacityCapturedSources(t, paths, acknowledgedAt,
		func(got rbacCapacitySourceHeader, resources []json.RawMessage) bool {
			return got.ContentDigest == header.ContentDigest && len(resources) == 1
		})
}

func (reader *rbacCapacitySourceReadCounter) Read(content []byte) (int, error) {
	reader.calls++
	return reader.Reader.Read(content)
}

func TestRBACCapacitySourceModelsRejectTamperingGapsAndUncommittedTails(t *testing.T) {
	resource := json.RawMessage(`{"kind":"Role","metadata":{"name":"owned-role"},"spec":{"rules":[]}}`)
	header := rbacCapacitySourceHeader{ModelVersion: "v1", State: json.RawMessage(`{"tierID":"owned"}`),
		Origin: json.RawMessage(`{"namespace":"owned"}`)}
	canonical, err := json.Marshal(header)
	require.NoError(t, err)
	digest := sha256.Sum256(append(append(canonical, 0), resource...))
	header.ContentDigest = hex.EncodeToString(digest[:])
	var stream bytes.Buffer
	encoder := json.NewEncoder(&stream)
	observedAt := time.Unix(1000, 0).UTC()
	require.NoError(t, encoder.Encode(map[string]any{"type": "begin", "snapshot": header, "resourceCount": 1, "observedAt": observedAt}))
	require.NoError(t, encoder.Encode(map[string]any{"type": "resource", "resource": resource}))
	incomplete := stream.String()
	require.NoError(t, encoder.Encode(map[string]any{"type": "end", "digest": header.ContentDigest}))
	complete := stream.String()
	later := strings.Replace(complete, observedAt.Format(time.RFC3339), observedAt.Add(time.Minute).Format(time.RFC3339), 1)
	var withinWindow int
	require.NoError(t, walkRBACCapacitySourceModelsWithClock(strings.NewReader(complete+later),
		func(got rbacCapacitySourceHeader, resources []json.RawMessage, at time.Time) error {
			if at.After(observedAt.Add(time.Second)) {
				return nil
			}
			require.Equal(t, observedAt, at)
			require.Equal(t, header.ContentDigest, got.ContentDigest)
			require.Len(t, resources, 1)
			withinWindow++
			return nil
		}))
	require.Equal(t, 1, withinWindow, "later source aliases must not rewrite an earlier workload's source window")
	t.Run("bounded-stream-reads", func(t *testing.T) {
		input := strings.Repeat(complete, 256)
		reader := &rbacCapacitySourceReadCounter{Reader: strings.NewReader(input)}
		accepted := 0
		require.NoError(t, walkRBACCapacitySourceModels(reader, func(rbacCapacitySourceHeader, []json.RawMessage) error {
			accepted++
			return nil
		}))
		require.Equal(t, 256, accepted, "buffering must not skip repeated source frames")
		require.LessOrEqual(t, reader.calls, len(input)/(32<<10)+2,
			"full-scale evidence must not require a separate file read for every few source records")
	})
	t.Run("partial-live-lines", func(t *testing.T) {
		lines := strings.Split(strings.TrimSuffix(complete, "\n"), "\n")
		prefix := ""
		for index, line := range lines {
			partial := complete + prefix + line[:len(line)-1]
			t.Run([]string{"begin", "resource", "end"}[index], func(t *testing.T) {
				accepted := 0
				visit := func(rbacCapacitySourceHeader, []json.RawMessage) error {
					accepted++
					return nil
				}
				require.ErrorIs(t, walkRBACCapacitySourceModels(strings.NewReader(partial), visit), io.ErrUnexpectedEOF)
				require.Equal(t, 1, accepted, "an unfinished appended line cannot register another source model")
				for _, suffix := range []string{"\n", "\n" + complete} {
					accepted = 0
					err := walkRBACCapacitySourceModels(strings.NewReader(partial+suffix), visit)
					require.Error(t, err)
					require.NotErrorIs(t, err, io.ErrUnexpectedEOF, "malformed committed or interior lines must still fail")
					require.Equal(t, 1, accepted)
				}
			})
			prefix += line + "\n"
		}
		accepted := 0
		require.NoError(t, walkRBACCapacitySourceModels(strings.NewReader(strings.TrimSuffix(complete, "\n")),
			func(rbacCapacitySourceHeader, []json.RawMessage) error {
				accepted++
				return nil
			}))
		require.Equal(t, 1, accepted, "a complete hashed final model remains valid without a trailing newline")
		err := walkRBACCapacitySourceModels(strings.NewReader(complete+`{"type":!`),
			func(rbacCapacitySourceHeader, []json.RawMessage) error { return nil })
		require.Error(t, err)
		require.NotErrorIs(t, err, io.ErrUnexpectedEOF, "invalid syntax is not an unfinished valid JSON prefix")
	})
	for _, tc := range []struct {
		name, input string
		accepted    int
		wantError   bool
	}{
		{"complete", complete, 1, false},
		{"altered-policy", strings.Replace(complete, `"rules":[]`, `"rules":[{"verbs":["write"]}]`, 1), 0, true},
		{"verified-then-forged-repeat", complete + strings.Replace(complete, `"rules":[]`, `"rules":[{"verbs":["write"]}]`, 1), 1, true},
		{"missing-end", incomplete, 0, true},
		{"missing-resource", strings.Replace(complete, `"resourceCount":1`, `"resourceCount":2`, 1), 0, true},
		{"overlapping-header", incomplete + complete, 0, true},
		{"complete-then-live-tail", complete + incomplete, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accepted := 0
			err := walkRBACCapacitySourceModels(strings.NewReader(tc.input), func(got rbacCapacitySourceHeader, resources []json.RawMessage) error {
				accepted++
				require.Equal(t, header, got)
				require.Equal(t, []json.RawMessage{resource}, resources)
				return nil
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.accepted, accepted, "only complete independently hashed models may reach the policy oracle")
		})
	}
	rejected := errors.New("source policy differs from independently authored history")
	require.ErrorIs(t, walkRBACCapacitySourceModels(strings.NewReader(complete), func(rbacCapacitySourceHeader, []json.RawMessage) error {
		return rejected
	}), rejected)
	require.ErrorIs(t, walkRBACCapacitySourceModels(strings.NewReader(incomplete), func(rbacCapacitySourceHeader, []json.RawMessage) error {
		t.Fatal("incomplete evidence cannot register a policy vector")
		return nil
	}), io.ErrUnexpectedEOF)
}

func TestRBACCapacitySeedReadbackBoundsAdmissionWaitAndRejectsLateSuccess(t *testing.T) {
	t.Run("canceled-admission-wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, awaitRBACCapacitySeedReadback(ctx, make(chan time.Time), func() bool {
			t.Fatal("canceled work must not start an observation")
			return true
		}), context.Canceled)
	})
	t.Run("late-success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		slots := make(chan time.Time, 1)
		slots <- time.Now()
		require.ErrorIs(t, awaitRBACCapacitySeedReadback(ctx, slots, func() bool {
			cancel()
			return true
		}), context.Canceled)
	})
	t.Run("retry-before-success", func(t *testing.T) {
		slots := make(chan time.Time, 2)
		slots <- time.Now()
		slots <- time.Now()
		observations := 0
		require.NoError(t, awaitRBACCapacitySeedReadback(t.Context(), slots, func() bool {
			observations++
			return observations == 2
		}))
		require.Equal(t, 2, observations)
	})
}

func TestRBACCapacitySeedAdmissionsPreserveAcknowledgementClocks(t *testing.T) {
	first := time.Unix(1000, 0).UTC()
	record := rbacCapacitySeedReceipt{}
	require.NoError(t, record.acknowledge(strings.Repeat("a", 40), first, 10))
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	var restored rbacCapacitySeedReceipt
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.Equal(t, first, restored.Admissions[0].AcknowledgedAt,
		"private receipts must retain admission time, not substitute a later readback time")
	require.NoError(t, restored.acknowledge(strings.Repeat("b", 40), first.Add(time.Second), 20))
	require.Equal(t, first, restored.Admissions[0].AcknowledgedAt)
	require.Equal(t, "main@sha1:"+strings.Repeat("b", 40), restored.GitRevision)
	for _, tc := range []struct {
		name, sha string
		at        time.Time
		resources int
	}{
		{"duplicate-commit", strings.Repeat("a", 40), first.Add(2 * time.Second), 30},
		{"missing-clock", strings.Repeat("c", 40), time.Time{}, 30},
		{"reordered-clock", strings.Repeat("c", 40), first, 30},
		{"repeated-clock", strings.Repeat("c", 40), first.Add(time.Second), 30},
		{"unidentified-commit", "not-a-commit", first.Add(2 * time.Second), 30},
		{"empty-batch", strings.Repeat("c", 40), first.Add(2 * time.Second), 20},
		{"oversized-batch", strings.Repeat("c", 40), first.Add(2 * time.Second), 31},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(restored)
			require.NoError(t, err)
			require.Error(t, restored.acknowledge(tc.sha, tc.at, tc.resources))
			after, err := json.Marshal(restored)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "invalid admission evidence must not alter history")
		})
	}
	for index := 2; index < 6; index++ {
		require.NoError(t, restored.acknowledge(strings.Repeat(string(rune('a'+index)), 40),
			first.Add(time.Duration(index)*time.Second), min(10*(index+1), 59)))
	}
	require.Len(t, restored.Admissions, 6)
	require.Equal(t, 59, restored.Admissions[5].Resources)
	require.Error(t, restored.acknowledge(strings.Repeat("1", 40), first.Add(6*time.Second), 60),
		"the seed receipt must not grow beyond its six declared imports")
}

func TestRBACCapacitySeedSingleDeclaredImportPreservesItsAdmission(t *testing.T) {
	first := time.Unix(1000, 0).UTC()
	record := rbacCapacitySeedReceipt{ImportBatchSize: 60}
	require.NoError(t, record.acknowledge(strings.Repeat("a", 40), first, 60))
	require.Len(t, record.Admissions, 1)
	require.Equal(t, first, record.Admissions[0].AcknowledgedAt)
	require.Equal(t, 60, record.Admissions[0].Resources)
	before, err := json.Marshal(record)
	require.NoError(t, err)
	require.Error(t, record.acknowledge(strings.Repeat("b", 40), first.Add(time.Second), 61))
	after, err := json.Marshal(record)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after))
	for _, limit := range []int{-1, 11, 61} {
		record := rbacCapacitySeedReceipt{ImportBatchSize: limit}
		require.Error(t, record.acknowledge(strings.Repeat("a", 40), first, 10),
			"only the declared small or complete bounded fixture import is supported")
		require.Empty(t, record.Admissions)
	}
}

func TestRBACCapacitySourceBindingRequiresTheAdmittedRoleIncarnation(t *testing.T) {
	for _, tc := range []struct {
		name, role, uid string
		observed        int
		ready, invalid  bool
	}{
		{"resolved", "owned-role", "admitted-uid", 1, true, false},
		{"pending-generation", "owned-role", "admitted-uid", 0, false, false},
		{"pending-resolution", "owned-role", "", 1, false, false},
		{"different-incarnation", "owned-role", "other-uid", 1, false, true},
		{"deliberately-unresolved", "capacity-unresolved", "", 1, true, false},
		{"invented-resolution", "capacity-unresolved", "invented-uid", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := map[string]any{
				"metadata": map[string]any{"generation": 1},
				"spec":     map[string]any{"roleRef": map[string]any{"kind": "Role", "name": tc.role}},
				"status":   map[string]any{"observedGeneration": tc.observed},
			}
			if tc.uid != "" {
				resource["status"].(map[string]any)["resolved"] = map[string]any{"roleRef": map[string]any{
					"kind": "Role", "name": tc.role, "uid": tc.uid, "observedGeneration": tc.observed,
				}}
			}
			encoded, err := json.Marshal(resource)
			require.NoError(t, err)
			ready, err := rbacCapacitySeedBindingResolved(encoded, map[string]string{"owned-role": "admitted-uid"}, 1)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.ready, ready)
		})
	}
}

func TestRBACCapacitySourceBindingPinsOriginalReferenceGenerationAcrossSubjectEdits(t *testing.T) {
	for _, generation := range []int{2, 3} {
		for _, reference := range []int{1, 2} {
			t.Run(fmt.Sprintf("binding=%d/reference=%d", generation, reference), func(t *testing.T) {
				resource := map[string]any{
					"metadata": map[string]any{"generation": generation},
					"spec":     map[string]any{"roleRef": map[string]any{"kind": "Role", "name": "owned-role"}},
					"status": map[string]any{
						"observedGeneration": generation,
						"resolved": map[string]any{"roleRef": map[string]any{
							"kind": "Role", "name": "owned-role", "uid": "admitted-uid", "observedGeneration": reference,
						}},
					},
				}
				encoded, err := json.Marshal(resource)
				require.NoError(t, err)
				ready, err := rbacCapacitySeedBindingResolved(encoded, map[string]string{"owned-role": "admitted-uid"}, 1)
				require.NoError(t, err)
				require.Equal(t, reference == 1, ready,
					"subject edits advance the binding generation, not its immutable role reference's pinned resolution generation")
			})
		}
	}
}

func TestRBACCapacitySeedContinuationRequiresSeparateEvidenceAndPreservesInputs(t *testing.T) {
	config := rbacCapacityNativeSeedConfig{
		Mode: "diagnostic", Count: 5, ImportBatchSize: 60,
		ResumeSeedDirectory: t.TempDir(), ReceiptDirectory: t.TempDir(),
		Plan: rbacCapacitySeedPlan{Owner: "owned", Prefix: "rbac-resume", PersonalSubject: "retained-seed",
			Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"},
		InitialAdminSubject: "bootstrap-admin",
		PrincipalTokenFiles: map[string]string{"reader-a": "a", "reader-b": "b", "reader-c": "c", "reader-d": "d"},
		SourceModelFiles:    []string{"source-a", "source-b"}, DecisionLogFiles: []string{"decisions-a", "decisions-b"},
		K6Workload: &rbacCapacityK6DiagnosticConfig{Duration: 24 * time.Second, Rate: 30, PreallocatedVUs: 1, MaxVUs: 10,
			Mutation: &rbacCapacityMutationConfig{Subject: "rbac-resume-filler-mutation", TokenFile: "holdout.jwt",
				Schedule: rbacCapacityScheduleConfig{Duration: 12 * time.Second, Drain: 5 * time.Second,
					Period: time.Minute, Rate: 50, Workers: 1, Pending: 1}}},
	}
	require.NoError(t, os.Chmod(config.ResumeSeedDirectory, 0700))
	require.NoError(t, os.Chmod(config.ReceiptDirectory, 0700))
	require.NoError(t, validateRBACCapacitySeedResume(config))
	encoded := mustRBACCapacityJSON(t, config)
	for _, scenario := range []string{"same-directory", "nested-output", "relative-input", "alpha", "read-only-mode", "no-mutations", "missing-evidence", "unbounded-workload"} {
		t.Run(scenario, func(t *testing.T) {
			var changed rbacCapacityNativeSeedConfig
			require.NoError(t, json.Unmarshal(encoded, &changed))
			switch scenario {
			case "same-directory":
				changed.ReceiptDirectory = changed.ResumeSeedDirectory
			case "nested-output":
				changed.ReceiptDirectory = filepath.Join(changed.ResumeSeedDirectory, "new")
			case "relative-input":
				changed.ResumeSeedDirectory = "relative"
			case "alpha":
				changed.Mode = "alpha"
			case "read-only-mode":
				changed.VerifyRetained = true
			case "no-mutations":
				changed.K6Workload.Mutation = nil
			case "missing-evidence":
				changed.SourceModelFiles = nil
			case "unbounded-workload":
				changed.K6Workload.Duration = time.Hour
			}
			require.Error(t, validateRBACCapacitySeedResume(changed))
		})
	}
	originals := make(map[string][]byte)
	for index := range config.Count {
		name := config.Plan.namespaceName(index) + ".json"
		originals[name] = mustRBACCapacityJSON(t, rbacCapacitySeedReceipt{
			Owner: config.Plan.Owner, Namespace: config.Plan.namespaceName(index), Complete: true,
		})
		require.NoError(t, os.WriteFile(filepath.Join(config.ResumeSeedDirectory, name), originals[name], 0600))
	}
	require.NoError(t, copyRBACCapacityRetainedSeed(config))
	require.Error(t, copyRBACCapacityRetainedSeed(config), "continuation must never overwrite a prior run")
	for name, original := range originals {
		for _, directory := range []string{config.ResumeSeedDirectory, config.ReceiptDirectory} {
			content, err := os.ReadFile(filepath.Join(directory, name))
			require.NoError(t, err)
			require.Equal(t, original, content)
		}
	}
	content, err := os.ReadFile(filepath.Join(config.ReceiptDirectory, ".retained-seed.json"))
	require.NoError(t, err)
	var provenance struct {
		ReceiptCount                                int
		ReceiptsSHA256                              string
		IndependentLiveBaselineVerificationRequired bool
	}
	require.NoError(t, json.Unmarshal(content, &provenance))
	require.Equal(t, config.Count, provenance.ReceiptCount)
	require.Len(t, provenance.ReceiptsSHA256, sha256.Size*2)
	require.True(t, provenance.IndependentLiveBaselineVerificationRequired,
		"copied admission inputs are not a new native baseline or acceptance pass")
	symlink := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(config.ResumeSeedDirectory, symlink))
	config.ResumeSeedDirectory, config.ReceiptDirectory = symlink, t.TempDir()
	require.Error(t, copyRBACCapacityRetainedSeed(config), "never follow an aliased retained directory")
}

func TestRBACCapacityRetainedVerificationRequiresCompleteReadOnlyReplay(t *testing.T) {
	config := rbacCapacityNativeSeedConfig{
		VerifyRetained: true, Mode: "diagnostic", Count: 1000, ImportBatchSize: 60,
		Plan: rbacCapacitySeedPlan{Owner: "owned", Prefix: "owned-retained", PersonalSubject: "personal-owner",
			Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"},
		InitialAdminSubject: "bootstrap-admin", PrincipalTokenFiles: map[string]string{
			"reader-a": "token-a", "reader-b": "token-b", "reader-c": "token-c", "reader-d": "token-d",
		},
		SourceModelFiles: []string{"source-a", "source-b"}, DecisionLogFiles: []string{"decisions-a", "decisions-b"},
		DecisionWorkload: &rbacCapacityDecisionWorkloadConfig{
			Schedule: rbacCapacityScheduleConfig{Duration: 500 * time.Second, Rate: 40,
				Drain: 5 * time.Second, Workers: 4, Pending: 16},
			GroupMembers: []string{"reader-a"},
		},
	}
	require.NoError(t, validateRBACCapacityRetainedVerification(config))
	conditionalRole := config
	conditionalRole.Plan.MutationSubject = config.Plan.Prefix + "-filler-mutation"
	require.NoError(t, validateRBACCapacityRetainedVerification(conditionalRole),
		"a declared conditional Role holdout is authored seed state, not permission to mutate a retained fixture")
	require.NoError(t, validateRBACCapacityMutationSelection(conditionalRole),
		"the native entry-point selection must accept the same explicitly read-only retained plan")
	encoded := mustRBACCapacityJSON(t, conditionalRole)
	for _, scenario := range []string{"alpha", "small-import", "missing-sources", "missing-decisions", "missing-principals",
		"missing-root-intent", "missing-workload", "incomplete-rotation", "propagation", "k6", "conditional-root", "conditional-role"} {
		t.Run(scenario, func(t *testing.T) {
			var changed rbacCapacityNativeSeedConfig
			require.NoError(t, json.Unmarshal(encoded, &changed))
			switch scenario {
			case "alpha":
				changed.Mode = "alpha"
			case "small-import":
				changed.ImportBatchSize = 10
			case "missing-sources":
				changed.SourceModelFiles = changed.SourceModelFiles[:1]
			case "missing-decisions":
				changed.DecisionLogFiles = nil
			case "missing-principals":
				delete(changed.PrincipalTokenFiles, "reader-a")
			case "missing-root-intent":
				changed.InitialAdminSubject = ""
			case "missing-workload":
				changed.DecisionWorkload = nil
			case "incomplete-rotation":
				changed.DecisionWorkload.Schedule.Duration -= time.Second
			case "propagation":
				changed.DecisionWorkload.PropagationDiagnostic = true
			case "k6":
				changed.K6Workload = &rbacCapacityK6DiagnosticConfig{}
			case "conditional-root":
				changed.Plan.ConditionalRoot = true
			case "conditional-role":
				changed.Plan.MutationSubject = "holdout"
			}
			require.Error(t, validateRBACCapacityRetainedVerification(changed))
			require.Error(t, validateRBACCapacityMutationSelection(changed),
				"static holdouts must not bypass read-only retained selection guards")
		})
	}
	config.VerifyRetained = false
	config.Mode, config.DecisionWorkload = "alpha", nil
	require.NoError(t, validateRBACCapacityRetainedVerification(config), "ordinary seeding retains its existing selection rules")
}

func TestRBACCapacityNativeProbeTracesRequireExactRequestAndSourceJoins(t *testing.T) {
	expected := map[string]rbacCapacityProbeExpectation{
		"owned-request": {Subject: "ordinary-reader", Namespace: "owned-namespace", Action: "product.list", Allowed: true},
	}
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{
		{TierUID: "root-uid", GitRevision: "root-commit", Digest: "root-digest"}:                {Namespace: "root", Root: true},
		{TierUID: "namespace-uid", GitRevision: "namespace-commit", Digest: "namespace-digest"}: {Namespace: "owned-namespace"},
	}
	decision := `{"request_id":"owned-request","action":"product.list","subject":"ordinary-reader",
		"resource_namespace":"owned-namespace","resource_kind":"product","outcome":"allow","provider":"opa",
		"authorization_tiers":[
			{"tierID":"root-uid","gitRevision":"root-commit","contentDigest":"root-digest"},
			{"tierID":"namespace-uid","gitRevision":"namespace-commit","contentDigest":"namespace-digest"}]}`
	// Each structured decision occupies one physical log line.
	decision = strings.ReplaceAll(decision, "\n", "") + "\n"
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"complete", "native test preamble\n" + decision, true},
		{"missing-decision", "", false},
		{"different-subject", strings.Replace(decision, "ordinary-reader", "administrator", 1), false},
		{"different-namespace", strings.Replace(decision, "owned-namespace", "other-namespace", 1), false},
		{"different-verdict", strings.Replace(decision, `"outcome":"allow"`, `"outcome":"deny"`, 1), false},
		{"unknown-source", strings.Replace(decision, "namespace-digest", "unverified-digest", 1), false},
		{"duplicate-decision", decision + decision, false},
		{"cross-tier-source", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(decision,
			"namespace-uid", "root-uid"), "namespace-commit", "root-commit"), "namespace-digest", "root-digest"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRBACCapacityProbeTraces(strings.NewReader(tc.input), expected, models)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				var unverified *rbacCapacityUnverifiedSourceError
				if tc.name == "unknown-source" {
					require.ErrorAs(t, err, &unverified)
					require.Equal(t, "owned-request", unverified.RequestID,
						"only an unjoined warm-up probe may be reissued; other verified probes remain pinned")
				} else {
					require.NotErrorAs(t, err, &unverified,
						"wrong outcomes, foreign scopes and malformed evidence must not become warm-up retries")
				}
			}
		})
	}
	denied := map[string]rbacCapacityProbeExpectation{
		"owned-request": {Subject: "ordinary-reader", Namespace: "owned-namespace", Action: "product.list"},
	}
	require.NoError(t, checkRBACCapacityProbeTraces(strings.NewReader(
		strings.Replace(decision, `"outcome":"allow"`, `"outcome":"deny"`, 1)), denied, models))
	role := map[string]rbacCapacityProbeExpectation{
		"owned-request": {Subject: "ordinary-reader", Namespace: "owned-namespace", Action: "role.list", Allowed: true},
	}
	require.NoError(t, checkRBACCapacityProbeTraces(strings.NewReader(strings.ReplaceAll(
		strings.ReplaceAll(decision, "product.list", "role.list"), `"resource_kind":"product"`, `"resource_kind":"role"`)), role, models))
}

func TestRBACCapacityFullTierProbeJoinsRequireEveryDeclaredDecisionAndSource(t *testing.T) {
	for _, count := range []int{1000, 10005} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			testRBACCapacityFullTierProbeJoins(t, count)
		})
	}
}

func testRBACCapacityFullTierProbeJoins(t *testing.T, count int) {
	t.Helper()
	expected := make(map[string]rbacCapacityProbeExpectation, count)
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{
		{TierUID: "root-uid", GitRevision: "root-commit", Digest: "root-digest"}: {Namespace: "root", Root: true},
	}
	var log bytes.Buffer
	var lastRevision rbacCapacityPolicyRevision
	var beforeLast int
	for index := range count {
		namespace := fmt.Sprintf("owned-%04d", index%1000)
		id := fmt.Sprintf("ordinary-%05d", index)
		expected[id] = rbacCapacityProbeExpectation{
			Subject: "ordinary-reader", Namespace: namespace, Action: "product.list", Allowed: true,
		}
		lastRevision = rbacCapacityPolicyRevision{TierUID: namespace, GitRevision: "namespace-commit", Digest: "namespace-digest"}
		models[lastRevision] = rbacCapacityProbeSource{Namespace: namespace}
		beforeLast = log.Len()
		fmt.Fprintf(&log, `{"request_id":%q,"action":"product.list","subject":"ordinary-reader","resource_namespace":%q,"resource_kind":"product","outcome":"allow","provider":"opa","authorization_tiers":[{"tierID":"root-uid","gitRevision":"root-commit","contentDigest":"root-digest"},{"tierID":%q,"gitRevision":"namespace-commit","contentDigest":"namespace-digest"}]}`+"\n",
			id, namespace, namespace)
	}
	require.Error(t, checkRBACCapacityProbeTraces(bytes.NewReader(log.Bytes()), expected, models),
		"the original small diagnostic bound must remain unchanged")
	require.NoError(t, checkRBACCapacityObservedTracesWithBounds(bytes.NewReader(log.Bytes()), expected, models, count, 1<<30, nil))
	require.Error(t, checkRBACCapacityObservedTracesWithBounds(bytes.NewReader(log.Bytes()), expected, models, count-1, 1<<30, nil))
	require.Error(t, checkRBACCapacityObservedTracesWithBounds(bytes.NewReader(log.Bytes()[:beforeLast]), expected, models, count, 1<<30, nil),
		"readiness must join the last Namespace, not truncate at a smaller probe bound")
	delete(models, lastRevision)
	require.Error(t, checkRBACCapacityObservedTracesWithBounds(bytes.NewReader(log.Bytes()), expected, models, count, 1<<30, nil),
		"every Namespace source remains independently required at full scale")
}

func TestRBACCapacityScheduledTracesVerifyEveryOfferWithoutRetainingRequestMaps(t *testing.T) {
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{
		{TierUID: "root-uid", GitRevision: "root-commit", Digest: "root-digest"}:                {Namespace: "root", Root: true},
		{TierUID: "namespace-uid", GitRevision: "namespace-commit", Digest: "namespace-digest"}: {Namespace: "owned-namespace"},
	}
	cases := make([]rbacCapacityRequest, 10)
	var log bytes.Buffer
	for index := range cases {
		_, expected := rbacCapacityCase(int64(index), 1)
		cases[index] = rbacCapacityRequest{Subject: "ordinary-reader", Namespace: "owned-namespace",
			Action: "product.list", Expected: expected}
	}
	for iteration := range 300 {
		outcome := "deny"
		if cases[iteration%len(cases)].Expected == "allow" {
			outcome = "allow"
		}
		require.NoError(t, json.NewEncoder(&log).Encode(map[string]any{
			"ts":         1000 + float64(iteration)/1000,
			"request_id": fmt.Sprintf("owned-ordinary-0-%d", iteration), "action": "product.list",
			"subject": "ordinary-reader", "resource_namespace": "owned-namespace",
			"resource_kind": "product", "outcome": outcome, "provider": "opa",
			"latency_ns": 1000 + iteration,
			"authorization_tiers": []map[string]string{
				{"tierID": "root-uid", "gitRevision": "root-commit", "contentDigest": "root-digest"},
				{"tierID": "namespace-uid", "gitRevision": "namespace-commit", "contentDigest": "namespace-digest"},
			},
		}))
	}
	var observed int
	accounting, err := checkRBACCapacityScheduledTraces(bytes.NewReader(log.Bytes()), "owned", 0, 300, cases, models,
		func(vector rbacCapacityPolicyVector, request rbacCapacityRequest, at time.Time, latency time.Duration) error {
			require.NotEmpty(t, vector.Cluster.Digest)
			require.NotEmpty(t, vector.Namespace.Digest)
			require.NotEmpty(t, request.ID)
			require.False(t, at.IsZero())
			require.Equal(t, time.Duration(1000+observed), latency, "source-joined timing must retain producer nanosecond precision for every offer")
			observed++
			return nil
		})
	require.NoError(t, err)
	require.NoError(t, accounting.validate(300, false))
	require.Equal(t, 300, observed)
	require.EqualValues(t, 180, accounting.Allowed)
	require.EqualValues(t, 90, accounting.Denied)
	require.EqualValues(t, 30, accounting.Invalid)
	plan := rbacCapacitySeedPlan{Owner: "owned", Prefix: "owned-seed", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"}
	native, err := plan.request(0, 2, []string{"reader-a"})
	require.NoError(t, err)
	nativeReceipt, err := json.Marshal(map[string]string{"request_id": native.ID, "action": native.Action})
	require.NoError(t, err)
	mixed := string(nativeReceipt) + "\n" + log.String()
	_, err = checkRBACCapacityScheduledTraces(strings.NewReader(mixed), "owned", 0, 300, cases, models, nil)
	require.NoError(t, err, "native companion requests must not alias the k6 scheduled-offer namespace")
	first, _, _ := strings.Cut(log.String(), "\n")
	for _, input := range []string{
		log.String() + first + "\n",
		strings.Replace(log.String(), "namespace-digest", "unknown-digest", 1),
		strings.Replace(log.String(), "owned-ordinary-0-0", "owned-ordinary-0-300", 1),
		strings.Replace(log.String(), "owned-ordinary-0-0", "owned-ordinary-0-00", 1),
		strings.Replace(log.String(), `"latency_ns":1000,`, "", 1),
		strings.Replace(log.String(), `"latency_ns":1000,`, `"latency_ns":-1,`, 1),
		strings.Replace(log.String(), `"latency_ns":1000,`, `"latency_ns":0.5,`, 1),
		log.String()[len(first)+1:],
	} {
		_, err := checkRBACCapacityScheduledTraces(strings.NewReader(input), "owned", 0, 300, cases, models, nil)
		require.Error(t, err, "aggregate counters cannot hide duplicate, missing or unverified ordinary decisions")
	}
}

func TestRBACCapacityObservedTracesCannotRedefineCommittedPolicyOrRestartClocks(t *testing.T) {
	now := time.Unix(1000, 0)
	root := rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "root-commit", Digest: "verified-root"}
	grant := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "grant", Digest: "verified-grant"}
	revoke := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "revoke", Digest: "verified-revoke"}
	regrant := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "regrant", Digest: "verified-regrant"}
	alias := grant
	alias.Digest = "verified-status-alias"
	oracle := newRBACCapacityOracle(5 * time.Second)
	for index, revision := range []rbacCapacityPolicyRevision{grant, revoke, regrant} {
		require.NoError(t, oracle.commit(rbacCapacityPolicyVector{Cluster: root, Namespace: revision},
			now, now.Add(time.Duration(index)*time.Second), map[string]bool{"reader/product.list": index != 1}))
	}
	require.NoError(t, oracle.commit(rbacCapacityPolicyVector{Cluster: root, Namespace: alias}, now, now,
		map[string]bool{"reader/product.list": true}))
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{root: {Namespace: "root", Root: true}}
	for _, revision := range []rbacCapacityPolicyRevision{grant, revoke, regrant, alias} {
		models[revision] = rbacCapacityProbeSource{Namespace: "owned-namespace"}
	}
	for _, tc := range []struct {
		name     string
		revision rbacCapacityPolicyRevision
		allowed  bool
		at       time.Time
		valid    bool
	}{
		{"original-grant", grant, true, now, true},
		{"bounded-old-alias", alias, true, now.Add(3 * time.Second), true},
		{"revoke", revoke, false, now.Add(time.Second), true},
		{"regrant", regrant, true, now.Add(3 * time.Second), true},
		{"wrong-verdict-with-matching-HTTP", revoke, true, now.Add(3 * time.Second), false},
		{"late-original-alias", alias, true, now.Add(7 * time.Second), false},
		{"future-policy", regrant, true, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := "deny"
			if tc.allowed {
				outcome = "allow"
			}
			decision, err := json.Marshal(map[string]any{
				"request_id": "observed", "action": "product.list", "subject": "reader",
				"resource_namespace": "owned-namespace", "resource_kind": "product", "outcome": outcome, "provider": "opa",
				"authorization_tiers": []map[string]string{
					{"tierID": root.TierUID, "gitRevision": root.GitRevision, "contentDigest": root.Digest},
					{"tierID": tc.revision.TierUID, "gitRevision": tc.revision.GitRevision, "contentDigest": tc.revision.Digest},
				},
			})
			require.NoError(t, err)
			expected := map[string]rbacCapacityProbeExpectation{"observed": {
				Subject: "reader", Namespace: "owned-namespace", Action: "product.list", Allowed: tc.allowed, ObservedAt: tc.at,
			}}
			called := 0
			err = checkRBACCapacityObservedTraces(bytes.NewReader(append(decision, '\n')), expected, models,
				func(vector rbacCapacityPolicyVector, id string, allowed bool) error {
					called++
					probe := expected[id]
					return oracle.check(vector, probe.Subject+"/"+probe.Action, allowed, probe.ObservedAt)
				})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "matching server and HTTP outcomes cannot author their own expected policy")
			}
			require.Equal(t, 1, called, "every joined ordinary response must reach the independent committed oracle")
		})
	}
}

func TestRBACCapacityDynamicHistoryJoinsSealedSourcesAndSkipsPendingAliases(t *testing.T) {
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-history", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	receipt := rbacCapacitySeedReceipt{
		Namespace: plan.namespaceName(1), NamespaceNodeUID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/namespace-uid")),
		Roles: make(map[string]string), Bindings: make(map[string]string),
	}
	for _, resource := range plan.namespace(1).Resources {
		name := resource["metadata"].(map[string]any)["name"].(string)
		uids := receipt.Roles
		if resource["kind"] == "RoleBinding" {
			uids = receipt.Bindings
		}
		uids[name] = "uid-" + name
	}
	var sources, decisions bytes.Buffer
	sourceEncoder := json.NewEncoder(&sources)
	capturedAt := time.Unix(1000, 0).UTC()
	writeSource := func(origin, state map[string]any, resources []json.RawMessage) rbacCapacityPolicyRevision {
		header := rbacCapacitySourceHeader{ModelVersion: "v1"}
		var err error
		header.Origin, err = json.Marshal(origin)
		require.NoError(t, err)
		header.State, err = json.Marshal(state)
		require.NoError(t, err)
		canonical, err := json.Marshal(header)
		require.NoError(t, err)
		hash := sha256.New()
		_, _ = hash.Write(canonical)
		for _, resource := range resources {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write(resource)
		}
		header.ContentDigest = hex.EncodeToString(hash.Sum(nil))
		require.NoError(t, sourceEncoder.Encode(map[string]any{"type": "begin", "snapshot": header, "resourceCount": len(resources), "observedAt": capturedAt}))
		for _, resource := range resources {
			require.NoError(t, sourceEncoder.Encode(map[string]any{"type": "resource", "resource": resource}))
		}
		require.NoError(t, sourceEncoder.Encode(map[string]any{"type": "end", "digest": header.ContentDigest}))
		return rbacCapacityPolicyRevision{
			TierUID: state["tierID"].(string), GitRevision: origin["gitRevision"].(string), Digest: header.ContentDigest,
		}
	}
	root := rbacCapacityRootPolicy{NamespaceUID: "root-uid", BindingUID: "admin-binding-uid", AdminSubject: "bootstrap-admin"}
	var err error
	root.TreeDigest, err = rbacCapacityRootTreeDigest(root.AdminSubject)
	require.NoError(t, err)
	binding := rbacCapacityInitialRootManifest(root.AdminSubject)
	binding["metadata"].(map[string]any)["uid"] = root.BindingUID
	binding["metadata"].(map[string]any)["generation"] = 1
	binding["status"] = map[string]any{"observedGeneration": 1, "resolved": map[string]any{
		"roleRef": map[string]any{"kind": "ClusterRole", "name": "cluster-admin", "uid": "builtin-admin", "observedGeneration": 1},
	}}
	encoded, err := json.Marshal(binding)
	require.NoError(t, err)
	rootOrigin := map[string]any{
		"namespace": "gitstore-system", "namespaceUID": root.NamespaceUID, "gitRevision": "root-commit",
		"root": true, "active": true, "treeDigest": root.TreeDigest,
	}
	rootState := map[string]any{"tierID": "cluster", "roleCount": 0, "bindingCount": 1}
	cluster := writeSource(rootOrigin, rootState, []json.RawMessage{encoded})
	capturedAt = time.Unix(999, 0).UTC()
	require.Equal(t, cluster, writeSource(rootOrigin, rootState, []json.RawMessage{encoded}))
	capturedAt = time.Unix(1001, 0).UTC()
	require.Equal(t, cluster, writeSource(rootOrigin, rootState, []json.RawMessage{encoded}))
	var history []rbacCapacityAuthoredAdmission
	var pendingRevision, laggingRevision, staleGeneration, resolvedRevocation, latestNamespace, reorderedRevision rbacCapacityPolicyRevision
	expected := make(map[string]rbacCapacityProbeExpectation)
	for step, allowed := range []bool{true, false, true} {
		namespace := plan.namespace(1)
		if !allowed {
			namespace.Resources[10]["spec"].(map[string]any)["subjects"] = []any{
				map[string]any{"kind": "User", "name": plan.Prefix + "-filler-propagation"},
			}
		}
		admitted := receipt
		admitted.GitRevision = fmt.Sprintf("main@sha1:%040d", step+1)
		at := time.Unix(1000+int64(step), 0)
		capturedAt = at
		history = append(history, rbacCapacityAuthoredAdmission{
			Namespace: namespace, Receipt: admitted, AcknowledgedAt: at,
			Cases:       map[string]bool{"reader-b/product.list": allowed},
			Generations: map[string]int64{"Role/capacity-role-00": 1, "RoleBinding/capacity-binding-00": int64(step + 1)},
			References:  map[string]int64{"RoleBinding/capacity-binding-00": 1},
		})
		var resources []json.RawMessage
		for _, resource := range namespace.Resources {
			data, err := json.Marshal(resource)
			require.NoError(t, err)
			var source map[string]any
			require.NoError(t, json.Unmarshal(data, &source))
			metadata := source["metadata"].(map[string]any)
			name := metadata["name"].(string)
			generation := 1
			uids := receipt.Roles
			if resource["kind"] == "RoleBinding" {
				uids = receipt.Bindings
				if name == "capacity-binding-00" {
					generation += step
				}
				status := map[string]any{"observedGeneration": generation}
				ref := source["spec"].(map[string]any)["roleRef"].(map[string]any)
				if ref["name"] != "capacity-unresolved" {
					roleUID := receipt.Roles[ref["name"].(string)]
					if ref["kind"] == "ClusterRole" {
						roleUID = "builtin-view"
					}
					status["resolved"] = map[string]any{"roleRef": map[string]any{
						"kind": ref["kind"], "name": ref["name"], "uid": roleUID, "observedGeneration": 1,
					}}
				}
				source["status"] = status
			}
			metadata["uid"], metadata["generation"] = uids[name], generation
			encoded, err := json.Marshal(source)
			require.NoError(t, err)
			resources = append(resources, encoded)
		}
		origin := map[string]any{"namespace": namespace.Name, "namespaceUID": "namespace-uid", "gitRevision": admitted.GitRevision}
		state := map[string]any{"tierID": "namespace-uid", "roleCount": 10, "bindingCount": 50,
			"projection": map[string]any{"gitRevision": admitted.GitRevision}}
		revision := writeSource(origin, state, resources)
		latestNamespace = revision
		if step == 1 {
			pending := append([]json.RawMessage(nil), resources...)
			var unresolved map[string]any
			require.NoError(t, json.Unmarshal(pending[10], &unresolved))
			unresolved["status"].(map[string]any)["observedGeneration"] = 1
			pending[10], err = json.Marshal(unresolved)
			require.NoError(t, err)
			laggingRevision = writeSource(origin, state, pending)
			resolution := unresolved["status"].(map[string]any)["resolved"]
			delete(unresolved["status"].(map[string]any), "resolved")
			pending[10], err = json.Marshal(unresolved)
			require.NoError(t, err)
			pendingRevision = writeSource(origin, state, pending)
			unresolved["status"].(map[string]any)["resolved"] = resolution
			resolvedRevocation = revision
			metadata := unresolved["metadata"].(map[string]any)
			metadata["generation"] = 1
			unresolved["status"].(map[string]any)["resolved"].(map[string]any)["roleRef"].(map[string]any)["observedGeneration"] = 1
			pending[10], err = json.Marshal(unresolved)
			require.NoError(t, err)
			staleGeneration = writeSource(origin, state, pending)
			reordered := append([]json.RawMessage(nil), resources...)
			reordered[0], reordered[1] = reordered[1], reordered[0]
			reorderedRevision = writeSource(origin, state, reordered)
		}
		id := fmt.Sprintf("request-%d", step)
		expected[id] = rbacCapacityProbeExpectation{
			Subject: "reader-b", Namespace: namespace.Name, Action: "product.list", Allowed: allowed, ObservedAt: at.Add(time.Millisecond),
		}
		outcome := "deny"
		if allowed {
			outcome = "allow"
		}
		require.NoError(t, json.NewEncoder(&decisions).Encode(map[string]any{
			"request_id": id, "action": "product.list", "subject": "reader-b", "resource_namespace": namespace.Name,
			"resource_kind": "product", "provider": "opa", "outcome": outcome, "authorization_tiers": []map[string]string{
				{"tierID": cluster.TierUID, "gitRevision": cluster.GitRevision, "contentDigest": cluster.Digest},
				{"tierID": revision.TierUID, "gitRevision": revision.GitRevision, "contentDigest": revision.Digest},
			},
		}))
	}
	directory := t.TempDir()
	sourcePath, decisionPath := filepath.Join(directory, "sources.jsonl"), filepath.Join(directory, "decisions.jsonl")
	require.NoError(t, os.WriteFile(sourcePath, sources.Bytes(), 0600))
	require.NoError(t, os.WriteFile(decisionPath, decisions.Bytes(), 0600))
	require.NoError(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected))
	seeds := map[string]rbacCapacitySeedReceipt{receipt.Namespace: history[0].Receipt}
	static := readRBACCapacityProbeSources(t, sourcePath, plan, 2, seeds, root)
	require.NotContains(t, static, latestNamespace)
	require.Equal(t, time.Unix(999, 0).UTC(), static[cluster].ObservedAt,
		"verified duplicate vectors must retain the earliest capture clock even when records arrive out of clock order")
	bounded := readRBACCapacityProbeSources(t, sourcePath, plan, 2, seeds, root, time.Unix(999, int64(500*time.Millisecond)))
	require.Len(t, bounded, 1, "verified-vector caching must not admit sources captured beyond the selected evidence window")
	require.Contains(t, bounded, cluster)
	dynamic := readRBACCapacityProbeSourcesWithHistory(t, sourcePath, plan, 2, seeds, root, history)
	require.Contains(t, dynamic, latestNamespace, "ordinary decisions cannot discard a concurrently admitted policy revision")
	require.Contains(t, dynamic, laggingRevision, "immutable reference truth remains usable across subject-only edits")
	require.Contains(t, dynamic, reorderedRevision, "bounded per-slot proof caches must reverify reordered resource content")
	require.NotContains(t, dynamic, staleGeneration)
	require.NotContains(t, dynamic, pendingRevision)
	changed := strings.Replace(decisions.String(), resolvedRevocation.Digest, laggingRevision.Digest, 1)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.NoError(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected),
		"current subjects and a verified immutable role reference establish authorization before controller acknowledgement")
	changed = strings.Replace(decisions.String(), resolvedRevocation.Digest, pendingRevision.Digest, 1)
	require.NotEqual(t, decisions.String(), changed)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.Error(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected),
		"a matching Git commit and HTTP denial cannot promote a pending source candidate into a verified policy")
	changed = strings.Replace(decisions.String(), resolvedRevocation.Digest, staleGeneration.Digest, 1)
	require.NotEqual(t, decisions.String(), changed)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.Error(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected),
		"self-consistent binding status cannot certify a generation older than the independently authored admission")

	rootHistory := []rbacCapacityAuthoredRootAdmission{{
		Policy: root, GitRevision: cluster.GitRevision, BaselineKnownAt: time.Unix(999, 0),
		Cases: map[string]bool{"reader-b/product.list": false},
	}}
	var rootGrantAlias, initialPendingRoot, rootRevocation, laggingRoot, pendingRoot, staleRoot, forgedRootReference, forgedRootRole, unknownRoot, removedRoot, purgedRoot, liveAfterRemoval rbacCapacityPolicyRevision
	for step := 1; step <= 3; step++ {
		subject := "reader-b"
		if step >= 2 {
			subject = "filler"
		}
		manifest := map[string]any{
			"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRoleBinding",
			"metadata": map[string]any{"name": "capacity-global-view"},
			"spec": map[string]any{
				"roleRef":  map[string]any{"kind": "ClusterRole", "name": "view"},
				"subjects": []any{map[string]any{"kind": "User", "name": subject}},
			},
		}
		admission := rbacCapacityAuthoredRootAdmission{
			Policy: root, GitRevision: fmt.Sprintf("root-commit-%d", step),
			AcknowledgedAt: time.Unix(1000+int64(step), int64(100*time.Millisecond)),
			Resources: []rbacCapacityAuthoredRootResource{{
				Manifest: manifest, UID: "global-binding-uid", RoleUID: "builtin-view",
				Generation: int64(step), ReferenceGeneration: 1,
			}},
			Cases: map[string]bool{"reader-b/product.list": step == 1},
		}
		if step == 3 {
			admission.Resources[0].Generation = 2
			admission.Resources[0].Removed = true
		}
		if step == 1 {
			admission.PendingCases = map[string]bool{"reader-b/product.list": false}
		}
		admission.Policy.TreeDigest, err = rbacCapacityAuthoredRootTreeDigest(root.AdminSubject, admission.Resources)
		require.NoError(t, err)
		rootHistory = append(rootHistory, admission)
		authored, err := json.Marshal(manifest)
		require.NoError(t, err)
		var source map[string]any
		require.NoError(t, json.Unmarshal(authored, &source))
		metadata := source["metadata"].(map[string]any)
		metadata["uid"], metadata["generation"] = "global-binding-uid", step
		source["status"] = map[string]any{
			"observedGeneration": step, "resolved": map[string]any{"roleRef": map[string]any{
				"kind": "ClusterRole", "name": "view", "uid": "builtin-view", "observedGeneration": 1,
			}},
		}
		if step == 3 {
			metadata["generation"] = 2
			metadata["deletionTimestamp"] = admission.AcknowledgedAt
			source["status"].(map[string]any)["observedGeneration"] = 2
		}
		resource, err := json.Marshal(source)
		require.NoError(t, err)
		origin := map[string]any{
			"namespace": "gitstore-system", "namespaceUID": root.NamespaceUID, "gitRevision": admission.GitRevision,
			"root": true, "active": true, "treeDigest": admission.Policy.TreeDigest,
		}
		state := map[string]any{"tierID": "cluster", "roleCount": 0, "bindingCount": 2}
		revision := writeSource(origin, state, []json.RawMessage{encoded, resource})
		if step == 1 {
			resolution := source["status"]
			source["status"] = map[string]any{}
			pending, err := json.Marshal(source)
			require.NoError(t, err)
			initialPendingRoot = writeSource(origin, state, []json.RawMessage{encoded, pending})
			source["status"] = resolution
			metadata["resourceVersion"] = "late-source-alias"
			alias, err := json.Marshal(source)
			require.NoError(t, err)
			rootGrantAlias = writeSource(origin, state, []json.RawMessage{encoded, alias})
			revision = rootGrantAlias
		} else if step == 2 {
			rootRevocation = revision
			status := source["status"].(map[string]any)
			status["observedGeneration"] = 1
			pending, err := json.Marshal(source)
			require.NoError(t, err)
			laggingRoot = writeSource(origin, state, []json.RawMessage{encoded, pending})
			resolution := status["resolved"]
			delete(status, "resolved")
			pending, err = json.Marshal(source)
			require.NoError(t, err)
			pendingRoot = writeSource(origin, state, []json.RawMessage{encoded, pending})
			status["resolved"] = resolution
			metadata["generation"] = 1
			stale, err := json.Marshal(source)
			require.NoError(t, err)
			staleRoot = writeSource(origin, state, []json.RawMessage{encoded, stale})
			metadata["generation"], status["observedGeneration"] = 2, 2
			status["resolved"].(map[string]any)["roleRef"].(map[string]any)["observedGeneration"] = 2
			forged, err := json.Marshal(source)
			require.NoError(t, err)
			forgedRootReference = writeSource(origin, state, []json.RawMessage{encoded, forged})
			ref := status["resolved"].(map[string]any)["roleRef"].(map[string]any)
			ref["observedGeneration"], ref["uid"] = 1, "counterfeit-view"
			counterfeit, err := json.Marshal(source)
			require.NoError(t, err)
			forgedRootRole = writeSource(origin, state, []json.RawMessage{encoded, counterfeit})
			origin["gitRevision"] = "undeclared-root-commit"
			unknownRoot = writeSource(origin, state, []json.RawMessage{encoded, resource})
		} else {
			removedRoot = revision
			state["bindingCount"] = 1
			purgedRoot = writeSource(origin, state, []json.RawMessage{encoded})
			state["bindingCount"] = 2
			delete(metadata, "deletionTimestamp")
			live, err := json.Marshal(source)
			require.NoError(t, err)
			liveAfterRemoval = writeSource(origin, state, []json.RawMessage{encoded, live})
		}
		id := fmt.Sprintf("dynamic-root-%d", step)
		expected[id] = rbacCapacityProbeExpectation{
			Subject: "reader-b", Namespace: receipt.Namespace, Action: "product.list", Allowed: step == 1,
			ObservedAt: admission.AcknowledgedAt.Add(time.Millisecond),
		}
		outcome := "deny"
		if step == 1 {
			outcome = "allow"
		}
		require.NoError(t, json.NewEncoder(&decisions).Encode(map[string]any{
			"request_id": id, "action": "product.list", "subject": "reader-b", "resource_namespace": receipt.Namespace,
			"resource_kind": "product", "provider": "opa", "outcome": outcome, "authorization_tiers": []map[string]string{
				{"tierID": revision.TierUID, "gitRevision": revision.GitRevision, "contentDigest": revision.Digest},
				{"tierID": resolvedRevocation.TierUID, "gitRevision": resolvedRevocation.GitRevision, "contentDigest": resolvedRevocation.Digest},
			},
		}))
	}
	require.NoError(t, os.WriteFile(sourcePath, sources.Bytes(), 0600))
	require.NoError(t, os.WriteFile(decisionPath, decisions.Bytes(), 0600))
	require.NoError(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, expected))
	t.Run("static-probes-before-root-mutations", func(t *testing.T) {
		verifyRBACCapacityProbeTraces(t, sourcePath, decisionPath, plan, 2, seeds, root,
			map[string]rbacCapacityProbeExpectation{"request-0": expected["request-0"]},
			time.Unix(1000, int64(500*time.Millisecond)))
	})
	rootCatalog := readRBACCapacityProbeSourcesWithRootHistory(t, sourcePath, plan, 2, seeds, root, history, rootHistory)
	for _, revision := range []rbacCapacityPolicyRevision{
		cluster, rootGrantAlias, initialPendingRoot, rootRevocation, laggingRoot, removedRoot, purgedRoot,
	} {
		require.Contains(t, rootCatalog, revision, "ordinary replay must retain complete and independently validated pending-creation aliases")
	}
	for _, revision := range []rbacCapacityPolicyRevision{
		pendingRoot, staleRoot, forgedRootReference, forgedRootRole, unknownRoot, liveAfterRemoval,
	} {
		require.NotContains(t, rootCatalog, revision, "k6 source catalog must not admit forged, undeclared or live-after-removal root aliases")
	}
	for _, tc := range []struct {
		name    string
		at      time.Time
		allowed bool
		valid   bool
	}{
		{"initial-unresolved", rootHistory[1].AcknowledgedAt.Add(time.Millisecond), false, true},
		{"unresolved-grant", rootHistory[1].AcknowledgedAt.Add(time.Millisecond), true, false},
		{"unresolved-future", rootHistory[1].AcknowledgedAt.Add(-time.Nanosecond), false, false},
		{"unresolved-expired", rootHistory[1].AcknowledgedAt.Add(5*time.Second + time.Nanosecond), false, false},
	} {
		probes := make(map[string]rbacCapacityProbeExpectation, len(expected)+1)
		for id, probe := range expected {
			probes[id] = probe
		}
		probes[tc.name] = rbacCapacityProbeExpectation{
			Subject: "reader-b", Namespace: receipt.Namespace, Action: "product.list", Allowed: tc.allowed, ObservedAt: tc.at,
		}
		log := bytes.NewBuffer(append([]byte(nil), decisions.Bytes()...))
		outcome := "deny"
		if tc.allowed {
			outcome = "allow"
		}
		require.NoError(t, json.NewEncoder(log).Encode(map[string]any{
			"request_id": tc.name, "action": "product.list", "subject": "reader-b", "resource_namespace": receipt.Namespace,
			"resource_kind": "product", "provider": "opa", "outcome": outcome, "authorization_tiers": []map[string]string{
				{"tierID": initialPendingRoot.TierUID, "gitRevision": initialPendingRoot.GitRevision, "contentDigest": initialPendingRoot.Digest},
				{"tierID": resolvedRevocation.TierUID, "gitRevision": resolvedRevocation.GitRevision, "contentDigest": resolvedRevocation.Digest},
			},
		}))
		require.NoError(t, os.WriteFile(decisionPath, log.Bytes(), 0600))
		err := verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, probes)
		if tc.valid {
			require.NoError(t, err, "a separately authored unresolved initial state contributes no grant within its original acknowledgement window")
		} else {
			require.Error(t, err, "unresolved state must not grant, predate admission, or reset the five-second acknowledgement clock")
		}
	}
	changed = strings.Replace(decisions.String(), rootRevocation.Digest, laggingRoot.Digest, 1)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.NoError(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, expected),
		"root subject edits retain verified reference identity without waiting for a later controller status write")
	changed = strings.Replace(decisions.String(), removedRoot.Digest, purgedRoot.Digest, 1)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.NoError(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, expected),
		"the same authored removal permits both its pinned terminating row and its later physical absence")
	changed = strings.Replace(decisions.String(), removedRoot.Digest, liveAfterRemoval.Digest, 1)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.Error(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, expected),
		"a retained live binding cannot certify removal merely because the canonical Git manifest is absent")
	for _, invalid := range []rbacCapacityPolicyRevision{pendingRoot, staleRoot, forgedRootReference, forgedRootRole, unknownRoot} {
		changed := strings.Replace(decisions.String(), rootRevocation.Digest, invalid.Digest, 1)
		if invalid == unknownRoot {
			changed = strings.ReplaceAll(changed, rootRevocation.GitRevision, unknownRoot.GitRevision)
		}
		require.NotEqual(t, decisions.String(), changed)
		require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
		require.Error(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, expected),
			"root decisions require declared commits and independently verified ready generations and immutable role references")
	}
	conflicting := append([]rbacCapacityAuthoredRootAdmission(nil), rootHistory...)
	conflicting[1].Cases = map[string]bool{"different-principal/product.list": true}
	require.NoError(t, os.WriteFile(decisionPath, decisions.Bytes(), 0600))
	require.Error(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, conflicting, expected),
		"root and Namespace admissions must independently declare the same probe domain")
	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"future-root", rootHistory[1].AcknowledgedAt.Add(-time.Nanosecond)},
		{"stale-root-new-namespace", rootHistory[2].AcknowledgedAt.Add(5*time.Second + time.Nanosecond)},
	} {
		probes := make(map[string]rbacCapacityProbeExpectation, len(expected)+1)
		for id, probe := range expected {
			probes[id] = probe
		}
		namespace := resolvedRevocation
		if tc.name == "stale-root-new-namespace" {
			namespace = latestNamespace
		}
		probes[tc.name] = rbacCapacityProbeExpectation{
			Subject: "reader-b", Namespace: receipt.Namespace, Action: "product.list", Allowed: true, ObservedAt: tc.at,
		}
		corrupt := bytes.NewBuffer(append([]byte(nil), decisions.Bytes()...))
		require.NoError(t, json.NewEncoder(corrupt).Encode(map[string]any{
			"request_id": tc.name, "action": "product.list", "subject": "reader-b", "resource_namespace": receipt.Namespace,
			"resource_kind": "product", "provider": "opa", "outcome": "allow", "authorization_tiers": []map[string]string{
				{"tierID": rootGrantAlias.TierUID, "gitRevision": rootGrantAlias.GitRevision, "contentDigest": rootGrantAlias.Digest},
				{"tierID": namespace.TierUID, "gitRevision": namespace.GitRevision, "contentDigest": namespace.Digest},
			},
		}))
		require.NoError(t, os.WriteFile(decisionPath, corrupt.Bytes(), 0600))
		require.Error(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, rootHistory, probes),
			"root source aliases must retain their own admission and supersession clocks across mixed Namespace vectors")
	}
	t.Run("root-under-load-untouched-Namespace", func(t *testing.T) {
		namespace := plan.namespace(5)
		seed := rbacCapacitySeedReceipt{
			Owner: plan.Owner, Namespace: namespace.Name, Complete: true,
			NamespaceNodeUID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/untouched-uid")),
			GitRevision:      "main@sha1:" + strings.Repeat("a", 40), Roles: make(map[string]string), Bindings: make(map[string]string),
		}
		seed.Admissions = []rbacCapacitySeedAdmission{{GitRevision: seed.GitRevision, AcknowledgedAt: time.Unix(1000, 0), Resources: 60}}
		for _, resource := range namespace.Resources {
			name := resource["metadata"].(map[string]any)["name"].(string)
			uids := seed.Roles
			if resource["kind"] == "RoleBinding" {
				uids = seed.Bindings
			}
			uids[name] = "untouched-" + name
		}
		subject := plan.Prefix + "-filler-mutation"
		baseline, err := rbacCapacityRootNamespaceBaseline(plan, map[string]rbacCapacitySeedReceipt{seed.Namespace: seed}, subject)
		require.NoError(t, err)
		require.Equal(t, seed.Admissions[0].AcknowledgedAt, baseline.AcknowledgedAt)
		require.Len(t, baseline.Generations, 60)
		require.Len(t, baseline.References, 50)
		var resources []json.RawMessage
		for _, manifest := range namespace.Resources {
			data, err := json.Marshal(manifest)
			require.NoError(t, err)
			var source map[string]any
			require.NoError(t, json.Unmarshal(data, &source))
			metadata := source["metadata"].(map[string]any)
			uids := seed.Roles
			if source["kind"] == "RoleBinding" {
				uids = seed.Bindings
				ref := source["spec"].(map[string]any)["roleRef"].(map[string]any)
				status := map[string]any{"observedGeneration": 1}
				if ref["name"] != "capacity-unresolved" {
					uid := seed.Roles[ref["name"].(string)]
					if ref["kind"] == "ClusterRole" {
						uid = "builtin-view"
					}
					status["resolved"] = map[string]any{"roleRef": map[string]any{
						"kind": ref["kind"], "name": ref["name"], "uid": uid, "observedGeneration": 1,
					}}
				}
				source["status"] = status
			}
			metadata["uid"], metadata["generation"] = uids[metadata["name"].(string)], 1
			resource, err := json.Marshal(source)
			require.NoError(t, err)
			resources = append(resources, resource)
		}
		selectedRoot := append([]rbacCapacityAuthoredRootAdmission(nil), rootHistory[:2]...)
		for index := range selectedRoot {
			selectedRoot[index].Cases = maps.Clone(baseline.Cases)
			if index > 0 {
				selectedRoot[index].PendingCases = maps.Clone(baseline.Cases)
			}
		}
		capturedAt = time.Unix(1003, 0)
		persist := func(resources []json.RawMessage) {
			revision := writeSource(map[string]any{
				"namespace": seed.Namespace, "namespaceUID": "untouched-uid", "gitRevision": seed.GitRevision,
			}, map[string]any{
				"tierID": "untouched-uid", "roleCount": 10, "bindingCount": 50,
				"projection": map[string]any{"gitRevision": seed.GitRevision},
			}, resources)
			require.NoError(t, os.WriteFile(sourcePath, sources.Bytes(), 0600))
			decision, err := json.Marshal(map[string]any{
				"request_id": "root-holdout", "action": "product.list", "subject": subject,
				"resource_namespace": seed.Namespace, "resource_kind": "product", "provider": "opa", "outcome": "deny",
				"authorization_tiers": []map[string]string{
					{"tierID": rootGrantAlias.TierUID, "gitRevision": rootGrantAlias.GitRevision, "contentDigest": rootGrantAlias.Digest},
					{"tierID": revision.TierUID, "gitRevision": revision.GitRevision, "contentDigest": revision.Digest},
				},
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(decisionPath, append(decision, '\n'), 0600))
		}
		probes := map[string]rbacCapacityProbeExpectation{"root-holdout": {
			Subject: subject, Namespace: seed.Namespace, Action: "product.list", Allowed: false, ObservedAt: capturedAt.Add(time.Millisecond),
		}}
		persist(resources)
		require.NoError(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath,
			[]rbacCapacityAuthoredAdmission{baseline}, root, selectedRoot, probes),
			"root-only replay still requires every authored Namespace generation and immutable reference pin")
		for _, mutation := range []struct {
			index int
			edit  func(map[string]any)
		}{
			{0, func(resource map[string]any) { resource["metadata"].(map[string]any)["generation"] = 2 }},
			{10, func(resource map[string]any) {
				resource["status"].(map[string]any)["resolved"].(map[string]any)["roleRef"].(map[string]any)["observedGeneration"] = 2
			}},
		} {
			corrupt := slices.Clone(resources)
			var resource map[string]any
			require.NoError(t, json.Unmarshal(corrupt[mutation.index], &resource))
			mutation.edit(resource)
			corrupt[mutation.index], err = json.Marshal(resource)
			require.NoError(t, err)
			persist(corrupt)
			require.Error(t, verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath,
				[]rbacCapacityAuthoredAdmission{baseline}, root, selectedRoot, probes),
				"the untouched Namespace cannot acquire newer generations or refreshed reference truth")
		}
		seed.baseline = &rbacCapacitySeedBaseline{Generation: 2}
		_, err = rbacCapacityRootNamespaceBaseline(plan, map[string]rbacCapacitySeedReceipt{seed.Namespace: seed}, subject)
		require.Error(t, err, "an advanced baseline is not the untouched root-under-load tier")
	})
}

func TestRBACCapacityAuthoredRootTreeUsesCanonicalBindingContent(t *testing.T) {
	manifest := map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "global-view", "labels": map[string]any{"fixture": "owned"}},
		"spec": map[string]any{
			"roleRef": map[string]any{"kind": "ClusterRole", "name": "view"},
			"subjects": []any{
				map[string]any{"kind": "User", "name": "zulu"},
				map[string]any{"kind": "User", "name": "alice"},
			},
		},
	}
	files := map[string]string{
		"rbac/clusterrolebindings/global-view.yaml": `apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: ClusterRoleBinding
metadata:
    name: global-view
    labels:
        fixture: owned
spec:
    roleRef:
        kind: ClusterRole
        name: view
    subjects:
        - kind: User
          name: alice
        - kind: User
          name: zulu
`,
		"rbac/clusterrolebindings/initial-admin.yaml": `apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: ClusterRoleBinding
metadata:
    name: initial-admin
spec:
    roleRef:
        kind: ClusterRole
        name: cluster-admin
    subjects:
        - kind: User
          name: bootstrap-admin
`,
	}
	tree := sha256.New()
	for _, path := range []string{"rbac/clusterrolebindings/global-view.yaml", "rbac/clusterrolebindings/initial-admin.yaml"} {
		entry := sha256.Sum256([]byte(files[path]))
		_, _ = fmt.Fprintf(tree, "%d:%s:%x\n", len(path), path, entry)
	}
	actual, err := rbacCapacityAuthoredRootTreeDigest("bootstrap-admin", []rbacCapacityAuthoredRootResource{{Manifest: manifest}})
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(tree.Sum(nil)), actual,
		"independently authored trees must normalize JSON encoding, metadata field order and subject order before hashing")
}

func TestRBACCapacityAuthoredRootTreeIncludesMutableClusterRoleRules(t *testing.T) {
	manifest := map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": "global-reader"},
		"spec": map[string]any{"rules": []any{map[string]any{
			"resources": []string{"role", "product", "product"}, "verbs": []string{"read", "list"},
		}}},
	}
	role := `apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: ClusterRole
metadata:
    name: global-reader
spec:
    rules:
        - resources:
            - product
            - role
          verbs:
            - list
            - read
`
	admin, err := rbacCapacityCanonicalRootBinding(rbacCapacityInitialRootManifest("bootstrap-admin"))
	require.NoError(t, err)
	tree := sha256.New()
	for _, file := range []struct{ path, content string }{
		{"rbac/clusterrolebindings/initial-admin.yaml", string(admin)},
		{"rbac/clusterroles/global-reader.yaml", role},
	} {
		entry := sha256.Sum256([]byte(file.content))
		_, _ = fmt.Fprintf(tree, "%d:%s:%x\n", len(file.path), file.path, entry)
	}
	digest, err := rbacCapacityAuthoredRootTreeDigest("bootstrap-admin",
		[]rbacCapacityAuthoredRootResource{{Manifest: manifest}})
	require.NoError(t, err)
	require.Equal(t, hex.EncodeToString(tree.Sum(nil)), digest)
	manifest["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["verbs"] = []string{"read"}
	revoked, err := rbacCapacityAuthoredRootTreeDigest("bootstrap-admin",
		[]rbacCapacityAuthoredRootResource{{Manifest: manifest}})
	require.NoError(t, err)
	require.NotEqual(t, digest, revoked, "a changed ClusterRole rule must not alias the previous root permission vector")
}

func TestRBACCapacityPendingRootCreationDoesNotReinterpretEarlierReferences(t *testing.T) {
	binding := rbacCapacityAuthoredRootResource{
		Manifest: map[string]any{"kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "new-binding"}},
		UID:      "new-binding-uid", Generation: 1, ReferenceGeneration: 1, RoleUID: "new-role-uid",
	}
	baseline := rbacCapacityAuthoredRootAdmission{Cases: map[string]bool{"ordinary/product.list": false}}
	creation := rbacCapacityAuthoredRootAdmission{PendingCases: maps.Clone(baseline.Cases),
		Resources: []rbacCapacityAuthoredRootResource{binding}}
	require.NoError(t, validateRBACCapacityPendingRootCreation(baseline, creation))
	removed := binding
	removed.Manifest = map[string]any{"kind": "ClusterRoleBinding", "metadata": map[string]any{"name": "old-binding"}}
	removed.UID, removed.Generation, removed.Removed = "old-binding-uid", 3, true
	baseline.Resources = []rbacCapacityAuthoredRootResource{removed}
	role := rbacCapacityAuthoredRootResource{
		Manifest: map[string]any{"kind": "ClusterRole", "metadata": map[string]any{"name": "new-role"}},
		UID:      "new-role-uid", Generation: 1,
	}
	creation.Resources = []rbacCapacityAuthoredRootResource{removed, role, binding}
	require.NoError(t, validateRBACCapacityPendingRootCreation(baseline, creation),
		"a later new root grant can be unresolved without pretending an earlier binding lost its reference")
	baseline.Resources[0].Removed = false
	require.Error(t, validateRBACCapacityPendingRootCreation(baseline, creation), "an earlier live reference cannot become a pending alias")
	baseline.Resources[0].Removed = true
	creation.Resources[2].Generation = 2
	require.Error(t, validateRBACCapacityPendingRootCreation(baseline, creation))
	creation.Resources[2].Generation = 1
	creation.PendingCases["ordinary/product.list"] = true
	require.Error(t, validateRBACCapacityPendingRootCreation(baseline, creation), "pending source cannot invent new grant truth")
}

func TestRBACCapacityMutableRootSourcePinsRoleGenerationAndOriginalBindingReference(t *testing.T) {
	role := map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": "global-reader"},
		"spec":     map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"list"}}}},
	}
	binding := map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "global-reader-binding"},
		"spec": map[string]any{
			"roleRef":  map[string]any{"kind": "ClusterRole", "name": "global-reader"},
			"subjects": []any{map[string]any{"kind": "User", "name": "ordinary-reader"}},
		},
	}
	admission := rbacCapacityAuthoredRootAdmission{
		Policy:      rbacCapacityRootPolicy{NamespaceUID: "root-uid", BindingUID: "admin-uid", AdminSubject: "bootstrap-admin"},
		GitRevision: "main@sha1:" + strings.Repeat("a", 40),
		Resources: []rbacCapacityAuthoredRootResource{
			{Manifest: role, UID: "role-uid", Generation: 2},
			{Manifest: binding, UID: "binding-uid", Generation: 1, ReferenceGeneration: 1, RoleUID: "role-uid"},
		},
	}
	var err error
	admission.Policy.TreeDigest, err = rbacCapacityAuthoredRootTreeDigest(admission.Policy.AdminSubject, admission.Resources)
	require.NoError(t, err)
	origin, err := json.Marshal(map[string]any{
		"namespace": "gitstore-system", "namespaceUID": "root-uid", "root": true, "active": true,
		"gitRevision": admission.GitRevision, "treeDigest": admission.Policy.TreeDigest,
	})
	require.NoError(t, err)
	state := json.RawMessage(`{"tierID":"cluster","roleCount":1,"bindingCount":2}`)
	for _, tc := range []struct {
		name                  string
		roleGen, referenceGen int64
		referenceUID          string
		complete              bool
	}{
		{"current role with original reference", 2, 1, "role-uid", true},
		{"older role generation", 1, 1, "role-uid", false},
		{"invented reference generation", 2, 2, "role-uid", false},
		{"replacement role incarnation", 2, 1, "replacement-role", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encode := func(manifest map[string]any, uid string, generation int64, status any) json.RawMessage {
				resource := maps.Clone(manifest)
				metadata := maps.Clone(manifest["metadata"].(map[string]any))
				metadata["uid"], metadata["generation"] = uid, generation
				resource["metadata"] = metadata
				if status != nil {
					resource["status"] = status
				}
				content, err := json.Marshal(resource)
				require.NoError(t, err)
				return content
			}
			resolved := func(name, uid string, generation int64) any {
				return map[string]any{"observedGeneration": 1, "resolved": map[string]any{
					"roleRef": map[string]any{"kind": "ClusterRole", "name": name, "uid": uid, "observedGeneration": generation},
				}}
			}
			resources := []json.RawMessage{
				encode(rbacCapacityInitialRootManifest("bootstrap-admin"), "admin-uid", 1, resolved("cluster-admin", "release-admin-role", 1)),
				encode(role, "role-uid", tc.roleGen, nil),
				encode(binding, "binding-uid", 1, resolved("global-reader", tc.referenceUID, tc.referenceGen)),
			}
			require.Equal(t, tc.complete, checkRBACCapacityAuthoredRootSource(t,
				rbacCapacitySourceHeader{Origin: origin, State: state}, resources, admission))
		})
	}
}

func TestRBACCapacityRootSourceRequiresAuthoredGrantAndPinnedIncarnations(t *testing.T) {
	expected := rbacCapacityRootPolicy{
		NamespaceUID: "root-incarnation", BindingUID: "binding-incarnation", AdminSubject: "bootstrap-admin",
	}
	var err error
	expected.TreeDigest, err = rbacCapacityRootTreeDigest(expected.AdminSubject)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, subject, rootUID, bindingUID string
		extra, pending                     bool
	}{
		{name: "authored", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID},
		{name: "different-administrator", subject: "ordinary-reader", rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID},
		{name: "recreated-root", subject: expected.AdminSubject, rootUID: "replacement-root", bindingUID: expected.BindingUID},
		{name: "recreated-binding", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: "replacement-binding"},
		{name: "additional-root-policy", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID, extra: true},
		{name: "unresolved", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := rbacCapacityInitialRootManifest(tc.subject)
			manifest["metadata"].(map[string]any)["uid"] = tc.bindingUID
			manifest["metadata"].(map[string]any)["generation"] = 1
			if !tc.pending {
				manifest["status"] = map[string]any{
					"observedGeneration": 1,
					"resolved": map[string]any{"roleRef": map[string]any{
						"kind": "ClusterRole", "name": "cluster-admin", "uid": "builtin-role", "observedGeneration": 1,
					}},
				}
			}
			encoded, err := json.Marshal(manifest)
			require.NoError(t, err)
			resources := []json.RawMessage{encoded}
			if tc.extra {
				resources = append(resources, encoded)
			}
			tree, err := rbacCapacityRootTreeDigest(tc.subject)
			require.NoError(t, err)
			origin, err := json.Marshal(map[string]any{"namespace": "gitstore-system", "namespaceUID": tc.rootUID,
				"root": true, "active": true, "treeDigest": tree})
			require.NoError(t, err)
			state, err := json.Marshal(map[string]any{"tierID": "cluster", "roleCount": 0, "bindingCount": len(resources)})
			require.NoError(t, err)
			complete, err := checkRBACCapacityRootSource(rbacCapacitySourceHeader{Origin: origin, State: state}, resources, expected)
			if tc.name == "authored" || tc.pending {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.name == "authored", complete)
		})
	}
}

func TestRBACCapacityMaximumRootBatchPreservesGrantsAndExactWireBounds(t *testing.T) {
	plan := rbacCapacitySeedPlan{Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"}
	role := map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRole",
		"metadata": map[string]any{"name": plan.Prefix + "-root-rule", "labels": map[string]any{rbacCapacityOwnerLabel: plan.Owner}},
		"spec":     map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"list"}}}},
	}
	binding := rbacCapacityInitialRootManifest("holdout")
	binding["metadata"] = map[string]any{"name": plan.Prefix + "-root-rule-binding", "labels": map[string]any{rbacCapacityOwnerLabel: plan.Owner}}
	binding["spec"].(map[string]any)["roleRef"] = map[string]any{"kind": "ClusterRole", "name": plan.Prefix + "-root-rule"}
	input := []rbacCapacityAuthoredRootResource{{Manifest: role, Generation: 1}, {Manifest: binding, Generation: 1, ReferenceGeneration: 1}}
	before, err := json.Marshal(input)
	require.NoError(t, err)
	batch, err := rbacCapacityMaximumRootBatch(plan, input)
	require.NoError(t, err)
	require.Len(t, batch, 256)
	require.Equal(t, input, batch[:2], "padding cannot change the actual grant or its reference")
	directory, err := os.MkdirTemp(".", ".rbac-root-wire-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	git := rbacNativeGit{dir: directory}
	_, err = git.command(t, "init", "--initial-branch=main")
	require.NoError(t, err)
	_, err = git.command(t, "commit", "--allow-empty", "--no-gpg-sign", "-m", "Establish owned root boundary fixture")
	require.NoError(t, err)
	baseline, err := git.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	bytesWritten := 0
	names := map[string]bool{}
	for index, resource := range batch {
		name := resource.Manifest["metadata"].(map[string]any)["name"].(string)
		path := "rbac/" + strings.ToLower(resource.Manifest["kind"].(string)) + "s/" + name + ".yaml"
		git.write(t, path, resource.Manifest)
		wire, err := os.ReadFile(filepath.Join(directory, path))
		require.NoError(t, err)
		require.True(t, json.Valid(wire), "the existing writer commits JSON into YAML-named manifest paths")
		require.LessOrEqual(t, len(wire), 64<<10)
		require.NoError(t, checkRBACCapacityRootRestorationBytes(resource, wire),
			"cleanup must accept the exact bytes produced by the real authoring helper")
		bytesWritten += len(wire)
		require.False(t, names[name])
		names[name] = true
		require.EqualValues(t, 1, resource.Generation)
		if index >= 2 {
			require.Equal(t, "ClusterRole", resource.Manifest["kind"], "padding must not introduce another grant")
			require.Zero(t, resource.ReferenceGeneration)
			_, err := rbacCapacityCanonicalRootRole(resource.Manifest)
			require.NoError(t, err)
		}
	}
	require.Equal(t, 4<<20, bytesWritten, "measure files written by the real authoring helper, not a parallel serializer")
	revision := git.commit(t)
	changed, err := git.command(t, "diff", "--name-only", strings.TrimSpace(string(baseline)), revision)
	require.NoError(t, err)
	require.Len(t, strings.Fields(string(changed)), 256, "the admitted commit must change exactly 256 manifest paths")
	tree, err := git.command(t, "ls-tree", "-r", "-l", revision)
	require.NoError(t, err)
	entries := strings.Split(strings.TrimSpace(string(tree)), "\n")
	require.Len(t, entries, 256)
	committedBytes := 0
	for _, entry := range entries {
		fields := strings.Fields(entry)
		require.Len(t, fields, 5)
		require.Equal(t, "blob", fields[1])
		size, err := strconv.Atoi(fields[3])
		require.NoError(t, err)
		require.LessOrEqual(t, size, 64<<10)
		actual, err := os.ReadFile(filepath.Join(directory, fields[4]))
		require.NoError(t, err)
		require.Len(t, actual, size, "committed blob size must match the actual submitted file")
		committedBytes += size
	}
	require.Equal(t, 4<<20, committedBytes, "the Git tree itself must contain exactly four MiB of manifest bytes")
	rolePath := "rbac/clusterroles/" + plan.Prefix + "-root-rule.yaml"
	committed, err := git.command(t, "show", revision+":"+rolePath)
	require.NoError(t, err)
	require.NoError(t, checkRBACCapacityRootRestorationBytes(batch[0], committed))
	require.Error(t, checkRBACCapacityRootRestorationBytes(batch[0], append(slices.Clone(committed), '\n')),
		"semantically equivalent JSON is still byte drift, not the exact authored canonical file")
	drift := maps.Clone(role)
	drift["spec"] = map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"delete"}}}}
	git.write(t, rolePath, drift)
	actual, err := os.ReadFile(filepath.Join(directory, rolePath))
	require.NoError(t, err)
	require.Error(t, checkRBACCapacityRootRestorationBytes(batch[0], actual), "real manifest changes must still stop cleanup")
	git.write(t, rolePath, role)
	actual, err = os.ReadFile(filepath.Join(directory, rolePath))
	require.NoError(t, err)
	require.Equal(t, committed, actual)
	require.NoError(t, checkRBACCapacityRootRestorationBytes(batch[0], actual))
	after, err := json.Marshal(input)
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = rbacCapacityMaximumRootBatch(plan, input[:1])
	require.Error(t, err)
	role["metadata"].(map[string]any)["annotations"] = map[string]any{"oversized": strings.Repeat("x", 64<<10)}
	_, err = rbacCapacityMaximumRootBatch(plan, input)
	require.Error(t, err, "oversized grant manifests cannot be hidden by reducing padding")
}

func TestRBACCapacitySeedPlanKeepsManagedOwnerSlotAndIsolatesSubjects(t *testing.T) {
	content, err := os.ReadFile("../../shared/schemas/authorization-actions.json")
	require.NoError(t, err)
	var catalog struct{ Actions []struct{ Action string } }
	require.NoError(t, json.Unmarshal(content, &catalog))
	actions := make(map[string]bool)
	for _, action := range catalog.Actions {
		actions[action.Action] = true
	}
	require.NotEmpty(t, actions)
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	require.NoError(t, plan.validate())
	personal := plan.namespace(0)
	require.Equal(t, "personal-owner", personal.Name)
	require.Equal(t, "USER", personal.Tier)
	require.Len(t, personal.Resources, 59, "the controller, not the seeder, owns the fiftieth binding")
	next := plan.namespace(1)
	require.Equal(t, "ORGANIZATION", next.Tier)
	require.NotEqual(t, personal.Name, next.Name)
	require.Len(t, next.Resources, 60)
	for _, namespace := range []rbacCapacitySeedNamespace{personal, next} {
		seen := make(map[string]bool)
		counts := make(map[string]int)
		for _, resource := range namespace.Resources {
			kind := resource["kind"].(string)
			metadata := resource["metadata"].(map[string]any)
			key := kind + "/" + metadata["name"].(string)
			require.False(t, seen[key])
			seen[key] = true
			counts[kind]++
			require.Equal(t, namespace.Name, metadata["namespace"])
			require.NotEqual(t, "namespace-owner-access", metadata["name"])
			require.Equal(t, plan.Owner, metadata["labels"].(map[string]any)[rbacCapacityOwnerLabel])
			content, err := json.Marshal(resource)
			require.NoError(t, err)
			require.Less(t, len(content), 64<<10)
			if kind == "Role" {
				rules := resource["spec"].(map[string]any)["rules"].([]any)
				for _, rule := range rules {
					value := rule.(map[string]any)
					for _, name := range value["resources"].([]string) {
						for _, verb := range value["verbs"].([]string) {
							require.True(t, actions[name+"."+verb], "seeded rules must use the current action catalog")
						}
					}
				}
			}
		}
		require.Equal(t, 10, counts["Role"])
		require.Equal(t, len(namespace.Resources)-10, counts["RoleBinding"])
	}
	firstBinding := func(namespace rbacCapacitySeedNamespace) map[string]any {
		return namespace.Resources[10]["spec"].(map[string]any)
	}
	require.NotEqual(t, firstBinding(personal)["subjects"], firstBinding(next)["subjects"],
		"adjacent namespaces must not grant the same rotating user")
	conditional := next.Resources[1]["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	require.Equal(t, []any{map[string]any{"attr": "namespace", "op": "eq", "value": next.Name}}, conditional["when"])
	require.Equal(t, "Group", next.Resources[11]["spec"].(map[string]any)["subjects"].([]any)[0].(map[string]any)["kind"])
	require.Equal(t, "ClusterRole", next.Resources[12]["spec"].(map[string]any)["roleRef"].(map[string]any)["kind"])
	require.Equal(t, "capacity-unresolved", next.Resources[13]["spec"].(map[string]any)["roleRef"].(map[string]any)["name"])
}

func TestRBACCapacitySeedPlanRejectsUnsafeIdentityAndScope(t *testing.T) {
	valid := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	for _, change := range []func(*rbacCapacitySeedPlan){
		func(p *rbacCapacitySeedPlan) { p.Owner = "" },
		func(p *rbacCapacitySeedPlan) { p.Prefix = "../operator" },
		func(p *rbacCapacitySeedPlan) { p.PersonalSubject = "gitstore-system" },
		func(p *rbacCapacitySeedPlan) { p.PersonalSubject = p.namespace(1).Name },
		func(p *rbacCapacitySeedPlan) { p.Subjects[1] = p.Subjects[0] },
		func(p *rbacCapacitySeedPlan) { p.Subjects[1] = p.PersonalSubject },
		func(p *rbacCapacitySeedPlan) { p.Subjects[1] = p.Prefix + "-filler-0001-04" },
		func(p *rbacCapacitySeedPlan) { p.Group = "" },
	} {
		broken := valid
		broken.Subjects = append([]string(nil), valid.Subjects...)
		change(&broken)
		require.Error(t, broken.validate())
	}
	seen := make(map[string]bool)
	for index := range 1000 {
		name := valid.namespace(index).Name
		require.False(t, seen[name], fmt.Sprintf("namespace %d must be unique", index))
		seen[name] = true
	}
}

func TestRBACCapacitySeedReadbackKeepsIndependentPolicySemantics(t *testing.T) {
	authored := map[string]any{"rules": []any{map[string]any{
		"resources": []string{"role", "product"}, "verbs": []string{"read", "list"},
		"when": []any{map[string]any{"attr": "namespace", "op": "eq", "value": "owned-namespace"}},
	}}}
	actual := json.RawMessage(`{"rules":[{"resources":["product","role"],"verbs":["list","read"],"resourceNames":null,
		"when":[{"token":null,"attribute":{"attr":"namespace","op":"eq","value":"owned-namespace","values":null}}]}]}`)
	before, err := json.Marshal(authored)
	require.NoError(t, err)
	expected := rbacCapacitySeedSpec(t, authored, true)
	require.Equal(t, expected, rbacCapacitySeedSpec(t, actual, false))
	after, err := json.Marshal(authored)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "readback comparison must not rewrite the authored oracle")
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"rules":[{"resources":["product","role"],"verbs":["list","read"]}]}`),
		json.RawMessage(`{"rules":[{"resources":["product","role"],"verbs":["list","read"],"when":[{"attribute":{"attr":"namespace","op":"eq","value":"other"}}]}]}`),
	} {
		require.NotEqual(t, expected, rbacCapacitySeedSpec(t, invalid, false), "missing or different conditions are not equivalent grants")
	}
	binding := map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": "reader"},
		"subjects": []any{map[string]any{"kind": "User", "name": "expected-subject"}},
	}
	require.NotEqual(t, rbacCapacitySeedSpec(t, binding, true), rbacCapacitySeedSpec(t,
		json.RawMessage(`{"roleRef":{"kind":"ROLE","name":"reader"},"subjects":[{"kind":"USER","name":"different-subject"}]}`), false))
}

func TestRBACCapacityRequestPlanPreservesReplicaMixAndExcludesOtherGrants(t *testing.T) {
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d", "reader-e", "reader-f", "reader-g", "reader-h"},
		Group:    "capacity-readers",
	}
	groups := []string{"reader-a", "reader-e"}
	counts := [2]map[string]int{{}, {}}
	namespaces, subjects, actions, requests := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for offer := int64(0); offer < 1000*20; offer++ {
		request, err := plan.request(offer, 1000, groups)
		require.NoError(t, err)
		require.False(t, requests[request.ID])
		requests[request.ID] = true
		counts[request.Replica][request.Expected]++
		namespaces[request.Namespace], subjects[request.Subject], actions[request.Action] = true, true, true
		if request.Expected == "deny" {
			index, _ := rbacCapacityCase(offer/2, 1000)
			require.NotEqual(t, plan.Subjects[index%len(plan.Subjects)], request.Subject)
			require.NotEqual(t, plan.Subjects[(index+1)%len(plan.Subjects)], request.Subject)
			require.NotContains(t, groups, request.Subject, "an unresolved or unbound User can still have a Group grant")
		}
		if request.Expected == "isolation" {
			require.Equal(t, plan.PersonalSubject, request.Subject)
			require.NotEqual(t, plan.PersonalSubject, request.Namespace)
		}
	}
	for _, actual := range counts {
		require.Equal(t, map[string]int{"allow": 6000, "deny": 3000, "isolation": 1000}, actual)
	}
	require.Len(t, namespaces, 1000)
	require.Len(t, subjects, 9)
	require.Equal(t, map[string]bool{"product.list": true, "role.list": true}, actions)
	_, err := plan.request(-1, 1000, groups)
	require.Error(t, err)
	_, err = plan.request(0, 1, groups)
	require.Error(t, err, "isolation probes require another owned Namespace")
	_, err = plan.request(0, 1000, []string{"not-an-authored-principal"})
	require.Error(t, err)
	_, err = plan.request(12, 1000, plan.Subjects)
	require.Error(t, err, "a fixture without an independently ungranted principal cannot produce expected denials")
}

func TestRBACCapacityK6WorkingSetComesFromTheAuthoredPlan(t *testing.T) {
	plan := rbacCapacitySeedPlan{Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers"}
	tokens := map[string]string{"personal-owner": "owner-token", "reader-a": "token-a",
		"reader-b": "token-b", "reader-c": "token-c", "reader-d": "token-d"}
	fixture, err := makeRBACCapacityK6WorkingSet(plan, 4, []string{"reader-a"}, tokens)
	require.NoError(t, err)
	require.Equal(t, plan.Owner, fixture.Owner)
	require.Len(t, fixture.Cases, 40)
	for index, sample := range fixture.Cases {
		expected, err := plan.request(int64(index)*2, 4, []string{"reader-a"})
		require.NoError(t, err)
		require.Equal(t, expected.Subject, sample.Subject)
		require.Equal(t, expected.Namespace, sample.Namespace)
		require.Equal(t, expected.Action, sample.Action)
		require.Equal(t, expected.Expected, sample.Expected)
		require.Equal(t, tokens[expected.Subject], sample.Token)
	}
	delete(tokens, plan.PersonalSubject)
	_, err = makeRBACCapacityK6WorkingSet(plan, 4, []string{"reader-a"}, tokens)
	require.Error(t, err, "isolation requests must use the independently enrolled ordinary owner, not an administrator fallback")
	_, err = makeRBACCapacityK6WorkingSet(plan, 4, nil, tokens)
	require.Error(t, err, "group membership must be independently declared")
}

func TestRBACCapacityIsolationCohortUsesOnlyDeclaredUnboundCredentials(t *testing.T) {
	var plan rbacCapacitySeedPlan
	require.NoError(t, json.Unmarshal([]byte(`{
		"Owner":"owned-fixture","Prefix":"rbac-cohort","PersonalSubject":"personal-owner",
		"Subjects":["reader-a","reader-b","reader-c","reader-d"],"Group":"capacity-readers",
		"IsolationSubjects":["visitor-a","visitor-b","visitor-c"]
	}`), &plan))
	require.NoError(t, plan.validate())
	tokens := map[string]string{
		"reader-a": "token-a", "reader-b": "token-b", "reader-c": "token-c", "reader-d": "token-d",
		"visitor-a": "visitor-token-a", "visitor-b": "visitor-token-b", "visitor-c": "visitor-token-c",
	}
	fixture, err := makeRBACCapacityK6WorkingSet(plan, 1000, []string{"reader-a"}, tokens)
	require.NoError(t, err, "an explicit visitor cohort must not consume the single personal owner's budget")
	counts := map[string]int{}
	for _, sample := range fixture.Cases {
		if sample.Expected != "isolation" {
			require.Contains(t, plan.Subjects, sample.Subject)
			continue
		}
		require.Contains(t, []string{"visitor-a", "visitor-b", "visitor-c"}, sample.Subject)
		require.Equal(t, tokens[sample.Subject], sample.Token)
		require.NotEqual(t, plan.PersonalSubject, sample.Namespace)
		counts[sample.Subject]++
	}
	require.Equal(t, map[string]int{"visitor-a": 334, "visitor-b": 333, "visitor-c": 333}, counts)
	delete(tokens, "visitor-c")
	_, err = makeRBACCapacityK6WorkingSet(plan, 1000, []string{"reader-a"}, tokens)
	require.Error(t, err, "each visitor needs its own enrolled credential; no administrator or owner fallback")
}

func TestRBACCapacityIsolationCohortRejectsOverlapsAndUnboundedIdentities(t *testing.T) {
	oversized := make([]string, 129)
	for index := range oversized {
		oversized[index] = fmt.Sprintf("visitor-%03d", index)
	}
	for _, visitors := range [][]string{
		{"reader-a"}, {"personal-owner"}, {"capacity-readers"}, {"visitor-a", "visitor-a"},
		{""}, {" visitor"}, {"rbac-cohort-filler-mutation"}, oversized,
	} {
		var plan rbacCapacitySeedPlan
		content, err := json.Marshal(map[string]any{
			"Owner": "owned-fixture", "Prefix": "rbac-cohort", "PersonalSubject": "personal-owner",
			"Subjects": []string{"reader-a", "reader-b", "reader-c", "reader-d"}, "Group": "capacity-readers",
			"IsolationSubjects": visitors,
		})
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(content, &plan))
		require.Error(t, plan.validate(), "visitors must be bounded and disjoint from every authored grant and mutation cohort")
	}
}

func TestRBACCapacityOrdinaryRequestsNeverCountInfrastructureOrPartialDataAsDenial(t *testing.T) {
	for _, tc := range []struct {
		name, expected, body, outcome string
		status                        int
	}{
		{"allow", "allow", `{"data":{"products":{"edges":[]}}}`, "allow", 200},
		{"deny", "deny", `{"data":{"products":null},"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "deny", 200},
		{"isolation", "isolation", `{"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "isolation", 200},
		{"wrong-allow", "deny", `{"data":{"products":{"edges":[]}}}`, "wrong", 200},
		{"wrong-denial", "allow", `{"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "wrong", 200},
		{"unavailable", "deny", `{"errors":[{"extensions":{"code":"AUTHORIZATION_UNAVAILABLE"}}]}`, "unavailable", 200},
		{"directory-unavailable", "deny", `{"errors":[{"message":"directory temporarily unavailable","extensions":{"code":"DIRECTORY_UNAVAILABLE"}}],"data":null}`, "unavailable", 200},
		{"unauthenticated", "deny", `{"errors":[{"extensions":{"code":"UNAUTHENTICATED"}}]}`, "unavailable", 200},
		{"partial-directory-error", "deny", `{"data":{"products":{"edges":[]}},"errors":[{"extensions":{"code":"DIRECTORY_UNAVAILABLE"}}]}`, "wrong", 200},
		{"partial-data", "deny", `{"data":{"products":{"edges":[]}},"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "wrong", 200},
		{"mixed-errors", "deny", `{"errors":[{"extensions":{"code":"FORBIDDEN"}},{"extensions":{"code":"INTERNAL"}}]}`, "wrong", 200},
		{"transport-forbidden", "deny", "", "unavailable", 403},
		{"transport-unavailable", "deny", "", "unavailable", 503},
		{"missing-envelope", "allow", `{"data":{}}`, "wrong", 200},
		{"missing-node", "allow", `{"data":{"products":{"edges":[{}]}}}`, "wrong", 200},
		{"malformed", "allow", `not-json`, "wrong", 200},
		{"bounded-body", "allow", strings.Repeat("x", 65537), "wrong", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/admin/graphql", r.URL.Path)
				require.Equal(t, "Bearer private-test-token", r.Header.Get("Authorization"))
				require.Equal(t, "owned-request", r.Header.Get("X-Request-ID"))
				var document struct {
					Query     string
					Variables map[string]string
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&document))
				require.Contains(t, document.Query, "products(namespace:$namespace,first:1)")
				require.Equal(t, "owned-namespace", document.Variables["namespace"])
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			request := rbacCapacityRequest{
				ID: "owned-request", Namespace: "owned-namespace", Action: "product.list", Expected: tc.expected,
			}
			require.Equal(t, tc.outcome, executeRBACCapacityRequest(t.Context(), server.Client(), server.URL, "private-test-token", request))
		})
	}
	t.Run("ordinary-role-list", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Contains(t, string(content), "roles(namespace:$namespace,first:1)")
			_, _ = io.WriteString(w, `{"data":{"roles":{"edges":[]}}}`)
		}))
		defer server.Close()
		require.Equal(t, "allow", executeRBACCapacityRequest(t.Context(), server.Client(), server.URL, "private-test-token",
			rbacCapacityRequest{ID: "owned-request", Namespace: "owned-namespace", Action: "role.list", Expected: "allow"}))
	})
}

func TestRBACCapacityScheduledOffersAndRotation(t *testing.T) {
	require.EqualValues(t, 900000, rbacCapacityScheduledOffers(30*time.Minute, 500))
	require.EqualValues(t, 1, rbacCapacityScheduledOffers(3*time.Millisecond, 500))
	require.Zero(t, rbacCapacityScheduledOffers(0, 500))
	counts := make(map[string]int)
	namespaces := make(map[int]bool)
	for offer := int64(0); offer < 10000; offer++ {
		namespace, probe := rbacCapacityCase(offer, 1000)
		counts[probe]++
		namespaces[namespace] = true
	}
	require.Equal(t, map[string]int{"allow": 6000, "deny": 3000, "isolation": 1000}, counts)
	require.Len(t, namespaces, 1000, "the working set must rotate, not repeat one namespace")
}

func TestRBACCapacityWorkingSetInterleavesNamespacesBetweenMutationAdmissions(t *testing.T) {
	for _, namespaces := range []int{1, 2, 5, 7, 31, 999, 1000} {
		t.Run(fmt.Sprint(namespaces), func(t *testing.T) {
			period := int64(namespaces * 10)
			seen := make(map[[2]int]bool)
			last := make(map[int]int64)
			mix := make(map[string]int)
			bound := int64(10*((namespaces+9)/10) + 9)
			for offer := int64(0); offer < 2*period; offer++ {
				namespace, outcome := rbacCapacityCase(offer, namespaces)
				mix[outcome]++
				if offer%10 == 9 {
					require.Equal(t, map[string]int{"allow": 6, "deny": 3, "isolation": 1}, mix)
					clear(mix)
				}
				require.GreaterOrEqual(t, namespace, 0)
				require.Less(t, namespace, namespaces)
				if previous, found := last[namespace]; found {
					require.LessOrEqual(t, offer-previous, bound,
						"a Namespace must remain observable between 4.8-second mutations, not disappear for twenty seconds at 500 requests/s")
				}
				last[namespace] = offer
				if offer < period {
					key := [2]int{namespace, int(offer % 10)}
					require.False(t, seen[key], "each Namespace/case pair must occur once per rotation")
					seen[key] = true
				}
			}
			require.Len(t, seen, namespaces*10)
		})
	}
}

func TestRBACCapacityBoundedScheduler(t *testing.T) {
	cfg := rbacCapacityScheduleConfig{Duration: 100 * time.Millisecond, Rate: 1000, Workers: 2, Pending: 16, Drain: time.Second}
	t.Run("accounts for every scheduled completion", func(t *testing.T) {
		healthy := cfg
		healthy.Duration, healthy.Rate, healthy.Pending = time.Second, 10, 10
		accounting, err := runRBACCapacitySchedule(t.Context(), healthy, func(_ context.Context, offer int64) string {
			_, probe := rbacCapacityCase(offer, 1000)
			return probe
		})
		require.NoError(t, err)
		require.NoError(t, accounting.validate(10, false))
		require.EqualValues(t, 6, accounting.Allowed)
		require.EqualValues(t, 3, accounting.Denied)
		require.EqualValues(t, 1, accounting.Invalid)
	})
	t.Run("queue saturation reports all offers without stretching scheduling", func(t *testing.T) {
		started := time.Now()
		accounting, err := runRBACCapacitySchedule(t.Context(), cfg, func(ctx context.Context, _ int64) string {
			<-ctx.Done()
			return "unavailable"
		})
		require.NoError(t, err)
		require.Less(t, time.Since(started), 2*time.Second)
		require.EqualValues(t, 100, accounting.Offered)
		require.Positive(t, accounting.Dropped)
		require.Positive(t, accounting.Unavailable)
		require.Equal(t, accounting.Offered, accounting.Completed+accounting.Dropped)
		require.Error(t, accounting.validate(100, false))
	})
	t.Run("cancellation cannot hide unscheduled offers", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		accounting, err := runRBACCapacitySchedule(ctx, cfg, func(context.Context, int64) string {
			t.Error("canceled workload must not execute")
			return "allow"
		})
		require.ErrorIs(t, err, context.Canceled)
		require.EqualValues(t, 100, accounting.Offered)
		require.EqualValues(t, 100, accounting.Dropped)
		require.Zero(t, accounting.Completed)
	})
	t.Run("invalid limits reject before dispatch", func(t *testing.T) {
		invalid := cfg
		invalid.Pending = 0
		_, err := runRBACCapacitySchedule(t.Context(), invalid, func(context.Context, int64) string {
			t.Error("invalid workload must not execute")
			return "allow"
		})
		require.Error(t, err)
	})
	t.Run("full load window remains active after the last offer", func(t *testing.T) {
		short := cfg
		short.Rate = 10
		started := time.Now()
		accounting, err := runRBACCapacitySchedule(t.Context(), short, func(context.Context, int64) string { return "allow" })
		require.NoError(t, err)
		require.EqualValues(t, 1, accounting.Completed)
		require.GreaterOrEqual(t, time.Since(started), short.Duration)
	})
}

func TestRBACCapacityAccountingRejectsLostOrMisclassifiedOffers(t *testing.T) {
	valid := rbacCapacityAccounting{Offered: 900000, Completed: 900000, Allowed: 540000, Denied: 270000, Invalid: 90000}
	require.NoError(t, valid.validate(900000, false))
	for _, tc := range []struct {
		name   string
		change func(*rbacCapacityAccounting)
	}{
		{"timer coalescing lost offers", func(a *rbacCapacityAccounting) { a.Offered-- }},
		{"unreported queued work", func(a *rbacCapacityAccounting) { a.Completed--; a.Allowed-- }},
		{"duplicate completions", func(a *rbacCapacityAccounting) { a.Completed++ }},
		{"dropped healthy work", func(a *rbacCapacityAccounting) { a.Dropped++; a.Completed--; a.Allowed-- }},
		{"infrastructure treated as denial", func(a *rbacCapacityAccounting) { a.Unavailable++; a.Denied-- }},
		{"wrong policy decision", func(a *rbacCapacityAccounting) { a.Wrong++; a.Allowed-- }},
		{"negative counter", func(a *rbacCapacityAccounting) { a.Dropped = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := valid
			tc.change(&broken)
			require.Error(t, broken.validate(900000, false))
		})
	}
	fault := valid
	fault.Unavailable, fault.Allowed = 10, fault.Allowed-10
	require.NoError(t, fault.validate(900000, true))
	fault.Dropped, fault.Completed = 1, fault.Completed-1
	fault.Allowed--
	require.Error(t, fault.validate(900000, true), "fault windows do not excuse dropped scheduled work")
}

func TestRBACCapacityOracleUsesIndependentCommittedRevisionVectors(t *testing.T) {
	now := time.Unix(1000, 0)
	cluster := rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "cluster-commit", Digest: "cluster-policy"}
	namespace := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "namespace-commit", Digest: "namespace-policy"}
	vector := rbacCapacityPolicyVector{Cluster: cluster, Namespace: namespace}
	oracle := newRBACCapacityOracle(5 * time.Second)
	require.NoError(t, oracle.commit(vector, now, now, map[string]bool{"owner": true, "outsider": false}))
	require.NoError(t, oracle.check(vector, "owner", true, now))
	require.NoError(t, oracle.check(vector, "outsider", false, now))
	require.Error(t, oracle.check(vector, "owner", false, now), "observations must not define their own expected policy")
	require.Error(t, oracle.check(vector, "unseeded", false, now), "unknown probes cannot silently become denials")
	for _, tc := range []struct {
		name   string
		change func(*rbacCapacityPolicyVector)
	}{
		{"unknown cluster commit", func(v *rbacCapacityPolicyVector) { v.Cluster.GitRevision = "unknown" }},
		{"unknown namespace commit", func(v *rbacCapacityPolicyVector) { v.Namespace.GitRevision = "unknown" }},
		{"wrong namespace UID", func(v *rbacCapacityPolicyVector) { v.Namespace.TierUID = "replacement-uid" }},
		{"different translated content", func(v *rbacCapacityPolicyVector) { v.Namespace.Digest = "wrong" }},
		{"cross-tier equal revision", func(v *rbacCapacityPolicyVector) { v.Namespace = cluster }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unknown := vector
			tc.change(&unknown)
			require.Error(t, oracle.check(unknown, "owner", true, now))
		})
	}
	require.Error(t, oracle.commit(vector, now, now, map[string]bool{"owner": false}), "a committed vector cannot be reinterpreted")
	newVector := vector
	newVector.Namespace.GitRevision, newVector.Namespace.Digest = "revocation", "revoked-policy"
	require.NoError(t, oracle.commit(newVector, now, now.Add(time.Second), map[string]bool{"owner": false, "outsider": false}))
	require.NoError(t, oracle.check(vector, "owner", true, now.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "owner", true, now.Add(6*time.Second+time.Nanosecond)),
		"an old, correctly evaluated allowance still violates the revocation bound")
	require.NoError(t, oracle.check(newVector, "owner", false, now.Add(7*time.Second)))
	require.Error(t, oracle.check(newVector, "owner", false, now), "a future commit is not evaluated evidence")
}

func TestRBACCapacityObservedPolicyIndexJoinsIndependentTierClocks(t *testing.T) {
	now := time.Unix(1000, 0)
	cluster := rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "grant", Digest: "root"}
	namespace := rbacCapacityPolicyRevision{TierUID: "namespace", GitRevision: "deny", Digest: "namespace"}
	vector := rbacCapacityPolicyVector{Cluster: cluster, Namespace: namespace}
	oracle := newRBACCapacityObservedPolicyIndex(5 * time.Second)
	cases := map[string]bool{"reader/product.list": true}
	require.NoError(t, oracle.add([]rbacCapacityPolicyRevision{cluster}, true, now, now.Add(time.Second), cases))
	cases["reader/product.list"] = false
	require.NoError(t, oracle.add([]rbacCapacityPolicyRevision{namespace}, false, now.Add(100*time.Millisecond), time.Time{}, cases))
	require.NoError(t, oracle.check(vector, "reader/product.list", true, now.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "reader/product.list", true, now.Add(6*time.Second+time.Nanosecond)))
	require.Error(t, oracle.check(vector, "reader/product.list", false, now.Add(time.Second)))
	require.Error(t, oracle.check(vector, "unknown", false, now.Add(time.Second)))
	require.Error(t, oracle.check(vector, "reader/product.list", true, now))

	alias := cluster
	alias.Digest = "verified-status"
	require.Error(t, oracle.add([]rbacCapacityPolicyRevision{alias}, true, now.Add(time.Second), now.Add(2*time.Second),
		map[string]bool{"reader/product.list": true}), "source aliases cannot refresh original clocks")
	require.Error(t, oracle.add([]rbacCapacityPolicyRevision{alias}, true, now, now.Add(time.Second),
		cases), "source aliases cannot change independently authored truth")
	require.NoError(t, oracle.add([]rbacCapacityPolicyRevision{alias}, true, now, now.Add(time.Second),
		map[string]bool{"reader/product.list": true}))
	vector.Cluster = alias
	require.NoError(t, oracle.check(vector, "reader/product.list", true, now.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "reader/product.list", true, now.Add(6*time.Second+time.Nanosecond)))
	require.Error(t, oracle.add([]rbacCapacityPolicyRevision{alias}, false, now, time.Time{}, cases))
	vector.Namespace = alias
	require.Error(t, oracle.check(vector, "reader/product.list", true, now.Add(time.Second)))
}

func TestRBACCapacityObservedPolicyIndexSupportsLongIndependentHistories(t *testing.T) {
	now := time.Unix(1000, 0)
	oracle := newRBACCapacityObservedPolicyIndex(5 * time.Second)
	revision := func(root bool, index int) rbacCapacityPolicyRevision {
		tier := "namespace"
		if root {
			tier = "cluster"
		}
		return rbacCapacityPolicyRevision{TierUID: tier, GitRevision: fmt.Sprintf("revision-%d", index), Digest: fmt.Sprintf("digest-%d", index)}
	}
	for index := 0; index <= 1500; index++ {
		for _, root := range []bool{false, true} {
			at := now.Add(time.Duration(index) * 1200 * time.Millisecond)
			if !root {
				at = at.Add(100 * time.Millisecond)
			}
			var superseded time.Time
			if index < 1500 {
				superseded = at.Add(1200 * time.Millisecond)
			}
			allowed := index%3 == 0
			if root {
				allowed = index%2 == 0
			}
			require.NoError(t, oracle.add([]rbacCapacityPolicyRevision{revision(root, index)}, root, at, superseded,
				map[string]bool{"reader/product.list": allowed}))
		}
	}
	at := now.Add(30*time.Minute + 100*time.Millisecond)
	for _, pair := range [][2]int{{1500, 1500}, {1499, 1498}, {1498, 1497}, {1496, 1496}} {
		vector := rbacCapacityPolicyVector{Cluster: revision(true, pair[0]), Namespace: revision(false, pair[1])}
		allowed := pair[0]%2 == 0 || pair[1]%3 == 0
		require.NoError(t, oracle.check(vector, "reader/product.list", allowed, at))
		require.Error(t, oracle.check(vector, "reader/product.list", !allowed, at))
	}
	require.Error(t, oracle.check(rbacCapacityPolicyVector{Cluster: revision(true, 1494), Namespace: revision(false, 1500)},
		"reader/product.list", true, at), "a newer granting Namespace cannot hide a stale root revision")
	require.Error(t, oracle.check(rbacCapacityPolicyVector{Cluster: revision(true, 1500), Namespace: revision(false, 1494)},
		"reader/product.list", true, at), "a newer granting root cannot hide a stale Namespace revision")
	vector := rbacCapacityPolicyVector{Cluster: revision(true, 1500), Namespace: revision(false, 1500)}
	vector.Cluster.Digest = "unverified"
	require.Error(t, oracle.check(vector, "reader/product.list", true, at))
}

func TestRBACCapacityOraclePreservesIntermediateRevocations(t *testing.T) {
	now := time.Unix(1000, 0)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "c", Digest: "c"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "ns", GitRevision: "grant", Digest: "grant"},
	}
	oracle := newRBACCapacityOracle(5 * time.Second)
	expected := map[string]bool{"owner": true}
	require.NoError(t, oracle.commit(vector, now, now, expected))
	expected["owner"] = false
	require.NoError(t, oracle.check(vector, "owner", true, now), "the oracle must copy caller-owned expectations")
	revoked := vector
	revoked.Namespace.GitRevision, revoked.Namespace.Digest = "revoke", "revoke"
	require.NoError(t, oracle.commit(revoked, now, now.Add(time.Second), map[string]bool{"owner": false}))
	regranted := vector
	regranted.Namespace.GitRevision, regranted.Namespace.Digest = "regrant", "regrant"
	require.NoError(t, oracle.commit(regranted, now, now.Add(2*time.Second), map[string]bool{"owner": true}))
	require.Error(t, oracle.check(vector, "owner", true, now.Add(7*time.Second)),
		"latest allow must not hide a missed intermediate revocation")
	require.NoError(t, oracle.check(regranted, "owner", true, now.Add(7*time.Second)))
	alias := vector
	alias.Cluster.Digest, alias.Namespace.Digest = "verified-cluster-status", "verified-namespace-status"
	require.Error(t, oracle.check(alias, "owner", true, now.Add(7*time.Second)),
		"matching authored Git commits alone cannot register an unverified source digest")
	require.Error(t, oracle.commit(alias, now, now.Add(3*time.Second), map[string]bool{"owner": true}),
		"a source alias cannot restart the original admission clock")
	require.Error(t, oracle.commit(alias, now, now, map[string]bool{"owner": false}),
		"a source alias cannot reinterpret independently authored expectations")
	require.NoError(t, oracle.commit(alias, now, now, map[string]bool{"owner": true}))
	require.NoError(t, oracle.commit(alias, now, now, map[string]bool{"owner": true}))
	require.NoError(t, oracle.check(alias, "owner", true, now.Add(6*time.Second)))
	require.Error(t, oracle.check(alias, "owner", true, now.Add(7*time.Second)),
		"a late source alias must not hide a previously admitted intermediate revocation")
	require.Error(t, oracle.check(alias, "owner", true, now.Add(-time.Nanosecond)))
	uncommitted := alias
	uncommitted.Namespace.GitRevision = "unadmitted"
	require.Error(t, oracle.commit(uncommitted, now, now, map[string]bool{"owner": true}),
		"only source variants of the same admitted Git vector can arrive out of order")
}

func TestRBACCapacityOracleEnforcesGrantPropagation(t *testing.T) {
	now := time.Unix(1000, 0)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "c", Digest: "c"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "ns", GitRevision: "deny", Digest: "deny"},
	}
	oracle := newRBACCapacityOracle(5 * time.Second)
	require.NoError(t, oracle.commit(vector, now, now, map[string]bool{"owner": false}))
	granted := vector
	granted.Namespace.GitRevision, granted.Namespace.Digest = "grant", "grant"
	require.NoError(t, oracle.commit(granted, now, now.Add(time.Second), map[string]bool{"owner": true}))
	require.NoError(t, oracle.check(vector, "owner", false, now.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "owner", false, now.Add(6*time.Second+time.Nanosecond)),
		"an old denial must not conceal a missed grant propagation deadline")
	require.NoError(t, oracle.check(granted, "owner", true, now.Add(7*time.Second)))
}

func TestRBACCapacityOracleDoesNotRefreshOneTierWithAnother(t *testing.T) {
	for _, changedTier := range []string{"cluster", "namespace"} {
		for _, sameOutcome := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same-outcome=%t", changedTier, sameOutcome), func(t *testing.T) {
				now := time.Unix(1000, 0)
				initial := rbacCapacityPolicyVector{
					Cluster:   rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "root-original", Digest: "root-original"},
					Namespace: rbacCapacityPolicyRevision{TierUID: "namespace", GitRevision: "ns-original", Digest: "ns-original"},
				}
				superseding, mixed := initial, initial
				if changedTier == "cluster" {
					superseding.Cluster.GitRevision, superseding.Cluster.Digest = "root-new", "root-new"
					mixed.Namespace.GitRevision, mixed.Namespace.Digest = "ns-new", "ns-new"
				} else {
					superseding.Namespace.GitRevision, superseding.Namespace.Digest = "ns-new", "ns-new"
					mixed.Cluster.GitRevision, mixed.Cluster.Digest = "root-new", "root-new"
				}
				oracle := newRBACCapacityOracle(5 * time.Second)
				clusterAt, namespaceAt := now, now
				mixedClusterAt, mixedNamespaceAt := now, now
				if changedTier == "cluster" {
					clusterAt, mixedNamespaceAt = now.Add(time.Second), now.Add(3*time.Second)
				} else {
					namespaceAt, mixedClusterAt = now.Add(time.Second), now.Add(3*time.Second)
				}
				require.NoError(t, oracle.commit(initial, now, now, map[string]bool{"reader": true}))
				require.NoError(t, oracle.commit(superseding, clusterAt, namespaceAt, map[string]bool{"reader": sameOutcome}))
				require.NoError(t, oracle.commit(mixed, mixedClusterAt, mixedNamespaceAt, map[string]bool{"reader": true}))
				require.NoError(t, oracle.check(mixed, "reader", true, now.Add(6*time.Second)))
				require.Error(t, oracle.check(mixed, "reader", true, now.Add(6*time.Second+time.Nanosecond)),
					"a newer companion tier must not restart the superseded tier's clock, even when the outcome is unchanged")

				alias := mixed
				alias.Cluster.Digest, alias.Namespace.Digest = "verified-root-alias", "verified-ns-alias"
				require.NoError(t, oracle.commit(alias, mixedClusterAt, mixedNamespaceAt, map[string]bool{"reader": true}))
				require.Error(t, oracle.check(alias, "reader", true, now.Add(7*time.Second)),
					"late verified source aliases must inherit both independent supersession clocks")
			})
		}
	}
}

func TestRBACCapacityOraclePreservesCanonicalTierAdmissions(t *testing.T) {
	now := time.Unix(1000, 0)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "root", GitRevision: "root-original", Digest: "root-original"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "namespace", GitRevision: "ns-original", Digest: "ns-original"},
	}
	oracle := newRBACCapacityOracle(5 * time.Second)
	require.NoError(t, oracle.commit(vector, now, now, map[string]bool{"reader": true}))
	alias := vector
	alias.Cluster.Digest, alias.Namespace.Digest = "root-alias", "ns-alias"
	require.Error(t, oracle.commit(alias, now.Add(time.Second), now, map[string]bool{"reader": true}),
		"a verified root alias cannot move its canonical admission time")
	require.Error(t, oracle.commit(alias, now, now.Add(time.Second), map[string]bool{"reader": true}),
		"a verified Namespace alias cannot move its canonical admission time")
	require.NoError(t, oracle.commit(alias, now, now, map[string]bool{"reader": true}))

	next := vector
	next.Cluster.GitRevision, next.Cluster.Digest = "root-next", "root-next"
	require.Error(t, oracle.commit(next, now.Add(3*time.Second), now.Add(time.Second), map[string]bool{"reader": true}))
	require.NoError(t, oracle.check(vector, "reader", true, now.Add(20*time.Second)),
		"a rejected mixed admission must not supersede either installed tier")
	require.Error(t, oracle.commit(next, now, now, map[string]bool{"reader": true}),
		"distinct canonical admissions require a strictly increasing per-tier clock")
	require.NoError(t, oracle.commit(next, now.Add(time.Second), now, map[string]bool{"reader": true}),
		"a rejected mixed admission must not leave a partial future root admission")
	namespace := next
	namespace.Namespace.GitRevision, namespace.Namespace.Digest = "ns-next", "ns-next"
	require.NoError(t, oracle.commit(namespace, now.Add(time.Second), now.Add(3*time.Second), map[string]bool{"reader": true}))
	require.Error(t, oracle.check(namespace, "reader", true, now.Add(2*time.Second)))
	require.NoError(t, oracle.check(namespace, "reader", true, now.Add(4*time.Second)))
	root := namespace
	root.Cluster.GitRevision, root.Cluster.Digest = "root-latest", "root-latest"
	require.NoError(t, oracle.commit(root, now.Add(5*time.Second), now.Add(3*time.Second), map[string]bool{"reader": true}))
	require.Error(t, oracle.check(root, "reader", true, now.Add(4*time.Second)))
	require.NoError(t, oracle.check(root, "reader", true, now.Add(5*time.Second)))

	foreign := root
	foreign.Cluster.TierUID = vector.Namespace.TierUID
	foreign.Namespace.TierUID = "other-namespace"
	require.Error(t, oracle.commit(foreign, now.Add(6*time.Second), now.Add(6*time.Second), map[string]bool{"reader": true}),
		"one tier UID cannot change between root and Namespace scope")
}

func TestRBACCapacityPrivateFixture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"cases":[]}`), 0600))
	_, err := readRBACCapacityPrivateFixture(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = readRBACCapacityPrivateFixture(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0600))
	alias := filepath.Join(root, "alias.json")
	require.NoError(t, os.Symlink(path, alias))
	_, err = readRBACCapacityPrivateFixture(alias)
	require.Error(t, err, "do not load credentials through an unverified symlink")
}

func TestRBACCapacityOwnedTopologyAndNativeCounts(t *testing.T) {
	valid := rbacCapacityTopology{Owner: "owned-fixture", NativeNamespaces: 1000, NativeRoles: 10000, NativeBindings: 50000}
	for index, role := range []string{"api", "api", "controller", "controller", "git", "scylla"} {
		process := rbacCapacityProcess{ID: string(rune('a' + index)), Role: role, Owner: valid.Owner, Epoch: "epoch", Running: true}
		if role == "controller" {
			process.Checkpoint = filepath.Join("/private/owned-fixture", process.ID)
		}
		valid.Processes = append(valid.Processes, process)
	}
	require.NoError(t, valid.validate())
	diagnostic := valid
	diagnostic.NativeNamespaces, diagnostic.NativeRoles, diagnostic.NativeBindings = 2, 20, 100
	require.NoError(t, diagnostic.validateCounts(2))
	require.Error(t, diagnostic.validate(), "diagnostic topology must never satisfy full native dataset acceptance")
	require.Error(t, diagnostic.validateCounts(1))
	clustered := valid
	clustered.Processes = append(append([]rbacCapacityProcess(nil), valid.Processes...),
		rbacCapacityProcess{ID: "scylla-b", Role: "scylla", Owner: valid.Owner, Epoch: "epoch", Running: true},
		rbacCapacityProcess{ID: "scylla-c", Role: "scylla", Owner: valid.Owner, Epoch: "epoch", Running: true})
	require.NoError(t, clustered.validate(), "shared native Scylla may be a multi-peer cluster")
	for _, tc := range []struct {
		name   string
		change func(*rbacCapacityTopology)
	}{
		{"wrong native count", func(v *rbacCapacityTopology) { v.NativeBindings-- }},
		{"foreign process", func(v *rbacCapacityTopology) { v.Processes[0].Owner = "operator" }},
		{"duplicate process", func(v *rbacCapacityTopology) { v.Processes[1].ID = v.Processes[0].ID }},
		{"missing epoch", func(v *rbacCapacityTopology) { v.Processes[0].Epoch = "" }},
		{"stopped dependency", func(v *rbacCapacityTopology) { v.Processes[5].Running = false }},
		{"shared checkpoints", func(v *rbacCapacityTopology) { v.Processes[3].Checkpoint = v.Processes[2].Checkpoint }},
		{"nested checkpoints", func(v *rbacCapacityTopology) { v.Processes[3].Checkpoint = v.Processes[2].Checkpoint + "/child" }},
		{"second Git writer", func(v *rbacCapacityTopology) {
			v.Processes = append(v.Processes, rbacCapacityProcess{ID: "other-git", Role: "git", Owner: v.Owner, Epoch: "epoch", Running: true})
		}},
		{"missing Scylla", func(v *rbacCapacityTopology) { v.Processes = v.Processes[:5] }},
		{"unknown dependency", func(v *rbacCapacityTopology) { v.Processes[5].Role = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := valid
			broken.Processes = append([]rbacCapacityProcess(nil), valid.Processes...)
			tc.change(&broken)
			require.Error(t, broken.validate())
		})
	}
}

func TestRBACCapacityEpochCountersAndFaultAttribution(t *testing.T) {
	segments := []rbacCapacityCounterSegment{
		{Epoch: "old-process", Before: 100, After: 120},
		{Epoch: "replacement", Before: 0, After: 30},
	}
	total, err := rbacCapacityCounterDelta(segments)
	require.NoError(t, err)
	require.EqualValues(t, 50, total)
	for _, invalid := range [][]rbacCapacityCounterSegment{
		{{Epoch: "same-process", Before: 100, After: 0}},
		{{Epoch: "", After: 30}},
		{{Epoch: "same"}, {Epoch: "same", After: 30}},
	} {
		_, err := rbacCapacityCounterDelta(invalid)
		require.Error(t, err)
	}
	window := rbacCapacityFaultWindow{Replica: 0, Start: time.Unix(100, 0), End: time.Unix(130, 0),
		Injected: true, Restored: true, OldEpoch: "old", NewEpoch: "new"}
	require.NoError(t, window.unavailable(0, time.Unix(110, 0), "old"))
	require.Error(t, window.unavailable(1, time.Unix(110, 0), "old"))
	require.Error(t, window.unavailable(0, time.Unix(131, 0), "new"))
	require.Error(t, window.unavailable(0, time.Unix(110, 0), "unknown"))
	window.Restored = false
	require.Error(t, window.unavailable(0, time.Unix(110, 0), "old"), "a planned allowance alone is not fault evidence")
}
