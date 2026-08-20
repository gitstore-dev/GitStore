// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"encoding/json"
	"math/big"
)

const (
	RepositoryInitialGeneration      int64  = 1
	RepositoryInitialResourceVersion string = "1"
)

var repositoryInitialStatus = json.RawMessage(`{"observedGeneration":0,"conditions":[]}`)

// NormalizeRepositoryContract supplies canonical values for rows created before
// the declarative Repository contract existed.
func NormalizeRepositoryContract(repository *Repository) {
	if repository == nil {
		return
	}
	if repository.UID == "" {
		repository.UID = repository.ID
	}
	if repository.ID == "" {
		repository.ID = repository.UID
	}
	if repository.Namespace == "" {
		repository.Namespace = repository.NamespaceID
	}
	if repository.NamespaceID == "" {
		repository.NamespaceID = repository.Namespace
	}
	if repository.Generation < RepositoryInitialGeneration {
		repository.Generation = RepositoryInitialGeneration
	}
	if !validRepositoryResourceVersion(repository.ResourceVersion) {
		repository.ResourceVersion = RepositoryInitialResourceVersion
	}
	if len(repository.Status) == 0 {
		repository.Status = append(json.RawMessage(nil), repositoryInitialStatus...)
	}
	if repository.Finalizers == nil {
		repository.Finalizers = []string{}
	}
}

// NormalizeNamespaceMappingContract keeps canonical mapping names populated
// while legacy callers are migrated.
func NormalizeNamespaceMappingContract(mapping *NamespaceMapping) {
	if mapping == nil {
		return
	}
	if mapping.Namespace == "" {
		mapping.Namespace = mapping.NamespaceID
	}
	if mapping.NamespaceID == "" {
		mapping.NamespaceID = mapping.Namespace
	}
	if mapping.RepositoryID == "" {
		mapping.RepositoryID = mapping.RepoID
	}
	if mapping.RepoID == "" {
		mapping.RepoID = mapping.RepositoryID
	}
}

// AdvanceRepositorySpecVersion advances both counters for author-controlled
// metadata or spec changes.
func AdvanceRepositorySpecVersion(repository *Repository) {
	NormalizeRepositoryContract(repository)
	repository.Generation++
	advanceRepositoryResourceVersion(repository)
}

// AdvanceRepositorySystemVersion advances resourceVersion while preserving
// generation for transfer, status, and other system-owned changes.
func AdvanceRepositorySystemVersion(repository *Repository) {
	NormalizeRepositoryContract(repository)
	advanceRepositoryResourceVersion(repository)
}

func validRepositoryResourceVersion(value string) bool {
	version, ok := new(big.Int).SetString(value, 10)
	return ok && version.Sign() > 0
}

func advanceRepositoryResourceVersion(repository *Repository) {
	version, _ := new(big.Int).SetString(repository.ResourceVersion, 10)
	version.Add(version, big.NewInt(1))
	repository.ResourceVersion = version.String()
}
