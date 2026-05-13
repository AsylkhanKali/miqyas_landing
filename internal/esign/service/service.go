// Package service — use cases ЭЦП Broker.
//
// Гарантии:
//   - Каждая операция подписи проходит через аудит-журнал (обязательно,
//     не best-effort): если audit-сервис недоступен, операция отклоняется.
//     Это сознательное ограничение: подпись без следа не лучше отсутствия
//     подписи.
//   - Просроченные/отозванные ключи не используются.
//   - Сам приватный ключ не покидает signer-бэкенд.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/goszakup/platform/internal/esign/domain"
	"github.com/goszakup/platform/internal/esign/signer"
	"github.com/goszakup/platform/internal/esign/storage"
	"github.com/goszakup/platform/internal/platform/auditclient"
)

type Service struct {
	repo     *storage.Repository
	software *signer.Software
	pkcs11   *signer.PKCS11
	audit    *auditclient.Client
}

func New(repo *storage.Repository, sw *signer.Software, hsm *signer.PKCS11, audit *auditclient.Client) *Service {
	return &Service{repo: repo, software: sw, pkcs11: hsm, audit: audit}
}

// ── Register ──────────────────────────────────────────────────────────────

type RegisterInput struct {
	OrgID    string
	Owner    string
	SubjectCN string
	KeySize  int     // 2048 / 3072 / 4096; relevant только для software/dev
	Backend  domain.Backend
}

