package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	TaskQueue = "tender-intel"

	// Имена workflow
	TenderSyncWorkflowName = "TenderSyncWorkflow"
)

// TenderSyncParams — входные параметры для workflow синхронизации.
type TenderSyncParams struct {
	// Page — страница пагинации API. 0 = начиная с первой.
	Page int
	// Limit — сколько записей за одну активность.
	Limit int
}

// TenderSyncResult — итог выполнения.
type TenderSyncResult struct {
	TotalFetched int
	Pages        int
}

// defaultActivityOptions — таймауты и retry-политика для внешних вызовов.
// Подача не входит в этот workflow, поэтому retry разрешён.
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    2 * time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    30 * time.Second,
		MaximumAttempts:    3,
	},
}

// auditActivityOptions — отдельные таймауты для аудит-активности.
// Аудит должен быть быстрым; если он недоступен — это сигнал тревоги,
// но не повод валить sync, поэтому MaximumAttempts ограничен.
var auditActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 10 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    1 * time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    5 * time.Second,
		MaximumAttempts:    3,
	},
}

// emitAudit — best-effort отправка аудит-события из workflow.
// Ошибка логируется, но не прерывает основной поток (журнал не должен
// быть единственной точкой отказа для синхронизации справочников).
func emitAudit(ctx workflow.Context, action, resource string, meta map[string]any) {
	auCtx := workflow.WithActivityOptions(ctx, auditActivityOptions)
	if err := workflow.ExecuteActivity(auCtx, "EmitAuditActivity", AuditParams{
		Action: action, Resource: resource, Metadata: meta,
	}).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("audit emit failed", "action", action, "error", err)
	}
}

// TenderSyncWorkflow — durable workflow для синхронизации открытого API goszakup.
// Постранично читает данные и сохраняет в postgres.
// Не совершает никаких действий от имени оператора.
func TenderSyncWorkflow(ctx workflow.Context, params TenderSyncParams) (TenderSyncResult, error) {
	if params.Limit == 0 {
		params.Limit = 100
	}

	info := workflow.GetInfo(ctx)
	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)
	log := workflow.GetLogger(ctx)

	emitAudit(ctx, "tender.sync.started", "tender-intel:sync", map[string]any{
		"workflow_id": info.WorkflowExecution.ID,
		"run_id":      info.WorkflowExecution.RunID,
		"page":        params.Page,
		"limit":       params.Limit,
	})

	var total, pages int
	page := params.Page

	for {
		var result FetchPageResult
		err := workflow.ExecuteActivity(actCtx, "FetchTendersPageActivity", FetchPageParams{
			Page:  page,
			Limit: params.Limit,
		}).Get(ctx, &result)
		if err != nil {
			log.Error("FetchTendersPage failed", "page", page, "error", err)
			emitAudit(ctx, "tender.sync.failed", "tender-intel:sync", map[string]any{
				"workflow_id": info.WorkflowExecution.ID,
				"stage":       "fetch",
				"page":        page,
				"error":       err.Error(),
			})
			return TenderSyncResult{TotalFetched: total, Pages: pages}, err
		}

		if len(result.Items) == 0 {
			break
		}

		var upsertResult UpsertResult
		err = workflow.ExecuteActivity(actCtx, "UpsertTendersActivity", UpsertParams{
			Items: result.Items,
		}).Get(ctx, &upsertResult)
		if err != nil {
			log.Error("UpsertTenders failed", "page", page, "error", err)
			emitAudit(ctx, "tender.sync.failed", "tender-intel:sync", map[string]any{
				"workflow_id": info.WorkflowExecution.ID,
				"stage":       "upsert",
				"page":        page,
				"error":       err.Error(),
			})
			return TenderSyncResult{TotalFetched: total, Pages: pages}, err
		}

		total += len(result.Items)
		pages++

		log.Info("page synced", "page", page, "items", len(result.Items), "total", total)

		if !result.HasMore {
			break
		}
		page++

		// Небольшая пауза между страницами — уважение к rate limits площадки.
		_ = workflow.Sleep(ctx, 500*time.Millisecond)
	}

	emitAudit(ctx, "tender.sync.finished", "tender-intel:sync", map[string]any{
		"workflow_id":   info.WorkflowExecution.ID,
		"run_id":        info.WorkflowExecution.RunID,
		"total_fetched": total,
		"pages":         pages,
	})

	return TenderSyncResult{TotalFetched: total, Pages: pages}, nil
}
