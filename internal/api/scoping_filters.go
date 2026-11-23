package api

import (
	"context"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// resolveAccountFilterIDs maps requested cloud_accounts UUIDs to the
// (uuid, provider, external_id) data needed for the dual-column purchase-history
// filter. purchase_history rows carry either the UUID FK (cloud_account_id) or
// the cloud-provider external number (account_id) and frequently only one of
// them, so a UUID-only WHERE silently drops the external-only rows. This
// resolver loads cloud_accounts once (reusing ListCloudAccounts), then returns
// the UUIDs unchanged plus the matched accounts' external ids grouped by
// provider.
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
	for i := range accounts {
		a := accounts[i]
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
	return unscopedAccountFilter(accounts, accountID)
}

// unscopedAccountFilter is the pure half of resolveSingleAccountFilterIDs: it
// maps one non-empty account identifier onto the dual-column filter using an
// already-loaded cloud_accounts list.
func unscopedAccountFilter(accounts []config.CloudAccount, accountID string) (uuids []string, externalIDsByProvider map[string][]string) {
	for i := range accounts {
		a := &accounts[i]
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
