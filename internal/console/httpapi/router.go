package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/goszakup/platform/internal/console/aggregator"
	"github.com/goszakup/platform/internal/console/clients"
	"github.com/goszakup/platform/internal/platform/auth"
	"github.com/goszakup/platform/internal/platform/metrics"
	"github.com/goszakup/platform/internal/platform/httpx"
)

type Deps struct {
	Log         *slog.Logger
	Agg         *aggregator.Aggregator
	Submission  *clients.Submission
	Document    *clients.Document
	Esign       *clients.Esign
	JWKS        *auth.JWKSClient
	AllowOrigin string
}

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(httpx.RequestLogger(d.Log))
	r.Use(cors(d.AllowOrigin))
	r.Use(metrics.Middleware)

	r.Get("/healthz", healthz)
	r.Get("/metrics", metrics.Handler())

	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(d.JWKS))

		// Aggregated views
		r.Get("/api/v1/submissions/{id}", getSubmission(d))
		r.Get("/api/v1/keys", listKeys(d))
		r.Get("/api/v1/templates", listTemplates(d))

		// Прокси на submission service (UI общается только с BFF).
		r.With(auth.RequireRole("operator")).Post("/api/v1/submissions", proxySubmissionStart(d))
		r.With(auth.RequireRole("operator", "reviewer")).Post("/api/v1/submissions/{id}/{signal}", proxySubmissionSignal(d))

		// Прокси на esign service.
		r.With(auth.RequireRole("operator")).Post("/api/v1/sign", proxySign(d))
	})

	return r
}

// ── middleware ────────────────────────────────────────────────────────────

func cors(origin string) func(http.Handler) http.Handler {
	if origin == "" {
		origin = "*"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
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

func getSubmission(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		view, err := d.Agg.GetSubmission(ctx, id, "")
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func listKeys(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		if owner == "" {
			httpError(w, http.StatusBadRequest, "owner required")
			return
		}
		keys, err := d.Agg.AvailableKeys(r.Context(), owner)
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
	}
}

func listTemplates(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := d.Document.ListTemplates(r.Context())
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": out})
	}
}

func proxySubmissionStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		sub, err := d.Submission.Start(r.Context(), body)
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, sub)
	}
}

func proxySubmissionSignal(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		signal := chi.URLParam(r, "signal")
		var body any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := d.Submission.Signal(r.Context(), id, signal, body); err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"signal": signal})
	}
}

func proxySign(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req clients.SignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp, err := d.Esign.Sign(r.Context(), req)
		if err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
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
