package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// fetchAndConvert is a convenience for the AWS ambient path.
//
// The *sweepGap is nil when the sweep covered everything it was asked to; see
// tolerateIncompleteSweep for why an incomplete one must not authorize
// stale-row eviction.
func (s *Scheduler) fetchAndConvert(ctx context.Context, prov provider.Provider, providerName string, accountID *string, expectedAccount string, globalCfg *config.GlobalConfig) ([]config.RecommendationRecord, *sweepGap, error) {
	recClient, err := prov.GetRecommendationsClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get %s recommendations client: %w", providerName, err)
	}
	recs, err := recClient.GetAllRecommendations(ctx)
	gap, err := tolerateIncompleteSweep(providerName, err)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get %s recommendations: %w", providerName, err)
	}
	recs = dropForeignZonalEC2(recs, expectedAccount)
	if len(recs) == 0 && globalCfg != nil {
		lookbackDays := globalCfg.RecommendationsLookbackDays
		if lookbackDays == 0 {
			lookbackDays = config.DefaultRecommendationsLookbackDays
		}
		payment := awsRecommendationPayment(globalCfg.DefaultPayment)
		for _, service := range []common.ServiceType{common.ServiceEC2, common.ServiceRDS, common.ServiceElastiCache, common.ServiceOpenSearch, common.ServiceRedshift, common.ServiceSavingsPlansAll} {
			params := common.RecommendationParams{
				Service:        service,
				Term:           fmt.Sprintf("%dyr", globalCfg.DefaultTerm),
				PaymentOption:  payment,
				LookbackPeriod: fmt.Sprintf("%dd", lookbackDays),
			}
			fallbackRecs, recErr := recClient.GetRecommendations(ctx, &params)
			fallbackGap, recErr := tolerateIncompleteSweep(providerName, recErr)
			if recErr != nil {
				return nil, nil, fmt.Errorf("failed to get %s recommendations with default term/payment fallback (service=%s, term=%s, payment=%s, lookback=%s): %w",
					providerName, params.Service, params.Term, params.PaymentOption, params.LookbackPeriod, recErr)
			}
			recs = append(recs, dropForeignZonalEC2(fallbackRecs, expectedAccount)...)
			gap = mergeSweepGaps(gap, fallbackGap)
		}
	}
	result := s.convertRecommendations(recs, providerName)
	if accountID != nil {
		result = s.tagAccount(result, *accountID)
	}
	return result, gap, nil
}

// tagAccount assigns the stored account identity and updates its recommendation ID.
// A rec whose ID cannot be computed is dropped loudly rather than emitted
// without its platform/tenancy pair (issue #806).
func (s *Scheduler) tagAccount(recs []config.RecommendationRecord, accountID string) []config.RecommendationRecord {
	kept := recs[:0]
	for i := range recs {
		recs[i].CloudAccountID = &accountID
		id, err := recommendationID(recs[i])
		if err != nil {
			logging.Errorf("tagAccount: dropping rec (account=%s service=%s sku=%s region=%s): %v",
				accountID, recs[i].Service, recs[i].ResourceType, recs[i].Region, err)
			continue
		}
		recs[i].ID = id
		kept = append(kept, recs[i])
	}
	return kept
}

// recommendationID uses the same identity dimensions as the stored natural key.
func recommendationID(rec config.RecommendationRecord) (string, error) {
	ci, err := rec.ComputeIdentity()
	if err != nil {
		return "", err
	}
	azConfig, err := rec.IdentityAZConfig()
	if err != nil {
		return "", err
	}
	accountID := ""
	if rec.CloudAccountID != nil {
		accountID = *rec.CloudAccountID
	}
	id := fmt.Sprintf("%s|%s|%s|%s|%s|%s|%d|%s",
		rec.Provider, accountID, rec.Service, rec.Region,
		rec.ResourceType, rec.Engine, rec.Term, rec.Payment)
	if rec.AvailabilityZone != "" {
		// Appended only for zonal EC2 recs so every other rec keeps its
		// existing ID (issue #792).
		id += "|" + rec.AvailabilityZone
	}
	if !ci.IsZero() {
		// The platform/tenancy pair is appended together, only for recs whose
		// Details carry them, so every other rec keeps its ID (issue #806).
		// AWS platform is the raw CE string, so the ID can contain "/" and
		// spaces.
		id += "|" + ci.Platform + "|" + ci.Tenancy
	}
	if azConfig != "" {
		// Appended only for AWS RDS recs whose Details carry it
		// (single-az/multi-az), so every other rec keeps its ID (issue #408).
		// Azure SQL is excluded: its az_config is a subscription-wide value
		// that is "" on an incomplete server lookup, so it would only flip IDs.
		id += "|" + azConfig
	}
	return id, nil
}

