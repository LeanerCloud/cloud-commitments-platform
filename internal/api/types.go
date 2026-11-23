// Package api provides the HTTP API handlers for the CUDly dashboard.
package api

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/analytics"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/commitmentopts"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/oidc"
)

// RateLimiterInterface defines the interface for rate limiting implementations
// This allows for both in-memory and database-backed rate limiters.
type RateLimiterInterface interface {
	// Allow checks if a request should be allowed based on rate limits
	// Returns (allowed bool, error)
	Allow(ctx context.Context, key string, endpoint string) (bool, error)

	// AllowWithIP is a convenience method for IP-based rate limiting
	AllowWithIP(ctx context.Context, ip string, endpoint string) (bool, error)

	// AllowWithEmail is a convenience method for email-based rate limiting
	AllowWithEmail(ctx context.Context, email string, endpoint string) (bool, error)

	// AllowWithUser is a convenience method for user-based rate limiting
	AllowWithUser(ctx context.Context, userID string, endpoint string) (bool, error)
}

// HandlerConfig holds configuration for the API handler.
type HandlerConfig struct {
	EmailNotifier       email.SenderInterface
	AnalyticsSnapshots  AnalyticsSnapshotStoreInterface
	PurchaseManager     PurchaseManagerInterface
	RateLimiter         RateLimiterInterface
	AuthService         AuthServiceInterface
	CommitmentOpts      CommitmentOptsInterface
	OIDCSigner          oidc.Signer
	ConfigStore         config.StoreInterface
	Scheduler           SchedulerInterface
	CredentialStore     credentials.CredentialStore
	AnalyticsCollector  AnalyticsCollectorInterface
	AnalyticsClient     AnalyticsClientInterface
	Insurance           InsuranceProvider
	DashboardURL        string
	CORSAllowedOrigin   string
	DashboardBucket     string
	OIDCIssuerURL       string
	APIKeySecretARN     string
	EncryptionKeySource string
	// RecommendationSchedule is the deployment's recommendation-collection
	// schedule (RECOMMENDATION_SCHEDULE, set by Terraform). Empty means
	// scheduled collection is off or the deployment predates the variable.
	// It feeds next_collection_at on GET /api/recommendations/freshness.
	RecommendationSchedule string
	EnableDashboard        bool
	// IsLambda is the caller's (Application's) single answer to "am I on
	// Lambda". It gates the background goroutines (RI utilization SWR
	// refresh, stale-execution sweep) that would freeze mid-flight on Lambda.
	// The handler never re-reads the environment itself (issue #110).
	IsLambda bool
	// FireSweepOff is true when Terraform says no fire_scheduled_purchases
	// sweep runs (FIRE_SWEEP_SCHEDULED=false). A delayed approval would then
	// sit in "scheduled" forever, so PUT /api/config refuses a delay (#1588).
	// False when the variable is unset, so older deployments keep saving.
	FireSweepOff bool
}

// CommitmentOptsInterface lets us swap the real *commitmentopts.Service for
// a stub in handler tests without pulling in the probe+store machinery.
type CommitmentOptsInterface interface {
	Get(ctx context.Context) (commitmentopts.Options, error)
	// CachedOptions reads one provider's stored combos without probing.
	CachedOptions(ctx context.Context, provider string) (map[string][]commitmentopts.Combo, bool, error)
	Validate(ctx context.Context, provider, service string, term int, payment string) (bool, error)
}

// AnalyticsClientInterface defines the interface for analytics queries.
//
// accountUUIDs / accountExternalIDsByProvider are the dual-column account
// filter: rows match when cloud_account_id = ANY(accountUUIDs) OR (provider = p
// AND account_id = ANY(accountExternalIDsByProvider[p])). Both nil/empty means
// "all accounts accessible to the caller" (scoping is enforced upstream in the
// handler). The handler resolves the requested account (a top-bar chip UUID)
// into both representations via resolveSingleAccountFilterIDs so rows that carry
// only the external account_id (cloud_account_id NULL) are still aggregated, and
// the external ids stay grouped by provider so a reused external number across
// providers cannot leak the wrong rows (issue #701/#498/#866).
type AnalyticsClientInterface interface {
	QueryHistory(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, provider string, start, end time.Time, interval string) ([]HistoryDataPoint, *HistorySummary, error)
	QueryBreakdown(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, start, end time.Time, dimension string) (map[string]BreakdownValue, error)
}

