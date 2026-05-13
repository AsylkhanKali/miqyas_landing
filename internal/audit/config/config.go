package config

import "github.com/kelseyhightower/envconfig"

type Config struct {
	HTTPAddr     string `envconfig:"HTTP_ADDR"     default:":8082"`
	LogLevel     string `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string `envconfig:"OTEL_ENDPOINT" default:""`
	// IngestToken — общий bearer-токен для приёма событий от внутренних сервисов.
	// В prod заменяется на mTLS + OIDC service accounts.
	IngestToken string `envconfig:"INGEST_TOKEN" required:"true"`
}

func Load() (Config, error) {
	var c Config
	return c, envconfig.Process("AUDIT", &c)
}
