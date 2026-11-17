// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
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
	"math/bits"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const capacitySubscription = `subscription($cursor: String) {
  watchNamespaces(resourceVersion: $cursor) {
    type
    name
    resourceVersion
    namespace {
      metadata { name creationTimestamp annotations }
      spec { title }
    }
  }
}`

const capacitySentAtAnnotation = "capacity.gitstore.dev/sent-at"

const rbacCapacityOwnerLabel = "capacity.gitstore.dev/owner"

const rbacCapacitySeedReadbackSelection = "kind metadata{uid name namespace generation labels} status{observedGeneration resolved{roleUID}} spec"

const rbacCapacitySeedReadbackQuery = `query($namespace:String!){
	roles(namespace:$namespace,first:10){edges{node{` + rbacCapacitySeedReadbackSelection + `{rules{resources verbs resourceNames when{token attribute{attr op value values}}}}}} pageInfo{hasNextPage}}
	roleBindings(namespace:$namespace,first:50){edges{node{` + rbacCapacitySeedReadbackSelection + `{roleRef{kind name} subjects{kind name}}}} pageInfo{hasNextPage}}
}`

var rbacCapacitySeedName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type rbacCapacitySeedPlan struct {
	Owner, Prefix, PersonalSubject, Group string
	Subjects                              []string
}

type rbacCapacitySeedNamespace struct {
	Name, Tier string
	Resources  []map[string]any
}

func (p rbacCapacitySeedPlan) validate() error {
	if len(p.Owner) > 63 || !rbacCapacitySeedName.MatchString(p.Owner) ||
		len(p.Prefix) > 40 || !strings.HasPrefix(p.Prefix, "rbac-") || !rbacCapacitySeedName.MatchString(p.Prefix) ||
		len(p.PersonalSubject) > 63 || p.PersonalSubject == "gitstore-system" || !rbacCapacitySeedName.MatchString(p.PersonalSubject) ||
		len(p.Subjects) < 4 || len(p.Subjects) > 128 {
		return errors.New("RBAC seed requires an explicit owner, isolated names and bounded distinct subjects")
	}
	seen := map[string]bool{p.PersonalSubject: true}
	for _, value := range append(append([]string{}, p.Subjects...), p.Group) {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value ||
			strings.ContainsAny(value, "\x00\r\n\t") || strings.HasPrefix(value, p.Prefix+"-filler-") || seen[value] {
			return errors.New("RBAC seed subjects and group must be distinct from the personal owner")
		}
		seen[value] = true
	}
	for index := 1; index < 1000; index++ {
		if p.PersonalSubject == fmt.Sprintf("%s-%04d", p.Prefix, index) {
			return errors.New("RBAC personal namespace collides with the organization working set")
		}
	}
	return nil
}

func (p rbacCapacitySeedPlan) namespace(index int) rbacCapacitySeedNamespace {
	result := rbacCapacitySeedNamespace{Name: p.namespaceName(index), Tier: "ORGANIZATION"}
	bindings := 50
	if index == 0 {
		result.Tier, bindings = "USER", 49
	}
	manifest := func(kind, name string, spec map[string]any) map[string]any {
		return map[string]any{
			"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": kind,
			"metadata": map[string]any{
				"name": name, "namespace": result.Name,
				"labels": map[string]any{rbacCapacityOwnerLabel: p.Owner},
			},
			"spec": spec,
		}
	}
	for role := range 10 {
		rule := map[string]any{"resources": []string{"role", "product", "categoryTaxonomy"}, "verbs": []string{"read", "list"}}
		if role%2 == 1 {
			rule["when"] = []any{map[string]any{"attr": "namespace", "op": "eq", "value": result.Name}}
		}
		result.Resources = append(result.Resources, manifest("Role", fmt.Sprintf("capacity-role-%02d", role),
			map[string]any{"rules": []any{rule}}))
	}
	for binding := range bindings {
		role := map[string]any{"kind": "Role", "name": fmt.Sprintf("capacity-role-%02d", binding%10)}
		subject := map[string]any{"kind": "User", "name": fmt.Sprintf("%s-filler-%04d-%02d", p.Prefix, index, binding)}
		switch binding {
		case 0:
			subject["name"] = p.Subjects[index%len(p.Subjects)]
		case 1:
			subject["kind"], subject["name"] = "Group", p.Group
		case 2:
			role["kind"], role["name"] = "ClusterRole", "view"
			subject["name"] = p.Subjects[(index+1)%len(p.Subjects)]
		case 3:
			role["name"] = "capacity-unresolved"
			subject["name"] = p.Subjects[(index+2)%len(p.Subjects)]
		}
		result.Resources = append(result.Resources, manifest("RoleBinding", fmt.Sprintf("capacity-binding-%02d", binding),
			map[string]any{"roleRef": role, "subjects": []any{subject}}))
	}
	return result
}

func (p rbacCapacitySeedPlan) namespaceName(index int) string {
	if index == 0 {
		return p.PersonalSubject
	}
	return fmt.Sprintf("%s-%04d", p.Prefix, index)
}

type rbacCapacityRequest struct {
	ID, Subject, Namespace, Action, Expected string
	Replica                                  int
}

func (p rbacCapacitySeedPlan) request(offer int64, count int, groupMembers []string) (rbacCapacityRequest, error) {
	if offer < 0 || count < 2 || count > 1000 || len(p.Subjects) < 4 || len(p.Subjects) > 128 ||
		len(groupMembers) == 0 || len(groupMembers) > len(p.Subjects) {
		return rbacCapacityRequest{}, errors.New("RBAC requests require bounded owned namespaces and independently declared principal groups")
	}
	groups := make(map[string]bool, len(groupMembers))
	for _, member := range groupMembers {
		known := false
		for _, subject := range p.Subjects {
			known = known || subject == member
		}
		if !known || groups[member] {
			return rbacCapacityRequest{}, errors.New("RBAC group membership differs from the declared principal working set")
		}
		groups[member] = true
	}
	index, expected := rbacCapacityCase(offer/2, count)
	direct, builtin := p.Subjects[index%len(p.Subjects)], p.Subjects[(index+1)%len(p.Subjects)]
	request := rbacCapacityRequest{
		ID: fmt.Sprintf("%s-ordinary-%012d", p.Owner, offer), Replica: int(offer % 2),
		Namespace: p.namespaceName(index), Subject: direct, Action: "product.list", Expected: expected,
	}
	switch expected {
	case "allow":
		switch offer / 2 % 6 {
		case 1:
			request.Subject = builtin
		case 2:
			request.Subject = groupMembers[index%len(groupMembers)]
		case 3:
			request.Action = "role.list"
		case 4:
			request.Subject, request.Action = groupMembers[index%len(groupMembers)], "role.list"
		}
	case "deny":
		request.Subject = ""
		for offset := range len(p.Subjects) {
			candidate := p.Subjects[(index+2+offset)%len(p.Subjects)]
			if candidate != direct && candidate != builtin && !groups[candidate] {
				request.Subject = candidate
				break
			}
		}
		if request.Subject == "" {
			return rbacCapacityRequest{}, errors.New("RBAC expected denials require a principal without an independently authored grant")
		}
		if offer/2%2 == 0 {
			request.Action = "role.list"
		}
	case "isolation":
		request.Subject = p.PersonalSubject
		if index == 0 {
			request.Namespace = p.namespaceName(1)
		}
	}
	return request, nil
}

func executeRBACCapacityRequest(ctx context.Context, client *http.Client, api, token string, request rbacCapacityRequest) string {
	outcome := observeRBACCapacityRequest(ctx, client, api, token, request)
	if outcome == "deny" && (request.Expected == "deny" || request.Expected == "isolation") {
		return request.Expected
	}
	if outcome == "allow" && request.Expected != "allow" || outcome == "deny" {
		return "wrong"
	}
	return outcome
}

