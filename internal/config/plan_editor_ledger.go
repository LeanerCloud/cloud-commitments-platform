package config

import "math"

type PlanEditorLedgerState = rampLedgerState

const (
	PlanEditorLedgerLegacy  PlanEditorLedgerState = "legacy"
	PlanEditorLedgerNew                           = rampLedgerNew
	PlanEditorLedgerTrusted                       = rampLedgerTrusted
)

// HasTrustedRampLedger describes captured lineage, not authorization or current sizing.
func (s *PlanEditorSnapshot) HasTrustedRampLedger() bool {
	if s == nil || s.Plan == nil || s.LedgerState != PlanEditorLedgerTrusted || len(s.Baseline) == 0 {
		return false
	}
	if !planEditorUnfinishedRamp(s.Plan.RampSchedule) {
		return false
	}
	for _, q := range s.Baseline {
		if !planEditorQuantityValid(q) {
			return false
		}
	}
	return true
}

func planEditorUnfinishedRamp(r RampSchedule) bool {
	return r.Validate() == nil && r.Type != "immediate" && r.TotalSteps > 1 &&
		r.CurrentStep < r.TotalSteps && !math.IsNaN(r.PercentPerStep) && !math.IsInf(r.PercentPerStep, 0)
}

func planEditorQuantityValid(q PlanRampQuantity) bool {
	return q.Count >= 0 && q.Hourly >= 0 && !math.IsNaN(q.Hourly) && !math.IsInf(q.Hourly, 0)
}
