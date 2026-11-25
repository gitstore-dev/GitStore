// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/validate"
)

// Keep repository scope on a request-local server copy. Sharing a mutable
// parser between callbacks would leak one repository's namespace into another.
func (s *Server) withRepositoryNamespace(ctx context.Context, repositoryID string) *Server {
	parser, ok := s.parser.(*validate.Parser)
	if !ok {
		return s
	}
	var once sync.Once
	var namespace string
	var resolveErr error
	scoped := *s
	scoped.parser = parser.WithRepositoryNamespace(func() (string, error) {
		once.Do(func() {
			namespace, resolveErr = s.resolveNamespaceIdentifier(ctx, repositoryID)
		})
		return namespace, resolveErr
	})
	return &scoped
}
