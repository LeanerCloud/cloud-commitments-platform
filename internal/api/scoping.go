package api

import (
	"context"
	"errors"
	"fmt"

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
func (h *Handler) requireAccountAccess(ctx context.Context, session *Session, accountID string) (*config.CloudAccount, error) {
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
// Plans with no account assignments are hidden from scoped users — the safe
// default when we can't attribute the plan to a specific account.
//
// requirePermission must fire first; the session it returns is what the
// caller passes here. This is the plan-level analog of requireAccountAccess
// and is used by the plans/purchases/ri-exchange per-record scoping.
func (h *Handler) requirePlanAccess(ctx context.Context, session *Session, planID string) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	accounts, err := h.config.GetPlanAccounts(ctx, planID)
	if err != nil {
		return fmt.Errorf("failed to get plan accounts: %w", err)
	}
	for _rvc := range accounts {
		acct := accounts[_rvc]
		if allowed.Allows(acct.ID, acct.Name) {
			return nil
		}
	}
	return errNotFound
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
// The scope is resolved once and unrestricted callers short-circuit BEFORE
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
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	if err := h.requirePlanAccess(ctx, session, planID); err != nil {
		return err
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
func (h *Handler) validatePurchaseRecommendationScope(ctx context.Context, session *Session, recs []config.RecommendationRecord) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	nameByID := h.resolveAccountNamesByID(ctx)
	for i := range recs {
		rec := recs[i]
		if rec.CloudAccountID == nil {
			return NewClientError(400, fmt.Sprintf("recommendation %d has no cloud_account_id; scoped users cannot execute unattributed recommendations", i))
		}
		id := *rec.CloudAccountID
		if !allowed.Allows(id, nameByID[id]) {
			return NewClientError(403, fmt.Sprintf("recommendation %d targets account %s which is outside your allowed_accounts", i, id))
		}
	}
	return nil
}

// requireExecutionAccess rejects with errNotFound when the execution's plan's
// associated accounts don't intersect with the session's allowed_accounts.
// Convenience wrapper around requirePlanAccess for the pause/resume/run/
// delete/details handlers that key on executionID. Returns errNotFound when
// the execution itself doesn't exist, so unauthenticated probing can't
// distinguish "no such execution" from "you can't see it".
//
// Short-circuits for admin / unrestricted sessions BEFORE the
// GetExecutionByID fetch to keep those happy-paths free of the extra store
// round-trip (and to keep existing unit-test fixtures for admin operations
// working without adding execution mocks).
func (h *Handler) requireExecutionAccess(ctx context.Context, session *Session, executionID string) error {
	allowed, err := h.getAccountScope(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to get allowed accounts: %w", err)
	}
	if allowed.AllowsAll() {
		return nil
	}
	execution, err := h.config.GetExecutionByID(ctx, executionID)
	if errors.Is(err, config.ErrNotFound) {
		return errNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to get execution: %w", err)
	}
	return h.requirePlanAccess(ctx, session, execution.PlanID)
}

// resolveAccountFilterIDs maps requested cloud_accounts UUIDs to the
// (uuid, provider, external_id) data needed for the dual-column purchase-history
// filter. purchase_history rows carry either the UUID FK (cloud_account_id) or
// the cloud-provider external number (account_id) and frequently only one of
// them, so a UUID-only WHERE silently drops the external-only rows. This
// resolver loads cloud_accounts once (reusing ListCloudAccounts, the same source
// as resolveAccountNamesByID), then returns the UUIDs unchanged plus the matched
// accounts' external ids grouped by provider.
//
// The external ids are grouped by provider so the downstream predicate compares
// each external number only against rows of its own provider
// ((provider = p AND account_id = ANY(...))). Without this, an external number
// reused across providers (aws/123 vs azure/123) would leak the wrong rows when
// the filter omits provider.
//
// Only UUIDs that resolve to a known cloud_accounts row contribute an external
// id, so a caller cannot inject an arbitrary external id. Unknown UUIDs are
// still returned in the uuid set so the cloud_account_id half of the predicate
// matches any rows that happen to carry them.
//
// Returns (uuids, nil) when uuids is empty or the account load fails — the
// dual-column predicate then degrades to UUID-only matching, no worse than the
// pre-fix behavior.
func (h *Handler) resolveAccountFilterIDs(ctx context.Context, uuids []string) (resolvedUUIDs []string, externalIDsByProvider map[string][]string) {
	if len(uuids) == 0 {
		return uuids, nil
	}
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return uuids, nil
	}
	type provExt struct{ provider, externalID string }
	byUUID := make(map[string]provExt, len(accounts))
	for _rvc := range accounts {
		a := accounts[_rvc]
		if a.ExternalID != "" {
			byUUID[a.ID] = provExt{provider: a.Provider, externalID: a.ExternalID}
		}
	}
	for _, u := range uuids {
		pe, ok := byUUID[u]
		if !ok {
			continue
		}
		externalIDsByProvider = addExternalIDForProvider(externalIDsByProvider, pe.provider, pe.externalID)
	}
	return uuids, externalIDsByProvider
}

// addExternalIDForProvider appends externalID to the provider's slice in m,
// allocating m and the slice lazily and skipping duplicates so each provider's
// ANY() bind has no repeated values. Returns the (possibly newly-allocated) map.
func addExternalIDForProvider(m map[string][]string, provider, externalID string) map[string][]string {
	if m == nil {
		m = make(map[string][]string)
	}
	for _, e := range m[provider] {
		if e == externalID {
			return m
		}
	}
	m[provider] = append(m[provider], externalID)
	return m
}

// resolveSingleAccountFilterIDs resolves a single account identifier (the
// legacy singular `account_id` param, which the frontend populates with the
// top-bar chip's cloud_accounts UUID) into the dual-column filter inputs:
//
//   - Known account UUID: returned in the uuid set, plus its external id (if
//     the account has one) grouped under the account's provider. Both columns
//     are then matched, with the external half scoped to that provider.
//   - Unknown value: treated as an opaque external account number (the pre-UUID
//     call shape, e.g. a raw AWS account number) so legacy single-account
//     callers keep working; it is NOT placed in the uuid set so a raw external
//     number is never compared against cloud_account_id UUIDs. Its provider is
//     unknown, so it is grouped under the "" key, which the predicate treats as
//     an unconstrained-provider match (legacy behavior preserved).
//
// Empty input returns nil maps (no account filter). A cloud_accounts load
// failure falls back to treating the value as an external id (no worse than the
// pre-fix behavior); per-record allowed_accounts scoping still applies
// downstream.
func (h *Handler) resolveSingleAccountFilterIDs(ctx context.Context, accountID string) (uuids []string, externalIDsByProvider map[string][]string) {
	if accountID == "" {
		return nil, nil
	}
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return nil, map[string][]string{"": {accountID}}
	}
	for _rvc := range accounts {
		a := accounts[_rvc]
		if a.ID == accountID {
			// Known UUID: match cloud_account_id by UUID and, when present,
			// account_id by the resolved external number scoped to its provider.
			if a.ExternalID != "" {
				return []string{accountID}, map[string][]string{a.Provider: {a.ExternalID}}
			}
			return []string{accountID}, nil
		}
	}
	// Not a known UUID: treat the value as an external account number with an
	// unknown provider ("" key = unconstrained provider match).
	return nil, map[string][]string{"": {accountID}}
}

