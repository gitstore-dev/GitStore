// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func humanActorToModel(profile *auth.UserProfile) (model.Actor, error) {
	if profile == nil || profile.Metadata == nil || profile.Metadata.UID != profile.Subject ||
		profile.Metadata.Generation < -1 || profile.Metadata.Generation > math.MaxInt32 ||
		!model.ActorLifecycleState(profile.Lifecycle).IsValid() {
		return nil, auth.ErrDirectoryUnavailable
	}
	meta := profile.Metadata
	metadata := &model.ObjectMeta{
		UID: meta.UID, Name: profile.Username, Generation: int32(meta.Generation),
		ResourceVersion: meta.ResourceVersion, CreationTimestamp: meta.CreationTimestamp,
		Owner: resourceOwnerFromSub(meta.OwnerSub), OwnerReferences: []*model.OwnerReference{}, Finalizers: []string{},
		Labels: stringMapToJSONMap(meta.Labels), Annotations: stringMapToJSONMap(meta.Annotations),
	}
	lifecycle := &model.ActorLifecycle{State: model.ActorLifecycleState(profile.Lifecycle)}
	consent := &model.ContactConsent{Transactional: profile.ContactConsent.Transactional, Marketing: profile.ContactConsent.Marketing}
	identities := append([]string{}, profile.Identities...)
	groups := append([]string{}, profile.Groups...)
	switch profile.Kind {
	case auth.ProfileStaff:
		return &model.Staff{
			ID: mustEncodeNodeID(nodeKindStaff, profile.Subject), APIVersion: "authentication.gitstore.dev/v1beta1",
			Kind: nodeKindStaff, Metadata: metadata, FullName: profile.DisplayName, Identities: identities, Groups: groups,
			Lifecycle: lifecycle, PrimaryEmail: profile.Email, EmailVerified: profile.EmailVerified,
			EmailDeliverable: profile.EmailDeliverable, ContactConsent: consent,
		}, nil
	case auth.ProfileCustomer:
		return &model.Customer{
			ID: mustEncodeNodeID(nodeKindCustomer, profile.Subject), APIVersion: "authentication.gitstore.dev/v1beta1",
			Kind: nodeKindCustomer, Metadata: metadata, FullName: profile.DisplayName, Identities: identities, Groups: groups,
			Lifecycle: lifecycle, PrimaryEmail: profile.Email, EmailVerified: profile.EmailVerified,
			EmailDeliverable: profile.EmailDeliverable, ContactConsent: consent,
		}, nil
	default:
		return nil, auth.ErrProfileNotFound
	}
}

func (r *queryResolver) actorNode(ctx context.Context, kind, uid string) (model.Node, error) {
	profile, account, authorized := security.AuthorizedActorNode(ctx, kind, uid)
	if !authorized {
		if kind == nodeKindServiceAccount {
			var err error
			account, err = r.store.GetServiceAccountByUID(ctx, uid)
			if err != nil {
				return nil, nodeLookupError(kind, err)
			}
		} else {
			if r.registry == nil || r.registry.UserDir() == nil {
				return nil, nodeLookupError(kind, datastore.ErrNotFound)
			}
			var err error
			profile, err = r.registry.UserDir().GetBySubject(ctx, uid)
			if errors.Is(err, auth.ErrProfileNotFound) {
				err = datastore.ErrNotFound
			}
			if err != nil {
				return nil, nodeLookupError(kind, err)
			}
		}
	}
	if kind == nodeKindServiceAccount {
		if account == nil || account.UID != uid {
			return nil, nodeLookupError(kind, datastore.ErrNotFound)
		}
		return serviceAccountToModel(account), nil
	}
	if profile == nil || profile.Subject != uid ||
		(kind == nodeKindStaff && profile.Kind != auth.ProfileStaff) ||
		(kind == nodeKindCustomer && profile.Kind != auth.ProfileCustomer) {
		return nil, nodeLookupError(kind, datastore.ErrNotFound)
	}
	actor, err := humanActorToModel(profile)
	if err != nil {
		return nil, nodeLookupError(kind, err)
	}
	return actor, nil
}

