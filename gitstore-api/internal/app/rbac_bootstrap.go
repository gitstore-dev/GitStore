// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

type rbacBootstrapGit interface {
	rbac.OriginGitReader
	CommitFileAtRevisionForRepo(context.Context, string, gitclient.CommitFileAtRevisionParams) (string, error)
}

// This composes initialization and readiness without activating production
// serving. The registered application must supply a purpose-bound Git client.
func newInitializedCanonicalRBACRuntime(ctx context.Context, store datastore.Datastore, journal datastore.ResourceWatchJournal,
	git rbacBootstrapGit, limits config.OPAConfig, log *zap.Logger, options ...rbacRuntimeOptions) (*rbacRuntime, error) {
	if err := limits.Validate("opa"); err != nil {
		return nil, err
	}
	builtins, err := rbac.ReleaseBuiltins()
	if err != nil {
		return nil, err
	}
	repairRegistry := auth.NewProviderRegistry(nil, nil, nil)
	projector, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
		Store: store, GitReader: git, Registry: repairRegistry, Logger: log,
		RBACValidator: &admission.RBACValidator{Builtins: builtins,
			Capture: rbac.ValidationCapture{Registry: repairRegistry, Reader: rbac.SnapshotReader{MaxBytes: limits.RebuildMemoryBytes}}},
	})
	if err != nil {
		return nil, err
	}
	var option rbacRuntimeOptions
	if len(options) > 0 {
		option = options[0]
	}
	option.initializing = true
	runtime, err := newCanonicalRBACRuntime(ctx, store, journal, git, limits, projector.ReconcileRBACTier, option)
	if err != nil {
		return nil, err
	}
	runtime.bootstrapDone = make(chan struct{})
	validator := &admission.RBACValidator{Builtins: builtins,
		Capture: runtime.validationCapture(auth.NewProviderRegistry(nil, runtime.provider, nil))}
	go func() {
		defer close(runtime.bootstrapDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		prepare := option.prepare
		var lastFailureLog time.Time
		logFailure := func(stage string, err error) {
			if log != nil && time.Since(lastFailureLog) >= time.Minute {
				log.Warn("RBAC initialization pending", zap.String("stage", stage), zap.Error(err))
				lastFailureLog = time.Now()
			}
		}
		for {
			if prepare != nil {
				attempt, cancel := context.WithTimeout(runtime.ctx, limits.ValidationTimeout)
				err := prepare(attempt)
				cancel()
				if err != nil {
					logFailure("prepare", err)
					select {
					case <-runtime.ctx.Done():
						return
					case <-runtime.done:
						return
					case <-ticker.C:
						continue
					}
				}
				prepare = nil
			}
			reservation, reserveErr := runtime.scratch.Reserve(runtime.reader.MaxBytes)
			initializeErr := reserveErr
			if initializeErr == nil {
				initializeErr = initializeRBACAdministrator(runtime.ctx, store, git, validator, projector.ReconcileRBACTier, limits.InitialAdminSubject)
			}
			if initializeErr != nil {
				logFailure("administrator", initializeErr)
			} else {
				current, err := runtime.reader.Read(runtime.ctx, rbac.ClusterTierID)
				published, ok := runtime.provider.AuthorizationSnapshot()
				if err == nil && ok && len(published.Tiers) == 1 {
					tier := published.Tiers[0]
					if tier.Revision == current.State.Revision && tier.GitRevision == current.State.Projection.GitRevision &&
						tier.ContentDigest == current.ContentDigest {
						runtime.initializing.Store(false)
						reservation.Close()
						return
					}
				}
			}
			if reservation != nil {
				reservation.Close()
			}
			select {
			case <-runtime.ctx.Done():
				return
			case <-runtime.done:
				return
			case <-ticker.C:
			}
		}
	}()
	return runtime, nil
}

func initialAdministratorManifest(subject string) ([]byte, error) {
	if !auth.ValidTrustedPrincipal(&auth.Principal{Subject: subject, AuthMethod: "bootstrap"}) {
		return nil, rbac.ErrInvalid
	}
	spec, err := json.Marshal(rbac.BindingSpec{
		RoleRef:  rbac.RoleRef{Kind: rbac.ClusterRole, Name: "cluster-admin"},
		Subjects: []rbac.Subject{{Kind: "User", Name: subject}},
	})
	if err != nil {
		return nil, err
	}
	return rbac.CanonicalManifest(&rbac.Resource{APIVersion: rbac.APIVersion, Kind: rbac.ClusterRoleBinding,
		Metadata: rbac.Metadata{Name: "initial-admin"}, Spec: spec})
}

func persistRBACBootstrap(ctx context.Context, store datastore.NamespaceStore, namespace *datastore.Namespace, receipt datastore.NamespaceRBACBootstrap) error {
	version := namespace.ResourceVersion
	if err := datastore.SetNamespaceRBACBootstrap(namespace, receipt); err != nil {
		return err
	}
	datastore.AdvanceNamespaceSystemVersion(namespace)
	namespace.UpdateActor, namespace.UpdateTimestamp = bootstrapActor, time.Now().UTC()
	return store.UpdateNamespace(ctx, namespace, version)
}

