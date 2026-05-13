package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/goszakup/platform/internal/console/aggregator"
	"github.com/goszakup/platform/internal/console/clients"
	"github.com/goszakup/platform/internal/console/config"
	"github.com/goszakup/platform/internal/console/httpapi"
	"github.com/goszakup/platform/internal/platform/auth"
	"github.com/goszakup/platform/internal/platform/logger"
	"github.com/goszakup/platform/internal/platform/otelx"
)

const (
	serviceName    = "console-bff"
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

	jwks := auth.NewJWKSClient(cfg.IdentityJWKSURL, 5*time.Minute)

	sub := clients.NewSubmission(cfg.SubmissionURL, cfg.Timeout)
	doc := clients.NewDocument(cfg.DocumentURL, cfg.Timeout)
	es := clients.NewEsign(cfg.EsignURL, cfg.Timeout)
	au := clients.NewAudit(cfg.AuditURL, cfg.Timeout)

	agg := aggregator.New(sub, doc, es, au)

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Log:         log,
			Agg:         agg,
			Submission:  sub,
			Document:    doc,
			Esign:       es,
			JWKS:        jwks,
			AllowOrigin: cfg.AllowOrigin,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("console BFF listening", "addr", cfg.HTTPAddr, "origin", cfg.AllowOrigin)
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
