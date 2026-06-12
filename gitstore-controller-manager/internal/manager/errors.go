// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import "github.com/gitstore-dev/gitstore/controller-manager/internal/types"

var (
	ErrNotFound          = types.ErrNotFound
	ErrQueueShutdown     = types.ErrQueueShutdown
	ErrKindNotRegistered = types.ErrKindNotRegistered
)
