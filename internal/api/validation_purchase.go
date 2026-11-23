package api

import (
	"fmt"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// purchasePaymentSet returns the provider-canonical set of accepted payment
// option tokens for the given (already-lowercased) provider. It derives the
// set from config.ValidPaymentOptionsByProvider so that this boundary and the
// plan-validator share a single source of truth and cannot drift (issue #717).
// Returns nil when provider is unknown.
func purchasePaymentSet(provider string) map[string]bool {
	opts, ok := config.ValidPaymentOptionsByProvider[provider]
	if !ok {
		return nil
	}
	set := make(map[string]bool, len(opts))
	for _, o := range opts {
		set[o] = true
	}
	return set
}

// purchaseTermWhitelist maps a concrete provider to the set of commitment
// terms (in years) that provider accepts. All three clouds offer 1- and
// 3-year reservations. A rec carrying e.g. Term:7 is rejected before
// execution rather than failing opaquely deep in the provider call.
var purchaseTermWhitelist = map[string]map[int]bool{
	"aws":   {1: true, 3: true},
	"azure": {1: true, 3: true},
	"gcp":   {1: true, 3: true},
}

// PaymentAdjustment surfaces a payment-option coercion to the API caller
// (follow-up to #1503): when the web execute path normalizes a caller-supplied
// payment option onto a provider-canonical token that bills on a DIFFERENT
// schedule than what was requested (e.g. Azure "partial-upfront" -> "monthly"),
// the response carries one of these per adjusted rec so the caller sees what
// they requested, what was actually applied, and why, not just the operator
// WARN log. Purely additive visibility: the coercion policy itself lives in
// config.NormalizePaymentOption and is unchanged.
//
// Cross-provider renames of the SAME schedule (Azure "all-upfront" ->
// "upfront", "no-upfront" -> "monthly") are not adjustments: nothing about
// the customer's cash flow changed, so there is nothing to disclose. See
// config.PaymentCoercionChangesSchedule.
type PaymentAdjustment struct {
	Provider string `json:"provider"`
	Service  string `json:"service"`
	// RequestedPaymentOption is the caller's token after trim/lowercase only
	// (case-only differences are not surfaced as adjustments, matching the
	// operator WARN-log semantics).
	RequestedPaymentOption string `json:"requested_payment_option"`
	// AppliedPaymentOption is the provider-canonical token the purchase will
	// actually carry.
	AppliedPaymentOption string `json:"applied_payment_option"`
	Reason               string `json:"reason"`
	// RecIndex is the rec's position in the request's recommendations slice.
	RecIndex int `json:"rec_index"`
}

// validatePurchaseRecommendation validates a single client-supplied
// recommendation before it reaches the cloud purchase SDK. Unlike the
// query-time validateProvider (which permits ""/"all"), the execute path
// requires a concrete provider because each rec triggers a real provider
// call. idx is the rec's position in the request slice, surfaced in the
// error so the caller can point at the offending row. Closes issues #643,
// #717.
//
// Payment-option handling follows the same two-step approach as the
// plan-validator in internal/config/validation.go:
//  1. NormalizePaymentOption coerces any legacy AWS-style token onto the
//     provider-canonical spelling (e.g. "all-upfront" on Azure -> "upfront").
//  2. The provider-canonical set (from config.ValidPaymentOptionsByProvider)
//     rejects anything that has no canonical mapping, with an error that
//     names the provider and lists the accepted tokens.
//
// When step 1 changes the BILLING SCHEDULE (not merely the spelling), the
// returned *PaymentAdjustment describes the coercion so the response can
// surface it to the caller; nil means the payment option was already canonical
// or was respelled onto an equivalent schedule.
func validatePurchaseRecommendation(rec *config.RecommendationRecord, idx int) (*PaymentAdjustment, error) {
	provider := strings.ToLower(strings.TrimSpace(rec.Provider))
	payments := purchasePaymentSet(provider)
	if payments == nil {
		return nil, NewClientError(400, fmt.Sprintf("recommendation %d has invalid provider %q: must be one of aws, azure, gcp", idx, rec.Provider))
	}
	rec.Provider = provider
	rec.Service = strings.TrimSpace(rec.Service)
	if rec.Service == "" {
		return nil, NewClientError(400, fmt.Sprintf("recommendation %d is missing a service", idx))
	}
	if rec.Count <= 0 {
		return nil, NewClientError(400, fmt.Sprintf("recommendation %d has non-positive count: %d", idx, rec.Count))
	}
	if !purchaseTermWhitelist[provider][rec.Term] {
		return nil, NewClientError(400, fmt.Sprintf("recommendation %d has invalid term %d for provider %s: must be 1 or 3", idx, rec.Term, provider))
	}
	// A negative MonthlyCost would let recTotalCommitment's recurring leg
	// subtract from the batch's total commitment, offsetting or masking a
	// real upfront cost and evading a MaxPurchaseAmount cap (adversarial
	// review follow-up to #1210). Nil is fine (no recurring charge); only a
	// present-and-negative value is rejected.
	if rec.MonthlyCost != nil && *rec.MonthlyCost < 0 {
		return nil, NewClientError(400, fmt.Sprintf("recommendation %d has negative monthly cost: %.2f", idx, *rec.MonthlyCost))
	}
	payment := strings.ToLower(strings.TrimSpace(rec.Payment))
	// Coerce any legacy/cross-provider alias before the whitelist check so
	// that callers using old AWS-style tokens are transparently redirected to
	// the canonical token for the target provider. WARN when a real
	// normalization occurs (raw != canonical) so an operator can audit the
	// coercion of this money-affecting field, matching the WARN contract
	// documented on config.NormalizePaymentOption and mirroring the same
	// coerced/uncoerced logging convertRecommendations does at the
	// scheduler's emission boundary (internal/scheduler/scheduler.go).
	//
	// The caller-facing PaymentAdjustment is deliberately NARROWER than the
	// WARN: it is returned only when the rewrite changes the billing schedule
	// itself, not when it merely respells the same schedule in the target
	// provider's vocabulary (Azure "all-upfront" -> "upfront", "no-upfront" ->
	// "monthly"). Every WARN is worth an operator's attention because it means
	// an upstream caller sent a non-canonical token; only a schedule change is
	// worth interrupting the user, whose money is what actually moved. The
	// fan-out purchase modal submits "all-upfront" for Azure buckets by
	// construction (frontend/src/lib/purchase-compatibility.ts), so surfacing
	// renames too would warn on the ordinary Azure upfront purchase and teach
	// users to dismiss the notice that matters.
	var adjustment *PaymentAdjustment
	if normalized, ok := config.NormalizePaymentOption(provider, payment); ok {
		if normalized != payment {
			logging.Warnf("validatePurchaseRecommendation: rec %d (%s/%s) payment option normalized: raw=%q canonical=%q",
				idx, provider, rec.Service, payment, normalized)
		}
		adjustment = paymentAdjustmentFor(idx, provider, rec.Service, payment, normalized)
		payment = normalized
	}
	if !payments[payment] {
		return nil, NewClientError(400, fmt.Sprintf(
			"invalid payment option for %s service: %q (valid for %s: %s)",
			provider, rec.Payment, provider,
			strings.Join(config.ValidPaymentOptionsByProvider[provider], ", "),
		))
	}
	rec.Payment = payment
	return adjustment, nil
}

// paymentAdjustmentFor builds the caller-facing coercion notice for a single
// rec, or returns nil when there is nothing to disclose: either the token was
// left alone, or it was only respelled into the target provider's vocabulary
// for the same billing schedule (see config.PaymentCoercionChangesSchedule).
//
// requested is the caller's token after trim/lowercase; applied is the
// provider-canonical token the purchase will actually carry.
func paymentAdjustmentFor(idx int, provider, service, requested, applied string) *PaymentAdjustment {
	if !config.PaymentCoercionChangesSchedule(requested, applied) {
		return nil
	}
	return &PaymentAdjustment{
		RecIndex:               idx,
		Provider:               provider,
		Service:                service,
		RequestedPaymentOption: requested,
		AppliedPaymentOption:   applied,
		Reason: fmt.Sprintf("payment option %q is not in %s's supported set (%s); the closest supported option %q was applied",
			requested, provider, strings.Join(config.ValidPaymentOptionsByProvider[provider], ", "), applied),
	}
}

// validateCapacityConsistency cross-checks the client-supplied capacity_percent
// against the scaled rec counts so the audit record can't claim a capacity that
// disagrees with what was actually purchased (#647). The frontend scales each
// rec as floor(RecommendedCount * pct / 100); this recomputes that and rejects
// any rec where the scaled Count doesn't match. Recs that don't carry a
// RecommendedCount (0 / absent: legacy callers, single-rec full-capacity
// purchases, retry replays) are skipped — the field is opt-in, so its absence
// means "no claim to verify" rather than a failure. Savings Plans rows are
// skipped too: their count is the placeholder 1 and capacity_percent scales
// the hourly commitment server-side (priceFromStored, #1959).
// capacityPercent is the already-defaulted/bounded value (1..100) from
// validateExecutePurchaseRequest.
func validateCapacityConsistency(recs []config.RecommendationRecord, capacityPercent int) error {
	for i := range recs {
		rec := recs[i]
		if rec.RecommendedCount <= 0 || isSavingsPlanSlug(rec.Service) {
			continue
		}
		expected := rec.RecommendedCount * capacityPercent / 100
		if expected != rec.Count {
			return NewClientError(400, fmt.Sprintf(
				"recommendation %d: count %d is inconsistent with capacity_percent %d%% of recommended_count %d (expected %d)",
				i, rec.Count, capacityPercent, rec.RecommendedCount, expected))
		}
	}
	return nil
}

// isSavingsPlanSlug reports whether service is in the Savings Plans slug
// family (umbrella, hyphenated and dash-free per-type, legacy
// "savings-plans"), case-insensitive. Broader than common.IsSavingsPlan, which
// misses the dash-free per-type slugs.
func isSavingsPlanSlug(service string) bool {
	svc := strings.ToLower(strings.TrimSpace(service))
	return strings.HasPrefix(svc, "savings-plans") || strings.HasPrefix(svc, "savingsplans")
}
