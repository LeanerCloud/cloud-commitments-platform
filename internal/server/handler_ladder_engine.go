package server

import (
	"context"
	"encoding/json"
	"fmt"
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
