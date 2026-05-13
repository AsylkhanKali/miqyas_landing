-- Submission Service: оркестрация подачи заявок на закупки.
--
-- Состояния: draft → reviewed → signed → submitted → acknowledged → archived.
-- В любой момент до submitted доступна отмена (cancelled).
--
-- БД здесь — проекция состояния workflow (source of truth — Temporal event log).
-- Проекция нужна для быстрых выборок в UI и SQL-репортов compliance.

CREATE SCHEMA IF NOT EXISTS submission;

CREATE TABLE IF NOT EXISTS submission.submissions (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         TEXT NOT NULL,
    tender_id      TEXT NOT NULL,         -- внешний id закупки на площадке
    lot_id         TEXT,                  -- опциональный id лота
    platform       TEXT NOT NULL,         -- 'goszakup' | 'samruk'
    document_id    UUID NOT NULL,         -- ссылка на document service
    document_version INT NOT NULL,
    deadline_at    TIMESTAMPTZ NOT NULL,
    state          TEXT NOT NULL DEFAULT 'draft',
    workflow_id    TEXT NOT NULL,
    run_id         TEXT NOT NULL,
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Идемпотентность: одна организация не подаёт дважды один и тот же лот.
    UNIQUE (org_id, tender_id, lot_id)
);

CREATE INDEX IF NOT EXISTS idx_submissions_state    ON submission.submissions (state);
CREATE INDEX IF NOT EXISTS idx_submissions_deadline ON submission.submissions (deadline_at);
CREATE INDEX IF NOT EXISTS idx_submissions_org      ON submission.submissions (org_id);

-- История переходов состояний (append-only). Дополняет audit-сервис локальной
-- проекцией для быстрых ad-hoc запросов оператором.
CREATE TABLE IF NOT EXISTS submission.transitions (
    id            BIGSERIAL PRIMARY KEY,
    submission_id UUID NOT NULL REFERENCES submission.submissions(id) ON DELETE RESTRICT,
    from_state    TEXT NOT NULL,
    to_state      TEXT NOT NULL,
    actor         TEXT NOT NULL,
    reason        TEXT NOT NULL DEFAULT '',
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_transitions_submission ON submission.transitions (submission_id, occurred_at);

-- Транзишены иммутабельны.
CREATE OR REPLACE FUNCTION submission.deny_transition_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'submission.transitions is append-only: % not permitted', TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_transitions_deny_update ON submission.transitions;
CREATE TRIGGER trg_transitions_deny_update
    BEFORE UPDATE ON submission.transitions
    FOR EACH ROW EXECUTE FUNCTION submission.deny_transition_mutation();

DROP TRIGGER IF EXISTS trg_transitions_deny_delete ON submission.transitions;
CREATE TRIGGER trg_transitions_deny_delete
    BEFORE DELETE ON submission.transitions
    FOR EACH ROW EXECUTE FUNCTION submission.deny_transition_mutation();
