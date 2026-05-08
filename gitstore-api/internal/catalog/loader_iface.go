// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Loader interface shared by the go-git Loader and the gRPC GRPCLoader.

package catalog

import "context"

// Loader abstracts catalog loading so callers are not coupled to a
// specific transport (local git vs gRPC).
type Loader interface {
	LoadFromTag(ctx context.Context, tag string) (*Catalog, error)
	LoadFromLatestTag(ctx context.Context) (*Catalog, error)
}
