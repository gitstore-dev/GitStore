// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package cataloggrpc implements the CatalogService gRPC server for the
// gitstore-api. It handles ValidateResources (blocking pre-receive validation)
// and AdmitResources (fire-and-forget post-receive catalog storage).
package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// GitReader is the read subset of gitclient.Client used by AdmitResources.
// Abstracted here so it can be mocked in tests.
type GitReader interface {
	ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error)
	ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error)
}

// gitClientReader wraps *gitclient.Client to satisfy GitReader.
// Each method passes repositoryID directly to the gRPC request instead of
// mutating the shared Client.RepositoryID field, making it safe for concurrent
// AdmitResources calls targeting different repositories.
type gitClientReader struct{ c *gitclient.Client }

func (r *gitClientReader) ListFiles(ctx context.Context, repositoryID, prefix, ref string) ([]string, error) {
	entries, err := r.c.ListFilesForRepo(ctx, repositoryID, prefix, ref)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return paths, nil
}

func (r *gitClientReader) ReadFile(ctx context.Context, repositoryID, path, ref string) ([]byte, error) {
	return r.c.ReadFileForRepo(ctx, repositoryID, path, ref)
}

// Server implements catalogv1.CatalogServiceServer.
type Server struct {
	catalogv1.UnimplementedCatalogServiceServer
	store datastore.Datastore
	git   GitReader
	log   *zap.Logger
}

// NewServer creates a new CatalogService gRPC server.
// store and gitClient may be nil for unit tests that only call ValidateResources.
func NewServer(store datastore.Datastore, gitClient *gitclient.Client) *Server {
	var git GitReader
	if gitClient != nil {
		git = &gitClientReader{c: gitClient}
	}
	return &Server{store: store, git: git, log: zap.NewNop()}
}

// NewServerWithLogger creates a new server with a custom logger.
func NewServerWithLogger(store datastore.Datastore, gitClient *gitclient.Client, log *zap.Logger) *Server {
	var git GitReader
	if gitClient != nil {
		git = &gitClientReader{c: gitClient}
	}
	return &Server{store: store, git: git, log: log}
}

// NewServerForTest creates a server with a mock GitReader for unit tests.
func NewServerForTest(store datastore.Datastore, git GitReader) *Server {
	return &Server{store: store, git: git, log: zap.NewNop()}
}

// ValidateResources validates resource blobs extracted from an incoming push commit.
// Called blocking in the pre-receive phase. Returns all violations across all blobs.
func (s *Server) ValidateResources(
	_ context.Context,
	req *catalogv1.ValidateResourcesRequest,
) (*catalogv1.ValidateResourcesResponse, error) {
	var allErrors []*catalogv1.ValidationError

	for _, blob := range req.Blobs {
		// Opt-in: blobs not starting with `---` are not product resources.
		trimmed := bytes.TrimLeft(blob.Content, " \t\r\n")
		if !bytes.HasPrefix(trimmed, []byte("---")) {
			continue
		}

		_, _, err := validate.Parse(bytes.NewReader(blob.Content))
		if err == nil {
			continue
		}

		// Convert the error string into ValidationError messages.
		// validate.Parse returns a joined error string; split on "; " and "\n".
		msgs := splitValidationErrors(err.Error())
		for _, msg := range msgs {
			ve := errorToValidationError(blob.Path, msg)
			allErrors = append(allErrors, ve)
		}
	}

	if len(allErrors) == 0 {
		return &catalogv1.ValidateResourcesResponse{Accepted: true}, nil
	}
	return &catalogv1.ValidateResourcesResponse{
		Accepted: false,
		Errors:   allErrors,
	}, nil
}

// splitValidationErrors splits the joined error string from validate.Parse into
// individual messages.
func splitValidationErrors(errStr string) []string {
	// validate.Parse joins errors with "; " or "\n"
	var parts []string
	for _, part := range strings.Split(errStr, "\n") {
		for _, sub := range strings.Split(part, "; ") {
			if s := strings.TrimSpace(sub); s != "" {
				parts = append(parts, s)
			}
		}
	}
	return parts
}

// errorToValidationError converts a single validate.Parse error message into a
// ValidationError proto, extracting field path and constraint where possible.
func errorToValidationError(filePath, msg string) *catalogv1.ValidationError {
	// Messages from toFriendlyError have the form:
	//   "validate: <field> <description>"
	// We extract the field name from the message.
	trimmed := strings.TrimPrefix(msg, "validate: ")

	field := ""
	constraint := ""

	// Try to extract a field path from known message patterns.
	if strings.HasPrefix(trimmed, "status") {
		field = "status"
		constraint = "system-managed"
	} else if strings.HasPrefix(trimmed, "spec.title exceeds") {
		field = "spec.title"
		constraint = "max=200"
	} else if strings.HasPrefix(trimmed, "metadata.") {
		// "metadata.uid is read-only..."
		parts := strings.SplitN(trimmed, " ", 2)
		if len(parts) > 0 {
			field = parts[0]
			constraint = "read-only"
		}
	} else {
		// Generic: try splitting on space to get field
		parts := strings.Fields(trimmed)
		if len(parts) > 0 {
			field = parts[0]
		}
	}

	return &catalogv1.ValidationError{
		FilePath:   filePath,
		Field:      field,
		Constraint: constraint,
		Message:    msg,
	}
}

