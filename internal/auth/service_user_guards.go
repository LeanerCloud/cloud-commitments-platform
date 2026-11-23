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

// guardSelfEscalation runs the self-edit checks: the #907 manage-users gate,
// the #1550 carved-out-verb gate that manage-users is not sufficient to pass,
// and the #1756 account ceiling. Split out of guardGroupChange to keep that
// function under gocyclo's threshold.
//
// The two dimensions have deliberately different reach, and the difference is
// a property of the data rather than a convenience:
//
//   - Permissions are a plain union with no inverted empty case, so dropping a
//     group can only SHRINK the effective set. Only an added group can grant a
//     permission, which is why the permission checks stay behind addsNewGroup.
//   - allowed_accounts is a union in which the EMPTY set means every account
//     (IsUnrestrictedAccess). Dropping the group that carries the restriction
//     collapses the union to empty and thereby WIDENS scope, so the account
//     ceiling has to run on every self-edit, additions or not.
//
// DO NOT "simplify" this back to re-reading the actor's row
// (GetUserPermissions, or any fresh GetUserByID). That is the shorter
// spelling and it is a silent regression.
//
// Both checks are evaluated against the actor's PRIOR membership, resolved
// from `prior` directly. "Did you hold this BEFORE the change?" is the
// question a self-escalation guard has to ask, so resolving it from the
// snapshot is the correct implementation as well as the simpler one -- not a
// workaround for anything.
//
// The previous version re-read the row and was correct only by accident of
// transaction timing: UpdateUser calls applyUpdateUserRequest BEFORE these
// guards run, so the in-memory user already carries the NEW membership, and
// the re-read returned the old values purely because the write had not been
// committed yet. The tell was in the test suite, where this case had to hand
// back "a distinct, unmutated viewer copy for that second read" to avoid
// aliasing the just-mutated object. Any caller that passes the mutated user,
// or any future change that resolves permissions through it, makes the guard
// authorize the very escalation it exists to block.
//
// Enforced by mutation, not just by this comment: swapping `prior` for `next`
// below fails the suite (see the M13 row in the PR #1737 mutation matrix).
//
// heldBefore is that PRIOR permission set, resolved once by guardGroupChange
// and shared with the #226 grant-ceiling check.
func (s *Service) guardSelfEscalation(ctx context.Context, prior, next []string, heldBefore []Permission) error {
	if addsNewGroup(prior, next) {
		if !permissionsAllow(heldBefore, ActionUpdate, ResourceUsers, nil) {
			return ErrSelfEscalation
		}
		// Holding update:users is NOT enough to hand yourself the money verbs.
		if err := s.guardSelfCarvedOutGrant(ctx, heldBefore, next, prior); err != nil {
			return err
		}
	}
	// Runs whether or not anything was added: see the account bullet above.
	return s.guardSelfAccountScope(ctx, prior, next)
}

// guardSelfAccountScope bounds the account dimension of a self-membership
// change: the scope the actor comes out with may not reach beyond the scope
// they went in with (issue #1756).
//
// It compares RESULTING scope against PRIOR scope rather than inspecting what
// is being added, and that is the whole point. Two routes widen scope and they
// run in opposite directions, so an "is anything being granted?" check only
// ever sees one of them:
//
//   - JOIN a wider group. The seeded Administrators group ships
//     allowed_accounts = ARRAY['*'] (migrations 000024/000057), so adding it
//     hands the actor every account. The permission gate passes this happily:
//     update:users is what it asks for and the actor already holds it.
//   - LEAVE the scoping group. The union collapses to empty, which
//     IsUnrestrictedAccess reads as every account. Removing the restriction
//     grants the restriction.
//
// accountScopeGap gets both cases right because it treats empty and "*" as
// UNRESTRICTED on EITHER side, so it is not a subset test: the empty set is a
// subset of everything while meaning the opposite of narrow. Prior
// unrestricted accepts anything (nothing left to widen); next unrestricted
// against a restricted prior is refused whatever produced it; otherwise next
// must name only accounts prior already named.
func (s *Service) guardSelfAccountScope(ctx context.Context, prior, next []string) error {
	priorAccounts, err := s.accountsForGroups(ctx, prior)
	if err != nil {
		return err
	}
	nextAccounts, err := s.accountsForGroups(ctx, next)
	if err != nil {
		return err
	}
	if gap := accountScopeGap(priorAccounts, nextAccounts); gap != "" {
		return fmt.Errorf(
			"%w: this change would give you %s, beyond your current account scope; ask another administrator to make it",
			ErrSelfEscalation, gap)
	}
	return nil
}
