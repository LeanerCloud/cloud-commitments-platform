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
