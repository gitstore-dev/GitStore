// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/admissionreport"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type rbacConditionalWriter interface {
	CommitFileAtRevisionForRepo(context.Context, string, gitclient.CommitFileAtRevisionParams) (string, error)
}

type rbacProvenanceReader interface {
	ReadFileProvenanceForRepo(context.Context, string, string, string) (*gitv1.GetFileProvenanceResponse, error)
}

func (r *Resolver) rbacProvenance(ctx context.Context, kind rbac.Kind, metadata *model.ObjectMeta) (*rbacProvenance, error) {
	if metadata == nil || metadata.Revision == nil {
		return nil, rbacUnavailableError()
	}
	namespace := namespaceValue(metadata.Namespace)
	if err := r.authorizeRBACRead(ctx, kind, "read", namespace, metadata.Name); err != nil {
		return nil, err
	}
	for _, builtin := range r.rbacBuiltinsResources {
		if builtin.Kind == kind && builtin.Metadata.UID == metadata.UID && builtin.Metadata.Name == metadata.Name &&
			*metadata.Revision == r.rbacBuiltinDigest {
			return &rbacProvenance{BuiltinName: builtin.Metadata.Name, BuiltinDigest: r.rbacBuiltinDigest}, nil
		}
	}
	select {
	case r.rbacSourceReads <- struct{}{}:
		defer func() { <-r.rbacSourceReads }()
	default:
		return nil, rbacUnavailableError()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	reader, ok := r.service.gitWriter.(rbacProvenanceReader)
	if !ok || r.rbacValidator == nil {
		return nil, rbacUnavailableError()
	}
	store, err := r.rbacStore()
	if err != nil {
		return nil, err
	}
	resource, err := store.GetRBACByUID(ctx, metadata.UID)
	if err != nil || resource == nil || resource.Kind != kind || resource.Metadata.Namespace != namespace ||
		resource.Metadata.Name != metadata.Name || resource.Metadata.Revision != *metadata.Revision {
		return nil, rbacUnavailableError()
	}
	tier := rbac.ClusterTierID
	if kind.Namespaced() {
		tier = resource.NamespaceUID
	}
	readOrigin := r.rbacValidator.Capture.Reader.ReadOrigin
	if readOrigin == nil {
		return nil, rbacUnavailableError()
	}
	origin, err := readOrigin(ctx, tier)
	if err != nil || !origin.Active || origin.Root != (tier == rbac.ClusterTierID) ||
		origin.RepositoryID == "" || !strings.HasPrefix(origin.GitRef, "refs/heads/") || len(origin.GitRef) > 256 || kind.Namespaced() &&
		(origin.NamespaceUID != resource.NamespaceUID || origin.Namespace != namespace) {
		return nil, rbacUnavailableError()
	}
	branch := strings.TrimPrefix(origin.GitRef, "refs/heads/")
	sha, found := strings.CutPrefix(*metadata.Revision, branch+"@sha1:")
	validSHA := func(value string) bool {
		decoded, err := hex.DecodeString(value)
		return err == nil && len(decoded) == 20 && value == strings.ToLower(value)
	}
	if branch == "" || !found || !validSHA(sha) {
		return nil, rbacUnavailableError()
	}
	repository, err := r.store.GetRepository(ctx, origin.RepositoryID)
	if err != nil || repository == nil || repository.Namespace != origin.Namespace ||
		repository.Name != SystemRepositoryName || repository.UID != origin.RepositoryID {
		return nil, rbacUnavailableError()
	}
	path, err := rbac.CanonicalPath(kind, metadata.Name)
	if err != nil {
		return nil, rbacUnavailableError()
	}
	provenance, err := reader.ReadFileProvenanceForRepo(ctx, origin.RepositoryID, path, sha)
	if err != nil || ctx.Err() != nil || provenance == nil || provenance.SnapshotCommitSha != sha ||
		provenance.Path != path || !validSHA(provenance.ChangeCommitSha) || provenance.CommitsExamined < 1 ||
		provenance.CommitsExamined > 256 || provenance.AuthorName == "" || len(provenance.AuthorName) > 256 ||
		!utf8.ValidString(provenance.AuthorName) || provenance.AuthorTimeUnix < -62135596800 || provenance.AuthorTimeUnix > 253402300799 {
		return nil, rbacUnavailableError()
	}
	authoredAt := time.Unix(provenance.AuthorTimeUnix, 0).UTC()
	r.logger.Info("rbac source inspected", zap.String("kind", string(kind)), zap.String("uid", metadata.UID),
		zap.String("revision", *metadata.Revision), zap.String("change_commit", provenance.ChangeCommitSha),
		zap.String("git_author", provenance.AuthorName))
	return &rbacProvenance{RepositoryRef: repository.Namespace + "/" + repository.Name,
		Ref: origin.GitRef, File: path, Commit: "sha1:" + sha, ChangeCommit: "sha1:" + provenance.ChangeCommitSha,
		Author: provenance.AuthorName, AuthoredAt: &authoredAt}, nil
}

func rbacAuthorError(err error) error {
	var rejected *admission.Error
	if errors.As(err, &rejected) {
		return rejected.ToGQLError()
	}
	var structured *gqlerror.Error
	if errors.As(err, &structured) {
		return structured
	}
	result := rbacUnavailableError().(*gqlerror.Error)
	result.Err = err
	return result
}

func rbacInputResource(kind rbac.Kind, version, discriminator string, metadata *model.ObjectMetaInput, spec any) (*rbac.Resource, error) {
	if version != rbac.APIVersion || discriminator != string(kind) || metadata == nil || metadata.Name == "" ||
		kind.Namespaced() && (metadata.Namespace == nil || *metadata.Namespace == "") ||
		!kind.Namespaced() && metadata.Namespace != nil {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_ENVELOPE", "the RBAC apiVersion, kind and scoped metadata are required")
	}
	labels, err := stringMap(metadata.Labels)
	if err != nil {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_LABELS", "RBAC labels must contain strings")
	}
	annotations, err := stringMap(metadata.Annotations)
	if err != nil {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_ANNOTATIONS", "RBAC annotations must contain strings")
	}
	switch input := spec.(type) {
	case *model.RoleBindingSpecInput:
		if input == nil {
			return nil, rbac.ErrInvalid
		}
		spec, err = rbacBindingInput(input.RoleRef, input.Subjects)
	case *model.ClusterRoleBindingSpecInput:
		if input == nil {
			return nil, rbac.ErrInvalid
		}
		spec, err = rbacBindingInput(input.RoleRef, input.Subjects)
	}
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	return &rbac.Resource{APIVersion: version, Kind: kind, Spec: data,
		Metadata: rbac.Metadata{Name: metadata.Name, Namespace: namespaceValue(metadata.Namespace), Labels: labels, Annotations: annotations}}, nil
}

var rbacRoleKinds = map[rbac.Kind]model.RBACRoleKind{
	rbac.Role: model.RBACRoleKindRole, rbac.ClusterRole: model.RBACRoleKindClusterRole,
}

var rbacSubjectKinds = map[string]model.RBACSubjectKind{
	"User": model.RBACSubjectKindUser, "Group": model.RBACSubjectKindGroup,
	"ServiceAccount": model.RBACSubjectKindServiceAccount,
}

func rbacBindingInput(ref *model.RBACRoleRefInput, subjects []*model.RBACSubjectInput) (rbac.BindingSpec, error) {
	spec := rbac.BindingSpec{Subjects: make([]rbac.Subject, 0, len(subjects))}
	if ref == nil {
		return spec, rbac.ErrInvalid
	}
	for kind, enum := range rbacRoleKinds {
		if ref.Kind == enum {
			spec.RoleRef = rbac.RoleRef{Kind: kind, Name: ref.Name}
		}
	}
	if spec.RoleRef.Kind == "" {
		return spec, rbac.ErrInvalid
	}
	for _, subject := range subjects {
		if subject == nil {
			return spec, rbac.ErrInvalid
		}
		var kind string
		for candidate, enum := range rbacSubjectKinds {
			if subject.Kind == enum {
				kind = candidate
			}
		}
		if kind == "" {
			return spec, rbac.ErrInvalid
		}
		spec.Subjects = append(spec.Subjects, rbac.Subject{Kind: kind, Name: subject.Name})
	}
	return spec, nil
}

func rbacRulesInput(rules []*model.RBACRuleInput) (rbac.RoleSpec, error) {
	spec := rbac.RoleSpec{Rules: make([]rbac.Rule, 0, len(rules))}
	for _, rule := range rules {
		if rule == nil {
			return spec, admission.NewError(admission.CodeBadUserInput, "INVALID_RULE", "an RBAC rule is required")
		}
		converted := rbac.Rule{Resources: rule.Resources, Verbs: rule.Verbs, ResourceNames: rule.ResourceNames}
		for _, condition := range rule.When {
			if condition == nil || (condition.Token == nil) == (condition.Attribute == nil) {
				return spec, admission.NewError(admission.CodeBadUserInput, "INVALID_CONDITION", "exactly one RBAC condition form is required")
			}
			entry := rbac.Condition{}
			if condition.Token != nil {
				entry.Token = *condition.Token
			} else {
				attribute := condition.Attribute
				entry.Attr, entry.Op, entry.Value, entry.Values = attribute.Attr, attribute.Op, attribute.Value, attribute.Values
			}
			converted.When = append(converted.When, entry)
		}
		spec.Rules = append(spec.Rules, converted)
	}
	return spec, nil
}

func (r *Resolver) mutateRBACSpec(ctx context.Context, kind rbac.Kind, version, discriminator string, metadata *model.ObjectMetaInput, spec any, operation admission.Operation) (model.Node, error) {
	resource, err := rbacInputResource(kind, version, discriminator, metadata, spec)
	if err != nil {
		return nil, rbacAuthorError(err)
	}
	resourceVersion := ""
	if operation == admission.OperationCreate {
		if metadata.ResourceVersion != nil {
			return nil, rbacAuthorError(admission.NewError(admission.CodeBadUserInput, "INVALID_RESOURCE_VERSION", "metadata.resourceVersion must be omitted on create"))
		}
	} else {
		if metadata.ResourceVersion == nil || *metadata.ResourceVersion == "" {
			return nil, rbacAuthorError(admission.NewError(admission.CodeBadUserInput, "INVALID_RESOURCE_VERSION", "metadata.resourceVersion is required for RBAC updates"))
		}
		resourceVersion = *metadata.ResourceVersion
	}
	node, _, err := r.commitRBAC(ctx, resource, operation, resourceVersion)
	return node, err
}

func (r *Resolver) mutateRBACRules(ctx context.Context, kind rbac.Kind, version, discriminator string, metadata *model.ObjectMetaInput, spec any, operation admission.Operation) (model.Node, error) {
	var rules []*model.RBACRuleInput
	switch value := spec.(type) {
	case *model.RoleSpecInput:
		if value == nil {
			return nil, rbacAuthorError(admission.NewError(admission.CodeBadUserInput, "INVALID_SPEC", "RBAC rules are required"))
		}
		rules = value.Rules
	case *model.ClusterRoleSpecInput:
		if value == nil {
			return nil, rbacAuthorError(admission.NewError(admission.CodeBadUserInput, "INVALID_SPEC", "RBAC rules are required"))
		}
		rules = value.Rules
	default:
		return nil, rbacAuthorError(admission.NewError(admission.CodeBadUserInput, "INVALID_SPEC", "RBAC rules are required"))
	}
	converted, err := rbacRulesInput(rules)
	if err != nil {
		return nil, rbacAuthorError(err)
	}
	return r.mutateRBACSpec(ctx, kind, version, discriminator, metadata, converted, operation)
}

func (r *Resolver) deleteRBAC(ctx context.Context, kind rbac.Kind, id, version string) (model.Node, model.ResourceDeletionOutcome, error) {
	if r.rbacValidator == nil {
		return nil, "", rbacUnavailableError()
	}
	uid, err := decodeNodeIDAs(string(kind), id)
	if err != nil {
		return nil, "", err
	}
	store, err := r.rbacStore()
	if err != nil {
		return nil, "", err
	}
	resource, err := store.GetRBACByUID(ctx, uid)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return nil, "", rbacAuthorError(err)
	}
	if errors.Is(err, datastore.ErrNotFound) || resource == nil || resource.Kind != kind {
		validation, err := r.rbacValidator.Capture.Begin(ctx, auth.PrincipalFromContext(ctx), "")
		if err != nil {
			return nil, "", rbacAuthorError(err)
		}
		defer validation.Close()
		resourceKind := strings.ToLower(string(kind[:1])) + string(kind[1:])
		decision, err := validation.Authorize(ctx, resourceKind+".delete", auth.ResourceContext{Kind: resourceKind})
		if err != nil {
			return nil, "", rbacAuthorError(err)
		}
		if decision.Outcome != auth.OutcomeAllow {
			return nil, "", rbacAuthorError(admission.NewError(admission.CodeForbidden, "RBAC_PERMISSION_REQUIRED", "forbidden"))
		}
		return nil, "", admission.ResourceNotFound(string(kind), "RBAC resource not found")
	}
	versionUID, _, err := decodeRBACResourceVersion(version)
	if err != nil {
		return nil, "", rbacAuthorError(err)
	}
	if versionUID != uid {
		return nil, "", rbacAuthorError(admission.NewError(admission.CodeBadUserInput, "INVALID_RESOURCE_VERSION", "resource version does not identify this Node"))
	}
	return r.commitRBAC(ctx, resource, admission.OperationDelete, version)
}

