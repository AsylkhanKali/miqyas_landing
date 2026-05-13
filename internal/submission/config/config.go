package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

// HTTPConfig — конфиг API-сервера submission.
type HTTPConfig struct {
	HTTPAddr     string `envconfig:"HTTP_ADDR"     default:":8084"`
	LogLevel     string `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string `envconfig:"OTEL_ENDPOINT" default:""`
	TemporalHost string `envconfig:"TEMPORAL_HOST" default:"localhost:7233"`

	// Identity (JWT validation)
	IdentityJWKSURL string `envconfig:"IDENTITY_JWKS_URL" default:"http://localhost:8086/.well-known/jwks.json"`
}

// WorkerConfig — конфиг Temporal worker submission.
type WorkerConfig struct {
	LogLevel     string        `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string        `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string        `envconfig:"OTEL_ENDPOINT" default:""`
	TemporalHost string        `envconfig:"TEMPORAL_HOST" default:"localhost:7233"`
	AuditURL     string        `envconfig:"AUDIT_URL"     default:"http://localhost:8082"`
	AuditToken   string        `envconfig:"AUDIT_TOKEN"   required:"true"`
	AuditTimeout time.Duration `envconfig:"AUDIT_TIMEOUT" default:"5s"`
	// Зарегистрированные адаптеры площадок (через запятую): goszakup,samruk.
	// В этой версии все они stub.
	Platforms string `envconfig:"PLATFORMS" default:"goszakup,samruk"`
}

func LoadHTTP() (HTTPConfig, error) {
	var c HTTPConfig
	return c, envconfig.Process("SUBMISSION", &c)
}

func LoadWorker() (WorkerConfig, error) {
	var c WorkerConfig
	return c, envconfig.Process("SUBMISSION", &c)
}
