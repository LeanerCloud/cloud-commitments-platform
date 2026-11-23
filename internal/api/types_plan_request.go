package api

import (
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// PlanRequest represents the API request format for creating/updating plans
// The frontend sends ramp_schedule as a string, which we convert to the proper struct.
type PlanRequest struct {
	TargetCoverage *int `json:"target_coverage,omitempty"`
	// MaxStepCommitment caps the total commitment one ramp step may
	// auto-purchase. Omitted on create = no cap (auto-purchase of steps with
	// recommendations stays refused); omitted on update = keep the current cap.
	MaxStepCommitment *float64 `json:"max_step_commitment,omitempty"`
	Term              *int     `json:"term,omitempty"`
	Payment           *string  `json:"payment,omitempty"`
	Provider          string   `json:"provider,omitempty"`
	Service           string   `json:"service,omitempty"`
	Name              string   `json:"name"`
	RampSchedule      string   `json:"ramp_schedule,omitempty"`
	// TargetAccounts is read by createPlan only. updatePlan ignores it; account
	// assignment on an existing plan goes through setPlanAccounts.
	TargetAccounts []string `json:"target_accounts,omitempty"`
	// Groups builds the plan from selected recommendations, one entry per
	// service. It excludes the flat provider/service/term/payment/target_coverage
	// fields. createPlan does not read it yet.
	Groups                 []PlanGroupRequest `json:"groups,omitempty"`
	NotificationDaysBefore int                `json:"notification_days_before"`
	CustomStepPercent      int                `json:"custom_step_percent,omitempty"`
	CustomIntervalDays     int                `json:"custom_interval_days,omitempty"`
	AutoPurchase           bool               `json:"auto_purchase"`
	Enabled                bool               `json:"enabled"`
}

// validateTargetCoverage refuses an explicit target outside 0..100. An explicit
// 0 is stored as 0 (a plan that buys nothing); only an omitted field defaults.
func (r *PlanRequest) validateTargetCoverage() error {
	if r.TargetCoverage != nil && (*r.TargetCoverage < 0 || *r.TargetCoverage > 100) {
		return NewClientError(400, "target_coverage must be between 0 and 100")
	}
	return nil
}

// validateRampSchedule refuses a ramp_schedule that is neither omitted, a preset
// nor "custom": it used to fall back to the immediate preset, buying the whole
// base in one step.
func (r *PlanRequest) validateRampSchedule() error {
	if r.RampSchedule != "custom" && (r.CustomStepPercent != 0 || r.CustomIntervalDays != 0) {
		// Without "custom" these fields are ignored and an omitted ramp_schedule
		// becomes the immediate preset, so a cadence the caller named would turn
		// into buying the whole base in one step (#1935).
		return NewClientError(400, "custom_step_percent and custom_interval_days apply only with ramp_schedule \"custom\"")
	}
	if r.RampSchedule == "" || r.RampSchedule == "custom" {
		return nil
	}
	if _, ok := config.PresetRampSchedules[r.RampSchedule]; !ok {
		return NewClientError(400, "ramp_schedule must be one of immediate, weekly-25pct, monthly-10pct or custom")
	}
	return nil
}

// omittedFields records which service fields an update request left out, so
// the overlay keeps the stored values instead of the create defaults.
type omittedFields struct{ Coverage, Term, Payment bool }

func (r *PlanRequest) omittedFields() omittedFields {
	return omittedFields{
		Coverage: r.TargetCoverage == nil,
		Term:     r.Term == nil || *r.Term == 0,
		Payment:  r.Payment == nil || *r.Payment == "",
	}
}

// toPurchasePlan converts a PlanRequest to a config.PurchasePlan.
func (r *PlanRequest) toPurchasePlan() *config.PurchasePlan {
	now := time.Now()
	plan := &config.PurchasePlan{
		Name:                   r.Name,
		Enabled:                r.Enabled,
		AutoPurchase:           r.AutoPurchase,
		NotificationDaysBefore: r.NotificationDaysBefore,
		MaxStepCommitment:      r.MaxStepCommitment,
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	plan.RampSchedule = r.buildRampSchedule(now)
	plan.Services = r.buildServiceConfig()
	plan.NextExecutionDate = r.calculateNextExecutionDate(now, plan.RampSchedule)

	return plan
}

// buildRampSchedule builds the ramp schedule from request parameters.
func (r *PlanRequest) buildRampSchedule(now time.Time) config.RampSchedule {
	if preset, ok := config.PresetRampSchedules[r.RampSchedule]; ok {
		preset.StartDate = now
		return preset
	}

	if r.RampSchedule == "custom" {
		return r.buildCustomRampSchedule(now)
	}

	// Default to immediate
	schedule := config.PresetRampSchedules["immediate"]
	schedule.StartDate = now
	return schedule
}

// buildCustomRampSchedule builds a custom ramp schedule with validated parameters.
func (r *PlanRequest) buildCustomRampSchedule(now time.Time) config.RampSchedule {
	stepPercent := float64(r.CustomStepPercent)
	if stepPercent <= 0 {
		stepPercent = 20
	}

	intervalDays := r.CustomIntervalDays
	if intervalDays <= 0 {
		intervalDays = 7
	}

	totalSteps := int(100 / stepPercent)
	if totalSteps < 1 {
		totalSteps = 1
	}

	return config.RampSchedule{
		Type:             "custom",
		PercentPerStep:   stepPercent,
		StepIntervalDays: intervalDays,
		TotalSteps:       totalSteps,
		StartDate:        now,
	}
}

// buildServiceConfig creates service configuration from request fields.
func (r *PlanRequest) buildServiceConfig() map[string]config.ServiceConfig {
	if r.Provider == "" || r.Service == "" {
		return nil
	}

	// nil or zero is an omitted field; create defaults it, update keeps the stored value.
	term := 3
	if r.Term != nil && *r.Term != 0 {
		term = *r.Term
	}

	payment := "no-upfront"
	if r.Payment != nil && *r.Payment != "" {
		payment = *r.Payment
	}

	// nil is an omitted field (create defaults it to 80); an explicit value is kept as sent.
	coverage := float64(config.DefaultCoveragePercent)
	if r.TargetCoverage != nil {
		coverage = float64(*r.TargetCoverage)
	}

	return map[string]config.ServiceConfig{
		r.Provider + "/" + r.Service: {
			Provider: r.Provider,
			Service:  r.Service,
			Enabled:  true,
			Term:     term,
			Payment:  payment,
			Coverage: coverage,
		},
	}
}

// calculateNextExecutionDate determines the next execution date based on ramp schedule.
func (r *PlanRequest) calculateNextExecutionDate(now time.Time, schedule config.RampSchedule) *time.Time {
	var nextDate time.Time

	if schedule.Type == "immediate" {
		nextDate = now.AddDate(0, 0, 1) // Schedule for tomorrow
	} else if schedule.StepIntervalDays > 0 {
		nextDate = now.AddDate(0, 0, schedule.StepIntervalDays)
	} else {
		return nil
	}

	return &nextDate
}
