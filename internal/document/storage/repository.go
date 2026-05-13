package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/document/domain"
)

var ErrNotFound = errors.New("not found")

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// ── Templates ─────────────────────────────────────────────────────────────

func (r *Repository) UpsertTemplate(ctx context.Context, t domain.Template) (domain.Template, error) {
	schema, _ := json.Marshal(t.Schema)
	rules, _ := json.Marshal(t.Rules)

	err := r.db.QueryRow(ctx, `
		INSERT INTO document.templates (code, name, description, schema, rules)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (code) DO UPDATE SET
			name        = EXCLUDED.name,
			description = EXCLUDED.description,
			schema      = EXCLUDED.schema,
			rules       = EXCLUDED.rules,
			updated_at  = now()
		RETURNING id, created_at, updated_at
	`, t.Code, t.Name, t.Description, schema, rules).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func (r *Repository) GetTemplate(ctx context.Context, code string) (domain.Template, error) {
	var t domain.Template
	var schema, rules []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, code, name, description, schema, rules, created_at, updated_at
		FROM document.templates WHERE code = $1
	`, code).Scan(&t.ID, &t.Code, &t.Name, &t.Description, &schema, &rules, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Template{}, ErrNotFound
	}
	if err != nil {
		return domain.Template{}, err
	}
	_ = json.Unmarshal(schema, &t.Schema)
	_ = json.Unmarshal(rules, &t.Rules)
	return t, nil
}

func (r *Repository) ListTemplates(ctx context.Context) ([]domain.Template, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, code, name, description, schema, rules, created_at, updated_at
		FROM document.templates ORDER BY code
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Template
	for rows.Next() {
		var t domain.Template
		var schema, rules []byte
		if err := rows.Scan(&t.ID, &t.Code, &t.Name, &t.Description, &schema, &rules, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(schema, &t.Schema)
		_ = json.Unmarshal(rules, &t.Rules)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ── Documents ─────────────────────────────────────────────────────────────

func (r *Repository) CreateDocument(ctx context.Context, d domain.Document) (domain.Document, error) {
	err := r.db.QueryRow(ctx, `
		INSERT INTO document.documents (org_id, template_code, title, status, created_by)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, created_at, updated_at
	`, d.OrgID, d.TemplateCode, d.Title, d.Status, d.CreatedBy).Scan(&d.ID, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}

func (r *Repository) GetDocument(ctx context.Context, id uuid.UUID) (domain.Document, error) {
	var d domain.Document
	err := r.db.QueryRow(ctx, `
		SELECT id, org_id, template_code, title, status, created_by, created_at, updated_at
		FROM document.documents WHERE id = $1
	`, id).Scan(&d.ID, &d.OrgID, &d.TemplateCode, &d.Title, &d.Status, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Document{}, ErrNotFound
	}
	return d, err
}

func (r *Repository) UpdateDocumentStatus(ctx context.Context, id uuid.UUID, status string) error {
	tag, err := r.db.Exec(ctx, `
		UPDATE document.documents SET status=$2, updated_at=now() WHERE id=$1
	`, id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Versions (immutable) ──────────────────────────────────────────────────

// AddVersion вставляет новую иммутабельную версию документа.
// Номер версии вычисляется атомарно как MAX(version)+1 в одной транзакции.
func (r *Repository) AddVersion(ctx context.Context, v domain.Version) (domain.Version, error) {
	payload, _ := json.Marshal(v.Payload)
	var validation []byte
	if v.Validation != nil {
		validation, _ = json.Marshal(v.Validation)
	} else {
		validation = []byte("{}")
	}

	err := pgx.BeginTxFunc(ctx, r.db, pgx.TxOptions{IsoLevel: pgx.Serializable}, func(tx pgx.Tx) error {
		var next int
		if err := tx.QueryRow(ctx, `
			SELECT COALESCE(MAX(version), 0) + 1
			FROM document.document_versions
			WHERE document_id = $1
		`, v.DocumentID).Scan(&next); err != nil {
			return fmt.Errorf("compute version: %w", err)
		}
		v.Version = next

		return tx.QueryRow(ctx, `
			INSERT INTO document.document_versions
				(document_id, version, payload, s3_bucket, s3_key, s3_etag,
				 content_sha256, content_size, validated, validation, created_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			RETURNING id, created_at
		`,
			v.DocumentID, v.Version, payload, v.S3Bucket, v.S3Key, v.S3ETag,
			v.ContentSHA256, v.ContentSize, v.Validated, validation, v.CreatedBy,
		).Scan(&v.ID, &v.CreatedAt)
	})
	return v, err
}

func (r *Repository) LatestVersion(ctx context.Context, docID uuid.UUID) (domain.Version, error) {
	var v domain.Version
	var payload, validation []byte
	err := r.db.QueryRow(ctx, `
		SELECT id, document_id, version, payload, s3_bucket, s3_key, s3_etag,
		       content_sha256, content_size, validated, validation, created_by, created_at
		FROM document.document_versions
		WHERE document_id = $1
		ORDER BY version DESC LIMIT 1
	`, docID).Scan(
		&v.ID, &v.DocumentID, &v.Version, &payload, &v.S3Bucket, &v.S3Key, &v.S3ETag,
		&v.ContentSHA256, &v.ContentSize, &v.Validated, &validation, &v.CreatedBy, &v.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Version{}, ErrNotFound
	}
	if err != nil {
		return domain.Version{}, err
	}
	_ = json.Unmarshal(payload, &v.Payload)
	if len(validation) > 0 && string(validation) != "{}" {
		var vr domain.ValidationResult
		_ = json.Unmarshal(validation, &vr)
		v.Validation = &vr
	}
	return v, nil
}
