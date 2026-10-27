package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ==========================================
// GROUP OPERATIONS
// ==========================================

// GetGroup retrieves a group by ID.
func (s *PostgresStore) GetGroup(ctx context.Context, groupID string) (*Group, error) {
	query := `
		SELECT id, name, description, permissions, allowed_accounts,
		       system_managed, created_at, updated_at, created_by
		FROM groups
		WHERE id = $1
	`

	return s.scanGroup(s.db.QueryRow(ctx, query, groupID))
}

// CreateGroup creates a new group.
func (s *PostgresStore) CreateGroup(ctx context.Context, group *Group) error {
	// Generate UUID if not provided
	if group.ID == "" {
		group.ID = uuid.New().String()
	}

	// Set timestamps
	now := time.Now()
	group.CreatedAt = now
	group.UpdatedAt = now

	// Marshal permissions to JSONB
	permissionsJSON, err := json.Marshal(group.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	query := `
		INSERT INTO groups (
			id, name, description, permissions, allowed_accounts,
			created_at, updated_at, created_by
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	// created_by is a nullable UUID FK; convert empty string to NULL so
	// Postgres doesn't reject the insert with "invalid input syntax for
	// type uuid" when the caller doesn't have a user ID.
	var createdBy any = group.CreatedBy
	if group.CreatedBy == "" {
		createdBy = nil
	}

	_, err = s.db.Exec(ctx, query,
		group.ID,
		group.Name,
		group.Description,
		permissionsJSON,
		group.AllowedAccounts,
		group.CreatedAt,
		group.UpdatedAt,
		createdBy,
	)

	if err != nil {
		return fmt.Errorf("failed to create group: %w", err)
	}

	return nil
}

// UpdateGroup updates an existing group.
func (s *PostgresStore) UpdateGroup(ctx context.Context, group *Group) error {
	group.UpdatedAt = time.Now()

	// Marshal permissions to JSONB
	permissionsJSON, err := json.Marshal(group.Permissions)
	if err != nil {
		return fmt.Errorf("failed to marshal permissions: %w", err)
	}

	query := `
		UPDATE groups SET
			name = $2,
			description = $3,
			permissions = $4,
			allowed_accounts = $5,
			updated_at = $6
		WHERE id = $1
	`

	result, err := s.db.Exec(ctx, query,
		group.ID,
		group.Name,
		group.Description,
		permissionsJSON,
		group.AllowedAccounts,
		group.UpdatedAt,
	)

	if err != nil {
		return fmt.Errorf("failed to update group: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("group not found: %s", group.ID)
	}

	return nil
}
