package storage

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/esign/domain"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("key already registered")
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

func (r *Repository) RegisterKey(ctx context.Context, k domain.Key) (domain.Key, error) {
	err := r.db.QueryRow(ctx, `
		INSERT INTO esign.keys
			(org_id, owner, cert_subject_cn, cert_serial, cert_not_before, cert_not_after,
			 cert_sha256, cert_pem, backend, backend_ref, algorithm, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id, created_at, updated_at
	`,
		k.OrgID, k.Owner, k.CertSubjectCN, k.CertSerial, k.CertNotBefore, k.CertNotAfter,
		k.CertSHA256, k.CertPEM, k.Backend, k.BackendRef, k.Algorithm, k.Status,
	).Scan(&k.ID, &k.CreatedAt, &k.UpdatedAt)
	if err != nil {
		if isUnique(err) {
			return domain.Key{}, ErrConflict
		}
		return domain.Key{}, err
	}
	return k, nil
}

func (r *Repository) GetKey(ctx context.Context, id uuid.UUID) (domain.Key, error) {
	var k domain.Key
	err := r.db.QueryRow(ctx, `
		SELECT id, org_id, owner, cert_subject_cn, cert_serial, cert_not_before, cert_not_after,
		       cert_sha256, cert_pem, backend, backend_ref, algorithm, status, revoked_at,
		       created_at, updated_at
		FROM esign.keys WHERE id=$1
	`, id).Scan(
		&k.ID, &k.OrgID, &k.Owner, &k.CertSubjectCN, &k.CertSerial, &k.CertNotBefore, &k.CertNotAfter,
		&k.CertSHA256, &k.CertPEM, &k.Backend, &k.BackendRef, &k.Algorithm, &k.Status, &k.RevokedAt,
		&k.CreatedAt, &k.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Key{}, ErrNotFound
	}
	return k, err
}

func (r *Repository) ListKeysByOwner(ctx context.Context, owner string) ([]domain.Key, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, org_id, owner, cert_subject_cn, cert_serial, cert_not_before, cert_not_after,
		       cert_sha256, cert_pem, backend, backend_ref, algorithm, status, revoked_at,
		       created_at, updated_at
		FROM esign.keys WHERE owner = $1 ORDER BY created_at DESC
	`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Key
	for rows.Next() {
		var k domain.Key
		if err := rows.Scan(
			&k.ID, &k.OrgID, &k.Owner, &k.CertSubjectCN, &k.CertSerial, &k.CertNotBefore, &k.CertNotAfter,
			&k.CertSHA256, &k.CertPEM, &k.Backend, &k.BackendRef, &k.Algorithm, &k.Status, &k.RevokedAt,
			&k.CreatedAt, &k.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r *Repository) RecordSign(ctx context.Context, op domain.SignOperation) (domain.SignOperation, error) {
	err := r.db.QueryRow(ctx, `
		INSERT INTO esign.sign_operations
			(key_id, actor, purpose, input_sha256, signature_sha256, algorithm, trace_id)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))
		RETURNING id, signed_at
	`, op.KeyID, op.Actor, op.Purpose, op.InputSHA256, op.SignatureSHA256, op.Algorithm, op.TraceID,
	).Scan(&op.ID, &op.SignedAt)
	return op, err
}

func isUnique(err error) bool {
	type sqlState interface{ SQLState() string }
	var s sqlState
	if errors.As(err, &s) {
		return s.SQLState() == "23505"
	}
	return false
}
