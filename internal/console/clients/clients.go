// Package clients содержит типизированные HTTP-клиенты ко всем
// внутренним бэкендам платформы. Это единственное место в console-BFF,
// которое знает URL'ы и формат ответов конкретных сервисов.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// httpDo — общая обёртка с otelhttp transport.
func newHTTP(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport),
	}
}

func doJSON(ctx context.Context, c *http.Client, method, url string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: %s: %s", method, url, resp.Status, string(buf))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── Audit ─────────────────────────────────────────────────────────────────

type Audit struct {
	base string
	http *http.Client
}

func NewAudit(base string, timeout time.Duration) *Audit {
	return &Audit{base: base, http: newHTTP(timeout)}
}

type AuditEvent struct {
	Seq        int64          `json:"seq"`
	OccurredAt time.Time      `json:"occurred_at"`
	ActorType  string         `json:"actor_type"`
	ActorID    string         `json:"actor_id"`
	Action     string         `json:"action"`
	Resource   string         `json:"resource"`
	Metadata   map[string]any `json:"metadata"`
	TraceID    string         `json:"trace_id"`
}

func (c *Audit) Recent(ctx context.Context, resource string, limit int) ([]AuditEvent, error) {
	url := fmt.Sprintf("%s/api/v1/events?limit=%d", c.base, limit)
	if resource != "" {
		url += "&resource=" + resource
	}
	var resp struct{ Events []AuditEvent `json:"events"` }
	if err := doJSON(ctx, c.http, http.MethodGet, url, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Events, nil
}

// ── Submission ────────────────────────────────────────────────────────────

type Submission struct {
	base string
	http *http.Client
}

func NewSubmission(base string, timeout time.Duration) *Submission {
	return &Submission{base: base, http: newHTTP(timeout)}
}

type SubmissionDTO struct {
	ID              string    `json:"id"`
	OrgID           string    `json:"org_id"`
	TenderID        string    `json:"tender_id"`
	Platform        string    `json:"platform"`
	State           string    `json:"state"`
	DeadlineAt      time.Time `json:"deadline_at"`
	DocumentID      string    `json:"document_id"`
	DocumentVersion int       `json:"document_version"`
	WorkflowID      string    `json:"workflow_id"`
	RunID           string    `json:"run_id"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type TransitionDTO struct {
	FromState  string         `json:"from_state"`
	ToState    string         `json:"to_state"`
	Actor      string         `json:"actor"`
	Reason     string         `json:"reason"`
	OccurredAt time.Time      `json:"occurred_at"`
	Metadata   map[string]any `json:"metadata"`
}

func (c *Submission) Get(ctx context.Context, id string) (SubmissionDTO, []TransitionDTO, error) {
	var resp struct {
		Submission  SubmissionDTO    `json:"submission"`
		Transitions []TransitionDTO  `json:"transitions"`
	}
	url := fmt.Sprintf("%s/api/v1/submissions/%s", c.base, id)
	if err := doJSON(ctx, c.http, http.MethodGet, url, nil, &resp); err != nil {
		return SubmissionDTO{}, nil, err
	}
	return resp.Submission, resp.Transitions, nil
}

func (c *Submission) Signal(ctx context.Context, id, name string, payload any) error {
	url := fmt.Sprintf("%s/api/v1/submissions/%s/%s", c.base, id, name)
	return doJSON(ctx, c.http, http.MethodPost, url, payload, nil)
}

func (c *Submission) Start(ctx context.Context, body any) (SubmissionDTO, error) {
	var out SubmissionDTO
	err := doJSON(ctx, c.http, http.MethodPost, c.base+"/api/v1/submissions", body, &out)
	return out, err
}

// ── Document ──────────────────────────────────────────────────────────────

type Document struct {
	base string
	http *http.Client
}

func NewDocument(base string, timeout time.Duration) *Document {
	return &Document{base: base, http: newHTTP(timeout)}
}

type DocumentDTO struct {
	ID           string    `json:"id"`
	OrgID        string    `json:"org_id"`
	TemplateCode string    `json:"template_code"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`
}

type DocumentVersionDTO struct {
	Version     int       `json:"version"`
	Validated   bool      `json:"validated"`
	ContentSize int64     `json:"content_size"`
	S3Key       string    `json:"s3_key"`
	CreatedAt   time.Time `json:"created_at"`
}

func (c *Document) Get(ctx context.Context, id string) (DocumentDTO, DocumentVersionDTO, error) {
	var resp struct {
		Document DocumentDTO        `json:"document"`
		Version  DocumentVersionDTO `json:"version"`
	}
	url := fmt.Sprintf("%s/api/v1/documents/%s", c.base, id)
	err := doJSON(ctx, c.http, http.MethodGet, url, nil, &resp)
	return resp.Document, resp.Version, err
}

func (c *Document) ListTemplates(ctx context.Context) ([]map[string]any, error) {
	var resp struct{ Templates []map[string]any `json:"templates"` }
	err := doJSON(ctx, c.http, http.MethodGet, c.base+"/api/v1/templates", nil, &resp)
	return resp.Templates, err
}

// ── Esign ─────────────────────────────────────────────────────────────────

type Esign struct {
	base string
	http *http.Client
}

func NewEsign(base string, timeout time.Duration) *Esign {
	return &Esign{base: base, http: newHTTP(timeout)}
}

type EsignKeyDTO struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	Owner         string    `json:"owner"`
	CertSubjectCN string    `json:"cert_subject_cn"`
	CertNotAfter  time.Time `json:"cert_not_after"`
	Algorithm     string    `json:"algorithm"`
	Status        string    `json:"status"`
}

func (c *Esign) ListByOwner(ctx context.Context, owner string) ([]EsignKeyDTO, error) {
	var resp struct{ Keys []EsignKeyDTO `json:"keys"` }
	url := fmt.Sprintf("%s/api/v1/keys?owner=%s", c.base, owner)
	err := doJSON(ctx, c.http, http.MethodGet, url, nil, &resp)
	return resp.Keys, err
}

// SignRequest — пара DTO для подписи (передаём как есть).
type SignRequest struct {
	KeyID      string `json:"key_id"`
	Actor      string `json:"actor"`
	Purpose    string `json:"purpose"`
	DataBase64 string `json:"data_base64"`
}

type SignResponse struct {
	KeyID           string    `json:"key_id"`
	Algorithm       string    `json:"algorithm"`
	SignatureBase64 string    `json:"signature_base64"`
	SignedAt        time.Time `json:"signed_at"`
}

func (c *Esign) Sign(ctx context.Context, req SignRequest) (SignResponse, error) {
	var out SignResponse
	err := doJSON(ctx, c.http, http.MethodPost, c.base+"/api/v1/sign", req, &out)
	return out, err
}
