package config

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ==========================================
// LADDER CONFIG VALIDATION
// ==========================================

// Validate validates a LadderConfigDB before persist. It delegates mode and
// cadence checks to pkg/ladder's Parse* functions (single source of truth for
// those enums) and validates numeric bounds using the same invariants that
// pkg/ladder.LadderConfig.Validate() enforces.
//
// Returns a specific, descriptive error on any violation. Callers must never
// silently default away a validation failure.
func (c *LadderConfigDB) Validate() error {
	if err := validateLadderProvider(c.Provider); err != nil {
		return err
	}
	if _, err := ladder.ParseLadderMode(c.Mode); err != nil {
		return fmt.Errorf("mode: %w", err)
	}
	if _, err := ladder.ParseLadderCadence(c.Cadence); err != nil {
		return fmt.Errorf("cadence: %w", err)
	}
	if err := c.validateLadderBaselineBounds(); err != nil {
		return err
	}
	if err := c.validateLadderRunLimits(); err != nil {
		return err
	}
	if c.MaxHourlyCommitPerSweep != nil {
		if _, err := LadderSweepCeilingMicros(*c.MaxHourlyCommitPerSweep); err != nil {
			return err
		}
	}
	return c.validateLadderRampSchedule()
}

// validateLadderProvider checks that Provider is a recognized cloud provider slug.
func validateLadderProvider(provider string) error {
	for _, p := range ValidProviders {
		if provider == p {
			return nil
		}
	}
	return fmt.Errorf("provider %q is not valid (allowed: %s)", provider, strings.Join(ValidProviders, ", "))
}

// validateLadderBaselineBounds checks the coverage target, buffer fraction,
// and baseline measurement parameters. Uses the same NaN-hostile invariants
// as pkg/ladder.LadderConfig.validateBaselineBounds (mirrored here so
// internal/config does not call unexported pkg methods).
func (c *LadderConfigDB) validateLadderBaselineBounds() error {
	if err := c.validateLadderCoverageBounds(); err != nil {
		return err
	}
	if math.IsNaN(c.BaselinePercentile) || !(c.BaselinePercentile > 0 && c.BaselinePercentile <= 50) {
		return fmt.Errorf("baseline_percentile %g must be in (0, 50]", c.BaselinePercentile)
	}
	if c.LookbackDays <= 0 {
		return fmt.Errorf("lookback_days %d must be > 0", c.LookbackDays)
	}
	if floor := ladderMinLookbackDays[c.Provider]; c.LookbackDays < floor {
		return fmt.Errorf("lookback_days %d is below the %d-day minimum the %s usage baseline needs (raise it to save any change to this config)", c.LookbackDays, floor, c.Provider)
	}
	return nil
}

// ladderMinLookbackDays is the shortest lookback window each provider's usage
// baseline can use reliably. A window of N days returns at most N daily points
// and Cost Explorer lags 24-48h, so AWS needs the 7-point floor
// (providers/aws ladder/baseline.go:17 minBaselineSeriesDays, pinned
// c83aef8b18a9, unexported) plus its 3-day missing-day tolerance
// (maxMissingDays): 10. Recheck when the aws provider module is bumped.
// A provider without an entry has no minimum beyond lookback_days > 0.
var ladderMinLookbackDays = map[string]int{"aws": 10}

// validateLadderCoverageBounds checks the coverage target and buffer fraction.
// Split out from validateLadderBaselineBounds to keep each function under the
// cyclomatic-complexity gate.
func (c *LadderConfigDB) validateLadderCoverageBounds() error {
	if math.IsNaN(c.TargetCoverage) || !(c.TargetCoverage > 0 && c.TargetCoverage <= 100) {
		return fmt.Errorf("target_coverage %g must be in (0, 100]", c.TargetCoverage)
	}
	if math.IsNaN(c.BufferFraction) || !(c.BufferFraction >= 0 && c.BufferFraction < 1) {
		return fmt.Errorf("buffer_fraction %g must be in [0, 1)", c.BufferFraction)
	}
	return nil
}

// validateLadderRunLimits checks the per-run spend and action caps and the
// buffer utilization threshold.
func (c *LadderConfigDB) validateLadderRunLimits() error {
	if v := c.MaxHourlyCommitPerRun; v != nil {
		if _, err := LadderCapMicros(*v); err != nil {
			return err
		}
	} else if c.Provider == "aws" {
		// The Compute SP layer is sized from an account-wide blended rate; only
		// a per-run cap bounds it, and executeLadderRun refuses uncapped runs.
		return fmt.Errorf("max_hourly_commit_per_run is required for an AWS ladder config")
	}
	if c.MaxActionsPerRun <= 0 {
		return fmt.Errorf("max_actions_per_run %d must be > 0", c.MaxActionsPerRun)
	}
	if c.MaxActionsPerRun > MaxLadderActionsPerRun {
		return fmt.Errorf("max_actions_per_run %d exceeds ceiling %d", c.MaxActionsPerRun, MaxLadderActionsPerRun)
	}
	if math.IsNaN(c.BufferUtilizationThreshold) || !(c.BufferUtilizationThreshold > 0 && c.BufferUtilizationThreshold <= 100) {
		return fmt.Errorf("buffer_utilization_threshold %g must be in (0, 100]", c.BufferUtilizationThreshold)
	}
	return nil
}

// validateLadderRampSchedule parses the JSONB ramp_schedule payload and
// delegates structural validation to pkg/ladder.RampSchedule.Validate so
// the two validation paths stay in sync.
func (c *LadderConfigDB) validateLadderRampSchedule() error {
	if len(c.RampSchedule) == 0 {
		return fmt.Errorf("ramp_schedule must not be empty")
	}
	var rs ladder.RampSchedule
	if err := json.Unmarshal(c.RampSchedule, &rs); err != nil {
		return fmt.Errorf("ramp_schedule is not valid JSON: %w", err)
	}
	if err := rs.Validate(); err != nil {
		return fmt.Errorf("ramp_schedule: %w", err)
	}
	return nil
}
