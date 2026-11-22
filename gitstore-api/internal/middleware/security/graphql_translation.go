// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"errors"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type authorizedTranslationReadKey struct{}
type authorizedTranslationNodesKey struct{}

func translationAuthorizationReadUnavailable() error {
	return &gqlerror.Error{
		Message:    "Translation read unavailable",
		Extensions: map[string]any{"code": "TRANSLATION_UNAVAILABLE", "retryable": true},
	}
}

type authorizedTranslationRead struct {
	field  *graphql.FieldContext
	source *datastore.Translation
}

type authorizedTranslationNodes struct {
	field   *graphql.FieldContext
	sources map[string]*datastore.Translation
}

func AuthorizedTranslationRead(ctx context.Context) (*datastore.Translation, bool) {
	read, ok := ctx.Value(authorizedTranslationReadKey{}).(*authorizedTranslationRead)
	if !ok || read == nil || read.field != graphql.GetFieldContext(ctx) {
		return nil, false
	}
	return read.source, true
}

func AuthorizedTranslationNode(ctx context.Context, uid string) (*datastore.Translation, bool) {
	read, ok := ctx.Value(authorizedTranslationNodesKey{}).(*authorizedTranslationNodes)
	if !ok || read == nil || read.field != graphql.GetFieldContext(ctx) {
		return nil, false
	}
	source, found := read.sources[uid]
	return source, found
}

func isTranslationQueryField(fc *graphql.FieldContext) bool {
	return fc != nil && fc.Object == "Query" && (fc.Field.Name == "translation" || fc.Field.Name == "translations")
}

func (a *Authorize) translationStore() datastore.TranslationStore {
	capable, ok := a.store.(datastore.TranslationCapable)
	if !ok {
		return nil
	}
	return capable.TranslationStore()
}

func (a *Authorize) authorizeTranslationQuery(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) (context.Context, error) {
	if authz == nil {
		return ctx, gqlerror.Errorf("authorization service unavailable")
	}
	if fc.Field.Name == "translations" {
		namespace, _ := directStringArg(fc.Args, "namespace")
		if namespace == "" {
			return ctx, admission.NewError(admission.CodeBadUserInput, "INVALID_NAMESPACE", "namespace is required").ToGQLError()
		}
		owner, err := a.namespaceOwnerFact(ctx, namespace)
		if err != nil {
			return ctx, err
		}
		return ctx, authorizeCategoryAction(ctx, authz, principal, auth.ActionTranslationList,
			auth.ResourceContext{Kind: "translation", OwnerSub: owner, Attrs: map[string]any{"namespace": namespace}})
	}
	id, hasID := nestedStringPath(fc.Args, "by", "id")
	namespace, hasNamespace := nestedStringPath(fc.Args, "by", "namespacePath", "namespace")
	name, hasName := nestedStringPath(fc.Args, "by", "namespacePath", "name")
	valid := (hasID && id != "" && !hasNamespace && !hasName) || (!hasID && hasNamespace && hasName && namespace != "" && name != "")
	if argument := fc.Field.Arguments.ForName("by"); argument != nil && argument.Value.Kind == ast.Variable && graphql.HasOperationContext(ctx) {
		value, err := argument.Value.Value(graphql.GetOperationContext(ctx).Variables)
		fields, ok := value.(map[string]any)
		valid = valid && err == nil && ok && len(fields) == 1
	}
	if !valid {
		return ctx, admission.NewError(admission.CodeBadUserInput, "INVALID_SELECTOR", "TranslationBy: exactly one selector required").ToGQLError()
	}
	store := a.translationStore()
	if store == nil {
		return ctx, gqlerror.Errorf("authorization service unavailable")
	}
	var source *datastore.Translation
	var uid string
	var err error
	if hasID {
		uid, err = decodeGlobalIDAs("Translation", id)
		if err != nil {
			return ctx, err
		}
		source, err = store.GetTranslation(ctx, uid)
	} else {
		source, err = store.GetTranslationByName(ctx, namespace, name)
	}
	if errors.Is(err, datastore.ErrNotFound) {
		source = nil
	} else if err != nil || source == nil {
		return ctx, translationAuthorizationReadUnavailable()
	}
	owner := ""
	if source != nil {
		if (hasID && source.UID != uid) || (!hasID && (source.Namespace != namespace || source.Name != name)) {
			return ctx, gqlerror.Errorf("authorization error")
		}
		namespace, name, owner = source.Namespace, source.Name, source.EffectiveOwnerSub()
	}
	if err := authorizeTranslationReadResource(ctx, principal, authz, namespace, name, owner); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, authorizedTranslationReadKey{}, &authorizedTranslationRead{field: fc, source: source}), nil
}

func authorizeTranslationReadResource(ctx context.Context, principal *auth.Principal, authz auth.AuthZProvider, namespace, name, owner string) error {
	if ctx.Err() != nil {
		return gqlerror.Errorf("authorization error")
	}
	err := authorizeCategoryAction(ctx, authz, principal, auth.ActionTranslationRead,
		auth.ResourceContext{Kind: "translation", Name: name, OwnerSub: owner, Attrs: map[string]any{"namespace": namespace}})
	if ctx.Err() != nil {
		return gqlerror.Errorf("authorization error")
	}
	return err
}

func (a *Authorize) authorizeTranslationNodes(ctx context.Context, fc *graphql.FieldContext, principal *auth.Principal, authz auth.AuthZProvider) (context.Context, error) {
	if fc.Object != "Query" || (fc.Field.Name != "node" && fc.Field.Name != "nodes") {
		return ctx, nil
	}
	ids, _ := directStringsArg(fc.Args, "ids")
	if fc.Field.Name == "node" {
		id, _ := directStringArg(fc.Args, "id")
		ids = []string{id}
	}
	authorized := &authorizedTranslationNodes{field: fc, sources: make(map[string]*datastore.Translation)}
	for _, id := range ids {
		kind, uid, err := decodeGlobalID(id)
		if err != nil || kind != "Translation" {
			continue
		}
		if _, seen := authorized.sources[uid]; seen {
			continue
		}
		store := a.translationStore()
		if store == nil || ctx.Err() != nil {
			return ctx, gqlerror.Errorf("authorization service unavailable")
		}
		source, err := store.GetTranslation(ctx, uid)
		if errors.Is(err, datastore.ErrNotFound) {
			source = nil
		} else if err != nil || source == nil || source.UID != uid {
			return ctx, translationAuthorizationReadUnavailable()
		}
		namespace, name, owner := "", "", ""
		if source != nil {
			namespace, name, owner = source.Namespace, source.Name, source.EffectiveOwnerSub()
		}
		if err := authorizeTranslationReadResource(ctx, principal, authz, namespace, name, owner); err != nil {
			return ctx, err
		}
		authorized.sources[uid] = source
	}
	return context.WithValue(ctx, authorizedTranslationNodesKey{}, authorized), nil
}