func observeRBACCapacityRequest(ctx context.Context, client *http.Client, api, token string, request rbacCapacityRequest) string {
	field := "products"
	if request.Action == "role.list" {
		field = "roles"
	} else if request.Action != "product.list" {
		return "wrong"
	}
	content, err := json.Marshal(map[string]any{
		"query":     `query($namespace:String!){` + field + `(namespace:$namespace,first:1){edges{node{id}}}}`,
		"variables": map[string]string{"namespace": request.Namespace},
	})
	if err != nil {
		return "wrong"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/admin/graphql", bytes.NewReader(content))
	if err != nil || token == "" || request.ID == "" {
		return "wrong"
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("X-Request-ID", request.ID)
	response, err := client.Do(httpRequest)
	if err != nil {
		return "unavailable"
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "unavailable"
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		return "unavailable"
	}
	if len(body) > 65536 {
		return "wrong"
	}
	var data struct {
		Data   map[string]json.RawMessage
		Errors []struct{ Extensions struct{ Code string } }
	}
	if json.Unmarshal(body, &data) != nil {
		return "wrong"
	}
	if len(data.Errors) != 0 {
		code := data.Errors[0].Extensions.Code
		for _, failure := range data.Errors {
			if failure.Extensions.Code != code {
				return "wrong"
			}
		}
		for _, value := range data.Data {
			if string(value) != "null" {
				return "wrong"
			}
		}
		if code == "AUTHORIZATION_UNAVAILABLE" {
			return "unavailable"
		}
		if code == "FORBIDDEN" {
			return "deny"
		}
		return "wrong"
	}
	var connection struct {
		Edges []struct{ Node *struct{ ID string } }
	}
	if json.Unmarshal(data.Data[field], &connection) != nil ||
		connection.Edges == nil || len(connection.Edges) > 1 {
		return "wrong"
	}
	for _, edge := range connection.Edges {
		if edge.Node == nil || edge.Node.ID == "" {
			return "wrong"
		}
	}
	return "allow"
}

func observeRBACCapacityPropagation(ctx context.Context, acknowledgedAt time.Time, allowed bool,
	observe func(context.Context, int, int) string,
) ([2]time.Duration, error) {
	var latencies [2]time.Duration
	if acknowledgedAt.IsZero() || acknowledgedAt.After(time.Now()) || observe == nil {
		return latencies, errors.New("RBAC propagation requires an independently recorded successful-push clock and observer")
	}
	ctx, cancel := context.WithDeadline(ctx, acknowledgedAt.Add(5*time.Second))
	defer cancel()
	type result struct {
		replica int
		latency time.Duration
		err     error
	}
	results := make(chan result, len(latencies))
	for replica := range latencies {
		go func() {
			finish := func(err error) {
				latency := time.Since(acknowledgedAt)
				if err == nil {
					err = ctx.Err()
					if err == nil && latency > 5*time.Second {
						err = context.DeadlineExceeded
					}
				}
				results <- result{replica: replica, latency: latency, err: err}
			}
			for attempt := 0; attempt < 51; attempt++ {
				if err := ctx.Err(); err != nil {
					finish(err)
					return
				}
				outcome := observe(ctx, replica, attempt)
				if err := ctx.Err(); err != nil {
					finish(err)
					return
				}
				switch outcome {
				case "allow", "deny":
					if (outcome == "allow") == allowed {
						finish(nil)
						return
					}
				default:
					finish(errors.New("RBAC propagation encountered a malformed response or unplanned unavailability"))
					return
				}
				timer := time.NewTimer(100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					finish(ctx.Err())
					return
				case <-timer.C:
				}
			}
			finish(errors.New("RBAC propagation exhausted its bounded observations"))
		}()
	}
	var failure error
	for range latencies {
		result := <-results
		latencies[result.replica] = result.latency
		if result.err != nil && failure == nil {
			failure = result.err
			cancel()
		}
	}
	return latencies, failure
}

type rbacCapacitySeedReceipt struct {
	Owner, Namespace, Tier, NamespaceNodeUID, GitRevision string
	Roles, Bindings                                       map[string]string
	Admissions                                            []rbacCapacitySeedAdmission
	LastCheck                                             string `json:",omitempty"`
	Complete                                              bool
}

type rbacCapacitySeedAdmission struct {
	GitRevision    string
	AcknowledgedAt time.Time
	Resources      int
}

func (r *rbacCapacitySeedReceipt) acknowledge(sha string, at time.Time, resources int) error {
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != 20 || hex.EncodeToString(decoded) != sha || at.IsZero() ||
		len(r.Admissions) >= 6 || resources <= 0 || resources > 60 {
		return errors.New("RBAC seed admission requires a bounded identified import and acknowledgement clock")
	}
	revision := "main@sha1:" + sha
	previousResources := 0
	for _, previous := range r.Admissions {
		if previous.GitRevision == revision || !at.After(previous.AcknowledgedAt) {
			return errors.New("RBAC seed admission cannot repeat a commit or restart its acknowledgement clock")
		}
		previousResources = previous.Resources
	}
	if resources <= previousResources || resources > previousResources+10 {
		return errors.New("RBAC seed admission must identify the next bounded authored batch")
	}
	r.Admissions = append(r.Admissions, rbacCapacitySeedAdmission{
		GitRevision: revision, AcknowledgedAt: at, Resources: resources,
	})
	r.GitRevision = revision
	return nil
}

func rbacCapacitySeedSpec(t *testing.T, spec any, authored bool) any {
	t.Helper()
	value := rbacNativeGraphQLSpec(t, spec)
	if authored {
		if rules, ok := value["rules"].([]any); ok {
			for _, rule := range rules {
				rule := rule.(map[string]any)
				if conditions, ok := rule["when"].([]any); ok {
					for index, condition := range conditions {
						key := "attribute"
						if _, token := condition.(string); token {
							key = "token"
						}
						conditions[index] = map[string]any{key: condition}
					}
				}
			}
		}
	}
	var normalize func(any) any
	normalize = func(value any) any {
		switch value := value.(type) {
		case map[string]any:
			result := make(map[string]any)
			for key, child := range value {
				if child = normalize(child); child != nil {
					result[key] = child
				}
			}
			return result
		case []any:
			if len(value) == 0 {
				return nil
			}
			result := make([]any, len(value))
			for index, child := range value {
				result[index] = normalize(child)
			}
			sort.Slice(result, func(i, j int) bool {
				left, _ := json.Marshal(result[i])
				right, _ := json.Marshal(result[j])
				return bytes.Compare(left, right) < 0
			})
			return result
		default:
			return value
		}
	}
	return normalize(value)
}

func awaitRBACCapacitySeedReadback(ctx context.Context, slots <-chan time.Time, observe func() bool) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-slots:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if ready := observe(); ready {
			return ctx.Err()
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type rbacCapacitySourceHeader struct {
	ModelVersion  string            `json:"modelVersion"`
	State         json.RawMessage   `json:"state"`
	Origin        json.RawMessage   `json:"origin"`
	CatalogDigest string            `json:"catalogDigest"`
	BuiltinDigest string            `json:"builtinDigest"`
	ContentDigest string            `json:"contentDigest"`
	Resources     []json.RawMessage `json:"resources"`
}

func walkRBACCapacitySourceModels(reader io.Reader, visit func(rbacCapacitySourceHeader, []json.RawMessage) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var header *rbacCapacitySourceHeader
	var resources []json.RawMessage
	var remaining, modelBytes, total int
	for scanner.Scan() {
		total += len(scanner.Bytes()) + 1
		if total > 1<<30 {
			return errors.New("RBAC source evidence exceeds its one-GiB stream bound")
		}
		var record struct {
			Type          string
			Snapshot      *rbacCapacitySourceHeader
			Resource      json.RawMessage
			ResourceCount int
			Digest        string
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return err
		}
		switch record.Type {
		case "begin":
			if header != nil || record.Snapshot == nil || record.ResourceCount < 0 || record.ResourceCount > 750 ||
				record.Snapshot.ModelVersion != "v1" || len(record.Snapshot.Resources) != 0 {
				return errors.New("RBAC source model has an invalid or overlapping header")
			}
			header, remaining, modelBytes = record.Snapshot, record.ResourceCount, len(scanner.Bytes())
			resources = nil
		case "resource":
			if header == nil || remaining == 0 || len(record.Resource) == 0 {
				return errors.New("RBAC source resource is outside its declared model")
			}
			modelBytes += len(record.Resource)
			if modelBytes > 64<<20 {
				return errors.New("RBAC source model exceeds its 64-MiB bound")
			}
			resources = append(resources, record.Resource)
			remaining--
		case "end":
			if header == nil || remaining != 0 || record.Digest != header.ContentDigest {
				return errors.New("RBAC source model is incomplete or has a different identity")
			}
			digest, err := hex.DecodeString(record.Digest)
			if err != nil || len(digest) != sha256.Size {
				return errors.New("RBAC source digest is invalid")
			}
			expected := *header
			expected.ContentDigest = ""
			canonical, err := json.Marshal(expected)
			if err != nil {
				return err
			}
			hash := sha256.New()
			_, _ = hash.Write(canonical)
			for _, resource := range resources {
				_, _ = hash.Write([]byte{0})
				_, _ = hash.Write(resource)
			}
			if !bytes.Equal(hash.Sum(nil), digest) {
				return errors.New("RBAC source model content does not match its digest")
			}
			if err := visit(*header, resources); err != nil {
				return err
			}
			header, resources = nil, nil
		default:
			return errors.New("RBAC source evidence has an unknown record type")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if header != nil {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func rbacCapacitySeedBindingResolved(encoded json.RawMessage, roles map[string]string, referenceGeneration int64) (bool, error) {
	var resource struct {
		Metadata struct{ Generation int64 }
		Spec     struct{ RoleRef struct{ Kind, Name string } }
		Status   struct {
			ObservedGeneration int64
			Resolved           *struct {
				RoleRef struct {
					Kind, Name, UID    string
					ObservedGeneration int64
				}
			}
		}
	}
	if err := json.Unmarshal(encoded, &resource); err != nil {
		return false, err
	}
	if resource.Status.ObservedGeneration != resource.Metadata.Generation {
		return false, nil
	}
	resolved := resource.Status.Resolved
	if resolved != nil {
		if !strings.EqualFold(resolved.RoleRef.Kind, resource.Spec.RoleRef.Kind) ||
			resolved.RoleRef.Name != resource.Spec.RoleRef.Name {
			return false, errors.New("RBAC source resolved a different authored role reference")
		}
		if referenceGeneration < 1 || resolved.RoleRef.ObservedGeneration != referenceGeneration || resolved.RoleRef.UID == "" {
			return false, nil
		}
	}
	switch strings.ToLower(resource.Spec.RoleRef.Kind) {
	case "role":
		uid, found := roles[resource.Spec.RoleRef.Name]
		if !found {
			if resolved != nil {
				return false, errors.New("RBAC source resolved an independently absent fixture Role")
			}
			return true, nil
		}
		if resolved == nil {
			return false, nil
		}
		if resolved.RoleRef.UID != uid {
			return false, errors.New("RBAC source binding resolved a different immutable Role incarnation")
		}
		return true, nil
	case "clusterrole":
		return resolved != nil, nil
	default:
		return false, errors.New("RBAC source binding has an undeclared role reference kind")
	}
}

func verifyRBACCapacitySeedSources(t *testing.T, path string, plan rbacCapacitySeedPlan, count int, receipts map[string]rbacCapacitySeedReceipt) {
	t.Helper()
	file, err := openRBACCapacityPrivateFixture(path, 1<<30)
	require.NoError(t, err)
	defer file.Close()
	indexes := make(map[string]int, count)
	for index := range count {
		indexes[plan.namespace(index).Name] = index
	}
	seen := make(map[string]bool)
	err = walkRBACCapacitySourceModels(file, func(header rbacCapacitySourceHeader, resources []json.RawMessage) error {
		var origin struct{ NamespaceUID, Namespace, GitRevision string }
		require.NoError(t, json.Unmarshal(header.Origin, &origin))
		index, owned := indexes[origin.Namespace]
		if !owned || origin.GitRevision != receipts[origin.Namespace].GitRevision {
			return nil
		}
		complete := checkRBACCapacityNamespaceSource(t, header, resources, plan.namespace(index), receipts[origin.Namespace], nil, nil)
		seen[origin.Namespace] = seen[origin.Namespace] || complete
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	for namespace := range indexes {
		require.True(t, seen[namespace], "each API must capture the complete admitted source model before provider preparation")
	}
}

func checkRBACCapacityNamespaceSource(t *testing.T, header rbacCapacitySourceHeader, resources []json.RawMessage,
	namespace rbacCapacitySeedNamespace, receipt rbacCapacitySeedReceipt, generations, references map[string]int64,
) bool {
	t.Helper()
	var origin struct{ NamespaceUID, Namespace, GitRevision string }
	var state struct {
		TierID                  string
		RoleCount, BindingCount int
		Projection              struct{ GitRevision string }
	}
	require.NoError(t, json.Unmarshal(header.Origin, &origin))
	require.NoError(t, json.Unmarshal(header.State, &state))
	require.Equal(t, namespace.Name, origin.Namespace)
	require.Equal(t, receipt.GitRevision, origin.GitRevision)
	decoded, err := base64.StdEncoding.DecodeString(receipt.NamespaceNodeUID)
	require.NoError(t, err)
	require.Equal(t, "gid://GitStore/Namespace/"+origin.NamespaceUID, string(decoded))
	require.NotEmpty(t, origin.NamespaceUID)
	require.Equal(t, origin.NamespaceUID, state.TierID)
	require.Equal(t, receipt.GitRevision, state.Projection.GitRevision)
	require.Equal(t, len(receipt.Roles), state.RoleCount)
	require.Equal(t, len(receipt.Bindings), state.BindingCount)
	expected := make(map[string]map[string]any)
	for _, resource := range namespace.Resources {
		expected[resource["kind"].(string)+"/"+resource["metadata"].(map[string]any)["name"].(string)] = resource["spec"].(map[string]any)
	}
	if namespace.Tier == "USER" {
		expected["RoleBinding/namespace-owner-access"] = map[string]any{
			"roleRef":  map[string]any{"kind": "ClusterRole", "name": "namespace-full-control"},
			"subjects": []any{map[string]any{"kind": "User", "name": namespace.Name}},
		}
	}
	for key, generation := range generations {
		require.Contains(t, expected, key, "generation pins must identify independently authored resources")
		require.Positive(t, generation)
	}
	for key, generation := range references {
		require.Contains(t, generations, key)
		require.Positive(t, generation)
		require.LessOrEqual(t, generation, generations[key])
	}
	require.Len(t, resources, len(expected), "a source model must contain the complete independently authored policy")
	identities := make(map[string]bool)
	complete := true
	for _, encoded := range resources {
		var resource rbacNativeResource
		require.NoError(t, json.Unmarshal(encoded, &resource))
		key := resource.Kind + "/" + resource.Metadata.Name
		spec, found := expected[key]
		require.True(t, found, "source models must not acquire undeclared policy")
		require.False(t, identities[key])
		identities[key] = true
		require.Equal(t, origin.Namespace, resource.Metadata.Namespace)
		require.Equal(t, rbacCapacitySeedSpec(t, spec, true), rbacCapacitySeedSpec(t, resource.Spec, true))
		if generation, pinned := generations[key]; pinned && resource.Metadata.Generation != generation {
			complete = false
		}
		uids := receipt.Roles
		if resource.Kind == "RoleBinding" {
			uids = receipt.Bindings
			referenceGeneration := resource.Metadata.Generation
			if pinned, found := references[key]; found {
				referenceGeneration = pinned
			}
			resolved, err := rbacCapacitySeedBindingResolved(encoded, receipt.Roles, referenceGeneration)
			require.NoError(t, err)
			complete = complete && resolved
		}
		require.Equal(t, uids[resource.Metadata.Name], resource.Metadata.UID)
	}
	return complete
}

type rbacCapacityProbeExpectation struct {
	Subject, Namespace, Action string
	Allowed                    bool
	ObservedAt                 time.Time
}

type rbacCapacityProbeSource struct {
	Namespace string
	Root      bool
}

type rbacCapacityRootPolicy struct {
	NamespaceUID, BindingUID, AdminSubject, TreeDigest string
}

func rbacCapacityInitialRootManifest(subject string) map[string]any {
	return map[string]any{
		"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": "initial-admin"},
		"spec": map[string]any{
			"roleRef":  map[string]any{"kind": "ClusterRole", "name": "cluster-admin"},
			"subjects": []any{map[string]any{"kind": "User", "name": subject}},
		},
	}
}

func rbacCapacityRootTreeDigest(subject string) (string, error) {
	content, err := yaml.Marshal(rbacCapacityInitialRootManifest(subject))
	if err != nil {
		return "", err
	}
	path := "rbac/clusterrolebindings/initial-admin.yaml"
	entry := sha256.Sum256(content)
	tree := sha256.Sum256(fmt.Appendf(nil, "%d:%s:%x\n", len(path), path, entry))
	return hex.EncodeToString(tree[:]), nil
}

func checkRBACCapacityRootSource(header rbacCapacitySourceHeader, resources []json.RawMessage, expected rbacCapacityRootPolicy) (bool, error) {
	var origin struct {
		Namespace, NamespaceUID, TreeDigest string
		Root, Active                        bool
	}
	var state struct {
		TierID                  string
		RoleCount, BindingCount int
	}
	if err := json.Unmarshal(header.Origin, &origin); err != nil {
		return false, err
	}
	if err := json.Unmarshal(header.State, &state); err != nil {
		return false, err
	}
	if expected.NamespaceUID == "" || expected.BindingUID == "" || expected.AdminSubject == "" || expected.TreeDigest == "" ||
		!origin.Root || !origin.Active || origin.Namespace != "gitstore-system" ||
		origin.NamespaceUID != expected.NamespaceUID || state.TierID != "cluster" {
		return false, errors.New("RBAC root source differs from the independently pinned root incarnation")
	}
	if len(resources) == 0 && state.RoleCount == 0 && state.BindingCount == 0 {
		return false, nil
	}
	if len(resources) != 1 || state.RoleCount != 0 || state.BindingCount != 1 || origin.TreeDigest != expected.TreeDigest {
		return false, errors.New("RBAC root source differs from the authored bootstrap policy tree")
	}
	var resource rbacNativeResource
	if err := json.Unmarshal(resources[0], &resource); err != nil {
		return false, err
	}
	var spec any
	if err := json.Unmarshal(resource.Spec, &spec); err != nil {
		return false, err
	}
	if resource.Kind != "ClusterRoleBinding" || resource.Metadata.Name != "initial-admin" ||
		resource.Metadata.UID != expected.BindingUID || resource.Metadata.Namespace != "" ||
		resource.Metadata.Generation != 1 || resource.Metadata.DeletionTimestamp != nil ||
		!reflect.DeepEqual(spec, rbacCapacityInitialRootManifest(expected.AdminSubject)["spec"]) {
		return false, errors.New("RBAC root source changed the authored administrator grant or binding incarnation")
	}
	return rbacCapacitySeedBindingResolved(resources[0], nil, 1)
}

func checkRBACCapacityProbeTraces(reader io.Reader, expected map[string]rbacCapacityProbeExpectation, models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource) error {
	return checkRBACCapacityObservedTraces(reader, expected, models, nil)
}

func checkRBACCapacityObservedTraces(reader io.Reader, expected map[string]rbacCapacityProbeExpectation,
	models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource,
	observe func(rbacCapacityPolicyVector, string, bool) error,
) error {
	if len(expected) == 0 || len(expected) > 128 {
		return errors.New("RBAC native probes require bounded, predeclared request expectations")
	}
	for id, probe := range expected {
		if id == "" || probe.Subject == "" || probe.Namespace == "" ||
			(probe.Action != "product.list" && probe.Action != "role.list") {
			return errors.New("RBAC native probe request identity is incomplete")
		}
	}
	seen := make(map[string]bool, len(expected))
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var total int64
	for scanner.Scan() {
		total += int64(len(scanner.Bytes())) + 1
		if total > 1<<30 {
			return errors.New("RBAC diagnostic decision evidence exceeds its one-GiB read bound")
		}
		var decision struct {
			RequestID     string `json:"request_id"`
			Action        string
			Subject       string
			Namespace     string `json:"resource_namespace"`
			Kind          string `json:"resource_kind"`
			Outcome       string
			Provider      string
			Authorization []struct {
				TierID, GitRevision, ContentDigest string
			} `json:"authorization_tiers"`
		}
		if json.Unmarshal(scanner.Bytes(), &decision) != nil {
			continue
		}
		probe, tracked := expected[decision.RequestID]
		if !tracked || decision.Action != probe.Action {
			continue
		}
		outcome := "deny"
		if probe.Allowed {
			outcome = "allow"
		}
		kind, _, _ := strings.Cut(probe.Action, ".")
		if seen[decision.RequestID] || decision.Subject != probe.Subject || decision.Namespace != probe.Namespace ||
			decision.Kind != kind || decision.Outcome != outcome || decision.Provider != "opa" || len(decision.Authorization) != 2 {
			return errors.New("RBAC native probe decision differs from its predeclared request expectation")
		}
		var rootVerified, namespaceVerified bool
		var vector rbacCapacityPolicyVector
		for _, tier := range decision.Authorization {
			revision := rbacCapacityPolicyRevision{TierUID: tier.TierID, GitRevision: tier.GitRevision, Digest: tier.ContentDigest}
			model, found := models[revision]
			if !found {
				return errors.New("RBAC native probe evaluated an independently unverified source vector")
			}
			if model.Root {
				if rootVerified {
					return errors.New("RBAC native probe repeated its cluster policy tier")
				}
				rootVerified = true
				vector.Cluster = revision
			} else {
				if namespaceVerified || model.Namespace != probe.Namespace {
					return errors.New("RBAC native probe evaluated a different Namespace policy tier")
				}
				namespaceVerified = true
				vector.Namespace = revision
			}
		}
		if !rootVerified || !namespaceVerified {
			return errors.New("RBAC native probe is missing an independently captured policy tier")
		}
		if observe != nil {
			if err := observe(vector, decision.RequestID, probe.Allowed); err != nil {
				return err
			}
		}
		seen[decision.RequestID] = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return errors.New("RBAC native probes are missing ordinary request decision receipts")
	}
	return nil
}

func verifyRBACCapacityProbeTraces(t *testing.T, sourcePath, decisionPath string, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, expected map[string]rbacCapacityProbeExpectation,
) {
	t.Helper()
	source, err := openRBACCapacityPrivateFixture(sourcePath, 1<<30)
	require.NoError(t, err)
	defer source.Close()
	indexes := make(map[string]int, count)
	for index := range count {
		indexes[plan.namespaceName(index)] = index
	}
	models := make(map[rbacCapacityPolicyRevision]rbacCapacityProbeSource)
	err = walkRBACCapacitySourceModels(source, func(header rbacCapacitySourceHeader, resources []json.RawMessage) error {
		var origin struct {
			Namespace, NamespaceUID, GitRevision string
			Root                                 bool
		}
		var state struct{ TierID string }
		require.NoError(t, json.Unmarshal(header.Origin, &origin))
		require.NoError(t, json.Unmarshal(header.State, &state))
		if !origin.Root {
			receipt, owned := receipts[origin.Namespace]
			if !owned || origin.GitRevision != receipt.GitRevision {
				return nil
			}
			index, found := indexes[origin.Namespace]
			require.True(t, found)
			if !checkRBACCapacityNamespaceSource(t, header, resources, plan.namespace(index), receipt, nil, nil) {
				return nil
			}
		} else {
			complete, err := checkRBACCapacityRootSource(header, resources, root)
			require.NoError(t, err)
			if !complete {
				return nil
			}
		}
		require.NotEmpty(t, origin.NamespaceUID)
		models[rbacCapacityPolicyRevision{TierUID: state.TierID, GitRevision: origin.GitRevision, Digest: header.ContentDigest}] =
			rbacCapacityProbeSource{Namespace: origin.Namespace, Root: origin.Root}
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	decisions, err := openRBACCapacityPrivateFixture(decisionPath, 1<<30)
	require.NoError(t, err)
	defer decisions.Close()
	require.NoError(t, checkRBACCapacityProbeTraces(decisions, expected, models))
}

func seedRBACCapacityNamespace(t *testing.T, fixture rbacNativeFixture, plan rbacCapacitySeedPlan, index int, personalToken, receiptDirectory string, namespaceWriter *sync.Mutex, readbackSlots map[string]<-chan time.Time) {
	t.Helper()
	require.NoError(t, plan.validate())
	require.GreaterOrEqual(t, index, 0)
	require.Less(t, index, 1000)
	require.NotNil(t, namespaceWriter)
	require.True(t, filepath.IsAbs(receiptDirectory) && filepath.Clean(receiptDirectory) == receiptDirectory)
	deadline, bounded := t.Deadline()
	require.True(t, bounded && time.Until(deadline) > 0,
		"native seeding requires an explicit finite test deadline covering the declared fixture and readback pacing")
	directory, err := os.Lstat(receiptDirectory)
	require.NoError(t, err)
	require.True(t, directory.IsDir() && directory.Mode().Perm()&0077 == 0,
		"seed receipts require an existing private directory, not a symlink")
	namespace := plan.namespace(index)
	record := rbacCapacitySeedReceipt{Owner: plan.Owner, Namespace: namespace.Name, Tier: namespace.Tier}
	receipt, err := os.OpenFile(filepath.Join(receiptDirectory, namespace.Name+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err, "never overwrite a prior seed or partial-failure receipt")
	defer func() {
		if !record.Complete && record.LastCheck != "" {
			t.Logf("last bounded native seed observation: %s", record.LastCheck)
		}
		require.NoError(t, json.NewEncoder(receipt).Encode(record))
		require.NoError(t, receipt.Sync())
		require.NoError(t, receipt.Close())
	}()
	func() {
		// Namespace manifests share the root repository; independent namespace
		// RBAC batches can use the worker pool after this single-writer stage.
		namespaceWriter.Lock()
		defer namespaceWriter.Unlock()
		for _, api := range fixture.apis {
			absent := fixture.query(t, api, `query($name:String!){namespace(by:{name:$name}){id}}`,
				map[string]any{"name": namespace.Name})
			rbacNativeErrorCode(t, absent, "NOT_FOUND")
		}
		creator := fixture
		if namespace.Tier == "USER" {
			require.NotEmpty(t, personalToken, "create the personal namespace as its authenticated Staff owner")
			creator.token = personalToken
		}
		created := creator.query(t, fixture.apis[0],
			`mutation($input:CreateNamespaceInput!){createNamespace(input:$input){namespace{metadata{uid name labels} spec{tier}}}}`,
			map[string]any{"input": map[string]any{
				"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace",
				"metadata": map[string]any{"name": namespace.Name, "labels": map[string]string{rbacCapacityOwnerLabel: plan.Owner}},
				"spec":     map[string]any{"title": "Owned RBAC capacity fixture", "tier": namespace.Tier},
			}})
		require.Empty(t, created.Errors)
		var acknowledged struct {
			CreateNamespace struct {
				Namespace struct {
					Metadata struct {
						UID, Name string
						Labels    map[string]string
					}
					Spec struct{ Tier string }
				}
			}
		}
		require.NoError(t, json.Unmarshal(created.Data, &acknowledged))
		actual := acknowledged.CreateNamespace.Namespace
		require.Equal(t, namespace.Name, actual.Metadata.Name)
		require.Equal(t, plan.Owner, actual.Metadata.Labels[rbacCapacityOwnerLabel])
		require.Equal(t, namespace.Tier, actual.Spec.Tier)
		require.NotEmpty(t, actual.Metadata.UID)
		record.NamespaceNodeUID = actual.Metadata.UID
	}()
	fixture.namespace = namespace.Name
	for _, api := range fixture.apis {
		rbacNativeEventually(t, func() bool {
			response := fixture.query(t, api,
				`query($name:String!){namespace(by:{name:$name}){metadata{uid name labels generation} status{observedGeneration conditions{type status}}}}`,
				map[string]any{"name": namespace.Name})
			if len(response.Errors) != 0 {
				return false
			}
			var data struct{ Namespace namespaceContractNamespace }
			require.NoError(t, json.Unmarshal(response.Data, &data))
			current := data.Namespace
			require.NotNil(t, current.Metadata)
			require.Equal(t, record.NamespaceNodeUID, current.Metadata.UID)
			require.Equal(t, plan.Owner, current.Metadata.Labels[rbacCapacityOwnerLabel])
			if current.Status == nil || current.Status.ObservedGeneration != current.Metadata.Generation {
				return false
			}
			for _, condition := range current.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "TRUE" {
					return true
				}
			}
			return false
		}, "both APIs must observe admitted system-repository readiness before native Git cloning")
	}
	if namespace.Tier == "USER" {
		fixture.await(t, "RoleBinding", "namespace-owner-access", false)
	}
	git := fixture.clone(t, "Role")
	expected := make(map[string]map[string]any)
	if namespace.Tier == "USER" {
		expected["RoleBinding/namespace-owner-access"] = map[string]any{
			"roleRef":  map[string]any{"kind": "ClusterRole", "name": "namespace-full-control"},
			"subjects": []any{map[string]any{"kind": "User", "name": plan.PersonalSubject}},
		}
	}
	// Bound controller status work by converging each admitted import batch
	// before submitting the next one.
	for start := 0; start < len(namespace.Resources); start += 10 {
		for _, resource := range namespace.Resources[start:min(start+10, len(namespace.Resources))] {
			name := resource["metadata"].(map[string]any)["name"].(string)
			expected[resource["kind"].(string)+"/"+name] = resource["spec"].(map[string]any)
			git.write(t, "rbac/"+strings.ToLower(resource["kind"].(string))+"s/"+name+".yaml", resource)
		}
		sha := git.commit(t)
		git.push(t)
		acknowledgedAt := time.Now()
		require.NoError(t, record.acknowledge(sha, acknowledgedAt, min(start+10, len(namespace.Resources))))
		tip := strings.Fields(git.remoteTip(t))
		require.Len(t, tip, 2)
		require.Equal(t, sha, tip[0], "the canonical ref must retain the independently identified admitted commit")
		for replica, api := range fixture.apis {
			var roles, bindings map[string]string
			require.NotNil(t, readbackSlots[api])
			readbackContext, cancelReadback := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancelReadback()
			err := awaitRBACCapacitySeedReadback(readbackContext, readbackSlots[api], func() bool {
				previous := record.LastCheck
				defer func() {
					if record.LastCheck != previous {
						t.Logf("bounded native seed observation: %s", record.LastCheck)
					}
				}()
				record.LastCheck = fmt.Sprintf("replica=%d querying native RBAC readback", replica)
				deadline, _ := readbackContext.Deadline()
				readback := secretTestGraphQLWithTimeout(t, api, fixture.token, rbacCapacitySeedReadbackQuery,
					map[string]any{"namespace": namespace.Name}, min(10*time.Second, time.Until(deadline)))
				if len(readback.Errors) != 0 {
					record.LastCheck = fmt.Sprintf("replica=%d status=%d errors=%.8192s", replica, readback.Status, namespaceContractErrors(readback.Errors))
					return false
				}
				var data struct {
					Roles, RoleBindings struct {
						Edges    []struct{ Node rbacNativeResource }
						PageInfo struct{ HasNextPage bool }
					}
				}
				require.NoError(t, json.Unmarshal(readback.Data, &data))
				require.False(t, data.Roles.PageInfo.HasNextPage, "the native fixture exceeds its exact Role envelope")
				require.False(t, data.RoleBindings.PageInfo.HasNextPage, "the native fixture exceeds its exact binding envelope")
				record.LastCheck = fmt.Sprintf("replica=%d roles=%d bindings=%d", replica, len(data.Roles.Edges), len(data.RoleBindings.Edges))
				if len(data.Roles.Edges) != 10 || len(data.RoleBindings.Edges) != len(expected)-10 {
					return false
				}
				roles, bindings = make(map[string]string), make(map[string]string)
				seenUIDs := make(map[string]bool)
				verify := func(resource rbacNativeResource) {
					require.NotEmpty(t, resource.Metadata.UID)
					require.False(t, seenUIDs[resource.Metadata.UID], "native fixture UIDs must identify distinct resources")
					seenUIDs[resource.Metadata.UID] = true
					spec, found := expected[resource.Kind+"/"+resource.Metadata.Name]
					require.True(t, found, "the native fixture must contain only the exact authored resources and protected owner template")
					require.Equal(t, rbacCapacitySeedSpec(t, spec, true), rbacCapacitySeedSpec(t, resource.Spec, false),
						"native policy readback must match independently authored expectations")
				}
				for _, edge := range data.Roles.Edges {
					resource := edge.Node
					require.Equal(t, "Role", resource.Kind)
					require.Equal(t, namespace.Name, resource.Metadata.Namespace)
					require.Equal(t, plan.Owner, resource.Metadata.Labels[rbacCapacityOwnerLabel])
					require.NotEmpty(t, resource.Metadata.UID)
					verify(resource)
					require.Empty(t, roles[resource.Metadata.Name], "duplicate native Role identity")
					roles[resource.Metadata.Name] = resource.Metadata.UID
				}
				for _, edge := range data.RoleBindings.Edges {
					resource := edge.Node
					require.Equal(t, "RoleBinding", resource.Kind)
					require.Equal(t, namespace.Name, resource.Metadata.Namespace)
					require.NotEmpty(t, resource.Metadata.UID)
					verify(resource)
					require.Empty(t, bindings[resource.Metadata.Name], "duplicate native binding identity")
					if resource.Metadata.Name != "namespace-owner-access" {
						require.Equal(t, plan.Owner, resource.Metadata.Labels[rbacCapacityOwnerLabel])
					} else {
						require.Equal(t, "USER", namespace.Tier, "organization fixtures must not acquire managed personal bindings")
					}
					var spec struct{ RoleRef struct{ Kind, Name string } }
					require.NoError(t, json.Unmarshal(resource.Spec, &spec))
					if resource.Status.ObservedGeneration != resource.Metadata.Generation {
						record.LastCheck = fmt.Sprintf("replica=%d binding=%s observed=%d generation=%d", replica,
							resource.Metadata.Name, resource.Status.ObservedGeneration, resource.Metadata.Generation)
						return false
					}
					if spec.RoleRef.Name == "capacity-unresolved" {
						require.Nil(t, resource.Status.Resolved)
					} else {
						if resource.Status.Resolved == nil || resource.Status.Resolved.RoleUID == "" {
							record.LastCheck = fmt.Sprintf("replica=%d binding=%s waiting for resolved role UID", replica, resource.Metadata.Name)
							return false
						}
						if spec.RoleRef.Kind == "ROLE" {
							require.Equal(t, roles[spec.RoleRef.Name], resource.Status.Resolved.RoleUID)
						}
					}
					bindings[resource.Metadata.Name] = resource.Metadata.UID
				}
				return true
			})
			cancelReadback()
			require.NoError(t, err, "both native APIs must enumerate the complete admitted fixture and resolved immutable Role links")
			if replica == 0 {
				for name, uid := range record.Roles {
					require.Equal(t, uid, roles[name], "later imports must retain the admitted Role incarnation")
				}
				for name, uid := range record.Bindings {
					require.Equal(t, uid, bindings[name], "later imports must retain the admitted binding incarnation")
				}
				record.Roles, record.Bindings = roles, bindings
			} else {
				require.Equal(t, record.Roles, roles, "both APIs must count the same exact Role incarnations")
				require.Equal(t, record.Bindings, bindings, "both APIs must count the same exact binding incarnations")
			}
		}
	}
	record.LastCheck = ""
	record.Complete = true
}

func TestRBACCapacityNativeSeed(t *testing.T) {
	if os.Getenv("RBAC_TEST_CAPACITY_SEED_RUN") != "1" {
		t.Skip("owned native RBAC seeding must be explicitly selected")
	}
	fixture := ownedRBACNativeFixture(t)
	content, err := readRBACCapacityPrivateFixture(os.Getenv("RBAC_TEST_CAPACITY_SEED_FILE"))
	require.NoError(t, err)
	var config struct {
		Plan                                      rbacCapacitySeedPlan
		Count, Workers                            int
		Mode, PersonalTokenFile, ReceiptDirectory string
		PrincipalTokenFiles                       map[string]string
		SourceModelFiles                          []string
		DecisionLogFiles                          []string
		InitialAdminSubject                       string
		DecisionWorkload                          *struct {
			Schedule              rbacCapacityScheduleConfig
			GroupMembers          []string
			PropagationDiagnostic bool
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&config))
	require.NoError(t, config.Plan.validate())
	mode, err := parseCapacityMode(config.Mode)
	require.NoError(t, err)
	require.GreaterOrEqual(t, config.Count, 1)
	require.LessOrEqual(t, config.Count, 1000)
	require.GreaterOrEqual(t, config.Workers, 1)
	require.LessOrEqual(t, config.Workers, 4)
	if mode != capacityModeDiagnostic {
		require.Equal(t, 1000, config.Count, "non-diagnostic seeding must prepare the full declared working set")
		require.Len(t, config.PrincipalTokenFiles, len(config.Plan.Subjects),
			"non-diagnostic fixtures require real authenticated rotating principals")
	}
	if len(config.DecisionLogFiles) != 0 {
		require.Len(t, config.DecisionLogFiles, len(fixture.apis))
		require.Len(t, config.SourceModelFiles, len(fixture.apis), "decision joins require independent pre-publication source evidence")
		require.NotEmpty(t, config.PrincipalTokenFiles)
	}
	var root rbacCapacityRootPolicy
	if len(config.DecisionLogFiles) != 0 {
		require.NotEmpty(t, config.InitialAdminSubject, "root expectations must come from authored bootstrap intent")
		require.NotEqual(t, config.Plan.PersonalSubject, config.InitialAdminSubject)
		require.NotContains(t, config.Plan.Subjects, config.InitialAdminSubject)
		root.AdminSubject = config.InitialAdminSubject
		root.TreeDigest, err = rbacCapacityRootTreeDigest(root.AdminSubject)
		require.NoError(t, err)
		for _, api := range fixture.apis {
			response := fixture.query(t, api, `query{namespace(by:{name:"gitstore-system"}){metadata{uid}}}`, nil)
			require.Empty(t, response.Errors)
			var data struct {
				Namespace struct{ Metadata struct{ UID string } }
			}
			require.NoError(t, json.Unmarshal(response.Data, &data))
			node, err := base64.StdEncoding.DecodeString(data.Namespace.Metadata.UID)
			require.NoError(t, err)
			uid, found := strings.CutPrefix(string(node), "gid://GitStore/Namespace/")
			require.True(t, found)
			require.NotEmpty(t, uid)
			binding, response := fixture.lookup(t, api, "ClusterRoleBinding", "initial-admin")
			require.Empty(t, response.Errors)
			require.NotEmpty(t, binding.Metadata.UID)
			require.Equal(t, rbacCapacitySeedSpec(t, rbacCapacityInitialRootManifest(root.AdminSubject)["spec"], true),
				rbacCapacitySeedSpec(t, binding.Spec, false))
			if root.NamespaceUID != "" {
				require.Equal(t, root.NamespaceUID, uid)
				require.Equal(t, root.BindingUID, binding.Metadata.UID)
			}
			root.NamespaceUID, root.BindingUID = uid, binding.Metadata.UID
		}
	}
	tokens := make(map[string]string, len(config.PrincipalTokenFiles))
	if len(config.PrincipalTokenFiles) != 0 {
		require.GreaterOrEqual(t, config.Count, 2, "principal probes require the personal and first organization namespace")
		require.Len(t, config.PrincipalTokenFiles, len(config.Plan.Subjects))
		for _, subject := range config.Plan.Subjects {
			content, err := readRBACCapacityPrivateFixture(config.PrincipalTokenFiles[subject])
			require.NoError(t, err)
			tokens[subject] = strings.TrimSpace(string(content))
			require.NotEmpty(t, tokens[subject])
			require.NotEqual(t, fixture.token, tokens[subject], "ordinary principals must not reuse the administrator credential")
		}
	}
	personalToken, err := readRBACCapacityPrivateFixture(config.PersonalTokenFile)
	require.NoError(t, err)
	var requests []rbacCapacityRequest
	if workload := config.DecisionWorkload; workload != nil {
		require.Equal(t, capacityModeDiagnostic, mode, "this bounded static parity driver is not sustained acceptance")
		require.Len(t, config.DecisionLogFiles, len(fixture.apis), "ordinary workload decisions require independent source and request joins")
		require.Len(t, config.SourceModelFiles, len(fixture.apis))
		require.NoError(t, workload.Schedule.validate())
		scheduled := rbacCapacityScheduledOffers(workload.Schedule.Duration, workload.Schedule.Rate)
		require.True(t, scheduled >= 20 && scheduled <= 240 && scheduled%20 == 0,
			"bounded diagnostic traces require complete 60/30/10 cycles on each API")
		tokens[config.Plan.PersonalSubject] = strings.TrimSpace(string(personalToken))
		requests = make([]rbacCapacityRequest, scheduled)
		for offer := range scheduled {
			request, err := config.Plan.request(offer, config.Count, workload.GroupMembers)
			require.NoError(t, err)
			require.NotEmpty(t, tokens[request.Subject], "every ordinary request must use its declared principal's credential")
			requests[offer] = request
		}
		if workload.PropagationDiagnostic {
			_, err := rbacCapacityPropagationNamespace(config.Plan, config.Count, workload.GroupMembers)
			require.NoError(t, err, "declare an organization principal without another independent grant before mutating the fixture")
		}
	}
	var next atomic.Int64
	var failed atomic.Bool
	var namespaceWriter sync.Mutex
	// Share each replica's observation budget across workers. The full native
	// readback costs 180 credits; four seconds and a four-query burst leave
	// room under the default 50-credit/second, 1000-credit principal budget.
	readbackSlots := make(map[string]<-chan time.Time)
	readbackContext, cancelReadbacks := context.WithCancel(t.Context())
	defer cancelReadbacks()
	for _, api := range fixture.apis {
		slots := make(chan time.Time, 4)
		for range cap(slots) {
			slots <- time.Now()
		}
		go func() {
			poll := time.NewTicker(4 * time.Second)
			defer poll.Stop()
			for {
				select {
				case <-readbackContext.Done():
					return
				case at := <-poll.C:
					select {
					case slots <- at:
					default:
					}
				}
			}
		}()
		readbackSlots[api] = slots
	}
	t.Run("seed", func(t *testing.T) {
		for worker := range config.Workers {
			t.Run(fmt.Sprintf("writer-%d", worker), func(t *testing.T) {
				t.Parallel()
				defer func() {
					if t.Failed() {
						failed.Store(true)
					}
				}()
				for !failed.Load() {
					index := int(next.Add(1)) - 1
					if index >= config.Count {
						return
					}
					seedRBACCapacityNamespace(t, fixture, config.Plan, index, strings.TrimSpace(string(personalToken)), config.ReceiptDirectory, &namespaceWriter, readbackSlots)
				}
			})
		}
	})
	require.False(t, failed.Load(), "partial seed receipts remain private; do not adopt or overwrite failed fixtures")
	seenUIDs := make(map[string]bool)
	receipts := make(map[string]rbacCapacitySeedReceipt, config.Count)
	for index := range config.Count {
		content, err := readRBACCapacityPrivateFixture(filepath.Join(config.ReceiptDirectory, config.Plan.namespace(index).Name+".json"))
		require.NoError(t, err)
		var record rbacCapacitySeedReceipt
		require.NoError(t, json.Unmarshal(content, &record))
		require.True(t, record.Complete)
		require.Len(t, record.Admissions, 6, "each native import must retain its successful admission acknowledgement")
		require.Equal(t, record.GitRevision, record.Admissions[5].GitRevision)
		require.Equal(t, len(config.Plan.namespace(index).Resources), record.Admissions[5].Resources)
		receipts[record.Namespace] = record
		require.Equal(t, config.Plan.Owner, record.Owner)
		require.Equal(t, config.Plan.namespace(index).Name, record.Namespace)
		require.Len(t, record.Roles, 10)
		require.Len(t, record.Bindings, 50)
		uids := []string{record.NamespaceNodeUID}
		for _, resources := range []map[string]string{record.Roles, record.Bindings} {
			for _, uid := range resources {
				uids = append(uids, uid)
			}
		}
		for _, uid := range uids {
			require.NotEmpty(t, uid)
			require.False(t, seenUIDs[uid], "native counts must not count duplicated or cross-namespace incarnations")
			seenUIDs[uid] = true
		}
	}
	probes := make([]map[string]rbacCapacityProbeExpectation, len(fixture.apis))
	for replica := range probes {
		probes[replica] = make(map[string]rbacCapacityProbeExpectation)
	}
	if len(tokens) != 0 {
		for index, probe := range []struct {
			name      string
			namespace int
			subject   int
			allow     bool
		}{
			{"unconditional-user", 0, 0, true},
			{"conditional-group", 1, 0, true},
			{"builtin-cluster-role", 0, 1, true},
			{"unresolved-role", 0, 2, false},
			{"unbound-subject", 0, 3, false},
		} {
			t.Run("ordinary-principal/"+probe.name, func(t *testing.T) {
				caller := fixture
				caller.token = tokens[config.Plan.Subjects[probe.subject]]
				for replica, api := range fixture.apis {
					id := fmt.Sprintf("%s-native-probe-%02d-%d", config.Plan.Owner, index, replica)
					namespace := config.Plan.namespace(probe.namespace).Name
					probes[replica][id] = rbacCapacityProbeExpectation{
						Subject: config.Plan.Subjects[probe.subject], Namespace: namespace, Action: "product.list", Allowed: probe.allow,
					}
					response := secretTestGraphQLWithHeaders(t, api, caller.token,
						`query($namespace:String!){products(namespace:$namespace,first:1){edges{node{id}}}}`,
						map[string]any{"namespace": namespace}, 10*time.Second, map[string]string{"X-Request-ID": id})
					if probe.allow {
						require.Equal(t, http.StatusOK, response.Status)
						require.Empty(t, response.Errors, "ordinary requests must evaluate the independently authored fixture")
					} else {
						rbacNativeErrorCode(t, response, "FORBIDDEN")
					}
				}
			})
		}
	}
	if workload := config.DecisionWorkload; workload != nil {
		scheduled := int64(len(requests))
		for _, request := range requests {
			probes[request.Replica][request.ID] = rbacCapacityProbeExpectation{
				Subject: request.Subject, Namespace: request.Namespace, Action: request.Action, Allowed: request.Expected == "allow",
			}
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		defer client.CloseIdleConnections()
		accounting, err := runRBACCapacitySchedule(t.Context(), workload.Schedule, func(ctx context.Context, offer int64) string {
			request := requests[offer]
			return executeRBACCapacityRequest(ctx, client, fixture.apis[request.Replica], tokens[request.Subject], request)
		})
		require.NoError(t, err)
		require.NoError(t, accounting.validate(scheduled, false))
		require.Equal(t, scheduled*6/10, accounting.Allowed)
		require.Equal(t, scheduled*3/10, accounting.Denied)
		require.Equal(t, scheduled/10, accounting.Invalid)
		t.Logf("bounded ordinary RBAC request parity: %+v; static diagnostic only, not propagation or sustained acceptance", accounting)
	}
	if len(config.SourceModelFiles) != 0 {
		require.Len(t, config.SourceModelFiles, len(fixture.apis))
		var previous, previousLog os.FileInfo
		for replica, path := range config.SourceModelFiles {
			current, err := os.Lstat(path)
			require.NoError(t, err)
			if previous != nil {
				require.False(t, os.SameFile(previous, current), "replica evidence must come from independent source files")
			}
			verifyRBACCapacitySeedSources(t, path, config.Plan, config.Count, receipts)
			if len(config.DecisionLogFiles) != 0 {
				currentLog, err := os.Lstat(config.DecisionLogFiles[replica])
				require.NoError(t, err)
				if previousLog != nil {
					require.False(t, os.SameFile(previousLog, currentLog), "replica decisions must come from independent log files")
				}
				verifyRBACCapacityProbeTraces(t, path, config.DecisionLogFiles[replica], config.Plan, config.Count,
					receipts, root, probes[replica])
				previousLog = currentLog
			}
			previous = current
		}
	}
	if workload := config.DecisionWorkload; workload != nil && workload.PropagationDiagnostic {
		runRBACCapacityPropagationDiagnostic(t, fixture, config.Plan, config.Count, workload.GroupMembers, tokens,
			config.ReceiptDirectory, receipts, config.SourceModelFiles, config.DecisionLogFiles, root)
	}
	t.Logf("native RBAC seeding completed: namespaces=%d roles=%d bindings=%d evidence_class=%s; this is not sustained workload acceptance",
		config.Count, config.Count*10, config.Count*50, mode)
}

func rbacCapacityPropagationNamespace(plan rbacCapacitySeedPlan, count int, groupMembers []string) (int, error) {
	if err := plan.validate(); err != nil {
		return 0, err
	}
	if count < 2 || count > 1000 {
		return 0, errors.New("RBAC propagation requires a bounded personal and organization working set")
	}
	groups := make(map[string]bool, len(groupMembers))
	subjects := make(map[string]bool, len(plan.Subjects))
	for _, subject := range plan.Subjects {
		subjects[subject] = true
	}
	for _, subject := range groupMembers {
		if !subjects[subject] || groups[subject] {
			return 0, errors.New("RBAC propagation requires distinct independently declared Group members")
		}
		groups[subject] = true
	}
	for index := 1; index < count; index++ {
		if !groups[plan.Subjects[index%len(plan.Subjects)]] {
			return index, nil
		}
	}
	return 0, errors.New("RBAC propagation cannot use a direct grantee with an independent Group grant")
}

func runRBACCapacityPropagationDiagnostic(t *testing.T, fixture rbacNativeFixture, plan rbacCapacitySeedPlan, count int,
	groupMembers []string, tokens map[string]string, receiptDirectory string, seeds map[string]rbacCapacitySeedReceipt,
	sourcePaths, decisionPaths []string, root rbacCapacityRootPolicy,
) {
	t.Helper()
	index, err := rbacCapacityPropagationNamespace(plan, count, groupMembers)
	require.NoError(t, err)
	namespace := plan.namespace(index)
	fixture.namespace = namespace.Name
	seed := seeds[namespace.Name]
	require.True(t, seed.Complete)
	subject := plan.Subjects[index%len(plan.Subjects)]
	require.NotEmpty(t, tokens[subject])
	type admission struct {
		Kind, Name          string
		GitRevision         string
		AcknowledgedAt      time.Time
		Generation          int64
		ReferenceGeneration int64
		Allowed             bool
		Latencies           [2]time.Duration
		Complete            bool
	}
	record := struct {
		Namespace, Subject string
		Admissions         []admission
		Complete           bool
	}{Namespace: namespace.Name, Subject: subject}
	receipt, err := os.OpenFile(filepath.Join(receiptDirectory, "propagation-diagnostic.json"),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err, "do not replace earlier or partial propagation receipts")
	defer func() {
		require.NoError(t, json.NewEncoder(receipt).Encode(record))
		require.NoError(t, receipt.Sync())
		require.NoError(t, receipt.Close())
	}()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	probes := [2]map[string]rbacCapacityProbeExpectation{{}, {}}
	generations := make(map[string]int64)
	references := make(map[string]int64)
	for _, kind := range []string{"Role", "RoleBinding"} {
		name, uid := "capacity-role-00", seed.Roles["capacity-role-00"]
		if kind == "RoleBinding" {
			name, uid = "capacity-binding-00", seed.Bindings["capacity-binding-00"]
		}
		for _, api := range fixture.apis {
			resource, response := fixture.lookup(t, api, kind, name)
			require.Empty(t, response.Errors)
			require.Equal(t, uid, resource.Metadata.UID)
			require.Positive(t, resource.Metadata.Generation)
			key := kind + "/" + name
			if previous, found := generations[key]; found {
				require.Equal(t, previous, resource.Metadata.Generation)
			}
			generations[key] = resource.Metadata.Generation
			if kind == "RoleBinding" {
				references[key] = resource.Metadata.Generation
			}
		}
	}
	history := []rbacCapacityAuthoredAdmission{{
		Namespace: namespace, Receipt: seed, AcknowledgedAt: seed.Admissions[5].AcknowledgedAt,
		Cases:       map[string]bool{subject + "/product.list": true},
		Generations: maps.Clone(generations),
		References:  maps.Clone(references),
	}}
	for replica, api := range fixture.apis {
		request := rbacCapacityRequest{
			ID: fmt.Sprintf("%s-propagation-baseline-%d", plan.Owner, replica), Subject: subject,
			Namespace: namespace.Name, Action: "product.list", Expected: "allow",
		}
		require.Equal(t, "allow", executeRBACCapacityRequest(t.Context(), client, api, tokens[subject], request))
		probes[replica][request.ID] = rbacCapacityProbeExpectation{
			Subject: subject, Namespace: namespace.Name, Action: request.Action, Allowed: true, ObservedAt: time.Now(),
		}
	}
	git := fixture.clone(t, "Role")
	tip, err := git.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	require.Equal(t, seed.GitRevision, "main@sha1:"+strings.TrimSpace(string(tip)),
		"mutate only the exact independently seeded canonical repository")
	for step, mutation := range []struct {
		kind     string
		resource int
		allowed  bool
	}{
		{"Role", 0, false}, {"Role", 0, true},
		{"RoleBinding", 10, false}, {"RoleBinding", 10, true},
	} {
		authored := plan.namespace(index)
		resource := authored.Resources[mutation.resource]
		if !mutation.allowed {
			spec := resource["spec"].(map[string]any)
			if mutation.kind == "Role" {
				spec["rules"].([]any)[0].(map[string]any)["verbs"] = []string{"read"}
			} else {
				spec["subjects"] = []any{map[string]any{"kind": "User", "name": plan.Prefix + "-filler-propagation"}}
			}
		}
		name := resource["metadata"].(map[string]any)["name"].(string)
		git.write(t, "rbac/"+strings.ToLower(mutation.kind)+"s/"+name+".yaml", resource)
		sha := git.commit(t)
		git.push(t)
		acknowledgedAt := time.Now()
		key := mutation.kind + "/" + name
		generations[key]++
		record.Admissions = append(record.Admissions, admission{
			Kind: mutation.kind, Name: name, Generation: generations[key],
			ReferenceGeneration: references[key],
			GitRevision:         "main@sha1:" + sha, AcknowledgedAt: acknowledgedAt, Allowed: mutation.allowed,
		})
		admitted := seed
		admitted.GitRevision = "main@sha1:" + sha
		history = append(history, rbacCapacityAuthoredAdmission{
			Namespace: authored, Receipt: admitted, AcknowledgedAt: acknowledgedAt,
			Cases:       map[string]bool{subject + "/product.list": mutation.allowed},
			Generations: maps.Clone(generations),
			References:  maps.Clone(references),
		})
		type observation struct {
			replica int
			id      string
			probe   rbacCapacityProbeExpectation
		}
		observations := make(chan observation, 102)
		latencies, err := observeRBACCapacityPropagation(t.Context(), acknowledgedAt, mutation.allowed,
			func(ctx context.Context, replica, attempt int) string {
				request := rbacCapacityRequest{
					ID:      fmt.Sprintf("%s-propagation-%d-%d-%02d", plan.Owner, step, replica, attempt),
					Subject: subject, Namespace: namespace.Name, Action: "product.list",
				}
				outcome := observeRBACCapacityRequest(ctx, client, fixture.apis[replica], tokens[subject], request)
				if outcome == "allow" || outcome == "deny" {
					observations <- observation{replica: replica, id: request.ID, probe: rbacCapacityProbeExpectation{
						Subject: subject, Namespace: namespace.Name, Action: request.Action,
						Allowed: outcome == "allow", ObservedAt: time.Now(),
					}}
				}
				return outcome
			})
		close(observations)
		for observation := range observations {
			probes[observation.replica][observation.id] = observation.probe
		}
		record.Admissions[step].Latencies = latencies
		require.NoError(t, err, "both APIs must deliver the committed change within the unchanged five-second bound")
		tip := strings.Fields(git.remoteTip(t))
		require.Len(t, tip, 2)
		require.Equal(t, sha, tip[0], "the observed propagation must retain its admitted canonical commit")
		for _, api := range fixture.apis {
			current, response := fixture.lookup(t, api, mutation.kind, name)
			require.Empty(t, response.Errors)
			uids := seed.Roles
			if mutation.kind == "RoleBinding" {
				uids = seed.Bindings
			}
			require.Equal(t, uids[name], current.Metadata.UID)
			require.Equal(t, generations[key], current.Metadata.Generation)
			require.Equal(t, rbacCapacitySeedSpec(t, resource["spec"], true), rbacCapacitySeedSpec(t, current.Spec, false))
		}
		record.Admissions[step].Complete = true
	}
	for _, api := range fixture.apis {
		role, roleResponse := fixture.lookup(t, api, "Role", "capacity-role-00")
		require.Empty(t, roleResponse.Errors)
		require.Equal(t, seed.Roles["capacity-role-00"], role.Metadata.UID,
			"grant/revoke diagnostics must preserve the admitted Role incarnation")
		require.Equal(t, rbacCapacitySeedSpec(t, plan.namespace(index).Resources[0]["spec"], true),
			rbacCapacitySeedSpec(t, role.Spec, false))
		current, response := fixture.lookup(t, api, "RoleBinding", "capacity-binding-00")
		require.Empty(t, response.Errors)
		require.Equal(t, seed.Bindings["capacity-binding-00"], current.Metadata.UID,
			"grant/revoke diagnostics must preserve the admitted binding incarnation")
		require.Equal(t, rbacCapacitySeedSpec(t, plan.namespace(index).Resources[10]["spec"], true),
			rbacCapacitySeedSpec(t, current.Spec, false))
	}
	for replica := range probes {
		require.NoError(t, verifyRBACCapacityDynamicTraces(t, sourcePaths[replica], decisionPaths[replica], history, root, probes[replica]))
	}
	record.Complete = true
	t.Log("bounded Role-rule and binding-subject grant/revoke diagnostics joined to authored Namespace history, mutation generations and fixed root intent; distributions, dynamic root history and sustained acceptance remain unverified")
}

type rbacCapacityAuthoredAdmission struct {
	Namespace      rbacCapacitySeedNamespace
	Receipt        rbacCapacitySeedReceipt
	AcknowledgedAt time.Time
	Cases          map[string]bool
	Generations    map[string]int64
	References     map[string]int64
}

func verifyRBACCapacityDynamicTraces(t *testing.T, sourcePath, decisionPath string, history []rbacCapacityAuthoredAdmission,
	root rbacCapacityRootPolicy, expected map[string]rbacCapacityProbeExpectation,
) error {
	t.Helper()
	require.True(t, len(history) >= 2 && len(history) <= 64)
	indexes := make(map[string]int, len(history))
	for index, admission := range history {
		require.NotEmpty(t, admission.Receipt.GitRevision)
		_, duplicate := indexes[admission.Receipt.GitRevision]
		require.False(t, duplicate, "independent admissions cannot reinterpret a previous Git revision")
		require.False(t, admission.AcknowledgedAt.IsZero())
		require.NotEmpty(t, admission.Generations, "dynamic source aliases require independently pinned mutation generations")
		require.NotEmpty(t, admission.References, "subject edits must retain the original immutable reference resolution generation")
		if index > 0 {
			require.True(t, admission.AcknowledgedAt.After(history[index-1].AcknowledgedAt))
			require.Equal(t, history[0].Receipt.NamespaceNodeUID, admission.Receipt.NamespaceNodeUID)
			require.Equal(t, history[0].Namespace.Name, admission.Namespace.Name)
			require.Equal(t, history[0].Receipt.Roles, admission.Receipt.Roles)
			require.Equal(t, history[0].Receipt.Bindings, admission.Receipt.Bindings)
		}
		indexes[admission.Receipt.GitRevision] = index
	}
	source, err := openRBACCapacityPrivateFixture(sourcePath, 1<<30)
	require.NoError(t, err)
	defer source.Close()
	models := make(map[rbacCapacityPolicyRevision]rbacCapacityProbeSource)
	admissions := make(map[rbacCapacityPolicyRevision]int)
	seen := make([]bool, len(history))
	err = walkRBACCapacitySourceModels(source, func(header rbacCapacitySourceHeader, resources []json.RawMessage) error {
		var origin struct {
			Namespace, GitRevision string
			Root                   bool
		}
		var state struct{ TierID string }
		require.NoError(t, json.Unmarshal(header.Origin, &origin))
		require.NoError(t, json.Unmarshal(header.State, &state))
		revision := rbacCapacityPolicyRevision{TierUID: state.TierID, GitRevision: origin.GitRevision, Digest: header.ContentDigest}
		if origin.Root {
			complete, err := checkRBACCapacityRootSource(header, resources, root)
			require.NoError(t, err)
			if !complete {
				return nil
			}
		} else {
			index, found := indexes[origin.GitRevision]
			if !found || origin.Namespace != history[0].Namespace.Name {
				return nil
			}
			admission := history[index]
			if !checkRBACCapacityNamespaceSource(t, header, resources, admission.Namespace, admission.Receipt,
				admission.Generations, admission.References) {
				return nil
			}
			admissions[revision], seen[index] = index, true
		}
		models[revision] = rbacCapacityProbeSource{Namespace: origin.Namespace, Root: origin.Root}
		require.LessOrEqual(t, len(models), 8192, "diagnostic source aliases must remain bounded")
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	for _, captured := range seen {
		require.True(t, captured, "each admitted policy must have an independently verified complete source")
	}
	for _, probe := range expected {
		require.False(t, probe.ObservedAt.IsZero(), "HTTP observations must retain their own completion clocks")
	}
	oracles := make(map[rbacCapacityPolicyRevision]*rbacCapacityOracle)
	decisions, err := openRBACCapacityPrivateFixture(decisionPath, 1<<30)
	require.NoError(t, err)
	defer decisions.Close()
	return checkRBACCapacityObservedTraces(decisions, expected, models,
		func(vector rbacCapacityPolicyVector, id string, allowed bool) error {
			oracle := oracles[vector.Cluster]
			if oracle == nil {
				oracle = newRBACCapacityOracle(5 * time.Second)
				// Root intent is pinned; only Namespace admissions advance in this diagnostic.
				for index, admission := range history {
					for revision, sourceIndex := range admissions {
						if sourceIndex == index {
							if err := oracle.commit(rbacCapacityPolicyVector{Cluster: vector.Cluster, Namespace: revision},
								history[0].AcknowledgedAt, admission.AcknowledgedAt, admission.Cases); err != nil {
								return err
							}
						}
					}
				}
				oracles[vector.Cluster] = oracle
			}
			probe := expected[id]
			return oracle.check(vector, probe.Subject+"/"+probe.Action, allowed, probe.ObservedAt)
		})
}

func rbacCapacityScheduledOffers(duration time.Duration, rate int64) int64 {
	if duration <= 0 || rate <= 0 {
		return 0
	}
	seconds, remainder := int64(duration/time.Second), int64(duration%time.Second)
	if seconds > math.MaxInt64/rate || remainder > 0 && rate > math.MaxInt64/remainder {
		return math.MaxInt64
	}
	whole, partial := seconds*rate, remainder*rate/int64(time.Second)
	if partial > math.MaxInt64-whole {
		return math.MaxInt64
	}
	return whole + partial
}

func rbacCapacityCase(offer int64, namespaces int) (int, string) {
	probe := "allow"
	if offer%10 >= 6 {
		probe = "deny"
	}
	if offer%10 == 9 {
		probe = "isolation"
	}
	return int(offer/10) % namespaces, probe
}

type rbacCapacityAccounting struct {
	Offered, Completed, Dropped, Allowed, Denied, Invalid, Unavailable, Wrong int64
}

type rbacCapacityScheduleConfig struct {
	Duration, Drain  time.Duration
	Rate             int64
	Workers, Pending int
}

func (c rbacCapacityScheduleConfig) validate() error {
	if c.Duration <= 0 || c.Duration > time.Hour || c.Drain <= 0 || c.Drain > 30*time.Second ||
		c.Rate <= 0 || c.Rate > 10000 || c.Workers <= 0 || c.Workers > 256 ||
		c.Pending <= 0 || c.Pending > 4096 {
		return errors.New("RBAC scheduler limits exceed the bounded workload envelope")
	}
	return nil
}

func runRBACCapacitySchedule(ctx context.Context, cfg rbacCapacityScheduleConfig, execute func(context.Context, int64) string) (rbacCapacityAccounting, error) {
	if err := cfg.validate(); err != nil {
		return rbacCapacityAccounting{}, err
	}
	if execute == nil {
		return rbacCapacityAccounting{}, errors.New("RBAC scheduler limits exceed the bounded workload envelope")
	}
	scheduled := rbacCapacityScheduledOffers(cfg.Duration, cfg.Rate)
	jobs := make(chan int64, cfg.Pending)
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var counts [5]atomic.Int64
	var workers sync.WaitGroup
	for range cfg.Workers {
		workers.Go(func() {
			for offer := range jobs {
				if workCtx.Err() != nil {
					return
				}
				outcome := 4
				switch execute(workCtx, offer) {
				case "allow":
					outcome = 0
				case "deny":
					outcome = 1
				case "isolation":
					outcome = 2
				case "unavailable":
					outcome = 3
				}
				counts[outcome].Add(1)
			}
		})
	}
	started := time.Now()
	pendingWindow := time.Duration(int64(cfg.Pending) * int64(time.Second) / cfg.Rate)
schedule:
	for offer := int64(0); offer < scheduled; offer++ {
		due := started.Add(time.Duration(offer * int64(time.Second) / cfg.Rate))
		if delay := time.Until(due); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				break schedule
			case <-timer.C:
			}
		}
		if ctx.Err() != nil || time.Since(started) >= cfg.Duration {
			break
		}
		if time.Since(due) > pendingWindow {
			continue
		}
		select {
		case jobs <- offer:
		default:
		}
	}
	if remaining := time.Until(started.Add(cfg.Duration)); remaining > 0 && ctx.Err() == nil {
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	close(jobs)
	drained := make(chan struct{})
	go func() {
		workers.Wait()
		close(drained)
	}()
	timer := time.NewTimer(cfg.Drain)
	select {
	case <-drained:
		timer.Stop()
	case <-ctx.Done():
		timer.Stop()
		cancel()
		<-drained
	case <-timer.C:
		cancel()
		<-drained
	}
	accounting := rbacCapacityAccounting{
		Offered: scheduled, Allowed: counts[0].Load(), Denied: counts[1].Load(),
		Invalid: counts[2].Load(), Unavailable: counts[3].Load(), Wrong: counts[4].Load(),
	}
	accounting.Completed = accounting.Allowed + accounting.Denied + accounting.Invalid + accounting.Unavailable + accounting.Wrong
	accounting.Dropped = scheduled - accounting.Completed
	return accounting, ctx.Err()
}

func (a rbacCapacityAccounting) validate(scheduled int64, faults bool) error {
	counts := []int64{a.Offered, a.Completed, a.Dropped, a.Allowed, a.Denied, a.Invalid, a.Unavailable, a.Wrong}
	for _, count := range counts {
		if count < 0 || count > scheduled {
			return errors.New("RBAC workload counter outside the scheduled envelope")
		}
	}
	remaining := a.Completed
	for _, count := range []int64{a.Allowed, a.Denied, a.Invalid, a.Unavailable, a.Wrong} {
		if count > remaining {
			return errors.New("RBAC workload reports more outcomes than completions")
		}
		remaining -= count
	}
	if scheduled <= 0 || a.Offered != scheduled || a.Completed != a.Offered-a.Dropped || remaining != 0 {
		return errors.New("RBAC offered/completed/outcome accounting is incomplete")
	}
	if a.Dropped != 0 || a.Wrong != 0 || !faults && a.Unavailable != 0 {
		return errors.New("RBAC workload has drops, wrong decisions or unplanned unavailability")
	}
	return nil
}

type rbacCapacityPolicyRevision struct {
	TierUID, GitRevision, Digest string
}

type rbacCapacityPolicyVector struct {
	Cluster, Namespace rbacCapacityPolicyRevision
}

type rbacCapacityTierCommit struct {
	admitted, superseded time.Time
}

type rbacCapacityTierHistory struct {
	root    bool
	latest  string
	commits map[string]rbacCapacityTierCommit
}

type rbacCapacityOracle struct {
	mu      sync.RWMutex
	bound   time.Duration
	history map[rbacCapacityPolicyVector]map[string]bool
	tiers   map[string]*rbacCapacityTierHistory
}

func newRBACCapacityOracle(bound time.Duration) *rbacCapacityOracle {
	return &rbacCapacityOracle{
		bound: bound, history: make(map[rbacCapacityPolicyVector]map[string]bool),
		tiers: make(map[string]*rbacCapacityTierHistory),
	}
}

func (o *rbacCapacityOracle) commit(vector rbacCapacityPolicyVector, clusterAt, namespaceAt time.Time, cases map[string]bool) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.bound <= 0 || clusterAt.IsZero() || namespaceAt.IsZero() || len(cases) == 0 || len(cases) > 128 {
		return errors.New("RBAC oracle requires bounded independent committed policy")
	}
	revisions := []rbacCapacityPolicyRevision{vector.Cluster, vector.Namespace}
	times := []time.Time{clusterAt, namespaceAt}
	for index, revision := range revisions {
		if revision.TierUID == "" || revision.GitRevision == "" || revision.Digest == "" {
			return errors.New("RBAC oracle revision identity is incomplete")
		}
		if tier := o.tiers[revision.TierUID]; tier != nil {
			if tier.root != (index == 0) {
				return errors.New("RBAC oracle cannot reinterpret a tier's scope")
			}
			if committed, found := tier.commits[revision.GitRevision]; found {
				if !committed.admitted.Equal(times[index]) {
					return errors.New("RBAC source aliases cannot restart a tier's admission clock")
				}
			} else if !times[index].After(tier.commits[tier.latest].admitted) {
				return errors.New("RBAC oracle commits must preserve independent per-tier admission order")
			}
		}
	}
	if vector.Cluster.TierUID == vector.Namespace.TierUID {
		return errors.New("RBAC oracle cluster and namespace identities must be independent")
	}
	for previous, expected := range o.history {
		if previous.Cluster.TierUID == vector.Cluster.TierUID &&
			previous.Cluster.GitRevision == vector.Cluster.GitRevision &&
			previous.Namespace.TierUID == vector.Namespace.TierUID &&
			previous.Namespace.GitRevision == vector.Namespace.GitRevision &&
			!reflect.DeepEqual(expected, cases) {
			return errors.New("RBAC source aliases must preserve independently verified policy")
		}
	}
	if _, found := o.history[vector]; found {
		return nil
	}
	if len(o.history) >= 8192 {
		return errors.New("RBAC oracle source history exceeds its bound")
	}
	expected := make(map[string]bool, len(cases))
	for name, allowed := range cases {
		if name == "" {
			return errors.New("RBAC oracle probe identity is empty")
		}
		expected[name] = allowed
	}
	for index, revision := range revisions {
		tier := o.tiers[revision.TierUID]
		if tier == nil {
			tier = &rbacCapacityTierHistory{root: index == 0, commits: make(map[string]rbacCapacityTierCommit)}
			o.tiers[revision.TierUID] = tier
		}
		if _, found := tier.commits[revision.GitRevision]; found {
			continue
		}
		if tier.latest != "" {
			previous := tier.commits[tier.latest]
			previous.superseded = times[index]
			tier.commits[tier.latest] = previous
		}
		tier.commits[revision.GitRevision] = rbacCapacityTierCommit{admitted: times[index]}
		tier.latest = revision.GitRevision
	}
	o.history[vector] = expected
	return nil
}

func (o *rbacCapacityOracle) check(vector rbacCapacityPolicyVector, probe string, allowed bool, at time.Time) error {
	o.mu.RLock()
	defer o.mu.RUnlock()
	policy, found := o.history[vector]
	if !found {
		return errors.New("RBAC decision evaluated an independently unverified policy revision")
	}
	expected, found := policy[probe]
	if !found || expected != allowed {
		return errors.New("RBAC decision disagrees with independent committed policy")
	}
	for _, revision := range []rbacCapacityPolicyRevision{vector.Cluster, vector.Namespace} {
		committed := o.tiers[revision.TierUID].commits[revision.GitRevision]
		if at.Before(committed.admitted) {
			return errors.New("RBAC decision predates an independently admitted tier")
		}
		if !committed.superseded.IsZero() && at.After(committed.superseded.Add(o.bound)) {
			return errors.New("RBAC decision retained a superseded tier beyond the propagation bound")
		}
	}
	return nil
}

func openRBACCapacityPrivateFixture(path string, maxBytes int64) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0 || before.Size() > maxBytes {
		return nil, errors.New("RBAC fixture must be a private regular file within its read bound")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	current, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(before, current) || current.Mode().Perm()&0077 != 0 {
		_ = file.Close()
		return nil, errors.New("RBAC private fixture changed while opening")
	}
	return file, nil
}

func readRBACCapacityPrivateFixture(path string) ([]byte, error) {
	file, err := openRBACCapacityPrivateFixture(path, 1<<20)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("RBAC private fixture exceeded its read bound")
	}
	return data, nil
}

type rbacCapacityProcess struct {
	ID, Role, Owner, Epoch, Checkpoint string
	Running                            bool
}

type rbacCapacityTopology struct {
	Owner                                         string
	NativeNamespaces, NativeRoles, NativeBindings int
	Processes                                     []rbacCapacityProcess
}

func (v rbacCapacityTopology) validate() error {
	if v.Owner == "" || v.NativeNamespaces != 1000 || v.NativeRoles != 10000 || v.NativeBindings != 50000 {
		return errors.New("RBAC capacity requires the exact independently counted native dataset")
	}
	roles, ids := make(map[string]int), make(map[string]bool)
	var checkpoints []string
	for _, process := range v.Processes {
		if process.Owner != v.Owner || process.ID == "" || process.Epoch == "" || !process.Running || ids[process.ID] {
			return errors.New("RBAC topology contains a foreign, duplicate, stopped or unidentified process")
		}
		ids[process.ID] = true
		roles[process.Role]++
		if process.Role == "controller" {
			if !filepath.IsAbs(process.Checkpoint) || filepath.Clean(process.Checkpoint) != process.Checkpoint || process.Checkpoint == "/" {
				return errors.New("RBAC controller checkpoint path is not an explicit private directory")
			}
			for _, previous := range checkpoints {
				if previous == process.Checkpoint || strings.HasPrefix(previous, process.Checkpoint+"/") || strings.HasPrefix(process.Checkpoint, previous+"/") {
					return errors.New("RBAC controller checkpoints overlap")
				}
			}
			checkpoints = append(checkpoints, process.Checkpoint)
		}
	}
	scyllaPeers := roles["scylla"]
	delete(roles, "scylla")
	if scyllaPeers < 1 || !reflect.DeepEqual(roles, map[string]int{"api": 2, "controller": 2, "git": 1}) {
		return errors.New("RBAC capacity requires two APIs, two controllers, one Git and shared Scylla")
	}
	return nil
}

type rbacCapacityCounterSegment struct {
	Epoch         string
	Before, After uint64
}

func rbacCapacityCounterDelta(segments []rbacCapacityCounterSegment) (uint64, error) {
	seen := make(map[string]bool)
	var total uint64
	if len(segments) == 0 {
		return 0, errors.New("RBAC SDK counter evidence has no process epochs")
	}
	for _, segment := range segments {
		if segment.Epoch == "" || seen[segment.Epoch] || segment.After < segment.Before {
			return 0, errors.New("RBAC SDK counter reset or ambiguous process epoch")
		}
		seen[segment.Epoch] = true
		delta := segment.After - segment.Before
		if delta > math.MaxUint64-total {
			return 0, errors.New("RBAC SDK counter delta overflow")
		}
		total += delta
	}
	return total, nil
}

type rbacCapacityFaultWindow struct {
	Replica            int
	Start, End         time.Time
	Injected, Restored bool
	OldEpoch, NewEpoch string
}

func (w rbacCapacityFaultWindow) unavailable(replica int, at time.Time, epoch string) error {
	if !w.Injected || !w.Restored || w.Replica != replica || w.Replica < 0 || w.Replica > 1 ||
		w.Start.IsZero() || !w.End.After(w.Start) || w.End.Sub(w.Start) > 30*time.Second ||
		at.Before(w.Start) || at.After(w.End) || w.OldEpoch == "" || w.NewEpoch == "" ||
		epoch != w.OldEpoch && epoch != w.NewEpoch {
		return errors.New("RBAC unavailability is outside a confirmed replica/process fault window")
	}
	return nil
}

type capacityConfig struct {
	apiA, apiB, replacement, token string
	replacementTrigger             string
	duration                       time.Duration
	replacementDelay               time.Duration
	subscribers                    int
	replayEvents                   int
	resourcePool                   int
	replaySamples                  int
	replayCatchupTimeout           time.Duration
	burstInterval                  time.Duration
	burstSize                      int
	mutationWorkers                int
	mutationDrainTimeout           time.Duration
	baselineStabilization          time.Duration
	postLoadStabilization          time.Duration
	allowMissingMetrics            bool
	skipReplacement                bool
	mode                           capacityMode
}

type capacityMode string

const (
	capacityModeDiagnostic capacityMode = "diagnostic"
	capacityModeAlpha      capacityMode = "alpha"
	capacityModeProduction capacityMode = "production"
)

func parseCapacityMode(value string) (capacityMode, error) {
	if value == "" {
		// Preserve the historical deployment-gate behavior for direct `go test`
		// callers while the Makefile always supplies an explicit evidence class.
		return capacityModeProduction, nil
	}
	mode := capacityMode(value)
	switch mode {
	case capacityModeDiagnostic, capacityModeAlpha, capacityModeProduction:
		return mode, nil
	default:
		return "", fmt.Errorf("MODE must be diagnostic, alpha, or production")
	}
}

func validateCapacityOverrides(mode capacityMode, skipReplacement, allowMissingMetrics bool) error {
	if skipReplacement && mode != capacityModeDiagnostic {
		return fmt.Errorf("NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT is only valid in diagnostic mode")
	}
	if allowMissingMetrics && mode != capacityModeDiagnostic {
		return fmt.Errorf("NAMESPACE_WATCH_CAPACITY_ALLOW_MISSING_METRICS is only valid in diagnostic mode")
	}
	return nil
}

func validateCapacityWorkloadScale(cfg capacityConfig) error {
	if cfg.mode == capacityModeDiagnostic {
		return nil
	}
	if cfg.duration < 60*time.Minute {
		return fmt.Errorf("%s evidence requires NAMESPACE_WATCH_CAPACITY_DURATION>=60m", cfg.mode)
	}
	if cfg.subscribers < 1000 {
		return fmt.Errorf("%s evidence requires NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS>=1000", cfg.mode)
	}
	if cfg.replayEvents < 10000 {
		return fmt.Errorf("%s evidence requires NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS>=10000", cfg.mode)
	}
	if cfg.burstSize < 100 {
		return fmt.Errorf("%s evidence requires NAMESPACE_WATCH_CAPACITY_BURST_SIZE>=100", cfg.mode)
	}
	if cfg.burstInterval <= 0 || cfg.burstInterval >= cfg.duration {
		return fmt.Errorf("%s evidence requires a positive NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL shorter than the run duration", cfg.mode)
	}
	return nil
}

func (m capacityMode) visibilityLimits() (time.Duration, time.Duration, bool) {
	switch m {
	case capacityModeDiagnostic:
		return time.Second, 3 * time.Second, false
	case capacityModeAlpha:
		return 2 * time.Second, 3 * time.Second, true
	default:
		return time.Second, 3 * time.Second, true
	}
}

func assertCapacityVisibility(t *testing.T, mode capacityMode, p95, p99 time.Duration) {
	t.Helper()
	p95Limit, p99Limit, enforce := mode.visibilityLimits()
	if p95 > time.Second {
		t.Logf("WARNING: %s evidence exceeds the production visibility target: p95=%s target=%s", mode, p95, time.Second)
	}
	if p99 > 3*time.Second {
		t.Logf("WARNING: %s evidence exceeds the production visibility target: p99=%s target=%s", mode, p99, 3*time.Second)
	}
	if !enforce {
		t.Logf("diagnostic evidence: latency thresholds are reported but do not produce passing capacity-gate evidence")
		return
	}
	require.LessOrEqualf(t, p95, p95Limit, "%s event visibility p95 must be <=%s", mode, p95Limit)
	require.LessOrEqualf(t, p99, p99Limit, "%s event visibility p99 must be <=%s", mode, p99Limit)
}

// TestNamespaceWatchDeploymentCapacity is deliberately deployment-driven. The
// two URLs must identify distinct API processes backed by the same Scylla
// deployment; no in-process journal or resolver substitute is used here.
func TestNamespaceWatchDeploymentCapacity(t *testing.T) {
	if os.Getenv("NAMESPACE_WATCH_CAPACITY_RUN") != "1" {
		t.Skip("run through make capacity TARGET=namespace PROFILE=watch MODE=production against a deployed two-replica stack")
	}
	cfg := loadCapacityConfig(t)
	t.Logf("capacity evidence class=%s production_visibility_target_p95=%s production_visibility_target_p99=%s", cfg.mode, time.Second, 3*time.Second)
	require.NotEqual(t, cfg.apiA, cfg.apiB, "capacity gate requires two distinct API endpoints")
	readinessClient := &http.Client{Timeout: 2 * time.Second}
	require.True(t, endpointReady(readinessClient, cfg.apiA), "API A must pass /health and /ready before the capacity gate starts")
	require.True(t, endpointReady(readinessClient, cfg.apiB), "API B must pass /health and /ready before the capacity gate starts")

	client := &http.Client{Timeout: 15 * time.Second}
	runID := strconv.FormatInt(time.Now().UnixNano(), 36)
	poolPrefix := "nwcp-" + runID + "-"
	replayPrefix := "nwcr-" + runID + "-"
	livePrefix := "nwcl-" + runID + "-"

	seedStarted := time.Now()
	createCapacityRange(t, client, cfg, poolPrefix, cfg.resourcePool)
	t.Logf("prepared bounded pool of %d Namespace resources in %s", cfg.resourcePool, time.Since(seedStarted))
	replayCursor := capacityBootstrapCursor(t, cfg.apiA, cfg.token)
	replayStarted := time.Now()
	updateCapacityRange(t, client, cfg, poolPrefix, replayPrefix, cfg.replayEvents)
	t.Logf("prepared %d acknowledged GraphQL transitions through both replicas in %s", cfg.replayEvents, time.Since(replayStarted))
	waitCapacityReplayCatchup(t, cfg, replayCursor, replayPrefix)

	replayDurations := runCapacityReplays(t, cfg, replayCursor, replayPrefix)
	replayP95 := percentileDuration(replayDurations, .95)
	require.LessOrEqual(t, replayP95, 5*time.Second, "10,000-event GraphQL replay p95 must be <=5s")

	maxTransitions := int(cfg.duration/(100*time.Millisecond)) +
		(int(cfg.duration/cfg.burstInterval)+2)*cfg.burstSize + 128
	subscribers := openCapacitySubscribers(t, cfg, maxTransitions)
	defer closeCapacitySubscribers(subscribers)

	readCtx, stopReaders := context.WithCancel(context.Background())
	defer stopReaders()
	readerErrors := make(chan error, len(subscribers))
	var readers sync.WaitGroup
	var latencyMu sync.Mutex
	latenciesByEndpoint := map[string][]time.Duration{
		cfg.apiA: make([]time.Duration, 0, 8*maxTransitions),
		cfg.apiB: make([]time.Duration, 0, 8*maxTransitions),
	}
	for i, subscriber := range subscribers {
		readers.Add(1)
		go func(index int, state *capacitySubscriber) {
			defer readers.Done()
			if err := state.read(readCtx, livePrefix, index < 16, &latencyMu, latenciesByEndpoint); err != nil && readCtx.Err() == nil {
				readerErrors <- fmt.Errorf("subscriber %d: %w", index, err)
			}
		}(i, subscriber)
	}

	warmupCfg := cfg
	warmupCfg.duration = 10 * time.Second
	warmupCfg.burstInterval = time.Minute
	warmup := runCapacityMutationWorkload(t, client, warmupCfg, poolPrefix, "nwcw-"+runID+"-", 256)
	require.Zero(t, warmup.backpressured, "subscriber warm-up must not drop work")
	require.Zero(t, warmup.failed, "subscriber warm-up mutations must succeed")
	require.False(t, warmup.drainTimedOut, "subscriber warm-up must drain")
	require.Equal(t, warmup.produced, warmup.acknowledged.count(), "subscriber warm-up must be acknowledged")
	metricsStart := scrapeCapacityMetrics(t, client, cfg)
	metricsStart = scrapeStabilizedCapacityBaseline(t, client, cfg, metricsStart, cfg.baselineStabilization, 5*time.Second)
	soakStarted := time.Now()
	recoveryResult := make(chan capacityRecoveryResult, 1)
	if cfg.skipReplacement {
		recoveryResult <- capacityRecoveryResult{}
	} else {
		go func() {
			recoveryResult <- coordinateCapacityReplacement(client, cfg, poolPrefix, livePrefix, maxTransitions+1, soakStarted)
		}()
	}
	workload := runCapacityMutationWorkload(t, client, cfg, poolPrefix, livePrefix, maxTransitions)
	soakElapsed := time.Since(soakStarted)
	recovery := <-recoveryResult
	t.Logf("load accounting produced=%d enqueued=%d backpressured=%d attempted=%d admitted=%d acknowledged=%d failed=%d drain_timed_out=%t",
		workload.produced, workload.enqueued, workload.backpressured, workload.attempted, workload.admitted,
		workload.acknowledged.count(), workload.failed, workload.drainTimedOut)
	if workload.firstFailure != nil {
		t.Logf("first mutation failure: %v", workload.firstFailure)
	}
	require.NoError(t, recovery.err)
	if !cfg.skipReplacement {
		t.Logf("replacement recovery endpoint=%s duration=%s before_stop_at=%s after_start_at=%s process_start_before=%.0f process_start_after=%.0f",
			cfg.replacement, recovery.duration, recovery.beforeStopAt, recovery.afterStartAt,
			recovery.beforeStop.processStart, recovery.afterStart.processStart)
		require.LessOrEqual(t, recovery.duration, 30*time.Second, "replacement endpoint must recover within 30s")
	}
	metricsEnd := scrapeCapacityMetrics(t, client, cfg)
	metricsElapsed := time.Since(soakStarted)
	metricsEnd = scrapeStabilizedCapacityMemory(t, client, cfg, metricsEnd, cfg.postLoadStabilization, 5*time.Second)
	assertCapacityMetrics(t, metricsStart, metricsEnd, metricsElapsed, recovery, cfg.allowMissingMetrics)

	require.Greater(t, workload.attempted, 0)
	assert.False(t, workload.drainTimedOut, "mutation workers must drain within the bounded shutdown window")
	assert.Equal(t, workload.produced, workload.sustained+workload.bursts, "all produced work must be classified")
	assert.Zero(t, workload.backpressured, "configured load must not be dropped by the bounded worker queue")
	assert.Equal(t, workload.produced, workload.enqueued, "every produced transition must enter the bounded worker queue")
	assert.Equal(t, workload.enqueued, workload.attempted, "every enqueued transition must be attempted before shutdown")
	assert.Equal(t, workload.attempted, workload.admitted+workload.failed, "every attempt must be admitted or reported failed")
	assert.Equal(t, workload.admitted, workload.acknowledged.count(), "admitted transitions must match acknowledged transitions")
	errorRate := float64(workload.failed) / float64(workload.attempted)
	assert.Less(t, errorRate, .001, "internal GraphQL mutation error rate must remain below 0.1%%")
	expectedSustained := int(cfg.duration/(100*time.Millisecond)) - 2
	if expectedSustained > 0 {
		assert.GreaterOrEqual(t, workload.sustained, expectedSustained, "must schedule 10 sustained transitions/second")
	}
	expectedBurstRounds := int(cfg.duration / cfg.burstInterval)
	if cfg.duration%cfg.burstInterval == 0 && expectedBurstRounds > 0 {
		// A tick exactly at the context deadline is not part of the measured window.
		expectedBurstRounds--
	}
	if expectedBurstRounds > 0 {
		assert.GreaterOrEqual(t, workload.bursts, expectedBurstRounds*cfg.burstSize,
			"must produce every configured 100/s burst inside the measured window")
	}

	catchupDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(catchupDeadline) && capacityMissing(subscribers, workload.acknowledged) != 0 {
		time.Sleep(100 * time.Millisecond)
	}
	missing := capacityMissing(subscribers, workload.acknowledged)
	assert.Zero(t, missing, "every subscriber must observe every acknowledged transition")
	duplicateDeliveries := capacityDuplicateDeliveries(subscribers)
	t.Logf("measured duplicate deliveries across subscribers=%d", duplicateDeliveries)

	latencyMu.Lock()
	observedLatencies := make([]time.Duration, 0, len(latenciesByEndpoint[cfg.apiA])+len(latenciesByEndpoint[cfg.apiB]))
	for _, endpoint := range []string{cfg.apiA, cfg.apiB} {
		endpointLatencies := latenciesByEndpoint[endpoint]
		observedLatencies = append(observedLatencies, endpointLatencies...)
		if len(endpointLatencies) > 0 {
			t.Logf("event visibility endpoint=%s samples=%d p50=%s p95=%s p99=%s",
				endpoint, len(endpointLatencies), percentileDuration(endpointLatencies, .50),
				percentileDuration(endpointLatencies, .95), percentileDuration(endpointLatencies, .99))
		}
	}
	latencyMu.Unlock()
	require.NotEmpty(t, observedLatencies)
	p50 := percentileDuration(observedLatencies, .50)
	p90 := percentileDuration(observedLatencies, .90)
	p95 := percentileDuration(observedLatencies, .95)
	p99 := percentileDuration(observedLatencies, .99)
	maximum := percentileDuration(observedLatencies, 1)
	t.Logf("event visibility samples=%d p50=%s p90=%s p95=%s p99=%s max=%s",
		len(observedLatencies), p50, p90, p95, p99, maximum)
	assertCapacityVisibility(t, cfg.mode, p95, p99)

	stopReaders()
	closeCapacitySubscribers(subscribers)
	readers.Wait()
	close(readerErrors)
	for err := range readerErrors {
		require.NoError(t, err)
	}
	t.Logf("evidence_class=%s duration=%s load_elapsed=%s subscribers=%d produced=%d enqueued=%d backpressured=%d attempted=%d admitted=%d acknowledged=%d duplicates=%d errors=%d error_rate=%.4f%% p95=%s p99=%s replay_samples=%d replay_p95=%s recovery=%s",
		cfg.mode, cfg.duration, soakElapsed, cfg.subscribers, workload.produced, workload.enqueued, workload.backpressured,
		workload.attempted, workload.admitted, workload.acknowledged.count(), duplicateDeliveries, workload.failed,
		100*errorRate, p95, p99, len(replayDurations), replayP95, recovery.duration)
}

func loadCapacityConfig(t *testing.T) capacityConfig {
	t.Helper()
	mode, err := parseCapacityMode(os.Getenv("MODE"))
	require.NoError(t, err)
	duration := capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_DURATION", 60*time.Minute)
	cfg := capacityConfig{
		apiA:                 strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_A"), "/"),
		apiB:                 strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_B"), "/"),
		replacement:          strings.TrimSuffix(os.Getenv("NAMESPACE_WATCH_API_REPLACEMENT"), "/"),
		token:                namespaceWatchToken(t),
		replacementTrigger:   os.Getenv("NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE"),
		duration:             duration,
		replacementDelay:     capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY", duration/2),
		subscribers:          capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS", 1000),
		replayEvents:         capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS", 10000),
		resourcePool:         capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_RESOURCE_POOL", 50),
		replaySamples:        capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES", 20),
		replayCatchupTimeout: capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_REPLAY_CATCHUP_TIMEOUT", 15*time.Minute),
		burstInterval:        capacityEnvDuration(t, "NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL", time.Minute),
		burstSize:            capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_BURST_SIZE", 100),
		mutationWorkers:      capacityEnvInt(t, "NAMESPACE_WATCH_CAPACITY_MUTATION_WORKERS", 20),
		mutationDrainTimeout: capacityMutationDrainTimeout,
		baselineStabilization: capacityEnvDuration(
			t, "NAMESPACE_WATCH_CAPACITY_BASELINE_STABILIZATION", 5*time.Minute,
		),
		postLoadStabilization: capacityEnvDuration(
			t, "NAMESPACE_WATCH_CAPACITY_POST_LOAD_STABILIZATION", 10*time.Minute,
		),
		allowMissingMetrics: os.Getenv("NAMESPACE_WATCH_CAPACITY_ALLOW_MISSING_METRICS") == "1",
		skipReplacement:     os.Getenv("NAMESPACE_WATCH_CAPACITY_SKIP_REPLACEMENT") == "1",
		mode:                mode,
	}
	require.NoError(t, validateCapacityOverrides(cfg.mode, cfg.skipReplacement, cfg.allowMissingMetrics))
	require.NoError(t, validateCapacityWorkloadScale(cfg))
	require.NotEmpty(t, cfg.apiA, "NAMESPACE_WATCH_API_A is required")
	require.NotEmpty(t, cfg.apiB, "NAMESPACE_WATCH_API_B is required")
	if !cfg.skipReplacement {
		require.NotEmpty(t, cfg.replacement, "NAMESPACE_WATCH_API_REPLACEMENT is required for rolling-replacement evidence")
		require.Contains(t, []string{cfg.apiA, cfg.apiB}, cfg.replacement, "replacement endpoint must identify one of the two loaded replicas")
		require.NotEmpty(t, cfg.replacementTrigger, "NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE is required for deployment-harness coordination")
		_, triggerErr := os.Stat(cfg.replacementTrigger)
		require.ErrorIs(t, triggerErr, os.ErrNotExist, "replacement trigger must not exist before the gate starts")
	}
	require.NotEmpty(t, cfg.token, "NAMESPACE_WATCH_TOKEN or NAMESPACE_WATCH_TOKEN_FILE is required")
	require.Positive(t, cfg.duration)
	if !cfg.skipReplacement {
		require.Positive(t, cfg.replacementDelay)
		require.Less(t, cfg.replacementDelay, cfg.duration)
	}
	require.Positive(t, cfg.subscribers)
	require.Positive(t, cfg.replayEvents)
	require.Positive(t, cfg.resourcePool)
	require.Positive(t, cfg.replaySamples)
	require.Positive(t, cfg.replayCatchupTimeout)
	require.Positive(t, cfg.burstInterval)
	require.Positive(t, cfg.burstSize)
	require.Positive(t, cfg.mutationWorkers)
	return cfg
}

func TestCapacityVisibilityPolicy(t *testing.T) {
	t.Run("production preserves the product SLO", func(t *testing.T) {
		assertCapacityVisibility(t, capacityModeProduction, time.Second, 3*time.Second)
	})
	t.Run("alpha permits bounded early-environment variance", func(t *testing.T) {
		assertCapacityVisibility(t, capacityModeAlpha, 2*time.Second, 3*time.Second)
	})
	t.Run("diagnostic never becomes passing latency evidence", func(t *testing.T) {
		assertCapacityVisibility(t, capacityModeDiagnostic, 10*time.Second, 10*time.Second)
	})
	for _, value := range []string{"diagnostic", "alpha", "production", ""} {
		_, err := parseCapacityMode(value)
		require.NoError(t, err)
	}
	_, err := parseCapacityMode("gate")
	require.Error(t, err)
	require.NoError(t, validateCapacityOverrides(capacityModeDiagnostic, true, true))
	require.NoError(t, validateCapacityOverrides(capacityModeProduction, false, false))
	require.Error(t, validateCapacityOverrides(capacityModeAlpha, true, false))
	require.Error(t, validateCapacityOverrides(capacityModeProduction, true, false))
	require.Error(t, validateCapacityOverrides(capacityModeAlpha, false, true))
	require.Error(t, validateCapacityOverrides(capacityModeProduction, false, true))

	minimum := capacityConfig{
		mode:          capacityModeProduction,
		duration:      60 * time.Minute,
		subscribers:   1000,
		replayEvents:  10000,
		burstInterval: time.Minute,
		burstSize:     100,
	}
	require.NoError(t, validateCapacityWorkloadScale(minimum))
	for name, mutate := range map[string]func(*capacityConfig){
		"duration":       func(cfg *capacityConfig) { cfg.duration = time.Second },
		"subscribers":    func(cfg *capacityConfig) { cfg.subscribers = 1 },
		"replay events":  func(cfg *capacityConfig) { cfg.replayEvents = 1 },
		"burst size":     func(cfg *capacityConfig) { cfg.burstSize = 1 },
		"burst interval": func(cfg *capacityConfig) { cfg.burstInterval = cfg.duration },
	} {
		t.Run("rejects undersized "+name, func(t *testing.T) {
			cfg := minimum
			mutate(&cfg)
			require.Error(t, validateCapacityWorkloadScale(cfg))
		})
	}
	diagnostic := minimum
	diagnostic.mode = capacityModeDiagnostic
	diagnostic.duration = time.Second
	diagnostic.subscribers = 1
	diagnostic.replayEvents = 1
	diagnostic.burstInterval = time.Second
	diagnostic.burstSize = 1
	require.NoError(t, validateCapacityWorkloadScale(diagnostic))
}

func waitCapacityReplayCatchup(t *testing.T, cfg capacityConfig, cursor, prefix string) {
	t.Helper()
	started := time.Now()
	deadline := started.Add(cfg.replayCatchupTimeout)
	seen := make(map[int]struct{}, cfg.replayEvents)
	var lastErr error
	for time.Now().Before(deadline) && len(seen) < cfg.replayEvents {
		attemptTimeout := min(30*time.Second, time.Until(deadline))
		conn, err := dialCapacityWatch(cfg.apiA, cfg.token, cursor, attemptTimeout)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_ = conn.SetReadDeadline(deadline)
		for len(seen) < cfg.replayEvents {
			event, readErr := readCapacityEvent(conn)
			if readErr != nil {
				lastErr = readErr
				break
			}
			cursor = capacityResumeCursor(cursor, event)
			if sequence, ok := capacitySequence(event.Name, prefix); ok {
				seen[sequence] = struct{}{}
			}
		}
		_ = conn.Close()
		if len(seen) < cfg.replayEvents {
			time.Sleep(250 * time.Millisecond)
		}
	}
	require.Lenf(t, seen, cfg.replayEvents,
		"durable journal did not catch up within %s before measured replay: last error: %v",
		cfg.replayCatchupTimeout, lastErr)
	t.Logf("durable journal caught up with %d replay transitions in %s", cfg.replayEvents, time.Since(started))
}

func capacityResumeCursor(current string, event capacityWireEvent) string {
	if event.ResourceVersion != "" {
		return event.ResourceVersion
	}
	return current
}

func TestCapacityResumeCursorAdvancesAcrossReplayReconnects(t *testing.T) {
	require.Equal(t, "next", capacityResumeCursor("original", capacityWireEvent{ResourceVersion: "next"}))
	require.Equal(t, "next", capacityResumeCursor("next", capacityWireEvent{}))
}

type capacitySubscriber struct {
	mu         sync.Mutex
	conn       *websocket.Conn
	endpoint   string
	token      string
	cursor     string
	received   *capacityBitset
	duplicates atomic.Int64
}

func openCapacitySubscribers(t *testing.T, cfg capacityConfig, maxTransitions int) []*capacitySubscriber {
	t.Helper()
	states := make([]*capacitySubscriber, cfg.subscribers)
	errs := make(chan error, cfg.subscribers)
	var wg sync.WaitGroup
	for i := range states {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			endpoint := cfg.apiA
			if index%2 == 1 {
				endpoint = cfg.apiB
			}
			conn, err := dialCapacityWatch(endpoint, cfg.token, "__namespace_watch_bootstrap__", 30*time.Second)
			if err != nil {
				errs <- fmt.Errorf("subscriber %d bootstrap: %w", index, err)
				return
			}
			event, err := readCapacityEvent(conn)
			if err != nil || event.Type != "BOOKMARK" {
				_ = conn.Close()
				errs <- fmt.Errorf("subscriber %d bootstrap event type=%q: %w", index, event.Type, err)
				return
			}
			states[index] = &capacitySubscriber{
				conn: conn, endpoint: endpoint, token: cfg.token, cursor: event.ResourceVersion,
				received: newCapacityBitset(maxTransitions + 2),
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		closeCapacitySubscribers(states)
		require.NoError(t, err)
	}
	for i, state := range states {
		require.NotNilf(t, state, "subscriber %d did not connect", i)
	}
	return states
}

func (s *capacitySubscriber) read(ctx context.Context, prefix string, sampleLatency bool, latencyMu *sync.Mutex, latenciesByEndpoint map[string][]time.Duration) error {
	for {
		conn := s.connection()
		if err := conn.SetReadDeadline(time.Now().Add(45 * time.Second)); err != nil {
			return err
		}
		event, err := readCapacityEvent(conn)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if strings.Contains(err.Error(), "GraphQL watch") || strings.Contains(err.Error(), "completed before") {
				return err
			}
			if reconnectErr := s.reconnect(ctx, conn); reconnectErr != nil {
				return fmt.Errorf("resume %s from cursor %q after %v: %w", s.endpoint, s.cursorValue(), err, reconnectErr)
			}
			continue
		}
		if event.ResourceVersion != "" {
			s.setCursor(event.ResourceVersion)
		}
		sequence, ok := capacitySequence(event.Name, prefix)
		if !ok {
			continue
		}
		if !s.received.mark(sequence) {
			s.duplicates.Add(1)
			continue
		}
		if sampleLatency && !event.CreatedAt.IsZero() {
			latency := time.Since(event.CreatedAt)
			if latency < 0 {
				latency = 0
			}
			latencyMu.Lock()
			latenciesByEndpoint[s.endpoint] = append(latenciesByEndpoint[s.endpoint], latency)
			latencyMu.Unlock()
		}
	}
}

func (s *capacitySubscriber) connection() *websocket.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn
}

func (s *capacitySubscriber) cursorValue() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *capacitySubscriber) setCursor(cursor string) {
	s.mu.Lock()
	s.cursor = cursor
	s.mu.Unlock()
}

func (s *capacitySubscriber) reconnect(ctx context.Context, failed *websocket.Conn) error {
	_ = failed.Close()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil
		}
		conn, err := dialCapacityWatch(s.endpoint, s.token, s.cursorValue(), 2*time.Second)
		if err == nil {
			if ctx.Err() != nil {
				_ = conn.Close()
				return nil
			}
			s.mu.Lock()
			s.conn = conn
			s.mu.Unlock()
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("replacement endpoint did not accept a resumed subscription within 30s: %w", lastErr)
}

func (s *capacitySubscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func closeCapacitySubscribers(subscribers []*capacitySubscriber) {
	for _, subscriber := range subscribers {
		if subscriber != nil {
			subscriber.close()
		}
	}
}

type capacityWireEvent struct {
	Type            string
	Name            string
	ResourceVersion string
	CreatedAt       time.Time
}

func dialCapacityWatch(apiURL, token, cursor string, timeout time.Duration) (*websocket.Conn, error) {
	return dialCapacitySubscription(apiURL, token, "namespace-capacity", capacitySubscription,
		map[string]any{"cursor": cursor}, timeout)
}

func dialCapacitySubscription(apiURL, token, subscriptionID, query string, variables map[string]any, timeout time.Duration) (*websocket.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		wsURL := strings.Replace(apiURL, "http", "ws", 1) + "/admin/graphql"
		dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Subprotocols: []string{"graphql-transport-ws"}}
		conn, response, err := dialer.Dial(wsURL, http.Header{"Authorization": []string{"Bearer " + token}})
		if err != nil {
			lastErr = capacityWebSocketHandshakeError(err, response)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err = conn.WriteJSON(map[string]any{
			"type": "connection_init",
			"payload": map[string]any{
				"Authorization": "Bearer " + token,
			},
		}); err == nil {
			var ack map[string]any
			err = conn.ReadJSON(&ack)
			if err == nil && ack["type"] != "connection_ack" {
				err = fmt.Errorf("connection init returned %v", ack["type"])
			}
		}
		if err == nil {
			err = conn.WriteJSON(map[string]any{
				"id": subscriptionID, "type": "subscribe",
				"payload": map[string]any{"query": query, "variables": variables},
			})
		}
		if err == nil {
			_ = conn.SetReadDeadline(time.Time{})
			return conn, nil
		}
		lastErr = err
		_ = conn.Close()
	}
	return nil, fmt.Errorf("watch endpoint unavailable for %s: %w", timeout, lastErr)
}

func capacityWebSocketHandshakeError(err error, response *http.Response) error {
	if response == nil {
		return err
	}
	defer response.Body.Close()
	contents, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("%w (HTTP %s: %s)", err, response.Status, strings.TrimSpace(string(contents)))
}

func readCapacityEvent(conn *websocket.Conn) (capacityWireEvent, error) {
	for {
		var message struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := conn.ReadJSON(&message); err != nil {
			return capacityWireEvent{}, err
		}
		switch message.Type {
		case "ping":
			if err := conn.WriteJSON(map[string]any{"type": "pong"}); err != nil {
				return capacityWireEvent{}, err
			}
			continue
		case "error":
			return capacityWireEvent{}, fmt.Errorf("GraphQL watch error: %s", message.Payload)
		case "complete":
			return capacityWireEvent{}, errors.New("GraphQL watch completed before the gate finished")
		case "next":
			var payload struct {
				Data struct {
					Watch struct {
						Type            string `json:"type"`
						Name            string `json:"name"`
						ResourceVersion string `json:"resourceVersion"`
						Namespace       *struct {
							Metadata struct {
								CreationTimestamp time.Time      `json:"creationTimestamp"`
								Annotations       map[string]any `json:"annotations"`
							} `json:"metadata"`
							Spec struct {
								Title *string `json:"title"`
							} `json:"spec"`
						} `json:"namespace"`
					} `json:"watchNamespaces"`
				} `json:"data"`
				Errors []json.RawMessage `json:"errors"`
			}
			if err := json.Unmarshal(message.Payload, &payload); err != nil {
				return capacityWireEvent{}, err
			}
			if len(payload.Errors) != 0 {
				return capacityWireEvent{}, fmt.Errorf("GraphQL watch next errors: %s", payload.Errors)
			}
			event := capacityWireEvent{
				Type: payload.Data.Watch.Type, Name: payload.Data.Watch.Name,
				ResourceVersion: payload.Data.Watch.ResourceVersion,
			}
			if payload.Data.Watch.Namespace != nil {
				namespace := payload.Data.Watch.Namespace
				if namespace.Spec.Title != nil && *namespace.Spec.Title != "" {
					event.Name = *namespace.Spec.Title
				}
				event.CreatedAt = namespace.Metadata.CreationTimestamp
				if raw, ok := namespace.Metadata.Annotations[capacitySentAtAnnotation].(string); ok {
					if sentAt, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
						event.CreatedAt = sentAt
					}
				}
			}
			return event, nil
		}
	}
}

func capacityBootstrapCursor(t *testing.T, apiURL, token string) string {
	t.Helper()
	cursor, err := getCapacityBootstrapCursor(apiURL, token)
	require.NoError(t, err)
	return cursor
}

func getCapacityBootstrapCursor(apiURL, token string) (string, error) {
	conn, err := dialCapacityWatch(apiURL, token, "__namespace_watch_bootstrap__", 30*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	event, err := readCapacityEvent(conn)
	if err != nil {
		return "", err
	}
	if event.Type != "BOOKMARK" || event.ResourceVersion == "" {
		return "", fmt.Errorf("bootstrap returned type=%q cursor=%q", event.Type, event.ResourceVersion)
	}
	return event.ResourceVersion, nil
}

func runCapacityReplays(t *testing.T, cfg capacityConfig, cursor, prefix string) []time.Duration {
	t.Helper()
	durations := make([]time.Duration, cfg.replaySamples)
	errs := make(chan error, cfg.replaySamples)
	var wg sync.WaitGroup
	for i := range durations {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			endpoint := cfg.apiA
			if index%2 == 1 {
				endpoint = cfg.apiB
			}
			started := time.Now()
			conn, err := dialCapacityWatch(endpoint, cfg.token, cursor, 10*time.Second)
			if err != nil {
				errs <- fmt.Errorf("replay sample %d via %s: dial: %w", index, endpoint, err)
				return
			}
			defer conn.Close()
			_ = conn.SetReadDeadline(started.Add(10 * time.Second))
			seen := make(map[int]struct{}, cfg.replayEvents)
			for len(seen) < cfg.replayEvents {
				event, readErr := readCapacityEvent(conn)
				if readErr != nil {
					errs <- fmt.Errorf("replay sample %d via %s: read: %w", index, endpoint, readErr)
					return
				}
				if sequence, ok := capacitySequence(event.Name, prefix); ok {
					seen[sequence] = struct{}{}
				}
			}
			durations[index] = time.Since(started)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	return durations
}

type capacityMutationJob struct {
	resourcePrefix string
	eventPrefix    string
	seq            int
}

type capacityWorkload struct {
	produced, enqueued, backpressured              int
	attempted, admitted, failed, sustained, bursts int
	drainTimedOut                                  bool
	acknowledged                                   *capacityBitset
	firstFailure                                   error
}

const capacityMutationDrainTimeout = 30 * time.Second

func runCapacityMutationWorkload(t *testing.T, client *http.Client, cfg capacityConfig, resourcePrefix, eventPrefix string, maxTransitions int) capacityWorkload {
	t.Helper()
	// Bound the queue by the amount of sustained work that may legally drain
	// after scheduling ends. This absorbs transient datastore/host jitter while
	// chronic under-capacity still saturates the queue or exceeds the drain bound.
	jobs := make(chan capacityMutationJob, capacityMutationQueueSize(cfg))
	acknowledged := newCapacityBitset(maxTransitions + 2)
	var sequence atomic.Int64
	var produced, enqueued, backpressured atomic.Int64
	var attempted, admitted, failed, sustained, bursts atomic.Int64
	var firstFailure error
	var firstFailureOnce sync.Once
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	var workers sync.WaitGroup
	for range cfg.mutationWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				if workerCtx.Err() != nil {
					return
				}
				job, ok := <-jobs
				if !ok {
					return
				}
				attempted.Add(1)
				primary, peer := cfg.apiA, cfg.apiB
				if job.seq%2 == 1 {
					primary, peer = peer, primary
				}
				name := capacityPoolName(job.resourcePrefix, job.seq, cfg.resourcePool)
				title := capacityName(job.eventPrefix, job.seq)
				retryCfg := cfg
				retryCfg.apiA, retryCfg.apiB = primary, peer
				retryWindow := cfg.mutationDrainTimeout
				if retryWindow <= 0 {
					retryWindow = capacityMutationDrainTimeout
				}
				if err := updateCapacityNamespaceRetryingContext(workerCtx, client, retryCfg, name, title, retryWindow); err != nil {
					firstFailureOnce.Do(func() { firstFailure = err })
					failed.Add(1)
					continue
				}
				acknowledged.mark(job.seq)
				admitted.Add(1)
			}
		}()
	}

	ctx, cancelSchedule := context.WithTimeout(context.Background(), cfg.duration)
	defer cancelSchedule()
	enqueue := func(job capacityMutationJob) {
		produced.Add(1)
		select {
		case jobs <- job:
			enqueued.Add(1)
		default:
			backpressured.Add(1)
		}
	}
	var schedulers sync.WaitGroup
	schedulers.Add(2)
	go func() {
		defer schedulers.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				seq := int(sequence.Add(1))
				sustained.Add(1)
				enqueue(capacityMutationJob{resourcePrefix: resourcePrefix, eventPrefix: eventPrefix, seq: seq})
			}
		}
	}()
	go func() {
		defer schedulers.Done()
		ticker := time.NewTicker(cfg.burstInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				burstTicker := time.NewTicker(10 * time.Millisecond)
				for range cfg.burstSize {
					select {
					case <-ctx.Done():
						burstTicker.Stop()
						return
					case <-burstTicker.C:
						seq := int(sequence.Add(1))
						bursts.Add(1)
						enqueue(capacityMutationJob{resourcePrefix: resourcePrefix, eventPrefix: eventPrefix, seq: seq})
					}
				}
				burstTicker.Stop()
			}
		}
	}()
	schedulers.Wait()
	close(jobs)
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()
	drainTimeout := cfg.mutationDrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = capacityMutationDrainTimeout
	}
	drainTimer := time.NewTimer(drainTimeout)
	drainTimedOut := false
	select {
	case <-workersDone:
		// Go 1.25 timer channels do not retain stale values after Stop. Avoid an
		// unconditional drain here: if worker completion wins the select while
		// expiry is concurrently becoming ready, receiving can block forever.
		drainTimer.Stop()
	case <-drainTimer.C:
		drainTimedOut = true
		cancelWorkers()
		<-workersDone
	}
	return capacityWorkload{
		produced: int(produced.Load()), enqueued: int(enqueued.Load()), backpressured: int(backpressured.Load()),
		attempted: int(attempted.Load()), admitted: int(admitted.Load()), failed: int(failed.Load()), drainTimedOut: drainTimedOut,
		sustained: int(sustained.Load()), bursts: int(bursts.Load()), acknowledged: acknowledged, firstFailure: firstFailure,
	}
}

func capacityMutationQueueSize(cfg capacityConfig) int {
	drainTimeout := cfg.mutationDrainTimeout
	if drainTimeout <= 0 {
		drainTimeout = capacityMutationDrainTimeout
	}
	const sustainedInterval = 100 * time.Millisecond
	return max(1, cfg.mutationWorkers*2, int(drainTimeout/sustainedInterval))
}

func TestCapacityMutationWorkloadAccountsForBackpressure(t *testing.T) {
	t.Run("keeps produced and admitted load equal when workers keep up", func(t *testing.T) {
		server := newCapacityMutationServer(t, 0)
		defer server.Close()
		cfg := capacityConfig{
			apiA: server.URL, apiB: server.URL, duration: 350 * time.Millisecond,
			burstInterval: 200 * time.Millisecond, burstSize: 3, mutationWorkers: 4, resourcePool: 16,
		}

		workload := runCapacityMutationWorkload(t, server.Client(), cfg, "pool-", "fast-", 64)

		require.False(t, workload.drainTimedOut)
		require.Zero(t, workload.backpressured)
		require.Equal(t, workload.produced, workload.enqueued)
		require.Equal(t, workload.enqueued, workload.attempted)
		require.Equal(t, workload.attempted, workload.admitted)
		require.Equal(t, workload.attempted, workload.acknowledged.count())
		require.Zero(t, workload.failed)
		require.GreaterOrEqual(t, workload.sustained, 2)
		require.GreaterOrEqual(t, workload.bursts, 3)
	})

	t.Run("reports queue saturation without stretching the scheduler", func(t *testing.T) {
		server := newCapacityMutationServer(t, 50*time.Millisecond)
		defer server.Close()
		cfg := capacityConfig{
			apiA: server.URL, apiB: server.URL, duration: 350 * time.Millisecond,
			burstInterval: 100 * time.Millisecond, burstSize: 50, mutationWorkers: 1,
			mutationDrainTimeout: 500 * time.Millisecond, resourcePool: 16,
		}
		started := time.Now()

		workload := runCapacityMutationWorkload(t, server.Client(), cfg, "pool-", "slow-", 512)

		require.Less(t, time.Since(started), 2*time.Second)
		require.False(t, workload.drainTimedOut)
		require.Positive(t, workload.backpressured)
		require.Less(t, workload.enqueued, workload.produced)
		require.Equal(t, workload.enqueued, workload.attempted)
		require.Equal(t, workload.attempted, workload.admitted)
	})

	t.Run("cancels in-flight requests when the bounded drain expires", func(t *testing.T) {
		releaseHandler := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-releaseHandler:
			}
		}))
		t.Cleanup(func() {
			close(releaseHandler)
			server.Close()
		})
		cfg := capacityConfig{
			apiA: server.URL, apiB: server.URL, duration: 150 * time.Millisecond,
			burstInterval: 100 * time.Millisecond, burstSize: 20, mutationWorkers: 1,
			mutationDrainTimeout: 50 * time.Millisecond, resourcePool: 16,
		}
		started := time.Now()

		workload := runCapacityMutationWorkload(t, server.Client(), cfg, "pool-", "blocked-", 128)

		require.Less(t, time.Since(started), time.Second)
		require.True(t, workload.drainTimedOut)
		require.Positive(t, workload.attempted)
	})
}

