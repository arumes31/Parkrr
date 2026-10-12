-- Shared, editable text/checklist templates for vehicle handovers.
CREATE TABLE IF NOT EXISTS handover_templates (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    applies_to TEXT NOT NULL DEFAULT 'beide'
               CHECK (applies_to IN ('einlagerung', 'auslagerung', 'beide')),
    content    TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_handover_templates_name
    ON handover_templates (lower(name), id);
