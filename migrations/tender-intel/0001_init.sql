-- Initial schema for tender-intel service.
-- Stores normalized snapshots of public reference dictionaries and tenders
-- pulled from the official goszakup.gov.kz open data APIs.

CREATE SCHEMA IF NOT EXISTS tender_intel;

CREATE TABLE IF NOT EXISTS tender_intel.references (
    kind        TEXT NOT NULL,
    code        TEXT NOT NULL,
    name_ru     TEXT NOT NULL,
    name_kz     TEXT,
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, code)
);

CREATE INDEX IF NOT EXISTS idx_references_kind ON tender_intel.references (kind);

CREATE TABLE IF NOT EXISTS tender_intel.tenders (
    external_id   TEXT PRIMARY KEY,
    status        TEXT NOT NULL,
    title         TEXT NOT NULL,
    organizer_bin TEXT,
    publish_date  TIMESTAMPTZ,
    deadline_at   TIMESTAMPTZ,
    payload       JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_tenders_deadline   ON tender_intel.tenders (deadline_at);
CREATE INDEX IF NOT EXISTS idx_tenders_organizer  ON tender_intel.tenders (organizer_bin);
CREATE INDEX IF NOT EXISTS idx_tenders_status     ON tender_intel.tenders (status);
