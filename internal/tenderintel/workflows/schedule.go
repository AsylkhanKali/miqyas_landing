package workflows

import (
	"context"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/client"
)

// ScheduleOptions — настройки cron-расписания для TenderSyncWorkflow.
type ScheduleOptions struct {
	// CronExpr — стандартный cron (5 полей). По умолчанию раз в час.
	CronExpr string
}

// EnsureSchedule создаёт или обновляет расписание синхронизации закупок в Temporal.
// Безопасно вызывать повторно (идемпотентно).
func EnsureSchedule(ctx context.Context, tc client.Client, log *slog.Logger, opts ScheduleOptions) error {
	if opts.CronExpr == "" {
		opts.CronExpr = "0 * * * *" // раз в час
	}

	scheduleID := "tender-sync-schedule"

	handle := tc.ScheduleClient().GetHandle(ctx, scheduleID)

	// Попробуем описать — если существует, обновляем; если нет — создаём.
	_, err := handle.Describe(ctx)
	if err == nil {
		log.Info("schedule already exists, skipping", "schedule_id", scheduleID)
		return nil
	}

	_, err = tc.ScheduleClient().Create(ctx, client.ScheduleOptions{
		ID: scheduleID,
		Spec: client.ScheduleSpec{
			CronExpressions: []string{opts.CronExpr},
		},
		Action: &client.ScheduleWorkflowAction{
			ID:        "tender-sync",
			Workflow:  TenderSyncWorkflowName,
			TaskQueue: TaskQueue,
			Args: []any{TenderSyncParams{
				Page:  0,
				Limit: 100,
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("create schedule: %w", err)
	}

	log.Info("schedule created", "schedule_id", scheduleID, "cron", opts.CronExpr)
	return nil
}