// nonZeroPtr returns &v when v > 0, else nil. Used by convertRecommendations
// to pass the provider's on-demand baseline (rec.OnDemandCost) through as a
// nullable pointer — a literal 0 from the provider means "not populated"
// because every real on-demand baseline is non-zero. Always returning a
// pointer would poison the frontend's "is this populated?" branch
// (recommendations.ts::effectiveSavingsPct, see #274). Extracted to keep
// convertRecommendations under the gocyclo gate (.golangci.yml
// min-complexity: 15; pre-commit hook flags > 10).
func nonZeroPtr(v float64) *float64 {
	if v > 0 {
		return &v
	}
	return nil
}

// extractEngine pulls the engine string out of a polymorphic
// common.ServiceDetails value when one is present. DatabaseDetails/
// CacheDetails are always pointers (every producer constructs them that
// way; see pkg/common/service_details_codec.go's package doc for the
// invariant). Returns "" for any other Details type (or nil). The
// nil-pointer guards are not dead code: the `details == nil` check above
// only catches an untyped nil, so a typed nil reaches the switch and would
// panic on the field read. Extracted from convertRecommendations to keep
// that function under the gocyclo budget (min-complexity: 10 in
// .pre-commit-config.yaml).
func extractEngine(details common.ServiceDetails) string {
	if details == nil {
		return ""
	}
	switch d := details.(type) {
	case *common.DatabaseDetails:
		if d == nil {
			return ""
		}
		return d.Engine
	case *common.CacheDetails:
		if d == nil {
			return ""
		}
		return d.Engine
	}
	return ""
}

// marshalRecDetails encodes the full ServiceDetails payload into a raw
// JSON blob so the purchase manager can reconstruct the correct typed
// *Details pointer later (issue #453). Without this, every persisted
// rec round-trips through the DB as "service + engine" only — losing
// EC2 platform / tenancy / scope, RDS AZ-config, SP plan-type / hourly
// commitment, etc. — and findOfferingID either picks the wrong offering
// (Windows recs purchased as Linux/UNIX) or fails the type-assertion
// outright. Marshal errors are non-fatal: the row still gets persisted
// without Details and falls through to the graceful-degradation path in
// common.DecodeServiceDetailsFor. Extracted from convertRecommendations
// to keep that function under the gocyclo budget.
func marshalRecDetails(rec common.Recommendation, providerName string) []byte {
	blob, err := common.MarshalServiceDetails(rec.Details)
	if err != nil {
		logging.Warnf("Failed to marshal service details for %s/%s rec (%s): %v — persisting without Details",
			providerName, rec.Service, rec.ResourceType, err)
		return nil
	}
	return blob
}

// isUnpurchasableAWSRI reports an AWS RI whose Cost Explorer fractional
// quantity rounded to Count 0 (#1949). It has no on-demand baseline and cannot
// be purchased, and storing it would let it slip past the MinSavingsPct floor,
// which only drops rows whose percentage is computable.
func isUnpurchasableAWSRI(rec common.Recommendation, providerName string) bool {
	return providerName == string(common.ProviderAWS) &&
		rec.CommitmentType == common.CommitmentReservedInstance && rec.Count <= 0
}

