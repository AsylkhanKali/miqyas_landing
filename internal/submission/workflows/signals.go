package workflows

import "github.com/google/uuid"

// Имена сигналов и query handlers в Temporal.
const (
	SignalReview = "review"
	SignalSign   = "sign"
	SignalSubmit = "submit"
	SignalCancel = "cancel"

	QueryState = "getState"
)

// Полезные нагрузки сигналов. Все они идут от человека-оператора —
// автоматическая отправка signals другим сервисом запрещена политикой.

type ReviewSignal struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason,omitempty"`
}

type SignSignal struct {
	Actor       string `json:"actor"`
	ESIGCertCN  string `json:"esig_cert_cn"` // common name сертификата ЭЦП оператора
	ESIGCertSHA string `json:"esig_cert_sha"`
	// Подпись применяется на стороне Operator Console / HSM Broker,
	// сюда приходит уже подтверждение факта подписи. Сам ключ не передаётся.
}

type SubmitSignal struct {
	Actor          string `json:"actor"`
	IdempotencyKey string `json:"idempotency_key"`
	// AcknowledgeCutoff — оператор подтвердил, что осознаёт правило
	// T-30 минут. Без этого флага сигнал в окне отсечки отклоняется.
	AcknowledgeCutoff bool `json:"acknowledge_cutoff"`
}

type CancelSignal struct {
	Actor  string `json:"actor"`
	Reason string `json:"reason"`
}

// StartParams — вход workflow.
type StartParams struct {
	SubmissionID    uuid.UUID `json:"submission_id"`
	OrgID           string    `json:"org_id"`
	TenderID        string    `json:"tender_id"`
	LotID           string    `json:"lot_id,omitempty"`
	Platform        string    `json:"platform"`
	DocumentID      uuid.UUID `json:"document_id"`
	DocumentVersion int       `json:"document_version"`
	DeadlineAt      string    `json:"deadline_at"` // RFC3339; время до отсечки считается от него
}

// Result — итог workflow.
type Result struct {
	FinalState string `json:"final_state"`
	ReceiptID  string `json:"receipt_id,omitempty"`
	Cancelled  bool   `json:"cancelled"`
	Reason     string `json:"reason,omitempty"`
}
