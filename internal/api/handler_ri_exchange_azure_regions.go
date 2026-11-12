package api

import (
	"strings"
)

// targetLocations returns the de-duplicated, canonically lower-cased set of
// target locations, used to populate the Regions dimension of the
// execute:ri-exchange constraint check. Callers must have already validated
// that every target has a non-blank Location.
//
// Azure treats location names case-insensitively and its own APIs return the
// lower-case form, so lower case is the canonical spelling. Normalizing
// before the dedup collapses "EastUS" -- the casing the Azure portal
// displays -- and "eastus" into the single region they actually are, so the
// constraint set names each target region exactly once rather than demanding
// a permission for two spellings of one place.
//
// auth.matchAllRegionsConstraint compares case-insensitively as well, so a
// permission stored in either casing matches either way. Normalizing here
// keeps the two layers agreeing on what a region is instead of leaving the
// constraint set's accuracy resting on the matcher's leniency.
//
// toAzureExchangeTargets sends Azure the raw, un-normalized Location. That
// is not a gap: Azure resolves either casing to the same region, so the
// region authorized here is the region the exchange lands in.
func targetLocations(targets []AzureExchangeTargetBody) []string {
	locations := make([]string, len(targets))
	for i, t := range targets {
		locations[i] = t.Location
	}
	return normalizeRegions(locations)
}

// normalizeRegions canonically lower-cases and trims region names and
// de-duplicates them, preserving first-seen order. Shared by targetLocations
// and exchangeRegions so both sides of an exchange are spelled the same way
// before they reach auth.matchAllRegionsConstraint.
func normalizeRegions(regions []string) []string {
	seen := make(map[string]bool, len(regions))
	out := make([]string, 0, len(regions))
	for _, r := range regions {
		region := strings.ToLower(strings.TrimSpace(r))
		if !seen[region] {
			seen[region] = true
			out = append(out, region)
		}
	}
	return out
}

// unknownRegionConstraint is the Regions entry substituted for a source
// reservation whose region Azure did not report (ExchangeableReservation.Region
// "may be empty for reservations with AppliedScopeType == Shared"), or that is
// absent from the tenant listing altogether.
//
// It is deliberately not a region name: no Azure location is spelled this way,
// so a permission carrying ANY Regions constraint cannot name it and the
// exchange is denied. A permission with NO Regions constraint is unaffected
// (auth.matchAllRegionsConstraint treats an empty permission list as "no
// restriction"), so callers who were never region-scoped are not penalized for
// a reservation Azure described incompletely.
//
// The alternative -- dropping an unreported region from the set -- would make
// "Azure did not tell us where this is" mean "unconstrained", which is exactly
// the fail-open shape of the empty-region defect fixed in PR #1495. Same
// posture as unattributedAccountConstraint on the AccountIDs dimension.
const unknownRegionConstraint = "unknown-region"

// exchangeRegions returns every region an Azure exchange touches: each
// target's location AND each source reservation's own region, normalized and
// de-duplicated into one set.
//
// Both halves belong in the Regions dimension because an exchange mutates
// both. The sources are handed back to Azure and their commitment value is
// consumed; the targets are acquired. Constraining only the targets (the
// pre-fix behavior) let a caller permitted solely in eastus name a
// westeurope reservation as the source of an eastus-targeted exchange: the
// Regions dimension saw only "eastus", requireAzureSourceOwnership keys on the
// reservation's BillingScopeID rather than its region, and no other gate
// consults a source's region at all. The westeurope commitment -- which the
// caller was never authorized to touch -- was consumed and relocated,
// irreversibly.
//
// Source regions come from the tenant listing (owned) rather than the request
// body, because the request names only reservation IDs; Azure is the authority
// on where a reservation lives. A source missing from the listing, or one
// whose Region Azure left empty, contributes unknownRegionConstraint rather
// than nothing -- see that constant for why.
//
// On the execute path requireAzureSourceOwnership has already refused any
// source absent from the listing by the time this runs, so the sentinel there
// means specifically "owned, but Azure reported no region". The missing-source
// branch is kept as a fail-closed default for any future caller that reaches
// this function without that guarantee.
func exchangeRegions(targets []AzureExchangeTargetBody, sources []AzureExchangeSourceBody, owned []azureExchangeableReservation) []string {
	regionByID := make(map[string]string, len(owned))
	for i := range owned {
		regionByID[strings.ToLower(owned[i].ReservationID)] = owned[i].Region
	}

	regions := targetLocations(targets)
	for _, s := range sources {
		region := strings.ToLower(strings.TrimSpace(regionByID[strings.ToLower(s.ReservationID)]))
		if region == "" {
			region = unknownRegionConstraint
		}
		regions = append(regions, region)
	}
	return normalizeRegions(regions)
}
