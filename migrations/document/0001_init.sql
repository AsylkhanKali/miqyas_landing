-- Document Service: шаблоны и инстансы документов для пакетов заявок.
--
-- Модель:
--   document_templates — версионируемые шаблоны (например, "ценовое предложение").
--   documents          — конкретные документы организации, привязанные к шаблону.
--   document_versions  — каждая версия документа = отдельная запись (immutable).
--                        Содержимое лежит в S3, в БД только метаданные и хэш.

CREATE SCHEMA IF NOT EXISTS document;

CREATE TABLE IF NOT EXISTS document.templates (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code          TEXT NOT NULL UNIQUE,           -- 'price-offer', 'tech-spec', ...
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    schema        JSONB NOT NULL,                  -- JSON-схема обязательных полей
    rules         JSONB NOT NULL DEFAULT '[]'::jsonb,  -- список правил валидации
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS document.documents (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        TEXT NOT NULL,
    template_code TEXT NOT NULL REFERENCES document.templates(code) ON DELETE RESTRICT,
    title         TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'draft',  -- draft|validated|signed|archived
    created_by    TEXT NOT NULL,                   -- email/sub оператора
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_documents_org_status ON document.documents (org_id, status);
CREATE INDEX IF NOT EXISTS idx_documents_template   ON document.documents (template_code);

CREATE TABLE IF NOT EXISTS document.document_versions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id    UUID NOT NULL REFERENCES document.documents(id) ON DELETE RESTRICT,
    version        INT  NOT NULL,                     -- инкрементальный номер от 1
    payload        JSONB NOT NULL,                    -- данные документа
    s3_bucket      TEXT NOT NULL,
    s3_key         TEXT NOT NULL,
    s3_etag        TEXT NOT NULL,
    content_sha256 BYTEA NOT NULL,                    -- хэш отрендеренного содержимого
    content_size   BIGINT NOT NULL,
    validated      BOOLEAN NOT NULL DEFAULT FALSE,
    validation     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (document_id, version)
);

CREATE INDEX IF NOT EXISTS idx_versions_document ON document.document_versions (document_id, version DESC);

-- Запрет UPDATE/DELETE на versions: версии иммутабельны.
CREATE OR REPLACE FUNCTION document.deny_version_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'document.document_versions is immutable: % not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_versions_deny_update ON document.document_versions;
CREATE TRIGGER trg_versions_deny_update
    BEFORE UPDATE ON document.document_versions
    FOR EACH ROW EXECUTE FUNCTION document.deny_version_mutation();

DROP TRIGGER IF EXISTS trg_versions_deny_delete ON document.document_versions;
CREATE TRIGGER trg_versions_deny_delete
    BEFORE DELETE ON document.document_versions
    FOR EACH ROW EXECUTE FUNCTION document.deny_version_mutation();
