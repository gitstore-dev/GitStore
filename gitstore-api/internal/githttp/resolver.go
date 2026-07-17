// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package githttp

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"go.uber.org/zap"
)

// repoIDKey is the gin context key used by RepoResolver to store the resolved repository ID.
// Use c.Set(repoIDKey, repoID) to write and c.MustGet(repoIDKey).(string) to read.
const repoIDKey = "repoID"

// RepoResolver is a gin middleware that resolves the (namespace, repo) URL parameters
// to a stable repository ID and stores it under repoIDKey in the gin context.
// Returns 404 pkt-line if the namespace or repo is not found.
func RepoResolver(store datastore.Datastore, log *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		namespace := c.Param("namespace")
		repo := strings.TrimSuffix(c.Param("repo"), ".git")

		ns, err := store.GetNamespaceByIdentifier(c.Request.Context(), namespace)
		if err != nil || ns == nil {
			gitPktLineErrorRaw(c.Writer, http.StatusNotFound, "repository not found")
			c.Abort()
			return
		}

		mapping, err := store.LookupRepository(c.Request.Context(), ns.ID, repo)
		if err != nil || mapping == nil {
			gitPktLineErrorRaw(c.Writer, http.StatusNotFound, "repository not found")
			c.Abort()
			return
		}

		log.Debug("repo resolved", zap.String("namespace", namespace), zap.String("repo", repo), zap.String("repo_id", mapping.RepoID))
		c.Set(repoIDKey, mapping.RepoID)
		c.Next()
	}
}

// gitPktLineErrorRaw writes a Git pkt-line ERR response directly to a http.ResponseWriter.
func gitPktLineErrorRaw(w http.ResponseWriter, status int, msg string) {
	body := fmt.Sprintf("ERR %s", msg)
	pktLen := len(body) + 4
	line := fmt.Sprintf("%04x%s", pktLen, body)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(line))
}