// AnalyticsCollectorInterface defines the interface for analytics collection.
type AnalyticsCollectorInterface interface {
	Collect(ctx context.Context) error
}

// AnalyticsSnapshotStoreInterface exposes the savings-snapshot time-series for
// the /api/analytics/trends endpoint. It is the read side of the now-wired
// internal/analytics collector. Scoping is the dual-column model: the handler
// resolves the requested account into accountUUIDs + accountExternalIDsByProvider
// (see resolveSingleAccountFilterIDs) and the store ORs both columns so rows
// carrying only one identifier are still matched. Both empty means "all" — the
// handler MUST enforce allowed_accounts scope before passing empty filters.
type AnalyticsSnapshotStoreInterface interface {
	QuerySavings(ctx context.Context, req analytics.QueryRequest) ([]analytics.SavingsSnapshot, error)
	QueryMonthlyTotals(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, months int) ([]analytics.MonthlySummary, error)
	QueryByProvider(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, startDate, endDate time.Time) ([]analytics.ProviderBreakdown, error)
	QueryByService(ctx context.Context, accountUUIDs []string, accountExternalIDsByProvider map[string][]string, provider string, startDate, endDate time.Time) ([]analytics.ServiceBreakdown, error)
}

// HistoryDataPoint represents aggregated historical data.
type HistoryDataPoint struct {
	Timestamp         time.Time          `json:"timestamp"`
	ByService         map[string]float64 `json:"by_service,omitempty"`
	ByProvider        map[string]float64 `json:"by_provider,omitempty"`
	TotalUpfront      *float64           `json:"total_upfront"`
	TotalSavings      float64            `json:"total_savings"`
	PurchaseCount     int                `json:"purchase_count"`
	CumulativeSavings float64            `json:"cumulative_savings"`
}

// BreakdownValue represents savings breakdown by dimension.
type BreakdownValue struct {
	TotalUpfront  *float64 `json:"total_upfront"`
	TotalSavings  float64  `json:"total_savings"`
	PurchaseCount int      `json:"purchase_count"`
	Percentage    float64  `json:"percentage"`
}

// API Response types for type safety

// ConfigResponse holds the configuration response.
type ConfigResponse struct {
	Global         *config.GlobalConfig   `json:"global"`
	SourceIdentity *sourceIdentity        `json:"source_identity,omitempty"`
	SourceCloud    string                 `json:"source_cloud,omitempty"`
	Services       []config.ServiceConfig `json:"services"`
}

// StatusResponse holds a simple status response.
type StatusResponse struct {
	Status string `json:"status"`
}

// DeletePlannedPurchaseResponse is the body of DELETE /api/purchases/planned/{id}.
// PlanDisabled is omitted when the execution has no plan and true when the
// plan was disabled. A plan the caller may not disable fails the request
// (403 before the cancel, 409 if the plan changed after it) instead of
// reporting false (issues #520, #623).
type DeletePlannedPurchaseResponse struct {
	PlanDisabled *bool  `json:"plan_disabled,omitempty"`
	Status       string `json:"status"`
}

// RecommendationsSummary holds aggregate statistics for recommendations.
type RecommendationsSummary struct {
	TotalCount          int     `json:"total_count"`
	TotalMonthlySavings float64 `json:"total_monthly_savings"`
	TotalUpfrontCost    float64 `json:"total_upfront_cost"`
	AvgPaybackMonths    float64 `json:"avg_payback_months"`
}

