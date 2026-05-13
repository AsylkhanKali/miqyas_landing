// Package service содержит use cases Document Service.
// Связывает репозиторий, S3, валидатор и аудит-журнал.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/goszakup/platform/internal/document/domain"
	"github.com/goszakup/platform/internal/document/s3store"
	"github.com/goszakup/platform/internal/document/storage"
	"github.com/goszakup/platform/internal/document/validator"
	"github.com/goszakup/platform/internal/platform/auditclient"
)

type Service struct {
	repo  *storage.Repository
	s3    *s3store.Client
	val   *validator.Validator
	audit *auditclient.Client
}

func New(repo *storage.Repository, s3 *s3store.Client, val *validator.Validator, audit *auditclient.Client) *Service {
	return &Service{repo: repo, s3: s3, val: val, audit: audit}
}

// ── Templates ─────────────────────────────────────────────────────────────

func (s *Service) UpsertTemplate(ctx context.Context, t domain.Template, actor string) (domain.Template, error) {
	out, err := s.repo.UpsertTemplate(ctx, t)
	if err != nil {
		return out, err
	}
	s.emitAudit(ctx, actor, "document.template.upserted", "document-template:"+out.Code, map[string]any{
		"name": out.Name,
	})
	return out, nil
}

func (s *Service) GetTemplate(ctx context.Context, code string) (domain.Template, error) {
	return s.repo.GetTemplate(ctx, code)
}

func (s *Service) ListTemplates(ctx context.Context) ([]domain.Template, error) {
	return s.repo.ListTemplates(ctx)
}

// ── Documents ─────────────────────────────────────────────────────────────

type CreateDocumentInput struct {
	OrgID        string
	TemplateCode string
	Title        string
	CreatedBy    string
	Payload      map[string]any
}

// CreateDocument создаёт документ + первую версию.
// Pipeline: get template → validate payload → put to S3 → insert document & version → audit.
// Документ переходит в статус validated, если payload прошёл валидацию; иначе остаётся draft.
func (s *Service) CreateDocument(ctx context.Context, in CreateDocumentInput) (domain.Document, domain.Version, error) {
	tmpl, err := s.repo.GetTemplate(ctx, in.TemplateCode)
	if err != nil {
		return domain.Document{}, domain.Version{}, fmt.Errorf("template %q: %w", in.TemplateCode, err)
	}

	valRes, err := s.val.Validate(tmpl, in.Payload)
	if err != nil {
		return domain.Document{}, domain.Version{}, fmt.Errorf("validate: %w", err)
	}

	body, err := json.MarshalIndent(in.Payload, "", "  ")
	if err != nil {
		return domain.Document{}, domain.Version{}, err
	}

	docID := uuid.New()
	key := fmt.Sprintf("%s/%s/%s/v1-%d.json", in.OrgID, in.TemplateCode, docID, time.Now().Unix())
	put, err := s.s3.Put(ctx, key, body, "application/json")
	if err != nil {
		return domain.Document{}, domain.Version{}, fmt.Errorf("s3 put: %w", err)
	}

	status := domain.StatusDraft
	if valRes.Valid {
		status = domain.StatusValidated
	}

	doc, err := s.repo.CreateDocument(ctx, domain.Document{
		ID:           docID,
		OrgID:        in.OrgID,
		TemplateCode: in.TemplateCode,
		Title:        in.Title,
		Status:       status,
		CreatedBy:    in.CreatedBy,
	})
	if err != nil {
		return domain.Document{}, domain.Version{}, fmt.Errorf("insert document: %w", err)
	}

	ver, err := s.repo.AddVersion(ctx, domain.Version{
		DocumentID:    doc.ID,
		Payload:       in.Payload,
		S3Bucket:      put.Bucket,
		S3Key:         put.Key,
		S3ETag:        put.ETag,
		ContentSHA256: put.SHA256,
		ContentSize:   put.Size,
		Validated:     valRes.Valid,
		Validation:    &valRes,
		CreatedBy:     in.CreatedBy,
	})
	if err != nil {
		return doc, domain.Version{}, fmt.Errorf("add version: %w", err)
	}

	s.emitAudit(ctx, in.CreatedBy, "document.created", "document:"+doc.ID.String(), map[string]any{
		"template": in.TemplateCode,
		"valid":    valRes.Valid,
		"version":  ver.Version,
		"size":     put.Size,
	})

	return doc, ver, nil
}

func (s *Service) GetDocument(ctx context.Context, id uuid.UUID) (domain.Document, domain.Version, error) {
	doc, err := s.repo.GetDocument(ctx, id)
	if err != nil {
		return domain.Document{}, domain.Version{}, err
	}
	ver, err := s.repo.LatestVersion(ctx, id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return doc, domain.Version{}, err
	}
	return doc, ver, nil
}

// ValidateInput — валидация произвольного payload против шаблона (без записи в БД).
func (s *Service) ValidateInput(ctx context.Context, templateCode string, payload map[string]any) (domain.ValidationResult, error) {
	tmpl, err := s.repo.GetTemplate(ctx, templateCode)
	if err != nil {
		return domain.ValidationResult{}, err
	}
	return s.val.Validate(tmpl, payload)
}

// ── helpers ───────────────────────────────────────────────────────────────

func (s *Service) emitAudit(ctx context.Context, actor, action, resource string, meta map[string]any) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Append(ctx, auditclient.Event{
		ActorType: actorType(actor),
		ActorID:   actor,
		Action:    action,
		Resource:  resource,
		Metadata:  meta,
	}); err != nil {
		// Audit не должен останавливать бизнес-операцию для второстепенных
		// действий (создание шаблона, валидация). Для подписания / финальной
		// подачи это станет блокирующим (отдельный путь).
		_ = err
	}
}

func actorType(actor string) string {
	if actor == "system" || actor == "" {
		return "system"
	}
	return "operator"
}
