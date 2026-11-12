package api

import (
	"context"
	"fmt"
)

// reshapeCloudAccountInScope checks whether the session's allowed_accounts
// permit access to the deployment's registered AWS cloud account. It returns
// (true, nil) when the session is unrestricted or the cloud account matches,
// (false, nil) when the cloud account is outside the session's scope (caller
// should return an empty response), and (false, err) on any lookup failure.
// Used by listConvertibleRIs, getRIUtilization, and getReshapeRecommendations
// to eliminate duplicated account-scoping blocks, and by executeExchange (via
// requireReshapeAccountScope) to enforce the same scope on the mutating path.
func (h *Handler) reshapeCloudAccountInScope(ctx context.Context, session *Session) (bool, error) {
	allowed, aErr := h.getAccountScope(ctx, session)
	if aErr != nil {
		return false, fmt.Errorf("failed to get allowed accounts: %w", aErr)
	}
	if allowed.AllowsAll() {
		return true, nil
	}
	cloudAccountID, aErr := h.resolveReshapeCloudAccountID(ctx)
	if aErr != nil {
		return false, fmt.Errorf("failed to resolve cloud account scope: %w", aErr)
	}
	return allowed.Allows(cloudAccountID, ""), nil
}

// resolveReshapeAccountScope resolves the deployment's cloud account once and
// reports whether the session's allowed_accounts permit it. Unlike
// reshapeCloudAccountInScope it always resolves, even for unrestricted
// sessions, because the RI utilization reads need the account to key their
// cache (issue #120). Returns ("", false, err) on any lookup failure.
func (h *Handler) resolveReshapeAccountScope(ctx context.Context, session *Session) (cloudAccountID string, inScope bool, err error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return "", false, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	cloudAccountID, err = h.resolveReshapeCloudAccountID(ctx)
	if err != nil {
		return "", false, fmt.Errorf("failed to resolve cloud account scope: %w", err)
	}
	return cloudAccountID, allowed.AllowsAll() || allowed.Allows(cloudAccountID, ""), nil
}

// requireReshapeAccountScope enforces the session's allowed_accounts scope for
// executeExchange (issue #93), returning 403 instead of an empty response
// since exchange execution is a mutating, financially irreversible action.
// See the PR description for why this is a separate gate from
// requirePermissionConstraints and why it is its own function.
func (h *Handler) requireReshapeAccountScope(ctx context.Context, session *Session) error {
	inScope, err := h.reshapeCloudAccountInScope(ctx, session)
	if err != nil {
		return err
	}
	if !inScope {
		return NewClientError(403, "permission denied: this deployment's cloud account is not covered by your session's allowed accounts")
	}
	return nil
}

// requireReshapeAccountVisible is the single-record read variant of
// requireReshapeAccountScope: the deployment's cloud account being outside the
// session scope yields errNotFound, indistinguishable from an unknown record.
func (h *Handler) requireReshapeAccountVisible(ctx context.Context, session *Session) error {
	inScope, err := h.reshapeCloudAccountInScope(ctx, session)
	if err != nil {
		return err
	}
	if !inScope {
		return errNotFound
	}
	return nil
}

// resolveReshapeCloudAccountIDOrUnattributed resolves the deployment's cloud
// account ID, substituting the unattributedAccountConstraint sentinel when the
// deployment maps to no registered account (SEC-01, issue #1141: an
// AccountIDs-constrained permission must deny rather than match an
// empty/absent constraint).
func (h *Handler) resolveReshapeCloudAccountIDOrUnattributed(ctx context.Context) (string, error) {
	cloudAccountID, err := h.resolveReshapeCloudAccountID(ctx)
	if err != nil {
		return "", err
	}
	if cloudAccountID == "" {
		return unattributedAccountConstraint, nil
	}
	return cloudAccountID, nil
}

// resolveReshapeCloudAccountID returns the cloud account ID for the running
// deployment, using the test-injected reshapeAccountResolver when set and
// falling back to the production resolveAWSCloudAccountID (STS-backed).
func (h *Handler) resolveReshapeCloudAccountID(ctx context.Context) (string, error) {
	if h.reshapeAccountResolver != nil {
		return h.reshapeAccountResolver(ctx)
	}
	return h.resolveAWSCloudAccountID(ctx)
}