func actorDirectoryError(err error) error {
	if errors.Is(err, auth.ErrDirectoryConflict) {
		return admission.NewError(admission.CodeBadUserInput, "DIRECTORY_CONFLICT", "the directory change conflicts with current identity data").ToGQLError()
	}
	if errors.Is(err, auth.ErrDirectoryInvalidInput) {
		return admission.NewError(admission.CodeBadUserInput, "INVALID_DIRECTORY_INPUT", "invalid actor directory input").ToGQLError()
	}
	if errors.Is(err, auth.ErrNotSupported) {
		return admission.NewError(admission.CodeBadUserInput, "UNSUPPORTED_OPERATION", "the directory does not support the requested actor operation").ToGQLError()
	}
	if errors.Is(err, auth.ErrProfileNotFound) || errors.Is(err, datastore.ErrNotFound) {
		return nodeLookupError("Actor", datastore.ErrNotFound)
	}
	return &gqlerror.Error{Message: "actor directory unavailable", Extensions: map[string]any{"code": "SERVICE_UNAVAILABLE"}, Err: err}
}

func (r *queryResolver) actorBy(ctx context.Context, by model.ActorBy) (model.Actor, error) {
	selectors := 0
	for _, selector := range []*string{by.ID, by.Username, by.ServiceAccountName} {
		if selector != nil {
			selectors++
		}
	}
	if selectors != 1 {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_ACTOR_SELECTOR", "exactly one actor identifier is required").ToGQLError()
	}
	if by.ID != nil {
		kind, _, err := DecodeNodeID(*by.ID)
		if err != nil || (kind != nodeKindStaff && kind != nodeKindCustomer && kind != nodeKindServiceAccount) {
			return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_GLOBAL_ID", "invalid actor ID").ToGQLError()
		}
		node, err := r.Node(ctx, *by.ID)
		if err != nil || node == nil {
			return nil, err
		}
		actor, ok := node.(model.Actor)
		if !ok {
			return nil, nodeLookupError("Actor", datastore.ErrNotFound)
		}
		return actor, nil
	}
	key, selector := "username", by.Username
	if by.ServiceAccountName != nil {
		key, selector = "serviceAccountName", by.ServiceAccountName
	}
	if selector == nil || *selector == "" {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_ACTOR_SELECTOR", "exactly one actor identifier is required").ToGQLError()
	}
	if key == "username" && !auth.ValidExternalUsername(*selector) {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_ACTOR_SELECTOR", "invalid actor username").ToGQLError()
	}
	profile, account, authorized := security.AuthorizedActorNode(ctx, key, *selector)
	if !authorized {
		if key == "serviceAccountName" {
			var err error
			account, err = r.store.GetServiceAccountBySubject(ctx, *selector)
			if err != nil {
				return nil, actorDirectoryError(err)
			}
		} else {
			if r.registry == nil || r.registry.UserDir() == nil {
				return nil, actorDirectoryError(auth.ErrNotSupported)
			}
			profiles, err := r.registry.UserDir().SearchUsers(ctx, *selector, 1)
			if err != nil {
				return nil, actorDirectoryError(err)
			}
			if len(profiles) > 1 {
				return nil, actorDirectoryError(auth.ErrDirectoryUnavailable)
			}
			if len(profiles) == 1 && profiles[0] != nil && profiles[0].Username == *selector {
				profile = profiles[0]
			}
		}
	}
	if account != nil {
		return serviceAccountToModel(account), nil
	}
	if profile == nil {
		return nil, actorDirectoryError(auth.ErrProfileNotFound)
	}
	actor, err := humanActorToModel(profile)
	if err != nil {
		return nil, actorDirectoryError(err)
	}
	return actor, nil
}

