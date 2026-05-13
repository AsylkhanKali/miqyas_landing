package config

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	HTTPAddr     string `envconfig:"HTTP_ADDR"     default:":8085"`
	LogLevel     string `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string `envconfig:"OTEL_ENDPOINT" default:""`

	// Software backend
	KeysDir         string `envconfig:"KEYS_DIR"         default:"./var/esign-keys"`
	MasterKeyHex    string `envconfig:"MASTER_KEY_HEX"   required:"true"` // 64 hex char = 32 байта
	masterKeyBytes  []byte

	// Audit
	AuditURL     string        `envconfig:"AUDIT_URL"     default:"http://localhost:8082"`
	AuditToken   string        `envconfig:"AUDIT_TOKEN"   required:"true"`
	AuditTimeout time.Duration `envconfig:"AUDIT_TIMEOUT" default:"5s"`

	// Identity (JWT validation)
	IdentityJWKSURL string `envconfig:"IDENTITY_JWKS_URL" default:"http://localhost:8086/.well-known/jwks.json"`
}

func (c *Config) MasterKey() []byte { return c.masterKeyBytes }

func Load() (Config, error) {
	var c Config
	if err := envconfig.Process("ESIGN", &c); err != nil {
		return c, err
	}
	b, err := hex.DecodeString(c.MasterKeyHex)
	if err != nil {
		return c, fmt.Errorf("MASTER_KEY_HEX: %w", err)
	}
	if len(b) != 32 {
		return c, fmt.Errorf("MASTER_KEY_HEX must decode to 32 bytes (64 hex chars), got %d", len(b))
	}
	c.masterKeyBytes = b
	return c, nil
}