// intersectAccountFilterScope narrows a caller-supplied dual-column account
// filter (filterUUIDs, filterExternalIDsByProvider — e.g. the resolved
// account_ids/account_id query params) down to the accounts the session's
// allowed_accounts scope permits.
//
// This is the single choke point for the "retrofit only guards the
// no-explicit-filter branch" defect (issue #99): resolveDashboardAccountScope
// previously fell back to resolveAllowedAccountScope only when the caller
// supplied no filter at all, so a restricted session that DID supply an
// account_ids/account_id filter got that filter unintersected — any account
// the client named came straight through, in or out of allowed_accounts. Every
// resolver that turns a client-controlled account filter into a query-ready
// scope must run it through here rather than re-deriving the guard locally.
//
//   - Unrestricted session (resolveAllowedAccountScope's AllowsAll() case,
//     signaled by a nil uuid slice): the caller-supplied filter passes through
//     unchanged, including nil/nil for "no filter -> all accounts".
//   - Restricted session, no caller-supplied filter: narrows to the full
//     allowed_accounts scope (the pre-existing no-filter behavior).
//   - Restricted session, explicit caller-supplied filter: narrows to the
//     INTERSECTION of the two sets. An account named in the filter but
//     outside allowed_accounts is dropped rather than honored. An empty
//     intersection returns the non-nil-but-empty uuid sentinel that
//     fetchCommitmentPurchases / GetActivePurchaseHistory callers already
//     treat as "match nothing" (issue #956).
func (h *Handler) intersectAccountFilterScope(
	ctx context.Context,
	session *Session,
	filterUUIDs []string,
	filterExternalIDsByProvider map[string][]string,
) (uuids []string, externalIDsByProvider map[string][]string, err error) {
	allowedUUIDs, allowedExternalIDsByProvider, err := h.resolveAllowedAccountScope(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	if allowedUUIDs == nil {
		// resolveAllowedAccountScope's (nil, nil, nil) sentinel for an
		// unrestricted/admin session: keep the caller-supplied filter as-is.
		return filterUUIDs, filterExternalIDsByProvider, nil
	}

	if len(filterUUIDs) == 0 && len(filterExternalIDsByProvider) == 0 {
		// No explicit filter from the caller: the effective scope is the full
		// allowed_accounts set.
		return allowedUUIDs, allowedExternalIDsByProvider, nil
	}

	uuids, externalIDsByProvider = intersectDualColumnScope(
		filterUUIDs, filterExternalIDsByProvider,
		allowedUUIDs, allowedExternalIDsByProvider,
	)
	return uuids, externalIDsByProvider, nil
}

// intersectDualColumnScope computes the AND of two dual-column account
// filters: an id (UUID, or external id under its provider bucket) survives
// only when present on both sides. The uuid half is a straightforward set
// intersection because both sides are always produced by
// resolveAccountFilterIDs (or resolveAllowedAccountScope, its caller) from
// the same cloud_accounts source, so a given account resolves to the same
// UUID on both sides.
//
// The external-id half needs one extra step for the "" (unknown-provider)
// bucket. filterExternalIDsByProvider can carry a "" entry --
// resolveSingleAccountFilterIDs groups a raw external number under "" when
// the value isn't a known UUID (or the cloud_accounts load failed) -- but
// allowedExternalIDsByProvider (built from resolveAccountFilterIDs, which
// only ever groups by a *resolved* account's real provider) never has a ""
// key. A plain per-provider lookup would therefore always miss the ""
// bucket and zero out an otherwise-in-scope legacy external-number filter
// (CodeRabbit #374). Since "" means "provider not known", a "" entry is
// matched against every allowed provider's ids and, on a hit, re-grouped
// under that real provider so the downstream dual-column predicate still
// scopes it correctly.
//
// The returned uuid slice is always non-nil (even when empty) so callers can
// rely on the "non-nil-but-empty means scoped to zero accounts" sentinel
// documented on resolveAllowedAccountScope. The returned map is nil when no
// external id survives the intersection.
func intersectDualColumnScope(
	filterUUIDs []string, filterExternalIDsByProvider map[string][]string,
	allowedUUIDs []string, allowedExternalIDsByProvider map[string][]string,
) (uuids []string, externalIDsByProvider map[string][]string) {
	uuids = make([]string, 0, len(filterUUIDs))
	for _, id := range filterUUIDs {
		if stringInSlice(id, allowedUUIDs) {
			uuids = append(uuids, id)
		}
	}

	for provider, ids := range filterExternalIDsByProvider {
		for _, id := range ids {
			if provider != "" {
				if stringInSlice(id, allowedExternalIDsByProvider[provider]) {
					externalIDsByProvider = addExternalIDForProvider(externalIDsByProvider, provider, id)
				}
				continue
			}
			// Unknown-provider bucket: narrow to whichever allowed provider(s)
			// actually own this id.
			for allowedProvider, allowedIDs := range allowedExternalIDsByProvider {
				if stringInSlice(id, allowedIDs) {
					externalIDsByProvider = addExternalIDForProvider(externalIDsByProvider, allowedProvider, id)
				}
			}
		}
	}
	return uuids, externalIDsByProvider
}

// a map from account identifier → display name. The map is keyed by BOTH
// the internal UUID (CloudAccount.ID) and the cloud-provider external ID
// (CloudAccount.ExternalID, e.g. an AWS account number or Azure subscription
// ID). Dual-keying is necessary because different record types use different
// identifiers: recommendation records carry the UUID (cloud_account_id FK)
// while legacy purchase_history rows carry the external ID (account_id
// VARCHAR). Without both keys, the name lookup always misses for one path.
//
// Returns an empty map on error; callers fall through to ID-only matching,
// which is still safe (AccountScope.Allows falls back to ID comparison when
// the name is empty).
func (h *Handler) resolveAccountNamesByID(ctx context.Context) map[string]string {
	accounts, err := h.config.ListCloudAccounts(ctx, config.CloudAccountFilter{})
	if err != nil {
		return map[string]string{}
	}
	// Allocate 2x capacity since each account contributes up to two keys.
	nameByID := make(map[string]string, len(accounts)*2)
	for _rvc := range accounts {
		a := accounts[_rvc]
		nameByID[a.ID] = a.Name
		if a.ExternalID != "" {
			nameByID[a.ExternalID] = a.Name
		}
	}
	return nameByID
}
