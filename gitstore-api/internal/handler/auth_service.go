// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package handler

import "time"

// AuthService is the authentication behavior required by HTTP auth handlers.
type AuthService interface {
	ValidateCredentials(username, password string) bool
	GenerateSessionToken(username string, isAdmin bool) (string, error)
	RefreshSessionToken(token string) (string, error)
	GetTokenDuration() time.Duration
}
