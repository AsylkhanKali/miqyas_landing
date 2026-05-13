// Package totp — обёртка над pquerna/otp для TOTP (RFC 6238).
//
// Secret в БД хранится зашифрованным AES-GCM с master-key брокера.
// Здесь — только генерация секрета и валидация кода.
package totp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const issuerName = "Procurement Platform"

// Enroll создаёт новый TOTP-секрет; возвращает otpauth-URL для отображения
// в виде QR + base32-secret (для ручного ввода) + зашифрованный байтовый
// secret для записи в БД.
type Enrollment struct {
	OTPAuthURL string
	Base32     string
	SecretEnc  []byte
}

func Enroll(email string, masterKey []byte) (Enrollment, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuerName,
		AccountName: email,
		Algorithm:   otp.AlgorithmSHA1, // совместимость с Google Authenticator
		Period:      30,
		Digits:      otp.DigitsSix,
	})
	if err != nil {
		return Enrollment{}, err
	}
	enc, err := seal(masterKey, []byte(key.Secret()))
	if err != nil {
		return Enrollment{}, err
	}
	return Enrollment{
		OTPAuthURL: key.URL(),
		Base32:     key.Secret(),
		SecretEnc:  enc,
	}, nil
}

// Verify проверяет 6-значный код против зашифрованного секрета.
func Verify(masterKey, secretEnc []byte, code string) (bool, error) {
	if len(secretEnc) == 0 {
		return false, errors.New("no totp secret")
	}
	secret, err := unseal(masterKey, secretEnc)
	if err != nil {
		return false, err
	}
	return totp.Validate(code, string(secret)), nil
}

// ── AES-GCM helpers ───────────────────────────────────────────────────────

func seal(masterKey, pt []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, pt, nil), nil
}

func unseal(masterKey, blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("blob too short")
	}
	return gcm.Open(nil, blob[:ns], blob[ns:], nil)
}
