package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/goszakup/platform/internal/esign/config"
	"github.com/goszakup/platform/internal/esign/httpapi"
	"github.com/goszakup/platform/internal/esign/service"
	"github.com/goszakup/platform/internal/esign/signer"
	"github.com/goszakup/platform/internal/esign/storage"
	"github.com/goszakup/platform/internal/platform/auth"
	"github.com/goszakup/platform/internal/platform/auditclient"
	"github.com/goszakup/platform/internal/platform/logger"
	"github.com/goszakup/platform/internal/platform/otelx"
	"github.com/goszakup/platform/internal/platform/pgxdb"
)

const (
	serviceName    = "esign"
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

	sw, err := signer.NewSoftware(cfg.KeysDir, cfg.MasterKey())
	if err != nil {
		log.Error("software signer init", "err", err)
		os.Exit(1)
	}
	hsm := signer.NewPKCS11() // заглушка до подключения реального HSM

	jwks := auth.NewJWKSClient(cfg.IdentityJWKSURL, 5*time.Minute)

	svc := service.New(
		storage.New(db), sw, hsm,
		auditclient.New(cfg.AuditURL, cfg.AuditToken, cfg.AuditTimeout),
	)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           httpapi.NewRouter(httpapi.Deps{Log: log, DB: db, Svc: svc, JWKS: jwks}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("esign http listening", "addr", cfg.HTTPAddr, "keys_dir", cfg.KeysDir)
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
