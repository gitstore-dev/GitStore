// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func rbacNativeDecisionMatches(t *testing.T, path string, offset int64, subject, name string, review rbacNativeReview) bool {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	_, err = file.Seek(offset, io.SeekStart)
	require.NoError(t, err)
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var decision struct {
			Subject, Action, Outcome string
			ResourceName             string `json:"resource_name"`
			CatalogDigest            string `json:"catalog_digest"`
			BuiltinDigest            string `json:"builtin_digest"`
			Tiers                    []struct {
				TierID, GitRevision string
				Revision            uint64
			} `json:"authorization_tiers"`
		}
		if json.Unmarshal(scanner.Bytes(), &decision) != nil || decision.Subject != subject ||
			decision.Action != "role.read" || decision.ResourceName != name || decision.Outcome != "allow" ||
			decision.CatalogDigest != review.Status.CatalogDigest || decision.BuiltinDigest != review.Status.BuiltinDigest ||
			len(decision.Tiers) != len(review.Status.Revisions) {
			continue
		}
		matches := 0
		for _, expected := range review.Status.Revisions {
			for _, actual := range decision.Tiers {
				if actual.TierID == expected.TierID && actual.GitRevision == expected.GitRevision &&
					strconv.FormatUint(actual.Revision, 10) == expected.TierRevision {
					matches++
				}
			}
		}
		if matches == len(review.Status.Revisions) {
			return true
		}
	}
	require.NoError(t, scanner.Err())
	return false
}

const rbacNativeReviewQuery = `query($input:AccessReviewInput!){
  accessReview(input:$input){subjectAccessReview{
    apiVersion kind spec{principal{subject groups} action resourceRef{id}}
    status{allowed hypothetical catalogDigest builtinDigest
      revisions{tierID namespaceUID gitRevision tierRevision capturedAt}}
  }}
}`

func (f rbacNativeFixture) create(t *testing.T, kind string, manifest map[string]any) rbacNativeResource {
	t.Helper()
	response := f.query(t, f.apis[0],
		fmt.Sprintf("mutation($input:Create%sInput!){create%s(input:$input){%s{id}}}", kind, kind, rbacNativeField(kind)),
		map[string]any{"input": manifest})
	name := manifest["metadata"].(map[string]any)["name"].(string)
	var committedUID string
	if len(response.Errors) != 0 {
		require.Len(t, response.Errors, 1)
		var partial struct {
			Extensions struct {
				Code, Phase, Commit string
				Diagnostics         []struct{ Reason, Level string }
			}
		}
		require.NoError(t, json.Unmarshal(response.Errors[0], &partial))
		require.Equal(t, "ADMISSION_REJECTED", partial.Extensions.Code)
		require.Equal(t, "POST_RECEIVE", partial.Extensions.Phase)
		require.Len(t, partial.Extensions.Diagnostics, 1)
		require.Equal(t, "RBAC_PROJECTION_PENDING", partial.Extensions.Diagnostics[0].Reason)
		require.Equal(t, "FAILURE", partial.Extensions.Diagnostics[0].Level)
		commit := partial.Extensions.Commit
		require.Len(t, commit, 40)
		_, err := hex.DecodeString(commit)
		require.NoError(t, err)
		git := f.clone(t, kind)
		_, err = git.command(t, "merge-base", "--is-ancestor", commit, "HEAD")
		require.NoError(t, err, "a pending creation must identify a real canonical commit")
		path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
		content, err := git.command(t, "show", commit+":"+path)
		require.NoError(t, err)
		var canonical struct {
			Kind     string
			Metadata struct{ UID, Name string }
		}
		require.NoError(t, yaml.Unmarshal(content, &canonical))
		require.Equal(t, kind, canonical.Kind)
		require.Equal(t, name, canonical.Metadata.Name)
		require.NotEmpty(t, canonical.Metadata.UID)
		committedUID = canonical.Metadata.UID
	}
	resource := f.await(t, kind, name, false)
	if committedUID != "" {
		require.Equal(t, committedUID, resource.Metadata.UID, "readback must converge to the exact committed incarnation")
	}
	return resource
}

