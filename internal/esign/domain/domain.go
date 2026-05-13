// Package domain — типы ЭЦП Broker.
//
// Базовое разделение: брокер видит только публичные сертификаты и ссылки
// на хранилище приватных ключей (HSM slot/label или зашифрованный файл).
// Сам приватный ключ не сериализуется наружу и не передаётся по сети.
package domain

import (
	"time"

	"github.com/google/uuid"
)

type Backend string

const (
	BackendSoftware Backend = "software" // dev / staging — файл, AES-GCM
	BackendPKCS11   Backend = "pkcs11"   // prod — HSM
)

type KeyStatus string

const (
	StatusActive  KeyStatus = "active"
	StatusRevoked KeyStatus = "revoked"
	StatusExpired KeyStatus = "expired"
)

// Key — метаданные зарегистрированного ключа.
type Key struct {
	ID            uuid.UUID `json:"id"`
	OrgID         string    `json:"org_id"`
	Owner         string    `json:"owner"`
	CertSubjectCN string    `json:"cert_subject_cn"`
	CertSerial    string    `json:"cert_serial"`
	CertNotBefore time.Time `json:"cert_not_before"`
	CertNotAfter  time.Time `json:"cert_not_after"`
	CertSHA256    []byte    `json:"cert_sha256"`
	CertPEM       []byte    `json:"cert_pem"`
	Backend       Backend   `json:"backend"`
	BackendRef    string    `json:"backend_ref"`
	Algorithm     string    `json:"algorithm"`
	Status        KeyStatus `json:"status"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// SignOperation — иммутабельная запись о факте подписания.
type SignOperation struct {
	ID              int64     `json:"id"`
	KeyID           uuid.UUID `json:"key_id"`
	Actor           string    `json:"actor"`
	Purpose         string    `json:"purpose"`
	InputSHA256     []byte    `json:"input_sha256"`
	SignatureSHA256 []byte    `json:"signature_sha256"`
	Algorithm       string    `json:"algorithm"`
	TraceID         string    `json:"trace_id,omitempty"`
	SignedAt        time.Time `json:"signed_at"`
}
