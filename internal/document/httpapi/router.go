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

	"github.com/goszakup/platform/internal/document/domain"
	"github.com/goszakup/platform/internal/document/service"
	"github.com/goszakup/platform/internal/document/storage"
	"github.com/goszakup/platform/internal/platform/auth"
	"github.com/goszakup/platform/internal/platform/httpx"
	"github.com/goszakup/platform/internal/platform/metrics"
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

		r.Route("/api/v1/templates", func(r chi.Router) {
			r.Get("/", listTemplates(d))
			r.Get("/{code}", getTemplate(d))
			r.Post("/{code}/validate", validatePayload(d))
			r.With(auth.RequireRole("admin", "operator")).Put("/", upsertTemplate(d))
		})

		r.Route("/api/v1/documents", func(r chi.Router) {
			r.With(auth.RequireRole("operator")).Post("/", createDocument(d))
			r.Get("/{id}", getDocument(d))
		})
	})

	return r
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

func listTemplates(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := d.Svc.ListTemplates(r.Context())
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": out})
	}
}

type upsertTemplateRequest struct {
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
	Rules       []domain.Rule  `json:"rules"`
	Actor       string         `json:"actor"` // временно, до OIDC
}

func upsertTemplate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req upsertTemplateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if req.Code == "" || req.Name == "" {
			httpError(w, http.StatusBadRequest, "code and name are required")
			return
		}
		t, err := d.Svc.UpsertTemplate(r.Context(), domain.Template{
			Code:        req.Code,
			Name:        req.Name,
			Description: req.Description,
			Schema:      req.Schema,
			Rules:       req.Rules,
		}, defaultActor(req.Actor))
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

func getTemplate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := chi.URLParam(r, "code")
		t, err := d.Svc.GetTemplate(r.Context(), code)
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "template not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, t)
	}
}

type validateRequest struct {
	Payload map[string]any `json:"payload"`
}

func validatePayload(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := chi.URLParam(r, "code")
		var req validateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		res, err := d.Svc.ValidateInput(r.Context(), code, req.Payload)
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "template not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

type createDocumentRequest struct {
	OrgID        string         `json:"org_id"`
	TemplateCode string         `json:"template_code"`
	Title        string         `json:"title"`
	CreatedBy    string         `json:"created_by"`
	Payload      map[string]any `json:"payload"`
}

func createDocument(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createDocumentRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if req.OrgID == "" || req.TemplateCode == "" || req.Title == "" || req.CreatedBy == "" {
			httpError(w, http.StatusBadRequest, "org_id, template_code, title, created_by are required")
			return
		}
		doc, ver, err := d.Svc.CreateDocument(r.Context(), service.CreateDocumentInput{
			OrgID:        req.OrgID,
			TemplateCode: req.TemplateCode,
			Title:        req.Title,
			CreatedBy:    req.CreatedBy,
			Payload:      req.Payload,
		})
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "template not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"document": doc,
			"version":  ver,
		})
	}
}

func getDocument(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			httpError(w, http.StatusBadRequest, "invalid id")
			return
		}
		doc, ver, err := d.Svc.GetDocument(r.Context(), id)
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "document not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"document": doc,
			"version":  ver,
		})
	}
}

// ── tiny helpers ──────────────────────────────────────────────────────────

func defaultActor(a string) string {
	if a == "" {
		return "system"
	}
	return a
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
