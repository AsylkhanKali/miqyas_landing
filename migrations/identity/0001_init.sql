-- Identity Service: пользователи, MFA, сессии (refresh tokens).
--
-- Решения:
--   - Пароли хранятся хэшем argon2id (никогда в открытом виде).
--   - TOTP-secret шифруется AES-GCM с master-key из Vault.
--   - Refresh-токены — хэшированы (SHA-256), отзываются установкой revoked_at.
--   - Роли — массив text для простоты; в проде заменяется на RBAC-таблицу.

CREATE SCHEMA IF NOT EXISTS identity;

CREATE TABLE IF NOT EXISTS identity.users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email           TEXT NOT NULL UNIQUE,
    full_name       TEXT NOT NULL DEFAULT '',
    org_id          TEXT NOT NULL,
    password_hash   TEXT NOT NULL,             -- argon2id encoded ($argon2id$...)
    totp_secret_enc BYTEA,                     -- зашифрованный TOTP secret, NULL если не активирован
    totp_enrolled   BOOLEAN NOT NULL DEFAULT FALSE,
    roles           TEXT[] NOT NULL DEFAULT '{}',
    status          TEXT NOT NULL DEFAULT 'active',  -- 'active' | 'disabled'
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_org ON identity.users (org_id);

-- Refresh-токены. Сам токен в БД не хранится — только sha256(token).
CREATE TABLE IF NOT EXISTS identity.sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES identity.users(id) ON DELETE CASCADE,
    refresh_sha256  BYTEA NOT NULL UNIQUE,
    user_agent      TEXT NOT NULL DEFAULT '',
    ip              TEXT NOT NULL DEFAULT '',
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sessions_user      ON identity.sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires   ON identity.sessions (expires_at);

-- История логинов (для безопасности и расследований). Иммутабельна.
CREATE TABLE IF NOT EXISTS identity.login_events (
    id          BIGSERIAL PRIMARY KEY,
    user_id     UUID,
    email       TEXT NOT NULL,
    outcome     TEXT NOT NULL,                -- 'success' | 'bad_password' | 'bad_totp' | 'disabled' | 'no_user'
    ip          TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT '',
    trace_id    TEXT,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_login_events_email   ON identity.login_events (email, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_login_events_outcome ON identity.login_events (outcome, occurred_at DESC);

CREATE OR REPLACE FUNCTION identity.deny_login_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'identity.login_events is append-only: % not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_login_deny_update ON identity.login_events;
CREATE TRIGGER trg_login_deny_update
    BEFORE UPDATE ON identity.login_events
    FOR EACH ROW EXECUTE FUNCTION identity.deny_login_mutation();

DROP TRIGGER IF EXISTS trg_login_deny_delete ON identity.login_events;
CREATE TRIGGER trg_login_deny_delete
    BEFORE DELETE ON identity.login_events
    FOR EACH ROW EXECUTE FUNCTION identity.deny_login_mutation();
