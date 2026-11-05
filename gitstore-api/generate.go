// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Code generation directives

package api

//go:generate go tool gqlgen generate --config gqlgen.admin.yml
//go:generate go tool gqlgen generate --config gqlgen.storefront.yml
//go:generate go run ./cmd/gitctl generate authz --root ..
