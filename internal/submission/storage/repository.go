package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/submission/domain"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrConflict  = errors.New("submission already exists for this lot")
	ErrBadState  = errors.New("invalid state transition")
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// Create вставляет новую подачу в состоянии draft.
// Идемпотентен через UNIQUE(org_id, tender_id, lot_id): повторный вызов
// для той же тройки вернёт ErrConflict.
func (r *Repository) Create(ctx context.Context, s domain.Submission) (domain.Submission, error) {
	err := r.db.QueryRow(ctx, `
		INSERT INTO submission.submissions
			(id, org_id, tender_id, lot_id, platform, document_id, document_version,
			 deadline_at, state, workflow_id, run_id, created_by)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING created_at, updated_at
	`,
		s.ID, s.OrgID, s.TenderID, s.LotID, s.Platform, s.DocumentID, s.DocumentVersion,
		s.DeadlineAt, s.State, s.WorkflowID, s.RunID, s.CreatedBy,
	).Scan(&s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.Submission{}, ErrConflict
		}
		return domain.Submission{}, err
	}
	return s, nil
}

func (r *Repository) Get(ctx context.Context, id uuid.UUID) (domain.Submission, error) {
	var s domain.Submission
	var lotID *string
	err := r.db.QueryRow(ctx, `
		SELECT id, org_id, tender_id, lot_id, platform, document_id, document_version,
		       deadline_at, state, workflow_id, run_id, created_by, created_at, updated_at
		FROM submission.submissions WHERE id=$1
	`, id).Scan(
		&s.ID, &s.OrgID, &s.TenderID, &lotID, &s.Platform, &s.DocumentID, &s.DocumentVersion,
		&s.DeadlineAt, &s.State, &s.WorkflowID, &s.RunID, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Submission{}, ErrNotFound
	}
	if err != nil {
		return domain.Submission{}, err
	}
	if lotID != nil {
		s.LotID = *lotID
	}
	return s, nil
}

// Transition атомарно меняет состояние submission и пишет транзишен.
// Проверяет from_state — оптимистичная блокировка.
func (r *Repository) Transition(ctx context.Context, id uuid.UUID, from, to domain.State, actor, reason string, meta map[string]any) (domain.Transition, error) {
	if meta == nil {
		meta = map[string]any{}
	}
	metaBytes, _ := json.Marshal(meta)

	var out domain.Transition
	err := pgx.BeginTxFunc(ctx, r.db, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE submission.submissions
			SET state=$2, updated_at=now()
			WHERE id=$1 AND state=$3
		`, id, to, from)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Узнаем, что именно: нет submission или несовпадает состояние.
			var cur domain.State
			row := tx.QueryRow(ctx, `SELECT state FROM submission.submissions WHERE id=$1`, id)
			if scanErr := row.Scan(&cur); errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("%w: cannot move from %s to %s", ErrBadState, cur, to)
		}

		return tx.QueryRow(ctx, `
			INSERT INTO submission.transitions
				(submission_id, from_state, to_state, actor, reason, metadata)
			VALUES ($1,$2,$3,$4,$5,$6)
			RETURNING id, occurred_at
		`, id, from, to, actor, reason, metaBytes).Scan(&out.ID, &out.OccurredAt)
	})
	if err != nil {
		return domain.Transition{}, err
	}
	out.SubmissionID = id
	out.FromState = from
	out.ToState = to
	out.Actor = actor
	out.Reason = reason
	out.Metadata = meta
	return out, nil
}

func (r *Repository) ListTransitions(ctx context.Context, id uuid.UUID) ([]domain.Transition, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, submission_id, from_state, to_state, actor, reason, occurred_at, metadata
		FROM submission.transitions
		WHERE submission_id = $1
		ORDER BY occurred_at ASC, id ASC
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Transition
	for rows.Next() {
		var t domain.Transition
		var meta []byte
		if err := rows.Scan(&t.ID, &t.SubmissionID, &t.FromState, &t.ToState, &t.Actor, &t.Reason, &t.OccurredAt, &meta); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(meta, &t.Metadata)
		out = append(out, t)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	// pgx возвращает pgconn.PgError с Code = "23505" для UNIQUE_VIOLATION.
	type sqlState interface{ SQLState() string }
	var s sqlState
	if errors.As(err, &s) {
		return s.SQLState() == "23505"
	}
	return false
}
