// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

//go:build !linux && !darwin

package integration

import (
	"context"
	"errors"
	"os"
)

func verifyRBACCapacityRunningExecutable(context.Context, string, *os.File) error {
	return errors.New("RBAC native executable identity requires Linux or macOS")
}
