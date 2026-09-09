package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/hydraclient"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/kratosclient"
)

// LoginHandler resolves Hydra login challenges against the browser's current
// Kratos session per contracts/oidc-bridge-routes.md.
type LoginHandler struct {
	hydra               hydraclient.Client
	kratos              kratosclient.Client
	kratosPublicBrowser string
	log                 *zap.Logger
}

func NewLoginHandler(hydra hydraclient.Client, kratos kratosclient.Client, kratosPublicBrowserURI string, log *zap.Logger) *LoginHandler {
	return &LoginHandler{
		hydra:               hydra,
		kratos:              kratos,
		kratosPublicBrowser: kratosPublicBrowserURI,
		log:                 log,
	}
}

func (h *LoginHandler) Handle(c *gin.Context) {
	challenge := c.Query("login_challenge")
	if challenge == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing login_challenge"})
		return
	}
	log := h.log.With(zap.String("login_challenge", challenge))

	if _, err := h.hydra.GetLoginRequest(c.Request.Context(), challenge); err != nil {
		log.Error("hydra login request lookup failed", zap.Error(err))
		h.reject(c, challenge, "server_error", "login request lookup failed")
		return
	}

	identity, err := h.kratos.WhoAmI(c.Request.Context(), c.GetHeader("Cookie"))
	if err != nil {
		if err == kratosclient.ErrNoSession {
			h.redirectToKratosLogin(c, challenge)
			return
		}
		log.Error("kratos session lookup failed", zap.Error(err))
		h.reject(c, challenge, "server_error", "session lookup failed")
		return
	}

	redirectTo, err := h.hydra.AcceptLoginRequest(c.Request.Context(), challenge, identity.ID)
	if err != nil {
		log.Error("hydra login accept failed", zap.Error(err))
		h.reject(c, challenge, "server_error", "login accept failed")
		return
	}
	log.Info("login challenge accepted", zap.String("subject", identity.ID))
	c.Redirect(http.StatusFound, redirectTo)
}

// redirectToKratosLogin sends the browser to Kratos's self-service login UI,
// preserving this /login URL (with its login_challenge) as return_to so the
// flow resumes here once the Kratos session exists.
func (h *LoginHandler) redirectToKratosLogin(c *gin.Context, challenge string) {
	returnTo := selfURL(c)
	target := fmt.Sprintf("%s/self-service/login/browser?return_to=%s",
		h.kratosPublicBrowser, url.QueryEscape(returnTo))
	h.log.Info("no kratos session; redirecting to login UI",
		zap.String("login_challenge", challenge), zap.String("return_to", returnTo))
	c.Redirect(http.StatusFound, target)
}

func (h *LoginHandler) reject(c *gin.Context, challenge, code, description string) {
	redirectTo, err := h.hydra.RejectLoginRequest(context.Background(), challenge, code, description)
	if err != nil {
		h.log.Error("hydra login reject failed", zap.String("login_challenge", challenge), zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": code, "error_description": description})
		return
	}
	h.log.Info("login challenge rejected", zap.String("login_challenge", challenge), zap.String("reason", description))
	c.Redirect(http.StatusFound, redirectTo)
}

func selfURL(c *gin.Context) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
	}
	return scheme + "://" + c.Request.Host + c.Request.URL.RequestURI()
}