// RecommendationsResponse holds the recommendations response.
type RecommendationsResponse struct {
	// NextCursor is set only on a paginated request (limit or cursor sent)
	// that has more rows; pass it back as ?cursor= for the next page.
	NextCursor      string                        `json:"next_cursor,omitempty"`
	Recommendations []config.RecommendationRecord `json:"recommendations"`
	Regions         []string                      `json:"regions"`
	Summary         RecommendationsSummary        `json:"summary"`
}

// UsagePoint is a single sample in the per-recommendation usage time
// series surfaced by GET /api/recommendations/:id/detail. The series is
// always ordered by Timestamp ascending. CPUPct/MemPct are 0..100.
//
// Empty in the current implementation: the collector pipeline does not
// yet persist time-series utilization per recommendation. The endpoint
// returns the empty slice with a non-error status so the frontend can
// render a "Usage history not yet available" placeholder rather than a
// broken empty chart. See known_issues/28_recommendations_detail_endpoint.md
// for the full collector wiring follow-up.
type UsagePoint struct {
	Timestamp string  `json:"timestamp"`
	CPUPct    float64 `json:"cpu_pct"`
	MemPct    float64 `json:"mem_pct"`
}

// RecommendationDetailResponse is the per-id payload backing the
// Recommendations row-click drawer. Contract documented in issue #44.
//
// ConfidenceBucket is "low" | "medium" | "high" — server-side mirror of
// the client-side heuristic that previously lived in
// frontend/src/recommendations.ts:confidenceBucketFor. Centralizing it
// server-side lets future provider-specific tuning happen in one place.
//
// ProvenanceNote is a short human-readable string naming the collector
// + the freshness window. Rendered verbatim in the drawer.
type RecommendationDetailResponse struct {
	ID               string       `json:"id"`
	UsageHistory     []UsagePoint `json:"usage_history"`
	ConfidenceBucket string       `json:"confidence_bucket"`
	ProvenanceNote   string       `json:"provenance_note"`
	// HiddenBy is non-nil when the rec is filtered out by an account-service
	// override (issue #214). Each element names one failing dimension:
	// "enabled=false", "engine", "region", "resource_type" or "min_count". The frontend
	// renders a "hidden by your override" banner when this field is present.
	// Absent (null) means the rec is fully visible.
	HiddenBy []string `json:"hidden_by,omitempty"`
}

// PlansResponse holds the purchase plans response.
type PlansResponse struct {
	Plans []PlanWithHealth `json:"plans"`
}

// PlanWithHealth wraps a config.PurchasePlan with its computed health-score
// badge data for the Plans list view (issue #340 follow-up, PR #376). The
// score/factors are derived at read time from the plan's own attributes and
// its recent executions -- see computePlanHealth in plan_health.go -- and
// are never persisted, so they can't drift into the config.PurchasePlan
// struct (and by extension the DynamoDB/Postgres columns backing it).
//
// HealthScore is a pointer so "not computable" stays distinguishable from a
// real score: when the execution counts the score depends on can't be read,
// the field serializes as null and the UI renders an explicit "unknown"
// badge. Any concrete default would be a fabricated number an operator
// could not tell apart from a measured one.
type PlanWithHealth struct {
	HealthScore   *int               `json:"health_score"`
	HealthFactors []PlanHealthFactor `json:"health_factors,omitempty"`
	config.PurchasePlan
}

// CurrentUserResponse holds the current user response.
type CurrentUserResponse struct {
	ID         string   `json:"id"`
	Email      string   `json:"email"`
	CSRFToken  string   `json:"csrf_token,omitempty"`
	Groups     []string `json:"groups,omitempty"`
	MFAEnabled bool     `json:"mfa_enabled"`
}

// AdminExistsResponse holds the admin exists check response.
type AdminExistsResponse struct {
	AdminExists bool `json:"admin_exists"`
}

