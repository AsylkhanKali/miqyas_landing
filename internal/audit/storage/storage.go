// Package storage реализует append-only журнал аудита с цепочкой хэшей.
//
// Гарантии:
//   - UPDATE/DELETE заблокированы на уровне БД триггерами.
//   - Каждая вставка происходит в транзакции с SELECT ... FOR UPDATE на
//     audit.chain_tail, что сериализует записи и гарантирует корректность
//     цепочки prev_hash → hash.
//   - hash = SHA-256(detserialize(event) || prev_hash).
package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Event — запись аудита, как она передаётся клиентом сервиса.
type Event struct {
	OccurredAt  time.Time      `json:"occurred_at"`
	ActorType   string         `json:"actor_type"`
	ActorID     string         `json:"actor_id"`
	Action      string         `json:"action"`
	Resource    string         `json:"resource"`
	OrgID       string         `json:"org_id,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	BeforeState map[string]any `json:"before_state,omitempty"`
	AfterState  map[string]any `json:"after_state,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// Stored — запись после вставки, с присвоенным seq и hash-цепочкой.
type Stored struct {
	Event
	Seq      int64  `json:"seq"`
	PrevHash []byte `json:"prev_hash"`
	Hash     []byte `json:"hash"`
}

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// Append добавляет запись в журнал, обновляет хвост цепочки.
// Возвращает сохранённую запись с seq, prev_hash и hash.
func (r *Repository) Append(ctx context.Context, e Event) (Stored, error) {
	if err := validate(e); err != nil {
		return Stored{}, err
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}

	var out Stored
	err := pgx.BeginTxFunc(ctx, r.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var prev []byte
		if err := tx.QueryRow(ctx, `
			SELECT last_hash FROM audit.chain_tail WHERE id = 1 FOR UPDATE
		`).Scan(&prev); err != nil {
			return fmt.Errorf("lock chain tail: %w", err)
		}

		hash := computeHash(e, prev)

		before, _ := json.Marshal(orEmpty(e.BeforeState))
		after, _ := json.Marshal(orEmpty(e.AfterState))
		meta, _ := json.Marshal(orEmpty(e.Metadata))

		var seq int64
		err := tx.QueryRow(ctx, `
			INSERT INTO audit.events
				(occurred_at, actor_type, actor_id, action, resource, org_id, trace_id,
				 before_state, after_state, metadata, prev_hash, hash)
			VALUES ($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9,$10,$11,$12)
			RETURNING seq
		`,
			e.OccurredAt, e.ActorType, e.ActorID, e.Action, e.Resource,
			e.OrgID, e.TraceID, before, after, meta, prev, hash,
		).Scan(&seq)
		if err != nil {
			return fmt.Errorf("insert event: %w", err)
		}

		if _, err := tx.Exec(ctx, `
			UPDATE audit.chain_tail SET last_hash = $1, last_seq = $2 WHERE id = 1
		`, hash, seq); err != nil {
			return fmt.Errorf("update tail: %w", err)
		}

		out = Stored{Event: e, Seq: seq, PrevHash: prev, Hash: hash}
		return nil
	})
	return out, err
}

// Query — фильтры для выборки журнала. Все поля опциональны.
type Query struct {
	Actor    string
	Resource string
	Action   string
	OrgID    string
	From     time.Time
	To       time.Time
	Limit    int
}

