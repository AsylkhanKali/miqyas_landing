package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/goszakup/platform/internal/identity/config"
	"github.com/goszakup/platform/internal/identity/httpapi"
	"github.com/goszakup/platform/internal/identity/jwtkey"
	"github.com/goszakup/platform/internal/identity/service"
	"github.com/goszakup/platform/internal/identity/storage"
	"github.com/goszakup/platform/internal/platform/logger"
	"github.com/goszakup/platform/internal/platform/otelx"
	"github.com/goszakup/platform/internal/platform/pgxdb"
)

const (
	serviceName    = "identity"
	serviceVersion = "0.1.0"
)

func main() {
	cfg, err := config.Load()
	log := logger.New(cfg.LogLevel, serviceName)
	if err != nil {
		log.Error("config load", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownOTel, err := otelx.Setup(ctx, serviceName, serviceVersion, cfg.OTELEndpoint)
	if err != nil {
		log.Error("otel setup", "err", err)
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownOTel(shutdownCtx)
	}()

	db, err := pgxdb.Connect(ctx, cfg.PostgresDSN)
	if err != nil {
		log.Error("postgres connect", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := os.MkdirAll(filepath.Dir(cfg.JWTKeyPath), 0o700); err != nil {
		log.Error("mkdir keys", "err", err)
		os.Exit(1)
	}
	issuer, err := jwtkey.LoadOrCreate(cfg.JWTKeyPath, cfg.JWTKeyBits)
	if err != nil {
		log.Error("jwt key", "err", err)
		os.Exit(1)
	}
	log.Info("jwt key ready", "kid", issuer.KID(), "path", cfg.JWTKeyPath)

	if cfg.DevSkipMFA {
		log.Warn("DEV_SKIP_MFA is enabled — TOTP not required on login. NEVER use in production!")
	}

	svc := service.New(storage.New(db), service.Options{
		Issuer:        issuer,
		IssuerName:    cfg.IssuerName,
		AccessTTL:     cfg.AccessTTL,
		RefreshTTL:    cfg.RefreshTTL,
		TOTPMasterKey: cfg.TOTPMasterKey(),
		DevSkipMFA:    cfg.DevSkipMFA,
	})

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Log: log, DB: db, Svc: svc, Issuer: issuer,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("identity listening", "addr", cfg.HTTPAddr, "issuer", cfg.IssuerName)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
}
