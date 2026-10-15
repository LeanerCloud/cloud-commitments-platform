package config

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlanEditorLedger_ObservedAuthority(t *testing.T) {
	t.Parallel()
	fresh := func() *PlanEditorSnapshot {
		s := editorRevisionFixture()
		s.Plan.RampSchedule.PercentPerStep, s.Plan.RampSchedule.StepIntervalDays = 25, 7
		s.LedgerState = PlanEditorLedgerTrusted
		return s
	}
	require.True(t, fresh().HasTrustedRampLedger())
	require.False(t, (*PlanEditorSnapshot)(nil).HasTrustedRampLedger())
	require.False(t, (&PlanEditorSnapshot{}).HasTrustedRampLedger())
	for _, tc := range []struct {
		name   string
		change func(*PlanEditorSnapshot)
	}{
		{"legacy", func(s *PlanEditorSnapshot) { s.LedgerState = PlanEditorLedgerLegacy }},
		{"new", func(s *PlanEditorSnapshot) { s.LedgerState = PlanEditorLedgerNew }},
		{"unknown", func(s *PlanEditorSnapshot) { s.LedgerState = "future" }},
		{"missing", func(s *PlanEditorSnapshot) { s.LedgerState = "" }},
		{"nil baseline", func(s *PlanEditorSnapshot) { s.Baseline = nil }},
		{"empty baseline", func(s *PlanEditorSnapshot) { s.Baseline = PlanRampBaseline{} }},
		{"immediate multistep", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.Type = "immediate" }},
		{"unknown type", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.Type = "future" }},
		{"one step", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.TotalSteps = 1 }},
		{"too many steps", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.TotalSteps = 101 }},
		{"negative current", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.CurrentStep = -1 }},
		{"completed", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.CurrentStep = 4 }},
		{"over total", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.CurrentStep = 5 }},
		{"negative interval", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.StepIntervalDays = -1 }},
		{"large interval", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.StepIntervalDays = MaxStepIntervalDays + 1 }},
		{"negative percent", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.PercentPerStep = -1 }},
		{"zero percent", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.PercentPerStep = 0 }},
		{"large percent", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.PercentPerStep = 101 }},
		{"nan percent", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.PercentPerStep = math.NaN() }},
		{"infinite percent", func(s *PlanEditorSnapshot) { s.Plan.RampSchedule.PercentPerStep = math.Inf(1) }},
		{"negative count", func(s *PlanEditorSnapshot) { s.Baseline["identity"] = PlanRampQuantity{Count: -1} }},
		{"negative hourly", func(s *PlanEditorSnapshot) { s.Baseline["identity"] = PlanRampQuantity{Hourly: -1} }},
		{"nan hourly", func(s *PlanEditorSnapshot) { s.Baseline["identity"] = PlanRampQuantity{Hourly: math.NaN()} }},
		{"infinite hourly", func(s *PlanEditorSnapshot) { s.Baseline["identity"] = PlanRampQuantity{Hourly: math.Inf(1)} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fresh()
			tc.change(s)
			require.False(t, s.HasTrustedRampLedger())
		})
	}
	s := fresh()
	s.Plan.RampSchedule.Type = ""
	s.Baseline["identity"] = PlanRampQuantity{}
	require.True(t, s.HasTrustedRampLedger(), "blank type follows existing validation; genuine zero is valid")
	s.LedgerState = PlanEditorLedgerLegacy
	s.Committed = map[string]PlanRampQuantity{"identity": {Count: 20}}
	require.False(t, s.HasTrustedRampLedger(), "retained quantities cannot promote lineage")
}
