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
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
	MutationSubject                       string `json:",omitempty"`
	ConditionalRoot                       bool   `json:",omitempty"`
	GeneralRoleMutations                  bool   `json:",omitempty"`
	MaximumRootBatch                      bool   `json:",omitempty"`
	Subjects                              []string
	IsolationSubjects                     []string `json:",omitempty"`
}

type rbacCapacitySeedNamespace struct {
	Name, Tier string
	Resources  []map[string]any
}

func (p rbacCapacitySeedPlan) validate() error {
	if len(p.Owner) > 63 || !rbacCapacitySeedName.MatchString(p.Owner) ||
		len(p.Prefix) > 40 || !strings.HasPrefix(p.Prefix, "rbac-") || !rbacCapacitySeedName.MatchString(p.Prefix) ||
		len(p.PersonalSubject) > 63 || p.PersonalSubject == "gitstore-system" || !rbacCapacitySeedName.MatchString(p.PersonalSubject) ||
		len(p.Subjects) < 4 || len(p.Subjects) > 128 || len(p.IsolationSubjects) > 128 {
		return errors.New("RBAC seed requires an explicit owner, isolated names and bounded distinct subjects")
	}
	if p.MutationSubject != "" && p.MutationSubject != p.Prefix+"-filler-mutation" {
		return errors.New("conditional Role seeding requires the exact separately enrolled mutation holdout")
	}
	if p.GeneralRoleMutations && p.MutationSubject == "" {
		return errors.New("general Role mutations require the separately enrolled Role holdout")
	}
	seen := map[string]bool{p.PersonalSubject: true}
	for _, value := range append(p.principalSubjects(), p.Group) {
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

func (p rbacCapacitySeedPlan) principalSubjects() []string {
	return append(slices.Clone(p.Subjects), p.IsolationSubjects...)
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
		if p.MutationSubject != "" && index >= 1 && index <= 4 && role == 4 {
			rule["when"] = []any{map[string]any{"attr": "namespace", "op": "eq", "value": p.Prefix + "-filler-condition"}}
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
		case 4:
			if p.MutationSubject != "" && index >= 1 && index <= 4 {
				subject["name"] = p.MutationSubject
			}
		}
		result.Resources = append(result.Resources, manifest("RoleBinding", fmt.Sprintf("capacity-binding-%02d", binding),
			map[string]any{"roleRef": role, "subjects": []any{subject}}))
	}
	return result
}

func (p rbacCapacitySeedPlan) mutationTarget() (string, string, int) {
	if p.MutationSubject != "" {
		return "Role", "capacity-role-04", 4
	}
	return "RoleBinding", "capacity-binding-04", 14
}

func (p rbacCapacitySeedPlan) mutationGenerations(generation int64) map[string]int64 {
	kind, name, _ := p.mutationTarget()
	generations := map[string]int64{"Role/capacity-role-04": 1, "RoleBinding/capacity-binding-04": 1}
	generations[kind+"/"+name] = generation
	return generations
}

func (p rbacCapacitySeedPlan) mutationNamespace(namespace rbacCapacitySeedNamespace, subject string, allowed bool) rbacCapacitySeedNamespace {
	if !allowed {
		return namespace
	}
	_, _, target := p.mutationTarget()
	namespace.Resources = slices.Clone(namespace.Resources)
	resource := maps.Clone(namespace.Resources[target])
	spec := maps.Clone(resource["spec"].(map[string]any))
	if p.MutationSubject != "" {
		rules := slices.Clone(spec["rules"].([]any))
		rule := maps.Clone(rules[0].(map[string]any))
		if p.GeneralRoleMutations {
			rule["resources"], rule["verbs"] = []string{"product"}, []string{"list"}
			delete(rule, "when")
		} else {
			rule["when"] = []any{map[string]any{"attr": "namespace", "op": "eq", "value": namespace.Name}}
		}
		rules[0], spec["rules"] = rule, rules
	} else {
		spec["subjects"] = []any{map[string]any{"kind": "User", "name": subject}}
	}
	resource["spec"], namespace.Resources[target] = spec, resource
	return namespace
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

type rbacCapacityK6Case struct {
	Subject   string `json:"subject"`
	Namespace string `json:"namespace"`
	Action    string `json:"action"`
	Expected  string `json:"expected"`
	Token     string `json:"token"`
}

type rbacCapacityK6WorkingSet struct {
	Owner string               `json:"owner"`
	Cases []rbacCapacityK6Case `json:"cases"`
}

type rbacCapacityK6DiagnosticConfig struct {
	Duration                      time.Duration
	Rate, PreallocatedVUs, MaxVUs int
	GroupMembers                  []string
	Mutation                      *rbacCapacityMutationConfig
	RootUnderLoad                 bool
	alpha                         bool
}

type rbacCapacityMutationConfig struct {
	Subject, TokenFile string
	Transport          string `json:",omitempty"`
	Schedule           rbacCapacityScheduleConfig
}

func (c rbacCapacityMutationConfig) transport(offer int64) string {
	if c.Transport == "" {
		return ""
	}
	if (offer+offer/4)%2 == 0 {
		return "git"
	}
	return "graphql"
}

func validateRBACCapacityK6Diagnostic(config *rbacCapacityK6DiagnosticConfig) error {
	if config == nil {
		return errors.New("ordinary RBAC diagnostic workload must be explicitly declared")
	}
	if config.RootUnderLoad && (config.Mutation == nil || config.Duration < 60*time.Second ||
		config.Mutation.Schedule.Duration <= 0 || config.Mutation.Schedule.Duration > config.Duration-60*time.Second) {
		return errors.New("root-under-load requires a disjoint mutation companion, 48-second preamble and 12-second tail")
	}
	maxDuration, maxRate := 30*time.Second, 100
	if config.Mutation != nil {
		maxDuration, maxRate = 31*time.Minute, 500
	}
	if config.Duration < time.Second || config.Duration > maxDuration || config.Duration%time.Second != 0 ||
		config.Rate <= 0 || config.Rate > maxRate ||
		(int64(config.Duration/time.Second)*int64(config.Rate))%10 != 0 ||
		config.PreallocatedVUs <= 0 || config.PreallocatedVUs > config.MaxVUs || config.MaxVUs > 200 {
		return errors.New("ordinary RBAC diagnostics require a bounded load window, VUs and complete per-API 60/30/10 cycles")
	}
	return nil
}

func rbacCapacityK6DecisionByteBound(config *rbacCapacityK6DiagnosticConfig) (int64, error) {
	if err := validateRBACCapacityK6Diagnostic(config); err != nil {
		return 0, err
	}
	ordinary := int64(config.Duration/time.Second) * int64(config.Rate)
	var companion int64
	if config.Mutation != nil {
		companion = 16384
	}
	if config.RootUnderLoad {
		companion += 8*51 + 1
	}
	// Budget 4 KiB per declared decision plus 128 MiB of auxiliary receipts.
	bound := (ordinary+companion)*4096 + (128 << 20)
	if bound > 8<<30 {
		return 0, errors.New("declared RBAC decision evidence exceeds the existing 8 GiB k6 ceiling")
	}
	return bound, nil
}

type rbacCapacityNativeSeedConfig struct {
	Plan                                      rbacCapacitySeedPlan
	Count, Workers, ImportBatchSize           int
	Mode, PersonalTokenFile, ReceiptDirectory string
	VerifyRetained                            bool
	ResumeSeedDirectory                       string
	ResumeBaselines                           map[string]rbacCapacitySeedBaseline
	PrincipalTokenFiles                       map[string]string
	SourceModelFiles                          []string
	DecisionLogFiles                          []string
	InitialAdminSubject                       string
	K6Workload                                *rbacCapacityK6DiagnosticConfig
	DecisionWorkload                          *rbacCapacityDecisionWorkloadConfig
}

type rbacCapacitySeedBaseline struct {
	SeedGitRevision, GitRevision string
	Generation                   int64
	AcknowledgedAt               time.Time
}

func validateRBACCapacityResumeBaselines(config rbacCapacityNativeSeedConfig) error {
	if len(config.ResumeBaselines) == 0 {
		return nil
	}
	if config.Mode == "alpha" {
		if err := validateRBACCapacityAlpha(config); err != nil {
			return err
		}
	}
	if config.Mode != "diagnostic" && config.Mode != "alpha" || !config.VerifyRetained && config.ResumeSeedDirectory == "" ||
		config.VerifyRetained && config.K6Workload != nil ||
		config.Count < 2 || config.Count > 1000 || len(config.ResumeBaselines) > 4 ||
		config.Plan.ConditionalRoot ||
		config.Plan.MutationSubject != "" && config.Plan.MutationSubject != config.Plan.Prefix+"-filler-mutation" {
		return errors.New("advanced baselines require explicit retained diagnostics and the original disjoint namespaced mutation intent")
	}
	var advances int64
	if config.K6Workload != nil && config.K6Workload.Mutation != nil {
		if err := validateRBACCapacityMutation(config.Plan, config.Count, config.K6Workload, config.InitialAdminSubject); err != nil {
			return err
		}
		schedule := config.K6Workload.Mutation.Schedule
		advances = (rbacCapacityScheduledOffers(schedule.Duration, schedule.Rate, schedule.Period)+3)/4 + 1
	}
	for name, baseline := range config.ResumeBaselines {
		owned := false
		for index := 1; index <= 4 && index < config.Count; index++ {
			owned = owned || name == config.Plan.namespaceName(index)
		}
		if !owned || baseline.Generation < 2 || baseline.Generation > math.MaxInt64-advances ||
			baseline.AcknowledgedAt.IsZero() || baseline.AcknowledgedAt.After(time.Now()) ||
			baseline.SeedGitRevision == baseline.GitRevision ||
			!regexp.MustCompile(`^main@sha1:[0-9a-f]{40}$`).MatchString(baseline.SeedGitRevision) ||
			!regexp.MustCompile(`^main@sha1:[0-9a-f]{40}$`).MatchString(baseline.GitRevision) {
			return errors.New("advanced baseline requires an owned mutation tier, distinct canonical refs, bounded generation and original acknowledgement clock")
		}
	}
	return nil
}

func applyRBACCapacityResumeBaselines(config rbacCapacityNativeSeedConfig, receipts map[string]rbacCapacitySeedReceipt) error {
	if err := validateRBACCapacityResumeBaselines(config); err != nil {
		return err
	}
	updates := make(map[string]rbacCapacitySeedReceipt, len(config.ResumeBaselines))
	for name, baseline := range config.ResumeBaselines {
		seed := receipts[name]
		if !seed.Complete || seed.Owner != config.Plan.Owner || seed.Namespace != name ||
			len(seed.Admissions) == 0 || seed.GitRevision != baseline.SeedGitRevision ||
			seed.Admissions[len(seed.Admissions)-1].GitRevision != baseline.SeedGitRevision ||
			!baseline.AcknowledgedAt.After(seed.Admissions[len(seed.Admissions)-1].AcknowledgedAt) {
			return errors.New("advanced baseline does not extend its independently complete original seed")
		}
		seed.GitRevision, seed.baseline = baseline.GitRevision, &baseline
		seed.baselineGenerations = config.Plan.mutationGenerations(baseline.Generation)
		updates[name] = seed
	}
	for name, seed := range updates {
		receipts[name] = seed
	}
	return nil
}

type rbacCapacityDecisionWorkloadConfig struct {
	Schedule              rbacCapacityScheduleConfig
	GroupMembers          []string
	PropagationDiagnostic bool
}

func validateRBACCapacityRetainedVerification(config rbacCapacityNativeSeedConfig) error {
	if !config.VerifyRetained {
		return nil
	}
	if config.Mode != "diagnostic" || config.Count < 2 || config.Count > 1000 || config.ImportBatchSize != 60 ||
		config.Plan.ConditionalRoot ||
		(config.Plan.MutationSubject != "" && config.Plan.MutationSubject != config.Plan.Prefix+"-filler-mutation") ||
		config.K6Workload != nil ||
		config.DecisionWorkload == nil || config.DecisionWorkload.PropagationDiagnostic {
		return errors.New("retained verification requires complete native imports and an explicitly read-only diagnostic workload")
	}
	if len(config.SourceModelFiles) != 2 || len(config.DecisionLogFiles) != 2 ||
		config.InitialAdminSubject == "" || len(config.PrincipalTokenFiles) != len(config.Plan.principalSubjects()) {
		return errors.New("retained verification requires independent source and decision evidence on both APIs and the authored principal/root intent")
	}
	workload := config.DecisionWorkload
	if err := workload.Schedule.validate(); err != nil {
		return err
	}
	if rbacCapacityScheduledOffers(workload.Schedule.Duration, workload.Schedule.Rate, workload.Schedule.period()) != int64(config.Count)*20 {
		return errors.New("retained verification requires one complete ordinary 60/30/10 rotation over every Namespace on each API")
	}
	return nil
}

func validateRBACCapacitySeedResume(config rbacCapacityNativeSeedConfig) error {
	if err := validateRBACCapacityResumeBaselines(config); err != nil {
		return err
	}
	if config.ResumeSeedDirectory == "" {
		return nil
	}
	input, output := config.ResumeSeedDirectory, config.ReceiptDirectory
	if !filepath.IsAbs(input) || filepath.Clean(input) != input ||
		!filepath.IsAbs(output) || filepath.Clean(output) != output ||
		input == output || strings.HasPrefix(input, output+string(filepath.Separator)) ||
		strings.HasPrefix(output, input+string(filepath.Separator)) ||
		config.VerifyRetained || config.Mode != "diagnostic" && config.Mode != "alpha" || config.ImportBatchSize != 60 ||
		config.DecisionWorkload != nil || config.K6Workload == nil || config.K6Workload.Mutation == nil ||
		len(config.SourceModelFiles) != 2 || len(config.DecisionLogFiles) != 2 ||
		len(config.PrincipalTokenFiles) != len(config.Plan.principalSubjects()) || config.InitialAdminSubject == "" {
		return errors.New("seed continuation requires distinct private input/output directories and an explicit mutation workload with independent replica evidence")
	}
	if err := validateRBACCapacityK6Diagnostic(config.K6Workload); err != nil {
		return err
	}
	return validateRBACCapacityMutationSelection(config)
}

func copyRBACCapacityRetainedSeed(config rbacCapacityNativeSeedConfig) error {
	if config.ResumeSeedDirectory == "" {
		return nil
	}
	if err := validateRBACCapacitySeedResume(config); err != nil {
		return err
	}
	input, err := os.Lstat(config.ResumeSeedDirectory)
	if err != nil {
		return err
	}
	output, err := os.Lstat(config.ReceiptDirectory)
	if err != nil {
		return err
	}
	if !input.IsDir() || !output.IsDir() || input.Mode().Perm()&0077 != 0 ||
		output.Mode().Perm()&0077 != 0 || os.SameFile(input, output) {
		return errors.New("retained seed inputs and new run evidence must be distinct private directories, not symlinks")
	}
	digest := sha256.New()
	for index := range config.Count {
		name := config.Plan.namespaceName(index) + ".json"
		content, err := readRBACCapacityPrivateFixture(filepath.Join(config.ResumeSeedDirectory, name))
		if err != nil {
			return err
		}
		file, err := os.OpenFile(filepath.Join(config.ReceiptDirectory, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(content)
		syncErr, closeErr := file.Sync(), file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		_, _ = digest.Write([]byte(name + "\x00"))
		_, _ = digest.Write(content)
	}
	file, err := os.OpenFile(filepath.Join(config.ReceiptDirectory, ".retained-seed.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	encodeErr := json.NewEncoder(file).Encode(map[string]any{
		"inputDirectory": config.ResumeSeedDirectory, "receiptCount": config.Count,
		"receiptsSHA256": hex.EncodeToString(digest.Sum(nil)), "copiedAt": time.Now().UTC(),
		"independentLiveBaselineVerificationRequired": true,
		"declaredAdvancedBaselines":                   config.ResumeBaselines,
	})
	return errors.Join(encodeErr, file.Sync(), file.Close())
}

type rbacCapacityK6ProcessWindow struct {
	Owner                  string
	ProfileDigest          string
	StartedAt, CompletedAt time.Time
	Root                   rbacCapacityRootPolicy
}

func makeRBACCapacityK6WorkingSet(plan rbacCapacitySeedPlan, count int, groupMembers []string,
	tokens map[string]string,
) (rbacCapacityK6WorkingSet, error) {
	if err := plan.validate(); err != nil {
		return rbacCapacityK6WorkingSet{}, err
	}
	if count < 2 || count > 1000 {
		return rbacCapacityK6WorkingSet{}, errors.New("RBAC k6 working set requires bounded independently owned Namespaces")
	}
	fixture := rbacCapacityK6WorkingSet{Owner: plan.Owner, Cases: make([]rbacCapacityK6Case, 0, count*10)}
	for index := range count * 10 {
		request, err := plan.request(int64(index)*2, count, groupMembers)
		if err != nil {
			return rbacCapacityK6WorkingSet{}, err
		}
		token := tokens[request.Subject]
		if token == "" || len(token) > 16384 {
			return rbacCapacityK6WorkingSet{}, errors.New("RBAC k6 working set lacks an independently enrolled ordinary principal")
		}
		fixture.Cases = append(fixture.Cases, rbacCapacityK6Case{
			Subject: request.Subject, Namespace: request.Namespace, Action: request.Action,
			Expected: request.Expected, Token: token,
		})
	}
	return fixture, nil
}

func (p rbacCapacitySeedPlan) request(offer int64, count int, groupMembers []string) (rbacCapacityRequest, error) {
	if offer < 0 || count < 2 || count > 1000 || len(p.Subjects) < 4 || len(p.Subjects) > 128 || len(p.IsolationSubjects) > 128 ||
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
		ID: fmt.Sprintf("%s-native-ordinary-%012d", p.Owner, offer), Replica: int(offer % 2),
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
		if len(p.IsolationSubjects) != 0 {
			request.Subject = p.IsolationSubjects[int(offer/20)%len(p.IsolationSubjects)]
		}
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
	return inspectRBACCapacityRequest(ctx, client, api, token, request).Outcome
}

type rbacCapacityRequestObservation struct {
	Outcome    string
	HTTPStatus int
	Code       string
}

func inspectRBACCapacityRequest(ctx context.Context, client *http.Client, api, token string, request rbacCapacityRequest) rbacCapacityRequestObservation {
	result := rbacCapacityRequestObservation{Outcome: "wrong", Code: "INVALID_REQUEST"}
	field := "products"
	if request.Action == "role.list" {
		field = "roles"
	} else if request.Action != "product.list" {
		return result
	}
	content, err := json.Marshal(map[string]any{
		"query":     `query($namespace:String!){` + field + `(namespace:$namespace,first:1){edges{node{id}}}}`,
		"variables": map[string]string{"namespace": request.Namespace},
	})
	if err != nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/admin/graphql", bytes.NewReader(content))
	if err != nil || token == "" || request.ID == "" {
		return result
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+token)
	httpRequest.Header.Set("X-Request-ID", request.ID)
	response, err := client.Do(httpRequest)
	if err != nil {
		result.Outcome, result.Code = "unavailable", "TRANSPORT_ERROR"
		return result
	}
	defer response.Body.Close()
	result.HTTPStatus = response.StatusCode
	if response.StatusCode != http.StatusOK {
		result.Outcome, result.Code = "unavailable", "HTTP_STATUS"
		return result
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil {
		result.Outcome, result.Code = "unavailable", "RESPONSE_READ_ERROR"
		return result
	}
	result.Code = "INVALID_RESPONSE"
	if len(body) > 65536 {
		return result
	}
	var data struct {
		Data   map[string]json.RawMessage
		Errors []struct{ Extensions struct{ Code string } }
	}
	if json.Unmarshal(body, &data) != nil {
		return result
	}
	if len(data.Errors) != 0 {
		code := data.Errors[0].Extensions.Code
		for _, failure := range data.Errors {
			if failure.Extensions.Code != code {
				result.Code = "MULTIPLE"
				return result
			}
		}
		for _, value := range data.Data {
			if string(value) != "null" {
				return result
			}
		}
		if code == "AUTHORIZATION_UNAVAILABLE" || code == "DIRECTORY_UNAVAILABLE" || code == "UNAUTHENTICATED" {
			result.Outcome, result.Code = "unavailable", code
			return result
		}
		if code == "FORBIDDEN" {
			result.Outcome, result.Code = "deny", code
			return result
		}
		result.Code = "UNCLASSIFIED"
		return result
	}
	var connection struct {
		Edges []struct{ Node *struct{ ID string } }
	}
	if json.Unmarshal(data.Data[field], &connection) != nil ||
		connection.Edges == nil || len(connection.Edges) > 1 {
		return result
	}
	for _, edge := range connection.Edges {
		if edge.Node == nil || edge.Node.ID == "" {
			return result
		}
	}
	result.Outcome, result.Code = "allow", ""
	return result
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
	ImportBatchSize                                       int
	LastCheck                                             string `json:",omitempty"`
	Complete                                              bool
	baseline                                              *rbacCapacitySeedBaseline
	baselineGenerations                                   map[string]int64
}

func (r rbacCapacitySeedReceipt) baselineAdmission() rbacCapacitySeedAdmission {
	if r.baseline != nil {
		return rbacCapacitySeedAdmission{GitRevision: r.baseline.GitRevision, AcknowledgedAt: r.baseline.AcknowledgedAt}
	}
	return r.Admissions[len(r.Admissions)-1]
}

func (r rbacCapacitySeedReceipt) baselineGeneration() int64 {
	if r.baseline != nil {
		return r.baseline.Generation
	}
	return 1
}

type rbacCapacitySeedAdmission struct {
	GitRevision    string
	AcknowledgedAt time.Time
	Resources      int
}

func (r *rbacCapacitySeedReceipt) acknowledge(sha string, at time.Time, resources int) error {
	batchSize := r.ImportBatchSize
	if batchSize == 0 {
		batchSize = 10
	}
	if batchSize != 10 && batchSize != 60 {
		return errors.New("RBAC seed admission requires a declared small or complete fixture import")
	}
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
	if resources <= previousResources || resources > previousResources+batchSize {
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
	return walkRBACCapacitySourceModelsWithClock(reader, func(header rbacCapacitySourceHeader, resources []json.RawMessage, _ time.Time) error {
		return visit(header, resources)
	})
}

func walkRBACCapacitySourceModelsWithClock(reader io.Reader,
	visit func(rbacCapacitySourceHeader, []json.RawMessage, time.Time) error,
) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	var unterminatedLine bool
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		advance, line, err := bufio.ScanLines(data, atEOF)
		unterminatedLine = atEOF && len(data) != 0 && bytes.IndexByte(data, '\n') < 0
		return advance, line, err
	})
	var header *rbacCapacitySourceHeader
	var resources []json.RawMessage
	var remaining, modelBytes, total int
	var observedAt time.Time
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
			ObservedAt    time.Time `json:"observedAt"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			if unterminatedLine {
				// A live append can reach the reader before the JSON line is complete.
				var tail json.RawMessage
				if err := json.NewDecoder(bytes.NewReader(scanner.Bytes())).Decode(&tail); errors.Is(err, io.ErrUnexpectedEOF) {
					return io.ErrUnexpectedEOF
				}
			}
			return err
		}
		switch record.Type {
		case "begin":
			if header != nil || record.Snapshot == nil || record.ResourceCount < 0 || record.ResourceCount > 750 ||
				record.Snapshot.ModelVersion != "v1" || len(record.Snapshot.Resources) != 0 {
				return errors.New("RBAC source model has an invalid or overlapping header")
			}
			header, remaining, modelBytes = record.Snapshot, record.ResourceCount, len(scanner.Bytes())
			observedAt = record.ObservedAt
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
			if err := visit(*header, resources, observedAt); err != nil {
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
	// Controller acknowledgement does not change a previously resolved immutable roleRef.
	if resource.Status.ObservedGeneration < 0 || resource.Status.ObservedGeneration > resource.Metadata.Generation {
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

func verifyRBACCapacitySeedSources(t *testing.T, path string, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, before ...time.Time,
) {
	t.Helper()
	file, err := openRBACCapacityPrivateFixture(path, 1<<30)
	require.NoError(t, err)
	defer file.Close()
	indexes := make(map[string]int, count)
	for index := range count {
		indexes[plan.namespace(index).Name] = index
	}
	seen := make(map[string]bool)
	err = walkRBACCapacitySourceModelsWithClock(file, func(header rbacCapacitySourceHeader, resources []json.RawMessage, observedAt time.Time) error {
		if len(before) != 0 {
			if observedAt.IsZero() {
				return errors.New("native counts lack their independently captured source observation clock")
			}
			if observedAt.After(before[0]) {
				return nil
			}
		}
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
	return checkRBACCapacityNamespaceSourceWithCache(t, header, resources, namespace, receipt, generations, references, nil)
}

type rbacCapacityVerifiedSourceResource struct {
	Digest   [sha256.Size]byte
	Key      string
	Complete bool
}

func checkRBACCapacityNamespaceSourceWithCache(t *testing.T, header rbacCapacitySourceHeader, resources []json.RawMessage,
	namespace rbacCapacitySeedNamespace, receipt rbacCapacitySeedReceipt, generations, references map[string]int64,
	verified []rbacCapacityVerifiedSourceResource,
) bool {
	t.Helper()
	if receipt.baseline != nil && generations == nil {
		generations = receipt.baselineGenerations
		references = map[string]int64{"RoleBinding/capacity-binding-04": 1}
	}
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
	if verified != nil {
		require.Len(t, verified, len(resources), "resource proof caches must remain bounded by the authored tier size")
	}
	identities := make(map[string]bool)
	complete := true
	for index, encoded := range resources {
		digest := sha256.Sum256(encoded)
		if verified != nil && verified[index].Key != "" && verified[index].Digest == digest {
			previous := verified[index]
			require.False(t, identities[previous.Key])
			identities[previous.Key] = true
			complete = complete && previous.Complete
			continue
		}
		var resource rbacNativeResource
		require.NoError(t, json.Unmarshal(encoded, &resource))
		key := resource.Kind + "/" + resource.Metadata.Name
		spec, found := expected[key]
		require.True(t, found, "source models must not acquire undeclared policy")
		require.False(t, identities[key])
		identities[key] = true
		require.Equal(t, origin.Namespace, resource.Metadata.Namespace)
		require.Equal(t, rbacCapacitySeedSpec(t, spec, true), rbacCapacitySeedSpec(t, resource.Spec, true))
		generation, pinned := generations[key]
		resourceComplete := !pinned || resource.Metadata.Generation == generation
		uids := receipt.Roles
		if resource.Kind == "RoleBinding" {
			uids = receipt.Bindings
			referenceGeneration := resource.Metadata.Generation
			if pinned, found := references[key]; found {
				referenceGeneration = pinned
			}
			resolved, err := rbacCapacitySeedBindingResolved(encoded, receipt.Roles, referenceGeneration)
			require.NoError(t, err)
			resourceComplete = resourceComplete && resolved
		}
		require.Equal(t, uids[resource.Metadata.Name], resource.Metadata.UID)
		if verified != nil {
			verified[index] = rbacCapacityVerifiedSourceResource{Digest: digest, Key: key, Complete: resourceComplete}
		}
		complete = complete && resourceComplete
	}
	return complete
}

type rbacCapacityProbeExpectation struct {
	Subject, Namespace, Action string
	Allowed                    bool
	ObservedAt                 time.Time
}

type rbacCapacityProbeSource struct {
	Namespace  string
	Root       bool
	ObservedAt time.Time
}

type rbacCapacityUnverifiedSourceError struct{ RequestID string }

func (*rbacCapacityUnverifiedSourceError) Error() string {
	return "RBAC native probe evaluated an independently unverified source vector"
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
	return rbacCapacityAuthoredRootTreeDigest(subject, nil)
}

func rbacCapacityCanonicalRootBinding(manifest map[string]any) ([]byte, error) {
	type subject struct {
		Kind string `json:"kind" yaml:"kind"`
		Name string `json:"name" yaml:"name"`
	}
	var wire struct {
		APIVersion string `json:"apiVersion" yaml:"apiVersion"`
		Kind       string `json:"kind" yaml:"kind"`
		Metadata   struct {
			Name        string            `json:"name" yaml:"name"`
			Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
			Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
		} `json:"metadata" yaml:"metadata"`
		Spec struct {
			RoleRef struct {
				Kind string `json:"kind" yaml:"kind"`
				Name string `json:"name" yaml:"name"`
			} `json:"roleRef" yaml:"roleRef"`
			Subjects []subject `json:"subjects" yaml:"subjects"`
		} `json:"spec" yaml:"spec"`
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, err
	}
	if wire.APIVersion != "rbac.authorization.gitstore.dev/v1beta1" || wire.Kind != "ClusterRoleBinding" ||
		wire.Spec.RoleRef.Kind != "ClusterRole" || len(wire.Spec.Subjects) < 1 || len(wire.Spec.Subjects) > 1000 {
		return nil, errors.New("RBAC authored root history requires bounded canonical cluster bindings")
	}
	sort.Slice(wire.Spec.Subjects, func(i, j int) bool {
		a, b := wire.Spec.Subjects[i], wire.Spec.Subjects[j]
		return a.Kind < b.Kind || a.Kind == b.Kind && a.Name < b.Name
	})
	return yaml.Marshal(wire)
}

func rbacCapacityCanonicalRootRole(manifest map[string]any) ([]byte, error) {
	type rule struct {
		Resources     []string `json:"resources" yaml:"resources"`
		Verbs         []string `json:"verbs" yaml:"verbs"`
		ResourceNames []string `json:"resourceNames,omitempty" yaml:"resourceNames,omitempty"`
		When          []struct {
			Attr  string `json:"attr" yaml:"attr"`
			Op    string `json:"op" yaml:"op"`
			Value string `json:"value" yaml:"value"`
		} `json:"when,omitempty" yaml:"when,omitempty"`
	}
	var wire struct {
		APIVersion string `json:"apiVersion" yaml:"apiVersion"`
		Kind       string `json:"kind" yaml:"kind"`
		Metadata   struct {
			Name        string            `json:"name" yaml:"name"`
			Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
			Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
		} `json:"metadata" yaml:"metadata"`
		Spec struct {
			Rules []rule `json:"rules" yaml:"rules"`
		} `json:"spec" yaml:"spec"`
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return nil, err
	}
	if wire.APIVersion != "rbac.authorization.gitstore.dev/v1beta1" || wire.Kind != "ClusterRole" ||
		len(wire.Spec.Rules) == 0 || len(wire.Spec.Rules) > 100 {
		return nil, errors.New("RBAC authored root rule history requires bounded ClusterRoles")
	}
	for index := range wire.Spec.Rules {
		rule := &wire.Spec.Rules[index]
		for _, values := range []*[]string{&rule.Resources, &rule.Verbs, &rule.ResourceNames} {
			slices.Sort(*values)
			*values = slices.Compact(*values)
		}
		if len(rule.Resources) == 0 || len(rule.Verbs) == 0 {
			return nil, errors.New("RBAC authored root rules require explicit resources and verbs")
		}
		if len(rule.When) > 1 || len(rule.When) == 1 &&
			(rule.When[0].Attr != "namespace" || rule.When[0].Op != "eq" ||
				!rbacCapacitySeedName.MatchString(rule.When[0].Value) || len(rule.When[0].Value) > 63) {
			return nil, errors.New("RBAC authored conditional root rules require one bounded Namespace equality")
		}
	}
	sort.Slice(wire.Spec.Rules, func(i, j int) bool {
		left, _ := json.Marshal(wire.Spec.Rules[i])
		right, _ := json.Marshal(wire.Spec.Rules[j])
		return bytes.Compare(left, right) < 0
	})
	wire.Spec.Rules = slices.CompactFunc(wire.Spec.Rules, func(a, b rule) bool {
		return reflect.DeepEqual(a, b)
	})
	content, err = yaml.Marshal(wire)
	if len(content) > 64<<10 {
		return nil, errors.New("RBAC authored root role exceeds the manifest bound")
	}
	return content, err
}

func rbacCapacityMaximumRootBatch(plan rbacCapacitySeedPlan, grants []rbacCapacityAuthoredRootResource) ([]rbacCapacityAuthoredRootResource, error) {
	if err := plan.validate(); err != nil {
		return nil, err
	}
	if len(grants) != 2 || grants[0].Manifest["kind"] != "ClusterRole" || grants[1].Manifest["kind"] != "ClusterRoleBinding" {
		return nil, errors.New("maximum root batch requires the declared custom Role and binding")
	}
	remaining := 4 << 20
	for index, resource := range grants {
		canonicalize := rbacCapacityCanonicalRootRole
		if index == 1 {
			canonicalize = rbacCapacityCanonicalRootBinding
		}
		if _, err := canonicalize(resource.Manifest); err != nil {
			return nil, err
		}
		wire, err := json.Marshal(resource.Manifest)
		if err != nil {
			return nil, err
		}
		if len(wire) > 64<<10 {
			return nil, errors.New("maximum root batch cannot exceed the individual manifest bound")
		}
		remaining -= len(wire)
	}
	resources := slices.Clone(grants)
	for index := 0; index < 254; index++ {
		annotations := map[string]any{"capacity.gitstore.dev/padding": "x"}
		manifest := map[string]any{
			"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": "ClusterRole",
			"metadata": map[string]any{
				"name":   fmt.Sprintf("%s-root-filler-%03d", plan.Prefix, index),
				"labels": map[string]any{rbacCapacityOwnerLabel: plan.Owner}, "annotations": annotations,
			},
			"spec": map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{"read"}}}},
		}
		wire, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		size := remaining / (254 - index)
		padding := size - len(wire) + 1
		if padding < 1 || size > 64<<10 {
			return nil, errors.New("maximum root batch cannot fit bounded padding manifests")
		}
		annotations["capacity.gitstore.dev/padding"] = strings.Repeat("x", padding)
		wire, err = json.Marshal(manifest)
		if err != nil || len(wire) != size {
			return nil, errors.New("maximum root batch padding differs from the exact authored wire size")
		}
		if _, err := rbacCapacityCanonicalRootRole(manifest); err != nil {
			return nil, err
		}
		remaining -= len(wire)
		resources = append(resources, rbacCapacityAuthoredRootResource{Manifest: manifest, Generation: 1})
	}
	if remaining != 0 {
		return nil, errors.New("maximum root batch must exercise exactly four MiB")
	}
	return resources, nil
}

func rbacCapacityAuthoredRootTreeDigest(subject string, resources []rbacCapacityAuthoredRootResource) (string, error) {
	content, err := rbacCapacityCanonicalRootBinding(rbacCapacityInitialRootManifest(subject))
	if err != nil {
		return "", err
	}
	files := map[string][]byte{"rbac/clusterrolebindings/initial-admin.yaml": content}
	declared := map[string]bool{"rbac/clusterrolebindings/initial-admin.yaml": true}
	for _, resource := range resources {
		kind, name := resource.Manifest["kind"].(string), resource.Manifest["metadata"].(map[string]any)["name"].(string)
		if (kind != "ClusterRoleBinding" && kind != "ClusterRole") || name == "" || len(name) > 63 || !rbacCapacitySeedName.MatchString(name) {
			return "", errors.New("RBAC authored root manifest has an invalid canonical resource path")
		}
		path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
		if declared[path] {
			return "", errors.New("RBAC authored root manifests cannot replace or duplicate another declaration")
		}
		declared[path] = true
		canonicalize := rbacCapacityCanonicalRootBinding
		if kind == "ClusterRole" {
			canonicalize = rbacCapacityCanonicalRootRole
		}
		content, err := canonicalize(resource.Manifest)
		if err != nil {
			return "", err
		}
		if resource.Removed {
			continue
		}
		files[path] = content
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	tree := sha256.New()
	for _, path := range paths {
		entry := sha256.Sum256(files[path])
		_, _ = fmt.Fprintf(tree, "%d:%s:%x\n", len(path), path, entry)
	}
	return hex.EncodeToString(tree.Sum(nil)), nil
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
	return checkRBACCapacityInitialRootBinding(resources[0], expected)
}

func checkRBACCapacityInitialRootBinding(encoded json.RawMessage, expected rbacCapacityRootPolicy) (bool, error) {
	var resource rbacNativeResource
	if err := json.Unmarshal(encoded, &resource); err != nil {
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
	return rbacCapacitySeedBindingResolved(encoded, nil, 1)
}

type rbacCapacityAuthoredRootResource struct {
	Manifest                        map[string]any
	UID, RoleUID                    string
	Generation, ReferenceGeneration int64
	Removed                         bool
}

type rbacCapacityAuthoredRootAdmission struct {
	Policy          rbacCapacityRootPolicy
	GitRevision     string
	BaselineKnownAt time.Time
	AcknowledgedAt  time.Time
	Resources       []rbacCapacityAuthoredRootResource
	Cases           map[string]bool
	PendingCases    map[string]bool
}

func (a rbacCapacityAuthoredRootAdmission) admittedAt() time.Time {
	if !a.BaselineKnownAt.IsZero() {
		return a.BaselineKnownAt
	}
	return a.AcknowledgedAt
}

func validateRBACCapacityPendingRootCreation(previous, admission rbacCapacityAuthoredRootAdmission) error {
	if !reflect.DeepEqual(previous.Cases, admission.PendingCases) || len(admission.Resources) > 257 {
		return errors.New("unresolved new root grants must retain the previously authored decision truth and bounds")
	}
	key := func(resource rbacCapacityAuthoredRootResource) (string, error) {
		kind, valid := resource.Manifest["kind"].(string)
		metadata, metadataOK := resource.Manifest["metadata"].(map[string]any)
		name, nameOK := metadata["name"].(string)
		if !valid || !metadataOK || !nameOK || name == "" || (kind != "ClusterRole" && kind != "ClusterRoleBinding") {
			return "", errors.New("unresolved root history requires explicit authored resources")
		}
		return kind + "/" + name, nil
	}
	prior := make(map[string]rbacCapacityAuthoredRootResource, len(previous.Resources))
	for _, resource := range previous.Resources {
		if !resource.Removed {
			return errors.New("an unresolved root creation cannot reinterpret any earlier live authored reference")
		}
		name, err := key(resource)
		if err != nil {
			return err
		}
		prior[name] = resource
	}
	seen := make(map[string]bool, len(admission.Resources))
	newBindings := 0
	for _, resource := range admission.Resources {
		name, err := key(resource)
		if err != nil {
			return err
		}
		if seen[name] {
			return errors.New("unresolved root declarations cannot duplicate authored resources")
		}
		seen[name] = true
		if previous, found := prior[name]; found {
			if !reflect.DeepEqual(previous, resource) {
				return errors.New("a new unresolved root grant cannot rewrite an earlier removed incarnation")
			}
			continue
		}
		if resource.UID == "" || resource.Generation != 1 || resource.Removed {
			return errors.New("pending root references must come from newly authored live incarnations")
		}
		if strings.HasPrefix(name, "ClusterRoleBinding/") {
			if resource.ReferenceGeneration != 1 || resource.RoleUID == "" {
				return errors.New("a newly unresolved root binding requires its original pinned role reference")
			}
			newBindings++
		} else if resource.ReferenceGeneration != 0 || resource.RoleUID != "" {
			return errors.New("a newly authored root role cannot acquire a binding reference")
		}
	}
	for name := range prior {
		if !seen[name] {
			return errors.New("pending root history must retain its previously removed declarations")
		}
	}
	if newBindings == 0 {
		return errors.New("pending root truth requires a newly authored binding")
	}
	return nil
}

func checkRBACCapacityAuthoredRootSource(t *testing.T, header rbacCapacitySourceHeader, resources []json.RawMessage,
	admission rbacCapacityAuthoredRootAdmission,
) bool {
	return checkRBACCapacityRootSourceState(t, header, resources, admission, false)
}

func checkRBACCapacityRootSourceState(t *testing.T, header rbacCapacitySourceHeader, resources []json.RawMessage,
	admission rbacCapacityAuthoredRootAdmission, pending bool,
) bool {
	t.Helper()
	expected := admission.Policy
	require.NotEmpty(t, expected.NamespaceUID)
	require.NotEmpty(t, expected.BindingUID)
	require.NotEmpty(t, expected.AdminSubject)
	require.NotEmpty(t, expected.TreeDigest)
	require.LessOrEqual(t, len(admission.Resources), 257)
	digest, err := rbacCapacityAuthoredRootTreeDigest(expected.AdminSubject, admission.Resources)
	require.NoError(t, err)
	require.Equal(t, digest, expected.TreeDigest, "root tree identity must come from independently canonicalized authored policy")
	var origin struct {
		Namespace, NamespaceUID, GitRevision, TreeDigest string
		Root, Active                                     bool
	}
	var state struct {
		TierID                  string
		RoleCount, BindingCount int
	}
	require.NoError(t, json.Unmarshal(header.Origin, &origin))
	require.NoError(t, json.Unmarshal(header.State, &state))
	require.True(t, origin.Root && origin.Active)
	require.Equal(t, "gitstore-system", origin.Namespace)
	require.Equal(t, expected.NamespaceUID, origin.NamespaceUID)
	require.Equal(t, admission.GitRevision, origin.GitRevision)
	require.Equal(t, "cluster", state.TierID)
	if len(resources) == 0 && state.RoleCount == 0 && state.BindingCount == 0 {
		return false
	}
	require.Equal(t, expected.TreeDigest, origin.TreeDigest)
	authored := make(map[string]rbacCapacityAuthoredRootResource)
	for _, resource := range admission.Resources {
		kind := resource.Manifest["kind"].(string)
		metadata := resource.Manifest["metadata"].(map[string]any)
		name := metadata["name"].(string)
		key := kind + "/" + name
		require.NotEmpty(t, name)
		require.NotEmpty(t, resource.UID)
		require.Positive(t, resource.Generation)
		require.NotContains(t, authored, key)
		require.NotEqual(t, "ClusterRoleBinding/initial-admin", key, "dynamic policy must retain the independently pinned bootstrap binding")
		require.Empty(t, metadata["namespace"])
		require.Contains(t, []string{"ClusterRole", "ClusterRoleBinding"}, kind)
		if kind == "ClusterRoleBinding" {
			require.NotEmpty(t, resource.RoleUID)
			require.Positive(t, resource.ReferenceGeneration)
			require.LessOrEqual(t, resource.ReferenceGeneration, resource.Generation)
		} else {
			require.Empty(t, resource.RoleUID)
			require.Zero(t, resource.ReferenceGeneration)
			require.NotContains(t, []string{"cluster-admin", "namespace-full-control", "view"}, name,
				"mutable root histories must not change immutable release built-ins")
		}
		authored[key] = resource
	}
	live := 1
	for _, resource := range authored {
		if !resource.Removed {
			live++
		}
	}
	require.Equal(t, len(resources), state.RoleCount+state.BindingCount)
	require.GreaterOrEqual(t, len(resources), live)
	require.LessOrEqual(t, len(resources), len(authored)+1)
	seen := make(map[string]bool)
	complete := true
	roles, bindings := 0, 0
	for _, encoded := range resources {
		var resource rbacNativeResource
		require.NoError(t, json.Unmarshal(encoded, &resource))
		key := resource.Kind + "/" + resource.Metadata.Name
		if resource.Kind == "ClusterRole" {
			roles++
		} else {
			require.Equal(t, "ClusterRoleBinding", resource.Kind)
			bindings++
		}
		require.False(t, seen[key])
		seen[key] = true
		if key == "ClusterRoleBinding/initial-admin" {
			ready, err := checkRBACCapacityInitialRootBinding(encoded, expected)
			require.NoError(t, err)
			complete = complete && ready
			continue
		}
		pinned, found := authored[key]
		require.True(t, found, "root sources cannot acquire independently unauthored policy")
		require.Empty(t, resource.Metadata.Namespace)
		require.Equal(t, pinned.UID, resource.Metadata.UID)
		require.Equal(t, rbacCapacitySeedSpec(t, pinned.Manifest["spec"], true), rbacCapacitySeedSpec(t, resource.Spec, true))
		if resource.Metadata.Generation != pinned.Generation {
			complete = false
		}
		if pinned.Removed {
			if resource.Metadata.DeletionTimestamp == nil {
				complete = false
			} else {
				at, err := time.Parse(time.RFC3339Nano, *resource.Metadata.DeletionTimestamp)
				if err != nil || at.IsZero() {
					complete = false
				}
			}
			continue
		}
		if resource.Metadata.DeletionTimestamp != nil {
			complete = false
			continue
		}
		if resource.Kind == "ClusterRole" {
			continue
		}
		ready, err := rbacCapacitySeedBindingResolved(encoded, nil, pinned.ReferenceGeneration)
		require.NoError(t, err)
		var binding struct {
			Spec   struct{ RoleRef struct{ Kind, Name string } }
			Status struct {
				ObservedGeneration int64
				Resolved           *struct{ RoleRef struct{ UID string } }
			}
		}
		require.NoError(t, json.Unmarshal(encoded, &binding))
		require.Equal(t, "ClusterRole", binding.Spec.RoleRef.Kind)
		if binding.Status.Resolved != nil && binding.Status.Resolved.RoleRef.UID != pinned.RoleUID {
			ready = false
		}
		if pending && pinned.Generation == 1 && pinned.ReferenceGeneration == 1 &&
			binding.Status.Resolved == nil && binding.Status.ObservedGeneration >= 0 && binding.Status.ObservedGeneration <= 1 {
			ready = true
		}
		complete = complete && ready
	}
	require.Equal(t, state.RoleCount, roles)
	require.Equal(t, state.BindingCount, bindings)
	require.True(t, seen["ClusterRoleBinding/initial-admin"], "removal must retain the independently pinned administrator binding")
	for key, resource := range authored {
		if !resource.Removed {
			require.True(t, seen[key], "live authored root grants must not disappear from a source")
		}
	}
	return complete
}

func checkRBACCapacityProbeTraces(reader io.Reader, expected map[string]rbacCapacityProbeExpectation, models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource) error {
	return checkRBACCapacityObservedTraces(reader, expected, models, nil)
}

func checkRBACCapacityObservedTraces(reader io.Reader, expected map[string]rbacCapacityProbeExpectation,
	models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource,
	observe func(rbacCapacityPolicyVector, string, bool) error,
	timed ...func(rbacCapacityPolicyVector, string, bool, time.Time) error,
) error {
	return checkRBACCapacityObservedTracesWithBounds(reader, expected, models, 128, 1<<30, observe, timed...)
}

func checkRBACCapacityObservedTracesWithBounds(reader io.Reader, expected map[string]rbacCapacityProbeExpectation,
	models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource, maxProbes int, readBound int64,
	observe func(rbacCapacityPolicyVector, string, bool) error,
	timed ...func(rbacCapacityPolicyVector, string, bool, time.Time) error,
) error {
	if maxProbes <= 0 || maxProbes > 16384 || readBound <= 0 || readBound > 8<<30 ||
		len(expected) == 0 || len(expected) > maxProbes || len(timed) > 1 {
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
		if total > readBound {
			return errors.New("RBAC diagnostic decision evidence exceeds its declared read bound")
		}
		var decision struct {
			At            float64 `json:"ts"`
			RequestID     string  `json:"request_id"`
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
				return &rbacCapacityUnverifiedSourceError{RequestID: decision.RequestID}
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
		if len(timed) != 0 {
			if decision.At <= 0 || decision.At >= float64(math.MaxInt64) {
				return errors.New("RBAC mutation decision lacks its actual producer clock")
			}
			at := time.Unix(int64(decision.At), int64((decision.At-float64(int64(decision.At)))*float64(time.Second)))
			if probe.ObservedAt.IsZero() || probe.ObservedAt.Before(at) {
				return errors.New("RBAC mutation HTTP receipt predates its actual producer decision")
			}
			if err := timed[0](vector, decision.RequestID, probe.Allowed, at); err != nil {
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

func checkRBACCapacityScheduledTraces(reader io.Reader, owner string, replica int, offered int64,
	cases []rbacCapacityRequest, models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource,
	observe func(rbacCapacityPolicyVector, rbacCapacityRequest, time.Time, time.Duration) error,
	readBounds ...int64,
) (accounting rbacCapacityAccounting, err error) {
	readBound := int64(8 << 30)
	if len(readBounds) == 1 {
		readBound = readBounds[0]
	}
	validOwner, _ := regexp.MatchString(`^[a-z0-9][a-z0-9-]{0,63}$`, owner)
	if len(readBounds) > 1 || readBound <= 0 || readBound > 8<<30 ||
		!validOwner || replica < 0 || replica > 1 || offered <= 0 || offered > 36_000_000 ||
		len(cases) < 10 || len(cases) > 20000 || len(cases)%10 != 0 || len(models) < 2 {
		return accounting, errors.New("RBAC scheduled traces require a bounded independently declared working set")
	}
	for offset := 0; offset < len(cases); offset += 10 {
		var allowed, denied, isolated int
		for _, request := range cases[offset : offset+10] {
			if request.Subject == "" || request.Namespace == "" ||
				(request.Action != "product.list" && request.Action != "role.list") {
				return accounting, errors.New("RBAC scheduled case has an incomplete ordinary request identity")
			}
			switch request.Expected {
			case "allow":
				allowed++
			case "deny":
				denied++
			case "isolation":
				isolated++
			default:
				return accounting, errors.New("RBAC scheduled case has an unknown outcome")
			}
		}
		if allowed != 6 || denied != 3 || isolated != 1 {
			return accounting, errors.New("RBAC scheduled cases must preserve the independently authored 60/30/10 mix")
		}
	}
	accounting.Offered = offered
	defer func() { accounting.Dropped = offered - accounting.Completed }()
	seen := make([]byte, (offered+7)/8)
	prefix := fmt.Sprintf("%s-ordinary-%d-", owner, replica)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var bytesRead int64
	for scanner.Scan() {
		bytesRead += int64(len(scanner.Bytes())) + 1
		if bytesRead > readBound {
			return accounting, errors.New("RBAC scheduled decision evidence exceeds its bounded read window")
		}
		var decision struct {
			RequestID string `json:"request_id"`
			Action    string
			At        float64 `json:"ts"`
			LatencyNS *int64  `json:"latency_ns"`
		}
		if json.Unmarshal(scanner.Bytes(), &decision) != nil ||
			!strings.HasPrefix(decision.RequestID, owner+"-ordinary-") {
			continue
		}
		if !strings.HasPrefix(decision.RequestID, prefix) {
			return accounting, errors.New("RBAC ordinary decision receipt belongs to a different replica")
		}
		suffix := strings.TrimPrefix(decision.RequestID, prefix)
		index, parseErr := strconv.ParseInt(suffix, 10, 64)
		if parseErr != nil || index < 0 || index >= offered || strconv.FormatInt(index, 10) != suffix {
			return accounting, errors.New("RBAC ordinary receipt is outside the independently scheduled offers")
		}
		request := cases[index%int64(len(cases))]
		if decision.Action != request.Action {
			continue
		}
		mask := byte(1 << (index % 8))
		if seen[index/8]&mask != 0 || decision.At <= 0 || decision.At >= float64(math.MaxInt64) {
			return accounting, errors.New("RBAC scheduled decision has a duplicate or invalid observation clock")
		}
		if decision.LatencyNS == nil || *decision.LatencyNS < 0 {
			return accounting, errors.New("RBAC scheduled decision lacks its nonnegative integer provider-call timing")
		}
		request.ID, request.Replica = decision.RequestID, replica
		expected := map[string]rbacCapacityProbeExpectation{
			request.ID: {Subject: request.Subject, Namespace: request.Namespace, Action: request.Action, Allowed: request.Expected == "allow"},
		}
		at := time.Unix(int64(decision.At), int64((decision.At-float64(int64(decision.At)))*float64(time.Second)))
		err := checkRBACCapacityObservedTraces(bytes.NewReader(scanner.Bytes()), expected, models,
			func(vector rbacCapacityPolicyVector, _ string, _ bool) error {
				if observe != nil {
					return observe(vector, request, at, time.Duration(*decision.LatencyNS))
				}
				return nil
			})
		if err != nil {
			return accounting, err
		}
		seen[index/8] |= mask
		accounting.Completed++
		switch request.Expected {
		case "allow":
			accounting.Allowed++
		case "deny":
			accounting.Denied++
		case "isolation":
			accounting.Invalid++
		}
	}
	if err := scanner.Err(); err != nil {
		return accounting, err
	}
	if accounting.Completed != offered {
		return accounting, errors.New("RBAC scheduled offers are missing independently verified ordinary decision receipts")
	}
	return accounting, nil
}

func verifyRBACCapacityProbeTraces(t *testing.T, sourcePath, decisionPath string, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, expected map[string]rbacCapacityProbeExpectation,
	capturedBefore time.Time,
) {
	t.Helper()
	require.False(t, capturedBefore.IsZero())
	models := readRBACCapacityProbeSources(t, sourcePath, plan, count, receipts, root, capturedBefore)
	decisions, err := openRBACCapacityPrivateFixture(decisionPath, 1<<30)
	require.NoError(t, err)
	defer decisions.Close()
	require.NoError(t, checkRBACCapacityObservedTracesWithBounds(decisions, expected, models, len(expected), 1<<30, nil))
}

func readRBACCapacityProbeSources(t *testing.T, sourcePath string, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, before ...time.Time,
) map[rbacCapacityPolicyRevision]rbacCapacityProbeSource {
	return readRBACCapacityProbeSourcesWithHistory(t, sourcePath, plan, count, receipts, root, nil, before...)
}

func readRBACCapacityProbeSourcesWithHistory(t *testing.T, sourcePath string, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, history []rbacCapacityAuthoredAdmission, before ...time.Time,
) map[rbacCapacityPolicyRevision]rbacCapacityProbeSource {
	return readRBACCapacityProbeSourcesWithRootHistory(t, sourcePath, plan, count, receipts, root, history, nil, before...)
}

func readRBACCapacityProbeSourcesWithRootHistory(t *testing.T, sourcePath string, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, history []rbacCapacityAuthoredAdmission,
	rootHistory []rbacCapacityAuthoredRootAdmission, before ...time.Time,
) map[rbacCapacityPolicyRevision]rbacCapacityProbeSource {
	t.Helper()
	source, err := openRBACCapacityPrivateFixture(sourcePath, 1<<30)
	require.NoError(t, err)
	defer source.Close()
	indexes := make(map[string]int, count)
	for index := range count {
		indexes[plan.namespaceName(index)] = index
	}
	historyIndexes := make(map[rbacCapacityMutationRevision]int, len(history))
	rootIndexes := make(map[string]int, len(rootHistory))
	for index, admission := range rootHistory {
		require.NotEmpty(t, admission.GitRevision)
		_, duplicate := rootIndexes[admission.GitRevision]
		require.False(t, duplicate, "authored root history cannot reinterpret a canonical revision")
		require.Equal(t, root.NamespaceUID, admission.Policy.NamespaceUID)
		require.Equal(t, root.BindingUID, admission.Policy.BindingUID)
		require.Equal(t, root.AdminSubject, admission.Policy.AdminSubject)
		if len(admission.PendingCases) != 0 {
			require.Positive(t, index)
			require.NoError(t, validateRBACCapacityPendingRootCreation(rootHistory[index-1], admission))
		}
		rootIndexes[admission.GitRevision] = index
	}
	for index, admission := range history {
		key := rbacCapacityMutationRevision{admission.Namespace.Name, admission.Receipt.GitRevision}
		_, duplicate := historyIndexes[key]
		require.False(t, duplicate, "authored source history cannot reinterpret an original tier revision")
		historyIndexes[key] = index
	}
	namespaces := make(map[int]rbacCapacitySeedNamespace, count)
	verifiedResources := make(map[rbacCapacityMutationRevision][]rbacCapacityVerifiedSourceResource)
	models := make(map[rbacCapacityPolicyRevision]rbacCapacityProbeSource)
	err = walkRBACCapacitySourceModelsWithClock(source, func(header rbacCapacitySourceHeader, resources []json.RawMessage, observedAt time.Time) error {
		if len(before) != 0 {
			if observedAt.IsZero() {
				return errors.New("ordinary source evidence lacks its actual capture clock")
			}
			if observedAt.After(before[0]) {
				return nil
			}
		}
		var origin struct {
			Namespace, NamespaceUID, GitRevision string
			Root                                 bool
		}
		var state struct{ TierID string }
		require.NoError(t, json.Unmarshal(header.Origin, &origin))
		require.NoError(t, json.Unmarshal(header.State, &state))
		revision := rbacCapacityPolicyRevision{TierUID: state.TierID, GitRevision: origin.GitRevision, Digest: header.ContentDigest}
		if previous, verified := models[revision]; verified {
			// The walker rehashes every frame before this immutable-vector cache.
			if !observedAt.IsZero() && (previous.ObservedAt.IsZero() || observedAt.Before(previous.ObservedAt)) {
				previous.ObservedAt = observedAt
				models[revision] = previous
			}
			return nil
		}
		if !origin.Root {
			receipt, owned := receipts[origin.Namespace]
			if !owned {
				return nil
			}
			index, found := indexes[origin.Namespace]
			require.True(t, found)
			var namespace rbacCapacitySeedNamespace
			var generations, references map[string]int64
			if historyIndex, found := historyIndexes[rbacCapacityMutationRevision{origin.Namespace, origin.GitRevision}]; found {
				admission := history[historyIndex]
				namespace, receipt = admission.Namespace, admission.Receipt
				generations, references = admission.Generations, admission.References
			} else {
				if origin.GitRevision != receipt.GitRevision {
					return nil
				}
				var cached bool
				namespace, cached = namespaces[index]
				if !cached {
					namespace = plan.namespace(index)
					namespaces[index] = namespace
				}
			}
			key := rbacCapacityMutationRevision{origin.Namespace, receipt.GitRevision}
			verified := verifiedResources[key]
			if verified == nil {
				verified = make([]rbacCapacityVerifiedSourceResource, len(receipt.Roles)+len(receipt.Bindings))
				verifiedResources[key] = verified
			}
			if origin.GitRevision != receipt.GitRevision ||
				!checkRBACCapacityNamespaceSourceWithCache(t, header, resources, namespace, receipt, generations, references, verified) {
				return nil
			}
		} else if len(rootHistory) != 0 {
			index, found := rootIndexes[origin.GitRevision]
			if !found {
				return nil
			}
			admission := rootHistory[index]
			if !checkRBACCapacityAuthoredRootSource(t, header, resources, admission) &&
				(len(admission.PendingCases) == 0 || !checkRBACCapacityRootSourceState(t, header, resources, admission, true)) {
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
		models[revision] = rbacCapacityProbeSource{Namespace: origin.Namespace, Root: origin.Root, ObservedAt: observedAt}
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	return models
}

func waitRBACCapacityDecisionReadiness(t *testing.T, fixture rbacNativeFixture, plan rbacCapacitySeedPlan, count int,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, tokens map[string]string, sourcePaths, decisionPaths []string,
) {
	t.Helper()
	startedAt := time.Now()
	deadline := startedAt.Add(30 * time.Second)
	for replica, api := range fixture.apis {
		probes := make(map[string]rbacCapacityProbeExpectation, count)
		for index := range count {
			namespace := plan.namespaceName(index)
			subject := plan.Subjects[index%len(plan.Subjects)]
			require.NotEmpty(t, tokens[subject], "source readiness requires the independently enrolled direct grantee")
			require.NotEqual(t, fixture.token, tokens[subject], "source readiness must not substitute administrator credentials")
			ready := false
			lastStatus := 0
			var lastCodes []string
			for attempt := 0; time.Now().Before(deadline); attempt++ {
				id := fmt.Sprintf("%s-readiness-%d-%d-%d", plan.Owner, replica, index, attempt)
				response := secretTestGraphQLWithHeaders(t, api, tokens[subject],
					`query($namespace:String!){products(namespace:$namespace,first:1){__typename}}`,
					map[string]any{"namespace": namespace}, min(5*time.Second, time.Until(deadline)), map[string]string{"X-Request-ID": id})
				lastStatus, lastCodes = response.Status, nil
				for _, failure := range response.Errors {
					var details struct{ Extensions struct{ Code string } }
					if err := json.Unmarshal(failure, &details); err != nil {
						lastCodes = append(lastCodes, "malformed-error")
					} else {
						lastCodes = append(lastCodes, details.Extensions.Code)
					}
				}
				if response.Status == http.StatusOK && len(response.Errors) == 0 {
					probes[id] = rbacCapacityProbeExpectation{
						Subject: subject, Namespace: namespace, Action: "product.list", Allowed: true,
					}
					ready = true
					break
				}
				time.Sleep(min(500*time.Millisecond, max(0, time.Until(deadline))))
			}
			require.True(t, ready, "static workload must start with an evaluated, independently verified admitted source; warm-up never resets mutation clocks (replica=%d index=%d status=%d codes=%v)",
				replica, index, lastStatus, lastCodes)
		}
		verified := false
		var joinError error
		retries := 0
		for time.Now().Before(deadline) {
			models := readRBACCapacityProbeSources(t, sourcePaths[replica], plan, count, receipts, root)
			decisions, err := openRBACCapacityPrivateFixture(decisionPaths[replica], 1<<30)
			require.NoError(t, err)
			err = checkRBACCapacityObservedTracesWithBounds(decisions, probes, models, count, 1<<30, nil)
			joinError = err
			require.NoError(t, decisions.Close())
			if err == nil {
				verified = time.Now().Before(deadline)
				break
			}
			var unverified *rbacCapacityUnverifiedSourceError
			if errors.As(err, &unverified) && time.Now().Before(deadline) {
				probe := probes[unverified.RequestID]
				id := fmt.Sprintf("%s-readiness-retry-%d-%d", plan.Owner, replica, retries)
				retries++
				response := secretTestGraphQLWithHeaders(t, api, tokens[probe.Subject],
					`query($namespace:String!){products(namespace:$namespace,first:1){__typename}}`,
					map[string]any{"namespace": probe.Namespace}, min(5*time.Second, time.Until(deadline)), map[string]string{"X-Request-ID": id})
				if response.Status == http.StatusOK && len(response.Errors) == 0 {
					delete(probes, unverified.RequestID)
					probes[id] = probe
				}
			}
			time.Sleep(min(500*time.Millisecond, max(0, time.Until(deadline))))
		}
		require.True(t, verified, "every readiness decision must join independently verified sources within the unchanged warm-up deadline (replica=%d probes=%d last_error=%v)",
			replica, len(probes), joinError)
		t.Logf("independent source readiness: replica=%d namespaces=%d elapsed=%s", replica, len(probes), time.Since(startedAt))
	}
}

func seedRBACCapacityNamespace(t *testing.T, fixture rbacNativeFixture, plan rbacCapacitySeedPlan, index, importBatchSize int, personalToken, receiptDirectory string, namespaceWriter *sync.Mutex, readbackSlots map[string]<-chan time.Time) {
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
	require.Contains(t, []int{10, 60}, importBatchSize)
	record := rbacCapacitySeedReceipt{Owner: plan.Owner, Namespace: namespace.Name, Tier: namespace.Tier, ImportBatchSize: importBatchSize}
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
	for start := 0; start < len(namespace.Resources); start += importBatchSize {
		end := min(start+importBatchSize, len(namespace.Resources))
		for _, resource := range namespace.Resources[start:end] {
			name := resource["metadata"].(map[string]any)["name"].(string)
			expected[resource["kind"].(string)+"/"+name] = resource["spec"].(map[string]any)
			git.write(t, "rbac/"+strings.ToLower(resource["kind"].(string))+"s/"+name+".yaml", resource)
		}
		sha := git.commit(t)
		git.push(t)
		acknowledgedAt := time.Now()
		require.NoError(t, record.acknowledge(sha, acknowledgedAt, end))
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
	var config rbacCapacityNativeSeedConfig
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&config))
	require.NoError(t, config.Plan.validate())
	require.NoError(t, validateRBACCapacityMode(config))
	if config.Mode == "alpha" {
		require.Equal(t, os.Getenv("CAPACITY_RBAC_SEED_FILE"), os.Getenv("RBAC_TEST_CAPACITY_SEED_FILE"))
		preflightRBACCapacityDeployment(t)
		config.K6Workload.alpha = true
	}
	mode, err := parseCapacityMode(config.Mode)
	require.NoError(t, err)
	require.NoError(t, validateRBACCapacityMutationSelection(config), "refuse incompatible propagation selection before any writes")
	require.NoError(t, validateRBACCapacityRetainedVerification(config), "retained fixtures require explicit read-only diagnostic replay")
	require.NoError(t, validateRBACCapacitySeedResume(config), "seed continuation must be explicitly selected before any writes")
	if config.K6Workload != nil && config.K6Workload.Mutation != nil {
		require.NoError(t, validateRBACCapacityMutation(config.Plan, config.Count, config.K6Workload, config.InitialAdminSubject))
	}
	if config.ImportBatchSize == 0 {
		config.ImportBatchSize = 10
		if mode != capacityModeDiagnostic {
			config.ImportBatchSize = 60
		}
	}
	require.Contains(t, []int{10, 60}, config.ImportBatchSize,
		"imports must use the declared small-batch diagnostic or complete bounded fixture")
	require.GreaterOrEqual(t, config.Count, 1)
	require.LessOrEqual(t, config.Count, 1000)
	require.GreaterOrEqual(t, config.Workers, 1)
	require.LessOrEqual(t, config.Workers, 4)
	if mode != capacityModeDiagnostic {
		require.Equal(t, 1000, config.Count, "non-diagnostic seeding must prepare the full declared working set")
		require.Len(t, config.PrincipalTokenFiles, len(config.Plan.principalSubjects()),
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
		require.NotContains(t, config.Plan.principalSubjects(), config.InitialAdminSubject)
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
		require.Len(t, config.PrincipalTokenFiles, len(config.Plan.principalSubjects()))
		for _, subject := range config.Plan.principalSubjects() {
			content, err := readRBACCapacityPrivateFixture(config.PrincipalTokenFiles[subject])
			require.NoError(t, err)
			tokens[subject] = strings.TrimSpace(string(content))
			require.NotEmpty(t, tokens[subject])
			require.NotEqual(t, fixture.token, tokens[subject], "ordinary principals must not reuse the administrator credential")
		}
	}
	personalToken, err := readRBACCapacityPrivateFixture(config.PersonalTokenFile)
	require.NoError(t, err)
	if config.K6Workload != nil {
		require.Len(t, config.DecisionLogFiles, len(fixture.apis))
		require.Len(t, config.SourceModelFiles, len(fixture.apis))
		tokens[config.Plan.PersonalSubject] = strings.TrimSpace(string(personalToken))
	}
	var requests []rbacCapacityRequest
	if workload := config.DecisionWorkload; workload != nil {
		require.Equal(t, capacityModeDiagnostic, mode, "this bounded static parity driver is not sustained acceptance")
		require.Len(t, config.DecisionLogFiles, len(fixture.apis), "ordinary workload decisions require independent source and request joins")
		require.Len(t, config.SourceModelFiles, len(fixture.apis))
		require.NoError(t, workload.Schedule.validate())
		scheduled := rbacCapacityScheduledOffers(workload.Schedule.Duration, workload.Schedule.Rate, workload.Schedule.period())
		maxOffers := int64(240)
		if config.VerifyRetained {
			maxOffers = int64(config.Count) * 20
		}
		require.True(t, scheduled >= 20 && scheduled <= maxOffers && scheduled%20 == 0,
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
	require.NoError(t, copyRBACCapacityRetainedSeed(config), "never overwrite original seed receipts or prior run evidence")
	if !config.VerifyRetained && config.ResumeSeedDirectory == "" {
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
						seedRBACCapacityNamespace(t, fixture, config.Plan, index, config.ImportBatchSize, strings.TrimSpace(string(personalToken)), config.ReceiptDirectory, &namespaceWriter, readbackSlots)
					}
				})
			}
		})
		require.False(t, failed.Load(), "partial seed receipts remain private; do not adopt or overwrite failed fixtures")
	}
	seenUIDs := make(map[string]bool)
	receipts := make(map[string]rbacCapacitySeedReceipt, config.Count)
	for index := range config.Count {
		content, err := readRBACCapacityPrivateFixture(filepath.Join(config.ReceiptDirectory, config.Plan.namespace(index).Name+".json"))
		require.NoError(t, err)
		var record rbacCapacitySeedReceipt
		require.NoError(t, json.Unmarshal(content, &record))
		require.True(t, record.Complete)
		resources := len(config.Plan.namespace(index).Resources)
		require.Equal(t, config.ImportBatchSize, record.ImportBatchSize, "receipt imports must match the independently declared fixture")
		require.Len(t, record.Admissions, (resources+config.ImportBatchSize-1)/config.ImportBatchSize,
			"each native import must retain its successful admission acknowledgement")
		finalAdmission := record.Admissions[len(record.Admissions)-1]
		require.Equal(t, record.GitRevision, finalAdmission.GitRevision)
		require.Equal(t, resources, finalAdmission.Resources)
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
	require.NoError(t, applyRBACCapacityResumeBaselines(config, receipts))
	if config.DecisionWorkload != nil || config.K6Workload != nil ||
		len(config.SourceModelFiles) != 0 && len(config.DecisionLogFiles) != 0 {
		waitRBACCapacityDecisionReadiness(t, fixture, config.Plan, config.Count, receipts, root,
			tokens, config.SourceModelFiles, config.DecisionLogFiles)
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
		t.Logf("bounded ordinary RBAC request parity: %+v; static diagnostic only, not propagation or sustained acceptance", accounting)
		require.NoError(t, accounting.validate(scheduled, false))
		require.Equal(t, scheduled*6/10, accounting.Allowed)
		require.Equal(t, scheduled*3/10, accounting.Denied)
		require.Equal(t, scheduled/10, accounting.Invalid)
	}
	staticSourcesBefore := time.Now()
	if workload := config.K6Workload; workload != nil {
		runRBACCapacityK6Diagnostic(t, fixture, config.Plan, config.Count, workload, tokens,
			config.ReceiptDirectory, receipts, root, config.SourceModelFiles, config.DecisionLogFiles)
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
					receipts, root, probes[replica], staticSourcesBefore)
				previousLog = currentLog
			}
			previous = current
		}
	}
	if workload := config.DecisionWorkload; workload != nil && workload.PropagationDiagnostic {
		runRBACCapacityPropagationDiagnostic(t, fixture, config.Plan, config.Count, workload.GroupMembers, tokens,
			config.ReceiptDirectory, receipts, config.SourceModelFiles, config.DecisionLogFiles, root)
	}
	phase := "seeding"
	if config.VerifyRetained {
		phase = "retained-source verification"
	}
	t.Logf("native RBAC %s completed: namespaces=%d roles=%d bindings=%d evidence_class=%s; this is not sustained workload acceptance and does not change prior results",
		phase, config.Count, config.Count*10, config.Count*50, mode)
}

type rbacCapacityMutationAdmission struct {
	Offer                           int64
	Namespace, BindingUID, RoleUID  string
	DispatchedAt, AcknowledgedAt    time.Time
	DispatchedAfter                 *time.Duration `json:",omitempty"`
	GitRevision                     string
	Transport                       string `json:",omitempty"`
	ReferenceReads                  int    `json:",omitempty"`
	Generation, ReferenceGeneration int64
	Probes                          [2]map[string]rbacCapacityProbeExpectation
}

type rbacCapacityMutationReceipt struct {
	Owner, Subject                       string
	ScheduledAt, CompletedAt, RestoredAt time.Time
	Baseline                             [2]map[string]rbacCapacityProbeExpectation
	Admissions                           []rbacCapacityMutationAdmission
	Restorations                         []rbacCapacityMutationAdmission
	Accounting                           rbacCapacityAccounting
	Failures                             []rbacCapacityMutationFailure
	Complete                             bool
	Pending                              *rbacCapacityMutationAdmission    `json:",omitempty"`
	CleanupError                         string                            `json:",omitempty"`
	RecoveryPins                         []rbacCapacityMutationRecoveryPin `json:",omitempty"`
}

type rbacCapacityMutationRecoveryPin struct {
	NamespaceNodeUID string
	State            string
	Admission        rbacCapacityMutationAdmission
}

type rbacCapacityMutationFailure struct {
	Offer      int64
	Kind       string
	ObservedAt time.Time
	GraphQL    *rbacCapacityGraphQLAdmissionError `json:",omitempty"`
}

type rbacCapacityGraphQLAdmissionError struct {
	HTTPStatus int
	Code       string
}

func (e *rbacCapacityGraphQLAdmissionError) Error() string {
	return fmt.Sprintf("single GraphQL mutation was not acknowledged successfully (HTTP %d, code %s)", e.HTTPStatus, e.Code)
}

func validateRBACCapacityMutation(plan rbacCapacitySeedPlan, count int, config *rbacCapacityK6DiagnosticConfig,
	admin string,
) error {
	if config == nil || config.Mutation == nil {
		return errors.New("mutation companion must be explicitly configured")
	}
	mutation := config.Mutation
	if mutation.Transport != "" && mutation.Transport != "mixed" {
		return errors.New("mutation transport must be native Git or an explicit mixed Git/GraphQL schedule")
	}
	if err := plan.validate(); err != nil {
		return err
	}
	if count < 5 || count > 1000 || mutation.Subject != plan.Prefix+"-filler-mutation" ||
		mutation.Subject == plan.PersonalSubject || mutation.Subject == plan.Group || mutation.Subject == admin ||
		slices.Contains(plan.principalSubjects(), mutation.Subject) || slices.Contains(config.GroupMembers, mutation.Subject) ||
		mutation.TokenFile == "" {
		return errors.New("mutation companion requires a separately enrolled disjoint holdout and four owned ORGANIZATION Namespaces")
	}
	if plan.MutationSubject != "" && plan.MutationSubject != mutation.Subject {
		return errors.New("conditional Role seed and mutation cohort must identify the same disjoint holdout")
	}
	schedule := mutation.Schedule
	if err := schedule.validate(); err != nil {
		return err
	}
	if config.RootUnderLoad && count < 6 {
		return errors.New("root-under-load requires untouched ORGANIZATION Namespace index five")
	}
	if schedule.Period != time.Minute || schedule.Rate != 50 || schedule.Workers != 1 || schedule.Pending != 1 ||
		schedule.Drain > 5*time.Second || schedule.Duration < 12*time.Second || schedule.Duration > 30*time.Minute ||
		schedule.Duration%(12*time.Second) != 0 || config.Duration < schedule.Duration+2*time.Second || config.Duration > 31*time.Minute {
		return errors.New("only one bounded writer at 50/minute with complete 12-second cycles up to 30 minutes is supported")
	}
	return nil
}

func rbacCapacityRootUnderLoadTarget(plan rbacCapacitySeedPlan, count int, config *rbacCapacityK6DiagnosticConfig,
	admin, token, adminToken string, ordinary rbacCapacityK6WorkingSet,
) (rbacCapacityRequest, error) {
	if err := validateRBACCapacityK6Diagnostic(config); err != nil {
		return rbacCapacityRequest{}, err
	}
	if !config.RootUnderLoad {
		return rbacCapacityRequest{}, errors.New("root-under-load must be explicitly selected")
	}
	if err := validateRBACCapacityMutation(plan, count, config, admin); err != nil {
		return rbacCapacityRequest{}, err
	}
	if token == "" || token == adminToken || ordinary.Owner != plan.Owner || len(ordinary.Cases) == 0 {
		return rbacCapacityRequest{}, errors.New("root holdout requires an independently enrolled token and owned ordinary working set")
	}
	for _, sample := range ordinary.Cases {
		if sample.Subject == config.Mutation.Subject || sample.Token == token {
			return rbacCapacityRequest{}, errors.New("root target subject and token must each be disjoint from every ordinary/cohort principal")
		}
	}
	return rbacCapacityRequest{Subject: config.Mutation.Subject, Namespace: plan.namespaceName(5),
		Action: "product.list", Expected: "deny"}, nil
}

func validateRBACCapacityMutationSelection(config rbacCapacityNativeSeedConfig) error {
	if config.Mode == "alpha" {
		return validateRBACCapacityAlpha(config)
	}
	if (config.Plan.ConditionalRoot || config.Plan.MaximumRootBatch) && (config.DecisionWorkload == nil || !config.DecisionWorkload.PropagationDiagnostic) &&
		(config.K6Workload == nil || !config.K6Workload.RootUnderLoad) {
		return errors.New("root variants require an explicitly selected sequential or under-load propagation diagnostic")
	}
	if config.K6Workload == nil || config.K6Workload.Mutation == nil {
		if config.Plan.MutationSubject != "" {
			if config.VerifyRetained {
				return validateRBACCapacityRetainedVerification(config)
			}
			return errors.New("conditional Role seeding requires its explicitly selected mutation companion")
		}
		return nil
	}
	if config.Mode != "diagnostic" || config.DecisionWorkload != nil && config.DecisionWorkload.PropagationDiagnostic {
		return errors.New("rotating mutations require diagnostic mode without original-seed sequential propagation")
	}
	return validateRBACCapacityMutation(config.Plan, config.Count, config.K6Workload, config.InitialAdminSubject)
}

func checkRBACCapacityMutationReceipt(plan rbacCapacitySeedPlan, count int,
	config *rbacCapacityK6DiagnosticConfig, seeds map[string]rbacCapacitySeedReceipt,
	record rbacCapacityMutationReceipt, window rbacCapacityK6ProcessWindow,
) error {
	if err := validateRBACCapacityMutation(plan, count, config, window.Root.AdminSubject); err != nil {
		return err
	}
	schedule := config.Mutation.Schedule
	offered := rbacCapacityScheduledOffers(schedule.Duration, schedule.Rate, schedule.Period)
	if window.Owner != plan.Owner || window.StartedAt.IsZero() || !window.CompletedAt.After(window.StartedAt) ||
		!record.Complete || record.Pending != nil || record.CleanupError != "" || len(record.RecoveryPins) != 0 ||
		len(record.Failures) != 0 || record.Owner != plan.Owner ||
		record.Subject != config.Mutation.Subject || len(record.Admissions) != int(offered) ||
		!record.ScheduledAt.After(window.StartedAt) || !record.CompletedAt.Before(window.CompletedAt) ||
		record.CompletedAt.Sub(record.ScheduledAt) < schedule.Duration ||
		record.CompletedAt.Sub(record.ScheduledAt) > schedule.Duration+schedule.Drain ||
		!record.RestoredAt.After(window.CompletedAt) || record.RestoredAt.Sub(window.CompletedAt) > 30*time.Second {
		return errors.New("sealed mutation journal lacks complete bounded load and post-load restoration")
	}
	if err := record.Accounting.validate(offered, false); err != nil {
		return err
	}
	generations := make(map[string]int64, 4)
	clocks := make(map[string]time.Time, 4)
	allowed := make(map[string]bool, 4)
	seen := make(map[string]bool)
	uids := make(map[string]bool)
	for index := 1; index <= 4; index++ {
		name := plan.namespaceName(index)
		seed := seeds[name]
		if !seed.Complete || seed.Owner != plan.Owner || seed.Namespace != name || seed.Tier != "ORGANIZATION" ||
			len(seed.Admissions) == 0 || seed.baselineAdmission().AcknowledgedAt.IsZero() ||
			seed.GitRevision != seed.baselineAdmission().GitRevision {
			return errors.New("mutation tier lacks its independently complete original seed")
		}
		for _, uid := range []string{seed.NamespaceNodeUID, seed.Roles["capacity-role-04"], seed.Bindings["capacity-binding-04"]} {
			if uid == "" || uids[uid] {
				return errors.New("mutation tiers require distinct original Namespace, Role and binding incarnations")
			}
			uids[uid] = true
		}
		generations[name], clocks[name] = seed.baselineGeneration(), seed.baselineAdmission().AcknowledgedAt
		seen[name+"/"+seed.GitRevision] = true
		for replica, probes := range record.Baseline {
			if len(probes) != 4 {
				return errors.New("mutation baseline requires each tier on both APIs")
			}
			probe, found := probes[fmt.Sprintf("%s-mutation-baseline-%d-%d", plan.Owner, index, replica)]
			if !found || probe.Subject != record.Subject || probe.Namespace != name || probe.Action != "product.list" ||
				probe.Allowed || !probe.ObservedAt.After(clocks[name]) || !probe.ObservedAt.Before(record.ScheduledAt) {
				return errors.New("mutation baseline differs from independently authored disjoint denial")
			}
		}
	}
	checkAdmission := func(admission rbacCapacityMutationAdmission, index int, restoring bool) error {
		expectedTransport := ""
		if !restoring {
			expectedTransport = config.Mutation.transport(admission.Offer)
		}
		if admission.Transport != expectedTransport {
			return errors.New("mutation receipt differs from the declared admission transport")
		}
		name := plan.namespaceName(index)
		seed := seeds[name]
		if admission.Namespace != name || admission.BindingUID != seed.Bindings["capacity-binding-04"] ||
			admission.RoleUID != seed.Roles["capacity-role-04"] || admission.ReferenceGeneration != 1 ||
			admission.Generation != generations[name]+1 || seen[name+"/"+admission.GitRevision] ||
			!regexp.MustCompile(`^main@sha1:[0-9a-f]{40}$`).MatchString(admission.GitRevision) ||
			!admission.AcknowledgedAt.After(admission.DispatchedAt) ||
			admission.AcknowledgedAt.Sub(admission.DispatchedAt) > 5*time.Second ||
			!admission.AcknowledgedAt.After(clocks[name]) {
			return errors.New("mutation journal changed its original tier, immutable reference, generation or ACK")
		}
		family := "mutation"
		phase := admission.Offer
		lastClock := record.CompletedAt
		if restoring {
			family, phase, lastClock = "mutation-restore", int64(index), record.RestoredAt
			if !admission.DispatchedAt.After(window.CompletedAt) || !allowed[name] {
				return errors.New("restoration must change an outstanding grant after the ordinary process stopped")
			}
		}
		for replica, probes := range admission.Probes {
			if len(probes) == 0 || len(probes) > 51 || !restoring && len(probes) != 10 {
				return errors.New("sealed mutation omitted or exceeded its bounded HTTP observations")
			}
			var previous time.Time
			for attempt := range len(probes) {
				probe, found := probes[fmt.Sprintf("%s-%s-%d-%d-%02d", plan.Owner, family, phase, replica, attempt)]
				if !found || probe.Subject != record.Subject || probe.Namespace != name || probe.Action != "product.list" ||
					probe.ObservedAt.Before(admission.AcknowledgedAt) ||
					probe.ObservedAt.Sub(admission.AcknowledgedAt) > 5*time.Second ||
					probe.ObservedAt.Before(previous) || !probe.ObservedAt.Before(lastClock) {
					return errors.New("sealed mutation omitted, fabricated or reordered a probe or its original clock")
				}
				if restoring && attempt == len(probes)-1 && probe.Allowed {
					return errors.New("restoration did not converge to the original filler denial")
				}
				previous = probe.ObservedAt
			}
		}
		generations[name], clocks[name] = admission.Generation, admission.AcknowledgedAt
		seen[name+"/"+admission.GitRevision] = true
		return nil
	}
	var grants int64
	for offer, admission := range record.Admissions {
		dueOffset := time.Duration(int64(offer) * int64(schedule.Period) / schedule.Rate)
		dispatchOffset := admission.DispatchedAt.Sub(record.ScheduledAt)
		if admission.DispatchedAfter != nil {
			dispatchOffset = *admission.DispatchedAfter
		}
		if admission.Offer != int64(offer) || dispatchOffset < dueOffset ||
			dispatchOffset-dueOffset > 1210*time.Millisecond || !admission.AcknowledgedAt.Before(record.CompletedAt) {
			return errors.New("mutation journal omitted or rescheduled an original global admission")
		}
		index := 1 + offer%4
		if err := checkAdmission(admission, index, false); err != nil {
			return err
		}
		allowed[admission.Namespace] = offer/4%2 == 0
		if allowed[admission.Namespace] {
			grants++
		}
	}
	if record.Accounting.Allowed != grants || record.Accounting.Denied != offered-grants {
		return errors.New("mutation accounting differs from the independently authored four-tier cycles")
	}
	restoration := 0
	for index := 1; index <= 4; index++ {
		name := plan.namespaceName(index)
		if !allowed[name] {
			continue
		}
		if restoration >= len(record.Restorations) || record.Restorations[restoration].Offer != int64(index) {
			return errors.New("mutation journal omitted an outstanding tier restoration")
		}
		if err := checkAdmission(record.Restorations[restoration], index, true); err != nil {
			return err
		}
		allowed[name] = false
		restoration++
	}
	if restoration != len(record.Restorations) {
		return errors.New("mutation journal fabricated an unneeded restoration")
	}
	return nil
}

func rbacCapacityMutationHistory(t *testing.T, plan rbacCapacitySeedPlan, count int,
	config *rbacCapacityK6DiagnosticConfig, seeds map[string]rbacCapacitySeedReceipt,
	record rbacCapacityMutationReceipt, window rbacCapacityK6ProcessWindow,
) []rbacCapacityAuthoredAdmission {
	t.Helper()
	require.NoError(t, checkRBACCapacityMutationReceipt(plan, count, config, seeds, record, window))
	history := make([]rbacCapacityAuthoredAdmission, 0, 4+len(record.Admissions)+len(record.Restorations))
	for index := 1; index <= 4; index++ {
		namespace := plan.namespace(index)
		seed := seeds[namespace.Name]
		history = append(history, rbacCapacityAuthoredAdmission{
			Namespace: namespace, Receipt: seed, AcknowledgedAt: seed.baselineAdmission().AcknowledgedAt,
			Cases:       map[string]bool{record.Subject + "/product.list": false},
			Generations: plan.mutationGenerations(seed.baselineGeneration()),
			References:  map[string]int64{"RoleBinding/capacity-binding-04": 1},
		})
		for _, admissions := range [][]rbacCapacityMutationAdmission{record.Admissions, record.Restorations} {
			for _, admission := range admissions {
				if admission.Namespace != namespace.Name {
					continue
				}
				allowed := admission.DispatchedAt.Before(window.CompletedAt) && admission.Offer/4%2 == 0
				authored := plan.mutationNamespace(namespace, record.Subject, allowed)
				admitted := seed
				admitted.GitRevision = admission.GitRevision
				history = append(history, rbacCapacityAuthoredAdmission{
					Namespace: authored, Receipt: admitted, AcknowledgedAt: admission.AcknowledgedAt,
					Cases:       map[string]bool{record.Subject + "/product.list": allowed},
					Generations: plan.mutationGenerations(admission.Generation),
					References:  map[string]int64{"RoleBinding/capacity-binding-04": 1},
				})
			}
		}
	}
	return history
}

type rbacCapacityMutationRevision struct {
	Namespace, GitRevision string
}

type rbacCapacityMutationClock struct {
	AcknowledgedAt, SupersededAt time.Time
	Mutation                     bool
}

type rbacCapacityMutationTraceIndex map[rbacCapacityMutationRevision]rbacCapacityMutationClock

func newRBACCapacityMutationTraceIndex(history []rbacCapacityAuthoredAdmission) (rbacCapacityMutationTraceIndex, error) {
	index := make(rbacCapacityMutationTraceIndex, len(history))
	latest := make(map[string]rbacCapacityMutationRevision)
	for _, admission := range history {
		key := rbacCapacityMutationRevision{admission.Namespace.Name, admission.Receipt.GitRevision}
		if key.Namespace == "" || key.GitRevision == "" || admission.AcknowledgedAt.IsZero() {
			return nil, errors.New("mutation history lacks its original tier, revision or acknowledgement")
		}
		if _, duplicate := index[key]; duplicate {
			return nil, errors.New("mutation history reinterprets an original tier revision")
		}
		previous, mutation := latest[key.Namespace]
		if mutation {
			clock := index[previous]
			if !admission.AcknowledgedAt.After(clock.AcknowledgedAt) {
				return nil, errors.New("mutation history reordered an original tier acknowledgement")
			}
			clock.SupersededAt = admission.AcknowledgedAt
			index[previous] = clock
		}
		index[key] = rbacCapacityMutationClock{AcknowledgedAt: admission.AcknowledgedAt, Mutation: mutation}
		latest[key.Namespace] = key
	}
	return index, nil
}

func (index rbacCapacityMutationTraceIndex) check(namespace, revision string, at time.Time) (bool, error) {
	clock, found := index[rbacCapacityMutationRevision{namespace, revision}]
	if !found {
		return false, nil
	}
	if at.Before(clock.AcknowledgedAt) {
		return clock.Mutation, errors.New("ordinary decision uses a mutation before its original successful-push acknowledgement")
	}
	if !clock.SupersededAt.IsZero() && at.After(clock.SupersededAt.Add(5*time.Second)) {
		return clock.Mutation, errors.New("ordinary decision retained a superseded mutation source beyond its original bound")
	}
	return clock.Mutation, nil
}

func readRBACCapacityMutation(t *testing.T, plan rbacCapacitySeedPlan, count int,
	config *rbacCapacityK6DiagnosticConfig, directory string, seeds map[string]rbacCapacitySeedReceipt,
	window rbacCapacityK6ProcessWindow,
) (rbacCapacityMutationReceipt, []rbacCapacityAuthoredAdmission) {
	t.Helper()
	if config.Mutation == nil {
		return rbacCapacityMutationReceipt{}, nil
	}
	file, err := openRBACCapacityPrivateFixture(filepath.Join(directory, "mutation-companion.json"), 16<<20)
	require.NoError(t, err)
	defer file.Close()
	var record rbacCapacityMutationReceipt
	decoder := json.NewDecoder(io.LimitReader(file, 16<<20+1))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&record))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	return record, rbacCapacityMutationHistory(t, plan, count, config, seeds, record, window)
}

func commitRBACCapacityMutation(command func(...string) ([]byte, error), paths ...string) (string, error) {
	path := "rbac/rolebindings/capacity-binding-04.yaml"
	if len(paths) > 1 {
		return "", errors.New("a mutation may change only its one declared canonical manifest")
	}
	if len(paths) == 1 {
		path = paths[0]
	}
	if path != "rbac/roles/capacity-role-04.yaml" && path != "rbac/rolebindings/capacity-binding-04.yaml" {
		return "", errors.New("mutation path differs from its declared owned Role or binding")
	}
	var sha string
	for _, args := range [][]string{
		{"add", path},
		{"commit", "--no-gpg-sign", "-m", "Exercise bounded concurrent RBAC admission"},
		{"rev-parse", "HEAD"}, {"push", "origin", "HEAD:refs/heads/main"},
	} {
		output, err := command(args...)
		if err != nil {
			return "", err
		}
		if args[0] == "rev-parse" {
			sha = strings.TrimSpace(string(output))
			if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(sha) {
				return "", errors.New("native mutation commit lacks its exact canonical object identity")
			}
		}
	}
	return "main@sha1:" + sha, nil
}

func finishRBACCapacityMutation(processErr error, restore func() error) error {
	if restore == nil {
		return processErr
	}
	return errors.Join(processErr, restore())
}

type rbacCapacityMutationCleanup struct {
	once    sync.Once
	timeout time.Duration
	stop    func(context.Context) error
	restore func(context.Context) error
	err     error
}

func (cleanup *rbacCapacityMutationCleanup) run(parent context.Context) error {
	cleanup.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanup.timeout)
		defer cancel()
		if cleanup.stop != nil {
			cleanup.err = cleanup.stop(ctx)
		}
		if cleanup.err == nil && cleanup.restore != nil {
			cleanup.err = cleanup.restore(ctx)
		}
	})
	return cleanup.err
}

func (cleanup *rbacCapacityMutationCleanup) guard(parent context.Context, failed func(error)) func() {
	return func() {
		if err := cleanup.run(parent); err != nil {
			failed(err)
		}
	}
}

func runRBACCapacityGuardedK6(parent context.Context, cleanup *rbacCapacityMutationCleanup, failed func(error), run func()) {
	defer cleanup.guard(parent, failed)()
	run()
}

func runRBACCapacityCleanupAssertions(run func()) error {
	done := make(chan error, 1)
	go func() {
		err := errors.New("owned bounded cleanup assertions failed; retain private test log and exact recovery pins")
		defer func() { done <- err }()
		run()
		err = nil
	}()
	return <-done
}

func rbacCapacityMutationRecoveryPins(record rbacCapacityMutationReceipt, seeds map[string]rbacCapacitySeedReceipt) []rbacCapacityMutationRecoveryPin {
	latest := make(map[string]rbacCapacityMutationRecoveryPin)
	for _, admission := range record.Admissions {
		if admission.Offer/4%2 == 0 {
			latest[admission.Namespace] = rbacCapacityMutationRecoveryPin{
				seeds[admission.Namespace].NamespaceNodeUID, "grant-last-acknowledged", admission}
		} else {
			delete(latest, admission.Namespace)
		}
	}
	if record.Pending != nil {
		admission := *record.Pending
		state := "single-attempt-admission-unacknowledged"
		if !admission.AcknowledgedAt.IsZero() {
			state = "admission-acknowledged-verification-unproven"
			if admission.GitRevision == "" {
				state = "admission-acknowledged-reference-unverified"
			}
		}
		latest[admission.Namespace] = rbacCapacityMutationRecoveryPin{
			seeds[admission.Namespace].NamespaceNodeUID, state, admission}
	}
	for _, admission := range record.Restorations {
		latest[admission.Namespace] = rbacCapacityMutationRecoveryPin{
			seeds[admission.Namespace].NamespaceNodeUID, "restoration-acknowledged-verification-unproven", admission}
	}
	names := make([]string, 0, len(latest))
	for name := range latest {
		names = append(names, name)
	}
	sort.Strings(names)
	pins := make([]rbacCapacityMutationRecoveryPin, 0, len(names))
	for _, name := range names {
		pins = append(pins, latest[name])
	}
	return pins
}

func runRBACCapacityMutationSchedule(ctx context.Context, config rbacCapacityScheduleConfig,
	admit func(context.Context, int64) (string, func(context.Context) error, error),
) (rbacCapacityAccounting, []rbacCapacityMutationFailure, error) {
	if config.Workers != 1 || admit == nil {
		return rbacCapacityAccounting{}, nil, errors.New("bounded rotating mutations require one serialized writer")
	}
	ctx, cancel := context.WithTimeout(ctx, config.Duration+config.Drain)
	defer cancel()
	var workers sync.WaitGroup
	slots := make(chan struct{}, 4)
	failures := make(chan rbacCapacityMutationFailure, 5)
	var next int64
	accounting, scheduleErr := runRBACCapacitySchedule(ctx, config, func(workCtx context.Context, offer int64) string {
		if workCtx.Err() != nil {
			return "unavailable"
		}
		if offer != next {
			failures <- rbacCapacityMutationFailure{Offer: offer, Kind: "original-schedule-gap", ObservedAt: time.Now()}
			cancel()
			return "unavailable"
		}
		select {
		case slots <- struct{}{}:
		case <-workCtx.Done():
			failures <- rbacCapacityMutationFailure{Offer: offer, Kind: "bounded-observer-backpressure", ObservedAt: time.Now()}
			cancel()
			return "unavailable"
		}
		if workCtx.Err() != nil {
			<-slots
			return "unavailable"
		}
		outcome, observe, err := admit(workCtx, offer)
		if err != nil || observe == nil {
			<-slots
			failure := rbacCapacityMutationFailure{Offer: offer, Kind: "single-attempt-admission-failed", ObservedAt: time.Now()}
			errors.As(err, &failure.GraphQL)
			failures <- failure
			cancel()
			return "unavailable"
		}
		next++
		workers.Go(func() {
			defer func() { <-slots }()
			// The scheduler cancels workCtx at its load boundary, not this
			// original bounded observer context.
			if observe(ctx) != nil {
				failures <- rbacCapacityMutationFailure{Offer: offer, Kind: "bounded-http-observation-failed", ObservedAt: time.Now()}
				cancel()
			}
		})
		return outcome
	})
	workers.Wait()
	close(failures)
	var recorded []rbacCapacityMutationFailure
	for failure := range failures {
		recorded = append(recorded, failure)
	}
	if len(recorded) != 0 {
		return accounting, recorded, errors.Join(scheduleErr, errors.New("bounded mutation admission or observation failed"))
	}
	return accounting, recorded, errors.Join(scheduleErr, ctx.Err())
}

func observeRBACCapacityMutationAdmission(t *testing.T, ctx context.Context, fixture rbacNativeFixture,
	client *http.Client, token, subject, family string, admission rbacCapacityMutationAdmission, restoring bool,
) error {
	t.Helper()
	type observation struct {
		replica int
		id      string
		probe   rbacCapacityProbeExpectation
	}
	observations := make(chan observation, 102)
	err := observeRBACCapacityMutationPhase(ctx, admission.AcknowledgedAt, restoring, !restoring && admission.Offer/4%2 == 0,
		func(ctx context.Context, replica, attempt int) string {
			request := rbacCapacityRequest{ID: fmt.Sprintf("%s-%d-%d-%02d", family, admission.Offer, replica, attempt),
				Subject: subject, Namespace: admission.Namespace, Action: "product.list"}
			outcome := observeRBACCapacityRequest(ctx, client, fixture.apis[replica], token, request)
			if outcome == "allow" || outcome == "deny" {
				observations <- observation{replica, request.ID, rbacCapacityProbeExpectation{
					Subject: subject, Namespace: admission.Namespace, Action: request.Action,
					Allowed: outcome == "allow", ObservedAt: time.Now(),
				}}
			}
			return outcome
		})
	close(observations)
	for observation := range observations {
		admission.Probes[observation.replica][observation.id] = observation.probe
	}
	return err
}

func submitRBACCapacityGraphQLMutation(ctx context.Context, client *http.Client, api, token, id, kind string,
	input map[string]any, uid string, generation int64,
) (time.Time, error) {
	if kind != "Role" && kind != "RoleBinding" {
		return time.Time{}, errors.New("mixed mutation requires a declared namespaced Role or binding")
	}
	field := rbacNativeField(kind)
	query := fmt.Sprintf("mutation($input:Update%sInput!){update%s(input:$input){%s{metadata{uid generation}}}}", kind, kind, field)
	content, err := json.Marshal(map[string]any{"query": query, "variables": map[string]any{"input": input}})
	if err != nil {
		return time.Time{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/admin/graphql", bytes.NewReader(content))
	if err != nil {
		return time.Time{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-Request-ID", id)
	response, err := client.Do(request)
	if err != nil {
		return time.Time{}, errors.New("single GraphQL mutation transport failed; do not retry an uncertain admission")
	}
	defer response.Body.Close()
	content, err = io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	acknowledgedAt := time.Now()
	if err != nil || len(content) > 1<<20 {
		return time.Time{}, errors.New("GraphQL mutation acknowledgement is unreadable or oversized")
	}
	var result struct {
		Data   map[string]map[string]*rbacNativeResource
		Errors []json.RawMessage
	}
	if response.StatusCode != http.StatusOK {
		return time.Time{}, &rbacCapacityGraphQLAdmissionError{HTTPStatus: response.StatusCode, Code: "HTTP_STATUS"}
	}
	if json.Unmarshal(content, &result) != nil {
		return time.Time{}, &rbacCapacityGraphQLAdmissionError{HTTPStatus: response.StatusCode, Code: "INVALID_JSON"}
	}
	if len(result.Errors) != 0 {
		code := ""
		for _, raw := range result.Errors {
			var reported struct {
				Extensions struct{ Code string }
			}
			classified := "UNCLASSIFIED"
			if json.Unmarshal(raw, &reported) == nil {
				switch reported.Extensions.Code {
				case "ADMISSION_REJECTED", "ALREADY_EXISTS", "AUTHORIZATION_UNAVAILABLE", "BAD_USER_INPUT",
					"CONFLICT", "FAILED_PRECONDITION", "FORBIDDEN", "INTERNAL_SERVER_ERROR", "NOT_FOUND",
					"UNAUTHENTICATED", "UNAUTHORIZED", "UNAVAILABLE":
					classified = reported.Extensions.Code
				}
			}
			if code != "" && code != classified {
				code = "MULTIPLE"
				break
			}
			code = classified
		}
		return time.Time{}, &rbacCapacityGraphQLAdmissionError{HTTPStatus: response.StatusCode, Code: code}
	}
	resource := result.Data["update"+kind][field]
	if resource == nil || resource.Metadata.UID != uid || resource.Metadata.Generation != generation {
		return acknowledgedAt, errors.New("GraphQL mutation acknowledgement changed its pinned identity or generation")
	}
	return acknowledgedAt, nil
}

func checkRBACCapacityOrdinaryWorkload(logPath string, processDone <-chan struct{}) error {
	select {
	case <-processDone:
		return errors.New("ordinary k6 exited; refuse another policy admission")
	default:
	}
	reader, err := openRBACCapacityPrivateFixture(logPath, 1<<20)
	if err != nil {
		return errors.New("ordinary k6 log is unavailable or exceeds its bounded health check")
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return errors.New("ordinary k6 log cannot be checked within its bound")
	}
	for _, failure := range []string{"rbac_failed_response", "Insufficient VUs", "level=error"} {
		if bytes.Contains(content, []byte(failure)) {
			return errors.New("ordinary k6 already reports failed responses or offers; refuse another policy admission")
		}
	}
	return nil
}

func checkRBACCapacityOrdinaryStart(reader io.Reader, owner string, replica int, started time.Time, now func() time.Time,
	sample rbacCapacityK6Case, models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource,
) (bool, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 128<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	id := fmt.Sprintf("%s-ordinary-%d-0", owner, replica)
	for scanner.Scan() {
		var decision struct {
			RequestID string `json:"request_id"`
			Action    string
			At        float64 `json:"ts"`
		}
		if json.Unmarshal(scanner.Bytes(), &decision) != nil || decision.RequestID != id || decision.Action != sample.Action {
			continue
		}
		if math.IsNaN(decision.At) || math.IsInf(decision.At, 0) || decision.At <= 0 || decision.At >= float64(math.MaxInt64) {
			return false, errors.New("ordinary startup evidence lacks a valid decision clock")
		}
		at := time.Unix(int64(decision.At), int64((decision.At-float64(int64(decision.At)))*float64(time.Second)))
		if at.Before(started) || at.After(now()) {
			return false, errors.New("ordinary startup decision is outside the live k6 window")
		}
		expected := map[string]rbacCapacityProbeExpectation{id: {
			Subject: sample.Subject, Namespace: sample.Namespace, Action: sample.Action, Allowed: sample.Expected == "allow",
		}}
		err := checkRBACCapacityObservedTraces(bytes.NewReader(scanner.Bytes()), expected, models,
			func(vector rbacCapacityPolicyVector, _ string, _ bool) error {
				for _, revision := range []rbacCapacityPolicyRevision{vector.Cluster, vector.Namespace} {
					if models[revision].ObservedAt.IsZero() || models[revision].ObservedAt.After(at) {
						return errors.New("ordinary startup decision predates its captured source")
					}
				}
				return nil
			})
		return err == nil, err
	}
	return false, scanner.Err()
}

func waitRBACCapacityOrdinaryStart(t *testing.T, ctx context.Context, owner string, started time.Time,
	paths []string, sample rbacCapacityK6Case, models [2]map[rbacCapacityPolicyRevision]rbacCapacityProbeSource,
	processDone <-chan struct{}, logPath string,
) {
	t.Helper()
	var confirmed [2]bool
	for {
		require.NoError(t, ctx.Err(), "both APIs must produce actual ordinary decisions before root writes")
		require.NoError(t, checkRBACCapacityOrdinaryWorkload(logPath, processDone))
		for replica := range confirmed {
			if confirmed[replica] {
				continue
			}
			reader, err := openRBACCapacityPrivateFixture(paths[replica], 8<<30)
			require.NoError(t, err)
			confirmed[replica], err = checkRBACCapacityOrdinaryStart(reader, owner, replica, started, time.Now, sample, models[replica])
			closeErr := reader.Close()
			require.NoError(t, err)
			require.NoError(t, closeErr)
		}
		require.NoError(t, ctx.Err(), "ordinary startup evidence must be verified within the original deadline")
		if confirmed[0] && confirmed[1] {
			return
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-processDone:
			timer.Stop()
			t.Fatal("ordinary k6 exited before both APIs confirmed ordinary decisions")
		case <-timer.C:
		}
	}
}

func readRBACCapacityMutationRevision(command func(...string) ([]byte, error)) (string, error) {
	output, err := command("ls-remote", "--exit-code", "origin", "refs/heads/main")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[1] != "refs/heads/main" ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(fields[0]) {
		return "", errors.New("canonical reference read omitted its immutable main revision")
	}
	return "main@sha1:" + fields[0], nil
}

func fetchRBACCapacityMutationRevision(command func(...string) ([]byte, error), revision string) error {
	commit, canonical := strings.CutPrefix(revision, "main@sha1:")
	if !canonical || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit) {
		return errors.New("working-clone synchronization requires an exact admitted canonical revision")
	}
	for _, args := range [][]string{{"fetch", "origin", "main"}, {"rev-parse", "FETCH_HEAD"}} {
		output, err := command(args...)
		if err != nil {
			return err
		}
		if args[0] == "rev-parse" && strings.TrimSpace(string(output)) != commit {
			return errors.New("working-clone synchronization refuses an unacknowledged canonical revision")
		}
	}
	return nil
}

func readRBACCapacityAcknowledgedRevision(ctx context.Context, acknowledgedAt time.Time,
	read func(context.Context) (string, error),
) (string, int, error) {
	if acknowledgedAt.IsZero() || read == nil {
		return "", 0, errors.New("canonical reference reads require the original successful admission clock")
	}
	ctx, cancel := context.WithDeadline(ctx, acknowledgedAt.Add(5*time.Second))
	defer cancel()
	var previous error
	for attempts := 0; ; {
		if err := ctx.Err(); err != nil {
			return "", attempts, errors.Join(err, previous)
		}
		attempts++
		revision, err := read(ctx)
		if ctx.Err() != nil {
			return "", attempts, errors.Join(ctx.Err(), err)
		}
		if err == nil {
			commit, found := strings.CutPrefix(revision, "main@sha1:")
			decoded, decodeErr := hex.DecodeString(commit)
			if !found || decodeErr != nil || len(decoded) != 20 || hex.EncodeToString(decoded) != commit {
				return "", attempts, errors.New("acknowledged reference read returned an invalid canonical revision")
			}
			return revision, attempts, nil
		}
		previous = err
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", attempts, errors.Join(ctx.Err(), previous)
		case <-timer.C:
		}
	}
}

func waitRBACCapacityMutationStart(ctx context.Context, processDone <-chan struct{}, notBefore time.Time,
	now func() time.Time,
) error {
	for {
		select {
		case <-processDone:
			return errors.New("ordinary k6 exited before the mutation companion started")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		current := now()
		// Honor both the live monotonic deadline and its serialized wall-clock instant.
		delay := max(notBefore.Sub(current), notBefore.UTC().Sub(current.UTC()))
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-processDone:
			timer.Stop()
			return errors.New("ordinary k6 exited before the mutation companion started")
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func runRBACCapacityMutation(t *testing.T, fixture rbacNativeFixture, repositories map[string]rbacNativeGit,
	plan rbacCapacitySeedPlan, config *rbacCapacityK6DiagnosticConfig, token string,
	seeds map[string]rbacCapacitySeedReceipt, processDone <-chan struct{}, logPath string, record *rbacCapacityMutationReceipt,
	notBefore ...time.Time,
) {
	t.Helper()
	subject := config.Mutation.Subject
	record.Owner, record.Subject = plan.Owner, subject
	record.Baseline = [2]map[string]rbacCapacityProbeExpectation{{}, {}}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for index := 1; index <= 4; index++ {
		for replica, api := range fixture.apis {
			request := rbacCapacityRequest{ID: fmt.Sprintf("%s-mutation-baseline-%d-%d", plan.Owner, index, replica),
				Subject: subject, Namespace: plan.namespaceName(index), Action: "product.list", Expected: "deny"}
			observation := inspectRBACCapacityRequest(t.Context(), client, api, token, request)
			require.Equal(t, "deny", observation.Outcome,
				"mutation baseline failed (replica=%d index=%d HTTPStatus=%d Code=%s)",
				replica, index, observation.HTTPStatus, observation.Code)
			record.Baseline[replica][request.ID] = rbacCapacityProbeExpectation{
				Subject: subject, Namespace: request.Namespace, Action: request.Action, ObservedAt: time.Now(),
			}
		}
	}
	// Replay also requires actual ordinary decisions before and after load ACKs.
	startNotBefore := time.Now().Add(time.Second)
	if len(notBefore) != 0 {
		startNotBefore = notBefore[0]
	}
	require.NoError(t, waitRBACCapacityMutationStart(t.Context(), processDone, startNotBefore, time.Now),
		"mutation companion startup must remain inside the live ordinary workload")
	require.NoError(t, checkRBACCapacityOrdinaryWorkload(logPath, processDone),
		"ordinary workload must not already be failing when the first policy write starts")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		select {
		case <-processDone:
			cancel()
		case <-ctx.Done():
		}
	}()
	record.ScheduledAt = time.Now()
	unsynchronized := make(map[string]string, 4)
	var err error
	record.Accounting, record.Failures, err = runRBACCapacityMutationSchedule(ctx, config.Mutation.Schedule,
		func(ctx context.Context, offer int64) (string, func(context.Context) error, error) {
			if err := checkRBACCapacityOrdinaryWorkload(logPath, processDone); err != nil {
				return "", nil, err
			}
			authored := plan.namespace(1 + int(offer)%4)
			seed := seeds[authored.Name]
			admission := rbacCapacityMutationAdmission{Offer: offer, Namespace: authored.Name, DispatchedAt: time.Now(),
				BindingUID: seed.Bindings["capacity-binding-04"], RoleUID: seed.Roles["capacity-role-04"],
				ReferenceGeneration: 1, Generation: seed.baselineGeneration() + offer/4 + 1, Transport: config.Mutation.transport(offer),
				Probes: [2]map[string]rbacCapacityProbeExpectation{{}, {}}}
			dispatchOffset := admission.DispatchedAt.Sub(record.ScheduledAt)
			admission.DispatchedAfter = &dispatchOffset
			allowed := offer/4%2 == 0
			authored = plan.mutationNamespace(authored, subject, allowed)
			kind, name, target := plan.mutationTarget()
			path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
			resource := authored.Resources[target]
			git := repositories[authored.Name]
			git.fixture.cleanupContext = ctx
			record.Pending = &admission
			if admission.Transport == "graphql" {
				api := fixture.apis[int(offer/4)%len(fixture.apis)]
				scoped := fixture
				scoped.namespace = authored.Name
				current, response := lookupRBACCapacityMutation(t, scoped, api, kind, name)
				uid := admission.BindingUID
				if kind == "Role" {
					uid = admission.RoleUID
				}
				if response.Status != http.StatusOK || len(response.Errors) != 0 || current.Metadata.UID != uid ||
					current.Metadata.Generation != admission.Generation-1 || current.Metadata.ResourceVersion == "" {
					return "", nil, errors.New("GraphQL mutation CAS read differs from its owned original identity or generation")
				}
				input := maps.Clone(resource)
				metadata := maps.Clone(resource["metadata"].(map[string]any))
				metadata["resourceVersion"] = current.Metadata.ResourceVersion
				input["metadata"], input["spec"] = metadata, rbacCapacitySeedSpec(t, resource["spec"], true)
				var err error
				admission.AcknowledgedAt, err = submitRBACCapacityGraphQLMutation(ctx, client, api, fixture.token,
					fmt.Sprintf("%s-mutation-admission-%d", plan.Owner, offer), kind, input, uid, admission.Generation)
				if err != nil {
					return "", nil, err
				}
				admission.GitRevision, admission.ReferenceReads, err = readRBACCapacityAcknowledgedRevision(ctx,
					admission.AcknowledgedAt, func(readCtx context.Context) (string, error) {
						reader := git
						reader.fixture.cleanupContext = readCtx
						return readRBACCapacityMutationRevision(func(args ...string) ([]byte, error) {
							return reader.command(t, args...)
						})
					})
				if err != nil {
					return "", nil, err
				}
				unsynchronized[authored.Name] = admission.GitRevision
			} else {
				if previous := unsynchronized[authored.Name]; previous != "" {
					if err := fetchRBACCapacityMutationRevision(func(args ...string) ([]byte, error) {
						return git.command(t, args...)
					}, previous); err != nil {
						return "", nil, err
					}
					if _, err := git.command(t, "merge", "--ff-only", strings.TrimPrefix(previous, "main@sha1:")); err != nil {
						return "", nil, err
					}
					delete(unsynchronized, authored.Name)
				}
				git.write(t, path, resource)
				revision, pushErr := commitRBACCapacityMutation(func(args ...string) ([]byte, error) {
					output, err := git.command(t, args...)
					if err == nil && args[0] == "rev-parse" {
						admission.GitRevision = "main@sha1:" + strings.TrimSpace(string(output))
					}
					return output, err
				}, path)
				if pushErr != nil {
					return "", nil, pushErr
				}
				admission.GitRevision, admission.AcknowledgedAt = revision, time.Now()
			}
			record.Admissions = append(record.Admissions, admission)
			record.Pending = nil
			observe := func(ctx context.Context) error {
				return observeRBACCapacityMutationAdmission(t, ctx, fixture, client, token, subject, plan.Owner+"-mutation", admission, false)
			}
			if allowed {
				return "allow", observe, nil
			}
			return "deny", observe, nil
		})
	record.CompletedAt = time.Now()
	require.NoError(t, err, "bounded companion failed; preserve original admissions and failure receipts")
	require.NoError(t, ctx.Err())
	offered := rbacCapacityScheduledOffers(config.Mutation.Schedule.Duration, 50, time.Minute)
	require.NoError(t, record.Accounting.validate(offered, false))
	require.Len(t, record.Admissions, int(offered))
}

func lookupRBACCapacityMutation(t *testing.T, fixture rbacNativeFixture, api, kind, name string) (rbacNativeResource, gqlResponse) {
	t.Helper()
	if kind != "Role" && kind != "ClusterRole" {
		return fixture.lookup(t, api, kind, name)
	}
	selection := strings.Replace(rbacNativeSelection(kind), "rules { resources verbs }",
		"rules { resources verbs resourceNames when { token attribute { attr op value values } } }", 1)
	selector := map[string]any{"name": name}
	if kind == "Role" {
		selector = map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": name}}
	}
	response := fixture.query(t, api,
		fmt.Sprintf("query($by:%sBy!){result:%s(by:$by){%s}}", kind, rbacNativeField(kind), selection),
		map[string]any{"by": selector})
	var result struct{ Result rbacNativeResource }
	if len(response.Data) != 0 {
		require.NoError(t, json.Unmarshal(response.Data, &result))
	}
	return result.Result, response
}

type rbacCapacityRootReadback struct {
	Resource rbacNativeResource
	Errors   []json.RawMessage
}

func lookupRBACCapacityRootBatch(t *testing.T, fixture rbacNativeFixture, api string,
	resources []rbacCapacityAuthoredRootResource,
) []rbacCapacityRootReadback {
	t.Helper()
	require.NotEmpty(t, resources)
	require.LessOrEqual(t, len(resources), 4, "root readbacks permit at most four concurrent field resolvers")
	var declarations, fields []string
	variables := make(map[string]any, len(resources))
	aliases := make(map[string]int, len(resources))
	for index, resource := range resources {
		kind := resource.Manifest["kind"].(string)
		require.Contains(t, []string{"ClusterRole", "ClusterRoleBinding"}, kind)
		name := resource.Manifest["metadata"].(map[string]any)["name"].(string)
		alias, variable := fmt.Sprintf("r%d", index), fmt.Sprintf("by%d", index)
		selection := rbacNativeSelection(kind)
		if kind == "ClusterRole" {
			selection = strings.Replace(selection, "rules { resources verbs }",
				"rules { resources verbs resourceNames when { token attribute { attr op value values } } }", 1)
		}
		declarations = append(declarations, "$"+variable+":"+kind+"By!")
		fields = append(fields, alias+":"+rbacNativeField(kind)+"(by:$"+variable+"){"+selection+"}")
		variables[variable] = map[string]any{"name": name}
		aliases[alias] = index
	}
	response := fixture.query(t, api, "query("+strings.Join(declarations, ",")+"){"+strings.Join(fields, " ")+"}", variables)
	var data map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(response.Data, &data))
	require.Len(t, data, len(resources), "every requested root readback must have an explicit result")
	result := make([]rbacCapacityRootReadback, len(resources))
	for alias, index := range aliases {
		raw, found := data[alias]
		require.True(t, found, "a root readback result is missing")
		require.NoError(t, json.Unmarshal(raw, &result[index].Resource))
	}
	for _, raw := range response.Errors {
		var failure struct{ Path []any }
		require.NoError(t, json.Unmarshal(raw, &failure))
		require.NotEmpty(t, failure.Path, "unscoped GraphQL errors cannot prove an individual root readback")
		alias, ok := failure.Path[0].(string)
		require.True(t, ok)
		index, found := aliases[alias]
		require.True(t, found, "GraphQL error refers to an unrequested root readback")
		result[index].Errors = append(result[index].Errors, raw)
	}
	return result
}

func prepareRBACCapacityRestoration(t *testing.T, git rbacNativeGit, revision string) rbacNativeGit {
	t.Helper()
	commit, canonical := strings.CutPrefix(revision, "main@sha1:")
	require.True(t, canonical && len(commit) == 40 && strings.ToLower(commit) == commit)
	_, err := hex.DecodeString(commit)
	require.NoError(t, err)
	require.NoError(t, fetchRBACCapacityMutationRevision(func(args ...string) ([]byte, error) {
		return git.command(t, args...)
	}, revision))
	directory, err := os.MkdirTemp(filepath.Dir(git.dir), "mutation-restoration-")
	require.NoError(t, err)
	_, err = git.command(t, "worktree", "add", "--detach", directory, commit)
	require.NoError(t, err, "restoration must start at the exact admitted canonical commit, not an unadmitted local candidate")
	git.dir = directory
	return git
}

func waitRBACCapacityMutationProjection(parent context.Context, bindingUID, roleUID string, generation int64,
	read func(context.Context) (rbacNativeResource, error),
) (rbacNativeResource, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	for {
		if err := ctx.Err(); err != nil {
			return rbacNativeResource{}, err
		}
		current, err := read(ctx)
		if err != nil {
			return rbacNativeResource{}, err
		}
		if current.Metadata.UID != bindingUID || current.Metadata.Generation > generation ||
			roleUID != "" && current.Status.Resolved != nil && current.Status.Resolved.RoleUID != roleUID {
			return rbacNativeResource{}, errors.New("owned cleanup refuses a changed binding, role incarnation or newer generation")
		}
		if current.Metadata.Generation == generation && current.Status.ObservedGeneration >= generation &&
			(roleUID == "" || current.Status.Resolved != nil) {
			return current, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return rbacNativeResource{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func restoreRBACCapacityMutation(t *testing.T, fixture rbacNativeFixture, repositories map[string]rbacNativeGit,
	plan rbacCapacitySeedPlan, token string, seeds map[string]rbacCapacitySeedReceipt,
	sourcePaths []string, record *rbacCapacityMutationReceipt,
	cleanupContext ...context.Context,
) {
	t.Helper()
	kind, name, target := plan.mutationTarget()
	path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
	ctx := t.Context()
	if len(cleanupContext) != 0 {
		ctx = cleanupContext[0]
		fixture.cleanupContext = ctx
		for namespace, repository := range repositories {
			repository.fixture.cleanupContext = ctx
			repositories[namespace] = repository
		}
	}
	last := make(map[string]rbacCapacityMutationAdmission, 4)
	for index := 1; index <= 4; index++ {
		namespace := plan.namespaceName(index)
		last[namespace] = rbacCapacityMutationAdmission{Offer: -1, Namespace: namespace,
			Generation: seeds[namespace].baselineGeneration(), GitRevision: seeds[namespace].GitRevision}
	}
	for _, admission := range record.Admissions {
		last[admission.Namespace] = admission
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for index := 1; index <= 4; index++ {
		namespace := plan.namespace(index)
		fixture.namespace = namespace.Name
		seed := seeds[namespace.Name]
		previous := last[namespace.Name]
		allowed := previous.Offer >= 0 && previous.Offer/4%2 == 0
		expected := plan.mutationNamespace(namespace, record.Subject, allowed).Resources[target]
		targetUID, referenceUID := seed.Bindings["capacity-binding-04"], seed.Roles["capacity-role-04"]
		associatedKind, associatedName, associatedTarget := "Role", "capacity-role-04", 4
		associatedUID := seed.Roles["capacity-role-04"]
		if kind == "Role" {
			targetUID, referenceUID = seed.Roles["capacity-role-04"], ""
			associatedKind, associatedName, associatedTarget = "RoleBinding", "capacity-binding-04", 14
			associatedUID = seed.Bindings["capacity-binding-04"]
		}
		for _, api := range fixture.apis {
			response := fixture.query(t, api, `query($name:String!){namespace(by:{name:$name}){metadata{uid labels} spec{tier}}}`,
				map[string]any{"name": namespace.Name})
			require.Empty(t, response.Errors)
			var identity struct {
				Namespace struct {
					Metadata struct {
						UID    string
						Labels map[string]string
					}
					Spec struct{ Tier string }
				}
			}
			require.NoError(t, json.Unmarshal(response.Data, &identity))
			require.Equal(t, seed.NamespaceNodeUID, identity.Namespace.Metadata.UID)
			require.Equal(t, plan.Owner, identity.Namespace.Metadata.Labels[rbacCapacityOwnerLabel])
			require.Equal(t, "ORGANIZATION", identity.Namespace.Spec.Tier)
			// Cleanup has its own bounded read-only wait, not a refreshed admission clock.
			current, err := waitRBACCapacityMutationProjection(ctx, targetUID,
				referenceUID, previous.Generation, func(readCtx context.Context) (rbacNativeResource, error) {
					reader := fixture
					reader.cleanupContext = readCtx
					current, response := lookupRBACCapacityMutation(t, reader, api, kind, name)
					if response.Status != http.StatusOK || len(response.Errors) != 0 {
						return rbacNativeResource{}, errors.New("owned cleanup projection lookup failed")
					}
					return current, nil
				})
			require.NoError(t, err)
			require.Equal(t, rbacCapacitySeedSpec(t, expected["spec"], true), rbacCapacitySeedSpec(t, current.Spec, false))
			associated, response := lookupRBACCapacityMutation(t, fixture, api, associatedKind, associatedName)
			require.Empty(t, response.Errors)
			require.Equal(t, associatedUID, associated.Metadata.UID)
			require.EqualValues(t, 1, associated.Metadata.Generation)
			require.Equal(t, rbacCapacitySeedSpec(t, namespace.Resources[associatedTarget]["spec"], true), rbacCapacitySeedSpec(t, associated.Spec, false))
			binding := current
			if kind == "Role" {
				binding = associated
			}
			require.NotNil(t, binding.Status.Resolved)
			require.Equal(t, seed.Roles["capacity-role-04"], binding.Status.Resolved.RoleUID)
		}
		git := repositories[namespace.Name]
		tip := strings.Fields(git.remoteTip(t))
		require.Len(t, tip, 2)
		require.Equal(t, previous.GitRevision, "main@sha1:"+tip[0], "restoration may only advance its exact owned admitted ref")
		if !allowed {
			continue
		}
		admission := rbacCapacityMutationAdmission{
			Offer: int64(index), Namespace: namespace.Name, BindingUID: seed.Bindings["capacity-binding-04"],
			RoleUID: seed.Roles["capacity-role-04"], ReferenceGeneration: 1, Generation: previous.Generation + 1,
			DispatchedAt: time.Now(), Probes: [2]map[string]rbacCapacityProbeExpectation{{}, {}},
		}
		require.NoError(t, ctx.Err(), "expired bounded cleanup must not admit another restoration")
		git = prepareRBACCapacityRestoration(t, git, previous.GitRevision)
		git.write(t, path, namespace.Resources[target])
		revision, err := commitRBACCapacityMutation(func(args ...string) ([]byte, error) { return git.command(t, args...) }, path)
		require.NoError(t, err, "owned post-load restoration is a single forward-only admission")
		admission.GitRevision, admission.AcknowledgedAt = revision, time.Now()
		record.Restorations = append(record.Restorations, admission)
		require.NoError(t, observeRBACCapacityMutationAdmission(t, ctx, fixture, client, token,
			record.Subject, plan.Owner+"-mutation-restore", admission, true))
		admitted := seed
		admitted.GitRevision = revision
		waitRBACCapacityCapturedSources(t, sourcePaths, admission.AcknowledgedAt,
			func(header rbacCapacitySourceHeader, resources []json.RawMessage) bool {
				var origin struct{ Namespace, GitRevision string }
				require.NoError(t, json.Unmarshal(header.Origin, &origin))
				return origin.Namespace == namespace.Name && origin.GitRevision == revision &&
					checkRBACCapacityNamespaceSource(t, header, resources, namespace, admitted,
						plan.mutationGenerations(admission.Generation),
						map[string]int64{"RoleBinding/capacity-binding-04": 1})
			}, ctx)
		last[namespace.Name] = admission
	}
	for index := 1; index <= 4; index++ {
		namespace := plan.namespace(index)
		fixture.namespace = namespace.Name
		seed := seeds[namespace.Name]
		for _, api := range fixture.apis {
			generations := plan.mutationGenerations(last[namespace.Name].Generation)
			for _, resource := range []struct {
				kind, name, uid string
				index           int
			}{
				{"RoleBinding", "capacity-binding-04", seed.Bindings["capacity-binding-04"], 14},
				{"Role", "capacity-role-04", seed.Roles["capacity-role-04"], 4},
			} {
				current, response := lookupRBACCapacityMutation(t, fixture, api, resource.kind, resource.name)
				require.Empty(t, response.Errors)
				require.Equal(t, resource.uid, current.Metadata.UID)
				require.Equal(t, generations[resource.kind+"/"+resource.name], current.Metadata.Generation)
				require.Equal(t, rbacCapacitySeedSpec(t, namespace.Resources[resource.index]["spec"], true), rbacCapacitySeedSpec(t, current.Spec, false))
			}
		}
	}
	record.RestoredAt = time.Now()
	record.Complete = record.Pending == nil && len(record.Failures) == 0
}

func observeRBACCapacityMutationPhase(ctx context.Context, acknowledgedAt time.Time, final, allowed bool,
	observe func(context.Context, int, int) string,
) error {
	if final {
		_, err := observeRBACCapacityPropagation(ctx, acknowledgedAt, false, observe)
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, acknowledgedAt.Add(5*time.Second))
	defer cancel()
	var workers sync.WaitGroup
	var failed atomic.Bool
	for replica := range 2 {
		workers.Go(func() {
			due := acknowledgedAt
			for attempt := range 10 {
				timer := time.NewTimer(max(0, time.Until(due)))
				select {
				case <-ctx.Done():
					timer.Stop()
					failed.Store(true)
					return
				case <-timer.C:
				}
				outcome := observe(ctx, replica, attempt)
				if outcome != "allow" && outcome != "deny" {
					failed.Store(true)
					return
				}
				if attempt < 9 {
					delay := 100 * time.Millisecond
					if (outcome == "allow") != allowed {
						delay = max(delay, time.Until(acknowledgedAt.Add(4500*time.Millisecond))/time.Duration(9-attempt))
					}
					due = time.Now().Add(delay)
				}
			}
		})
	}
	workers.Wait()
	if failed.Load() {
		return errors.New("concurrent mutation observation was unavailable or exceeded its original acknowledgement deadline")
	}
	return nil
}

func rbacCapacityMutationProbes(record rbacCapacityMutationReceipt, replica int) (map[string]rbacCapacityProbeExpectation, error) {
	if replica < 0 || replica >= len(record.Baseline) {
		return nil, errors.New("mutation probe replay requires one declared API replica")
	}
	expected := make(map[string]rbacCapacityProbeExpectation)
	appendProbes := func(probes map[string]rbacCapacityProbeExpectation) error {
		if len(probes) > 16384-len(expected) {
			return errors.New("mutation probes exceed the shared per-API replay bound")
		}
		for id, probe := range probes {
			if _, duplicate := expected[id]; duplicate {
				return errors.New("mutation phases reused an individual probe request ID")
			}
			expected[id] = probe
		}
		return nil
	}
	if err := appendProbes(record.Baseline[replica]); err != nil {
		return nil, err
	}
	for _, admissions := range [][]rbacCapacityMutationAdmission{record.Admissions, record.Restorations} {
		for _, admission := range admissions {
			if err := appendProbes(admission.Probes[replica]); err != nil {
				return nil, err
			}
		}
	}
	return expected, nil
}

func rbacCapacityMutationPropagation(record rbacCapacityMutationReceipt, replica int, revisions map[string]string) ([]time.Duration, error) {
	latencies := make([]time.Duration, 0, len(record.Admissions))
	for _, admission := range record.Admissions {
		var first time.Time
		for id, probe := range admission.Probes[replica] {
			if revisions[id] != admission.GitRevision || probe.Allowed != (admission.Offer/4%2 == 0) {
				continue
			}
			if first.IsZero() || probe.ObservedAt.Before(first) {
				first = probe.ObservedAt
			}
		}
		if first.IsZero() || first.Before(admission.AcknowledgedAt) || first.Sub(admission.AcknowledgedAt) > 5*time.Second {
			return nil, errors.New("mutation propagation lacks a matching source-joined observation at its original admitted revision within five seconds")
		}
		latencies = append(latencies, first.Sub(admission.AcknowledgedAt))
	}
	return latencies, nil
}

func verifyRBACCapacityConcurrentTraces(t *testing.T, decisionPath string,
	history []rbacCapacityAuthoredAdmission, models map[rbacCapacityPolicyRevision]rbacCapacityProbeSource,
	expected map[string]rbacCapacityProbeExpectation, observedRevisions map[string]string,
	readBounds ...int64,
) error {
	t.Helper()
	readBound := int64(8 << 30)
	require.LessOrEqual(t, len(readBounds), 1)
	if len(readBounds) == 1 {
		readBound = readBounds[0]
	}
	require.True(t, readBound > 0 && readBound <= 8<<30)
	require.True(t, len(history) >= 14 && len(history) <= rbacCapacityMutationHistoryLimit)
	indexes := make(map[string]int, len(history))
	for index, admission := range history {
		indexes[admission.Namespace.Name+"/"+admission.Receipt.GitRevision] = index
	}
	captured := make(map[string]bool)
	for revision, model := range models {
		captured[model.Namespace+"/"+revision.GitRevision] = true
	}
	for _, admission := range history {
		if !captured[admission.Namespace.Name+"/"+admission.Receipt.GitRevision] {
			return errors.New("each original tier admission requires its actual independently captured policy source")
		}
	}
	oracles := make(map[rbacCapacityPolicyRevision]*rbacCapacityOracle)
	reader, err := openRBACCapacityPrivateFixture(decisionPath, readBound)
	require.NoError(t, err)
	defer reader.Close()
	return checkRBACCapacityObservedTracesWithBounds(reader, expected, models, 16384, readBound, nil,
		func(vector rbacCapacityPolicyVector, id string, allowed bool, at time.Time) error {
			for _, revision := range []rbacCapacityPolicyRevision{vector.Cluster, vector.Namespace} {
				if models[revision].ObservedAt.IsZero() || models[revision].ObservedAt.After(at) {
					return errors.New("mutation decision predates its independently captured policy source")
				}
			}
			oracle := oracles[vector.Cluster]
			if oracle == nil {
				oracle = newRBACCapacityOracle(5 * time.Second)
				// Original per-tier admissions own supersession, never late aliases.
				for _, admission := range history {
					rawUID, err := base64.StdEncoding.DecodeString(admission.Receipt.NamespaceNodeUID)
					if err != nil {
						return err
					}
					namespaceUID, found := strings.CutPrefix(string(rawUID), "gid://GitStore/Namespace/")
					if !found {
						return errors.New("mutation history lacks its original Namespace identity")
					}
					if err := oracle.admitNamespace(namespaceUID, admission.Receipt.GitRevision, admission.AcknowledgedAt); err != nil {
						return err
					}
				}
				for revision, model := range models {
					if model.Root {
						continue
					}
					index, found := indexes[model.Namespace+"/"+revision.GitRevision]
					if !found {
						continue
					}
					admission := history[index]
					if err := oracle.commit(rbacCapacityPolicyVector{Cluster: vector.Cluster, Namespace: revision},
						history[0].AcknowledgedAt, admission.AcknowledgedAt, admission.Cases); err != nil {
						return err
					}
				}
				oracles[vector.Cluster] = oracle
			}
			probe := expected[id]
			if err := oracle.check(vector, probe.Subject+"/"+probe.Action, allowed, at); err != nil {
				return err
			}
			if observedRevisions != nil {
				observedRevisions[id] = vector.Namespace.GitRevision
			}
			return nil
		})
}

func runRBACCapacityK6Diagnostic(t *testing.T, fixture rbacNativeFixture, plan rbacCapacitySeedPlan, count int,
	config *rbacCapacityK6DiagnosticConfig, tokens map[string]string, directory string,
	receipts map[string]rbacCapacitySeedReceipt, root rbacCapacityRootPolicy, sourcePaths, decisionPaths []string,
) {
	t.Helper()
	require.Len(t, fixture.apis, 2)
	require.Len(t, sourcePaths, 2)
	require.Len(t, decisionPaths, 2)
	require.NoError(t, validateRBACCapacityK6Diagnostic(config))
	offered := int64(config.Duration/time.Second) * int64(config.Rate)
	require.Zero(t, offered%10, "each direct API must receive complete independently declared 60/30/10 cycles")
	mutationGit := make(map[string]rbacNativeGit, 4)
	var mutationToken string
	var mutationRecord rbacCapacityMutationReceipt
	var mutationFile *os.File
	persistMutation := func() {
		if mutationFile != nil {
			require.NoError(t, json.NewEncoder(mutationFile).Encode(mutationRecord))
			require.NoError(t, mutationFile.Sync())
			require.NoError(t, mutationFile.Close())
			mutationFile = nil
		}
	}
	defer persistMutation()
	if config.Mutation != nil {
		require.NoError(t, validateRBACCapacityMutation(plan, count, config, root.AdminSubject))
		content, err := readRBACCapacityPrivateFixture(config.Mutation.TokenFile)
		require.NoError(t, err)
		mutationToken = strings.TrimSpace(string(content))
		require.NotEmpty(t, mutationToken)
		require.NotEqual(t, fixture.token, mutationToken)
		for _, token := range tokens {
			require.NotEqual(t, token, mutationToken)
		}
		mutationFile, err = os.OpenFile(filepath.Join(directory, "mutation-companion.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		for index := 1; index <= 4; index++ {
			fixture.namespace = plan.namespaceName(index)
			for _, api := range fixture.apis {
				for _, resource := range []struct {
					kind, name, uid string
					index           int
				}{
					{"Role", "capacity-role-04", receipts[fixture.namespace].Roles["capacity-role-04"], 4},
					{"RoleBinding", "capacity-binding-04", receipts[fixture.namespace].Bindings["capacity-binding-04"], 14},
				} {
					current, response := lookupRBACCapacityMutation(t, fixture, api, resource.kind, resource.name)
					require.Empty(t, response.Errors)
					require.Equal(t, resource.uid, current.Metadata.UID)
					require.Equal(t, plan.mutationGenerations(receipts[fixture.namespace].baselineGeneration())[resource.kind+"/"+resource.name],
						current.Metadata.Generation)
					require.Equal(t, rbacCapacitySeedSpec(t, plan.namespace(index).Resources[resource.index]["spec"], true),
						rbacCapacitySeedSpec(t, current.Spec, false))
				}
			}
			git := rbacNativeGit{fixture: fixture, dir: filepath.Join(directory, fmt.Sprintf("mutation-work-%d", index))}
			_, err = git.command(t, "clone", "--branch", "main",
				strings.TrimRight(fixture.gitURL, "/")+"/"+fixture.namespace+"/gitstore-system", git.dir)
			require.NoError(t, err, "prepare the exact private canonical clone once before starting ordinary k6")
			require.NoError(t, os.Chmod(git.dir, 0o700))
			tip, err := git.command(t, "rev-parse", "HEAD")
			require.NoError(t, err)
			require.Equal(t, receipts[fixture.namespace].GitRevision, "main@sha1:"+strings.TrimSpace(string(tip)))
			mutationGit[fixture.namespace] = git
		}
	}
	workingSet, err := makeRBACCapacityK6WorkingSet(plan, count, config.GroupMembers, tokens)
	require.NoError(t, err)
	var rootTarget rbacCapacityRequest
	var rootGit *rbacNativeGit
	var startupModels [2]map[rbacCapacityPolicyRevision]rbacCapacityProbeSource
	if config.RootUnderLoad {
		rootTarget, err = rbacCapacityRootUnderLoadTarget(plan, count, config, root.AdminSubject, mutationToken, fixture.token, workingSet)
		require.NoError(t, err)
		prepared := fixture.clone(t, "ClusterRoleBinding")
		rootGit = &prepared
		for replica := range startupModels {
			startupModels[replica] = readRBACCapacityProbeSources(t, sourcePaths[replica], plan, count, receipts, root, time.Now())
		}
	}
	content, err := json.Marshal(workingSet)
	require.NoError(t, err)
	require.LessOrEqual(t, len(content), 16<<20)
	info, err := os.Lstat(directory)
	require.NoError(t, err)
	require.True(t, info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o077 == 0,
		"ordinary principal credentials must remain in the existing private evidence directory")
	workingSetPath, err := filepath.Abs(filepath.Join(directory, "k6-working-set.json"))
	require.NoError(t, err)
	file, err := os.OpenFile(workingSetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	require.NoError(t, writeErr)
	require.NoError(t, closeErr)
	logPath := filepath.Join(directory, "k6-workload.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	defer log.Close()
	summaryPath, err := filepath.Abs(filepath.Join(directory, "k6-summary.json"))
	require.NoError(t, err)
	summaryFile, err := os.OpenFile(summaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, summaryFile.Close())
	ctx, cancel := context.WithTimeout(t.Context(), config.Duration+45*time.Second)
	defer cancel()
	repository, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	profilePath := filepath.Join(strings.TrimSpace(string(repository)), "tests", "capacity", "profiles", "api-readiness.js")
	binary := os.Getenv("K6_BIN")
	if binary == "" {
		binary, err = exec.LookPath("k6")
		require.NoError(t, err, "connected native k6 verification requires the existing k6 runner")
	}
	command := exec.CommandContext(ctx, binary, "run", "--summary-export", summaryPath, profilePath)
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "CAPACITY_") && !strings.HasPrefix(variable, "K6_") {
			command.Env = append(command.Env, variable)
		}
	}
	command.Env = append(command.Env, "CAPACITY_RBAC_WORKLOAD=1",
		"CAPACITY_BASE_URL="+fixture.apis[0], "CAPACITY_API_ENDPOINTS="+strings.Join(fixture.apis, ","),
		"CAPACITY_RBAC_FIXTURE_FILE="+workingSetPath,
		"CAPACITY_DURATION="+strconv.FormatInt(int64(config.Duration/time.Second), 10)+"s",
		"CAPACITY_RATE="+strconv.Itoa(config.Rate),
		"CAPACITY_PREALLOCATED_VUS="+strconv.Itoa(config.PreallocatedVUs),
		"CAPACITY_MAX_VUS="+strconv.Itoa(config.MaxVUs))
	command.Stdout, command.Stderr = log, log
	processDone := make(chan struct{})
	processStarted := false
	var processErr error
	var processCompleted time.Time
	cleanup := &rbacCapacityMutationCleanup{
		timeout: 30 * time.Second,
		stop: func(ctx context.Context) error {
			cancel()
			if !processStarted {
				return nil
			}
			select {
			case <-processDone:
				return nil
			case <-ctx.Done():
				return errors.New("bounded cleanup could not join k6; restoration must not overlap live traffic")
			}
		},
		restore: func(ctx context.Context) error {
			if config.Mutation == nil || len(mutationRecord.Admissions) == 0 && mutationRecord.Pending == nil {
				return nil
			}
			return runRBACCapacityCleanupAssertions(func() {
				restoreRBACCapacityMutation(t, fixture, mutationGit, plan, mutationToken, receipts, sourcePaths, &mutationRecord, ctx)
			})
		},
	}
	cleanupReported := false
	cleanupFailed := func(err error) {
		if cleanupReported {
			return
		}
		cleanupReported = true
		mutationRecord.CleanupError = err.Error()
		mutationRecord.RecoveryPins = rbacCapacityMutationRecoveryPins(mutationRecord, receipts)
		t.Errorf("bounded owned cleanup failed: %v; retain exact private recovery pins", err)
	}
	runRBACCapacityGuardedK6(t.Context(), cleanup, cleanupFailed, func() {
		runtimeReceipt := rbacCapacityRuntimeReceipt{Owner: plan.Owner}
		if config.alpha {
			runtimeReceipt.Before, runtimeReceipt.MetricsSHA256[0] = captureRBACCapacityRuntime(t, fixture, directory, "before")
		}
		started := time.Now()
		require.NoError(t, command.Start())
		processStarted = true
		go func() {
			processErr = command.Wait()
			processCompleted = time.Now()
			close(processDone)
		}()
		var mutationNotBefore []time.Time
		if config.RootUnderLoad {
			preamble, stopPreamble := context.WithDeadline(t.Context(), started.Add(48*time.Second))
			defer stopPreamble()
			go func() {
				select {
				case <-processDone:
					stopPreamble()
				case <-preamble.Done():
				}
			}()
			startup, stopStartup := context.WithDeadline(preamble, started.Add(8*time.Second))
			waitRBACCapacityOrdinaryStart(t, startup, plan.Owner, started, decisionPaths, workingSet.Cases[0],
				startupModels, processDone, logPath)
			stopStartup()
			baseline, err := rbacCapacityRootNamespaceBaseline(plan, receipts, rootTarget.Subject)
			require.NoError(t, err)
			history := []rbacCapacityAuthoredAdmission{baseline}
			rootReceipt := runRBACCapacityRootPropagationDiagnostic(t, fixture, plan, count, config.GroupMembers,
				map[string]string{rootTarget.Subject: mutationToken}, directory, history, sourcePaths, decisionPaths, root,
				rbacCapacityRootDiagnosticControl{
					Target: rootTarget, Context: preamble, Git: rootGit,
					BeforeAdmission: func() error { return checkRBACCapacityOrdinaryWorkload(logPath, processDone) },
					BeforeRestore: func() error {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						return cleanup.stop(ctx)
					},
				})
			require.True(t, rootReceipt.Complete && !rootReceipt.CompletedAt.After(started.Add(48*time.Second)),
				"root completion cannot consume time reserved for the full Namespace write schedule")
			stopPreamble()
			mutationNotBefore = []time.Time{started.Add(48 * time.Second)}
		}
		if config.Mutation != nil {
			runRBACCapacityMutation(t, fixture, mutationGit, plan, config, mutationToken, receipts, processDone, logPath, &mutationRecord, mutationNotBefore...)
		}
		<-processDone
		completed := processCompleted
		if config.alpha {
			runtimeReceipt.After, runtimeReceipt.MetricsSHA256[1] = captureRBACCapacityRuntime(t, fixture, directory, "after")
			sealRBACCapacityEvidence(t, filepath.Join(directory, "rbac-runtime.json"), runtimeReceipt)
		}
		require.NoError(t, log.Sync())
		profile, err := os.ReadFile(profilePath)
		require.NoError(t, err)
		digest := sha256.Sum256(profile)
		window := rbacCapacityK6ProcessWindow{Owner: plan.Owner, ProfileDigest: hex.EncodeToString(digest[:]),
			StartedAt: started.UTC(), CompletedAt: completed.UTC(), Root: root}
		windowFile, err := os.OpenFile(filepath.Join(directory, "k6-process.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		encodeErr := json.NewEncoder(windowFile).Encode(window)
		syncErr := windowFile.Sync()
		closeErr = windowFile.Close()
		require.NoError(t, encodeErr)
		require.NoError(t, syncErr)
		require.NoError(t, closeErr)
		processErr = finishRBACCapacityMutation(processErr, func() error {
			err := cleanup.run(t.Context())
			if err != nil {
				cleanupFailed(err)
			}
			return err
		})
		persistMutation()
		require.NoError(t, processErr, "real ordinary k6 workload failed; retain failed evidence even after successful ownership-pinned restoration")
		verifyRBACCapacityK6Evidence(t, plan, count, config, directory, receipts, root, sourcePaths, decisionPaths,
			workingSet, window, summaryPath)
	})
}

func verifyRBACCapacityK6Evidence(t *testing.T, plan rbacCapacitySeedPlan, count int,
	config *rbacCapacityK6DiagnosticConfig, directory string, receipts map[string]rbacCapacitySeedReceipt,
	root rbacCapacityRootPolicy, sourcePaths, decisionPaths []string, workingSet rbacCapacityK6WorkingSet,
	window rbacCapacityK6ProcessWindow, summaryPath string,
) {
	t.Helper()
	require.Len(t, sourcePaths, 2)
	require.Len(t, decisionPaths, 2)
	require.NoError(t, validateRBACCapacityK6Diagnostic(config))
	require.Equal(t, plan.Owner, window.Owner)
	decisionBound, err := rbacCapacityK6DecisionByteBound(config)
	require.NoError(t, err)
	require.Equal(t, plan.Owner, workingSet.Owner)
	require.True(t, !window.StartedAt.IsZero() && window.CompletedAt.After(window.StartedAt) &&
		window.CompletedAt.Sub(window.StartedAt) <= config.Duration+45*time.Second)
	if config.alpha {
		require.GreaterOrEqual(t, window.CompletedAt.Sub(window.StartedAt), config.Duration,
			"alpha cannot truncate the actual ordinary workload")
	}
	repository, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	profile, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(repository)), "tests", "capacity", "profiles", "api-readiness.js"))
	require.NoError(t, err)
	digest := sha256.Sum256(profile)
	require.Equal(t, hex.EncodeToString(digest[:]), window.ProfileDigest, "ordinary workload evidence must match the verified profile source")
	summaryContent, err := readRBACCapacityPrivateFixture(summaryPath)
	require.NoError(t, err)
	offered, err := checkRBACCapacityK6Summary(summaryContent, config)
	require.NoError(t, err)
	cases := make([]rbacCapacityRequest, len(workingSet.Cases))
	for index, sample := range workingSet.Cases {
		cases[index] = rbacCapacityRequest{Subject: sample.Subject, Namespace: sample.Namespace,
			Action: sample.Action, Expected: sample.Expected}
	}
	for _, paths := range [][]string{sourcePaths, decisionPaths} {
		first, err := os.Lstat(paths[0])
		require.NoError(t, err)
		second, err := os.Lstat(paths[1])
		require.NoError(t, err)
		require.False(t, os.SameFile(first, second), "replica source and decision evidence must remain independent")
	}
	mutation, mutationHistory := readRBACCapacityMutation(t, plan, count, config, directory, receipts, window)
	mutationIndex, err := newRBACCapacityMutationTraceIndex(mutationHistory)
	require.NoError(t, err)
	var rootRecord rbacCapacityRootPropagationReceipt
	var rootHistory []rbacCapacityAuthoredRootAdmission
	var rootIndex rbacCapacityMutationTraceIndex
	if config.RootUnderLoad {
		rootRecord = readRBACCapacityRootPropagationReceipt(t, filepath.Join(directory, "root-propagation-diagnostic.json"))
		require.NoError(t, checkRBACCapacityRootUnderLoadReceipt(plan, count, config, rootRecord, mutation, window))
		rootHistory = rootRecord.History
		clocks := make([]rbacCapacityAuthoredAdmission, len(rootHistory))
		for index, admission := range rootHistory {
			acknowledgedAt := admission.admittedAt()
			if index == 0 {
				acknowledgedAt = window.StartedAt
			}
			clocks[index] = rbacCapacityAuthoredAdmission{
				Namespace: rbacCapacitySeedNamespace{Name: "cluster"},
				Receipt:   rbacCapacitySeedReceipt{GitRevision: admission.GitRevision}, AcknowledgedAt: acknowledgedAt,
			}
		}
		rootIndex, err = newRBACCapacityMutationTraceIndex(clocks)
		require.NoError(t, err)
		baseline, err := rbacCapacityRootNamespaceBaseline(plan, receipts, rootRecord.Subject)
		require.NoError(t, err)
		history := []rbacCapacityAuthoredAdmission{baseline}
		observedRevisions := [2]map[string]string{{}, {}}
		for stage, probes := range rootRecord.ProbeStages {
			for replica := range probes {
				require.NoError(t, verifyRBACCapacityAuthoredTracesWithObservations(t, sourcePaths[replica], decisionPaths[replica],
					history, root, rootHistory[:(stage+1)*4+1], probes[replica], decisionBound,
					func(vector rbacCapacityPolicyVector, id string) {
						observedRevisions[replica][id] = vector.Cluster.GitRevision
					},
					rootRecord.CompletedAt))
			}
		}
		if config.alpha {
			for replica := range 2 {
				latencies, err := rbacCapacityRootPropagationLatencies(rootRecord, replica, observedRevisions[replica])
				require.NoError(t, err)
				slices.Sort(latencies)
				require.NoError(t, checkRBACCapacityAlphaPercentiles(latencies, false))
				p95 := latencies[int(math.Ceil(float64(len(latencies))*.95))-1]
				p99 := latencies[int(math.Ceil(float64(len(latencies))*.99))-1]
				t.Logf("source-joined root propagation: replica=%d admissions=%d p95=%s p99=%s; non-blocking-p95-goal=2s goal-met=%t; enforced-p99=5s",
					replica, len(latencies), p95, p99, p95 <= 2*time.Second)
			}
		}
	}
	for replica := range 2 {
		verifyRBACCapacitySeedSources(t, sourcePaths[replica], plan, count, receipts, window.StartedAt)
		models := readRBACCapacityProbeSourcesWithRootHistory(t, sourcePaths[replica], plan, count, receipts, root, mutationHistory, rootHistory, window.CompletedAt)
		decisions, err := openRBACCapacityPrivateFixture(decisionPaths[replica], decisionBound)
		require.NoError(t, err)
		var beforeMutation, afterMutation bool
		var beforeRoot, afterRoot bool
		observedRootRevisions := make(map[string]bool)
		observedMutationRevisions := make(map[string]bool)
		latencies := make([]time.Duration, 0, offered)
		accounting, traceErr := checkRBACCapacityScheduledTraces(decisions, plan.Owner, replica, offered, cases, models,
			func(vector rbacCapacityPolicyVector, _ rbacCapacityRequest, at time.Time, latency time.Duration) error {
				if at.Before(window.StartedAt) || at.After(window.CompletedAt) {
					return errors.New("ordinary k6 decision receipt is outside its actual workload process window")
				}
				for _, revision := range []rbacCapacityPolicyRevision{vector.Cluster, vector.Namespace} {
					source := models[revision]
					if source.ObservedAt.IsZero() || source.ObservedAt.After(at) {
						return errors.New("ordinary k6 decision predates its independently captured source")
					}
				}
				if len(rootHistory) != 0 {
					beforeRoot = beforeRoot || at.Before(rootHistory[1].AcknowledgedAt)
					afterRoot = afterRoot || at.After(rootRecord.CompletedAt)
					changed, err := rootIndex.check("cluster", vector.Cluster.GitRevision, at)
					if err != nil {
						return err
					}
					if changed {
						observedRootRevisions[vector.Cluster.GitRevision] = true
					}
				}
				if len(mutationHistory) != 0 {
					beforeMutation = beforeMutation || at.Before(mutation.Admissions[0].AcknowledgedAt)
					afterMutation = afterMutation || at.After(mutation.Admissions[len(mutation.Admissions)-1].AcknowledgedAt)
					namespace := models[vector.Namespace].Namespace
					changed, err := mutationIndex.check(namespace, vector.Namespace.GitRevision, at)
					if err != nil {
						return err
					}
					if changed {
						observedMutationRevisions[namespace+"/"+vector.Namespace.GitRevision] = true
					}
				}
				latencies = append(latencies, latency)
				return nil
			}, decisionBound)
		closeErr := decisions.Close()
		require.NoError(t, traceErr, "every ordinary k6 outcome must join its independently authored source vector")
		require.NoError(t, closeErr)
		require.NoError(t, accounting.validate(offered, false))
		require.Equal(t, offered*6/10, accounting.Allowed)
		require.Equal(t, offered*3/10, accounting.Denied)
		require.Equal(t, offered/10, accounting.Invalid)
		require.Len(t, latencies, int(offered), "provider latency percentiles must include every independently joined ordinary decision")
		slices.Sort(latencies)
		if config.alpha {
			require.NoError(t, checkRBACCapacityAlphaPercentiles(latencies, true))
		}
		t.Logf("source-joined provider-call latency: replica=%d observations=%d p99=%s maximum=%s; exact producer nanoseconds, not HTTP latency; healthy-alpha-gates=%t",
			replica, len(latencies), latencies[int(math.Ceil(float64(len(latencies))*.99))-1], latencies[len(latencies)-1], config.alpha)
		if len(mutationHistory) != 0 {
			require.True(t, beforeMutation && afterMutation,
				"the real ordinary stream must bracket mutation admissions on each API")
			require.Len(t, observedMutationRevisions, len(mutation.Admissions),
				"ordinary decisions must join every actual mutation across all four owned tiers")
			mutationModels := readRBACCapacityProbeSourcesWithRootHistory(t, sourcePaths[replica], plan, count, receipts,
				root, mutationHistory, rootHistory, mutation.RestoredAt)
			if len(rootHistory) != 0 {
				for revision, model := range mutationModels {
					if model.Root && revision.GitRevision != rootHistory[len(rootHistory)-1].GitRevision {
						delete(mutationModels, revision)
					}
				}
			}
			probes, err := rbacCapacityMutationProbes(mutation, replica)
			require.NoError(t, err)
			revisions := make(map[string]string, len(probes))
			require.NoError(t, verifyRBACCapacityConcurrentTraces(t, decisionPaths[replica],
				mutationHistory, mutationModels, probes, revisions, decisionBound),
				"one bounded stream pass must join every baseline, admission and forward-restoration probe to its original clocks")
			latencies, err := rbacCapacityMutationPropagation(mutation, replica, revisions)
			require.NoError(t, err)
			slices.Sort(latencies)
			if config.alpha {
				require.NoError(t, checkRBACCapacityAlphaPercentiles(latencies, false))
			}
			p95 := latencies[int(math.Ceil(float64(len(latencies))*.95))-1]
			p99 := latencies[int(math.Ceil(float64(len(latencies))*.99))-1]
			t.Logf("source-joined mutation propagation: replica=%d admissions=%d p95=%s p99=%s; non-blocking-p95-goal=2s goal-met=%t; original ACK to first matching HTTP response at the exact admitted Namespace revision; healthy-alpha-gates=%t",
				replica, len(latencies), p95, p99, p95 <= 2*time.Second, config.alpha)
			t.Logf("concurrent mutation replay: replica=%d admissions=%d tiers=4 restorations=%d original fillers restored; total50/min bounded diagnostic",
				replica, len(mutation.Admissions), len(mutation.Restorations))
		}
		if len(rootHistory) != 0 {
			require.True(t, beforeRoot && afterRoot, "actual ordinary decisions on each API must bracket the complete root preamble")
			require.Len(t, observedRootRevisions, 8, "each API ordinary stream must join all eight authored root phases")
		}
		t.Logf("connected ordinary k6 source parity: replica=%d accounting=%+v; healthy-alpha-gates=%t", replica, accounting, config.alpha)
	}
	if config.alpha {
		verifyRBACCapacityAlphaRuntime(t, directory, window, config.MaxVUs)
	}
}

func checkRBACCapacityK6Summary(content []byte, config *rbacCapacityK6DiagnosticConfig) (int64, error) {
	if err := validateRBACCapacityK6Diagnostic(config); err != nil {
		return 0, err
	}
	offered := int64(config.Duration/time.Second) * int64(config.Rate)
	var summary struct {
		Metrics map[string]struct{ Count *float64 }
	}
	if err := json.Unmarshal(content, &summary); err != nil {
		return 0, err
	}
	expected := map[string]int64{
		"rbac_requests": offered * 2, "rbac_completed": offered * 2,
		"rbac_allowed": offered * 12 / 10, "rbac_denied": offered * 6 / 10,
		"rbac_isolation": offered * 2 / 10, "rbac_wrong": 0, "rbac_unavailable": 0,
	}
	for replica := range 2 {
		for _, name := range []string{"rbac_requests", "rbac_completed"} {
			expected[fmt.Sprintf("%s{replica:%d}", name, replica)] = offered
		}
	}
	for name, count := range expected {
		actual := summary.Metrics[name].Count
		if actual == nil || *actual != float64(count) {
			return 0, fmt.Errorf("ordinary k6 metric %s must report exactly %d", name, count)
		}
	}
	return offered, nil
}

func TestRBACCapacityK6EvidenceVerifier(t *testing.T) {
	if os.Getenv("RBAC_TEST_CAPACITY_VERIFY_K6") != "1" {
		t.Skip("ordinary k6 evidence verification must be explicitly selected")
	}
	content, err := readRBACCapacityPrivateFixture(os.Getenv("CAPACITY_RBAC_SEED_FILE"))
	require.NoError(t, err)
	var config rbacCapacityNativeSeedConfig
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&config))
	require.NoError(t, config.Plan.validate())
	require.NoError(t, validateRBACCapacityMode(config))
	if config.Mode == "alpha" {
		config.K6Workload.alpha = true
	}
	require.NoError(t, validateRBACCapacitySeedResume(config))
	directory, err := filepath.Abs(config.ReceiptDirectory)
	require.NoError(t, err)
	require.Equal(t, directory, os.Getenv("CAPACITY_RBAC_EVIDENCE_DIR"), "the verifier must consume this run's own evidence directory")
	info, err := os.Lstat(directory)
	require.NoError(t, err)
	require.True(t, info.IsDir() && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0o077 == 0)
	require.True(t, config.Count >= 2 && config.Count <= 1000)
	require.NotNil(t, config.K6Workload)
	require.Len(t, config.SourceModelFiles, 2)
	require.Len(t, config.DecisionLogFiles, 2)
	receipts := make(map[string]rbacCapacitySeedReceipt, config.Count)
	identities := make(map[string]bool, config.Count*61)
	for index := range config.Count {
		namespace := config.Plan.namespace(index)
		content, err := readRBACCapacityPrivateFixture(filepath.Join(config.ReceiptDirectory, namespace.Name+".json"))
		require.NoError(t, err)
		var receipt rbacCapacitySeedReceipt
		require.NoError(t, json.Unmarshal(content, &receipt))
		require.True(t, receipt.Complete)
		require.Equal(t, config.Plan.Owner, receipt.Owner)
		require.Equal(t, namespace.Name, receipt.Namespace)
		require.Equal(t, namespace.Tier, receipt.Tier)
		require.NotEmpty(t, receipt.Admissions)
		require.Equal(t, receipt.GitRevision, receipt.Admissions[len(receipt.Admissions)-1].GitRevision)
		require.Equal(t, len(namespace.Resources), receipt.Admissions[len(receipt.Admissions)-1].Resources)
		require.Len(t, receipt.Roles, 10)
		require.Len(t, receipt.Bindings, 50)
		for _, resources := range []map[string]string{{"Namespace": receipt.NamespaceNodeUID}, receipt.Roles, receipt.Bindings} {
			for _, uid := range resources {
				require.NotEmpty(t, uid)
				require.False(t, identities[uid], "source counts require distinct immutable native identities")
				identities[uid] = true
			}
		}
		receipts[namespace.Name] = receipt
	}
	require.NoError(t, applyRBACCapacityResumeBaselines(config, receipts))
	content, err = readRBACCapacityPrivateFixture(filepath.Join(config.ReceiptDirectory, "k6-process.json"))
	require.NoError(t, err)
	var window rbacCapacityK6ProcessWindow
	require.NoError(t, json.Unmarshal(content, &window))
	require.Equal(t, config.InitialAdminSubject, window.Root.AdminSubject)
	rootDigest, err := rbacCapacityRootTreeDigest(config.InitialAdminSubject)
	require.NoError(t, err)
	require.Equal(t, rootDigest, window.Root.TreeDigest)
	content, err = readRBACCapacityPrivateFixtureWithLimit(filepath.Join(config.ReceiptDirectory, "k6-working-set.json"), 16<<20)
	require.NoError(t, err)
	var workingSet rbacCapacityK6WorkingSet
	require.NoError(t, json.Unmarshal(content, &workingSet))
	require.Equal(t, config.Plan.Owner, workingSet.Owner)
	require.Len(t, workingSet.Cases, config.Count*10)
	for index, actual := range workingSet.Cases {
		expected, err := config.Plan.request(int64(index)*2, config.Count, config.K6Workload.GroupMembers)
		require.NoError(t, err)
		require.Equal(t, expected.Subject, actual.Subject)
		require.Equal(t, expected.Namespace, actual.Namespace)
		require.Equal(t, expected.Action, actual.Action)
		require.Equal(t, expected.Expected, actual.Expected)
	}
	verifyRBACCapacityK6Evidence(t, config.Plan, config.Count, config.K6Workload, config.ReceiptDirectory,
		receipts, window.Root, config.SourceModelFiles, config.DecisionLogFiles, workingSet, window,
		filepath.Join(config.ReceiptDirectory, "k6-summary.json"))
	if workload := config.DecisionWorkload; workload != nil && workload.PropagationDiagnostic {
		verifyRBACCapacitySealedPropagation(t, config, receipts, window)
	}
	if config.Mode == "alpha" {
		topologyContent, err := readRBACCapacityPrivateFixture(os.Getenv("CAPACITY_RBAC_TOPOLOGY_FILE"))
		require.NoError(t, err)
		var topology rbacCapacityTopology
		require.NoError(t, json.Unmarshal(topologyContent, &topology))
		require.NoError(t, topology.validateCounts(config.Count))
		sealRBACCapacityProvenance(t, config, topology, false)
		sealRBACCapacityEvidence(t, filepath.Join(directory, "rbac-alpha-verifier.json"), struct {
			Passed         bool
			SourceRevision string
			Window         rbacCapacityK6ProcessWindow
		}{true, rbacCapacitySourceRevision(t), window})
	}
}

func verifyRBACCapacitySealedPropagation(t *testing.T, config rbacCapacityNativeSeedConfig,
	seeds map[string]rbacCapacitySeedReceipt, window rbacCapacityK6ProcessWindow,
) {
	t.Helper()
	read := func(name string, record any) {
		content, err := readRBACCapacityPrivateFixture(filepath.Join(config.ReceiptDirectory, name))
		require.NoError(t, err)
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		require.NoError(t, decoder.Decode(record))
		require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	}
	var record rbacCapacityPropagationReceipt
	read("propagation-diagnostic.json", &record)
	require.True(t, record.Complete)
	require.Equal(t, config.Plan.Owner, record.Owner)
	index, err := rbacCapacityPropagationNamespace(config.Plan, config.Count, config.DecisionWorkload.GroupMembers)
	require.NoError(t, err)
	namespace := config.Plan.namespace(index)
	subject := config.Plan.Subjects[index%len(config.Plan.Subjects)]
	require.Equal(t, namespace.Name, record.Namespace)
	require.Equal(t, subject, record.Subject)
	require.Len(t, record.Admissions, 4)
	require.Len(t, record.History, 5)
	seed := seeds[namespace.Name]
	require.NotEmpty(t, seed.Admissions)
	generations := map[string]int64{"Role/capacity-role-00": 1, "RoleBinding/capacity-binding-00": 1}
	references := map[string]int64{"RoleBinding/capacity-binding-00": 1}
	equalJSON := func(expected, actual any) {
		expectedJSON, err := json.Marshal(expected)
		require.NoError(t, err)
		actualJSON, err := json.Marshal(actual)
		require.NoError(t, err)
		require.JSONEq(t, string(expectedJSON), string(actualJSON), "sealed declarations must match the independently reconstructed mutation recipe")
	}
	equalJSON(rbacCapacityAuthoredAdmission{
		Namespace: namespace, Receipt: seed, AcknowledgedAt: seed.Admissions[len(seed.Admissions)-1].AcknowledgedAt,
		Cases: map[string]bool{subject + "/product.list": true}, Generations: maps.Clone(generations), References: references,
	}, record.History[0])
	clocks := make([]time.Time, 4)
	for step, admission := range record.Admissions {
		require.True(t, admission.Complete)
		require.True(t, admission.AcknowledgedAt.After(window.CompletedAt), "mutation probes cannot reinterpret the earlier static k6 window")
		authored := config.Plan.namespace(index)
		kind, name, resource := "Role", "capacity-role-00", 0
		if step >= 2 {
			kind, name, resource = "RoleBinding", "capacity-binding-00", 10
		}
		allowed := step%2 == 1
		if !allowed {
			spec := authored.Resources[resource]["spec"].(map[string]any)
			if kind == "Role" {
				spec["rules"].([]any)[0].(map[string]any)["verbs"] = []string{"read"}
			} else {
				spec["subjects"] = []any{map[string]any{"kind": "User", "name": config.Plan.Prefix + "-filler-propagation"}}
			}
		}
		key := kind + "/" + name
		generations[key]++
		require.Equal(t, kind, admission.Kind)
		require.Equal(t, name, admission.Name)
		require.Equal(t, generations[key], admission.Generation)
		require.Equal(t, references[key], admission.ReferenceGeneration)
		require.Equal(t, allowed, admission.Allowed)
		for _, latency := range admission.Latencies {
			require.True(t, latency > 0 && latency <= 5*time.Second)
		}
		admitted := seed
		admitted.GitRevision = admission.GitRevision
		equalJSON(rbacCapacityAuthoredAdmission{
			Namespace: authored, Receipt: admitted, AcknowledgedAt: admission.AcknowledgedAt,
			Cases: map[string]bool{subject + "/product.list": allowed}, Generations: maps.Clone(generations), References: references,
		}, record.History[step+1])
		clocks[step] = admission.AcknowledgedAt
	}
	require.NoError(t, validateRBACCapacitySealedProbes(record.Owner, subject, namespace.Name, false, 0, clocks, record.Probes))
	rootRecord := readRBACCapacityRootPropagationReceipt(t, filepath.Join(config.ReceiptDirectory, "root-propagation-diagnostic.json"))
	require.True(t, rootRecord.Complete)
	require.Equal(t, config.Plan.Owner, rootRecord.Owner)
	require.Equal(t, namespace.Name, rootRecord.Namespace)
	var target rbacCapacityRequest
	for offer := int64(0); offer < int64(20*config.Count); offer++ {
		request, err := config.Plan.request(offer, config.Count, config.DecisionWorkload.GroupMembers)
		require.NoError(t, err)
		if request.Namespace == namespace.Name && request.Action == "product.list" && request.Expected == "deny" {
			target = request
			break
		}
	}
	require.NotEmpty(t, target.Subject)
	require.Equal(t, target.Subject, rootRecord.Subject)
	require.Len(t, rootRecord.History, 9)
	require.Len(t, rootRecord.Latencies, 8)
	require.Len(t, rootRecord.ProbeStages, 2)
	require.Equal(t, 8, rootRecord.VerifiedSources)
	require.True(t, rootRecord.History[0].BaselineKnownAt.After(clocks[3]))
	require.NoError(t, validateRBACCapacitySealedRootHistory(config.Plan, target.Subject, window.Root, rootRecord.History, namespace.Name))
	for replica := range record.Probes {
		require.NoError(t, verifyRBACCapacityDynamicTraces(t, config.SourceModelFiles[replica], config.DecisionLogFiles[replica],
			record.History, window.Root, record.Probes[replica], rootRecord.History[0].BaselineKnownAt))
	}
	for index := range record.History {
		record.History[index].Cases = map[string]bool{target.Subject + "/product.list": false}
	}
	for stage, probes := range rootRecord.ProbeStages {
		for local := range clocks {
			step := stage*4 + local
			clocks[local] = rootRecord.History[step+1].AcknowledgedAt
			for _, latency := range rootRecord.Latencies[step] {
				require.True(t, latency > 0 && latency <= 5*time.Second)
			}
		}
		require.NoError(t, validateRBACCapacitySealedProbes(rootRecord.Owner, target.Subject, namespace.Name, true, stage*4, clocks, probes))
		for replica := range probes {
			require.NoError(t, verifyRBACCapacityAuthoredTraces(t, config.SourceModelFiles[replica], config.DecisionLogFiles[replica],
				record.History, window.Root, rootRecord.History[:(stage+1)*4+1], probes[replica]))
		}
	}
	t.Log("sealed four-phase Namespace and eight-phase root HTTP evidence independently replayed against authored histories and both persisted source/decision streams")
}

func validateRBACCapacitySealedRootHistory(plan rbacCapacitySeedPlan, subject string, root rbacCapacityRootPolicy,
	history []rbacCapacityAuthoredRootAdmission, namespace string,
) error {
	if len(history) != 9 || subject == "" {
		return errors.New("sealed root propagation requires the complete eight-phase authored history")
	}
	if plan.ConditionalRoot && (!rbacCapacitySeedName.MatchString(namespace) || len(namespace) > 63) {
		return errors.New("sealed conditional root history requires its independently selected Namespace")
	}
	equal := func(expected, actual any) bool {
		left, leftErr := json.Marshal(expected)
		right, rightErr := json.Marshal(actual)
		return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
	}
	baseline := history[0]
	if !equal(root, baseline.Policy) || !equal(map[string]bool{subject + "/product.list": false}, baseline.Cases) ||
		len(baseline.Resources) != 0 || len(baseline.PendingCases) != 0 || baseline.BaselineKnownAt.IsZero() || !baseline.AcknowledgedAt.IsZero() {
		return errors.New("sealed root history reinterprets the independently pinned bootstrap")
	}
	manifest := func(kind, name string, spec map[string]any) map[string]any {
		return map[string]any{
			"apiVersion": "rbac.authorization.gitstore.dev/v1beta1", "kind": kind,
			"metadata": map[string]any{"name": name, "labels": map[string]any{rbacCapacityOwnerLabel: plan.Owner}}, "spec": spec,
		}
	}
	bindingSpec := func(role, target string) map[string]any {
		return map[string]any{"roleRef": map[string]any{"kind": "ClusterRole", "name": role},
			"subjects": []any{map[string]any{"kind": "User", "name": target}}}
	}
	identities := map[string]bool{root.NamespaceUID: true, root.BindingUID: true}
	revisions := make(map[string]bool, len(history))
	canonical := regexp.MustCompile(`^main@sha1:[0-9a-f]{40}$`)
	for _, admission := range history {
		if !canonical.MatchString(admission.GitRevision) || revisions[admission.GitRevision] {
			return errors.New("sealed root history requires distinct canonical admission revisions")
		}
		revisions[admission.GitRevision] = true
	}
	for step := range 8 {
		actual := history[step+1]
		if actual.AcknowledgedAt.IsZero() || !actual.AcknowledgedAt.After(history[step].admittedAt()) ||
			!actual.BaselineKnownAt.IsZero() || actual.GitRevision == "" {
			return errors.New("sealed root history lost its original ordered admission clocks")
		}
		allowed := step%2 == 0
		expected := rbacCapacityAuthoredRootAdmission{
			Policy: root, GitRevision: actual.GitRevision, AcknowledgedAt: actual.AcknowledgedAt,
			Cases:     map[string]bool{subject + "/product.list": allowed},
			Resources: append([]rbacCapacityAuthoredRootResource(nil), history[step].Resources...),
		}
		if step == 0 || step == 4 {
			expected.PendingCases = maps.Clone(history[step].Cases)
		}
		switch {
		case step < 3:
			if len(actual.Resources) != 1 {
				return errors.New("sealed root binding phase has the wrong resource count")
			}
			target := subject
			if !allowed {
				target = plan.Prefix + "-filler-root-propagation"
			}
			uid, roleUID := actual.Resources[0].UID, actual.Resources[0].RoleUID
			if step > 0 {
				uid, roleUID = history[step].Resources[0].UID, history[step].Resources[0].RoleUID
			} else if uid == "" || roleUID == "" || identities[uid] || identities[roleUID] || uid == roleUID {
				return errors.New("sealed root binding requires a distinct immutable incarnation and reference")
			}
			identities[uid] = true
			identities[roleUID] = true
			expected.Resources = []rbacCapacityAuthoredRootResource{{
				Manifest: manifest("ClusterRoleBinding", plan.Prefix+"-root-view", bindingSpec("view", target)),
				UID:      uid, RoleUID: roleUID, Generation: int64(step + 1), ReferenceGeneration: 1,
			}}
		case step == 3:
			expected.Resources[0].Removed = true
		case step == 7:
			for index := 1; index < len(expected.Resources); index++ {
				expected.Resources[index].Removed = true
			}
		default:
			verb := "list"
			if !allowed {
				verb = "read"
			}
			rule := map[string]any{"resources": []string{"product"}, "verbs": []string{verb}}
			if plan.ConditionalRoot {
				value := namespace
				if !allowed {
					value = plan.Prefix + "-filler-root-condition"
				}
				rule["verbs"] = []string{"list"}
				rule["when"] = []any{map[string]any{"attr": "namespace", "op": "eq", "value": value}}
			}
			role := manifest("ClusterRole", plan.Prefix+"-root-rule", map[string]any{"rules": []any{rule}})
			if step == 4 {
				count := 3
				if plan.MaximumRootBatch {
					count = 257
				}
				if len(actual.Resources) != count {
					return errors.New("sealed custom root creation requires exactly the declared Role and binding")
				}
				roleUID, bindingUID := actual.Resources[1].UID, actual.Resources[2].UID
				if roleUID == "" || bindingUID == "" || roleUID == bindingUID || identities[roleUID] || identities[bindingUID] {
					return errors.New("sealed custom root creation reused an earlier incarnation")
				}
				identities[roleUID], identities[bindingUID] = true, true
				grants := []rbacCapacityAuthoredRootResource{
					{Manifest: role, UID: roleUID, Generation: 1},
					{
						Manifest: manifest("ClusterRoleBinding", plan.Prefix+"-root-rule-binding", bindingSpec(plan.Prefix+"-root-rule", subject)),
						UID:      bindingUID, RoleUID: roleUID, Generation: 1, ReferenceGeneration: 1,
					},
				}
				if plan.MaximumRootBatch {
					var err error
					grants, err = rbacCapacityMaximumRootBatch(plan, grants)
					if err != nil {
						return err
					}
					for index := 2; index < len(grants); index++ {
						uid := actual.Resources[index+1].UID
						if uid == "" || identities[uid] {
							return errors.New("sealed maximum root batch requires distinct pinned filler incarnations")
						}
						identities[uid], grants[index].UID = true, uid
					}
				}
				expected.Resources = append(expected.Resources, grants...)
			} else {
				expected.Resources[1].Manifest, expected.Resources[1].Generation = role, int64(step-3)
			}
		}
		digest, err := rbacCapacityAuthoredRootTreeDigest(root.AdminSubject, expected.Resources)
		if err != nil {
			return err
		}
		expected.Policy.TreeDigest = digest
		if !equal(expected, actual) {
			return errors.New("sealed root declaration differs from the independent grant/revoke/restore/removal recipe")
		}
	}
	return nil
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

type rbacCapacityPropagationAdmission struct {
	Kind, Name          string
	GitRevision         string
	AcknowledgedAt      time.Time
	Generation          int64
	ReferenceGeneration int64
	Allowed             bool
	Latencies           [2]time.Duration
	Complete            bool
}

type rbacCapacityPropagationReceipt struct {
	Owner, Namespace, Subject string
	Admissions                []rbacCapacityPropagationAdmission
	History                   []rbacCapacityAuthoredAdmission
	Probes                    [2]map[string]rbacCapacityProbeExpectation
	Complete                  bool
}

type rbacCapacityRootPropagationReceipt struct {
	Owner, Namespace, Subject string
	History                   []rbacCapacityAuthoredRootAdmission
	Latencies                 [][2]time.Duration
	Pending                   *rbacCapacityAuthoredRootAdmission  `json:",omitempty"`
	Restorations              []rbacCapacityAuthoredRootAdmission `json:",omitempty"`
	CleanupError              string                              `json:",omitempty"`
	RestoredAt                time.Time                           `json:",omitempty"`
	ProbeStages               [][2]map[string]rbacCapacityProbeExpectation
	VerifiedSources           int
	Complete                  bool
	CompletedAt               time.Time `json:",omitempty"`
}

func readRBACCapacityRootPropagationReceipt(t *testing.T, path string) rbacCapacityRootPropagationReceipt {
	t.Helper()
	file, err := openRBACCapacityPrivateFixture(path, 32<<20)
	require.NoError(t, err)
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (32<<20)+1))
	decoder.DisallowUnknownFields()
	var record rbacCapacityRootPropagationReceipt
	require.NoError(t, decoder.Decode(&record))
	var trailing any
	require.ErrorIs(t, decoder.Decode(&trailing), io.EOF)
	return record
}

type rbacCapacityRootDiagnosticControl struct {
	Target          rbacCapacityRequest
	Context         context.Context
	Git             *rbacNativeGit
	BeforeAdmission func() error
	BeforeRestore   func() error
}

func runRBACCapacityRootCleanup(record *rbacCapacityRootPropagationReceipt, stop, restore func() error) error {
	if record.Complete {
		return nil
	}
	if stop != nil {
		if err := stop(); err != nil {
			record.CleanupError = err.Error()
			return err
		}
	}
	if err := restore(); err != nil {
		if record.CleanupError == "" {
			record.CleanupError = err.Error()
		} else {
			record.CleanupError = errors.Join(errors.New(record.CleanupError), err).Error()
		}
		return err
	}
	return nil
}

func checkRBACCapacityRootUnderLoadReceipt(plan rbacCapacitySeedPlan, count int, config *rbacCapacityK6DiagnosticConfig,
	record rbacCapacityRootPropagationReceipt, mutation rbacCapacityMutationReceipt, window rbacCapacityK6ProcessWindow,
) error {
	if err := validateRBACCapacityK6Diagnostic(config); err != nil {
		return err
	}
	if err := validateRBACCapacityMutation(plan, count, config, window.Root.AdminSubject); err != nil {
		return err
	}
	if !config.RootUnderLoad || record.Owner != plan.Owner || record.Subject != config.Mutation.Subject ||
		record.Namespace != plan.namespaceName(5) || !record.Complete || record.Pending != nil ||
		record.CleanupError != "" || !record.RestoredAt.IsZero() || len(record.Restorations) != 0 ||
		len(record.History) != 9 || len(record.Latencies) != 8 || len(record.ProbeStages) != 2 || record.VerifiedSources != 8 {
		return errors.New("root-under-load requires a clean complete eight-phase receipt for the untouched holdout tier")
	}
	preambleEnd := window.StartedAt.Add(48 * time.Second)
	if !record.History[0].BaselineKnownAt.After(window.StartedAt) ||
		!record.CompletedAt.After(record.History[8].AcknowledgedAt) || record.CompletedAt.After(preambleEnd) ||
		mutation.ScheduledAt.Before(preambleEnd) || !mutation.ScheduledAt.After(record.CompletedAt) ||
		!window.CompletedAt.After(mutation.CompletedAt) ||
		mutation.CompletedAt.Sub(mutation.ScheduledAt) < config.Mutation.Schedule.Duration ||
		!window.CompletedAt.After(record.CompletedAt) {
		return errors.New("root phases, full Namespace write schedule and ordinary traffic lack their fixed ordered load windows")
	}
	if err := validateRBACCapacitySealedRootHistory(plan, record.Subject, window.Root, record.History, record.Namespace); err != nil {
		return err
	}
	for stage, probes := range record.ProbeStages {
		clocks := make([]time.Time, 4)
		for local := range clocks {
			step := stage*4 + local
			clocks[local] = record.History[step+1].AcknowledgedAt
			for _, latency := range record.Latencies[step] {
				if latency <= 0 || latency > 5*time.Second {
					return errors.New("root propagation exceeded its original five-second successful-push clock")
				}
			}
		}
		if err := validateRBACCapacitySealedProbes(record.Owner, record.Subject, record.Namespace, true, stage*4, clocks, probes); err != nil {
			return err
		}
		for _, observations := range probes {
			for _, probe := range observations {
				if probe.ObservedAt.After(record.CompletedAt) || probe.ObservedAt.Before(window.StartedAt) {
					return errors.New("root HTTP evidence lies outside its actual preamble")
				}
			}
		}
	}
	return nil
}

func validateRBACCapacitySealedProbes(owner, subject, namespace string, root bool, firstStep int,
	clocks []time.Time, probes [2]map[string]rbacCapacityProbeExpectation,
) error {
	if owner == "" || subject == "" || namespace == "" || len(clocks) != 4 || (firstStep != 0 && (!root || firstStep != 4)) {
		return errors.New("sealed propagation requires an owned four-phase probe stage")
	}
	family := "propagation"
	if root {
		family = "root-propagation"
	}
	for step, clock := range clocks {
		if clock.IsZero() || (step > 0 && !clock.After(clocks[step-1])) {
			return errors.New("sealed propagation must retain ordered successful-push clocks")
		}
	}
	for replica, observations := range probes {
		if len(observations) == 0 || len(observations) > 128 {
			return errors.New("sealed propagation requires nonempty bounded probes on both APIs")
		}
		baselineID := fmt.Sprintf("%s-propagation-baseline-%d", owner, replica)
		if root {
			baselineID = fmt.Sprintf("%s-root-propagation--1-%d-00", owner, replica)
		}
		var phases [4]map[int]rbacCapacityProbeExpectation
		for step := range phases {
			phases[step] = make(map[int]rbacCapacityProbeExpectation)
		}
		baseline := false
		for id, probe := range observations {
			if probe.Subject != subject || probe.Namespace != namespace || probe.Action != "product.list" || probe.ObservedAt.IsZero() {
				return errors.New("sealed propagation probe has a different principal, Namespace, action or missing clock")
			}
			if id == baselineID && firstStep == 0 {
				if !probe.ObservedAt.Before(clocks[0]) || probe.Allowed == root {
					return errors.New("sealed propagation baseline must precede writes and match independent intent")
				}
				baseline = true
				continue
			}
			suffix, found := strings.CutPrefix(id, owner+"-"+family+"-")
			parts := strings.Split(suffix, "-")
			if !found || len(parts) != 3 {
				return errors.New("sealed propagation has an undeclared request ID")
			}
			step, stepErr := strconv.Atoi(parts[0])
			api, apiErr := strconv.Atoi(parts[1])
			attempt, attemptErr := strconv.Atoi(parts[2])
			if stepErr != nil || apiErr != nil || attemptErr != nil || api != replica || step < firstStep ||
				step >= firstStep+4 || attempt < 0 || attempt > 50 ||
				id != fmt.Sprintf("%s-%s-%d-%d-%02d", owner, family, step, replica, attempt) {
				return errors.New("sealed propagation request ID is not a canonical phase/replica/attempt")
			}
			local := step - firstStep
			if probe.ObservedAt.Before(clocks[local]) || probe.ObservedAt.After(clocks[local].Add(5*time.Second)) ||
				(local < 3 && !probe.ObservedAt.Before(clocks[local+1])) {
				return errors.New("sealed propagation observation is outside its original admission window")
			}
			phases[local][attempt] = probe
		}
		if firstStep == 0 && !baseline {
			return errors.New("sealed propagation baseline is missing")
		}
		for local, phase := range phases {
			if len(phase) == 0 {
				return errors.New("sealed propagation omitted a phase or replica")
			}
			var previous time.Time
			for attempt := range len(phase) {
				probe, found := phase[attempt]
				if !found || (!previous.IsZero() && probe.ObservedAt.Before(previous)) {
					return errors.New("sealed propagation omitted or reordered an HTTP attempt")
				}
				previous = probe.ObservedAt
			}
			allowed := (local%2 == 1) != root
			if phase[len(phase)-1].Allowed != allowed {
				return errors.New("sealed propagation did not converge to independently authored intent")
			}
		}
	}
	return nil
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
	record := rbacCapacityPropagationReceipt{Owner: plan.Owner, Namespace: namespace.Name, Subject: subject}
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
	record.Probes = probes
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
		Namespace: namespace, Receipt: seed, AcknowledgedAt: seed.Admissions[len(seed.Admissions)-1].AcknowledgedAt,
		Cases:       map[string]bool{subject + "/product.list": true},
		Generations: maps.Clone(generations),
		References:  maps.Clone(references),
	}}
	defer func() { record.History = history }()
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
		record.Admissions = append(record.Admissions, rbacCapacityPropagationAdmission{
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
		waitRBACCapacityCapturedSources(t, sourcePaths, acknowledgedAt, func(header rbacCapacitySourceHeader, resources []json.RawMessage) bool {
			var origin struct {
				Namespace, GitRevision string
				Root                   bool
			}
			require.NoError(t, json.Unmarshal(header.Origin, &origin))
			if origin.Root || origin.Namespace != namespace.Name || origin.GitRevision != admitted.GitRevision {
				return false
			}
			return checkRBACCapacityNamespaceSource(t, header, resources, authored, admitted, generations, references)
		})
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
	t.Log("bounded Role-rule and binding-subject grant/revoke diagnostics joined to authored Namespace history, mutation generations and fixed root intent")
	runRBACCapacityRootPropagationDiagnostic(t, fixture, plan, count, groupMembers, tokens, receiptDirectory,
		history, sourcePaths, decisionPaths, root)
}

func waitRBACCapacityCapturedSources(t *testing.T, paths []string, acknowledgedAt time.Time,
	matches func(rbacCapacitySourceHeader, []json.RawMessage) bool,
	cleanupContext ...context.Context,
) {
	t.Helper()
	require.Len(t, paths, 2)
	parent := t.Context()
	if len(cleanupContext) != 0 {
		parent = cleanupContext[0]
	}
	deadline := acknowledgedAt.Add(5 * time.Second)
	verified := make([]bool, len(paths))
	for attempt := 0; attempt < 51 && parent.Err() == nil; attempt++ {
		for index, path := range paths {
			if verified[index] {
				continue
			}
			source, err := openRBACCapacityPrivateFixture(path, 1<<30)
			require.NoError(t, err)
			verified[index], err = checkRBACCapacityCapturedSource(source, deadline, matches)
			require.NoError(t, source.Close())
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				require.NoError(t, err)
			}
		}
		if parent.Err() == nil && verified[0] && verified[1] {
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-parent.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	require.FailNow(t, "both APIs must capture independently eligible source before the original five-second mutation deadline",
		"verified=%v; never advance the writer using only an early HTTP outcome from pending status", verified)
}

func checkRBACCapacityCapturedSource(reader io.Reader, deadline time.Time,
	matches func(rbacCapacitySourceHeader, []json.RawMessage) bool,
) (bool, error) {
	verified := false
	err := walkRBACCapacitySourceModelsWithClock(reader,
		func(header rbacCapacitySourceHeader, resources []json.RawMessage, observedAt time.Time) error {
			if observedAt.IsZero() || observedAt.After(time.Now()) {
				return errors.New("RBAC source capture requires a nonzero, nonfuture observation clock")
			}
			// UID readbacks and replay do not change when the complete source was captured.
			if !verified && !observedAt.After(deadline) {
				verified = matches(header, resources)
			}
			return nil
		})
	return verified, err
}

func restoreRBACCapacityRootDiagnostic(t *testing.T, fixture rbacNativeFixture, git rbacNativeGit, token string,
	sourcePaths []string, record *rbacCapacityRootPropagationReceipt,
) {
	t.Helper()
	record.CleanupError = "owned root restoration did not finish; preserve canonical candidate and recovery pins"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fixture.cleanupContext, git.fixture.cleanupContext = ctx, ctx
	canonical := strings.Fields(git.remoteTip(t))
	require.Len(t, canonical, 2)
	revision := "main@sha1:" + canonical[0]
	owned, err := rbacCapacityRootRestorationAdmission(record, revision)
	require.NoError(t, err, "root restoration refuses a canonical ref outside the exact owned authored history or dispatched candidate")
	git = prepareRBACCapacityRestoration(t, git, revision)
	resources := slices.Clone(owned.Resources)
	paths := []string{"rm", "--"}
	for index := range resources {
		resource := &resources[index]
		if resource.Removed {
			continue
		}
		kind := resource.Manifest["kind"].(string)
		name := resource.Manifest["metadata"].(map[string]any)["name"].(string)
		path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
		current, err := os.ReadFile(filepath.Join(git.dir, path))
		require.NoError(t, err)
		require.NoError(t, checkRBACCapacityRootRestorationBytes(*resource, current))
		for _, api := range fixture.apis {
			current, response := lookupRBACCapacityMutation(t, fixture, api, kind, name)
			require.Empty(t, response.Errors)
			require.Equal(t, resource.Generation, current.Metadata.Generation)
			require.Equal(t, rbacCapacitySeedSpec(t, resource.Manifest["spec"], true), rbacCapacitySeedSpec(t, current.Spec, false))
			require.NotEmpty(t, current.Metadata.UID)
			if resource.UID == "" {
				resource.UID = current.Metadata.UID
			}
			require.Equal(t, resource.UID, current.Metadata.UID, "root cleanup refuses a replaced incarnation")
		}
		resource.Removed = true
		paths = append(paths, path)
	}
	restoration := rbacCapacityAuthoredRootAdmission{
		Policy: record.History[0].Policy, Resources: resources,
		Cases: map[string]bool{record.Subject + "/product.list": false},
	}
	denialCheckAt, sourceAfter := time.Now(), owned.admittedAt()
	if len(paths) > 2 {
		_, err := git.command(t, paths...)
		require.NoError(t, err)
		sha := git.commit(t)
		git.push(t)
		restoration.AcknowledgedAt, restoration.GitRevision = time.Now(), "main@sha1:"+sha
		denialCheckAt, sourceAfter = restoration.AcknowledgedAt, restoration.AcknowledgedAt
	} else {
		restoration.AcknowledgedAt, restoration.BaselineKnownAt, restoration.GitRevision =
			owned.AcknowledgedAt, owned.BaselineKnownAt, revision
		if sourceAfter.IsZero() {
			sourceAfter = record.History[0].BaselineKnownAt
		}
	}
	record.Restorations = append(record.Restorations, restoration)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	_, err = observeRBACCapacityPropagation(ctx, denialCheckAt, false, func(ctx context.Context, replica, attempt int) string {
		return observeRBACCapacityRequest(ctx, client, fixture.apis[replica], token, rbacCapacityRequest{
			ID: fmt.Sprintf("%s-root-restoration-%d-%02d", record.Owner, replica, attempt), Replica: replica,
			Subject: record.Subject, Namespace: record.Namespace, Action: "product.list", Expected: "deny",
		})
	})
	require.NoError(t, err, "root cleanup must restore denial on both APIs")
	waitRBACCapacityCapturedSources(t, sourcePaths, sourceAfter, func(header rbacCapacitySourceHeader, resources []json.RawMessage) bool {
		var origin struct {
			GitRevision string
			Root        bool
		}
		require.NoError(t, json.Unmarshal(header.Origin, &origin))
		return origin.Root && origin.GitRevision == restoration.GitRevision &&
			checkRBACCapacityAuthoredRootSource(t, header, resources, restoration)
	}, ctx)
	require.NoError(t, ctx.Err())
	record.CleanupError, record.RestoredAt = "", time.Now()
}

func checkRBACCapacityRootRestorationBytes(resource rbacCapacityAuthoredRootResource, current []byte) error {
	authored, err := json.Marshal(resource.Manifest)
	if err != nil {
		return err
	}
	if len(authored) > 64<<10 || !bytes.Equal(authored, current) {
		return errors.New("root restoration requires the exact bounded JSON bytes authored into the canonical manifest path")
	}
	return nil
}

func rbacCapacityRootRestorationAdmission(record *rbacCapacityRootPropagationReceipt, revision string) (*rbacCapacityAuthoredRootAdmission, error) {
	if record == nil || record.Owner == "" || revision == "" || len(record.History) == 0 {
		return nil, errors.New("root restoration requires its owned canonical receipt")
	}
	var owned *rbacCapacityAuthoredRootAdmission
	for index := range record.History {
		if record.History[index].GitRevision == revision {
			owned = &record.History[index]
		}
	}
	if record.Pending != nil && record.Pending.GitRevision == revision {
		owned = record.Pending
	}
	if owned == nil {
		return nil, errors.New("canonical root ref drifted outside the authored candidates")
	}
	base := record.History[0].Policy
	if owned.Policy.NamespaceUID != base.NamespaceUID || owned.Policy.BindingUID != base.BindingUID || owned.Policy.AdminSubject != base.AdminSubject {
		return nil, errors.New("root restoration refuses changed bootstrap authority")
	}
	for _, resource := range owned.Resources {
		kind, _ := resource.Manifest["kind"].(string)
		metadata, _ := resource.Manifest["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		labels, _ := metadata["labels"].(map[string]any)
		if (kind != "ClusterRole" && kind != "ClusterRoleBinding") || !rbacCapacitySeedName.MatchString(name) ||
			name == "initial-admin" || labels[rbacCapacityOwnerLabel] != record.Owner || resource.Generation < 1 {
			return nil, errors.New("root restoration refuses an unowned or out-of-scope resource")
		}
	}
	return owned, nil
}

func runRBACCapacityRootPropagationDiagnostic(t *testing.T, fixture rbacNativeFixture, plan rbacCapacitySeedPlan, count int,
	groupMembers []string, tokens map[string]string, receiptDirectory string, namespaceHistory []rbacCapacityAuthoredAdmission,
	sourcePaths, decisionPaths []string, root rbacCapacityRootPolicy,
	controls ...rbacCapacityRootDiagnosticControl,
) *rbacCapacityRootPropagationReceipt {
	t.Helper()
	var target rbacCapacityRequest
	ctx := t.Context()
	if len(controls) != 0 {
		require.Len(t, controls, 1)
		target, ctx = controls[0].Target, controls[0].Context
		require.NotNil(t, ctx)
		require.NotNil(t, controls[0].BeforeAdmission)
		require.NotNil(t, controls[0].BeforeRestore)
		fixture.cleanupContext = ctx
	} else {
		for offer := int64(0); offer < int64(20*count); offer++ {
			request, err := plan.request(offer, count, groupMembers)
			require.NoError(t, err)
			if request.Namespace == namespaceHistory[0].Namespace.Name && request.Action == "product.list" && request.Expected == "deny" {
				target = request
				break
			}
		}
	}
	require.NotEmpty(t, target.Subject, "root diagnostics require an independently ungranted organization principal")
	require.NotEmpty(t, tokens[target.Subject])
	namespaceHistory = append([]rbacCapacityAuthoredAdmission(nil), namespaceHistory...)
	for index := range namespaceHistory {
		namespaceHistory[index].Cases = map[string]bool{target.Subject + "/product.list": false}
	}
	record := rbacCapacityRootPropagationReceipt{Owner: plan.Owner, Namespace: target.Namespace, Subject: target.Subject}
	receipt, err := os.OpenFile(filepath.Join(receiptDirectory, "root-propagation-diagnostic.json"),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	require.NoError(t, err, "preserve earlier and incomplete root diagnostics")
	defer func() {
		require.NoError(t, json.NewEncoder(receipt).Encode(record))
		require.NoError(t, receipt.Sync())
		require.NoError(t, receipt.Close())
	}()
	name := plan.Prefix + "-root-view"
	path := "rbac/clusterrolebindings/" + name + ".yaml"
	roleUID := ""
	for _, api := range fixture.apis {
		_, absent := fixture.lookup(t, api, "ClusterRoleBinding", name)
		require.Len(t, absent.Errors, 1)
		require.Contains(t, string(absent.Errors[0]), `"code":"NOT_FOUND"`)
		role, response := fixture.lookup(t, api, "ClusterRole", "view")
		require.Empty(t, response.Errors)
		require.NotEmpty(t, role.Metadata.UID)
		if roleUID != "" {
			require.Equal(t, roleUID, role.Metadata.UID)
		}
		roleUID = role.Metadata.UID
	}
	var git rbacNativeGit
	if len(controls) != 0 && controls[0].Git != nil {
		git = *controls[0].Git
		require.Equal(t, fixture.gitURL, git.fixture.gitURL)
		require.True(t, fixture.token == git.fixture.token, "prepared clone must use the same credentials")
	} else {
		git = fixture.clone(t, "ClusterRoleBinding")
	}
	require.NoFileExists(t, filepath.Join(git.dir, path), "never replace an existing root manifest")
	tip, err := git.command(t, "rev-parse", "HEAD")
	require.NoError(t, err)
	canonical := strings.Fields(git.remoteTip(t))
	require.Len(t, canonical, 2)
	require.Equal(t, strings.TrimSpace(string(tip)), canonical[0])
	record.History = append(record.History, rbacCapacityAuthoredRootAdmission{
		Policy: root, GitRevision: "main@sha1:" + canonical[0], BaselineKnownAt: time.Now(),
		Cases: map[string]bool{target.Subject + "/product.list": false},
	})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	probes := [2]map[string]rbacCapacityProbeExpectation{{}, {}}
	record.ProbeStages = append(record.ProbeStages, probes)
	observe := func(ctx context.Context, replica, step, attempt int) string {
		request := target
		request.ID = fmt.Sprintf("%s-root-propagation-%d-%d-%02d", plan.Owner, step, replica, attempt)
		request.Replica = replica
		outcome := observeRBACCapacityRequest(ctx, client, fixture.apis[replica], tokens[target.Subject], request)
		probes[replica][request.ID] = rbacCapacityProbeExpectation{
			Subject: request.Subject, Namespace: request.Namespace, Action: request.Action,
			Allowed: outcome == "allow", ObservedAt: time.Now(),
		}
		return outcome
	}
	for replica := range probes {
		require.Equal(t, "deny", observe(ctx, replica, -1, 0), "the authored Namespace and bootstrap root must not grant the target")
	}
	bindingUID := ""
	customRoleName, customBindingName := plan.Prefix+"-root-rule", plan.Prefix+"-root-rule-binding"
	customRolePath, customBindingPath := "rbac/clusterroles/"+customRoleName+".yaml", "rbac/clusterrolebindings/"+customBindingName+".yaml"
	for _, api := range fixture.apis {
		for kind, name := range map[string]string{"ClusterRole": customRoleName, "ClusterRoleBinding": customBindingName} {
			_, absent := fixture.lookup(t, api, kind, name)
			require.Len(t, absent.Errors, 1)
			require.Contains(t, string(absent.Errors[0]), `"code":"NOT_FOUND"`)
		}
	}
	require.NoFileExists(t, filepath.Join(git.dir, customRolePath))
	require.NoFileExists(t, filepath.Join(git.dir, customBindingPath))
	defer func() {
		var stop func() error
		if len(controls) != 0 {
			stop = controls[0].BeforeRestore
		}
		err := runRBACCapacityRootCleanup(&record, stop, func() error {
			return runRBACCapacityCleanupAssertions(func() {
				restoreRBACCapacityRootDiagnostic(t, fixture, git, tokens[target.Subject], sourcePaths, &record)
			})
		})
		require.NoError(t, err, "bounded root restoration failed; preserve the failed receipt and exact canonical candidate")
	}()
	for step, allowed := range []bool{true, false, true, false, true, false, true, false} {
		require.NoError(t, ctx.Err(), "root phases must fit the declared preamble")
		if len(controls) != 0 {
			require.NoError(t, controls[0].BeforeAdmission(), "root admissions require a healthy live ordinary stream")
		}
		if step == 4 {
			for replica := range probes {
				require.NoError(t, verifyRBACCapacityAuthoredTraces(t, sourcePaths[replica], decisionPaths[replica],
					namespaceHistory, root, record.History, probes[replica]))
			}
			probes = [2]map[string]rbacCapacityProbeExpectation{{}, {}}
			record.ProbeStages = append(record.ProbeStages, probes)
		}
		admission := rbacCapacityAuthoredRootAdmission{Policy: root, Cases: map[string]bool{target.Subject + "/product.list": allowed}}
		if step == 0 || step == 4 {
			admission.PendingCases = maps.Clone(record.History[len(record.History)-1].Cases)
		}
		if step >= 4 {
			admission.Resources = append([]rbacCapacityAuthoredRootResource(nil), record.History[len(record.History)-1].Resources...)
			if step == 7 {
				paths := []string{"rm", "--"}
				for index := 1; index < len(admission.Resources); index++ {
					resource := &admission.Resources[index]
					resource.Removed = true
					kind := resource.Manifest["kind"].(string)
					name := resource.Manifest["metadata"].(map[string]any)["name"].(string)
					paths = append(paths, "rbac/"+strings.ToLower(kind)+"s/"+name+".yaml")
				}
				_, err := git.command(t, paths...)
				require.NoError(t, err)
			} else {
				role := fixture.manifest("ClusterRole", customRoleName)
				role["metadata"].(map[string]any)["labels"] = map[string]any{rbacCapacityOwnerLabel: plan.Owner}
				verb := "list"
				if !allowed {
					verb = "read"
				}
				role["spec"] = map[string]any{"rules": []any{map[string]any{"resources": []string{"product"}, "verbs": []string{verb}}}}
				if plan.ConditionalRoot {
					value := target.Namespace
					if !allowed {
						value = plan.Prefix + "-filler-root-condition"
					}
					rule := role["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
					rule["verbs"] = []string{"list"}
					rule["when"] = []any{map[string]any{"attr": "namespace", "op": "eq", "value": value}}
				}
				if step == 4 {
					binding := fixture.manifest("ClusterRoleBinding", customBindingName)
					binding["metadata"].(map[string]any)["labels"] = map[string]any{rbacCapacityOwnerLabel: plan.Owner}
					binding["spec"] = map[string]any{
						"roleRef":  map[string]any{"kind": "ClusterRole", "name": customRoleName},
						"subjects": []any{map[string]any{"kind": "User", "name": target.Subject}},
					}
					grants := []rbacCapacityAuthoredRootResource{
						{Manifest: role, Generation: 1},
						{Manifest: binding, Generation: 1, ReferenceGeneration: 1},
					}
					if plan.MaximumRootBatch {
						grants, err = rbacCapacityMaximumRootBatch(plan, grants)
						require.NoError(t, err)
					}
					admission.Resources = append(admission.Resources, grants...)
					for _, resource := range grants[1:] {
						kind := resource.Manifest["kind"].(string)
						name := resource.Manifest["metadata"].(map[string]any)["name"].(string)
						path := "rbac/" + strings.ToLower(kind) + "s/" + name + ".yaml"
						require.NoFileExists(t, filepath.Join(git.dir, path), "never replace an unrelated root manifest")
						git.write(t, path, resource.Manifest)
					}
				} else {
					admission.Resources[1].Manifest = role
					admission.Resources[1].Generation = int64(step - 3)
				}
				git.write(t, customRolePath, role)
			}
		} else if step == 3 {
			admission.Resources = append([]rbacCapacityAuthoredRootResource(nil), record.History[len(record.History)-1].Resources...)
			admission.Resources[0].Removed = true
			_, err := git.command(t, "rm", "--", path)
			require.NoError(t, err)
		} else {
			manifest := fixture.manifest("ClusterRoleBinding", name)
			manifest["metadata"].(map[string]any)["labels"] = map[string]any{rbacCapacityOwnerLabel: plan.Owner}
			subject := target.Subject
			if !allowed {
				subject = plan.Prefix + "-filler-root-propagation"
			}
			manifest["spec"].(map[string]any)["subjects"] = []any{map[string]any{"kind": "User", "name": subject}}
			admission.Resources = []rbacCapacityAuthoredRootResource{{
				Manifest: manifest, UID: bindingUID, RoleUID: roleUID,
				Generation: int64(step + 1), ReferenceGeneration: 1,
			}}
			git.write(t, path, manifest)
		}
		admission.Policy.TreeDigest, err = rbacCapacityAuthoredRootTreeDigest(root.AdminSubject, admission.Resources)
		require.NoError(t, err)
		sha := git.commit(t)
		admission.GitRevision = "main@sha1:" + sha
		record.Pending = &admission
		git.push(t)
		admission.AcknowledgedAt = time.Now()
		record.History = append(record.History, admission)
		record.Pending = &record.History[len(record.History)-1]
		latencies, err := observeRBACCapacityPropagation(ctx, admission.AcknowledgedAt, allowed,
			func(ctx context.Context, replica, attempt int) string { return observe(ctx, replica, step, attempt) })
		record.Latencies = append(record.Latencies, latencies)
		require.NoError(t, err, "both APIs must observe root changes within the original five-second successful-push bound")
		canonical := strings.Fields(git.remoteTip(t))
		require.Len(t, canonical, 2)
		require.Equal(t, sha, canonical[0])
		active := record.History[len(record.History)-1].Resources
		if step >= 4 {
			active = active[1:]
		}
		for _, api := range fixture.apis {
			readback := fixture
			readback.cleanupContext = ctx
			for base := 0; base < len(active); base += 4 {
				batch := active[base:min(base+4, len(active))]
				for index, response := range lookupRBACCapacityRootBatch(t, readback, api, batch) {
					pinned := &batch[index]
					current := response.Resource
					if pinned.Removed {
						if len(response.Errors) == 0 {
							require.Equal(t, pinned.UID, current.Metadata.UID)
							require.NotNil(t, current.Metadata.DeletionTimestamp)
						} else {
							require.Len(t, response.Errors, 1)
							require.Contains(t, string(response.Errors[0]), `"code":"NOT_FOUND"`)
						}
						continue
					}
					require.Empty(t, response.Errors)
					require.NotEmpty(t, current.Metadata.UID)
					if pinned.UID == "" {
						pinned.UID = current.Metadata.UID
					}
					require.Equal(t, pinned.UID, current.Metadata.UID)
					require.Equal(t, pinned.Generation, current.Metadata.Generation)
					require.Equal(t, rbacCapacitySeedSpec(t, pinned.Manifest["spec"], true),
						rbacCapacitySeedSpec(t, current.Spec, false))
				}
			}
		}
		if step < 3 {
			bindingUID = active[0].UID
		} else if step >= 4 {
			record.History[len(record.History)-1].Resources[2].RoleUID = active[0].UID
		}
		waitRBACCapacityCapturedSources(t, sourcePaths, admission.AcknowledgedAt, func(header rbacCapacitySourceHeader, resources []json.RawMessage) bool {
			var origin struct {
				GitRevision string
				Root        bool
			}
			require.NoError(t, json.Unmarshal(header.Origin, &origin))
			if !origin.Root || origin.GitRevision != admission.GitRevision {
				return false
			}
			return checkRBACCapacityAuthoredRootSource(t, header, resources, record.History[len(record.History)-1])
		}, ctx)
		record.VerifiedSources++
		record.Pending = nil
	}
	for replica := range probes {
		require.NoError(t, verifyRBACCapacityAuthoredTraces(t, sourcePaths[replica], decisionPaths[replica],
			namespaceHistory, root, record.History, probes[replica]))
	}
	record.CompletedAt = time.Now()
	require.NoError(t, ctx.Err(), "all root readbacks and source joins must fit the fixed preamble")
	record.Complete = true
	t.Log("bounded root binding and mutable ClusterRole rule grant/revoke/restore/removal joined to independently authored root and Namespace source histories; distributions and sustained acceptance remain unverified")
	return &record
}

type rbacCapacityAuthoredAdmission struct {
	Namespace      rbacCapacitySeedNamespace
	Receipt        rbacCapacitySeedReceipt
	AcknowledgedAt time.Time
	Cases          map[string]bool
	Generations    map[string]int64
	References     map[string]int64
}

func rbacCapacityRootNamespaceBaseline(plan rbacCapacitySeedPlan, receipts map[string]rbacCapacitySeedReceipt,
	subject string,
) (rbacCapacityAuthoredAdmission, error) {
	namespace := plan.namespace(5)
	seed := receipts[namespace.Name]
	if subject == "" || !seed.Complete || seed.Owner != plan.Owner || seed.Namespace != namespace.Name ||
		len(seed.Admissions) == 0 || seed.baseline != nil ||
		seed.GitRevision != seed.baselineAdmission().GitRevision || seed.baselineAdmission().AcknowledgedAt.IsZero() {
		return rbacCapacityAuthoredAdmission{}, errors.New("root-under-load requires the complete original seed of untouched Namespace index five")
	}
	generations, references := make(map[string]int64), make(map[string]int64)
	for _, resource := range namespace.Resources {
		kind := resource["kind"].(string)
		key := kind + "/" + resource["metadata"].(map[string]any)["name"].(string)
		generations[key] = 1
		if kind == "RoleBinding" {
			references[key] = 1
		}
	}
	return rbacCapacityAuthoredAdmission{
		Namespace: namespace, Receipt: seed, AcknowledgedAt: seed.baselineAdmission().AcknowledgedAt,
		Cases: map[string]bool{subject + "/product.list": false}, Generations: generations, References: references,
	}, nil
}

func verifyRBACCapacityDynamicTraces(t *testing.T, sourcePath, decisionPath string, history []rbacCapacityAuthoredAdmission,
	root rbacCapacityRootPolicy, expected map[string]rbacCapacityProbeExpectation, before ...time.Time,
) error {
	t.Helper()
	return verifyRBACCapacityAuthoredTraces(t, sourcePath, decisionPath, history, root, nil, expected, before...)
}

func verifyRBACCapacityAuthoredTraces(t *testing.T, sourcePath, decisionPath string, history []rbacCapacityAuthoredAdmission,
	root rbacCapacityRootPolicy, rootHistory []rbacCapacityAuthoredRootAdmission, expected map[string]rbacCapacityProbeExpectation, before ...time.Time,
) error {
	t.Helper()
	return verifyRBACCapacityAuthoredTracesWithDecisionBound(t, sourcePath, decisionPath, history, root, rootHistory, expected, 1<<30, before...)
}

func verifyRBACCapacityAuthoredTracesWithDecisionBound(t *testing.T, sourcePath, decisionPath string, history []rbacCapacityAuthoredAdmission,
	root rbacCapacityRootPolicy, rootHistory []rbacCapacityAuthoredRootAdmission, expected map[string]rbacCapacityProbeExpectation,
	decisionBound int64, before ...time.Time,
) error {
	t.Helper()
	return verifyRBACCapacityAuthoredTracesWithObservations(t, sourcePath, decisionPath, history, root, rootHistory,
		expected, decisionBound, nil, before...)
}

func verifyRBACCapacityAuthoredTracesWithObservations(t *testing.T, sourcePath, decisionPath string, history []rbacCapacityAuthoredAdmission,
	root rbacCapacityRootPolicy, rootHistory []rbacCapacityAuthoredRootAdmission, expected map[string]rbacCapacityProbeExpectation,
	decisionBound int64, observe func(rbacCapacityPolicyVector, string), before ...time.Time,
) error {
	t.Helper()
	require.True(t, len(history) >= 1 && len(history) <= 64 && (len(history) >= 2 || len(rootHistory) >= 2))
	require.LessOrEqual(t, len(rootHistory), 64)
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
	rootIndexes := make(map[string]int, len(rootHistory))
	for index, admission := range rootHistory {
		require.NotEmpty(t, admission.GitRevision)
		_, duplicate := rootIndexes[admission.GitRevision]
		require.False(t, duplicate, "independent root admissions cannot reinterpret a previous Git revision")
		require.Equal(t, root.NamespaceUID, admission.Policy.NamespaceUID)
		require.Equal(t, root.BindingUID, admission.Policy.BindingUID)
		require.Equal(t, root.AdminSubject, admission.Policy.AdminSubject)
		require.NotEmpty(t, admission.Cases)
		if index == 0 {
			require.False(t, admission.BaselineKnownAt.IsZero(), "pin bootstrap intent before observing workload decisions")
			require.True(t, admission.AcknowledgedAt.IsZero(), "a known bootstrap baseline is not a measured mutation acknowledgement")
			require.Equal(t, root.TreeDigest, admission.Policy.TreeDigest)
			require.Empty(t, admission.Resources, "root history begins with the independently pinned bootstrap policy")
		} else {
			require.True(t, admission.BaselineKnownAt.IsZero(), "mutations must retain their original successful-push clocks")
			require.False(t, admission.AcknowledgedAt.IsZero())
			require.True(t, admission.AcknowledgedAt.After(rootHistory[index-1].admittedAt()))
		}
		if len(admission.PendingCases) != 0 {
			require.Positive(t, index, "a known bootstrap baseline cannot be an unresolved authored mutation")
			require.NoError(t, validateRBACCapacityPendingRootCreation(rootHistory[index-1], admission),
				"pending root truth may only describe a new reference, never an earlier live reference or refreshed truth")
		}
		rootIndexes[admission.GitRevision] = index
	}
	source, err := openRBACCapacityPrivateFixture(sourcePath, 1<<30)
	require.NoError(t, err)
	defer source.Close()
	models := make(map[rbacCapacityPolicyRevision]rbacCapacityProbeSource)
	aliases := make([][]rbacCapacityPolicyRevision, len(history))
	rootAliases := make([][]rbacCapacityPolicyRevision, len(rootHistory))
	pendingRoots := make(map[rbacCapacityPolicyRevision]int)
	err = walkRBACCapacitySourceModelsWithClock(source, func(header rbacCapacitySourceHeader, resources []json.RawMessage, observedAt time.Time) error {
		if len(before) != 0 {
			if observedAt.IsZero() {
				return errors.New("sealed propagation source lacks its actual capture clock")
			}
			if observedAt.After(before[0]) {
				return nil
			}
		}
		var origin struct {
			Namespace, GitRevision string
			Root                   bool
		}
		var state struct{ TierID string }
		require.NoError(t, json.Unmarshal(header.Origin, &origin))
		require.NoError(t, json.Unmarshal(header.State, &state))
		revision := rbacCapacityPolicyRevision{TierUID: state.TierID, GitRevision: origin.GitRevision, Digest: header.ContentDigest}
		if _, found := models[revision]; found {
			return nil
		}
		if origin.Root {
			if len(rootHistory) == 0 {
				complete, err := checkRBACCapacityRootSource(header, resources, root)
				require.NoError(t, err)
				if !complete {
					return nil
				}
			} else {
				index, found := rootIndexes[origin.GitRevision]
				if !found {
					return nil
				}
				admission := rootHistory[index]
				if checkRBACCapacityAuthoredRootSource(t, header, resources, admission) {
					rootAliases[index] = append(rootAliases[index], revision)
				} else if len(admission.PendingCases) != 0 &&
					checkRBACCapacityRootSourceState(t, header, resources, admission, true) {
					pendingRoots[revision] = index
				} else {
					return nil
				}
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
			aliases[index] = append(aliases[index], revision)
		}
		models[revision] = rbacCapacityProbeSource{Namespace: origin.Namespace, Root: origin.Root, ObservedAt: observedAt}
		require.LessOrEqual(t, len(models), 8192, "diagnostic source aliases must remain bounded")
		return nil
	})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	for _, captured := range append(append([][]rbacCapacityPolicyRevision(nil), aliases...), rootAliases...) {
		require.NotEmpty(t, captured, "each admitted policy must have an independently verified complete source")
	}
	for _, probe := range expected {
		require.False(t, probe.ObservedAt.IsZero(), "HTTP observations must retain their own completion clocks")
	}
	oracle := newRBACCapacityObservedPolicyIndex(5 * time.Second)
	for index, admission := range history {
		if len(rootHistory) != 0 {
			if len(admission.Cases) != len(rootHistory[0].Cases) {
				return errors.New("RBAC root and Namespace history must independently declare the same probe domain")
			}
			for probe := range admission.Cases {
				if _, found := rootHistory[0].Cases[probe]; !found {
					return errors.New("RBAC root and Namespace history disagree on the independent probe domain")
				}
			}
		}
		var superseded time.Time
		if index+1 < len(history) {
			superseded = history[index+1].AcknowledgedAt
		}
		if err := oracle.add(aliases[index], false, admission.AcknowledgedAt, superseded, admission.Cases); err != nil {
			return err
		}
	}
	if len(rootHistory) == 0 {
		cases := make(map[string]bool, len(history[0].Cases))
		for probe := range history[0].Cases {
			cases[probe] = false
		}
		for revision, model := range models {
			if model.Root {
				if err := oracle.add([]rbacCapacityPolicyRevision{revision}, true, history[0].AcknowledgedAt, time.Time{}, cases); err != nil {
					return err
				}
			}
		}
	} else {
		for index, admission := range rootHistory {
			if len(admission.Cases) != len(history[0].Cases) {
				return errors.New("RBAC root and Namespace history must independently declare the same probe domain")
			}
			for probe := range history[0].Cases {
				if _, found := admission.Cases[probe]; !found {
					return errors.New("RBAC root and Namespace history disagree on the independent probe domain")
				}
			}
			var superseded time.Time
			if index+1 < len(rootHistory) {
				superseded = rootHistory[index+1].AcknowledgedAt
			}
			if err := oracle.add(rootAliases[index], true, admission.admittedAt(), superseded, admission.Cases); err != nil {
				return err
			}
		}
	}
	decisions, err := openRBACCapacityPrivateFixture(decisionPath, decisionBound)
	require.NoError(t, err)
	defer decisions.Close()
	return checkRBACCapacityObservedTracesWithBounds(decisions, expected, models, 128, decisionBound,
		func(vector rbacCapacityPolicyVector, id string, allowed bool) error {
			probe := expected[id]
			if observe != nil {
				observe(vector, id)
			}
			if index, pending := pendingRoots[vector.Cluster]; pending {
				at := rootHistory[index].AcknowledgedAt
				if probe.ObservedAt.Before(at) || probe.ObservedAt.Sub(at) > 5*time.Second {
					return errors.New("RBAC unresolved initial root grant is outside its original acknowledgement window")
				}
				// This source is verified inactive, not a ready alias of the new grant.
				vector.Cluster = rootAliases[index-1][0]
			}
			return oracle.check(vector, probe.Subject+"/"+probe.Action, allowed, probe.ObservedAt)
		})
}

type rbacCapacityObservedTierPolicy struct {
	rbacCapacityTierCommit
	root  bool
	cases map[string]bool
}

type rbacCapacityObservedPolicyIndex struct {
	bound      time.Duration
	policies   map[rbacCapacityPolicyRevision]rbacCapacityObservedTierPolicy
	admissions map[[2]string]rbacCapacityObservedTierPolicy
	scopes     map[string]bool
}

func newRBACCapacityObservedPolicyIndex(bound time.Duration) *rbacCapacityObservedPolicyIndex {
	return &rbacCapacityObservedPolicyIndex{
		bound: bound, policies: make(map[rbacCapacityPolicyRevision]rbacCapacityObservedTierPolicy),
		admissions: make(map[[2]string]rbacCapacityObservedTierPolicy), scopes: make(map[string]bool),
	}
}

func (o *rbacCapacityObservedPolicyIndex) add(aliases []rbacCapacityPolicyRevision, root bool,
	admitted, superseded time.Time, cases map[string]bool,
) error {
	if o.bound <= 0 || admitted.IsZero() || !superseded.IsZero() && !superseded.After(admitted) ||
		len(aliases) == 0 || len(cases) == 0 || len(cases) > 128 {
		return errors.New("RBAC observed policy requires bounded independent truth and original admission clocks")
	}
	for probe := range cases {
		if probe == "" {
			return errors.New("RBAC observed policy requires a declared probe identity")
		}
	}
	policy := rbacCapacityObservedTierPolicy{
		rbacCapacityTierCommit: rbacCapacityTierCommit{admitted: admitted, superseded: superseded},
		root:                   root, cases: maps.Clone(cases),
	}
	for _, revision := range aliases {
		if revision.TierUID == "" || revision.GitRevision == "" || revision.Digest == "" {
			return errors.New("RBAC observed policy revision identity is incomplete")
		}
		if scope, found := o.scopes[revision.TierUID]; found && scope != root {
			return errors.New("RBAC observed policy cannot reinterpret a tier's scope")
		}
		if previous, found := o.admissions[[2]string{revision.TierUID, revision.GitRevision}]; found && !reflect.DeepEqual(previous, policy) {
			return errors.New("RBAC observed policy cannot reinterpret an alias or refresh an original clock")
		}
	}
	for _, revision := range aliases {
		if _, found := o.policies[revision]; !found && len(o.policies) >= 8192 {
			return errors.New("RBAC observed policy source history exceeds its bound")
		}
		o.policies[revision] = policy
		o.admissions[[2]string{revision.TierUID, revision.GitRevision}] = policy
		o.scopes[revision.TierUID] = root
	}
	return nil
}

func (o *rbacCapacityObservedPolicyIndex) check(vector rbacCapacityPolicyVector, probe string, allowed bool, at time.Time) error {
	if vector.Cluster.TierUID == vector.Namespace.TierUID {
		return errors.New("RBAC observed root and Namespace identities must be independent")
	}
	expected := false
	for index, revision := range []rbacCapacityPolicyRevision{vector.Cluster, vector.Namespace} {
		policy, found := o.policies[revision]
		if !found || policy.root != (index == 0) {
			return errors.New("RBAC decision evaluated an independently unverified policy revision or scope")
		}
		value, found := policy.cases[probe]
		if !found {
			return errors.New("RBAC decision lacks independently declared tier truth")
		}
		if at.Before(policy.admitted) {
			return errors.New("RBAC decision predates an independently admitted tier")
		}
		if !policy.superseded.IsZero() && at.After(policy.superseded.Add(o.bound)) {
			return errors.New("RBAC decision retained a superseded tier beyond the propagation bound")
		}
		expected = expected || value
	}
	if expected != allowed {
		return errors.New("RBAC decision disagrees with independent committed policy")
	}
	return nil
}

func rbacCapacityScheduledOffers(duration time.Duration, rate int64, period ...time.Duration) int64 {
	unit := time.Second
	if len(period) == 1 {
		unit = period[0]
	}
	if duration <= 0 || rate <= 0 || len(period) > 1 || (unit != time.Second && unit != time.Minute) {
		return 0
	}
	wholePeriods, remainder := int64(duration/unit), int64(duration%unit)
	if wholePeriods > math.MaxInt64/rate || remainder > 0 && rate > math.MaxInt64/remainder {
		return math.MaxInt64
	}
	whole, partial := wholePeriods*rate, remainder*rate/int64(unit)
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
	stride := int64((namespaces + 9) / 10)
	return int((offer/10 + offer%10*stride) % int64(namespaces)), probe
}

type rbacCapacityAccounting struct {
	Offered, Completed, Dropped, Allowed, Denied, Invalid, Unavailable, Wrong int64
}

type rbacCapacityScheduleConfig struct {
	Duration, Drain, Period time.Duration
	Rate                    int64
	Workers, Pending        int
}

func (c rbacCapacityScheduleConfig) period() time.Duration {
	if c.Period == 0 {
		return time.Second
	}
	return c.Period
}

func (c rbacCapacityScheduleConfig) validate() error {
	if c.Duration <= 0 || c.Duration > time.Hour || c.Drain <= 0 || c.Drain > 30*time.Second ||
		(c.period() != time.Second && c.period() != time.Minute) ||
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
	scheduled := rbacCapacityScheduledOffers(cfg.Duration, cfg.Rate, cfg.period())
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
	pendingWindow := time.Duration(int64(cfg.Pending) * int64(cfg.period()) / cfg.Rate)
schedule:
	for offer := int64(0); offer < scheduled; offer++ {
		due := started.Add(time.Duration(offer * int64(cfg.period()) / cfg.Rate))
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

const rbacCapacityMutationHistoryLimit = 1500 + 4 + 4

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

func (o *rbacCapacityOracle) admitNamespace(uid, revision string, at time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if uid == "" || revision == "" || at.IsZero() || o.bound <= 0 {
		return errors.New("RBAC Namespace admission requires an independently pinned identity and clock")
	}
	tier := o.tiers[uid]
	if tier == nil {
		tier = &rbacCapacityTierHistory{commits: make(map[string]rbacCapacityTierCommit)}
		o.tiers[uid] = tier
	}
	if tier.root {
		return errors.New("RBAC Namespace admission scope or bounded history is invalid")
	}
	if previous, found := tier.commits[revision]; found {
		if !previous.admitted.Equal(at) {
			return errors.New("RBAC Namespace admission cannot refresh an original clock")
		}
		return nil
	}
	if len(tier.commits) >= rbacCapacityMutationHistoryLimit {
		return errors.New("RBAC Namespace admission scope or bounded history is invalid")
	}
	if tier.latest != "" {
		previous := tier.commits[tier.latest]
		if !at.After(previous.admitted) {
			return errors.New("RBAC Namespace admissions must preserve original ordering")
		}
		previous.superseded = at
		tier.commits[tier.latest] = previous
	}
	tier.commits[revision] = rbacCapacityTierCommit{admitted: at}
	tier.latest = revision
	return nil
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
	return readRBACCapacityPrivateFixtureWithLimit(path, 1<<20)
}

func readRBACCapacityPrivateFixtureWithLimit(path string, limit int64) ([]byte, error) {
	if limit <= 0 || limit > 16<<20 {
		return nil, errors.New("RBAC private fixture requires a declared read bound of at most 16 MiB")
	}
	file, err := openRBACCapacityPrivateFixture(path, limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("RBAC private fixture exceeded its read bound")
	}
	return data, nil
}

type rbacCapacityProcess struct {
	ID, Role, Owner, Epoch, Checkpoint     string
	Executable, ExecutableDigest, Endpoint string
	Running                                bool
	BuildReceipt                           string `json:",omitempty"`
}

type rbacCapacityTopology struct {
	Owner                                         string
	GitSmartHTTP                                  string
	NativeNamespaces, NativeRoles, NativeBindings int
	Processes                                     []rbacCapacityProcess
}

func (v rbacCapacityTopology) validate() error {
	return v.validateCounts(1000)
}

func (v rbacCapacityTopology) validateCounts(count int) error {
	if count < 2 || count > 1000 || v.Owner == "" || v.NativeNamespaces != count ||
		v.NativeRoles != count*10 || v.NativeBindings != count*50 {
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

func (v rbacCapacityTopology) verifyLive(ctx context.Context) error {
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for _, process := range v.Processes {
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := func() error {
			if container, found := strings.CutPrefix(process.ID, "docker:"); found {
				if process.Role != "scylla" || len(container) != 64 {
					return errors.New("RBAC topology only permits explicitly owned Scylla containers")
				}
				output, err := exec.CommandContext(attempt, "docker", "inspect", container).Output()
				if err != nil {
					return err
				}
				var facts []struct {
					ID     string
					Config struct{ Labels map[string]string }
					State  struct {
						Running   bool
						StartedAt string
					}
				}
				if err := json.Unmarshal(output, &facts); err != nil {
					return err
				}
				if len(facts) != 1 || facts[0].ID != container || !facts[0].State.Running ||
					facts[0].State.StartedAt != process.Epoch || facts[0].Config.Labels["dev.gitstore.task"] != v.Owner {
					return errors.New("RBAC Scylla ownership or process epoch changed")
				}
				return nil
			}
			id, found := strings.CutPrefix(process.ID, "native:")
			pid, err := strconv.Atoi(id)
			if !found || err != nil || pid <= 0 || strconv.Itoa(pid) != id {
				return errors.New("RBAC native topology requires canonical live process identities")
			}
			command := func(field string) (string, error) {
				cmd := exec.CommandContext(attempt, "ps", "-p", id, "-o", field+"=")
				cmd.Env = append(os.Environ(), "LC_ALL=C")
				output, err := cmd.Output()
				return strings.TrimSpace(string(output)), err
			}
			epoch, err := command("lstart")
			if err != nil || epoch != process.Epoch {
				return errors.New("RBAC native process stopped or changed epoch")
			}
			args, err := command("command")
			if err != nil || !filepath.IsAbs(process.Executable) || filepath.Clean(process.Executable) != process.Executable ||
				!(args == process.Executable || strings.HasPrefix(args, process.Executable+" ")) {
				return errors.New("RBAC live process differs from its explicitly owned executable")
			}
			parent, err := os.Lstat(filepath.Dir(process.Executable))
			if err != nil || !parent.IsDir() || parent.Mode().Perm()&0o077 != 0 {
				return errors.New("RBAC native executable must remain in its private owned fixture")
			}
			file, err := os.Open(process.Executable)
			if err != nil {
				return err
			}
			if err := verifyRBACCapacityRunningExecutable(attempt, id, file); err != nil {
				return errors.Join(err, file.Close())
			}
			hash := sha256.New()
			read, readErr := io.Copy(hash, io.LimitReader(file, 256<<20+1))
			identityErr := verifyRBACCapacityRunningExecutable(attempt, id, file)
			closeErr := file.Close()
			if identityErr != nil {
				return errors.Join(identityErr, closeErr)
			}
			if readErr != nil || closeErr != nil || read <= 0 || read > 256<<20 ||
				hex.EncodeToString(hash.Sum(nil)) != process.ExecutableDigest {
				return errors.New("RBAC native executable source identity changed")
			}
			if process.Role == "controller" {
				checkpoint, err := os.Lstat(process.Checkpoint)
				if err != nil || !checkpoint.IsDir() || checkpoint.Mode().Perm()&0o077 != 0 {
					return errors.New("RBAC controller checkpoint is not an independent private directory")
				}
			}
			if process.Role == "git" {
				host, port, err := net.SplitHostPort(process.Endpoint)
				number, portErr := strconv.Atoi(port)
				if err != nil || host == "" || portErr != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
					return errors.New("RBAC native Git requires its direct gRPC listening address, not an API smart-HTTP URL")
				}
				connection, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(attempt, "tcp", process.Endpoint)
				if err != nil {
					return err
				}
				return connection.Close()
			}
			request, err := http.NewRequestWithContext(attempt, http.MethodGet, strings.TrimRight(process.Endpoint, "/")+"/health", nil)
			if err != nil {
				return err
			}
			if (request.URL.Scheme != "http" && request.URL.Scheme != "https") || request.URL.Host == "" ||
				request.URL.User != nil || request.URL.RawQuery != "" || request.URL.Fragment != "" || request.URL.Path != "/health" {
				return errors.New("RBAC process health requires a direct credential-free base URL")
			}
			response, err := client.Do(request)
			if err != nil {
				return err
			}
			closeErr = response.Body.Close()
			if response.StatusCode != http.StatusOK || closeErr != nil {
				return errors.New("RBAC directly addressed process is not healthy")
			}
			return nil
		}()
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func preflightRBACCapacityDeployment(t *testing.T) (rbacCapacityNativeSeedConfig, rbacCapacityTopology) {
	t.Helper()
	require.Equal(t, "1", os.Getenv("RBAC_TEST_RUN"))
	fixture := ownedRBACNativeFixture(t)
	content, err := readRBACCapacityPrivateFixture(os.Getenv("CAPACITY_RBAC_SEED_FILE"))
	require.NoError(t, err)
	var config rbacCapacityNativeSeedConfig
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&config))
	require.NoError(t, validateRBACCapacityMode(config))
	require.False(t, config.VerifyRetained, "retained-source replay is not a public seeding or sustained acceptance receipt")
	require.Equal(t, config.Mode, os.Getenv("MODE"))
	if config.Mode == "alpha" {
		require.Equal(t, os.Getenv("CAPACITY_EVIDENCE_DIR"), config.ReceiptDirectory,
			"public alpha evidence must use the selected private run directory")
		require.NotEmpty(t, rbacCapacitySourceRevision(t))
	}
	require.NoError(t, config.Plan.validate())
	require.NotNil(t, config.K6Workload, "public RBAC selection must execute ordinary k6 traffic")
	workload := config.K6Workload
	require.NoError(t, validateRBACCapacityMutationSelection(config), "public preflight must refuse incompatible selected phases")
	require.NoError(t, validateRBACCapacitySeedResume(config), "public preflight must refuse incompatible retained-seed continuation")
	if workload.Mutation != nil {
		require.NoError(t, validateRBACCapacityMutation(config.Plan, config.Count, workload, config.InitialAdminSubject))
		require.False(t, config.DecisionWorkload != nil && config.DecisionWorkload.PropagationDiagnostic,
			"four-tier mutation selection cannot silently skip selected sequential propagation")
	}
	require.NoError(t, validateRBACCapacityK6Diagnostic(workload))
	_, err = config.Plan.request(0, config.Count, workload.GroupMembers)
	require.NoError(t, err, "group membership must be independently declared before any seeding")
	require.Len(t, config.SourceModelFiles, 2)
	require.Len(t, config.DecisionLogFiles, 2)
	content, err = readRBACCapacityPrivateFixture(os.Getenv("CAPACITY_RBAC_TOPOLOGY_FILE"))
	require.NoError(t, err)
	var topology rbacCapacityTopology
	decoder = json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&topology))
	require.NoError(t, topology.validateCounts(config.Count))
	require.Equal(t, fixture.gitURL, topology.GitSmartHTTP, "native pushes must use the declared API smart-HTTP endpoint")
	var apis []string
	for _, process := range topology.Processes {
		if process.Role == "api" {
			apis = append(apis, strings.TrimRight(process.Endpoint, "/"))
		}
	}
	require.ElementsMatch(t, fixture.apis, apis, "both ordinary APIs must be directly probed owned processes")
	require.NoError(t, topology.verifyLive(t.Context()))
	if config.Mode == "alpha" {
		sealRBACCapacityProvenance(t, config, topology, true)
	}
	return config, topology
}

func TestRBACCapacityDeploymentPreflight(t *testing.T) {
	if os.Getenv("RBAC_TEST_CAPACITY_VERIFY_DEPLOYMENT") != "1" {
		t.Skip("owned RBAC deployment preflight must be explicitly selected")
	}
	preflightRBACCapacityDeployment(t)
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
	if os.Getenv("CAPACITY_RBAC_WORKLOAD") == "1" {
		config, topology := preflightRBACCapacityDeployment(t)
		require.Equal(t, "1", os.Getenv("RBAC_TEST_CAPACITY_SEED_RUN"))
		require.Equal(t, os.Getenv("CAPACITY_RBAC_SEED_FILE"), os.Getenv("RBAC_TEST_CAPACITY_SEED_FILE"))
		TestRBACCapacityNativeSeed(t)
		require.NoError(t, topology.verifyLive(t.Context()), "owned processes and epochs must remain unchanged through the ordinary workload")
		directory, err := filepath.Abs(config.ReceiptDirectory)
		require.NoError(t, err)
		t.Setenv("RBAC_TEST_CAPACITY_VERIFY_K6", "1")
		t.Setenv("CAPACITY_RBAC_EVIDENCE_DIR", directory)
		TestRBACCapacityK6EvidenceVerifier(t)
		return
	}
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