func (r *queryResolver) actorConnection(ctx context.Context, first *int32, after *string, last *int32, before *string) (*model.ActorConnection, error) {
	if r.registry == nil {
		return nil, actorDirectoryError(auth.ErrNotSupported)
	}
	directory, ok := r.registry.UserDir().(auth.ActorDirectory)
	if !ok {
		return nil, actorDirectoryError(auth.ErrNotSupported)
	}
	request := auth.ActorPageRequest{After: after, Before: before}
	if first != nil {
		size := int(*first)
		request.First = &size
	}
	if last != nil {
		size := int(*last)
		request.Last = &size
	}
	page, err := directory.ListActors(ctx, request)
	if err != nil {
		if errors.Is(err, auth.ErrNotSupported) && (last != nil || before != nil) {
			return nil, admission.NewError(admission.CodeBadUserInput, "UNSUPPORTED_PAGINATION", "the directory does not support the requested query direction").ToGQLError()
		}
		return nil, actorDirectoryError(err)
	}
	if page == nil || len(page.Edges) > 500 {
		return nil, actorDirectoryError(auth.ErrDirectoryUnavailable)
	}
	result := &model.ActorConnection{Edges: []*model.ActorEdge{},
		PageInfo: &model.PageInfo{HasNextPage: page.HasNextPage, HasPreviousPage: page.HasPreviousPage}}
	for _, edge := range page.Edges {
		if edge.Profile == nil {
			return nil, actorDirectoryError(auth.ErrDirectoryUnavailable)
		}
		var actor model.Actor
		if edge.Profile.Kind == auth.ProfileServiceAccount {
			if edge.Profile.ServiceAccountUID == "" {
				continue
			}
			account, err := r.store.GetServiceAccountByUID(ctx, edge.Profile.ServiceAccountUID)
			if errors.Is(err, datastore.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, actorDirectoryError(err)
			}
			if account == nil || account.UID != edge.Profile.ServiceAccountUID {
				continue
			}
			actor = serviceAccountToModel(account)
		} else {
			actor, err = humanActorToModel(edge.Profile)
			if err != nil {
				return nil, actorDirectoryError(err)
			}
		}
		result.Edges = append(result.Edges, &model.ActorEdge{Cursor: edge.Cursor, Node: actor})
	}
	if len(result.Edges) != 0 {
		result.PageInfo.StartCursor = &result.Edges[0].Cursor
		result.PageInfo.EndCursor = &result.Edges[len(result.Edges)-1].Cursor
	} else if page.HasNextPage {
		return nil, admission.NewError(admission.CodeBadUserInput, "ACTOR_PAGE_HAS_NO_VISIBLE_EDGES", "the bounded directory page contains no authoritative actors; pagination cannot advance").ToGQLError()
	}
	return result, nil
}

const serviceAccountCursorPrefix = "gitstore:serviceAccounts:v1:"

func serviceAccountCursor(account *datastore.ServiceAccount) string {
	return base64.RawURLEncoding.EncodeToString([]byte(serviceAccountCursorPrefix + EncodeKeysetCursor(account.CreationTimestamp, account.UID)))
}

func (r *queryResolver) serviceAccountConnection(ctx context.Context, first *int32, after *string, last *int32, before *string) (*model.ServiceAccountConnection, error) {
	if (first != nil && last != nil) || (after != nil && before != nil) ||
		(first != nil && (*first < 1 || *first > 500)) || (last != nil && (*last < 1 || *last > 500)) {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_PAGINATION", "invalid ServiceAccount page arguments").ToGQLError()
	}
	params := toPageParams(first, nil, last, nil)
	for _, item := range []struct {
		input  *string
		target *string
	}{{after, &params.After}, {before, &params.Before}} {
		if item.input == nil {
			continue
		}
		if len(*item.input) > 1024 {
			return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid ServiceAccount cursor").ToGQLError()
		}
		raw, err := base64.RawURLEncoding.DecodeString(*item.input)
		token, valid := strings.CutPrefix(string(raw), serviceAccountCursorPrefix)
		if err != nil || !valid {
			return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid ServiceAccount cursor").ToGQLError()
		}
		if _, err := DecodeKeysetCursor(token); err != nil {
			return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid ServiceAccount cursor").ToGQLError()
		}
		*item.target = token
	}
	page, err := r.store.ListServiceAccounts(ctx, params)
	if err != nil {
		return nil, actorDirectoryError(err)
	}
	if page == nil || len(page.Items) > params.Limit() {
		return nil, actorDirectoryError(auth.ErrDirectoryUnavailable)
	}
	for _, account := range page.Items {
		if account == nil || account.UID == "" {
			return nil, actorDirectoryError(auth.ErrDirectoryUnavailable)
		}
	}
	result := &model.ServiceAccountConnection{Edges: []*model.ServiceAccountEdge{}, PageInfo: buildPageInfo(page, serviceAccountCursor)}
	for _, account := range page.Items {
		result.Edges = append(result.Edges, &model.ServiceAccountEdge{Cursor: serviceAccountCursor(account), Node: serviceAccountToModel(account)})
	}
	return result, nil
}

func validActorEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && len(value) <= 254
}

func (r *Resolver) auditActorMutation(ctx context.Context, operation, target string, err error) {
	principal := auth.PrincipalFromContext(ctx)
	actor := "anonymous"
	if principal != nil {
		actor = principal.Subject
	}
	if strings.Contains(actor, "@") {
		actor = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(actor)))
	}
	if len(target) > 1024 || strings.Contains(target, "@") {
		target = "invalid"
	}
	r.logger.Info("directory mutation", zap.String("actor", actor), zap.String("target_id", target),
		zap.String("change", operation), zap.String("outcome", auth.DirectoryErrorClass(err)), zap.Time("time", r.clock.Now()))
}

func (r *mutationResolver) createStaffActor(ctx context.Context, input model.CreateStaffInput) (staff *model.Staff, resultErr error) {
	defer func() {
		target := ""
		if staff != nil {
			target = staff.ID
		}
		r.auditActorMutation(ctx, "createStaff", target, resultErr)
	}()
	if !auth.ValidTrustedPrincipal(auth.PrincipalFromContext(ctx)) || auth.PrincipalFromContext(ctx).Subject == "anon" || input.Metadata == nil ||
		!auth.ValidExternalUsername(input.Metadata.Name) || !validActorEmail(input.PrimaryEmail) {
		return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
	}
	if input.Metadata.Namespace != nil && *input.Metadata.Namespace != "" {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_NAMESPACE_SCOPE", "human actors are cluster-scoped").ToGQLError()
	}
	if r.registry == nil {
		return nil, actorDirectoryError(auth.ErrNotSupported)
	}
	directory, ok := r.registry.UserDir().(auth.HumanDirectory)
	if !ok {
		return nil, actorDirectoryError(auth.ErrNotSupported)
	}
	metadata := &auth.DirectoryMetadata{}
	for _, item := range []struct {
		source map[string]any
		target *map[string]string
	}{
		{input.Metadata.Labels, &metadata.Labels}, {input.Metadata.Annotations, &metadata.Annotations},
	} {
		*item.target = map[string]string{}
		for key, value := range item.source {
			text, ok := value.(string)
			if !ok {
				return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
			}
			(*item.target)[key] = text
		}
	}
	for _, group := range input.Groups {
		if group == "" || strings.HasPrefix(group, "system:") {
			return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
		}
	}
	profile := &auth.UserProfile{Username: input.Metadata.Name, DisplayName: input.FullName, Email: input.PrimaryEmail,
		Kind: auth.ProfileStaff, Metadata: metadata, Groups: append([]string{}, input.Groups...)}
	if err := directory.CreateStaff(ctx, profile); err != nil {
		return nil, actorDirectoryError(err)
	}
	actor, err := humanActorToModel(profile)
	if err != nil {
		return nil, actorDirectoryError(err)
	}
	return actor.(*model.Staff), nil
}

