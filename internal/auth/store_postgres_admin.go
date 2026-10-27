package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// isDuplicateKeyError checks if the error is a PostgreSQL unique constraint violation (code 23505).
func isDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// isEmailDuplicateError returns true only when the violated constraint is the
// users_email_key (or users_email_unique) constraint, so we don't mistakenly
// surface "email already in use" for ID collisions or other uniqueness violations.
func isEmailDuplicateError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName == "users_email_key" ||
			pgErr.ConstraintName == "users_email_unique"
	}
	return false
}

// isLastAdminConstraintViolation reports whether err is the deferred trigger
// exception from migration 000065 (trg_min_one_admin). The trigger uses
// RAISE EXCEPTION with a message prefixed by "last_admin_constraint_violation"
// and PostgreSQL error code P0001 (raise_exception). Detecting by message
// prefix rather than code alone avoids false positives from other user-raised
// exceptions in the same codebase.
//
// The function also handles plain errors whose message contains the sentinel
// prefix, which allows unit tests to simulate the trigger violation without a
// live PostgreSQL connection.
func isLastAdminConstraintViolation(err error) bool {
	if err == nil {
		return false
	}
	const sentinel = "last_admin_constraint_violation"
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "P0001" && strings.HasPrefix(pgErr.Message, sentinel)
	}
	// Fallback for unit-test stubs that return a plain error with the sentinel
	// message (no pgconn available in mock-based tests).
	return strings.HasPrefix(err.Error(), sentinel)
}

// adminGroupContainsClause is the SQL predicate matching users who are members
// of the Administrators group. Authorization is group-membership-only after
// issue #907, so "is an admin" == "group_ids contains the Administrators group
// UUID". $1 must be bound to DefaultAdminGroupID.
const adminGroupContainsClause = `group_ids @> ARRAY[$1::uuid]`

// AdminExists checks if any active Administrators-group member exists. This is
// the group-membership replacement for the former role = 'admin' check.
func (s *PostgresStore) AdminExists(ctx context.Context) (bool, error) {
	query := `SELECT EXISTS(SELECT 1 FROM users WHERE ` + adminGroupContainsClause + ` AND active = true)`

	var exists bool
	err := s.db.QueryRow(ctx, query, DefaultAdminGroupID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check admin existence: %w", err)
	}

	return exists, nil
}

// CountGroupMembers returns the number of users whose group_ids contains
// groupID. Used to enforce last-administrator protection (issue #907).
func (s *PostgresStore) CountGroupMembers(ctx context.Context, groupID string) (int, error) {
	query := `SELECT COUNT(*) FROM users WHERE group_ids @> ARRAY[$1::uuid]`

	var count int
	if err := s.db.QueryRow(ctx, query, groupID).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count group members: %w", err)
	}
	return count, nil
}
