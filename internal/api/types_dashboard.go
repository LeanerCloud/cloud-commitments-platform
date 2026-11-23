package api

import (
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// DashboardSummaryResponse holds the dashboard summary data.
type DashboardSummaryResponse struct {
	ByService map[string]ServiceSavings `json:"by_service"`
	// CurrentCoverage is null when there is neither committed nor potential
	// savings to measure (0/0), instead of a fabricated 100% (issue #72).
	CurrentCoverage         *float64 `json:"current_coverage"`
	PotentialMonthlySavings float64  `json:"potential_monthly_savings"`
	TotalRecommendations    int      `json:"total_recommendations"`
	ActiveCommitments       int      `json:"active_commitments"`
	// CommittedMonthly is the estimated monthly savings of the active
	// commitments (the sum of their EstimatedSavings), not their cost (#1709).
	CommittedMonthly float64 `json:"committed_monthly"`
	// ExcludedLegacyEstimateCount is how many of ActiveCommitments are Azure
	// rows with legacy-estimate amounts (issue #669). Their dollars are not in
	// CommittedMonthly, YTDSavings or the per-service current savings.
	ExcludedLegacyEstimateCount int     `json:"excluded_legacy_estimate_count"`
	TargetCoverage              float64 `json:"target_coverage"`
	YTDSavings                  float64 `json:"ytd_savings"`
}

// ServiceSavings holds savings data for a service.
type ServiceSavings struct {
	PotentialSavings float64 `json:"potential_savings"`
	CurrentSavings   float64 `json:"current_savings"`
}

// InventoryCommitment is one row in the per-commitment Inventory &
// Coverage view (issue #340 deferred sub-task — "Active commitments").
// Aggregated from PurchaseHistoryRecord rows that are still within
// their term; the inventory endpoint filters out expired commitments
// before responding.
//
// ID is `{account_id}:{purchase_id}` so the row is uniquely identifiable
// in the JSON payload without a DB schema change — purchase_id alone
// is unique within an account but not globally across the table.
//
// Status is always `"active"` today (the handler drops expired rows).
// The field stays in the response shape so a future "expiring soon"
// sub-state has a slot without a breaking API change.
type InventoryCommitment struct {
	StartDate time.Time `json:"start_date"`
	EndDate   time.Time `json:"end_date"`
	// MonthlyCost is nil when the source purchase_history row has a NULL
	// monthly_cost (provider did not return a monthly breakdown). The
	// frontend renders "--" for nil and "$X.XX" when non-nil.
	MonthlyCost   *float64 `json:"monthly_cost"`
	UpfrontCost   *float64 `json:"upfront_cost"`
	Region        string   `json:"region"`
	AccountName   string   `json:"account_name,omitempty"`
	Service       string   `json:"service"`
	ResourceType  string   `json:"resource_type,omitempty"`
	AccountID     string   `json:"account_id"`
	Status        string   `json:"status"`
	ID            string   `json:"id"`
	PaymentOption string   `json:"payment_option,omitempty"`
	Provider      string   `json:"provider"`
	// AmountBasis is "legacy_window" when the dollar amounts are a legacy
	// estimate (issue #669); omitted for normal rows.
	AmountBasis      config.PurchaseAmountBasis `json:"amount_basis,omitempty"`
	TermYears        int                        `json:"term_years"`
	EstimatedSavings float64                    `json:"estimated_savings"`
	Count            int                        `json:"count"`
}

// InventoryCommitmentsResponse is the envelope returned by
// GET /api/inventory/commitments. Commitments is always a slice — never
// nil — so the frontend can rely on `resp.commitments.length` without
// a null check.
type InventoryCommitmentsResponse struct {
	Commitments []InventoryCommitment `json:"commitments"`
}

// CoverageServiceRow is one service row within a provider's coverage
// section. CoveredMonthly is the sum of active-commitment MonthlyCost
// values for the (provider, service) pair. OnDemandMonthly is the sum
// of recommendation Savings values — i.e. the portion of on-demand
// spend that is NOT yet committed. CoveragePct is nil when both sums
// are zero (no usage detected), not 0, to preserve the "absent"
// semantic per feedback_nullable_not_zero.
type CoverageServiceRow struct {
	CoveragePct     *float64 `json:"coverage_pct"`
	CoveredMonthly  *float64 `json:"covered_monthly"`
	Service         string   `json:"service"`
	OnDemandMonthly float64  `json:"on_demand_monthly"`
}

// ProviderCoverageSection is the per-provider block returned by
// GET /api/inventory/coverage. Services is nil (not []) when the
// provider has no usage data, which the frontend uses to distinguish
// "no usage detected" from "usage exists but all services are 0%".
// OverallCoveragePct follows the same null-vs-zero contract as
// CoverageServiceRow.CoveragePct.
type ProviderCoverageSection struct {
	OverallCoveragePct *float64             `json:"overall_coverage_pct"`
	Provider           string               `json:"provider"`
	Services           []CoverageServiceRow `json:"services"`
}

// CoverageBreakdownResponse is the envelope returned by
// GET /api/inventory/coverage.
type CoverageBreakdownResponse struct {
	Providers []ProviderCoverageSection `json:"providers"`
}

// UpcomingPurchaseResponse holds upcoming purchase data.
type UpcomingPurchaseResponse struct {
	Purchases []UpcomingPurchase `json:"purchases"`
}

// UpcomingPurchase represents one upcoming planned purchase — a pending
// purchase_executions row whose scheduled_date hasn't fired yet, joined
// to its parent PurchasePlan for display.
//
// The dashboard's Cancel button targets ExecutionID via
// DELETE /api/purchases/planned/{id} (api.deletePlannedPurchase). That
// cancels THIS scheduled instance and also DISABLES its plan (issue #774;
// DeletePlannedPurchaseResponse.PlanDisabled reports it): the plan row is
// kept, but the scheduler creates no further instances until someone
// re-enables it on the Plans page (issue #312 corrected this comment, which
// used to say the plan stays active). PlanID is exposed as context (e.g. for
// linking to the plan's settings) and is NOT what destructive action
// endpoints should target. PR #207 + #213 history: an earlier iteration
// routed Cancel to api.deletePlan(planID) which deleted the entire plan;
// that was too aggressive.
type UpcomingPurchase struct {
	// CreatedByUserID propagates the underlying execution's
	// created_by_user_id so the dashboard widget can apply the same
	// creator-scope ownership gate the Plans page uses (issue #950).
	// Without it the widget renders a "Cancel" button on every row
	// while the backend now 403s for non-owners -- a UX hole that
	// surfaces as a confusing toast on click. Mirrors the field on
	// PlannedPurchase / PurchaseHistoryEntry. omitempty so legacy
	// NULL-creator rows keep the JSON shape they had pre-fix.
	CreatedByUserID *string `json:"created_by_user_id,omitempty"`
	ExecutionID     string  `json:"execution_id"`
	PlanID          string  `json:"plan_id"`
	PlanName        string  `json:"plan_name"`
	ScheduledDate   string  `json:"scheduled_date"`
	Provider        string  `json:"provider"`
	Service         string  `json:"service"`
	// Providers lists a bound plan's group providers (#1746); omitted otherwise.
	Providers        []string `json:"providers,omitempty"`
	StepNumber       int      `json:"step_number"`
	TotalSteps       int      `json:"total_steps"`
	EstimatedSavings float64  `json:"estimated_savings"`
	// HasRecommendations is false for a step that carries no recommendations,
	// whose EstimatedSavings is then a placeholder, not data (platform#609).
	HasRecommendations bool `json:"has_recommendations"`
}

// PlannedPurchasesResponse holds the list of planned purchases.
type PlannedPurchasesResponse struct {
	Purchases []PlannedPurchase `json:"purchases"`
}
