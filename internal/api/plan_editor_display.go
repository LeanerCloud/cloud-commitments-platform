package api

import (
	"math"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type PlanEditorValueSource string
type PlanEditorQuantityUnit string

const (
	PlanEditorValueStoredSnapshot      PlanEditorValueSource  = "stored_snapshot"
	PlanEditorValueUnavailable         PlanEditorValueSource  = "unavailable"
	PlanEditorQuantityInstances        PlanEditorQuantityUnit = "instances"
	PlanEditorQuantityReservationUnits PlanEditorQuantityUnit = "reservation_units"
	PlanEditorQuantityVCPU             PlanEditorQuantityUnit = "vcpu"
)

type PlanEditorDisplay struct {
	ResourceType       string                  `json:"resource_type"`
	Region             string                  `json:"region"`
	Quantity           *int                    `json:"quantity"`
	QuantityUnit       *PlanEditorQuantityUnit `json:"quantity_unit"`
	FullTermCommitment *float64                `json:"full_term_commitment"`
	CommitmentCurrency *string                 `json:"commitment_currency"`
	QuantitySource     PlanEditorValueSource   `json:"quantity_source"`
	CommitmentSource   PlanEditorValueSource   `json:"commitment_source"`
}

func buildPlanEditorDisplay(plan *config.PurchasePlan, execution *config.PurchaseExecution) PlanEditorDisplay {
	filtered := *execution
	var valid bool
	filtered.Recommendations, valid = planEditorDisplayRecords(execution)
	row := buildPlannedPurchase(plan, &filtered)
	display := PlanEditorDisplay{ResourceType: row.ResourceType, Region: row.Region,
		QuantitySource: PlanEditorValueUnavailable, CommitmentSource: PlanEditorValueUnavailable}
	if valid && len(filtered.Recommendations) > 0 {
		unit := planEditorQuantityUnit(filtered.Recommendations)
		if unit != "" {
			display.Quantity, display.QuantityUnit = &row.Count, &unit
			display.QuantitySource = PlanEditorValueStoredSnapshot
		}
	}
	return display
}

func planEditorDisplayRecords(execution *config.PurchaseExecution) ([]config.RecommendationRecord, bool) {
	records := make([]config.RecommendationRecord, 0, len(execution.Recommendations))
	sum, valid := 0, true
	for i := range execution.Recommendations {
		rec := &execution.Recommendations[i]
		if !rec.Selected && !rec.Purchased {
			continue
		}
		records = append(records, *rec)
		if rec.Count < 0 || rec.Count > math.MaxInt-sum {
			valid = false
		} else {
			sum += rec.Count
		}
	}
	if !valid {
		for i := range records {
			records[i].Count = 0
		}
	}
	return records, valid
}

func planEditorQuantityUnit(records []config.RecommendationRecord) PlanEditorQuantityUnit {
	first := &records[0]
	unit := map[string]PlanEditorQuantityUnit{
		"aws/ec2":       PlanEditorQuantityInstances,
		"azure/compute": PlanEditorQuantityReservationUnits,
		"gcp/compute":   PlanEditorQuantityVCPU,
	}[first.Provider+"/"+first.Service]
	for i := range records {
		if records[i].Provider != first.Provider || records[i].Service != first.Service {
			return ""
		}
	}
	return unit
}
