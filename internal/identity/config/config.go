package config

import (
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Config struct {
	HTTPAddr     string        `envconfig:"HTTP_ADDR"     default:":8086"`
	LogLevel     string        `envconfig:"LOG_LEVEL"     default:"info"`
	PostgresDSN  string        `envconfig:"POSTGRES_DSN"  required:"true"`
	OTELEndpoint string        `envconfig:"OTEL_ENDPOINT" default:""`

	IssuerName   string        `envconfig:"ISSUER_NAME"   default:"http://localhost:8086"`
	JWTKeyPath   string        `envconfig:"JWT_KEY_PATH"  default:"./var/identity-jwt.pem"`
	JWTKeyBits   int           `envconfig:"JWT_KEY_BITS"  default:"2048"`
	AccessTTL    time.Duration `envconfig:"ACCESS_TTL"    default:"15m"`
	RefreshTTL   time.Duration `envconfig:"REFRESH_TTL"   default:"24h"`

	// 32-байтный ключ для шифрования TOTP-secret'ов на диске.
	TOTPMasterKeyHex string `envconfig:"TOTP_MASTER_KEY_HEX" required:"true"`
	totpKey          []byte

	// DEV ONLY: пропустить проверку TOTP при логине (для bootstrap первого пользователя).
	// НИКОГДА не включать в production.
	DevSkipMFA bool `envconfig:"DEV_SKIP_MFA" default:"false"`
}

func (c *Config) TOTPMasterKey() []byte { return c.totpKey }

func Load() (Config, error) {
	var c Config
	if err := envconfig.Process("IDENTITY", &c); err != nil {
		return c, err
	}
	b, err := hex.DecodeString(c.TOTPMasterKeyHex)
	if err != nil {
		return c, fmt.Errorf("TOTP_MASTER_KEY_HEX: %w", err)
	}
	if len(b) != 32 {
		return c, fmt.Errorf("TOTP_MASTER_KEY_HEX must decode to 32 bytes, got %d", len(b))
	}
	c.totpKey = b
	return c, nil
}
