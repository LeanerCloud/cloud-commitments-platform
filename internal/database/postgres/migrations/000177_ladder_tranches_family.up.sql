-- Migration 000177 (platform#1964): EC2 instance family of a ladder tranche.
-- Empty string means no family (legacy rows and non-EC2-SP layers). ladder_tranches
-- has no UNIQUE constraint or ON CONFLICT target on layer, so two tranches that
-- differ only by family already coexist. Idempotent.
ALTER TABLE ladder_tranches
    ADD COLUMN IF NOT EXISTS family TEXT NOT NULL DEFAULT '';
