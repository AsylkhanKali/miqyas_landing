// Package domain — типы Submission Service.
//
// Бизнес-инвариант, не подлежащий обходу:
//   Подача невозможна позже T-30 минут до дедлайна.
//   Все автоматические попытки подачи в этом окне отклоняются.
//   Это правило защищает от риска дисквалификации и операционной паники.
package domain

import (
	"time"

	"github.com/google/uuid"
)

type State string

const (
	StateDraft        State = "draft"
	StateReviewed     State = "reviewed"
	StateSigned       State = "signed"
	StateSubmitted    State = "submitted"
	StateAcknowledged State = "acknowledged"
	StateArchived     State = "archived"
	StateCancelled    State = "cancelled"
	StateFailed       State = "failed"
)

// CutoffBeforeDeadline — защитное окно перед дедлайном площадки.
// Внутри окна автоматическая подача запрещена; разрешено только
// явное подтверждение оператора (отдельный signal с override-флагом).
const CutoffBeforeDeadline = 30 * time.Minute

// Submission — проекция состояния подачи.
type Submission struct {
	ID              uuid.UUID `json:"id"`
	OrgID           string    `json:"org_id"`
	TenderID        string    `json:"tender_id"`
	LotID           string    `json:"lot_id,omitempty"`
	Platform        string    `json:"platform"`
	DocumentID      uuid.UUID `json:"document_id"`
	DocumentVersion int       `json:"document_version"`
	DeadlineAt      time.Time `json:"deadline_at"`
	State           State     `json:"state"`
	WorkflowID      string    `json:"workflow_id"`
	RunID           string    `json:"run_id"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Transition — иммутабельная запись об изменении состояния.
type Transition struct {
	ID           int64          `json:"id"`
	SubmissionID uuid.UUID      `json:"submission_id"`
	FromState    State          `json:"from_state"`
	ToState      State          `json:"to_state"`
	Actor        string         `json:"actor"`
	Reason       string         `json:"reason,omitempty"`
	OccurredAt   time.Time      `json:"occurred_at"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}
