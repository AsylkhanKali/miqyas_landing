-- Append-only журнал аудита с цепочкой хэшей для детекции тампера.
-- Каждая запись хранит prev_hash (хэш предыдущей записи) и hash (свой).
-- При вставке pgsql-функция вычисляет hash как SHA-256 от детерминированно
-- сериализованных полей + prev_hash, образуя цепочку. Цепочка валидируется
-- отдельным запросом (см. README audit-сервиса).

CREATE SCHEMA IF NOT EXISTS audit;

CREATE TABLE IF NOT EXISTS audit.events (
    seq          BIGSERIAL PRIMARY KEY,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor_type   TEXT        NOT NULL,          -- 'operator' | 'service' | 'system'
    actor_id     TEXT        NOT NULL,          -- email/sub оператора или имя сервиса
    action       TEXT        NOT NULL,          -- 'tender.synced', 'document.signed', ...
    resource     TEXT        NOT NULL,          -- 'tender:12345', 'doc:abc', ...
    org_id       TEXT,                          -- BIN/идентификатор организации, если применимо
    trace_id     TEXT,                          -- OpenTelemetry trace id для корреляции
    before_state JSONB       NOT NULL DEFAULT '{}'::jsonb,
    after_state  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    metadata     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    prev_hash    BYTEA       NOT NULL,
    hash         BYTEA       NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_occurred_at ON audit.events (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_actor       ON audit.events (actor_type, actor_id);
CREATE INDEX IF NOT EXISTS idx_events_resource    ON audit.events (resource);
CREATE INDEX IF NOT EXISTS idx_events_trace       ON audit.events (trace_id);

-- Запрет UPDATE/DELETE на уровне БД: журнал append-only.
CREATE OR REPLACE FUNCTION audit.deny_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit.events is append-only: % not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_deny_update ON audit.events;
CREATE TRIGGER trg_audit_deny_update
    BEFORE UPDATE ON audit.events
    FOR EACH ROW EXECUTE FUNCTION audit.deny_mutation();

DROP TRIGGER IF EXISTS trg_audit_deny_delete ON audit.events;
CREATE TRIGGER trg_audit_deny_delete
    BEFORE DELETE ON audit.events
    FOR EACH ROW EXECUTE FUNCTION audit.deny_mutation();

-- Хвост цепочки: хэш последней записи. Используется для корректного prev_hash
-- следующей вставки. Изначально пустая строка байт (\x00..).
CREATE TABLE IF NOT EXISTS audit.chain_tail (
    id         INT  PRIMARY KEY DEFAULT 1,
    last_hash  BYTEA NOT NULL DEFAULT decode('0000000000000000000000000000000000000000000000000000000000000000', 'hex'),
    last_seq   BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT single_row CHECK (id = 1)
);

INSERT INTO audit.chain_tail (id) VALUES (1)
    ON CONFLICT (id) DO NOTHING;
