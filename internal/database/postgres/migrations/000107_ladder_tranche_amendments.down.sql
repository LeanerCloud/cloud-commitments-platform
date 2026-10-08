-- Amendment history must survive rollback. Refuse only when it holds rows; an empty table can be dropped safely.
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM ladder_tranche_amendments) THEN
        RAISE EXCEPTION 'Migration 107 retains amendment history and cannot be rolled back';
    END IF;
END $$;
DROP TABLE ladder_tranche_amendments;
ALTER TABLE ladder_tranches DROP COLUMN revision;
