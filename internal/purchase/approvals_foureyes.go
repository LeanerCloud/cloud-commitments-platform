package purchase

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
)

// enforceFourEyesPolicy is the UNIVERSAL 4-eyes approval gate (issue #1005).
// It is the single choke point ApproveAndExecute runs before mutating any
// execution state, so every approve/execute entry point inherits the policy
// regardless of which caller reaches ApproveAndExecute:
//
//   - approvePurchaseViaSession (session-authed dashboard approve)
//   - directExecutePurchase (execute_mode="direct" -- issue #289)
//   - ApproveExecution (email-token deep-link approve, which funnels into
//     ApproveAndExecute)
//
// Adversarial review of PR #1500 found that requireDifferentApprover in
// internal/api was wired into only 2 of the 4 entry points then in use, leaving
// execute_mode="direct" (HIGH: the requester always equals the creator on
// this path, so this is unconditional self-authorization) able to bypass
// dual control entirely. This method closes that gap by running at the one place
// all of them share.
//
// Identity comparison has two tiers, checked in order of precision:
//
//  1. actorUserID (== transitionedBy, the actor's own UUID): populated by the
//     two session-based callers (approvePurchaseViaSession, directExecutePurchase)
//     via validUUIDPtrOrNil(&session.UserID). When present it is compared
//     DIRECTLY against execution.CreatedByUserID -- both are UUIDs from the
//     same `users` table, so this is authoritative identity, not a proxy.
//     This tier is what closes a gap an independent adversarial review found
//     in an earlier version of this fix: a per-user API key session
//     (Session.UserAPIKeyID != "") carries a real user UUID but an EMPTY
//     Session.Email, unlike a normal bearer-token session. An email-only
//     comparison would fall back to a non-email placeholder for that empty
//     Email and could never match the creator's real email, silently
//     allowing self-direct-execute. Comparing UUIDs first sidesteps the
//     email domain entirely for these two callers and cannot be fooled by an
//     empty or substituted email string.
//  2. actorEmail: used only when actorUserID is nil -- the token
//     (approveViaToken) caller into ApproveExecution, which always passes
//     transitionedBy=nil (see ApproveExecution's own doc comment), so the
//     RBAC/contact-email-verified actor identity from
//     authorizeApprovalAction is the only signal available there.
//     execution.CreatedByUserID is a UUID; internal/purchase cannot resolve
//     it through internal/auth directly (internal/auth already imports
//     internal/config, so the reverse import would cycle), so
//     GetUserEmailByID resolves the creator's email via a minimal,
//     auth-type-free query on the shared config store instead.
//
// Fails CLOSED whenever mode is on and any of the following holds: the
// execution has no recorded creator (legacy row); neither actorUserID nor a
// non-empty actorEmail identify the acting party; or the resolved identity
// (UUID or email) matches the creator's.
func (m *Manager) enforceFourEyesPolicy(ctx context.Context, executionID, actorEmail string, actorUserID *string) error {
	cfg, err := m.config.GetGlobalConfig(ctx)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to load global config: %w", err)
	}
	if cfg == nil || !cfg.RequireDifferentApprover {
		return nil
	}

	execution, err := m.loadExecutionForFourEyes(ctx, executionID)
	if err != nil || execution == nil {
		return err
	}
	return m.checkDifferentApprover(ctx, executionID, execution, actorEmail, actorUserID)
}

// loadExecutionForFourEyes fetches the execution enforceFourEyesPolicy needs
// to compare identities against. Returns (nil, nil) when the executionID
// does not resolve to a row -- there is nothing to gate, and the caller's
// subsequent TransitionExecutionStatus surfaces the standard not-found/
// cannot-transition error for a bogus or already-terminal executionID.
// Extracted from enforceFourEyesPolicy to keep it under the gocyclo
// threshold.
func (m *Manager) loadExecutionForFourEyes(ctx context.Context, executionID string) (*config.PurchaseExecution, error) {
	execution, err := m.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("4-eyes policy check: failed to load execution: %w", err)
	}
	return execution, nil
}