func TestCapacityMutationQueueSizeTracksBoundedDrain(t *testing.T) {
	require.Equal(t, 300, capacityMutationQueueSize(capacityConfig{mutationWorkers: 20}))
	require.Equal(t, 8, capacityMutationQueueSize(capacityConfig{
		mutationWorkers: 4, mutationDrainTimeout: 500 * time.Millisecond,
	}))
}

func newCapacityMutationServer(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		var request gqlRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode mutation request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		input, _ := request.Variables["input"].(map[string]any)
		metadata, _ := input["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		spec, _ := input["spec"].(map[string]any)
		title, _ := spec["title"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":{"updateNamespace":{"namespace":{"metadata":{"name":%q},"spec":{"title":%q}}}}}`, name, title)
	}))
}

func createCapacityRange(t *testing.T, client *http.Client, cfg capacityConfig, prefix string, count int) {
	t.Helper()
	jobs := make(chan int)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range cfg.mutationWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sequence := range jobs {
				if err := createCapacityNamespaceForReplay(client, cfg, capacityName(prefix, sequence), sequence); err != nil {
					errs <- fmt.Errorf("transition %d: %w", sequence, err)
				}
			}
		}()
	}
	for sequence := 1; sequence <= count; sequence++ {
		jobs <- sequence
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "all replay preparation transitions must be acknowledged")
	}
}

