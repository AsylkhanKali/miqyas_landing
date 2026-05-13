package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	HTTPAddr     string `envconfig:"HTTP_ADDR"     default:":8083"`
	LogLevel     string `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string `envconfig:"OTEL_ENDPOINT" default:""`

	// S3 / MinIO
	S3Endpoint     string `envconfig:"S3_ENDPOINT"     default:"http://localhost:9000"`
	S3Region       string `envconfig:"S3_REGION"       default:"us-east-1"`
	S3AccessKey    string `envconfig:"S3_ACCESS_KEY"   required:"true"`
	S3SecretKey    string `envconfig:"S3_SECRET_KEY"   required:"true"`
	S3Bucket       string `envconfig:"S3_BUCKET"       default:"documents"`
	S3UsePathStyle bool   `envconfig:"S3_USE_PATH_STYLE" default:"true"`

	// Audit
	AuditURL     string        `envconfig:"AUDIT_URL"     default:"http://localhost:8082"`
	AuditToken   string        `envconfig:"AUDIT_TOKEN"   required:"true"`
	AuditTimeout time.Duration `envconfig:"AUDIT_TIMEOUT" default:"5s"`

	// Identity (JWT validation)
	IdentityJWKSURL string `envconfig:"IDENTITY_JWKS_URL" default:"http://localhost:8086/.well-known/jwks.json"`
}

func Load() (Config, error) {
	var c Config
	return c, envconfig.Process("DOCUMENT", &c)
}
