package workflows

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/goszakup/platform/internal/platform/auditclient"
	"github.com/goszakup/platform/internal/submission/domain"
	platformpkg "github.com/goszakup/platform/internal/submission/platform"
	"github.com/goszakup/platform/internal/submission/storage"
)

// Activities — зависимости активностей submission worker.
type Activities struct {
	Repo      *storage.Repository
	Platforms *platformpkg.Registry
	Audit     *auditclient.Client
}

// ── RecordTransition ──────────────────────────────────────────────────────

type RecordTransitionParams struct {
	SubmissionID string
	From         string
	To           string
	Actor        string
	Reason       string
	Metadata     map[string]any
}

// RecordTransitionActivity — единая точка записи переходов состояния.
// Делает две вещи: переводит состояние в БД и публикует событие в audit.
// "" в From означает старт (pseudo-state) — мы не вызываем БД-переход,
// только аудит.
func (a *Activities) RecordTransitionActivity(ctx context.Context, p RecordTransitionParams) error {
	id, err := uuid.Parse(p.SubmissionID)
	if err != nil {
		return fmt.Errorf("invalid submission id: %w", err)
	}

	if p.From != "" {
		// Нормальный переход в БД.
		if _, err := a.Repo.Transition(ctx, id, domain.State(p.From), domain.State(p.To), p.Actor, p.Reason, p.Metadata); err != nil {
			return fmt.Errorf("repo transition: %w", err)
		}
	}

	// Аудит — обязательная часть. Без него считаем переход неудачным,
	// чтобы Temporal ретраил активность.
	meta := p.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	meta["from"] = p.From
	meta["to"] = p.To
	meta["reason"] = p.Reason

	actorType := "system"
	if p.Actor != "" && p.Actor != "system" {
		actorType = "operator"
	}

	if _, err := a.Audit.Append(ctx, auditclient.Event{
		ActorType: actorType,
		ActorID:   nonEmpty(p.Actor, "submission-worker"),
		Action:    "submission.transitioned",
		Resource:  "submission:" + p.SubmissionID,
		Metadata:  meta,
	}); err != nil {
		return fmt.Errorf("audit append: %w", err)
	}
	return nil
}

// ── SubmitToPlatform ──────────────────────────────────────────────────────

type SubmitActivityParams struct {
	SubmissionID    uuid.UUID
	Platform        string
	OrgID           string
	TenderID        string
	LotID           string
	DocumentID      uuid.UUID
	DocumentVersion int
	IdempotencyKey  string
	Actor           string
}

type SubmitActivityResult struct {
	ReceiptID  string    `json:"receipt_id"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// SubmitToPlatformActivity — единственное место в системе, где происходит
// фактическая подача на ЭТП. Любые ретраи здесь запрещены (см. workflow):
// риск двойной подачи слишком высок, лучше остановиться и отдать оператору.
func (a *Activities) SubmitToPlatformActivity(ctx context.Context, p SubmitActivityParams) (SubmitActivityResult, error) {
	if p.IdempotencyKey == "" {
		return SubmitActivityResult{}, fmt.Errorf("idempotency_key required")
	}
	adapter, err := a.Platforms.Get(p.Platform)
	if err != nil {
		return SubmitActivityResult{}, err
	}

	// Аудит-событие ДО фактической подачи: если процесс упадёт сразу после,
	// мы всё равно знаем, что пытались.
	_, _ = a.Audit.Append(ctx, auditclient.Event{
		ActorType: "service",
		ActorID:   "submission-worker",
		Action:    "submission.submit.attempt",
		Resource:  "submission:" + p.SubmissionID.String(),
		Metadata: map[string]any{
			"platform":        p.Platform,
			"tender_id":       p.TenderID,
			"lot_id":          p.LotID,
			"idempotency_key": p.IdempotencyKey,
			"actor":           p.Actor,
		},
	})

	res, err := adapter.Submit(ctx, platformpkg.SubmitRequest{
		OrgID:           p.OrgID,
		TenderID:        p.TenderID,
		LotID:           p.LotID,
		DocumentID:      p.DocumentID,
		DocumentVersion: p.DocumentVersion,
		IdempotencyKey:  p.IdempotencyKey,
	})
	if err != nil {
		_, _ = a.Audit.Append(ctx, auditclient.Event{
			ActorType: "service",
			ActorID:   "submission-worker",
			Action:    "submission.submit.failed",
			Resource:  "submission:" + p.SubmissionID.String(),
			Metadata:  map[string]any{"error": err.Error()},
		})
		return SubmitActivityResult{}, err
	}

	return SubmitActivityResult{
		ReceiptID:  res.ReceiptID,
		AcceptedAt: res.AcceptedAt,
	}, nil
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
