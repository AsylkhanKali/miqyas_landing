package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/goszakup/platform/internal/platform/auditclient"
	"github.com/goszakup/platform/internal/platform/logger"
	"github.com/goszakup/platform/internal/platform/otelx"
	"github.com/goszakup/platform/internal/platform/pgxdb"
	"github.com/goszakup/platform/internal/tenderintel/goszakup"
	"github.com/goszakup/platform/internal/tenderintel/storage"
	"github.com/goszakup/platform/internal/tenderintel/workerconfig"
	"github.com/goszakup/platform/internal/tenderintel/workflows"
)

const (
	serviceName    = "tender-intel-worker"
	serviceVersion = "0.1.0"
)

func main() {
	cfg, err := workerconfig.Load()
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

	tc, err := client.Dial(client.Options{
		HostPort: cfg.TemporalHost,
	})
	if err != nil {
		log.Error("temporal connect", "err", err)
		os.Exit(1)
	}
	defer tc.Close()

	// Регистрируем workflow и activities.
	acts := &workflows.Activities{
		Client: goszakup.New(cfg.GoszakupURL, cfg.GoszakupTO),
		Repo:   storage.New(db),
		Audit:  auditclient.New(cfg.AuditURL, cfg.AuditToken, cfg.AuditTimeout),
	}
	w := worker.New(tc, workflows.TaskQueue, worker.Options{})
	w.RegisterWorkflow(workflows.TenderSyncWorkflow)
	w.RegisterActivity(acts)

	// Убеждаемся, что расписание создано в Temporal.
	if err := workflows.EnsureSchedule(ctx, tc, log, workflows.ScheduleOptions{
		CronExpr: cfg.SyncCron,
	}); err != nil {
		log.Error("ensure schedule", "err", err)
		os.Exit(1)
	}

	log.Info("worker starting", "task_queue", workflows.TaskQueue, "temporal", cfg.TemporalHost)
	if err := w.Run(worker.InterruptCh()); err != nil {
		log.Error("worker stopped", "err", err)
		os.Exit(1)
	}
}