func (r *Repository) List(ctx context.Context, q Query) ([]Stored, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	sql := `
		SELECT seq, occurred_at, actor_type, actor_id, action, resource,
		       COALESCE(org_id,''), COALESCE(trace_id,''),
		       before_state, after_state, metadata, prev_hash, hash
		FROM audit.events
		WHERE ($1='' OR actor_id = $1)
		  AND ($2='' OR resource = $2)
		  AND ($3='' OR action = $3)
		  AND ($4='' OR org_id = $4)
		  AND ($5::timestamptz IS NULL OR occurred_at >= $5)
		  AND ($6::timestamptz IS NULL OR occurred_at <= $6)
		ORDER BY seq DESC
		LIMIT $7
	`
	var fromArg, toArg any
	if !q.From.IsZero() {
		fromArg = q.From
	}
	if !q.To.IsZero() {
		toArg = q.To
	}

	rows, err := r.db.Query(ctx, sql, q.Actor, q.Resource, q.Action, q.OrgID, fromArg, toArg, q.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Stored
	for rows.Next() {
		var s Stored
		var before, after, meta []byte
		if err := rows.Scan(
			&s.Seq, &s.OccurredAt, &s.ActorType, &s.ActorID, &s.Action, &s.Resource,
			&s.OrgID, &s.TraceID, &before, &after, &meta, &s.PrevHash, &s.Hash,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(before, &s.BeforeState)
		_ = json.Unmarshal(after, &s.AfterState)
		_ = json.Unmarshal(meta, &s.Metadata)
		out = append(out, s)
	}
	return out, rows.Err()
}

// VerifyChain проходит весь журнал по возрастанию seq и проверяет,
// что hash каждой записи соответствует SHA-256(содержимое || prev_hash).
// Возвращает seq первого расхождения или 0, если цепочка целостна.
func (r *Repository) VerifyChain(ctx context.Context) (badSeq int64, err error) {
	rows, err := r.db.Query(ctx, `
		SELECT seq, occurred_at, actor_type, actor_id, action, resource,
		       COALESCE(org_id,''), COALESCE(trace_id,''),
		       before_state, after_state, metadata, prev_hash, hash
		FROM audit.events
		ORDER BY seq ASC
	`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var expectedPrev []byte // nil == E'\\x' — начальный last_hash в chain_tail
	for rows.Next() {
		var s Stored
		var before, after, meta []byte
		if err := rows.Scan(
			&s.Seq, &s.OccurredAt, &s.ActorType, &s.ActorID, &s.Action, &s.Resource,
			&s.OrgID, &s.TraceID, &before, &after, &meta, &s.PrevHash, &s.Hash,
		); err != nil {
			return 0, err
		}
		if !bytes.Equal(s.PrevHash, expectedPrev) {
			return s.Seq, nil
		}
		_ = json.Unmarshal(before, &s.BeforeState)
		_ = json.Unmarshal(after, &s.AfterState)
		_ = json.Unmarshal(meta, &s.Metadata)

		recomputed := computeHash(s.Event, s.PrevHash)
		if !bytes.Equal(recomputed, s.Hash) {
			return s.Seq, nil
		}
		expectedPrev = s.Hash
	}
	return 0, rows.Err()
}

// ── helpers ───────────────────────────────────────────────────────────────

func validate(e Event) error {
	if e.ActorType == "" || e.ActorID == "" {
		return errors.New("actor_type and actor_id are required")
	}
	if e.Action == "" {
		return errors.New("action is required")
	}
	if e.Resource == "" {
		return errors.New("resource is required")
	}
	return nil
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// computeHash детерминированно сериализует событие и считает SHA-256 || prev_hash.
// Порядок и формат полей здесь часть контракта цепочки — менять только с
// миграцией данных (пересчёт всей цепочки).
func computeHash(e Event, prev []byte) []byte {
	h := sha256.New()

	writeStr := func(s string) {
		var lenBuf [8]byte
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(s)))
		h.Write(lenBuf[:])
		h.Write([]byte(s))
	}
	writeJSON := func(m map[string]any) {
		b, _ := json.Marshal(orEmpty(m))
		writeStr(string(b))
	}

	writeStr(e.OccurredAt.UTC().Format(time.RFC3339Nano))
	writeStr(e.ActorType)
	writeStr(e.ActorID)
	writeStr(e.Action)
	writeStr(e.Resource)
	writeStr(e.OrgID)
	writeStr(e.TraceID)
	writeJSON(e.BeforeState)
	writeJSON(e.AfterState)
	writeJSON(e.Metadata)
	h.Write(prev)

	return h.Sum(nil)
}
