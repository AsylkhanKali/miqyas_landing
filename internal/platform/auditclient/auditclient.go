// Package auditclient — тонкий HTTP-клиент к сервису audit.
// Используется бизнес-сервисами для отправки событий в неизменяемый журнал.
//
// Принципы:
//   - Аудит — обязательная часть бизнес-операции, не fire-and-forget.
//     Если запись не легла в журнал, операция считается несостоявшейся
//     (зависит от вызывающего: либо вернуть ошибку, либо ретраить).
//   - Клиент не глотает ошибки и не делает фоновую буферизацию: это
//     создавало бы окно потери событий.
package auditclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

type Event struct {
	OccurredAt  time.Time      `json:"occurred_at,omitempty"`
	ActorType   string         `json:"actor_type"`
	ActorID     string         `json:"actor_id"`
	Action      string         `json:"action"`
	Resource    string         `json:"resource"`
	OrgID       string         `json:"org_id,omitempty"`
	TraceID     string         `json:"trace_id,omitempty"`
	BeforeState map[string]any `json:"before_state,omitempty"`
	AfterState  map[string]any `json:"after_state,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type AppendResult struct {
	Seq      int64  `json:"seq"`
	PrevHash string `json:"prev_hash"`
	Hash     string `json:"hash"`
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string, timeout time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http: &http.Client{
			Timeout:   timeout,
			Transport: otelhttp.NewTransport(http.DefaultTransport),
		},
	}
}

// Append отправляет событие. TraceID автоматически подхватывается из ctx,
// если не задан явно.
func (c *Client) Append(ctx context.Context, ev Event) (AppendResult, error) {
	if ev.TraceID == "" {
		if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
			ev.TraceID = sc.TraceID().String()
		}
	}

	body, err := json.Marshal(ev)
	if err != nil {
		return AppendResult{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v1/events", bytes.NewReader(body))
	if err != nil {
		return AppendResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return AppendResult{}, fmt.Errorf("audit append: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		return AppendResult{}, errors.New("audit append status " + resp.Status + ": " + errResp.Error)
	}

	var out AppendResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AppendResult{}, err
	}
	return out, nil
}
