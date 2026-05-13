// Package auth — общая HTTP-middleware для валидации JWT через JWKS.
// Используется всеми сервисами платформы, кроме identity (тот валидирует
// свои же токены локально).
//
// Принципы:
//   - JWKS кешируется на 5 минут с фоновым обновлением.
//   - Поддерживается только RS256 (явно). Алгоритм проверяется на каждом
//     токене — защита от alg=none и подмены.
//   - Claims извлекаются в request-context; downstream-хендлеры читают
//     ClaimsFromContext().
//   - При недоступности identity-сервиса лучше отказать, чем пропустить —
//     middleware возвращает 503.
package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type Claims struct {
	UserID uuid.UUID
	Email  string
	OrgID  string
	Roles  []string
}

func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

type ctxKey int

const claimsKey ctxKey = 1

func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

func ClaimsFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey).(Claims)
	return c, ok
}

// ── JWKS client ───────────────────────────────────────────────────────────

type jwk struct {
	Kty, Use, Kid, Alg, N, E string
}

type JWKSClient struct {
	url    string
	http   *http.Client
	mu     sync.RWMutex
	keys   map[string]*rsa.PublicKey
	cached time.Time
	ttl    time.Duration
}

func NewJWKSClient(jwksURL string, ttl time.Duration) *JWKSClient {
	return &JWKSClient{
		url:  jwksURL,
		http: &http.Client{Timeout: 5 * time.Second},
		keys: map[string]*rsa.PublicKey{},
		ttl:  ttl,
	}
}

func (c *JWKSClient) Key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	k, ok := c.keys[kid]
	fresh := time.Since(c.cached) < c.ttl
	c.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("kid %q not found in JWKS", kid)
}

func (c *JWKSClient) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("jwks fetch: %s: %s", resp.Status, string(buf))
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return err
	}
	out := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || k.Alg != "RS256" {
			continue
		}
		nB, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eB, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nB),
			E: int(new(big.Int).SetBytes(eB).Int64()),
		}
	}
	c.mu.Lock()
	c.keys = out
	c.cached = time.Now()
	c.mu.Unlock()
	return nil
}

// ── Middleware ────────────────────────────────────────────────────────────

func Middleware(jwks *JWKSClient) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := verify(r.Context(), r.Header.Get("Authorization"), jwks)
			if err != nil {
				status := http.StatusUnauthorized
				if errors.Is(err, errJWKSUnavailable) {
					status = http.StatusServiceUnavailable
				}
				http.Error(w, err.Error(), status)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithClaims(r.Context(), c)))
		})
	}
}

// RequireRole — middleware, требующий хотя бы одну из ролей.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := ClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, "unauthenticated", http.StatusUnauthorized)
				return
			}
			for _, want := range roles {
				if c.HasRole(want) {
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		})
	}
}

var errJWKSUnavailable = errors.New("jwks unavailable")

func verify(ctx context.Context, authHeader string, jwks *JWKSClient) (Claims, error) {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return Claims{}, errors.New("missing bearer token")
	}
	tok := strings.TrimPrefix(authHeader, "Bearer ")

	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodRS256 {
			return nil, fmt.Errorf("unexpected algorithm: %v", t.Method)
		}
		kid, _ := t.Header["kid"].(string)
		if kid == "" {
			return nil, errors.New("missing kid")
		}
		key, err := jwks.Key(ctx, kid)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", errJWKSUnavailable, err)
		}
		return key, nil
	})
	if err != nil || !parsed.Valid {
		return Claims{}, fmt.Errorf("invalid token: %w", err)
	}

	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return Claims{}, errors.New("invalid claims")
	}
	idStr, _ := mc["sub"].(string)
	id, err := uuid.Parse(idStr)
	if err != nil {
		return Claims{}, errors.New("invalid sub")
	}
	email, _ := mc["email"].(string)
	org, _ := mc["org_id"].(string)
	rolesAny, _ := mc["roles"].([]any)
	roles := make([]string, 0, len(rolesAny))
	for _, r := range rolesAny {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}
	return Claims{UserID: id, Email: email, OrgID: org, Roles: roles}, nil
}