// Register создаёт новый ключ (DEV: генерирует самоподписанный;
// PROD-PKCS11 — пока не реализовано).
func (s *Service) Register(ctx context.Context, in RegisterInput) (domain.Key, error) {
	if in.Backend == "" {
		in.Backend = domain.BackendSoftware
	}
	if in.KeySize == 0 {
		in.KeySize = 2048
	}

	switch in.Backend {
	case domain.BackendSoftware:
		mat, cert, err := s.software.GenerateAndStore(in.SubjectCN, in.KeySize)
		if err != nil {
			return domain.Key{}, fmt.Errorf("generate: %w", err)
		}
		sum := sha256.Sum256(cert.Raw)
		k := domain.Key{
			OrgID:         in.OrgID,
			Owner:         in.Owner,
			CertSubjectCN: cert.Subject.CommonName,
			CertSerial:    cert.SerialNumber.String(),
			CertNotBefore: cert.NotBefore,
			CertNotAfter:  cert.NotAfter,
			CertSHA256:    sum[:],
			CertPEM:       mat.CertPEM,
			Backend:       domain.BackendSoftware,
			BackendRef:    mat.BackendRef,
			Algorithm:     "RSA-SHA256",
			Status:        domain.StatusActive,
		}
		out, err := s.repo.RegisterKey(ctx, k)
		if err != nil {
			return domain.Key{}, err
		}
		s.auditOrLog(ctx, auditclient.Event{
			ActorType: "operator",
			ActorID:   in.Owner,
			Action:    "esign.key.registered",
			Resource:  "esign-key:" + out.ID.String(),
			Metadata: map[string]any{
				"backend":      out.Backend,
				"subject_cn":   out.CertSubjectCN,
				"sha256":       hex.EncodeToString(out.CertSHA256),
				"not_after":    out.CertNotAfter,
			},
		})
		return out, nil

	case domain.BackendPKCS11:
		return domain.Key{}, fmt.Errorf("pkcs11 registration not implemented yet")
	}
	return domain.Key{}, fmt.Errorf("unknown backend %q", in.Backend)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (domain.Key, error) {
	return s.repo.GetKey(ctx, id)
}

func (s *Service) ListByOwner(ctx context.Context, owner string) ([]domain.Key, error) {
	return s.repo.ListKeysByOwner(ctx, owner)
}

// ── Sign ──────────────────────────────────────────────────────────────────

type SignInput struct {
	KeyID   uuid.UUID
	Actor   string // оператор, инициировавший подпись
	Purpose string // например, "document:<uuid>:v1"
	Data    []byte
}

type SignResult struct {
	KeyID     uuid.UUID `json:"key_id"`
	Algorithm string    `json:"algorithm"`
	Signature []byte    `json:"signature"`
	SignedAt  time.Time `json:"signed_at"`
}

// Sign — главная критическая операция. Обязательный аудит, проверка ключа,
// делегирование в signer-бэкенд. Никаких ретраев — двойная подпись недопустима.
func (s *Service) Sign(ctx context.Context, in SignInput) (SignResult, error) {
	if in.Actor == "" {
		return SignResult{}, fmt.Errorf("actor is required")
	}
	if in.Purpose == "" {
		return SignResult{}, fmt.Errorf("purpose is required")
	}
	if len(in.Data) == 0 {
		return SignResult{}, fmt.Errorf("empty data")
	}

	key, err := s.repo.GetKey(ctx, in.KeyID)
	if err != nil {
		return SignResult{}, err
	}
	if err := assertUsable(key); err != nil {
		return SignResult{}, err
	}

	// Аудит-попытка ДО подписания.
	inputHash := sha256.Sum256(in.Data)
	if _, err := s.audit.Append(ctx, auditclient.Event{
		ActorType: "operator",
		ActorID:   in.Actor,
		Action:    "esign.sign.attempt",
		Resource:  "esign-key:" + key.ID.String(),
		Metadata: map[string]any{
			"purpose":       in.Purpose,
			"input_sha256":  hex.EncodeToString(inputHash[:]),
			"algorithm":     key.Algorithm,
			"size_bytes":    len(in.Data),
		},
	}); err != nil {
		return SignResult{}, fmt.Errorf("audit unavailable, refusing to sign: %w", err)
	}

	// Делегируем в нужный бэкенд.
	var (
		sig    []byte
		signer signerBackend
	)
	switch key.Backend {
	case domain.BackendSoftware:
		signer = s.software
	case domain.BackendPKCS11:
		signer = s.pkcs11
	default:
		return SignResult{}, fmt.Errorf("unknown backend %q", key.Backend)
	}

	sig, err = signer.Sign(signerInput(key, in.Data))
	if err != nil {
		_, _ = s.audit.Append(ctx, auditclient.Event{
			ActorType: "operator",
			ActorID:   in.Actor,
			Action:    "esign.sign.failed",
			Resource:  "esign-key:" + key.ID.String(),
			Metadata: map[string]any{
				"purpose": in.Purpose,
				"error":   err.Error(),
			},
		})
		return SignResult{}, err
	}

	sigHash := sha256.Sum256(sig)
	op := domain.SignOperation{
		KeyID:           key.ID,
		Actor:           in.Actor,
		Purpose:         in.Purpose,
		InputSHA256:     inputHash[:],
		SignatureSHA256: sigHash[:],
		Algorithm:       key.Algorithm,
	}
	if _, err := s.repo.RecordSign(ctx, op); err != nil {
		// Запись операции — обязательна. Подпись формально вышла, но мы
		// сообщаем о сбое аудита, чтобы оператор не считал её надёжной.
		return SignResult{}, fmt.Errorf("record sign operation: %w", err)
	}

	s.auditOrLog(ctx, auditclient.Event{
		ActorType: "operator",
		ActorID:   in.Actor,
		Action:    "esign.signed",
		Resource:  "esign-key:" + key.ID.String(),
		Metadata: map[string]any{
			"purpose":          in.Purpose,
			"input_sha256":     hex.EncodeToString(inputHash[:]),
			"signature_sha256": hex.EncodeToString(sigHash[:]),
			"algorithm":        key.Algorithm,
		},
	})

	return SignResult{
		KeyID:     key.ID,
		Algorithm: key.Algorithm,
		Signature: sig,
		SignedAt:  time.Now().UTC(),
	}, nil
}

// ── helpers ───────────────────────────────────────────────────────────────

type signerBackend interface {
	Sign(in signer.SignInput) ([]byte, error)
}

func signerInput(k domain.Key, data []byte) signer.SignInput {
	return signer.SignInput{
		BackendRef: k.BackendRef,
		Algorithm:  k.Algorithm,
		Data:       data,
	}
}

func assertUsable(k domain.Key) error {
	if k.Status != domain.StatusActive {
		return fmt.Errorf("key status is %s", k.Status)
	}
	now := time.Now().UTC()
	if now.Before(k.CertNotBefore) {
		return fmt.Errorf("certificate not yet valid")
	}
	if now.After(k.CertNotAfter) {
		return fmt.Errorf("certificate expired at %s", k.CertNotAfter)
	}
	return nil
}

func (s *Service) auditOrLog(ctx context.Context, ev auditclient.Event) {
	// Для нерасчётных событий допустим best-effort: регистрация ключа,
	// финальное "signed" (там у нас уже есть запись в БД).
	_, _ = s.audit.Append(ctx, ev)
}
