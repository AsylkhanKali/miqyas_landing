package workflows

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/goszakup/platform/internal/platform/auditclient"
	"github.com/goszakup/platform/internal/tenderintel/goszakup"
	"github.com/goszakup/platform/internal/tenderintel/storage"
)

// FetchPageParams — параметры активности получения страницы.
type FetchPageParams struct {
	Page  int
	Limit int
}

// FetchPageResult — результат активности получения страницы.
type FetchPageResult struct {
	Items   []map[string]any
	HasMore bool
}

// UpsertParams — параметры активности записи в БД.
type UpsertParams struct {
	Items []map[string]any
}

// UpsertResult — результат активности записи.
type UpsertResult struct {
	Saved int
}

// Activities хранит зависимости для Temporal activities.
// Регистрируется в воркере как ресивер.
type Activities struct {
	Client *goszakup.Client
	Repo   *storage.Repository
	Audit  *auditclient.Client
}

// AuditParams — параметры активности отправки события в журнал аудита.
type AuditParams struct {
	Action   string
	Resource string
	Metadata map[string]any
}

// EmitAuditActivity отправляет событие в audit-сервис.
// Вынесено в отдельную активность, поскольку workflow не должен делать
// сетевой ввод-вывод напрямую (Temporal требование детерминизма).
func (a *Activities) EmitAuditActivity(ctx context.Context, p AuditParams) error {
	if a.Audit == nil {
		return nil // в тестах без аудита
	}
	_, err := a.Audit.Append(ctx, auditclient.Event{
		ActorType: "service",
		ActorID:   "tender-intel-worker",
		Action:    p.Action,
		Resource:  p.Resource,
		Metadata:  p.Metadata,
	})
	return err
}

// FetchTendersPageActivity — получает одну страницу из публичного API goszakup.
// Работает только через задокументированные open-data эндпоинты, только чтение.
func (a *Activities) FetchTendersPageActivity(ctx context.Context, params FetchPageParams) (FetchPageResult, error) {
	log := activity.GetLogger(ctx)

	q := url.Values{}
	q.Set("page", strconv.Itoa(params.Page+1)) // API нумерует с 1
	q.Set("limit", strconv.Itoa(params.Limit))

	// Публичный открытый эндпоинт v3 goszakup open data.
	path := "/v3/tender?" + q.Encode()

	var resp struct {
		Total int              `json:"total"`
		Items []map[string]any `json:"items"`
	}
	if err := a.Client.FetchJSON(ctx, path, &resp); err != nil {
		return FetchPageResult{}, fmt.Errorf("fetch page %d: %w", params.Page, err)
	}

	fetched := len(resp.Items)
	offset := params.Page * params.Limit
	hasMore := offset+fetched < resp.Total

	log.Info("fetched tenders page",
		"page", params.Page,
		"fetched", fetched,
		"total", resp.Total,
		"hasMore", hasMore,
	)

	return FetchPageResult{Items: resp.Items, HasMore: hasMore}, nil
}

// UpsertTendersActivity — нормализует и сохраняет пачку закупок в postgres.
func (a *Activities) UpsertTendersActivity(ctx context.Context, params UpsertParams) (UpsertResult, error) {
	tenders := make([]storage.Tender, 0, len(params.Items))
	for _, raw := range params.Items {
		t, err := normalizeTender(raw)
		if err != nil {
			activity.GetLogger(ctx).Warn("normalize tender failed", "err", err, "id", raw["id"])
			continue
		}
		tenders = append(tenders, t)
	}
	if err := a.Repo.UpsertTenders(ctx, tenders); err != nil {
		return UpsertResult{}, fmt.Errorf("upsert: %w", err)
	}
	return UpsertResult{Saved: len(tenders)}, nil
}

// normalizeTender — преобразует сырой JSON-объект из API в структуру storage.Tender.
// Поля маппятся по именам из v3 open-data API goszakup.
func normalizeTender(raw map[string]any) (storage.Tender, error) {
	id, _ := stringField(raw, "id")
	if id == "" {
		return storage.Tender{}, fmt.Errorf("missing id")
	}
	t := storage.Tender{
		ExternalID:   id,
		Status:       stringOrEmpty(raw, "status_ru"),
		Title:        stringOrEmpty(raw, "name_ru"),
		OrganizerBIN: stringOrEmpty(raw, "organizer_bin"),
		Payload:      raw,
	}
	t.PublishDate = parseAPITime(raw, "publish_date")
	t.DeadlineAt = parseAPITime(raw, "end_date")
	return t, nil
}

func stringOrEmpty(m map[string]any, key string) string {
	v, _ := stringField(m, key)
	return v
}

func stringField(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return "", false
	}
	switch s := v.(type) {
	case string:
		return s, true
	case json.Number:
		return s.String(), true
	default:
		return fmt.Sprintf("%v", v), true
	}
}

func parseAPITime(m map[string]any, key string) *time.Time {
	s, ok := stringField(m, key)
	if !ok || s == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}
