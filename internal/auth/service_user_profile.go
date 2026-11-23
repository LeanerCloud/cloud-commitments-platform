package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/jackc/pgx/v5"
)

// UpdateUserProfile allows a user to update their own email and password.
func (s *Service) UpdateUserProfile(ctx context.Context, userID, email, currentPassword, newPassword string) error {
	user, err := s.loadUser(ctx, userID)
	if err != nil {
		return err
	}

	if !s.verifyPassword(currentPassword, user.PasswordHash) {
		return ErrCurrentPasswordIncorrect
	}
	readEmail, readPasswordHash := user.Email, user.PasswordHash

	err = s.updateUserEmail(ctx, user, email)
	if err != nil {
		return err
	}

	passwordChanged, err := s.updateUserPassword(user, newPassword)
	if err != nil {
		return err
	}

	if err := s.store.UpdateUserCredentials(ctx, user, readEmail, readPasswordHash); err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	// Invalidate sessions and API keys when password changes. This is a
	// third self-service password-rotation path with the same gap as
	// ChangePassword/ConfirmPasswordReset (issue #102): an API key
	// authenticates via X-API-Key rather than the session store, so it
	// would otherwise survive a profile-driven password change too.
	if passwordChanged {
		s.invalidateUserCredentialsBestEffort(ctx, userID, "profile update")
		s.notifyPasswordChange(ctx, userID, newPassword)
	}

	logging.Infof("User profile updated: id=%s", user.ID)
	return nil
}

// updateUserEmail sets user.Email to raw, normalized by
// normalizeAccountEmail, after checking no other account holds it. An empty
// value, or one equal to the stored address, leaves the address unchanged, so
// a form that resubmits the current email keeps working for a row stored
// before normalization.
func (s *Service) updateUserEmail(ctx context.Context, user *User, raw string) error {
	if raw == "" || raw == user.Email {
		return nil
	}
	email, err := normalizeAccountEmail(raw)
	if err != nil {
		return err
	}
	if email != user.Email {
		// Check email uniqueness
		existing, err := s.store.GetUserByEmail(ctx, email)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if existing != nil {
			return ErrEmailInUse
		}
		user.Email = email
	}
	return nil
}

func (s *Service) updateUserPassword(user *User, newPassword string) (bool, error) {
	if newPassword == "" {
		return false, nil
	}

	if err := s.validatePassword(newPassword); err != nil {
		return false, err
	}

	// Check password history to prevent reuse
	if err := s.checkPasswordHistory(newPassword, user.PasswordHash, user.PasswordHistory); err != nil {
		return false, err
	}

	hash, err := s.hashPassword(newPassword)
	if err != nil {
		return false, fmt.Errorf("failed to hash password: %w", err)
	}

	// Update password history
	if user.PasswordHash != "" {
		user.PasswordHistory = addToPasswordHistory(user.PasswordHash, user.PasswordHistory)
	}

	user.PasswordHash = hash
	return true, nil
}

// ListUsers returns all users (admin only).
func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	return s.store.ListUsers(ctx)
}

// recordFailedLogin increments failed login attempts and locks the account if necessary.
func (s *Service) recordFailedLogin(ctx context.Context, user *User) {
	if err := s.store.RecordFailedLogin(ctx, user.ID); err != nil {
		logging.Errorf("Failed to record failed login attempt for user %s: %v", user.ID, err)
	}
}
