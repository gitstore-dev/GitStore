// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"context"
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

type compiledCondition struct {
	Condition
	values map[string]struct{}
}
type conditionValue struct {
	value string
	valid bool
}
type conditionValues struct {
	namespace, tier, visibility conditionValue
}
type compiledRule struct {
	index      int
	names      map[string]struct{}
	conditions []compiledCondition
}
type compiledRole struct {
	id       int
	byAction map[string][]*compiledRule
}
type compiledBinding struct {
	id         int
	diagnostic string
	roleName   string
	namespace  string
	cluster    bool
	role       *compiledRole
}
type compiledPolicy struct {
	digest    string
	bySubject map[SubjectRef]bindingSet
	bindings  []*compiledBinding
	ruleCount int
	roleCount int
}

// Bounds match policy validation. Bitsets make overlapping group bindings a
// bounded union, rather than repeatedly expanding/sorting the same candidates.
type bindingSet [(maxPolicyBindings + 63) / 64]uint64

// RBACLocalProvider publishes immutable snapshots; evaluation never waits for
// policy parsing, compilation or another evaluation.
type RBACLocalProvider struct {
	reloadMu    sync.Mutex
	snapshot    atomic.Pointer[compiledPolicy]
	path        string
	logger      *zap.Logger
	activeUsers []*auth.UserProfile
}

func New(cfg config.RBACConfig, logger *zap.Logger) (*RBACLocalProvider, error) {
	path := cfg.PolicyFile
	if path == "" {
		path = "policy.yaml"
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	p := &RBACLocalProvider{path: path, logger: logger}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}
func (p *RBACLocalProvider) Name() string { return "rbac-local" }

func (p *RBACLocalProvider) AuthorizationSnapshot() (auth.AuthorizationSnapshot, bool) {
	snapshot := p.snapshot.Load()
	if snapshot == nil {
		return auth.AuthorizationSnapshot{APIVersion: PolicyAPIVersion}, true
	}
	return auth.AuthorizationSnapshot{
		Loaded: true, APIVersion: PolicyAPIVersion, Digest: snapshot.digest,
		Roles: snapshot.roleCount, Bindings: len(snapshot.bindings), Rules: snapshot.ruleCount,
	}, true
}

func (p *RBACLocalProvider) Reload() error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()
	policy, err := loadPolicy(p.path)
	if err != nil {
		p.logger.Warn("rbac-local reload rejected")
		return err
	}
	candidate := compilePolicy(policy)
	validator := &RBACLocalProvider{}
	validator.snapshot.Store(candidate)
	if err := auth.ValidateActiveUserPermissions(validator, p.activeUsers); err != nil {
		p.logger.Warn("rbac-local reload rejected: ineffective local-user grant")
		return err
	}
	p.snapshot.Store(candidate)
	p.logger.Info("rbac-local policy reloaded", zap.String("policy_digest", candidate.digest))
	return nil
}

// SetActiveUsers installs the startup-equivalent validation for direct Reload
// calls as well as complete registry reloads. Profiles are copied, not shared.
func (p *RBACLocalProvider) SetActiveUsers(users []*auth.UserProfile) error {
	p.reloadMu.Lock()
	defer p.reloadMu.Unlock()
	if err := auth.ValidateActiveUserPermissions(p, users); err != nil {
		return err
	}
	copies := make([]*auth.UserProfile, 0, len(users))
	for _, user := range users {
		if user != nil {
			copy := *user
			copy.Groups = append([]string(nil), user.Groups...)
			copies = append(copies, &copy)
		}
	}
	p.activeUsers = copies
	return nil
}

