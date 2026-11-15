package purchase

import (
	"context"
	"fmt"
	"strconv"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/execution"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// recPurchaseOutcome carries the result of a single fan-out unit so the
// aggregator can write back into exec.Recommendations and call
// savePurchaseHistory from a single goroutine (no concurrent map / slice
// mutation). The index field is the position in exec.Recommendations.
type recPurchaseOutcome struct {
	err      error
	purchase common.PurchaseResult
	index    int
}

func (m *Manager) processPurchaseRecommendations(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, accountID string, provCfg *provider.Config, awsPartition string) (totalSavings, totalUpfront float64, purchaseErrors []string, allRefused bool, procErr error) {
	// ExecutionID is carried into PurchaseOptions so executeSinglePurchase
	// can tag every per-rec log line with the owning exec UUID. Without
	// this, CloudWatch filtering by exec ID returns zero hits and a stuck
	// or failed execution (e.g. issue #667's context-deadline timeout)
	// can't be correlated to the cloud SDK call that produced it.
	opts := common.PurchaseOptions{
		Source:      m.normalizePurchaseSource(exec),
		ExecutionID: exec.ExecutionID,
	}
	if exec.FirstDispatchAt != nil {
		opts.RequestedAt = exec.FirstDispatchAt.UTC()
	}

	// Load GlobalConfig to pick up the OfferingClass setting for EC2 RI
	// purchases. A load error is fatal: proceeding with an empty OfferingClass
	// silently defaults to "convertible" even when the operator configured
	// "standard", which is an irreversible, more-expensive purchase. Fail the
	// run so the operator can diagnose the DB issue and retry with correct
	// settings (no-silent-fallbacks-on-money-paths rule).
	globalCfg, err := m.config.GetGlobalConfig(ctx)
	if err != nil {
		return 0, 0, nil, false, fmt.Errorf("purchase[%s]: failed to load GlobalConfig for offering class: %w",
			exec.ExecutionID, err)
	}
	if globalCfg != nil {
		opts.OfferingClass = globalCfg.OfferingClass
	}
	offeringClass, err := resolveEC2OfferingClass(opts.OfferingClass)
	if err != nil {
		return 0, 0, nil, false, fmt.Errorf("purchase[%s]: %w", exec.ExecutionID, err)
	}

	// Build the list of selected indices once so the fan-out closure only
	// has to look up rec[i] (no second pass over the full slice).
	selected := selectedIndices(exec.Recommendations)
	ownNames := newPurchaseOwnNames(exec, selected)
	if len(selected) == 0 {
		logging.Infof("purchase[%s]: no selected recommendations, nothing to execute (account=%s plan=%q)",
			exec.ExecutionID, accountID, plan.Name)
		return 0, 0, nil, false, nil
	}
	logging.Infof("purchase[%s]: dispatching %d recommendation(s) for account=%s plan=%q",
		exec.ExecutionID, len(selected), accountID, plan.Name)

	// Each rec runs in its own goroutine so a multi-rec execution that
	// spans providers (AWS RI + Azure reservation + GCP CUD) or services
	// (EC2 + RDS + ElastiCache + OpenSearch within AWS) makes its cloud
	// API calls in parallel rather than blocking on the slowest one.
	// Provider client construction inside executeSinglePurchase is
	// independent per call, so cross-provider parallelism is safe.
	// Concurrency is capped at getMaxAccountParallelism so the same
	// operator-level CUDLY_MAX_ACCOUNT_PARALLELISM knob covers both the
	// outer account fan-out and this inner rec fan-out.
	results := execution.FanOutWithConcurrency(ctx, indexKeys(selected),
		func(ctx context.Context, key string) (recPurchaseOutcome, error) {
			// indexKeys() builds these keys from selectedIndices(), so parse +
			// bounds errors are not expected at runtime. Surface them as
			// closure-level errors anyway so the aggregator can correlate
			// them via execution.Result.Err and never silently dereference a
			// zero-valued outcome at index 0.
			i, parseErr := strconv.Atoi(key)
			if parseErr != nil {
				return recPurchaseOutcome{}, fmt.Errorf("invalid fan-out key %q: %w", key, parseErr)
			}
			if i < 0 || i >= len(exec.Recommendations) {
				return recPurchaseOutcome{}, fmt.Errorf("fan-out index %d out of range for %d recommendations", i, len(exec.Recommendations))
			}
			rec := exec.Recommendations[i]
			// Refused before any provider is built, so no AWS call is made
			// for an aws-cn Savings Plans rec (issue #1194).
			if partitionErr := m.rejectUnsupportedPartition(&rec, awsPartition); partitionErr != nil {
				logging.Warnf("purchase[%s]: %s/%s rec %d not purchased: %v", exec.ExecutionID, rec.Provider, rec.Service, i, partitionErr)
				return recPurchaseOutcome{index: i, err: partitionErr}, nil
			}
			logging.Infof("Purchasing: %dx %s in %s (%s/%s)", rec.Count, rec.ResourceType, rec.Region, rec.Provider, rec.Service)
			// Derive a deterministic per-rec idempotency token from the
			// execution's STABLE lineage key (not its mutable ExecutionID)
			// and this rec's index so a re-drive of a stranded execution
			// reuses the identical token and the commitment is never created
			// twice. The lineage key survives Retry and multi-account fan-out
			// (issue #1012), where the ExecutionID is regenerated; deriving
			// from the ExecutionID directly defeated the provider guard on
			// exactly those re-drive paths (#636/#639). opts is a value, so
			// this per-rec copy is safe under the parallel fan-out (no shared
			// mutation across goroutines).
			recOpts := opts
			recOpts.IdempotencyToken = common.DeriveIdempotencyToken(idempotencyLineageKey(exec), i)
			recOpts.AttemptJournal = m.attemptJournal
			purchaseResult, err := m.executeSinglePurchase(ctx, rec, provCfg, recOpts, ownNames)
			return recPurchaseOutcome{index: i, purchase: purchaseResult, err: err}, nil
		}, getMaxAccountParallelism())

	// aggregatePurchaseOutcomes is intentionally single-threaded: the fan-out
	// results are collected above, and the aggregator walks them serially so
	// there are no concurrent writes to totals, exec.Recommendations, or
	// purchaseErrors (05-N2). Do NOT move the aggregation inside the FanOut
	// closure or run it concurrently with the fan-out.
	totalSavings, totalUpfront, purchaseErrors, allRefused = m.aggregatePurchaseOutcomes(ctx, exec, plan, accountID, offeringClass, results)
	return totalSavings, totalUpfront, purchaseErrors, allRefused, nil
}

// aggregatePurchaseOutcomes walks the fan-out results serially and writes
// each rec's outcome back to exec.Recommendations + savePurchaseHistory.
// Extracted so processPurchaseRecommendations stays under gocyclo:10 and
// so the aggregation logic is single-threaded — no concurrent writes to
// totals, purchaseErrors, or exec.Recommendations[i] regardless of how
// many recs ran in parallel.
func (m *Manager) aggregatePurchaseOutcomes(ctx context.Context, exec *config.PurchaseExecution, plan *config.PurchasePlan, accountID string, offeringClass ec2types.OfferingClassType, results []execution.Result[recPurchaseOutcome]) (float64, float64, []string, bool) { //nolint:gocritic // unnamedResult: return names would conflict with body locals
	var totalSavings, totalUpfront float64
	var purchaseErrors []string
	var tally failureTally
	for _, r := range results {
		// Closure-level error (parse / bounds / framework). r.Value is the
		// zero recPurchaseOutcome here — its index is 0 and would mis-target
		// exec.Recommendations[0] if blindly trusted. Record it as a
		// generic execution failure and skip the per-rec writes.
		if r.Err != nil {
			logging.Errorf("Internal fan-out failure during purchase execution: %v", r.Err)
			tally.add(r.Err)
			purchaseErrors = append(purchaseErrors, fmt.Sprintf("internal fan-out error: %v", r.Err))
			continue
		}
		v := r.Value
		i := v.index
		// Defense-in-depth: even with the closure's bounds check, never
		// index past exec.Recommendations here (a future refactor that
		// mutates the slice between fan-out and aggregation would corrupt
		// this otherwise).
		if i < 0 || i >= len(exec.Recommendations) {
			logging.Errorf("Aggregator received out-of-range index %d (len=%d)", i, len(exec.Recommendations))
			tally.add(nil)
			purchaseErrors = append(purchaseErrors, fmt.Sprintf("aggregator index %d out of range", i))
			continue
		}
		rec := exec.Recommendations[i]
		if v.err != nil {
			logging.Errorf("Failed to purchase %s: %v", rec.ResourceType, v.err)
			exec.Recommendations[i].Error = v.err.Error()
			// The provider returned a commitment it could not record (go#107):
			// keep its ID on the rec, still Purchased=false, so the failure
			// email names it. A failure without an ID leaves any ID the rec
			// already carried (a Retry predecessor's) untouched.
			if v.purchase.CommitmentID != "" {
				exec.Recommendations[i].PurchaseID = v.purchase.CommitmentID
			}
			tally.add(v.err)
			purchaseErrors = append(purchaseErrors, fmt.Sprintf("%s: %v", rec.ResourceType, v.err))
			continue
		}
		exec.Recommendations[i].Purchased = true
		exec.Recommendations[i].PurchaseID = v.purchase.CommitmentID
		totalSavings += rec.Savings
		totalUpfront += rec.UpfrontCost
		if histErr := m.savePurchaseHistory(ctx, exec, plan, rec, v.purchase, accountID, offeringClass); histErr != nil {
			// The purchase SUCCEEDED but its purchase_history row failed to
			// persist. Do NOT add this to purchaseErrors — that would flip the
			// execution to "failed" and tempt the user to re-approve a purchase
			// that already fired (the exact double-spend trap issue #621 warns
			// about). Instead stamp an audit-gap marker on the execution so it
			// stays "completed" but visible in the History view despite the
			// missing purchase_history row.
			recordHistoryAuditGap(exec, v.purchase.CommitmentID, histErr)
		}
	}
	return totalSavings, totalUpfront, purchaseErrors, tally.allRefused(exec.Recommendations)
}
