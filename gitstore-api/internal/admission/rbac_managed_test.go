// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type managedDirectory struct {
	auth.UserDirProvider
	profile *auth.UserProfile
}

func (d managedDirectory) GetBySubject(context.Context, string) (*auth.UserProfile, error) {
	return d.profile, nil
}

type qualifiedManagedDirectory struct {
	managedDirectory
	resolve func(context.Context, *auth.Principal) (*auth.UserProfile, error)
}

func (d qualifiedManagedDirectory) ResolvePrincipal(ctx context.Context, principal *auth.Principal) (*auth.UserProfile, error) {
	return d.resolve(ctx, principal)
}

func (qualifiedManagedDirectory) ListActors(context.Context, auth.ActorPageRequest) (*auth.ActorPage, error) {
	return nil, auth.ErrNotSupported
}

func (qualifiedManagedDirectory) GetBySubject(context.Context, string) (*auth.UserProfile, error) {
	return nil, auth.ErrNotSupported
}

func managedFixture(t *testing.T, methods ...string) (admission.RBACValidator, admission.RBACCandidate, *auth.Principal, *rbac.Resource) {
	t.Helper()
	method := "static-users"
	if len(methods) != 0 {
		method = methods[0]
	}
	validator, candidate := candidateFixture(t, nil)
	store := validator.ManagedStore
	require.NoError(t, store.CreateNamespace(t.Context(), &datastore.Namespace{
		UID: candidate.Scope.NamespaceUID, Name: candidate.Scope.Namespace, Tier: datastore.NamespaceTierUser, CreationActor: candidate.Scope.Namespace,
		Annotations: map[string]string{
			datastore.OwnerAuthMethodAnnotationKey: method,
			datastore.OwnerIssuerAnnotationKey:     "fixture-issuer",
		},
	}))
	controller := &auth.Principal{Subject: "system:serviceaccount:gitstore-controller-manager",
		AuthMethod: "serviceaccount-jwt", ServiceAccountUID: uuid.NewString()}
	require.NoError(t, store.CreateServiceAccount(t.Context(), &datastore.ServiceAccount{
		UID: controller.ServiceAccountUID, Name: "gitstore-controller-manager",
	}))
	registry := validator.Capture.Registry
	validator.Capture.Registry = auth.NewProviderRegistry(nil, registry.AuthZ(), managedDirectory{
		profile: &auth.UserProfile{Subject: "directory-uid", Username: "directory-name", Active: true, Kind: auth.ProfileStaff, Lifecycle: auth.ActorActive},
	})
	validator.ControllerSubject = controller.Subject
	template := rbac.ManagedBindingTemplate{Namespace: candidate.Scope.Namespace, NamespaceUID: candidate.Scope.NamespaceUID, Subject: candidate.Scope.Namespace}
	resource, err := template.Resource()
	require.NoError(t, err)
	return validator, candidate, controller, resource
}

func managedChange(t *testing.T, candidate *admission.RBACCandidate, resource *rbac.Resource) {
	t.Helper()
	content, err := rbac.CanonicalManifest(resource)
	require.NoError(t, err)
	candidate.Changes = []admission.RBACManifestChange{{Path: "rbac/rolebindings/namespace-owner-access.yaml", Content: content}}
}

func TestManagedBindingUsesProtectedOwnerAuthenticationWithoutReplacingSubject(t *testing.T) {
	for _, method := range []string{"oidc-jwt", "static-users", ""} {
		t.Run(method, func(t *testing.T) {
			validator, candidate, controller, resource := managedFixture(t, method)
			registry := validator.Capture.Registry
			directory := registry.UserDir().(managedDirectory)
			ownerResolved := false
			validator.Capture.Registry = auth.NewProviderRegistry(nil, registry.AuthZ(), qualifiedManagedDirectory{
				managedDirectory: directory,
				resolve: func(_ context.Context, principal *auth.Principal) (*auth.UserProfile, error) {
					if principal.ServiceAccountUID != "" {
						return nil, auth.ErrProfileNotFound
					}
					require.Equal(t, "shop", principal.Subject)
					require.Equal(t, method, principal.AuthMethod)
					require.Equal(t, "fixture-issuer", principal.Issuer)
					ownerResolved = true
					return directory.profile, nil
				},
			})
			managedChange(t, &candidate, resource)
			result, err := validator.Validate(t.Context(), controller, candidate)
			if method == "" {
				require.Error(t, err)
				require.Nil(t, result)
				require.False(t, ownerResolved, "missing provenance must not guess an issuer/provider")
				return
			}
			require.NoError(t, err)
			defer result.Close()
			require.True(t, ownerResolved)
			spec, err := rbac.DecodeBindingSpec(result.Write.Changes[0].Resource)
			require.NoError(t, err)
			require.Equal(t, []rbac.Subject{{Kind: "User", Name: "shop"}}, spec.Subjects)
		})
	}
}

