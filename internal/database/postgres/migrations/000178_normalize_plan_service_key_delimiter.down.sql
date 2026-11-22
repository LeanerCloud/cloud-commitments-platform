-- Intentionally a no-op. The up migration cannot be told apart from plans the
-- API wrote under "provider/service" all along, so restoring the colon keys
-- would rewrite entries that never had one. The previous release reads both
-- spellings (findServiceKey), so keeping the normalized keys is safe.
SELECT 1;
