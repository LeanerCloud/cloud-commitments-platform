package config

import (
	"math"
	"time"
)

// CumulativeStepCount derives a cumulative ramp's step count, tolerant of float noise (100/31 is 31).
// It returns 0 outside (0, 100]; callers bound the result by MaxTotalSteps.
func CumulativeStepCount(percent float64) int {
	if !(percent > 0 && percent <= 100) {
		return 0
	}
	return int(min(math.Ceil(100/percent-1e-9), math.MaxInt32))
}

type PlanScheduleServicePatch struct {
	Provider       *string   `json:"provider,omitempty"`
	Service        *string   `json:"service,omitempty"`
	Enabled        *bool     `json:"enabled,omitempty"`
	Term           *int      `json:"term,omitempty"`
	Payment        *string   `json:"payment,omitempty"`
	Coverage       *float64  `json:"coverage,omitempty"`
	RampSchedule   *string   `json:"ramp_schedule,omitempty"`
	IncludeEngines *[]string `json:"include_engines,omitempty"`
	ExcludeEngines *[]string `json:"exclude_engines,omitempty"`
	IncludeRegions *[]string `json:"include_regions,omitempty"`
	ExcludeRegions *[]string `json:"exclude_regions,omitempty"`
	IncludeTypes   *[]string `json:"include_types,omitempty"`
	ExcludeTypes   *[]string `json:"exclude_types,omitempty"`
	MinCount       *int      `json:"min_count,omitempty"`
}

type PlanScheduleRampPatch struct {
	Type             *string    `json:"type,omitempty"`
	PercentPerStep   *float64   `json:"percent_per_step,omitempty"`
	StepIntervalDays *int       `json:"step_interval_days,omitempty"`
	StartDate        *time.Time `json:"start_date,omitempty"`
}

type PlanSchedulePlanPatch struct {
	Name                   *string  `json:"name,omitempty"`
	Enabled                *bool    `json:"enabled,omitempty"`
	AutoPurchase           *bool    `json:"auto_purchase,omitempty"`
	NotificationDaysBefore *int     `json:"notification_days_before,omitempty"`
	CapSupplied            bool     `json:"cap_supplied"`
	MaxStepCommitment      *float64 `json:"max_step_commitment"`
}

type PlanScheduleStep struct {
	StepNumber    int        `json:"step_number"`
	ScheduledDate *time.Time `json:"scheduled_date"`
}

type PlanScheduleIntent struct {
	Plan               PlanSchedulePlanPatch               `json:"plan"`
	Services           map[string]PlanScheduleServicePatch `json:"services,omitempty"`
	Ramp               PlanScheduleRampPatch               `json:"ramp_schedule"`
	AccountIDs         *[]string                           `json:"account_ids,omitempty"`
	StepCount          *int                                `json:"step_count,omitempty"`
	Steps              []PlanScheduleStep                  `json:"steps"`
	AllowApprovedEdits bool                                `json:"allow_approved_edits"`
}
