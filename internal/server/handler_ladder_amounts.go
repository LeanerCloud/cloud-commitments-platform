package server

import (
	"fmt"
	"math/big"

	pkgladder "github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// ratFromFloat converts a float64 to *big.Rat. It returns nil when
// big.Rat.SetFloat64 returns nil (non-finite inputs like NaN or Inf).
// This mirrors the project-wide convention for converting float64 to exact
// rational arithmetic at the boundary.
func ratFromFloat(f float64) *big.Rat {
	r := new(big.Rat)
	if r.SetFloat64(f) == nil {
		return nil
	}
	return r
}

// ratToFloat64Ptr converts a *big.Rat to a *float64 suitable for storage in
// a nullable DB column. nil stays nil (never 0-coerced).
func ratToFloat64Ptr(r *big.Rat) *float64 {
	if r == nil {
		return nil
	}
	f, _ := r.Float64()
	return &f
}

// sumAllocationOnDemandGaps sums the on-demand-equivalent gap across all
// allocations as an exact *big.Rat, the unit of target and existing. It returns a zero (non-nil) rat for an empty allocation set: a
// Hold-only run plans exactly $0 of new commitment, which is a meaningful
// value here (what is being placed), not "not computed".
func sumAllocationOnDemandGaps(allocs []pkgladder.Allocation) *big.Rat {
	sum := new(big.Rat)
	for _, a := range allocs {
		if a.OnDemandEquivalentUSDPerHour != nil {
			sum.Add(sum, a.OnDemandEquivalentUSDPerHour)
		}
	}
	return sum
}

// sumTrancheRowAmounts sums the amounts of the tranche rows a run persists.
// This is the non-nullable total_hourly_commit stored in the run row, in
// commitment dollars; it is 0 for a no-op (Hold-only) plan, which is the
// correct semantic (zero new commitment, not "not computed").
func sumTrancheRowAmounts(rows []config.LadderTrancheDB) float64 {
	var sum float64
	for i := range rows {
		sum += rows[i].AmountUSDHr
	}
	return sum
}

// storedTrancheAmount is the amount a tranche row persists: the planned exact
// amount floored to the column's six decimals (never rounded up; see
// config.LadderAmountFloor). It fails loud on a malformed or non-positive
// planned amount: BuildTranches never emits one, so it means a broken tranche,
// and writing it would be a $0 (or negative) row. stored=false means the
// positive amount is below 0.000001 USD/hr and floors to zero; the caller
// drops that tranche instead of persisting a $0 row. Such an amount is far
// below the provider minimum (0.001 USD/hr), so it could never be purchased.
func storedTrancheAmount(tr *pkgladder.Tranche) (amount *big.Rat, stored bool, err error) {
	r, ok := new(big.Rat).SetString(tr.AmountUSDPerHour)
	if !ok || r == nil {
		return nil, false, fmt.Errorf("tranche %s: malformed amount_usd_per_hour %q (not a parseable rational)", tr.ID, tr.AmountUSDPerHour)
	}
	if r.Sign() <= 0 {
		return nil, false, fmt.Errorf("tranche %s: non-positive amount_usd_per_hour %q", tr.ID, tr.AmountUSDPerHour)
	}
	floored := config.LadderAmountFloor(r)
	return floored, floored.Sign() > 0, nil
}

// buildTrancheDBRows converts pkg/ladder Tranche rows to config.LadderTrancheDB
// rows ready for SaveLadderTranches. It fails loud on a malformed or
// non-positive AmountUSDPerHour (money path): rather than persist a silently
// 0-coerced $0 tranche, it returns an error so the whole run is counted
// Errored and nothing partial is written. A positive amount below 0.000001
// USD/hr, which the six-decimal column would store as 0, is dropped with a log
// line instead (see storedTrancheAmount): it is a planner remainder no provider
// can buy, and refusing it would fail the same run again on every rerun.
func buildTrancheDBRows(tranches []pkgladder.Tranche, runID string, configID *string) ([]config.LadderTrancheDB, error) {
	rows := make([]config.LadderTrancheDB, 0, len(tranches))
	for i := range tranches {
		tr := &tranches[i] // use pointer to avoid copying 152-byte Tranche struct per iteration
		// AmountUSDHr: parse the RatString and floor it to the stored scale
		// (never round a planned commitment up; see config.LadderAmountFloor).
		r, stored, err := storedTrancheAmount(tr)
		if err != nil {
			return nil, err
		}
		if !stored {
			logging.Warnf("ladder_run: run %s: dropping tranche %s (layer %s): amount_usd_per_hour %s floors to 0 at the stored six-decimal scale",
				runID, tr.ID, tr.Layer, tr.AmountUSDPerHour)
			continue
		}
		amountUSDHr, _ := r.Float64()
		runIDCopy := runID
		if tr.CommitmentRate == "" {
			return nil, fmt.Errorf("tranche %s: empty commitment rate", tr.ID)
		}
		basis := config.LadderAmountCommitment
		rate := tr.CommitmentRate
		rows = append(rows, config.LadderTrancheDB{
			ID:             tr.ID,
			ConfigID:       configID,
			RunID:          &runIDCopy,
			LayerType:      tr.Layer,
			Family:         tr.Family,
			Term:           tr.Term,
			PaymentOption:  tr.PaymentOption,
			Status:         pkgladder.TrancheStatusScheduled,
			AmountUSDHr:    amountUSDHr,
			AmountBasis:    &basis,
			CommitmentRate: &rate,
			ScheduledDate:  tr.FireAfter,
		})
	}
	return rows, nil
}