func compilePolicy(policy *Policy) *compiledPolicy {
	snapshot := &compiledPolicy{
		digest: policy.digest, bySubject: map[SubjectRef]bindingSet{},
		roleCount: len(policy.ClusterRoles) + len(policy.Roles),
	}
	roles := map[RoleRef]*compiledRole{}
	for _, tier := range []struct {
		kind  string
		roles map[string]RoleDefinition
	}{{"ClusterRole", policy.ClusterRoles}, {"Role", policy.Roles}} {
		names := make([]string, 0, len(tier.roles))
		for name := range tier.roles {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			role := &compiledRole{id: len(roles), byAction: map[string][]*compiledRule{}}
			for i, rule := range tier.roles[name].Rules {
				compiled := &compiledRule{index: i, names: stringSet(rule.ResourceNames)}
				snapshot.ruleCount++
				for _, condition := range rule.When {
					compiled.conditions = append(compiled.conditions, compiledCondition{Condition: condition, values: stringSet(condition.Values)})
				}
				for _, action := range rule.actions {
					role.byAction[action.Action] = append(role.byAction[action.Action], compiled)
				}
			}
			roles[RoleRef{Kind: tier.kind, Name: name}] = role
		}
	}
	for _, tier := range []struct {
		cluster  bool
		name     string
		bindings []Binding
	}{{true, "clusterRoleBindings", policy.ClusterRoleBindings}, {false, "roleBindings", policy.RoleBindings}} {
		for i, b := range tier.bindings {
			compiled := &compiledBinding{id: len(snapshot.bindings), diagnostic: fmt.Sprintf("%s[%d]", tier.name, i), roleName: b.RoleRef.Kind + "/" + b.RoleRef.Name, cluster: tier.cluster, role: roles[b.RoleRef]}
			if b.Namespace != nil {
				compiled.namespace = *b.Namespace
			}
			snapshot.bindings = append(snapshot.bindings, compiled)
			for _, s := range b.Subjects {
				set := snapshot.bySubject[s]
				set[compiled.id/64] |= uint64(1) << (compiled.id % 64)
				snapshot.bySubject[s] = set
			}
		}
	}
	return snapshot
}
func stringSet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[v] = struct{}{}
	}
	return out
}

// principalSubjects defends the provider boundary as well as AuthN. A prefix
// alone never proves ServiceAccount identity; groups are already trusted facts.
func principalSubjects(p *auth.Principal, subjects []SubjectRef) ([]SubjectRef, bool) {
	if p == nil || !validIdentifier(p.Subject) || p.AuthMethod == "" || len(p.Groups) > MaxPrincipalGroups {
		return nil, false
	}
	var subject SubjectRef
	switch {
	case strings.HasPrefix(p.Subject, "system:serviceaccount:"):
		name := strings.TrimPrefix(p.Subject, "system:serviceaccount:")
		if !validIdentifier(name) || strings.ContainsAny(name, ":/") || p.ServiceAccountUID == "" || (p.AuthMethod != "serviceaccount-jwt" && p.AuthMethod != "serviceaccount-assertion") {
			return nil, false
		}
		subject = SubjectRef{Kind: "ServiceAccount", Name: name}
	case p.Subject == "system:api":
		if p.AuthMethod != "internal" {
			return nil, false
		}
		subject = SubjectRef{Kind: "User", Name: p.Subject}
	case strings.HasPrefix(p.Subject, "system:") || strings.HasPrefix(p.Subject, "serviceaccount:"):
		return nil, false
	case p.AuthMethod == "none":
		if p.Subject != "anon" {
			return nil, false
		}
	default:
		if p.ServiceAccountUID != "" || p.AuthMethod == "serviceaccount-jwt" || p.AuthMethod == "serviceaccount-assertion" {
			return nil, false
		}
		subject = SubjectRef{Kind: "User", Name: p.Subject}
	}
	if subject.Kind != "" {
		subjects = append(subjects, subject)
	}
	for _, g := range p.Groups {
		if !validIdentifier(g) {
			return nil, false
		}
		if (p.AuthMethod == "none" && (g == "system:authenticated" || g == "system:serviceaccounts" || g == "system:masters")) || (p.AuthMethod != "none" && g == "system:unauthenticated") || (g == "system:serviceaccounts" && subject.Kind != "ServiceAccount") {
			return nil, false
		}
		// Binding-set union below is idempotent, including duplicate groups.
		subjects = append(subjects, SubjectRef{Kind: "Group", Name: g})
	}
	return subjects, true
}