// checkSystemCreatedApprover gates a scheduler-minted execution (issue #233):
// there is no human requester to differ from, so any identified approver
// passes and an anonymous actor is denied (fail-closed).
func (m *Manager) checkSystemCreatedApprover(executionID, actorEmail string, actorUserID *string) error {
	if actorUserID == nil && strings.TrimSpace(actorEmail) == "" {
		logging.Warnf("purchase[%s]: 4-eyes mode on; system-created row, no actor identity available, denying (fail-closed)", executionID)
		return fmt.Errorf("%w: 4-eyes approval mode is enabled but no approver identity could be determined; sign in or supply a verified actor before approving", ErrFourEyesDenied)
	}
	logging.Infof("purchase[%s]: 4-eyes mode on; system-created row has no human requester, approver accepted", executionID)
	return nil
}

// checkDifferentApprover runs the actual identity comparison once mode is
// confirmed on and the execution is loaded. Extracted from
// enforceFourEyesPolicy to keep it under the gocyclo threshold.
func (m *Manager) checkDifferentApprover(ctx context.Context, executionID string, execution *config.PurchaseExecution, actorEmail string, actorUserID *string) error {
	if execution.CreatedByUserID == nil && execution.CreatedBySystem {
		return m.checkSystemCreatedApprover(executionID, actorEmail, actorUserID)
	}
	if execution.CreatedByUserID == nil {
		logging.Warnf("purchase[%s]: 4-eyes mode on; NULL creator (legacy row), denying", executionID)
		return fmt.Errorf("%w: this execution predates the dual-control feature and has no recorded creator; an admin must disable 4-eyes mode to approve", ErrFourEyesDenied)
	}

	// Tier 1: authoritative UUID comparison when the caller identified the
	// actor's own user row (session-based callers). See enforceFourEyesPolicy's
	// doc comment for why this must run before any email-based fallback.
	if actorUserID != nil {
		if *actorUserID == *execution.CreatedByUserID {
			logging.Warnf("purchase[%s]: 4-eyes mode on; creator %s attempted self-approval (actor UUID match), denied",
				executionID, *execution.CreatedByUserID)
			return fmt.Errorf("%w: 4-eyes mode requires a different approver than the requester", ErrFourEyesDenied)
		}
		return nil
	}

	// Tier 2: no actor UUID available (token callers) -- fall back to
	// resolving and comparing emails.
	actorEmail = strings.TrimSpace(actorEmail)
	if actorEmail == "" {
		logging.Warnf("purchase[%s]: 4-eyes mode on; no actor identity available, denying (fail-closed)", executionID)
		return fmt.Errorf("%w: 4-eyes approval mode is enabled but no approver identity could be determined; sign in or supply a verified actor before approving", ErrFourEyesDenied)
	}

	creatorEmail, err := m.config.GetUserEmailByID(ctx, *execution.CreatedByUserID)
	if err != nil {
		return fmt.Errorf("4-eyes policy check: failed to resolve creator identity: %w", err)
	}
	creatorEmail = strings.TrimSpace(creatorEmail)
	if creatorEmail == "" {
		logging.Warnf("purchase[%s]: 4-eyes mode on; creator account %s not found, denying (fail-closed)",
			executionID, *execution.CreatedByUserID)
		return fmt.Errorf("%w: 4-eyes approval mode is enabled but the creator's account could not be resolved; an admin must investigate before approving", ErrFourEyesDenied)
	}

	if email.SameAddress(creatorEmail, actorEmail) {
		logging.Warnf("purchase[%s]: 4-eyes mode on; creator %s attempted self-approval via actor %q, denied",
			executionID, *execution.CreatedByUserID, maskActor(actorEmail))
		return fmt.Errorf("%w: 4-eyes mode requires a different approver than the requester", ErrFourEyesDenied)
	}
	return nil
}