func updateCapacityRange(t *testing.T, client *http.Client, cfg capacityConfig, resourcePrefix, eventPrefix string, count int) {
	t.Helper()
	jobs := make(chan int)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for range cfg.mutationWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sequence := range jobs {
				name := capacityPoolName(resourcePrefix, sequence, cfg.resourcePool)
				title := capacityName(eventPrefix, sequence)
				if err := updateCapacityNamespaceForReplay(client, cfg, name, title, sequence); err != nil {
					errs <- fmt.Errorf("transition %d: %w", sequence, err)
				}
			}
		}()
	}
	for sequence := 1; sequence <= count; sequence++ {
		jobs <- sequence
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err, "all replay preparation transitions must be acknowledged")
	}
}

const (
	capacityReplayCreateTimeout    = 2 * time.Minute
	capacityReplayCreateMaxBackoff = 500 * time.Millisecond
)

func createCapacityNamespaceForReplay(client *http.Client, cfg capacityConfig, name string, sequence int) error {
	primary, peer := cfg.apiA, cfg.apiB
	if sequence%2 == 1 {
		primary, peer = peer, primary
	}
	retryCfg := cfg
	retryCfg.apiA, retryCfg.apiB = primary, peer
	return createCapacityNamespaceRetrying(client, retryCfg, name, capacityReplayCreateTimeout)
}