// This internal step never registers a provider or grants a request identity.
// The Namespace intent and retained Git receipt survive independent API retries.
func initializeRBACAdministrator(ctx context.Context, store datastore.Datastore, git rbacBootstrapGit,
	validator *admission.RBACValidator, repair func(context.Context, string) error, subject string) error {
	if store == nil || git == nil || validator == nil || validator.Capture.Reader.Store == nil || repair == nil {
		return rbac.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	source := rbac.OriginReader{Store: store, Git: git, MaxBytes: validator.Capture.Reader.MaxBytes}
	for range 3 {
		namespace, err := store.GetNamespaceByName(ctx, "gitstore-system")
		if err != nil || namespace == nil || namespace.DeletionTimestamp != nil {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		receipt, err := datastore.ReadNamespaceRBACBootstrap(namespace)
		if err != nil {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		if receipt == nil {
			if _, err := initialAdministratorManifest(subject); err != nil {
				return errors.Join(rbac.ErrUnavailable, err)
			}
			origin, resources, err := source.ReadTree(ctx, rbac.ClusterTierID)
			if err != nil || len(resources) != 0 {
				return errors.Join(rbac.ErrUnavailable, err)
			}
			if origin.NamespaceUID != namespace.UID || origin.NamespaceVersion != namespace.ResourceVersion {
				continue
			}
			if err := repair(ctx, rbac.ClusterTierID); err != nil {
				return errors.Join(rbac.ErrUnavailable, err)
			}
			state, err := validator.Capture.Reader.Store.ReadRBACTier(ctx, rbac.ClusterTierID)
			if err != nil || state.Revision != 1 || state.RoleCount != 0 || state.BindingCount != 0 ||
				state.ProjectionPending || state.Projection.GitRevision != origin.GitRevision {
				return errors.Join(rbac.ErrUnavailable, err)
			}
			receipt = &datastore.NamespaceRBACBootstrap{NamespaceUID: namespace.UID,
				RepositoryID: origin.RepositoryID, GitRef: origin.GitRef,
				ExpectedCommit: strings.SplitN(origin.GitRevision, "@sha1:", 2)[1], Subject: subject}
			if err := persistRBACBootstrap(ctx, store, namespace, *receipt); errors.Is(err, datastore.ErrConflict) {
				continue
			} else if err != nil {
				return errors.Join(rbac.ErrUnavailable, err)
			}
			continue
		}
		content, err := initialAdministratorManifest(receipt.Subject)
		if err != nil {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		params := gitclient.CommitFileAtRevisionParams{
			CommitFileParams: gitclient.CommitFileParams{
				Path: "rbac/clusterrolebindings/initial-admin.yaml", Content: content,
				CommitMessage: "Initialize RBAC for " + namespace.UID, AuthorName: bootstrapActor,
			},
			RefName: receipt.GitRef, ExpectedCommitSHA: receipt.ExpectedCommit, RetainReceipt: true,
		}
		if receipt.Commit == "" {
			params.ReceiptOnly = true
			commit, probeErr := git.CommitFileAtRevisionForRepo(ctx, receipt.RepositoryID, params)
			if probeErr != nil {
				if code := grpcstatus.Code(probeErr); code != codes.NotFound && code != codes.FailedPrecondition {
					return errors.Join(rbac.ErrUnavailable, probeErr)
				}
				admitted, err := validator.ValidateInitialAdministrator(ctx, store)
				if err != nil {
					return errors.Join(rbac.ErrUnavailable, err)
				}
				err = admitted.Validation.Check(ctx)
				if err == nil {
					params.ReceiptOnly = false
					commit, err = git.CommitFileAtRevisionForRepo(ctx, receipt.RepositoryID, params)
				}
				admitted.Close()
				if err != nil {
					return errors.Join(rbac.ErrUnavailable, err)
				}
			}
			receipt.Commit = commit
			if err := persistRBACBootstrap(ctx, store, namespace, *receipt); errors.Is(err, datastore.ErrConflict) {
				continue
			} else if err != nil {
				return errors.Join(rbac.ErrUnavailable, err)
			}
		}
		committed, err := git.ReadFile(ctx, receipt.RepositoryID, params.Path, receipt.Commit)
		if err != nil || !bytes.Equal(committed, content) {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		origin, err := source.ReadOrigin(ctx, rbac.ClusterTierID)
		if err != nil || origin.NamespaceUID != receipt.NamespaceUID || origin.RepositoryID != receipt.RepositoryID || origin.GitRef != receipt.GitRef {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		if err := repair(ctx, rbac.ClusterTierID); err != nil {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		// Resolve only the exact seed, using the normal UID/claim protocol. The
		// first administrator must be able to enroll the lifecycle controller.
		key := rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRoleBinding, Name: "initial-admin"}
		binding, err := validator.Capture.Reader.Store.GetRBAC(ctx, key)
		if errors.Is(err, rbac.ErrNotFound) {
			return nil
		}
		if err != nil {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		actual, err := rbac.CanonicalManifest(binding)
		if err != nil {
			return errors.Join(rbac.ErrUnavailable, err)
		}
		if binding.Metadata.DeletionTimestamp != nil || !bytes.Equal(actual, content) {
			return nil
		}
		_, err = (rbac.References{Store: validator.Capture.Reader.Store, Builtins: validator.Builtins}).Resolve(
			ctx, key, binding.Metadata.UID, binding.Metadata.ResourceVersion)
		if errors.Is(err, rbac.ErrConflict) {
			continue
		}
		return err
	}
	return rbac.ErrUnavailable
}