// PermissionEntry is a single {action, resource} pair in the permissions
// response. Constraints are omitted from the wire shape for now; the
// frontend uses the pair for UX gating only.
type PermissionEntry struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// UserPermissionsResponse is the response shape for GET /api/auth/me/permissions.
// Permissions is the caller's effective set: for a user session, the union of
// the user's groups; for a user API key, the key's permissions intersected
// with its owner's (issue #62); for the admin API key, {admin, *}.
// IsAdmin mirrors whether that effective set contains the {admin, *} wildcard.
// UnrestrictedAccountAccess reports whether the caller's account scope covers
// every account, which global actions such as POST
// /api/recommendations/refresh require (issue #619). Like the permission
// entries it is for UX gating only; the backend still enforces the scope.
type UserPermissionsResponse struct {
	Permissions               []PermissionEntry `json:"permissions"`
	IsAdmin                   bool              `json:"is_admin"`
	UnrestrictedAccountAccess bool              `json:"unrestricted_account_access"`
}

// MFA enrollment + lifecycle DTOs (issue #497). Passwords carried by
// these requests are base64-encoded by the frontend (same convention
// as login / change-password / reset-password); the handler decodes
// before handing to the auth service.

// EmptyServiceConfigResponse represents an empty service config.
type EmptyServiceConfigResponse struct{}

// PublicInfoResponse holds public information about the CUDly instance.
// Only fields safe for unauthenticated callers — sensitive identifiers
// (API key secret URL, deployment AWS account ID) live on the
// authenticated /api/info/deployment endpoint instead.
type PublicInfoResponse struct {
	Version     string `json:"version"`
	AdminExists bool   `json:"admin_exists"`
}

// DeploymentInfoResponse holds deployment-scoped identifiers that must
// not be exposed to unauthenticated callers. Served by
// GET /api/info/deployment (AuthUser).
type DeploymentInfoResponse struct {
	// APIKeySecretURL is the AWS Console deep-link to the Secrets Manager
	// secret holding the CUDly API key.
	APIKeySecretURL string `json:"api_key_secret_url,omitempty"`
	// DeploymentAWSAccountID is the AWS account ID of the Lambda host,
	// resolved via STS GetCallerIdentity. Empty on non-AWS deployments
	// or when STS is unreachable. Used by the frontend to distinguish
	// legitimate ambient-credential executions from orphan rows (#608).
	DeploymentAWSAccountID string `json:"deployment_aws_account_id,omitempty"`
}

// PlannedPurchase represents a scheduled purchase from a plan.
type PlannedPurchase struct {
	// CreatedByUserID is the UUID of the user who created the scheduled
	// purchase, mirroring PurchaseHistoryRecord.CreatedByUserID. The
	// frontend gates the row action buttons on creator-scope ownership
	// (issue #950); omitted for legacy rows with a NULL creator.
	CreatedByUserID *string `json:"created_by_user_id,omitempty"`
	ID              string  `json:"id"`
	PlanID          string  `json:"plan_id"`
	PlanName        string  `json:"plan_name"`
	ScheduledDate   string  `json:"scheduled_date"`
	Provider        string  `json:"provider"`
	Service         string  `json:"service"`
	EstimateNote    string  `json:"estimate_note,omitempty"`
	ResourceType    string  `json:"resource_type"`
	Status          string  `json:"status"`
	Region          string  `json:"region"`
	Payment         string  `json:"payment"`
	// Providers lists a bound plan's group providers (#1746); omitted otherwise.
	Providers        []string `json:"providers,omitempty"`
	Count            int      `json:"count"`
	UpfrontCost      float64  `json:"upfront_cost"`
	EstimatedSavings float64  `json:"estimated_savings"`
	StepNumber       int      `json:"step_number"`
	TotalSteps       int      `json:"total_steps"`
	Term             int      `json:"term"`
	// HasRecommendations is false for a step that carries no recommendations,
	// whose Count, UpfrontCost, EstimatedSavings and ResourceType are then
	// placeholders, not data (platform#609).
	HasRecommendations bool `json:"has_recommendations"`
	// Estimate: the figures come from current recommendations, not a stored
	// attach; the purchase recomputes them. EstimateNote says why there are none.
	Estimate bool `json:"estimate,omitempty"`
}

