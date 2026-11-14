// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/rego"
)

func (e *Engine) Covers(ctx context.Context, cluster, namespace *Translation, principal *auth.Principal, request auth.AuthorizationCoverageRequest) (auth.AuthorizationCoverage, error) {
	if e == nil || e.digest == "" || cluster == nil || cluster.tierID != rbac.ClusterTierID || !cluster.active ||
		namespace != nil && !namespace.active || ctx.Err() != nil {
		return auth.AuthorizationCoverage{}, ErrUnavailable
	}
	digest := e.digest + ":" + cluster.digest
	if namespace != nil {
		digest += ":" + namespace.digest
	}
	sum := sha256.Sum256([]byte(digest))
	proof := auth.AuthorizationCoverage{Reason: "explicit bind or escalate required", PolicyDigest: hex.EncodeToString(sum[:])}
	var buffer [rbaclocal.MaxPrincipalGroups + 1]rbaclocal.SubjectRef
	subjects, valid := rbaclocal.PrincipalSubjects(principal, buffer[:0])
	if !valid || len(request.Permissions) > rbac.MaxRulesPerRole*len(auth.AuthorizationActions()) {
		return proof, nil
	}
	scope := "cluster"
	if request.NamespaceUID != "" {
		if namespace == nil || namespace.tierID != request.NamespaceUID || namespace.namespace != request.Namespace {
			return auth.AuthorizationCoverage{}, ErrUnavailable
		}
		scope = "namespace"
	} else if request.Namespace != "" {
		return proof, nil
	}
	permissions := make([]any, 0, len(request.Permissions))
	remaining := rbac.MaxSnapshotBytes
	for _, permission := range request.Permissions {
		definition, ok := auth.LookupAction(permission.Action)
		if !ok || scope == "namespace" && definition.Scope != "namespace" || len(permission.ResourceNames) > 256 {
			return proof, nil
		}
		remaining -= len(permission.Action)*6 + 128
		for _, name := range permission.ResourceNames {
			if name == "" || len(name) > 256 {
				return proof, nil
			}
			remaining -= len(name)*6 + 16
		}
		if remaining < 0 {
			return auth.AuthorizationCoverage{}, ErrUnavailable
		}
		permissions = append(permissions, map[string]any{"action": permission.Action, "names": append([]string{}, permission.ResourceNames...)})
	}
	keys := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		keys = append(keys, subjectKey(subject.Kind, subject.Name))
	}
	inputRequest, err := ast.InterfaceToValue(map[string]any{
		"subjects": keys, "masters": slices.Contains(principal.Groups, "system:masters"),
		"scope": scope, "namespace": request.Namespace, "permissions": permissions,
	})
	if err != nil {
		return auth.AuthorizationCoverage{}, ErrUnavailable
	}
	tiers := ast.NewArray(cluster.data)
	if namespace != nil {
		tiers = tiers.Append(namespace.data)
	}
	input := ast.NewObject(
		[2]*ast.Term{ast.StringTerm("request"), ast.NewTerm(inputRequest)},
		[2]*ast.Term{ast.StringTerm("tiers"), ast.NewTerm(tiers)},
	)
	results, err := e.coverage.Eval(ctx, rego.EvalParsedInput(input))
	if err != nil || len(results) != 1 {
		return auth.AuthorizationCoverage{}, ErrUnavailable
	}
	result, ok := results[0].Bindings["coverage"].(map[string]any)
	if !ok || len(result) != 1 {
		return auth.AuthorizationCoverage{}, ErrUnavailable
	}
	covered, ok := result["covered"].(bool)
	if !ok {
		return auth.AuthorizationCoverage{}, ErrUnavailable
	}
	proof.Covered = covered
	if covered {
		proof.Reason = "unconditional permission coverage"
	}
	return proof, nil
}