func TestManagedBindingCannotAdoptCollisionsOrBeRemovedOrReplaced(t *testing.T) {
	for _, scenario := range []string{"ordinary collision", "ordinary collision delete", "ordinary collision unchanged final tree", "administrator delete", "controller delete", "subject replacement", "old namespace manifest", "unchanged final tree"} {
		t.Run(scenario, func(t *testing.T) {
			validator, candidate, controller, resource := managedFixture(t)
			managedChange(t, &candidate, resource)
			result, err := validator.Validate(t.Context(), controller, candidate)
			require.NoError(t, err)
			write := result.Write
			result.Close()
			if strings.HasPrefix(scenario, "ordinary collision") {
				write.Changes[0].Resource.Metadata.UID = uuid.NewString()
				write.Changes[0].Resource.Metadata.Annotations = nil
			}
			readOrigin := validator.Capture.Reader.ReadOrigin
			origin, err := readOrigin(t.Context(), candidate.Scope.NamespaceUID)
			require.NoError(t, err)
			origin.GitRevision = "main@sha1:" + strings.Repeat("e", 40)
			origin.TreeDigest, err = rbac.ManifestTreeDigest([]*rbac.Resource{write.Changes[0].Resource})
			require.NoError(t, err)
			write.Projection = &rbac.ProjectionIdentity{RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
				GitRevision: origin.GitRevision, TreeDigest: origin.TreeDigest, Actor: controller.Subject}
			_, err = validator.Capture.Reader.Store.ApplyRBAC(t.Context(), write)
			require.NoError(t, err)
			candidate.ExpectedGitRevision = origin.GitRevision
			validator.Capture.Reader.ReadOrigin = func(ctx context.Context, tier string) (rbac.SnapshotOrigin, error) {
				if tier == candidate.Scope.NamespaceUID {
					return origin, nil
				}
				return readOrigin(ctx, tier)
			}
			actor := controller
			switch scenario {
			case "administrator delete", "controller delete", "ordinary collision delete":
				candidate.Changes[0].Content = nil
				candidate.Changes[0].Delete = true
				candidate.FinalTree = map[string][]byte{}
				if scenario != "controller delete" {
					actor = &auth.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
				}
			case "subject replacement":
				resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"namespace-full-control"},"subjects":[{"kind":"User","name":"other"}]}`)
				managedChange(t, &candidate, resource)
			case "old namespace manifest":
				resource.Metadata.Annotations["rbac.authorization.gitstore.dev/namespace-uid"] = uuid.NewString()
				managedChange(t, &candidate, resource)
			case "unchanged final tree", "ordinary collision unchanged final tree":
				actor = &auth.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
				body := candidate.Changes[0].Content
				if scenario == "ordinary collision unchanged final tree" {
					body, err = rbac.CanonicalManifest(write.Changes[0].Resource)
					require.NoError(t, err)
				}
				change := roleChange(t, candidate, "unrelated", []rbac.Rule{{Resources: []string{"product"}, Verbs: []string{"read"}}})
				candidate.FinalTree = map[string][]byte{candidate.Changes[0].Path: body, change.Path: change.Content}
				candidate.Changes = []admission.RBACManifestChange{change}
			}
			result, err = validator.Validate(t.Context(), actor, candidate)
			if scenario == "unchanged final tree" || scenario == "ordinary collision unchanged final tree" || scenario == "ordinary collision delete" {
				require.NoError(t, err, "ordinary collision cleanup and unchanged manifests must retain normal authorization")
				result.Close()
			} else {
				require.Error(t, err)
				require.Nil(t, result)
			}
		})
	}
}

func TestManagedBindingRequiresLiveEnrolledControllerAndTrustedStaffSubject(t *testing.T) {
	for _, scenario := range []string{"valid", "customer", "serviceaccount owner", "username substituted", "directory uid substituted", "inactive owner", "expired owner",
		"wrong namespace uid", "wrong role", "copied annotations", "administrator", "owner", "old controller uid", "disabled controller"} {
		t.Run(scenario, func(t *testing.T) {
			validator, candidate, controller, resource := managedFixture(t)
			profile := validator.Capture.Registry.UserDir().(managedDirectory).profile
			switch scenario {
			case "customer":
				profile.Kind = auth.ProfileCustomer
			case "serviceaccount owner":
				profile.Kind = auth.ProfileServiceAccount
			case "username substituted":
				resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"namespace-full-control"},"subjects":[{"kind":"User","name":"directory-name"}]}`)
			case "directory uid substituted":
				resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"namespace-full-control"},"subjects":[{"kind":"User","name":"directory-uid"}]}`)
			case "inactive owner":
				profile.Active = false
			case "expired owner":
				profile.FreshUntil = time.Unix(1, 0)
			case "wrong namespace uid":
				resource.Metadata.Annotations["rbac.authorization.gitstore.dev/namespace-uid"] = uuid.NewString()
			case "wrong role":
				resource.Spec = json.RawMessage(`{"roleRef":{"kind":"ClusterRole","name":"cluster-admin"},"subjects":[{"kind":"User","name":"shop"}]}`)
			case "copied annotations":
				resource.Metadata.Name = "copied"
			case "administrator":
				controller = &auth.Principal{Subject: "admin", AuthMethod: "static-users", Groups: []string{"system:masters"}}
			case "owner":
				controller = &auth.Principal{Subject: "shop", AuthMethod: "static-users", Username: "directory-name", ProfileKind: auth.ProfileStaff}
			case "old controller uid":
				controller.ServiceAccountUID = uuid.NewString()
			case "disabled controller":
				require.NoError(t, validator.ManagedStore.SetServiceAccountDisabled(t.Context(), controller.ServiceAccountUID, true))
			}

			managedChange(t, &candidate, resource)
			if scenario == "copied annotations" {
				candidate.Changes[0].Path = "rbac/rolebindings/copied.yaml"
			}
			result, err := validator.Validate(t.Context(), controller, candidate)
			if scenario != "valid" {
				require.Error(t, err)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			defer result.Close()
			written := result.Write.Changes[0].Resource
			require.Equal(t, "shop", written.Metadata.Annotations["rbac.authorization.gitstore.dev/initiator"])
			spec, err := rbac.DecodeBindingSpec(written)
			require.NoError(t, err)
			require.Equal(t, []rbac.Subject{{Kind: "User", Name: "shop"}}, spec.Subjects)
			require.Equal(t, (rbac.ManagedBindingTemplate{Namespace: candidate.Scope.Namespace,
				NamespaceUID: candidate.Scope.NamespaceUID, Subject: "shop"}).UID(), written.Metadata.UID)
		})
	}
}
