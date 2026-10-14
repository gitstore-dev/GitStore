// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"strings"
	"time"
	"unicode"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func validationRefs(trees []*catalogv1.ResourceValidationTree) []*catalogv1.ReceiveRef {
	refs := make([]*catalogv1.ReceiveRef, 0, len(trees))
	for _, tree := range trees {
		refs = append(refs, tree.GetRef())
	}
	return refs
}

func (s *Server) authorizeValidationTrees(ctx context.Context, repositoryID string, trees []*catalogv1.ResourceValidationTree) (hasNamespaces bool, err error) {
	namespace, err := s.resolveNamespaceIdentifier(ctx, repositoryID)
	if err != nil {
		return false, status.Error(codes.Unavailable, "repository authorization facts unavailable")
	}
	for _, tree := range trees {
		oldEntries, err := s.parseDeletionTreeEntries(tree.GetOldBlobs(), namespace)
		if err != nil {
			return false, status.Error(codes.InvalidArgument, "invalid old resource tree")
		}
		newEntries, err := s.parseDeletionTreeEntries(tree.GetProposedBlobs(), namespace)
		if err != nil {
			return false, status.Error(codes.InvalidArgument, "invalid proposed resource tree")
		}
		for _, entries := range [][]*parsedEntry{oldEntries, newEntries} {
			for _, entry := range entries {
				hasNamespaces = hasNamespaces || entry.parsed.Namespace != nil
			}
		}
		if err := s.authorizeOperations(ctx, deriveResourceAdmissionOperations(oldEntries, newEntries, nil)); err != nil {
			return false, err
		}
	}
	return hasNamespaces, nil
}

// The service credential authenticates transport, not a cached grant. Every
// callback binds sanitized identity facts to this exact receive and ref set.
func (s *Server) authenticateCallback(ctx context.Context, method string, request proto.Message, repositoryID string, envelope *catalogv1.CallbackAuthorization, refs []*catalogv1.ReceiveRef) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(auth.RPCSignatureMetadata)
	if len(values) != 1 || len(md.Get("authorization")) != 0 {
		return ctx, status.Error(codes.Unauthenticated, "authenticated Git service required")
	}
	digest, expires, err := auth.VerifyRPC(s.serviceSecret, "gitstore-api", method, values[0], request, time.Now())
	if err != nil && s.serviceSecretPrevious != "" {
		digest, expires, err = auth.VerifyRPC(s.serviceSecretPrevious, "gitstore-api", method, values[0], request, time.Now())
	}
	if err != nil {
		return ctx, status.Error(codes.Unauthenticated, "invalid signed callback")
	}
	consumed, err := s.store.TryConsumeServiceAccountAssertion(ctx, digest, expires)
	if err != nil {
		return ctx, status.Error(codes.Unavailable, "callback replay check unavailable")
	}
	if !consumed {
		return ctx, status.Error(codes.Unauthenticated, "callback replay rejected")
	}
	if len(refs) == 0 {
		return ctx, status.Error(codes.InvalidArgument, "callback refs are required")
	}
	if envelope == nil || repositoryID == "" || envelope.GetRepositoryId() != repositoryID || envelope.GetReceiveId() == "" || len(envelope.GetReceiveId()) > 128 || strings.IndexFunc(envelope.GetReceiveId(), unicode.IsControl) >= 0 || len(refs) == 0 || len(refs) > 256 || len(refs) != len(envelope.GetRefs()) {
		return ctx, status.Error(codes.PermissionDenied, "callback receive binding is invalid")
	}
	seen := map[string]bool{}
	// Git has already validated object IDs before constructing the callback.
	// Here equality to the authenticated receive snapshot, plus bounded input,
	// is the authority check; this layer does not resolve arbitrary refs.
	oid := func(value string) bool {
		return value != "" && len(value) <= 128 && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	for i, ref := range refs {
		if ref == nil || !proto.Equal(ref, envelope.Refs[i]) || !strings.HasPrefix(ref.GetRefName(), "refs/") || len(ref.GetRefName()) > 1024 || strings.IndexFunc(ref.GetRefName(), unicode.IsControl) >= 0 || !oid(ref.GetOldCommitSha()) || !oid(ref.GetNewCommitSha()) || seen[ref.GetRefName()] {
			return ctx, status.Error(codes.PermissionDenied, "callback ref binding is invalid")
		}
		seen[ref.GetRefName()] = true
	}
	actor := envelope.GetActor()
	principal := &auth.Principal{Subject: actor.GetSubject(), Groups: append([]string(nil), actor.GetGroups()...), AuthMethod: actor.GetAuthMethod(), ServiceAccountUID: actor.GetServiceAccountUid()}
	if !auth.ValidTrustedPrincipal(principal) || principal.AuthMethod == "none" || principal.AuthMethod == "serviceaccount-assertion" {
		return ctx, status.Error(codes.Unauthenticated, "verified callback actor required")
	}
	if principal.ServiceAccountUID != "" {
		name := strings.TrimPrefix(principal.Subject, "system:serviceaccount:")
		account, err := s.store.GetServiceAccountBySubject(ctx, name)
		if err != nil || account == nil || account.Disabled || account.DeletionTimestamp != nil || account.UID != principal.ServiceAccountUID {
			return ctx, status.Error(codes.Unauthenticated, "callback ServiceAccount is no longer valid")
		}
	}
	if s.registry == nil || s.registry.AuthZ() == nil {
		return ctx, status.Error(codes.Unavailable, "callback authorizer unavailable")
	}
	return auth.ContextWithPrincipal(ctx, principal), nil
}

// AdmissionContext carries per-request state for the post-receive admission
// phase. It is populated once per AdmitResources call from a single DB lookup
// (repository → namespace) and threaded through all per-resource admit helpers.
type AdmissionContext struct {
	RepositoryID      string
	Namespace         string    // resolved namespace identifier
	ActorSubject      string    // authenticated principal that performed the push
	CommitSHA         string    // full SHA of the accepted push commit
	RefName           string    // fully-qualified ref, e.g. "refs/heads/main"
	Revision          string    // human revision label, e.g. "main@sha1:abc123"
	Now               time.Time // admission timestamp, set once for the entire push
	superseded        *bool
	repositoryCurrent func(context.Context) (bool, error)
}

func (c AdmissionContext) markSuperseded() {
	if c.superseded != nil {
		*c.superseded = true
	}
}

func (c AdmissionContext) wasSuperseded() bool {
	return c.superseded != nil && *c.superseded
}
