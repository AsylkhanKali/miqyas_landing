// Package jwtkey — менеджмент RSA-ключей для подписи JWT и публикация JWKS.
//
// В DEV-режиме ключ генерируется/читается с диска (PEM PKCS#8). В prod
// заменяется на ключи из Vault Transit / HSM. Интерфейс одинаков.
package jwtkey

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
)

// Issuer хранит активную пару ключей для подписи access-токенов.
type Issuer struct {
	priv  *rsa.PrivateKey
	pub   *rsa.PublicKey
	kid   string
}

// LoadOrCreate читает приватный ключ из PEM-файла или генерирует новый.
func LoadOrCreate(path string, bits int) (*Issuer, error) {
	if data, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, errors.New("invalid PEM in key file")
		}
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse pkcs8: %w", err)
		}
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("not RSA key")
		}
		return newIssuer(rsaKey), nil
	}

	priv, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	return newIssuer(priv), nil
}

func newIssuer(priv *rsa.PrivateKey) *Issuer {
	pub := &priv.PublicKey
	// kid — sha256(DER(public)) первые 16 байт в hex.
	der, _ := x509.MarshalPKIXPublicKey(pub)
	sum := sha256.Sum256(der)
	return &Issuer{priv: priv, pub: pub, kid: fmt.Sprintf("%x", sum[:16])}
}

func (i *Issuer) Private() *rsa.PrivateKey { return i.priv }
func (i *Issuer) Public() *rsa.PublicKey   { return i.pub }
func (i *Issuer) KID() string              { return i.kid }

// JWKS — структура, публикуемая в /.well-known/jwks.json.
type JWKS struct {
	Keys []JWK `json:"keys"`
}

type JWK struct {
	Kty string `json:"kty"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (i *Issuer) JWKS() JWKS {
	return JWKS{Keys: []JWK{{
		Kty: "RSA",
		Use: "sig",
		Kid: i.kid,
		Alg: "RS256",
		N:   base64.RawURLEncoding.EncodeToString(i.pub.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(i.pub.E)).Bytes()),
	}}}
}
