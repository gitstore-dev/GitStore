// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
package staticusers

import (
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

var (
	ErrUserNotFound = fmt.Errorf("staticusers: user not found: %w", auth.ErrProfileNotFound)
	ErrUserExists   = errors.New("staticusers: user already exists")
)
