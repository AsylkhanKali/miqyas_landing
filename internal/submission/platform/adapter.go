// Package platform — адаптеры для подачи заявок на конкретные ЭТП.
//
// На уровне дизайна:
//   - Адаптер моделирует ТОЛЬКО легитимные пользовательские сценарии.
//   - Подача — финальное действие, выполняется ПОСЛЕ явного подтверждения
//     оператора и за пределами защитного окна T-30 минут до дедлайна.
//   - Реальные реализации (goszakup, samruk) живут в подпакетах и подключаются
//     по имени из конфига. Сейчас здесь только Stub — для разработки и
//     демонстрации потока без реальных вызовов площадки.
package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SubmitRequest — параметры подачи.
type SubmitRequest struct {
	OrgID           string
	TenderID        string
	LotID           string
	DocumentID      uuid.UUID
	DocumentVersion int
	S3Bucket        string
	S3Key           string
	IdempotencyKey  string // обязателен — для предотвращения двойной подачи
}

// SubmitResult — результат подачи (квитанция от площадки).
type SubmitResult struct {
	ReceiptID  string    `json:"receipt_id"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// Adapter — контракт интеграции с конкретной ЭТП.
type Adapter interface {
	Name() string
	Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error)
}

// ── Stub реализация ───────────────────────────────────────────────────────

// Stub — заглушка. Никуда ничего не отправляет, только формирует
// детерминированную квитанцию по IdempotencyKey. Подходит для локальной
// разработки и end-to-end тестов в стейдже.
type Stub struct{ Platform string }

func NewStub(platform string) *Stub { return &Stub{Platform: platform} }

func (s *Stub) Name() string { return s.Platform }

func (s *Stub) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	if req.IdempotencyKey == "" {
		return SubmitResult{}, fmt.Errorf("idempotency key is required")
	}
	receipt := fmt.Sprintf("STUB-%s-%s", strings.ToUpper(s.Platform), req.IdempotencyKey)
	return SubmitResult{
		ReceiptID:  receipt,
		AcceptedAt: time.Now().UTC(),
	}, nil
}

// Registry — выбор адаптера по имени площадки. В тестах удобно подменять.
type Registry struct {
	byName map[string]Adapter
}

func NewRegistry(adapters ...Adapter) *Registry {
	m := make(map[string]Adapter, len(adapters))
	for _, a := range adapters {
		m[a.Name()] = a
	}
	return &Registry{byName: m}
}

func (r *Registry) Get(name string) (Adapter, error) {
	a, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("no adapter registered for platform %q", name)
	}
	return a, nil
}
