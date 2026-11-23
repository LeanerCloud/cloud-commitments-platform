package server

import (
	"encoding/json"
	"fmt"
	"time"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

// ladderPlanJSONAction is a JSON-serializable form of pkgladder.PlannedAction.
// PlannedAction.AmountUSDPerHour is *big.Rat (not JSON-friendly); this DTO
// converts it to *float64 at the boundary.
type ladderPlanJSONAction struct {
	Action        string   `json:"action"`
	Layer         string   `json:"layer"`
	Family        string   `json:"family,omitempty"`
	AmountUSDHr   *float64 `json:"amount_usd_hr,omitempty"` // nil for Hold/Reshape
	Term          string   `json:"term,omitempty"`
	PaymentOption string   `json:"payment_option,omitempty"`
	Rationale     string   `json:"rationale"`
	DataSources   []string `json:"data_sources,omitempty"`
}

// ladderPlanJSONDTO is a JSON-serializable form of pkgladder.LadderPlan.
// The *big.Rat monetary fields are converted to *float64 so they round-trip
// cleanly through JSONB without binary encoding.
type ladderPlanJSONDTO struct {
	// ReasonCode is set only when the run could not size its gap (see
	// ladderRunPlanner.reasonCode); ordinary runs omit it.
	ReasonCode    string                  `json:"reason_code,omitempty"`
	Scope         pkgladder.Scope         `json:"scope"`
	GeneratedAt   time.Time               `json:"generated_at"`
	TargetUSDHr   *float64                `json:"target_usd_hr,omitempty"`
	ExistingUSDHr *float64                `json:"existing_usd_hr,omitempty"`
	GapUSDHr      *float64                `json:"gap_usd_hr,omitempty"`
	Actions       []ladderPlanJSONAction  `json:"actions"`
	Baseline      pkgladder.UsageBaseline `json:"baseline"`
}

// marshalLadderPlan serializes a LadderPlan to JSON for storage in the
// plan JSONB column. *big.Rat fields are converted to *float64 at this
// boundary; nil stays nil. reasonCode is stored as reason_code when non-empty.
func marshalLadderPlan(plan *pkgladder.LadderPlan, reasonCode string) (json.RawMessage, error) {
	actions := make([]ladderPlanJSONAction, 0, len(plan.Actions))
	for i := range plan.Actions {
		a := &plan.Actions[i]
		action := ladderPlanJSONAction{
			Action:        string(a.Action),
			Layer:         string(a.Layer),
			Family:        a.Family,
			AmountUSDHr:   ratToFloat64Ptr(a.AmountUSDPerHour),
			Term:          string(a.Term),
			PaymentOption: string(a.PaymentOption),
			Rationale:     a.Rationale,
			DataSources:   a.DataSources,
		}
		actions = append(actions, action)
	}
	dto := ladderPlanJSONDTO{
		ReasonCode:    reasonCode,
		Scope:         plan.Scope,
		GeneratedAt:   plan.GeneratedAt,
		TargetUSDHr:   ratToFloat64Ptr(plan.TargetUSDPerHour),
		ExistingUSDHr: ratToFloat64Ptr(plan.ExistingUSDPerHour),
		GapUSDHr:      ratToFloat64Ptr(plan.GapUSDPerHour),
		Actions:       actions,
		Baseline:      plan.Baseline,
	}
	b, err := json.Marshal(dto)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// ladderTermFromYears converts a GlobalConfig.DefaultTerm (integer years) to
// a typed pkgladder.Term. Fails loud on any unrecognized value so money-path
// decisions never proceed with an unknown commitment term.
func ladderTermFromYears(years int) (pkgladder.Term, error) {
	switch years {
	case 1:
		return pkgladder.Term1Year, nil
	case 3:
		return pkgladder.Term3Year, nil
	default:
		return "", fmt.Errorf("unsupported default_term=%d years (allowed: 1, 3)", years)
	}
}
