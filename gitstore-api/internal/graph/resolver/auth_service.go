package resolver

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"go.uber.org/zap"
)

func tokenResponse(accessToken, refreshToken string, exp time.Time) *model.TokenResponse {
	ttl := int32(time.Until(exp).Seconds())
	if ttl < 0 {
		ttl = 0
	}
	return &model.TokenResponse{
		AccessToken:  accessToken,
		TokenType:    "Bearer",
		ExpiresIn:    ttl,
		RefreshToken: &refreshToken,
	}
}

// Login implements the login mutation on the base Resolver so it can be unit-tested directly.
func (r *Resolver) Login(ctx context.Context, input model.LoginInput) (*model.LoginPayload, error) {
	if r.registry == nil || r.registry.AuthN() == nil {
		r.logger.Error("auth provider registry not configured")
		return nil, gqlerror.Errorf("authentication service unavailable")
	}
	if input.Scope != nil && *input.Scope != "" {
		return nil, gqlerror.Errorf("scope requests are not supported by active auth configuration")
	}

	creds := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", input.Username, input.Password)))
	req := auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Basic " + creds}},
	}

	principal, decision, err := r.registry.AuthN().Authenticate(ctx, req)
	if err != nil {
		r.logger.Debug("login auth error", zap.Error(err))
		return nil, gqlerror.Errorf("invalid username or password")
	}
	if decision.Outcome != auth.OutcomeAllow {
		r.logger.Debug("login denied", zap.String("reason", decision.Reason))
		return nil, gqlerror.Errorf("invalid username or password")
	}

	token, exp, err := r.registry.AuthN().IssueSession(ctx, principal.Subject)
	if err != nil {
		if errors.Is(err, auth.ErrNotSupported) {
			r.logger.Error("no auth provider supports IssueSession")
			return nil, gqlerror.Errorf("authentication service unavailable")
		}
		r.logger.Error("failed to issue session token", zap.String("subject", principal.Subject), zap.Error(err))
		return nil, gqlerror.Errorf("internal server error")
	}

	r.logger.Debug("login succeeded", zap.String("subject", principal.Subject), zap.String("provider", decision.Provider))
	return &model.LoginPayload{
		Token: tokenResponse(token, token, exp),
	}, nil
}

// Logout implements the logout mutation on the base Resolver so it can be unit-tested directly.
func (r *Resolver) Logout(ctx context.Context, input model.LogoutInput) (*model.LogoutPayload, error) {
	if r.registry == nil {
		return nil, gqlerror.Errorf("authentication service unavailable")
	}

	// GraphQLAuthorizer already denies anonymous principals for non-login
	// mutations; this check is defense-in-depth so Logout fails closed even
	// if invoked without the operation middleware (e.g. direct resolver call).
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.AuthMethod == "none" {
		return nil, gqlerror.Errorf("authentication required")
	}

	// Empty TokenID means a Basic Auth session with no jti — nothing to revoke.
	if principal.TokenID != "" {
		err := r.registry.AuthN().RevokeSession(ctx, principal.TokenID, principal.ExpiresAt)
		if err != nil {
			if errors.Is(err, auth.ErrNotSupported) {
				r.logger.Warn("logout not supported by active auth configuration")
				return nil, gqlerror.Errorf("logout not supported by active auth configuration")
			}
			r.logger.Error("failed to revoke session", zap.String("subject", principal.Subject), zap.Error(err))
			return nil, gqlerror.Errorf("internal server error")
		}
	}

	r.logger.Debug("logout succeeded", zap.String("subject", principal.Subject))
	return &model.LogoutPayload{Success: true}, nil
}

// RefreshToken implements the refreshToken mutation on the base Resolver so it can be unit-tested directly.
func (r *Resolver) RefreshToken(ctx context.Context, input model.RefreshTokenInput) (*model.RefreshTokenPayload, error) {
	if r.registry == nil {
		return nil, gqlerror.Errorf("authentication service unavailable")
	}
	if input.Scope != nil && *input.Scope != "" {
		return nil, gqlerror.Errorf("scope requests are not supported by active auth configuration")
	}
	if input.RefreshToken == "" {
		return nil, gqlerror.Errorf("refresh token is required")
	}

	newToken, exp, err := r.registry.AuthN().RefreshSession(ctx, input.RefreshToken)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrNotSupported):
			return nil, gqlerror.Errorf("token refresh not supported by active auth configuration")
		case errors.Is(err, auth.ErrTokenTooOld):
			return nil, gqlerror.Errorf("token too old to refresh, please log in again")
		case errors.Is(err, auth.ErrTokenRevoked):
			return nil, gqlerror.Errorf("token has been revoked")
		default:
			r.logger.Error("token refresh failed", zap.Error(err))
			return nil, gqlerror.Errorf("internal server error")
		}
	}

	r.logger.Debug("token refreshed")
	return &model.RefreshTokenPayload{
		Token: tokenResponse(newToken, newToken, exp),
	}, nil
}
