package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/platform/httpx"
	"github.com/goszakup/platform/internal/platform/metrics"
	"github.com/goszakup/platform/internal/tenderintel/goszakup"
)

type Deps struct {
	Log     *slog.Logger
	DB      *pgxpool.Pool
	Upstream *goszakup.Client
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
	r.Get("/api/v1/upstream/health", upstreamHealth(d))
	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
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

func upstreamHealth(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if err := d.Upstream.Health(ctx); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"upstream": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"upstream": "reachable"})
	}
}
