-- Migration 000103: bind API keys to the owner's password (issue #402).
--
-- users.password_version is bumped by trigger whenever password_hash changes,
-- whichever code path writes it. A key records the version of the user row
-- its minting read, and ValidateUserAPIKey rejects it once the two differ, so
-- a rotation invalidates every earlier key even if the revocation scan fails
-- or a key is minted concurrently with the rotation. Existing keys start at
-- version 0, like their owners, and stop working at the owner's next rotation.
ALTER TABLE users ADD COLUMN password_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN password_version BIGINT NOT NULL DEFAULT 0;

CREATE OR REPLACE FUNCTION bump_user_password_version()
RETURNS TRIGGER AS $$
BEGIN
    NEW.password_version = OLD.password_version + 1;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER users_bump_password_version
    BEFORE UPDATE OF password_hash ON users
    FOR EACH ROW
    WHEN (OLD.password_hash IS DISTINCT FROM NEW.password_hash)
    EXECUTE FUNCTION bump_user_password_version();