func (r *Resolver) commitRBAC(ctx context.Context, resource *rbac.Resource, operation admission.Operation, version string) (node model.Node, outcome model.ResourceDeletionOutcome, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = rbacAuthorError(resultErr)
		}
	}()
	if r.rbacValidator == nil || r.rbacValidator.Capture.Reader.ReadOrigin == nil || r.service.gitWriter == nil || resource == nil {
		return nil, "", rbacUnavailableError()
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	principal := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(principal) {
		return nil, "", rbacUnavailableError()
	}
	conditional, capable := r.service.gitWriter.(rbacConditionalWriter)
	if operation != admission.OperationDelete && !capable {
		return nil, "", rbacUnavailableError()
	}
	uid, expectedVersion := "", ""
	if operation != admission.OperationCreate {
		var err error
		uid, expectedVersion, err = decodeRBACResourceVersion(version)
		if err != nil {
			return nil, "", err
		}
	}
	namespace := resource.Metadata.Namespace
	if !resource.Kind.Namespaced() {
		namespace = SystemRepositoryName
	}
	parent, err := r.store.GetNamespaceByName(ctx, namespace)
	if err != nil || parent == nil || parent.DeletionTimestamp != nil {
		return nil, "", rbacUnavailableError()
	}
	tier := parent.UID
	if namespace == SystemRepositoryName {
		tier = rbac.ClusterTierID
	}
	path, err := rbac.CanonicalPath(resource.Kind, resource.Metadata.Name)
	if err != nil {
		return nil, "", admission.NewError(admission.CodeBadUserInput, "INVALID_RESOURCE_NAME", "invalid RBAC kind or name")
	}
	change := admission.RBACManifestChange{Path: path, Delete: operation == admission.OperationDelete,
		ExpectedUID: uid, ExpectedResourceVersion: expectedVersion, Operation: operation}
	if !change.Delete {
		change.Content, err = rbac.CanonicalManifest(resource)
		if err != nil {
			return nil, "", err
		}
	}
	for range 3 {
		origin, err := r.rbacValidator.Capture.Reader.ReadOrigin(ctx, tier)
		if err != nil {
			return nil, "", err
		}
		candidate := admission.RBACCandidate{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
			ExpectedGitRevision: origin.GitRevision, Changes: []admission.RBACManifestChange{change},
			Scope: rbac.RepositoryScope{Namespace: parent.Name, NamespaceUID: parent.UID, Root: tier == rbac.ClusterTierID}}
		admitted, err := r.rbacValidator.Validate(ctx, principal, candidate)
		if err != nil {
			return nil, "", err
		}
		branch := strings.TrimPrefix(origin.GitRef, "refs/heads/")
		expectedSHA, valid := strings.CutPrefix(origin.GitRevision, branch+"@sha1:")
		if !valid || !admitted.Canonical {
			admitted.Close()
			return nil, "", rbacUnavailableError()
		}
		if change.Delete && admitted.Write.Changes[0].Resource.Metadata.DeletionTimestamp != nil {
			current := admitted.Write.Changes[0].Resource
			admitted.Close()
			node, err := rbacResourceToGraphQL(current)
			return node, model.ResourceDeletionOutcomeAlreadyTerminating, err
		}
		treeDigest, err := admitted.Validation.ProposedTreeDigest(tier, admitted.Write.Changes)
		if err == nil {
			err = admitted.Validation.Check(ctx)
		}
		if err != nil {
			admitted.Close()
			return nil, "", err
		}
		var sha string
		writeCtx := gitclient.ContextWithPinnedBackingAuthorization(ctx, origin.RepositoryID, admitted.Validation.Review)
		if change.Delete {
			sha, err = r.service.gitWriter.DeleteFileForRepo(writeCtx, origin.RepositoryID, gitclient.DeleteFileParams{
				Path: path, RefName: origin.GitRef, ExpectedCommitSHA: expectedSHA,
				CommitMessage: "Delete " + string(resource.Kind) + " " + resource.Metadata.Name, AuthorName: principal.Subject})
		} else {
			sha, err = conditional.CommitFileAtRevisionForRepo(writeCtx, origin.RepositoryID, gitclient.CommitFileAtRevisionParams{
				CommitFileParams: gitclient.CommitFileParams{Path: path, Content: change.Content,
					CommitMessage: string(operation) + " " + string(resource.Kind) + " " + resource.Metadata.Name, AuthorName: principal.Subject},
				RefName: origin.GitRef, ExpectedCommitSHA: expectedSHA})
		}
		write := admitted.Write
		admitted.Close()
		if grpcstatus.Code(err) == codes.Aborted {
			continue
		}
		if err != nil {
			if grpcstatus.Code(err) == codes.Unimplemented {
				return nil, "", rbacUnavailableError()
			}
			return nil, "", admission.NewError(admission.CodeFailedPrecondition, "RBAC_COMMIT_OUTCOME_UNKNOWN", "Git outcome could not be confirmed; inspect the canonical revision before retrying")
		}
		if len(sha) != 40 || strings.ToLower(sha) != sha {
			return nil, "", admission.NewError(admission.CodeFailedPrecondition, "RBAC_COMMIT_OUTCOME_UNKNOWN", "Git did not return a canonical commit identity")
		}
		if _, err := hex.DecodeString(sha); err != nil {
			return nil, "", admission.NewError(admission.CodeFailedPrecondition, "RBAC_COMMIT_OUTCOME_UNKNOWN", "Git did not return a canonical commit identity")
		}
		write.Projection = &rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
			GitRevision: branch + "@sha1:" + sha, TreeDigest: treeDigest, Actor: principal.Subject}
		_, err = admission.ProjectCommittedRBAC(ctx, r.rbacValidator.Capture.Reader.Store, write)
		if err != nil {
			return nil, "", admission.Rejected(admission.PhasePostReceive, sha, []admission.Diagnostic{{
				Reason: "RBAC_PROJECTION_PENDING", Message: "Git commit succeeded; RBAC projection repair remains pending", Level: admission.LevelFailure,
			}})
		}
		projected, err := r.rbacValidator.Capture.Reader.Store.GetRBAC(ctx, rbac.ResourceKey{TierID: tier, Kind: resource.Kind, Name: resource.Metadata.Name})
		if err != nil {
			return nil, "", admission.Rejected(admission.PhasePostReceive, sha, []admission.Diagnostic{{
				Reason: "RBAC_PROJECTION_PENDING", Message: "Git commit succeeded; RBAC readback remains pending", Level: admission.LevelFailure,
			}})
		}
		admissionreport.Report(ctx, sha, nil)
		if change.Delete {
			outcome = model.ResourceDeletionOutcomeTerminationStarted
		}
		node, err := rbacResourceToGraphQL(projected)
		return node, outcome, err
	}
	return nil, "", admission.NewError(admission.CodeConflict, "RBAC_GIT_CONFLICT", "canonical Git revision changed; retry with a current resource version")
}

