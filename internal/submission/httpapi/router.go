package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/platform/auth"
	"github.com/goszakup/platform/internal/platform/metrics"
	"github.com/goszakup/platform/internal/platform/httpx"
	"github.com/goszakup/platform/internal/submission/service"
	"github.com/goszakup/platform/internal/submission/storage"
	"github.com/goszakup/platform/internal/submission/workflows"
)

type Deps struct {
	Log  *slog.Logger
	DB   *pgxpool.Pool
	Svc  *service.Service
	JWKS *auth.JWKSClient
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

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(d.JWKS))

		r.Route("/api/v1/submissions", func(r chi.Router) {
			r.With(auth.RequireRole("operator")).Post("/", startSubmission(d))
			r.Get("/{id}", getSubmission(d))
			r.With(auth.RequireRole("reviewer", "operator")).Post("/{id}/review", signal[workflows.ReviewSignal](d, workflows.SignalReview))
			r.With(auth.RequireRole("operator")).Post("/{id}/sign", signal[workflows.SignSignal](d, workflows.SignalSign))
			r.With(auth.RequireRole("operator")).Post("/{id}/submit", signal[workflows.SubmitSignal](d, workflows.SignalSubmit))
			r.With(auth.RequireRole("operator")).Post("/{id}/cancel", signal[workflows.CancelSignal](d, workflows.SignalCancel))
		})
	})
	return r
}

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

type startRequest struct {
	OrgID           string    `json:"org_id"`
	TenderID        string    `json:"tender_id"`
	LotID           string    `json:"lot_id,omitempty"`
	Platform        string    `json:"platform"`
	DocumentID      uuid.UUID `json:"document_id"`
	DocumentVersion int       `json:"document_version"`
	DeadlineAt      time.Time `json:"deadline_at"`
	CreatedBy       string    `json:"created_by"`
}

func startSubmission(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req startRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if req.OrgID == "" || req.TenderID == "" || req.Platform == "" ||
			req.DocumentID == uuid.Nil || req.DocumentVersion == 0 ||
			req.CreatedBy == "" || req.DeadlineAt.IsZero() {
			httpError(w, http.StatusBadRequest, "missing required fields")
			return
		}
		sub, err := d.Svc.Start(r.Context(), service.StartInput{
			OrgID:           req.OrgID,
			TenderID:        req.TenderID,
			LotID:           req.LotID,
			Platform:        req.Platform,
			DocumentID:      req.DocumentID,
			DocumentVersion: req.DocumentVersion,
			DeadlineAt:      req.DeadlineAt,
			CreatedBy:       req.CreatedBy,
		})
		switch {
		case errors.Is(err, storage.ErrConflict):
			httpError(w, http.StatusConflict, "submission already exists for this lot")
			return
		case err != nil:
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sub)
	}
}

func getSubmission(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			httpError(w, http.StatusBadRequest, "invalid id")
			return
		}
		sub, transitions, err := d.Svc.Get(r.Context(), id)
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "submission not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"submission":  sub,
			"transitions": transitions,
		})
	}
}

// signal — generic-обработчик: парсит payload в T и шлёт в Temporal.
func signal[T any](d Deps, signalName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			httpError(w, http.StatusBadRequest, "invalid id")
			return
		}
		var payload T
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if err := d.Svc.Signal(r.Context(), id, signalName, payload); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				httpError(w, http.StatusNotFound, "submission not found")
				return
			}
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"signal": signalName})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