// CreatePlannedPurchasesRequest represents a request to create planned purchases.
type CreatePlannedPurchasesRequest struct {
	StartDate string `json:"start_date"`
	Count     int    `json:"count"`
}

// CreatePlannedPurchasesResponse represents the response after creating planned purchases.
type CreatePlannedPurchasesResponse struct {
	Created int `json:"created"`
}

// HistoryResponse represents the response from the history API.
type HistoryResponse struct {
	// NextCursor is the opaque position after the last paged row of this
	// response, set only when HasMore is true.
	NextCursor string                         `json:"next_cursor,omitempty"`
	Purchases  []config.PurchaseHistoryRecord `json:"purchases"`
	// Summary covers every row matching the request's filters and the caller's
	// account scope, including rows beyond the row caps (issue #675), unless
	// SummaryComplete is false.
	Summary HistorySummary `json:"summary"`
	// Limit is the page size: the most completed purchase_history rows and
	// terminal executions (failed, expired, canceled, ...) together that one
	// response lists (issue #674).
	Limit int `json:"limit"`
	// ExecutionsLimit is the row cap on terminal executions. They share the
	// page with completed purchases, so it equals Limit.
	// Actionable executions (pending, approved, ...) use ActionableExecutionsLimit.
	ExecutionsLimit int `json:"executions_limit"`
	// ActionableExecutionsLimit is the row cap on actionable executions
	// (pending, notified, scheduled, approved, running, paused), fixed at
	// config.MaxListLimit regardless of the request's limit parameter.
	// Actionable executions are not paged: the first page lists them all, up
	// to this cap, and pages fetched with a cursor leave them out.
	ActionableExecutionsLimit int `json:"actionable_executions_limit"`
	// SummaryComplete is true when Summary was computed over every matching
	// row. It is false only when that aggregate failed and Summary fell back
	// to the rows in Purchases, which under-reports when Truncated is true.
	SummaryComplete bool `json:"summary_complete"`
	// Truncated is true when a fetch reached its row cap, so older rows (and
	// possibly pending approvals) may be missing from Purchases. Summary still
	// counts them while SummaryComplete is true.
	// It also fires when the actionable execution class hits its MaxListLimit cap,
	// and when an execution class listing failed, so its rows are missing.
	// It can be a false positive when exactly a cap's worth of rows exist.
	Truncated bool `json:"truncated"`
	// HasMore is true when more completed purchases or terminal executions the
	// caller may see follow this page; pass NextCursor as the cursor parameter
	// to fetch them (issue #674). Truncated is also true then.
	HasMore bool `json:"has_more"`
}

// HistorySummary provides aggregate statistics for purchase history.
// TotalPurchases is the total count of rows (completed + all non-completed
// states); the per-state counters break it down so the UI can render
// meaningful totals. Dollar totals count completed, non-revoked rows only:
// pending, in-progress, failed, expired, canceled, and revoked rows are all
// excluded because no money is being actively committed for those states.
type HistorySummary struct {
	TotalUpfront   *float64 `json:"total_upfront"`
	TotalPurchases int      `json:"total_purchases"`
	TotalCompleted int      `json:"total_completed"`
	TotalPending   int      `json:"total_pending"`
	// TotalInProgress counts executions that have been approved but whose
	// synchronous purchase has not finalized (status approved/running/paused).
	// Tracked separately from pending and excluded from the dollar totals so an
	// interrupted approval (issue #621) stays visible without inflating
	// committed spend/savings.
	TotalInProgress int `json:"total_in_progress"`
	TotalFailed     int `json:"total_failed"`
	TotalExpired    int `json:"total_expired"`
	// TotalRevoked counts commitments that have been revoked/refunded via
	// MarkPurchaseRevoked. They are excluded from TotalCompleted and all dollar
	// totals because the provider has canceled the commitment.
	TotalRevoked        int     `json:"total_revoked"`
	TotalMonthlySavings float64 `json:"total_monthly_savings"`
	TotalAnnualSavings  float64 `json:"total_annual_savings"`
}
