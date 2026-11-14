// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	bootstrapActor          = "system:bootstrap"
	bootstrapRepositoryName = "gitstore-system"
)

type bootstrapRepositoryProvisioner interface {
	CreateRepository(ctx context.Context, repositoryID, storageClass string) (string, error)
}

type bootstrapNamespace struct {
	name  string
	title string
	tier  datastore.NamespaceTier
}

var bootstrapNamespaces = []bootstrapNamespace{
	{name: "gitstore-system", title: "GitStore System", tier: datastore.NamespaceTierOrganization},
	{name: "default", title: "Default", tier: datastore.NamespaceTierOrganization},
}

func ensureBootstrapResources(
	ctx context.Context,
	store datastore.Datastore,
	git bootstrapRepositoryProvisioner,
	clock apiruntime.Clock,
	ids apiruntime.IDGenerator,
	log *zap.Logger,
) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, bootstrap := range bootstrapNamespaces {
		namespace, err := store.GetNamespaceByName(ctx, bootstrap.name)
		if errors.Is(err, datastore.ErrNotFound) {
			id, idErr := ids.NewV7ID()
			if idErr != nil {
				return fmt.Errorf("bootstrap namespace %q: generate id: %w", bootstrap.name, idErr)
			}
			now := clock.Now().UTC()
			namespace = &datastore.Namespace{
				UID:               id,
				Name:              bootstrap.name,
				Title:             bootstrap.title,
				Tier:              bootstrap.tier,
				CreationTimestamp: now,
				CreationActor:     bootstrapActor,
				UpdateTimestamp:   now,
				UpdateActor:       bootstrapActor,
			}
			datastore.NormalizeNamespaceContract(namespace)
			if err := store.CreateNamespace(ctx, namespace); errors.Is(err, datastore.ErrAlreadyExists) || errors.Is(err, datastore.ErrConflict) {
				namespace, err = store.GetNamespaceByName(ctx, bootstrap.name)
				if err != nil {
					return fmt.Errorf("bootstrap namespace %q: concurrent create: %w", bootstrap.name, err)
				}
			} else if err != nil {
				return fmt.Errorf("bootstrap namespace %q: create: %w", bootstrap.name, err)
			}
		} else if err != nil {
			return fmt.Errorf("bootstrap namespace %q: lookup: %w", bootstrap.name, err)
		}
		if namespace == nil || namespace.DeletionTimestamp != nil || datastore.HasDeletionIntent(namespace.Status) {
			return fmt.Errorf("bootstrap namespace %q: %w", bootstrap.name, datastore.ErrNamespaceNotActive)
		}
		if err := ensureBootstrapRepository(ctx, store, git, clock, namespace); err != nil {
			return fmt.Errorf("bootstrap namespace %q: %w", bootstrap.name, err)
		}
		log.Info("bootstrap namespace ready", zap.String("namespace", bootstrap.name))
	}
	return nil
}

func ensureBootstrapRepository(
	ctx context.Context,
	store datastore.Datastore,
	git bootstrapRepositoryProvisioner,
	clock apiruntime.Clock,
	namespace *datastore.Namespace,
) error {
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("gitstore/bootstrap/"+namespace.UID+"/"+namespace.Name)).String()
	if mapping, err := store.LookupRepository(ctx, namespace.Name, bootstrapRepositoryName); err == nil {
		var resolve func(context.Context, string, string) (string, error)
		if reader, ok := git.(interface {
			ResolveRef(context.Context, string, string) (string, error)
		}); ok {
			resolve = reader.ResolveRef
		} else if reader, ok := git.(interface {
			ResolveRefForRepo(context.Context, string, string) (string, error)
		}); ok {
			resolve = reader.ResolveRefForRepo
		}
		if resolve != nil {
			if mapping == nil {
				return fmt.Errorf("bootstrap repository mapping is unavailable: %w", datastore.ErrConflict)
			}
			repository, err := store.GetRepository(ctx, mapping.RepositoryID)
			if err != nil || repository == nil || repository.Namespace != namespace.Name ||
				repository.Name != bootstrapRepositoryName || repository.DeletionTimestamp != nil {
				return fmt.Errorf("bootstrap repository is unavailable: %w", datastore.ErrConflict)
			}
			if repository.UID == id && repository.RepositoryID == "" && repository.SourcePath == "" {
				expectedVersion := repository.ResourceVersion
				repository.RepositoryID = id
				if err := store.UpdateRepository(ctx, repository, expectedVersion); errors.Is(err, datastore.ErrConflict) {
					current, lookupErr := store.GetRepository(ctx, id)
					if lookupErr != nil || current == nil || current.RepositoryID != id ||
						current.Namespace != namespace.Name || current.Name != bootstrapRepositoryName ||
						current.SourcePath != "" || current.DeletionTimestamp != nil {
						return fmt.Errorf("concurrent system repository identity is unavailable: %w", datastore.ErrConflict)
					}
				} else if err != nil {
					return fmt.Errorf("restore system repository identity: %w", err)
				}
			}
			if _, err := resolve(ctx, repository.UID, "refs/heads/"+repository.DefaultBranch); err == nil {
				return nil
			}
			_, err = git.CreateRepository(gitclient.ContextForBackingWork(ctx), repository.UID, repository.StorageClass)
			if grpcstatus.Code(err) == codes.AlreadyExists {
				return nil
			}
			return err
		}
		return nil
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return fmt.Errorf("lookup system repository: %w", err)
	}

	// A Namespace incarnation has one bootstrap repository identity, including
	// after a crash between row creation, mapping publication and Git storage.
	now := clock.Now().UTC()
	repository := &datastore.Repository{
		UID:               id,
		RepositoryID:      id,
		Namespace:         namespace.Name,
		Name:              bootstrapRepositoryName,
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     bootstrapActor,
		UpdateTimestamp:   now,
		UpdateActor:       bootstrapActor,
	}
	datastore.NormalizeRepositoryContract(repository)
	if err := store.CreateRepositoryInActiveNamespace(ctx, repository); errors.Is(err, datastore.ErrAlreadyExists) || errors.Is(err, datastore.ErrConflict) {
		repository, err = store.GetRepository(ctx, id)
		if err != nil || repository == nil || repository.Namespace != namespace.Name ||
			repository.Name != bootstrapRepositoryName || repository.DeletionTimestamp != nil {
			return fmt.Errorf("concurrent system repository is unavailable: %w", datastore.ErrConflict)
		}
	} else if err != nil {
		return fmt.Errorf("create system repository row: %w", err)
	}
	if err := store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		Namespace:    namespace.Name,
		Name:         bootstrapRepositoryName,
		RepositoryID: repository.UID,
	}); err != nil {
		mapping, lookupErr := store.LookupRepository(ctx, namespace.Name, bootstrapRepositoryName)
		if lookupErr != nil || mapping == nil || mapping.RepositoryID != repository.UID {
			return fmt.Errorf("create system repository mapping: %w", err)
		}
	}
	if _, err := git.CreateRepository(gitclient.ContextForBackingWork(ctx), repository.UID, repository.StorageClass); err != nil && grpcstatus.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("provision system repository storage: %w", err)
	}
	return nil
}
