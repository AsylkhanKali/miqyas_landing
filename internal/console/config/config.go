package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	HTTPAddr     string `envconfig:"HTTP_ADDR"     default:":8090"`
	LogLevel     string `envconfig:"LOG_LEVEL"     default:"info"`
	OTELEndpoint string `envconfig:"OTEL_ENDPOINT" default:""`
	AllowOrigin  string `envconfig:"ALLOW_ORIGIN"  default:"http://localhost:3000"`

	TenderURL     string        `envconfig:"TENDER_URL"     default:"http://localhost:8081"`
	AuditURL      string        `envconfig:"AUDIT_URL"      default:"http://localhost:8082"`
	DocumentURL   string        `envconfig:"DOCUMENT_URL"   default:"http://localhost:8083"`
	SubmissionURL string        `envconfig:"SUBMISSION_URL" default:"http://localhost:8084"`
	EsignURL      string        `envconfig:"ESIGN_URL"      default:"http://localhost:8085"`
	IdentityURL   string        `envconfig:"IDENTITY_URL"   default:"http://localhost:8086"`
	Timeout       time.Duration `envconfig:"BACKEND_TIMEOUT" default:"10s"`

	// Identity (JWT validation)
	IdentityJWKSURL string `envconfig:"IDENTITY_JWKS_URL" default:"http://localhost:8086/.well-known/jwks.json"`
}

func Load() (Config, error) {
	var c Config
	return c, envconfig.Process("CONSOLE", &c)
}
