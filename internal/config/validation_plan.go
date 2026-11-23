package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// MinStepCommitment and MaxStepCommitmentLimit bound max_step_commitment to what
// the NUMERIC(18,2) column stores: below one cent it rounds to 0 and violates
// CHECK > 0 (a 500), and 1e308 overflows it. NaN fails both comparisons.
const (
	MinStepCommitment      = 0.01
	MaxStepCommitmentLimit = 1e15
)

// StepCapCents returns the cap in whole cents, exactly. The column is
// NUMERIC(18,2) and Postgres rounds the decimal text pgx sends (the shortest
// representation of the float), so a cap with more than two decimals would be
// stored as a different value than the one compared (1.005 stores as 1.01).
// Such caps are rejected instead of rounded, which makes the stored and the
// compared value identical. The decimal text is parsed, not multiplied, because
// v*100 loses exactness near the 1e15 limit.
func StepCapCents(v float64) (int64, error) {
	if !(v >= MinStepCommitment) || !(v <= MaxStepCommitmentLimit) {
		return 0, fmt.Errorf("max_step_commitment must be between %v and %v, got: %v", MinStepCommitment, MaxStepCommitmentLimit, v)
	}
	whole, frac, _ := strings.Cut(strconv.FormatFloat(v, 'f', -1, 64), ".")
	if len(frac) > 2 {
		return 0, fmt.Errorf("max_step_commitment must have at most 2 decimal places, got: %v", v)
	}
	n, err := strconv.ParseInt(whole+(frac + "00")[:2], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("max_step_commitment is not representable: %w", err)
	}
	return n, nil
}

// validateNotificationAndCap checks the notification window and requires a set
// cap to be a positive finite amount in whole cents. A nil cap means no cap,
// which refuses auto-purchase of steps with recommendations.
func (p *PurchasePlan) validateNotificationAndCap() error {
	if p.NotificationDaysBefore < 0 || p.NotificationDaysBefore > MaxNotificationDaysBefore {
		return fmt.Errorf("notification days must be between 0 and %d, got: %d", MaxNotificationDaysBefore, p.NotificationDaysBefore)
	}
	if p.MaxStepCommitment != nil {
		if _, err := StepCapCents(*p.MaxStepCommitment); err != nil {
			return err
		}
	}
	return nil
}

// Validate validates the PurchasePlan.
func (p *PurchasePlan) Validate() error {
	return p.validate(true)
}

// ValidateDeescalation is Validate without the bound-group checks, for a write
// that only disables or disarms the plan: stopping a plan must never be blocked
// by a group conflict it already stores (issue #1746). Every other check runs.
func (p *PurchasePlan) ValidateDeescalation() error {
	return p.validate(false)
}

func (p *PurchasePlan) checkBoundGroups(enabled bool) error {
	if !enabled {
		return nil
	}
	return p.validateBoundGroups()
}

func (p *PurchasePlan) validate(boundGroups bool) error {
	// Name is required
	if p.Name == "" {
		return fmt.Errorf("plan name is required")
	}
	if len(p.Name) > MaxPlanNameLength {
		return fmt.Errorf("plan name is too long (max %d characters)", MaxPlanNameLength)
	}

	if err := p.validateNotificationAndCap(); err != nil {
		return err
	}

	// Validate ramp schedule
	if err := p.RampSchedule.Validate(); err != nil {
		return fmt.Errorf("invalid ramp schedule: %w", err)
	}

	// Enabled plans must have at least one service; disabled/draft plans may not yet
	if p.Enabled && len(p.Services) == 0 {
		return fmt.Errorf("plan must have at least one service")
	}
	for key := range p.Services {
		svc := p.Services[key]
		if err := svc.Validate(); err != nil {
			return fmt.Errorf("invalid service config '%s': %w", key, err)
		}
	}
	if err := p.checkBoundGroups(boundGroups); err != nil {
		return err
	}

	return p.CheckNoMultiStepSavingsPlans()
}