func (r *Resolver) authorizeRBACRead(ctx context.Context, kind rbac.Kind, verb, namespace, name string) error {
	if r.registry == nil {
		return rbacUnavailableError()
	}
	provider := r.registry.AuthZ()
	if provider == nil {
		return rbacUnavailableError()
	}
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil {
		principal = auth.Anonymous()
	}
	resourceKind := strings.ToLower(string(kind[:1])) + string(kind[1:])
	resource := auth.ResourceContext{Kind: resourceKind, Name: name}
	if kind.Namespaced() {
		resource.Attrs = map[string]any{"namespace": namespace}
	}
	decision, err := provider.Authorize(ctx, principal, resourceKind+"."+verb, resource)
	if err != nil {
		result := rbacUnavailableError().(*gqlerror.Error)
		result.Err = err
		return result
	}
	if decision.Outcome != auth.OutcomeAllow {
		return admission.NewError(admission.CodeForbidden, "RBAC_PERMISSION_REQUIRED", "forbidden").ToGQLError()
	}
	return nil
}

func rbacReadError(kind rbac.Kind, err error) error {
	if errors.Is(err, rbac.ErrUnavailable) {
		result := rbacUnavailableError().(*gqlerror.Error)
		result.Err = err
		return result
	}
	return nodeLookupError(string(kind), err)
}

