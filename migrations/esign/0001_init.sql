-- ЭЦП Broker: метаданные зарегистрированных ключей.
--
-- ВАЖНО: приватный ключ НИКОГДА не хранится в этой БД.
-- В dev-режиме приватный ключ лежит в зашифрованном виде на диске
-- broker'а (AES-GCM с ключом из Vault); в проде — в HSM, и здесь хранится
-- только handle (slot/label) к нему.
--
-- В таблице — публичный сертификат, его метаданные и ссылка на хранилище.

CREATE SCHEMA IF NOT EXISTS esign;

CREATE TABLE IF NOT EXISTS esign.keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          TEXT NOT NULL,
    owner           TEXT NOT NULL,           -- email/sub оператора
    cert_subject_cn TEXT NOT NULL,
    cert_serial     TEXT NOT NULL,
    cert_not_before TIMESTAMPTZ NOT NULL,
    cert_not_after  TIMESTAMPTZ NOT NULL,
    cert_sha256     BYTEA NOT NULL,
    cert_pem        BYTEA NOT NULL,           -- публичный сертификат, не приватный ключ
    backend         TEXT NOT NULL,           -- 'software' | 'pkcs11'
    backend_ref     TEXT NOT NULL,           -- путь к зашифрованному файлу или slot/label HSM
    algorithm       TEXT NOT NULL DEFAULT 'RSA-SHA256',
    status          TEXT NOT NULL DEFAULT 'active', -- 'active' | 'revoked' | 'expired'
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (cert_sha256)
);

CREATE INDEX IF NOT EXISTS idx_esign_keys_owner ON esign.keys (owner);
CREATE INDEX IF NOT EXISTS idx_esign_keys_org   ON esign.keys (org_id);

-- Журнал операций подписи (дополняет audit-сервис локальной проекцией).
CREATE TABLE IF NOT EXISTS esign.sign_operations (
    id             BIGSERIAL PRIMARY KEY,
    key_id         UUID NOT NULL REFERENCES esign.keys(id) ON DELETE RESTRICT,
    actor          TEXT NOT NULL,
    purpose        TEXT NOT NULL,           -- свободный текст: 'document:<uuid>:v1' и т.п.
    input_sha256   BYTEA NOT NULL,
    signature_sha256 BYTEA NOT NULL,
    algorithm      TEXT NOT NULL,
    trace_id       TEXT,
    signed_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sign_ops_key   ON esign.sign_operations (key_id, signed_at DESC);
CREATE INDEX IF NOT EXISTS idx_sign_ops_actor ON esign.sign_operations (actor, signed_at DESC);

-- Операции подписи иммутабельны.
CREATE OR REPLACE FUNCTION esign.deny_sign_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'esign.sign_operations is append-only: % not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sign_ops_deny_update ON esign.sign_operations;
CREATE TRIGGER trg_sign_ops_deny_update
    BEFORE UPDATE ON esign.sign_operations
    FOR EACH ROW EXECUTE FUNCTION esign.deny_sign_mutation();

DROP TRIGGER IF EXISTS trg_sign_ops_deny_delete ON esign.sign_operations;
CREATE TRIGGER trg_sign_ops_deny_delete
    BEFORE DELETE ON esign.sign_operations
    FOR EACH ROW EXECUTE FUNCTION esign.deny_sign_mutation();