func rbacNativeErrorCode(t *testing.T, response gqlResponse, expected string) {
	t.Helper()
	require.Len(t, response.Errors, 1)
	var failure struct {
		Extensions struct{ Code string }
	}
	require.NoError(t, json.Unmarshal(response.Errors[0], &failure))
	require.Equal(t, expected, failure.Extensions.Code, "%s", response.Errors[0])
}

type rbacNativeReview struct {
	APIVersion string
	Kind       string
	Spec       struct {
		Principal struct {
			Subject string
			Groups  []string
		}
		Action      string
		ResourceRef struct{ ID string }
	}
	Status struct {
		Allowed       bool
		Hypothetical  bool
		CatalogDigest string
		BuiltinDigest string
		Revisions     []struct {
			TierID, NamespaceUID, GitRevision, TierRevision, CapturedAt string
		}
	}
}

func (f rbacNativeFixture) review(t *testing.T, api string, input map[string]any) rbacNativeReview {
	t.Helper()
	response := f.query(t, api, rbacNativeReviewQuery, map[string]any{"input": input})
	require.Empty(t, response.Errors, "access review HTTP status: %d", response.Status)
	var result struct {
		AccessReview struct{ SubjectAccessReview rbacNativeReview }
	}
	require.NoError(t, json.Unmarshal(response.Data, &result))
	review := result.AccessReview.SubjectAccessReview
	require.Equal(t, "authorization.gitstore.dev/v1beta1", review.APIVersion)
	require.Equal(t, "SubjectAccessReview", review.Kind)
	require.NotEmpty(t, review.Status.CatalogDigest)
	require.NotEmpty(t, review.Status.BuiltinDigest)
	require.NotEmpty(t, review.Status.Revisions)
	for _, revision := range review.Status.Revisions {
		require.NotEmpty(t, revision.TierID)
		require.NotEmpty(t, revision.GitRevision)
		require.NotEmpty(t, revision.TierRevision)
		require.NotEmpty(t, revision.CapturedAt)
	}
	return review
}