func (r *Resolver) rbacStore() (rbac.Store, error) {
	capable, ok := r.store.(datastore.RBACCapable)
	if !ok || capable.RBACStore() == nil {
		return nil, rbacUnavailableError()
	}
	return capable.RBACStore(), nil
}

func (r *Resolver) readRBACNode(ctx context.Context, kind rbac.Kind, uid string) (model.Node, error) {
	store, err := r.rbacStore()
	if err != nil {
		return nil, err
	}
	var resource *rbac.Resource
	for _, builtin := range r.rbacBuiltinsResources {
		if builtin.Kind == kind && builtin.Metadata.UID == uid {
			resource = builtin
			break
		}
	}
	if resource == nil {
		resource, err = store.GetRBACByUID(ctx, uid)
		if err == nil && (resource == nil || resource.Kind != kind) {
			resource, err = nil, datastore.ErrNotFound
		}
	}
	namespace, name := "", ""
	if resource != nil {
		namespace, name = resource.Metadata.Namespace, resource.Metadata.Name
	}
	if denied := r.authorizeRBACRead(ctx, kind, "read", namespace, name); denied != nil {
		return nil, denied
	}
	if err != nil {
		return nil, rbacReadError(kind, err)
	}
	if resource.Metadata.UID != uid {
		return nil, rbacReadError(kind, rbac.ErrUnavailable)
	}
	if kind.Namespaced() {
		parent, parentErr := r.store.GetNamespaceByName(ctx, namespace)
		if parentErr != nil {
			return nil, rbacReadError(kind, parentErr)
		}
		if parent == nil || parent.UID != resource.NamespaceUID {
			return nil, rbacReadError(kind, datastore.ErrNotFound)
		}
	}
	node, err := rbacResourceToGraphQL(resource)
	if err != nil {
		return nil, rbacReadError(kind, err)
	}
	return node, nil
}

