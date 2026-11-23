package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	pkgcommon "github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderWithinCadenceWindow reports whether a new run should be skipped because
// a recent run already covers the cadence window. It queries
// LatestLadderRunStartedAt for the config and returns:
//   - (true, reason, nil)  when a recent run is within the window (skip),
//   - (false, "", nil)     when the config is eligible to run,
//   - (false, "", err)     when the lookup itself failed.
//
// The lookup error is propagated (NOT swallowed) so the caller can fail CLOSED:
// if we cannot determine whether a recent run exists, proceeding risks a
// duplicate money action, so the caller counts the config Errored rather than
// running it.
func ladderWithinCadenceWindow(ctx context.Context, store config.StoreInterface, dbCfg *config.LadderConfigDB, now time.Time) (within bool, reason string, err error) {
	latest, err := store.LatestLadderRunStartedAt(ctx, dbCfg.ID)
	if err != nil {
		return false, "", fmt.Errorf("LatestLadderRunStartedAt: %w", err)
	}
	if latest == nil {
		return false, "", nil // no previous run, always eligible
	}
	elapsed := now.Sub(*latest)
	switch dbCfg.Cadence {
	case string(pkgladder.CadenceDaily):
		if elapsed < cadenceDailyMin {
			return true, fmt.Sprintf("cadence=daily: last run %v ago (threshold %v), skipping", elapsed.Round(time.Minute), cadenceDailyMin), nil
		}
	case string(pkgladder.CadenceWeekly):
		if elapsed < cadenceWeeklyMin {
			return true, fmt.Sprintf("cadence=weekly: last run %v ago (threshold %v), skipping", elapsed.Round(time.Minute), cadenceWeeklyMin), nil
		}
	default:
		// Unknown cadence: log but do not block the run. The LadderConfig
		// validator surfaces this as an error in ladderConfigToEngine (pre-persist,
		// fail-loud), so the run will fail before any money action is taken.
		logging.Warnf("ladder_run: config %s: unknown cadence=%q, proceeding without cadence gate", dbCfg.ID, dbCfg.Cadence)
	}
	return false, "", nil
}

// readLadderBaseline sets the config's buffer fraction on the capability and
// then reads the usage baseline. The order matters: the ladder only fills
// Stable once it has a buffer fraction.
func readLadderBaseline(ctx context.Context, capability pkgladder.LadderCapability, engineCfg pkgladder.LadderConfig) (pkgladder.UsageBaseline, error) {
	if err := setLadderBufferFraction(capability, engineCfg.BufferFraction); err != nil {
		return pkgladder.UsageBaseline{}, fmt.Errorf("buffer fraction: %w", err)
	}
	baseline, err := capability.GetUsageBaseline(ctx, engineCfg.Scope, engineCfg.LookbackDays, engineCfg.BaselinePercentile)
	if err != nil {
		return pkgladder.UsageBaseline{}, fmt.Errorf("GetUsageBaseline: %w", err)
	}
	return baseline, nil
}

// ladderConfigToEngine converts a LadderConfigDB row to a pkg/ladder LadderConfig.
// It parses typed enums at the boundary and fails loud on any unknown value.
func ladderConfigToEngine(dbCfg *config.LadderConfigDB, accountID string) (pkgladder.LadderConfig, error) {
	mode, err := pkgladder.ParseLadderMode(dbCfg.Mode)
	if err != nil {
		return pkgladder.LadderConfig{}, fmt.Errorf("mode: %w", err)
	}
	cadence, err := pkgladder.ParseLadderCadence(dbCfg.Cadence)
	if err != nil {
		return pkgladder.LadderConfig{}, fmt.Errorf("cadence: %w", err)
	}

	var ramp pkgladder.RampSchedule
	if err := json.Unmarshal(dbCfg.RampSchedule, &ramp); err != nil {
		return pkgladder.LadderConfig{}, fmt.Errorf("ramp_schedule: %w", err)
	}

	// The scope is always AWS: the run gate (processOneLadderConfig) compares the
	// account's external id with the Lambda's own AWS account and never reads
	// dbCfg.Provider. A config stored for azure or gcp (the API and the UI accept
	// them) is therefore stored and inert until the engine generalises: it is
	// skipped as another account, and would act only if its external id equalled
	// that AWS account number.
	return pkgladder.LadderConfig{
		Scope: pkgladder.Scope{
			Provider:  pkgcommon.ProviderAWS,
			AccountID: accountID,
		},
		Mode:                          mode,
		Cadence:                       cadence,
		Ramp:                          ramp,
		TargetCoveragePct:             dbCfg.TargetCoverage,
		BufferFraction:                dbCfg.BufferFraction,
		BaselinePercentile:            dbCfg.BaselinePercentile,
		LookbackDays:                  dbCfg.LookbackDays,
		MaxActionsPerRun:              dbCfg.MaxActionsPerRun,
		BufferUtilizationThresholdPct: dbCfg.BufferUtilizationThreshold,
		MaxHourlyCommitPerRun:         dbCfg.MaxHourlyCommitPerRun,
	}, nil
}

