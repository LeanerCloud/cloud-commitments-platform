package migrations

import (
	"context"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

// bcryptCost matches the cost used in internal/auth/service_password.go.
const bcryptCost = 12

// defaultAdminGroupID is the fixed UUID of the Administrators group
// seeded by migration 000024_seed_default_groups.up.sql. Duplicated
// here as a literal (rather than imported from internal/auth) because
// the migrations package must not depend on the auth package -
// auth depends on the DB, not the reverse. Keep in sync with
// auth.DefaultAdminGroupID (internal/auth/types.go) and the literal
// inside the 000024 migration SQL.
const defaultAdminGroupID = "00000000-0000-5000-8000-000000000001"

// ensureAdminUser creates the admin user if it doesn't exist.
// When password is provided, the admin is created with a bcrypt-hashed password and active=true.
// When password is empty, the admin is created inactive and must use password reset to log in.
//
// The INSERT seeds group_ids with the Administrators group so the user
// has full group-based permissions from the first boot. After the
// insert, assignAdminGroupAndWarn runs an idempotent backfill on any
// admin row whose group_ids drifted to empty (e.g. from an out-of-band
// manual DB seed), and warns operators if the drift cannot be repaired
// (e.g. groups table not yet populated). See issue #351.
//
// Note: the `role` column was dropped by migration 000057; this INSERT
// intentionally omits it (issue #945).
func ensureAdminUser(ctx context.Context, pool *pgxpool.Pool, email, password string) error {
	if password != "" {
		return ensureAdminUserWithPassword(ctx, pool, email, password)
	}

	logging.Infof("Ensuring admin user exists: %s (user will need to reset password to login)", email)

	// Create admin user with no password - account is inactive until password is set via reset flow.
	// Use ON CONFLICT to prevent race conditions when multiple instances run migrations.
	// group_ids is seeded with the Administrators group so a fresh bootstrap admin
	// has group-based permissions from the start (issue #351).
	// The `role` column was removed by migration 000057 (issue #945).
	result, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, password_hash, active, group_ids, created_at, updated_at
		) VALUES (
			gen_random_uuid(), $1, '', false, ARRAY[$2]::UUID[], NOW(), NOW()
		)
		ON CONFLICT (email) DO NOTHING
	`, email, defaultAdminGroupID)

	if err != nil {
		return fmt.Errorf("failed to insert admin user: %w", err)
	}

	if result.RowsAffected() > 0 {
		logging.Infof("Admin user created: %s (password not set - user must reset)", email)
	} else {
		logging.Infof("Admin user already exists: %s", email)
	}

	// Idempotent backfill + invariant check on the bootstrap admin row
	// only. Scoping by email prevents the backfill from touching
	// non-admin users in pre-057/rollback states (issue #945).
	if err := assignAdminGroupAndWarn(ctx, pool, defaultAdminGroupID, email); err != nil {
		return fmt.Errorf("failed to assign admin group: %w", err)
	}
	return nil
}

// ensureAdminUserWithPassword creates or updates the admin with a hashed password and active=true.
// If the admin already exists with an empty password_hash, the password and active flag are updated.
//
// The INSERT seeds group_ids with the Administrators group so the user
// has full group-based permissions from the first boot. The DO UPDATE
// clause is deliberately NOT extended to touch group_ids - the
// post-insert assignAdminGroupAndWarn handles drift uniformly without
// coupling that semantics to the password-empty WHERE clause. See
// issue #351.
//
// Note: the `role` column was dropped by migration 000057; this INSERT
// intentionally omits it (issue #945).
func ensureAdminUserWithPassword(ctx context.Context, pool *pgxpool.Pool, email, password string) error {
	logging.Infof("Ensuring admin user exists with password: %s", email)

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	// Insert new admin or update existing one that has no password set yet.
	// Only overwrite password_hash/active when the existing hash is empty,
	// so we never clobber a password that was already set via the UI.
	// group_ids is seeded with the Administrators group on insert (issue #351).
	// The `role` column was removed by migration 000057 (issue #945).
	result, err := pool.Exec(ctx, `
		INSERT INTO users (
			id, email, password_hash, active, group_ids, created_at, updated_at
		) VALUES (
			gen_random_uuid(), $1, $2, true, ARRAY[$3]::UUID[], NOW(), NOW()
		)
		ON CONFLICT (email) DO UPDATE
			SET password_hash = $2, active = true, updated_at = NOW()
			WHERE users.password_hash = ''
	`, email, string(hashedPassword), defaultAdminGroupID)

	if err != nil {
		return fmt.Errorf("failed to upsert admin user: %w", err)
	}

	if result.RowsAffected() > 0 {
		logging.Infof("Admin user created/activated: %s", email)
	} else {
		logging.Infof("Admin user already has a password set: %s (skipping)", email)
	}

	// Idempotent backfill + invariant check on the bootstrap admin row
	// only. Scoping by email prevents the backfill from touching
	// non-admin users in pre-057/rollback states (issue #945).
	if err := assignAdminGroupAndWarn(ctx, pool, defaultAdminGroupID, email); err != nil {
		return fmt.Errorf("failed to assign admin group: %w", err)
	}
	return nil
}

// assignAdminGroupAndWarn runs an idempotent backfill that appends
// groupID to the bootstrap admin row (identified by adminEmail) when
// its group_ids is empty (NULL or zero-length). Scoping by email
// prevents the backfill from touching non-admin users in pre-057 or
// rollback states. The DISTINCT(unnest(...)) dedupe makes the UPDATE
// safe to run repeatedly. After the backfill, a defensive SELECT
// checks whether the admin row still has empty group_ids and logs a
// WARN so operators see drift in container logs.
//
// Post-migration-000057: the `users_min_one_group` CHECK constraint
// prevents group_ids from being NULL or zero-length, so this backfill
// is a no-op in normal operation. It remains as defense-in-depth for
// pre-057 schemas (rollback scenarios) and any future drift. The `role`
// column was removed by migration 000057 (issue #945) and must not be
// referenced here.
//
// The EXISTS guard on the groups table makes the backfill a no-op
// when migration 000024 hasn't yet seeded the Administrators group -
// defense-in-depth, since in practice this function is invoked
// after RunMigrations -> m.Up() completes.
func assignAdminGroupAndWarn(ctx context.Context, pool *pgxpool.Pool, groupID, adminEmail string) error {
	res, err := pool.Exec(ctx, `
		UPDATE users
		SET group_ids = ARRAY(
			SELECT DISTINCT unnest(
				COALESCE(group_ids, '{}') || ARRAY[$1]::UUID[]
			)
		),
			updated_at = NOW()
		WHERE email = $2
		  AND (group_ids IS NULL OR cardinality(group_ids) = 0)
		  AND EXISTS (SELECT 1 FROM groups WHERE id = $1::UUID)
	`, groupID, adminEmail)
	if err != nil {
		return fmt.Errorf("failed to backfill admin group_ids: %w", err)
	}
	if n := res.RowsAffected(); n > 0 {
		// Route to pkg/logging (stderr) like every other admin-activity
		// message in this file. fmt.Printf would echo this to stdout, which
		// issue #440 explicitly forbids for admin-account operations.
		logging.Infof("Backfilled group_ids for %d user(s) to include Administrators group", n)
	}

	// Invariant check: if the bootstrap admin row still has empty
	// group_ids after the backfill (e.g. EXISTS guard failed because
	// the Administrators group is missing), log loudly so operators notice.
	// Post-057, the users_min_one_group CHECK means this count is
	// always 0 unless a rollback is in progress.
	var remaining int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM users
		WHERE email = $1
		  AND (group_ids IS NULL OR cardinality(group_ids) = 0)
	`, adminEmail).Scan(&remaining); err != nil {
		return fmt.Errorf("failed to check admin group_ids invariant: %w", err)
	}
	if remaining > 0 {
		logging.Warnf("bootstrap admin %s has empty group_ids and the Administrators group could not be assigned. Permissions may not work as expected. Check that migration 000024_seed_default_groups has run successfully.", adminEmail)
	}
	return nil
}