func (r *Resolver) readRBAC(ctx context.Context, kind rbac.Kind, id *string, path *model.RBACNamespacePath, name *string) (model.Node, error) {
	choices := 0
	for _, present := range []bool{id != nil, path != nil, name != nil} {
		if present {
			choices++
		}
	}
	if choices != 1 || (path != nil && !kind.Namespaced()) || (name != nil && kind.Namespaced()) {
		return nil, admission.NewError(admission.CodeBadUserInput, "INVALID_SELECTOR", "exactly one scoped RBAC selector is required").ToGQLError()
	}
	if id != nil {
		uid, err := decodeNodeIDAs(string(kind), *id)
		if err != nil {
			return nil, err
		}
		return r.readRBACNode(ctx, kind, uid)
	}
	namespace, resourceName := "", ""
	if path != nil {
		namespace, resourceName = path.Namespace, path.Name
	} else {
		resourceName = *name
	}
	if err := r.authorizeRBACRead(ctx, kind, "read", namespace, resourceName); err != nil {
		return nil, err
	}
	tier := rbac.ClusterTierID
	if path != nil {
		parent, err := r.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return nil, rbacReadError(kind, err)
		}
		if parent == nil {
			return nil, rbacReadError(kind, datastore.ErrNotFound)
		}
		tier = parent.UID
	}
	for _, builtin := range r.rbacBuiltinsResources {
		if builtin.Kind == kind && builtin.Metadata.Name == resourceName {
			return rbacResourceToGraphQL(builtin)
		}
	}
	store, err := r.rbacStore()
	if err != nil {
		return nil, err
	}
	resource, err := store.GetRBAC(ctx, rbac.ResourceKey{TierID: tier, Kind: kind, Name: resourceName})
	if err != nil {
		return nil, rbacReadError(kind, err)
	}
	if resource == nil || kind.Namespaced() && resource.NamespaceUID != tier ||
		resource.Metadata.Namespace != namespace || resource.Kind != kind {
		return nil, rbacReadError(kind, rbac.ErrUnavailable)
	}
	node, err := rbacResourceToGraphQL(resource)
	if err != nil {
		return nil, rbacReadError(kind, err)
	}
	return node, nil
}

