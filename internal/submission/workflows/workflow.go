package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/goszakup/platform/internal/submission/domain"
)

const TaskQueue = "submission"
const SubmissionWorkflowName = "SubmissionWorkflow"

// Все внешние действия (запись в БД, аудит, вызов адаптера площадки) идут
// через activities. Workflow содержит только координацию и инварианты.
var defaultActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 30 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		InitialInterval:    1 * time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    15 * time.Second,
		MaximumAttempts:    3,
	},
}

// submitActivityOptions — подача площадке. Ретраи отключены: повторная
// автоматическая попытка может привести к дубликату. Если активность
// упала, оператор разбирается вручную.
var submitActivityOptions = workflow.ActivityOptions{
	StartToCloseTimeout: 60 * time.Second,
	RetryPolicy: &temporal.RetryPolicy{
		MaximumAttempts: 1,
	},
}

// SubmissionWorkflow — durable координация подачи.
//
// Линейная FSM, продвигаемая сигналами от оператора:
//   draft → reviewed → signed → submitted → acknowledged → archived
//   из любого pre-submitted состояния доступна отмена.
//
// Защита окна T-30 минут перед дедлайном: автоматический submit
// запрещён; разрешён только сигнал с AcknowledgeCutoff=true и в этом
// случае помечается в transition.metadata.
func SubmissionWorkflow(ctx workflow.Context, params StartParams) (Result, error) {
	log := workflow.GetLogger(ctx)

	deadline, err := time.Parse(time.RFC3339, params.DeadlineAt)
	if err != nil {
		return Result{FinalState: string(domain.StateFailed)}, fmt.Errorf("invalid deadline: %w", err)
	}

	state := domain.StateDraft
	_ = workflow.SetQueryHandler(ctx, QueryState, func() (string, error) {
		return string(state), nil
	})

	actCtx := workflow.WithActivityOptions(ctx, defaultActivityOptions)

	// Сообщаем БД и аудиту о факте старта (transition pseudo → draft).
	if err := writeTransition(actCtx, params.SubmissionID, "", domain.StateDraft, "system", "workflow_started", nil); err != nil {
		log.Error("initial transition failed", "error", err)
		return Result{FinalState: string(domain.StateFailed)}, err
	}

	// Контейнер для последнего ReceiptID — выставляется submit activity.
	var receiptID string

	// Сигнал отмены может прийти в любой момент до submitted.
	cancelCh := workflow.GetSignalChannel(ctx, SignalCancel)

	// ── draft → reviewed ────────────────────────────────────────────────
	reviewCh := workflow.GetSignalChannel(ctx, SignalReview)
	cancelled, cancelMsg := awaitOrCancel(ctx, reviewCh, cancelCh, func(sig ReviewSignal) error {
		return writeTransition(actCtx, params.SubmissionID, domain.StateDraft, domain.StateReviewed, sig.Actor, sig.Reason, nil)
	})
	if cancelled {
		return finishCancelled(actCtx, params.SubmissionID, state, cancelMsg)
	}
	state = domain.StateReviewed

	// ── reviewed → signed ───────────────────────────────────────────────
	signCh := workflow.GetSignalChannel(ctx, SignalSign)
	cancelled, cancelMsg = awaitOrCancel(ctx, signCh, cancelCh, func(sig SignSignal) error {
		meta := map[string]any{
			"esig_cert_cn":  sig.ESIGCertCN,
			"esig_cert_sha": sig.ESIGCertSHA,
		}
		return writeTransition(actCtx, params.SubmissionID, domain.StateReviewed, domain.StateSigned, sig.Actor, "esig_applied", meta)
	})
	if cancelled {
		return finishCancelled(actCtx, params.SubmissionID, state, cancelMsg)
	}
	state = domain.StateSigned

	// ── signed → submitted ──────────────────────────────────────────────
	// Здесь работает защита окна T-30 минут.
	submitCh := workflow.GetSignalChannel(ctx, SignalSubmit)
	for {
		var sig SubmitSignal
		var gotCancel CancelSignal
		var sawCancel bool

		sel := workflow.NewSelector(ctx)
		sel.AddReceive(submitCh, func(ch workflow.ReceiveChannel, _ bool) {
			ch.Receive(ctx, &sig)
		})
		sel.AddReceive(cancelCh, func(ch workflow.ReceiveChannel, _ bool) {
			ch.Receive(ctx, &gotCancel)
			sawCancel = true
		})
		sel.Select(ctx)

		if sawCancel {
			return finishCancelled(actCtx, params.SubmissionID, state, gotCancel.Reason)
		}

		now := workflow.Now(ctx)
		untilDeadline := deadline.Sub(now)
		insideCutoff := untilDeadline > 0 && untilDeadline <= domain.CutoffBeforeDeadline

		if untilDeadline <= 0 {
			// Дедлайн прошёл — подача невозможна.
			_ = writeTransition(actCtx, params.SubmissionID, state, domain.StateFailed,
				sig.Actor, "deadline_passed", map[string]any{
					"deadline":      deadline.Format(time.RFC3339),
					"now":           now.Format(time.RFC3339),
				})
			return Result{FinalState: string(domain.StateFailed), Reason: "deadline_passed"}, nil
		}

		if insideCutoff && !sig.AcknowledgeCutoff {
			// Внутри защитного окна без явного подтверждения — отклоняем сигнал,
			// возвращаемся в состояние signed, ждём корректный submit.
			_ = writeTransition(actCtx, params.SubmissionID, state, state,
				sig.Actor, "submit_rejected_cutoff_window", map[string]any{
					"until_deadline_seconds": int64(untilDeadline.Seconds()),
					"cutoff_seconds":         int64(domain.CutoffBeforeDeadline.Seconds()),
				})
			log.Warn("submit rejected: inside cutoff window without acknowledge",
				"untilDeadline", untilDeadline,
			)
			continue
		}

		// Подача разрешена. Выполняем submit-активность БЕЗ ретраев.
		subCtx := workflow.WithActivityOptions(ctx, submitActivityOptions)
		var subRes SubmitActivityResult
		err := workflow.ExecuteActivity(subCtx, "SubmitToPlatformActivity", SubmitActivityParams{
			SubmissionID:    params.SubmissionID,
			Platform:        params.Platform,
			OrgID:           params.OrgID,
			TenderID:        params.TenderID,
			LotID:           params.LotID,
			DocumentID:      params.DocumentID,
			DocumentVersion: params.DocumentVersion,
			IdempotencyKey:  sig.IdempotencyKey,
			Actor:           sig.Actor,
		}).Get(ctx, &subRes)
		if err != nil {
			_ = writeTransition(actCtx, params.SubmissionID, state, domain.StateFailed,
				sig.Actor, "submit_activity_failed", map[string]any{"error": err.Error()})
			return Result{FinalState: string(domain.StateFailed), Reason: err.Error()}, err
		}

		receiptID = subRes.ReceiptID
		meta := map[string]any{
			"receipt_id":     subRes.ReceiptID,
			"accepted_at":    subRes.AcceptedAt,
			"inside_cutoff":  insideCutoff,
			"acknowledged":   sig.AcknowledgeCutoff,
		}
		if err := writeTransition(actCtx, params.SubmissionID, state, domain.StateSubmitted, sig.Actor, "submitted_to_platform", meta); err != nil {
			return Result{FinalState: string(domain.StateFailed), Reason: err.Error()}, err
		}
		state = domain.StateSubmitted
		break
	}

	// ── submitted → acknowledged → archived ─────────────────────────────
	// Простая последовательная фиксация. Реально acknowledge может ждать
	// callback от площадки; в этой версии — сразу.
	if err := writeTransition(actCtx, params.SubmissionID, domain.StateSubmitted, domain.StateAcknowledged,
		"system", "platform_receipt_recorded", map[string]any{"receipt_id": receiptID}); err != nil {
		return Result{FinalState: string(domain.StateAcknowledged), ReceiptID: receiptID}, err
	}
	if err := writeTransition(actCtx, params.SubmissionID, domain.StateAcknowledged, domain.StateArchived,
		"system", "archived", nil); err != nil {
		return Result{FinalState: string(domain.StateArchived), ReceiptID: receiptID}, err
	}

	return Result{
		FinalState: string(domain.StateArchived),
		ReceiptID:  receiptID,
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────

// awaitOrCancel блокируется на двух каналах: либо ожидаемый сигнал, либо
// отмена. Возвращает (cancelled, reason). При получении ожидаемого сигнала
// вызывает onSignal — туда обычно отдаётся writeTransition.
//
// Параметризован на T, чтобы переиспользовать для review/sign.
func awaitOrCancel[T any](ctx workflow.Context, ch, cancelCh workflow.ReceiveChannel, onSignal func(T) error) (bool, string) {
	var sig T
	var cancel CancelSignal
	var sawCancel bool

	sel := workflow.NewSelector(ctx)
	sel.AddReceive(ch, func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, &sig)
	})
	sel.AddReceive(cancelCh, func(c workflow.ReceiveChannel, _ bool) {
		c.Receive(ctx, &cancel)
		sawCancel = true
	})
	sel.Select(ctx)

	if sawCancel {
		return true, cancel.Reason
	}
	if err := onSignal(sig); err != nil {
		workflow.GetLogger(ctx).Error("transition activity failed", "error", err)
	}
	return false, ""
}

func writeTransition(ctx workflow.Context, id any, from, to domain.State, actor, reason string, meta map[string]any) error {
	return workflow.ExecuteActivity(ctx, "RecordTransitionActivity", RecordTransitionParams{
		SubmissionID: fmt.Sprint(id),
		From:         string(from),
		To:           string(to),
		Actor:        actor,
		Reason:       reason,
		Metadata:     meta,
	}).Get(ctx, nil)
}

func finishCancelled(actCtx workflow.Context, id any, from domain.State, reason string) (Result, error) {
	_ = writeTransition(actCtx, id, from, domain.StateCancelled, "operator", reason, nil)
	return Result{FinalState: string(domain.StateCancelled), Cancelled: true, Reason: reason}, nil
}
