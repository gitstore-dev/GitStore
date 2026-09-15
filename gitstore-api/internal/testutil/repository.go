// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package testutil

import (
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// RepositoryFixture returns a complete, admitted Repository suitable for API
// lifecycle, watch, and resolver tests. Callers may mutate the returned value
// for the condition under test without repeating contract defaults.
func RepositoryFixture(uid, namespace, name string) *datastore.Repository {
	return &datastore.Repository{
		UID: uid, ID: uid, RepositoryID: uid,
		APIVersion: "gitstore.dev/v1beta1", Kind: "Repository",
		Namespace: namespace, NamespaceID: namespace, Name: name,
		DefaultBranch: "main", StorageClass: "standard",
		Spec:              []byte(`{"defaultBranch":"main","visibility":"PRIVATE","storageClass":"standard"}`),
		Generation: 1, ResourceVersion: "1",
		Status:            []byte(`{"observedGeneration":0,"conditions":[]}`),
		CreationTimestamp: time.Now().UTC(),
	}
}

// SystemRepositoryFixture returns the sole datastore-only Repository fixture.
func SystemRepositoryFixture(uid, namespace string) *datastore.Repository {
	repository := RepositoryFixture(uid, namespace, "gitstore-system")
	repository.CreationActor = "system:namespace-controller"
	repository.UpdateActor = repository.CreationActor
	return repository
}
