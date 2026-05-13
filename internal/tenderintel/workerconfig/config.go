package workerconfig

import (
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	LogLevel     string        `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string        `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string        `envconfig:"OTEL_ENDPOINT" default:""`
	TemporalHost string        `envconfig:"TEMPORAL_HOST" default:"localhost:7233"`
	GoszakupURL  string        `envconfig:"GOSZAKUP_BASE_URL" default:"https://ows.goszakup.gov.kz"`
	GoszakupTO   time.Duration `envconfig:"GOSZAKUP_TIMEOUT"  default:"15s"`
	SyncCron     string        `envconfig:"SYNC_CRON" default:"0 * * * *"`

	AuditURL     string        `envconfig:"AUDIT_URL"     default:"http://localhost:8082"`
	AuditToken   string        `envconfig:"AUDIT_TOKEN"   required:"true"`
	AuditTimeout time.Duration `envconfig:"AUDIT_TIMEOUT" default:"5s"`
}

func Load() (Config, error) {
	var c Config
	return c, envconfig.Process("TENDER_INTEL", &c)
}
