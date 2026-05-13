package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/audit/storage"
	"github.com/goszakup/platform/internal/platform/httpx"
	"github.com/goszakup/platform/internal/platform/metrics"
)

type Deps struct {
	Log         *slog.Logger
	DB          *pgxpool.Pool
	Repo        *storage.Repository
	IngestToken string
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(httpx.RequestLogger(d.Log))
	r.Use(metrics.Middleware)

	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(d))
	r.Get("/metrics", metrics.Handler())

	r.Route("/api/v1/events", func(r chi.Router) {
		r.With(requireBearer(d.IngestToken)).Post("/", appendEvent(d))
		r.Get("/", listEvents(d))
	})
	r.Get("/api/v1/verify", verifyChain(d))

	return r
}

// ── middleware ────────────────────────────────────────────────────────────

func requireBearer(token string) func(http.Handler) http.Handler {
	prefix := "Bearer "
	expected := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, prefix) {
				httpError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			got := []byte(strings.TrimPrefix(h, prefix))
			if subtle.ConstantTimeCompare(got, expected) != 1 {
				httpError(w, http.StatusUnauthorized, "invalid token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── handlers ──────────────────────────────────────────────────────────────

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := d.DB.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"db": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

type appendRequest struct {
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

type appendResponse struct {
	Seq        int64     `json:"seq"`
	OccurredAt time.Time `json:"occurred_at"`
	PrevHash   string    `json:"prev_hash"`
	Hash       string    `json:"hash"`
}

func appendEvent(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req appendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		stored, err := d.Repo.Append(r.Context(), storage.Event{
			OccurredAt:  req.OccurredAt,
			ActorType:   req.ActorType,
			ActorID:     req.ActorID,
			Action:      req.Action,
			Resource:    req.Resource,
			OrgID:       req.OrgID,
			TraceID:     req.TraceID,
			BeforeState: req.BeforeState,
			AfterState:  req.AfterState,
			Metadata:    req.Metadata,
		})
		if err != nil {
			d.Log.Error("audit append failed", "err", err)
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, appendResponse{
			Seq:        stored.Seq,
			OccurredAt: stored.OccurredAt,
			PrevHash:   hex.EncodeToString(stored.PrevHash),
			Hash:       hex.EncodeToString(stored.Hash),
		})
	}
}

func listEvents(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := storage.Query{
			Actor:    q.Get("actor"),
			Resource: q.Get("resource"),
			Action:   q.Get("action"),
			OrgID:    q.Get("org_id"),
		}
		if v := q.Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				query.Limit = n
			}
		}
		if v := q.Get("from"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				query.From = t
			}
		}
		if v := q.Get("to"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				query.To = t
			}
		}

		events, err := d.Repo.List(r.Context(), query)
		if err != nil {
			d.Log.Error("audit list failed", "err", err)
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		// Возвращаем hex-представление байтовых хэшей, чтобы JSON читался удобно.
		out := make([]map[string]any, 0, len(events))
		for _, e := range events {
			out = append(out, map[string]any{
				"seq":          e.Seq,
				"occurred_at":  e.OccurredAt,
				"actor_type":   e.ActorType,
				"actor_id":     e.ActorID,
				"action":       e.Action,
				"resource":     e.Resource,
				"org_id":       e.OrgID,
				"trace_id":     e.TraceID,
				"before_state": e.BeforeState,
				"after_state":  e.AfterState,
				"metadata":     e.Metadata,
				"prev_hash":    hex.EncodeToString(e.PrevHash),
				"hash":         hex.EncodeToString(e.Hash),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": out})
	}
}

func verifyChain(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Полный проход журнала может быть тяжёлым; ограничиваем 5 минутами.
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		badSeq, err := d.Repo.VerifyChain(ctx)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if badSeq != 0 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"valid":          false,
				"first_bad_seq":  badSeq,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"valid": true})
	}
}

// ── tiny helpers ──────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// errNotFound — заготовка для будущих эндпоинтов выборки по seq.
var errNotFound = errors.New("not found")

var _ = errNotFound
