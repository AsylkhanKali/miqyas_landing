package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/esign/domain"
	"github.com/goszakup/platform/internal/esign/service"
	"github.com/goszakup/platform/internal/esign/storage"
	"github.com/goszakup/platform/internal/platform/auth"
	"github.com/goszakup/platform/internal/platform/metrics"
	"github.com/goszakup/platform/internal/platform/httpx"
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

		r.Route("/api/v1/keys", func(r chi.Router) {
			r.With(auth.RequireRole("operator")).Post("/", registerKey(d))
			r.Get("/", listKeys(d))
			r.Get("/{id}", getKey(d))
		})

		r.With(auth.RequireRole("operator")).Post("/api/v1/sign", signHandler(d))
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

type registerRequest struct {
	OrgID     string         `json:"org_id"`
	Owner     string         `json:"owner"`
	SubjectCN string         `json:"subject_cn"`
	KeySize   int            `json:"key_size,omitempty"`
	Backend   domain.Backend `json:"backend,omitempty"`
}

func registerKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		if req.OrgID == "" || req.Owner == "" || req.SubjectCN == "" {
			httpError(w, http.StatusBadRequest, "org_id, owner, subject_cn required")
			return
		}
		key, err := d.Svc.Register(r.Context(), service.RegisterInput{
			OrgID: req.OrgID, Owner: req.Owner, SubjectCN: req.SubjectCN,
			KeySize: req.KeySize, Backend: req.Backend,
		})
		if errors.Is(err, storage.ErrConflict) {
			httpError(w, http.StatusConflict, "key already registered")
			return
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, keyDTO(key))
	}
}

func listKeys(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner := r.URL.Query().Get("owner")
		if owner == "" {
			httpError(w, http.StatusBadRequest, "owner query param is required")
			return
		}
		keys, err := d.Svc.ListByOwner(r.Context(), owner)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, keyDTO(k))
		}
		writeJSON(w, http.StatusOK, map[string]any{"keys": out})
	}
}

func getKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.Parse(chi.URLParam(r, "id"))
		if err != nil {
			httpError(w, http.StatusBadRequest, "invalid id")
			return
		}
		k, err := d.Svc.Get(r.Context(), id)
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "key not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, keyDTO(k))
	}
}

type signRequest struct {
	KeyID     uuid.UUID `json:"key_id"`
	Actor     string    `json:"actor"`
	Purpose   string    `json:"purpose"`
	DataBase64 string   `json:"data_base64"`
}

func signHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req signRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
		data, err := base64.StdEncoding.DecodeString(req.DataBase64)
		if err != nil {
			httpError(w, http.StatusBadRequest, "data_base64: "+err.Error())
			return
		}
		res, err := d.Svc.Sign(r.Context(), service.SignInput{
			KeyID:   req.KeyID,
			Actor:   req.Actor,
			Purpose: req.Purpose,
			Data:    data,
		})
		if errors.Is(err, storage.ErrNotFound) {
			httpError(w, http.StatusNotFound, "key not found")
			return
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"key_id":           res.KeyID,
			"algorithm":        res.Algorithm,
			"signature_base64": base64.StdEncoding.EncodeToString(res.Signature),
			"signed_at":        res.SignedAt,
		})
	}
}

// keyDTO — экспортируемое представление ключа. CertPEM возвращается, но
// никакая информация о приватном ключе не утекает наружу (поле BackendRef
// сознательно ОПУСКАЕМ — его знание увеличивает blast-radius при инциденте).
func keyDTO(k domain.Key) map[string]any {
	return map[string]any{
		"id":              k.ID,
		"org_id":          k.OrgID,
		"owner":           k.Owner,
		"cert_subject_cn": k.CertSubjectCN,
		"cert_serial":     k.CertSerial,
		"cert_not_before": k.CertNotBefore,
		"cert_not_after":  k.CertNotAfter,
		"cert_sha256":     base64.StdEncoding.EncodeToString(k.CertSHA256),
		"cert_pem":        string(k.CertPEM),
		"backend":         k.Backend,
		"algorithm":       k.Algorithm,
		"status":          k.Status,
		"created_at":      k.CreatedAt,
		"updated_at":      k.UpdatedAt,
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
