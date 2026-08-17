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
	if repository.Generation < RepositoryInitialGeneration {
		repository.Generation = RepositoryInitialGeneration
	}
	if !validRepositoryResourceVersion(repository.ResourceVersion) {
		repository.ResourceVersion = RepositoryInitialResourceVersion
	}
	if len(repository.Status) == 0 {
		repository.Status = append(json.RawMessage(nil), repositoryInitialStatus...)
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
