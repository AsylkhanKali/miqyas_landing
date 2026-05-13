package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tender — нормализованная запись о закупке из внешнего API.
type Tender struct {
	ExternalID   string
	Status       string
	Title        string
	OrganizerBIN string
	PublishDate  *time.Time
	DeadlineAt   *time.Time
	Payload      map[string]any
}

// Reference — элемент справочника.
type Reference struct {
	Kind    string
	Code    string
	NameRU  string
	NameKZ  string
	Payload map[string]any
}

// SyncRun — запись о запуске синхронизации (аудит).
type SyncRun struct {
	ID          int64
	WorkflowID  string
	RunID       string
	Kind        string // "tenders" | "references"
	StartedAt   time.Time
	FinishedAt  *time.Time
	Status      string // "running" | "ok" | "failed"
	ItemsFetched int
	ErrorMsg    string
}

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// UpsertTenders — атомарный upsert пачки закупок.
func (r *Repository) UpsertTenders(ctx context.Context, tenders []Tender) error {
	if len(tenders) == 0 {
		return nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, t := range tenders {
		raw, err := json.Marshal(t.Payload)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO tender_intel.tenders
				(external_id, status, title, organizer_bin, publish_date, deadline_at, payload, fetched_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,now(),now())
			ON CONFLICT (external_id) DO UPDATE SET
				status       = EXCLUDED.status,
				title        = EXCLUDED.title,
				organizer_bin = EXCLUDED.organizer_bin,
				publish_date = EXCLUDED.publish_date,
				deadline_at  = EXCLUDED.deadline_at,
				payload      = EXCLUDED.payload,
				updated_at   = now()
		`, t.ExternalID, t.Status, t.Title, t.OrganizerBIN, t.PublishDate, t.DeadlineAt, raw)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpsertReferences — атомарный upsert пачки справочников.
func (r *Repository) UpsertReferences(ctx context.Context, refs []Reference) error {
	if len(refs) == 0 {
		return nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, ref := range refs {
		raw, _ := json.Marshal(ref.Payload)
		_, err = tx.Exec(ctx, `
			INSERT INTO tender_intel.references (kind, code, name_ru, name_kz, payload, fetched_at)
			VALUES ($1,$2,$3,$4,$5,now())
			ON CONFLICT (kind, code) DO UPDATE SET
				name_ru    = EXCLUDED.name_ru,
				name_kz    = EXCLUDED.name_kz,
				payload    = EXCLUDED.payload,
				fetched_at = now()
		`, ref.Kind, ref.Code, ref.NameRU, ref.NameKZ, raw)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// InsertSyncRun открывает запись о запуске синхронизации и возвращает её ID.
func (r *Repository) InsertSyncRun(ctx context.Context, run SyncRun) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `
		INSERT INTO tender_intel.sync_runs (workflow_id, run_id, kind, started_at, status)
		VALUES ($1,$2,$3,now(),'running')
		RETURNING id
	`, run.WorkflowID, run.RunID, run.Kind).Scan(&id)
	return id, err
}

// FinishSyncRun закрывает запись о запуске синхронизации.
func (r *Repository) FinishSyncRun(ctx context.Context, id int64, status string, items int, errMsg string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE tender_intel.sync_runs
		SET finished_at=now(), status=$2, items_fetched=$3, error_msg=$4
		WHERE id=$1
	`, id, status, items, errMsg)
	return err
}