func updateCapacityNamespaceForReplay(client *http.Client, cfg capacityConfig, name, title string, sequence int) error {
	primary, peer := cfg.apiA, cfg.apiB
	if sequence%2 == 1 {
		primary, peer = peer, primary
	}
	retryCfg := cfg
	retryCfg.apiA, retryCfg.apiB = primary, peer
	return updateCapacityNamespaceRetryingContext(context.Background(), client, retryCfg, name, title, capacityReplayCreateTimeout)
}

func updateCapacityNamespaceRetryingContext(ctx context.Context, client *http.Client, cfg capacityConfig, name, title string, retryWindow time.Duration) error {
	if retryWindow <= 0 {
		return fmt.Errorf("update retry window expired for Namespace %q", name)
	}
	endpoints := [2]string{cfg.apiA, cfg.apiB}
	deadline := time.Now().Add(retryWindow)
	sentAt := time.Now().UTC()
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = updateCapacityNamespaceContext(ctx, client, endpoints[attempt%len(endpoints)], cfg.token, name, title, sentAt)
		if lastErr == nil {
			return nil
		}
		if !capacityCreateRetryable(lastErr) && !capacityCreateAmbiguous(lastErr) {
			return lastErr
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("update remained retryable for %s: %w", retryWindow, lastErr)
		}
		backoff := time.Duration(1<<min(attempt, 4)) * 25 * time.Millisecond
		backoff = min(backoff, capacityReplayCreateMaxBackoff)
		timer := time.NewTimer(min(backoff, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func createCapacityNamespaceRetrying(client *http.Client, cfg capacityConfig, name string, retryWindow time.Duration) error {
	return createCapacityNamespaceRetryingContext(context.Background(), client, cfg, name, retryWindow)
}

func createCapacityNamespaceRetryingContext(ctx context.Context, client *http.Client, cfg capacityConfig, name string, retryWindow time.Duration) error {
	if retryWindow <= 0 {
		return fmt.Errorf("create retry window expired for Namespace %q", name)
	}
	endpoints := [2]string{cfg.apiA, cfg.apiB}
	var lastErr error
	deadline := time.Now().Add(retryWindow)
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		endpoint := endpoints[attempt%len(endpoints)]
		lastErr = createCapacityNamespaceContext(ctx, client, endpoint, cfg.token, name)
		if lastErr == nil {
			return nil
		}
		if capacityCreateAlreadyExists(lastErr) {
			// Capacity names are unique to this workload. An already-exists
			// response after an ambiguous transport outcome proves that an
			// earlier attempt committed; unlike a read, it cannot observe the
			// staged pre-commit row and manufacture a ghost acknowledgement.
			return nil
		}
		if !capacityCreateRetryable(lastErr) && !capacityCreateAmbiguous(lastErr) {
			return lastErr
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("create remained retryable for %s: %w", retryWindow, lastErr)
		}
		backoff := time.Duration(1<<min(attempt, 4)) * 25 * time.Millisecond
		backoff = min(backoff, capacityReplayCreateMaxBackoff)
		timer := time.NewTimer(min(backoff, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type capacityGraphQLErrors struct{ raw string }

func (e *capacityGraphQLErrors) Error() string { return "GraphQL errors: " + e.raw }

type capacityHTTPStatusError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *capacityHTTPStatusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

func capacityRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 31); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, at.Sub(now))
	}
	return 0
}

func capacityCreateRetryable(err error) bool {
	var graphqlErr *capacityGraphQLErrors
	if !errors.As(err, &graphqlErr) {
		return false
	}
	return strings.Contains(graphqlErr.raw, `"code":"CONFLICT"`) ||
		strings.Contains(graphqlErr.raw, `"reason":"RESOURCE_VERSION_CONFLICT"`) ||
		(strings.Contains(graphqlErr.raw, "Cannot achieve consistency level") &&
			strings.Contains(graphqlErr.raw, "potentially executed: false"))
}

func capacityCreateAmbiguous(err error) bool {
	var statusErr *capacityHTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status == http.StatusRequestTimeout || statusErr.status == http.StatusTooManyRequests || statusErr.status >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func capacityCreateAlreadyExists(err error) bool {
	var graphqlErr *capacityGraphQLErrors
	return errors.As(err, &graphqlErr) && strings.Contains(graphqlErr.raw, `"reason":"NAMESPACE_ALREADY_EXISTS"`)
}

func confirmCapacityNamespaceExists(cfg capacityConfig, name string, timeout time.Duration) bool {
	return confirmCapacityNamespaceExistsContext(context.Background(), cfg, name, timeout)
}

func confirmCapacityNamespaceExistsContext(ctx context.Context, cfg capacityConfig, name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 250 * time.Millisecond}
	for time.Now().Before(deadline) {
		if capacityNamespaceExistsContext(ctx, client, cfg.apiA, cfg.token, name) || capacityNamespaceExistsContext(ctx, client, cfg.apiB, cfg.token, name) {
			return true
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	return false
}

func createCapacityNamespaceContext(ctx context.Context, client *http.Client, apiURL, token, name string) error {
	body, err := json.Marshal(gqlRequest{
		Query: `mutation($input: CreateNamespaceInput!) { createNamespace(input: $input) { namespace { metadata { name } } } }`,
		Variables: map[string]any{"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace",
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"title": name, "tier": "USER"},
		}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/admin/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		contents, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &capacityHTTPStatusError{status: response.StatusCode, body: string(contents)}
	}
	var result gqlResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Errors) != 0 {
		return &capacityGraphQLErrors{raw: namespaceContractErrors(result.Errors)}
	}
	var data struct {
		CreateNamespace *struct {
			Namespace struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			} `json:"namespace"`
		} `json:"createNamespace"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return err
	}
	if data.CreateNamespace == nil || data.CreateNamespace.Namespace.Metadata.Name != name {
		return fmt.Errorf("GraphQL mutation did not acknowledge Namespace %q", name)
	}
	return nil
}

func updateCapacityNamespaceContext(ctx context.Context, client *http.Client, apiURL, token, name, title string, sentAt time.Time) error {
	body, err := json.Marshal(gqlRequest{
		Query: `mutation($input: UpdateNamespaceInput!) { updateNamespace(input: $input) { namespace { metadata { name } spec { title } } } }`,
		Variables: map[string]any{"input": map[string]any{
			"apiVersion": "gitstore.dev/v1beta1", "kind": "Namespace",
			"metadata": map[string]any{
				"name":        name,
				"annotations": map[string]any{capacitySentAtAnnotation: sentAt.Format(time.RFC3339Nano)},
			},
			"spec": map[string]any{"title": title, "tier": "USER"},
		}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/admin/graphql", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		contents, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &capacityHTTPStatusError{status: response.StatusCode, body: string(contents)}
	}
	var result gqlResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return err
	}
	if len(result.Errors) != 0 {
		return &capacityGraphQLErrors{raw: namespaceContractErrors(result.Errors)}
	}
	var data struct {
		UpdateNamespace *struct {
			Namespace struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					Title *string `json:"title"`
				} `json:"spec"`
			} `json:"namespace"`
		} `json:"updateNamespace"`
	}
	if err := json.Unmarshal(result.Data, &data); err != nil {
		return err
	}
	if data.UpdateNamespace == nil || data.UpdateNamespace.Namespace.Metadata.Name != name ||
		data.UpdateNamespace.Namespace.Spec.Title == nil || *data.UpdateNamespace.Namespace.Spec.Title != title {
		return fmt.Errorf("GraphQL mutation did not acknowledge Namespace %q transition %q", name, title)
	}
	return nil
}

func capacityNamespaceExistsContext(ctx context.Context, client *http.Client, apiURL, token, name string) bool {
	body, err := json.Marshal(gqlRequest{
		Query:     `query($name: String!) { namespace(by: {name: $name}) { id } }`,
		Variables: map[string]any{"name": name},
	})
	if err != nil {
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+"/admin/graphql", bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return false
	}
	var result struct {
		Data struct {
			Namespace *struct {
				ID string `json:"id"`
			} `json:"namespace"`
		} `json:"data"`
	}
	return json.NewDecoder(response.Body).Decode(&result) == nil && result.Data.Namespace != nil && result.Data.Namespace.ID != ""
}

func TestCapacityReplayCreateRetriesOnlyConflicts(t *testing.T) {
	t.Run("conflict alternates to peer", func(t *testing.T) {
		var primaryCalls, peerCalls atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			if !strings.Contains(request.Query, "mutation") {
				_, _ = io.WriteString(w, `{"data":{"namespace":null}}`)
				return
			}
			primaryCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"errors":[{"message":"superseded","extensions":{"code":"CONFLICT","diagnostics":[{"reason":"SUPERSEDED","message":"superseded","level":"FAILURE"}]}}]}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			if !strings.Contains(request.Query, "mutation") {
				_, _ = io.WriteString(w, `{"data":{"namespace":null}}`)
				return
			}
			peerCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"createNamespace":{"namespace":{"metadata":{"name":"replay-name"}}}}}`)
		}))
		defer peer.Close()

		err := createCapacityNamespaceForReplay(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", 2)
		require.NoError(t, err)
		require.EqualValues(t, 1, primaryCalls.Load())
		require.EqualValues(t, 1, peerCalls.Load())
	})

	t.Run("explicit conflict is never confirmed by an existence query", func(t *testing.T) {
		var primaryMutations, primaryQueries, peerMutations atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			w.Header().Set("Content-Type", "application/json")
			if !strings.Contains(request.Query, "mutation") {
				primaryQueries.Add(1)
				_, _ = io.WriteString(w, `{"data":{"namespace":{"id":"staged-but-not-committed"}}}`)
				return
			}
			primaryMutations.Add(1)
			_, _ = io.WriteString(w, `{"errors":[{"message":"superseded","extensions":{"code":"CONFLICT","diagnostics":[{"reason":"SUPERSEDED","message":"superseded","level":"FAILURE"}]}}]}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(request.Query, "mutation") {
				peerMutations.Add(1)
				_, _ = io.WriteString(w, `{"data":{"createNamespace":{"namespace":{"metadata":{"name":"replay-name"}}}}}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"namespace":null}}`)
		}))
		defer peer.Close()

		err := createCapacityNamespaceRetrying(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", time.Second)
		require.NoError(t, err)
		require.EqualValues(t, 1, primaryMutations.Load())
		require.EqualValues(t, 1, peerMutations.Load())
		require.Zero(t, primaryQueries.Load(), "explicit conflicts must retry rather than probe potentially uncommitted state")
	})

	t.Run("non-executed serial quorum error is retryable", func(t *testing.T) {
		err := &capacityGraphQLErrors{raw: `{"message":"Cannot achieve consistency level for cl SERIAL. Requires 2, alive 1 (potentially executed: false)"}`}
		require.True(t, capacityCreateRetryable(err))
	})

	t.Run("permanent GraphQL error is not retried", func(t *testing.T) {
		var peerCalls atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"errors":[{"message":"invalid","extensions":{"code":"BAD_USER_INPUT"}}]}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			peerCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer peer.Close()

		err := createCapacityNamespaceForReplay(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", 2)
		require.Error(t, err)
		require.False(t, capacityCreateRetryable(err))
		require.Zero(t, peerCalls.Load())
	})

	t.Run("ambiguous response requires a mutation proof of commit", func(t *testing.T) {
		var primaryQueries, peerCalls atomic.Int64
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request gqlRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(request.Query, "mutation") {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `commit outcome unknown`)
				return
			}
			primaryQueries.Add(1)
			_, _ = io.WriteString(w, `{"data":{"namespace":{"id":"confirmed"}}}`)
		}))
		defer primary.Close()
		peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			peerCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"errors":[{"message":"already exists","extensions":{"code":"ALREADY_EXISTS","diagnostics":[{"reason":"NAMESPACE_ALREADY_EXISTS","message":"already exists","level":"FAILURE"}]}}]}`)
		}))
		defer peer.Close()

		err := createCapacityNamespaceForReplay(http.DefaultClient, capacityConfig{apiA: primary.URL, apiB: peer.URL}, "replay-name", 2)
		require.NoError(t, err)
		require.EqualValues(t, 1, peerCalls.Load())
		require.Zero(t, primaryQueries.Load(), "ambiguous outcomes must never be confirmed through staged-row visibility")
	})
}

