package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// requireAccountAccess fetches the account by ID, then verifies the session's
// AccountScope allows it (AccountScope.Allows). Returns the
// fetched account on success so callers can avoid a second GetCloudAccount
// call.
//
// Returns errNotFound when either the account does not exist OR the user is
// restricted to a subset of accounts and the target is outside their scope.
// The 404-not-403 choice is deliberate: a user with view:accounts permission
// who can see account A shouldn't be able to infer that account B exists by
// probing a single-account endpoint. IDOR-via-enumeration is cheap with
// UUIDv4, but logs, URLs, and copy-paste still leak IDs.
//
// requirePermission must be called before this helper so the verb gate fires
// first; callers pass the session it returned.
//
// A non-UUID accountID is refused with a 400 before the lookup:
// cloud_accounts.id is a UUID column, so Postgres would reject the value with
// a syntax error that handleRequestError turns into a 500. The ladder routes
// pass caller-supplied ids (body or query string) straight here, without the
// validateUUID the account path handlers run first.
func (h *Handler) requireAccountAccess(ctx context.Context, session *Session, accountID string) (*config.CloudAccount, error) {
	if !uuidRegex.MatchString(accountID) {
		return nil, NewClientError(400, "invalid account id: must be a valid UUID")
	}
	account, err := h.config.GetCloudAccount(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	if account == nil {
		return nil, errNotFound
	}

	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return account, nil
	}
	if !allowed.Allows(account.ID, account.Name) {
		return nil, errNotFound
	}
	return account, nil
}

// requirePlanAccess fetches the plan's associated accounts and rejects with
// errNotFound when the session's allowed_accounts list doesn't intersect
// with any of them. Admin / unrestricted sessions pass through unchanged.
// Plans with no account assignments are hidden from scoped users, the safe
// default when we can't attribute the plan to a specific account.
//
// This is the READ gate: one in-scope account is enough to see the plan.
// Mutations must use requirePlanMutationAccess instead.
//
// requirePermission must fire first; the session it returns is what the
// caller passes here. This is the plan-level analog of requireAccountAccess
// and is used by the plans/purchases/ri-exchange per-record scoping.
func (h *Handler) requirePlanAccess(ctx context.Context, session *Session, planID string) error {
	allowed, accounts, err := h.planScopeInputs(ctx, session, planID)
	if err != nil || allowed.AllowsAll() {
		return err
	}
	for i := range accounts {
		if allowed.Allows(accounts[i].ID, accounts[i].Name) {
			return nil
		}
	}
	return errNotFound
}

// requirePlanMutationAccess is the write gate: every account on the plan must
// be in the session's scope, otherwise a user scoped to [A] could change or
// delete a plan that also buys for account B. Plans with no accounts are
// hidden from scoped users, as in requirePlanAccess. Refusals are errNotFound.
func (h *Handler) requirePlanMutationAccess(ctx context.Context, session *Session, planID string) error {
	allowed, accounts, err := h.planScopeInputs(ctx, session, planID)
	if err != nil || allowed.AllowsAll() {
		return err
	}
	if len(accounts) == 0 {
		return errNotFound
	}
	for i := range accounts {
		if !allowed.Allows(accounts[i].ID, accounts[i].Name) {
			return errNotFound
		}
	}
	return nil
}

// planScopeInputs resolves the session's scope and, only for scoped sessions,
// the plan's accounts.
func (h *Handler) planScopeInputs(ctx context.Context, session *Session, planID string) (auth.AccountScope, []config.CloudAccount, error) {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return allowed, nil, fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return allowed, nil, nil
	}
	accounts, err := h.config.GetPlanAccounts(ctx, planID)
	if err != nil {
		return allowed, nil, fmt.Errorf("failed to get plan accounts: %w", err)
	}
	return allowed, accounts, nil
}

// requirePlanAccountsAccess guards BOTH axes of a plan↔account association
// write (PUT /api/plans/:id/accounts, issue #1769): the plan being re-pointed
// and every account being attached to it.
//
// Checking only one axis leaves the other open. Without the plan check a
// scoped caller can re-point a plan whose accounts are all outside their
// scope; without the per-account check they can attach an account they have
// no entitlement to onto a plan they legitimately hold. A plan's account set
// decides which accounts that plan buys commitments for, so either half
// redirects purchasing.
//
// Unrestricted callers short-circuit in both helpers BEFORE
// any store lookup, mirroring requireExecutionAccess: an admin/API-key
// session must not pay for (or need fixtures for) reads it cannot be refused
// by. Empty and "*" allow-lists are unrestricted at exactly this seam
// (getAccountScope → AccountScope.AllowsAll), so "empty means all accounts"
// is handled in one place rather than re-derived here.
//
// Refusals are the enumeration-safe errNotFound from requireAccountAccess /
// requirePlanAccess rather than a 403, so a scoped caller cannot use this
// endpoint to confirm that a plan or an account exists.
//
// requirePermission must fire first; pass it the session that returned.
func (h *Handler) requirePlanAccountsAccess(ctx context.Context, session *Session, planID string, accountIDs []string) error {
	if err := h.requirePlanMutationAccess(ctx, session, planID); err != nil {
		return err
	}
	return h.requireAccountsAccess(ctx, session, accountIDs)
}

// requireAccountsAccess is requireAccountAccess over a batch: every account
// must be in the session's scope or the whole batch is refused with
// errNotFound. Unrestricted callers return before any store lookup.
func (h *Handler) requireAccountsAccess(ctx context.Context, session *Session, accountIDs []string) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	for _, aid := range accountIDs {
		if _, err := h.requireAccountAccess(ctx, session, aid); err != nil {
			return err
		}
	}
	return nil
}

