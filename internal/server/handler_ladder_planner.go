package server

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ladderRunPlanner holds the read-side data of one run and turns the locked
// on-demand-equivalent netting total into the run row and tranche rows to persist. plan is pure
// compute (it runs while the per-config lock is held).
type ladderRunPlanner struct {
	now         time.Time
	layerStates map[pkgladder.LayerType]pkgladder.LayerState
	dbCfg       *config.LadderConfigDB
	rates       map[pkgladder.LayerType]float64
	global      ladderTerms // reference pair for hold rationale only
	term        pkgladder.Term
	paymentOpt  pkgladder.PaymentOption
	layers      []pkgladder.LayerSpec
	// missing lists the layers whose existing coverage has no rate. When set,
	// the run is a Hold-only run (planMissingRateHold) and layerStates is nil.
	missing                      []ladderMissingRate
	baseline                     pkgladder.UsageBaseline
	engineCfg                    pkgladder.LadderConfig
	allocations, tranches, holds int // set by plan, for the log line
}

// build is the LadderPlanBuilder handed to PlanAndSaveLadderRun.
func (p *ladderRunPlanner) build(rows []config.LadderInFlightRow) (*config.LadderRunDB, []config.LadderTrancheDB, error) {
	if len(p.missing) > 0 {
		return p.planMissingRateHold()
	}
	return p.plan(rows)
}

// reasonCode is the plan's machine-readable reason_code: set only for a run
// whose gap could not be sized. An ordinary run, including a target-met Hold,
// has none and explains itself in its actions' rationale.
func (p *ladderRunPlanner) reasonCode() string {
	if len(p.missing) > 0 {
		return ladderHoldMissingDiscountRate
	}
	return ""
}

// logPersisted logs the saved run. A missing-rate Hold is logged at WARN,
// once per run: it is an expected state until the provider returns a rate,
// not a failure, and the run counts as planned.
func (p *ladderRunPlanner) logPersisted(run *config.LadderRunDB) {
	if len(p.missing) > 0 {
		logging.Warnf("ladder_run: config %s: persisted Hold-only run %s (reason_code=%s): %d layer(s) hold existing coverage with no observed or new-purchase rate; nothing bought",
			p.dbCfg.ID, run.ID, ladderHoldMissingDiscountRate, len(p.missing))
		return
	}
	logging.Infof("ladder_run: config %s: persisted run %s (status=%s, allocations=%d, tranches=%d, holds=%d)",
		p.dbCfg.ID, run.ID, run.Status, p.allocations, p.tranches, p.holds)
}

func (p *ladderRunPlanner) plan(rows []config.LadderInFlightRow) (*config.LadderRunDB, []config.LadderTrancheDB, error) {
	// Allocate rejects a row on a layer outside Layers, so it cannot be silently un-netted.
	inFlight := make([]pkgladder.InFlightTranche, len(rows))
	for i, r := range rows {
		inFlight[i] = pkgladder.InFlightTranche{Layer: r.Layer, Family: r.Family, CommitmentUSDPerHour: r.CommitmentUSDPerHour, Rate: r.Rate}
	}
	allocResult, err := pkgladder.Allocate(&pkgladder.AllocationInput{
		Now:             p.now,
		Baseline:        p.baseline,
		LayerStates:     p.layerStates,
		Layers:          p.layers,
		Config:          p.engineCfg,
		DataSources:     []string{dataSourceAWSCostExplorer},
		InFlight:        inFlight,
		CommitmentRates: p.rates,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("allocate: %w", err)
	}

	runID := uuid.New().String()
	trancheResult, err := pkgladder.BuildTranches(&pkgladder.TrancheInput{
		Config:        &p.engineCfg,
		RunID:         runID,
		Term:          p.term,
		PaymentOption: p.paymentOpt,
		NewID:         func() string { return uuid.New().String() },
		Now:           p.now,
		Allocations:   allocResult.Allocations,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("BuildTranches: %w", err)
	}

	// The plan is display only: its purchase amounts are the immediate tranches
	// persisted below, so it never authorizes more than the rows netting sees.
	immediate, err := immediateTrancheAmounts(trancheResult.Tranches, p.now)
	if err != nil {
		return nil, nil, err
	}
	plan := assembleLadderPlan(p.engineCfg.Scope, p.now, allocResult, immediate, p.baseline, p.engineCfg.TargetCoveragePct, p.term, p.paymentOpt)
	p.allocations, p.tranches, p.holds = len(allocResult.Allocations), len(trancheResult.Tranches), len(allocResult.Holds)
	return p.buildRows(runID, plan, trancheResult)
}

// buildRows validates the assembled plan and builds the ladder_runs row and its
// ladder_tranches rows. L5 APPEND-ONLY: the rows are INSERTED and never
// supersede upcoming tranches (the store only cancels already-overdue ones,
// #544). Netting has already removed the config's existing scheduled tranches
// from the gap, so the rows carry exactly the delta needed to reach target-E:
//   - Hold (gap ~0): no tranche rows; only the run row is written and the
//     existing scheduled ramp keeps its original fire dates.
//   - Drift-up (gap re-opens): the rows are the top-up; the prior generation
//     stays intact.
//
// Cancel-and-replace was removed because it fought the netting: on drift-up it
// discarded the amount the netting had subtracted, never converging.
//
// Drift-DOWN: if usage drops after a ramp is planned, the too-large scheduled
// ramp is preserved uncancelled (a Hold). That over-commitment is an accepted,
// separately-tracked follow-up (drift-down shrink), bounded by the low-water
// clamp in Allocate.
func (p *ladderRunPlanner) buildRows(runID string, plan *pkgladder.LadderPlan, trancheResult *pkgladder.TrancheResult) (*config.LadderRunDB, []config.LadderTrancheDB, error) {
	if err := plan.Validate(); err != nil {
		return nil, nil, fmt.Errorf("LadderPlan.Validate: %w", err)
	}
	planJSON, err := marshalLadderPlan(plan, p.reasonCode())
	if err != nil {
		return nil, nil, fmt.Errorf("marshal plan: %w", err)
	}

	// Map *big.Rat monetary snapshot to *float64 (nil stays nil). Stored in the
	// run row for dashboards; the authoritative detail is in plan_json.
	cfgID := p.dbCfg.ID
	modeStr := p.dbCfg.Mode
	cadenceStr := p.dbCfg.Cadence
	runRow := &config.LadderRunDB{
		ID:            runID,
		ConfigID:      &cfgID,
		StartedAt:     p.now,
		Status:        pkgladder.RunStatusPlanned,
		Mode:          &modeStr,
		Cadence:       &cadenceStr,
		Plan:          planJSON,
		BaselineUSDHr: p.baseline.LowWaterUSDPerHour,
		TargetUSDHr:   ratToFloat64Ptr(plan.TargetUSDPerHour),
		ExistingUSDHr: ratToFloat64Ptr(plan.ExistingUSDPerHour),
		GapUSDHr:      ratToFloat64Ptr(plan.GapUSDPerHour),
	}

	// EVERY ramp step is a row (status=scheduled; nothing fires them yet),
	// including the immediate one (scheduled_date = run start), so in-flight
	// netting sees it.
	trancheRows, err := buildTrancheDBRows(trancheResult.Tranches, runRow.ID, &cfgID)
	if err != nil {
		return nil, nil, fmt.Errorf("buildTrancheDBRows: %w", err)
	}
	// total_hourly_commit is what this run persists, not the allocation gap, so
	// it always equals the sum of the rows netting will read back.
	runRow.TotalHourlyCommit = sumTrancheRowAmounts(trancheRows)
	return runRow, trancheRows, nil
}