func (p *RBACLocalProvider) Authorize(_ context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	snapshot := p.snapshot.Load()
	deny := func(reason string) (auth.Decision, error) {
		decision := auth.Deny(p.Name(), reason)
		if snapshot != nil {
			decision.PolicyDigest = snapshot.digest
		}
		return decision, nil
	}
	definition, registered := auth.LookupAction(action)
	var subjectBuffer [MaxPrincipalGroups + 1]SubjectRef
	subjects, valid := principalSubjects(principal, subjectBuffer[:0])
	if snapshot == nil || !registered || resource.Kind != definition.Kind || !valid || len(resource.Name) > 256 || len(resource.OwnerSub) > 256 {
		return deny("invalid authorization request")
	}
	target := ""
	if definition.Verb == "transfer" {
		var ok bool
		target, ok = targetOwner(resource.Attrs["targetOwnerRef"])
		if !ok {
			return deny("transfer requires valid targetOwnerRef")
		}
	}
	if slices.Contains(principal.Groups, "system:masters") {
		decision := auth.Allow(p.Name(), "trusted system:masters; policy="+snapshot.digest)
		decision.PolicyDigest = snapshot.digest
		return decision, nil
	}
	// Resolve candidate bindings once, in document order. Duplicate groups and
	// subjects cannot multiply rule evaluation. Rule bodies are shared by role.
	var candidates bindingSet
	for _, subject := range subjects {
		for word, matches := range snapshot.bySubject[subject] {
			candidates[word] |= matches
		}
	}
	attributes := readConditionValues(resource.Attrs)
	namespace := attributes.namespace.value
	owner := ownerMatches(resource.OwnerSub, principal)
	member := ownerMatches(target, principal)
	var visited [(maxPolicyRoles*2 + 63) / 64]uint64 // cluster/namespaced provenance
	reason := "no matching binding"
	for word, matches := range candidates {
		for matches != 0 {
			bit := bits.TrailingZeros64(matches)
			matches &= matches - 1
			binding := snapshot.bindings[word*64+bit]
			rules := binding.role.byAction[action]
			if len(rules) == 0 {
				continue
			}
			if !binding.cluster && (definition.Scope != "namespace" || namespace == "" || namespace != binding.namespace) {
				reason = "binding scope mismatch"
				continue
			}
			// All rule inputs are fixed for this request after binding scope
			// checks. Cluster transfer overrides require separate provenance.
			key := binding.role.id * 2
			if binding.cluster {
				key++
			}
			mask := uint64(1) << (key % 64)
			if visited[key/64]&mask != 0 {
				continue
			}
			visited[key/64] |= mask
			for _, rule := range rules {
				if len(rule.names) > 0 {
					if _, ok := rule.names[resource.Name]; !ok || resource.Name == "" {
						reason = "instance mismatch"
						continue
					}
				}
				matched := true
				for i := range rule.conditions {
					if !conditionMatches(&rule.conditions[i], &attributes, owner, member) {
						matched = false
						reason = "condition missing or false"
						break
					}
				}
				if !matched {
					continue
				}
				unconditional := len(rule.conditions) == 0
				if definition.Verb == "transfer" && (!(owner || (binding.cluster && unconditional)) || !(member || unconditional)) {
					reason = "transfer constraint"
					continue
				}
				decision := auth.Allow(p.Name(), fmt.Sprintf("policy=%s binding=%s role=%s rule=%d", snapshot.digest, binding.diagnostic, binding.roleName, rule.index))
				decision.PolicyDigest = snapshot.digest
				return decision, nil
			}
		}
	}
	return deny("policy=" + snapshot.digest + " " + reason)
}

func ownerMatches(owner string, p *auth.Principal) bool {
	return owner != "" && (owner == p.Subject || slices.Contains(p.Groups, owner))
}
func targetOwner(value any) (string, bool) {
	target, ok := value.(map[string]any)
	if !ok || len(target) != 2 {
		return "", false
	}
	kind, ok := target["kind"].(string)
	if !ok {
		return "", false
	}
	name, ok := target["name"].(string)
	if !ok || !validIdentifier(name) {
		return "", false
	}
	switch kind {
	case "User":
		if strings.HasPrefix(name, "system:") || strings.HasPrefix(name, "serviceaccount:") {
			return "", false
		}
		return datastore.EncodeOwnerSubject(datastore.OwnerKindUser, name), true
	case "Group":
		if strings.HasPrefix(name, "system:") && !strings.HasPrefix(name, "system:group:") {
			return "", false
		}
		name = strings.TrimPrefix(name, "system:group:")
		if !validIdentifier(name) {
			return "", false
		}
		return datastore.EncodeOwnerSubject(datastore.OwnerKindGroup, name), true
	case "ServiceAccount":
		if strings.ContainsAny(name, ":/") {
			return "", false
		}
		return datastore.EncodeOwnerSubject(datastore.OwnerKindServiceAccount, name), true
	default:
		return "", false
	}
}
func readConditionValues(attrs map[string]any) conditionValues {
	read := func(attribute string) conditionValue {
		value, ok := attrs[attribute].(string)
		return conditionValue{value: value, valid: ok && len(value) <= 256}
	}
	return conditionValues{namespace: read("namespace"), tier: read("tier"), visibility: read("visibility")}
}