type capacityRecoveryResult struct {
	duration               time.Duration
	label                  string
	beforeStop, afterStart capacityProcessMetrics
	beforeStopAt           time.Duration
	afterStartAt           time.Duration
	err                    error
}

func coordinateCapacityReplacement(client *http.Client, cfg capacityConfig, resourcePrefix, eventPrefix string, sequence int, soakStarted time.Time) capacityRecoveryResult {
	peerEndpoint := cfg.apiA
	label := "api_b"
	if cfg.replacement == cfg.apiA {
		peerEndpoint = cfg.apiB
		label = "api_a"
	}
	cursor, err := getCapacityBootstrapCursor(peerEndpoint, cfg.token)
	if err != nil {
		return capacityRecoveryResult{err: fmt.Errorf("capture peer cursor before replacement: %w", err)}
	}

	timer := time.NewTimer(cfg.replacementDelay)
	defer timer.Stop()
	<-timer.C
	beforeStop, err := fetchCapacityMetrics(client, cfg.replacement)
	if err != nil {
		return capacityRecoveryResult{err: fmt.Errorf("scrape replacement identity before trigger: %w", err)}
	}
	result := capacityRecoveryResult{label: label, beforeStop: beforeStop, beforeStopAt: time.Since(soakStarted)}
	trigger := fmt.Sprintf("replace %s start_time=%.0f requested_at=%s\n", cfg.replacement, beforeStop.processStart, time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(cfg.replacementTrigger, []byte(trigger), 0o600); err != nil {
		result.err = fmt.Errorf("write replacement trigger %s: %w", cfg.replacementTrigger, err)
		return result
	}

	probeClient := &http.Client{Timeout: 500 * time.Millisecond}
	outageDeadline := time.Now().Add(30 * time.Second)
	for endpointReady(probeClient, cfg.replacement) && time.Now().Before(outageDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if endpointReady(probeClient, cfg.replacement) {
		result.err = errors.New("replacement trigger did not produce an observed endpoint outage within 30s")
		return result
	}
	outageStarted := time.Now()

	recoveryDeadline := outageStarted.Add(30 * time.Second)
	for time.Now().Before(recoveryDeadline) {
		if endpointReady(probeClient, cfg.replacement) {
			afterStart, metricsErr := fetchCapacityMetrics(client, cfg.replacement)
			if metricsErr == nil && afterStart.processStart != beforeStop.processStart {
				result.afterStart = afterStart
				result.afterStartAt = time.Since(soakStarted)
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if result.afterStart.processStart == 0 {
		result.err = errors.New("replacement endpoint did not return with a changed process_start_time_seconds within 30s")
		return result
	}

	conn, err := dialCapacityWatch(cfg.replacement, cfg.token, cursor, 30*time.Second)
	if err != nil {
		result.err = fmt.Errorf("resume peer cursor through replacement: %w", err)
		return result
	}
	defer conn.Close()

	name := capacityPoolName(resourcePrefix, sequence, cfg.resourcePool)
	title := capacityName(eventPrefix, sequence)
	retryCfg := cfg
	retryCfg.apiA, retryCfg.apiB = peerEndpoint, cfg.replacement
	if err := updateCapacityNamespaceRetryingContext(context.Background(), client, retryCfg, name, title, time.Until(recoveryDeadline)); err != nil {
		result.err = fmt.Errorf("update post-replacement transition: %w", err)
		return result
	}
	_ = conn.SetReadDeadline(outageStarted.Add(30 * time.Second))
	for {
		event, readErr := readCapacityEvent(conn)
		if readErr != nil {
			result.err = fmt.Errorf("read post-replacement transition: %w", readErr)
			return result
		}
		if event.Name == title {
			result.duration = time.Since(outageStarted)
			return result
		}
	}
}

func endpointReady(client *http.Client, endpoint string, role ...string) bool {
	readinessPath := "/ready"
	if len(role) > 0 {
		readinessPath += "/" + role[0]
	}
	for _, path := range []string{"/health", readinessPath} {
		response, err := client.Get(endpoint + path)
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		if response.StatusCode/100 != 2 {
			return false
		}
	}
	return true
}

type capacityProcessMetrics struct {
	instanceID   string
	goroutines   float64
	available    bool
	cpu          float64
	resident     float64
	processStart float64
	gomaxprocs   float64
}

func scrapeCapacityMetrics(t *testing.T, client *http.Client, cfg capacityConfig) map[string]capacityProcessMetrics {
	t.Helper()
	result := make(map[string]capacityProcessMetrics, 2)
	for label, endpoint := range map[string]string{"api_a": cfg.apiA, "api_b": cfg.apiB} {
		metrics, err := fetchCapacityMetrics(client, endpoint)
		if err != nil {
			t.Logf("%s process metrics unavailable at %s/metrics: %v", label, endpoint, err)
			continue
		}
		result[label] = metrics
	}
	return result
}

// scrapeStabilizedCapacityMemory lets the Go runtime complete post-load GC and
// scavenging before retained-memory growth is evaluated. CPU and timing remain
// fixed at the end of measured load; only resident memory is replaced with the
// lowest valid sample from the unchanged process during the bounded window.
func scrapeStabilizedCapacityMemory(
	t *testing.T,
	client *http.Client,
	cfg capacityConfig,
	result map[string]capacityProcessMetrics,
	window time.Duration,
	interval time.Duration,
) map[string]capacityProcessMetrics {
	t.Helper()
	if window <= 0 || interval <= 0 {
		return result
	}
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(min(interval, time.Until(deadline)))
		result = mergeCapacityMemorySamples(result, scrapeCapacityMetrics(t, client, cfg))
	}
	return result
}

func scrapeStabilizedCapacityBaseline(
	t *testing.T,
	client *http.Client,
	cfg capacityConfig,
	result map[string]capacityProcessMetrics,
	window time.Duration,
	interval time.Duration,
) map[string]capacityProcessMetrics {
	t.Helper()
	if window <= 0 || interval <= 0 {
		return result
	}
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		time.Sleep(min(interval, time.Until(deadline)))
		result = mergeCapacityBaselineSamples(result, scrapeCapacityMetrics(t, client, cfg))
	}
	return result
}

func mergeCapacityBaselineSamples(
	retained map[string]capacityProcessMetrics,
	latest map[string]capacityProcessMetrics,
) map[string]capacityProcessMetrics {
	for label, sample := range latest {
		previous, ok := retained[label]
		if !ok || !previous.available || !sample.available || !sameCapacityProcess(previous, sample) {
			previous.available = false
			retained[label] = previous
			continue
		}
		if previous.resident < sample.resident {
			sample.resident = previous.resident
		}
		retained[label] = sample
	}
	return retained
}

func mergeCapacityMemorySamples(
	retained map[string]capacityProcessMetrics,
	latest map[string]capacityProcessMetrics,
) map[string]capacityProcessMetrics {
	for label, sample := range latest {
		previous, ok := retained[label]
		if !ok || !previous.available || !sample.available || !sameCapacityProcess(previous, sample) {
			previous.available = false
			retained[label] = previous
			continue
		}
		if sample.resident < previous.resident {
			previous.resident = sample.resident
		}
		retained[label] = previous
	}
	return retained
}

func sameCapacityProcess(left, right capacityProcessMetrics) bool {
	if left.instanceID != "" || right.instanceID != "" {
		return left.instanceID != "" && left.instanceID == right.instanceID
	}
	// process_start_time_seconds is derived from host boot time and process
	// start ticks. Docker Desktop's virtualized procfs has shown multi-second
	// collector offsets for an unchanged container even though repeated samples
	// are otherwise stable. Controlled replacement is verified independently,
	// and a real mid-gate replacement differs by minutes.
	return math.Abs(left.processStart-right.processStart) < 5*time.Second.Seconds()
}

func TestMergeCapacityMemorySamplesRetainsLowestRSSAndLoadEndCounters(t *testing.T) {
	retained := map[string]capacityProcessMetrics{
		"api_a": {available: true, cpu: 10, resident: 160, processStart: 1, gomaxprocs: 2},
	}
	latest := map[string]capacityProcessMetrics{
		"api_a": {available: true, cpu: 12, resident: 84, processStart: 1, gomaxprocs: 2},
	}

	merged := mergeCapacityMemorySamples(retained, latest)["api_a"]
	require.Equal(t, float64(84), merged.resident)
	require.Equal(t, float64(10), merged.cpu)

	latest["api_a"] = capacityProcessMetrics{
		available: true, cpu: 14, resident: 100, processStart: 1, gomaxprocs: 2,
	}
	merged = mergeCapacityMemorySamples(retained, latest)["api_a"]
	require.Equal(t, float64(84), merged.resident)
	require.Equal(t, float64(10), merged.cpu)
}

func TestMergeCapacityMemorySamplesRejectsProcessChange(t *testing.T) {
	retained := map[string]capacityProcessMetrics{
		"api_a": {available: true, cpu: 10, resident: 84, processStart: 1, gomaxprocs: 2},
	}
	latest := map[string]capacityProcessMetrics{
		"api_a": {available: true, cpu: 1, resident: 120, processStart: 7, gomaxprocs: 2},
	}

	merged := mergeCapacityMemorySamples(retained, latest)["api_a"]
	require.False(t, merged.available)
	require.Equal(t, float64(84), merged.resident)
	require.Equal(t, float64(10), merged.cpu)
	require.Equal(t, float64(1), merged.processStart)
}

func TestMergeCapacityBaselineSamplesRetainsRSSFloorAndLatestCounters(t *testing.T) {
	retained := map[string]capacityProcessMetrics{
		"api_a": {available: true, cpu: 10, resident: 84, processStart: 1, gomaxprocs: 2},
	}
	latest := map[string]capacityProcessMetrics{
		"api_a": {available: true, cpu: 12, resident: 100, processStart: 1.25, gomaxprocs: 2},
	}

	merged := mergeCapacityBaselineSamples(retained, latest)["api_a"]
	require.Equal(t, float64(84), merged.resident)
	require.Equal(t, float64(12), merged.cpu)
	require.Equal(t, float64(1.25), merged.processStart)
}

func TestSameCapacityProcessAllowsFractionalCollectorJitter(t *testing.T) {
	left := capacityProcessMetrics{processStart: 100}
	require.True(t, sameCapacityProcess(left, capacityProcessMetrics{processStart: 100.25}))
	require.True(t, sameCapacityProcess(left, capacityProcessMetrics{processStart: 104}))
	require.False(t, sameCapacityProcess(left, capacityProcessMetrics{processStart: 106}))
}

func TestCapacityMetricMergesUseProcessIdentity(t *testing.T) {
	for name, merge := range map[string]func(map[string]capacityProcessMetrics, map[string]capacityProcessMetrics) map[string]capacityProcessMetrics{
		"baseline":  mergeCapacityBaselineSamples,
		"post-load": mergeCapacityMemorySamples,
	} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				identity  string
				start     float64
				available bool
				wantValid bool
			}{
				{"same process with collector offset", "original", 110, true, true},
				{"replacement with same start sample", "replacement", 100, true, false},
				{"missing identity", "", 100, true, false},
				{"unavailable sample", "original", 100, false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					retained := map[string]capacityProcessMetrics{
						"api_a": {available: true, instanceID: "original", processStart: 100, cpu: 10, resident: 120, gomaxprocs: 2},
					}
					latest := map[string]capacityProcessMetrics{
						"api_a": {available: tc.available, instanceID: tc.identity, processStart: tc.start, cpu: 12, resident: 100, gomaxprocs: 2},
					}
					got := merge(retained, latest)["api_a"]
					require.Equal(t, tc.wantValid, got.available)
					if tc.wantValid {
						require.Equal(t, float64(100), got.resident)
						if name == "baseline" {
							require.Equal(t, float64(12), got.cpu)
						} else {
							require.Equal(t, float64(10), got.cpu)
						}
					}
				})
			}
		})
	}
}

