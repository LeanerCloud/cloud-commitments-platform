-- Migration 000175: drop users.salt (issue #1956).
--
-- users.salt is a pre-bcrypt leftover (000001) that no runtime code reads or
-- writes (#313 removed the last writers; the bootstrap admin INSERTs in 000005
-- and 000006 ran before this column goes).
--
-- Rollback safety: every (id, salt) pair is copied into users_salt_backup_000175
-- before the column is dropped, and the down migration restores from it. The
-- backup has no foreign key, so it survives deletes from users.
-- NOTE: the backup table keeps the salt values in the database until the owner
-- drops it (DROP TABLE users_salt_backup_000175) once the rollback window ends.
BEGIN;

CREATE TABLE users_salt_backup_000175 (
    id UUID PRIMARY KEY,
    salt VARCHAR(255) NOT NULL
);

INSERT INTO users_salt_backup_000175 (id, salt)
SELECT id, salt FROM users;

ALTER TABLE users DROP COLUMN salt;

COMMIT;