type rbacListCursor struct {
	Tier     string    `json:"tier"`
	Kind     rbac.Kind `json:"kind"`
	Revision uint64    `json:"revision"`
	Builtins string    `json:"builtins"`
	Selector string    `json:"selector"`
	Key      string    `json:"key"`
}

func rbacCreatedAt(resource *rbac.Resource) time.Time {
	if resource.Metadata.CreationTimestamp != nil {
		return *resource.Metadata.CreationTimestamp
	}
	return time.Unix(0, 0).UTC()
}

func (r *Resolver) listRBAC(ctx context.Context, kind rbac.Kind, namespace string, first *int32, after *string, selector *model.LabelSelectorInput) ([]model.Node, []string, *model.PageInfo, error) {
	if first != nil && (*first < 1 || *first > 500) {
		return nil, nil, nil, admission.NewError(admission.CodeBadUserInput, "INVALID_PAGINATION", "RBAC page size must be between 1 and 500").ToGQLError()
	}
	if err := r.authorizeRBACRead(ctx, kind, "list", namespace, ""); err != nil {
		return nil, nil, nil, err
	}
	tier := rbac.ClusterTierID
	if kind.Namespaced() {
		parent, err := r.store.GetNamespaceByName(ctx, namespace)
		if err != nil {
			return nil, nil, nil, rbacReadError(kind, err)
		}
		if parent == nil {
			return nil, nil, nil, rbacReadError(kind, datastore.ErrNotFound)
		}
		tier = parent.UID
	}
	store, err := r.rbacStore()
	if err != nil {
		return nil, nil, nil, err
	}
	snapshot, err := datastore.ReadRBACSnapshot(ctx, store, tier)
	if err != nil {
		return nil, nil, nil, rbacReadError(kind, err)
	}
	if snapshot.State.ProjectionPending {
		return nil, nil, nil, rbacUnavailableError()
	}
	selectorJSON, err := json.Marshal(selector)
	if err != nil {
		return nil, nil, nil, err
	}
	selectorDigest := sha256.Sum256(selectorJSON)
	cursor := rbacListCursor{Tier: tier, Kind: kind, Revision: snapshot.State.Revision,
		Builtins: r.rbacBuiltinDigest, Selector: hex.EncodeToString(selectorDigest[:])}
	var previous *KeysetCursor
	if after != nil {
		if len(*after) > 2048 {
			return nil, nil, nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid scoped RBAC cursor").ToGQLError()
		}
		raw, decodeErr := base64.RawURLEncoding.DecodeString(*after)
		var supplied rbacListCursor
		if decodeErr != nil || json.Unmarshal(raw, &supplied) != nil ||
			supplied.Tier != tier || supplied.Kind != kind || supplied.Builtins != cursor.Builtins || supplied.Selector != cursor.Selector {
			return nil, nil, nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid scoped RBAC cursor").ToGQLError()
		}
		if supplied.Revision != cursor.Revision {
			return nil, nil, nil, admission.NewError(admission.CodeConflict, "RBAC_LIST_CHANGED", "RBAC list changed; restart pagination").ToGQLError()
		}
		previous, err = DecodeKeysetCursor(supplied.Key)
		if err != nil {
			return nil, nil, nil, admission.NewError(admission.CodeBadUserInput, "INVALID_CURSOR", "invalid RBAC keyset cursor").ToGQLError()
		}
	}
	resources := make([]*rbac.Resource, 0, len(snapshot.Resources)+len(r.rbacBuiltinsResources))
	seen := map[string]bool{}
	for _, resource := range append(slices.Clone(r.rbacBuiltinsResources), snapshot.Resources...) {
		if resource.Kind != kind {
			continue
		}
		if kind.Namespaced() && resource.Metadata.Namespace != namespace {
			return nil, nil, nil, rbacUnavailableError()
		}
		if seen[resource.Metadata.Name] {
			return nil, nil, nil, rbacUnavailableError()
		}
		seen[resource.Metadata.Name] = true
		if !matchesWatchSelector(selector, resource.Metadata.Labels) {
			continue
		}
		if previous != nil && (rbacCreatedAt(resource).After(previous.CreatedAt) ||
			rbacCreatedAt(resource).Equal(previous.CreatedAt) && resource.Metadata.UID >= previous.ID) {
			continue
		}
		resources = append(resources, resource)
	}
	slices.SortFunc(resources, func(a, b *rbac.Resource) int {
		if compared := rbacCreatedAt(b).Compare(rbacCreatedAt(a)); compared != 0 {
			return compared
		}
		return strings.Compare(b.Metadata.UID, a.Metadata.UID)
	})
	limit := toPageParams(first, nil, nil, nil).Limit()
	info := &model.PageInfo{HasNextPage: len(resources) > limit, HasPreviousPage: after != nil}
	resources = resources[:min(limit, len(resources))]
	nodes, cursors := make([]model.Node, 0, len(resources)), make([]string, 0, len(resources))
	for _, resource := range resources {
		node, err := rbacResourceToGraphQL(resource)
		if err != nil {
			return nil, nil, nil, rbacReadError(kind, err)
		}
		cursor.Key = EncodeKeysetCursor(rbacCreatedAt(resource), resource.Metadata.UID)
		raw, err := json.Marshal(cursor)
		if err != nil {
			return nil, nil, nil, err
		}
		nodes, cursors = append(nodes, node), append(cursors, base64.RawURLEncoding.EncodeToString(raw))
	}
	if len(cursors) > 0 {
		info.StartCursor, info.EndCursor = &cursors[0], &cursors[len(cursors)-1]
	}
	return nodes, cursors, info, nil
}

