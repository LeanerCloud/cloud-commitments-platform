package purchase

import (
	"fmt"
	"slices"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// LadderBuildInput is everything a builder may read. Evidence fields for
// later instruments are added here, so builder signatures never change.
type LadderBuildInput struct {
	Tranche        config.LadderTrancheDB // read-only; Build never mutates it.
	CloudAccountID string                 // cloud_accounts.id, not the 12-digit number.
	Region         string                 // deployment region.
}

// LadderInstrumentBuilder turns one claimed tranche into the records to buy.
// The sum of the returned hourly commitments never exceeds the tranche amount.
type LadderInstrumentBuilder func(in LadderBuildInput) ([]config.RecommendationRecord, error)

// LadderInstrumentRegistry maps a ladder layer to its builder. It is built by
// its constructor only, so a layer is buildable exactly when it is listed.
type LadderInstrumentRegistry struct {
	builders map[ladder.LayerType]LadderInstrumentBuilder
}

// NewLadderInstrumentRegistry returns the registry of buildable layers.
func NewLadderInstrumentRegistry() *LadderInstrumentRegistry {
	return &LadderInstrumentRegistry{builders: map[ladder.LayerType]LadderInstrumentBuilder{
		ladder.LayerComputeSP: buildComputeSPRecs,
	}}
}

// Layers lists the buildable layers in a stable order, for the claim allowlist.
func (r *LadderInstrumentRegistry) Layers() []ladder.LayerType {
	layers := make([]ladder.LayerType, 0, len(r.builders))
	for l := range r.builders {
		layers = append(layers, l)
	}
	slices.Sort(layers)
	return layers
}

// Build maps the tranche to purchase records, failing closed: any error means
// nothing is bought and the tranche stays scheduled.
func (r *LadderInstrumentRegistry) Build(in LadderBuildInput) ([]config.RecommendationRecord, error) {
	build, ok := r.builders[in.Tranche.LayerType]
	if !ok {
		return nil, fmt.Errorf("no instrument builder for layer %q", in.Tranche.LayerType)
	}
	if err := checkBuildInput(in); err != nil {
		return nil, err
	}
	return build(in)
}

// checkBuildInput holds the checks every instrument shares. An empty account
// would make the purchase run on the host's ambient credentials.
func checkBuildInput(in LadderBuildInput) error {
	if strings.TrimSpace(in.Region) == "" {
		return fmt.Errorf("ladder tranche %s: region is required", in.Tranche.ID)
	}
	if strings.TrimSpace(in.CloudAccountID) == "" {
		return fmt.Errorf("ladder tranche %s: cloud account is required", in.Tranche.ID)
	}
	if in.Tranche.Status != ladder.TrancheStatusScheduled || in.Tranche.ExecutionID != nil {
		return fmt.Errorf("ladder tranche %s: not a scheduled tranche without an execution", in.Tranche.ID)
	}
	return nil
}

// computeSPInputs validates the tranche fields the Compute SP record is
// derived from.
func computeSPInputs(in LadderBuildInput) (years int, hourly float64, err error) {
	tr := in.Tranche
	if basisErr := config.RequireLadderCommitmentBasis(&tr); basisErr != nil {
		return 0, 0, basisErr
	}
	years, err = ladderTermYears(tr.Term)
	if err != nil {
		return 0, 0, err
	}
	if payErr := requireNoUpfront(tr.PaymentOption); payErr != nil {
		return 0, 0, payErr
	}
	hourly, err = floorSPHourly(tr.AmountUSDHr)
	if err != nil {
		return 0, 0, err
	}
	return years, hourly, nil
}

// buildComputeSPRecs builds the one account-wide Compute Savings Plan record.
// Empty OfferingID makes the client look the offering up instead of trusting
// a pre-resolved id, and Compute plans are global so no region goes in Details.
func buildComputeSPRecs(in LadderBuildInput) ([]config.RecommendationRecord, error) {
	years, hourly, err := computeSPInputs(in)
	if err != nil {
		return nil, fmt.Errorf("ladder tranche %s: %w", in.Tranche.ID, err)
	}
	planType, err := spPlanTypeForLayer(in.Tranche.LayerType)
	if err != nil {
		return nil, err
	}
	details, err := common.MarshalServiceDetails(&common.SavingsPlanDetails{
		PlanType:         string(planType),
		HourlyCommitment: hourly,
	})
	if err != nil {
		return nil, err
	}
	monthly := spMonthlyCost(hourly, years)
	return []config.RecommendationRecord{{
		Provider:           string(common.ProviderAWS),
		Service:            string(common.ServiceSavingsPlansCompute),
		Region:             in.Region,
		ResourceType:       "compute-sp",
		Details:            details,
		Count:              1,
		Term:               years,
		Payment:            string(in.Tranche.PaymentOption),
		MonthlyCost:        &monthly,
		CommitmentCurrency: "USD",
		Selected:           true,
		CloudAccountID:     &in.CloudAccountID,
	}}, nil
}
