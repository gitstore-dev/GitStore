// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package bridge

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/hydraclient"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/kratosclient"
)

// ConsentHandler resolves Hydra consent challenges with no user-facing consent
// screen (the registered client is first-party by construction), granting only
// the intersection of requested and permitted scopes and populating ID token
// claims from the Kratos identity's traits.
type ConsentHandler struct {
	hydra           hydraclient.Client
	kratos          kratosclient.Client
	permittedScope  []string
	defaultAudience string
	log             *zap.Logger
}

func NewConsentHandler(hydra hydraclient.Client, kratos kratosclient.Client, permittedScope []string, defaultAudience string, log *zap.Logger) *ConsentHandler {
	return &ConsentHandler{hydra: hydra, kratos: kratos, permittedScope: permittedScope, defaultAudience: defaultAudience, log: log}
}

func (h *ConsentHandler) Handle(c *gin.Context) {
	challenge := c.Query("consent_challenge")
	if challenge == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing consent_challenge"})
		return
	}
	log := h.log.With(zap.String("consent_challenge", challenge))

	req, err := h.hydra.GetConsentRequest(c.Request.Context(), challenge)
	if err != nil {
		log.Error("hydra consent request lookup failed", zap.Error(err))
		h.reject(c, challenge, "server_error", "consent request lookup failed")
		return
	}

	identity, err := h.kratos.GetIdentity(c.Request.Context(), req.Subject)
	if err != nil {
		log.Error("kratos identity lookup failed", zap.String("subject", req.Subject), zap.Error(err))
		h.reject(c, challenge, "server_error", "identity lookup failed")
		return
	}

	grantScope := intersectScope(req.RequestedScope, h.permittedScope)
	grantAudience := req.RequestedAudience
	if len(grantAudience) == 0 && h.defaultAudience != "" {
		// Generic OIDC clients rarely send an audience/resource parameter;
		// without one the access token's aud would be empty and rejected by
		// gitstore-api's aud-enforcing oidc-jwt provider.
		grantAudience = []string{h.defaultAudience}
	}
	claims := map[string]interface{}{
		"email":              identity.Email,
		"preferred_username": identity.Username,
	}
	redirectTo, err := h.hydra.AcceptConsentRequest(c.Request.Context(), challenge, grantScope, grantAudience, claims)
	if err != nil {
		log.Error("hydra consent accept failed", zap.Error(err))
		h.reject(c, challenge, "server_error", "consent accept failed")
		return
	}
	log.Info("consent challenge accepted",
		zap.String("subject", req.Subject),
		zap.Strings("grant_scope", grantScope))
	c.Redirect(http.StatusFound, redirectTo)
}

func (h *ConsentHandler) reject(c *gin.Context, challenge, code, description string) {
	redirectTo, err := h.hydra.RejectConsentRequest(context.Background(), challenge, code, description)
	if err != nil {
		h.log.Error("hydra consent reject failed", zap.String("consent_challenge", challenge), zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": code, "error_description": description})
		return
	}
	h.log.Info("consent challenge rejected", zap.String("consent_challenge", challenge), zap.String("reason", description))
	c.Redirect(http.StatusFound, redirectTo)
}

// intersectScope returns the subset of requested scopes that appear in the
// permitted set, preserving requested order. Anything outside the permitted
// set is never silently granted (spec.md FR-006).
func intersectScope(requested, permitted []string) []string {
	allowed := make(map[string]struct{}, len(permitted))
	for _, s := range permitted {
		allowed[s] = struct{}{}
	}
	var granted []string
	for _, s := range requested {
		if _, ok := allowed[s]; ok {
			granted = append(granted, s)
		}
	}
	return granted
}
