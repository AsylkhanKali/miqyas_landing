package config

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	HTTPAddr     string        `envconfig:"HTTP_ADDR" default:":8081"`
	LogLevel     string        `envconfig:"LOG_LEVEL" default:"info"`
	PostgresDSN  string        `envconfig:"POSTGRES_DSN" required:"true"`
	OTELEndpoint string        `envconfig:"OTEL_ENDPOINT" default:""`
	GoszakupURL  string        `envconfig:"GOSZAKUP_BASE_URL" default:"https://ows.goszakup.gov.kz"`
	GoszakupTO   time.Duration `envconfig:"GOSZAKUP_TIMEOUT" default:"15s"`
}

func Load() (Config, error) {
	var c Config
	err := envconfig.Process("TENDER_INTEL", &c)
	return c, err
}
