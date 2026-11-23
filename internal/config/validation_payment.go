package config

// NormalizePaymentOption maps a raw payment-option token onto the canonical
// token the given provider semantically models (see ValidPaymentOptionsByProvider).
// It exists so the recommendation-emission boundary (see
// internal/scheduler/scheduler.go:convertRecommendations) can defensively
// canonicalize any AWS-style token that a code path or a globally-default
// payment-option setting might stamp onto a non-AWS rec, before the rec is
// persisted and later validated against the provider-canonical set.
//
// Returns (canonical, true) when raw is already canonical for the provider
// or has an unambiguous canonical mapping. Returns ("", false) for unknown
// providers and (raw, false) for tokens that have no canonical mapping on
// the given provider (e.g. an Azure/GCP-style "upfront" on AWS) — callers
// can use ok=false to surface the unmapped token at the next validator
// boundary. Per-provider mapping:
//
//   - AWS  : passthrough (AWS already speaks the three-tier set).
//   - Azure: all-upfront → upfront, no-upfront → monthly,
//     partial-upfront → monthly (no semantic equivalent — coerce to the
//     no-upfront tier, CUDly's default billing schedule for Azure, rather
//     than drop the rec; caller may log). Coercing to upfront here would
//     silently bill an all-upfront schedule the caller never chose: the
//     canonical token is persisted onto the execution and copied into
//     common.Recommendation.PaymentOption (internal/purchase/execution.go),
//     and the reservation-purchase billingPlan wiring being added in
//     #1495/#1502 maps only upfront/monthly tokens and hard-errors on
//     partial-upfront, so landing on monthly keeps the rec on the
//     no-upfront schedule instead of an irreversible upfront charge —
//     Azure does not allow changing a reservation's billing frequency
//     after purchase.
//   - GCP  : all-upfront → monthly, no-upfront → monthly,
//     partial-upfront → monthly, upfront → monthly (GCP CUDs are
//     inherently monthly-billed — every non-monthly token collapses to the
//     one billing plan GCP actually models). The collapse from "upfront" to
//     "monthly" is what makes the existing
//     providers/gcp/services/computeengine/client.go:804 stamp safe: the
//     scheduler.convertRecommendations boundary coerces it once before
//     persistence so the rec carries the canonical token downstream.
//
// The partial-upfront coercion is deliberate on both Azure and GCP: dropping
// the rec would be a silent data loss for the user, while coercing to the
// closest billing model the provider offers preserves the rec without
// changing the total the caller committed to. Microsoft documents that
// choice as cost-neutral: "The total cost of up-front and monthly
// reservations is the same and you don't pay any extra fees when you choose
// to pay monthly"
// (https://learn.microsoft.com/en-us/azure/cost-management-billing/reservations/prepare-buy-reservation).
// Caveat from the same page: monthly payments are NOT offered for SUSE Linux
// reservations, Red Hat plans, Azure Red Hat OpenShift licenses, or
// pre-purchase plans, so for those products the coerced token can make Azure
// reject the purchase. That is the intended failure mode — a loud purchase-
// time rejection is preferable to silently committing the caller to an
// upfront charge they never chose and cannot undo.
//
// The caller is expected to log a warning when raw != canonical so an
// operator notices the upstream input bug.
//
// The frontend mirrors this mapping in
// frontend/src/commitmentOptions.ts:normalizePaymentValue (it decides which
// option the plan/purchase dropdowns pre-select); the two must stay in
// lockstep.
//
// Empty raw passes through as ("", true) — callers that distinguish "unset"
// from "invalid" can check the returned bool only when raw is non-empty.
func NormalizePaymentOption(provider, raw string) (string, bool) {
	if _, known := ValidPaymentOptionsByProvider[provider]; !known {
		return "", false
	}
	if raw == "" {
		return "", true
	}
	// Already canonical for this provider: passthrough.
	for _, v := range ValidPaymentOptionsByProvider[provider] {
		if raw == v {
			return raw, true
		}
	}
	// Cross-provider tokens that have a canonical equivalent in the target
	// provider's billing model. Each provider's mapping is delegated to a
	// helper to keep cyclomatic complexity below the project limit.
	if canon, ok := crossProviderPaymentAlias(provider, raw); ok {
		return canon, true
	}
	// Anything else (including Azure/GCP-style tokens on AWS) is left as-is
	// and will surface as a validation error at the next boundary.
	return raw, false
}

// crossProviderPaymentAlias maps an AWS-style (or Azure-style on GCP) token
// onto the target provider's canonical token. Returns (canonical, true) when
// a mapping exists, ("", false) when none does. Split out of
// NormalizePaymentOption purely to keep that function under the project's
// gocyclo limit; the policy lives here.
func crossProviderPaymentAlias(provider, raw string) (string, bool) {
	switch provider {
	case "azure":
		// Azure reservations model both billing plans; coerce AWS-style tokens
		// to the Azure-canonical spelling. partial-upfront has no Azure
		// equivalent — coerce to the no-upfront (monthly) tier, CUDly's
		// default billing schedule, so the rec survives validation rather
		// than dropping silently (caller WARN-logs). Coercing to "upfront"
		// would silently bill an upfront schedule the caller never chose
		// and cannot undo (Azure forbids changing a reservation's billing
		// frequency after purchase), whereas monthly costs the same total.
		// The reservation-purchase billingPlan wiring being added in
		// #1495/#1502 hard-errors on partial-upfront, so only the
		// canonical upfront/monthly tokens survive to the purchase body.
		// Rationale and doc citations: see NormalizePaymentOption above.
		switch raw {
		case "all-upfront":
			return "upfront", true
		case "no-upfront", "partial-upfront":
			return "monthly", true
		}
	case "gcp":
		// GCP commitments are monthly-only — every non-monthly token (AWS-style
		// or the legacy "upfront" some GCP code paths still stamp) collapses
		// to "monthly", the one billing plan GCP semantically models.
		switch raw {
		case "all-upfront", "no-upfront", "partial-upfront", "upfront":
			return "monthly", true
		}
	}
	return "", false
}
