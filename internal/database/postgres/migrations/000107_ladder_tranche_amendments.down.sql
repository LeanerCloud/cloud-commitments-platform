-- Amendment history must survive rollback. Removing its schema would destroy audit data.
DO $$ BEGIN
    RAISE EXCEPTION 'Migration 107 retains amendment history and cannot be rolled back';
END $$;
