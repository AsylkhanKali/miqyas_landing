package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/goszakup/platform/internal/identity/jwtkey"
	"github.com/goszakup/platform/internal/identity/service"
	"github.com/goszakup/platform/internal/identity/storage"
	"github.com/goszakup/platform/internal/platform/httpx"
	"github.com/goszakup/platform/internal/platform/metrics"
)

type Deps struct {
	Log    *slog.Logger
	DB     *pgxpool.Pool
	Svc    *service.Service
	Issuer *jwtkey.Issuer
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

	// Публичный JWKS — без аутентификации, по дизайну.
	r.Get("/.well-known/jwks.json", jwks(d))
	// Discovery (subset of OIDC): пригождается клиентам.
	r.Get("/.well-known/openid-configuration", discovery(d))

	r.Route("/api/v1/auth", func(r chi.Router) {
		r.Post("/register", registerHandler(d))
		r.Post("/login", loginHandler(d))
		// Требуют access-токена (валидируем локально через issuer.Public()).
		r.Group(func(r chi.Router) {
			r.Use(requireSelfAuth(d))
			r.Post("/enroll-totp", enrollTOTP(d))
			r.Post("/confirm-totp", confirmTOTP(d))
			r.Get("/me", me(d))
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

func jwks(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, http.StatusOK, d.Issuer.JWKS())
	}
}

func discovery(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                base,
			"jwks_uri":                              base + "/.well-known/jwks.json",
			"token_endpoint":                        base + "/api/v1/auth/login",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	}
}

type registerRequest struct {
	Email    string   `json:"email"`
	FullName string   `json:"full_name"`
	OrgID    string   `json:"org_id"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
}

func registerHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		u, err := d.Svc.Register(r.Context(), service.RegisterInput{
			Email: req.Email, FullName: req.FullName, OrgID: req.OrgID,
			Password: req.Password, Roles: req.Roles,
		})
		if errors.Is(err, storage.ErrConflict) {
			httpError(w, http.StatusConflict, "email already taken")
			return
		}
		if err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, u)
	}
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	TOTPCode string `json:"totp_code"`
}

func loginHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := d.Svc.Login(r.Context(), service.LoginInput{
			Email: req.Email, Password: req.Password, TOTPCode: req.TOTPCode,
			IP: r.RemoteAddr, UserAgent: r.UserAgent(),
		})
		switch {
		case errors.Is(err, service.ErrMFANotEnrolled):
			httpError(w, http.StatusForbidden, "mfa not enrolled; enroll first")
			return
		case errors.Is(err, service.ErrMFARequired):
			httpError(w, http.StatusUnauthorized, "totp_code required")
			return
		case errors.Is(err, service.ErrInvalidCredentials):
			httpError(w, http.StatusUnauthorized, "invalid credentials")
			return
		case err != nil:
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

func enrollTOTP(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := userIDFromCtx(r.Context())
		out, err := d.Svc.EnrollTOTP(r.Context(), uid)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"otpauth_url": out.OTPAuthURL,
			"base32":      out.Base32,
		})
	}
}

type confirmTOTPRequest struct {
	Code string `json:"code"`
}

func confirmTOTP(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req confirmTOTPRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		uid := userIDFromCtx(r.Context())
		if err := d.Svc.ConfirmTOTP(r.Context(), uid, req.Code); err != nil {
			httpError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "enrolled"})
	}
}

func me(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := userIDFromCtx(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"user_id": uid.String()})
	}
}

// ── local middleware (валидация собственного токена через issuer.Public()) ────

type ctxKey int

const userIDKey ctxKey = 1

func requireSelfAuth(d Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				httpError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			token := strings.TrimPrefix(h, "Bearer ")
			claims, err := d.Svc.SelfFromAccess(token)
			if err != nil {
				httpError(w, http.StatusUnauthorized, err.Error())
				return
			}
			ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func userIDFromCtx(ctx context.Context) uuid.UUID {
	v, _ := ctx.Value(userIDKey).(uuid.UUID)
	return v
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