func decodeRBACResourceVersion(version string) (string, string, error) {
	if len(version) > 256 {
		return "", "", admission.NewError(admission.CodeBadUserInput, "INVALID_RESOURCE_VERSION", "invalid RBAC resource version")
	}
	raw, err := base64.RawURLEncoding.DecodeString(version)
	uid, resourceVersion, ok := strings.Cut(string(raw), "\n")
	if err != nil || !ok || uid == "" || resourceVersion == "" || strings.Contains(resourceVersion, "\n") {
		return "", "", admission.NewError(admission.CodeBadUserInput, "INVALID_RESOURCE_VERSION", "invalid RBAC resource version")
	}
	return uid, resourceVersion, nil
}

func rbacResourceToGraphQL(resource *rbac.Resource) (model.Node, error) {
	if resource == nil || resource.Metadata.Generation < 0 || resource.Metadata.Generation > math.MaxInt32 {
		return nil, rbac.ErrUnavailable
	}
	id, err := EncodeNodeID(string(resource.Kind), resource.Metadata.UID)
	if err != nil {
		return nil, err
	}
	metadata := &model.ObjectMeta{OwnerReferences: []*model.OwnerReference{}, Finalizers: []string{},
		CreationTimestamp: time.Unix(0, 0).UTC()}
	data, err := json.Marshal(resource.Metadata)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, metadata); err != nil {
		return nil, err
	}
	metadata.ResourceVersion = base64.RawURLEncoding.EncodeToString([]byte(resource.Metadata.UID + "\n" + resource.Metadata.ResourceVersion))
	if metadata.OwnerReferences == nil {
		metadata.OwnerReferences = []*model.OwnerReference{}
	}

	if metadata.Finalizers == nil {
		metadata.Finalizers = []string{}
	}
	status := &model.RBACStatus{Conditions: []*model.Condition{}}
	if len(resource.Status) > 0 {
		var stored struct {
			ObservedGeneration int32          `json:"observedGeneration"`
			Conditions         []rawCondition `json:"conditions"`
		}
		if err := json.Unmarshal(resource.Status, &stored); err != nil {
			return nil, err
		}
		status.ObservedGeneration = stored.ObservedGeneration
		for _, condition := range stored.Conditions {
			status.Conditions = append(status.Conditions, conditionFromRaw(condition))
		}
	}
	if status.Conditions == nil {
		status.Conditions = []*model.Condition{}
	}
	status.Resolved = nil
	if resource.Kind.IsRole() {
		spec, err := rbac.DecodeRoleSpec(resource)
		if err != nil {
			return nil, err
		}
		rules := make([]*model.RBACRule, 0, len(spec.Rules))
		for _, rule := range spec.Rules {
			converted := &model.RBACRule{Resources: rule.Resources, Verbs: rule.Verbs, ResourceNames: rule.ResourceNames}
			for _, condition := range rule.When {
				entry := &model.RBACCondition{}
				if condition.Token != "" {
					entry.Token = &condition.Token
				} else {
					entry.Attribute = &model.RBACAttributeCondition{
						Attr: condition.Attr, Op: condition.Op, Value: condition.Value, Values: condition.Values,
					}
				}
				converted.When = append(converted.When, entry)
			}
			rules = append(rules, converted)
		}
		if resource.Kind == rbac.Role {
			return &model.Role{ID: id, APIVersion: resource.APIVersion, Kind: string(resource.Kind),
				Metadata: metadata, Spec: &model.RoleSpec{Rules: rules}, Status: status}, nil
		}
		return &model.ClusterRole{ID: id, APIVersion: resource.APIVersion, Kind: string(resource.Kind),
			Metadata: metadata, Spec: &model.ClusterRoleSpec{Rules: rules}, Status: status}, nil
	}
	spec, err := rbac.DecodeBindingSpec(resource)
	if err != nil {
		return nil, err
	}
	roleKind, ok := rbacRoleKinds[spec.RoleRef.Kind]
	if !ok {
		return nil, rbac.ErrUnavailable
	}
	ref := &model.RBACRoleRef{Kind: roleKind, Name: spec.RoleRef.Name}
	subjects := make([]*model.RBACSubject, 0, len(spec.Subjects))
	for _, subject := range spec.Subjects {
		subjectKind, ok := rbacSubjectKinds[subject.Kind]
		if !ok {
			return nil, rbac.ErrUnavailable
		}
		subjects = append(subjects, &model.RBACSubject{Kind: subjectKind, Name: subject.Name})
	}
	resolvedUID, err := rbac.ResolvedRoleUID(resource)
	if err != nil {
		return nil, err
	}
	if resolvedUID != "" {
		status.Resolved = &model.RBACResolvedRole{RoleUID: resolvedUID, RoleKind: ref.Kind, RoleName: ref.Name}
	}
	switch resource.Kind {
	case rbac.RoleBinding:
		return &model.RoleBinding{ID: id, APIVersion: resource.APIVersion, Kind: string(resource.Kind),
			Metadata: metadata, Spec: &model.RoleBindingSpec{RoleRef: ref, Subjects: subjects}, Status: status}, nil
	case rbac.ClusterRoleBinding:
		return &model.ClusterRoleBinding{ID: id, APIVersion: resource.APIVersion, Kind: string(resource.Kind),
			Metadata: metadata, Spec: &model.ClusterRoleBindingSpec{RoleRef: ref, Subjects: subjects}, Status: status}, nil
	default:
		return nil, fmt.Errorf("unsupported RBAC kind %q", resource.Kind)
	}
}
