package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
)

// guardActiveChange caps a reactivation at the actor's grant ceiling, then
// applies guardDeactivation. Reactivating restores the target's whole
// membership at once, so update:users alone must not bring back someone holding
// more than the actor does (issue #89 review; otherwise the #226 ceiling only
// runs on GroupIDs changes). Deactivation is a revocation and, like a group
// removal, is not ceiling-checked. Trusted internal callers (actorUserID == "")
// skip the ceiling.
func (s *Service) guardActiveChange(ctx context.Context, actorUserID string, user *User, priorActive bool, reqActive *bool) error {
	reactivating := !priorActive && reqActive != nil && *reqActive
	if actorUserID != "" && reactivating {
		if err := s.checkMembershipGrantCeiling(ctx, actorUserID, user.GroupIDs); err != nil {
			return err
		}
	}
	return s.guardDeactivation(ctx, user, priorActive, reqActive)
}

// guardDeactivation rejects deactivating the last active Administrators-group
// member, which would lock the deployment out of admin functionality, the same
// hazard as removing the group. AdminExists and the 000065 trigger both count
// only active members, so this soft check mirrors that invariant and rejects
// early with a friendly 409. The deferred trigger is the race-free backstop
// when concurrent requests slip past this read-then-write check. Extracted from
// UpdateUser to keep that function under gocyclo's complexity threshold.
//
// user carries the post-applyUpdateUserRequest group membership; priorActive is
// the active state before the update and reqActive the requested change (nil
// when the request does not touch Active).
func (s *Service) guardDeactivation(ctx context.Context, user *User, priorActive bool, reqActive *bool) error {
	deactivating := priorActive && reqActive != nil && !*reqActive
	if deactivating && containsGroup(user.GroupIDs, DefaultAdminGroupID) {
		return s.checkLastAdminConstraint(ctx)
	}
	return nil
}

// revokeSessionsOnDeactivation deletes the user's sessions after an
// Active true -> false change has been persisted. Failure is logged, not
// returned, matching DeleteUser and ChangePassword; ValidateSession's Active
// check covers the gap (issue #89).
func (s *Service) revokeSessionsOnDeactivation(ctx context.Context, user *User, priorActive bool) {
	if !priorActive || user.Active {
		return
	}
	if err := s.store.DeleteUserSessions(ctx, user.ID); err != nil {
		logging.Warnf("Failed to delete sessions for user %s during deactivation: %v", user.ID, err)
	}
}

// applyDeactivatedAtTransition sets or clears User.DeactivatedAt from the
// requested Active value. An explicit deactivation stamps it even when the
// user was already inactive (an invited user), so an outstanding invite link
// cannot activate the account afterwards; a repeat deactivation keeps the
// original timestamp. DeactivatedAt is what lets the password-reset flow
// refuse admin-deactivated accounts while still completing invites (A03-006).
func applyDeactivatedAtTransition(user *User, reqActive *bool) {
	if reqActive == nil {
		return
	}
	if *reqActive {
		user.DeactivatedAt = nil
		return
	}
	if user.DeactivatedAt == nil {
		now := time.Now()
		user.DeactivatedAt = &now
	}
}

// guardGroupChange enforces the invariants for a group-membership change:
// the issue #907 / #226 access rules in guardGroupChangeAccess, then the issue
// #78 check that the change does not leave a view-only group next to a
// write-capable one. The contradiction check runs last so a refused
// escalation is still reported as the escalation (403), and it runs for every
// caller, trusted internal ones included, because it is about the resulting
// membership rather than the actor. An unchanged membership is not
// re-checked (see checkGroupMembershipChange).
func (s *Service) guardGroupChange(ctx context.Context, actorUserID, targetUserID string, prior, next []string) error {
	if err := s.guardGroupChangeAccess(ctx, actorUserID, targetUserID, prior, next); err != nil {
		return err
	}
	return s.checkGroupMembershipChange(ctx, prior, next)
}

// guardGroupChangeAccess enforces the issue #907 invariants for a
// group-membership change: at least one group remains, the last
// Administrators-group member is not removed, and a non-privileged actor
// cannot escalate their own access.
func (s *Service) guardGroupChangeAccess(ctx context.Context, actorUserID, targetUserID string, prior, next []string) error {
	if len(next) == 0 {
		return ErrNoGroups
	}

	// Last-admin protection: if this change removes the Administrators group
	// from a user who currently has it, ensure at least one other member
	// keeps it.
	if containsGroup(prior, DefaultAdminGroupID) && !containsGroup(next, DefaultAdminGroupID) {
		if err := s.checkLastAdminConstraint(ctx); err != nil {
			return err
		}
	}

	// Internal callers (actorUserID == "") are already trusted and skip
	// every actor-scoped guard below.
	if actorUserID == "" {
		// Trusted internal caller (e.g. bootstrap/seeding); already authorized.
		return nil
	}

	// Grant ceiling (issue #226): update:users alone does not authorize
	// handing any target, the actor included, more access than the actor
	// already holds. guardSelfEscalation only checks that a self-editor holds
	// update:users, so without this a caller holding just update:users could
	// add Administrators to their own membership. A self-edit resolves the
	// actor's permissions from `prior` (see guardSelfEscalation for why it
	// must not re-read the row).
	added := addedGroups(prior, next)
	if actorUserID == targetUserID {
		heldBefore, err := s.permissionsForGroups(ctx, prior)
		if err != nil {
			return fmt.Errorf("failed to resolve the acting user's permissions: %w", err)
		}
		// Self-escalation guard: the change must not leave the actor
		// holding more than they went in with. It deliberately runs on
		// removals too (issue #1756): dropping a group can WIDEN account
		// scope, since an empty allowed_accounts union means every account.
		if err := s.guardSelfEscalation(ctx, prior, next, heldBefore); err != nil {
			return err
		}
		return s.checkMembershipGrantCeilingWithPerms(ctx, heldBefore, added)
	}
	return s.checkMembershipGrantCeiling(ctx, actorUserID, added)
}