func TestRBACNativeAccessReviewMatchesEnforcement(t *testing.T) {
	admin := ownedRBACNativeFixture(t)
	subject := os.Getenv("RBAC_TEST_SUBJECT")
	require.NotEmpty(t, subject, "a real, initially unprivileged owned UserDir subject is required")
	actor := admin
	actor.token = rbacNativeToken(t, os.Getenv("RBAC_TEST_ACTOR_TOKEN_FILE"))
	logs := []string{os.Getenv("RBAC_TEST_API_A_LOG"), os.Getenv("RBAC_TEST_API_B_LOG")}
	for _, path := range logs {
		require.NotEmpty(t, path, "owned decision logs are required for exact enforced-revision joins")
	}
	target := admin.create(t, "Role", admin.manifest("Role", uniqueName("review-target")))
	input := map[string]any{"spec": map[string]any{
		"principal": map[string]any{"subject": subject},
		"action":    "role.read", "resourceRef": map[string]any{"id": target.ID},
	}}
	for _, api := range admin.apis {
		_, denied := actor.lookup(t, api, "Role", target.Metadata.Name)
		rbacNativeErrorCode(t, denied, "FORBIDDEN")
		rbacNativeErrorCode(t, actor.query(t, api, rbacNativeReviewQuery, map[string]any{"input": input}), "FORBIDDEN")
		review := admin.review(t, api, input)
		require.False(t, review.Status.Allowed)
		require.False(t, review.Status.Hypothetical)
	}

	name := uniqueName("scoped-reviewer")
	manifest := admin.manifest("Role", name)
	manifest["spec"] = map[string]any{"rules": []any{
		map[string]any{"resources": []string{"role"}, "verbs": []string{"read"}},
		map[string]any{"resources": []string{"role/access-review"}, "verbs": []string{"create"}},
	}}
	admin.create(t, "Role", manifest)
	bindingManifest := admin.manifest("RoleBinding", name)
	bindingManifest["spec"] = map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": name},
		"subjects": []any{map[string]any{"kind": "User", "name": subject}},
	}
	binding := admin.create(t, "RoleBinding", bindingManifest)
	for index, api := range admin.apis {
		log, err := os.Stat(logs[index])
		require.NoError(t, err)
		offset := log.Size()
		var review rbacNativeReview
		rbacNativeEventually(t, func() bool {
			got, response := actor.lookup(t, api, "Role", target.Metadata.Name)
			if len(response.Errors) != 0 || got.ID != target.ID {
				return false
			}
			review = actor.review(t, api, input)
			return rbacNativeDecisionMatches(t, logs[index], offset, subject, target.Metadata.Name, review)
		}, "review must match a real enforcement decision's exact input, tier revisions and release digests")
		require.True(t, review.Status.Allowed)
		require.False(t, review.Status.Hypothetical)
		require.Nil(t, review.Spec.Principal.Groups)
		require.Equal(t, subject, review.Spec.Principal.Subject)
		require.Equal(t, target.ID, review.Spec.ResourceRef.ID)

		hypothetical := map[string]any{"spec": map[string]any{
			"principal": map[string]any{"subject": subject, "groups": []string{}},
			"action":    "role.read", "resourceRef": map[string]any{"id": target.ID},
			"context": map[string]any{"visibility": "private"},
		}}
		hypothesis := actor.review(t, api, hypothetical)
		require.True(t, hypothesis.Status.Hypothetical)
		require.True(t, hypothesis.Status.Allowed)
		require.NotNil(t, hypothesis.Spec.Principal.Groups)
		require.Len(t, hypothesis.Status.Revisions, len(review.Status.Revisions))
		for index, revision := range review.Status.Revisions {
			require.Equal(t, revision.TierID, hypothesis.Status.Revisions[index].TierID)
			require.Equal(t, revision.TierRevision, hypothesis.Status.Revisions[index].TierRevision, "reviews must not mutate policy")
			require.Equal(t, revision.GitRevision, hypothesis.Status.Revisions[index].GitRevision)
		}
		builtin, response := admin.lookup(t, api, "ClusterRole", "view")
		require.Empty(t, response.Errors)
		clusterInput := map[string]any{"spec": map[string]any{
			"principal": map[string]any{"subject": subject, "groups": []string{"system:masters"}},
			"action":    "clusterRole.read", "resourceRef": map[string]any{"id": builtin.ID},
		}}
		rbacNativeErrorCode(t, actor.query(t, api, rbacNativeReviewQuery, map[string]any{"input": clusterInput}), "FORBIDDEN")
		clusterReview := admin.review(t, api, clusterInput)
		require.True(t, clusterReview.Status.Hypothetical)
		require.True(t, clusterReview.Status.Allowed)
	}

	current, response := admin.lookup(t, admin.apis[0], "RoleBinding", name)
	require.Empty(t, response.Errors)
	require.Equal(t, binding.Metadata.UID, current.Metadata.UID)
	deleted := admin.query(t, admin.apis[0],
		`mutation($input:DeleteRoleBindingInput!){deleteRoleBinding(input:$input){roleBinding{id}}}`,
		map[string]any{"input": map[string]any{"id": current.ID, "resourceVersion": current.Metadata.ResourceVersion}})
	require.Empty(t, deleted.Errors)
	for _, api := range admin.apis {
		rbacNativeEventually(t, func() bool {
			_, denied := actor.lookup(t, api, "Role", target.Metadata.Name)
			if len(denied.Errors) != 1 {
				return false
			}
			var failure struct{ Extensions struct{ Code string } }
			return json.Unmarshal(denied.Errors[0], &failure) == nil && failure.Extensions.Code == "FORBIDDEN"
		}, "revoking the ordinary binding must remove actual access on each API")
		require.False(t, admin.review(t, api, input).Status.Allowed)
	}
}