func conditionMatches(c *compiledCondition, attributes *conditionValues, owner, member bool) bool {
	if c.Token == "owner" {
		return owner
	}
	if c.Token == "targetOwnerMember" {
		return member
	}
	var attribute conditionValue
	switch c.Attr {
	case "namespace":
		attribute = attributes.namespace
	case "tier":
		attribute = attributes.tier
	case "visibility":
		attribute = attributes.visibility
	default:
		return false
	}
	if !attribute.valid {
		return false
	}
	value := attribute.value
	switch c.Op {
	case "eq":
		return value == *c.Value
	case "neq":
		return value != *c.Value
	case "in":
		_, ok := c.values[value]
		return ok
	case "notIn":
		_, ok := c.values[value]
		return !ok
	}
	return false
}

// HasAnyRoleBindingFor is retained as the existing startup hook while its
// caller is tightened to validate every active identity. No principal roles
// participate in authorization.
func (p *RBACLocalProvider) HasAnyRoleBindingFor(subjects []string) bool {
	for _, subject := range subjects {
		principal := &auth.Principal{Subject: subject, AuthMethod: "static-users", Groups: []string{"system:authenticated"}}
		if p.hasEffectivePermission(principal) {
			return true
		}
	}
	return false
}
func (p *RBACLocalProvider) hasEffectivePermission(principal *auth.Principal) bool {
	snapshot := p.snapshot.Load()
	var subjectBuffer [MaxPrincipalGroups + 1]SubjectRef
	subjects, ok := principalSubjects(principal, subjectBuffer[:0])
	if !ok || snapshot == nil {
		return false
	}
	for _, subject := range subjects {
		for word, matches := range snapshot.bySubject[subject] {
			for matches != 0 {
				bit := bits.TrailingZeros64(matches)
				matches &= matches - 1
				binding := snapshot.bindings[word*64+bit]
				for action, rules := range binding.role.byAction {
					definition, _ := auth.LookupAction(action)
					if !binding.cluster && definition.Scope == "cluster" {
						continue
					}
					for _, rule := range rules {
						if (definition.Verb == "list" || definition.Verb == "watch") && len(rule.names) > 0 {
							continue
						}
						attrs := map[string]any{}
						if !binding.cluster {
							attrs["namespace"] = binding.namespace
						}
						possible := true
						for _, attribute := range []string{"namespace", "tier", "visibility"} {
							choices := []string{"__other__", "USER", "ORGANIZATION", "public", "private", "internal"}
							for _, c := range rule.conditions {
								if c.Attr == attribute {
									if c.Value != nil {
										choices = append(choices, *c.Value)
									}
									choices = append(choices, c.Values...)
								}
							}
							if attribute == "tier" {
								choices = []string{"USER", "ORGANIZATION"}
							}
							if attribute == "visibility" {
								choices = []string{"public", "internal", "private"}
							}
							if attribute == "namespace" && !binding.cluster {
								choices = []string{binding.namespace}
							}
							match := false
							for _, value := range choices {
								attrs[attribute] = value
								attributes := readConditionValues(attrs)
								satisfies := true
								for i := range rule.conditions {
									c := &rule.conditions[i]
									if c.Attr == attribute && !conditionMatches(c, &attributes, true, true) {
										satisfies = false
										break
									}
								}
								if satisfies {
									match = true
									break
								}
							}
							if !match {
								possible = false
								break
							}
						}
						if possible {
							return true
						}
					}
				}
			}
		}
	}
	return false
}

// HasEffectivePermission reports whether this identity has any satisfiable
// registered grant, without a live resource/datastore query.
func (p *RBACLocalProvider) HasEffectivePermission(principal *auth.Principal) bool {
	return p.hasEffectivePermission(principal)
}
