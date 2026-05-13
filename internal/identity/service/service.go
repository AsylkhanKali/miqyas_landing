// Package service — use cases Identity: регистрация, MFA, логин.
//
// Принципы:
//   - login_events ВСЕГДА пишется (успех или провал) — даже если пользователь
//     не найден. Это нужно для расследований и rate-limit.
//   - Сообщения об ошибках логина намеренно одинаковые (`invalid credentials`),
//     чтобы не различать "нет пользователя" и "неверный пароль".
//   - Access-токен короткий (15 мин), refresh — длинный (24 ч). Refresh
//     хранится в БД ХЭШИРОВАННЫМ.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/goszakup/platform/internal/identity/domain"
	"github.com/goszakup/platform/internal/identity/jwtkey"
	"github.com/goszakup/platform/internal/identity/password"
	"github.com/goszakup/platform/internal/identity/storage"
	"github.com/goszakup/platform/internal/identity/totp"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrMFARequired        = errors.New("mfa required")
	ErrMFANotEnrolled     = errors.New("mfa not enrolled")
)

type Service struct {
	repo          *storage.Repository
	issuer        *jwtkey.Issuer
	issuerName    string
	accessTTL     time.Duration
	refreshTTL    time.Duration
	totpMasterKey []byte
	devSkipMFA    bool
}

type Options struct {
	Issuer        *jwtkey.Issuer
	IssuerName    string
	AccessTTL     time.Duration
	RefreshTTL    time.Duration
	TOTPMasterKey []byte // 32 байта
	// DevSkipMFA — DEV ONLY: позволяет залогиниться без TOTP для bootstrap.
	// Никогда не включать в production.
	DevSkipMFA bool
}

func New(repo *storage.Repository, opts Options) *Service {
	return &Service{
		repo: repo, issuer: opts.Issuer, issuerName: opts.IssuerName,
		accessTTL: opts.AccessTTL, refreshTTL: opts.RefreshTTL,
		totpMasterKey: opts.TOTPMasterKey, devSkipMFA: opts.DevSkipMFA,
	}
}

// ── Register ──────────────────────────────────────────────────────────────

type RegisterInput struct {
	Email    string
	FullName string
	OrgID    string
	Password string
	Roles    []string
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (domain.User, error) {
	if in.Email == "" || in.Password == "" || in.OrgID == "" {
		return domain.User{}, errors.New("email, password, org_id required")
	}
	if len(in.Password) < 12 {
		return domain.User{}, errors.New("password must be at least 12 characters")
	}
	hash, err := password.Hash(in.Password)
	if err != nil {
		return domain.User{}, err
	}
	return s.repo.CreateUser(ctx, storage.CreateUserParams{
		Email: in.Email, FullName: in.FullName, OrgID: in.OrgID,
		PasswordHash: hash, Roles: in.Roles,
	})
}

// ── TOTP enrollment ───────────────────────────────────────────────────────

type EnrollResult struct {
	OTPAuthURL string
	Base32     string
}

func (s *Service) EnrollTOTP(ctx context.Context, userID uuid.UUID) (EnrollResult, error) {
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return EnrollResult{}, err
	}
	en, err := totp.Enroll(u.UserEmail(), s.totpMasterKey)
	if err != nil {
		return EnrollResult{}, err
	}
	if err := s.repo.UpdateTOTPSecret(ctx, userID, en.SecretEnc); err != nil {
		return EnrollResult{}, err
	}
	return EnrollResult{OTPAuthURL: en.OTPAuthURL, Base32: en.Base32}, nil
}

func (s *Service) ConfirmTOTP(ctx context.Context, userID uuid.UUID, code string) error {
	u, err := s.repo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}
	ok, err := totp.Verify(s.totpMasterKey, u.TOTPSecret(), code)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("invalid totp code")
	}
	return s.repo.MarkTOTPEnrolled(ctx, userID)
}

// ── Login ─────────────────────────────────────────────────────────────────

type LoginInput struct {
	Email     string
	Password  string
	TOTPCode  string
	IP        string
	UserAgent string
	TraceID   string
}

type LoginResult struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	User         domain.User `json:"user"`
}

