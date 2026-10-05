-- 000103 down: drop the password-version binding of API keys.
DROP TRIGGER IF EXISTS users_bump_password_version ON users;
DROP FUNCTION IF EXISTS bump_user_password_version();
ALTER TABLE api_keys DROP COLUMN IF EXISTS password_version;
ALTER TABLE users DROP COLUMN IF EXISTS password_version;
