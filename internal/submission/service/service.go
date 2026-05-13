// Package service — use cases Submission Service: создаёт запись о подаче,
// стартует Temporal workflow, маршрутизирует сигналы.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/client"

	"github.com/goszakup/platform/internal/submission/domain"
	"github.com/goszakup/platform/internal/submission/storage"
	"github.com/goszakup/platform/internal/submission/workflows"
)

type Service struct {
	repo *storage.Repository
	tc   client.Client
}

func New(repo *storage.Repository, tc client.Client) *Service {
	return &Service{repo: repo, tc: tc}
}

type StartInput struct {
	OrgID           string
	TenderID        string
	LotID           string
	Platform        string
	DocumentID      uuid.UUID
	DocumentVersion int
	DeadlineAt      time.Time
	CreatedBy       string
}

// Start создаёт submission и стартует workflow.
// Идемпотентен по (org_id, tender_id, lot_id): повторный вызов вернёт ErrConflict.
func (s *Service) Start(ctx context.Context, in StartInput) (domain.Submission, error) {
	if in.DeadlineAt.Before(time.Now().Add(domain.CutoffBeforeDeadline)) {
		// Создание подачи в самом окне отсечки — допустимо, но опасно;
		// предупреждаем clear-cut отказом, чтобы оператор увидел рамки.
		return domain.Submission{}, fmt.Errorf(
			"deadline is within cutoff window (%s); create submissions earlier",
			domain.CutoffBeforeDeadline,
		)
	}

	id := uuid.New()
	wfID := fmt.Sprintf("submission-%s", id.String())

	// Старт workflow ДО записи в БД — чтобы получить run_id; idempotent через WorkflowIDReusePolicy.
	we, err := s.tc.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    wfID,
		TaskQueue:             workflows.TaskQueue,
		WorkflowIDReusePolicy: 0, // AllowDuplicateFailedOnly — поведение по умолчанию
	}, workflows.SubmissionWorkflowName, workflows.StartParams{
		SubmissionID:    id,
		OrgID:           in.OrgID,
		TenderID:        in.TenderID,
		LotID:           in.LotID,
		Platform:        in.Platform,
		DocumentID:      in.DocumentID,
		DocumentVersion: in.DocumentVersion,
		DeadlineAt:      in.DeadlineAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return domain.Submission{}, fmt.Errorf("start workflow: %w", err)
	}

	sub, err := s.repo.Create(ctx, domain.Submission{
		ID:              id,
		OrgID:           in.OrgID,
		TenderID:        in.TenderID,
		LotID:           in.LotID,
		Platform:        in.Platform,
		DocumentID:      in.DocumentID,
		DocumentVersion: in.DocumentVersion,
		DeadlineAt:      in.DeadlineAt,
		State:           domain.StateDraft,
		WorkflowID:      we.GetID(),
		RunID:           we.GetRunID(),
		CreatedBy:       in.CreatedBy,
	})
	if err != nil {
		// Если БД отказала (например, конфликт), уведомляем workflow,
		// чтобы он корректно завершился.
		_ = s.tc.CancelWorkflow(ctx, we.GetID(), we.GetRunID())
		return domain.Submission{}, err
	}
	return sub, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Submission, []domain.Transition, error) {
	sub, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Submission{}, nil, err
	}
	tr, err := s.repo.ListTransitions(ctx, id)
	return sub, tr, err
}

// Signal маршрутизирует сигнал в Temporal по имени.
// Не интерпретирует payload — это задача workflow.
func (s *Service) Signal(ctx context.Context, id uuid.UUID, name string, payload any) error {
	sub, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.tc.SignalWorkflow(ctx, sub.WorkflowID, sub.RunID, name, payload)
}