// ErrMultiStepSavingsPlans marks an armed multi-step plan with an enabled
// Savings Plans service. Step 1 suppresses the whole SP recommendation for 7
// days, so later steps find nothing and the ramp strands (platform#631, Q7).
// Read-only query for already-armed hits: runbooks/armed-multi-step-savings-plans-plans.md.
var ErrMultiStepSavingsPlans = errors.New("multi-step ramps do not support Savings Plans: use a single-step (immediate) ramp or remove the Savings Plans service")

// CheckNoMultiStepSavingsPlans refuses an armed (enabled and auto-purchase)
// plan that has more than one ramp step and an enabled Savings Plans service.
// It is the one definition, shared by plan validation (create, update, arm) and
// the step precheck at attach. Single-step plans are unaffected.
func (p *PurchasePlan) CheckNoMultiStepSavingsPlans() error {
	if !p.Enabled || !p.AutoPurchase || p.RampSchedule.TotalSteps <= 1 {
		return nil
	}
	for key := range p.Services {
		svc := p.Services[key]
		if svc.Enabled && common.IsSavingsPlan(common.ServiceType(svc.Service)) {
			return fmt.Errorf("%w (service %q, total_steps %d)", ErrMultiStepSavingsPlans, key, p.RampSchedule.TotalSteps)
		}
	}
	return nil
}

// Validate validates the RampSchedule.
func (r *RampSchedule) Validate() error {
	if err := r.ValidateAllocationMode(); err != nil {
		return err
	}
	if r.Type != "" && !isValidRampScheduleType(r.Type) {
		return fmt.Errorf("invalid ramp schedule type: %s (valid: %s)", r.Type, strings.Join(ValidRampScheduleTypes, ", "))
	}
	if err := r.validatePercentPerStep(); err != nil {
		return err
	}
	if r.StepIntervalDays < 0 || r.StepIntervalDays > MaxStepIntervalDays {
		return fmt.Errorf("step interval must be between 0 and %d days, got: %d", MaxStepIntervalDays, r.StepIntervalDays)
	}
	if r.CurrentStep < 0 {
		return fmt.Errorf("current step cannot be negative")
	}
	if r.TotalSteps < 0 || r.TotalSteps > MaxTotalSteps {
		return fmt.Errorf("total steps must be between 0 and %d, got: %d", MaxTotalSteps, r.TotalSteps)
	}
	return nil
}

func (r *RampSchedule) ValidateAllocationMode() error {
	switch r.AllocationMode {
	case "", RampAllocationBoundGroups:
		return nil
	case RampAllocationEqualCount:
		if r.TotalSteps < 1 || r.TotalSteps > MaxTotalSteps || (r.Type == "immediate" && r.TotalSteps != 1) {
			return fmt.Errorf("invalid equal-count total steps: %d", r.TotalSteps)
		}
		return nil
	default:
		return fmt.Errorf("unsupported ramp allocation mode: %q", r.AllocationMode)
	}
}

// validatePercentPerStep checks PercentPerStep bounds based on ramp type.
// Step-based types require > 0; "immediate" ignores it; unset type allows 0.
func (r *RampSchedule) validatePercentPerStep() error {
	switch r.Type {
	case "immediate":
		// Immediate mode purchases all at once — PercentPerStep is irrelevant.
		return nil
	case "":
		if r.PercentPerStep < MinCoverage || r.PercentPerStep > MaxCoverage {
			return fmt.Errorf("percent per step must be between %d and %d, got: %.2f", MinCoverage, MaxCoverage, r.PercentPerStep)
		}
	default:
		if r.PercentPerStep <= MinCoverage || r.PercentPerStep > MaxCoverage {
			return fmt.Errorf("percent per step must be between %d and %d for ramp schedule, got: %.2f", MinCoverage+1, MaxCoverage, r.PercentPerStep)
		}
	}
	return nil
}
