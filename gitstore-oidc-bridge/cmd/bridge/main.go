// Command bridge runs gitstore-oidc-bridge: the minimal standalone service
// resolving Hydra's login/consent challenges against the current Kratos session
// (specs/059-optional-oidc-provider).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/bridge"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/config"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/hydraclient"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/kratosclient"
)

func main() {
	log, err := zap.NewProduction()
	if err != nil {
		panic(fmt.Sprintf("logger init: %v", err))
	}
	defer log.Sync() //nolint:errcheck // best-effort flush on exit

	cfg, err := config.Load()
	if err != nil {
		log.Fatal("config load failed", zap.Error(err))
	}

	hydra := hydraclient.New(cfg.HydraAdminURI)
	kratos := kratosclient.New(cfg.KratosPublicURI, cfg.KratosAdminURI)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatal("trusted proxies setup failed", zap.Error(err))
	}

	login := bridge.NewLoginHandler(hydra, kratos, cfg.KratosPublicBrowserURI, log)
	consent := bridge.NewConsentHandler(hydra, kratos, cfg.OAuth2ClientScope, cfg.DefaultAudience, log)
	health := bridge.NewHealthHandler(cfg.HydraAdminURI, cfg.KratosAdminURI)

	r.GET("/login", login.Handle)
	r.GET("/consent", consent.Handle)
	r.GET("/health", health.Handle)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.ListenPort),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("gitstore-oidc-bridge listening", zap.Int("port", cfg.ListenPort))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("http server failed", zap.Error(err))
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", zap.Error(err))
	}
}
