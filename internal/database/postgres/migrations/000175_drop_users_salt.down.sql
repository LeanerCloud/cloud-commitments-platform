-- 000175 down: restore users.salt from users_salt_backup_000175, then drop the backup.
--
-- Rows with an empty salt keep the column default and are not rewritten (no updated_at bump).
-- Users created after the up migration have no backup row and get the default ''.
-- Fails loudly if the backup table is gone.
BEGIN;

ALTER TABLE users ADD COLUMN salt VARCHAR(255) NOT NULL DEFAULT '';

UPDATE users
   SET salt = b.salt
  FROM users_salt_backup_000175 b
 WHERE users.id = b.id AND b.salt <> '';

DROP TABLE users_salt_backup_000175;

COMMIT;