// convertRecommendations converts common.Recommendation slice to config.RecommendationRecord slice.
func (s *Scheduler) convertRecommendations(recs []common.Recommendation, providerName string) []config.RecommendationRecord {
	records := make([]config.RecommendationRecord, 0, len(recs))

	for i := range recs {
		rec := recs[i]
		engine := extractEngine(rec.Details)
		detailsBlob := marshalRecDetails(rec, providerName)

		// Canonicalize PaymentOption at the emission boundary so a downstream
		// plan-validator round-trip never sees a cross-provider/AWS-style
		// token on a non-AWS rec (issue #698). The provider service clients
		// historically aliased "all-upfront"/"no-upfront" to the canonical
		// "upfront"/"monthly" inside their pricing switches; the validator
		// no longer accepts those aliases. Normalize once here so persisted
		// recs always carry the provider-canonical token.
		if canon, ok := config.NormalizePaymentOption(providerName, string(rec.PaymentOption)); ok {
			if canon != string(rec.PaymentOption) {
				logging.Warnf("convertRecommendations: coerced %s payment_option %q to canonical %q (account=%s service=%s sku=%s)",
					providerName, rec.PaymentOption, canon, rec.Account, rec.Service, rec.ResourceType)
				rec.PaymentOption = common.PaymentOption(canon)
			}
		} else if rec.PaymentOption != "" {
			// Unknown provider or unmapped token: leave as-is. The next
			// validator boundary will surface the issue with a clear error.
			logging.Warnf("convertRecommendations: cannot canonicalize %s payment_option %q (account=%s service=%s sku=%s) — leaving as-is",
				providerName, rec.PaymentOption, rec.Account, rec.Service, rec.ResourceType)
		}

		// Parse term to integer (e.g., "3yr" -> 3). An empty or unparseable
		// term must NOT be coerced to a valid default: rec.Term round-trips
		// through this int on the purchase path (internal/purchase/execution.go
		// formats it back as "%dyr"), so silently defaulting here would let a
		// malformed term launder into a valid, purchasable commitment length
		// downstream, bypassing the ARCH-04 whitelist checks in the provider
		// clients entirely, since "3yr" is itself a whitelisted value (issue
		// #1207 follow-up). Drop the recommendation instead of persisting a
		// fabricated term.
		termStr := strings.TrimSuffix(string(rec.Term), "yr")
		term, err := strconv.Atoi(termStr)
		if err != nil || term <= 0 {
			logging.Errorf("convertRecommendations: dropping %s rec (account=%s service=%s sku=%s region=%s): invalid term %q",
				providerName, rec.Account, rec.Service, rec.ResourceType, rec.Region, rec.Term)
			continue
		}

		if isUnpurchasableAWSRI(rec, providerName) {
			logging.Warnf("convertRecommendations: dropping %s rec (account=%s service=%s sku=%s region=%s): non-positive count %d",
				providerName, rec.Account, rec.Service, rec.ResourceType, rec.Region, rec.Count)
			continue
		}

		var sourceAccountID *string
		if rec.Account != "" {
			sourceAccountID = new(rec.Account)
		}
		record := config.RecommendationRecord{
			SourceAccountID:   sourceAccountID,
			Provider:          providerName,
			Service:           config.StoredRecommendationService(providerName, rec.Service),
			Region:            rec.Region,
			ResourceType:      rec.ResourceType,
			Engine:            engine,
			Details:           detailsBlob, // full ServiceDetails payload (issue #453)
			AvailabilityZone:  zonalAvailabilityZone(rec.Details),
			Count:             rec.Count,
			Term:              term,
			Payment:           string(rec.PaymentOption),
			UpfrontCost:       rec.CommitmentCost,
			MonthlyCost:       rec.RecurringMonthlyCost, // nil when provider API didn't return a monthly breakdown
			Savings:           rec.EstimatedSavings,
			OnDemandCost:      nonZeroPtr(rec.OnDemandCost),      // nil when provider API didn't return a baseline; frontend falls back to reconstruction (#274)
			SavingsPercentage: nonZeroPtr(rec.SavingsPercentage), // provider-authoritative %; nil when not reported, frontend falls back to effectiveSavingsPct
			UsageHistory:      rec.UsageHistory,                  // daily coverage pcts (nil when provider not yet wired; see #239)
			Selected:          true,                              // Default to selected
			Purchased:         false,
		}
		// Keep the currency the provider parser proved for the commitment
		// legs; empty stays empty (unknown), never a defaulted "USD". The
		// purchase spend-cap check refuses a capped purchase of a rec that
		// is not provably USD (issue #361).
		record.CommitmentCurrency = rec.CommitmentCurrency
		id, err := recommendationID(record)
		if err != nil {
			logging.Errorf("convertRecommendations: dropping %s rec (account=%s service=%s sku=%s region=%s): %v",
				providerName, rec.Account, rec.Service, rec.ResourceType, rec.Region, err)
			continue
		}
		record.ID = id
		records = append(records, record)
	}

	return records
}
