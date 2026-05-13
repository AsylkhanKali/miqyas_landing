package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/goszakup/platform/internal/platform/auditclient"
	"github.com/goszakup/platform/internal/platform/logger"
	"github.com/goszakup/platform/internal/platform/otelx"
	"github.com/goszakup/platform/internal/platform/pgxdb"
	"github.com/goszakup/platform/internal/submission/config"
	platformpkg "github.com/goszakup/platform/internal/submission/platform"
	"github.com/goszakup/platform/internal/submission/storage"
	"github.com/goszakup/platform/internal/submission/workflows"
)

const (
	serviceName    = "submission-worker"
	serviceVersion = "0.1.0"
)

func main() {
	cfg, err := config.LoadWorker()
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

	tc, err := client.Dial(client.Options{HostPort: cfg.TemporalHost})
	if err != nil {
		log.Error("temporal connect", "err", err)
		os.Exit(1)
	}
	defer tc.Close()

	// Регистрируем stub-адаптеры для перечисленных площадок.
	// Реальные адаптеры подключаются в проде через отдельную сборку.
	adapters := make([]platformpkg.Adapter, 0)
	for _, name := range strings.Split(cfg.Platforms, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		adapters = append(adapters, platformpkg.NewStub(name))
	}
	registry := platformpkg.NewRegistry(adapters...)

	acts := &workflows.Activities{
		Repo:      storage.New(db),
		Platforms: registry,
		Audit:     auditclient.New(cfg.AuditURL, cfg.AuditToken, cfg.AuditTimeout),
	}

	w := worker.New(tc, workflows.TaskQueue, worker.Options{})
	w.RegisterWorkflow(workflows.SubmissionWorkflow)
	w.RegisterActivity(acts)

	log.Info("submission worker starting",
		"task_queue", workflows.TaskQueue,
		"temporal", cfg.TemporalHost,
		"platforms", cfg.Platforms,
	)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}
