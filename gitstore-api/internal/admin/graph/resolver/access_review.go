// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

const accessReviewAPIVersion = "authorization.gitstore.dev/v1beta1"

func invalidAccessReview() error {
	return admission.NewError(admission.CodeBadUserInput, "INVALID_ACCESS_REVIEW", "invalid SubjectAccessReview input").ToGQLError()
}

func reviewForbidden() error {
	return admission.NewError(admission.CodeForbidden, "RBAC_REVIEW_PERMISSION_REQUIRED", "access review is not authorized").ToGQLError()
}

func (r *Resolver) reviewAccess(ctx context.Context, input model.AccessReviewInput) (*model.AccessReviewPayload, error) {
	if input.APIVersion != accessReviewAPIVersion || input.Kind != "SubjectAccessReview" ||
		input.Spec == nil || input.Spec.Principal == nil || input.Spec.ResourceRef == nil {
		return nil, invalidAccessReview()
	}
	spec := input.Spec
	if len(spec.ResourceRef.ID) > 1024 {
		return nil, invalidAccessReview()
	}
	definition, registered := auth.LookupAction(spec.Action)
	kind, uid, err := DecodeNodeID(spec.ResourceRef.ID)
	if err != nil {
		return nil, invalidAccessReview()
	}
	resourceKind := strings.ToLower(kind[:1]) + kind[1:]
	if kind == nodeKindCategory {
		resourceKind = "categoryTaxonomy"
	} else if kind == nodeKindStaff || kind == nodeKindCustomer {
		resourceKind = "user"
	}
	if !registered || definition.Kind != resourceKind ||
		!validReviewPrincipalInput(spec.Principal) || !validReviewContext(spec.Context) {
		return nil, invalidAccessReview()
	}
	if r.rbacValidator == nil || r.registry == nil || r.store == nil {
		return nil, rbacUnavailableError()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if r.rbacValidator.Ready != nil && r.rbacValidator.Ready(ctx) != nil {
		return nil, rbacUnavailableError()
	}
	reviewer := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(reviewer) {
		return nil, reviewForbidden()
	}
	var resource auth.ResourceContext
	var parent *datastore.Namespace
	var namespaceUID string
	var lookupErr error
	if definition.Scope == "namespace" {
		resource, namespaceUID, lookupErr = r.reviewResource(ctx, kind, uid)
		namespace, _ := resource.Attrs["namespace"].(string)
		if lookupErr == nil {
			parent, lookupErr = r.store.GetNamespaceByName(ctx, namespace)
			if lookupErr == nil && (parent == nil || namespaceUID != "" && namespaceUID != parent.UID) {
				lookupErr = datastore.ErrNotFound
			}
			if lookupErr == nil && namespace != SystemRepositoryName {
				namespaceUID = parent.UID
			} else {
				namespaceUID = ""
			}
		}
	}
	capturedAt := time.Now().UTC()
	validation, err := r.rbacValidator.Capture.Begin(ctx, reviewer, namespaceUID)
	if err != nil {
		return nil, rbacUnavailableError()
	}
	defer validation.Close()
	reviewAction := auth.ActionClusterRoleAccessReviewCreate
	reviewResource := auth.ResourceContext{Kind: "clusterRole"}
	if definition.Scope == "namespace" && lookupErr == nil {
		reviewAction = auth.ActionRoleAccessReviewCreate
		reviewResource = auth.ResourceContext{Kind: "role", Attrs: map[string]any{"namespace": resource.Attrs["namespace"]}}
	}
	permission, err := validation.Authorize(ctx, reviewAction, reviewResource)
	if err != nil {
		return nil, rbacUnavailableError()
	}
	if permission.Outcome != auth.OutcomeAllow {
		return nil, reviewForbidden()
	}
	if lookupErr != nil {
		return nil, reviewLookupError(kind, lookupErr)
	}
	if definition.Scope != "namespace" {
		resource, _, err = r.reviewResource(ctx, kind, uid)
		if err != nil {
			return nil, reviewLookupError(kind, err)
		}
	}
	if parent != nil && namespaceUID != "" {
		captured, err := validation.Snapshot(namespaceUID)
		if err != nil || captured.Origin.NamespaceUID != parent.UID || captured.Origin.NamespaceVersion != parent.ResourceVersion {
			return nil, rbacUnavailableError()
		}
	}
	target, err := r.reviewPrincipal(ctx, spec.Principal)
	if err != nil {
		return nil, rbacUnavailableError()
	}
	if resource.Attrs == nil {
		resource.Attrs = map[string]any{}
	}
	for name, value := range spec.Context {
		resource.Attrs[name] = value
	}
	decision, err := validation.Review(ctx, target, spec.Action, resource)
	if err != nil || decision.AuthorizationSnapshot == nil || decision.Outcome != auth.OutcomeAllow && decision.Outcome != auth.OutcomeDeny {
		return nil, rbacUnavailableError()
	}
	identity := decision.AuthorizationSnapshot
	expectedTiers := 1
	if namespaceUID != "" {
		expectedTiers++
	}
	if !identity.Loaded || identity.ModelVersion != rbac.ModelVersion || len(identity.Tiers) != expectedTiers ||
		identity.CatalogDigest != auth.AuthorizationCatalogDigest() || identity.BuiltinDigest != r.rbacValidator.Capture.Reader.BuiltinDigest {
		return nil, rbacUnavailableError()
	}
	status := &model.SubjectAccessReviewStatus{
		Allowed: decision.Outcome == auth.OutcomeAllow, Reason: decision.Reason,
		Hypothetical:  spec.Principal.Groups != nil || spec.Context != nil,
		CatalogDigest: identity.CatalogDigest, BuiltinDigest: identity.BuiltinDigest,
	}
	if len(status.Reason) > 256 {
		status.Reason = "evaluated authoritative policy"
	}
	seen := map[string]bool{}
	for _, tier := range identity.Tiers {
		if seen[tier.TierID] || tier.TierID != rbac.ClusterTierID && tier.TierID != namespaceUID {
			return nil, rbacUnavailableError()
		}
		seen[tier.TierID] = true
		captured, err := validation.Snapshot(tier.TierID)
		if err != nil || captured.State.Revision != tier.Revision || captured.Origin.GitRevision != tier.GitRevision ||
			captured.ContentDigest != tier.ContentDigest {
			return nil, rbacUnavailableError()
		}
		status.Revisions = append(status.Revisions, &model.SubjectAccessReviewRevision{
			TierID: tier.TierID, NamespaceUID: captured.Origin.NamespaceUID,
			GitRevision: tier.GitRevision, TierRevision: strconv.FormatUint(tier.Revision, 10), CapturedAt: capturedAt,
		})
	}
	return &model.AccessReviewPayload{SubjectAccessReview: &model.SubjectAccessReview{
		APIVersion: input.APIVersion, Kind: input.Kind, Status: status,
		Spec: &model.SubjectAccessReviewSpec{
			Principal: &model.SubjectAccessReviewPrincipal{Subject: spec.Principal.Subject, Groups: slices.Clone(spec.Principal.Groups)},
			Action:    spec.Action, ResourceRef: &model.SubjectAccessReviewResourceRef{ID: spec.ResourceRef.ID}, Context: spec.Context,
		},
	}}, nil
}

func validReviewPrincipalInput(input *model.SubjectAccessReviewPrincipalInput) bool {
	principal := &auth.Principal{Subject: input.Subject, Groups: input.Groups, AuthMethod: "access-review"}
	if input.Subject == "anon" {
		principal.AuthMethod = "none"
	} else if input.Subject == "system:api" {
		principal.AuthMethod = "internal"
	} else if strings.HasPrefix(input.Subject, "system:serviceaccount:") {
		principal.AuthMethod, principal.ServiceAccountUID = "serviceaccount-jwt", "review"
	}
	return auth.ValidTrustedPrincipal(principal)
}

func validReviewContext(attrs map[string]any) bool {
	if len(attrs) > 3 {
		return false
	}
	for name, value := range attrs {
		switch name {
		case "visibility":
			if value != "public" && value != "private" && value != "internal" {
				return false
			}
		case "tier":
			if value != "USER" && value != "ORGANIZATION" {
				return false
			}
		case "targetOwnerRef":
			if _, valid := rbaclocal.TargetOwner(value); !valid {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (r *Resolver) reviewPrincipal(ctx context.Context, input *model.SubjectAccessReviewPrincipalInput) (*auth.Principal, error) {
	principal := &auth.Principal{Subject: input.Subject, AuthMethod: "access-review", Groups: slices.Clone(input.Groups)}
	switch {
	case input.Subject == "anon":
		principal.AuthMethod = "none"
		if input.Groups == nil {
			principal.Groups = []string{"system:unauthenticated"}
		}
		return principal, nil
	case input.Subject == "system:api":
		principal.AuthMethod = "internal"
		return principal, nil
	case strings.HasPrefix(input.Subject, "system:serviceaccount:"):
		account, err := r.store.GetServiceAccountBySubject(ctx, strings.TrimPrefix(input.Subject, "system:serviceaccount:"))
		if err != nil || account == nil || account.Disabled {
			return nil, rbac.ErrUnavailable
		}
		principal.AuthMethod, principal.ServiceAccountUID = "serviceaccount-jwt", account.UID
	}
	if input.Groups != nil {
		return principal, nil
	}
	principal.Groups = []string{"system:authenticated"}
	return auth.EnrichReviewPrincipal(ctx, r.registry.UserDir(), principal)
}

func reviewLookupError(kind string, err error) error {
	if errors.Is(err, datastore.ErrNotFound) || errors.Is(err, rbac.ErrNotFound) {
		return nodeLookupError(kind, datastore.ErrNotFound)
	}
	return rbacUnavailableError()
}

func (r *Resolver) reviewResource(ctx context.Context, kind, uid string) (auth.ResourceContext, string, error) {
	resource := auth.ResourceContext{Kind: strings.ToLower(kind[:1]) + kind[1:], Attrs: map[string]any{}}
	namespaceUID := ""
	switch kind {
	case nodeKindRole, nodeKindRoleBinding, nodeKindClusterRole, nodeKindClusterRoleBinding:
		store, err := r.rbacStore()
		if err != nil {
			return resource, "", err
		}
		var row *rbac.Resource
		for _, builtin := range r.rbacBuiltinsResources {
			if string(builtin.Kind) == kind && builtin.Metadata.UID == uid {
				row = builtin
				break
			}
		}
		if row == nil {
			row, err = store.GetRBACByUID(ctx, uid)
		}
		if err != nil {
			return resource, "", err
		}
		if row == nil || string(row.Kind) != kind {
			return resource, "", datastore.ErrNotFound
		}
		resource.Name, namespaceUID = row.Metadata.Name, row.NamespaceUID
		if row.Kind.Namespaced() {
			resource.Attrs["namespace"] = row.Metadata.Namespace
		}
	case nodeKindProduct:
		row, err := r.store.GetProduct(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name, resource.OwnerSub, resource.Attrs["namespace"] = row.Name, row.EffectiveOwnerSub(), row.Namespace
	case nodeKindProductVariant:
		row, err := r.store.GetProductVariant(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name, resource.OwnerSub, resource.Attrs["namespace"] = row.Name, row.EffectiveOwnerSub(), row.Namespace
	case nodeKindCollection:
		row, err := r.store.GetCollection(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name, resource.OwnerSub, resource.Attrs["namespace"] = row.Name, row.EffectiveOwnerSub(), row.Namespace
	case nodeKindCategory:
		row, err := r.store.GetCategoryTaxonomy(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Kind = "categoryTaxonomy"
		resource.Name, resource.OwnerSub, resource.Attrs["namespace"] = row.Name, row.EffectiveOwnerSub(), row.Namespace
		resource.Attrs["repositoryID"] = row.RepositoryID
	case nodeKindFile:
		row, err := r.store.GetFile(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name, resource.OwnerSub, resource.Attrs["namespace"] = row.Name, row.EffectiveOwnerSub(), row.Namespace
	case nodeKindRepository:
		row, err := r.store.GetRepository(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name, resource.OwnerSub, resource.Attrs["namespace"] = row.Name, row.EffectiveOwnerSub(), row.Namespace
		if visibility, ok := auth.VisibilityFromSpec(row.Spec); ok {
			resource.Attrs["visibility"] = visibility
		}
	case nodeKindNamespace:
		row, err := r.store.GetNamespace(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name, resource.OwnerSub = row.Name, row.EffectiveOwnerSub()
		switch row.Tier {
		case datastore.NamespaceTierUser:
			resource.Attrs["tier"] = "USER"
		case datastore.NamespaceTierOrganization:
			resource.Attrs["tier"] = "ORGANIZATION"
		}
	case nodeKindServiceAccount:
		row, err := r.store.GetServiceAccountByUID(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		resource.Name = row.Name
	case nodeKindStaff, nodeKindCustomer:
		if r.registry.UserDir() == nil {
			return resource, "", auth.ErrDirectoryUnavailable
		}
		row, err := r.registry.UserDir().GetBySubject(ctx, uid)
		if err != nil {
			return resource, "", err
		}
		if row == nil || row.Kind != auth.ProfileStaff && kind == nodeKindStaff || row.Kind != auth.ProfileCustomer && kind == nodeKindCustomer {
			return resource, "", datastore.ErrNotFound
		}
		resource.Kind, resource.Name = "user", row.Subject
	default:
		return resource, "", invalidAccessReview()
	}
	return resource, namespaceUID, nil
}
