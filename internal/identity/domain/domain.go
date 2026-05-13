// Package domain — типы Identity Service.
package domain

import (
	"time"

	"github.com/google/uuid"
)

type UserStatus string

const (
	StatusActive   UserStatus = "active"
	StatusDisabled UserStatus = "disabled"
)

type User struct {
	ID            uuid.UUID  `json:"id"`
	Email         string     `json:"email"`
	FullName      string     `json:"full_name"`
	OrgID         string     `json:"org_id"`
	Roles         []string   `json:"roles"`
	Status        UserStatus `json:"status"`
	TOTPEnrolled  bool       `json:"totp_enrolled"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// AccessTokenClaims — содержимое access-токена (JWT).
// Используется и identity-сервисом для выпуска, и платформенной auth-middleware
// для валидации.
type AccessTokenClaims struct {
	UserID uuid.UUID `json:"sub"`
	Email  string    `json:"email"`
	OrgID  string    `json:"org_id"`
	Roles  []string  `json:"roles"`
}

// LoginOutcome — фиксированные значения для аудит-журнала login_events.
type LoginOutcome string

const (
	OutcomeSuccess      LoginOutcome = "success"
	OutcomeBadPassword  LoginOutcome = "bad_password"
	OutcomeBadTOTP      LoginOutcome = "bad_totp"
	OutcomeDisabled     LoginOutcome = "disabled"
	OutcomeNoUser       LoginOutcome = "no_user"
	OutcomeMFARequired  LoginOutcome = "mfa_required"
)
