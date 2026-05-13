// Package domain содержит доменные типы Document Service.
// Хранение содержимого — в S3-совместимом бакете; в БД лежат метаданные,
// JSON-payload и хэш для целостности.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// Template — шаблон документа (например, "ценовое предложение").
// Schema — JSON-схема, валидирующая Payload документа.
// Rules — дополнительные правила (например, "сумма > 0", "до дедлайна").
type Template struct {
	ID          uuid.UUID      `json:"id"`
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
	Rules       []Rule         `json:"rules"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// Rule — простое правило, применяемое к payload документа.
// Поддерживаемые типы: "required", "min_amount", "deadline_before".
type Rule struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params"`
}

// Document — экземпляр документа организации.
type Document struct {
	ID           uuid.UUID `json:"id"`
	OrgID        string    `json:"org_id"`
	TemplateCode string    `json:"template_code"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Version — иммутабельная версия документа.
type Version struct {
	ID            uuid.UUID         `json:"id"`
	DocumentID    uuid.UUID         `json:"document_id"`
	Version       int               `json:"version"`
	Payload       map[string]any    `json:"payload"`
	S3Bucket      string            `json:"s3_bucket"`
	S3Key         string            `json:"s3_key"`
	S3ETag        string            `json:"s3_etag"`
	ContentSHA256 []byte            `json:"content_sha256"`
	ContentSize   int64             `json:"content_size"`
	Validated     bool              `json:"validated"`
	Validation    *ValidationResult `json:"validation,omitempty"`
	CreatedBy     string            `json:"created_by"`
	CreatedAt     time.Time         `json:"created_at"`
}

// ValidationResult — итог валидации payload по schema + rules.
type ValidationResult struct {
	Valid  bool              `json:"valid"`
	Errors []ValidationError `json:"errors,omitempty"`
}

type ValidationError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

const (
	StatusDraft     = "draft"
	StatusValidated = "validated"
	StatusSigned    = "signed"
	StatusArchived  = "archived"
)
