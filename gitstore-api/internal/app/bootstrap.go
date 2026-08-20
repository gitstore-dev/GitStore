// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"go.uber.org/zap"
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
	{name: "default", title: "Default", tier: datastore.NamespaceTierUser},
}

func ensureBootstrapResources(
	ctx context.Context,
	store datastore.Datastore,
	git bootstrapRepositoryProvisioner,
	clock apiruntime.Clock,
	ids apiruntime.IDGenerator,
	log *zap.Logger,
) error {
	for _, bootstrap := range bootstrapNamespaces {
		namespace, err := store.GetNamespaceByName(ctx, bootstrap.name)
		if errors.Is(err, datastore.ErrNotFound) {
			id, idErr := ids.NewV7ID()
			if idErr != nil {
				return fmt.Errorf("bootstrap namespace %q: generate id: %w", bootstrap.name, idErr)
			}
			now := clock.Now().UTC()
			namespace = &datastore.Namespace{
				ID:                id,
				Name:              bootstrap.name,
				Title:             bootstrap.title,
				Tier:              bootstrap.tier,
				CreationTimestamp: now,
				CreationActor:     bootstrapActor,
				UpdateTimestamp:   now,
				UpdateActor:       bootstrapActor,
			}
			datastore.NormalizeNamespaceContract(namespace)
			if err := store.CreateNamespace(ctx, namespace); err != nil {
				return fmt.Errorf("bootstrap namespace %q: create: %w", bootstrap.name, err)
			}
		} else if err != nil {
			return fmt.Errorf("bootstrap namespace %q: lookup: %w", bootstrap.name, err)
		}
		if err := ensureBootstrapRepository(ctx, store, git, clock, ids, namespace); err != nil {
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
	ids apiruntime.IDGenerator,
	namespace *datastore.Namespace,
) error {
	if _, err := store.LookupRepository(ctx, namespace.ID, bootstrapRepositoryName); err == nil {
		return nil
	} else if !errors.Is(err, datastore.ErrNotFound) {
		return fmt.Errorf("lookup system repository: %w", err)
	}

	id, err := ids.NewV7ID()
	if err != nil {
		return fmt.Errorf("generate system repository id: %w", err)
	}
	now := clock.Now().UTC()
	repository := &datastore.Repository{
		ID:                id,
		NamespaceID:       namespace.ID,
		Name:              bootstrapRepositoryName,
		DefaultBranch:     "main",
		StorageClass:      "default",
		CreationTimestamp: now,
		CreationActor:     bootstrapActor,
		UpdateTimestamp:   now,
		UpdateActor:       bootstrapActor,
	}
	datastore.NormalizeRepositoryContract(repository)
	if err := store.CreateRepository(ctx, repository); err != nil {
		return fmt.Errorf("create system repository row: %w", err)
	}
	if err := store.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		NamespaceID: namespace.ID,
		Name:        bootstrapRepositoryName,
		RepoID:      repository.ID,
	}); err != nil {
		_ = store.DeleteRepository(ctx, repository.ID)
		return fmt.Errorf("create system repository mapping: %w", err)
	}
	if _, err := git.CreateRepository(ctx, repository.ID, repository.StorageClass); err != nil {
		_ = store.DeleteNamespaceMapping(ctx, namespace.ID, bootstrapRepositoryName)
		_ = store.DeleteRepository(ctx, repository.ID)
		return fmt.Errorf("provision system repository storage: %w", err)
	}
	return nil
}