// AdmitResources fetches, parses, and stores catalog resources from an accepted push commit.
// Called fire-and-forget from the post-receive hook. Each product is processed independently;
// failures are logged and do not block remaining products (FR-011).
func (s *Server) AdmitResources(
	ctx context.Context,
	req *catalogv1.AdmitResourcesRequest,
) (*catalogv1.AdmitResourcesResponse, error) {
	if s.git == nil || s.store == nil {
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	paths, err := s.git.ListFiles(ctx, req.RepositoryId, "", req.CommitSha)
	if err != nil {
		s.log.Error("admit_resources: list files failed",
			zap.String("repository_id", req.RepositoryId),
			zap.String("commit_sha", req.CommitSha),
			zap.Error(err))
		return &catalogv1.AdmitResourcesResponse{}, nil
	}

	// Extract branch name from ref: "refs/heads/main" → "main"
	branch := strings.TrimPrefix(req.RefName, "refs/heads/")
	revision := branch + "@sha1:" + req.CommitSha

	for _, path := range paths {
		content, err := s.git.ReadFile(ctx, req.RepositoryId, path, req.CommitSha)
		if err != nil {
			s.log.Error("admit_resources: read file failed",
				zap.String("path", path), zap.Error(err))
			continue
		}

		resource, body, err := validate.Parse(bytes.NewReader(content))
		if err != nil || resource == nil {
			if err != nil {
				s.log.Error("admit_resources: parse failed",
					zap.String("path", path), zap.Error(err))
			}
			continue
		}

		specJSON, err := json.Marshal(resource.Spec)
		if err != nil {
			s.log.Error("admit_resources: marshal spec failed",
				zap.String("path", path), zap.Error(err))
			continue
		}

		namespace := resource.Metadata.Namespace
		if namespace == "" {
			namespace = req.RepositoryId
		}

		existing, getErr := s.store.GetProductByName(ctx, namespace, resource.Metadata.Name)
		now := time.Now().UTC()

		if getErr != nil || existing == nil {
			p := &datastore.Product{
				UID:               uuid.Must(uuid.NewV7()).String(),
				Namespace:         namespace,
				Name:              resource.Metadata.Name,
				APIVersion:        resource.APIVersion,
				Kind:              resource.Kind,
				Labels:            resource.Metadata.Labels,
				Annotations:       resource.Metadata.Annotations,
				Generation:        1,
				ResourceVersion:   "1",
				CreationTimestamp: now,
				Revision:          revision,
				GitCommitSHA:      req.CommitSha,
				GitRef:            req.RefName,
				Spec:              specJSON,
				Body:              string(body),
			}
			p.Status = admissionAcceptedStatus(1, revision, now)
			if cerr := s.store.CreateProduct(ctx, p); cerr != nil {
				s.log.Error("admit_resources: create product failed",
					zap.String("name", resource.Metadata.Name), zap.Error(cerr))
			}
		} else {
			gen := existing.Generation + 1
			existing.APIVersion = resource.APIVersion
			existing.Kind = resource.Kind
			existing.Labels = resource.Metadata.Labels
			existing.Annotations = resource.Metadata.Annotations
			existing.Generation = gen
			existing.ResourceVersion = fmt.Sprintf("%d", gen)
			existing.Revision = revision
			existing.GitCommitSHA = req.CommitSha
			existing.GitRef = req.RefName
			existing.Spec = specJSON
			existing.Body = string(body)
			existing.Status = admissionAcceptedStatus(gen, revision, now)
			if uerr := s.store.UpdateProduct(ctx, existing); uerr != nil {
				s.log.Error("admit_resources: update product failed",
					zap.String("name", resource.Metadata.Name), zap.Error(uerr))
			}
		}
	}

	return &catalogv1.AdmitResourcesResponse{}, nil
}

// admissionAcceptedStatus builds the initial status JSON with AdmissionAccepted: True (FR-009).
func admissionAcceptedStatus(generation int64, revision string, now time.Time) []byte {
	status := catalog.ProductStatus{
		ObservedGeneration:  generation,
		LastAppliedRevision: revision,
		Conditions: []catalog.Condition{
			{
				Type:               catalog.ConditionAdmissionAccepted,
				Status:             catalog.ConditionTrue,
				ObservedGeneration: generation,
				LastTransitionTime: now,
				Reason:             "AdmittedByHookPipeline",
				Message:            "Resource admitted via the post-receive hook pipeline.",
			},
		},
	}
	b, _ := json.Marshal(status)
	return b
}
