// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

//go:embed policy.rego
var policySource string

var ErrUnavailable = auth.ErrAuthorizationUnavailable

// Engine prepares only the release policy; immutable tier data is supplied at
// evaluation time. It does not own readiness or perform source I/O.
type Engine struct {
	query    rego.PreparedEvalQuery
	coverage rego.PreparedEvalQuery
	digest   string
}

func NewEngine(ctx context.Context) (*Engine, error) {
	query, err := rego.New(
		rego.Query("decision = data.gitstore.rbac.decision"),
		rego.Module("gitstore-rbac.rego", policySource),
		rego.Capabilities(policyCapabilities()),
		rego.StrictBuiltinErrors(true),
	).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare embedded decision: %v", ErrUnavailable, err)
	}
	coverage, err := rego.New(
		rego.Query("coverage = data.gitstore.rbac.coverage"),
		rego.Module("gitstore-rbac.rego", policySource),
		rego.Capabilities(policyCapabilities()),
		rego.StrictBuiltinErrors(true),
	).PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: prepare embedded coverage: %v", ErrUnavailable, err)
	}
	sum := sha256.Sum256([]byte(policySource))
	return &Engine{query: query, coverage: coverage, digest: hex.EncodeToString(sum[:])}, nil
}

func policyCapabilities() *ast.Capabilities {
	caps := ast.CapabilitiesForThisVersion()
	allowed := []string{"eq", "equal", "neq", "lte", "count", "sort", "is_string", "object.get", "internal.member_2"}
	builtins := make([]*ast.Builtin, 0, len(allowed))
	for _, builtin := range caps.Builtins {
		if slices.Contains(allowed, builtin.Name) {
			builtins = append(builtins, builtin)
		}
	}
	caps.Builtins = builtins
	// No network destination is permitted, even if a future policy accidentally
	// enables a network-capable builtin.
	caps.AllowNet = []string{}
	return caps
}

func (e *Engine) Evaluate(ctx context.Context, cluster, namespace *Translation, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	unavailable := func() (auth.Decision, error) {
		return auth.Deny("opa", ErrUnavailable.Error()), ErrUnavailable
	}
	if e == nil || cluster == nil || cluster.tierID != rbac.ClusterTierID || !cluster.active ||
		namespace != nil && !namespace.active || ctx.Err() != nil {
		return unavailable()
	}
	digest := e.digest + ":" + cluster.digest
	if namespace != nil {
		digest += ":" + namespace.digest
	}
	sum := sha256.Sum256([]byte(digest))
	digest = hex.EncodeToString(sum[:])
	deny := func(reason string) (auth.Decision, error) {
		decision := auth.Deny("opa", reason)
		decision.PolicyDigest = digest
		return decision, nil
	}
	definition, registered := auth.LookupAction(action)
	var buffer [rbaclocal.MaxPrincipalGroups + 1]rbaclocal.SubjectRef
	subjects, valid := rbaclocal.PrincipalSubjects(principal, buffer[:0])
	if !registered || !valid || resource.Kind != definition.Kind || len(resource.Name) > 256 || len(resource.OwnerSub) > 256 {
		return deny("invalid authorization request")
	}
	target := ""
	if definition.Verb == "transfer" {
		var ok bool
		target, ok = rbaclocal.TargetOwner(resource.Attrs["targetOwnerRef"])
		if !ok {
			return deny("transfer requires valid targetOwnerRef")
		}
	}
	namespaceName, _ := resource.Attrs["namespace"].(string)
	// The root Namespace is represented by the required cluster publication,
	// not a second namespaced tier or an identity-based readiness exception.
	if namespaceName == "gitstore-system" && namespace != nil ||
		namespaceName != "" && namespaceName != "gitstore-system" &&
			(namespace == nil || namespace.namespace != namespaceName) &&
			!(namespace == nil && clusterScopedNamespaceCleanup(principal, action, resource)) ||
		namespace != nil && (namespace.tierID == cluster.tierID || namespace.namespace == "") {
		return unavailable()
	}
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		keys = append(keys, subjectKey(subject.Kind, subject.Name))
	}
	attrs := map[string]string{}
	for _, name := range []string{"namespace", "tier", "visibility"} {
		if value, ok := resource.Attrs[name].(string); ok && len(value) <= 256 {
			attrs[name] = value
		}
	}
	username := ""
	if auth.ValidExternalUsername(principal.Username) &&
		(principal.ProfileKind == auth.ProfileStaff || principal.ProfileKind == auth.ProfileCustomer) {
		username = principal.Subject
	}
	groups := append([]string{}, principal.Groups...)
	request, err := ast.InterfaceToValue(map[string]any{
		"subjects": keys, "subject": principal.Subject, "groups": groups,
		"masters": slices.Contains(groups, "system:masters"), "username": username,
		"action": action, "scope": definition.Scope, "verb": definition.Verb,
		"name": resource.Name, "owner": resource.OwnerSub, "target": target,
		"namespace": namespaceName, "attrs": attrs,
	})
	if err != nil {
		return unavailable()
	}
	// Reuse private immutable AST terms, not a marshaled copy of the complete
	// snapshot on every request. No caller receives a mutable reference to them.
	tiers := ast.NewArray(cluster.data)
	if namespace != nil {
		tiers = tiers.Append(namespace.data)
	}
	input := ast.NewObject(
		[2]*ast.Term{ast.StringTerm("request"), ast.NewTerm(request)},
		[2]*ast.Term{ast.StringTerm("tiers"), ast.NewTerm(tiers)},
	)
	results, err := e.query.Eval(ctx, rego.EvalParsedInput(input))
	if err != nil || len(results) != 1 {
		return unavailable()
	}
	result, ok := results[0].Bindings["decision"].(map[string]any)
	if !ok || len(result) != 5 {
		return unavailable()
	}
	allowed, okAllow := result["allow"].(bool)
	masters, okMasters := result["masters"].(bool)
	binding, okBinding := result["binding"].(string)
	role, okRole := result["role"].(string)
	rule, okRule := result["rule"].(json.Number)
	index, parseErr := strconv.Atoi(string(rule))
	if !okAllow || !okMasters || !okBinding || !okRole || !okRule || parseErr != nil ||
		masters && (!allowed || !slices.Contains(groups, "system:masters")) {
		return unavailable()
	}
	if allowed && !masters {
		bindingUID, bindingErr := uuid.Parse(binding)
		roleUID, roleErr := uuid.Parse(role)
		if bindingErr != nil || roleErr != nil || bindingUID.String() != binding || roleUID.String() != role ||
			index < 0 || index >= rbac.MaxRulesPerRole {
			return unavailable()
		}
	} else if binding != "" || role != "" || index != -1 {
		return unavailable()
	}
	if !allowed {
		return deny("no matching binding")
	}
	reason := "trusted system:masters"
	if !masters {
		reason = fmt.Sprintf("binding=%s role=%s rule=%d", binding, role, index)
	}
	decision := auth.Allow("opa", reason)
	decision.PolicyDigest = digest
	return decision, nil
}
