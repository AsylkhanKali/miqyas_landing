package storage

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/identity/domain"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("email already taken")
)

type Repository struct {
	db *pgxpool.Pool
}

func New(db *pgxpool.Pool) *Repository { return &Repository{db: db} }

// ── Users ─────────────────────────────────────────────────────────────────

type CreateUserParams struct {
	Email        string
	FullName     string
	OrgID        string
	PasswordHash string
	Roles        []string
}

func (r *Repository) CreateUser(ctx context.Context, p CreateUserParams) (domain.User, error) {
	var u domain.User
	err := r.db.QueryRow(ctx, `
		INSERT INTO identity.users (email, full_name, org_id, password_hash, roles)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, email, full_name, org_id, roles, status, totp_enrolled, created_at, updated_at
	`, p.Email, p.FullName, p.OrgID, p.PasswordHash, p.Roles).Scan(
		&u.ID, &u.Email, &u.FullName, &u.OrgID, &u.Roles, &u.Status, &u.TOTPEnrolled, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if isUnique(err) {
			return domain.User{}, ErrConflict
		}
		return domain.User{}, err
	}
	return u, nil
}

// userRow — внутренняя структура с приватными полями для login flow.
// Поля строчные — намеренно, чтобы не конфликтовали с методами-геттерами.
type userRow struct {
	id            uuid.UUID
	email         string
	fullName      string
	orgID         string
	passwordHash  string
	totpSecretEnc []byte
	totpEnrolled  bool
	roles         []string
	status        domain.UserStatus
}

func (r *Repository) getUserRow(ctx context.Context, column string, arg any) (userRow, error) {
	// column — статическая константа из вызывающего кода, не пользовательский ввод.
	var u userRow
	err := r.db.QueryRow(ctx, `
		SELECT id, email, full_name, org_id, password_hash, totp_secret_enc, totp_enrolled, roles, status
		FROM identity.users WHERE `+column+` = $1
	`, arg).Scan(&u.id, &u.email, &u.fullName, &u.orgID, &u.passwordHash, &u.totpSecretEnc, &u.totpEnrolled, &u.roles, &u.status)
	if errors.Is(err, pgx.ErrNoRows) {
		return userRow{}, ErrNotFound
	}
	return u, err
}

// GetUserByEmail возвращает чувствительные поля (для login flow). Не отдавать наружу.
func (r *Repository) GetUserByEmail(ctx context.Context, email string) (userRow, error) {
	return r.getUserRow(ctx, "email", email)
}

func (r *Repository) GetUserByID(ctx context.Context, id uuid.UUID) (userRow, error) {
	return r.getUserRow(ctx, "id", id)
}

// UserRow — публичный alias для тестов и service-слоя.
type UserRow = userRow

func (u userRow) ToDomain() domain.User {
	return domain.User{
		ID: u.id, Email: u.email, FullName: u.fullName, OrgID: u.orgID,
		Roles: u.roles, Status: u.status, TOTPEnrolled: u.totpEnrolled,
	}
}

// Геттеры приватных полей — используются только в service.
func (u userRow) PasswordHash() string          { return u.passwordHash }
func (u userRow) TOTPSecret() []byte            { return u.totpSecretEnc }
func (u userRow) Enrolled() bool                { return u.totpEnrolled }
func (u userRow) UserID() uuid.UUID             { return u.id }
func (u userRow) UserRoles() []string           { return u.roles }
func (u userRow) UserStatus() domain.UserStatus { return u.status }
func (u userRow) UserOrgID() string             { return u.orgID }
func (u userRow) UserEmail() string             { return u.email }

// UpdateTOTPSecret сохраняет зашифрованный TOTP-секрет; totp_enrolled остаётся false
// до тех пор, пока не подтвержден первый код.
func (r *Repository) UpdateTOTPSecret(ctx context.Context, id uuid.UUID, enc []byte) error {
	tag, err := r.db.Exec(ctx, `UPDATE identity.users SET totp_secret_enc=$2, updated_at=now() WHERE id=$1`, id, enc)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) MarkTOTPEnrolled(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `UPDATE identity.users SET totp_enrolled=true, updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Sessions ──────────────────────────────────────────────────────────────

type CreateSessionParams struct {
	UserID        uuid.UUID
	RefreshSHA256 []byte
	UserAgent     string
	IP            string
	ExpiresAt     time.Time
}

func (r *Repository) CreateSession(ctx context.Context, p CreateSessionParams) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.db.QueryRow(ctx, `
		INSERT INTO identity.sessions (user_id, refresh_sha256, user_agent, ip, expires_at)
		VALUES ($1,$2,$3,$4,$5) RETURNING id
	`, p.UserID, p.RefreshSHA256, p.UserAgent, p.IP, p.ExpiresAt).Scan(&id)
	return id, err
}

func (r *Repository) RevokeSession(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `UPDATE identity.sessions SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── Login events ──────────────────────────────────────────────────────────

type LoginEvent struct {
	UserID    *uuid.UUID
	Email     string
	Outcome   domain.LoginOutcome
	IP        string
	UserAgent string
	TraceID   string
}

func (r *Repository) AppendLoginEvent(ctx context.Context, e LoginEvent) error {
	_, err := r.db.Exec(ctx, `
		INSERT INTO identity.login_events (user_id, email, outcome, ip, user_agent, trace_id)
		VALUES ($1,$2,$3,$4,$5,NULLIF($6,''))
	`, e.UserID, e.Email, e.Outcome, e.IP, e.UserAgent, e.TraceID)
	return err
}

func isUnique(err error) bool {
	type sqlState interface{ SQLState() string }
	var s sqlState
	if errors.As(err, &s) {
		return s.SQLState() == "23505"
	}
	return false
}
