// Package signer — абстракция над хранилищем приватных ключей.
//
//   Software — для dev/staging: ключ зашифрован AES-GCM с master-ключом,
//             приходящим из Vault. Никогда не использовать в prod.
//   PKCS11   — для prod: операции подписи выполняются ВНУТРИ HSM,
//             приватный ключ никогда не покидает железо.
//
// Реализация PKCS#11 здесь — каркас (методы возвращают ErrNotImplemented),
// чтобы граница интерфейса была чистой и подключение прод-HSM было лишь
// заменой бэкенда без рефакторинга вышестоящих слоёв.
package signer

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNotImplemented — заглушка для PKCS#11 в этой версии.
var ErrNotImplemented = errors.New("backend not implemented")

// MaterializedKey — то, что сам signer хранит/использует для подписания.
// БД хранит только публичный сертификат и backend_ref, не само содержимое.
type MaterializedKey struct {
	BackendRef string // путь или slot/label
	CertPEM    []byte // публичный сертификат
}

// SignInput — то, что подписывается.
type SignInput struct {
	BackendRef string
	Algorithm  string // 'RSA-SHA256'
	Data       []byte // байты для подписи (хэшируется внутри)
}

// Signer — единая абстракция для всех бэкендов.
type Signer interface {
	// Sign возвращает подпись. data хэшируется внутри по схеме,
	// соответствующей Algorithm (например, RSA-SHA256 = PKCS#1 v1.5).
	Sign(in SignInput) ([]byte, error)
}

// ── SOFTWARE BACKEND ─────────────────────────────────────────────────────
//
// Приватные ключи лежат в файле .key.enc — это AES-256-GCM шифрование
// маршалинга PKCS#8. Master-ключ — 32 байта из env, в проде заменяется
// на dynamic-secret из Vault Transit. Зашифрованный файл сам по себе
// бесполезен без master-ключа.

type Software struct {
	dir       string
	masterKey []byte // 32 байта
}

func NewSoftware(dir string, masterKey []byte) (*Software, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir keys: %w", err)
	}
	return &Software{dir: dir, masterKey: masterKey}, nil
}

// GenerateAndStore генерирует RSA-ключ и самоподписанный сертификат,
// сохраняет приватный ключ зашифрованным, возвращает MaterializedKey
// (только публичная часть для записи в БД).
//
// Используется в dev-инструменте регистрации. В проде сертификаты приходят
// извне (Удостоверяющий центр), здесь только хранится handle к ключу.
func (s *Software) GenerateAndStore(subjectCN string, keySize int) (MaterializedKey, *x509.Certificate, error) {
	priv, err := rsa.GenerateKey(rand.Reader, keySize)
	if err != nil {
		return MaterializedKey{}, nil, err
	}
	cert, err := selfSignedCert(priv, subjectCN)
	if err != nil {
		return MaterializedKey{}, nil, err
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return MaterializedKey{}, nil, err
	}
	enc, err := s.seal(keyDER)
	if err != nil {
		return MaterializedKey{}, nil, err
	}

	fp := sha256.Sum256(cert.Raw)
	ref := filepath.Join(s.dir, fmt.Sprintf("%x.key.enc", fp))
	if err := os.WriteFile(ref, enc, 0o600); err != nil {
		return MaterializedKey{}, nil, err
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	return MaterializedKey{BackendRef: ref, CertPEM: pemBytes}, cert, nil
}

func (s *Software) Sign(in SignInput) ([]byte, error) {
	enc, err := os.ReadFile(in.BackendRef)
	if err != nil {
		return nil, fmt.Errorf("read key: %w", err)
	}
	keyDER, err := s.unseal(enc)
	if err != nil {
		return nil, fmt.Errorf("unseal: %w", err)
	}
	priv, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("parse pkcs8: %w", err)
	}
	rsaKey, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not RSA private key")
	}

	switch in.Algorithm {
	case "RSA-SHA256":
		sum := sha256.Sum256(in.Data)
		return rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, sum[:])
	default:
		return nil, fmt.Errorf("unsupported algorithm: %s", in.Algorithm)
	}
}

// seal шифрует plaintext AES-256-GCM; nonce генерируется случайно
// и хранится в начале выходного буфера.
func (s *Software) seal(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey)
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
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *Software) unseal(blob []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.masterKey)
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

// ── PKCS#11 STUB ─────────────────────────────────────────────────────────

// PKCS11 — заготовка для prod: подключение к HSM через github.com/miekg/pkcs11.
// Контракт интерфейса тот же, что у Software, чтобы вышестоящие слои не
// знали о смене бэкенда. Реализация добавляется отдельным изменением,
// которое не трогает service/HTTP-уровни.
type PKCS11 struct {
	// LibraryPath, SlotID, Pin etc. — будущие поля
}

func NewPKCS11() *PKCS11 { return &PKCS11{} }

func (p *PKCS11) Sign(SignInput) ([]byte, error) {
	return nil, fmt.Errorf("pkcs11: %w", ErrNotImplemented)
}
