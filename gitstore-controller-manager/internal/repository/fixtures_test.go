// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package repository

import "github.com/gitstore-dev/gitstore/controller-manager/internal/status"

func repositoryFixture(mutate ...func(*Repository)) Repository {
	repository := Repository{
		UID: "repo-1", Namespace: "acme", Name: "catalog",
		StorageClass: "standard", Generation: 1, ResourceVersion: "2",
		Status: status.ResourceStatus{ResourceVersion: "2", Conditions: []*status.Condition{admissionAccepted(1)}},
	}
	for _, apply := range mutate {
		apply(&repository)
	}
	return repository
}