// Login проверяет пароль, TOTP, выпускает access + refresh.
// Обязательно пишет login_events независимо от исхода.
func (s *Service) Login(ctx context.Context, in LoginInput) (LoginResult, error) {
	u, err := s.repo.GetUserByEmail(ctx, in.Email)
	if errors.Is(err, storage.ErrNotFound) {
		_ = s.recordLogin(ctx, nil, in, domain.OutcomeNoUser)
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}

	if u.UserStatus() != domain.StatusActive {
		_ = s.recordLogin(ctx, ptrUUID(u.UserID()), in, domain.OutcomeDisabled)
		return LoginResult{}, ErrInvalidCredentials
	}

	if err := password.Verify(in.Password, u.PasswordHash()); err != nil {
		_ = s.recordLogin(ctx, ptrUUID(u.UserID()), in, domain.OutcomeBadPassword)
		return LoginResult{}, ErrInvalidCredentials
	}

	if !u.Enrolled() {
		if !s.devSkipMFA {
			_ = s.recordLogin(ctx, ptrUUID(u.UserID()), in, domain.OutcomeMFARequired)
			return LoginResult{}, ErrMFANotEnrolled
		}
		// DEV ONLY: пропускаем TOTP, выдаём токен для enrollment.
	} else {
		if in.TOTPCode == "" {
			_ = s.recordLogin(ctx, ptrUUID(u.UserID()), in, domain.OutcomeMFARequired)
			return LoginResult{}, ErrMFARequired
		}
		ok, err := totp.Verify(s.totpMasterKey, u.TOTPSecret(), in.TOTPCode)
		if err != nil || !ok {
			_ = s.recordLogin(ctx, ptrUUID(u.UserID()), in, domain.OutcomeBadTOTP)
			return LoginResult{}, ErrInvalidCredentials
		}
	}

	access, exp, err := s.issueAccess(u)
	if err != nil {
		return LoginResult{}, err
	}
	refresh, err := s.issueRefresh(ctx, u.UserID(), in)
	if err != nil {
		return LoginResult{}, err
	}

	_ = s.recordLogin(ctx, ptrUUID(u.UserID()), in, domain.OutcomeSuccess)
	return LoginResult{
		AccessToken: access, RefreshToken: refresh, ExpiresAt: exp,
		User: u.ToDomain(),
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────

func (s *Service) issueAccess(u storage.UserRow) (string, time.Time, error) {
	now := time.Now().UTC()
	exp := now.Add(s.accessTTL)
	claims := jwt.MapClaims{
		"sub":     u.UserID().String(),
		"email":   u.UserEmail(),
		"org_id":  u.UserOrgID(),
		"roles":   u.UserRoles(),
		"iss":     s.issuerName,
		"iat":     now.Unix(),
		"nbf":     now.Unix(),
		"exp":     exp.Unix(),
		"token_type": "access",
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	t.Header["kid"] = s.issuer.KID()
	signed, err := t.SignedString(s.issuer.Private())
	return signed, exp, err
}

func (s *Service) issueRefresh(ctx context.Context, userID uuid.UUID, in LoginInput) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	if _, err := s.repo.CreateSession(ctx, storage.CreateSessionParams{
		UserID:        userID,
		RefreshSHA256: sum[:],
		UserAgent:     in.UserAgent,
		IP:            in.IP,
		ExpiresAt:     time.Now().UTC().Add(s.refreshTTL),
	}); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) recordLogin(ctx context.Context, userID *uuid.UUID, in LoginInput, outcome domain.LoginOutcome) error {
	return s.repo.AppendLoginEvent(ctx, storage.LoginEvent{
		UserID: userID, Email: in.Email, Outcome: outcome,
		IP: in.IP, UserAgent: in.UserAgent, TraceID: in.TraceID,
	})
}

func ptrUUID(u uuid.UUID) *uuid.UUID { return &u }

// SelfFromAccess читает claims из подписанного access-токена. Используется
// для эндпоинта /me — настоящая валидация чужих сервисов идёт через JWKS
// (см. internal/platform/auth).
func (s *Service) SelfFromAccess(tokenStr string) (domain.AccessTokenClaims, error) {
	tok, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Method)
		}
		return s.issuer.Public(), nil
	})
	if err != nil || !tok.Valid {
		return domain.AccessTokenClaims{}, errors.New("invalid token")
	}
	c, ok := tok.Claims.(jwt.MapClaims)
	if !ok {
		return domain.AccessTokenClaims{}, errors.New("invalid claims")
	}
	idStr, _ := c["sub"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil {
		return domain.AccessTokenClaims{}, errors.New("invalid sub")
	}
	rolesAny, _ := c["roles"].([]any)
	roles := make([]string, 0, len(rolesAny))
	for _, r := range rolesAny {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}
	email, _ := c["email"].(string)
	org, _ := c["org_id"].(string)
	return domain.AccessTokenClaims{
		UserID: id, Email: email, OrgID: org, Roles: roles,
	}, nil
}