func fetchCapacityMetrics(client *http.Client, endpoint string) (capacityProcessMetrics, error) {
	return fetchCapacityMetricsObserved(client, endpoint, nil)
}

func fetchCapacityMetricsObserved(client *http.Client, endpoint string, observe func([]byte) error) (capacityProcessMetrics, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint+"/metrics", nil)
	if err != nil {
		return capacityProcessMetrics{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return capacityProcessMetrics{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return capacityProcessMetrics{}, fmt.Errorf("metrics returned %s", response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
	if err != nil || len(body) > 8*1024*1024 {
		return capacityProcessMetrics{}, errors.New("metrics response exceeds bounds or could not be read")
	}
	if observe != nil {
		if err := observe(body); err != nil {
			return capacityProcessMetrics{}, err
		}
	}
	return parseCapacityMetrics(body)
}

func parseCapacityMetrics(body []byte) (capacityProcessMetrics, error) {
	values := map[string]float64{}
	instanceID := ""
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "process_cpu_seconds_total", "process_resident_memory_bytes", "process_start_time_seconds", "go_sched_gomaxprocs_threads", "go_goroutines":
			if len(fields) != 2 {
				return capacityProcessMetrics{}, errors.New("invalid process metric fields")
			}
			value, err := strconv.ParseFloat(fields[1], 64)
			if _, duplicate := values[fields[0]]; duplicate || err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return capacityProcessMetrics{}, errors.New("invalid or duplicate process metric")
			}
			values[fields[0]] = value
			if (fields[0] == "go_sched_gomaxprocs_threads" || fields[0] == "go_goroutines") && math.Trunc(value) != value {
				return capacityProcessMetrics{}, errors.New("fractional process count metric")
			}
		default:
			for _, prefix := range []string{`gitstore_api_process_instance_info{instance_id="`, `gitstore_controller_process_instance_info{instance_id="`} {
				if strings.HasPrefix(fields[0], prefix) {
					if len(fields) != 2 || instanceID != "" || fields[1] != "1" || !strings.HasSuffix(fields[0], `"}`) {
						return capacityProcessMetrics{}, errors.New("ambiguous process identity metric")
					}
					instanceID = strings.TrimSuffix(strings.TrimPrefix(fields[0], prefix), `"}`)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return capacityProcessMetrics{}, err
	}
	cpu, hasCPU := values["process_cpu_seconds_total"]
	resident, hasResident := values["process_resident_memory_bytes"]
	processStart, hasStart := values["process_start_time_seconds"]
	gomaxprocs, hasGOMAXPROCS := values["go_sched_gomaxprocs_threads"]
	if !hasCPU || !hasResident || !hasStart || !hasGOMAXPROCS || gomaxprocs <= 0 {
		return capacityProcessMetrics{}, errors.New("required process metrics are absent")
	}
	return capacityProcessMetrics{
		instanceID: instanceID, goroutines: values["go_goroutines"],
		available: true, cpu: cpu, resident: resident, processStart: processStart, gomaxprocs: gomaxprocs,
	}, nil
}

type capacityMemoryLimits struct {
	maxGrowthPercent float64
	maxResidentBytes float64
}

var strictCapacityMemoryLimits = capacityMemoryLimits{maxGrowthPercent: 10}

func assertCapacityMetrics(t *testing.T, start, end map[string]capacityProcessMetrics, elapsed time.Duration, recovery capacityRecoveryResult, allowMissing bool) {
	t.Helper()
	assertCapacityMetricsWithLimits(t, start, end, elapsed, recovery, allowMissing, strictCapacityMemoryLimits)
}

func assertCapacityMetricsWithLimits(t *testing.T, start, end map[string]capacityProcessMetrics, elapsed time.Duration, recovery capacityRecoveryResult, allowMissing bool, memoryLimits capacityMemoryLimits) {
	t.Helper()
	for _, label := range []string{"api_a", "api_b"} {
		before, beforeOK := start[label]
		after, afterOK := end[label]
		if !beforeOK || !afterOK || !before.available || !after.available {
			if allowMissing {
				t.Logf("%s CPU/resident thresholds explicitly skipped because /metrics was unavailable", label)
				continue
			}
			require.Failf(t, "required process metrics unavailable", "%s must expose process CPU, resident memory, and start time", label)
			continue
		}
		// process_cpu_seconds_total aggregates work across executing threads.
		// Dividing each segment by its reported GOMAXPROCS converts it to
		// utilized normalized CPU before applying the wall-time threshold.
		normalizedCPUSeconds := (after.cpu - before.cpu) / before.gomaxprocs
		measuredSeconds := elapsed.Seconds()
		memoryGrowth := capacityMemoryGrowth(before.resident, after.resident)
		if label == recovery.label {
			normalizedCPUSeconds = (recovery.beforeStop.cpu-before.cpu)/before.gomaxprocs +
				(after.cpu-recovery.afterStart.cpu)/recovery.afterStart.gomaxprocs
			measuredSeconds = recovery.beforeStopAt.Seconds() + (elapsed - recovery.afterStartAt).Seconds()
			// The first ready scrape is an identity marker, not a valid memory
			// baseline: a fresh Go process has not populated its steady-state
			// heap yet. Both replacement instances run the same release binary
			// and configuration, so compare each process segment with the warmed,
			// stabilized pre-load baseline from the original instance.
			memoryGrowth = capacityReplacementMemoryGrowth(
				before.resident, recovery.beforeStop.resident, after.resident,
			)
		}
		cpuPercent := 100 * normalizedCPUSeconds / measuredSeconds
		t.Logf("%s normalized_cpu=%.2f%% gomaxprocs=%.0f resident_start=%.0f resident_end=%.0f resident_growth=%.2f%%", label, cpuPercent, before.gomaxprocs, before.resident, after.resident, memoryGrowth)
		assert.Lessf(t, cpuPercent, 80.0, "%s process CPU must remain below 80%%", label)
		assert.Truef(t, capacityMemoryWithinLimits(memoryGrowth, after.resident, memoryLimits),
			"%s resident memory must remain below %.0f%% growth%s; got growth %.2f%% and end RSS %.0f bytes",
			label, memoryLimits.maxGrowthPercent, capacityResidentLimitDescription(memoryLimits), memoryGrowth, after.resident)
	}
}

func capacityMemoryWithinLimits(growthPercent, residentBytes float64, limits capacityMemoryLimits) bool {
	return growthPercent < limits.maxGrowthPercent &&
		(limits.maxResidentBytes <= 0 || residentBytes < limits.maxResidentBytes)
}

func capacityResidentLimitDescription(limits capacityMemoryLimits) string {
	if limits.maxResidentBytes <= 0 {
		return ""
	}
	return fmt.Sprintf(" and below %.0f MiB final RSS", limits.maxResidentBytes/(1024*1024))
}

func capacityMemoryGrowth(start, end float64) float64 {
	if start <= 0 {
		return 0
	}
	return 100 * (end - start) / start
}

func capacityReplacementMemoryGrowth(baseline, beforeStop, replacementEnd float64) float64 {
	return max(
		capacityMemoryGrowth(baseline, beforeStop),
		capacityMemoryGrowth(baseline, replacementEnd),
	)
}

func TestCapacityReplacementMemoryGrowthUsesWarmedBaseline(t *testing.T) {
	// The replacement's immediate-ready RSS may be arbitrarily low and is not
	// part of this calculation. Both process segments are compared with the
	// same stabilized, pre-load release-process baseline.
	require.InDelta(t, 8.0, capacityReplacementMemoryGrowth(100, 108, 105), .001)
	require.InDelta(t, 12.0, capacityReplacementMemoryGrowth(100, 104, 112), .001)
}

type capacityBitset struct {
	words []atomic.Uint64
}

func newCapacityBitset(max int) *capacityBitset {
	return &capacityBitset{words: make([]atomic.Uint64, (max+63)/64)}
}

func (b *capacityBitset) mark(sequence int) bool {
	if sequence < 1 {
		return false
	}
	word := (sequence - 1) / 64
	if word >= len(b.words) {
		return false
	}
	mask := uint64(1) << uint((sequence-1)%64)
	for {
		old := b.words[word].Load()
		if old&mask != 0 {
			return false
		}
		if b.words[word].CompareAndSwap(old, old|mask) {
			return true
		}
	}
}

func (b *capacityBitset) count() int {
	total := 0
	for i := range b.words {
		total += bits.OnesCount64(b.words[i].Load())
	}
	return total
}

func capacityMissing(subscribers []*capacitySubscriber, acknowledged *capacityBitset) int {
	missing := 0
	for _, subscriber := range subscribers {
		for i := range acknowledged.words {
			missing += bits.OnesCount64(acknowledged.words[i].Load() &^ subscriber.received.words[i].Load())
		}
	}
	return missing
}

func capacityDuplicateDeliveries(subscribers []*capacitySubscriber) int64 {
	var duplicates int64
	for _, subscriber := range subscribers {
		duplicates += subscriber.duplicates.Load()
	}
	return duplicates
}

func capacityName(prefix string, sequence int) string {
	return fmt.Sprintf("%s%08d", prefix, sequence)
}

func capacityPoolName(prefix string, sequence, poolSize int) string {
	return capacityName(prefix, (sequence-1)%poolSize+1)
}

func capacitySequence(name, prefix string) (int, bool) {
	if !strings.HasPrefix(name, prefix) {
		return 0, false
	}
	sequence, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	return sequence, err == nil && sequence > 0
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[int(float64(len(ordered)-1)*percentile)]
}

func capacityEnvDuration(t *testing.T, key string, fallback time.Duration) time.Duration {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		value, err := time.ParseDuration(raw)
		require.NoError(t, err)
		return value
	}
	return fallback
}

func capacityEnvInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	if raw := os.Getenv(key); raw != "" {
		value, err := strconv.Atoi(raw)
		require.NoError(t, err)
		return value
	}
	return fallback
}
