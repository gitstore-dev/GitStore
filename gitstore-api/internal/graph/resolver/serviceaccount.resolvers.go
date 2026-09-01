package resolver

import (
	"context"

	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
)

// IssueServiceAccountToken is the resolver for the issueServiceAccountToken field.
func (r *mutationResolver) IssueServiceAccountToken(ctx context.Context, input model.IssueServiceAccountTokenInput) (*model.IssueServiceAccountTokenPayload, error) {
	return r.Resolver.IssueServiceAccountToken(ctx, &input)
}

// CreateServiceAccount is the resolver for the createServiceAccount field.
func (r *mutationResolver) CreateServiceAccount(ctx context.Context, input model.CreateServiceAccountInput) (*model.CreateServiceAccountPayload, error) {
	return r.Resolver.CreateServiceAccount(ctx, &input)
}

// RotateServiceAccountKey is the resolver for the rotateServiceAccountKey field.
func (r *mutationResolver) RotateServiceAccountKey(ctx context.Context, input model.RotateServiceAccountKeyInput) (*model.CreateServiceAccountPayload, error) {
	return r.Resolver.RotateServiceAccountKey(ctx, &input)
}

// DeleteServiceAccount is the resolver for the deleteServiceAccount field.
func (r *mutationResolver) DeleteServiceAccount(ctx context.Context, input model.DeleteServiceAccountInput) (*model.DeleteServiceAccountPayload, error) {
	return r.Resolver.DeleteServiceAccount(ctx, &input)
}
