-- Аудит-таблица для записей о запусках синхронизации.
-- Каждый запуск Temporal workflow оставляет здесь след: когда, что, сколько,
-- статус и ошибка. Дополняет Temporal UI как быстрый SQL-запрос для SRE.

CREATE TABLE IF NOT EXISTS tender_intel.sync_runs (
    id            BIGSERIAL PRIMARY KEY,
    workflow_id   TEXT NOT NULL,
    run_id        TEXT NOT NULL,
    kind          TEXT NOT NULL,           -- 'tenders' | 'references'
    started_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at   TIMESTAMPTZ,
    status        TEXT NOT NULL DEFAULT 'running',  -- 'running' | 'ok' | 'failed'
    items_fetched INT NOT NULL DEFAULT 0,
    error_msg     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sync_runs_kind_started ON tender_intel.sync_runs (kind, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_sync_runs_status       ON tender_intel.sync_runs (status);
