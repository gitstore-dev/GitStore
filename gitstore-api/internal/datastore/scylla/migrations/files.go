// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package migrations

import "embed"

// Files contains *.cql schema migration files
//
//go:embed *.cql
var Files embed.FS