func (r *mutationResolver) changeActor(ctx context.Context, id, operation string, edit *model.UpdateUserInput, deliverable *bool, group string) (actor model.Actor, resultErr error) {
	defer func() { r.auditActorMutation(ctx, operation, id, resultErr) }()
	principal := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(principal) || principal.Subject == "anon" {
		return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
	}
	kind, uid, err := DecodeNodeID(id)
	isGroup := operation == "assignUserGroup" || operation == "removeUserGroup"
	if err != nil || (kind != nodeKindStaff && kind != nodeKindCustomer && !(isGroup && kind == nodeKindServiceAccount)) {
		return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
	}
	if r.registry == nil || r.registry.UserDir() == nil {
		return nil, actorDirectoryError(auth.ErrNotSupported)
	}
	directory := r.registry.UserDir()
	profile, account, checked := security.AuthorizedActorNode(ctx, kind, uid)
	if !checked {
		if kind == nodeKindServiceAccount {
			account, err = r.store.GetServiceAccountByUID(ctx, uid)
		} else {
			profile, err = directory.GetBySubject(ctx, uid)
		}
		if err != nil {
			return nil, actorDirectoryError(err)
		}
	}
	if kind == nodeKindServiceAccount {
		if account == nil || account.UID != uid {
			return nil, actorDirectoryError(auth.ErrProfileNotFound)
		}
		if principal.ServiceAccountUID == uid {
			return nil, admission.NewError(admission.CodeBadUserInput, "FORBIDDEN_SELF_EDIT", "administrative self-edits are not permitted").ToGQLError()
		}
		editor, ok := directory.(auth.GroupDirectory)
		if !ok {
			return nil, actorDirectoryError(auth.ErrNotSupported)
		}
		_, err := editor.SetActorGroup(ctx, auth.ProfileServiceAccount, uid, group, operation == "assignUserGroup")
		if err != nil {
			return nil, actorDirectoryError(err)
		}
		current, err := r.store.GetServiceAccountByUID(ctx, uid)
		if err != nil {
			return nil, actorDirectoryError(err)
		}
		if current == nil || current.UID != uid {
			return nil, actorDirectoryError(auth.ErrProfileNotFound)
		}
		return serviceAccountToModel(current), nil
	}
	expected := auth.ProfileStaff
	if kind == nodeKindCustomer {
		expected = auth.ProfileCustomer
	}
	if profile == nil || profile.Subject != uid || profile.Kind != expected {
		return nil, actorDirectoryError(auth.ErrProfileNotFound)
	}
	if operation != "updateUser" && (principal.Subject == uid || principal.Subject == profile.Username ||
		(principal.Username != "" && principal.Username == profile.Username)) {
		return nil, admission.NewError(admission.CodeBadUserInput, "FORBIDDEN_SELF_EDIT", "administrative self-edits are not permitted").ToGQLError()
	}
	copy := *profile
	profile = &copy
	switch operation {
	case "updateUser":
		if edit.FullName != nil {
			profile.DisplayName = *edit.FullName
		}
		if edit.PrimaryEmail != nil {
			if !validActorEmail(*edit.PrimaryEmail) {
				return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
			}
			profile.Email = *edit.PrimaryEmail
		}
		if edit.TransactionalConsent != nil {
			profile.ContactConsent.Transactional = *edit.TransactionalConsent
		}
		if edit.MarketingConsent != nil {
			profile.ContactConsent.Marketing = *edit.MarketingConsent
		}
		err = directory.UpsertProfile(ctx, profile)
	case "setUserEmailDeliverability":
		profile.EmailDeliverable = *deliverable
		err = directory.UpsertProfile(ctx, profile)
	case "assignUserGroup", "removeUserGroup":
		editor, ok := directory.(auth.GroupDirectory)
		if !ok {
			return nil, actorDirectoryError(auth.ErrNotSupported)
		}
		profile, err = editor.SetActorGroup(ctx, expected, uid, group, operation == "assignUserGroup")
	default:
		humans, ok := directory.(auth.HumanDirectory)
		if !ok {
			return nil, actorDirectoryError(auth.ErrNotSupported)
		}
		switch operation {
		case "deactivateUser":
			profile, err = humans.SetHumanLifecycle(ctx, uid, auth.ActorInactive)
		case "reactivateUser":
			profile, err = humans.SetHumanLifecycle(ctx, uid, auth.ActorActive)
		case "suspendUser":
			profile, err = humans.SetHumanLifecycle(ctx, uid, auth.ActorSuspended)
		case "deleteUser":
			profile, err = humans.DeleteHuman(ctx, uid)
		case "resendStaffInvitation":
			if expected != auth.ProfileStaff {
				return nil, actorDirectoryError(auth.ErrDirectoryInvalidInput)
			}
			profile, err = humans.ResendInvitation(ctx, uid)
		}
	}
	if err != nil {
		return nil, actorDirectoryError(err)
	}
	actor, err = humanActorToModel(profile)
	if err != nil {
		return nil, actorDirectoryError(err)
	}
	return actor, nil
}