// assembleLadderPlan builds a LadderPlan from the Allocate result and the
// collected read-side data. Q5: Actions interleaves both purchases
// (immediate ramp steps only) and Reshapes in one display-only plan JSON. Term and
// PaymentOption are stamped on every ActionPurchase entry; they are required
// by PlannedAction.Validate for purchase actions.
//
// The monetary snapshot is derived to stay consistent with the authoritative
// allocation rather than recomputed independently:
//   - target = baseline low-water * targetCoveragePct/100 (the coverage goal),
//   - existing = Allocate's NetExistingUSDPerHour, the net (existing minus
//     expiring) commitment over the configured layers that the gap was sized
//     against, in the on-demand-equivalent dollars ladderLayerStatesOnDemand
//     converted the layer states to. Reporting the gross per-layer sum instead
//     would show a gap that does not reconcile with target - existing,
//   - gap = sum of the planned allocations' on-demand-equivalent gaps (the unit
//     of target and existing), NOT target - existing.
//
// When the baseline low-water is nil the run is a Hold-only no-op (Allocate
// returns no allocations), so target and gap stay nil (never 0-coerced);
// existing is still reported, since Allocate sets it on every result.
func assembleLadderPlan(
	scope pkgladder.Scope,
	now time.Time,
	allocResult *pkgladder.AllocateResult,
	immediate map[ladderAmountKey]*big.Rat,
	baseline pkgladder.UsageBaseline,
	targetCoveragePct float64,
	term pkgladder.Term,
	paymentOpt pkgladder.PaymentOption,
) *pkgladder.LadderPlan {
	actions := make([]pkgladder.PlannedAction, 0, len(allocResult.Allocations)+len(allocResult.Reshapes)+len(allocResult.Holds))
	// Purchase actions come first so approval-email ordering is stable. Each
	// carries only the immediate ramp step for its layer, not the allocation
	// gap: the later steps are persisted tranches, and a layer whose ramp has no
	// immediate step has no purchase action. The plan is display only; the sole
	// purchase path is tranche -> purchase_executions (not built yet).
	for _, alloc := range allocResult.Allocations {
		amount := immediate[ladderAmountKey{layer: alloc.Layer, family: alloc.Family}]
		if amount == nil {
			continue
		}
		actions = append(actions, pkgladder.PlannedAction{
			Action:           pkgladder.ActionPurchase,
			Layer:            alloc.Layer,
			Family:           alloc.Family,
			AmountUSDPerHour: amount,
			Term:             term,
			PaymentOption:    paymentOpt,
			Rationale:        alloc.Rationale,
			DataSources:      alloc.DataSources,
		})
	}
	// Reshapes follow; they carry no amount (AmountUSDPerHour must be nil).
	actions = append(actions, allocResult.Reshapes...)
	// Holds are informational; append last.
	actions = append(actions, allocResult.Holds...)

	// existing is the net figure Allocate sized the gap against (#243), copied
	// so the plan does not alias the result.
	var existing *big.Rat
	if allocResult.NetExistingUSDPerHour != nil {
		existing = new(big.Rat).Set(allocResult.NetExistingUSDPerHour)
	}

	// target and gap are only meaningful when the baseline is available; nil
	// stays nil (a Hold-only no-op has no purchase target/gap to report).
	target := ladderPlanTarget(baseline, targetCoveragePct)
	var gap *big.Rat
	if baseline.LowWaterUSDPerHour != nil {
		// gap is what is being placed this run in on-demand-equivalent dollars,
		// so it compares with target and existing; total_hourly_commit is the
		// same placement in commitment dollars.
		gap = sumAllocationOnDemandGaps(allocResult.Allocations)
	}

	return &pkgladder.LadderPlan{
		Scope:              scope,
		GeneratedAt:        now,
		TargetUSDPerHour:   target,
		ExistingUSDPerHour: existing,
		GapUSDPerHour:      gap,
		Actions:            actions,
		Baseline:           baseline,
	}
}

// ladderPlanTarget is the plan's on-demand-equivalent target, baseline
// low-water * targetCoveragePct/100. nil when the baseline is unavailable.
func ladderPlanTarget(baseline pkgladder.UsageBaseline, targetCoveragePct float64) *big.Rat {
	if baseline.LowWaterUSDPerHour == nil {
		return nil
	}
	low := ratFromFloat(*baseline.LowWaterUSDPerHour)
	pctFrac := ratFromFloat(targetCoveragePct / 100.0)
	if low == nil || pctFrac == nil {
		return nil
	}
	return new(big.Rat).Mul(low, pctFrac)
}
