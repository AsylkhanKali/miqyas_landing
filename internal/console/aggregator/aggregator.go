// Package aggregator склеивает данные из нескольких бэкендов в один
// view-model для UI. Параллельные вызовы — через errgroup.
package aggregator

import (
	"context"
	"time"

	"github.com/goszakup/platform/internal/console/clients"
)

type Aggregator struct {
	submission *clients.Submission
	document   *clients.Document
	esign      *clients.Esign
	audit      *clients.Audit
}

func New(s *clients.Submission, d *clients.Document, e *clients.Esign, a *clients.Audit) *Aggregator {
	return &Aggregator{submission: s, document: d, esign: e, audit: a}
}

// SubmissionView — полная картина одной подачи для UI: submission +
// transitions + связанный документ + последние audit-события.
type SubmissionView struct {
	Submission  clients.SubmissionDTO       `json:"submission"`
	Transitions []clients.TransitionDTO     `json:"transitions"`
	Document    *clients.DocumentDTO        `json:"document,omitempty"`
	Version     *clients.DocumentVersionDTO `json:"version,omitempty"`
	AuditEvents []clients.AuditEvent        `json:"audit_events"`
	UI          UIHints                     `json:"ui"`
}

// UIHints — вычисленные подсказки для рендера в UI, чтобы он не
// дублировал бизнес-правила.
type UIHints struct {
	UntilDeadlineSeconds int64    `json:"until_deadline_seconds"`
	InsideCutoffWindow   bool     `json:"inside_cutoff_window"`
	CutoffSeconds        int64    `json:"cutoff_seconds"`
	AllowedActions       []string `json:"allowed_actions"`
}

// CutoffBeforeDeadline дублирует значение из submission/domain.
// Намеренно не импортируем чужой пакет, чтобы BFF мог собираться
// независимо. При изменении правил это место обновляется руками.
const CutoffBeforeDeadline = 30 * time.Minute

func (a *Aggregator) GetSubmission(ctx context.Context, id, ownerForAudit string) (SubmissionView, error) {
	sub, transitions, err := a.submission.Get(ctx, id)
	if err != nil {
		return SubmissionView{}, err
	}

	view := SubmissionView{
		Submission:  sub,
		Transitions: transitions,
	}

	// Документ — best effort.
	if sub.DocumentID != "" {
		doc, ver, err := a.document.Get(ctx, sub.DocumentID)
		if err == nil {
			view.Document = &doc
			view.Version = &ver
		}
	}

	// Audit для конкретной подачи.
	events, err := a.audit.Recent(ctx, "submission:"+id, 20)
	if err == nil {
		view.AuditEvents = events
	}

	view.UI = computeHints(sub)
	return view, nil
}

// AvailableKeys — список ЭЦП-ключей оператора, отфильтрованный по статусу.
func (a *Aggregator) AvailableKeys(ctx context.Context, owner string) ([]clients.EsignKeyDTO, error) {
	keys, err := a.esign.ListByOwner(ctx, owner)
	if err != nil {
		return nil, err
	}
	out := make([]clients.EsignKeyDTO, 0, len(keys))
	now := time.Now().UTC()
	for _, k := range keys {
		if k.Status == "active" && k.CertNotAfter.After(now) {
			out = append(out, k)
		}
	}
	return out, nil
}

func computeHints(s clients.SubmissionDTO) UIHints {
	now := time.Now().UTC()
	until := s.DeadlineAt.Sub(now)
	inside := until > 0 && until <= CutoffBeforeDeadline

	allowed := allowedActionsFor(s.State, inside)
	return UIHints{
		UntilDeadlineSeconds: int64(until.Seconds()),
		InsideCutoffWindow:   inside,
		CutoffSeconds:        int64(CutoffBeforeDeadline.Seconds()),
		AllowedActions:       allowed,
	}
}

func allowedActionsFor(state string, insideCutoff bool) []string {
	switch state {
	case "draft":
		return []string{"review", "cancel"}
	case "reviewed":
		return []string{"sign", "cancel"}
	case "signed":
		if insideCutoff {
			return []string{"submit_with_ack", "cancel"}
		}
		return []string{"submit", "cancel"}
	case "submitted", "acknowledged":
		return []string{} // ничего не разрешено — ждём площадку / архивацию
	case "archived", "cancelled", "failed":
		return []string{}
	}
	return []string{}
}