// validatePurchaseRecommendationScope returns a 400 client error when any
// recommendation in the batch targets an account the session can't access.
// Admin / unrestricted sessions pass through. Recommendations with a nil
// CloudAccountID are rejected when the session is scoped — we can't
// attribute them, and silently letting them through would bypass the filter.
//
// The decision is on CloudAccountID itself, the internal account ID the
// purchase resolves its credentials from (GetCloudAccount), and never on a
// display name looked up for it. External IDs are free text an account editor
// sets, so a name keyed by identifier can belong to a different account than
// the one the purchase acts on.
func (h *Handler) validatePurchaseRecommendationScope(ctx context.Context, session *Session, recs []config.RecommendationRecord) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	for i := range recs {
		rec := recs[i]
		if rec.CloudAccountID == nil {
			return NewClientError(400, fmt.Sprintf("recommendation %d has no cloud_account_id; scoped users cannot execute unattributed recommendations", i))
		}
		id := *rec.CloudAccountID
		if !allowed.Allows(id, "") {
			return NewClientError(403, fmt.Sprintf("recommendation %d targets account %s which is outside your allowed_accounts", i, id))
		}
	}
	return nil
}

// requireExecutionAccess rejects with errNotFound unless every cloud account
// the execution can buy in (executionAccounts) is inside the session's
// allowed_accounts (issue #92). Used by the approve/cancel/retry/revoke and
// pause/resume/run/delete handlers that key on executionID. Returns
// errNotFound when the execution itself doesn't exist, so probing can't
// distinguish "no such execution" from "you can't see it".
//
// Short-circuits for admin / unrestricted sessions BEFORE the
// GetExecutionByID fetch to keep those happy-paths free of the extra store
// round-trip (and to keep existing unit-test fixtures for admin operations
// working without adding execution mocks).
func (h *Handler) requireExecutionAccess(ctx context.Context, session *Session, executionID string) error {
	scope, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if scope.AllowsAll() {
		return nil
	}
	execution, err := h.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) || (err == nil && execution == nil) {
		return errNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to get execution: %w", err)
	}
	return h.requireExecutionInScope(ctx, scope, execution, nil)
}

// requireExecutionInScope returns errNotFound unless the scope allows every
// account in executionAccounts(execution). An empty set is a deny: an
// execution that cannot be attributed must not be actionable by a scoped
// user. planAccounts memoises GetPlanAccounts for list callers; nil disables
// it.
func (h *Handler) requireExecutionInScope(ctx context.Context, scope auth.AccountScope, execution *config.PurchaseExecution, planAccounts map[string][]config.CloudAccount) error {
	if scope.AllowsAll() {
		return nil
	}
	accounts, err := h.executionAccounts(ctx, execution, planAccounts)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		return errNotFound
	}
	for i := range accounts {
		if !scope.Allows(accounts[i].ID, accounts[i].Name) {
			return errNotFound
		}
	}
	return nil
}

// executionAccounts returns every cloud account the execution can buy in:
//   - CloudAccountID set: that account only (a fan-out child or a
//     single-account execution).
//   - otherwise PlanID set: all of the plan's accounts, since a plan-level
//     parent fans out to every one of them (purchase.Manager.executeScopeAware).
//   - otherwise (an ad-hoc web purchase, plan_id NULL): the account of every
//     recommendation. One without an account is unattributable, so the whole
//     execution is refused with errNotFound.
//
// An account ID with no cloud_accounts row comes back without a name, so it
// can only match an ID entry in the scope.
func (h *Handler) executionAccounts(ctx context.Context, execution *config.PurchaseExecution, planAccounts map[string][]config.CloudAccount) ([]config.CloudAccount, error) {
	if execution.CloudAccountID != nil {
		a, err := h.lookupCloudAccount(ctx, *execution.CloudAccountID)
		if err != nil {
			return nil, err
		}
		return []config.CloudAccount{a}, nil
	}
	if execution.PlanID != "" {
		if cached, ok := planAccounts[execution.PlanID]; ok {
			return cached, nil
		}
		accounts, err := h.config.GetPlanAccounts(ctx, execution.PlanID)
		if err != nil {
			return nil, fmt.Errorf("failed to get plan accounts: %w", err)
		}
		if planAccounts != nil {
			planAccounts[execution.PlanID] = accounts
		}
		return accounts, nil
	}
	return h.recommendationAccounts(ctx, execution.Recommendations)
}

// recommendationAccounts returns the distinct accounts of recs, or
// errNotFound when any recommendation has no account.
func (h *Handler) recommendationAccounts(ctx context.Context, recs []config.RecommendationRecord) ([]config.CloudAccount, error) {
	seen := make(map[string]bool, len(recs))
	accounts := make([]config.CloudAccount, 0, len(recs))
	for i := range recs {
		id := recs[i].CloudAccountID
		if id == nil || *id == "" {
			return nil, errNotFound
		}
		if seen[*id] {
			continue
		}
		seen[*id] = true
		a, err := h.lookupCloudAccount(ctx, *id)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

// lookupCloudAccount resolves an account ID for a scope check; a missing row
// yields an ID-only account rather than an error (see executionAccounts).
func (h *Handler) lookupCloudAccount(ctx context.Context, id string) (config.CloudAccount, error) {
	a, err := h.config.GetCloudAccount(ctx, id)
	if err != nil {
		return config.CloudAccount{}, fmt.Errorf("accounts: %w", err)
	}
	if a == nil {
		return config.CloudAccount{ID: id}, nil
	}
	return *a, nil
}
